package review

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

const (
	maxSessionSummaryRunes = 5_000
	// maxSessionRoleRunes mengikuti kolom session_participants.role VARCHAR(20).
	// Peran yang lebih panjang akan ditolak database (500) alih-alih 422.
	maxSessionRoleRunes = 20
	dateLayout          = "2006-01-02"
)

// SessionParticipantInput adalah satu peserta sesi pemeriksaan.
type SessionParticipantInput struct {
	PartyID int64  `json:"party_id"`
	Role    string `json:"role"`
}

// SessionRequest adalah body POST /api/cases/{id}/sessions.
type SessionRequest struct {
	SessionDate  string                    `json:"session_date"`
	Summary      string                    `json:"summary"`
	Participants []SessionParticipantInput `json:"participants"`
}

// SessionInput adalah sesi yang sudah divalidasi dan siap ditulis.
type SessionInput struct {
	SessionDate  time.Time
	Summary      string
	Participants []SessionParticipantInput
}

// SessionParticipantView adalah peserta sesi beserta identitas pihaknya.
type SessionParticipantView struct {
	PartyID   int64  `json:"party_id"`
	Role      string `json:"role"`
	PartyType string `json:"party_type"`
	PartyName string `json:"party_name"`
}

// SessionView adalah satu sesi pemeriksaan yang tersimpan.
type SessionView struct {
	ID           int64                    `json:"id"`
	Examiner     MemberRef                `json:"examiner"`
	SessionDate  string                   `json:"session_date"`
	Summary      string                   `json:"summary"`
	Participants []SessionParticipantView `json:"participants"`
	CreatedAt    time.Time                `json:"created_at"`
}

// Validate menormalkan dan memeriksa sesi pemeriksaan. Tanggal sesi memakai
// zona WIB, tidak boleh di masa depan, dan wajib memiliki minimal satu peserta
// tanpa duplikasi.
func (r SessionRequest) Validate(now time.Time) (SessionInput, *httpx.AppError) {
	fields := make(map[string]string)

	rawDate := trimText(r.SessionDate)
	var sessionDate time.Time
	if rawDate == "" {
		fields["session_date"] = "Tanggal sesi wajib diisi."
	} else if parsed, err := time.ParseInLocation(dateLayout, rawDate, deadline.Jakarta); err != nil {
		fields["session_date"] = "Format tanggal harus YYYY-MM-DD."
	} else if parsed.After(startOfDay(now)) {
		fields["session_date"] = "Tanggal sesi tidak boleh di masa depan."
	} else {
		sessionDate = parsed
	}

	summary := trimText(r.Summary)
	maxRunesField(fields, "summary", summary, maxSessionSummaryRunes)

	if len(r.Participants) == 0 {
		fields["participants"] = "Minimal satu peserta sesi harus diisi."
	}

	participants := make([]SessionParticipantInput, 0, len(r.Participants))
	seen := make(map[int64]struct{}, len(r.Participants))
	for i, item := range r.Participants {
		role := trimText(item.Role)
		if item.PartyID <= 0 {
			fields[fmt.Sprintf("participants.%d.party_id", i)] = "ID pihak tidak valid."
			continue
		}
		if _, duplicate := seen[item.PartyID]; duplicate {
			fields["participants"] = "Peserta sesi tidak boleh duplikat."
			continue
		}
		seen[item.PartyID] = struct{}{}
		if role == "" {
			fields[fmt.Sprintf("participants.%d.role", i)] = "Peran peserta wajib diisi."
			continue
		}
		maxRunesField(fields, fmt.Sprintf("participants.%d.role", i), role, maxSessionRoleRunes)
		participants = append(participants, SessionParticipantInput{PartyID: item.PartyID, Role: role})
	}

	if len(fields) > 0 {
		appErr := httpx.ErrValidation("Data sesi pemeriksaan belum lengkap atau tidak valid.")
		appErr.Fields = fields
		return SessionInput{}, appErr
	}
	return SessionInput{
		SessionDate:  sessionDate,
		Summary:      summary,
		Participants: participants,
	}, nil
}

