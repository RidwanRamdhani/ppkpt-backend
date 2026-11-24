package main

import (
	"net/http"

	"github.com/RidwanRamdhani/ppkpt-backend/internal/auth"
	"github.com/RidwanRamdhani/ppkpt-backend/internal/review"
)

// registerReviewRoutes memasang endpoint penelaahan dan pemeriksaan (M05).
// Semua route memakai sesi dan permission Satgas; pengecekan PJ dilakukan oleh
// layanan review.
func registerReviewRoutes(mux *http.ServeMux, deps Dependencies) {
	if deps.Auth == nil {
		return
	}

	svc := review.NewService(deps.Pool, deps.Evidence, deps.Audit, deps.Notifier, deps.Logger, review.Options{
		ReviewDueDays: deps.Config.Deadline.ReviewDays,
		ExamDueDays:   deps.Config.Deadline.ExamDays,
	})
	handler := review.NewHandler(svc, deps.Logger, submitBodyLimit(deps.Config))

	mux.Handle("POST /api/cases/{id}/review",
		deps.Auth.Protect(auth.PermCaseReview, http.HandlerFunc(handler.SaveReview)))
	mux.Handle("POST /api/cases/{id}/review/continue",
		deps.Auth.Protect(auth.PermCaseReview, http.HandlerFunc(handler.ContinueReview)))
	mux.Handle("POST /api/cases/{id}/review/close",
		deps.Auth.Protect(auth.PermCaseReview, http.HandlerFunc(handler.CloseReview)))
	mux.Handle("POST /api/cases/{id}/sessions",
		deps.Auth.Protect(auth.PermCaseExamine, http.HandlerFunc(handler.CreateSession)))
	mux.Handle("POST /api/cases/{id}/evidences",
		deps.Auth.Protect(auth.PermCaseExamine, http.HandlerFunc(handler.UploadEvidence)))
}
