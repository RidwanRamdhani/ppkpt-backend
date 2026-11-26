package referral

import (
	"fmt"
	"time"
	"unicode/utf8"

	"github.com/RidwanRamdhani/ppkpt-backend/internal/deadline"
	"github.com/RidwanRamdhani/ppkpt-backend/internal/platform/httpx"
)

const (
	// maxInstitutionRunes mengikuti kolom referrals.institution VARCHAR(150).
	maxInstitutionRunes  = 150
	maxNotesRunes        = 5_000
	maxRecoveryNoteRunes = 5_000
	dateLayout           = "2006-01-02"
)

const (
	conditionDarurat      = "DARURAT"
	conditionTidakDarurat = "TIDAK_DARURAT"

	followUpDiajukan = "DIAJUKAN"
)

// Enum di bawah adalah satu-satunya sumber nilai sah. Kolom
// referrals.follow_up_status tidak memiliki CHECK di database, sehingga
// validasi di sini wajib agar nilai tak dikenal tidak masuk.
var (
	validConditions = map[string]struct{}{
		conditionDarurat:      {},
		conditionTidakDarurat: {},
	}
	validServiceTypes = map[string]struct{}{
		"PSIKOLOGIS":   {},
		"HUKUM":        {},
		"MEDIS":        {},
		"PERLINDUNGAN": {},
	}
	validFollowUpStatuses = map[string]struct{}{
		followUpDiajukan: {},
		"DIPROSES":       {},
		"SELESAI":        {},
	}
	validRecoveryTypes = map[string]struct{}{
		"PEMANTAUAN": {},
		"EVALUASI":   {},
		"PENCEGAHAN": {},
	}
)

// ConditionRequest adalah body PUT /api/cases/{id}/condition.
type ConditionRequest struct {
	Condition string `json:"condition"`
}

// ConditionInput adalah kondisi yang sudah divalidasi.
type ConditionInput struct {
	Condition string
}

// Validate menormalkan dan memeriksa nilai kondisi kasus.
func (r ConditionRequest) Validate() (ConditionInput, *httpx.AppError) {
	condition := trimText(r.Condition)
	if condition == "" {
		appErr := httpx.ErrValidation("Kondisi kasus wajib diisi.")
		appErr.Fields = map[string]string{"condition": "Kondisi kasus wajib diisi."}
		return ConditionInput{}, appErr
	}
	if _, ok := validConditions[condition]; !ok {
		appErr := httpx.ErrValidation("Kondisi kasus tidak valid.")
		appErr.Fields = map[string]string{
			"condition": "Nilai harus DARURAT atau TIDAK_DARURAT.",
		}
		return ConditionInput{}, appErr
	}
	return ConditionInput{Condition: condition}, nil
}

// ReferralRequest adalah body POST /api/cases/{id}/referrals.
type ReferralRequest struct {
	ServiceType    string `json:"service_type"`
	TargetPartyID  *int64 `json:"target_party_id"`
	Institution    string `json:"institution"`
	ReferralDate   string `json:"referral_date"`
	FollowUpStatus string `json:"follow_up_status"`
	Notes          string `json:"notes"`
}

// ReferralInput adalah rujukan yang sudah divalidasi dan siap ditulis.
type ReferralInput struct {
	ServiceType    string
	TargetPartyID  *int64
	Institution    string
	ReferralDate   time.Time
	FollowUpStatus string
	Notes          string
}