// CreateSession mencatat sesi pemeriksaan beserta pesertanya (FR-30).
//
// Sesi dan peserta ditulis dalam satu transaksi. Setiap party_id harus milik
// kasus pada path; peserta dari kasus lain ditolak sebagai kesalahan validasi
// agar tidak ada kebocoran atau pencampuran data antar-kasus.
func (s *Service) CreateSession(
	ctx context.Context,
	actor Actor,
	reportID int64,
	req SessionRequest,
	ip string,
) (SessionView, error) {
	if actor.Role != auth.RoleSatgas {
		return SessionView{}, httpx.ErrForbidden()
	}
	input, appErr := req.Validate(s.now())
	if appErr != nil {
		return SessionView{}, appErr
	}

	var sessionID int64
	err := db.WithTx(ctx, s.pool, func(tx pgx.Tx) error {
		access, err := s.requireAssignee(ctx, tx, reportID, actor.UserID)
		if err != nil {
			return err
		}
		if access.status != string(workflow.Pemeriksaan) {
			return httpx.ErrConflict("Sesi pemeriksaan hanya dapat dicatat saat status PEMERIKSAAN.")
		}

		if err := s.ensurePartiesBelong(ctx, tx, reportID, input.Participants); err != nil {
			return err
		}

		if err := tx.QueryRow(ctx, `
			INSERT INTO examination_sessions (report_id, examiner_id, session_date, summary)
			VALUES ($1, $2, $3, $4)
			RETURNING id`,
			reportID, actor.UserID, input.SessionDate, nullIfEmpty(input.Summary),
		).Scan(&sessionID); err != nil {
			return fmt.Errorf("review: simpan sesi pemeriksaan: %w", err)
		}

		for _, participant := range input.Participants {
			if _, err := tx.Exec(ctx, `
				INSERT INTO session_participants (session_id, party_id, role)
				VALUES ($1, $2, $3)`,
				sessionID, participant.PartyID, participant.Role,
			); err != nil {
				return fmt.Errorf("review: simpan peserta sesi: %w", err)
			}
		}

		return s.audit.Log(ctx, tx, audit.Event{
			ActorType: audit.ActorUser,
			UserID:    &actor.UserID,
			Action:    audit.ActionExamSessionCreated,
			Entity:    "report",
			EntityID:  &reportID,
			IP:        ip,
			Detail: map[string]any{
				"session_id":   sessionID,
				"participants": len(input.Participants),
			},
		})
	})
	if err != nil {
		return SessionView{}, err
	}

	return s.loadSession(ctx, s.pool, sessionID)
}

// ensurePartiesBelong memastikan seluruh party_id milik kasus ini.
func (s *Service) ensurePartiesBelong(
	ctx context.Context,
	q db.DBTX,
	reportID int64,
	participants []SessionParticipantInput,
) error {
	ids := make([]int64, len(participants))
	for i, participant := range participants {
		ids[i] = participant.PartyID
	}

	rows, err := q.Query(ctx,
		`SELECT id FROM parties WHERE report_id = $1 AND id = ANY($2)`, reportID, ids)
	if err != nil {
		return fmt.Errorf("review: periksa pihak sesi: %w", err)
	}
	defer rows.Close()

	found := make(map[int64]struct{}, len(ids))
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			return fmt.Errorf("review: baca pihak sesi: %w", err)
		}
		found[id] = struct{}{}
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("review: iterasi pihak sesi: %w", err)
	}

	if len(found) != len(ids) {
		appErr := httpx.ErrValidation("Peserta sesi harus pihak pada kasus ini.")
		appErr.Fields = map[string]string{
			"participants": "Peserta sesi harus pihak pada kasus ini.",
		}
		return appErr
	}
	return nil
}

const sessionByIDSQL = `
SELECT s.id, s.examiner_id, u.name, s.session_date, COALESCE(s.summary, ''), s.created_at
FROM examination_sessions s
JOIN users u ON u.id = s.examiner_id
WHERE s.id = $1`

const sessionParticipantsSQL = `
SELECT sp.party_id, sp.role, p.party_type, COALESCE(p.name, '')
FROM session_participants sp
JOIN parties p ON p.id = sp.party_id
WHERE sp.session_id = $1
ORDER BY sp.party_id`

func (s *Service) loadSession(ctx context.Context, q db.DBTX, sessionID int64) (SessionView, error) {
	var (
		view SessionView
		date time.Time
	)
	err := q.QueryRow(ctx, sessionByIDSQL, sessionID).Scan(
		&view.ID,
		&view.Examiner.ID,
		&view.Examiner.Name,
		&date,
		&view.Summary,
		&view.CreatedAt,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return SessionView{}, httpx.ErrNotFound()
	}
	if err != nil {
		return SessionView{}, fmt.Errorf("review: baca sesi pemeriksaan: %w", err)
	}
	view.SessionDate = date.In(deadline.Jakarta).Format(dateLayout)

	rows, err := q.Query(ctx, sessionParticipantsSQL, sessionID)
	if err != nil {
		return SessionView{}, fmt.Errorf("review: baca peserta sesi: %w", err)
	}
	defer rows.Close()

	view.Participants = make([]SessionParticipantView, 0, 4)
	for rows.Next() {
		var participant SessionParticipantView
		if err := rows.Scan(
			&participant.PartyID,
			&participant.Role,
			&participant.PartyType,
			&participant.PartyName,
		); err != nil {
			return SessionView{}, fmt.Errorf("review: baca peserta sesi: %w", err)
		}
		view.Participants = append(view.Participants, participant)
	}
	if err := rows.Err(); err != nil {
		return SessionView{}, fmt.Errorf("review: iterasi peserta sesi: %w", err)
	}
	return view, nil
}

// startOfDay mengembalikan tengah malam WIB pada tanggal t.
func startOfDay(t time.Time) time.Time {
	local := t.In(deadline.Jakarta)
	return time.Date(local.Year(), local.Month(), local.Day(), 0, 0, 0, 0, deadline.Jakarta)
}
