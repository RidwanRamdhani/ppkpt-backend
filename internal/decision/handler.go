package decision

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"strconv"

	"github.com/RidwanRamdhani/ppkpt-backend/internal/auth"
	"github.com/RidwanRamdhani/ppkpt-backend/internal/evidence"
	"github.com/RidwanRamdhani/ppkpt-backend/internal/platform/httpx"
	"github.com/RidwanRamdhani/ppkpt-backend/internal/platform/storage"
)

// Backend adalah operasi M07 yang dibutuhkan handler. *Service memenuhinya;
// interface sempit ini memudahkan pengujian HTTP tanpa database.
type Backend interface {
	SaveConclusion(ctx context.Context, actor Actor, reportID int64, req ConclusionRequest, ip string) (ConclusionView, error)
	SubmitConclusion(ctx context.Context, actor Actor, reportID int64, ip string) (SubmitResult, error)
	ViewDecision(ctx context.Context, actor Actor, reportID int64, ip string) (*DecisionView, error)
	RecordDecision(ctx context.Context, actor Actor, reportID int64, req DecisionRequest, ip string) (DecisionResult, error)
	RecordDelivery(ctx context.Context, actor Actor, reportID int64, req DeliveryRequest, ip string) (DeliveryResult, error)
	CloseCase(ctx context.Context, actor Actor, reportID int64, ip string) (CloseResult, error)
	DownloadEvidence(ctx context.Context, actor Actor, evidenceID int64, ip string) (evidence.Meta, *storage.Object, error)
}

// Handler melayani endpoint kesimpulan, keputusan, penyampaian, penutupan, dan
// unduhan berkas kasus.
type Handler struct {
	backend Backend
	logger  *slog.Logger
}

// NewHandler membuat handler keputusan. logger boleh nil.
func NewHandler(backend Backend, logger *slog.Logger) *Handler {
	if logger == nil {
		logger = slog.Default()
	}
	return &Handler{backend: backend, logger: logger}
}

// SaveConclusion menangani POST /api/cases/{id}/conclusion.
func (h *Handler) SaveConclusion(w http.ResponseWriter, r *http.Request) {
	actor, ok := actorFrom(r)
	if !ok {
		httpx.WriteError(w, httpx.ErrUnauthorized())
		return
	}
	reportID, ok := pathReportID(w, r)
	if !ok {
		return
	}

	var req ConclusionRequest
	if err := httpx.ReadJSON(w, r, &req); err != nil {
		httpx.WriteError(w, err)
		return
	}

	view, err := h.backend.SaveConclusion(r.Context(), actor, reportID, req, clientIP(r))
	if err != nil {
		h.writeServiceError(w, r, err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, view)
}

// SubmitConclusion menangani POST /api/cases/{id}/conclusion/submit.
func (h *Handler) SubmitConclusion(w http.ResponseWriter, r *http.Request) {
	actor, ok := actorFrom(r)
	if !ok {
		httpx.WriteError(w, httpx.ErrUnauthorized())
		return
	}
	reportID, ok := pathReportID(w, r)
	if !ok {
		return
	}

	result, err := h.backend.SubmitConclusion(r.Context(), actor, reportID, clientIP(r))
	if err != nil {
		h.writeServiceError(w, r, err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, result)
}

// ViewDecision menangani GET /api/cases/{id}/decision-view.
func (h *Handler) ViewDecision(w http.ResponseWriter, r *http.Request) {
	actor, ok := actorFrom(r)
	if !ok {
		httpx.WriteError(w, httpx.ErrUnauthorized())
		return
	}
	reportID, ok := pathReportID(w, r)
	if !ok {
		return
	}

	view, err := h.backend.ViewDecision(r.Context(), actor, reportID, clientIP(r))
	if err != nil {
		h.writeServiceError(w, r, err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, view)
}

// RecordDecision menangani POST /api/cases/{id}/decision.
func (h *Handler) RecordDecision(w http.ResponseWriter, r *http.Request) {
	actor, ok := actorFrom(r)
	if !ok {
		httpx.WriteError(w, httpx.ErrUnauthorized())
		return
	}
	reportID, ok := pathReportID(w, r)
	if !ok {
		return
	}

	var req DecisionRequest
	if err := httpx.ReadJSON(w, r, &req); err != nil {
		httpx.WriteError(w, err)
		return
	}

	result, err := h.backend.RecordDecision(r.Context(), actor, reportID, req, clientIP(r))
	if err != nil {
		h.writeServiceError(w, r, err)
		return
	}
	httpx.WriteJSON(w, http.StatusCreated, result)
}

// RecordDelivery menangani POST /api/cases/{id}/delivery.
func (h *Handler) RecordDelivery(w http.ResponseWriter, r *http.Request) {
	actor, ok := actorFrom(r)
	if !ok {
		httpx.WriteError(w, httpx.ErrUnauthorized())
		return
	}
	reportID, ok := pathReportID(w, r)
	if !ok {
		return
	}

	var req DeliveryRequest
	if err := httpx.ReadJSON(w, r, &req); err != nil {
		httpx.WriteError(w, err)
		return
	}

	result, err := h.backend.RecordDelivery(r.Context(), actor, reportID, req, clientIP(r))
	if err != nil {
		h.writeServiceError(w, r, err)
		return
	}
	httpx.WriteJSON(w, http.StatusCreated, result)
}

// CloseCase menangani POST /api/cases/{id}/close. Endpoint tidak memakai body.
func (h *Handler) CloseCase(w http.ResponseWriter, r *http.Request) {
	actor, ok := actorFrom(r)
	if !ok {
		httpx.WriteError(w, httpx.ErrUnauthorized())
		return
	}
	reportID, ok := pathReportID(w, r)
	if !ok {
		return
	}

	result, err := h.backend.CloseCase(r.Context(), actor, reportID, clientIP(r))
	if err != nil {
		h.writeServiceError(w, r, err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, result)
}

// DownloadEvidence menangani GET /api/evidences/{id}/download. Berkas selalu
// dialirkan lewat backend agar otorisasi dan audit terjadi di server.
func (h *Handler) DownloadEvidence(w http.ResponseWriter, r *http.Request) {
	actor, ok := actorFrom(r)
	if !ok {
		httpx.WriteError(w, httpx.ErrUnauthorized())
		return
	}
	evidenceID, ok := pathEvidenceID(w, r)
	if !ok {
		return
	}

	meta, obj, err := h.backend.DownloadEvidence(r.Context(), actor, evidenceID, clientIP(r))
	if err != nil {
		h.writeServiceError(w, r, err)
		return
	}
	defer func() { _ = obj.Body.Close() }()

	evidence.WriteDownloadHeaders(w, meta, obj)
	w.WriteHeader(http.StatusOK)
	if _, err := io.Copy(w, obj.Body); err != nil {
		// Header sudah terkirim; kegagalan hanya dapat dicatat di server.
		h.logger.Warn("gagal mengirim berkas unduhan",
			"request_id", httpx.IDFromContext(r.Context()),
			"evidence_id", meta.ID,
			"err", err)
	}
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

// pathEvidenceID memvalidasi {id} pada route unduhan berkas.
func pathEvidenceID(w http.ResponseWriter, r *http.Request) (int64, bool) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
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
		h.logger.Error("gagal memproses kesimpulan/keputusan",
			"request_id", httpx.IDFromContext(r.Context()),
			"err", err)
	}
	httpx.WriteError(w, appErr)
}

// Pastikan *Service memenuhi Backend.
var _ Backend = (*Service)(nil)
