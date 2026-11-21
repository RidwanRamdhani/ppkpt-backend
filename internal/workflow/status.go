// Package workflow memusatkan aturan perpindahan status kasus.
//
// Hanya Service.Transition yang boleh mengubah kolom reports.status. Semua
// modul (pelaporan, verifikasi, penelaahan, pemeriksaan, keputusan) memakai
// fungsi ini agar urutan status, baris status_history, dan audit log selalu
// konsisten serta atomik di dalam transaksi pemanggil.
package workflow

import "github.com/RidwanRamdhani/ppkpt-backend/internal/platform/httpx"

// Status adalah tahap kasus sesuai CHECK constraint reports.status.
type Status string

const (
	Dilaporkan      Status = "DILAPORKAN"
	PerluDilengkapi Status = "PERLU_DILENGKAPI"
	Penelaahan      Status = "PENELAAHAN"
	Pemeriksaan     Status = "PEMERIKSAAN"
	Rekomendasi     Status = "REKOMENDASI"
	Keputusan       Status = "KEPUTUSAN"
	Pemantauan      Status = "PEMANTAUAN"
	Selesai         Status = "SELESAI"
)

// allowedTransitions adalah satu-satunya sumber kebenaran state machine
// (tabel transisi SDD M04–M07). Status yang tidak terdaftar di sini tidak
// boleh berubah ke mana pun.
var allowedTransitions = map[Status][]Status{
	Dilaporkan:      {PerluDilengkapi, Penelaahan},
	PerluDilengkapi: {Dilaporkan},
	Penelaahan:      {Pemeriksaan, Selesai},
	Pemeriksaan:     {Rekomendasi},
	Rekomendasi:     {Keputusan},
	Keputusan:       {Pemantauan, Selesai},
	Pemantauan:      {Selesai},
	Selesai:         {},
}

// Valid melaporkan apakah s termasuk status yang dikenal.
func (s Status) Valid() bool {
	switch s {
	case Dilaporkan, PerluDilengkapi, Penelaahan, Pemeriksaan,
		Rekomendasi, Keputusan, Pemantauan, Selesai:
		return true
	default:
		return false
	}
}

// CanTransition melaporkan apakah perpindahan from -> to diizinkan.
// Status yang tidak dikenal selalu menghasilkan false.
func CanTransition(from, to Status) bool {
	for _, candidate := range allowedTransitions[from] {
		if candidate == to {
			return true
		}
	}
	return false
}

// AllowedTransitions mengembalikan salinan daftar status tujuan yang sah dari
// from. Salinan dipakai agar pemanggil tidak bisa mengubah state machine.
func AllowedTransitions(from Status) []Status {
	return append([]Status(nil), allowedTransitions[from]...)
}

// EnsureWritable menolak penulisan pada kasus yang sudah SELESAI. Kasus
// tertutup bersifat hanya-baca (SDD M07); seluruh service tulis wajib memanggil
// fungsi ini sebelum mengubah data.
func EnsureWritable(status Status) error {
	if status == Selesai {
		return httpx.ErrConflict("Kasus sudah selesai dan tidak dapat diubah.")
	}
	return nil
}
