package review_test

// Tes integrasi ini membutuhkan PostgreSQL dengan migrasi 0001 terpasang. Isi
// TEST_DATABASE_URL untuk menjalankannya, misalnya:
//
//	TEST_DATABASE_URL=postgres://ppkpt_app:secret@localhost:5432/ppkpt?sslmode=disable go test ./internal/review
//
// Akun uji (username berawalan "test_review_") tidak dihapus karena audit_logs
// append-only mereferensikan users. Laporan uji dihapus di akhir tes.

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/RidwanRamdhani/ppkpt-backend/internal/audit"
	"github.com/RidwanRamdhani/ppkpt-backend/internal/auth"
	"github.com/RidwanRamdhani/ppkpt-backend/internal/deadline"
	"github.com/RidwanRamdhani/ppkpt-backend/internal/evidence"
	"github.com/RidwanRamdhani/ppkpt-backend/internal/platform/httpx"
	"github.com/RidwanRamdhani/ppkpt-backend/internal/platform/storage"
	"github.com/RidwanRamdhani/ppkpt-backend/internal/review"
	"github.com/jackc/pgx/v5/pgxpool"
)

type integrationEnv struct {
	t       *testing.T
	pool    *pgxpool.Pool
	svc     *review.Service
	store   *fakeStore
	prefix  string
	satgasA int64
	satgasB int64
}

type fakeStore struct {
	mu      sync.Mutex
	puts    []string
	removed []string
}

func (f *fakeStore) Put(_ context.Context, key string, _ io.Reader, _ int64, _ string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.puts = append(f.puts, key)
	return nil
}

func (f *fakeStore) Get(_ context.Context, _ string) (*storage.Object, error) {
	return nil, storage.ErrNotFound
}

func (f *fakeStore) Remove(_ context.Context, key string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.removed = append(f.removed, key)
	return nil
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
	store := &fakeStore{}
	env := &integrationEnv{
		t:      t,
		pool:   pool,
		store:  store,
		prefix: prefix,
		svc: review.NewService(
			pool,
			evidence.NewService(store, audit.NewService(), logger, evidence.Options{}),
			audit.NewService(),
			nil,
			logger,
			review.Options{ReviewDueDays: 7, ExamDueDays: 30},
		),
	}
	env.satgasA = env.user(auth.RoleSatgas, "satgas_a")
	env.satgasB = env.user(auth.RoleSatgas, "satgas_b")
	return env
}

// cleanupReports menghapus peserta sesi lebih dulu karena session_participants
// tidak memakai ON DELETE CASCADE (mengikuti SDD 5.2).
func cleanupReports(t *testing.T, pool *pgxpool.Pool, prefix string) {
	t.Helper()
	ctx := context.Background()
	like := prefix + "%"

	if _, err := pool.Exec(ctx, `
		DELETE FROM session_participants sp
		USING examination_sessions s, reports r
		WHERE sp.session_id = s.id AND s.report_id = r.id AND r.case_number LIKE $1`,
		like,
	); err != nil {
		t.Logf("bersihkan peserta sesi gagal (abaikan): %v", err)
	}
	if _, err := pool.Exec(ctx, `DELETE FROM reports WHERE case_number LIKE $1`, like); err != nil {
		t.Logf("bersihkan laporan uji gagal (abaikan): %v", err)
	}
}

