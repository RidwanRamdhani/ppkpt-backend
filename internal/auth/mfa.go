package auth

import (
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"math/big"
	"net/http"
	"strings"
	"time"

	"github.com/RidwanRamdhani/ppkpt-backend/internal/audit"
	"github.com/RidwanRamdhani/ppkpt-backend/internal/platform/db"
	"github.com/RidwanRamdhani/ppkpt-backend/internal/platform/httpx"
	"github.com/jackc/pgx/v5"
	"github.com/pquerna/otp"
	"github.com/pquerna/otp/hotp"
	"github.com/pquerna/otp/totp"
)

// Implementasi 2FA TOTP sesuai FR-17 revisi dan DECISIONS T15/D25:
//   - login tahap 1 hanya memvalidasi password dan menerbitkan challenge;
//   - sesi baru dibuat setelah kode TOTP/recovery code terverifikasi;
//   - secret TOTP disimpan terenkripsi AES-GCM;
//   - kode TOTP tidak dapat dipakai ulang (anti-replay);
//   - recovery code disimpan sebagai hash SHA-256 dan sekali pakai.

const (
	mfaPurposeLogin  = "LOGIN"
	mfaPurposeEnroll = "ENROLL"

	totpPeriodSeconds = 30
	totpSkew          = 1
	totpCodeDigits    = 6

	// recoveryAlphabet 32 karakter = 5 bit per karakter; 16 karakter = 80 bit.
	recoveryAlphabet      = "ABCDEFGHJKLMNPQRSTUVWXYZ23456789"
	recoveryCodeLength    = 16
	recoveryCodeGroupSize = 4
)

// MFASetupResult adalah respons pendaftaran perangkat 2FA. Secret hanya
// ditampilkan sekali saat setup.
type MFASetupResult struct {
	OTPAuthURL string
	Secret     string
}

// MFAVerifyResult adalah hasil verifikasi 2FA yang sudah membuat sesi penuh.
type MFAVerifyResult struct {
	User          User
	Token         string
	ExpiresAt     time.Time
	RecoveryCodes []string // hanya berisi nilai saat enrollment
}

// MFAEnableInput adalah masukan untuk menyelesaikan enrollment.
type MFAEnableInput struct {
	ChallengeToken string
	Code           string
	IP             string
	UserAgent      string
}

// MFAVerifyInput adalah masukan verifikasi login tahap dua. Isi salah satu:
// Code (TOTP) atau RecoveryCode.
type MFAVerifyInput struct {
	ChallengeToken string
	Code           string
	RecoveryCode   string
	IP             string
	UserAgent      string
}

// MFACredentialInput adalah masukan endpoint pengelolaan 2FA yang memerlukan
// password dan kode TOTP dari sesi aktif.
type MFACredentialInput struct {
	Password string
	Code     string
	IP       string
}

type mfaChallenge struct {
	ID         string
	UserID     int64
	Purpose    string
	Attempts   int16
	ExpiresAt  time.Time
	ConsumedAt *time.Time
	User       userRecord
	secretEnc  *string
}

func newMFACipher(key []byte) (cipher.AEAD, error) {
	if len(key) != 32 {
		return nil, fmt.Errorf("auth: MFA_ENCRYPTION_KEY harus 32 byte")
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, fmt.Errorf("auth: siapkan cipher 2FA: %w", err)
	}
	return cipher.NewGCM(block)
}

func (s *Service) encryptSecret(secret string) (string, error) {
	if s.mfa == nil {
		return "", errors.New("auth: 2FA tidak aktif")
	}
	nonce := make([]byte, s.mfa.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return "", err
	}
	out := s.mfa.Seal(nonce, nonce, []byte(secret), nil)
	return base64.StdEncoding.EncodeToString(out), nil
}

func (s *Service) decryptSecret(enc string) (string, error) {
	if s.mfa == nil {
		return "", errors.New("auth: 2FA tidak aktif")
	}
	raw, err := base64.StdEncoding.DecodeString(enc)
	if err != nil {
		return "", fmt.Errorf("auth: secret TOTP rusak: %w", err)
	}
	if len(raw) < s.mfa.NonceSize() {
		return "", errors.New("auth: secret TOTP rusak")
	}
	nonce, ciphertext := raw[:s.mfa.NonceSize()], raw[s.mfa.NonceSize():]
	plain, err := s.mfa.Open(nil, nonce, ciphertext, nil)
	if err != nil {
		return "", fmt.Errorf("auth: gagal membuka secret TOTP: %w", err)
	}
	return string(plain), nil
}

