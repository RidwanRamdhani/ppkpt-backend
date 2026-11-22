package workflow_test

import (
	"errors"
	"testing"

	"github.com/RidwanRamdhani/ppkpt-backend/internal/platform/httpx"
	"github.com/RidwanRamdhani/ppkpt-backend/internal/workflow"
)

// TestCanTransition memeriksa seluruh matriks status x status (8x8) terhadap
// tabel transisi SDD. Matriks ditulis ulang di sini agar perubahan state
// machine tanpa memperbarui dokumentasi/tes akan gagal.
func TestCanTransition(t *testing.T) {
	all := []workflow.Status{
		workflow.Dilaporkan,
		workflow.PerluDilengkapi,
		workflow.Penelaahan,
		workflow.Pemeriksaan,
		workflow.Rekomendasi,
		workflow.Keputusan,
		workflow.Pemantauan,
		workflow.Selesai,
	}

	allowed := map[workflow.Status][]workflow.Status{
		workflow.Dilaporkan:      {workflow.PerluDilengkapi, workflow.Penelaahan},
		workflow.PerluDilengkapi: {workflow.Dilaporkan},
		workflow.Penelaahan:      {workflow.Pemeriksaan, workflow.Selesai},
		workflow.Pemeriksaan:     {workflow.Rekomendasi},
		workflow.Rekomendasi:     {workflow.Keputusan},
		workflow.Keputusan:       {workflow.Pemantauan, workflow.Selesai},
		workflow.Pemantauan:      {workflow.Selesai},
		workflow.Selesai:         {},
	}

	for _, from := range all {
		for _, to := range all {
			want := false
			for _, candidate := range allowed[from] {
				if candidate == to {
					want = true
					break
				}
			}
			if got := workflow.CanTransition(from, to); got != want {
				t.Errorf("CanTransition(%s, %s) = %v, ingin %v", from, to, got, want)
			}
		}
	}
}

// TestCanTransitionUnknownStatus memastikan status di luar enum tidak pernah
// dianggap sah, baik sebagai asal maupun tujuan.
func TestCanTransitionUnknownStatus(t *testing.T) {
	unknown := workflow.Status("STATUS_ASING")
	cases := []struct {
		from, to workflow.Status
	}{
		{"", workflow.Dilaporkan},
		{unknown, workflow.Dilaporkan},
		{workflow.Dilaporkan, unknown},
		{unknown, unknown},
	}
	for _, tc := range cases {
		if workflow.CanTransition(tc.from, tc.to) {
			t.Errorf("CanTransition(%q, %q) = true, ingin false", tc.from, tc.to)
		}
	}
}

func TestStatusValid(t *testing.T) {
	valid := []workflow.Status{
		workflow.Dilaporkan, workflow.PerluDilengkapi, workflow.Penelaahan,
		workflow.Pemeriksaan, workflow.Rekomendasi, workflow.Keputusan,
		workflow.Pemantauan, workflow.Selesai,
	}
	for _, s := range valid {
		if !s.Valid() {
			t.Errorf("%s.Valid() = false, ingin true", s)
		}
	}
	if workflow.Status("").Valid() || workflow.Status("selesai").Valid() {
		t.Error("status kosong/huruf kecil dianggap valid")
	}
}

// TestAllowedTransitionsIsCopy memastikan pemanggil tidak bisa mengubah state
// machine lewat slice yang dikembalikan.
func TestAllowedTransitionsIsCopy(t *testing.T) {
	got := workflow.AllowedTransitions(workflow.Dilaporkan)
	if len(got) != 2 {
		t.Fatalf("AllowedTransitions(DILAPORKAN) = %v, ingin 2 status", got)
	}
	got[0] = workflow.Selesai

	again := workflow.AllowedTransitions(workflow.Dilaporkan)
	if again[0] != workflow.PerluDilengkapi {
		t.Fatalf("state machine berubah setelah slice hasil dimodifikasi: %v", again)
	}
	if n := len(workflow.AllowedTransitions(workflow.Selesai)); n != 0 {
		t.Fatalf("AllowedTransitions(SELESAI) berisi %d status, ingin 0", n)
	}
}

func TestEnsureWritable(t *testing.T) {
	all := []workflow.Status{
		workflow.Dilaporkan, workflow.PerluDilengkapi, workflow.Penelaahan,
		workflow.Pemeriksaan, workflow.Rekomendasi, workflow.Keputusan,
		workflow.Pemantauan,
	}
	for _, s := range all {
		if err := workflow.EnsureWritable(s); err != nil {
			t.Errorf("EnsureWritable(%s) = %v, ingin nil", s, err)
		}
	}

	err := workflow.EnsureWritable(workflow.Selesai)
	var appErr *httpx.AppError
	if !errors.As(err, &appErr) {
		t.Fatalf("EnsureWritable(SELESAI) = %v, ingin AppError", err)
	}
	if appErr.Status != 409 {
		t.Fatalf("status = %d, ingin 409", appErr.Status)
	}
}
