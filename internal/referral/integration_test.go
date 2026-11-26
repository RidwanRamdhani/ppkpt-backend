package referral_test

// Tes integrasi ini membutuhkan PostgreSQL dengan migrasi 0001 terpasang. Isi
// TEST_DATABASE_URL untuk menjalankannya, misalnya:
//
//	TEST_DATABASE_URL=postgres://ppkpt_app:secret@localhost:5432/ppkpt?sslmode=disable go test ./internal/referral
//
// Akun uji (username berawalan "test_referral_") tidak dihapus karena
// audit_logs append-only mereferensikan users. Laporan uji dihapus di akhir tes;
// rujukan dan pemulihan ikut terhapus lewat ON DELETE CASCADE.

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"os"
	"testing"
	"time"

	"github.com/RidwanRamdhani/ppkpt-backend/internal/audit"
	"github.com/RidwanRamdhani/ppkpt-backend/internal/auth"
	"github.com/RidwanRamdhani/ppkpt-backend/internal/platform/httpx"
	"github.com/RidwanRamdhani/ppkpt-backend/internal/referral"
	"github.com/jackc/pgx/v5/pgxpool"
)

type integrationEnv struct {
	t       *testing.T
	pool    *pgxpool.Pool
	svc     *referral.Service
	prefix  string
	satgasA int64
	satgasB int64
	pejabat int64
}

func newIntegrationEnv(t *testing.T) *integrationEnv {
	t.Helper()
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("TEST_DATABASE_URL tidak diisi; tes integrasi dilewati")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatalf("koneksi: %v", err)
	}
	t.Cleanup(pool.Close)
	if err := pool.Ping(ctx); err != nil {
		t.Fatalf("ping: %v", err)
	}

	prefix := fmt.Sprintf("%06d", time.Now().UnixNano()%1_000_000)
	cleanupReports(t, pool, prefix)
	t.Cleanup(func() {
		cleanupReports(t, pool, prefix)
	})

	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	env := &integrationEnv{
		t:      t,
		pool:   pool,
		prefix: prefix,
		svc:    referral.NewService(pool, audit.NewService(), logger),
	}
	env.satgasA = env.user("satgas_a", auth.RoleSatgas)
	env.satgasB = env.user("satgas_b", auth.RoleSatgas)
	env.pejabat = env.user("pejabat", auth.RolePejabat)
	return env
}

func cleanupReports(t *testing.T, pool *pgxpool.Pool, prefix string) {
	t.Helper()
	if _, err := pool.Exec(context.Background(),
		`DELETE FROM reports WHERE case_number LIKE $1`, prefix+"%",
	); err != nil {
		t.Logf("bersihkan laporan uji gagal (abaikan): %v", err)
	}
}

func (e *integrationEnv) user(label, role string) int64 {
	e.t.Helper()
	username := fmt.Sprintf("test_referral_%s_%s", e.prefix, label)
	var id int64
	err := e.pool.QueryRow(context.Background(), `
		INSERT INTO users (name, username, email, password_hash, role, is_active, must_change_password)
		VALUES ($1, $2, $3, 'x', $4, TRUE, FALSE)
		ON CONFLICT (username) DO UPDATE SET name = EXCLUDED.name
		RETURNING id`,
		"Petugas "+label, username, username+"@test.local", role,
	).Scan(&id)
	if err != nil {
		e.t.Fatalf("buat user %s: %v", label, err)
	}
	return id
}

func (e *integrationEnv) report(suffix, status string, assigned *int64) int64 {
	e.t.Helper()
	var id int64
	err := e.pool.QueryRow(context.Background(), `
		INSERT INTO reports
			(case_number, pin_hash, channel, status, reporter_is_victim, chronology, assigned_to)
		VALUES ($1, 'x', 'HOTLINE', $2, TRUE, 'Kronologi uji', $3)
		RETURNING id`,
		e.prefix+suffix, status, assigned,
	).Scan(&id)
	if err != nil {
		e.t.Fatalf("buat laporan %s: %v", suffix, err)
	}
	return id
}

