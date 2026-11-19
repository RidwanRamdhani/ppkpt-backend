package ratelimit

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestAllow_MenerapkanBurstLaluMenolak(t *testing.T) {
	l := NewPerMinute(60, 2)

	first := l.Allow("203.0.113.1")
	second := l.Allow("203.0.113.1")
	if !first || !second {
		t.Fatal("dua permintaan pertama harus lolos")
	}
	if l.Allow("203.0.113.1") {
		t.Fatal("permintaan ketiga harus ditolak")
	}
	if !l.Allow("203.0.113.2") {
		t.Fatal("IP lain harus punya kuota sendiri")
	}
}

func TestAllow_MengisiKembaliSeiringWaktu(t *testing.T) {
	now := time.Unix(0, 0)
	l := NewPerMinute(60, 1) // satu token per detik
	l.now = func() time.Time { return now }

	if !l.Allow("ip") {
		t.Fatal("permintaan pertama harus lolos")
	}
	if l.Allow("ip") {
		t.Fatal("permintaan kedua harus ditolak")
	}

	now = now.Add(time.Second)
	if !l.Allow("ip") {
		t.Fatal("setelah satu detik token harus terisi kembali")
	}
}

func TestSweep_MembuangBucketIdle(t *testing.T) {
	now := time.Unix(0, 0)
	l := NewPerMinute(60, 1)
	l.now = func() time.Time { return now }
	l.idleAfter = time.Minute
	l.sweepInterval = 0 // selalu sapu saat Allow dipanggil
	l.lastSweep = now

	l.Allow("lama")

	now = now.Add(2 * time.Minute)
	l.Allow("baru")

	l.mu.Lock()
	_, ada := l.buckets["lama"]
	l.mu.Unlock()
	if ada {
		t.Fatal("bucket idle seharusnya sudah dibuang")
	}
}

func TestMiddleware_MenolakDengan429(t *testing.T) {
	l := NewPerMinute(60, 1)
	h := l.Middleware(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	req := httptest.NewRequest(http.MethodPost, "/api/auth/login", nil)
	req.RemoteAddr = "203.0.113.1:1234"
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("permintaan pertama: status = %d, mau 200", rec.Code)
	}

	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusTooManyRequests {
		t.Fatalf("permintaan kedua: status = %d, mau 429", rec.Code)
	}
	if rec.Header().Get("Retry-After") == "" {
		t.Error("header Retry-After tidak ada")
	}
}
