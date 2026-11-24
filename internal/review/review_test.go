package review

import (
	"strings"
	"testing"
	"time"

	"github.com/RidwanRamdhani/ppkpt-backend/internal/deadline"
	"github.com/RidwanRamdhani/ppkpt-backend/internal/workflow"
)

func wib(year int, month time.Month, day, hour, minute int) time.Time {
	return time.Date(year, month, day, hour, minute, 0, 0, deadline.Jakarta)
}

func TestReviewRequestValidate(t *testing.T) {
	tests := []struct {
		name    string
		req     ReviewRequest
		wantErr string
	}{
		{
			name: "korban dikenali",
			req:  ReviewRequest{VictimIdentified: " Korban dikenali "},
		},
		{
			name: "saksi dan bukti awal",
			req: ReviewRequest{
				WitnessIdentified:   "Saksi A",
				InitialEvidenceNote: "Tangkapan layar percakapan",
			},
		},
		{
			name:    "semua kosong",
			req:     ReviewRequest{},
			wantErr: "_",
		},
		{
			name:    "hanya spasi",
			req:     ReviewRequest{VictimIdentified: "   "},
			wantErr: "_",
		},
		{
			name:    "terlalu panjang",
			req:     ReviewRequest{VictimIdentified: strings.Repeat("a", maxReviewTextRunes+1)},
			wantErr: "victim_identified",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, appErr := tt.req.Validate()
			if tt.wantErr == "" {
				if appErr != nil {
					t.Fatalf("Validate = %v (fields: %v), ingin valid", appErr, appErr.Fields)
				}
				if got.VictimIdentified != strings.TrimSpace(tt.req.VictimIdentified) {
					t.Errorf("victim_identified = %q", got.VictimIdentified)
				}
				return
			}
			if appErr == nil || appErr.Fields[tt.wantErr] == "" {
				t.Fatalf("ingin error %s, dapat %+v", tt.wantErr, appErr)
			}
		})
	}
}

func TestCloseRequestValidate(t *testing.T) {
	tests := []struct {
		name    string
		req     CloseRequest
		wantErr string
	}{
		{name: "alasan valid", req: CloseRequest{Reason: " Bukan ranah PPKPT "}},
		{name: "alasan kosong", req: CloseRequest{}, wantErr: "reason"},
		{name: "alasan spasi", req: CloseRequest{Reason: "  "}, wantErr: "reason"},
		{
			name:    "alasan terlalu panjang",
			req:     CloseRequest{Reason: strings.Repeat("a", maxCloseReasonRunes+1)},
			wantErr: "reason",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, appErr := tt.req.Validate()
			if tt.wantErr == "" {
				if appErr != nil {
					t.Fatalf("Validate = %v, ingin valid", appErr)
				}
				if got.Reason != strings.TrimSpace(tt.req.Reason) {
					t.Errorf("reason = %q", got.Reason)
				}
				return
			}
			if appErr == nil || appErr.Fields[tt.wantErr] == "" {
				t.Fatalf("ingin error %s, dapat %+v", tt.wantErr, appErr)
			}
		})
	}
}

func TestSessionRequestValidate(t *testing.T) {
	now := wib(2026, time.November, 20, 10, 0)
	validParticipants := []SessionParticipantInput{{PartyID: 1, Role: " KORBAN "}}

	tests := []struct {
		name    string
		req     SessionRequest
		wantErr string
	}{
		{
			name: "valid",
			req: SessionRequest{
				SessionDate:  "2026-11-20",
				Summary:      " Wawancara awal ",
				Participants: validParticipants,
			},
		},
		{
			name:    "tanggal kosong",
			req:     SessionRequest{Participants: validParticipants},
			wantErr: "session_date",
		},
		{
			name: "format tanggal salah",
			req: SessionRequest{
				SessionDate: "20-11-2026", Participants: validParticipants,
			},
			wantErr: "session_date",
		},
		{
			name: "tanggal masa depan",
			req: SessionRequest{
				SessionDate: "2026-11-21", Participants: validParticipants,
			},
			wantErr: "session_date",
		},
		{
			name:    "tanpa peserta",
			req:     SessionRequest{SessionDate: "2026-11-20"},
			wantErr: "participants",
		},
		{
			name: "peserta duplikat",
			req: SessionRequest{
				SessionDate: "2026-11-20",
				Participants: []SessionParticipantInput{
					{PartyID: 1, Role: "KORBAN"},
					{PartyID: 1, Role: "SAKSI"},
				},
			},
			wantErr: "participants",
		},
		{
			name: "id pihak tidak valid",
			req: SessionRequest{
				SessionDate:  "2026-11-20",
				Participants: []SessionParticipantInput{{PartyID: 0, Role: "KORBAN"}},
			},
			wantErr: "participants.0.party_id",
		},
		{
			name: "peran kosong",
			req: SessionRequest{
				SessionDate:  "2026-11-20",
				Participants: []SessionParticipantInput{{PartyID: 1, Role: "  "}},
			},
			wantErr: "participants.0.role",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, appErr := tt.req.Validate(now)
			if tt.wantErr == "" {
				if appErr != nil {
					t.Fatalf("Validate = %v (fields: %v), ingin valid", appErr, appErr.Fields)
				}
				if got.SessionDate.Format(dateLayout) != tt.req.SessionDate {
					t.Errorf("session_date = %s", got.SessionDate.Format(dateLayout))
				}
				if got.Participants[0].Role != "KORBAN" {
					t.Errorf("role = %q", got.Participants[0].Role)
				}
				return
			}
			if appErr == nil || appErr.Fields[tt.wantErr] == "" {
				t.Fatalf("ingin error %s, dapat %+v", tt.wantErr, appErr)
			}
		})
	}
}

