package cases

import (
	"strings"
	"time"
	"unicode/utf8"

	"github.com/RidwanRamdhani/ppkpt-backend/internal/deadline"
	"github.com/RidwanRamdhani/ppkpt-backend/internal/platform/httpx"
	"github.com/RidwanRamdhani/ppkpt-backend/internal/report"
)

const (
	maxReferralUnitRunes = 100
	maxOfficerNoteRunes  = 5_000
)

// ManualRequest adalah isi bagian "payload" pada POST /api/cases. Field
// laporan memakai kontrak yang sama dengan form portal, ditambah field khusus
// input manual.
type ManualRequest struct {
	report.SubmitRequest
	Channel              string `json:"channel"`
	ReferralUnit         string `json:"referral_unit"`
	ReporterConfidential bool   `json:"reporter_confidential"`
	ReceivedAt           string `json:"received_at"`
	OfficerNote          string `json:"officer_note"`
	TakeCase             bool   `json:"take_case"`
}

// ManualInput adalah payload input manual yang sudah divalidasi dan siap
// ditulis ke database.
type ManualInput struct {
	Submission           report.Submission
	Channel              string
	ReferralUnit         string
	ReporterConfidential bool
	ReceivedAt           time.Time
	OfficerNote          string
	TakeCase             bool
}

// validManualChannels adalah kanal yang boleh dipakai input manual. PORTAL
// hanya untuk pelaporan mandiri dan tetap membutuhkan persetujuan digital.
var validManualChannels = map[string]struct{}{
	"HOTLINE":    {},
	"TATAP_MUKA": {},
	"RUJUKAN":    {},
}

// Validate memeriksa payload input manual. Aturan laporan memakai
// report.ManualValidation (tanpa consent, email pelapor opsional), sedangkan
// kanal, unit rujukan, tanggal diterima, dan catatan petugas diperiksa di sini.
func (r ManualRequest) Validate(now time.Time) (ManualInput, *httpx.AppError) {
	fields := make(map[string]string)

	sub, appErr := r.ValidateWith(now, report.ManualValidation)
	if appErr != nil {
		for field, message := range appErr.Fields {
			fields[field] = message
		}
	}

	channel := strings.ToUpper(strings.TrimSpace(r.Channel))
	switch channel {
	case "":
		fields["channel"] = "Kanal laporan wajib dipilih."
	default:
		if _, ok := validManualChannels[channel]; !ok {
			fields["channel"] = "Kanal input manual harus HOTLINE, TATAP_MUKA, atau RUJUKAN."
		}
	}

	referralUnit := strings.TrimSpace(r.ReferralUnit)
	if channel == "RUJUKAN" && referralUnit == "" {
		fields["referral_unit"] = "Unit rujukan wajib diisi untuk kanal RUJUKAN."
	}
	if utf8.RuneCountInString(referralUnit) > maxReferralUnitRunes {
		fields["referral_unit"] = "Maksimal 100 karakter."
	}

	receivedAt := now
	if raw := strings.TrimSpace(r.ReceivedAt); raw != "" {
		parsed, err := time.ParseInLocation("2006-01-02", raw, deadline.Jakarta)
		if err != nil {
			fields["received_at"] = "Tanggal diterima tidak valid (YYYY-MM-DD)."
		} else if parsed.After(startOfDay(now)) {
			fields["received_at"] = "Tanggal diterima tidak boleh di masa depan."
		} else {
			receivedAt = parsed
		}
	}

	officerNote := strings.TrimSpace(r.OfficerNote)
	if utf8.RuneCountInString(officerNote) > maxOfficerNoteRunes {
		fields["officer_note"] = "Maksimal 5000 karakter."
	}

	if len(fields) > 0 {
		appErr := httpx.ErrValidation("Data input manual belum lengkap atau tidak valid.")
		appErr.Fields = fields
		return ManualInput{}, appErr
	}

	return ManualInput{
		Submission:           sub,
		Channel:              channel,
		ReferralUnit:         referralUnit,
		ReporterConfidential: r.ReporterConfidential,
		ReceivedAt:           receivedAt,
		OfficerNote:          officerNote,
		TakeCase:             r.TakeCase,
	}, nil
}

// startOfDay mengembalikan tengah malam tanggal now di zona WIB.
func startOfDay(now time.Time) time.Time {
	local := now.In(deadline.Jakarta)
	return time.Date(local.Year(), local.Month(), local.Day(), 0, 0, 0, 0, deadline.Jakarta)
}
