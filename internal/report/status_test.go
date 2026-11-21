package report_test

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/RidwanRamdhani/ppkpt-backend/internal/report"
	"github.com/RidwanRamdhani/ppkpt-backend/internal/workflow"
)

func wib() *time.Location {
	return time.FixedZone("WIB", 7*60*60)
}

func statusReport(status workflow.Status, reviewDue, examDue *time.Time) report.Report {
	return report.Report{
		ID:          1,
		CaseNumber:  "20481735",
		Status:      status,
		ReviewDueAt: reviewDue,
		ExamDueAt:   examDue,
	}
}

// TestPresentStatusStages memeriksa pemetaan tahap 1..6 sesuai Tabel 4 SDD.
func TestPresentStatusStages(t *testing.T) {
	now := time.Date(2026, 11, 21, 10, 0, 0, 0, wib())
	cases := []struct {
		status workflow.Status
		stage  int
		label  string
	}{
		{workflow.Dilaporkan, 2, "Laporan diterima"},
		{workflow.PerluDilengkapi, 2, "Perlu dilengkapi"},
		{workflow.Penelaahan, 3, "Penelaahan"},
		{workflow.Pemeriksaan, 4, "Pemeriksaan"},
		{workflow.Rekomendasi, 5, "Rekomendasi"},
		{workflow.Keputusan, 5, "Keputusan"},
		{workflow.Pemantauan, 5, "Pemantauan"},
		{workflow.Selesai, 6, "Selesai"},
	}

	for _, tc := range cases {
		view := report.PresentStatus(statusReport(tc.status, nil, nil), "", now)
		if view.Stage != tc.stage || view.StatusLabel != tc.label {
			t.Errorf("%s -> tahap %d %q, ingin %d %q",
				tc.status, view.Stage, view.StatusLabel, tc.stage, tc.label)
		}
		if got := view.CanComplete; got != (tc.status == workflow.PerluDilengkapi) {
			t.Errorf("%s can_complete = %v", tc.status, got)
		}
	}

	unknown := report.PresentStatus(statusReport(workflow.Status("ASING"), nil, nil), "", now)
	if unknown.Stage != 0 {
		t.Errorf("status asing stage = %d, ingin 0", unknown.Stage)
	}
}

// TestPresentStatusNoteOnlyWhenIncomplete memastikan catatan verifikasi tidak
// bocor pada status lain.
func TestPresentStatusNoteOnlyWhenIncomplete(t *testing.T) {
	now := time.Date(2026, 11, 21, 10, 0, 0, 0, wib())

	incomplete := report.PresentStatus(statusReport(workflow.PerluDilengkapi, nil, nil), "Lengkapi bukti.", now)
	if incomplete.NoteForReporter != "Lengkapi bukti." {
		t.Errorf("catatan = %q", incomplete.NoteForReporter)
	}

	other := report.PresentStatus(statusReport(workflow.Pemeriksaan, nil, nil), "catatan internal", now)
	if other.NoteForReporter != "" {
		t.Errorf("catatan bocor pada status PEMERIKSAAN: %q", other.NoteForReporter)
	}
}

// TestPresentStatusDeadline memastikan hanya tahap bertenggat yang menampilkan
// batas waktu dan sisa hari dihitung per hari kalender WIB.
func TestPresentStatusDeadline(t *testing.T) {
	now := time.Date(2026, 11, 21, 10, 0, 0, 0, wib())
	reviewDue := time.Date(2026, 11, 26, 23, 0, 0, 0, wib())
	examDue := time.Date(2026, 12, 21, 23, 0, 0, 0, wib())

	review := report.PresentStatus(statusReport(workflow.Penelaahan, &reviewDue, nil), "", now)
	if review.Deadline == nil || review.Deadline.DaysLeft != 5 {
		t.Fatalf("deadline penelaahan = %+v, ingin sisa 5 hari", review.Deadline)
	}

	exam := report.PresentStatus(statusReport(workflow.Pemeriksaan, &reviewDue, &examDue), "", now)
	if exam.Deadline == nil || exam.Deadline.DaysLeft != 30 {
		t.Fatalf("deadline pemeriksaan = %+v, ingin sisa 30 hari", exam.Deadline)
	}

	// Tenggat lewat harus negatif, bukan nol.
	late := time.Date(2026, 11, 19, 9, 0, 0, 0, wib())
	expired := report.PresentStatus(statusReport(workflow.Penelaahan, &late, nil), "", now)
	if expired.Deadline == nil || expired.Deadline.DaysLeft != -2 {
		t.Fatalf("deadline lewat = %+v, ingin -2 hari", expired.Deadline)
	}

	// Status tanpa tenggat tidak menampilkan deadline meski kolomnya terisi.
	done := report.PresentStatus(statusReport(workflow.Selesai, &reviewDue, &examDue), "", now)
	if done.Deadline != nil {
		t.Errorf("status SELESAI menampilkan deadline: %+v", done.Deadline)
	}
}

// TestPresentStatusJSONWhitelist memastikan respons hanya memuat kunci yang
// diizinkan (FR-14): tidak ada PIN, isi laporan, atau identitas pihak.
func TestPresentStatusJSONWhitelist(t *testing.T) {
	now := time.Date(2026, 11, 21, 10, 0, 0, 0, wib())
	due := time.Date(2026, 11, 26, 23, 0, 0, 0, wib())
	view := report.PresentStatus(statusReport(workflow.Penelaahan, &due, nil), "", now)

	raw, err := json.Marshal(view)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}

	var decoded map[string]json.RawMessage
	if err := json.Unmarshal(raw, &decoded); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}

	allowed := map[string]bool{
		"case_number": true, "stage": true, "status": true, "status_label": true,
		"deadline": true, "note_for_reporter": true, "can_complete": true,
	}
	for key := range decoded {
		if !allowed[key] {
			t.Errorf("kunci tidak diizinkan muncul: %q (%s)", key, raw)
		}
	}

	forbidden := []string{"pin", "password", "chronology", "additional_description",
		"reporter", "parties", "accused", "witness", "summary", "reported_to"}
	for _, word := range forbidden {
		for key := range decoded {
			if key == word {
				t.Errorf("kunci sensitif %q bocor", word)
			}
		}
	}
}
