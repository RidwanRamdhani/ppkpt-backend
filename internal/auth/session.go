package auth

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"net/http"
	"time"
)

const tokenBytes = 32

// touchInterval membatasi penulisan last_seen_at agar tidak ada UPDATE di
// setiap request. Idle timeout tetap akurat dalam kisaran satu menit.
const touchInterval = time.Minute

// newToken membuat token sesi acak (untuk cookie) beserta id-nya (hash untuk DB).
func newToken() (token, id string, err error) {
	b := make([]byte, tokenBytes)
	if _, err = rand.Read(b); err != nil {
		return "", "", err
	}
	token = base64.RawURLEncoding.EncodeToString(b)
	return token, hashToken(token), nil
}

// hashToken mengubah token cookie menjadi id sesi 64 karakter heksadesimal
// (SHA-256). Dengan begitu kebocoran tabel sessions tidak membocorkan token
// yang masih berlaku.
func hashToken(token string) string {
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:])
}

// validTokenFormat menolak token berbentuk salah tanpa menyentuh database.
func validTokenFormat(token string) bool {
	if len(token) != base64.RawURLEncoding.EncodedLen(tokenBytes) {
		return false
	}
	b, err := base64.RawURLEncoding.DecodeString(token)
	return err == nil && len(b) == tokenBytes
}

// sessionCookie membuat cookie sesi. Tanpa Max-Age/Expires sehingga cookie
// hilang saat browser ditutup; masa berlaku sebenarnya dikendalikan server.
func (s *Service) sessionCookie(token string) *http.Cookie {
	return &http.Cookie{ //nolint:gosec // Secure mengikuti konfigurasi; HttpOnly dan SameSite selalu aktif
		Name:     SessionCookieName,
		Value:    token,
		Path:     "/",
		HttpOnly: true,
		Secure:   s.opts.CookieSecure,
		SameSite: http.SameSiteLaxMode,
	}
}

// clearCookie membuat cookie yang menghapus cookie sesi di browser.
func (s *Service) clearCookie() *http.Cookie {
	return &http.Cookie{ //nolint:gosec // Secure mengikuti konfigurasi; HttpOnly dan SameSite selalu aktif
		Name:     SessionCookieName,
		Value:    "",
		Path:     "/",
		HttpOnly: true,
		Secure:   s.opts.CookieSecure,
		SameSite: http.SameSiteLaxMode,
		MaxAge:   -1,
		Expires:  time.Unix(0, 0),
	}
}
