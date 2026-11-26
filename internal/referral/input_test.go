package referral

import (
	"strings"
	"testing"
	"time"

	"github.com/RidwanRamdhani/ppkpt-backend/internal/deadline"
	"github.com/RidwanRamdhani/ppkpt-backend/internal/workflow"
)

func fixedNow() time.Time {
	return time.Date(2026, 11, 26, 15, 30, 0, 0, deadline.Jakarta)
}

func TestConditionValidate(t *testing.T) {
	tests := []struct {
		name      string
		input     string
		want      string
		wantError bool
	}{
		{name: "darurat", input: "DARURAT", want: "DARURAT"},
		{name: "tidak darurat dengan spasi", input: " TIDAK_DARURAT ", want: "TIDAK_DARURAT"},
		{name: "kosong", input: "  ", wantError: true},
		{name: "nilai asing", input: "TINGGI", wantError: true},
		{name: "huruf kecil", input: "darurat", wantError: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, appErr := ConditionRequest{Condition: tt.input}.Validate()
			if tt.wantError {
				if appErr == nil || appErr.Status != 422 {
					t.Fatalf("ingin 422, dapat %+v", appErr)
				}
				return
			}
			if appErr != nil {
				t.Fatalf("tidak ingin error: %+v", appErr)
			}
			if got.Condition != tt.want {
				t.Errorf("kondisi = %q, ingin %q", got.Condition, tt.want)
			}
		})
	}
}

func validReferral() ReferralRequest {
	return ReferralRequest{
		ServiceType:  "PSIKOLOGIS",
		Institution:  "Unit Layanan Psikologi",
		ReferralDate: "2026-11-26",
		Notes:        "Rujukan awal",
	}
}

func TestReferralValidate(t *testing.T) {
	now := fixedNow()

	t.Run("valid dengan default status tindak lanjut", func(t *testing.T) {
		got, appErr := validReferral().Validate(now)
		if appErr != nil {
			t.Fatalf("tidak ingin error: %+v", appErr)
		}
		if got.FollowUpStatus != "DIAJUKAN" {
			t.Errorf("follow up = %q, ingin DIAJUKAN", got.FollowUpStatus)
		}
		if got.ReferralDate.Format(dateLayout) != "2026-11-26" {
			t.Errorf("tanggal = %v", got.ReferralDate)
		}
	})

	t.Run("target pihak nol ditolak", func(t *testing.T) {
		req := validReferral()
		zero := int64(0)
		req.TargetPartyID = &zero
		_, appErr := req.Validate(now)
		if appErr == nil || appErr.Fields["target_party_id"] == "" {
			t.Fatalf("ingin error target_party_id, dapat %+v", appErr)
		}
	})

	t.Run("institusi wajib dan dibatasi 150 karakter", func(t *testing.T) {
		req := validReferral()
		req.Institution = "   "
		if _, appErr := req.Validate(now); appErr == nil || appErr.Fields["institution"] == "" {
			t.Fatalf("ingin error institusi kosong, dapat %+v", appErr)
		}

		req.Institution = strings.Repeat("a", 151)
		if _, appErr := req.Validate(now); appErr == nil || appErr.Fields["institution"] == "" {
			t.Fatalf("ingin error institusi terlalu panjang, dapat %+v", appErr)
		}
	})

	t.Run("tanggal tidak boleh masa depan dan harus valid", func(t *testing.T) {
		req := validReferral()
		req.ReferralDate = "2026-11-27"
		if _, appErr := req.Validate(now); appErr == nil || appErr.Fields["referral_date"] == "" {
			t.Fatalf("ingin error tanggal masa depan, dapat %+v", appErr)
		}

		req.ReferralDate = "26-11-2026"
		if _, appErr := req.Validate(now); appErr == nil || appErr.Fields["referral_date"] == "" {
			t.Fatalf("ingin error format tanggal, dapat %+v", appErr)
		}
	})

	t.Run("tanggal hari ini diterima", func(t *testing.T) {
		req := validReferral()
		req.ReferralDate = "2026-11-26"
		if _, appErr := req.Validate(now); appErr != nil {
			t.Fatalf("tanggal hari ini seharusnya diterima: %+v", appErr)
		}
	})

	t.Run("enum tidak dikenal ditolak", func(t *testing.T) {
		req := validReferral()
		req.ServiceType = "PENDIDIKAN"
		if _, appErr := req.Validate(now); appErr == nil || appErr.Fields["service_type"] == "" {
			t.Fatalf("ingin error service_type, dapat %+v", appErr)
		}

		req = validReferral()
		req.FollowUpStatus = "BARU"
		if _, appErr := req.Validate(now); appErr == nil || appErr.Fields["follow_up_status"] == "" {
			t.Fatalf("ingin error follow_up_status, dapat %+v", appErr)
		}
	})

	t.Run("catatan dibatasi", func(t *testing.T) {
		req := validReferral()
		req.Notes = strings.Repeat("x", maxNotesRunes+1)
		if _, appErr := req.Validate(now); appErr == nil || appErr.Fields["notes"] == "" {
			t.Fatalf("ingin error notes terlalu panjang, dapat %+v", appErr)
		}
	})
}