func (e *integrationEnv) party(reportID int64, partyType string) int64 {
	e.t.Helper()
	var id int64
	if err := e.pool.QueryRow(context.Background(), `
		INSERT INTO parties (report_id, party_type, name)
		VALUES ($1, $2, $3)
		RETURNING id`,
		reportID, partyType, "Pihak-"+e.prefix,
	).Scan(&id); err != nil {
		e.t.Fatalf("buat pihak %s: %v", partyType, err)
	}
	return id
}

func (e *integrationEnv) setStatus(reportID int64, status string) {
	e.t.Helper()
	if _, err := e.pool.Exec(context.Background(),
		`UPDATE reports SET status = $2 WHERE id = $1`, reportID, status,
	); err != nil {
		e.t.Fatalf("ubah status: %v", err)
	}
}

func (e *integrationEnv) auditCount(reportID int64, action string) int {
	e.t.Helper()
	var count int
	if err := e.pool.QueryRow(context.Background(), `
		SELECT COUNT(*) FROM audit_logs
		WHERE entity = 'report' AND entity_id = $1 AND action::text = $2`,
		reportID, action,
	).Scan(&count); err != nil {
		e.t.Fatalf("hitung audit: %v", err)
	}
	return count
}

func appErrStatus(t *testing.T, err error, want int) {
	t.Helper()
	if err == nil {
		t.Fatalf("ingin error %d, dapat nil", want)
	}
	appErr := httpx.FromError(err)
	if appErr.Status != want {
		t.Fatalf("ingin status %d, dapat %d (%v)", want, appErr.Status, err)
	}
}

func TestSetCondition(t *testing.T) {
	e := newIntegrationEnv(t)
	ctx := context.Background()
	actor := referral.Actor{UserID: e.satgasA, Role: auth.RoleSatgas}

	reportID := e.report("31", "PEMERIKSAAN", &e.satgasA)

	view, err := e.svc.SetCondition(ctx, actor, reportID, referral.ConditionRequest{Condition: "DARURAT"}, "203.0.113.7")
	if err != nil {
		t.Fatalf("SetCondition: %v", err)
	}
	if view.CaseCondition != "DARURAT" {
		t.Errorf("respons = %q, ingin DARURAT", view.CaseCondition)
	}

	var stored string
	if err := e.pool.QueryRow(ctx,
		`SELECT case_condition FROM reports WHERE id = $1`, reportID,
	).Scan(&stored); err != nil {
		t.Fatalf("baca kondisi: %v", err)
	}
	if stored != "DARURAT" {
		t.Errorf("kolom case_condition = %q", stored)
	}
	if got := e.auditCount(reportID, audit.ActionCaseConditionChanged); got != 1 {
		t.Errorf("audit kondisi = %d, ingin 1", got)
	}

	t.Run("bukan PJ ditolak", func(t *testing.T) {
		_, err := e.svc.SetCondition(ctx, referral.Actor{UserID: e.satgasB, Role: auth.RoleSatgas}, reportID,
			referral.ConditionRequest{Condition: "TIDAK_DARURAT"}, "")
		appErrStatus(t, err, 403)
	})

	t.Run("Pejabat ditolak", func(t *testing.T) {
		_, err := e.svc.SetCondition(ctx, referral.Actor{UserID: e.pejabat, Role: auth.RolePejabat}, reportID,
			referral.ConditionRequest{Condition: "TIDAK_DARURAT"}, "")
		appErrStatus(t, err, 403)
	})

	t.Run("status di luar jendela ditolak", func(t *testing.T) {
		penelaahan := e.report("32", "PENELAAHAN", &e.satgasA)
		_, err := e.svc.SetCondition(ctx, actor, penelaahan,
			referral.ConditionRequest{Condition: "DARURAT"}, "")
		appErrStatus(t, err, 409)
	})

	t.Run("kasus selesai ditolak", func(t *testing.T) {
		selesai := e.report("33", "SELESAI", &e.satgasA)
		_, err := e.svc.SetCondition(ctx, actor, selesai,
			referral.ConditionRequest{Condition: "TIDAK_DARURAT"}, "")
		appErrStatus(t, err, 409)
	})

	t.Run("nilai tidak valid ditolak sebelum menulis", func(t *testing.T) {
		_, err := e.svc.SetCondition(ctx, actor, reportID,
			referral.ConditionRequest{Condition: "TINGGI"}, "")
		appErrStatus(t, err, 422)
	})
}

