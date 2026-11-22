package auth

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"math"
	"strings"
	"time"

	"github.com/RidwanRamdhani/ppkpt-backend/internal/audit"
	"github.com/RidwanRamdhani/ppkpt-backend/internal/platform/db"
	"github.com/RidwanRamdhani/ppkpt-backend/internal/platform/httpx"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// maxUserAgentLen mengikuti lebar kolom sessions.user_agent.
const maxUserAgentLen = 255

// Service menjalankan logika autentikasi, sesi, dan pengelolaan akun.
type Service struct {
	pool   *pgxpool.Pool
	audit  *audit.Service
	logger *slog.Logger
	opts   Options
	hasher *hasher
	now    func() time.Time
}

// NewService membuat Service. pool boleh nil hanya untuk pengujian unit yang
// tidak menyentuh database.
func NewService(pool *pgxpool.Pool, auditor *audit.Service, logger *slog.Logger, opts Options) (*Service, error) {
	if logger == nil {
		logger = slog.Default()
	}
	opts = opts.withDefaults()
	h, err := newHasher(opts.BcryptCost)
	if err != nil {
		return nil, err
	}
	return &Service{pool: pool, audit: auditor, logger: logger, opts: opts, hasher: h, now: time.Now}, nil
}

// ---------------------------------------------------------------- akses data

type userRecord struct {
	User
	passwordHash string
	failedCount  int16
	lockedUntil  *time.Time
}

const userSelect = `SELECT id, name, username, email, password_hash, role, is_active,
	must_change_password, failed_login_count, locked_until, last_login_at, created_at
	FROM users`

func scanUser(row pgx.Row) (*userRecord, error) {
	var u userRecord
	if err := row.Scan(&u.ID, &u.Name, &u.Username, &u.Email, &u.passwordHash, &u.Role,
		&u.IsActive, &u.MustChangePassword, &u.failedCount, &u.lockedUntil, &u.LastLoginAt, &u.CreatedAt); err != nil {
		return nil, err
	}
	return &u, nil
}

// findByIdentifier mencari akun berdasarkan username atau email (tanpa membedakan
// huruf besar/kecil). Username tidak boleh memuat '@' sehingga tidak ambigu.
func findByIdentifier(ctx context.Context, q db.DBTX, ident string) (*userRecord, error) {
	return scanUser(q.QueryRow(ctx, userSelect+
		` WHERE lower(username) = $1 OR lower(email) = $1 ORDER BY id LIMIT 1`, ident))
}

func normalizeIdentifier(s string) string {
	return strings.ToLower(strings.TrimSpace(s))
}

// registerFailure menaikkan penghitung gagal secara atomik. Bila batas tercapai,
// akun dikunci sampai until dan penghitung direset agar setelah kunci berakhir
// pengguna mendapat percobaan baru. Mengembalikan true bila akun baru saja dikunci.
func (s *Service) registerFailure(ctx context.Context, q db.DBTX, userID int64, until time.Time) (bool, error) {
	var locked bool
	err := q.QueryRow(ctx, `
		UPDATE users SET
			failed_login_count = CASE WHEN failed_login_count + 1 >= $2::int THEN 0 ELSE failed_login_count + 1 END,
			locked_until       = CASE WHEN failed_login_count + 1 >= $2::int THEN $3::timestamptz ELSE locked_until END
		WHERE id = $1
		RETURNING (failed_login_count = 0 AND locked_until = $3::timestamptz)`,
		userID, s.opts.LoginMaxAttempts, until).Scan(&locked)
	return locked, err
}

func lockedError(until, now time.Time) *httpx.AppError {
	minutes := int(math.Ceil(until.Sub(now).Minutes()))
	if minutes < 1 {
		minutes = 1
	}
	e := httpx.ErrLocked(fmt.Sprintf(
		"Akun terkunci sementara karena terlalu banyak percobaan gagal. Coba lagi dalam %d menit.", minutes))
	e.Fields = map[string]string{"locked_until": until.UTC().Format(time.RFC3339)}
	return e
}

func invalidCredentials() *httpx.AppError {
	return httpx.NewAppError(401, httpx.CodeUnauthorized, "Nama pengguna/email atau password salah.")
}

func fieldError(field, msg string) *httpx.AppError {
	e := httpx.ErrValidation("Data tidak valid.")
	e.Fields = map[string]string{field: msg}
	return e
}

func userIDPtr(id int64) *int64 { return &id }

// ---------------------------------------------------------------------- login

// LoginInput adalah masukan Login.
type LoginInput struct {
	Identifier string // username atau email
	Password   string
	IP         string
	UserAgent  string
}

