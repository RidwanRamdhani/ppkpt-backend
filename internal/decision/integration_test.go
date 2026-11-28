package decision_test

// Tes integrasi ini membutuhkan PostgreSQL dengan migrasi 0001 terpasang. Isi
// TEST_DATABASE_URL untuk menjalankannya, misalnya:
//
//	TEST_DATABASE_URL=postgres://ppkpt_app:secret@localhost:5432/ppkpt?sslmode=disable go test ./internal/decision
//
// Akun uji (username berawalan "test_decision_") tidak dihapus karena
// audit_logs append-only mereferensikan users. Laporan uji dihapus di akhir tes;
// kesimpulan, rekomendasi, keputusan, dan penyampaian ikut terhapus lewat
// ON DELETE CASCADE.

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/RidwanRamdhani/ppkpt-backend/internal/audit"
	"github.com/RidwanRamdhani/ppkpt-backend/internal/auth"
	"github.com/RidwanRamdhani/ppkpt-backend/internal/decision"
	"github.com/RidwanRamdhani/ppkpt-backend/internal/evidence"
	"github.com/RidwanRamdhani/ppkpt-backend/internal/platform/httpx"
	"github.com/RidwanRamdhani/ppkpt-backend/internal/platform/storage"
	"github.com/jackc/pgx/v5/pgxpool"
)

type fakeStore struct {
	mu      sync.Mutex
	objects map[string][]byte
}

func newFakeStore() *fakeStore {
	return &fakeStore{objects: make(map[string][]byte)}
}

func (f *fakeStore) seed(key, content string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.objects[key] = []byte(content)
}