func validReferralRequest(partyID *int64) referral.ReferralRequest {
	return referral.ReferralRequest{
		ServiceType:   "PSIKOLOGIS",
		TargetPartyID: partyID,
		Institution:   "Unit Layanan Psikologi",
		ReferralDate:  "2026-11-20",
		Notes:         "Rujukan awal",
	}
}

func TestCreateReferral(t *testing.T) {
	e := newIntegrationEnv(t)
	ctx := context.Background()
	actor := referral.Actor{UserID: e.satgasA, Role: auth.RoleSatgas}

	reportID := e.report("41", "PEMERIKSAAN", &e.satgasA)
	partyID := e.party(reportID, "KORBAN")

	view, err := e.svc.CreateReferral(ctx, actor, reportID, validReferralRequest(&partyID), "203.0.113.8")
	if err != nil {
		t.Fatalf("CreateReferral: %v", err)
	}
	if view.ID <= 0 || view.ServiceType != "PSIKOLOGIS" || view.FollowUpStatus != "DIAJUKAN" {
		t.Errorf("respons rujukan = %+v", view)
	}
	if view.TargetPartyID == nil || *view.TargetPartyID != partyID {
		t.Errorf("target pihak = %v, ingin %d", view.TargetPartyID, partyID)
	}
	if view.TargetPartyName == nil || *view.TargetPartyName != "Pihak-"+e.prefix {
		t.Errorf("nama target pihak = %v", view.TargetPartyName)
	}
	if got := e.auditCount(reportID, audit.ActionReferralCreated); got != 1 {
		t.Errorf("audit rujukan = %d, ingin 1", got)
	}

	t.Run("pihak kasus lain ditolak dan tidak tersimpan", func(t *testing.T) {
		other := e.report("42", "PEMERIKSAAN", &e.satgasA)
		foreignParty := e.party(other, "KORBAN")

		before := e.countReferrals(reportID)
		_, err := e.svc.CreateReferral(ctx, actor, reportID, validReferralRequest(&foreignParty), "")
		appErrStatus(t, err, 422)
		if after := e.countReferrals(reportID); after != before {
			t.Errorf("rujukan tersimpan meski ditolak: %d -> %d", before, after)
		}
	})

	t.Run("bukan PJ ditolak", func(t *testing.T) {
		_, err := e.svc.CreateReferral(ctx, referral.Actor{UserID: e.satgasB, Role: auth.RoleSatgas}, reportID,
			validReferralRequest(nil), "")
		appErrStatus(t, err, 403)
	})

	t.Run("kasus selesai ditolak", func(t *testing.T) {
		closed := e.report("43", "SELESAI", &e.satgasA)
		_, err := e.svc.CreateReferral(ctx, actor, closed, validReferralRequest(nil), "")
		appErrStatus(t, err, 409)
	})
}

func (e *integrationEnv) countReferrals(reportID int64) int {
	e.t.Helper()
	var count int
	if err := e.pool.QueryRow(context.Background(),
		`SELECT COUNT(*) FROM referrals WHERE report_id = $1`, reportID,
	).Scan(&count); err != nil {
		e.t.Fatalf("hitung rujukan: %v", err)
	}
	return count
}

