package review

import (
	"context"
	"errors"
	"log/slog"
	"mime/multipart"
	"net/http"
	"strconv"

	"github.com/RidwanRamdhani/ppkpt-backend/internal/auth"
	"github.com/RidwanRamdhani/ppkpt-backend/internal/platform/httpx"
)

const (
	// maxMultipartMemory adalah batas bagian non-berkas yang disimpan di memori.
	maxMultipartMemory = 8 << 20 // 8 MiB

	defaultMaxUploadBytes = 10 << 20 // 10 MB per berkas
	defaultMaxFiles       = 10
	multipartOverhead     = 1 << 20
)

// Backend adalah operasi review yang dibutuhkan handler. *Service memenuhinya;
// interface sempit ini memudahkan pengujian HTTP tanpa database.
type Backend interface {
	SaveReview(ctx context.Context, actor Actor, reportID int64, req ReviewRequest, ip string) (ReviewView, error)
	ContinueReview(ctx context.Context, actor Actor, reportID int64, ip string) (ContinueResult, error)
	CloseReview(ctx context.Context, actor Actor, reportID int64, req CloseRequest, ip string) (CloseResult, error)
	CreateSession(ctx context.Context, actor Actor, reportID int64, req SessionRequest, ip string) (SessionView, error)
	UploadEvidence(ctx context.Context, actor Actor, reportID int64, category string, files []*multipart.FileHeader, ip string) (UploadResult, error)
}

// Handler melayani endpoint penelaahan dan pemeriksaan.
type Handler struct {
	backend Backend
	logger  *slog.Logger
	maxBody int64
}

// NewHandler membuat handler review. logger boleh nil. maxBody adalah batas
// total body multipart unggah berkas; nilai <= 0 diganti default aman.
func NewHandler(backend Backend, logger *slog.Logger, maxBody int64) *Handler {
	if logger == nil {
		logger = slog.Default()
	}
	if maxBody <= 0 {
		maxBody = int64(defaultMaxUploadBytes)*defaultMaxFiles + multipartOverhead
	}
	return &Handler{backend: backend, logger: logger, maxBody: maxBody}
}

// SaveReview menangani POST /api/cases/{id}/review.
func (h *Handler) SaveReview(w http.ResponseWriter, r *http.Request) {
	actor, ok := actorFrom(r)
	if !ok {
		httpx.WriteError(w, httpx.ErrUnauthorized())
		return
	}
	reportID, ok := pathReportID(w, r)
	if !ok {
		return
	}

	var req ReviewRequest
	if err := httpx.ReadJSON(w, r, &req); err != nil {
		httpx.WriteError(w, err)
		return
	}

	view, err := h.backend.SaveReview(r.Context(), actor, reportID, req, clientIP(r))
	if err != nil {
		h.writeServiceError(w, r, err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, view)
}

// ContinueReview menangani POST /api/cases/{id}/review/continue.
func (h *Handler) ContinueReview(w http.ResponseWriter, r *http.Request) {
	actor, ok := actorFrom(r)
	if !ok {
		httpx.WriteError(w, httpx.ErrUnauthorized())
		return
	}
	reportID, ok := pathReportID(w, r)
	if !ok {
		return
	}

	result, err := h.backend.ContinueReview(r.Context(), actor, reportID, clientIP(r))
	if err != nil {
		h.writeServiceError(w, r, err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, result)
}

// CloseReview menangani POST /api/cases/{id}/review/close.
func (h *Handler) CloseReview(w http.ResponseWriter, r *http.Request) {
	actor, ok := actorFrom(r)
	if !ok {
		httpx.WriteError(w, httpx.ErrUnauthorized())
		return
	}
	reportID, ok := pathReportID(w, r)
	if !ok {
		return
	}

	var req CloseRequest
	if err := httpx.ReadJSON(w, r, &req); err != nil {
		httpx.WriteError(w, err)
		return
	}

	result, err := h.backend.CloseReview(r.Context(), actor, reportID, req, clientIP(r))
	if err != nil {
		h.writeServiceError(w, r, err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, result)
}

// CreateSession menangani POST /api/cases/{id}/sessions.
func (h *Handler) CreateSession(w http.ResponseWriter, r *http.Request) {
	actor, ok := actorFrom(r)
	if !ok {
		httpx.WriteError(w, httpx.ErrUnauthorized())
		return
	}
	reportID, ok := pathReportID(w, r)
	if !ok {
		return
	}

	var req SessionRequest
	if err := httpx.ReadJSON(w, r, &req); err != nil {
		httpx.WriteError(w, err)
		return
	}

	view, err := h.backend.CreateSession(r.Context(), actor, reportID, req, clientIP(r))
	if err != nil {
		h.writeServiceError(w, r, err)
		return
	}
	httpx.WriteJSON(w, http.StatusCreated, view)
}

// UploadEvidence menangani POST /api/cases/{id}/evidences (multipart).
func (h *Handler) UploadEvidence(w http.ResponseWriter, r *http.Request) {
	actor, ok := actorFrom(r)
	if !ok {
		httpx.WriteError(w, httpx.ErrUnauthorized())
		return
	}
	reportID, ok := pathReportID(w, r)
	if !ok {
		return
	}
	if !h.parseMultipart(w, r) {
		return
	}

	result, err := h.backend.UploadEvidence(
		r.Context(), actor, reportID, formValue(r, "category"), multipartFiles(r), clientIP(r),
	)
	if err != nil {
		h.writeServiceError(w, r, err)
		return
	}
	httpx.WriteJSON(w, http.StatusCreated, result)
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
		h.logger.Error("gagal memproses penelaahan/pemeriksaan",
			"request_id", httpx.IDFromContext(r.Context()),
			"err", err)
	}
	httpx.WriteError(w, appErr)
}

// parseMultipart membatasi total body lalu mem-parse formulir unggah berkas.
func (h *Handler) parseMultipart(w http.ResponseWriter, r *http.Request) bool {
	r.Body = http.MaxBytesReader(w, r.Body, h.maxBody)
	if err := r.ParseMultipartForm(maxMultipartMemory); err != nil { //nolint:gosec // total body dibatasi MaxBytesReader
		var maxErr *http.MaxBytesError
		if errors.As(err, &maxErr) {
			writeFieldError(w, "form", "Ukuran permintaan terlalu besar.")
			return false
		}
		writeFieldError(w, "form", "Formulir tidak valid.")
		return false
	}
	return true
}

// multipartFiles mengambil bagian berkas dari formulir.
func multipartFiles(r *http.Request) []*multipart.FileHeader {
	if r.MultipartForm == nil {
		return nil
	}
	return r.MultipartForm.File["evidences"]
}

// formValue mengambil field teks hanya dari body multipart.
func formValue(r *http.Request, key string) string {
	if r.MultipartForm == nil {
		return ""
	}
	values := r.MultipartForm.Value[key]
	if len(values) == 0 {
		return ""
	}
	return values[0]
}

func writeFieldError(w http.ResponseWriter, field, message string) {
	appErr := httpx.ErrValidation("Data yang dikirim belum lengkap atau tidak valid.")
	appErr.Fields = map[string]string{field: message}
	httpx.WriteError(w, appErr)
}

// Pastikan *Service memenuhi Backend.
var _ Backend = (*Service)(nil)