func (f *fakeStore) Put(_ context.Context, key string, r io.Reader, _ int64, _ string) error {
	data, err := io.ReadAll(r)
	if err != nil {
		return err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	f.objects[key] = data
	return nil
}

func (f *fakeStore) Get(_ context.Context, key string) (*storage.Object, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	data, ok := f.objects[key]
	if !ok {
		return nil, storage.ErrNotFound
	}
	return &storage.Object{
		Body:        io.NopCloser(bytes.NewReader(data)),
		Size:        int64(len(data)),
		ContentType: "application/pdf",
	}, nil
}

func (f *fakeStore) Remove(_ context.Context, key string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	delete(f.objects, key)
	return nil
}

type integrationEnv struct {
	t       *testing.T
	pool    *pgxpool.Pool
	svc     *decision.Service
	store   *fakeStore
	prefix  string
	seq     int
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
	t.Cleanup(func() { cleanupReports(t, pool, prefix) })

	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	store := newFakeStore()
	auditor := audit.NewService()
	env := &integrationEnv{
		t:      t,
		pool:   pool,
		store:  store,
		prefix: prefix,
		svc: decision.NewService(
			pool,
			evidence.NewService(store, auditor, logger, evidence.Options{}),
			auditor,
			nil,
			logger,
		),
	}
	env.satgasA = env.user("satgas_a", auth.RoleSatgas)
	env.satgasB = env.user("satgas_b", auth.RoleSatgas)
	env.pejabat = env.user("pejabat", auth.RolePejabat)
	return env
}

func cleanupReports(t *testing.T, pool *pgxpool.Pool, prefix string) {
	t.Helper()
	ctx := context.Background()
	// recommendations.conclusion_id tidak memakai ON DELETE CASCADE, sehingga
	// harus dihapus sebelum laporan (yang menghapus conclusions lewat cascade).
	if _, err := pool.Exec(ctx, `
		DELETE FROM recommendations
		WHERE conclusion_id IN (
			SELECT c.id FROM conclusions c
			JOIN reports r ON r.id = c.report_id
			WHERE r.case_number LIKE $1
		)`, prefix+"%",
	); err != nil {
		t.Logf("bersihkan rekomendasi uji gagal (abaikan): %v", err)
	}
	if _, err := pool.Exec(ctx,
		`DELETE FROM reports WHERE case_number LIKE $1`, prefix+"%",
	); err != nil {
		t.Logf("bersihkan laporan uji gagal (abaikan): %v", err)
	}
}

func (e *integrationEnv) user(label, role string) int64 {
	e.t.Helper()
	username := fmt.Sprintf("test_decision_%s_%s", e.prefix, label)
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

func (e *integrationEnv) report(status string, assigned *int64) int64 {
	e.t.Helper()
	e.seq++
	var id int64
	err := e.pool.QueryRow(context.Background(), `
		INSERT INTO reports
			(case_number, pin_hash, channel, status, reporter_is_victim, chronology, assigned_to)
		VALUES ($1, 'x', 'HOTLINE', $2, TRUE, 'Kronologi uji', $3)
		RETURNING id`,
		fmt.Sprintf("%s%02d", e.prefix, e.seq), status, assigned,
	).Scan(&id)
	if err != nil {
		e.t.Fatalf("buat laporan: %v", err)
	}
	return id
}

func (e *integrationEnv) session(reportID int64) {
	e.t.Helper()
	if _, err := e.pool.Exec(context.Background(), `
		INSERT INTO examination_sessions (report_id, examiner_id, session_date, summary)
		VALUES ($1, $2, CURRENT_DATE, 'Sesi pemeriksaan uji')`,
		reportID, e.satgasA,
	); err != nil {
		e.t.Fatalf("buat sesi pemeriksaan: %v", err)
	}
}

func (e *integrationEnv) evidence(reportID int64, category string) (int64, string) {
	e.t.Helper()
	key := fmt.Sprintf("%s-ev-%d-%s", e.prefix, reportID, category)
	var id int64
	if err := e.pool.QueryRow(context.Background(), `
		INSERT INTO evidences
			(report_id, category, original_name, stored_name, mime_type, size_bytes, uploaded_by)
		VALUES ($1, $2, 'berkas.pdf', $3, 'application/pdf', 4, $4)
		RETURNING id`,
		reportID, category, key, e.satgasA,
	).Scan(&id); err != nil {
		e.t.Fatalf("buat berkas: %v", err)
	}
	return id, key
}

func (e *integrationEnv) conclusion(reportID int64, submission string) {
	e.t.Helper()
	var submittedAt *time.Time
	if submission == "DIAJUKAN" {
		now := time.Now()
		submittedAt = &now
	}
	if _, err := e.pool.Exec(context.Background(), `
		INSERT INTO conclusions (report_id, result, summary, submission_status, submitted_at, created_by)
		VALUES ($1, 'TERBUKTI', 'Ringkasan uji', $2, $3, $4)`,
		reportID, submission, submittedAt, e.satgasA,
	); err != nil {
		e.t.Fatalf("buat kesimpulan: %v", err)
	}
}

func (e *integrationEnv) dbStatus(reportID int64) string {
	e.t.Helper()
	var status string
	if err := e.pool.QueryRow(context.Background(),
		`SELECT status FROM reports WHERE id = $1`, reportID,
	).Scan(&status); err != nil {
		e.t.Fatalf("baca status: %v", err)
	}
	return status
}

func (e *integrationEnv) history(reportID int64) []string {
	e.t.Helper()
	rows, err := e.pool.Query(context.Background(), `
		SELECT from_status, to_status FROM status_history
		WHERE report_id = $1 ORDER BY id`, reportID)
	if err != nil {
		e.t.Fatalf("baca riwayat status: %v", err)
	}
	defer rows.Close()

	items := make([]string, 0, 4)
	for rows.Next() {
		var from *string
		var to string
		if err := rows.Scan(&from, &to); err != nil {
			e.t.Fatalf("baca riwayat status: %v", err)
		}
		if from == nil {
			items = append(items, "->"+to)
			continue
		}
		items = append(items, *from+"->"+to)
	}
	return items
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
	var appErr *httpx.AppError
	if !errors.As(err, &appErr) {
		t.Fatalf("error bukan AppError: %v", err)
	}
	if appErr.Status != want {
		t.Fatalf("status AppError = %d (%s), ingin %d", appErr.Status, appErr.Message, want)
	}
}

func satgasActor(id int64) decision.Actor {
	return decision.Actor{UserID: id, Role: auth.RoleSatgas}
}

func pejabatActor(id int64) decision.Actor {
	return decision.Actor{UserID: id, Role: auth.RolePejabat}
}

func validConclusionRequest() decision.ConclusionRequest {
	return decision.ConclusionRequest{
		Result:  "TERBUKTI",
		Summary: "Perbuatan terbukti berdasarkan BAI dan keterangan saksi.",
		Recommendations: []decision.RecommendationRequest{
			{Category: "ADMINISTRATIF", Severity: "SEDANG", Description: "Teguran tertulis"},
		},
	}
}

func today() string {
	return time.Now().In(time.FixedZone("WIB", 7*60*60)).Format("2006-01-02")
}

func TestIntegrasiAlurPenuhSanksi(t *testing.T) {
	e := newIntegrationEnv(t)
	ctx := context.Background()
	reportID := e.report("PEMERIKSAAN", &e.satgasA)
	e.session(reportID)
	baiID, _ := e.evidence(reportID, "BAI")

	view, err := e.svc.SaveConclusion(ctx, satgasActor(e.satgasA), reportID, validConclusionRequest(), "127.0.0.1")
	if err != nil {
		t.Fatalf("SaveConclusion: %v", err)
	}
	if view.SubmissionStatus != "DRAF" || len(view.Recommendations) != 1 {
		t.Fatalf("kesimpulan = %+v", view)
	}

	submit, err := e.svc.SubmitConclusion(ctx, satgasActor(e.satgasA), reportID, "127.0.0.1")
	if err != nil {
		t.Fatalf("SubmitConclusion: %v", err)
	}
	if submit.Status != "REKOMENDASI" || submit.SubmittedAt == nil {
		t.Fatalf("submit = %+v", submit)
	}
	if got := e.dbStatus(reportID); got != "REKOMENDASI" {
		t.Fatalf("status DB = %q", got)
	}

	decisionView, err := e.svc.ViewDecision(ctx, pejabatActor(e.pejabat), reportID, "")
	if err != nil {
		t.Fatalf("ViewDecision: %v", err)
	}
	if decisionView.Conclusion.Result != "TERBUKTI" || len(decisionView.Conclusion.Recommendations) != 1 {
		t.Errorf("proyeksi kesimpulan = %+v", decisionView.Conclusion)
	}
	if len(decisionView.Evidences) != 1 || decisionView.Evidences[0].ID != baiID {
		t.Errorf("berkas BAI tidak tampil: %+v", decisionView.Evidences)
	}
	if decisionView.AssignedTo == nil || decisionView.AssignedTo.ID != e.satgasA {
		t.Errorf("PJ tidak tampil: %+v", decisionView.AssignedTo)
	}

	recorded, err := e.svc.RecordDecision(ctx, pejabatActor(e.pejabat), reportID, decision.DecisionRequest{
		DecisionType:   "SANKSI",
		SanctionDetail: "Teguran tertulis dan pembinaan",
		DecidedAt:      today(),
	}, "127.0.0.1")
	if err != nil {
		t.Fatalf("RecordDecision: %v", err)
	}
	if recorded.Status != "KEPUTUSAN" || recorded.Official.ID != e.pejabat {
		t.Fatalf("keputusan = %+v", recorded)
	}

	// Close sebelum penyampaian harus ditolak.
	_, err = e.svc.CloseCase(ctx, satgasActor(e.satgasA), reportID, "")
	appErrStatus(t, err, 409)

	delivery, err := e.svc.RecordDelivery(ctx, satgasActor(e.satgasA), reportID, decision.DeliveryRequest{
		DeliveredTo:  "Korban dan pendamping",
		DeliveredAt:  today(),
		NameRestored: true,
		Note:         "Disampaikan langsung",
	}, "127.0.0.1")
	if err != nil {
		t.Fatalf("RecordDelivery: %v", err)
	}
	if delivery.Status != "PEMANTAUAN" {
		t.Fatalf("status setelah penyampaian = %q", delivery.Status)
	}

	closed, err := e.svc.CloseCase(ctx, satgasActor(e.satgasA), reportID, "")
	if err != nil {
		t.Fatalf("CloseCase: %v", err)
	}
	if closed.Status != "SELESAI" || closed.ClosedAt == nil {
		t.Fatalf("penutupan = %+v", closed)
	}
	if got := e.dbStatus(reportID); got != "SELESAI" {
		t.Fatalf("status DB = %q", got)
	}

	want := []string{
		"PEMERIKSAAN->REKOMENDASI",
		"REKOMENDASI->KEPUTUSAN",
		"KEPUTUSAN->PEMANTAUAN",
		"PEMANTAUAN->SELESAI",
	}
	got := e.history(reportID)
	if fmt.Sprint(got) != fmt.Sprint(want) {
		t.Errorf("riwayat status = %v, ingin %v", got, want)
	}

	for _, action := range []string{
		"CONCLUSION_SAVED", "CONCLUSION_SUBMITTED", "DECISION_RECORDED",
		"DELIVERY_RECORDED", "CASE_CLOSED", "STATUS_CHANGED",
	} {
		if count := e.auditCount(reportID, action); count == 0 {
			t.Errorf("audit %s tidak tercatat", action)
		}
	}

	// Kasus selesai bersifat hanya-baca.
	_, err = e.svc.SaveConclusion(ctx, satgasActor(e.satgasA), reportID, validConclusionRequest(), "")
	appErrStatus(t, err, 409)
	_, err = e.svc.CloseCase(ctx, satgasActor(e.satgasA), reportID, "")
	appErrStatus(t, err, 409)
}

func TestIntegrasiKesimpulanPrasyarat(t *testing.T) {
	e := newIntegrationEnv(t)
	ctx := context.Background()
	reportID := e.report("PEMERIKSAAN", &e.satgasA)

	_, err := e.svc.SaveConclusion(ctx, satgasActor(e.satgasA), reportID, validConclusionRequest(), "")
	appErrStatus(t, err, 409) // belum ada sesi pemeriksaan

	e.session(reportID)
	_, err = e.svc.SaveConclusion(ctx, satgasActor(e.satgasA), reportID, validConclusionRequest(), "")
	appErrStatus(t, err, 409) // belum ada BAI

	e.evidence(reportID, "BAI")
	if _, err := e.svc.SaveConclusion(ctx, satgasActor(e.satgasA), reportID, validConclusionRequest(), ""); err != nil {
		t.Fatalf("SaveConclusion: %v", err)
	}

	noRecommendation := validConclusionRequest()
	noRecommendation.Recommendations = nil
	_, err = e.svc.SaveConclusion(ctx, satgasActor(e.satgasA), reportID, noRecommendation, "")
	appErrStatus(t, err, 422)

	notProven := validConclusionRequest()
	notProven.Result = "TIDAK_TERBUKTI"
	_, err = e.svc.SaveConclusion(ctx, satgasActor(e.satgasA), reportID, notProven, "")
	appErrStatus(t, err, 422)
}

func TestIntegrasiKesimpulanImutabel(t *testing.T) {
	e := newIntegrationEnv(t)
	ctx := context.Background()
	reportID := e.report("PEMERIKSAAN", &e.satgasA)
	e.session(reportID)
	e.evidence(reportID, "BAI")

	if _, err := e.svc.SaveConclusion(ctx, satgasActor(e.satgasA), reportID, validConclusionRequest(), ""); err != nil {
		t.Fatalf("SaveConclusion: %v", err)
	}
	if _, err := e.svc.SubmitConclusion(ctx, satgasActor(e.satgasA), reportID, ""); err != nil {
		t.Fatalf("SubmitConclusion: %v", err)
	}

	_, err := e.svc.SaveConclusion(ctx, satgasActor(e.satgasA), reportID, validConclusionRequest(), "")
	appErrStatus(t, err, 409)
	_, err = e.svc.SubmitConclusion(ctx, satgasActor(e.satgasA), reportID, "")
	appErrStatus(t, err, 409)

	// Hanya PJ yang boleh menulis; Pejabat tidak boleh menyusun kesimpulan.
	other := e.report("PEMERIKSAAN", &e.satgasA)
	e.session(other)
	e.evidence(other, "BAI")
	_, err = e.svc.SaveConclusion(ctx, satgasActor(e.satgasB), other, validConclusionRequest(), "")
	appErrStatus(t, err, 403)
	_, err = e.svc.SaveConclusion(ctx, pejabatActor(e.pejabat), other, validConclusionRequest(), "")
	appErrStatus(t, err, 403)
}

func TestIntegrasiKeputusan(t *testing.T) {
	e := newIntegrationEnv(t)
	ctx := context.Background()
	reportID := e.report("PEMERIKSAAN", &e.satgasA)
	e.session(reportID)
	e.evidence(reportID, "BAI")
	if _, err := e.svc.SaveConclusion(ctx, satgasActor(e.satgasA), reportID, validConclusionRequest(), ""); err != nil {
		t.Fatalf("SaveConclusion: %v", err)
	}

	// Belum diajukan: keputusan ditolak.
	_, err := e.svc.RecordDecision(ctx, pejabatActor(e.pejabat), reportID, decision.DecisionRequest{
		DecisionType: "SANKSI", SanctionDetail: "Teguran", DecidedAt: today(),
	}, "")
	appErrStatus(t, err, 409)

	if _, err := e.svc.SubmitConclusion(ctx, satgasActor(e.satgasA), reportID, ""); err != nil {
		t.Fatalf("SubmitConclusion: %v", err)
	}

	// Satgas tidak boleh memutuskan (penjagaan tingkat layanan; middleware juga
	// menolak lewat permission).
	_, err = e.svc.RecordDecision(ctx, satgasActor(e.satgasA), reportID, decision.DecisionRequest{
		DecisionType: "SANKSI", SanctionDetail: "Teguran", DecidedAt: today(),
	}, "")
	appErrStatus(t, err, 403)

	// SANKSI tanpa rincian sanksi.
	_, err = e.svc.RecordDecision(ctx, pejabatActor(e.pejabat), reportID, decision.DecisionRequest{
		DecisionType: "SANKSI", DecidedAt: today(),
	}, "")
	appErrStatus(t, err, 422)

	// Tanggal masa depan.
	_, err = e.svc.RecordDecision(ctx, pejabatActor(e.pejabat), reportID, decision.DecisionRequest{
		DecisionType: "TIDAK_TERBUKTI", DecidedAt: "2999-01-01",
	}, "")
	appErrStatus(t, err, 422)

	if _, err := e.svc.RecordDecision(ctx, pejabatActor(e.pejabat), reportID, decision.DecisionRequest{
		DecisionType: "SANKSI", SanctionDetail: "Teguran tertulis", DecidedAt: today(),
	}, ""); err != nil {
		t.Fatalf("RecordDecision: %v", err)
	}

	// Keputusan ganda ditolak.
	_, err = e.svc.RecordDecision(ctx, pejabatActor(e.pejabat), reportID, decision.DecisionRequest{
		DecisionType: "TIDAK_TERBUKTI", DecidedAt: today(),
	}, "")
	appErrStatus(t, err, 409)

	// decision-view hanya untuk tahap REKOMENDASI.
	_, err = e.svc.ViewDecision(ctx, pejabatActor(e.pejabat), reportID, "")
	appErrStatus(t, err, 409)
}

func TestIntegrasiKeputusanGandaUnik(t *testing.T) {
	e := newIntegrationEnv(t)
	ctx := context.Background()
	reportID := e.report("REKOMENDASI", &e.satgasA)
	e.conclusion(reportID, "DIAJUKAN")
	if _, err := e.pool.Exec(ctx, `
		INSERT INTO decisions (report_id, official_id, decision_type, sanction_detail, decided_at)
		VALUES ($1, $2, 'SANKSI', 'Sanksi awal', CURRENT_DATE)`, reportID, e.pejabat,
	); err != nil {
		t.Fatalf("buat keputusan awal: %v", err)
	}

	_, err := e.svc.RecordDecision(ctx, pejabatActor(e.pejabat), reportID, decision.DecisionRequest{
		DecisionType: "SANKSI", SanctionDetail: "Sanksi kedua", DecidedAt: today(),
	}, "")
	appErrStatus(t, err, 409)
}

func TestIntegrasiKeputusanKonkuren(t *testing.T) {
	e := newIntegrationEnv(t)
	ctx := context.Background()
	reportID := e.report("REKOMENDASI", &e.satgasA)
	e.conclusion(reportID, "DIAJUKAN")

	var wg sync.WaitGroup
	results := make(chan error, 2)
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, err := e.svc.RecordDecision(ctx, pejabatActor(e.pejabat), reportID, decision.DecisionRequest{
				DecisionType: "SANKSI", SanctionDetail: "Sanksi konkuren", DecidedAt: today(),
			}, "")
			results <- err
		}()
	}
	wg.Wait()
	close(results)

	var success, conflict int
	for err := range results {
		if err == nil {
			success++
			continue
		}
		if httpx.FromError(err).Status == 409 {
			conflict++
			continue
		}
		t.Fatalf("error tak terduga: %v", err)
	}
	if success != 1 || conflict != 1 {
		t.Fatalf("sukses=%d konflik=%d, ingin 1 dan 1", success, conflict)
	}
}

