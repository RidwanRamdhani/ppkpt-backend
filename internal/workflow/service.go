package workflow

import (
	"context"
	"fmt"
	"strings"

	"github.com/RidwanRamdhani/ppkpt-backend/internal/audit"
	"github.com/RidwanRamdhani/ppkpt-backend/internal/platform/httpx"
	"github.com/jackc/pgx/v5/pgconn"
)

// DBTX adalah subset koneksi database yang dibutuhkan workflow. Nilai yang
// memenuhinya adalah pgx.Tx (dan *pgxpool.Pool), tetapi Transition wajib
// dipanggil dengan transaksi agar perubahan status, status_history, dan audit
// log commit bersama.
type DBTX interface {
	Exec(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error)
}

// Auditor adalah bagian layanan audit yang dipakai workflow. Interface sempit
// ini membuat state machine dapat diuji tanpa database.
type Auditor interface {
	Log(ctx context.Context, q audit.DBTX, e audit.Event) error
}

// Options mengatur tenggat yang dipasang otomatis saat status berpindah.
// Nilai <= 0 diganti dengan default (7 hari penelaahan, 30 hari pemeriksaan).
type Options struct {
	ReviewDueDays int
	ExamDueDays   int
}

const (
	defaultReviewDueDays = 7
	defaultExamDueDays   = 30
)

// Service menjalankan transisi status beserta efek sampingnya.
type Service struct {
	auditor       Auditor
	reviewDueDays int
	examDueDays   int
}

// NewService membuat state machine. auditor boleh nil (memakai audit.Service
// biasa); di produksi selalu berikan layanan audit agar perubahan status
// tercatat.
func NewService(auditor Auditor, opts Options) *Service {
	if auditor == nil {
		auditor = audit.NewService()
	}
	if opts.ReviewDueDays <= 0 {
		opts.ReviewDueDays = defaultReviewDueDays
	}
	if opts.ExamDueDays <= 0 {
		opts.ExamDueDays = defaultExamDueDays
	}
	return &Service{
		auditor:       auditor,
		reviewDueDays: opts.ReviewDueDays,
		examDueDays:   opts.ExamDueDays,
	}
}

// updateStatus membangun pernyataan UPDATE yang memindahkan status sekaligus
// memasang tenggat sesuai tahap baru. Tenggat memakai jam database dan hari
// kalender (keputusan #6), sehingga tidak bergantung pada jam server aplikasi.
//
// Kolom tenggat hanya diisi saat memasuki tahapnya; nilai lama tidak pernah
// ditimpa pada transisi lain. Klausa dibangun dari daftar tetap (bukan input
// pengguna), jadi tidak ada risiko injeksi SQL.
func (s *Service) updateStatus(reportID int64, from, to Status) (string, []any) {
	args := []any{reportID, string(from), string(to)}
	set := []string{"status = $3", "updated_at = now()"}

	switch to {
	case Penelaahan:
		set = append(set,
			"review_started_at = now()",
			"review_due_at = now() + make_interval(days => $4)")
		args = append(args, s.reviewDueDays)
	case Pemeriksaan:
		set = append(set,
			"exam_started_at = now()",
			"exam_due_at = now() + make_interval(days => $4)")
		args = append(args, s.examDueDays)
	case Selesai:
		set = append(set, "closed_at = now()")
	}

	query := "UPDATE reports SET " + strings.Join(set, ", ") + " WHERE id = $1 AND status = $2"
	return query, args
}

const insertHistorySQL = `
	INSERT INTO status_history (report_id, from_status, to_status, changed_by)
	VALUES ($1, $2, $3, $4)`

// Transition memindahkan satu kasus dari status from ke to.
//
// Langkahnya dijalankan berurutan di dalam transaksi pemanggil:
//  1. tolak perpindahan yang tidak ada di state machine (409),
//  2. UPDATE ... WHERE id = $1 AND status = $2; nol baris berarti status sudah
//     berubah oleh permintaan lain (409),
//  3. catat status_history,
//  4. catat audit STATUS_CHANGED.
//
// by adalah ID petugas yang memicu; nil berarti pelapor (mis. melengkapi
// laporan). Kegagalan audit membatalkan seluruh transaksi, sehingga tidak ada
// perubahan status tanpa jejak audit.
//
// Notifikasi email bukan tanggung jawab workflow: kirim setelah transaksi
// commit (mis. notification.Service.SendStatusUpdate) agar email tidak terkirim
// untuk perubahan yang akhirnya di-rollback.
func (s *Service) Transition(ctx context.Context, q DBTX, reportID int64, from, to Status, by *int64) error {
	if reportID <= 0 {
		return fmt.Errorf("workflow: report id %d tidak valid", reportID)
	}
	if !from.Valid() || !to.Valid() {
		return fmt.Errorf("workflow: status tidak dikenal (%q -> %q)", from, to)
	}
	if !CanTransition(from, to) {
		return httpx.ErrConflict("Perubahan status kasus tidak diizinkan.")
	}

	updateSQL, args := s.updateStatus(reportID, from, to)
	tag, err := q.Exec(ctx, updateSQL, args...)
	if err != nil {
		return fmt.Errorf("workflow: ubah status: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return httpx.ErrConflict("Status kasus sudah berubah. Muat ulang data lalu coba lagi.")
	}

	if _, err := q.Exec(ctx, insertHistorySQL, reportID, string(from), string(to), by); err != nil {
		return fmt.Errorf("workflow: catat riwayat status: %w", err)
	}

	actorType := audit.ActorReporter
	if by != nil {
		actorType = audit.ActorUser
	}
	event := audit.Event{
		ActorType: actorType,
		UserID:    by,
		Action:    audit.ActionStatusChanged,
		Entity:    "report",
		EntityID:  &reportID,
		Detail: map[string]any{
			"from": string(from),
			"to":   string(to),
		},
	}
	if err := s.auditor.Log(ctx, q, event); err != nil {
		return fmt.Errorf("workflow: audit status: %w", err)
	}
	return nil
}

// Pastikan *audit.Service memenuhi Auditor.
var _ Auditor = (*audit.Service)(nil)
