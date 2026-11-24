package review

import (
	"context"
	"errors"
	"fmt"
	"time"
	"unicode/utf8"

	"github.com/RidwanRamdhani/ppkpt-backend/internal/audit"
	"github.com/RidwanRamdhani/ppkpt-backend/internal/auth"
	"github.com/RidwanRamdhani/ppkpt-backend/internal/platform/db"
	"github.com/RidwanRamdhani/ppkpt-backend/internal/platform/httpx"
	"github.com/RidwanRamdhani/ppkpt-backend/internal/workflow"
	"github.com/jackc/pgx/v5"
)

const (
	maxReviewTextRunes  = 5_000
	maxCloseReasonRunes = 5_000
)

// ReviewRequest adalah body POST /api/cases/{id}/review.
type ReviewRequest struct {
	VictimIdentified    string `json:"victim_identified"`
	WitnessIdentified   string `json:"witness_identified"`
	InitialEvidenceNote string `json:"initial_evidence_note"`
}

// ReviewView adalah catatan penelaahan yang tersimpan.
type ReviewView struct {
	VictimIdentified    string    `json:"victim_identified"`
	WitnessIdentified   string    `json:"witness_identified"`
	InitialEvidenceNote string    `json:"initial_evidence_note"`
	Member              MemberRef `json:"member"`
	CreatedAt           time.Time `json:"created_at"`
}

// ContinueResult adalah respons POST /api/cases/{id}/review/continue.
type ContinueResult struct {
	Status    string     `json:"status"`
	ExamDueAt *time.Time `json:"exam_due_at"`
}

// CloseRequest adalah body POST /api/cases/{id}/review/close.
type CloseRequest struct {
	Reason string `json:"reason"`
}

// CloseResult adalah respons POST /api/cases/{id}/review/close.
type CloseResult struct {
	Status string `json:"status"`
}

// Validate menormalkan dan memeriksa catatan penelaahan. Minimal satu catatan
// harus diisi agar baris review_notes tidak kosong.
func (r ReviewRequest) Validate() (ReviewRequest, *httpx.AppError) {
	fields := make(map[string]string)

	normalized := ReviewRequest{
		VictimIdentified:    trimText(r.VictimIdentified),
		WitnessIdentified:   trimText(r.WitnessIdentified),
		InitialEvidenceNote: trimText(r.InitialEvidenceNote),
	}

	if normalized.VictimIdentified == "" &&
		normalized.WitnessIdentified == "" &&
		normalized.InitialEvidenceNote == "" {
		fields["_"] = "Minimal satu catatan penelaahan harus diisi."
	}
	maxRunesField(fields, "victim_identified", normalized.VictimIdentified, maxReviewTextRunes)
	maxRunesField(fields, "witness_identified", normalized.WitnessIdentified, maxReviewTextRunes)
	maxRunesField(fields, "initial_evidence_note", normalized.InitialEvidenceNote, maxReviewTextRunes)

	if len(fields) > 0 {
		appErr := httpx.ErrValidation("Catatan penelaahan belum lengkap atau tidak valid.")
		appErr.Fields = fields
		return ReviewRequest{}, appErr
	}
	return normalized, nil
}

// Validate memeriksa alasan penutupan kasus.
func (r CloseRequest) Validate() (CloseRequest, *httpx.AppError) {
	fields := make(map[string]string)

	reason := trimText(r.Reason)
	if reason == "" {
		fields["reason"] = "Alasan tidak melanjutkan kasus wajib diisi."
	}
	maxRunesField(fields, "reason", reason, maxCloseReasonRunes)

	if len(fields) > 0 {
		appErr := httpx.ErrValidation("Alasan penutupan belum lengkap atau tidak valid.")
		appErr.Fields = fields
		return CloseRequest{}, appErr
	}
	return CloseRequest{Reason: reason}, nil
}

const reviewNoteSQL = `
SELECT COALESCE(rn.victim_identified, ''), COALESCE(rn.witness_identified, ''),
       COALESCE(rn.initial_evidence_note, ''), rn.member_id, u.name, rn.created_at
FROM review_notes rn
JOIN users u ON u.id = rn.member_id
WHERE rn.report_id = $1`

func loadReviewNote(ctx context.Context, q db.DBTX, reportID int64) (ReviewView, error) {
	var view ReviewView
	err := q.QueryRow(ctx, reviewNoteSQL, reportID).Scan(
		&view.VictimIdentified,
		&view.WitnessIdentified,
		&view.InitialEvidenceNote,
		&view.Member.ID,
		&view.Member.Name,
		&view.CreatedAt,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return ReviewView{}, httpx.ErrNotFound()
	}
	if err != nil {
		return ReviewView{}, fmt.Errorf("review: baca catatan penelaahan: %w", err)
	}
	return view, nil
}