// --------------------------------------------------------------- cookie 2FA

func (s *Service) mfaCookie(token string, ttl time.Duration) *http.Cookie {
	return &http.Cookie{ //nolint:gosec // Secure mengikuti konfigurasi; HttpOnly dan SameSite selalu aktif
		Name:     MFACookieName,
		Value:    token,
		Path:     "/api/auth",
		HttpOnly: true,
		Secure:   s.opts.CookieSecure,
		SameSite: http.SameSiteLaxMode,
		MaxAge:   int(ttl / time.Second),
	}
}

func (s *Service) clearMFACookie() *http.Cookie {
	return &http.Cookie{ //nolint:gosec // Secure mengikuti konfigurasi; HttpOnly dan SameSite selalu aktif
		Name:     MFACookieName,
		Value:    "",
		Path:     "/api/auth",
		HttpOnly: true,
		Secure:   s.opts.CookieSecure,
		SameSite: http.SameSiteLaxMode,
		MaxAge:   -1,
		Expires:  time.Unix(0, 0),
	}
}

// DiscardMFAChallenge menghapus challenge yang cocok dengan token cookie.
// Token cacat atau tidak dikenal diabaikan agar logout tetap idempoten.
func (s *Service) DiscardMFAChallenge(ctx context.Context, token string) error {
	if !validTokenFormat(token) {
		return nil
	}
	_, err := s.pool.Exec(ctx, `DELETE FROM mfa_challenges WHERE id = $1`, hashToken(token))
	return err
}

// ----------------------------------------------------------- login tahap 1

func (s *Service) startMFAChallenge(ctx context.Context, u *userRecord, in LoginInput, now time.Time) (*LoginResult, error) {
	enroll := u.totpEnabledAt == nil
	purpose := mfaPurposeLogin
	if enroll {
		purpose = mfaPurposeEnroll
	}

	token, id, err := newToken()
	if err != nil {
		return nil, fmt.Errorf("auth: buat token 2FA: %w", err)
	}
	expires := now.Add(s.opts.MFAChallengeTTL)

	err = db.WithTx(ctx, s.pool, func(tx pgx.Tx) error {
		// Urutan lock global: baris user dulu, baru baris challenge.
		// Transaksi lain (setup/verify/disable/reset) memakai urutan yang sama
		// sehingga login bersamaan dengan verifikasi tidak saling menunggu.
		if err := s.prepareLogin(ctx, tx, u, in, now); err != nil {
			return err
		}
		// Satu pengguna hanya boleh punya satu challenge aktif.
		if _, err := tx.Exec(ctx, `DELETE FROM mfa_challenges WHERE user_id = $1`, u.ID); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `
			INSERT INTO mfa_challenges (id, user_id, purpose, expires_at, ip_address)
			VALUES ($1, $2, $3, $4, NULLIF($5, ''))`,
			id, u.ID, purpose, expires, in.IP); err != nil {
			return err
		}
		return s.audit.Log(ctx, tx, audit.Event{
			ActorType: audit.ActorUser, UserID: userIDPtr(u.ID), Action: audit.ActionLoginSuccess,
			Entity: "user", EntityID: userIDPtr(u.ID), IP: in.IP,
			Detail: map[string]any{"role": u.Role, "mfa_required": true, "enrollment_required": enroll},
		})
	})
	if err != nil {
		return nil, fmt.Errorf("auth: simpan challenge 2FA: %w", err)
	}

	return &LoginResult{
		MFARequired:        true,
		EnrollmentRequired: enroll,
		MFAToken:           token,
		MFAExpiresAt:       expires,
	}, nil
}

// --------------------------------------------------------- challenge helper

func errMFAChallenge() *httpx.AppError {
	return httpx.NewAppError(http.StatusUnauthorized, httpx.CodeUnauthorized,
		"Verifikasi 2FA tidak valid atau sudah kedaluwarsa. Silakan login ulang.")
}

