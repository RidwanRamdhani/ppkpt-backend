package decision

import (
	"context"
	"fmt"
	"time"

	"github.com/RidwanRamdhani/ppkpt-backend/internal/audit"
	"github.com/RidwanRamdhani/ppkpt-backend/internal/auth"
	"github.com/RidwanRamdhani/ppkpt-backend/internal/platform/db"
	"github.com/RidwanRamdhani/ppkpt-backend/internal/platform/httpx"
	"github.com/RidwanRamdhani/ppkpt-backend/internal/workflow"
	"github.com/jackc/pgx/v5"
)

// CloseResult adalah respons POST /api/cases/{id}/close.
type CloseResult struct {
	Status   string     `json:"status"`
	ClosedAt *time.Time `json:"closed_at"`
}

// checkClosePrerequisites memeriksa syarat penutupan kasus (FR-40/FR-41):
// keputusan SANKSI harus sudah memasuki PEMANTAUAN dan memiliki penyampaian
// hasil; keputusan TIDAK_TERBUKTI harus memiliki penyampaian dengan pemulihan
// nama baik (BRU-05).
func checkClosePrerequisites(decisionType, status string, hasDelivery, hasRestored bool) error {
	switch decisionType {
	case decisionSanksi:
		if status != string(workflow.Pemantauan) {
			return httpx.ErrConflict("Kasus bersanksi hanya dapat ditutup setelah memasuki tahap PEMANTAUAN.")
		}
		if !hasDelivery {
			return httpx.ErrConflict("Catat minimal satu penyampaian hasil sebelum menutup kasus.")
		}
	case decisionTidakTerbukti:
		if !hasRestored {
			return httpx.ErrConflict("Catat penyampaian hasil dengan pemulihan nama baik sebelum menutup kasus.")
		}
	default:
		return httpx.ErrConflict("Kasus belum memiliki keputusan.")
	}
	return nil
}

// CloseCase menutup kasus dan menjadikannya hanya-baca (FR-40/FR-41).
//
// Seluruh syarat diperiksa di dalam transaksi yang sama dengan transisi status
// dan audit, sehingga kasus tidak mungkin tertutup tanpa jejak yang lengkap.
func (s *Service) CloseCase(
	ctx context.Context,
	actor Actor,
	reportID int64,
	ip string,
) (CloseResult, error) {
	if actor.Role != auth.RoleSatgas {
		return CloseResult{}, httpx.ErrForbidden()
	}

	var (
		access   caseAccess
		closedAt *time.Time
	)
	err := db.WithTx(ctx, s.pool, func(tx pgx.Tx) error {
		var err error
		access, err = s.requireAssignee(ctx, tx, reportID, actor.UserID)
		if err != nil {
			return err
		}
		if access.decisionID == nil || access.decisionType == nil {
			return httpx.ErrConflict("Kasus belum memiliki keputusan.")
		}

		var hasRestored bool
		if err := tx.QueryRow(ctx, `
			SELECT EXISTS (
				SELECT 1 FROM result_deliveries
				WHERE report_id = $1 AND name_restored
			)`, reportID,
		).Scan(&hasRestored); err != nil {
			return fmt.Errorf("decision: periksa pemulihan nama baik: %w", err)
		}

		if err := checkClosePrerequisites(
			*access.decisionType, access.status, access.deliveries > 0, hasRestored,
		); err != nil {
			return err
		}

		if err := s.flow.Transition(ctx, tx, reportID,
			workflow.Status(access.status), workflow.Selesai, &actor.UserID,
		); err != nil {
			return err
		}

		if err := s.audit.Log(ctx, tx, audit.Event{
			ActorType: audit.ActorUser,
			UserID:    &actor.UserID,
			Action:    audit.ActionCaseClosed,
			Entity:    "report",
			EntityID:  &reportID,
			IP:        ip,
			Detail: map[string]any{
				"decision_type": *access.decisionType,
			},
		}); err != nil {
			return err
		}

		if err := tx.QueryRow(ctx,
			`SELECT closed_at FROM reports WHERE id = $1`, reportID,
		).Scan(&closedAt); err != nil {
			return fmt.Errorf("decision: baca waktu penutupan: %w", err)
		}
		return nil
	})
	if err != nil {
		return CloseResult{}, err
	}

	s.notifyStatusChanged(access.reporterEmail, access.caseNumber)
	return CloseResult{Status: string(workflow.Selesai), ClosedAt: closedAt}, nil
}
