package referral

import (
	"context"
	"log/slog"
	"net/http"
	"strconv"

	"github.com/RidwanRamdhani/ppkpt-backend/internal/auth"
	"github.com/RidwanRamdhani/ppkpt-backend/internal/platform/httpx"
)

// Backend adalah operasi referral yang dibutuhkan handler. *Service
// memenuhinya; interface sempit ini memudahkan pengujian HTTP tanpa database.
type Backend interface {
	SetCondition(ctx context.Context, actor Actor, reportID int64, req ConditionRequest, ip string) (ConditionView, error)
	CreateReferral(ctx context.Context, actor Actor, reportID int64, req ReferralRequest, ip string) (ReferralView, error)
	UpdateReferral(ctx context.Context, actor Actor, reportID, referralID int64, req ReferralUpdateRequest, ip string) (ReferralView, error)
	RecordRecovery(ctx context.Context, actor Actor, reportID int64, req RecoveryRequest, ip string) (RecoveryView, error)
}

// Handler melayani endpoint kondisi, rujukan, dan pemulihan.
type Handler struct {
	backend Backend
	logger  *slog.Logger
}

// NewHandler membuat handler referral. logger boleh nil.
func NewHandler(backend Backend, logger *slog.Logger) *Handler {
	if logger == nil {
		logger = slog.Default()
	}
	return &Handler{backend: backend, logger: logger}
}

// SetCondition menangani PUT /api/cases/{id}/condition.
func (h *Handler) SetCondition(w http.ResponseWriter, r *http.Request) {
	actor, ok := actorFrom(r)
	if !ok {
		httpx.WriteError(w, httpx.ErrUnauthorized())
		return
	}
	reportID, ok := pathReportID(w, r)
	if !ok {
		return
	}

	var req ConditionRequest
	if err := httpx.ReadJSON(w, r, &req); err != nil {
		httpx.WriteError(w, err)
		return
	}

	view, err := h.backend.SetCondition(r.Context(), actor, reportID, req, clientIP(r))
	if err != nil {
		h.writeServiceError(w, r, err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, view)
}

// CreateReferral menangani POST /api/cases/{id}/referrals.
func (h *Handler) CreateReferral(w http.ResponseWriter, r *http.Request) {
	actor, ok := actorFrom(r)
	if !ok {
		httpx.WriteError(w, httpx.ErrUnauthorized())
		return
	}
	reportID, ok := pathReportID(w, r)
	if !ok {
		return
	}

	var req ReferralRequest
	if err := httpx.ReadJSON(w, r, &req); err != nil {
		httpx.WriteError(w, err)
		return
	}

	view, err := h.backend.CreateReferral(r.Context(), actor, reportID, req, clientIP(r))
	if err != nil {
		h.writeServiceError(w, r, err)
		return
	}
	httpx.WriteJSON(w, http.StatusCreated, view)
}

// UpdateReferral menangani PUT /api/cases/{id}/referrals/{rid}.
func (h *Handler) UpdateReferral(w http.ResponseWriter, r *http.Request) {
	actor, ok := actorFrom(r)
	if !ok {
		httpx.WriteError(w, httpx.ErrUnauthorized())
		return
	}
	reportID, ok := pathReportID(w, r)
	if !ok {
		return
	}
	referralID, ok := pathReferralID(w, r)
	if !ok {
		return
	}

	var req ReferralUpdateRequest
	if err := httpx.ReadJSON(w, r, &req); err != nil {
		httpx.WriteError(w, err)
		return
	}

	view, err := h.backend.UpdateReferral(r.Context(), actor, reportID, referralID, req, clientIP(r))
	if err != nil {
		h.writeServiceError(w, r, err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, view)
}

// RecordRecovery menangani POST /api/cases/{id}/recovery.
func (h *Handler) RecordRecovery(w http.ResponseWriter, r *http.Request) {
	actor, ok := actorFrom(r)
	if !ok {
		httpx.WriteError(w, httpx.ErrUnauthorized())
		return
	}
	reportID, ok := pathReportID(w, r)
	if !ok {
		return
	}

	var req RecoveryRequest
	if err := httpx.ReadJSON(w, r, &req); err != nil {
		httpx.WriteError(w, err)
		return
	}

	view, err := h.backend.RecordRecovery(r.Context(), actor, reportID, req, clientIP(r))
	if err != nil {
		h.writeServiceError(w, r, err)
		return
	}
	httpx.WriteJSON(w, http.StatusCreated, view)
}

// actorFrom mengambil identitas petugas dari context.
func actorFrom(r *http.Request) (Actor, bool) {
	p, ok := auth.PrincipalFrom(r.Context())
	if !ok {
		return Actor{}, false
	}
	return Actor{UserID: p.UserID, Role: p.Role}, true
}

// pathReportID memvalidasi {id}. ID tidak valid diperlakukan sebagai 404.
func pathReportID(w http.ResponseWriter, r *http.Request) (int64, bool) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil || id <= 0 {
		httpx.WriteError(w, httpx.ErrNotFound())
		return 0, false
	}
	return id, true
}

// pathReferralID memvalidasi {rid}. ID tidak valid diperlakukan sebagai 404.
func pathReferralID(w http.ResponseWriter, r *http.Request) (int64, bool) {
	id, err := strconv.ParseInt(r.PathValue("rid"), 10, 64)
	if err != nil || id <= 0 {
		httpx.WriteError(w, httpx.ErrNotFound())
		return 0, false
	}
	return id, true
}

// clientIP memakai resolver IP tepercaya dari context bila tersedia.
func clientIP(r *http.Request) string {
	if resolver := httpx.ResolverFromContext(r.Context()); resolver != nil {
		if ip := resolver.Resolve(r); ip != "" {
			return ip
		}
	}
	return httpx.ClientIP(r)
}

// writeServiceError memetakan error layanan ke respons HTTP. Detail teknis
// hanya masuk log server (NFR-23).
func (h *Handler) writeServiceError(w http.ResponseWriter, r *http.Request, err error) {
	appErr := httpx.FromError(err)
	if appErr.Status >= http.StatusInternalServerError {
		h.logger.Error("gagal memproses kondisi/rujukan/pemulihan",
			"request_id", httpx.IDFromContext(r.Context()),
			"err", err)
	}
	httpx.WriteError(w, appErr)
}

// Pastikan *Service memenuhi Backend.
var _ Backend = (*Service)(nil)
