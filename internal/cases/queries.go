package cases

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/RidwanRamdhani/ppkpt-backend/internal/platform/db"
	"github.com/RidwanRamdhani/ppkpt-backend/internal/platform/httpx"
	"github.com/RidwanRamdhani/ppkpt-backend/internal/review"
	"github.com/RidwanRamdhani/ppkpt-backend/internal/workflow"
	"github.com/jackc/pgx/v5"
)

// listSelectSQL adalah proyeksi dasar daftar kasus. Data pihak tidak pernah
// diambil di sini agar endpoint daftar tidak mungkin membocorkannya.
const listSelectSQL = `
SELECT r.id, r.case_number, r.channel, r.status, r.case_condition,
       r.assigned_to, u.name,
       r.review_due_at, r.exam_due_at, r.created_at,
       COALESCE((
           SELECT array_agg(vt.code::text ORDER BY vt.code)
           FROM report_violation_types rvt
           JOIN violation_types vt ON vt.id = rvt.violation_type_id
           WHERE rvt.report_id = r.id
       ), '{}'::text[]) AS violation_types,
       c.result, c.submission_status, c.submitted_at,
       d.decision_type, d.decided_at,
       COUNT(*) OVER() AS total
FROM reports r
LEFT JOIN users u ON u.id = r.assigned_to
LEFT JOIN conclusions c ON c.report_id = r.id
LEFT JOIN decisions d ON d.report_id = r.id`

type listRow struct {
	ID               int64
	CaseNumber       string
	Channel          string
	Status           string
	CaseCondition    *string
	AssignedID       *int64
	AssignedName     *string
	ReviewDueAt      *time.Time
	ExamDueAt        *time.Time
	CreatedAt        time.Time
	ViolationTypes   []string
	ConclusionResult *string
	ConclusionStatus *string
	SubmittedAt      *time.Time
	DecisionType     *string
	DecidedAt        *time.Time
	Total            int64
}

// accessInfo adalah data minimum untuk memutuskan boleh-tidaknya membaca
// detail/riwayat dan menyusun flag UI.
type accessInfo struct {
	caseNumber          string
	reporterEmail       string
	status              string
	assignedTo          *int64
	conclusionSubmitted bool
}

const accessSQL = `
SELECT r.id, r.case_number, COALESCE(rp.email, ''), r.status, r.assigned_to,
       COALESCE(c.submission_status = 'DIAJUKAN', FALSE)
FROM reports r
LEFT JOIN reporters rp ON rp.report_id = r.id
LEFT JOIN conclusions c ON c.report_id = r.id
WHERE r.id = $1`

func (s *Service) loadAccess(ctx context.Context, reportID int64) (accessInfo, error) {
	return s.loadAccessWith(ctx, s.pool, reportID)
}

func (s *Service) loadAccessWith(ctx context.Context, q db.DBTX, reportID int64) (accessInfo, error) {
	var (
		info accessInfo
		id   int64
	)
	err := q.QueryRow(ctx, accessSQL, reportID).
		Scan(&id, &info.caseNumber, &info.reporterEmail, &info.status, &info.assignedTo, &info.conclusionSubmitted)
	if errors.Is(err, pgx.ErrNoRows) {
		return accessInfo{}, httpx.ErrNotFound()
	}
	if err != nil {
		return accessInfo{}, fmt.Errorf("cases: baca akses kasus: %w", err)
	}
	return info, nil
}