func TestUpdateReferral(t *testing.T) {
	e := newIntegrationEnv(t)
	ctx := context.Background()
	actor := referral.Actor{UserID: e.satgasA, Role: auth.RoleSatgas}

	reportID := e.report("51", "PEMERIKSAAN", &e.satgasA)
	created, err := e.svc.CreateReferral(ctx, actor, reportID, validReferralRequest(nil), "")
	if err != nil {
		t.Fatalf("CreateReferral: %v", err)
	}

	updated, err := e.svc.UpdateReferral(ctx, actor, reportID, created.ID,
		referral.ReferralUpdateRequest{FollowUpStatus: "DIPROSES"}, "203.0.113.9")
	if err != nil {
		t.Fatalf("UpdateReferral: %v", err)
	}
	if updated.FollowUpStatus != "DIPROSES" {
		t.Errorf("status tindak lanjut = %q", updated.FollowUpStatus)
	}

	var stored string
	if err := e.pool.QueryRow(ctx,
		`SELECT follow_up_status FROM referrals WHERE id = $1`, created.ID,
	).Scan(&stored); err != nil {
		t.Fatalf("baca rujukan: %v", err)
	}
	if stored != "DIPROSES" {
		t.Errorf("kolom follow_up_status = %q", stored)
	}
	if got := e.auditCount(reportID, audit.ActionReferralUpdated); got != 1 {
		t.Errorf("audit pembaruan = %d, ingin 1", got)
	}

	t.Run("rujukan kasus lain tidak ditemukan", func(t *testing.T) {
		other := e.report("52", "PEMERIKSAAN", &e.satgasA)
		foreign, err := e.svc.CreateReferral(ctx, actor, other, validReferralRequest(nil), "")
		if err != nil {
			t.Fatalf("CreateReferral kasus lain: %v", err)
		}
		_, err = e.svc.UpdateReferral(ctx, actor, reportID, foreign.ID,
			referral.ReferralUpdateRequest{FollowUpStatus: "SELESAI"}, "")
		appErrStatus(t, err, 404)
	})

	t.Run("bukan PJ ditolak", func(t *testing.T) {
		_, err := e.svc.UpdateReferral(ctx, referral.Actor{UserID: e.satgasB, Role: auth.RoleSatgas},
			reportID, created.ID, referral.ReferralUpdateRequest{FollowUpStatus: "SELESAI"}, "")
		appErrStatus(t, err, 403)
	})

	t.Run("kasus selesai ditolak", func(t *testing.T) {
		e.setStatus(reportID, "SELESAI")
		_, err := e.svc.UpdateReferral(ctx, actor, reportID, created.ID,
			referral.ReferralUpdateRequest{FollowUpStatus: "SELESAI"}, "")
		appErrStatus(t, err, 409)
	})
}

func TestRecordRecovery(t *testing.T) {
	e := newIntegrationEnv(t)
	ctx := context.Background()
	actor := referral.Actor{UserID: e.satgasA, Role: auth.RoleSatgas}

	reportID := e.report("61", "PEMANTAUAN", &e.satgasA)

	view, err := e.svc.RecordRecovery(ctx, actor, reportID, referral.RecoveryRequest{
		Type:       "PEMANTAUAN",
		Note:       "Pemantauan kondisi korban",
		RecordedAt: "2026-11-25",
	}, "203.0.113.10")
	if err != nil {
		t.Fatalf("RecordRecovery: %v", err)
	}
	if view.ID <= 0 || view.Type != "PEMANTAUAN" || view.Note == "" {
		t.Errorf("respons pemulihan = %+v", view)
	}
	if got := e.auditCount(reportID, audit.ActionRecoveryRecorded); got != 1 {
		t.Errorf("audit pemulihan = %d, ingin 1", got)
	}

	t.Run("catatan kosong ditolak", func(t *testing.T) {
		before := e.countRecoveries(reportID)
		_, err := e.svc.RecordRecovery(ctx, actor, reportID, referral.RecoveryRequest{
			Type: "EVALUASI", RecordedAt: "2026-11-25",
		}, "")
		appErrStatus(t, err, 422)
		if after := e.countRecoveries(reportID); after != before {
			t.Errorf("pemulihan tersimpan meski ditolak: %d -> %d", before, after)
		}
	})

	t.Run("bukan PJ ditolak", func(t *testing.T) {
		_, err := e.svc.RecordRecovery(ctx, referral.Actor{UserID: e.satgasB, Role: auth.RoleSatgas},
			reportID, referral.RecoveryRequest{
				Type: "EVALUASI", Note: "x", RecordedAt: "2026-11-25",
			}, "")
		appErrStatus(t, err, 403)
	})

	t.Run("kasus selesai ditolak", func(t *testing.T) {
		closed := e.report("62", "SELESAI", &e.satgasA)
		_, err := e.svc.RecordRecovery(ctx, actor, closed, referral.RecoveryRequest{
			Type: "PENCEGAHAN", Note: "x", RecordedAt: "2026-11-25",
		}, "")
		appErrStatus(t, err, 409)
	})
}

func (e *integrationEnv) countRecoveries(reportID int64) int {
	e.t.Helper()
	var count int
	if err := e.pool.QueryRow(context.Background(),
		`SELECT COUNT(*) FROM recovery_monitorings WHERE report_id = $1`, reportID,
	).Scan(&count); err != nil {
		e.t.Fatalf("hitung pemulihan: %v", err)
	}
	return count
}