// LoginResult berisi pengguna dan token sesi untuk dipasang di cookie.
type LoginResult struct {
	User      User
	Token     string
	ExpiresAt time.Time
}

// Login memverifikasi kredensial lalu membuat sesi baru.
//
// Pesan galat tidak membedakan akun yang tidak ada, password salah, dan akun
// nonaktif. Pengecualian: akun yang sedang terkunci menghasilkan 423 beserta
// waktu pembukaan (sesuai rancangan M03). Pemeriksaan kunci dilakukan sebelum
// password dibandingkan sehingga penebakan tidak bisa berlanjut selama dikunci.
func (s *Service) Login(ctx context.Context, in LoginInput) (*LoginResult, error) {
	ident := normalizeIdentifier(in.Identifier)
	if ident == "" || in.Password == "" {
		e := httpx.ErrValidation("Nama pengguna/email dan password wajib diisi.")
		e.Fields = map[string]string{}
		if ident == "" {
			e.Fields["identifier"] = "Wajib diisi."
		}
		if in.Password == "" {
			e.Fields["password"] = "Wajib diisi."
		}
		return nil, e
	}
	now := s.now()

	u, err := findByIdentifier(ctx, s.pool, ident)
	if errors.Is(err, pgx.ErrNoRows) {
		s.hasher.BurnTime(in.Password)
		// Identitas yang diketik tidak dicatat: bisa jadi itu password yang salah ketik.
		if aerr := s.audit.Log(ctx, s.pool, audit.Event{
			ActorType: audit.ActorSystem, Action: audit.ActionLoginFailed, Entity: "user",
			IP: in.IP, Detail: map[string]any{"reason": "unknown_identifier"},
		}); aerr != nil {
			s.logger.Error("audit login gagal", "err", aerr)
		}
		return nil, invalidCredentials()
	}
	if err != nil {
		return nil, fmt.Errorf("auth: cari akun: %w", err)
	}

	if u.lockedUntil != nil && u.lockedUntil.After(now) {
		return nil, lockedError(*u.lockedUntil, now)
	}

	passwordOK := s.hasher.Verify(u.passwordHash, in.Password)
	if !passwordOK {
		return nil, s.failLogin(ctx, u.ID, in.IP, "bad_password")
	}
	if !u.IsActive {
		return nil, s.failLogin(ctx, u.ID, in.IP, "inactive")
	}

	token, sessionID, err := newToken()
	if err != nil {
		return nil, fmt.Errorf("auth: buat token: %w", err)
	}
	expires := now.Add(s.opts.SessionAbsolute)

	err = db.WithTx(ctx, s.pool, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx,
			`UPDATE users SET failed_login_count = 0, locked_until = NULL, last_login_at = $2 WHERE id = $1`,
			u.ID, now); err != nil {
			return err
		}
		// Naikkan cost hash secara bertahap bila BCRYPT_COST dinaikkan.
		if s.hasher.NeedsRehash(u.passwordHash) {
			if h, herr := s.hasher.Hash(in.Password); herr == nil {
				if _, err := tx.Exec(ctx, `UPDATE users SET password_hash = $2 WHERE id = $1`, u.ID, h); err != nil {
					return err
				}
			}
		}
		// Bersihkan sesi lama milik pengguna yang sudah tidak berlaku.
		if _, err := tx.Exec(ctx,
			`DELETE FROM sessions WHERE user_id = $1 AND (expires_at <= $2 OR last_seen_at <= $3)`,
			u.ID, now, now.Add(-s.opts.SessionIdle)); err != nil {
			return err
		}
		ua := in.UserAgent
		if len(ua) > maxUserAgentLen {
			ua = ua[:maxUserAgentLen]
		}
		if _, err := tx.Exec(ctx, `
			INSERT INTO sessions (id, user_id, created_at, last_seen_at, expires_at, ip_address, user_agent)
			VALUES ($1, $2, $3, $3, $4, NULLIF($5, ''), NULLIF($6, ''))`,
			sessionID, u.ID, now, expires, in.IP, ua); err != nil {
			return err
		}
		return s.audit.Log(ctx, tx, audit.Event{
			ActorType: audit.ActorUser, UserID: userIDPtr(u.ID), Action: audit.ActionLoginSuccess,
			Entity: "user", EntityID: userIDPtr(u.ID), IP: in.IP,
			Detail: map[string]any{"role": u.Role},
		})
	})
	if err != nil {
		return nil, fmt.Errorf("auth: simpan sesi: %w", err)
	}

	user := u.User
	user.LastLoginAt = &now
	return &LoginResult{User: user, Token: token, ExpiresAt: expires}, nil
}

