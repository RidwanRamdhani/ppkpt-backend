// Package review mengimplementasikan Modul Penelaahan dan Pemeriksaan (M05):
// catatan penelaahan, transisi ke pemeriksaan atau penutupan, sesi pemeriksaan,
// unggah BAI/lampiran, serta penjadwal peringatan tenggat (FR-28..FR-32).
//
// Seluruh operasi tulis hanya boleh dilakukan oleh Satgas yang menjadi
// penanggung jawab (PJ) kasus. Status kasus diperiksa sebelum menulis agar
// urutan tahap tetap konsisten dengan state machine workflow.
package review

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/RidwanRamdhani/ppkpt-backend/internal/audit"
	"github.com/RidwanRamdhani/ppkpt-backend/internal/evidence"
	"github.com/RidwanRamdhani/ppkpt-backend/internal/notification"
	"github.com/RidwanRamdhani/ppkpt-backend/internal/platform/db"
	"github.com/RidwanRamdhani/ppkpt-backend/internal/platform/httpx"
	"github.com/RidwanRamdhani/ppkpt-backend/internal/workflow"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Actor adalah pemanggil endpoint penelaahan/pemeriksaan.
type Actor struct {
	UserID int64
	Role   string
}

// MemberRef adalah identitas petugas Satgas. Tipe ini tidak pernah memuat
// identitas pihak kasus.
type MemberRef struct {
	ID   int64  `json:"id"`
	Name string `json:"name"`
}

// Options mengatur tenggat tahap yang dipasang state machine saat transisi.
// Nilai <= 0 memakai default workflow (7 hari penelaahan, 30 hari pemeriksaan).
type Options struct {
	ReviewDueDays int
	ExamDueDays   int
}

// Service melayani operasi penelaahan dan pemeriksaan.
type Service struct {
	pool     *pgxpool.Pool
	evidence *evidence.Service
	audit    *audit.Service
	notifier *notification.Service
	flow     *workflow.Service
	logger   *slog.Logger
	now      func() time.Time
}

// NewService membuat layanan review. evidence/auditor/notifier/logger boleh nil
// agar mudah diuji; di produksi seluruhnya diisi.
func NewService(
	pool *pgxpool.Pool,
	evidenceSvc *evidence.Service,
	auditor *audit.Service,
	notifier *notification.Service,
	logger *slog.Logger,
	opts Options,
) *Service {
	if auditor == nil {
		auditor = audit.NewService()
	}
	if logger == nil {
		logger = slog.Default()
	}
	return &Service{
		pool:     pool,
		evidence: evidenceSvc,
		audit:    auditor,
		notifier: notifier,
		flow: workflow.NewService(auditor, workflow.Options{
			ReviewDueDays: opts.ReviewDueDays,
			ExamDueDays:   opts.ExamDueDays,
		}),
		logger: logger,
		now:    time.Now,
	}
}

// caseAccess adalah data minimum untuk memutuskan boleh-tidaknya menulis dan
// mengirim notifikasi status.
type caseAccess struct {
	caseNumber    string
	reporterEmail string
	status        string
	assignedTo    *int64
}

const caseAccessSQL = `
SELECT r.case_number, COALESCE(rp.email, ''), r.status, r.assigned_to
FROM reports r
LEFT JOIN reporters rp ON rp.report_id = r.id
WHERE r.id = $1`

func (s *Service) loadCase(ctx context.Context, q db.DBTX, reportID int64) (caseAccess, error) {
	var info caseAccess
	err := q.QueryRow(ctx, caseAccessSQL, reportID).Scan(
		&info.caseNumber, &info.reporterEmail, &info.status, &info.assignedTo,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return caseAccess{}, httpx.ErrNotFound()
	}
	if err != nil {
		return caseAccess{}, fmt.Errorf("review: baca akses kasus: %w", err)
	}
	return info, nil
}

// checkAssignee memastikan kasus belum selesai dan pemanggil adalah PJ.
func checkAssignee(info caseAccess, userID int64) error {
	if info.status == string(workflow.Selesai) {
		return httpx.ErrConflict("Kasus sudah selesai dan tidak dapat diubah.")
	}
	if info.assignedTo == nil || *info.assignedTo != userID {
		return httpx.ErrForbidden()
	}
	return nil
}

func (s *Service) requireAssignee(ctx context.Context, q db.DBTX, reportID, userID int64) (caseAccess, error) {
	info, err := s.loadCase(ctx, q, reportID)
	if err != nil {
		return caseAccess{}, err
	}
	if err := checkAssignee(info, userID); err != nil {
		return caseAccess{}, err
	}
	return info, nil
}

// notifyStatusChanged memberi tahu pelapor bahwa status berubah tanpa memuat
// detail kasus. Best-effort: kegagalan email tidak membatalkan perubahan.
func (s *Service) notifyStatusChanged(email, caseNumber string) {
	if s.notifier == nil {
		return
	}
	s.notifier.SendStatusUpdate(email, caseNumber)
}

func trimText(value string) string {
	return strings.TrimSpace(value)
}

func nullIfEmpty(value string) *string {
	if value == "" {
		return nil
	}
	return &value
}
