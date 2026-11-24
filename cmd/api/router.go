package main

import (
	"context"
	"log/slog"
	"net/http"
	"time"

	"github.com/RidwanRamdhani/ppkpt-backend/internal/audit"
	"github.com/RidwanRamdhani/ppkpt-backend/internal/auth"
	"github.com/RidwanRamdhani/ppkpt-backend/internal/config"
	"github.com/RidwanRamdhani/ppkpt-backend/internal/evidence"
	"github.com/RidwanRamdhani/ppkpt-backend/internal/notification"
	"github.com/RidwanRamdhani/ppkpt-backend/internal/platform/captcha"
	"github.com/RidwanRamdhani/ppkpt-backend/internal/platform/httpx"
	"github.com/RidwanRamdhani/ppkpt-backend/internal/platform/ratelimit"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Dependencies mengelompokkan layanan yang dipakai handler. Menyusunnya di satu
// tempat membuat komposisi aplikasi eksplisit dan memudahkan pengujian.
type Dependencies struct {
	Config        config.Config
	Pool          *pgxpool.Pool
	Logger        *slog.Logger
	Audit         *audit.Service
	Auth          *auth.Service
	Evidence      *evidence.Service
	Notifier      *notification.Service
	Captcha       *captcha.Verifier
	PublicLimiter *ratelimit.Limiter
	AuthLimiter   *ratelimit.Limiter
}

// NewRouter membangun http.Handler dengan seluruh middleware (urutan luar->dalam),
// route dasar, route autentikasi, dan route pelaporan publik. Route fitur kasus
// ditambahkan pada Phase berikutnya dan memakai layanan dari Dependencies.
func NewRouter(deps Dependencies) http.Handler {
	cfg := deps.Config
	logger := deps.Logger
	if logger == nil {
		logger = slog.Default()
	}

	resolver := httpx.NewClientIPResolver(cfg.TrustedProxyCIDRs)
	isProd := cfg.AppEnv == "production" || cfg.AppEnv == "prod"

	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/health", healthHandler(deps.Pool))
	registerReportRoutes(mux, deps)
	registerCaseRoutes(mux, deps)
	registerReviewRoutes(mux, deps)

	// Autentikasi: login, logout, ganti password, dan /me. Login dan ganti
	// password dibatasi AuthLimiter (per IP). Route fitur lain memakai
	// deps.Auth.Protect(permission, handler) agar sesi dan hak akses diperiksa.
	if deps.Auth != nil {
		var authLimit func(http.Handler) http.Handler
		if deps.AuthLimiter != nil {
			authLimit = deps.AuthLimiter.Middleware
		}
		auth.NewHandler(deps.Auth).Register(mux, authLimit)
	}

	withResolver := func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			next.ServeHTTP(w, r.WithContext(httpx.WithClientIPResolver(r.Context(), resolver)))
		})
	}

	// Batas ukuran body sengaja tidak dipasang global: tiap route menegakkan
	// batasnya sendiri. Route JSON memakai httpx.ReadJSON (1 MiB), sedangkan
	// route multipart pelaporan memakai MaxBytesReader sebesar submitBodyLimit
	// (total semua berkas) karena jauh lebih besar dari batas per berkas.
	chain := []func(http.Handler) http.Handler{
		httpx.RequestID,
		httpx.Recover(logger),
		httpx.SecurityHeaders(isProd),
		httpx.AccessLog(logger),
		httpx.CSRF(cfg.AppOrigin),
		withResolver,
	}

	return httpx.Chain(mux, chain...)
}

// healthHandler memeriksa koneksi database (E1).
func healthHandler(pool *pgxpool.Pool) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		ctx, cancel := context.WithTimeout(r.Context(), 3*time.Second)
		defer cancel()

		if err := pool.Ping(ctx); err != nil {
			httpx.WriteJSON(w, http.StatusServiceUnavailable, map[string]string{
				"status": "unhealthy",
				"db":     "down",
			})
			return
		}
		httpx.WriteJSON(w, http.StatusOK, map[string]string{
			"status": "ok",
			"db":     "up",
		})
	}
}
