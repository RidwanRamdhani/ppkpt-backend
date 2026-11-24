package cases

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"mime/multipart"
	"net/http"
	"strconv"
	"strings"

	"github.com/RidwanRamdhani/ppkpt-backend/internal/auth"
	"github.com/RidwanRamdhani/ppkpt-backend/internal/platform/httpx"
)

const (
	// maxMultipartMemory adalah batas bagian non-berkas yang disimpan di memori;
	// berkas yang lebih besar dialirkan ke berkas sementara.
	maxMultipartMemory = 8 << 20 // 8 MiB

	defaultMaxUploadBytes = 10 << 20 // 10 MB per berkas (SDD M01)
	defaultMaxFiles       = 10
	multipartOverhead     = 1 << 20
)

// Backend adalah operasi kasus yang dibutuhkan handler. *Service memenuhinya;
// interface sempit ini memudahkan pengujian HTTP tanpa database.
type Backend interface {
	List(ctx context.Context, actor Actor, filter Filter) (ListPage, error)
	Detail(ctx context.Context, actor Actor, reportID int64, ip string) (any, error)
	History(ctx context.Context, actor Actor, reportID int64) (HistoryResponse, error)
	CreateManual(ctx context.Context, actor Actor, req ManualRequest, files []*multipart.FileHeader, ip string) (CreateResult, error)
	Take(ctx context.Context, actor Actor, reportID int64, ip string) (TakeResult, error)
	Verify(ctx context.Context, actor Actor, reportID int64, req VerificationRequest, ip string) (VerificationResult, error)
	AddParty(ctx context.Context, actor Actor, reportID int64, input PartyInput, ip string) (PartyView, error)
	UpdateParty(ctx context.Context, actor Actor, reportID, partyID int64, input PartyUpdate, ip string) (PartyView, error)
}

// Handler melayani endpoint kasus: daftar, detail, riwayat, input manual, dan
// pengambilan kasus.
type Handler struct {
	backend Backend
	logger  *slog.Logger
	maxBody int64
}

