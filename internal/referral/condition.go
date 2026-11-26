package referral

import (
	"context"
	"fmt"

	"github.com/RidwanRamdhani/ppkpt-backend/internal/audit"
	"github.com/RidwanRamdhani/ppkpt-backend/internal/auth"
	"github.com/RidwanRamdhani/ppkpt-backend/internal/platform/db"
	"github.com/RidwanRamdhani/ppkpt-backend/internal/platform/httpx"
	"github.com/jackc/pgx/v5"
)

// ConditionView adalah respons PUT /api/cases/{id}/condition.
type ConditionView struct {
	CaseCondition string `json:"case_condition"`
}

// SetCondition menetapkan penanda darurat kasus (FR-33).
//
// Hanya PJ kasus yang boleh menulis, dan hanya pada status PEMERIKSAAN sampai
// PEMANTAUAN. Perubahan disimpan bersama audit CASE_CONDITION_CHANGED dalam
// satu transaksi; kolom status tidak pernah diubah modul ini.
func (s *Service) SetCondition(
	ctx context.Context,
	actor Actor,
	reportID int64,
	req ConditionRequest,
	ip string,
) (ConditionView, error) {
	if actor.Role != auth.RoleSatgas {
		return ConditionView{}, httpx.ErrForbidden()
	}
	input, appErr := req.Validate()
	if appErr != nil {
		return ConditionView{}, appErr
	}

	err := db.WithTx(ctx, s.pool, func(tx pgx.Tx) error {
		access, err := s.requireAssignee(ctx, tx, reportID, actor.UserID)
		if err != nil {
			return err
		}
		if !ConditionWritable(access.status) {
			return httpx.ErrConflict("Kondisi kasus hanya dapat diubah pada tahap PEMERIKSAAN sampai PEMANTAUAN.")
		}

		// Klausa status membuat perubahan tetap aman bila status berubah di
		// antara pembacaan dan penulisan (permintaan lain menang lebih dulu).
		tag, err := tx.Exec(ctx, `
			UPDATE reports
			SET case_condition = $2, updated_at = now()
			WHERE id = $1 AND status = $3`,
			reportID, input.Condition, access.status,
		)
		if err != nil {
			return fmt.Errorf("referral: simpan kondisi kasus: %w", err)
		}
		if tag.RowsAffected() == 0 {
			return httpx.ErrConflict("Status kasus sudah berubah. Muat ulang data lalu coba lagi.")
		}

		return s.audit.Log(ctx, tx, audit.Event{
			ActorType: audit.ActorUser,
			UserID:    &actor.UserID,
			Action:    audit.ActionCaseConditionChanged,
			Entity:    "report",
			EntityID:  &reportID,
			IP:        ip,
			Detail: map[string]any{
				"from": access.caseCondition,
				"to":   input.Condition,
			},
		})
	})
	if err != nil {
		return ConditionView{}, err
	}
	return ConditionView{CaseCondition: input.Condition}, nil
}