func TestIntegrasiTidakTerbuktiMenutup(t *testing.T) {
	e := newIntegrationEnv(t)
	ctx := context.Background()
	reportID := e.report("PEMERIKSAAN", &e.satgasA)
	e.session(reportID)
	e.evidence(reportID, "BAI")

	if _, err := e.svc.SaveConclusion(ctx, satgasActor(e.satgasA), reportID, decision.ConclusionRequest{
		Result:  "TIDAK_TERBUKTI",
		Summary: "Tidak cukup bukti.",
	}, ""); err != nil {
		t.Fatalf("SaveConclusion: %v", err)
	}
	if _, err := e.svc.SubmitConclusion(ctx, satgasActor(e.satgasA), reportID, ""); err != nil {
		t.Fatalf("SubmitConclusion: %v", err)
	}
	if _, err := e.svc.RecordDecision(ctx, pejabatActor(e.pejabat), reportID, decision.DecisionRequest{
		DecisionType: "TIDAK_TERBUKTI", DecidedAt: today(),
	}, ""); err != nil {
		t.Fatalf("RecordDecision: %v", err)
	}

	_, err := e.svc.CloseCase(ctx, satgasActor(e.satgasA), reportID, "")
	appErrStatus(t, err, 409) // belum ada penyampaian

	if _, err := e.svc.RecordDelivery(ctx, satgasActor(e.satgasA), reportID, decision.DeliveryRequest{
		DeliveredTo: "Korban", DeliveredAt: today(), NameRestored: false,
	}, ""); err != nil {
		t.Fatalf("RecordDelivery: %v", err)
	}

	_, err = e.svc.CloseCase(ctx, satgasActor(e.satgasA), reportID, "")
	appErrStatus(t, err, 409) // nama baik belum dipulihkan

	if _, err := e.svc.RecordDelivery(ctx, satgasActor(e.satgasA), reportID, decision.DeliveryRequest{
		DeliveredTo: "Korban", DeliveredAt: today(), NameRestored: true,
	}, ""); err != nil {
		t.Fatalf("RecordDelivery kedua: %v", err)
	}

	closed, err := e.svc.CloseCase(ctx, satgasActor(e.satgasA), reportID, "")
	if err != nil {
		t.Fatalf("CloseCase: %v", err)
	}
	if closed.Status != "SELESAI" {
		t.Fatalf("status = %q", closed.Status)
	}
}

