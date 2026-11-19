// Package captcha memverifikasi token captcha di sisi server.
//
// Saat CAPTCHA_ENABLED=false (hanya untuk dev/test) verifikasi selalu lolos.
// Di produksi flag tersebut wajib true dan secret harus diisi.
package captcha

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// ErrFailed menandai token captcha tidak valid. Handler memetakannya menjadi
// respons 429 yang netral.
var ErrFailed = errors.New("verifikasi captcha gagal")

const (
	verifyTimeout  = 5 * time.Second
	maxResponseLen = 1 << 20 // 1 MiB
)

// endpoint per penyedia captcha.
var endpoints = map[string]string{
	"turnstile": "https://challenges.cloudflare.com/turnstile/v0/siteverify",
	"hcaptcha":  "https://api.hcaptcha.com/siteverify",
	"recaptcha": "https://www.google.com/recaptcha/api/siteverify",
}

// Verifier memverifikasi token captcha melalui HTTP POST ke penyedia.
type Verifier struct {
	enabled  bool
	endpoint string
	secret   string
	client   *http.Client
}

// New membuat verifier. Bila captcha diaktifkan, provider harus dikenal dan
// secret wajib diisi agar kesalahan konfigurasi terdeteksi saat start.
func New(enabled bool, provider, secret string) (*Verifier, error) {
	if !enabled {
		return &Verifier{enabled: false}, nil
	}

	provider = strings.ToLower(strings.TrimSpace(provider))
	if provider == "" {
		provider = "turnstile"
	}
	endpoint, ok := endpoints[provider]
	if !ok {
		return nil, fmt.Errorf("captcha: provider %q tidak dikenal", provider)
	}
	if strings.TrimSpace(secret) == "" {
		return nil, errors.New("captcha: CAPTCHA_SECRET wajib diisi saat captcha aktif")
	}

	return &Verifier{
		enabled:  true,
		endpoint: endpoint,
		secret:   secret,
		client:   &http.Client{Timeout: verifyTimeout},
	}, nil
}

// Enabled melaporkan apakah verifikasi captcha aktif.
func (v *Verifier) Enabled() bool {
	return v != nil && v.enabled
}

// Verify memeriksa token captcha. ip bersifat opsional dan diteruskan sebagai
// remoteip ke penyedia. Semua kegagalan dibungkus ErrFailed agar handler dapat
// memberi respons yang seragam.
func (v *Verifier) Verify(ctx context.Context, token, ip string) error {
	if !v.Enabled() {
		return nil
	}

	token = strings.TrimSpace(token)
	if token == "" {
		return ErrFailed
	}

	form := url.Values{}
	form.Set("secret", v.secret)
	form.Set("response", token)
	if ip = strings.TrimSpace(ip); ip != "" {
		form.Set("remoteip", ip)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, v.endpoint,
		strings.NewReader(form.Encode()))
	if err != nil {
		return fmt.Errorf("%w: susun permintaan: %v", ErrFailed, err)
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")

	resp, err := v.client.Do(req)
	if err != nil {
		return fmt.Errorf("%w: hubungi penyedia: %v", ErrFailed, err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("%w: penyedia membalas status %d", ErrFailed, resp.StatusCode)
	}

	var result struct {
		Success    bool     `json:"success"`
		ErrorCodes []string `json:"error-codes"`
	}
	dec := json.NewDecoder(io.LimitReader(resp.Body, maxResponseLen))
	if err := dec.Decode(&result); err != nil {
		return fmt.Errorf("%w: respons tidak valid: %v", ErrFailed, err)
	}
	if !result.Success {
		return fmt.Errorf("%w: %s", ErrFailed, strings.Join(result.ErrorCodes, ", "))
	}
	return nil
}
