package main

import (
	"net/http"

	"github.com/RidwanRamdhani/ppkpt-backend/internal/auth"
	"github.com/RidwanRamdhani/ppkpt-backend/internal/referral"
)

// registerReferralRoutes memasang endpoint kondisi, rujukan, dan pemulihan
// (M06). Semua route memakai sesi dan permission Satgas; pengecekan PJ
// dilakukan oleh layanan referral.
func registerReferralRoutes(mux *http.ServeMux, deps Dependencies) {
	if deps.Auth == nil {
		return
	}

	svc := referral.NewService(deps.Pool, deps.Audit, deps.Logger)
	handler := referral.NewHandler(svc, deps.Logger)

	mux.Handle("PUT /api/cases/{id}/condition",
		deps.Auth.Protect(auth.PermCaseCondition, http.HandlerFunc(handler.SetCondition)))
	mux.Handle("POST /api/cases/{id}/referrals",
		deps.Auth.Protect(auth.PermCaseReferral, http.HandlerFunc(handler.CreateReferral)))
	mux.Handle("PUT /api/cases/{id}/referrals/{rid}",
		deps.Auth.Protect(auth.PermCaseReferral, http.HandlerFunc(handler.UpdateReferral)))
	mux.Handle("POST /api/cases/{id}/recovery",
		deps.Auth.Protect(auth.PermCaseRecovery, http.HandlerFunc(handler.RecordRecovery)))
}
