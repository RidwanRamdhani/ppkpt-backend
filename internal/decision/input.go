package decision

import (
	"fmt"
	"time"
	"unicode/utf8"

	"github.com/RidwanRamdhani/ppkpt-backend/internal/deadline"
	"github.com/RidwanRamdhani/ppkpt-backend/internal/platform/httpx"
)

const (
	// Batas panjang mengikuti kolom database dan pola modul referral D26.
	maxConclusionSummaryRunes = 5_000
	maxRecommendationRunes    = 5_000
	maxSanctionDetailRunes    = 5_000
	maxDeliveryNoteRunes      = 5_000
	maxDeliveredToRunes       = 150

	dateLayout = "2006-01-02"
)

const (
	conclusionTerbukti      = "TERBUKTI"
	conclusionTidakTerbukti = "TIDAK_TERBUKTI"

	decisionSanksi        = "SANKSI"
	decisionTidakTerbukti = "TIDAK_TERBUKTI"

	submissionDraft     = "DRAF"
	submissionSubmitted = "DIAJUKAN"
)

// Enum di bawah adalah satu-satunya sumber nilai sah. Kolom database sudah
// memiliki CHECK, tetapi validasi di sini memberi pesan per-field dan mencegah
// nilai tak dikenal menyentuh database.
var (
	validResults = map[string]struct{}{
		conclusionTerbukti:      {},
		conclusionTidakTerbukti: {},
	}
	validDecisionTypes = map[string]struct{}{
		decisionSanksi:        {},
		decisionTidakTerbukti: {},
	}
	validRecommendationCategories = map[string]struct{}{
		"ADMINISTRATIF": {},
		"AKADEMIK":      {},
		"KEPEGAWAIAN":   {},
	}
	validRecommendationSeverities = map[string]struct{}{
		"RINGAN": {},
		"SEDANG": {},
		"BERAT":  {},
	}
)

// RecommendationRequest adalah satu rekomendasi pada body kesimpulan.
type RecommendationRequest struct {
	Category    string `json:"category"`
	Severity    string `json:"severity"`
	Description string `json:"description"`
}

// RecommendationInput adalah rekomendasi yang sudah divalidasi.
type RecommendationInput struct {
	Category    string
	Severity    string
	Description string
}

// ConclusionRequest adalah body POST /api/cases/{id}/conclusion.
type ConclusionRequest struct {
	Result          string                  `json:"result"`
	Summary         string                  `json:"summary"`
	Recommendations []RecommendationRequest `json:"recommendations"`
}

// ConclusionInput adalah kesimpulan yang sudah divalidasi dan siap ditulis.
type ConclusionInput struct {
	Result          string
	Summary         string
	Recommendations []RecommendationInput
}

// Validate menormalkan dan memeriksa kesimpulan beserta rekomendasinya.
// Kesimpulan TERBUKTI wajib memiliki minimal satu rekomendasi; kesimpulan
// TIDAK_TERBUKTI tidak boleh memiliki rekomendasi (FR-36/FR-37).
func (r ConclusionRequest) Validate() (ConclusionInput, *httpx.AppError) {
	fields := make(map[string]string)

	result := trimText(r.Result)
	if result == "" {
		fields["result"] = "Hasil kesimpulan wajib diisi."
	} else if _, ok := validResults[result]; !ok {
		fields["result"] = "Nilai harus TERBUKTI atau TIDAK_TERBUKTI."
	}

	summary := trimText(r.Summary)
	if summary == "" {
		fields["summary"] = "Ringkasan kesimpulan wajib diisi."
	}
	maxRunesField(fields, "summary", summary, maxConclusionSummaryRunes)

	recommendations := make([]RecommendationInput, 0, len(r.Recommendations))
	for i, rec := range r.Recommendations {
		category := trimText(rec.Category)
		severity := trimText(rec.Severity)
		description := trimText(rec.Description)
		prefix := fmt.Sprintf("recommendations[%d].", i)

		if _, ok := validRecommendationCategories[category]; !ok {
			fields[prefix+"category"] = "Nilai harus ADMINISTRATIF, AKADEMIK, atau KEPEGAWAIAN."
		}
		if _, ok := validRecommendationSeverities[severity]; !ok {
			fields[prefix+"severity"] = "Nilai harus RINGAN, SEDANG, atau BERAT."
		}
		maxRunesField(fields, prefix+"description", description, maxRecommendationRunes)

		recommendations = append(recommendations, RecommendationInput{
			Category:    category,
			Severity:    severity,
			Description: description,
		})
	}

	// Hubungan hasil dan rekomendasi hanya diperiksa saat hasil valid agar
	// pengguna tidak menerima dua pesan yang saling bergantung.
	if _, ok := validResults[result]; ok {
		switch result {
		case conclusionTerbukti:
			if len(recommendations) == 0 {
				fields["recommendations"] = "Kesimpulan TERBUKTI wajib memiliki minimal satu rekomendasi."
			}
		case conclusionTidakTerbukti:
			if len(recommendations) > 0 {
				fields["recommendations"] = "Kesimpulan TIDAK_TERBUKTI tidak boleh memiliki rekomendasi."
			}
		}
	}

	if len(fields) > 0 {
		appErr := httpx.ErrValidation("Data kesimpulan belum lengkap atau tidak valid.")
		appErr.Fields = fields
		return ConclusionInput{}, appErr
	}
	return ConclusionInput{
		Result:          result,
		Summary:         summary,
		Recommendations: recommendations,
	}, nil
}