const satgasDetailSQL = `
SELECT r.id, r.case_number, r.channel, COALESCE(r.referral_unit, ''),
       r.status, r.case_condition, r.reporter_confidential, r.reporter_is_victim,
       r.chronology, COALESCE(r.additional_description, ''),
       r.reported_elsewhere, COALESCE(r.reported_to, ''),
       COALESCE(r.efforts_made, ''), COALESCE(r.expectation, ''),
       r.assigned_to, u.name, r.entered_by, eu.name,
       r.received_at, r.review_started_at, r.review_due_at,
       r.exam_started_at, r.exam_due_at, r.closed_at,
       r.created_at, r.updated_at,
       COALESCE((
           SELECT array_agg(vt.code::text ORDER BY vt.code)
           FROM report_violation_types rvt
           JOIN violation_types vt ON vt.id = rvt.violation_type_id
           WHERE rvt.report_id = r.id
       ), '{}'::text[]) AS violation_types,
       c.result, c.submission_status, c.summary, c.submitted_at
FROM reports r
LEFT JOIN users u ON u.id = r.assigned_to
LEFT JOIN users eu ON eu.id = r.entered_by
LEFT JOIN conclusions c ON c.report_id = r.id
WHERE r.id = $1`

type satgasDetailRow struct {
	ID                    int64
	CaseNumber            string
	Channel               string
	ReferralUnit          string
	Status                string
	CaseCondition         *string
	ReporterConfidential  bool
	ReporterIsVictim      bool
	Chronology            string
	AdditionalDescription string
	ReportedElsewhere     bool
	ReportedTo            string
	EffortsMade           string
	Expectation           string
	AssignedID            *int64
	AssignedName          *string
	EnteredID             *int64
	EnteredName           *string
	ReceivedAt            *time.Time
	ReviewStartedAt       *time.Time
	ReviewDueAt           *time.Time
	ExamStartedAt         *time.Time
	ExamDueAt             *time.Time
	ClosedAt              *time.Time
	CreatedAt             time.Time
	UpdatedAt             time.Time
	ViolationTypes        []string
	ConclusionResult      *string
	ConclusionStatus      *string
	ConclusionSummary     *string
	SubmittedAt           *time.Time
}

func (s *Service) loadSatgasDetail(ctx context.Context, reportID, userID int64, access accessInfo) (*SatgasDetail, error) {
	var row satgasDetailRow
	err := s.pool.QueryRow(ctx, satgasDetailSQL, reportID).Scan(
		&row.ID,
		&row.CaseNumber,
		&row.Channel,
		&row.ReferralUnit,
		&row.Status,
		&row.CaseCondition,
		&row.ReporterConfidential,
		&row.ReporterIsVictim,
		&row.Chronology,
		&row.AdditionalDescription,
		&row.ReportedElsewhere,
		&row.ReportedTo,
		&row.EffortsMade,
		&row.Expectation,
		&row.AssignedID,
		&row.AssignedName,
		&row.EnteredID,
		&row.EnteredName,
		&row.ReceivedAt,
		&row.ReviewStartedAt,
		&row.ReviewDueAt,
		&row.ExamStartedAt,
		&row.ExamDueAt,
		&row.ClosedAt,
		&row.CreatedAt,
		&row.UpdatedAt,
		&row.ViolationTypes,
		&row.ConclusionResult,
		&row.ConclusionStatus,
		&row.ConclusionSummary,
		&row.SubmittedAt,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, httpx.ErrNotFound()
	}
	if err != nil {
		return nil, fmt.Errorf("cases: baca detail kasus: %w", err)
	}

	detail := &SatgasDetail{
		ID:                    row.ID,
		CaseNumber:            row.CaseNumber,
		Channel:               row.Channel,
		ReferralUnit:          row.ReferralUnit,
		Status:                row.Status,
		CaseCondition:         row.CaseCondition,
		ReporterConfidential:  row.ReporterConfidential,
		ReporterIsVictim:      row.ReporterIsVictim,
		Chronology:            row.Chronology,
		AdditionalDescription: row.AdditionalDescription,
		ReportedElsewhere:     row.ReportedElsewhere,
		ReportedTo:            row.ReportedTo,
		EffortsMade:           row.EffortsMade,
		Expectation:           row.Expectation,
		AssignedTo:            assignee(row.AssignedID, row.AssignedName),
		EnteredBy:             assignee(row.EnteredID, row.EnteredName),
		ReceivedAt:            row.ReceivedAt,
		ReviewStartedAt:       row.ReviewStartedAt,
		ReviewDueAt:           row.ReviewDueAt,
		ExamStartedAt:         row.ExamStartedAt,
		ExamDueAt:             row.ExamDueAt,
		ClosedAt:              row.ClosedAt,
		CreatedAt:             row.CreatedAt,
		UpdatedAt:             row.UpdatedAt,
		Deadline:              s.deadlineView(row.Status, row.ReviewDueAt, row.ExamDueAt),
		ViolationTypes:        nonNilStrings(row.ViolationTypes),
		Conclusion:            conclusionSummary(row.ConclusionResult, row.ConclusionStatus, row.SubmittedAt),
	}
	if detail.Conclusion != nil && row.ConclusionSummary != nil {
		detail.Conclusion.Summary = *row.ConclusionSummary
	}

	detail.Reporter, err = s.loadReporter(ctx, reportID)
	if err != nil {
		return nil, err
	}
	detail.Parties, err = s.loadParties(ctx, reportID)
	if err != nil {
		return nil, err
	}
	detail.Incidents, err = s.loadIncidents(ctx, reportID)
	if err != nil {
		return nil, err
	}
	detail.Evidences, err = s.loadEvidences(ctx, reportID)
	if err != nil {
		return nil, err
	}
	detail.Verifications, err = s.loadVerifications(ctx, reportID)
	if err != nil {
		return nil, err
	}
	detail.ReviewNote, err = s.loadReviewNote(ctx, reportID)
	if err != nil {
		return nil, err
	}
	detail.Sessions, err = s.loadSessions(ctx, reportID)
	if err != nil {
		return nil, err
	}
	detail.StatusHistory, err = s.loadStatusHistory(ctx, reportID)
	if err != nil {
		return nil, err
	}

	isPJ := access.assignedTo != nil && *access.assignedTo == userID
	if row.ReporterConfidential && !isPJ {
		maskReporter(detail.Reporter)
	}
	detail.IsPJ = isPJ
	detail.CanTake = access.assignedTo == nil && access.status != string(workflow.Selesai)
	detail.CanVerify = isPJ && access.status == string(workflow.Dilaporkan)
	detail.CanReview = isPJ && access.status == string(workflow.Penelaahan)
	detail.CanExamine = isPJ && access.status == string(workflow.Pemeriksaan)
	return detail, nil
}

