package report

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"mime/multipart"
	"net/http"
	"strings"

	"github.com/RidwanRamdhani/ppkpt-backend/internal/platform/captcha"
	"github.com/RidwanRamdhani/ppkpt-backend/internal/platform/httpx"
	"github.com/RidwanRamdhani/ppkpt-backend/internal/platform/ratelimit"
)

const (
	// maxMultipartMemory adalah batas bagian non-berkas yang disimpan di memori;
	// berkas yang lebih besar dialirkan ke berkas sementara.
	maxMultipartMemory = 8 << 20 // 8 MiB

	defaultMaxUploadBytes = 10 << 20 // 10 MB per berkas (SDD M01)
	defaultMaxFiles       = 10
	multipartOverhead     = 1 << 20 // ruang untuk boundary/header/payload JSON

	// CaptchaHeader membawa token captcha di luar body multipart. Token harus
	// tersedia sebelum body dibaca agar berkas tidak pernah diparse lebih dulu.
	CaptchaHeader = "X-Captcha-Token"
)

// Backend adalah operasi yang dibutuhkan handler. *Service memenuhinya;
// interface ini memudahkan pengujian HTTP tanpa database.
type Backend interface {
	Submit(ctx context.Context, req SubmitRequest, files []*multipart.FileHeader, ip string) (Result, error)
	VerifyPIN(ctx context.Context, ticket, pin string) (Report, error)
	Status(ctx context.Context, report Report, ip string) (StatusView, error)
	Complete(ctx context.Context, report Report, additionalText string, files []*multipart.FileHeader, ip string) (Completion, error)
}

// CaptchaVerifier memverifikasi token captcha di sisi server. Implementasi
// captcha.Verifier mengabaikan verifikasi saat captcha dinonaktifkan.
type CaptchaVerifier interface {
	Verify(ctx context.Context, token, ip string) error
}

// Limiter membatasi jumlah permintaan per kunci (IP).
type Limiter interface {
	Allow(key string) bool
}

// Handler melayani endpoint pelaporan publik:
// POST /api/reports, POST /api/status, dan PUT /api/reports/{ticket}/complete.
type Handler struct {
	backend Backend
	captcha CaptchaVerifier
	limiter Limiter
	logger  *slog.Logger
	maxBody int64
}

// NewHandler membuat handler pelaporan. maxBody adalah batas total body
// multipart (semua berkas + payload); nilai <= 0 diganti default aman.
func NewHandler(backend Backend, captchaVerifier CaptchaVerifier, limiter Limiter, logger *slog.Logger, maxBody int64) *Handler {
	if logger == nil {
		logger = slog.Default()
	}
	if maxBody <= 0 {
		maxBody = int64(defaultMaxUploadBytes)*defaultMaxFiles + multipartOverhead
	}
	return &Handler{
		backend: backend,
		captcha: captchaVerifier,
		limiter: limiter,
		logger:  logger,
		maxBody: maxBody,
	}
}

// Submit menangani pengiriman laporan publik.
//
// Urutan pemeriksaan: rate limit -> captcha (header) -> batas body/multipart ->
// validasi & simpan. Token captcha wajib dikirim pada header X-Captcha-Token
// agar dapat diverifikasi sebelum ParseMultipartForm membaca body dan menulis
// berkas ke memori/berkas sementara.
func (h *Handler) Submit(w http.ResponseWriter, r *http.Request) {
	ip := ratelimit.ClientKey(r)
	if !h.allow(w, ip) {
		return
	}
	if h.captcha != nil {
		if err := h.captcha.Verify(r.Context(), strings.TrimSpace(r.Header.Get(CaptchaHeader)), ip); err != nil {
			// Semua kegagalan captcha dipetakan ke 429 agar tidak membocorkan
			// alasan internal ke klien.
			httpx.WriteError(w, httpx.ErrTooMany("Verifikasi keamanan gagal. Silakan coba lagi."))
			return
		}
	}
	if !h.parseMultipart(w, r) {
		return
	}

	raw := formValue(r, "payload")
	if strings.TrimSpace(raw) == "" {
		writeFieldError(w, "payload", "Data laporan wajib diisi.")
		return
	}

	var req SubmitRequest
	if err := decodePayload(raw, &req); err != nil {
		writeFieldError(w, "payload", "Data laporan tidak valid.")
		return
	}

	result, err := h.backend.Submit(r.Context(), req, multipartFiles(r), ip)
	if err != nil {
		h.writeServiceError(w, r, err)
		return
	}

	httpx.WriteJSON(w, http.StatusCreated, result)
}

