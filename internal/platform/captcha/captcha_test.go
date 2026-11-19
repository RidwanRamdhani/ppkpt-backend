package captcha

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestNew_TidakValidSaatAktifTanpaSecret(t *testing.T) {
	if _, err := New(true, "turnstile", ""); err == nil {
		t.Fatal("secret kosong harus ditolak saat captcha aktif")
	}
}

func TestNew_MenolakProviderTidakDikenal(t *testing.T) {
	if _, err := New(true, "provider-antah", "secret"); err == nil {
		t.Fatal("provider tidak dikenal harus ditolak")
	}
}

func TestVerify_SelaluLolosSaatNonaktif(t *testing.T) {
	v, err := New(false, "turnstile", "")
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if err := v.Verify(context.Background(), "", ""); err != nil {
		t.Fatalf("captcha nonaktif harus lolos: %v", err)
	}
}

func TestVerify_MenolakTokenKosong(t *testing.T) {
	v, err := New(true, "turnstile", "secret")
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if err := v.Verify(context.Background(), "  ", "127.0.0.1"); !errors.Is(err, ErrFailed) {
		t.Fatalf("err = %v, mau ErrFailed", err)
	}
}

func TestVerify_MengirimFormDanMenerimaSukses(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := r.ParseForm(); err != nil {
			t.Errorf("parse form: %v", err)
		}
		if got := r.Form.Get("secret"); got != "secret" {
			t.Errorf("secret = %q", got)
		}
		if got := r.Form.Get("response"); got != "token-valid" {
			t.Errorf("response = %q", got)
		}
		if got := r.Form.Get("remoteip"); got != "203.0.113.1" {
			t.Errorf("remoteip = %q", got)
		}
		_, _ = w.Write([]byte(`{"success":true}`))
	}))
	defer srv.Close()

	v := newTestVerifier(t, srv.URL)
	if err := v.Verify(context.Background(), "token-valid", "203.0.113.1"); err != nil {
		t.Fatalf("Verify: %v", err)
	}
}

func TestVerify_MenolakTokenTidakValid(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"success":false,"error-codes":["invalid-input-response"]}`))
	}))
	defer srv.Close()

	v := newTestVerifier(t, srv.URL)
	if err := v.Verify(context.Background(), "token-salah", ""); !errors.Is(err, ErrFailed) {
		t.Fatalf("err = %v, mau ErrFailed", err)
	}
}

func TestVerify_MenolakResponsBukan200(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv.Close()

	v := newTestVerifier(t, srv.URL)
	if err := v.Verify(context.Background(), "token", ""); !errors.Is(err, ErrFailed) {
		t.Fatalf("err = %v, mau ErrFailed", err)
	}
}

func newTestVerifier(t *testing.T, endpoint string) *Verifier {
	t.Helper()
	v, err := New(true, "turnstile", "secret")
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	v.endpoint = endpoint
	return v
}