const officialDetailSQL = `
SELECT r.id, r.case_number, r.channel, r.status,
       r.assigned_to, u.name, r.created_at,
       COALESCE((
           SELECT array_agg(vt.code::text ORDER BY vt.code)
           FROM report_violation_types rvt
           JOIN violation_types vt ON vt.id = rvt.violation_type_id
           WHERE rvt.report_id = r.id
       ), '{}'::text[]) AS violation_types,
       c.result, c.submission_status, c.summary, c.submitted_at
FROM reports r
LEFT JOIN users u ON u.id = r.assigned_to
LEFT JOIN conclusions c ON c.report_id = r.id
WHERE r.id = $1`

func (s *Service) loadOfficialDetail(ctx context.Context, reportID int64) (*OfficialDetail, error) {
	var (
		detail         OfficialDetail
		assignedID     *int64
		assignedName   *string
		result         *string
		status         *string
		summary        *string
		submittedAt    *time.Time
		violationTypes []string
	)
	err := s.pool.QueryRow(ctx, officialDetailSQL, reportID).Scan(
		&detail.ID,
		&detail.CaseNumber,
		&detail.Channel,
		&detail.Status,
		&assignedID,
		&assignedName,
		&detail.CreatedAt,
		&violationTypes,
		&result,
		&status,
		&summary,
		&submittedAt,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, httpx.ErrNotFound()
	}
	if err != nil {
		return nil, fmt.Errorf("cases: baca detail kasus pejabat: %w", err)
	}

	detail.AssignedTo = assignee(assignedID, assignedName)
	detail.ViolationTypes = nonNilStrings(violationTypes)
	detail.Conclusion = conclusionSummary(result, status, submittedAt)
	if detail.Conclusion != nil && summary != nil {
		detail.Conclusion.Summary = *summary
	}
	detail.CanReview = readyToReview(detail.Status, detail.Conclusion)
	return &detail, nil
}

