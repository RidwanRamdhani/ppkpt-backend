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

func TestRouter_Route2FATerdaftar(t *testing.T) {
	h := newTestRouter(t, true, nil)
	for _, path := range []string{
		"/api/auth/2fa/setup",
		"/api/auth/2fa/enable",
		"/api/auth/2fa/verify",
		"/api/auth/2fa/disable",
		"/api/auth/2fa/recovery",
	} {
		rec := send(h, http.MethodPost, path, `{}`, testOrigin)
		if rec.Code == http.StatusNotFound || rec.Code == http.StatusMethodNotAllowed {
			t.Errorf("route POST %s tidak terdaftar (status %d)", path, rec.Code)
			continue
		}
		if rec.Code != http.StatusUnauthorized {
			t.Errorf("POST %s tanpa sesi/challenge: %d, mau 401", path, rec.Code)
		}
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

// TestReportRouteTerdaftar memastikan POST /api/reports benar-benar terpasang
// di router. Body kosong harus berhenti di validasi form (422), bukan 404/405.
func TestReportRouteTerdaftar(t *testing.T) {
	router := NewRouter(Dependencies{
		Config: config.Config{AppOrigin: "http://localhost:3000"},
	})

	req := httptest.NewRequest(http.MethodPost, "/api/reports", strings.NewReader(""))
	req.Header.Set("Content-Type", "multipart/form-data; boundary=batas")
	rec := httptest.NewRecorder()

	router.ServeHTTP(rec, req)

	if rec.Code == http.StatusNotFound || rec.Code == http.StatusMethodNotAllowed {
		t.Fatalf("route POST /api/reports tidak terdaftar (status %d)", rec.Code)
	}
	if rec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("status = %d (%s), ingin 422", rec.Code, rec.Body.String())
	}
}

func TestStatusRouteTerdaftar(t *testing.T) {
	router := NewRouter(Dependencies{
		Config: config.Config{AppOrigin: "http://localhost:3000"},
	})

	req := httptest.NewRequest(http.MethodPost, "/api/status", strings.NewReader(""))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	if rec.Code == http.StatusNotFound || rec.Code == http.StatusMethodNotAllowed {
		t.Fatalf("route POST /api/status tidak terdaftar (status %d)", rec.Code)
	}
	if rec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("status = %d (%s), ingin 422", rec.Code, rec.Body.String())
	}
}

func TestCompleteRouteTerdaftar(t *testing.T) {
	router := NewRouter(Dependencies{
		Config: config.Config{AppOrigin: "http://localhost:3000"},
	})

	req := httptest.NewRequest(http.MethodPut, "/api/reports/20481735/complete", strings.NewReader(""))
	req.Header.Set("Content-Type", "multipart/form-data; boundary=batas")
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	if rec.Code == http.StatusNotFound || rec.Code == http.StatusMethodNotAllowed {
		t.Fatalf("route PUT /api/reports/{ticket}/complete tidak terdaftar (status %d)", rec.Code)
	}
	if rec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("status = %d (%s), ingin 422", rec.Code, rec.Body.String())
	}
}

// TestCaseRoutesTerdaftarDanTerlindungi memastikan route kasus terpasang dan
// berhenti di middleware sesi (401), bukan 404/405.
func TestCaseRoutesTerdaftarDanTerlindungi(t *testing.T) {
	h := newTestRouter(t, true, nil)

	routes := []struct {
		method string
		path   string
	}{
		{http.MethodGet, "/api/cases"},
		{http.MethodPost, "/api/cases"},
		{http.MethodGet, "/api/cases/1"},
		{http.MethodGet, "/api/cases/1/history"},
		{http.MethodPost, "/api/cases/1/take"},
		{http.MethodPost, "/api/cases/1/verification"},
		{http.MethodPost, "/api/cases/1/parties"},
		{http.MethodPut, "/api/cases/1/parties/2"},
		{http.MethodPost, "/api/cases/1/review"},
		{http.MethodPost, "/api/cases/1/review/continue"},
		{http.MethodPost, "/api/cases/1/review/close"},
		{http.MethodPost, "/api/cases/1/sessions"},
		{http.MethodPost, "/api/cases/1/evidences"},
		{http.MethodPut, "/api/cases/1/condition"},
		{http.MethodPost, "/api/cases/1/referrals"},
		{http.MethodPut, "/api/cases/1/referrals/2"},
		{http.MethodPost, "/api/cases/1/recovery"},
	}
	for _, route := range routes {
		rec := send(h, route.method, route.path, "", "")
		if rec.Code == http.StatusNotFound || rec.Code == http.StatusMethodNotAllowed {
			t.Errorf("route %s %s tidak terdaftar (status %d)", route.method, route.path, rec.Code)
			continue
		}
		if rec.Code != http.StatusUnauthorized {
			t.Errorf("%s %s tanpa sesi: %d, mau 401", route.method, route.path, rec.Code)
		}
	}
}

func TestSubmitBodyLimit(t *testing.T) {
	cfg := config.Config{MaxUploadBytes: 10 << 20, MaxFilesPerRequest: 10}
	want := int64(10<<20)*10 + (1 << 20)
	if got := submitBodyLimit(cfg); got != want {
		t.Fatalf("submitBodyLimit = %d, ingin %d", got, want)
	}

	// Nilai nol harus jatuh ke default yang aman, bukan 1 MB.
	got := submitBodyLimit(config.Config{})
	if got <= 10<<20 {
		t.Fatalf("submitBodyLimit default = %d, terlalu kecil", got)
	}
}
