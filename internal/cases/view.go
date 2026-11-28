package cases

import (
	"time"

	"github.com/RidwanRamdhani/ppkpt-backend/internal/referral"
	"github.com/RidwanRamdhani/ppkpt-backend/internal/review"
)

// Assignee adalah petugas Satgas yang menjadi penanggung jawab kasus. Hanya
// berisi identitas petugas, bukan pihak kasus.
type Assignee struct {
	ID   int64  `json:"id"`
	Name string `json:"name"`
}

// DeadlineView adalah sisa waktu tahap berjalan. DaysLeft negatif berarti
// tenggat sudah lewat. State bernilai NORMAL, MENDEKATI, atau MELEWATI.
type DeadlineView struct {
	DueAt    time.Time `json:"due_at"`
	DaysLeft int       `json:"days_left"`
	State    string    `json:"state"`
}

// ConclusionSummary adalah ringkasan kesimpulan yang aman ditampilkan di
// daftar kasus. Summary diisi hanya pada detail, bukan pada daftar.
type ConclusionSummary struct {
	Result           string     `json:"result"`
	SubmissionStatus string     `json:"submission_status"`
	Summary          string     `json:"summary,omitempty"`
	SubmittedAt      *time.Time `json:"submitted_at,omitempty"`
}

// DecisionSummary adalah ringkasan keputusan pejabat.
type DecisionSummary struct {
	DecisionType string     `json:"decision_type"`
	DecidedAt    *time.Time `json:"decided_at,omitempty"`
}

// SatgasListItem adalah proyeksi daftar kasus untuk Satgas. Tidak memuat data
// pelapor maupun pihak lain.
type SatgasListItem struct {
	ID             int64         `json:"id"`
	CaseNumber     string        `json:"case_number"`
	ViolationTypes []string      `json:"violation_types"`
	Channel        string        `json:"channel"`
	Status         string        `json:"status"`
	CaseCondition  *string       `json:"case_condition"`
	AssignedTo     *Assignee     `json:"assigned_to"`
	Deadline       *DeadlineView `json:"deadline"`
	CreatedAt      time.Time     `json:"created_at"`
}

// OfficialListItem adalah proyeksi daftar kasus untuk Pejabat. Tidak boleh
// memuat nama atau identitas pihak kasus (FR-45); nama Satgas PJ diperbolehkan.
type OfficialListItem struct {
	ID             int64              `json:"id"`
	CaseNumber     string             `json:"case_number"`
	ViolationTypes []string           `json:"violation_types"`
	Channel        string             `json:"channel"`
	Status         string             `json:"status"`
	AssignedTo     *Assignee          `json:"assigned_to"`
	Conclusion     *ConclusionSummary `json:"conclusion"`
	Decision       *DecisionSummary   `json:"decision"`
	CanReview      bool               `json:"can_review"`
	CreatedAt      time.Time          `json:"created_at"`
}

// ListPage adalah respons GET /api/cases. Items berisi []SatgasListItem atau
// []OfficialListItem sesuai role pemanggil.
type ListPage struct {
	Items    any   `json:"items"`
	Page     int   `json:"page"`
	PageSize int   `json:"page_size"`
	Total    int64 `json:"total"`
}

// ReporterView adalah identitas pelapor pada detail kasus Satgas. Untuk
// anggota Satgas yang bukan PJ dan kasus rahasia, field identitas dikosongkan
// dan Masked bernilai true.
type ReporterView struct {
	Name        string `json:"name"`
	Age         *int   `json:"age"`
	Gender      string `json:"gender"`
	Category    string `json:"category"`
	IDNumber    string `json:"id_number"`
	Institution string `json:"institution"`
	Faculty     string `json:"faculty"`
	Program     string `json:"program"`
	Phone       string `json:"phone"`
	Email       string `json:"email"`
	Masked      bool   `json:"masked"`
}

// PartyView adalah identitas satu pihak pada detail kasus Satgas.
type PartyView struct {
	ID              int64  `json:"id"`
	PartyType       string `json:"party_type"`
	Name            string `json:"name"`
	Gender          string `json:"gender"`
	IDNumber        string `json:"id_number"`
	Category        string `json:"category"`
	Institution     string `json:"institution"`
	Faculty         string `json:"faculty"`
	Program         string `json:"program"`
	Contact         string `json:"contact"`
	RelationToOther string `json:"relation_to_other"`
}

