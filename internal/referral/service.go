// Package referral mengimplementasikan Modul Kondisi, Rujukan, dan Pemulihan
// (M06): penanda darurat kasus, rujukan layanan, dan pencatatan pemulihan
// (FR-33..FR-35).
//
// Seluruh operasi tulis hanya boleh dilakukan oleh Satgas yang menjadi
// penanggung jawab (PJ) kasus. Modul ini tidak pernah mengubah status kasus;
// status hanya dibaca untuk memastikan penulisan masih diizinkan. Semua
// integrasi bersifat manual (SDD M06) — tidak ada layanan eksternal.
package referral

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/RidwanRamdhani/ppkpt-backend/internal/audit"
	"github.com/RidwanRamdhani/ppkpt-backend/internal/platform/db"
	"github.com/RidwanRamdhani/ppkpt-backend/internal/platform/httpx"
	"github.com/RidwanRamdhani/ppkpt-backend/internal/workflow"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Actor adalah pemanggil endpoint kondisi, rujukan, dan pemulihan.
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

// Service melayani operasi kondisi, rujukan, dan pemulihan.
type Service struct {
	pool   *pgxpool.Pool
	audit  *audit.Service
	logger *slog.Logger
	now    func() time.Time
}

// NewService membuat layanan referral. auditor dan logger boleh nil agar mudah
// diuji; di produksi keduanya selalu diisi.
func NewService(pool *pgxpool.Pool, auditor *audit.Service, logger *slog.Logger) *Service {
	if auditor == nil {
		auditor = audit.NewService()
	}
	if logger == nil {
		logger = slog.Default()
	}
	return &Service{
		pool:   pool,
		audit:  auditor,
		logger: logger,
		now:    time.Now,
	}
}

// caseAccess adalah data minimum untuk memutuskan boleh-tidaknya menulis.
type caseAccess struct {
	status        string
	assignedTo    *int64
	caseCondition *string
}

const caseAccessSQL = `
SELECT status, assigned_to, case_condition
FROM reports
WHERE id = $1`

func (s *Service) loadCase(ctx context.Context, q db.DBTX, reportID int64) (caseAccess, error) {
	var info caseAccess
	err := q.QueryRow(ctx, caseAccessSQL, reportID).Scan(
		&info.status, &info.assignedTo, &info.caseCondition,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return caseAccess{}, httpx.ErrNotFound()
	}
	if err != nil {
		return caseAccess{}, fmt.Errorf("referral: baca akses kasus: %w", err)
	}
	return info, nil
}

// checkAssignee memastikan kasus belum selesai dan pemanggil adalah PJ.
func checkAssignee(info caseAccess, userID int64) error {
	if err := workflow.EnsureWritable(workflow.Status(info.status)); err != nil {
		return err
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

// conditionWritable melaporkan apakah kondisi kasus boleh diubah pada status
// tersebut (FR-33: PEMERIKSAAN sampai PEMANTAUAN).
func ConditionWritable(status string) bool {
	switch workflow.Status(status) {
	case workflow.Pemeriksaan, workflow.Rekomendasi, workflow.Keputusan, workflow.Pemantauan:
		return true
	default:
		return false
	}
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
