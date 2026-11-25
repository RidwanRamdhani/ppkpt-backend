package auth

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/RidwanRamdhani/ppkpt-backend/internal/audit"
	"github.com/pquerna/otp"
	"github.com/pquerna/otp/totp"
)

// Test integrasi 2FA. Butuh TEST_DATABASE_URL dengan migrasi 0001 dan 0002.

func mfaTestOptions(o *Options) {
	o.MFAEnabled = true
	o.MFAEncryptionKey = testMFAKey()
	o.MFAMaxAttempts = 3
	o.MFARecoveryCodes = 10
	o.MFAChallengeTTL = 5 * time.Minute
}

func cookieNamed(resp *http.Response, name string) *http.Cookie {
	for _, c := range resp.Cookies() {
		if c.Name == name {
			return c
		}
	}
	return nil
}

func codeAt(t *testing.T, secret string, at time.Time) string {
	t.Helper()
	code, err := totp.GenerateCodeCustom(secret, at, totp.ValidateOpts{
		Period: totpPeriodSeconds, Digits: otp.DigitsSix, Algorithm: otp.AlgorithmSHA1,
	})
	if err != nil {
		t.Fatal(err)
	}
	return code
}

func TestMFA_EnrollmentLoginRecoveryDanReset(t *testing.T) {
	e := newEnv(t, mfaTestOptions)
	ctx := context.Background()
	username, _ := e.newUser(RoleSatgas, false)

	// Tahap 1: password benar, belum ada sesi.
	first, err := e.login(username, testPassword)
	if err != nil {
		t.Fatal(err)
	}
	if !first.MFARequired || !first.EnrollmentRequired || first.Token != "" || first.MFAToken == "" {
		t.Fatalf("login tahap 1 = %+v", first)
	}
	if e.count(`SELECT count(*) FROM sessions s JOIN users u ON u.id=s.user_id WHERE u.username=$1`, username) != 0 {
		t.Fatal("tidak boleh ada sesi sebelum 2FA selesai")
	}
	if e.count(`SELECT count(*) FROM users WHERE username=$1 AND last_login_at IS NOT NULL`, username) != 0 {
		t.Fatal("last_login_at baru boleh diisi setelah 2FA selesai")
	}
	if e.count(`SELECT count(*) FROM mfa_challenges c JOIN users u ON u.id=c.user_id WHERE u.username=$1 AND c.purpose='ENROLL'`, username) != 1 {
		t.Fatal("challenge ENROLL harus dibuat")
	}

	setup, err := e.svc.MFASetup(ctx, first.MFAToken)
	if err != nil {
		t.Fatal(err)
	}
	if setup.Secret == "" || !strings.HasPrefix(setup.OTPAuthURL, "otpauth://totp/") {
		t.Fatalf("setup = %+v", setup)
	}
	var stored string
	if err := e.pool.QueryRow(ctx, `SELECT totp_secret_enc FROM users WHERE username=$1`, username).Scan(&stored); err != nil {
		t.Fatal(err)
	}
	if stored == setup.Secret || strings.Contains(stored, setup.Secret) {
		t.Fatal("secret TOTP harus tersimpan terenkripsi")
	}

	enabled, err := e.svc.MFAEnable(ctx, MFAEnableInput{
		ChallengeToken: first.MFAToken, Code: codeAt(t, setup.Secret, e.clock), IP: "203.0.113.9", UserAgent: "go-test",
	})
	if err != nil {
		t.Fatal(err)
	}
	if enabled.Token == "" || len(enabled.RecoveryCodes) != 10 || !enabled.User.TotpEnabled {
		t.Fatalf("enable = %+v", enabled)
	}
	if e.count(`SELECT count(*) FROM sessions s JOIN users u ON u.id=s.user_id WHERE u.username=$1`, username) != 1 {
		t.Fatal("sesi harus dibuat setelah enrollment")
	}
	if e.count(`SELECT count(*) FROM recovery_codes rc JOIN users u ON u.id=rc.user_id WHERE u.username=$1`, username) != 10 {
		t.Fatal("10 recovery code harus tersimpan")
	}
	if e.auditCount(username, audit.ActionMFAEnrolled) != 1 {
		t.Fatal("MFA_ENROLLED harus tercatat")
	}
	if e.count(`SELECT count(*) FROM users WHERE username=$1 AND last_login_at IS NOT NULL`, username) != 1 {
		t.Fatal("last_login_at harus terisi setelah enrollment 2FA")
	}

	// Tahap 1 berikutnya adalah challenge LOGIN.
	second, err := e.login(username, testPassword)
	if err != nil {
		t.Fatal(err)
	}
	if !second.MFARequired || second.EnrollmentRequired {
		t.Fatalf("login kedua = %+v", second)
	}
	// Kode yang dipakai saat enable tidak boleh dipakai ulang.
	if _, err := e.svc.MFAVerify(ctx, MFAVerifyInput{
		ChallengeToken: second.MFAToken, Code: codeAt(t, setup.Secret, e.clock), IP: "203.0.113.9",
	}); err == nil {
		t.Fatal("anti-replay: kode enrollment yang sama harus ditolak")
	}

	// Langkah berikutnya menerima kode baru dan membuat sesi.
	e.advance(30 * time.Second)
	verified, err := e.svc.MFAVerify(ctx, MFAVerifyInput{
		ChallengeToken: second.MFAToken, Code: codeAt(t, setup.Secret, e.clock), IP: "203.0.113.9", UserAgent: "go-test",
	})
	if err != nil {
		t.Fatal(err)
	}
	if verified.Token == "" || !verified.User.TotpEnabled {
		t.Fatalf("verify = %+v", verified)
	}
	if e.auditCount(username, audit.ActionMFASuccess) != 1 {
		t.Fatal("MFA_SUCCESS harus tercatat")
	}

	// Recovery code sekali pakai.
	third, _ := e.login(username, testPassword)
	rec, err := e.svc.MFAVerify(ctx, MFAVerifyInput{
		ChallengeToken: third.MFAToken, RecoveryCode: enabled.RecoveryCodes[0], IP: "203.0.113.9",
	})
	if err != nil {
		t.Fatal(err)
	}
	if rec.Token == "" {
		t.Fatal("recovery code harus menghasilkan sesi")
	}
	if e.auditCount(username, audit.ActionMFARecoveryUsed) != 1 {
		t.Fatal("MFA_RECOVERY_USED harus tercatat")
	}
	fourth, _ := e.login(username, testPassword)
	if _, err := e.svc.MFAVerify(ctx, MFAVerifyInput{
		ChallengeToken: fourth.MFAToken, RecoveryCode: enabled.RecoveryCodes[0], IP: "203.0.113.9",
	}); err == nil {
		t.Fatal("recovery code tidak boleh dipakai dua kali")
	}

	// Tiga kode salah mematikan challenge.
	for i := 0; i < 2; i++ {
		_, err := e.svc.MFAVerify(ctx, MFAVerifyInput{
			ChallengeToken: fourth.MFAToken, Code: "000000", IP: "203.0.113.9",
		})
		wantStatus(t, err, http.StatusUnauthorized)
	}
	_, err = e.svc.MFAVerify(ctx, MFAVerifyInput{
		ChallengeToken: fourth.MFAToken, Code: "000000", IP: "203.0.113.9",
	})
	wantStatus(t, err, http.StatusUnauthorized)
	if e.count(`SELECT count(*) FROM mfa_challenges WHERE id=$1 AND consumed_at IS NOT NULL`, hashToken(fourth.MFAToken)) != 1 {
		t.Fatal("challenge harus ditandai consumed setelah batas percobaan")
	}
	if _, err := e.svc.MFAVerify(ctx, MFAVerifyInput{
		ChallengeToken: fourth.MFAToken, Code: codeAt(t, setup.Secret, e.clock), IP: "203.0.113.9",
	}); err == nil {
		t.Fatal("challenge yang sudah mati harus ditolak")
	}
	if e.auditCount(username, audit.ActionMFAFailed) < 4 {
		t.Fatal("setiap kegagalan 2FA harus tercatat")
	}

	// Reset oleh admin: perangkat, recovery code, dan sesi dicabut.
	if err := e.svc.ResetMFA(ctx, username, "test"); err != nil {
		t.Fatal(err)
	}
	if e.count(`SELECT count(*) FROM users WHERE username=$1 AND totp_secret_enc IS NULL AND totp_enabled_at IS NULL AND totp_last_step IS NULL`, username) != 1 {
		t.Fatal("kolom TOTP harus dibersihkan")
	}
	if e.count(`SELECT count(*) FROM recovery_codes rc JOIN users u ON u.id=rc.user_id WHERE u.username=$1`, username) != 0 {
		t.Fatal("recovery code harus dihapus")
	}
	if e.count(`SELECT count(*) FROM sessions s JOIN users u ON u.id=s.user_id WHERE u.username=$1`, username) != 0 {
		t.Fatal("sesi harus dicabut")
	}
	if e.auditCount(username, audit.ActionMFAResetByAdmin) != 1 {
		t.Fatal("MFA_RESET_BY_ADMIN harus tercatat")
	}
	afterReset, err := e.login(username, testPassword)
	if err != nil || !afterReset.EnrollmentRequired {
		t.Fatalf("setelah reset harus enrollment lagi: %+v %v", afterReset, err)
	}
}