// IncidentView adalah satu kejadian. Tanggal memakai format YYYY-MM-DD.
type IncidentView struct {
	ID          int64   `json:"id"`
	DateStart   string  `json:"date_start"`
	DateEnd     *string `json:"date_end"`
	Location    string  `json:"location"`
	Description string  `json:"description"`
}

// EvidenceView adalah metadata berkas. Object key (stored_name) tidak pernah
// dikirim ke klien; unduhan selalu lewat endpoint terotorisasi.
type EvidenceView struct {
	ID           int64     `json:"id"`
	Category     string    `json:"category"`
	OriginalName string    `json:"original_name"`
	MimeType     string    `json:"mime_type"`
	SizeBytes    int64     `json:"size_bytes"`
	UploadedBy   *Assignee `json:"uploaded_by"`
	UploadedAt   time.Time `json:"uploaded_at"`
}

// VerificationView adalah catatan verifikasi yang sudah dibuat.
type VerificationView struct {
	ID              int64     `json:"id"`
	Member          Assignee  `json:"member"`
	Result          string    `json:"result"`
	NoteForReporter string    `json:"note_for_reporter"`
	CreatedAt       time.Time `json:"created_at"`
}

// RecommendationView adalah satu rekomendasi pada kesimpulan kasus.
type RecommendationView struct {
	ID          int64  `json:"id"`
	Category    string `json:"category"`
	Severity    string `json:"severity"`
	Description string `json:"description"`
}

// DecisionDetail adalah keputusan Pejabat yang sudah tercatat.
type DecisionDetail struct {
	ID             int64     `json:"id"`
	DecisionType   string    `json:"decision_type"`
	SanctionDetail string    `json:"sanction_detail"`
	DecidedAt      string    `json:"decided_at"`
	Official       *Assignee `json:"official"`
	CreatedAt      time.Time `json:"created_at"`
}

// DeliveryView adalah satu catatan penyampaian hasil kepada pihak terkait.
type DeliveryView struct {
	ID           int64     `json:"id"`
	DeliveredTo  string    `json:"delivered_to"`
	DeliveredAt  string    `json:"delivered_at"`
	NameRestored bool      `json:"name_restored"`
	Note         string    `json:"note"`
	CreatedBy    *Assignee `json:"created_by"`
}

// StatusHistoryView adalah satu baris riwayat status kasus.
type StatusHistoryView struct {
	ID         int64     `json:"id"`
	FromStatus *string   `json:"from_status"`
	ToStatus   string    `json:"to_status"`
	ChangedBy  *Assignee `json:"changed_by"`
	ChangedAt  time.Time `json:"changed_at"`
}

