package cases

import (
	"testing"
	"time"

	"github.com/RidwanRamdhani/ppkpt-backend/internal/audit"
	"github.com/RidwanRamdhani/ppkpt-backend/internal/workflow"
)

func TestMergeHistoryMenggabungkanStatusDanAksi(t *testing.T) {
	base := time.Date(2026, time.November, 20, 9, 0, 0, 0, time.UTC)
	perlu := string(workflow.PerluDilengkapi)
	dilaporkan := string(workflow.Dilaporkan)
	statuses := []StatusHistoryView{
		{
			ID:         1,
			FromStatus: nil,
			ToStatus:   dilaporkan,
			ChangedAt:  base,
		},
		{
			ID:         2,
			FromStatus: &perlu,
			ToStatus:   dilaporkan,
			ChangedAt:  base.Add(2 * time.Hour),
		},
	}
	actions := []historyAction{
		{
			Action:    audit.ActionCaseTaken,
			ActorType: audit.ActorUser,
			ActorName: "Satgas A",
			At:        base.Add(time.Hour),
		},
	}

	got := mergeHistory(statuses, actions)
	if len(got) != 3 {
		t.Fatalf("jumlah riwayat = %d, ingin 3", len(got))
	}
	if got[0].Title != "Laporan diterima" || got[0].Kind != "STATUS" {
		t.Errorf("entri pertama = %+v", got[0])
	}
	if got[1].Title != "Kasus diambil" || got[1].Actor != "Satgas A" {
		t.Errorf("entri kedua = %+v", got[1])
	}
	if got[2].Title != "Status: Perlu dilengkapi → Dilaporkan" || got[2].Actor != "Pelapor" {
		t.Errorf("entri ketiga = %+v", got[2])
	}
}

func TestMergeHistoryUrutanDanAktor(t *testing.T) {
	base := time.Date(2026, time.November, 20, 9, 0, 0, 0, time.UTC)
	statuses := []StatusHistoryView{
		{
			ID:         1,
			FromStatus: nil,
			ToStatus:   string(workflow.Dilaporkan),
			ChangedAt:  base.Add(2 * time.Hour),
		},
	}
	actions := []historyAction{
		{Action: audit.ActionVerificationRecorded, ActorType: audit.ActorUser, At: base.Add(time.Hour)},
		{Action: audit.ActionReportSubmitted, ActorType: audit.ActorReporter, At: base},
	}

	got := mergeHistory(statuses, actions)
	if len(got) != 3 {
		t.Fatalf("jumlah riwayat = %d", len(got))
	}
	if !got[0].At.Equal(base) || got[0].Actor != "Pelapor" {
		t.Errorf("entri pertama = %+v", got[0])
	}
	if got[1].Title != "Verifikasi dicatat" || got[1].Actor != "Petugas" {
		t.Errorf("entri kedua = %+v", got[1])
	}
	if got[2].Actor != "Pelapor" {
		t.Errorf("aktor status = %q, ingin Pelapor", got[2].Actor)
	}
}

func TestMergeHistoryKosongBukanNil(t *testing.T) {
	got := mergeHistory(nil, nil)
	if got == nil {
		t.Fatal("mergeHistory menghasilkan nil; JSON harus []")
	}
	if len(got) != 0 {
		t.Fatalf("mergeHistory = %+v", got)
	}
}

func TestActionTitlesMencakupSemuaWhitelist(t *testing.T) {
	for _, action := range actionHistoryWhitelist {
		if _, ok := actionTitles[action]; !ok {
			t.Errorf("aksi %s ada di whitelist tetapi tidak punya label", action)
		}
	}
}
