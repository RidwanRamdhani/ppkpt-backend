package httpx

import (
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestWriteError_JSONShape(t *testing.T) {
	rec := httptest.NewRecorder()
	WriteError(rec, ErrValidation("contoh galat"))

	if rec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("status = %d, mau 422", rec.Code)
	}
	if ct := rec.Header().Get("Content-Type"); !strings.Contains(ct, "application/json") {
		t.Fatalf("content-type = %q", ct)
	}

	var body struct {
		Error struct {
			Code    string `json:"code"`
			Message string `json:"message"`
		} `json:"error"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("body bukan JSON valid: %v", err)
	}
	if body.Error.Code != CodeValidation {
		t.Fatalf("code = %q, mau %q", body.Error.Code, CodeValidation)
	}
	if body.Error.Message == "" {
		t.Fatal("message kosong")
	}
}

func TestRecover_PanicJadi500(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(&strings.Builder{}, nil))
	h := Recover(logger)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		panic("boom")
	}))

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/", nil))

	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, mau 500", rec.Code)
	}
	if strings.Contains(rec.Body.String(), "boom") {
		t.Fatalf("detail panic bocor ke klien: %q", rec.Body.String())
	}
}

func TestSecurityHeaders(t *testing.T) {
	h := SecurityHeaders(false)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/", nil))

	if got := rec.Header().Get("X-Content-Type-Options"); got != "nosniff" {
		t.Errorf("X-Content-Type-Options = %q", got)
	}
	if got := rec.Header().Get("Cache-Control"); got != "no-store" {
		t.Errorf("Cache-Control = %q", got)
	}
}

func TestCSRF_OppositeOriginDitolak(t *testing.T) {
	h := CSRF("http://localhost:3000")(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	// Origin berbeda -> 403.
	req := httptest.NewRequest(http.MethodPost, "/api/x", nil)
	req.Header.Set("Origin", "http://evil.example")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("origin asing: status = %d, mau 403", rec.Code)
	}

	// Origin sama -> lolos.
	req = httptest.NewRequest(http.MethodPost, "/api/x", nil)
	req.Header.Set("Origin", "http://localhost:3000")
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("origin sama: status = %d, mau 200", rec.Code)
	}

	// GET tanpa Origin -> lolos.
	req = httptest.NewRequest(http.MethodGet, "/api/x", nil)
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("GET: status = %d, mau 200", rec.Code)
	}
}

func TestClientIP_TanpaProxyTepercaya(t *testing.T) {
	r := NewClientIPResolver(nil)
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.RemoteAddr = "203.0.113.1:4444"
	req.Header.Set("X-Forwarded-For", "1.2.3.4")

	if got := r.Resolve(req); got != "203.0.113.1" {
		t.Fatalf("ip = %q, mau 203.0.113.1 (XFF harus diabaikan)", got)
	}
}

func TestClientIP_DenganProxyTepercaya(t *testing.T) {
	r := NewClientIPResolver([]string{"10.0.0.0/8"})
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.RemoteAddr = "10.1.2.3:5555"
	req.Header.Set("X-Forwarded-For", "1.2.3.4, 10.9.9.9")

	if got := r.Resolve(req); got != "1.2.3.4" {
		t.Fatalf("ip = %q, mau 1.2.3.4", got)
	}
}

func TestChain_Order(t *testing.T) {
	var order []string
	mw := func(name string) func(http.Handler) http.Handler {
		return func(next http.Handler) http.Handler {
			return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				order = append(order, name)
				next.ServeHTTP(w, r)
			})
		}
	}
	h := Chain(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		order = append(order, "handler")
	}), mw("a"), mw("b"))

	h.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/", nil))
	want := []string{"a", "b", "handler"}
	for i := range want {
		if order[i] != want[i] {
			t.Fatalf("order = %v, mau %v", order, want)
		}
	}
}

func TestReadJSON_UnknownFieldDitolak(t *testing.T) {
	req := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(`{"a":1,"bogus":2}`))
	var dst struct {
		A int `json:"a"`
	}
	err := ReadJSON(httptest.NewRecorder(), req, &dst)
	if err == nil {
		t.Fatal("mau error untuk field tak dikenal")
	}
}
