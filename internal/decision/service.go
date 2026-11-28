// Package decision mengimplementasikan Modul Kesimpulan, Rekomendasi,
// Keputusan, dan Penutupan (M07, FR-36..FR-41).
//
// Alur modul: Satgas PJ menyimpan kesimpulan berstatus DRAF saat kasus berada
// pada tahap PEMERIKSAAN lalu mengajukannya (status kasus menjadi REKOMENDASI);
// Pejabat membaca proyeksi terbatas (tanpa isi pemeriksaan dan identitas pihak)
// dan mencatat keputusan (status KEPUTUSAN); PJ mencatat penyampaian hasil
// (keputusan SANKSI masuk PEMANTAUAN) lalu menutup kasus (SELESAI).
//
// Seluruh operasi tulis berjalan dalam satu transaksi bersama audit, dan status
// kasus hanya berubah lewat workflow.Transition agar status_history selalu
// konsisten.
package decision

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/RidwanRamdhani/ppkpt-backend/internal/audit"
	"github.com/RidwanRamdhani/ppkpt-backend/internal/auth"
	"github.com/RidwanRamdhani/ppkpt-backend/internal/evidence"
	"github.com/RidwanRamdhani/ppkpt-backend/internal/notification"
	"github.com/RidwanRamdhani/ppkpt-backend/internal/platform/db"
	"github.com/RidwanRamdhani/ppkpt-backend/internal/platform/httpx"
	"github.com/RidwanRamdhani/ppkpt-backend/internal/platform/storage"
	"github.com/RidwanRamdhani/ppkpt-backend/internal/workflow"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Actor adalah pemanggil endpoint keputusan. Role menentukan siapa yang boleh
// menulis; UserID menentukan apakah pemanggil adalah PJ kasus.
type Actor struct {
	UserID int64
	Role   string
}

// MemberRef adalah identitas petugas. Tipe ini tidak pernah memuat identitas
// pihak kasus.
type MemberRef struct {
	ID   int64  `json:"id"`
	Name string `json:"name"`
}

// Service melayani operasi kesimpulan, keputusan, penyampaian, penutupan, dan
// unduhan berkas kasus.
type Service struct {
	pool     *pgxpool.Pool
	evidence *evidence.Service
	audit    *audit.Service
	notifier *notification.Service
	flow     *workflow.Service
	logger   *slog.Logger
	now      func() time.Time
}

// NewService membuat layanan keputusan. evidence/auditor/notifier/logger boleh
// nil agar mudah diuji; di produksi seluruhnya diisi.
func NewService(
	pool *pgxpool.Pool,
	evidenceSvc *evidence.Service,
	auditor *audit.Service,
	notifier *notification.Service,
	logger *slog.Logger,
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
		flow:     workflow.NewService(auditor, workflow.Options{}),
		logger:   logger,
		now:      time.Now,
	}
}

// caseAccess adalah data minimum untuk memutuskan boleh-tidaknya menulis,
// memeriksa kelengkapan keputusan, dan mengirim notifikasi status.
type caseAccess struct {
	caseNumber       string
	reporterEmail    string
	status           string
	assignedTo       *int64
	conclusionID     *int64
	conclusionResult *string
	submission       *string
	decisionID       *int64
	decisionType     *string
	deliveries       int
}

const caseAccessSQL = `
SELECT r.case_number, COALESCE(rp.email, ''), r.status, r.assigned_to,
       c.id, c.result, c.submission_status,
       d.id, d.decision_type,
       (SELECT count(*) FROM result_deliveries rd WHERE rd.report_id = r.id)
FROM reports r
LEFT JOIN reporters rp ON rp.report_id = r.id
LEFT JOIN conclusions c ON c.report_id = r.id
LEFT JOIN decisions d ON d.report_id = r.id
WHERE r.id = $1`

func (s *Service) loadCase(ctx context.Context, q db.DBTX, reportID int64) (caseAccess, error) {
	var info caseAccess
	err := q.QueryRow(ctx, caseAccessSQL, reportID).Scan(
		&info.caseNumber, &info.reporterEmail, &info.status, &info.assignedTo,
		&info.conclusionID, &info.conclusionResult, &info.submission,
		&info.decisionID, &info.decisionType, &info.deliveries,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return caseAccess{}, httpx.ErrNotFound()
	}
	if err != nil {
		return caseAccess{}, fmt.Errorf("decision: baca akses kasus: %w", err)
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

// conclusionSubmitted melaporkan apakah kasus sudah memiliki kesimpulan yang
// diajukan. Pejabat hanya boleh melihat kasus pada kondisi ini (FR-38).
func conclusionSubmitted(info caseAccess) bool {
	return info.conclusionID != nil && info.submission != nil && *info.submission == submissionSubmitted
}

// notifyStatusChanged memberi tahu pelapor bahwa status berubah tanpa memuat
// detail kasus. Best-effort: kegagalan email tidak membatalkan perubahan.
func (s *Service) notifyStatusChanged(email, caseNumber string) {
	if s.notifier == nil {
		return
	}
	s.notifier.SendStatusUpdate(email, caseNumber)
}

// logViewed mencatat CASE_VIEWED secara best-effort. Kegagalan audit tidak
// boleh menghalangi petugas membaca kasus.
func (s *Service) logViewed(ctx context.Context, actor Actor, reportID int64, ip string) {
	if s.audit == nil {
		return
	}
	userID := actor.UserID
	err := s.audit.Log(ctx, s.pool, audit.Event{
		ActorType: audit.ActorUser,
		UserID:    &userID,
		Action:    audit.ActionCaseViewed,
		Entity:    "report",
		EntityID:  &reportID,
		IP:        ip,
	})
	if err != nil {
		s.logger.Warn("gagal mencatat audit lihat kasus", "report_id", reportID, "err", err)
	}
}

// DownloadEvidence membuka berkas kasus untuk diunduh setelah memeriksa hak
// akses tingkat kasus. Satgas hanya boleh mengunduh berkas kasus yang ia PJ;
// Pejabat hanya berkas kasus yang kesimpulannya sudah diajukan (D29).
//
// Audit unduhan (fail-closed) dilakukan oleh evidence.Service.Download.
func (s *Service) DownloadEvidence(
	ctx context.Context,
	actor Actor,
	evidenceID int64,
	ip string,
) (evidence.Meta, *storage.Object, error) {
	if s.evidence == nil {
		return evidence.Meta{}, nil, errors.New("decision: layanan evidence tidak tersedia")
	}

	meta, err := s.evidence.Find(ctx, s.pool, evidenceID)
	if err != nil {
		return evidence.Meta{}, nil, err
	}

	info, err := s.loadCase(ctx, s.pool, meta.ReportID)
	if err != nil {
		return evidence.Meta{}, nil, err
	}

	switch actor.Role {
	case auth.RoleSatgas:
		if info.assignedTo == nil || *info.assignedTo != actor.UserID {
			return evidence.Meta{}, nil, httpx.ErrForbidden()
		}
	case auth.RolePejabat:
		if !conclusionSubmitted(info) {
			return evidence.Meta{}, nil, httpx.ErrForbidden()
		}
	default:
		return evidence.Meta{}, nil, httpx.ErrForbidden()
	}

	return s.evidence.Download(ctx, s.pool, evidenceID, evidence.Actor{
		UserID: actor.UserID,
		IP:     ip,
	})
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
