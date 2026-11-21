package report

import (
	"time"

	"github.com/RidwanRamdhani/ppkpt-backend/internal/workflow"
)

// StatusView adalah SATU-SATUNYA data yang boleh dilihat pelapor (FR-14).
// Jangan menambahkan isi laporan, identitas pihak, bukti, atau catatan internal
// ke struct ini.
type StatusView struct {
	CaseNumber      string        `json:"case_number"`
	Stage           int           `json:"stage"`  // tahap 1..6 (Tabel 4 SDD)
	Status          string        `json:"status"` // status mesin, mis. PENELAAHAN
	StatusLabel     string        `json:"status_label"`
	Deadline        *DeadlineView `json:"deadline,omitempty"`
	NoteForReporter string        `json:"note_for_reporter,omitempty"`
	CanComplete     bool          `json:"can_complete"`
}

// DeadlineView adalah batas waktu tahap berjalan yang aman ditampilkan.
type DeadlineView struct {
	DueAt    time.Time `json:"due_at"`
	DaysLeft int       `json:"days_left"` // negatif berarti sudah lewat
}

type statusMeta struct {
	stage int
	label string
}

// statusMetadata memetakan status ke tahap dan label yang dilihat pelapor.
// Pemetaan mengikuti Tabel 4 SDD (lihat Phase 6.3 panduan).
var statusMetadata = map[workflow.Status]statusMeta{
	workflow.Dilaporkan:      {stage: 2, label: "Laporan diterima"},
	workflow.PerluDilengkapi: {stage: 2, label: "Perlu dilengkapi"},
	workflow.Penelaahan:      {stage: 3, label: "Penelaahan"},
	workflow.Pemeriksaan:     {stage: 4, label: "Pemeriksaan"},
	workflow.Rekomendasi:     {stage: 5, label: "Rekomendasi"},
	workflow.Keputusan:       {stage: 5, label: "Keputusan"},
	workflow.Pemantauan:      {stage: 5, label: "Pemantauan"},
	workflow.Selesai:         {stage: 6, label: "Selesai"},
}

// PresentStatus membangun respons cek status. Fungsi murni agar mudah diuji dan
// agar tidak ada jalan bagi data internal untuk ikut terkirim.
func PresentStatus(r Report, noteForReporter string, now time.Time) StatusView {
	meta, ok := statusMetadata[r.Status]
	if !ok {
		meta = statusMeta{stage: 0, label: "Tidak diketahui"}
	}

	view := StatusView{
		CaseNumber:  r.CaseNumber,
		Stage:       meta.stage,
		Status:      string(r.Status),
		StatusLabel: meta.label,
		CanComplete: r.Status == workflow.PerluDilengkapi,
	}

	if r.Status == workflow.PerluDilengkapi {
		view.NoteForReporter = noteForReporter
	}

	// Hanya tahap yang punya tenggat yang menampilkan batas waktu.
	var due *time.Time
	switch r.Status {
	case workflow.Penelaahan:
		due = r.ReviewDueAt
	case workflow.Pemeriksaan:
		due = r.ExamDueAt
	}
	if due != nil {
		view.Deadline = &DeadlineView{
			DueAt:    due.In(jakartaZone),
			DaysLeft: calendarDaysBetween(now, *due),
		}
	}

	return view
}

// calendarDaysBetween menghitung selisih hari kalender (WIB) tanpa terpengaruh
// jam. Hasil negatif berarti tenggat sudah lewat.
func calendarDaysBetween(now, due time.Time) int {
	nowDay := startOfDay(now.In(jakartaZone))
	dueDay := startOfDay(due.In(jakartaZone))
	return int(dueDay.Sub(nowDay).Hours() / 24)
}