// failLogin mencatat satu kegagalan login dan mengembalikan galat yang sesuai
// (401 atau 423 bila akun baru saja terkunci).
func (s *Service) failLogin(ctx context.Context, userID int64, ip, reason string) error {
	now := s.now()
	until := now.Add(s.opts.LoginLockDuration)
	var locked bool
	err := db.WithTx(ctx, s.pool, func(tx pgx.Tx) error {
		l, err := s.registerFailure(ctx, tx, userID, until)
		if err != nil {
			return err
		}
		locked = l
		if err := s.audit.Log(ctx, tx, audit.Event{
			ActorType: audit.ActorUser, UserID: userIDPtr(userID), Action: audit.ActionLoginFailed,
			Entity: "user", EntityID: userIDPtr(userID), IP: ip, Detail: map[string]any{"reason": reason},
		}); err != nil {
			return err
		}
		if locked {
			return s.audit.Log(ctx, tx, audit.Event{
				ActorType: audit.ActorUser, UserID: userIDPtr(userID), Action: audit.ActionAccountLocked,
				Entity: "user", EntityID: userIDPtr(userID), IP: ip,
				Detail: map[string]any{"locked_minutes": int(s.opts.LoginLockDuration / time.Minute)},
			})
		}
		return nil
	})
	if err != nil {
		return fmt.Errorf("auth: catat login gagal: %w", err)
	}
	if locked {
		return lockedError(until, now)
	}
	return invalidCredentials()
}

// ----------------------------------------------------------------------- sesi

// Authenticate memvalidasi token sesi dan mengembalikan Principal. Sesi yang
// kedaluwarsa (idle maupun mutlak) atau milik akun nonaktif dihapus.
func (s *Service) Authenticate(ctx context.Context, token string) (*Principal, error) {
	if !validTokenFormat(token) {
		return nil, httpx.ErrUnauthorized()
	}
	id := hashToken(token)
	now := s.now()

	var (
		lastSeen, expires time.Time
		p                 = Principal{SessionID: id}
		active            bool
	)
	err := s.pool.QueryRow(ctx, `
		SELECT s.last_seen_at, s.expires_at, u.id, u.name, u.username, u.email, u.role, u.is_active, u.must_change_password
		FROM sessions s JOIN users u ON u.id = s.user_id
		WHERE s.id = $1`, id).Scan(&lastSeen, &expires, &p.UserID, &p.Name, &p.Username, &p.Email, &p.Role, &active, &p.MustChangePassword)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, httpx.ErrUnauthorized()
	}
	if err != nil {
		return nil, fmt.Errorf("auth: baca sesi: %w", err)
	}

	if !active || !expires.After(now) || !lastSeen.Add(s.opts.SessionIdle).After(now) {
		if _, derr := s.pool.Exec(ctx, `DELETE FROM sessions WHERE id = $1`, id); derr != nil {
			s.logger.Error("hapus sesi kedaluwarsa", "err", derr)
		}
		return nil, httpx.ErrUnauthorized()
	}

	if now.Sub(lastSeen) >= touchInterval {
		if _, uerr := s.pool.Exec(ctx,
			`UPDATE sessions SET last_seen_at = $2 WHERE id = $1 AND last_seen_at < $2`, id, now); uerr != nil {
			s.logger.Error("perbarui last_seen_at", "err", uerr)
		}
	}
	return &p, nil
}

// Logout menghapus sesi milik token. Token yang tidak dikenal diabaikan
// (idempoten) agar klien selalu dapat membersihkan cookie-nya.
func (s *Service) Logout(ctx context.Context, token, ip string) error {
	if !validTokenFormat(token) {
		return nil
	}
	id := hashToken(token)
	return db.WithTx(ctx, s.pool, func(tx pgx.Tx) error {
		var uid int64
		err := tx.QueryRow(ctx, `DELETE FROM sessions WHERE id = $1 RETURNING user_id`, id).Scan(&uid)
		if errors.Is(err, pgx.ErrNoRows) {
			return nil
		}
		if err != nil {
			return err
		}
		return s.audit.Log(ctx, tx, audit.Event{
			ActorType: audit.ActorUser, UserID: userIDPtr(uid), Action: audit.ActionLogout,
			Entity: "user", EntityID: userIDPtr(uid), IP: ip,
		})
	})
}

// CleanupSessions menghapus sesi yang sudah tidak berlaku dan mengembalikan jumlahnya.
func (s *Service) CleanupSessions(ctx context.Context) (int64, error) {
	now := s.now()
	tag, err := s.pool.Exec(ctx,
		`DELETE FROM sessions WHERE expires_at <= $1 OR last_seen_at <= $2`, now, now.Add(-s.opts.SessionIdle))
	if err != nil {
		return 0, err
	}
	return tag.RowsAffected(), nil
}

