// Package ratelimit menyediakan token bucket per IP untuk endpoint sensitif
// Implementasi in-memory ini cukup untuk satu instance
package ratelimit

import (
	"net/http"
	"sync"
	"time"

	"github.com/RidwanRamdhani/ppkpt-backend/internal/platform/httpx"
	"golang.org/x/time/rate"
)

const (
	defaultPerMinute = 60
	sweepInterval    = 5 * time.Minute
	idleAfter        = 10 * time.Minute
)

// Limiter menyimpan satu token bucket per kunci (biasanya alamat IP).
type Limiter struct {
	mu            sync.Mutex
	buckets       map[string]*bucket
	limit         rate.Limit
	burst         int
	idleAfter     time.Duration
	sweepInterval time.Duration
	lastSweep     time.Time
	now           func() time.Time
}

type bucket struct {
	limiter  *rate.Limiter
	lastSeen time.Time
}

// NewPerMinute membuat limiter dengan laju perMenit permintaan dan burst awal.
// Jika burst <= 0, nilainya disamakan dengan perMenit.
func NewPerMinute(perMinute, burst int) *Limiter {
	if perMinute <= 0 {
		perMinute = defaultPerMinute
	}
	if burst <= 0 {
		burst = perMinute
	}

	now := time.Now
	return &Limiter{
		buckets:       make(map[string]*bucket),
		limit:         rate.Limit(float64(perMinute) / 60.0),
		burst:         burst,
		idleAfter:     idleAfter,
		sweepInterval: sweepInterval,
		lastSweep:     now(),
		now:           now,
	}
}

// Allow melaporkan apakah satu permintaan untuk key boleh diproses.
func (l *Limiter) Allow(key string) bool {
	now := l.now()

	l.mu.Lock()
	defer l.mu.Unlock()

	l.sweepLocked(now)

	b, ok := l.buckets[key]
	if !ok {
		b = &bucket{limiter: rate.NewLimiter(l.limit, l.burst)}
		l.buckets[key] = b
	}
	b.lastSeen = now
	return b.limiter.AllowN(now, 1)
}

// Middleware menolak permintaan dengan 429 bila kuota untuk IP habis.
func (l *Limiter) Middleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !l.Allow(ClientKey(r)) {
			w.Header().Set("Retry-After", "60")
			httpx.WriteError(w, httpx.ErrTooMany("Terlalu banyak permintaan. Silakan coba lagi nanti."))
			return
		}
		next.ServeHTTP(w, r)
	})
}

// ClientKey mengambil IP klien dari resolver tepercaya di context, atau
// resolver default bila tidak tersedia.
func ClientKey(r *http.Request) string {
	if resolver := httpx.ResolverFromContext(r.Context()); resolver != nil {
		if ip := resolver.Resolve(r); ip != "" {
			return ip
		}
	}
	if ip := httpx.ClientIP(r); ip != "" {
		return ip
	}
	return "unknown"
}

// sweepLocked membuang bucket yang sudah lama tidak dipakai agar map tidak
// tumbuh tanpa batas. Pemanggil wajib memegang lock.
func (l *Limiter) sweepLocked(now time.Time) {
	if now.Sub(l.lastSweep) < l.sweepInterval {
		return
	}
	l.lastSweep = now
	for key, b := range l.buckets {
		if now.Sub(b.lastSeen) > l.idleAfter {
			delete(l.buckets, key)
		}
	}
}
