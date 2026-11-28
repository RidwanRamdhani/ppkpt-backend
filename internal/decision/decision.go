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
	"github.com/jackc/pgx/v5/pgconn"
)

// DecisionResult adalah respons POST /api/cases/{id}/decision.
type DecisionResult struct {
	ID             int64     `json:"id"`
	DecisionType   string    `json:"decision_type"`
	SanctionDetail string    `json:"sanction_detail"`
	DecidedAt      string    `json:"decided_at"`
	Official       MemberRef `json:"official"`
	CreatedAt      time.Time `json:"created_at"`
	Status         string    `json:"status"`
}

const decisionSQL = `
SELECT d.id, d.decision_type, COALESCE(d.sanction_detail, ''), d.decided_at,
       d.official_id, u.name, d.created_at
FROM decisions d
JOIN users u ON u.id = d.official_id
WHERE d.report_id = $1`

func (s *Service) loadDecision(ctx context.Context, q db.DBTX, reportID int64) (DecisionResult, error) {
	var (
		view      DecisionResult
		decidedAt time.Time
	)
	err := q.QueryRow(ctx, decisionSQL, reportID).Scan(
		&view.ID,
		&view.DecisionType,
		&view.SanctionDetail,
		&decidedAt,
		&view.Official.ID,
		&view.Official.Name,
		&view.CreatedAt,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return DecisionResult{}, httpx.ErrNotFound()
	}
	if err != nil {
		return DecisionResult{}, fmt.Errorf("decision: baca keputusan: %w", err)
	}
	view.DecidedAt = decidedAt.In(deadline.Jakarta).Format(dateLayout)
	return view, nil
}

// RecordDecision mencatat keputusan Pejabat atas kasus yang sudah diajukan
// (FR-39).
//
// Hanya Pejabat yang boleh mencatat. Kasus harus berada pada tahap REKOMENDASI,
// dan satu kasus hanya memiliki satu keputusan (UNIQUE report_id): permintaan
// ganda ditolak 409. Sistem tidak menentukan sanksi otomatis (BRU-04).
func (s *Service) RecordDecision(
	ctx context.Context,
	actor Actor,
	reportID int64,
	req DecisionRequest,
	ip string,
) (DecisionResult, error) {
	if actor.Role != auth.RolePejabat {
		return DecisionResult{}, httpx.ErrForbidden()
	}
	input, appErr := req.Validate(s.now())
	if appErr != nil {
		return DecisionResult{}, appErr
	}

	var (
		access caseAccess
		result DecisionResult
	)
	err := db.WithTx(ctx, s.pool, func(tx pgx.Tx) error {
		var err error
		access, err = s.loadCase(ctx, tx, reportID)
		if err != nil {
			return err
		}
		if access.status != string(workflow.Rekomendasi) {
			return httpx.ErrConflict("Keputusan hanya dapat dicatat saat status REKOMENDASI.")
		}
		if !conclusionSubmitted(access) {
			return httpx.ErrConflict("Kesimpulan kasus belum diajukan.")
		}

		if _, err := tx.Exec(ctx, `
			INSERT INTO decisions
				(report_id, official_id, decision_type, sanction_detail, decided_at)
			VALUES ($1, $2, $3, $4, $5)`,
			reportID,
			actor.UserID,
			input.DecisionType,
			nullIfEmpty(input.SanctionDetail),
			input.DecidedAt,
		); err != nil {
			var pgErr *pgconn.PgError
			if errors.As(err, &pgErr) && pgErr.Code == "23505" {
				return httpx.ErrConflict("Keputusan untuk kasus ini sudah tercatat.")
			}
			return fmt.Errorf("decision: simpan keputusan: %w", err)
		}

		if err := s.flow.Transition(ctx, tx, reportID,
			workflow.Rekomendasi, workflow.Keputusan, &actor.UserID,
		); err != nil {
			return err
		}

		if err := s.audit.Log(ctx, tx, audit.Event{
			ActorType: audit.ActorUser,
			UserID:    &actor.UserID,
			Action:    audit.ActionDecisionRecorded,
			Entity:    "report",
			EntityID:  &reportID,
			IP:        ip,
			Detail: map[string]any{
				"decision_type":     input.DecisionType,
				"conclusion_result": derefString(access.conclusionResult),
			},
		}); err != nil {
			return err
		}

		// Dibaca di dalam transaksi agar kegagalan baca membatalkan tulisan.
		loaded, err := s.loadDecision(ctx, tx, reportID)
		if err != nil {
			return err
		}
		result = loaded
		return nil
	})
	if err != nil {
		return DecisionResult{}, err
	}

	result.Status = string(workflow.Keputusan)
	s.notifyStatusChanged(access.reporterEmail, access.caseNumber)
	return result, nil
}
