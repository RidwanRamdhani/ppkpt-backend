package decision

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/RidwanRamdhani/ppkpt-backend/internal/auth"
	"github.com/RidwanRamdhani/ppkpt-backend/internal/platform/httpx"
	"github.com/RidwanRamdhani/ppkpt-backend/internal/workflow"
	"github.com/jackc/pgx/v5"
)

// DecisionEvidence adalah metadata berkas BAI/lampiran pada proyeksi Pejabat.
// Object key (stored_name) tidak pernah dikirim ke klien; unduhan lewat
// endpoint terotorisasi.
type DecisionEvidence struct {
	ID           int64     `json:"id"`
	Category     string    `json:"category"`
	OriginalName string    `json:"original_name"`
	MimeType     string    `json:"mime_type"`
	SizeBytes    int64     `json:"size_bytes"`
	UploadedAt   time.Time `json:"uploaded_at"`
}

// DecisionView adalah proyeksi terbatas untuk Pejabat (FR-38,
// OfficialViewPresenter): ringkasan kasus, kesimpulan, rekomendasi, dan daftar
// BAI/lampiran. Tidak memuat isi pemeriksaan maupun identitas pihak kasus.
type DecisionView struct {
	ID             int64              `json:"id"`
	CaseNumber     string             `json:"case_number"`
	Channel        string             `json:"channel"`
	Status         string             `json:"status"`
	ViolationTypes []string           `json:"violation_types"`
	CreatedAt      time.Time          `json:"created_at"`
	SubmittedAt    *time.Time         `json:"submitted_at"`
	AssignedTo     *MemberRef         `json:"assigned_to"`
	Conclusion     ConclusionView     `json:"conclusion"`
	Evidences      []DecisionEvidence `json:"evidences"`
}

const decisionViewSQL = `
SELECT r.id, r.case_number, r.channel, r.status, r.created_at,
       r.assigned_to, u.name, c.submitted_at,
       COALESCE((
           SELECT array_agg(vt.code::text ORDER BY vt.code)
           FROM report_violation_types rvt
           JOIN violation_types vt ON vt.id = rvt.violation_type_id
           WHERE rvt.report_id = r.id
       ), '{}'::text[]) AS violation_types
FROM reports r
LEFT JOIN users u ON u.id = r.assigned_to
JOIN conclusions c ON c.report_id = r.id
WHERE r.id = $1`

const decisionEvidenceSQL = `
SELECT id, category, original_name, mime_type, size_bytes, uploaded_at
FROM evidences
WHERE report_id = $1 AND category IN ('BAI', 'LAMPIRAN')
ORDER BY id`

// ViewDecision mengembalikan proyeksi kasus untuk Pejabat yang akan memutuskan.
//
// Hanya kasus dengan kesimpulan DIAJUKAN pada tahap REKOMENDASI yang dapat
// dibuka. Kasus yang belum diajukan ditolak 403 (sama seperti GET detail kasus
// untuk Pejabat); kasus yang sudah melewati tahap ini ditolak 409.
func (s *Service) ViewDecision(
	ctx context.Context,
	actor Actor,
	reportID int64,
	ip string,
) (*DecisionView, error) {
	if actor.Role != auth.RolePejabat {
		return nil, httpx.ErrForbidden()
	}

	info, err := s.loadCase(ctx, s.pool, reportID)
	if err != nil {
		return nil, err
	}
	if !conclusionSubmitted(info) {
		return nil, httpx.ErrForbidden()
	}
	if info.status != string(workflow.Rekomendasi) {
		return nil, httpx.ErrConflict("Kasus tidak berada pada tahap REKOMENDASI.")
	}

	view, err := s.loadDecisionView(ctx, reportID)
	if err != nil {
		return nil, err
	}

	s.logViewed(ctx, actor, reportID, ip)
	return view, nil
}

func (s *Service) loadDecisionView(ctx context.Context, reportID int64) (*DecisionView, error) {
	var (
		view           DecisionView
		assignedID     *int64
		assignedName   *string
		violationTypes []string
	)
	err := s.pool.QueryRow(ctx, decisionViewSQL, reportID).Scan(
		&view.ID,
		&view.CaseNumber,
		&view.Channel,
		&view.Status,
		&view.CreatedAt,
		&assignedID,
		&assignedName,
		&view.SubmittedAt,
		&violationTypes,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, httpx.ErrNotFound()
	}
	if err != nil {
		return nil, fmt.Errorf("decision: baca proyeksi keputusan: %w", err)
	}
	view.ViolationTypes = nonNilStrings(violationTypes)
	view.AssignedTo = memberRef(assignedID, assignedName)

	conclusion, err := loadConclusion(ctx, s.pool, reportID)
	if err != nil {
		return nil, err
	}
	view.Conclusion = conclusion

	evidences, err := s.loadDecisionEvidences(ctx, reportID)
	if err != nil {
		return nil, err
	}
	view.Evidences = evidences
	return &view, nil
}

func (s *Service) loadDecisionEvidences(ctx context.Context, reportID int64) ([]DecisionEvidence, error) {
	rows, err := s.pool.Query(ctx, decisionEvidenceSQL, reportID)
	if err != nil {
		return nil, fmt.Errorf("decision: baca berkas kasus: %w", err)
	}
	defer rows.Close()

	items := make([]DecisionEvidence, 0, 4)
	for rows.Next() {
		var item DecisionEvidence
		if err := rows.Scan(
			&item.ID,
			&item.Category,
			&item.OriginalName,
			&item.MimeType,
			&item.SizeBytes,
			&item.UploadedAt,
		); err != nil {
			return nil, fmt.Errorf("decision: baca berkas kasus: %w", err)
		}
		items = append(items, item)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("decision: iterasi berkas kasus: %w", err)
	}
	return items, nil
}

// memberRef membangun pointer MemberRef hanya bila kedua kolom terisi.
func memberRef(id *int64, name *string) *MemberRef {
	if id == nil || name == nil {
		return nil
	}
	return &MemberRef{ID: *id, Name: *name}
}

// nonNilStrings memastikan slice selalu dapat di-serialisasi sebagai [] dan
// tidak pernah null.
func nonNilStrings(values []string) []string {
	if values == nil {
		return []string{}
	}
	return values
}
