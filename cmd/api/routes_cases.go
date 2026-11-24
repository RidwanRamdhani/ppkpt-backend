package main

import (
	"net/http"

	"github.com/RidwanRamdhani/ppkpt-backend/internal/auth"
	"github.com/RidwanRamdhani/ppkpt-backend/internal/cases"
)

// registerCaseRoutes memasang endpoint baca kasus (M04). Semua route memakai
// sesi dan permission; pengecekan PJ serta proyeksi per role dilakukan oleh
// layanan kasus.
func registerCaseRoutes(mux *http.ServeMux, deps Dependencies) {
	if deps.Auth == nil {
		return
	}

	svc := cases.NewService(deps.Pool, deps.Audit, deps.Logger, cases.Options{
		WarnReviewDays: deps.Config.Deadline.WarnReviewDays,
		WarnExamDays:   deps.Config.Deadline.WarnExamDays,
	})
	handler := cases.NewHandler(svc, deps.Logger)

	mux.Handle("GET /api/cases",
		deps.Auth.Protect(auth.PermCaseList, http.HandlerFunc(handler.List)))
	mux.Handle("GET /api/cases/{id}",
		deps.Auth.Protect(auth.PermCaseView, http.HandlerFunc(handler.Detail)))
	mux.Handle("GET /api/cases/{id}/history",
		deps.Auth.Protect(auth.PermCaseView, http.HandlerFunc(handler.History)))
}
