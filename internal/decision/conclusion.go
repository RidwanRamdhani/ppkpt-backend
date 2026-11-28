package decision

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/RidwanRamdhani/ppkpt-backend/internal/audit"
	"github.com/RidwanRamdhani/ppkpt-backend/internal/auth"
	"github.com/RidwanRamdhani/ppkpt-backend/internal/platform/db"
	"github.com/RidwanRamdhani/ppkpt-backend/internal/platform/httpx"
	"github.com/RidwanRamdhani/ppkpt-backend/internal/workflow"
	"github.com/jackc/pgx/v5"
)

// RecommendationView adalah satu rekomendasi tersimpan.
type RecommendationView struct {
	ID          int64  `json:"id"`
	Category    string `json:"category"`
	Severity    string `json:"severity"`
	Description string `json:"description"`
}

// ConclusionView adalah kesimpulan beserta rekomendasinya (FR-36/FR-37).
type ConclusionView struct {
	ID               int64                `json:"id"`
	Result           string               `json:"result"`
	Summary          string               `json:"summary"`
	SubmissionStatus string               `json:"submission_status"`
	SubmittedAt      *time.Time           `json:"submitted_at"`
	CreatedBy        MemberRef            `json:"created_by"`
	CreatedAt        time.Time            `json:"created_at"`
	Recommendations  []RecommendationView `json:"recommendations"`
}

// SubmitResult adalah respons POST /api/cases/{id}/conclusion/submit.
type SubmitResult struct {
	Status      string     `json:"status"`
	SubmittedAt *time.Time `json:"submitted_at"`
}

const conclusionSQL = `
SELECT c.id, c.result, c.summary, c.submission_status, c.submitted_at,
       c.created_by, u.name, c.created_at
FROM conclusions c
JOIN users u ON u.id = c.created_by
WHERE c.report_id = $1`

func loadConclusion(ctx context.Context, q db.DBTX, reportID int64) (ConclusionView, error) {
	var view ConclusionView
	err := q.QueryRow(ctx, conclusionSQL, reportID).Scan(
		&view.ID,
		&view.Result,
		&view.Summary,
		&view.SubmissionStatus,
		&view.SubmittedAt,
		&view.CreatedBy.ID,
		&view.CreatedBy.Name,
		&view.CreatedAt,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return ConclusionView{}, httpx.ErrNotFound()
	}
	if err != nil {
		return ConclusionView{}, fmt.Errorf("decision: baca kesimpulan: %w", err)
	}

	recommendations, err := loadRecommendations(ctx, q, view.ID)
	if err != nil {
		return ConclusionView{}, err
	}
	view.Recommendations = recommendations
	return view, nil
}

const recommendationsSQL = `
SELECT id, category, severity, COALESCE(description, '')
FROM recommendations
WHERE conclusion_id = $1
ORDER BY id`

func loadRecommendations(ctx context.Context, q db.DBTX, conclusionID int64) ([]RecommendationView, error) {
	rows, err := q.Query(ctx, recommendationsSQL, conclusionID)
	if err != nil {
		return nil, fmt.Errorf("decision: baca rekomendasi: %w", err)
	}
	defer rows.Close()

	items := make([]RecommendationView, 0, 4)
	for rows.Next() {
		var item RecommendationView
		if err := rows.Scan(&item.ID, &item.Category, &item.Severity, &item.Description); err != nil {
			return nil, fmt.Errorf("decision: baca rekomendasi: %w", err)
		}
		items = append(items, item)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("decision: iterasi rekomendasi: %w", err)
	}
	return items, nil
}