// Validate menormalkan dan memeriksa rujukan. Tanggal rujukan memakai zona WIB
// dan tidak boleh di masa depan, mengikuti aturan sesi pemeriksaan (D21).
func (r ReferralRequest) Validate(now time.Time) (ReferralInput, *httpx.AppError) {
	fields := make(map[string]string)

	serviceType := trimText(r.ServiceType)
	if serviceType == "" {
		fields["service_type"] = "Jenis layanan wajib diisi."
	} else if _, ok := validServiceTypes[serviceType]; !ok {
		fields["service_type"] = "Nilai harus PSIKOLOGIS, HUKUM, MEDIS, atau PERLINDUNGAN."
	}

	if r.TargetPartyID != nil && *r.TargetPartyID <= 0 {
		fields["target_party_id"] = "ID pihak tidak valid."
	}

	institution := trimText(r.Institution)
	if institution == "" {
		fields["institution"] = "Institusi tujuan rujukan wajib diisi."
	}
	maxRunesField(fields, "institution", institution, maxInstitutionRunes)

	referralDate, _ := validatePastDate(fields, "referral_date", r.ReferralDate, now)

	followUp := trimText(r.FollowUpStatus)
	if followUp == "" {
		followUp = followUpDiajukan
	} else if _, ok := validFollowUpStatuses[followUp]; !ok {
		fields["follow_up_status"] = "Nilai harus DIAJUKAN, DIPROSES, atau SELESAI."
	}

	notes := trimText(r.Notes)
	maxRunesField(fields, "notes", notes, maxNotesRunes)

	if len(fields) > 0 {
		appErr := httpx.ErrValidation("Data rujukan belum lengkap atau tidak valid.")
		appErr.Fields = fields
		return ReferralInput{}, appErr
	}
	return ReferralInput{
		ServiceType:    serviceType,
		TargetPartyID:  r.TargetPartyID,
		Institution:    institution,
		ReferralDate:   referralDate,
		FollowUpStatus: followUp,
		Notes:          notes,
	}, nil
}

// ReferralUpdateRequest adalah body PUT /api/cases/{id}/referrals/{rid}.
// Hanya status tindak lanjut yang boleh diperbarui; data rujukan lain bersifat
// tetap agar riwayat tetap kredibel (D28).
type ReferralUpdateRequest struct {
	FollowUpStatus string `json:"follow_up_status"`
}

// ReferralUpdateInput adalah status tindak lanjut yang sudah divalidasi.
type ReferralUpdateInput struct {
	FollowUpStatus string
}

// Validate memeriksa status tindak lanjut rujukan.
func (r ReferralUpdateRequest) Validate() (ReferralUpdateInput, *httpx.AppError) {
	followUp := trimText(r.FollowUpStatus)
	if followUp == "" {
		appErr := httpx.ErrValidation("Status tindak lanjut wajib diisi.")
		appErr.Fields = map[string]string{"follow_up_status": "Status tindak lanjut wajib diisi."}
		return ReferralUpdateInput{}, appErr
	}
	if _, ok := validFollowUpStatuses[followUp]; !ok {
		appErr := httpx.ErrValidation("Status tindak lanjut tidak valid.")
		appErr.Fields = map[string]string{
			"follow_up_status": "Nilai harus DIAJUKAN, DIPROSES, atau SELESAI.",
		}
		return ReferralUpdateInput{}, appErr
	}
	return ReferralUpdateInput{FollowUpStatus: followUp}, nil
}

// RecoveryRequest adalah body POST /api/cases/{id}/recovery.
type RecoveryRequest struct {
	Type       string `json:"type"`
	Note       string `json:"note"`
	RecordedAt string `json:"recorded_at"`
}

// RecoveryInput adalah catatan pemulihan yang sudah divalidasi.
type RecoveryInput struct {
	Type       string
	Note       string
	RecordedAt time.Time
}

// Validate menormalkan dan memeriksa catatan pemulihan. Catatan wajib diisi
// karena kolom recovery_monitorings.note NOT NULL.
func (r RecoveryRequest) Validate(now time.Time) (RecoveryInput, *httpx.AppError) {
	fields := make(map[string]string)

	recoveryType := trimText(r.Type)
	if recoveryType == "" {
		fields["type"] = "Jenis pemulihan wajib diisi."
	} else if _, ok := validRecoveryTypes[recoveryType]; !ok {
		fields["type"] = "Nilai harus PEMANTAUAN, EVALUASI, atau PENCEGAHAN."
	}

	note := trimText(r.Note)
	if note == "" {
		fields["note"] = "Catatan pemulihan wajib diisi."
	}
	maxRunesField(fields, "note", note, maxRecoveryNoteRunes)

	recordedAt, _ := validatePastDate(fields, "recorded_at", r.RecordedAt, now)

	if len(fields) > 0 {
		appErr := httpx.ErrValidation("Data pemulihan belum lengkap atau tidak valid.")
		appErr.Fields = fields
		return RecoveryInput{}, appErr
	}
	return RecoveryInput{Type: recoveryType, Note: note, RecordedAt: recordedAt}, nil
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