func TestIntegrasiUnduhBerkas(t *testing.T) {
	e := newIntegrationEnv(t)
	ctx := context.Background()

	reportID := e.report("REKOMENDASI", &e.satgasA)
	e.conclusion(reportID, "DIAJUKAN")
	evidenceID, key := e.evidence(reportID, "BAI")
	e.store.seed(key, "isi berkas uji")

	meta, obj, err := e.svc.DownloadEvidence(ctx, satgasActor(e.satgasA), evidenceID, "127.0.0.1")
	if err != nil {
		t.Fatalf("unduh PJ: %v", err)
	}
	if meta.ID != evidenceID {
		t.Errorf("meta = %+v", meta)
	}
	_ = obj.Body.Close()

	_, _, err = e.svc.DownloadEvidence(ctx, satgasActor(e.satgasB), evidenceID, "")
	appErrStatus(t, err, 403)

	if _, _, err := e.svc.DownloadEvidence(ctx, pejabatActor(e.pejabat), evidenceID, ""); err != nil {
		t.Fatalf("unduh Pejabat: %v", err)
	}

	// Kasus yang belum diajukan tidak boleh dibuka Pejabat.
	open := e.report("PENELAAHAN", &e.satgasA)
	openEvidenceID, openKey := e.evidence(open, "BAI")
	e.store.seed(openKey, "berkas")
	_, _, err = e.svc.DownloadEvidence(ctx, pejabatActor(e.pejabat), openEvidenceID, "")
	appErrStatus(t, err, 403)

	_, _, err = e.svc.DownloadEvidence(ctx, satgasActor(e.satgasA), 9_999_999_999, "")
	appErrStatus(t, err, 404)
}