func TestMFA_DisableDanRegenerasiRecovery(t *testing.T) {
	e := newEnv(t, mfaTestOptions)
	ctx := context.Background()
	username, _ := e.newUser(RolePejabat, false)

	login, _ := e.login(username, testPassword)
	setup, err := e.svc.MFASetup(ctx, login.MFAToken)
	if err != nil {
		t.Fatal(err)
	}
	enabled, err := e.svc.MFAEnable(ctx, MFAEnableInput{
		ChallengeToken: login.MFAToken, Code: codeAt(t, setup.Secret, e.clock),
	})
	if err != nil {
		t.Fatal(err)
	}
	p, err := e.svc.Authenticate(ctx, enabled.Token)
	if err != nil {
		t.Fatal(err)
	}
	if !p.TotpEnabled {
		t.Fatal("Principal harus memuat status 2FA")
	}

	// Password salah / kode salah tidak mengubah apa pun.
	err = e.svc.MFADisable(ctx, p, MFACredentialInput{Password: "Salah-Sandi-1", Code: codeAt(t, setup.Secret, e.clock)}) //nolint:gosec // kredensial uji
	wantStatus(t, err, http.StatusUnprocessableEntity)
	err = e.svc.MFADisable(ctx, p, MFACredentialInput{Password: testPassword, Code: "000000"})
	wantStatus(t, err, http.StatusUnprocessableEntity)
	if e.count(`SELECT count(*) FROM users WHERE username=$1 AND totp_enabled_at IS NOT NULL`, username) != 1 {
		t.Fatal("2FA tidak boleh berubah saat kredensial salah")
	}

	// Regenerasi recovery code memakai kode baru.
	e.advance(30 * time.Second)
	codes, err := e.svc.MFARegenerateRecoveryCodes(ctx, p, MFACredentialInput{
		Password: testPassword, Code: codeAt(t, setup.Secret, e.clock), IP: "203.0.113.9",
	})
	if err != nil || len(codes) != 10 {
		t.Fatalf("regenerasi = %d %v", len(codes), err)
	}
	if e.auditCount(username, audit.ActionMFARecoveryCodesRegenerated) != 1 {
		t.Fatal("audit regenerasi harus tercatat")
	}
	// Recovery code lama tidak berlaku.
	next, _ := e.login(username, testPassword)
	if _, err := e.svc.MFAVerify(ctx, MFAVerifyInput{
		ChallengeToken: next.MFAToken, RecoveryCode: enabled.RecoveryCodes[0],
	}); err == nil {
		t.Fatal("recovery code lama harus tidak berlaku")
	}
	// Kode baru berlaku dan menghasilkan sesi kedua.
	next2, _ := e.login(username, testPassword)
	second, err := e.svc.MFAVerify(ctx, MFAVerifyInput{
		ChallengeToken: next2.MFAToken, RecoveryCode: codes[0],
	})
	if err != nil {
		t.Fatalf("recovery code baru: %v", err)
	}

	// Disable menghapus 2FA dan sesi lain; sesi saat ini tetap berlaku.
	e.advance(30 * time.Second)
	if err := e.svc.MFADisable(ctx, p, MFACredentialInput{
		Password: testPassword, Code: codeAt(t, setup.Secret, e.clock), IP: "203.0.113.9",
	}); err != nil {
		t.Fatal(err)
	}
	if e.count(`SELECT count(*) FROM users WHERE username=$1 AND totp_enabled_at IS NULL AND totp_secret_enc IS NULL`, username) != 1 {
		t.Fatal("2FA harus nonaktif")
	}
	if e.count(`SELECT count(*) FROM recovery_codes rc JOIN users u ON u.id=rc.user_id WHERE u.username=$1`, username) != 0 {
		t.Fatal("recovery code harus dihapus")
	}
	if _, err := e.svc.Authenticate(ctx, second.Token); err == nil {
		t.Fatal("sesi lain harus dicabut saat 2FA dimatikan")
	}
	if _, err := e.svc.Authenticate(ctx, enabled.Token); err != nil {
		t.Fatalf("sesi saat ini harus tetap berlaku: %v", err)
	}
	if e.auditCount(username, audit.ActionMFADisabled) != 1 {
		t.Fatal("MFA_DISABLED harus tercatat")
	}
}