func (e *integrationEnv) user(role, label string) int64 {
	e.t.Helper()
	username := fmt.Sprintf("test_review_%s_%s", e.prefix, label)
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

type reportSeed struct {
	suffix    string
	status    string
	assigned  *int64
	reviewDue *time.Time
	examDue   *time.Time
}

// report membuat laporan beserta pelapor, satu pihak KORBAN, dan riwayat status
// awal. Pihak KORBAN dipakai sebagai peserta sesi pada tes.
func (e *integrationEnv) report(seed reportSeed) (int64, int64) {
	e.t.Helper()
	ctx := context.Background()
	caseNumber := e.prefix + seed.suffix

	var id int64
	err := e.pool.QueryRow(ctx, `
		INSERT INTO reports
			(case_number, pin_hash, channel, status, reporter_is_victim,
			 chronology, assigned_to, review_due_at, exam_due_at)
		VALUES ($1, 'x', 'HOTLINE', $2, TRUE, 'Kronologi uji', $3, $4, $5)
		RETURNING id`,
		caseNumber, seed.status, seed.assigned, seed.reviewDue, seed.examDue,
	).Scan(&id)
	if err != nil {
		e.t.Fatalf("buat laporan %s: %v", caseNumber, err)
	}

	if _, err := e.pool.Exec(ctx, `
		INSERT INTO reporters (report_id, name, age, gender, category, institution, email)
		VALUES ($1, $2, 21, 'PEREMPUAN', 'MAHASISWA', 'Telkom University', $3)`,
		id, "Pelapor-"+e.prefix, "pelapor-"+e.prefix+"@test.local",
	); err != nil {
		e.t.Fatalf("buat pelapor: %v", err)
	}

	var partyID int64
	if err := e.pool.QueryRow(ctx, `
		INSERT INTO parties (report_id, party_type, name, gender, category, institution)
		VALUES ($1, 'KORBAN', $2, 'PEREMPUAN', 'MAHASISWA', 'Telkom University')
		RETURNING id`,
		id, "Korban-"+e.prefix,
	).Scan(&partyID); err != nil {
		e.t.Fatalf("buat pihak: %v", err)
	}

	if _, err := e.pool.Exec(ctx, `
		INSERT INTO status_history (report_id, from_status, to_status, changed_by)
		VALUES ($1, NULL, $2, NULL)`, id, seed.status,
	); err != nil {
		e.t.Fatalf("buat riwayat status: %v", err)
	}
	return id, partyID
}

func (e *integrationEnv) auditCount(action string, reportID int64) int {
	e.t.Helper()
	var count int
	if err := e.pool.QueryRow(context.Background(), `
		SELECT count(*) FROM audit_logs
		WHERE action = $1 AND entity = 'report' AND entity_id = $2`,
		action, reportID,
	).Scan(&count); err != nil {
		e.t.Fatalf("hitung audit %s: %v", action, err)
	}
	return count
}

func (e *integrationEnv) statusOf(reportID int64) string {
	e.t.Helper()
	var status string
	if err := e.pool.QueryRow(context.Background(),
		`SELECT status FROM reports WHERE id = $1`, reportID,
	).Scan(&status); err != nil {
		e.t.Fatalf("baca status: %v", err)
	}
	return status
}

func appErrStatus(t *testing.T, err error, want int) {
	t.Helper()
	var appErr *httpx.AppError
	if !errors.As(err, &appErr) {
		t.Fatalf("error bukan AppError: %v", err)
	}
	if appErr.Status != want {
		t.Fatalf("status AppError = %d, ingin %d (%s)", appErr.Status, want, appErr.Message)
	}
}

func actorSatgas(userID int64) review.Actor {
	return review.Actor{UserID: userID, Role: auth.RoleSatgas}
}

func todayWIB() string {
	return time.Now().In(deadline.Jakarta).Format("2006-01-02")
}

func TestIntegrasiAlurPenelaahanDanPemeriksaan(t *testing.T) {
	e := newIntegrationEnv(t)
	ctx := context.Background()
	actorA := actorSatgas(e.satgasA)
	actorB := actorSatgas(e.satgasB)

	reportID, partyID := e.report(reportSeed{
		suffix: "01", status: "PENELAAHAN", assigned: &e.satgasA,
	})

	// Bukan PJ tidak boleh menulis.
	_, err := e.svc.SaveReview(ctx, actorB, reportID,
		review.ReviewRequest{VictimIdentified: "x"}, "")
	appErrStatus(t, err, http.StatusForbidden)

	// Simpan catatan penelaahan.
	view, err := e.svc.SaveReview(ctx, actorA, reportID,
		review.ReviewRequest{VictimIdentified: "Korban dikenali"}, "203.0.113.9")
	if err != nil {
		t.Fatalf("SaveReview: %v", err)
	}
	if view.Member.ID != e.satgasA || view.VictimIdentified != "Korban dikenali" {
		t.Fatalf("view = %+v", view)
	}

	// Upsert: baris tetap satu, field diganti.
	view, err = e.svc.SaveReview(ctx, actorA, reportID,
		review.ReviewRequest{WitnessIdentified: "Saksi A"}, "")
	if err != nil {
		t.Fatalf("SaveReview kedua: %v", err)
	}
	if view.VictimIdentified != "" || view.WitnessIdentified != "Saksi A" {
		t.Fatalf("upsert tidak mengganti field: %+v", view)
	}
	var notes int
	if err := e.pool.QueryRow(ctx,
		`SELECT count(*) FROM review_notes WHERE report_id = $1`, reportID,
	).Scan(&notes); err != nil {
		t.Fatalf("hitung review_notes: %v", err)
	}
	if notes != 1 {
		t.Fatalf("review_notes = %d, ingin 1", notes)
	}
	if got := e.auditCount(audit.ActionReviewRecorded, reportID); got != 2 {
		t.Fatalf("audit REVIEW_RECORDED = %d, ingin 2", got)
	}

	// Lanjut ke pemeriksaan.
	result, err := e.svc.ContinueReview(ctx, actorA, reportID, "")
	if err != nil {
		t.Fatalf("ContinueReview: %v", err)
	}
	if result.Status != "PEMERIKSAAN" || result.ExamDueAt == nil {
		t.Fatalf("hasil lanjut = %+v", result)
	}
	if got := e.statusOf(reportID); got != "PEMERIKSAAN" {
		t.Fatalf("status = %q", got)
	}
	if got := e.auditCount(audit.ActionReviewContinued, reportID); got != 1 {
		t.Fatalf("audit REVIEW_CONTINUED = %d, ingin 1", got)
	}

	// Catat sesi pemeriksaan dengan peserta milik kasus ini.
	session, err := e.svc.CreateSession(ctx, actorA, reportID, review.SessionRequest{
		SessionDate: todayWIB(),
		Summary:     "Wawancara awal",
		Participants: []review.SessionParticipantInput{
			{PartyID: partyID, Role: "KORBAN"},
		},
	}, "")
	if err != nil {
		t.Fatalf("CreateSession: %v", err)
	}
	if session.ID == 0 || len(session.Participants) != 1 ||
		session.Participants[0].PartyID != partyID {
		t.Fatalf("sesi = %+v", session)
	}
	if got := e.auditCount(audit.ActionExamSessionCreated, reportID); got != 1 {
		t.Fatalf("audit EXAM_SESSION_CREATED = %d, ingin 1", got)
	}

	// Unggah BAI.
	file := multipartFileHeader(t, "bai.pdf", []byte("%PDF-1.4 isi BAI"))
	upload, err := e.svc.UploadEvidence(ctx, actorA, reportID, "bai", []*multipart.FileHeader{file}, "")
	if err != nil {
		t.Fatalf("UploadEvidence: %v", err)
	}
	if upload.Category != "BAI" || upload.Uploaded != 1 {
		t.Fatalf("upload = %+v", upload)
	}
	var category string
	var uploadedBy *int64
	if err := e.pool.QueryRow(ctx, `
		SELECT category, uploaded_by FROM evidences
		WHERE report_id = $1 ORDER BY id DESC LIMIT 1`, reportID,
	).Scan(&category, &uploadedBy); err != nil {
		t.Fatalf("baca evidence: %v", err)
	}
	if category != "BAI" || uploadedBy == nil || *uploadedBy != e.satgasA {
		t.Fatalf("evidence category=%q uploaded_by=%v", category, uploadedBy)
	}
	if got := e.auditCount(audit.ActionEvidenceUploaded, reportID); got != 1 {
		t.Fatalf("audit EVIDENCE_UPLOADED = %d, ingin 1", got)
	}

	// Kategori BUKTI tidak boleh lewat endpoint petugas.
	_, err = e.svc.UploadEvidence(ctx, actorA, reportID, "BUKTI", []*multipart.FileHeader{file}, "")
	appErrStatus(t, err, http.StatusUnprocessableEntity)

	// Penutupan hanya dari tahap penelaahan.
	_, err = e.svc.CloseReview(ctx, actorA, reportID, review.CloseRequest{Reason: "x"}, "")
	appErrStatus(t, err, http.StatusConflict)
}

func TestIntegrasiContinueTanpaCatatan(t *testing.T) {
	e := newIntegrationEnv(t)
	ctx := context.Background()
	reportID, _ := e.report(reportSeed{
		suffix: "02", status: "PENELAAHAN", assigned: &e.satgasA,
	})

	_, err := e.svc.ContinueReview(ctx, actorSatgas(e.satgasA), reportID, "")
	appErrStatus(t, err, http.StatusConflict)

	if got := e.statusOf(reportID); got != "PENELAAHAN" {
		t.Fatalf("status berubah tanpa catatan: %q", got)
	}
}

func TestIntegrasiCloseReview(t *testing.T) {
	e := newIntegrationEnv(t)
	ctx := context.Background()
	actorA := actorSatgas(e.satgasA)
	reportID, _ := e.report(reportSeed{
		suffix: "03", status: "PENELAAHAN", assigned: &e.satgasA,
	})

	_, err := e.svc.CloseReview(ctx, actorA, reportID, review.CloseRequest{}, "")
	appErrStatus(t, err, http.StatusUnprocessableEntity)

	result, err := e.svc.CloseReview(ctx, actorA, reportID,
		review.CloseRequest{Reason: "Bukan ranah PPKPT"}, "")
	if err != nil {
		t.Fatalf("CloseReview: %v", err)
	}
	if result.Status != "SELESAI" {
		t.Fatalf("status = %q", result.Status)
	}

	var closedAt *time.Time
	if err := e.pool.QueryRow(ctx,
		`SELECT closed_at FROM reports WHERE id = $1`, reportID,
	).Scan(&closedAt); err != nil {
		t.Fatalf("baca closed_at: %v", err)
	}
	if closedAt == nil {
		t.Fatal("closed_at kosong")
	}
	if got := e.auditCount(audit.ActionReviewClosed, reportID); got != 1 {
		t.Fatalf("audit REVIEW_CLOSED = %d, ingin 1", got)
	}

	// Kasus selesai bersifat read-only.
	_, err = e.svc.SaveReview(ctx, actorA, reportID,
		review.ReviewRequest{VictimIdentified: "x"}, "")
	appErrStatus(t, err, http.StatusConflict)
}

func TestIntegrasiSessionPesertaKasusLain(t *testing.T) {
	e := newIntegrationEnv(t)
	ctx := context.Background()
	actorA := actorSatgas(e.satgasA)

	reportA, _ := e.report(reportSeed{
		suffix: "04", status: "PEMERIKSAAN", assigned: &e.satgasA,
	})
	_, partyB := e.report(reportSeed{
		suffix: "05", status: "PEMERIKSAAN", assigned: &e.satgasB,
	})

	_, err := e.svc.CreateSession(ctx, actorA, reportA, review.SessionRequest{
		SessionDate: todayWIB(),
		Participants: []review.SessionParticipantInput{
			{PartyID: partyB, Role: "SAKSI"},
		},
	}, "")
	appErrStatus(t, err, http.StatusUnprocessableEntity)

	var sessions int
	if err := e.pool.QueryRow(ctx,
		`SELECT count(*) FROM examination_sessions WHERE report_id = $1`, reportA,
	).Scan(&sessions); err != nil {
		t.Fatalf("hitung sesi: %v", err)
	}
	if sessions != 0 {
		t.Fatalf("sesi tersimpan meski peserta dari kasus lain: %d", sessions)
	}
}

func TestIntegrasiUploadStatusTerlarang(t *testing.T) {
	e := newIntegrationEnv(t)
	ctx := context.Background()
	reportID, _ := e.report(reportSeed{
		suffix: "06", status: "KEPUTUSAN", assigned: &e.satgasA,
	})

	file := multipartFileHeader(t, "lampiran.pdf", []byte("%PDF-1.4 isi lampiran"))
	_, err := e.svc.UploadEvidence(ctx, actorSatgas(e.satgasA), reportID,
		"LAMPIRAN", []*multipart.FileHeader{file}, "")
	appErrStatus(t, err, http.StatusConflict)

	e.store.mu.Lock()
	puts := len(e.store.puts)
	e.store.mu.Unlock()
	if puts != 0 {
		t.Fatalf("objek terunggah meski status terlarang: %d", puts)
	}
}

func TestIntegrasiUploadTipeTerlarang(t *testing.T) {
	e := newIntegrationEnv(t)
	ctx := context.Background()
	reportID, _ := e.report(reportSeed{
		suffix: "10", status: "PEMERIKSAAN", assigned: &e.satgasA,
	})

	// Berkas executable yang dinamai .pdf tetap harus ditolak berdasarkan isi.
	file := multipartFileHeader(t, "bai.pdf", []byte("MZ\x90\x00\x03\x00\x00\x00"))
	_, err := e.svc.UploadEvidence(ctx, actorSatgas(e.satgasA), reportID,
		"BAI", []*multipart.FileHeader{file}, "")
	appErrStatus(t, err, http.StatusUnprocessableEntity)

	e.store.mu.Lock()
	puts := len(e.store.puts)
	e.store.mu.Unlock()
	if puts != 0 {
		t.Fatalf("objek terunggah meski tipe berkas terlarang: %d", puts)
	}

	var count int
	if err := e.pool.QueryRow(ctx,
		`SELECT count(*) FROM evidences WHERE report_id = $1 AND category = 'BAI'`, reportID,
	).Scan(&count); err != nil {
		t.Fatalf("hitung evidence: %v", err)
	}
	if count != 0 {
		t.Fatalf("metadata BAI tersimpan meski berkas ditolak: %d", count)
	}
}

func TestIntegrasiSchedulerTenggat(t *testing.T) {
	e := newIntegrationEnv(t)
	ctx := context.Background()
	now := time.Now()
	soon := now.Add(24 * time.Hour)
	past := now.Add(-24 * time.Hour)
	far := now.Add(20 * 24 * time.Hour)

	approaching, _ := e.report(reportSeed{
		suffix: "07", status: "PENELAAHAN", assigned: &e.satgasA, reviewDue: &soon,
	})
	overdue, _ := e.report(reportSeed{
		suffix: "08", status: "PEMERIKSAAN", assigned: &e.satgasA, examDue: &past,
	})
	normal, _ := e.report(reportSeed{
		suffix: "09", status: "PENELAAHAN", assigned: &e.satgasA, reviewDue: &far,
	})

	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	scheduler := review.NewScheduler(e.pool, audit.NewService(), logger, review.SchedulerOptions{
		WarnReviewDays: 2,
		WarnExamDays:   7,
		Now:            func() time.Time { return now },
	})

	warnings, err := scheduler.RunOnce(ctx)
	if err != nil {
		t.Fatalf("RunOnce: %v", err)
	}
	found := map[int64]review.DeadlineWarning{}
	for _, warning := range warnings {
		found[warning.ReportID] = warning
	}
	if _, ok := found[approaching]; !ok {
		t.Error("kasus mendekati tenggat tidak terdeteksi")
	}
	if warning, ok := found[overdue]; !ok || warning.State != deadline.Overdue {
		t.Errorf("kasus melewati tenggat tidak terdeteksi: %+v", warning)
	}
	if _, ok := found[normal]; ok {
		t.Error("kasus dengan tenggat jauh tidak boleh diperingatkan")
	}

	if got := e.auditCount(audit.ActionDeadlineWarningSent, approaching); got != 1 {
		t.Fatalf("audit peringatan mendekati = %d, ingin 1", got)
	}
	if got := e.auditCount(audit.ActionDeadlineWarningSent, overdue); got != 1 {
		t.Fatalf("audit peringatan melewati = %d, ingin 1", got)
	}

	// Pemindaian kedua pada hari yang sama tidak menggandakan audit.
	if _, err := scheduler.RunOnce(ctx); err != nil {
		t.Fatalf("RunOnce kedua: %v", err)
	}
	if got := e.auditCount(audit.ActionDeadlineWarningSent, approaching); got != 1 {
		t.Fatalf("audit peringatan setelah run kedua = %d, ingin 1", got)
	}
}

// multipartFileHeader membangun satu FileHeader multipart berisi berkas uji.
func multipartFileHeader(t *testing.T, name string, content []byte) *multipart.FileHeader {
	t.Helper()

	var buf bytes.Buffer
	writer := multipart.NewWriter(&buf)
	part, err := writer.CreateFormFile("evidences", name)
	if err != nil {
		t.Fatalf("buat bagian berkas: %v", err)
	}
	if _, err := part.Write(content); err != nil {
		t.Fatalf("tulis berkas: %v", err)
	}
	if err := writer.Close(); err != nil {
		t.Fatalf("tutup writer: %v", err)
	}

	req := httptest.NewRequest(http.MethodPost, "/", &buf)
	req.Header.Set("Content-Type", writer.FormDataContentType())
	if err := req.ParseMultipartForm(1 << 20); err != nil {
		t.Fatalf("parse multipart: %v", err)
	}
	t.Cleanup(func() {
		if req.MultipartForm != nil {
			_ = req.MultipartForm.RemoveAll()
		}
	})

	files := req.MultipartForm.File["evidences"]
	if len(files) != 1 {
		t.Fatalf("berkas multipart = %d, ingin 1", len(files))
	}
	return files[0]
}
