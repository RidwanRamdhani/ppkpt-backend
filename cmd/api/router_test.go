package main

import (
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/RidwanRamdhani/ppkpt-backend/internal/audit"
	"github.com/RidwanRamdhani/ppkpt-backend/internal/auth"
	"github.com/RidwanRamdhani/ppkpt-backend/internal/config"
	"github.com/RidwanRamdhani/ppkpt-backend/internal/platform/ratelimit"
	"golang.org/x/crypto/bcrypt"
)

const testOrigin = "http://localhost:3000"

// newTestRouter merakit router lengkap dengan middleware tanpa database:
// layanan auth dibuat dengan pool nil sehingga hanya jalur yang tidak
// menyentuh DB (validasi, 401 tanpa cookie, CSRF, rate limit) yang diuji di sini.
func newTestRouter(t *testing.T, withAuth bool, limiter *ratelimit.Limiter) http.Handler {
	t.Helper()
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	deps := Dependencies{
		Config:      config.Config{AppOrigin: testOrigin, MaxUploadBytes: 1 << 20},
		Logger:      logger,
		AuthLimiter: limiter,
	}
	if withAuth {
		svc, err := auth.NewService(nil, audit.NewService(), logger, auth.Options{BcryptCost: bcrypt.MinCost})
		if err != nil {
			t.Fatal(err)
		}
		deps.Auth = svc
	}
	return NewRouter(deps)
}

func send(h http.Handler, method, path, body, origin string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	if origin != "" {
		req.Header.Set("Origin", origin)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

func TestRouter_RouteAutentikasiTerdaftar(t *testing.T) {
	h := newTestRouter(t, true, nil)

	if rec := send(h, http.MethodGet, "/api/auth/me", "", ""); rec.Code != http.StatusUnauthorized {
		t.Fatalf("GET /api/auth/me tanpa sesi: %d, mau 401", rec.Code)
	}
	if rec := send(h, http.MethodPut, "/api/auth/password", `{}`, testOrigin); rec.Code != http.StatusUnauthorized {
		t.Fatalf("PUT /api/auth/password tanpa sesi: %d, mau 401", rec.Code)
	}
	if rec := send(h, http.MethodPost, "/api/auth/logout", "", testOrigin); rec.Code != http.StatusNoContent {
		t.Fatalf("POST /api/auth/logout: %d, mau 204 (idempoten)", rec.Code)
	}

	rec := send(h, http.MethodPost, "/api/auth/login", `{}`, testOrigin)
	if rec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("login kosong: %d, mau 422", rec.Code)
	}
	for _, want := range []string{"identifier", "password"} {
		if !strings.Contains(rec.Body.String(), want) {
			t.Errorf("body 422 tanpa field %q: %s", want, rec.Body.String())
		}
	}
	if rec := send(h, http.MethodPost, "/api/auth/login", `{"identifier":"a","password":"b","admin":true}`, testOrigin); rec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("field tak dikenal: %d, mau 422", rec.Code)
	}
	if rec := send(h, http.MethodPost, "/api/auth/login", `bukan json`, testOrigin); rec.Code != http.StatusUnprocessableEntity && rec.Code != http.StatusBadRequest {
		t.Fatalf("JSON rusak: %d", rec.Code)
	}
	if rec := send(h, http.MethodGet, "/api/auth/login", "", ""); rec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("GET /api/auth/login: %d, mau 405", rec.Code)
	}
}

func TestRouter_CSRFMenolakOriginAsing(t *testing.T) {
	h := newTestRouter(t, true, nil)
	rec := send(h, http.MethodPost, "/api/auth/login", `{"identifier":"a","password":"b"}`, "https://jahat.example")
	if rec.Code != http.StatusForbidden {
		t.Fatalf("Origin asing: %d, mau 403", rec.Code)
	}
}

func TestRouter_HeaderKeamananDanRequestID(t *testing.T) {
	h := newTestRouter(t, true, nil)
	rec := send(h, http.MethodGet, "/api/auth/me", "", "")
	if rec.Header().Get("X-Content-Type-Options") != "nosniff" {
		t.Error("X-Content-Type-Options: nosniff tidak ada")
	}
	if rec.Header().Get("X-Request-Id") == "" && rec.Header().Get("X-Request-ID") == "" {
		t.Error("X-Request-ID tidak ada")
	}
}

func TestRouter_LoginDibatasiAuthLimiter(t *testing.T) {
	h := newTestRouter(t, true, ratelimit.NewPerMinute(2, 2))
	for i := 0; i < 2; i++ {
		if rec := send(h, http.MethodPost, "/api/auth/login", `{}`, testOrigin); rec.Code != http.StatusUnprocessableEntity {
			t.Fatalf("percobaan %d: %d, mau 422", i+1, rec.Code)
		}
	}
	rec := send(h, http.MethodPost, "/api/auth/login", `{}`, testOrigin)
	if rec.Code != http.StatusTooManyRequests {
		t.Fatalf("percobaan ke-3: %d, mau 429", rec.Code)
	}
}

func TestRouter_TanpaLayananAuthRouteTidakAda(t *testing.T) {
	h := newTestRouter(t, false, nil)
	if rec := send(h, http.MethodPost, "/api/auth/login", `{}`, testOrigin); rec.Code != http.StatusNotFound {
		t.Fatalf("tanpa Auth: %d, mau 404", rec.Code)
	}
}