func TestMFA_LoginBersamaanDenganVerifyTidakDeadlock(t *testing.T) {
	e := newEnv(t, mfaTestOptions)
	ctx := context.Background()
	username, _ := e.newUser(RoleSatgas, false)

	// Enroll sekali agar challenge berikutnya berpurpose LOGIN.
	first, _ := e.login(username, testPassword)
	setup, err := e.svc.MFASetup(ctx, first.MFAToken)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := e.svc.MFAEnable(ctx, MFAEnableInput{
		ChallengeToken: first.MFAToken, Code: codeAt(t, setup.Secret, e.clock),
	}); err != nil {
		t.Fatal(err)
	}

	// Login (membuat challenge baru) dijalankan bersamaan dengan pemakaian
	// challenge. Keduanya mengunci baris challenge lalu user; jika urutannya
	// berbeda, Postgres membatalkan salah satunya dengan deadlock.
	const iterations = 40
	code := codeAt(t, setup.Secret, e.clock)
	var wg sync.WaitGroup
	loginErrs := make(chan error, iterations)
	otherErrs := make(chan error, iterations)

	for i := 0; i < iterations; i++ {
		wg.Add(2)
		go func() {
			defer wg.Done()
			if _, err := e.login(username, testPassword); err != nil {
				loginErrs <- err
			}
		}()
		go func() {
			defer wg.Done()
			res, err := e.login(username, testPassword)
			if err != nil {
				loginErrs <- err
				return
			}
			// Tujuannya mengambil lock baris; 401 karena purpose LOGIN
			// (atau challenge sudah digantikan) masih dianggap wajar.
			_, _ = e.svc.MFASetup(ctx, res.MFAToken)
			_, verr := e.svc.MFAVerify(ctx, MFAVerifyInput{
				ChallengeToken: res.MFAToken, Code: code, IP: "203.0.113.9",
			})
			otherErrs <- verr
		}()
	}
	wg.Wait()
	close(loginErrs)
	close(otherErrs)

	for err := range loginErrs {
		t.Fatalf("login seharusnya selalu berhasil: %v", err)
	}
	for err := range otherErrs {
		if err != nil && strings.Contains(strings.ToLower(err.Error()), "deadlock") {
			t.Fatalf("terjadi deadlock: %v", err)
		}
	}
}