// SaveReview menyimpan atau memperbarui catatan penelaahan (FR-28).
//
// Hanya kasus berstatus PENELAAHAN yang dapat diisi. review_notes memiliki
// UNIQUE(report_id), sehingga penyimpanan kedua memperbarui baris yang sama.
func (s *Service) SaveReview(
	ctx context.Context,
	actor Actor,
	reportID int64,
	req ReviewRequest,
	ip string,
) (ReviewView, error) {
	if actor.Role != auth.RoleSatgas {
		return ReviewView{}, httpx.ErrForbidden()
	}
	input, appErr := req.Validate()
	if appErr != nil {
		return ReviewView{}, appErr
	}

	var view ReviewView
	err := db.WithTx(ctx, s.pool, func(tx pgx.Tx) error {
		access, err := s.requireAssignee(ctx, tx, reportID, actor.UserID)
		if err != nil {
			return err
		}
		if access.status != string(workflow.Penelaahan) {
			return httpx.ErrConflict("Catatan penelaahan hanya dapat disimpan saat status PENELAAHAN.")
		}

		if _, err := tx.Exec(ctx, `
			INSERT INTO review_notes
				(report_id, member_id, victim_identified, witness_identified, initial_evidence_note)
			VALUES ($1, $2, $3, $4, $5)
			ON CONFLICT (report_id) DO UPDATE SET
				member_id = EXCLUDED.member_id,
				victim_identified = EXCLUDED.victim_identified,
				witness_identified = EXCLUDED.witness_identified,
				initial_evidence_note = EXCLUDED.initial_evidence_note`,
			reportID,
			actor.UserID,
			nullIfEmpty(input.VictimIdentified),
			nullIfEmpty(input.WitnessIdentified),
			nullIfEmpty(input.InitialEvidenceNote),
		); err != nil {
			return fmt.Errorf("review: simpan catatan penelaahan: %w", err)
		}

		view, err = loadReviewNote(ctx, tx, reportID)
		if err != nil {
			return err
		}

		return s.audit.Log(ctx, tx, audit.Event{
			ActorType: audit.ActorUser,
			UserID:    &actor.UserID,
			Action:    audit.ActionReviewRecorded,
			Entity:    "report",
			EntityID:  &reportID,
			IP:        ip,
		})
	})
	if err != nil {
		return ReviewView{}, err
	}
	return view, nil
}

// ContinueReview memindahkan kasus dari penelaahan ke pemeriksaan.
//
// Hasil penelaahan wajib sudah dicatat (SDD M05); tanpa itu permintaan ditolak
// dengan 409. State machine mengisi exam_started_at dan exam_due_at.
func (s *Service) ContinueReview(
	ctx context.Context,
	actor Actor,
	reportID int64,
	ip string,
) (ContinueResult, error) {
	if actor.Role != auth.RoleSatgas {
		return ContinueResult{}, httpx.ErrForbidden()
	}

	var (
		access  caseAccess
		examDue *time.Time
	)
	err := db.WithTx(ctx, s.pool, func(tx pgx.Tx) error {
		var err error
		access, err = s.requireAssignee(ctx, tx, reportID, actor.UserID)
		if err != nil {
			return err
		}
		if access.status != string(workflow.Penelaahan) {
			return httpx.ErrConflict("Kasus hanya dapat dilanjutkan saat status PENELAAHAN.")
		}

		var hasNotes bool
		if err := tx.QueryRow(ctx,
			`SELECT EXISTS (SELECT 1 FROM review_notes WHERE report_id = $1)`, reportID,
		).Scan(&hasNotes); err != nil {
			return fmt.Errorf("review: periksa catatan penelaahan: %w", err)
		}
		if !hasNotes {
			return httpx.ErrConflict("Catat hasil penelaahan sebelum melanjutkan ke pemeriksaan.")
		}

		if err := s.flow.Transition(ctx, tx, reportID,
			workflow.Penelaahan, workflow.Pemeriksaan, &actor.UserID,
		); err != nil {
			return err
		}
		if err := tx.QueryRow(ctx,
			`SELECT exam_due_at FROM reports WHERE id = $1`, reportID,
		).Scan(&examDue); err != nil {
			return fmt.Errorf("review: baca tenggat pemeriksaan: %w", err)
		}

		return s.audit.Log(ctx, tx, audit.Event{
			ActorType: audit.ActorUser,
			UserID:    &actor.UserID,
			Action:    audit.ActionReviewContinued,
			Entity:    "report",
			EntityID:  &reportID,
			IP:        ip,
		})
	})
	if err != nil {
		return ContinueResult{}, err
	}

	s.notifyStatusChanged(access.reporterEmail, access.caseNumber)
	return ContinueResult{Status: string(workflow.Pemeriksaan), ExamDueAt: examDue}, nil
}

// CloseReview menutup kasus dari tahap penelaahan tanpa melanjutkan ke
// pemeriksaan. Alasan wajib diisi dan disimpan pada detail audit.
func (s *Service) CloseReview(
	ctx context.Context,
	actor Actor,
	reportID int64,
	req CloseRequest,
	ip string,
) (CloseResult, error) {
	if actor.Role != auth.RoleSatgas {
		return CloseResult{}, httpx.ErrForbidden()
	}
	input, appErr := req.Validate()
	if appErr != nil {
		return CloseResult{}, appErr
	}

	var access caseAccess
	err := db.WithTx(ctx, s.pool, func(tx pgx.Tx) error {
		var err error
		access, err = s.requireAssignee(ctx, tx, reportID, actor.UserID)
		if err != nil {
			return err
		}
		if access.status != string(workflow.Penelaahan) {
			return httpx.ErrConflict("Kasus hanya dapat ditutup dari tahap PENELAAHAN.")
		}

		if err := s.flow.Transition(ctx, tx, reportID,
			workflow.Penelaahan, workflow.Selesai, &actor.UserID,
		); err != nil {
			return err
		}

		return s.audit.Log(ctx, tx, audit.Event{
			ActorType: audit.ActorUser,
			UserID:    &actor.UserID,
			Action:    audit.ActionReviewClosed,
			Entity:    "report",
			EntityID:  &reportID,
			IP:        ip,
			Detail:    map[string]any{"reason": input.Reason},
		})
	})
	if err != nil {
		return CloseResult{}, err
	}

	s.notifyStatusChanged(access.reporterEmail, access.caseNumber)
	return CloseResult{Status: string(workflow.Selesai)}, nil
}

// maxRunesField mencatat kesalahan bila panjang teks melebihi batas.
func maxRunesField(fields map[string]string, field, value string, limit int) {
	if utf8.RuneCountInString(value) > limit {
		fields[field] = fmt.Sprintf("Maksimal %d karakter.", limit)
	}
}
