package main

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/RidwanRamdhani/ppkpt-backend/internal/config"
)

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
