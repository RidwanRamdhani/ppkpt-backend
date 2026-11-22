// Package auth mengimplementasikan Modul Authentication and Account (M03):
// login petugas, sesi berbasis cookie, otorisasi role di server, ganti
// password, dan pembuatan/penonaktifan akun oleh sistem (tanpa halaman UI).
//
// Prinsip utama:
//   - Password disimpan sebagai hash bcrypt (NFR-05); token sesi tidak pernah
//     disimpan mentah, hanya hash SHA-256-nya (kolom sessions.id).
//   - Cookie sesi HttpOnly, Secure (sesuai konfigurasi), dan SameSite=Lax (NFR-14).
//   - Sesi berakhir karena tidak aktif (SESSION_IDLE_MINUTES) atau karena batas
//     mutlak (SESSION_ABSOLUTE_HOURS).
//   - Akun dikunci sementara setelah LOGIN_MAX_ATTEMPTS percobaan gagal (FR-21).
//   - Otorisasi selalu diterapkan di server, bukan hanya di antarmuka (NFR-10).
package auth

import (
	"time"

	"github.com/RidwanRamdhani/ppkpt-backend/internal/config"
)

// Role pengguna petugas. Pelapor tidak memiliki akun.
const (
	RoleSatgas  = "SATGAS"
	RolePejabat = "PEJABAT"
)

// IsValidRole melaporkan apakah role dikenal sistem.
func IsValidRole(role string) bool {
	return role == RoleSatgas || role == RolePejabat
}

const (
	// SessionCookieName adalah nama cookie sesi.
	SessionCookieName = "ppkpt_session"

	// CodePasswordChangeRequired adalah kode mesin untuk akun yang wajib
	// mengganti password sebelum memakai endpoint lain (FR-20).
	CodePasswordChangeRequired = "PASSWORD_CHANGE_REQUIRED" //nolint:gosec // kode galat, bukan kredensial
)

// Options adalah konfigurasi perilaku autentikasi.
type Options struct {
	BcryptCost        int
	LoginMaxAttempts  int
	LoginLockDuration time.Duration
	SessionIdle       time.Duration
	SessionAbsolute   time.Duration
	CookieSecure      bool
}

// OptionsFromConfig menurunkan Options dari konfigurasi aplikasi.
func OptionsFromConfig(cfg config.Config) Options {
	return Options{
		BcryptCost:        cfg.BcryptCost,
		LoginMaxAttempts:  cfg.LoginMaxAttempts,
		LoginLockDuration: time.Duration(cfg.LoginLockMinutes) * time.Minute,
		SessionIdle:       time.Duration(cfg.SessionIdleMinutes) * time.Minute,
		SessionAbsolute:   time.Duration(cfg.SessionAbsoluteHours) * time.Hour,
		CookieSecure:      cfg.CookieSecure,
	}
}

// withDefaults mengganti nilai yang tidak masuk akal dengan default aman.
func (o Options) withDefaults() Options {
	if o.BcryptCost <= 0 {
		o.BcryptCost = 12
	}
	if o.LoginMaxAttempts <= 0 {
		o.LoginMaxAttempts = 5
	}
	if o.LoginLockDuration <= 0 {
		o.LoginLockDuration = 15 * time.Minute
	}
	if o.SessionIdle <= 0 {
		o.SessionIdle = 30 * time.Minute
	}
	if o.SessionAbsolute <= 0 {
		o.SessionAbsolute = 8 * time.Hour
	}
	return o
}

// User adalah akun petugas/pejabat tanpa data rahasia. Hash password tidak
// pernah menjadi bagian dari tipe ini.
type User struct {
	ID                 int64      `json:"id"`
	Name               string     `json:"name"`
	Username           string     `json:"username"`
	Email              string     `json:"email"`
	Role               string     `json:"role"`
	IsActive           bool       `json:"is_active"`
	MustChangePassword bool       `json:"must_change_password"`
	LastLoginAt        *time.Time `json:"last_login_at,omitempty"`
	CreatedAt          time.Time  `json:"-"`
}

// Principal adalah identitas yang sudah terautentikasi pada satu request.
type Principal struct {
	UserID             int64
	Name               string
	Username           string
	Email              string
	Role               string
	MustChangePassword bool
	// SessionID adalah hash token sesi (kunci baris sessions), bukan token mentah.
	SessionID string
}
