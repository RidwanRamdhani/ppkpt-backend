package report_test

// Tes integrasi untuk cek status (FR-13/14) dan pelengkapan laporan (FR-15).
// Set TEST_DATABASE_URL untuk menjalankannya.

import (
	"context"
	"encoding/json"
	"fmt"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/RidwanRamdhani/ppkpt-backend/internal/audit"
	"github.com/RidwanRamdhani/ppkpt-backend/internal/evidence"
	"github.com/RidwanRamdhani/ppkpt-backend/internal/platform/captcha"
	"github.com/RidwanRamdhani/ppkpt-backend/internal/report"
	"github.com/jackc/pgx/v5/pgxpool"
	"golang.org/x/crypto/bcrypt"
)

func newStatusIntegration(t *testing.T) (*pgxpool.Pool, *report.Service, *report.Handler) {
	t.Helper()
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("TEST_DATABASE_URL tidak diisi; tes integrasi dilewati")
	}

	pool, err := pgxpool.New(context.Background(), dsn)
	if err != nil {
		t.Fatalf("koneksi: %v", err)
	}
	t.Cleanup(pool.Close)

	store := newFakeObjectStore()
	svc := report.NewService(
		pool,
		evidence.NewService(store, audit.NewService(), nil, evidence.Options{MaxUploadBytes: 1 << 20, MaxFiles: 5}),
		audit.NewService(),
		nil, nil,
		report.Options{BcryptCost: bcrypt.MinCost},
	)
	captchaVerifier, err := captcha.New(false, "", "")
	if err != nil {
		t.Fatalf("captcha: %v", err)
	}
	handler := report.NewHandler(svc, captchaVerifier, nil, nil, 20<<20)
	return pool, svc, handler
}

func createReportForTest(t *testing.T, pool *pgxpool.Pool, svc *report.Service) (ticket, pin string, id int64) {
	t.Helper()
	ctx := context.Background()
	result, err := svc.Submit(ctx, validRequest(),
		[]*multipart.FileHeader{fileHeader(t, "evidences", "bukti.png", pngContent())},
		"203.0.113.9")
	if err != nil {
		t.Fatalf("Submit: %v", err)
	}
	if err := pool.QueryRow(ctx, `SELECT id FROM reports WHERE case_number = $1`, result.CaseNumber).Scan(&id); err != nil {
		t.Fatalf("cari laporan: %v", err)
	}
	t.Cleanup(func() {
		if _, err := pool.Exec(context.Background(), `DELETE FROM reports WHERE id = $1`, id); err != nil {
			t.Logf("bersihkan laporan uji gagal (abaikan): %v", err)
		}
	})
	return result.CaseNumber, result.PIN, id
}

func createUserForTest(t *testing.T, pool *pgxpool.Pool) int64 {
	t.Helper()
	var id int64
	suffix := time.Now().UnixNano()
	err := pool.QueryRow(context.Background(), `
		INSERT INTO users (name, username, email, password_hash, role)
		VALUES ('Petugas Uji', $1, $2, 'x', 'SATGAS')
		RETURNING id`,
		fmt.Sprintf("uji%d", suffix), fmt.Sprintf("uji%d@example.test", suffix)).Scan(&id)
	if err != nil {
		t.Fatalf("buat user uji: %v", err)
	}
	t.Cleanup(func() {
		if _, err := pool.Exec(context.Background(), `DELETE FROM users WHERE id = $1`, id); err != nil {
			t.Logf("bersihkan user uji gagal (abaikan): %v", err)
		}
	})
	return id
}