// SatgasDetail adalah respons detail kasus untuk Satgas. Data pihak dan isi
// laporan boleh ada di sini karena pemanggilnya anggota Satgas; identitas
// pelapor tetap disamarkan bila kasus rahasia dan pemanggil bukan PJ.
type SatgasDetail struct {
	ID                    int64                   `json:"id"`
	CaseNumber            string                  `json:"case_number"`
	Channel               string                  `json:"channel"`
	ReferralUnit          string                  `json:"referral_unit"`
	Status                string                  `json:"status"`
	CaseCondition         *string                 `json:"case_condition"`
	ReporterConfidential  bool                    `json:"reporter_confidential"`
	ReporterIsVictim      bool                    `json:"reporter_is_victim"`
	Chronology            string                  `json:"chronology"`
	AdditionalDescription string                  `json:"additional_description"`
	ReportedElsewhere     bool                    `json:"reported_elsewhere"`
	ReportedTo            string                  `json:"reported_to"`
	EffortsMade           string                  `json:"efforts_made"`
	Expectation           string                  `json:"expectation"`
	AssignedTo            *Assignee               `json:"assigned_to"`
	EnteredBy             *Assignee               `json:"entered_by"`
	ReceivedAt            *time.Time              `json:"received_at"`
	ReviewStartedAt       *time.Time              `json:"review_started_at"`
	ReviewDueAt           *time.Time              `json:"review_due_at"`
	ExamStartedAt         *time.Time              `json:"exam_started_at"`
	ExamDueAt             *time.Time              `json:"exam_due_at"`
	ClosedAt              *time.Time              `json:"closed_at"`
	CreatedAt             time.Time               `json:"created_at"`
	UpdatedAt             time.Time               `json:"updated_at"`
	Deadline              *DeadlineView           `json:"deadline"`
	ViolationTypes        []string                `json:"violation_types"`
	Reporter              *ReporterView           `json:"reporter"`
	Parties               []PartyView             `json:"parties"`
	Incidents             []IncidentView          `json:"incidents"`
	Evidences             []EvidenceView          `json:"evidences"`
	Verifications         []VerificationView      `json:"verifications"`
	ReviewNote            *review.ReviewView      `json:"review_note"`
	Sessions              []review.SessionView    `json:"sessions"`
	Referrals             []referral.ReferralView `json:"referrals"`
	Recoveries            []referral.RecoveryView `json:"recoveries"`
	Recommendations       []RecommendationView    `json:"recommendations"`
	Decision              *DecisionDetail         `json:"decision"`
	Deliveries            []DeliveryView          `json:"deliveries"`
	StatusHistory         []StatusHistoryView     `json:"status_history"`
	Conclusion            *ConclusionSummary      `json:"conclusion"`
	IsPJ                  bool                    `json:"is_pj"`
	CanTake               bool                    `json:"can_take"`
	CanVerify             bool                    `json:"can_verify"`
	CanReview             bool                    `json:"can_review"`
	CanExamine            bool                    `json:"can_examine"`
	CanSetCondition       bool                    `json:"can_set_condition"`
	CanRefer              bool                    `json:"can_refer"`
	CanRecordRecovery     bool                    `json:"can_record_recovery"`
	CanWriteConclusion    bool                    `json:"can_write_conclusion"`
	CanSubmitConclusion   bool                    `json:"can_submit_conclusion"`
	CanRecordDelivery     bool                    `json:"can_record_delivery"`
	CanClose              bool                    `json:"can_close"`
}

// OfficialDetail adalah respons detail kasus untuk Pejabat. Hanya tersedia
// setelah kesimpulan diajukan dan tidak memuat identitas pihak mana pun.
type OfficialDetail struct {
	ID             int64              `json:"id"`
	CaseNumber     string             `json:"case_number"`
	Channel        string             `json:"channel"`
	Status         string             `json:"status"`
	AssignedTo     *Assignee          `json:"assigned_to"`
	Conclusion     *ConclusionSummary `json:"conclusion"`
	CanReview      bool               `json:"can_review"`
	CreatedAt      time.Time          `json:"created_at"`
	ViolationTypes []string           `json:"violation_types"`
}

// maskedReporterName adalah nilai pengganti nama pelapor pada kasus rahasia.
const maskedReporterName = "Dirahasiakan"

// maskReporter mengosongkan identitas pelapor. Data yang tetap ditampilkan
// (usia, gender, kategori, institusi) tidak cukup untuk mengenali pelapor dan
// dibutuhkan untuk penanganan kasus.
func maskReporter(r *ReporterView) {
	if r == nil {
		return
	}
	r.Name = maskedReporterName
	r.IDNumber = ""
	r.Phone = ""
	r.Email = ""
	r.Masked = true
}

// conclusionSummary membangun ringkasan dari nilai nullable hasil query.
func conclusionSummary(result, status *string, submittedAt *time.Time) *ConclusionSummary {
	if result == nil && status == nil {
		return nil
	}
	summary := &ConclusionSummary{SubmittedAt: submittedAt}
	if result != nil {
		summary.Result = *result
	}
	if status != nil {
		summary.SubmissionStatus = *status
	}
	return summary
}

// readyToReview melaporkan apakah Pejabat sudah boleh meninjau kasus:
// kesimpulan sudah diajukan dan kasus berada pada tahap REKOMENDASI.
func readyToReview(status string, conclusion *ConclusionSummary) bool {
	return status == "REKOMENDASI" && conclusion != nil && conclusion.SubmissionStatus == "DIAJUKAN"
}
