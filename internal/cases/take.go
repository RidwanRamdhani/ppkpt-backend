package cases

import (
	"context"
	"errors"
	"fmt"

	"github.com/RidwanRamdhani/ppkpt-backend/internal/audit"
	"github.com/RidwanRamdhani/ppkpt-backend/internal/auth"
	"github.com/RidwanRamdhani/ppkpt-backend/internal/platform/db"
	"github.com/RidwanRamdhani/ppkpt-backend/internal/platform/httpx"
	"github.com/RidwanRamdhani/ppkpt-backend/internal/workflow"
	"github.com/jackc/pgx/v5"
)

// TakeResult adalah respons POST /api/cases/{id}/take.
type TakeResult struct {
	AssignedTo Assignee `json:"assigned_to"`
}

const takeCaseSQL = `
	UPDATE reports
	SET assigned_to = $1, updated_at = now()
	WHERE id = $2 AND assigned_to IS NULL AND status <> 'SELESAI'
	RETURNING id`

// Take menetapkan pemanggil sebagai penanggung jawab kasus.
//
// UPDATE bersyarat membuat operasi atomik: dari banyak permintaan bersamaan,
// hanya satu yang mendapat baris. Audit ditulis dalam transaksi yang sama agar
// pengambilan kasus tidak pernah terjadi tanpa jejak.
func (s *Service) Take(ctx context.Context, actor Actor, reportID int64, ip string) (TakeResult, error) {
	if actor.Role != auth.RoleSatgas {
		return TakeResult{}, httpx.ErrForbidden()
	}

	var name string
	err := db.WithTx(ctx, s.pool, func(tx pgx.Tx) error {
		var id int64
		err := tx.QueryRow(ctx, takeCaseSQL, actor.UserID, reportID).Scan(&id)
		if errors.Is(err, pgx.ErrNoRows) {
			return s.takeConflict(ctx, tx, reportID)
		}
		if err != nil {
			return fmt.Errorf("cases: ambil kasus: %w", err)
		}

		if err := tx.QueryRow(ctx, `SELECT name FROM users WHERE id = $1`, actor.UserID).Scan(&name); err != nil {
			return fmt.Errorf("cases: baca nama penanggung jawab: %w", err)
		}

		return s.audit.Log(ctx, tx, audit.Event{
			ActorType: audit.ActorUser,
			UserID:    &actor.UserID,
			Action:    audit.ActionCaseTaken,
			Entity:    "report",
			EntityID:  &reportID,
			IP:        ip,
		})
	})
	if err != nil {
		return TakeResult{}, err
	}
	return TakeResult{AssignedTo: Assignee{ID: actor.UserID, Name: name}}, nil
}

// takeConflict menjelaskan mengapa UPDATE tidak mengenai baris: kasus tidak
// ada, sudah selesai, atau sudah diambil petugas lain.
func (s *Service) takeConflict(ctx context.Context, tx pgx.Tx, reportID int64) error {
	info, err := s.loadAccessWith(ctx, tx, reportID)
	if err != nil {
		return err
	}
	if info.status == string(workflow.Selesai) {
		return httpx.ErrConflict("Kasus sudah selesai dan tidak dapat diambil.")
	}
	if info.assignedTo != nil {
		appErr := httpx.ErrConflict("Kasus sudah diambil oleh petugas lain.")
		var pjName string
		if err := tx.QueryRow(ctx, `SELECT name FROM users WHERE id = $1`, *info.assignedTo).Scan(&pjName); err == nil && pjName != "" {
			appErr.Fields = map[string]string{"pj_name": pjName}
		}
		return appErr
	}
	return httpx.ErrConflict("Kasus tidak dapat diambil saat ini.")
}