func TestIntegrasiCekStatus(t *testing.T) {
	pool, svc, handler := newStatusIntegration(t)
	ticket, pin, id := createReportForTest(t, pool, svc)
	ctx := context.Background()

	rec := httptest.NewRecorder()
	handler.Status(rec, jsonRequest(t, http.MethodPost, "/api/status",
		fmt.Sprintf(`{"case_number":%q,"pin":%q}`, ticket, pin)))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d (%s), ingin 200", rec.Code, rec.Body.String())
	}
	var view report.StatusView
	if err := json.Unmarshal(rec.Body.Bytes(), &view); err != nil {
		t.Fatalf("decode status: %v", err)
	}
	if view.Stage != 2 || view.Status != "DILAPORKAN" {
		t.Errorf("view = %+v", view)
	}
	if view.Deadline != nil || view.NoteForReporter != "" {
		t.Errorf("data internal/belum relevan muncul: %+v", view)
	}

	// PIN salah lima kali -> percobaan berikutnya terkunci 429.
	for i := 0; i < 5; i++ {
		rec = httptest.NewRecorder()
		handler.Status(rec, jsonRequest(t, http.MethodPost, "/api/status",
			fmt.Sprintf(`{"case_number":%q,"pin":"000000"}`, ticket)))
		if rec.Code != http.StatusUnauthorized {
			t.Fatalf("percobaan %d status = %d, ingin 401", i+1, rec.Code)
		}
	}
	rec = httptest.NewRecorder()
	handler.Status(rec, jsonRequest(t, http.MethodPost, "/api/status",
		fmt.Sprintf(`{"case_number":%q,"pin":%q}`, ticket, pin)))
	if rec.Code != http.StatusTooManyRequests {
		t.Fatalf("status PIN terkunci = %d (%s), ingin 429", rec.Code, rec.Body.String())
	}
	if rec.Header().Get("Retry-After") == "" {
		t.Error("Retry-After tidak ada pada respons terkunci")
	}

	var failedCount int
	var locked *time.Time
	if err := pool.QueryRow(ctx,
		`SELECT pin_failed_count, pin_locked_until FROM reports WHERE id = $1`, id).
		Scan(&failedCount, &locked); err != nil {
		t.Fatalf("baca status kunci: %v", err)
	}
	if locked == nil || !locked.After(time.Now()) {
		t.Errorf("pin_locked_until = %v, ingin di masa depan", locked)
	}
	if failedCount != 0 {
		t.Errorf("pin_failed_count = %d, ingin 0 setelah kunci dipasang", failedCount)
	}

	// Tiket tidak dikenal harus memberi pesan yang sama dengan PIN salah.
	rec = httptest.NewRecorder()
	handler.Status(rec, jsonRequest(t, http.MethodPost, "/api/status",
		`{"case_number":"99999999","pin":"123456"}`))
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("tiket asing status = %d, ingin 401", rec.Code)
	}
	env := decodeError(t, rec)
	if env.Error.Message != "Kode tiket atau PIN salah." {
		t.Errorf("pesan = %q", env.Error.Message)
	}

	// Tiket lebih dari 8 digit tidak boleh terpotong lalu cocok.
	rec = httptest.NewRecorder()
	handler.Status(rec, jsonRequest(t, http.MethodPost, "/api/status",
		fmt.Sprintf(`{"case_number":"%s0","pin":%q}`, ticket, pin)))
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("tiket 9 digit status = %d, ingin 401", rec.Code)
	}
}

func TestIntegrasiStatusPerluDilengkapiDanTenggat(t *testing.T) {
	pool, svc, handler := newStatusIntegration(t)
	userID := createUserForTest(t, pool)
	ticket, pin, id := createReportForTest(t, pool, svc)
	ctx := context.Background()

	if _, err := pool.Exec(ctx, `
		INSERT INTO verifications (report_id, member_id, result, note_for_reporter)
		VALUES ($1, $2, 'PERLU_DILENGKAPI', 'Mohon lampirkan tangkapan layar.')`, id, userID); err != nil {
		t.Fatalf("insert verifikasi: %v", err)
	}
	if _, err := pool.Exec(ctx, `
		UPDATE reports SET status = 'PERLU_DILENGKAPI' WHERE id = $1`, id); err != nil {
		t.Fatalf("ubah status: %v", err)
	}

	rec := httptest.NewRecorder()
	handler.Status(rec, jsonRequest(t, http.MethodPost, "/api/status",
		fmt.Sprintf(`{"case_number":%q,"pin":%q}`, ticket, pin)))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d (%s)", rec.Code, rec.Body.String())
	}
	var view report.StatusView
	if err := json.Unmarshal(rec.Body.Bytes(), &view); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if !view.CanComplete {
		t.Error("can_complete = false, ingin true")
	}
	if view.NoteForReporter != "Mohon lampirkan tangkapan layar." {
		t.Errorf("note_for_reporter = %q", view.NoteForReporter)
	}

	// Masuk penelaahan: tenggat 7 hari harus tampil.
	if _, err := pool.Exec(ctx, `
		UPDATE reports SET status = 'PENELAAHAN',
			review_started_at = now(), review_due_at = now() + interval '7 days'
		WHERE id = $1`, id); err != nil {
		t.Fatalf("ubah status penelaahan: %v", err)
	}
	rec = httptest.NewRecorder()
	handler.Status(rec, jsonRequest(t, http.MethodPost, "/api/status",
		fmt.Sprintf(`{"case_number":%q,"pin":%q}`, ticket, pin)))
	if rec.Code != http.StatusOK {
		t.Fatalf("status penelaahan = %d (%s)", rec.Code, rec.Body.String())
	}
	view = report.StatusView{}
	if err := json.Unmarshal(rec.Body.Bytes(), &view); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if view.Stage != 3 || view.Deadline == nil || view.Deadline.DaysLeft != 7 {
		t.Errorf("view penelaahan = %+v (deadline %+v)", view, view.Deadline)
	}
	if view.NoteForReporter != "" {
		t.Errorf("catatan verifikasi bocor pada status PENELAAHAN: %q", view.NoteForReporter)
	}
}