func TestHTTP_MFAAlurLoginDuaTahap(t *testing.T) {
	e := newEnv(t, mfaTestOptions)
	srv := e.server(nil)
	defer srv.Close()
	username, _ := e.newUser(RoleSatgas, false)
	body := fmt.Sprintf(`{"identifier":%q,"password":%q}`, username, testPassword)

	r := do(t, srv, "POST", "/api/auth/login", body)
	if r.StatusCode != 200 || sessionCookie(r) != nil {
		t.Fatalf("login tahap 1: %d cookie sesi=%v", r.StatusCode, sessionCookie(r))
	}
	mc := cookieNamed(r, MFACookieName)
	if mc == nil {
		t.Fatal("cookie ppkpt_mfa harus dipasang")
	}
	var stage1 struct {
		MFARequired        bool `json:"mfa_required"`
		EnrollmentRequired bool `json:"enrollment_required"`
	}
	_ = json.NewDecoder(r.Body).Decode(&stage1)
	if !stage1.MFARequired || !stage1.EnrollmentRequired {
		t.Fatalf("respons tahap 1 = %+v", stage1)
	}
	if r := do(t, srv, "GET", "/api/auth/me", ""); r.StatusCode != 401 {
		t.Fatalf("cookie 2FA bukan sesi: %d", r.StatusCode)
	}

	r = do(t, srv, "POST", "/api/auth/2fa/setup", "", mc)
	if r.StatusCode != 200 {
		t.Fatalf("setup: %d", r.StatusCode)
	}
	var setup mfaSetupResponse
	_ = json.NewDecoder(r.Body).Decode(&setup)
	if setup.Secret == "" {
		t.Fatal("respons setup tanpa secret")
	}

	r = do(t, srv, "POST", "/api/auth/2fa/enable", fmt.Sprintf(`{"code":%q}`, codeAt(t, setup.Secret, e.clock)), mc)
	if r.StatusCode != 200 {
		t.Fatalf("enable: %d", r.StatusCode)
	}
	sc := sessionCookie(r)
	if sc == nil {
		t.Fatal("enable harus menerbitkan sesi")
	}
	if c := cookieNamed(r, MFACookieName); c == nil || c.MaxAge >= 0 {
		t.Fatal("enable harus menghapus cookie 2FA")
	}
	var enabled struct {
		RecoveryCodes []string `json:"recovery_codes"`
		User          User     `json:"user"`
	}
	_ = json.NewDecoder(r.Body).Decode(&enabled)
	if len(enabled.RecoveryCodes) != 10 || !enabled.User.TotpEnabled {
		t.Fatalf("respons enable = %+v", enabled)
	}

	r = do(t, srv, "GET", "/api/auth/me", "", sc)
	if r.StatusCode != 200 {
		t.Fatalf("me: %d", r.StatusCode)
	}
	var me meResponse
	_ = json.NewDecoder(r.Body).Decode(&me)
	if !me.MFAEnabled || !me.User.TotpEnabled {
		t.Fatalf("me harus memuat status 2FA: %+v", me)
	}

	// Logout menghapus kedua cookie.
	r = do(t, srv, "POST", "/api/auth/logout", "", sc, mc)
	if r.StatusCode != 204 || cookieNamed(r, MFACookieName) == nil || cookieNamed(r, SessionCookieName) == nil {
		t.Fatalf("logout harus membersihkan cookie sesi dan 2FA: %d", r.StatusCode)
	}
}