const reporterSQL = `
SELECT name, age, COALESCE(gender, ''), COALESCE(id_number, ''),
       COALESCE(category, ''), COALESCE(institution, ''),
       COALESCE(faculty, ''), COALESCE(program, ''),
       COALESCE(phone, ''), COALESCE(email, '')
FROM reporters
WHERE report_id = $1`

func (s *Service) loadReporter(ctx context.Context, reportID int64) (*ReporterView, error) {
	var (
		view ReporterView
		age  *int16
	)
	err := s.pool.QueryRow(ctx, reporterSQL, reportID).Scan(
		&view.Name,
		&age,
		&view.Gender,
		&view.Category,
		&view.IDNumber,
		&view.Institution,
		&view.Faculty,
		&view.Program,
		&view.Phone,
		&view.Email,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("cases: baca pelapor: %w", err)
	}
	if age != nil {
		v := int(*age)
		view.Age = &v
	}
	return &view, nil
}

const partiesSQL = `
SELECT id, party_type, COALESCE(name, ''), COALESCE(gender, ''),
       COALESCE(id_number, ''), COALESCE(category, ''),
       COALESCE(institution, ''), COALESCE(faculty, ''),
       COALESCE(program, ''), COALESCE(contact, ''),
       COALESCE(relation_to_other, '')
FROM parties
WHERE report_id = $1
ORDER BY id`

func (s *Service) loadParties(ctx context.Context, reportID int64) ([]PartyView, error) {
	rows, err := s.pool.Query(ctx, partiesSQL, reportID)
	if err != nil {
		return nil, fmt.Errorf("cases: baca pihak: %w", err)
	}
	defer rows.Close()

	items := make([]PartyView, 0, 4)
	for rows.Next() {
		var p PartyView
		if err := rows.Scan(
			&p.ID,
			&p.PartyType,
			&p.Name,
			&p.Gender,
			&p.IDNumber,
			&p.Category,
			&p.Institution,
			&p.Faculty,
			&p.Program,
			&p.Contact,
			&p.RelationToOther,
		); err != nil {
			return nil, fmt.Errorf("cases: baca pihak: %w", err)
		}
		items = append(items, p)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("cases: iterasi pihak: %w", err)
	}
	return items, nil
}

const incidentsSQL = `
SELECT id, date_start, date_end, location, description
FROM incidents
WHERE report_id = $1
ORDER BY date_start, id`

func (s *Service) loadIncidents(ctx context.Context, reportID int64) ([]IncidentView, error) {
	rows, err := s.pool.Query(ctx, incidentsSQL, reportID)
	if err != nil {
		return nil, fmt.Errorf("cases: baca kejadian: %w", err)
	}
	defer rows.Close()

	items := make([]IncidentView, 0, 2)
	for rows.Next() {
		var (
			item  IncidentView
			start time.Time
			end   *time.Time
		)
		if err := rows.Scan(&item.ID, &start, &end, &item.Location, &item.Description); err != nil {
			return nil, fmt.Errorf("cases: baca kejadian: %w", err)
		}
		item.DateStart = start.Format("2006-01-02")
		if end != nil {
			value := end.Format("2006-01-02")
			item.DateEnd = &value
		}
		items = append(items, item)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("cases: iterasi kejadian: %w", err)
	}
	return items, nil
}

const evidencesSQL = `
SELECT e.id, e.category, e.original_name, e.mime_type, e.size_bytes,
       e.uploaded_by, u.name, e.uploaded_at
FROM evidences e
LEFT JOIN users u ON u.id = e.uploaded_by
WHERE e.report_id = $1
ORDER BY e.uploaded_at, e.id`

