// Helper HTTP: tipe error aplikasi, penulis respons JSON, pembaca body, dan middleware (request ID, recover, header keamanan, log akses).
package httpx

import (
	"errors"
	"net/http"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

// Kode error aplikasi yang dipakai sebagai kode mesin pada respons API.
const (
	CodeValidation   = "VALIDATION_ERROR"
	CodeUnauthorized = "UNAUTHORIZED"
	CodeForbidden    = "FORBIDDEN"
	CodeNotFound     = "NOT_FOUND"
	CodeConflict     = "CONFLICT"
	CodeLocked       = "ACCOUNT_LOCKED"
	CodeTooMany      = "RATE_LIMITED"
	CodeInternal     = "INTERNAL"
)

// Error aplikasi yang dapat langsung ditulis sebagai respons JSON.
type AppError struct {
	Status  int               // Kode status HTTP.
	Code    string            // Kode mesin, mis. VALIDATION_ERROR.
	Message string            // Pesan error yang ditampilkan kepada pengguna.
	Fields  map[string]string // Detail error tambahan berdasarkan field.
}

func (e *AppError) Error() string {
	if e.Message != "" {
		return e.Message
	}
	return e.Code
}

// NewAppError membuat AppError dengan pesan yang diberikan.
func NewAppError(status int, code, message string) *AppError {
	return &AppError{Status: status, Code: code, Message: message}
}

// ErrValidation membuat AppError 422 untuk kesalahan validasi.
func ErrValidation(message string) *AppError {
	return &AppError{Status: http.StatusUnprocessableEntity, Code: CodeValidation, Message: message}
}

// ErrUnauthorized membuat AppError 401 untuk kredensial atau sesi tidak valid.
func ErrUnauthorized() *AppError {
	return &AppError{Status: http.StatusUnauthorized, Code: CodeUnauthorized, Message: "Anda harus masuk terlebih dahulu."}
}

// ErrForbidden membuat AppError 403 untuk role yang tidak berwenang.
func ErrForbidden() *AppError {
	return &AppError{Status: http.StatusForbidden, Code: CodeForbidden, Message: "Anda tidak memiliki hak untuk tindakan ini."}
}

// ErrNotFound membuat AppError 404 untuk data yang tidak ditemukan.
func ErrNotFound() *AppError {
	return &AppError{Status: http.StatusNotFound, Code: CodeNotFound, Message: "Data tidak ditemukan."}
}

// ErrConflict membuat AppError 409 untuk konflik status atau duplikasi.
func ErrConflict(message string) *AppError {
	return &AppError{Status: http.StatusConflict, Code: CodeConflict, Message: message}
}

// ErrLocked membuat AppError 423 untuk akun yang terkunci sementara.
func ErrLocked(message string) *AppError {
	return &AppError{Status: http.StatusLocked, Code: CodeLocked, Message: message}
}

// ErrTooMany membuat AppError 429 untuk rate limit atau captcha gagal.
func ErrTooMany(message string) *AppError {
	return &AppError{Status: http.StatusTooManyRequests, Code: CodeTooMany, Message: message}
}

// ErrInternal membuat AppError 500 generik tanpa detail teknis.
func ErrInternal() *AppError {
	return &AppError{Status: http.StatusInternalServerError, Code: CodeInternal, Message: "Terjadi kesalahan pada server."}
}

// FromError memetakan error, termasuk error database, ke AppError yang sesuai.
func FromError(err error) *AppError {
	if err == nil {
		return nil
	}

	var appErr *AppError
	if errors.As(err, &appErr) {
		return appErr
	}

	// pgx: tidak ada baris -> 404.
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrNotFound()
	}

	// pgconn: pelanggaran unique -> 409, foreign key -> 422.
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) {
		switch pgErr.Code {
		case "23505": // unique_violation
			return ErrConflict("Data sudah ada.")
		case "23503": // foreign_key_violation
			return ErrValidation("Data terkait tidak valid.")
		}
	}

	return ErrInternal()
}
