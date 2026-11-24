package cases

import (
	"strings"
	"testing"
	"time"

	"github.com/RidwanRamdhani/ppkpt-backend/internal/deadline"
	"github.com/RidwanRamdhani/ppkpt-backend/internal/report"
)

func manualNow() time.Time {
	return time.Date(2026, time.November, 21, 10, 0, 0, 0, deadline.Jakarta)
}

func validManualRequest() ManualRequest {
	age := 30
	return ManualRequest{
		SubmitRequest: report.SubmitRequest{
			Reporter: report.ReporterInput{
				Name:        "Pelapor Manual",
				Age:         &age,
				Gender:      "PEREMPUAN",
				Category:    "DOSEN",
				Institution: "Telkom University",
				Phone:       "081234567890",
			},
			ReporterIsVictim: true,
			Accused: []report.PersonInput{{
				Name: "Terlapor Manual",
			}},
			Incidents: []report.IncidentRequest{{
				DateStart:   "2026-09-15",
				Location:    "Gedung A",
				Description: "Kejadian uji",
			}},
			ViolationTypes: []string{"PERUNDUNGAN"},
			Chronology:     "Kronologi input manual.",
		},
		Channel:     "hotline",
		ReceivedAt:  "2026-11-20",
		OfficerNote: "  Catatan petugas.  ",
	}
}

func TestManualRequestValidateValid(t *testing.T) {
	input, appErr := validManualRequest().Validate(manualNow())
	if appErr != nil {
		t.Fatalf("Validate = %v (fields: %v)", appErr, appErr.Fields)
	}
	if input.Channel != "HOTLINE" {
		t.Errorf("channel = %q", input.Channel)
	}
	if input.OfficerNote != "Catatan petugas." {
		t.Errorf("officer note = %q", input.OfficerNote)
	}
	if input.ReceivedAt.Format("2006-01-02") != "2026-11-20" {
		t.Errorf("received_at = %s", input.ReceivedAt.Format("2006-01-02"))
	}
	if input.Submission.Reporter.Email != "" {
		t.Errorf("email = %q, ingin kosong", input.Submission.Reporter.Email)
	}
}

func TestManualRequestReceivedAtDefaultSekarang(t *testing.T) {
	req := validManualRequest()
	req.ReceivedAt = ""

	input, appErr := req.Validate(manualNow())
	if appErr != nil {
		t.Fatalf("Validate = %v", appErr)
	}
	if !input.ReceivedAt.Equal(manualNow()) {
		t.Errorf("received_at = %v, ingin %v", input.ReceivedAt, manualNow())
	}
}

func TestManualRequestChannel(t *testing.T) {
	tests := []struct {
		name    string
		channel string
		field   string
	}{
		{"kosong", "", "channel"},
		{"portal ditolak", "PORTAL", "channel"},
		{"kanal asing", "EMAIL", "channel"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := validManualRequest()
			req.Channel = tt.channel
			_, appErr := req.Validate(manualNow())
			if appErr == nil || appErr.Fields[tt.field] == "" {
				t.Fatalf("ingin error %s, dapat %+v", tt.field, appErr)
			}
		})
	}
}

func TestManualRequestRujukanWajibUnit(t *testing.T) {
	req := validManualRequest()
	req.Channel = "RUJUKAN"
	req.ReferralUnit = ""

	if _, appErr := req.Validate(manualNow()); appErr == nil || appErr.Fields["referral_unit"] == "" {
		t.Fatalf("ingin error referral_unit, dapat %+v", appErr)
	}

	req.ReferralUnit = "Layanan Psikologi"
	input, appErr := req.Validate(manualNow())
	if appErr != nil {
		t.Fatalf("Validate = %v", appErr)
	}
	if input.ReferralUnit != "Layanan Psikologi" {
		t.Errorf("referral unit = %q", input.ReferralUnit)
	}
}

func TestManualRequestTanggalMasaDepanDitolak(t *testing.T) {
	req := validManualRequest()
	req.ReceivedAt = "2026-11-22"

	_, appErr := req.Validate(manualNow())
	if appErr == nil || appErr.Fields["received_at"] == "" {
		t.Fatalf("ingin error received_at, dapat %+v", appErr)
	}
}

func TestManualRequestEmailSalahDitolak(t *testing.T) {
	req := validManualRequest()
	req.Reporter.Email = "bukan-email"

	_, appErr := req.Validate(manualNow())
	if appErr == nil || appErr.Fields["reporter.email"] == "" {
		t.Fatalf("ingin error reporter.email, dapat %+v", appErr)
	}
}

func TestManualRequestOfficerNoteTerlaluPanjang(t *testing.T) {
	req := validManualRequest()
	req.OfficerNote = strings.Repeat("a", maxOfficerNoteRunes+1)

	_, appErr := req.Validate(manualNow())
	if appErr == nil || appErr.Fields["officer_note"] == "" {
		t.Fatalf("ingin error officer_note, dapat %+v", appErr)
	}
}

func TestManualRequestMengumpulkanSemuaError(t *testing.T) {
	req := validManualRequest()
	req.Channel = ""
	req.Reporter.Name = ""
	req.Chronology = ""

	_, appErr := req.Validate(manualNow())
	if appErr == nil {
		t.Fatal("ingin error validasi")
	}
	for _, field := range []string{"channel", "reporter.name", "chronology"} {
		if appErr.Fields[field] == "" {
			t.Errorf("field %q tidak ada di %+v", field, appErr.Fields)
		}
	}
}

func TestMergeOfficerNote(t *testing.T) {
	now := manualNow()
	got := mergeOfficerNote("Catatan awal", "Catatan petugas", now)
	if !strings.Contains(got, "Catatan awal") ||
		!strings.Contains(got, "Catatan petugas") ||
		!strings.Contains(got, "[Petugas 2026-11-21 10:00 WIB]") {
		t.Fatalf("mergeOfficerNote = %q", got)
	}

	if got := mergeOfficerNote("", "", now); got != "" {
		t.Errorf("merge kosong = %q", got)
	}
	if got := mergeOfficerNote("", "hanya petugas", now); !strings.HasPrefix(got, "[Petugas") {
		t.Errorf("merge tanpa base = %q", got)
	}
}