// Status menangani POST /api/status. Hanya membutuhkan kode tiket dan PIN.
func (h *Handler) Status(w http.ResponseWriter, r *http.Request) {
	if !h.allow(w, ratelimit.ClientKey(r)) {
		return
	}

	var req StatusRequest
	if err := httpx.ReadJSON(w, r, &req); err != nil {
		httpx.WriteError(w, err)
		return
	}

	fields := map[string]string{}
	if strings.TrimSpace(req.CaseNumber) == "" {
		fields["case_number"] = "Wajib diisi."
	}
	if strings.TrimSpace(req.PIN) == "" {
		fields["pin"] = "Wajib diisi."
	}
	if len(fields) > 0 {
		writeFieldsError(w, fields)
		return
	}

	report, err := h.backend.VerifyPIN(r.Context(), req.CaseNumber, req.PIN)
	if err != nil {
		h.writeServiceError(w, r, err)
		return
	}

	view, err := h.backend.Status(r.Context(), report, ratelimit.ClientKey(r))
	if err != nil {
		h.writeServiceError(w, r, err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, view)
}

// Complete menangani PUT /api/reports/{ticket}/complete (multipart).
func (h *Handler) Complete(w http.ResponseWriter, r *http.Request) {
	ip := ratelimit.ClientKey(r)
	if !h.allow(w, ip) {
		return
	}
	if !h.parseMultipart(w, r) {
		return
	}

	ticket := strings.TrimSpace(r.PathValue("ticket"))
	pin := formValue(r, "pin")

	fields := map[string]string{}
	if ticket == "" {
		fields["case_number"] = "Wajib diisi."
	}
	if strings.TrimSpace(pin) == "" {
		fields["pin"] = "Wajib diisi."
	}
	if len(fields) > 0 {
		writeFieldsError(w, fields)
		return
	}

	report, err := h.backend.VerifyPIN(r.Context(), ticket, pin)
	if err != nil {
		h.writeServiceError(w, r, err)
		return
	}

	result, err := h.backend.Complete(r.Context(), report, formValue(r, "additional_text"), multipartFiles(r), ip)
	if err != nil {
		h.writeServiceError(w, r, err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, result)
}

// allow menerapkan rate limit per IP. Header Retry-After membantu klien.
func (h *Handler) allow(w http.ResponseWriter, ip string) bool {
	if h.limiter == nil || h.limiter.Allow(ip) {
		return true
	}
	w.Header().Set("Retry-After", "60")
	httpx.WriteError(w, httpx.ErrTooMany("Terlalu banyak permintaan. Silakan coba lagi nanti."))
	return false
}

// parseMultipart membatasi total body lalu mem-parse formulir. Body sudah
// dibatasi MaxBytesReader; gosec tidak mengenali pola assignment ini sehingga
// peringatan G120 ditekan dengan alasan tersebut.
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

func multipartFiles(r *http.Request) []*multipart.FileHeader {
	if r.MultipartForm == nil {
		return nil
	}
	return r.MultipartForm.File["evidences"]
}

// formValue mengambil field teks hanya dari body multipart, bukan query string,
// agar kontrak tetap ketat.
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

// decodePayload mendekode JSON dengan field tak dikenal ditolak dan memastikan
// hanya ada satu objek.
func decodePayload(raw string, dst *SubmitRequest) error {
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

func (h *Handler) writeServiceError(w http.ResponseWriter, r *http.Request, err error) {
	appErr := httpx.FromError(err)
	if appErr.Status == http.StatusTooManyRequests {
		w.Header().Set("Retry-After", "60")
	}
	if appErr.Status >= http.StatusInternalServerError {
		// Detail teknis hanya masuk log server, tidak pernah ke respons (NFR-23).
		h.logger.Error("gagal memproses laporan",
			"request_id", httpx.IDFromContext(r.Context()),
			"err", err)
	}
	httpx.WriteError(w, appErr)
}

func writeFieldError(w http.ResponseWriter, field, message string) {
	writeFieldsError(w, map[string]string{field: message})
}

func writeFieldsError(w http.ResponseWriter, fields map[string]string) {
	appErr := httpx.ErrValidation("Data yang dikirim belum lengkap atau tidak valid.")
	appErr.Fields = fields
	httpx.WriteError(w, appErr)
}

// Pastikan captcha.Verifier memenuhi CaptchaVerifier.
var _ CaptchaVerifier = (*captcha.Verifier)(nil)
