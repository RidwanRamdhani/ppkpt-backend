// Command api menjalankan server HTTP backend PPKPT dengan graceful shutdown.
package main

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/RidwanRamdhani/ppkpt-backend/internal/config"
	"github.com/RidwanRamdhani/ppkpt-backend/internal/platform/db"
	"github.com/joho/godotenv"
)

func main() {
	// Memuat konfigurasi dari .env jika tersedia.
	_ = godotenv.Load()

	cfg := config.MustLoad()

	logger := slog.New(slog.NewJSONHandler(os.Stdout, nil))
	slog.SetDefault(logger)

	logger.Info("memulai aplikasi", "env", cfg.AppEnv, "addr", cfg.HTTPAddr)

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	pool := db.MustConnect(ctx, cfg.DatabaseURL)
	defer pool.Close()

	srv := &http.Server{
		Addr:              cfg.HTTPAddr,
		Handler:           NewRouter(cfg, pool, logger),
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       60 * time.Second, // unggah berkas
		WriteTimeout:      60 * time.Second,
		IdleTimeout:       120 * time.Second,
	}

	go func() {
		logger.Info("server mendengarkan", "addr", cfg.HTTPAddr)
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			logger.Error("server gagal", "err", err)
			stop()
		}
	}()

	<-ctx.Done()
	logger.Info("mematikan server...")

	shCtx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	if err := srv.Shutdown(shCtx); err != nil {
		logger.Error("shutdown tidak bersih", "err", err)
	}
	logger.Info("server berhenti")
}