func errMFADisabled() *httpx.AppError {
	return httpx.NewAppError(http.StatusConflict, httpx.CodeConflict, "2FA tidak diaktifkan pada server ini.")
}

func errMFAAlreadyEnabled() *httpx.AppError {
	return httpx.NewAppError(http.StatusConflict, httpx.CodeConflict, "2FA sudah aktif untuk akun ini.")
}

func (s *Service) loadChallenge(ctx context.Context, tx pgx.Tx, token string, forUpdate bool) (*mfaChallenge, error) {
	if !validTokenFormat(token) {
		return nil, errMFAChallenge()
	}
	tokenHash := hashToken(token)

	// Urutan lock global: baris user dulu, baru baris challenge. Query pertama
	// hanya mengunci user (FOR UPDATE OF u) sehingga baris challenge belum
	// dipegang; baris challenge dikunci pada query kedua. Login, disable, dan
	// reset memakai urutan yang sama, sehingga transaksi bersamaan tidak ada
	// yang saling menunggu (deadlock).
	q := `SELECT u.id, u.name, u.username, u.email, u.role, u.is_active, u.must_change_password,
		u.totp_enabled_at, u.totp_last_step, u.totp_secret_enc, c.user_id
		FROM mfa_challenges c
		JOIN users u ON u.id = c.user_id
		WHERE c.id = $1`
	if forUpdate {
		q += ` FOR UPDATE OF u`
	}

	var ch mfaChallenge
	err := tx.QueryRow(ctx, q, tokenHash).Scan(
		&ch.User.ID, &ch.User.Name, &ch.User.Username, &ch.User.Email, &ch.User.Role,
		&ch.User.IsActive, &ch.User.MustChangePassword, &ch.User.totpEnabledAt,
		&ch.User.totpLastStep, &ch.secretEnc, &ch.UserID)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, errMFAChallenge()
	}
	if err != nil {
		return nil, err
	}
	ch.User.TotpEnabled = ch.User.totpEnabledAt != nil

	q = `SELECT id, user_id, purpose, attempts, expires_at, consumed_at
		FROM mfa_challenges WHERE id = $1 AND user_id = $2`
	if forUpdate {
		q += ` FOR UPDATE`
	}
	err = tx.QueryRow(ctx, q, tokenHash, ch.UserID).Scan(
		&ch.ID, &ch.UserID, &ch.Purpose, &ch.Attempts, &ch.ExpiresAt, &ch.ConsumedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, errMFAChallenge()
	}
	if err != nil {
		return nil, err
	}

	if ch.ConsumedAt != nil || !ch.ExpiresAt.After(s.now()) || !ch.User.IsActive {
		return nil, errMFAChallenge()
	}
	return &ch, nil
}

func (s *Service) recordMFAFailure(ctx context.Context, tx pgx.Tx, ch *mfaChallenge, ip, reason string) error {
	var (
		attempts   int16
		consumedAt *time.Time
	)
	now := s.now()
	if err := tx.QueryRow(ctx, `
		UPDATE mfa_challenges
		SET attempts = attempts + 1,
			consumed_at = CASE WHEN attempts + 1 >= $2 THEN $3::timestamptz ELSE consumed_at END
		WHERE id = $1
		RETURNING attempts, consumed_at`,
		ch.ID, s.opts.MFAMaxAttempts, now).Scan(&attempts, &consumedAt); err != nil {
		return err
	}
	if err := s.audit.Log(ctx, tx, audit.Event{
		ActorType: audit.ActorUser, UserID: userIDPtr(ch.UserID), Action: audit.ActionMFAFailed,
		Entity: "user", EntityID: userIDPtr(ch.UserID), IP: ip,
		Detail: map[string]any{"reason": reason, "attempts": attempts},
	}); err != nil {
		return err
	}
	return errMFAChallenge()
}

func lastTOTPStep(p *int64) uint64 {
	if p == nil || *p < 0 {
		return 0
	}
	return uint64(*p)
}

