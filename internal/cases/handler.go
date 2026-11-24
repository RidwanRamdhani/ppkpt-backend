package cases

import (
	"context"
	"log/slog"
	"net/http"
	"strconv"

	"github.com/RidwanRamdhani/ppkpt-backend/internal/auth"
	"github.com/RidwanRamdhani/ppkpt-backend/internal/platform/httpx"
)

// Backend adalah operasi kasus yang dibutuhkan handler. *Service memenuhinya;
// interface sempit ini memudahkan pengujian HTTP tanpa database.
type Backend interface {
	List(ctx context.Context, actor Actor, filter Filter) (ListPage, error)
	Detail(ctx context.Context, actor Actor, reportID int64, ip string) (any, error)
	History(ctx context.Context, actor Actor, reportID int64) (HistoryResponse, error)
}

// Handler melayani endpoint baca kasus:
// GET /api/cases, GET /api/cases/{id}, dan GET /api/cases/{id}/history.
type Handler struct {
	backend Backend
	logger  *slog.Logger
}

// NewHandler membuat handler kasus. logger boleh nil.
func NewHandler(backend Backend, logger *slog.Logger) *Handler {
	if logger == nil {
		logger = slog.Default()
	}
	return &Handler{backend: backend, logger: logger}
}

// List menangani GET /api/cases.
func (h *Handler) List(w http.ResponseWriter, r *http.Request) {
	actor, ok := actorFrom(r)
	if !ok {
		httpx.WriteError(w, httpx.ErrUnauthorized())
		return
	}

	filter, appErr := ParseFilter(r.URL.Query(), actor.UserID)
	if appErr != nil {
		httpx.WriteError(w, appErr)
		return
	}

	page, err := h.backend.List(r.Context(), actor, filter)
	if err != nil {
		h.writeServiceError(w, r, err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, page)
}

// Detail menangani GET /api/cases/{id}.
func (h *Handler) Detail(w http.ResponseWriter, r *http.Request) {
	actor, ok := actorFrom(r)
	if !ok {
		httpx.WriteError(w, httpx.ErrUnauthorized())
		return
	}
	reportID, ok := pathReportID(w, r)
	if !ok {
		return
	}

	view, err := h.backend.Detail(r.Context(), actor, reportID, clientIP(r))
	if err != nil {
		h.writeServiceError(w, r, err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, view)
}

// History menangani GET /api/cases/{id}/history.
func (h *Handler) History(w http.ResponseWriter, r *http.Request) {
	actor, ok := actorFrom(r)
	if !ok {
		httpx.WriteError(w, httpx.ErrUnauthorized())
		return
	}
	reportID, ok := pathReportID(w, r)
	if !ok {
		return
	}

	history, err := h.backend.History(r.Context(), actor, reportID)
	if err != nil {
		h.writeServiceError(w, r, err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, history)
}

// actorFrom mengambil identitas petugas dari context. Middleware auth sudah
// memastikan principal ada; pemeriksaan ulang mencegah handler dipakai tanpa
// middleware.
func actorFrom(r *http.Request) (Actor, bool) {
	p, ok := auth.PrincipalFrom(r.Context())
	if !ok {
		return Actor{}, false
	}
	return Actor{UserID: p.UserID, Role: p.Role}, true
}

// pathReportID memvalidasi {id}. ID yang tidak valid diperlakukan sebagai data
// tidak ditemukan agar tidak membocorkan detail parsing.
func pathReportID(w http.ResponseWriter, r *http.Request) (int64, bool) {
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
		h.logger.Error("gagal membaca kasus",
			"request_id", httpx.IDFromContext(r.Context()),
			"err", err)
	}
	httpx.WriteError(w, appErr)
}

// Pastikan *Service memenuhi Backend.
var _ Backend = (*Service)(nil)