// RunCleanup membersihkan sesi kedaluwarsa secara berkala sampai ctx selesai.
func (s *Service) RunCleanup(ctx context.Context, interval time.Duration) {
	if interval <= 0 {
		interval = 10 * time.Minute
	}
	t := time.NewTicker(interval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			n, err := s.CleanupSessions(ctx)
			switch {
			case err != nil && ctx.Err() == nil:
				s.logger.Error("bersihkan sesi", "err", err)
			case n > 0:
				s.logger.Info("sesi kedaluwarsa dihapus", "jumlah", n)
			}
		}
	}
}

// --------------------------------------------------------------- ganti password

// ChangePasswordInput adalah masukan ChangePassword.
type ChangePasswordInput struct {
	Current string
	New     string
	IP      string
}

// ChangePassword mengganti password pengguna yang sedang login (FR-20).
// Semua sesi lain milik pengguna dicabut; sesi saat ini tetap berlaku.
// Salah memasukkan password saat ini dihitung seperti login gagal (penguncian).
func (s *Service) ChangePassword(ctx context.Context, p *Principal, in ChangePasswordInput) error {
	if in.Current == "" {
		return fieldError("current_password", "Wajib diisi.")
	}
	if in.New == in.Current {
		return fieldError("new_password", "Password baru harus berbeda dari password saat ini.")
	}
	if perr := ValidatePassword(in.New, p.Username, p.Email, emailLocalPart(p.Email)); perr != nil {
		perr.Fields = map[string]string{"new_password": perr.Fields["password"]}
		return perr
	}

	var failure error
	err := db.WithTx(ctx, s.pool, func(tx pgx.Tx) error {
		u, err := scanUser(tx.QueryRow(ctx, userSelect+` WHERE id = $1 FOR UPDATE`, p.UserID))
		if errors.Is(err, pgx.ErrNoRows) {
			failure = httpx.ErrUnauthorized()
			return nil
		}
		if err != nil {
			return err
		}
		now := s.now()
		if !u.IsActive {
			failure = httpx.ErrUnauthorized()
			return nil
		}
		if u.lockedUntil != nil && u.lockedUntil.After(now) {
			failure = lockedError(*u.lockedUntil, now)
			return nil
		}

		if !s.hasher.Verify(u.passwordHash, in.Current) {
			until := now.Add(s.opts.LoginLockDuration)
			locked, err := s.registerFailure(ctx, tx, u.ID, until)
			if err != nil {
				return err
			}
			if err := s.audit.Log(ctx, tx, audit.Event{
				ActorType: audit.ActorUser, UserID: userIDPtr(u.ID), Action: audit.ActionLoginFailed,
				Entity: "user", EntityID: userIDPtr(u.ID), IP: in.IP,
				Detail: map[string]any{"reason": "wrong_current_password"},
			}); err != nil {
				return err
			}
			if locked {
				// Akun terkunci: paksa keluar dari semua sesi.
				if _, err := tx.Exec(ctx, `DELETE FROM sessions WHERE user_id = $1`, u.ID); err != nil {
					return err
				}
				if err := s.audit.Log(ctx, tx, audit.Event{
					ActorType: audit.ActorUser, UserID: userIDPtr(u.ID), Action: audit.ActionAccountLocked,
					Entity: "user", EntityID: userIDPtr(u.ID), IP: in.IP,
					Detail: map[string]any{"locked_minutes": int(s.opts.LoginLockDuration / time.Minute)},
				}); err != nil {
					return err
				}
				failure = lockedError(until, now)
				return nil
			}
			failure = fieldError("current_password", "Password saat ini salah.")
			return nil
		}

		hash, err := s.hasher.Hash(in.New)
		if err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `
			UPDATE users SET password_hash = $2, must_change_password = FALSE,
				failed_login_count = 0, locked_until = NULL WHERE id = $1`, u.ID, hash); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `DELETE FROM sessions WHERE user_id = $1 AND id <> $2`, u.ID, p.SessionID); err != nil {
			return err
		}
		return s.audit.Log(ctx, tx, audit.Event{
			ActorType: audit.ActorUser, UserID: userIDPtr(u.ID), Action: audit.ActionPasswordChanged,
			Entity: "user", EntityID: userIDPtr(u.ID), IP: in.IP, Detail: map[string]any{"by": "self"},
		})
	})
	if err != nil {
		return fmt.Errorf("auth: ganti password: %w", err)
	}
	return failure
}

func emailLocalPart(email string) string {
	if i := strings.IndexByte(email, '@'); i > 0 {
		return email[:i]
	}
	return email
}
