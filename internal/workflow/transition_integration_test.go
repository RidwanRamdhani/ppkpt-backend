package workflow_test

// Tes integrasi ini membutuhkan database yang sudah dimigrasi. Set
// TEST_DATABASE_URL untuk menjalankannya, misalnya:
//
//	TEST_DATABASE_URL=postgres://ppkpt_app:secret@localhost:5432/ppkpt?sslmode=disable go test ./internal/workflow
//
// Tujuannya memastikan SQL transisi benar-benar diterima PostgreSQL: kolom
// tenggat terisi dengan interval yang tepat, status_history bertambah, audit
// tercatat, dan perubahan bersamaan ditolak 409.

import (
	"context"
	"errors"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/RidwanRamdhani/ppkpt-backend/internal/audit"
	"github.com/RidwanRamdhani/ppkpt-backend/internal/platform/db"
	"github.com/RidwanRamdhani/ppkpt-backend/internal/platform/httpx"
	"github.com/RidwanRamdhani/ppkpt-backend/internal/workflow"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

func TestIntegrasiTransition(t *testing.T) {
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("TEST_DATABASE_URL tidak diisi; tes integrasi dilewati")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatalf("koneksi: %v", err)
	}
	defer pool.Close()
	if err := pool.Ping(ctx); err != nil {
		t.Fatalf("ping: %v", err)
	}

	// Laporan uji minimal; kanal non-PORTAL agar consent tidak wajib.
	caseNumber := fmt.Sprintf("WF%06d", time.Now().UnixNano()%1_000_000)
	var reportID int64
	err = pool.QueryRow(ctx, `
		INSERT INTO reports (case_number, pin_hash, channel, status, reporter_is_victim, chronology)
		VALUES ($1, 'x', 'HOTLINE', 'DILAPORKAN', TRUE, 'uji workflow')
		RETURNING id`, caseNumber).Scan(&reportID)
	if err != nil {
		t.Fatalf("insert laporan uji: %v", err)
	}
	defer func() {
		// status_history dan baris anak ikut terhapus (ON DELETE CASCADE).
		if _, err := pool.Exec(context.Background(), `DELETE FROM reports WHERE id = $1`, reportID); err != nil {
			t.Logf("bersihkan laporan uji gagal (abaikan): %v", err)
		}
	}()

	svc := workflow.NewService(audit.NewService(), workflow.Options{ReviewDueDays: 7, ExamDueDays: 30})
	err = db.WithTx(ctx, pool, func(tx pgx.Tx) error {
		return svc.Transition(ctx, tx, reportID, workflow.Dilaporkan, workflow.Penelaahan, nil)
	})
	if err != nil {
		t.Fatalf("Transition: %v", err)
	}

	var status string
	var reviewFilled bool
	var dueMatches bool
	err = pool.QueryRow(ctx, `
		SELECT status,
		       review_started_at IS NOT NULL,
		       review_due_at = review_started_at + interval '7 days'
		FROM reports WHERE id = $1`, reportID).Scan(&status, &reviewFilled, &dueMatches)
	if err != nil {
		t.Fatalf("baca laporan: %v", err)
	}
	if status != string(workflow.Penelaahan) {
		t.Errorf("status = %q, ingin PENELAAHAN", status)
	}
	if !reviewFilled {
		t.Error("review_started_at tidak terisi")
	}
	if !dueMatches {
		t.Error("review_due_at bukan review_started_at + 7 hari")
	}

	var historyCount int
	err = pool.QueryRow(ctx, `
		SELECT count(*) FROM status_history
		WHERE report_id = $1 AND from_status = 'DILAPORKAN' AND to_status = 'PENELAAHAN'`, reportID).Scan(&historyCount)
	if err != nil {
		t.Fatalf("baca status_history: %v", err)
	}
	if historyCount != 1 {
		t.Errorf("baris status_history = %d, ingin 1", historyCount)
	}

	var auditCount int
	err = pool.QueryRow(ctx, `
		SELECT count(*) FROM audit_logs
		WHERE action = 'STATUS_CHANGED' AND entity = 'report' AND entity_id = $1`, reportID).Scan(&auditCount)
	if err != nil {
		t.Fatalf("baca audit_logs: %v", err)
	}
	if auditCount != 1 {
		t.Errorf("baris audit STATUS_CHANGED = %d, ingin 1", auditCount)
	}

	// Transisi yang sama diulang: status sudah berubah sehingga 0 baris -> 409.
	err = db.WithTx(ctx, pool, func(tx pgx.Tx) error {
		return svc.Transition(ctx, tx, reportID, workflow.Dilaporkan, workflow.Penelaahan, nil)
	})
	var appErr *httpx.AppError
	if !errors.As(err, &appErr) || appErr.Status != 409 {
		t.Fatalf("transisi ulang = %v, ingin AppError 409", err)
	}

	transition := func(from, to workflow.Status) {
		t.Helper()
		err := db.WithTx(ctx, pool, func(tx pgx.Tx) error {
			return svc.Transition(ctx, tx, reportID, from, to, nil)
		})
		if err != nil {
			t.Fatalf("Transition %s -> %s: %v", from, to, err)
		}
	}

	// Tahap pemeriksaan: only exam_* yang diisi, tenggat penelaahan tetap.
	transition(workflow.Penelaahan, workflow.Pemeriksaan)
	var examDue, reviewIntact bool
	err = pool.QueryRow(ctx, `
		SELECT exam_due_at = exam_started_at + interval '30 days',
		       review_due_at = review_started_at + interval '7 days'
		FROM reports WHERE id = $1`, reportID).Scan(&examDue, &reviewIntact)
	if err != nil {
		t.Fatalf("baca tenggat pemeriksaan: %v", err)
	}
	if !examDue {
		t.Error("exam_due_at bukan exam_started_at + 30 hari")
	}
	if !reviewIntact {
		t.Error("tenggat penelaahan berubah saat masuk pemeriksaan")
	}

	transition(workflow.Pemeriksaan, workflow.Rekomendasi)
	transition(workflow.Rekomendasi, workflow.Keputusan)
	transition(workflow.Keputusan, workflow.Selesai)

	var finalStatus string
	var closed bool
	err = pool.QueryRow(ctx, `
		SELECT status, closed_at IS NOT NULL FROM reports WHERE id = $1`, reportID).
		Scan(&finalStatus, &closed)
	if err != nil {
		t.Fatalf("baca status akhir: %v", err)
	}
	if finalStatus != string(workflow.Selesai) {
		t.Errorf("status akhir = %q, ingin SELESAI", finalStatus)
	}
	if !closed {
		t.Error("closed_at tidak diisi saat SELESAI")
	}

	err = pool.QueryRow(ctx, `SELECT count(*) FROM status_history WHERE report_id = $1`, reportID).
		Scan(&historyCount)
	if err != nil {
		t.Fatalf("hitung status_history: %v", err)
	}
	if historyCount != 5 {
		t.Errorf("total status_history = %d, ingin 5", historyCount)
	}

	err = pool.QueryRow(ctx, `
		SELECT count(*) FROM audit_logs
		WHERE action = 'STATUS_CHANGED' AND entity = 'report' AND entity_id = $1`, reportID).
		Scan(&auditCount)
	if err != nil {
		t.Fatalf("hitung audit_logs: %v", err)
	}
	if auditCount != 5 {
		t.Errorf("total audit STATUS_CHANGED = %d, ingin 5", auditCount)
	}
}
