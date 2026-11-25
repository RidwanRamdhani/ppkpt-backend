package auth

import (
	"context"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/pquerna/otp"
	"github.com/pquerna/otp/totp"
	"golang.org/x/crypto/bcrypt"
)

func testMFAKey() []byte {
	key := make([]byte, 32)
	for i := range key {
		key[i] = byte(i + 1)
	}
	return key
}

func TestMFAEncryptionRoundTrip(t *testing.T) {
	svc, err := NewService(nil, nil, nil, Options{
		BcryptCost: bcrypt.MinCost, MFAEnabled: true, MFAEncryptionKey: testMFAKey(),
	})
	if err != nil {
		t.Fatal(err)
	}
	secret := "JBSWY3DPEHPK3PXP" //nolint:gosec // secret uji, bukan rahasia
	enc, err := svc.encryptSecret(secret)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(enc, secret) {
		t.Fatal("secret TOTP tidak boleh tersimpan plaintext")
	}
	enc2, _ := svc.encryptSecret(secret)
	if enc == enc2 {
		t.Fatal("nonce harus acak sehingga ciphertext berbeda")
	}
	got, err := svc.decryptSecret(enc)
	if err != nil || got != secret {
		t.Fatalf("decrypt = %q, %v", got, err)
	}
	if _, err := svc.decryptSecret("bukan-base64"); err == nil {
		t.Fatal("ciphertext rusak harus ditolak")
	}

	other, _ := NewService(nil, nil, nil, Options{
		BcryptCost: bcrypt.MinCost, MFAEnabled: true,
		MFAEncryptionKey: bytesRepeat(7, 32),
	})
	if _, err := other.decryptSecret(enc); err == nil {
		t.Fatal("kunci berbeda harus gagal mendekripsi")
	}
}

func bytesRepeat(b byte, n int) []byte {
	out := make([]byte, n)
	for i := range out {
		out[i] = b
	}
	return out
}

func TestMFAKeyWajibSaatAktif(t *testing.T) {
	if _, err := NewService(nil, nil, nil, Options{MFAEnabled: true}); err == nil {
		t.Fatal("MFA_ENABLED tanpa kunci harus ditolak")
	}
	if _, err := NewService(nil, nil, nil, Options{MFAEnabled: true, MFAEncryptionKey: []byte("pendek")}); err == nil {
		t.Fatal("kunci bukan 32 byte harus ditolak")
	}
}

func TestRecoveryCode(t *testing.T) {
	seen := map[string]bool{}
	for i := 0; i < 50; i++ {
		code, err := generateRecoveryCode()
		if err != nil {
			t.Fatal(err)
		}
		if len(code) != 19 || strings.Count(code, "-") != 3 {
			t.Fatalf("format recovery code salah: %q", code)
		}
		if seen[code] {
			t.Fatalf("recovery code berulang: %q", code)
		}
		seen[code] = true

		// Pengguna boleh mengetik tanpa tanda hubung atau huruf kecil.
		lain := strings.ToLower(strings.ReplaceAll(code, "-", " "))
		if hashRecoveryCode(code) != hashRecoveryCode(lain) {
			t.Fatal("kanonikalisasi recovery code gagal")
		}
		if len(hashRecoveryCode(code)) != 64 {
			t.Fatal("hash recovery code harus SHA-256 hex 64 karakter")
		}
	}
}

func TestMatchTOTPWindowDanAntiReplay(t *testing.T) {
	key, err := totp.Generate(totp.GenerateOpts{
		Issuer: "PPKPT", AccountName: "uji@example.test", Period: totpPeriodSeconds,
	})
	if err != nil {
		t.Fatal(err)
	}
	secret := key.Secret()
	now := time.Date(2026, 11, 25, 10, 0, 0, 0, time.UTC)
	code, err := totp.GenerateCodeCustom(secret, now, totp.ValidateOpts{
		Period: totpPeriodSeconds, Digits: otp.DigitsSix, Algorithm: otp.AlgorithmSHA1,
	})
	if err != nil {
		t.Fatal(err)
	}

	step, ok, err := matchTOTP(secret, code, now, 0)
	if err != nil || !ok {
		t.Fatalf("kode sekarang harus diterima: step=%d ok=%v err=%v", step, ok, err)
	}
	if step != uint64(now.Unix()/totpPeriodSeconds) { //nolint:gosec // waktu uji selalu positif
		t.Fatalf("step = %d", step)
	}
	// Kode yang sama tidak boleh dipakai ulang.
	if _, ok, _ := matchTOTP(secret, code, now, step); ok {
		t.Fatal("anti-replay gagal: kode yang sudah dipakai diterima")
	}
	// Kode langkah berikutnya boleh (dalam window), dan kode langkah lama
	// setelah step terbaru tetap ditolak.
	next := now.Add(totpPeriodSeconds * time.Second)
	nextCode, _ := totp.GenerateCodeCustom(secret, next, totp.ValidateOpts{
		Period: totpPeriodSeconds, Digits: otp.DigitsSix, Algorithm: otp.AlgorithmSHA1,
	})
	if _, ok, _ := matchTOTP(secret, nextCode, now, step); !ok {
		t.Fatal("kode langkah +1 harus diterima (skew 1)")
	}
	if _, ok, _ := matchTOTP(secret, code, now, step+1); ok {
		t.Fatal("kode langkah lama harus ditolak")
	}
	// Kode di luar window ditolak.
	far, _ := totp.GenerateCodeCustom(secret, now.Add(5*time.Minute), totp.ValidateOpts{
		Period: totpPeriodSeconds, Digits: otp.DigitsSix, Algorithm: otp.AlgorithmSHA1,
	})
	if _, ok, _ := matchTOTP(secret, far, now, 0); ok {
		t.Fatal("kode di luar window harus ditolak")
	}
	if _, ok, _ := matchTOTP(secret, "12345", now, 0); ok {
		t.Fatal("kode bukan 6 digit harus ditolak")
	}
}

func TestMFACookieAttributes(t *testing.T) {
	s, _ := NewService(nil, nil, nil, Options{BcryptCost: bcrypt.MinCost, CookieSecure: true})
	c := s.mfaCookie("abc", 5*time.Minute)
	if c.Name != MFACookieName || c.Path != "/api/auth" || !c.HttpOnly || !c.Secure ||
		c.SameSite != http.SameSiteLaxMode || c.MaxAge != 300 {
		t.Fatalf("atribut cookie 2FA salah: %+v", c)
	}
	cc := s.clearMFACookie()
	if cc.MaxAge >= 0 || cc.Value != "" || cc.Path != "/api/auth" {
		t.Fatalf("cookie penghapus 2FA salah: %+v", cc)
	}
}

func TestMFADisabledMenolakEndpoint(t *testing.T) {
	s := newUnitService(t)
	if _, err := s.MFASetup(context.Background(), "token"); err == nil {
		t.Fatal("setup saat MFA nonaktif harus ditolak")
	}
	if _, err := s.MFAVerify(context.Background(), MFAVerifyInput{}); err == nil {
		t.Fatal("verify saat MFA nonaktif harus ditolak")
	}
}

func TestMFAOptionsDefaults(t *testing.T) {
	o := Options{MFAEnabled: true}.withDefaults()
	if o.MFAIssuer != "PPKPT" || o.MFAChallengeTTL != 5*time.Minute ||
		o.MFAMaxAttempts != 5 || o.MFARecoveryCodes != 10 {
		t.Fatalf("default 2FA salah: %+v", o)
	}
}