func (s *Service) loadEvidences(ctx context.Context, reportID int64) ([]EvidenceView, error) {
	rows, err := s.pool.Query(ctx, evidencesSQL, reportID)
	if err != nil {
		return nil, fmt.Errorf("cases: baca bukti: %w", err)
	}
	defer rows.Close()

	items := make([]EvidenceView, 0, 4)
	for rows.Next() {
		var (
			item       EvidenceView
			uploadedID *int64
			uploader   *string
		)
		if err := rows.Scan(
			&item.ID,
			&item.Category,
			&item.OriginalName,
			&item.MimeType,
			&item.SizeBytes,
			&uploadedID,
			&uploader,
			&item.UploadedAt,
		); err != nil {
			return nil, fmt.Errorf("cases: baca bukti: %w", err)
		}
		item.UploadedBy = assignee(uploadedID, uploader)
		items = append(items, item)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("cases: iterasi bukti: %w", err)
	}
	return items, nil
}

const verificationsSQL = `
SELECT v.id, v.member_id, u.name, v.result,
       COALESCE(v.note_for_reporter, ''), v.created_at
FROM verifications v
JOIN users u ON u.id = v.member_id
WHERE v.report_id = $1
ORDER BY v.created_at DESC, v.id DESC`

func (s *Service) loadVerifications(ctx context.Context, reportID int64) ([]VerificationView, error) {
	rows, err := s.pool.Query(ctx, verificationsSQL, reportID)
	if err != nil {
		return nil, fmt.Errorf("cases: baca verifikasi: %w", err)
	}
	defer rows.Close()

	items := make([]VerificationView, 0, 2)
	for rows.Next() {
		var item VerificationView
		if err := rows.Scan(
			&item.ID,
			&item.Member.ID,
			&item.Member.Name,
			&item.Result,
			&item.NoteForReporter,
			&item.CreatedAt,
		); err != nil {
			return nil, fmt.Errorf("cases: baca verifikasi: %w", err)
		}
		items = append(items, item)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("cases: iterasi verifikasi: %w", err)
	}
	return items, nil
}

const statusHistorySQL = `
SELECT h.id, h.from_status, h.to_status, h.changed_by, u.name, h.changed_at
FROM status_history h
LEFT JOIN users u ON u.id = h.changed_by
WHERE h.report_id = $1
ORDER BY h.changed_at, h.id`

func (s *Service) loadStatusHistory(ctx context.Context, reportID int64) ([]StatusHistoryView, error) {
	rows, err := s.pool.Query(ctx, statusHistorySQL, reportID)
	if err != nil {
		return nil, fmt.Errorf("cases: baca riwayat status: %w", err)
	}
	defer rows.Close()

	items := make([]StatusHistoryView, 0, 6)
	for rows.Next() {
		var (
			item      StatusHistoryView
			changedBy *int64
			changer   *string
		)
		if err := rows.Scan(
			&item.ID,
			&item.FromStatus,
			&item.ToStatus,
			&changedBy,
			&changer,
			&item.ChangedAt,
		); err != nil {
			return nil, fmt.Errorf("cases: baca riwayat status: %w", err)
		}
		item.ChangedBy = assignee(changedBy, changer)
		items = append(items, item)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("cases: iterasi riwayat status: %w", err)
	}
	return items, nil
}

const actionHistorySQL = `
SELECT a.action, a.actor_type, COALESCE(u.name, ''), a.created_at
FROM audit_logs a
LEFT JOIN users u ON u.id = a.user_id
WHERE a.entity = 'report' AND a.entity_id = $1
  AND a.action::text = ANY($2)
ORDER BY a.created_at, a.id`

func (s *Service) loadActionHistory(ctx context.Context, reportID int64) ([]historyAction, error) {
	rows, err := s.pool.Query(ctx, actionHistorySQL, reportID, actionHistoryWhitelist)
	if err != nil {
		return nil, fmt.Errorf("cases: baca riwayat aksi: %w", err)
	}
	defer rows.Close()

	items := make([]historyAction, 0, 6)
	for rows.Next() {
		var item historyAction
		if err := rows.Scan(&item.Action, &item.ActorType, &item.ActorName, &item.At); err != nil {
			return nil, fmt.Errorf("cases: baca riwayat aksi: %w", err)
		}
		items = append(items, item)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("cases: iterasi riwayat aksi: %w", err)
	}
	return items, nil
}

