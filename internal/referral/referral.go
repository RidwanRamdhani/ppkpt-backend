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

// ReferralView adalah satu rujukan layanan yang tersimpan (FR-34).
type ReferralView struct {
	ID              int64     `json:"id"`
	ServiceType     string    `json:"service_type"`
	TargetPartyID   *int64    `json:"target_party_id"`
	TargetPartyName *string   `json:"target_party_name"`
	Institution     string    `json:"institution"`
	ReferralDate    string    `json:"referral_date"`
	FollowUpStatus  string    `json:"follow_up_status"`
	Notes           string    `json:"notes"`
	CreatedBy       MemberRef `json:"created_by"`
}

const referralByIDSQL = `
SELECT rf.id, rf.service_type, rf.target_party_id, p.name,
       rf.institution, rf.referral_date, rf.follow_up_status, COALESCE(rf.notes, ''),
       rf.created_by, u.name
FROM referrals rf
LEFT JOIN parties p ON p.id = rf.target_party_id
JOIN users u ON u.id = rf.created_by
WHERE rf.id = $1 AND rf.report_id = $2`

func (s *Service) loadReferral(ctx context.Context, q db.DBTX, reportID, referralID int64) (ReferralView, error) {
	var (
		view ReferralView
		date time.Time
	)
	err := q.QueryRow(ctx, referralByIDSQL, referralID, reportID).Scan(
		&view.ID,
		&view.ServiceType,
		&view.TargetPartyID,
		&view.TargetPartyName,
		&view.Institution,
		&date,
		&view.FollowUpStatus,
		&view.Notes,
		&view.CreatedBy.ID,
		&view.CreatedBy.Name,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return ReferralView{}, httpx.ErrNotFound()
	}
	if err != nil {
		return ReferralView{}, fmt.Errorf("referral: baca rujukan: %w", err)
	}
	view.ReferralDate = date.In(deadline.Jakarta).Format(dateLayout)
	return view, nil
}

// CreateReferral mencatat rujukan layanan baru (FR-34).
//
// Hanya PJ kasus yang boleh menulis dan kasus yang sudah SELESAI ditolak.
// target_party_id opsional, tetapi bila diisi harus pihak pada kasus yang sama
// agar tidak ada pencampuran data antar-kasus.
func (s *Service) CreateReferral(
	ctx context.Context,
	actor Actor,
	reportID int64,
	req ReferralRequest,
	ip string,
) (ReferralView, error) {
	if actor.Role != auth.RoleSatgas {
		return ReferralView{}, httpx.ErrForbidden()
	}
	input, appErr := req.Validate(s.now())
	if appErr != nil {
		return ReferralView{}, appErr
	}

	var view ReferralView
	err := db.WithTx(ctx, s.pool, func(tx pgx.Tx) error {
		if _, err := s.requireAssignee(ctx, tx, reportID, actor.UserID); err != nil {
			return err
		}
		if input.TargetPartyID != nil {
			if err := ensurePartyBelongs(ctx, tx, reportID, *input.TargetPartyID); err != nil {
				return err
			}
		}

		var referralID int64
		if err := tx.QueryRow(ctx, `
			INSERT INTO referrals
				(report_id, service_type, target_party_id, institution,
				 referral_date, follow_up_status, notes, created_by)
			VALUES ($1, $2, $3, $4, $5, $6, $7, $8)
			RETURNING id`,
			reportID,
			input.ServiceType,
			input.TargetPartyID,
			input.Institution,
			input.ReferralDate,
			input.FollowUpStatus,
			nullIfEmpty(input.Notes),
			actor.UserID,
		).Scan(&referralID); err != nil {
			return fmt.Errorf("referral: simpan rujukan: %w", err)
		}

		if err := s.audit.Log(ctx, tx, audit.Event{
			ActorType: audit.ActorUser,
			UserID:    &actor.UserID,
			Action:    audit.ActionReferralCreated,
			Entity:    "report",
			EntityID:  &reportID,
			IP:        ip,
			Detail: map[string]any{
				"referral_id":      referralID,
				"service_type":     input.ServiceType,
				"follow_up_status": input.FollowUpStatus,
				"target_party_id":  input.TargetPartyID,
			},
		}); err != nil {
			return err
		}

		// Dibaca di dalam transaksi: bila pembacaan gagal, seluruh tulisan ikut
		// batal, sehingga klien tidak menerima 500 dengan data sudah tersimpan.
		loaded, err := s.loadReferral(ctx, tx, reportID, referralID)
		if err != nil {
			return err
		}
		view = loaded
		return nil
	})
	if err != nil {
		return ReferralView{}, err
	}
	return view, nil
}

// UpdateReferral memperbarui status tindak lanjut rujukan (FR-34).
//
// Hanya follow_up_status yang boleh berubah; kolom lain tetap agar riwayat
// rujukan kredibel (D28). Klausa report_id menutup IDOR: rujukan kasus lain
// diperlakukan sebagai tidak ditemukan.
func (s *Service) UpdateReferral(
	ctx context.Context,
	actor Actor,
	reportID, referralID int64,
	req ReferralUpdateRequest,
	ip string,
) (ReferralView, error) {
	if actor.Role != auth.RoleSatgas {
		return ReferralView{}, httpx.ErrForbidden()
	}
	input, appErr := req.Validate()
	if appErr != nil {
		return ReferralView{}, appErr
	}

	var view ReferralView
	err := db.WithTx(ctx, s.pool, func(tx pgx.Tx) error {
		if _, err := s.requireAssignee(ctx, tx, reportID, actor.UserID); err != nil {
			return err
		}

		tag, err := tx.Exec(ctx, `
			UPDATE referrals
			SET follow_up_status = $3
			WHERE id = $1 AND report_id = $2`,
			referralID, reportID, input.FollowUpStatus,
		)
		if err != nil {
			return fmt.Errorf("referral: perbarui rujukan: %w", err)
		}
		if tag.RowsAffected() == 0 {
			return httpx.ErrNotFound()
		}

		if err := s.audit.Log(ctx, tx, audit.Event{
			ActorType: audit.ActorUser,
			UserID:    &actor.UserID,
			Action:    audit.ActionReferralUpdated,
			Entity:    "report",
			EntityID:  &reportID,
			IP:        ip,
			Detail: map[string]any{
				"referral_id":      referralID,
				"follow_up_status": input.FollowUpStatus,
			},
		}); err != nil {
			return err
		}

		// Dibaca di dalam transaksi agar kegagalan baca membatalkan tulisan.
		loaded, err := s.loadReferral(ctx, tx, reportID, referralID)
		if err != nil {
			return err
		}
		view = loaded
		return nil
	})
	if err != nil {
		return ReferralView{}, err
	}
	return view, nil
}

// ensurePartyBelongs memastikan partyID milik kasus. Pihak dari kasus lain
// ditolak sebagai kesalahan validasi (422), sama seperti peserta sesi.
func ensurePartyBelongs(ctx context.Context, q db.DBTX, reportID, partyID int64) error {
	var exists bool
	if err := q.QueryRow(ctx,
		`SELECT EXISTS (SELECT 1 FROM parties WHERE id = $1 AND report_id = $2)`,
		partyID, reportID,
	).Scan(&exists); err != nil {
		return fmt.Errorf("referral: periksa pihak rujukan: %w", err)
	}
	if !exists {
		appErr := httpx.ErrValidation("Pihak rujukan harus pihak pada kasus ini.")
		appErr.Fields = map[string]string{
			"target_party_id": "Pihak rujukan harus pihak pada kasus ini.",
		}
		return appErr
	}
	return nil
}
