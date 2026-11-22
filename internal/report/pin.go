package report

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/RidwanRamdhani/ppkpt-backend/internal/platform/httpx"
	"github.com/RidwanRamdhani/ppkpt-backend/internal/workflow"
	"github.com/jackc/pgx/v5"
	"golang.org/x/crypto/bcrypt"
)

// StatusRequest adalah body POST /api/status.
type StatusRequest struct {
	CaseNumber string `json:"case_number"`
	PIN        string `json:"pin"`
}

// Completion adalah respons PUT /api/reports/{ticket}/complete.
type Completion struct {
	CaseNumber string `json:"case_number"`
	Status     string `json:"status"`
}

// Report adalah data minimum yang boleh dipakai setelah PIN terverifikasi.
// Sengaja tidak memuat isi laporan, identitas pihak, atau hash selain PIN.
type Report struct {
	ID             int64
	CaseNumber     string
	Status         workflow.Status
	PinHash        string
	PinFailedCount int
	PinLockedUntil *time.Time
	ReviewDueAt    *time.Time
	ExamDueAt      *time.Time
	ReporterEmail  string
}

// errInvalidPIN sengaja sama untuk tiket tidak dikenal dan PIN salah agar tidak
// membocorkan keberadaan tiket (FR-13).
func errInvalidPIN() *httpx.AppError {
	return httpx.NewAppError(http.StatusUnauthorized, httpx.CodeUnauthorized, "Kode tiket atau PIN salah.")
}

// errPINLocked dipakai setelah percobaan gagal melewati batas. Kode 429 sesuai
// panduan; rate limit per IP menutup celah enumerasi tiket.
func errPINLocked() *httpx.AppError {
	return httpx.ErrTooMany("Terlalu banyak percobaan PIN. Silakan coba lagi nanti.")
}

const findByTicketSQL = `
	SELECT r.id, r.case_number, r.status, r.pin_hash, r.pin_failed_count,
	       r.pin_locked_until, r.review_due_at, r.exam_due_at,
	       COALESCE(rp.email, '')
	FROM reports r
	LEFT JOIN reporters rp ON rp.report_id = r.id
	WHERE r.case_number = $1::char(8)`

func (s *Service) findByTicket(ctx context.Context, ticket string) (Report, error) {
	var r Report
	err := s.pool.QueryRow(ctx, findByTicketSQL, ticket).Scan(
		&r.ID,
		&r.CaseNumber,
		&r.Status,
		&r.PinHash,
		&r.PinFailedCount,
		&r.PinLockedUntil,
		&r.ReviewDueAt,
		&r.ExamDueAt,
		&r.ReporterEmail,
	)
	if err != nil {
		return Report{}, err
	}
	return r, nil
}

// VerifyPIN memeriksa kode tiket + PIN dan mengembalikan data minimum laporan.
//
// Agar waktu respons tidak membocorkan keberadaan tiket, PIN untuk tiket yang
// tidak dikenal tetap dibandingkan dengan hash dummy. Percobaan gagal dicatat
// secara atomik; setelah PinMaxAttempts kali, PIN dikunci selama
// PinLockMinutes.
func (s *Service) VerifyPIN(ctx context.Context, ticket, pin string) (Report, error) {
	ticket = strings.TrimSpace(ticket)
	pin = strings.TrimSpace(pin)
	if ticket == "" || pin == "" {
		return Report{}, errInvalidPIN()
	}
	// Tiket selalu 8 digit; tolak format lain sebelum query agar cast ke
	// char(8) tidak memotong masukan yang lebih panjang.
	if len(ticket) != TicketDigits || strings.Trim(ticket, "0123456789") != "" {
		_ = bcrypt.CompareHashAndPassword(s.dummyPinHash, []byte(pin))
		return Report{}, errInvalidPIN()
	}

	r, err := s.findByTicket(ctx, ticket)
	if errors.Is(err, pgx.ErrNoRows) {
		// Samakan waktu respons dengan kasus PIN salah pada tiket yang ada.
		_ = bcrypt.CompareHashAndPassword(s.dummyPinHash, []byte(pin))
		return Report{}, errInvalidPIN()
	}
	if err != nil {
		return Report{}, err
	}

	if r.PinLockedUntil != nil && r.PinLockedUntil.After(s.now()) {
		return Report{}, errPINLocked()
	}

	if bcrypt.CompareHashAndPassword([]byte(r.PinHash), []byte(pin)) != nil {
		if ferr := s.registerPinFailure(ctx, r.ID); ferr != nil {
			// Jangan gagalkan permintaan: PIN tetap ditolak, hanya penghitung
			// yang gagal diperbarui. Dicatat untuk investigasi.
			s.logger.Error("gagal mencatat percobaan PIN", "report_id", r.ID, "err", ferr)
		}
		return Report{}, errInvalidPIN()
	}

	if r.PinFailedCount > 0 || r.PinLockedUntil != nil {
		if err := s.resetPinFailures(ctx, r.ID); err != nil {
			s.logger.Warn("gagal mereset percobaan PIN", "report_id", r.ID, "err", err)
		}
	}
	return r, nil
}

// registerPinFailure menaikkan penghitung dan memasang kunci secara atomik.
// Nilai di sisi kanan selalu nilai lama sehingga aman untuk permintaan paralel.
const registerPinFailureSQL = `
	UPDATE reports SET
		pin_failed_count = CASE WHEN pin_failed_count + 1 >= $2 THEN 0 ELSE pin_failed_count + 1 END,
		pin_locked_until = CASE WHEN pin_failed_count + 1 >= $2
			THEN now() + make_interval(mins => $3)
			ELSE pin_locked_until END
	WHERE id = $1`

func (s *Service) registerPinFailure(ctx context.Context, reportID int64) error {
	_, err := s.pool.Exec(ctx, registerPinFailureSQL, reportID, s.pinMaxAttempts, s.pinLockMinutes)
	return err
}

const resetPinFailuresSQL = `
	UPDATE reports
	SET pin_failed_count = 0, pin_locked_until = NULL
	WHERE id = $1 AND (pin_failed_count > 0 OR pin_locked_until IS NOT NULL)`

func (s *Service) resetPinFailures(ctx context.Context, reportID int64) error {
	_, err := s.pool.Exec(ctx, resetPinFailuresSQL, reportID)
	return err
}
