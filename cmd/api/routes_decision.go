package main

import (
	"net/http"

	"github.com/RidwanRamdhani/ppkpt-backend/internal/auth"
	"github.com/RidwanRamdhani/ppkpt-backend/internal/decision"
)

// registerDecisionRoutes memasang endpoint kesimpulan, keputusan, penyampaian
// hasil, penutupan, dan unduhan berkas (M07). Pengecekan PJ dan tahap kasus
// dilakukan oleh layanan decision.
func registerDecisionRoutes(mux *http.ServeMux, deps Dependencies) {
	if deps.Auth == nil {
		return
	}

	svc := decision.NewService(deps.Pool, deps.Evidence, deps.Audit, deps.Notifier, deps.Logger)
	handler := decision.NewHandler(svc, deps.Logger)

	mux.Handle("POST /api/cases/{id}/conclusion",
		deps.Auth.Protect(auth.PermConclusionWrite, http.HandlerFunc(handler.SaveConclusion)))
	mux.Handle("POST /api/cases/{id}/conclusion/submit",
		deps.Auth.Protect(auth.PermConclusionSubmit, http.HandlerFunc(handler.SubmitConclusion)))
	mux.Handle("GET /api/cases/{id}/decision-view",
		deps.Auth.Protect(auth.PermDecisionView, http.HandlerFunc(handler.ViewDecision)))
	mux.Handle("POST /api/cases/{id}/decision",
		deps.Auth.Protect(auth.PermDecisionRecord, http.HandlerFunc(handler.RecordDecision)))
	mux.Handle("POST /api/cases/{id}/delivery",
		deps.Auth.Protect(auth.PermDeliveryRecord, http.HandlerFunc(handler.RecordDelivery)))
	mux.Handle("POST /api/cases/{id}/close",
		deps.Auth.Protect(auth.PermCaseClose, http.HandlerFunc(handler.CloseCase)))
	mux.Handle("GET /api/evidences/{id}/download",
		deps.Auth.Protect(auth.PermEvidenceDownload, http.HandlerFunc(handler.DownloadEvidence)))
}