// NewHandler membuat handler kasus. logger boleh nil. maxBody adalah batas
// total body multipart input manual; nilai <= 0 diganti default aman.
func NewHandler(backend Backend, logger *slog.Logger, maxBody int64) *Handler {
	if logger == nil {
		logger = slog.Default()
	}
	if maxBody <= 0 {
		maxBody = int64(defaultMaxUploadBytes)*defaultMaxFiles + multipartOverhead
	}
	return &Handler{backend: backend, logger: logger, maxBody: maxBody}
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

// CreateManual menangani POST /api/cases (multipart).
//
// Token captcha tidak dipakai pada input manual karena endpoint hanya dapat
// diakses Satgas yang sudah login. Berkas bukti opsional.
func (h *Handler) CreateManual(w http.ResponseWriter, r *http.Request) {
	actor, ok := actorFrom(r)
	if !ok {
		httpx.WriteError(w, httpx.ErrUnauthorized())
		return
	}
	if !h.parseMultipart(w, r) {
		return
	}

	raw := formValue(r, "payload")
	if strings.TrimSpace(raw) == "" {
		writeFieldError(w, "payload", "Data laporan wajib diisi.")
		return
	}

	var req ManualRequest
	if err := decodeManualPayload(raw, &req); err != nil {
		writeFieldError(w, "payload", "Data laporan tidak valid.")
		return
	}

	result, err := h.backend.CreateManual(r.Context(), actor, req, multipartFiles(r), clientIP(r))
	if err != nil {
		h.writeServiceError(w, r, err)
		return
	}
	httpx.WriteJSON(w, http.StatusCreated, result)
}

// Take menangani POST /api/cases/{id}/take.
func (h *Handler) Take(w http.ResponseWriter, r *http.Request) {
	actor, ok := actorFrom(r)
	if !ok {
		httpx.WriteError(w, httpx.ErrUnauthorized())
		return
	}
	reportID, ok := pathReportID(w, r)
	if !ok {
		return
	}

	result, err := h.backend.Take(r.Context(), actor, reportID, clientIP(r))
	if err != nil {
		h.writeServiceError(w, r, err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, result)
}

// Verify menangani POST /api/cases/{id}/verification.
func (h *Handler) Verify(w http.ResponseWriter, r *http.Request) {
	actor, ok := actorFrom(r)
	if !ok {
		httpx.WriteError(w, httpx.ErrUnauthorized())
		return
	}
	reportID, ok := pathReportID(w, r)
	if !ok {
		return
	}

	var req VerificationRequest
	if err := httpx.ReadJSON(w, r, &req); err != nil {
		httpx.WriteError(w, err)
		return
	}

	result, err := h.backend.Verify(r.Context(), actor, reportID, req, clientIP(r))
	if err != nil {
		h.writeServiceError(w, r, err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, result)
}

// AddParty menangani POST /api/cases/{id}/parties.
func (h *Handler) AddParty(w http.ResponseWriter, r *http.Request) {
	actor, ok := actorFrom(r)
	if !ok {
		httpx.WriteError(w, httpx.ErrUnauthorized())
		return
	}
	reportID, ok := pathReportID(w, r)
	if !ok {
		return
	}

	var input PartyInput
	if err := httpx.ReadJSON(w, r, &input); err != nil {
		httpx.WriteError(w, err)
		return
	}

	party, err := h.backend.AddParty(r.Context(), actor, reportID, input, clientIP(r))
	if err != nil {
		h.writeServiceError(w, r, err)
		return
	}
	httpx.WriteJSON(w, http.StatusCreated, party)
}

// UpdateParty menangani PUT /api/cases/{id}/parties/{partyId}.
func (h *Handler) UpdateParty(w http.ResponseWriter, r *http.Request) {
	actor, ok := actorFrom(r)
	if !ok {
		httpx.WriteError(w, httpx.ErrUnauthorized())
		return
	}
	reportID, ok := pathReportID(w, r)
	if !ok {
		return
	}
	partyID, ok := pathPartyID(w, r)
	if !ok {
		return
	}

	var input PartyUpdate
	if err := httpx.ReadJSON(w, r, &input); err != nil {
		httpx.WriteError(w, err)
		return
	}

	party, err := h.backend.UpdateParty(r.Context(), actor, reportID, partyID, input, clientIP(r))
	if err != nil {
		h.writeServiceError(w, r, err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, party)
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
	return pathID(w, r, "id")
}

// pathPartyID memvalidasi {partyId} pada path pihak.
func pathPartyID(w http.ResponseWriter, r *http.Request) (int64, bool) {
	return pathID(w, r, "partyId")
}

func pathID(w http.ResponseWriter, r *http.Request, name string) (int64, bool) {
	id, err := strconv.ParseInt(r.PathValue(name), 10, 64)
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
		h.logger.Error("gagal memproses kasus",
			"request_id", httpx.IDFromContext(r.Context()),
			"err", err)
	}
	httpx.WriteError(w, appErr)
}

// parseMultipart membatasi total body lalu mem-parse formulir input manual.
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

// multipartFiles mengambil bagian berkas bukti dari formulir.
func multipartFiles(r *http.Request) []*multipart.FileHeader {
	if r.MultipartForm == nil {
		return nil
	}
	return r.MultipartForm.File["evidences"]
}

// formValue mengambil field teks hanya dari body multipart, bukan query string.
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

// decodeManualPayload mendekode JSON dengan field tak dikenal ditolak dan
// memastikan hanya ada satu objek.
func decodeManualPayload(raw string, dst *ManualRequest) error {
	dec := json.NewDecoder(strings.NewReader(raw))
	dec.DisallowUnknownFields()
	if err := dec.Decode(dst); err != nil {
		return err
	}
	var extra any
	if err := dec.Decode(&extra); !errors.Is(err, io.EOF) {
		return errors.New("payload memuat data tambahan")
	}
	return nil
}

func writeFieldError(w http.ResponseWriter, field, message string) {
	appErr := httpx.ErrValidation("Data yang dikirim belum lengkap atau tidak valid.")
	appErr.Fields = map[string]string{field: message}
	httpx.WriteError(w, appErr)
}

// Pastikan *Service memenuhi Backend.
var _ Backend = (*Service)(nil)
