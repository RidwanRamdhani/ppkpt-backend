package main

import (
	"net/http"

	"github.com/RidwanRamdhani/ppkpt-backend/internal/config"
	"github.com/RidwanRamdhani/ppkpt-backend/internal/report"
)

// registerReportRoutes memasang endpoint pelaporan publik (M01). Endpoint ini
// tidak memakai sesi; perlindungannya adalah rate limit, captcha, dan validasi
// ketat di server.
func registerReportRoutes(mux *http.ServeMux, deps Dependencies) {
	svc := report.NewService(
		deps.Pool,
		deps.Evidence,
		deps.Audit,
		deps.Notifier,
		deps.Logger,
		report.Options{BcryptCost: deps.Config.BcryptCost},
	)
	// Interface yang menyimpan pointer nil tetap dianggap non-nil oleh Go,
	// jadi limiter hanya diteruskan bila benar-benar tersedia.
	var limiter report.Limiter
	if deps.PublicLimiter != nil {
		limiter = deps.PublicLimiter
	}

	handler := report.NewHandler(
		svc,
		deps.Captcha,
		limiter,
		deps.Logger,
		submitBodyLimit(deps.Config),
	)
	mux.Handle("POST /api/reports", http.HandlerFunc(handler.Submit))
}

// submitBodyLimit menghitung batas total body unggahan: seluruh berkas maksimum
// plus ruang untuk payload JSON dan overhead multipart. Batas per berkas tetap
// ditegakkan evidence.Service.
func submitBodyLimit(cfg config.Config) int64 {
	maxUpload := cfg.MaxUploadBytes
	if maxUpload <= 0 {
		maxUpload = 10 << 20
	}
	maxFiles := cfg.MaxFilesPerRequest
	if maxFiles <= 0 {
		maxFiles = 10
	}
	return maxUpload*int64(maxFiles) + (1 << 20)
}
