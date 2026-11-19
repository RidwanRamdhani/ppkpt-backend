package audit_test

// Test integrasi ini membutuhkan database yang sudah dimigrasi. Set
// TEST_DATABASE_URL untuk menjalankannya, misalnya:
//
//	TEST_DATABASE_URL=postgres://ppkpt_app:secret@localhost:5432/ppkpt?sslmode=disable go test ./internal/audit
//
// Tujuannya memastikan audit_logs benar-benar append-only, baik karena hak
// akses role aplikasi maupun trigger audit_logs_immutable (NFR-13).

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/RidwanRamdhani/ppkpt-backend/internal/audit"
	"github.com/jackc/pgx/v5/pgxpool"
)

func TestIntegrasiAuditLogsTidakBisaDiubah(t *testing.T) {
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("TEST_DATABASE_URL tidak diisi; tes integrasi dilewati")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatalf("koneksi: %v", err)
	}
	defer pool.Close()

	action := "TEST_APPEND_ONLY"
	if _, err := pool.Exec(ctx, `DELETE FROM audit_logs WHERE action = $1`, action); err != nil {
		// Bisa gagal karena trigger/privilege; itu justru hasil yang diharapkan.
		t.Logf("hapus data uji lama tidak perlu/gagal: %v", err)
	}

	svc := audit.NewService()
	if err := svc.Log(ctx, pool, audit.Event{
		ActorType: audit.ActorSystem,
		Action:    action,
	}); err != nil {
		t.Fatalf("tulis audit: %v", err)
	}

	if _, err := pool.Exec(ctx, `UPDATE audit_logs SET action = 'DIUBAH' WHERE action = $1`, action); err == nil {
		t.Fatal("UPDATE audit_logs berhasil, padahal harus ditolak")
	}

	if _, err := pool.Exec(ctx, `DELETE FROM audit_logs WHERE action = $1`, action); err == nil {
		t.Fatal("DELETE audit_logs berhasil, padahal harus ditolak")
	}
}
