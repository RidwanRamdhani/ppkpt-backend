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

	svc := cases.NewService(deps.Pool, deps.Evidence, deps.Audit, deps.Notifier, deps.Logger, cases.Options{
		WarnReviewDays: deps.Config.Deadline.WarnReviewDays,
		WarnExamDays:   deps.Config.Deadline.WarnExamDays,
		ReviewDueDays:  deps.Config.Deadline.ReviewDays,
		ExamDueDays:    deps.Config.Deadline.ExamDays,
		BcryptCost:     deps.Config.BcryptCost,
	})
	handler := cases.NewHandler(svc, deps.Logger, submitBodyLimit(deps.Config))

	mux.Handle("GET /api/cases",
		deps.Auth.Protect(auth.PermCaseList, http.HandlerFunc(handler.List)))
	mux.Handle("POST /api/cases",
		deps.Auth.Protect(auth.PermManualReportCreate, http.HandlerFunc(handler.CreateManual)))
	mux.Handle("GET /api/cases/{id}",
		deps.Auth.Protect(auth.PermCaseView, http.HandlerFunc(handler.Detail)))
	mux.Handle("GET /api/cases/{id}/history",
		deps.Auth.Protect(auth.PermCaseView, http.HandlerFunc(handler.History)))
	mux.Handle("POST /api/cases/{id}/take",
		deps.Auth.Protect(auth.PermCaseTake, http.HandlerFunc(handler.Take)))
	mux.Handle("POST /api/cases/{id}/verification",
		deps.Auth.Protect(auth.PermCaseVerify, http.HandlerFunc(handler.Verify)))
	mux.Handle("POST /api/cases/{id}/parties",
		deps.Auth.Protect(auth.PermPartyWrite, http.HandlerFunc(handler.AddParty)))
	mux.Handle("PUT /api/cases/{id}/parties/{partyId}",
		deps.Auth.Protect(auth.PermPartyWrite, http.HandlerFunc(handler.UpdateParty)))
}