// matchTOTP mencocokkan kode 6 digit pada langkah sekarang ±1 dan menolak kode
// yang langkahnya sudah pernah dipakai (anti-replay).
func matchTOTP(secret, code string, now time.Time, lastStep uint64) (uint64, bool, error) {
	code = strings.TrimSpace(code)
	if len(code) != totpCodeDigits {
		return 0, false, nil
	}
	current := int64(now.Unix() / totpPeriodSeconds)
	// Kode diterima pada langkah sekarang lalu ± totpSkew langkah.
	deltas := make([]int64, 0, int(totpSkew)*2+1)
	deltas = append(deltas, 0)
	for i := int64(1); i <= int64(totpSkew); i++ {
		deltas = append(deltas, i, -i)
	}
	for _, delta := range deltas {
		step := current + delta
		if step < 0 || uint64(step) <= lastStep {
			continue
		}
		ok, err := hotp.ValidateCustom(code, uint64(step), secret, hotp.ValidateOpts{
			Digits: otp.DigitsSix, Algorithm: otp.AlgorithmSHA1,
		})
		if err != nil {
			return 0, false, err
		}
		if ok {
			return uint64(step), true, nil
		}
	}
	return 0, false, nil
}

// --------------------------------------------------------------- recovery

func canonicalRecoveryCode(code string) string {
	var b strings.Builder
	for _, r := range strings.ToUpper(strings.TrimSpace(code)) {
		if (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') {
			b.WriteRune(r)
		}
	}
	return b.String()
}

func hashRecoveryCode(code string) string {
	sum := sha256.Sum256([]byte(canonicalRecoveryCode(code)))
	return hex.EncodeToString(sum[:])
}

func generateRecoveryCode() (string, error) {
	max := big.NewInt(int64(len(recoveryAlphabet)))
	raw := make([]byte, recoveryCodeLength)
	for i := range raw {
		n, err := rand.Int(rand.Reader, max)
		if err != nil {
			return "", err
		}
		raw[i] = recoveryAlphabet[n.Int64()]
	}
	var b strings.Builder
	for i, c := range raw {
		if i > 0 && i%recoveryCodeGroupSize == 0 {
			b.WriteByte('-')
		}
		b.WriteByte(c)
	}
	return b.String(), nil
}

func generateRecoveryCodes(n int) (plaintext []string, hashes []string, err error) {
	plaintext = make([]string, 0, n)
	hashes = make([]string, 0, n)
	for i := 0; i < n; i++ {
		code, err := generateRecoveryCode()
		if err != nil {
			return nil, nil, err
		}
		plaintext = append(plaintext, code)
		hashes = append(hashes, hashRecoveryCode(code))
	}
	return plaintext, hashes, nil
}

func (s *Service) replaceRecoveryCodes(ctx context.Context, tx pgx.Tx, userID int64) ([]string, error) {
	plaintext, hashes, err := generateRecoveryCodes(s.opts.MFARecoveryCodes)
	if err != nil {
		return nil, err
	}
	if _, err := tx.Exec(ctx, `DELETE FROM recovery_codes WHERE user_id = $1`, userID); err != nil {
		return nil, err
	}
	for _, h := range hashes {
		if _, err := tx.Exec(ctx,
			`INSERT INTO recovery_codes (user_id, code_hash) VALUES ($1, $2)`, userID, h); err != nil {
			return nil, err
		}
	}
	return plaintext, nil
}

// ------------------------------------------------------------- enrollment

