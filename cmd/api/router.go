package main

import (
	"context"
	"log/slog"
	"net/http"
	"time"

	"github.com/RidwanRamdhani/ppkpt-backend/internal/config"
	"github.com/RidwanRamdhani/ppkpt-backend/internal/platform/httpx"
	"github.com/jackc/pgx/v5/pgxpool"
)

// NewRouter membangun http.Handler dengan seluruh middleware (urutan luar->dalam)
// dan route dasar. Route fitur (report/cases/auth) ditambahkan pada Phase berikutnya.
func NewRouter(cfg config.Config, pool *pgxpool.Pool, logger *slog.Logger) http.Handler {
	resolver := httpx.NewClientIPResolver(cfg.TrustedProxyCIDRs)
	isProd := cfg.AppEnv == "production"

	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/health", healthHandler(pool))

	withResolver := func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			next.ServeHTTP(w, r.WithContext(httpx.WithClientIPResolver(r.Context(), resolver)))
		})
	}

	chain := []func(http.Handler) http.Handler{
		httpx.RequestID,
		httpx.Recover(logger),
		httpx.SecurityHeaders(isProd),
		httpx.AccessLog(logger),
		httpx.BodyLimit(cfg.MaxUploadBytes),
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
