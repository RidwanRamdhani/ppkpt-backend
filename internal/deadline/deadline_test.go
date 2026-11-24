package deadline

import (
	"testing"
	"time"
)

func wib(year int, month time.Month, day, hour, minute int) time.Time {
	return time.Date(year, month, day, hour, minute, 0, 0, Jakarta)
}

func TestEvaluate(t *testing.T) {
	now := wib(2026, time.November, 10, 10, 0)

	tests := []struct {
		name      string
		due       time.Time
		warnDays  int
		wantState State
		wantDays  int
	}{
		{"masih jauh", wib(2026, time.November, 17, 17, 0), 2, Normal, 7},
		{"tepat ambang", wib(2026, time.November, 12, 8, 0), 2, Approaching, 2},
		{"di bawah ambang", wib(2026, time.November, 11, 8, 0), 2, Approaching, 1},
		{"hari ini", wib(2026, time.November, 10, 23, 59), 2, Approaching, 0},
		{"kemarin", wib(2026, time.November, 9, 23, 59), 2, Overdue, -1},
		{"lewat sepekan", wib(2026, time.November, 3, 8, 0), 2, Overdue, -7},
		{"ambang nol dan hari ini", wib(2026, time.November, 10, 20, 0), 0, Approaching, 0},
		{"ambang negatif", wib(2026, time.November, 13, 8, 0), -5, Normal, 3},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			state, days := Evaluate(tt.due, now, tt.warnDays)
			if state != tt.wantState || days != tt.wantDays {
				t.Fatalf("Evaluate() = (%s, %d), ingin (%s, %d)",
					state, days, tt.wantState, tt.wantDays)
			}
		})
	}
}

// Tanggal harus dihitung sebagai hari kalender WIB, bukan UTC. Dua waktu yang
// berbeda tanggal di UTC tetapi sama tanggal di WIB tidak boleh dianggap
// melewati tenggat.
func TestEvaluateMemakaiHariKalenderWIB(t *testing.T) {
	// 2026-11-10 23:30 WIB = 2026-11-10 16:30 UTC.
	now := time.Date(2026, time.November, 10, 23, 30, 0, 0, Jakarta)
	// 2026-11-11 00:30 WIB = 2026-11-10 17:30 UTC (masih tanggal 10 di UTC).
	due := time.Date(2026, time.November, 11, 0, 30, 0, 0, Jakarta)

	state, days := Evaluate(due, now, 1)
	if state != Approaching || days != 1 {
		t.Fatalf("Evaluate() = (%s, %d), ingin (%s, 1)", state, days, Approaching)
	}
}
