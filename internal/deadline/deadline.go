// Package deadline menghitung status tenggat kasus (FR-32).
//
// Perhitungan dibuat sebagai fungsi murni agar mudah diuji dan dipakai ulang:
// daftar kasus (Phase 7) memakainya untuk menampilkan sisa waktu, sedangkan
// scheduler harian (Phase 8) memakainya untuk menandai kasus mendekati atau
// melewati tenggat. Tenggat selalu dihitung ulang saat data dibaca, sehingga
// kegagalan scheduler tidak membuat angka tenggat menjadi salah.
package deadline

import "time"

// State adalah status sebuah tenggat.
type State string

const (
	// Normal berarti tenggat masih jauh.
	Normal State = "NORMAL"
	// Approaching berarti sisa hari sudah mencapai ambang peringatan.
	Approaching State = "MENDEKATI"
	// Overdue berarti tenggat sudah lewat.
	Overdue State = "MELEWATI"
)

// Jakarta adalah zona waktu aplikasi (WIB). Memakai offset tetap agar tidak
// bergantung pada database zona waktu di mesin pengembang/CI.
var Jakarta = time.FixedZone("WIB", 7*60*60)

// Evaluate menentukan status tenggat dan sisa hari kalender.
//
// daysLeft bernilai negatif bila tenggat sudah lewat. warnDays adalah ambang
// "mendekati" dalam hari; nilai negatif diperlakukan sebagai 0. Tenggat yang
// jatuh hari ini dihitung 0 hari (mendekati), bukan lewat.
func Evaluate(due, now time.Time, warnDays int) (State, int) {
	if warnDays < 0 {
		warnDays = 0
	}

	daysLeft := DaysBetween(now, due)
	switch {
	case daysLeft < 0:
		return Overdue, daysLeft
	case daysLeft <= warnDays:
		return Approaching, daysLeft
	default:
		return Normal, daysLeft
	}
}

// DaysBetween menghitung selisih hari kalender WIB tanpa terpengaruh jam.
// Hasil negatif berarti due sudah lewat.
func DaysBetween(now, due time.Time) int {
	nowDay := startOfDay(now.In(Jakarta))
	dueDay := startOfDay(due.In(Jakarta))
	return int(dueDay.Sub(nowDay).Hours() / 24)
}

// startOfDay mengembalikan tengah malam pada tanggal yang sama di zona t.
func startOfDay(t time.Time) time.Time {
	return time.Date(t.Year(), t.Month(), t.Day(), 0, 0, 0, 0, t.Location())
}