const reviewNoteSQL = `
SELECT COALESCE(rn.victim_identified, ''), COALESCE(rn.witness_identified, ''),
       COALESCE(rn.initial_evidence_note, ''), rn.member_id, u.name, rn.created_at
FROM review_notes rn
JOIN users u ON u.id = rn.member_id
WHERE rn.report_id = $1`

func (s *Service) loadReviewNote(ctx context.Context, reportID int64) (*review.ReviewView, error) {
	var view review.ReviewView
	err := s.pool.QueryRow(ctx, reviewNoteSQL, reportID).Scan(
		&view.VictimIdentified,
		&view.WitnessIdentified,
		&view.InitialEvidenceNote,
		&view.Member.ID,
		&view.Member.Name,
		&view.CreatedAt,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("cases: baca catatan penelaahan: %w", err)
	}
	return &view, nil
}

const sessionsSQL = `
SELECT s.id, s.examiner_id, u.name, s.session_date, COALESCE(s.summary, ''), s.created_at
FROM examination_sessions s
JOIN users u ON u.id = s.examiner_id
WHERE s.report_id = $1
ORDER BY s.session_date, s.id`

const sessionParticipantsByReportSQL = `
SELECT sp.session_id, sp.party_id, sp.role, p.party_type, COALESCE(p.name, '')
FROM session_participants sp
JOIN examination_sessions s ON s.id = sp.session_id
JOIN parties p ON p.id = sp.party_id
WHERE s.report_id = $1
ORDER BY sp.session_id, sp.party_id`

func (s *Service) loadSessions(ctx context.Context, reportID int64) ([]review.SessionView, error) {
	rows, err := s.pool.Query(ctx, sessionsSQL, reportID)
	if err != nil {
		return nil, fmt.Errorf("cases: baca sesi pemeriksaan: %w", err)
	}

	items := make([]review.SessionView, 0, 4)
	index := make(map[int64]int, 4)
	for rows.Next() {
		var (
			view review.SessionView
			date time.Time
		)
		if err := rows.Scan(
			&view.ID,
			&view.Examiner.ID,
			&view.Examiner.Name,
			&date,
			&view.Summary,
			&view.CreatedAt,
		); err != nil {
			rows.Close()
			return nil, fmt.Errorf("cases: baca sesi pemeriksaan: %w", err)
		}
		view.SessionDate = date.Format("2006-01-02")
		view.Participants = []review.SessionParticipantView{}
		index[view.ID] = len(items)
		items = append(items, view)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return nil, fmt.Errorf("cases: iterasi sesi pemeriksaan: %w", err)
	}
	rows.Close()

	if len(items) == 0 {
		return items, nil
	}

	participantRows, err := s.pool.Query(ctx, sessionParticipantsByReportSQL, reportID)
	if err != nil {
		return nil, fmt.Errorf("cases: baca peserta sesi: %w", err)
	}
	defer participantRows.Close()

	for participantRows.Next() {
		var (
			sessionID   int64
			participant review.SessionParticipantView
		)
		if err := participantRows.Scan(
			&sessionID,
			&participant.PartyID,
			&participant.Role,
			&participant.PartyType,
			&participant.PartyName,
		); err != nil {
			return nil, fmt.Errorf("cases: baca peserta sesi: %w", err)
		}
		if position, ok := index[sessionID]; ok {
			items[position].Participants = append(items[position].Participants, participant)
		}
	}
	if err := participantRows.Err(); err != nil {
		return nil, fmt.Errorf("cases: iterasi peserta sesi: %w", err)
	}
	return items, nil
}

// nonNilStrings memastikan slice tidak dikirim sebagai null di JSON.
func nonNilStrings(in []string) []string {
	if in == nil {
		return []string{}
	}
	return in
}
