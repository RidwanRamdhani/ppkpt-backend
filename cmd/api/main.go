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

	"github.com/RidwanRamdhani/ppkpt-backend/internal/audit"
	"github.com/RidwanRamdhani/ppkpt-backend/internal/auth"
	"github.com/RidwanRamdhani/ppkpt-backend/internal/config"
	"github.com/RidwanRamdhani/ppkpt-backend/internal/evidence"
	"github.com/RidwanRamdhani/ppkpt-backend/internal/notification"
	"github.com/RidwanRamdhani/ppkpt-backend/internal/platform/captcha"
	"github.com/RidwanRamdhani/ppkpt-backend/internal/platform/db"
	"github.com/RidwanRamdhani/ppkpt-backend/internal/platform/mailer"
	"github.com/RidwanRamdhani/ppkpt-backend/internal/platform/ratelimit"
	"github.com/RidwanRamdhani/ppkpt-backend/internal/platform/storage"
	"github.com/RidwanRamdhani/ppkpt-backend/internal/review"
	"github.com/joho/godotenv"
)

// startupTimeout membatasi inisialisasi layanan eksternal (database, Silo) agar
// proses gagal cepat dengan pesan jelas ketika dependensi belum siap.
const startupTimeout = 15 * time.Second

func main() {
	os.Exit(run())
}

// run memisahkan logika dari os.Exit agar seluruh defer (penutupan pool,
// penghentian worker email) tetap berjalan.
func run() int {
	// Memuat konfigurasi dari .env jika tersedia.
	_ = godotenv.Load()

	logger := slog.New(slog.NewJSONHandler(os.Stdout, nil))
	slog.SetDefault(logger)

	cfg, err := config.Load()
	if err != nil {
		logger.Error("konfigurasi tidak valid", "err", err)
		return 1
	}

	logger.Info("memulai aplikasi", "env", cfg.AppEnv, "addr", cfg.HTTPAddr)

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	initCtx, cancelInit := context.WithTimeout(ctx, startupTimeout)
	defer cancelInit()

	pool, err := db.Connect(initCtx, cfg.DatabaseURL)
	if err != nil {
		logger.Error("database tidak siap", "err", err)
		return 1
	}
	defer pool.Close()

	store, err := storage.New(initCtx, storage.Config{
		Endpoint:  cfg.Storage.Endpoint,
		AccessKey: cfg.Storage.AccessKey,
		SecretKey: cfg.Storage.SecretKey,
		Bucket:    cfg.Storage.Bucket,
		UseSSL:    cfg.Storage.UseSSL,
	})
	if err != nil {
		logger.Error("penyimpanan berkas tidak siap", "err", err)
		return 1
	}

	mail, err := mailer.New(mailer.Config{
		Host: cfg.SMTP.Host,
		Port: cfg.SMTP.Port,
		User: cfg.SMTP.User,
		Pass: cfg.SMTP.Pass,
		From: cfg.SMTP.From,
	}, logger)
	if err != nil {
		logger.Error("konfigurasi email tidak valid", "err", err)
		return 1
	}
	mail.Start(ctx)
	// Pastikan worker berhenti sebelum proses keluar, termasuk pada error path.
	defer func() {
		stop()
		mail.Wait()
	}()

	captchaVerifier, err := captcha.New(cfg.CaptchaEnabled, cfg.CaptchaProvider, cfg.CaptchaSecret)
	if err != nil {
		logger.Error("konfigurasi captcha tidak valid", "err", err)
		return 1
	}

	auditor := audit.NewService()

	// Penjadwal tenggat harian (FR-32). Status tenggat tetap dihitung ulang saat
	// data dibaca, jadi scheduler hanya menambah audit dan log.
	scheduler := review.NewScheduler(pool, auditor, logger, review.SchedulerOptions{
		WarnReviewDays: cfg.Deadline.WarnReviewDays,
		WarnExamDays:   cfg.Deadline.WarnExamDays,
	})
	go scheduler.Run(ctx)

	authSvc, err := auth.NewService(pool, auditor, logger, auth.OptionsFromConfig(cfg))
	if err != nil {
		logger.Error("konfigurasi autentikasi tidak valid", "err", err)
		return 1
	}
	// Membersihkan sesi kedaluwarsa secara berkala; berhenti bersama ctx.
	go authSvc.RunCleanup(ctx, 10*time.Minute)

	deps := Dependencies{
		Config: cfg,
		Pool:   pool,
		Logger: logger,
		Audit:  auditor,
		Auth:   authSvc,
		Evidence: evidence.NewService(store, auditor, logger, evidence.Options{
			MaxUploadBytes: cfg.MaxUploadBytes,
			MaxFiles:       cfg.MaxFilesPerRequest,
		}),
		Notifier:      notification.New(mail, logger),
		Captcha:       captchaVerifier,
		PublicLimiter: ratelimit.NewPerMinute(cfg.RateLimit.PublicPerMinute, 0),
		AuthLimiter:   ratelimit.NewPerMinute(cfg.RateLimit.AuthPerMinute, 0),
	}

	srv := &http.Server{
		Addr:              cfg.HTTPAddr,
		Handler:           NewRouter(deps),
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
	return 0
}
