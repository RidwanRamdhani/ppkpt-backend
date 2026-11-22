package auth

import (
	"crypto/rand"
	"errors"
	"math/big"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/RidwanRamdhani/ppkpt-backend/internal/platform/httpx"
	"golang.org/x/crypto/bcrypt"
)

const (
	// MinPasswordLength adalah panjang minimum password (dalam karakter),
	// sesuai BACKEND_IMPLEMENTATION_GUIDE Phase 5.
	MinPasswordLength = 8
	// MaxPasswordBytes adalah batas bcrypt: byte setelah ke-72 diabaikan.
	// Password lebih panjang ditolak agar tidak terpotong diam-diam.
	MaxPasswordBytes = 72

	generatedPasswordLength = 16
	// Huruf dan angka yang mudah tertukar (0/O, 1/l/I) sengaja dibuang agar
	// password sementara mudah disalin dari layar.
	generatedAlphabet = "ABCDEFGHJKLMNPQRSTUVWXYZabcdefghijkmnpqrstuvwxyz23456789"
)

// hasher membungkus bcrypt dengan cost dari konfigurasi.
type hasher struct {
	cost  int
	dummy []byte
}

func newHasher(cost int) (*hasher, error) {
	if cost < bcrypt.MinCost || cost > bcrypt.MaxCost {
		return nil, errors.New("auth: BCRYPT_COST di luar rentang bcrypt (4-31)")
	}
	// Hash tiruan dipakai agar login untuk identitas yang tidak ada tetap
	// memakan waktu bcrypt, sehingga keberadaan akun tidak bocor lewat waktu respons.
	dummy, err := bcrypt.GenerateFromPassword([]byte("dummy-password-untuk-menyamakan-waktu"), cost)
	if err != nil {
		return nil, err
	}
	return &hasher{cost: cost, dummy: dummy}, nil
}

// Hash membuat hash bcrypt dari password.
func (h *hasher) Hash(password string) (string, error) {
	b, err := bcrypt.GenerateFromPassword([]byte(password), h.cost)
	if err != nil {
		return "", err
	}
	return string(b), nil
}

// Verify mencocokkan password dengan hash. Semua kegagalan (termasuk hash
// rusak dan password terlalu panjang) dianggap tidak cocok.
func (h *hasher) Verify(hash, password string) bool {
	if len(password) > MaxPasswordBytes {
		_ = bcrypt.CompareHashAndPassword(h.dummy, []byte("x"))
		return false
	}
	return bcrypt.CompareHashAndPassword([]byte(hash), []byte(password)) == nil
}

// BurnTime menjalankan satu perbandingan bcrypt tiruan.
func (h *hasher) BurnTime(password string) {
	if len(password) > MaxPasswordBytes {
		password = "x"
	}
	_ = bcrypt.CompareHashAndPassword(h.dummy, []byte(password))
}

// NeedsRehash melaporkan apakah hash dibuat dengan cost lebih rendah daripada
// konfigurasi saat ini.
func (h *hasher) NeedsRehash(hash string) bool {
	c, err := bcrypt.Cost([]byte(hash))
	return err == nil && c < h.cost
}

// ValidatePassword memeriksa kebijakan password. forbidden berisi nilai yang
// tidak boleh dipakai sebagai password (mis. username dan email).
// Mengembalikan nil bila lolos.
func ValidatePassword(password string, forbidden ...string) *httpx.AppError {
	fail := func(msg string) *httpx.AppError {
		e := httpx.ErrValidation("Password tidak memenuhi ketentuan.")
		e.Fields = map[string]string{"password": msg}
		return e
	}
	if utf8.RuneCountInString(password) < MinPasswordLength {
		return fail("Password minimal 8 karakter.")
	}
	if len(password) > MaxPasswordBytes {
		return fail("Password terlalu panjang (maksimal 72 byte).")
	}
	var hasLetter, hasDigit bool
	for _, r := range password {
		switch {
		case unicode.IsLetter(r):
			hasLetter = true
		case unicode.IsDigit(r):
			hasDigit = true
		}
	}
	if !hasLetter || !hasDigit {
		return fail("Password harus memuat huruf dan angka.")
	}
	lower := strings.ToLower(password)
	for _, f := range forbidden {
		f = strings.ToLower(strings.TrimSpace(f))
		if f != "" && lower == f {
			return fail("Password tidak boleh sama dengan username atau email.")
		}
	}
	return nil
}

// GeneratePassword membuat password acak yang lolos ValidatePassword,
// dipakai untuk password sementara akun baru.
func GeneratePassword() (string, error) {
	max := big.NewInt(int64(len(generatedAlphabet)))
	for {
		buf := make([]byte, generatedPasswordLength)
		for i := range buf {
			n, err := rand.Int(rand.Reader, max)
			if err != nil {
				return "", err
			}
			buf[i] = generatedAlphabet[n.Int64()]
		}
		if p := string(buf); ValidatePassword(p) == nil {
			return p, nil
		}
	}
}
