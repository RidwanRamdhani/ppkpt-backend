package cases

import (
	"context"
	"strings"
	"unicode/utf8"

	"github.com/RidwanRamdhani/ppkpt-backend/internal/audit"
	"github.com/RidwanRamdhani/ppkpt-backend/internal/auth"
	"github.com/RidwanRamdhani/ppkpt-backend/internal/platform/db"
	"github.com/RidwanRamdhani/ppkpt-backend/internal/platform/httpx"
	"github.com/RidwanRamdhani/ppkpt-backend/internal/workflow"
	"github.com/jackc/pgx/v5"
)

const (
	verificationLengkap         = "LENGKAP"
	verificationPerluDilengkapi = "PERLU_DILENGKAPI"
	maxVerificationNoteRunes    = 5_000
)

// VerificationRequest adalah body POST /api/cases/{id}/verification.
type VerificationRequest struct {
	Result          string `json:"result"`
	NoteForReporter string `json:"note_for_reporter"`
}

// VerificationResult adalah respons pencatatan verifikasi.
type VerificationResult struct {
	ID     int64  `json:"id"`
	Result string `json:"result"`
	Status string `json:"status"`
}

// Validate menormalkan dan memeriksa hasil verifikasi. Catatan untuk pelapor
// wajib ada ketika hasilnya PERLU_DILENGKAPI.
func (r VerificationRequest) Validate() (VerificationRequest, *httpx.AppError) {
	fields := make(map[string]string)

	result := strings.ToUpper(strings.TrimSpace(r.Result))
	switch result {
	case verificationLengkap, verificationPerluDilengkapi:
	default:
		fields["result"] = "Hasil verifikasi harus LENGKAP atau PERLU_DILENGKAPI."
	}

	note := strings.TrimSpace(r.NoteForReporter)
	if result == verificationPerluDilengkapi && note == "" {
		fields["note_for_reporter"] = "Catatan untuk pelapor wajib diisi."
	}
	if utf8.RuneCountInString(note) > maxVerificationNoteRunes {
		fields["note_for_reporter"] = "Maksimal 5000 karakter."
	}

	if len(fields) > 0 {
		appErr := httpx.ErrValidation("Data verifikasi belum lengkap atau tidak valid.")
		appErr.Fields = fields
		return VerificationRequest{}, appErr
	}
	return VerificationRequest{Result: result, NoteForReporter: note}, nil
}

// Verify mencatat verifikasi kasus oleh PJ (FR-26).
//
// Hanya kasus berstatus DILAPORKAN yang dapat diverifikasi. Hasil LENGKAP
// memindahkan kasus ke PENELAAHAN (tenggat 7 hari diisi state machine), hasil
// PERLU_DILENGKAPI memindahkan ke PERLU_DILENGKAPI. Insert verifikasi,
// transisi, dan audit berada dalam satu transaksi; notifikasi dikirim setelah
// commit.
func (s *Service) Verify(
	ctx context.Context,
	actor Actor,
	reportID int64,
	req VerificationRequest,
	ip string,
) (VerificationResult, error) {
	if actor.Role != auth.RoleSatgas {
		return VerificationResult{}, httpx.ErrForbidden()
	}
	input, appErr := req.Validate()
	if appErr != nil {
		return VerificationResult{}, appErr
	}

	var (
		verificationID int64
		access         accessInfo
		target         workflow.Status
	)
	err := db.WithTx(ctx, s.pool, func(tx pgx.Tx) error {
		var err error
		access, err = s.requireAssignee(ctx, tx, reportID, actor.UserID)
		if err != nil {
			return err
		}
		if access.status != string(workflow.Dilaporkan) {
			return httpx.ErrConflict("Verifikasi hanya dapat dilakukan saat status DILAPORKAN.")
		}

		if err := tx.QueryRow(ctx, `
			INSERT INTO verifications (report_id, member_id, result, note_for_reporter)
			VALUES ($1, $2, $3, $4)
			RETURNING id`,
			reportID, actor.UserID, input.Result, nullIfEmpty(input.NoteForReporter),
		).Scan(&verificationID); err != nil {
			return err
		}

		target = workflow.Penelaahan
		if input.Result == verificationPerluDilengkapi {
			target = workflow.PerluDilengkapi
		}
		if err := s.flow.Transition(ctx, tx, reportID, workflow.Dilaporkan, target, &actor.UserID); err != nil {
			return err
		}

		return s.audit.Log(ctx, tx, audit.Event{
			ActorType: audit.ActorUser,
			UserID:    &actor.UserID,
			Action:    audit.ActionVerificationRecorded,
			Entity:    "report",
			EntityID:  &reportID,
			IP:        ip,
			Detail:    map[string]any{"result": input.Result},
		})
	})
	if err != nil {
		return VerificationResult{}, err
	}

	s.notifyStatusChanged(access.reporterEmail, access.caseNumber)
	return VerificationResult{
		ID:     verificationID,
		Result: input.Result,
		Status: string(target),
	}, nil
}

// notifyStatusChanged memberi tahu pelapor bahwa status berubah tanpa memuat
// detail kasus. Best-effort: kegagalan email tidak membatalkan perubahan.
func (s *Service) notifyStatusChanged(email, caseNumber string) {
	if s.notifier == nil {
		return
	}
	s.notifier.SendStatusUpdate(email, caseNumber)
}