// DecisionRequest adalah body POST /api/cases/{id}/decision.
type DecisionRequest struct {
	DecisionType   string `json:"decision_type"`
	SanctionDetail string `json:"sanction_detail"`
	DecidedAt      string `json:"decided_at"`
}

// DecisionInput adalah keputusan yang sudah divalidasi dan siap ditulis.
type DecisionInput struct {
	DecisionType   string
	SanctionDetail string
	DecidedAt      time.Time
}

// Validate menormalkan dan memeriksa keputusan Pejabat. Rincian sanksi wajib
// untuk keputusan SANKSI dan diabaikan untuk TIDAK_TERBUKTI (BRU-04: sistem
// tidak menentukan sanksi otomatis).
func (r DecisionRequest) Validate(now time.Time) (DecisionInput, *httpx.AppError) {
	fields := make(map[string]string)

	decisionType := trimText(r.DecisionType)
	if decisionType == "" {
		fields["decision_type"] = "Jenis keputusan wajib diisi."
	} else if _, ok := validDecisionTypes[decisionType]; !ok {
		fields["decision_type"] = "Nilai harus SANKSI atau TIDAK_TERBUKTI."
	}

	sanctionDetail := trimText(r.SanctionDetail)
	if decisionType == decisionSanksi {
		if sanctionDetail == "" {
			fields["sanction_detail"] = "Rincian sanksi wajib diisi untuk keputusan SANKSI."
		}
		maxRunesField(fields, "sanction_detail", sanctionDetail, maxSanctionDetailRunes)
	} else {
		// Rincian sanksi tidak bermakna untuk TIDAK_TERBUKTI.
		sanctionDetail = ""
	}

	decidedAt, _ := validatePastDate(fields, "decided_at", r.DecidedAt, now)

	if len(fields) > 0 {
		appErr := httpx.ErrValidation("Data keputusan belum lengkap atau tidak valid.")
		appErr.Fields = fields
		return DecisionInput{}, appErr
	}
	return DecisionInput{
		DecisionType:   decisionType,
		SanctionDetail: sanctionDetail,
		DecidedAt:      decidedAt,
	}, nil
}

// DeliveryRequest adalah body POST /api/cases/{id}/delivery.
type DeliveryRequest struct {
	DeliveredTo  string `json:"delivered_to"`
	DeliveredAt  string `json:"delivered_at"`
	NameRestored bool   `json:"name_restored"`
	Note         string `json:"note"`
}

// DeliveryInput adalah penyampaian hasil yang sudah divalidasi.
type DeliveryInput struct {
	DeliveredTo  string
	DeliveredAt  time.Time
	NameRestored bool
	Note         string
}

// Validate menormalkan dan memeriksa penyampaian hasil (FR-41).
func (r DeliveryRequest) Validate(now time.Time) (DeliveryInput, *httpx.AppError) {
	fields := make(map[string]string)

	deliveredTo := trimText(r.DeliveredTo)
	if deliveredTo == "" {
		fields["delivered_to"] = "Pihak penerima hasil wajib diisi."
	}
	maxRunesField(fields, "delivered_to", deliveredTo, maxDeliveredToRunes)

	deliveredAt, _ := validatePastDate(fields, "delivered_at", r.DeliveredAt, now)

	note := trimText(r.Note)
	maxRunesField(fields, "note", note, maxDeliveryNoteRunes)

	if len(fields) > 0 {
		appErr := httpx.ErrValidation("Data penyampaian hasil belum lengkap atau tidak valid.")
		appErr.Fields = fields
		return DeliveryInput{}, appErr
	}
	return DeliveryInput{
		DeliveredTo:  deliveredTo,
		DeliveredAt:  deliveredAt,
		NameRestored: r.NameRestored,
		Note:         note,
	}, nil
}

// validatePastDate mem-parse tanggal YYYY-MM-DD pada zona WIB dan menolak
// tanggal setelah hari ini. Bila tanggal tidak valid, kesalahan dicatat pada
// fields dan nilai nol dikembalikan.
func validatePastDate(fields map[string]string, field, raw string, now time.Time) (time.Time, bool) {
	value := trimText(raw)
	if value == "" {
		fields[field] = "Tanggal wajib diisi."
		return time.Time{}, false
	}
	parsed, err := time.ParseInLocation(dateLayout, value, deadline.Jakarta)
	if err != nil {
		fields[field] = "Format tanggal harus YYYY-MM-DD."
		return time.Time{}, false
	}
	if parsed.After(startOfDay(now)) {
		fields[field] = "Tanggal tidak boleh di masa depan."
		return time.Time{}, false
	}
	return parsed, true
}

// startOfDay mengembalikan tengah malam WIB pada tanggal t.
func startOfDay(t time.Time) time.Time {
	local := t.In(deadline.Jakarta)
	return time.Date(local.Year(), local.Month(), local.Day(), 0, 0, 0, 0, deadline.Jakarta)
}

// maxRunesField mencatat kesalahan bila panjang teks melebihi batas.
func maxRunesField(fields map[string]string, field, value string, limit int) {
	if utf8.RuneCountInString(value) > limit {
		fields[field] = fmt.Sprintf("Maksimal %d karakter.", limit)
	}
}