// TestSessionRoleLength memastikan batas peran peserta sesi selaras dengan
// kolom session_participants.role VARCHAR(20). Tanpa batas ini, peran 21
// karakter lolos validasi lalu gagal di database sebagai 500, bukan 422.
func TestSessionRoleLength(t *testing.T) {
	now := wib(2026, time.November, 20, 10, 0)

	tests := []struct {
		name    string
		role    string
		wantErr bool
	}{
		{name: "tepat 20 karakter", role: strings.Repeat("a", 20)},
		{name: "21 karakter", role: strings.Repeat("a", 21), wantErr: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := SessionRequest{
				SessionDate: "2026-11-20",
				Participants: []SessionParticipantInput{
					{PartyID: 1, Role: tt.role},
				},
			}
			_, appErr := req.Validate(now)
			if tt.wantErr {
				if appErr == nil || appErr.Fields["participants.0.role"] == "" {
					t.Fatalf("ingin error participants.0.role, dapat %+v", appErr)
				}
				return
			}
			if appErr != nil {
				t.Fatalf("Validate = %v (fields: %v), ingin valid", appErr, appErr.Fields)
			}
		})
	}
}

func TestEvaluateCase(t *testing.T) {
	now := wib(2026, time.November, 10, 10, 0)
	dueSoon := wib(2026, time.November, 11, 10, 0)
	dueFar := wib(2026, time.November, 20, 10, 0)
	duePast := wib(2026, time.November, 9, 10, 0)

	tests := []struct {
		name      string
		status    string
		reviewDue *time.Time
		examDue   *time.Time
		wantOK    bool
		wantState deadline.State
		wantDays  int
	}{
		{
			name: "penelaahan mendekati", status: string(workflow.Penelaahan),
			reviewDue: &dueSoon, wantOK: true, wantState: deadline.Approaching, wantDays: 1,
		},
		{
			name: "penelaahan masih jauh", status: string(workflow.Penelaahan),
			reviewDue: &dueFar, wantOK: false,
		},
		{
			name: "pemeriksaan melewati", status: string(workflow.Pemeriksaan),
			examDue: &duePast, wantOK: true, wantState: deadline.Overdue, wantDays: -1,
		},
		{
			name: "pemeriksaan memakai tenggat pemeriksaan", status: string(workflow.Pemeriksaan),
			reviewDue: &duePast, examDue: &dueFar, wantOK: false,
		},
		{
			name: "status tidak bertenggat", status: string(workflow.Dilaporkan),
			reviewDue: &dueSoon, wantOK: false,
		},
		{
			name: "tenggat kosong", status: string(workflow.Penelaahan), wantOK: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			warning, ok := EvaluateCase(tt.status, tt.reviewDue, tt.examDue, now, 2, 7)
			if ok != tt.wantOK {
				t.Fatalf("ok = %v, ingin %v", ok, tt.wantOK)
			}
			if !ok {
				return
			}
			if warning.State != tt.wantState || warning.DaysLeft != tt.wantDays {
				t.Fatalf("warning = (%s, %d), ingin (%s, %d)",
					warning.State, warning.DaysLeft, tt.wantState, tt.wantDays)
			}
		})
	}
}

func TestCaseEvidenceCategory(t *testing.T) {
	tests := []struct {
		input   string
		want    string
		wantErr bool
	}{
		{"bai", "BAI", false},
		{" LAMPIRAN ", "LAMPIRAN", false},
		{"BUKTI", "", true},
		{"", "", true},
		{"lainnya", "", true},
	}

	for _, tt := range tests {
		got, appErr := caseEvidenceCategory(tt.input)
		if tt.wantErr {
			if appErr == nil || appErr.Fields["category"] == "" {
				t.Errorf("%q: ingin error category, dapat %+v", tt.input, appErr)
			}
			continue
		}
		if appErr != nil {
			t.Errorf("%q: %v", tt.input, appErr)
			continue
		}
		if got != tt.want {
			t.Errorf("%q: got %q, ingin %q", tt.input, got, tt.want)
		}
	}
}

func TestCaseEvidenceWritable(t *testing.T) {
	tests := []struct {
		status string
		want   bool
	}{
		{string(workflow.Penelaahan), true},
		{string(workflow.Pemeriksaan), true},
		{string(workflow.Rekomendasi), true},
		{string(workflow.Keputusan), false},
		{string(workflow.Pemantauan), false},
		{string(workflow.Selesai), false},
		{string(workflow.Dilaporkan), false},
	}
	for _, tt := range tests {
		if got := caseEvidenceWritable(tt.status); got != tt.want {
			t.Errorf("%s: got %v, ingin %v", tt.status, got, tt.want)
		}
	}
}
