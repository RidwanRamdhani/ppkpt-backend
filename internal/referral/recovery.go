package referral

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/RidwanRamdhani/ppkpt-backend/internal/audit"
	"github.com/RidwanRamdhani/ppkpt-backend/internal/auth"
	"github.com/RidwanRamdhani/ppkpt-backend/internal/deadline"
	"github.com/RidwanRamdhani/ppkpt-backend/internal/platform/db"
	"github.com/RidwanRamdhani/ppkpt-backend/internal/platform/httpx"
	"github.com/jackc/pgx/v5"
)

// RecoveryView adalah satu catatan pemulihan yang tersimpan (FR-35).
type RecoveryView struct {
	ID         int64     `json:"id"`
	Type       string    `json:"type"`
	Note       string    `json:"note"`
	RecordedAt string    `json:"recorded_at"`
	CreatedBy  MemberRef `json:"created_by"`
}

const recoveryByIDSQL = `
SELECT rm.id, rm.type, rm.note, rm.recorded_at, rm.created_by, u.name
FROM recovery_monitorings rm
JOIN users u ON u.id = rm.created_by
WHERE rm.id = $1 AND rm.report_id = $2`

func (s *Service) loadRecovery(ctx context.Context, q db.DBTX, reportID, recoveryID int64) (RecoveryView, error) {
	var (
		view       RecoveryView
		recordedAt time.Time
	)
	err := q.QueryRow(ctx, recoveryByIDSQL, recoveryID, reportID).Scan(
		&view.ID,
		&view.Type,
		&view.Note,
		&recordedAt,
		&view.CreatedBy.ID,
		&view.CreatedBy.Name,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return RecoveryView{}, httpx.ErrNotFound()
	}
	if err != nil {
		return RecoveryView{}, fmt.Errorf("referral: baca pemulihan: %w", err)
	}
	view.RecordedAt = recordedAt.In(deadline.Jakarta).Format(dateLayout)
	return view, nil
}

// RecordRecovery mencatat pemantauan, evaluasi, atau pencegahan (FR-35).
//
// SRS tidak membatasi tahap pencatatan, sehingga hanya kasus SELESAI yang
// ditolak (409). Catatan wajib diisi karena kolom note NOT NULL.
func (s *Service) RecordRecovery(
	ctx context.Context,
	actor Actor,
	reportID int64,
	req RecoveryRequest,
	ip string,
) (RecoveryView, error) {
	if actor.Role != auth.RoleSatgas {
		return RecoveryView{}, httpx.ErrForbidden()
	}
	input, appErr := req.Validate(s.now())
	if appErr != nil {
		return RecoveryView{}, appErr
	}

	var view RecoveryView
	err := db.WithTx(ctx, s.pool, func(tx pgx.Tx) error {
		if _, err := s.requireAssignee(ctx, tx, reportID, actor.UserID); err != nil {
			return err
		}

		var recoveryID int64
		if err := tx.QueryRow(ctx, `
			INSERT INTO recovery_monitorings (report_id, type, note, recorded_at, created_by)
			VALUES ($1, $2, $3, $4, $5)
			RETURNING id`,
			reportID, input.Type, input.Note, input.RecordedAt, actor.UserID,
		).Scan(&recoveryID); err != nil {
			return fmt.Errorf("referral: simpan pemulihan: %w", err)
		}

		if err := s.audit.Log(ctx, tx, audit.Event{
			ActorType: audit.ActorUser,
			UserID:    &actor.UserID,
			Action:    audit.ActionRecoveryRecorded,
			Entity:    "report",
			EntityID:  &reportID,
			IP:        ip,
			Detail: map[string]any{
				"recovery_id": recoveryID,
				"type":        input.Type,
			},
		}); err != nil {
			return err
		}

		// Dibaca di dalam transaksi: bila pembacaan gagal, seluruh tulisan ikut
		// batal, sehingga klien tidak menerima 500 dengan data sudah tersimpan.
		loaded, err := s.loadRecovery(ctx, tx, reportID, recoveryID)
		if err != nil {
			return err
		}
		view = loaded
		return nil
	})
	if err != nil {
		return RecoveryView{}, err
	}
	return view, nil
}
