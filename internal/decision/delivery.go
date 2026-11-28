package decision

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
	"github.com/RidwanRamdhani/ppkpt-backend/internal/workflow"
	"github.com/jackc/pgx/v5"
)

// DeliveryResult adalah respons POST /api/cases/{id}/delivery.
type DeliveryResult struct {
	ID           int64     `json:"id"`
	DeliveredTo  string    `json:"delivered_to"`
	DeliveredAt  string    `json:"delivered_at"`
	NameRestored bool      `json:"name_restored"`
	Note         string    `json:"note"`
	CreatedBy    MemberRef `json:"created_by"`
	Status       string    `json:"status"`
}

const deliverySQL = `
SELECT rd.id, rd.delivered_to, rd.delivered_at, rd.name_restored,
       COALESCE(rd.note, ''), rd.created_by, u.name
FROM result_deliveries rd
JOIN users u ON u.id = rd.created_by
WHERE rd.id = $1 AND rd.report_id = $2`

func (s *Service) loadDelivery(ctx context.Context, q db.DBTX, reportID, deliveryID int64) (DeliveryResult, error) {
	var (
		view        DeliveryResult
		deliveredAt time.Time
	)
	err := q.QueryRow(ctx, deliverySQL, deliveryID, reportID).Scan(
		&view.ID,
		&view.DeliveredTo,
		&deliveredAt,
		&view.NameRestored,
		&view.Note,
		&view.CreatedBy.ID,
		&view.CreatedBy.Name,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return DeliveryResult{}, httpx.ErrNotFound()
	}
	if err != nil {
		return DeliveryResult{}, fmt.Errorf("decision: baca penyampaian hasil: %w", err)
	}
	view.DeliveredAt = deliveredAt.In(deadline.Jakarta).Format(dateLayout)
	return view, nil
}

// deliveryWritable melaporkan apakah penyampaian hasil boleh dicatat. Penyampaian
// hanya bermakna setelah keputusan ada dan sebelum kasus ditutup (FR-41).
func deliveryWritable(status string) bool {
	switch workflow.Status(status) {
	case workflow.Keputusan, workflow.Pemantauan:
		return true
	default:
		return false
	}
}

// RecordDelivery mencatat penyampaian hasil kepada pihak terkait (FR-41).
//
// Untuk keputusan SANKSI, penyampaian pertama memindahkan kasus dari KEPUTUSAN
// ke PEMANTAUAN (D11). Keputusan TIDAK_TERBUKTI tetap pada KEPUTUSAN sampai
// kasus ditutup.
func (s *Service) RecordDelivery(
	ctx context.Context,
	actor Actor,
	reportID int64,
	req DeliveryRequest,
	ip string,
) (DeliveryResult, error) {
	if actor.Role != auth.RoleSatgas {
		return DeliveryResult{}, httpx.ErrForbidden()
	}
	input, appErr := req.Validate(s.now())
	if appErr != nil {
		return DeliveryResult{}, appErr
	}

	var (
		access caseAccess
		result DeliveryResult
		status string
	)
	err := db.WithTx(ctx, s.pool, func(tx pgx.Tx) error {
		var err error
		access, err = s.requireAssignee(ctx, tx, reportID, actor.UserID)
		if err != nil {
			return err
		}
		if !deliveryWritable(access.status) {
			return httpx.ErrConflict("Penyampaian hasil hanya dapat dicatat saat status KEPUTUSAN atau PEMANTAUAN.")
		}
		if access.decisionID == nil || access.decisionType == nil {
			return httpx.ErrConflict("Kasus belum memiliki keputusan.")
		}

		var deliveryID int64
		if err := tx.QueryRow(ctx, `
			INSERT INTO result_deliveries
				(report_id, delivered_to, delivered_at, name_restored, note, created_by)
			VALUES ($1, $2, $3, $4, $5, $6)
			RETURNING id`,
			reportID,
			input.DeliveredTo,
			input.DeliveredAt,
			input.NameRestored,
			nullIfEmpty(input.Note),
			actor.UserID,
		).Scan(&deliveryID); err != nil {
			return fmt.Errorf("decision: simpan penyampaian hasil: %w", err)
		}

		status = access.status
		firstDelivery := access.deliveries == 0
		if *access.decisionType == decisionSanksi &&
			firstDelivery &&
			access.status == string(workflow.Keputusan) {
			if err := s.flow.Transition(ctx, tx, reportID,
				workflow.Keputusan, workflow.Pemantauan, &actor.UserID,
			); err != nil {
				return err
			}
			status = string(workflow.Pemantauan)
		}

		if err := s.audit.Log(ctx, tx, audit.Event{
			ActorType: audit.ActorUser,
			UserID:    &actor.UserID,
			Action:    audit.ActionDeliveryRecorded,
			Entity:    "report",
			EntityID:  &reportID,
			IP:        ip,
			Detail: map[string]any{
				"delivery_id":    deliveryID,
				"name_restored":  input.NameRestored,
				"first_delivery": firstDelivery,
			},
		}); err != nil {
			return err
		}

		loaded, err := s.loadDelivery(ctx, tx, reportID, deliveryID)
		if err != nil {
			return err
		}
		result = loaded
		return nil
	})
	if err != nil {
		return DeliveryResult{}, err
	}

	result.Status = status
	if status == string(workflow.Pemantauan) {
		s.notifyStatusChanged(access.reporterEmail, access.caseNumber)
	}
	return result, nil
}