func TestIntegrasiComplete(t *testing.T) {
	pool, svc, handler := newStatusIntegration(t)
	ticket, pin, id := createReportForTest(t, pool, svc)
	ctx := context.Background()

	if _, err := pool.Exec(ctx, `UPDATE reports SET status = 'PERLU_DILENGKAPI' WHERE id = $1`, id); err != nil {
		t.Fatalf("ubah status: %v", err)
	}

	req := newCompleteRequest(t, completeUpload{
		ticket: ticket,
		pin:    pin,
		text:   "Ini tambahan dari pelapor.",
		files:  []upload{{name: "tambahan.png", content: pngContent()}},
	})
	rec := httptest.NewRecorder()
	handler.Complete(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d (%s), ingin 200", rec.Code, rec.Body.String())
	}
	var completion report.Completion
	if err := json.Unmarshal(rec.Body.Bytes(), &completion); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if completion.Status != "DILAPORKAN" || completion.CaseNumber != ticket {
		t.Errorf("completion = %+v", completion)
	}

	var status, description string
	if err := pool.QueryRow(ctx,
		`SELECT status, COALESCE(additional_description,'') FROM reports WHERE id = $1`, id).
		Scan(&status, &description); err != nil {
		t.Fatalf("baca laporan: %v", err)
	}
	if status != "DILAPORKAN" {
		t.Errorf("status = %q, ingin DILAPORKAN", status)
	}
	if !strings.Contains(description, "WIB] Ini tambahan dari pelapor.") {
		t.Errorf("additional_description tanpa penanda waktu: %q", description)
	}

	var evidenceCount int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM evidences WHERE report_id = $1`, id).
		Scan(&evidenceCount); err != nil {
		t.Fatalf("hitung evidence: %v", err)
	}
	if evidenceCount != 2 {
		t.Errorf("jumlah bukti = %d, ingin 2 (awal + tambahan)", evidenceCount)
	}

	var historyCount int
	if err := pool.QueryRow(ctx, `
		SELECT count(*) FROM status_history
		WHERE report_id = $1 AND from_status = 'PERLU_DILENGKAPI' AND to_status = 'DILAPORKAN'`, id).
		Scan(&historyCount); err != nil {
		t.Fatalf("baca status_history: %v", err)
	}
	if historyCount != 1 {
		t.Errorf("riwayat pelengkapan = %d, ingin 1", historyCount)
	}

	var auditCount int
	if err := pool.QueryRow(ctx, `
		SELECT count(*) FROM audit_logs
		WHERE action = 'REPORT_COMPLETED' AND entity = 'report' AND entity_id = $1`, id).
		Scan(&auditCount); err != nil {
		t.Fatalf("baca audit: %v", err)
	}
	if auditCount != 1 {
		t.Errorf("audit REPORT_COMPLETED = %d, ingin 1", auditCount)
	}

	// Pelengkapan kedua ditolak 409 karena status bukan PERLU_DILENGKAPI lagi.
	req = newCompleteRequest(t, completeUpload{ticket: ticket, pin: pin, text: "lagi"})
	rec = httptest.NewRecorder()
	handler.Complete(rec, req)
	if rec.Code != http.StatusConflict {
		t.Fatalf("pelengkapan kedua = %d (%s), ingin 409", rec.Code, rec.Body.String())
	}
}
