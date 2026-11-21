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
)

// Submitter adalah operasi yang dibutuhkan handler. *Service memenuhinya;
// interface ini memudahkan pengujian HTTP tanpa database.
type Submitter interface {
	Submit(ctx context.Context, req SubmitRequest, files []*multipart.FileHeader, ip string) (Result, error)
}

// CaptchaVerifier memverifikasi token captcha di sisi server.
type CaptchaVerifier interface {
	Verify(ctx context.Context, token, ip string) error
}

// Limiter membatasi jumlah permintaan per kunci (IP).
type Limiter interface {
	Allow(key string) bool
}

// Handler melayani POST /api/reports (multipart/form-data).
type Handler struct {
	submitter Submitter
	captcha   CaptchaVerifier
	limiter   Limiter
	logger    *slog.Logger
	maxBody   int64
}

// NewHandler membuat handler pelaporan. maxBody adalah batas total body
// multipart (semua berkas + payload); nilai <= 0 diganti default aman.
func NewHandler(submitter Submitter, captchaVerifier CaptchaVerifier, limiter Limiter, logger *slog.Logger, maxBody int64) *Handler {
	if logger == nil {
		logger = slog.Default()
	}
	if maxBody <= 0 {
		maxBody = int64(defaultMaxUploadBytes)*defaultMaxFiles + multipartOverhead
	}
	return &Handler{
		submitter: submitter,
		captcha:   captchaVerifier,
		limiter:   limiter,
		logger:    logger,
		maxBody:   maxBody,
	}
}

// Submit menangani pengiriman laporan publik.
//
// Urutan pemeriksaan: rate limit -> batas body -> parse multipart -> captcha ->
// validasi & simpan. Captcha diperiksa sebelum berkas diproses agar
// penyalahgunaan tidak membebani storage.
func (h *Handler) Submit(w http.ResponseWriter, r *http.Request) {
	ip := ratelimit.ClientKey(r)

	if h.limiter != nil && !h.limiter.Allow(ip) {
		w.Header().Set("Retry-After", "60")
		httpx.WriteError(w, httpx.ErrTooMany("Terlalu banyak permintaan. Silakan coba lagi nanti."))
		return
	}

	r.Body = http.MaxBytesReader(w, r.Body, h.maxBody)
	// Body sudah dibatasi MaxBytesReader di atas; gosec tidak mengenali pola
	// assignment ini sehingga peringatan G120 ditekan dengan alasan tersebut.
	if err := r.ParseMultipartForm(maxMultipartMemory); err != nil { //nolint:gosec // total body dibatasi MaxBytesReader
		var maxErr *http.MaxBytesError
		if errors.As(err, &maxErr) {
			writeFieldError(w, "payload", "Ukuran permintaan terlalu besar.")
			return
		}
		writeFieldError(w, "payload", "Formulir tidak valid.")
		return
	}

	if h.captcha != nil {
		if err := h.captcha.Verify(r.Context(), formValue(r, "captcha_token"), ip); err != nil {
			// Semua kegagalan captcha dipetakan ke 429 agar tidak membocorkan
			// alasan internal ke klien.
			httpx.WriteError(w, httpx.ErrTooMany("Verifikasi keamanan gagal. Silakan coba lagi."))
			return
		}
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

	var files []*multipart.FileHeader
	if r.MultipartForm != nil {
		files = r.MultipartForm.File["evidences"]
	}

	result, err := h.submitter.Submit(r.Context(), req, files, ip)
	if err != nil {
		h.writeServiceError(w, r, err)
		return
	}

	httpx.WriteJSON(w, http.StatusCreated, result)
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
	if appErr.Status >= http.StatusInternalServerError {
		// Detail teknis hanya masuk log server, tidak pernah ke respons (NFR-23).
		h.logger.Error("gagal memproses laporan",
			"request_id", httpx.IDFromContext(r.Context()),
			"err", err)
	}
	httpx.WriteError(w, appErr)
}

func writeFieldError(w http.ResponseWriter, field, message string) {
	appErr := httpx.ErrValidation(message)
	appErr.Fields = map[string]string{field: message}
	httpx.WriteError(w, appErr)
}

// Pastikan captcha.Verifier memenuhi CaptchaVerifier.
var _ CaptchaVerifier = (*captcha.Verifier)(nil)