func TestReferralUpdateValidate(t *testing.T) {
	if _, appErr := (ReferralUpdateRequest{FollowUpStatus: " DIPROSES "}).Validate(); appErr != nil {
		t.Fatalf("tidak ingin error: %+v", appErr)
	}
	for _, value := range []string{"", "SELESAIKAN", "diajukan"} {
		if _, appErr := (ReferralUpdateRequest{FollowUpStatus: value}).Validate(); appErr == nil {
			t.Errorf("nilai %q seharusnya ditolak", value)
		}
	}
}

func TestRecoveryValidate(t *testing.T) {
	now := fixedNow()
	valid := RecoveryRequest{Type: "EVALUASI", Note: "Evaluasi bulanan", RecordedAt: "2026-11-26"}

	got, appErr := valid.Validate(now)
	if appErr != nil {
		t.Fatalf("tidak ingin error: %+v", appErr)
	}
	if got.Type != "EVALUASI" || got.Note != "Evaluasi bulanan" {
		t.Errorf("hasil = %+v", got)
	}

	tests := []struct {
		name  string
		req   RecoveryRequest
		field string
	}{
		{name: "jenis kosong", req: RecoveryRequest{Note: "x", RecordedAt: "2026-11-26"}, field: "type"},
		{name: "jenis asing", req: RecoveryRequest{Type: "TERAPI", Note: "x", RecordedAt: "2026-11-26"}, field: "type"},
		{name: "catatan kosong", req: RecoveryRequest{Type: "EVALUASI", RecordedAt: "2026-11-26"}, field: "note"},
		{name: "tanggal masa depan", req: RecoveryRequest{Type: "EVALUASI", Note: "x", RecordedAt: "2026-11-27"}, field: "recorded_at"},
		{name: "tanggal tidak valid", req: RecoveryRequest{Type: "EVALUASI", Note: "x", RecordedAt: "besok"}, field: "recorded_at"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, appErr := tt.req.Validate(now)
			if appErr == nil || appErr.Fields[tt.field] == "" {
				t.Fatalf("ingin error %s, dapat %+v", tt.field, appErr)
			}
		})
	}
}

func TestConditionWritable(t *testing.T) {
	tests := map[string]bool{
		string(workflow.Dilaporkan):      false,
		string(workflow.PerluDilengkapi): false,
		string(workflow.Penelaahan):      false,
		string(workflow.Pemeriksaan):     true,
		string(workflow.Rekomendasi):     true,
		string(workflow.Keputusan):       true,
		string(workflow.Pemantauan):      true,
		string(workflow.Selesai):         false,
		"TIDAK_DIKENAL":                  false,
	}
	for status, want := range tests {
		if got := ConditionWritable(status); got != want {
			t.Errorf("ConditionWritable(%q) = %v, ingin %v", status, got, want)
		}
	}
}