// MFASetup membuat secret TOTP baru untuk challenge ENROLL. Memanggil ulang
// sebelum enable akan mengganti secret lama.
func (s *Service) MFASetup(ctx context.Context, challengeToken string) (*MFASetupResult, error) {
	if !s.opts.MFAEnabled {
		return nil, errMFADisabled()
	}

	var result *MFASetupResult
	err := db.WithTx(ctx, s.pool, func(tx pgx.Tx) error {
		ch, err := s.loadChallenge(ctx, tx, challengeToken, true)
		if err != nil {
			return err
		}
		if ch.Purpose != mfaPurposeEnroll {
			return errMFAChallenge()
		}
		if ch.User.totpEnabledAt != nil {
			return errMFAAlreadyEnabled()
		}

		key, err := totp.Generate(totp.GenerateOpts{
			Issuer:      s.opts.MFAIssuer,
			AccountName: ch.User.Email,
			Period:      totpPeriodSeconds,
			Digits:      otp.DigitsSix,
			Algorithm:   otp.AlgorithmSHA1,
		})
		if err != nil {
			return fmt.Errorf("auth: buat secret TOTP: %w", err)
		}
		enc, err := s.encryptSecret(key.Secret())
		if err != nil {
			return err
		}
		if _, err := tx.Exec(ctx,
			`UPDATE users SET totp_secret_enc = $2 WHERE id = $1`, ch.UserID, enc); err != nil {
			return err
		}
		result = &MFASetupResult{OTPAuthURL: key.URL(), Secret: key.Secret()}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return result, nil
}

// MFAEnable memverifikasi kode enrollment, mengaktifkan TOTP, menerbitkan
// recovery code sekali tampil, lalu membuat sesi penuh.
func (s *Service) MFAEnable(ctx context.Context, in MFAEnableInput) (*MFAVerifyResult, error) {
	if !s.opts.MFAEnabled {
		return nil, errMFADisabled()
	}
	if strings.TrimSpace(in.Code) == "" {
		return nil, fieldError("code", "Kode 6 digit wajib diisi.")
	}

	var (
		result  *MFAVerifyResult
		failure error
	)
	err := db.WithTx(ctx, s.pool, func(tx pgx.Tx) error {
		ch, err := s.loadChallenge(ctx, tx, in.ChallengeToken, true)
		if err != nil {
			failure = err
			return nil
		}
		if ch.Purpose != mfaPurposeEnroll {
			failure = errMFAChallenge()
			return nil
		}
		if ch.User.totpEnabledAt != nil {
			failure = errMFAAlreadyEnabled()
			return nil
		}
		if ch.secretEnc == nil || *ch.secretEnc == "" {
			failure = httpx.ErrConflict("Jalankan setup 2FA terlebih dahulu.")
			return nil
		}
		secret, err := s.decryptSecret(*ch.secretEnc)
		if err != nil {
			return err
		}
		step, ok, err := matchTOTP(secret, in.Code, s.now(), lastTOTPStep(ch.User.totpLastStep))
		if err != nil {
			return err
		}
		if !ok {
			failure = s.recordMFAFailure(ctx, tx, ch, in.IP, "bad_enrollment_code")
			return nil
		}

		now := s.now()
		if _, err := tx.Exec(ctx, `
			UPDATE users SET totp_enabled_at = $2, totp_last_step = $3, failed_login_count = 0,
				locked_until = NULL, last_login_at = $2
			WHERE id = $1`, ch.UserID, now, step); err != nil {
			return err
		}
		codes, err := s.replaceRecoveryCodes(ctx, tx, ch.UserID)
		if err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `DELETE FROM mfa_challenges WHERE user_id = $1`, ch.UserID); err != nil {
			return err
		}
		token, expires, err := s.createSession(ctx, tx, ch.UserID, in.IP, in.UserAgent, now)
		if err != nil {
			return err
		}
		if err := s.audit.Log(ctx, tx, audit.Event{
			ActorType: audit.ActorUser, UserID: userIDPtr(ch.UserID), Action: audit.ActionMFAEnrolled,
			Entity: "user", EntityID: userIDPtr(ch.UserID), IP: in.IP,
		}); err != nil {
			return err
		}

		user := ch.User.User
		user.TotpEnabled = true
		user.LastLoginAt = &now
		result = &MFAVerifyResult{User: user, Token: token, ExpiresAt: expires, RecoveryCodes: codes}
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("auth: aktifkan 2FA: %w", err)
	}
	return result, failure
}

// MFAVerify menukar kode TOTP atau recovery code dengan sesi penuh.
func (s *Service) MFAVerify(ctx context.Context, in MFAVerifyInput) (*MFAVerifyResult, error) {
	if !s.opts.MFAEnabled {
		return nil, errMFADisabled()
	}
	hasCode := strings.TrimSpace(in.Code) != ""
	hasRecovery := strings.TrimSpace(in.RecoveryCode) != ""
	if hasCode == hasRecovery {
		e := httpx.ErrValidation("Isi tepat satu: kode TOTP atau recovery code.")
		e.Fields = map[string]string{"code": "Isi kode TOTP atau recovery code, bukan keduanya."}
		return nil, e
	}

	var (
		result  *MFAVerifyResult
		failure error
	)
	err := db.WithTx(ctx, s.pool, func(tx pgx.Tx) error {
		ch, err := s.loadChallenge(ctx, tx, in.ChallengeToken, true)
		if err != nil {
			failure = err
			return nil
		}
		if ch.Purpose != mfaPurposeLogin {
			failure = errMFAChallenge()
			return nil
		}

		now := s.now()
		method := "totp"
		if hasCode {
			if ch.secretEnc == nil || *ch.secretEnc == "" {
				failure = httpx.ErrConflict("2FA belum terdaftar untuk akun ini.")
				return nil
			}
			secret, err := s.decryptSecret(*ch.secretEnc)
			if err != nil {
				return err
			}
			step, ok, err := matchTOTP(secret, in.Code, now, lastTOTPStep(ch.User.totpLastStep))
			if err != nil {
				return err
			}
			if !ok {
				failure = s.recordMFAFailure(ctx, tx, ch, in.IP, "bad_totp_code")
				return nil
			}
			if _, err := tx.Exec(ctx, `
				UPDATE users SET totp_last_step = $2, failed_login_count = 0, locked_until = NULL,
					last_login_at = $3
				WHERE id = $1`, ch.UserID, step, now); err != nil {
				return err
			}
		} else {
			method = "recovery_code"
			var codeID int64
			err := tx.QueryRow(ctx, `
				UPDATE recovery_codes SET used_at = $2
				WHERE user_id = $1 AND code_hash = $3 AND used_at IS NULL
				RETURNING id`, ch.UserID, now, hashRecoveryCode(in.RecoveryCode)).Scan(&codeID)
			if errors.Is(err, pgx.ErrNoRows) {
				failure = s.recordMFAFailure(ctx, tx, ch, in.IP, "bad_recovery_code")
				return nil
			}
			if err != nil {
				return err
			}
			if err := s.audit.Log(ctx, tx, audit.Event{
				ActorType: audit.ActorUser, UserID: userIDPtr(ch.UserID), Action: audit.ActionMFARecoveryUsed,
				Entity: "user", EntityID: userIDPtr(ch.UserID), IP: in.IP,
			}); err != nil {
				return err
			}
			if _, err := tx.Exec(ctx, `
				UPDATE users SET failed_login_count = 0, locked_until = NULL, last_login_at = $2
				WHERE id = $1`, ch.UserID, now); err != nil {
				return err
			}
		}

		if _, err := tx.Exec(ctx, `DELETE FROM mfa_challenges WHERE id = $1`, ch.ID); err != nil {
			return err
		}
		token, expires, err := s.createSession(ctx, tx, ch.UserID, in.IP, in.UserAgent, now)
		if err != nil {
			return err
		}
		if err := s.audit.Log(ctx, tx, audit.Event{
			ActorType: audit.ActorUser, UserID: userIDPtr(ch.UserID), Action: audit.ActionMFASuccess,
			Entity: "user", EntityID: userIDPtr(ch.UserID), IP: in.IP,
			Detail: map[string]any{"method": method},
		}); err != nil {
			return err
		}

		user := ch.User.User
		user.LastLoginAt = &now
		result = &MFAVerifyResult{User: user, Token: token, ExpiresAt: expires}
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("auth: verifikasi 2FA: %w", err)
	}
	return result, failure
}

// ------------------------------------------------------------ pengelolaan

// checkPasswordAndCode memeriksa password dan kode TOTP dari sesi aktif.
func (s *Service) checkPasswordAndCode(ctx context.Context, tx pgx.Tx, userID int64, password, code string) (uint64, error) {
	var (
		passwordHash string
		secretEnc    *string
		enabledAt    *time.Time
		lastStep     *int64
		lockedUntil  *time.Time
		isActive     bool
	)
	err := tx.QueryRow(ctx, `
		SELECT password_hash, totp_secret_enc, totp_enabled_at, totp_last_step, locked_until, is_active
		FROM users WHERE id = $1 FOR UPDATE`, userID).Scan(
		&passwordHash, &secretEnc, &enabledAt, &lastStep, &lockedUntil, &isActive)
	if errors.Is(err, pgx.ErrNoRows) {
		return 0, httpx.ErrUnauthorized()
	}
	if err != nil {
		return 0, err
	}
	if !isActive {
		return 0, httpx.ErrUnauthorized()
	}
	now := s.now()
	if lockedUntil != nil && lockedUntil.After(now) {
		return 0, lockedError(*lockedUntil, now)
	}
	if enabledAt == nil {
		return 0, httpx.ErrConflict("2FA belum aktif untuk akun ini.")
	}
	if !s.hasher.Verify(passwordHash, password) {
		return 0, fieldError("password", "Password salah.")
	}
	if strings.TrimSpace(code) == "" {
		return 0, fieldError("code", "Kode 6 digit wajib diisi.")
	}
	if secretEnc == nil || *secretEnc == "" {
		return 0, httpx.ErrConflict("2FA belum terdaftar untuk akun ini.")
	}
	secret, err := s.decryptSecret(*secretEnc)
	if err != nil {
		return 0, err
	}
	step, ok, err := matchTOTP(secret, code, now, lastTOTPStep(lastStep))
	if err != nil {
		return 0, err
	}
	if !ok {
		return 0, fieldError("code", "Kode verifikasi salah atau sudah dipakai.")
	}
	return step, nil
}

// MFADisable mematikan 2FA setelah memverifikasi password dan kode TOTP.
func (s *Service) MFADisable(ctx context.Context, p *Principal, in MFACredentialInput) error {
	if !s.opts.MFAEnabled {
		return errMFADisabled()
	}
	var failure error
	err := db.WithTx(ctx, s.pool, func(tx pgx.Tx) error {
		step, err := s.checkPasswordAndCode(ctx, tx, p.UserID, in.Password, in.Code)
		if err != nil {
			failure = err
			return nil
		}
		_ = step
		if _, err := tx.Exec(ctx, `
			UPDATE users SET totp_secret_enc = NULL, totp_enabled_at = NULL, totp_last_step = NULL
			WHERE id = $1`, p.UserID); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `DELETE FROM recovery_codes WHERE user_id = $1`, p.UserID); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `DELETE FROM mfa_challenges WHERE user_id = $1`, p.UserID); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx,
			`DELETE FROM sessions WHERE user_id = $1 AND id <> $2`, p.UserID, p.SessionID); err != nil {
			return err
		}
		return s.audit.Log(ctx, tx, audit.Event{
			ActorType: audit.ActorUser, UserID: userIDPtr(p.UserID), Action: audit.ActionMFADisabled,
			Entity: "user", EntityID: userIDPtr(p.UserID), IP: in.IP,
		})
	})
	if err != nil {
		return fmt.Errorf("auth: matikan 2FA: %w", err)
	}
	return failure
}