// SaveConclusion menyimpan atau memperbarui kesimpulan berstatus DRAF
// (FR-36/FR-37).
//
// Syarat: pemanggil PJ kasus, status PEMERIKSAAN, sudah ada minimal satu sesi
// pemeriksaan dan satu berkas BAI (FR-36 bergantung FR-31), serta kesimpulan
// belum diajukan. Daftar rekomendasi diganti seluruhnya dalam transaksi yang
// sama. Respons dibaca kembali di dalam transaksi agar kegagalan baca ikut
// membatalkan tulisan.
func (s *Service) SaveConclusion(
	ctx context.Context,
	actor Actor,
	reportID int64,
	req ConclusionRequest,
	ip string,
) (ConclusionView, error) {
	if actor.Role != auth.RoleSatgas {
		return ConclusionView{}, httpx.ErrForbidden()
	}
	input, appErr := req.Validate()
	if appErr != nil {
		return ConclusionView{}, appErr
	}

	var view ConclusionView
	err := db.WithTx(ctx, s.pool, func(tx pgx.Tx) error {
		access, err := s.requireAssignee(ctx, tx, reportID, actor.UserID)
		if err != nil {
			return err
		}
		if access.status != string(workflow.Pemeriksaan) {
			return httpx.ErrConflict("Kesimpulan hanya dapat disimpan saat status PEMERIKSAAN.")
		}

		if err := ensureConclusionPrerequisites(ctx, tx, reportID); err != nil {
			return err
		}

		var conclusionID int64
		err = tx.QueryRow(ctx, `
			INSERT INTO conclusions (report_id, result, summary, submission_status, created_by)
			VALUES ($1, $2, $3, 'DRAF', $4)
			ON CONFLICT (report_id) DO UPDATE
			SET result = EXCLUDED.result,
			    summary = EXCLUDED.summary,
			    created_by = EXCLUDED.created_by
			WHERE conclusions.submission_status = 'DRAF'
			RETURNING id`,
			reportID, input.Result, input.Summary, actor.UserID,
		).Scan(&conclusionID)
		if errors.Is(err, pgx.ErrNoRows) {
			return httpx.ErrConflict("Kesimpulan yang sudah diajukan tidak dapat diubah.")
		}
		if err != nil {
			return fmt.Errorf("decision: simpan kesimpulan: %w", err)
		}

		if _, err := tx.Exec(ctx,
			`DELETE FROM recommendations WHERE conclusion_id = $1`, conclusionID,
		); err != nil {
			return fmt.Errorf("decision: ganti rekomendasi: %w", err)
		}
		for _, rec := range input.Recommendations {
			if _, err := tx.Exec(ctx, `
				INSERT INTO recommendations (conclusion_id, category, severity, description)
				VALUES ($1, $2, $3, $4)`,
				conclusionID, rec.Category, rec.Severity, nullIfEmpty(rec.Description),
			); err != nil {
				return fmt.Errorf("decision: simpan rekomendasi: %w", err)
			}
		}

		if err := s.audit.Log(ctx, tx, audit.Event{
			ActorType: audit.ActorUser,
			UserID:    &actor.UserID,
			Action:    audit.ActionConclusionSaved,
			Entity:    "report",
			EntityID:  &reportID,
			IP:        ip,
			Detail: map[string]any{
				"result":               input.Result,
				"recommendation_count": len(input.Recommendations),
			},
		}); err != nil {
			return err
		}

		// Dibaca di dalam transaksi: bila pembacaan gagal, seluruh tulisan ikut
		// batal sehingga klien tidak menerima 500 dengan data sudah tersimpan.
		loaded, err := loadConclusion(ctx, tx, reportID)
		if err != nil {
			return err
		}
		view = loaded
		return nil
	})
	if err != nil {
		return ConclusionView{}, err
	}
	return view, nil
}

// ensureConclusionPrerequisites memastikan minimal satu sesi pemeriksaan dan
// satu berkas BAI sudah ada sebelum kesimpulan disusun (FR-36).
func ensureConclusionPrerequisites(ctx context.Context, q db.DBTX, reportID int64) error {
	var hasSession, hasBAI bool
	if err := q.QueryRow(ctx, `
		SELECT EXISTS (SELECT 1 FROM examination_sessions WHERE report_id = $1),
		       EXISTS (SELECT 1 FROM evidences WHERE report_id = $1 AND category = 'BAI')`,
		reportID,
	).Scan(&hasSession, &hasBAI); err != nil {
		return fmt.Errorf("decision: periksa prasyarat kesimpulan: %w", err)
	}
	if !hasSession {
		return httpx.ErrConflict("Catat minimal satu sesi pemeriksaan sebelum menyusun kesimpulan.")
	}
	if !hasBAI {
		return httpx.ErrConflict("Unggah minimal satu berkas BAI sebelum menyusun kesimpulan.")
	}
	return nil
}

// SubmitConclusion mengajukan kesimpulan yang masih DRAF dan memindahkan kasus
// ke tahap REKOMENDASI (FR-37).
func (s *Service) SubmitConclusion(
	ctx context.Context,
	actor Actor,
	reportID int64,
	ip string,
) (SubmitResult, error) {
	if actor.Role != auth.RoleSatgas {
		return SubmitResult{}, httpx.ErrForbidden()
	}

	var (
		access      caseAccess
		submittedAt *time.Time
	)
	err := db.WithTx(ctx, s.pool, func(tx pgx.Tx) error {
		var err error
		access, err = s.requireAssignee(ctx, tx, reportID, actor.UserID)
		if err != nil {
			return err
		}
		if access.status != string(workflow.Pemeriksaan) {
			return httpx.ErrConflict("Kesimpulan hanya dapat diajukan saat status PEMERIKSAAN.")
		}

		err = tx.QueryRow(ctx, `
			UPDATE conclusions
			SET submission_status = 'DIAJUKAN', submitted_at = now()
			WHERE report_id = $1 AND submission_status = 'DRAF'
			RETURNING submitted_at`,
			reportID,
		).Scan(&submittedAt)
		if errors.Is(err, pgx.ErrNoRows) {
			return httpx.ErrConflict("Simpan kesimpulan terlebih dahulu; kesimpulan yang sudah diajukan tidak dapat diajukan ulang.")
		}
		if err != nil {
			return fmt.Errorf("decision: ajukan kesimpulan: %w", err)
		}

		if err := s.flow.Transition(ctx, tx, reportID,
			workflow.Pemeriksaan, workflow.Rekomendasi, &actor.UserID,
		); err != nil {
			return err
		}

		return s.audit.Log(ctx, tx, audit.Event{
			ActorType: audit.ActorUser,
			UserID:    &actor.UserID,
			Action:    audit.ActionConclusionSubmitted,
			Entity:    "report",
			EntityID:  &reportID,
			IP:        ip,
			Detail: map[string]any{
				"result": derefString(access.conclusionResult),
			},
		})
	})
	if err != nil {
		return SubmitResult{}, err
	}

	s.notifyStatusChanged(access.reporterEmail, access.caseNumber)
	return SubmitResult{Status: string(workflow.Rekomendasi), SubmittedAt: submittedAt}, nil
}

// derefString mengubah pointer string menjadi string kosong bila nil.
func derefString(value *string) string {
	if value == nil {
		return ""
	}
	return *value
}