// MFARegenerateRecoveryCodes menerbitkan set recovery code baru (yang lama
// dihapus). Plaintext hanya dikembalikan sekali.
func (s *Service) MFARegenerateRecoveryCodes(ctx context.Context, p *Principal, in MFACredentialInput) ([]string, error) {
	if !s.opts.MFAEnabled {
		return nil, errMFADisabled()
	}
	var (
		codes   []string
		failure error
	)
	err := db.WithTx(ctx, s.pool, func(tx pgx.Tx) error {
		step, err := s.checkPasswordAndCode(ctx, tx, p.UserID, in.Password, in.Code)
		if err != nil {
			failure = err
			return nil
		}
		if _, err := tx.Exec(ctx,
			`UPDATE users SET totp_last_step = $2 WHERE id = $1`, p.UserID, step); err != nil {
			return err
		}
		codes, err = s.replaceRecoveryCodes(ctx, tx, p.UserID)
		if err != nil {
			return err
		}
		return s.audit.Log(ctx, tx, audit.Event{
			ActorType: audit.ActorUser, UserID: userIDPtr(p.UserID), Action: audit.ActionMFARecoveryCodesRegenerated,
			Entity: "user", EntityID: userIDPtr(p.UserID), IP: in.IP,
		})
	})
	if err != nil {
		return nil, fmt.Errorf("auth: terbitkan ulang recovery code: %w", err)
	}
	return codes, failure
}
