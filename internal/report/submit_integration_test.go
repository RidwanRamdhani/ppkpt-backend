package report_test

// Tes integrasi ini membutuhkan database yang sudah dimigrasi. Set
// TEST_DATABASE_URL untuk menjalankannya, misalnya:
//
//	TEST_DATABASE_URL=postgres://ppkpt_app:secret@localhost:5432/ppkpt?sslmode=disable go test ./internal/report
//
// Silo tidak dibutuhkan karena objek disimpan oleh fake store. Yang diuji:
// multipart -> validasi -> transaksi -> baris database, hash PIN, riwayat
// status, audit, dan email bukti pelaporan.

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/RidwanRamdhani/ppkpt-backend/internal/audit"
	"github.com/RidwanRamdhani/ppkpt-backend/internal/evidence"
	"github.com/RidwanRamdhani/ppkpt-backend/internal/notification"
	"github.com/RidwanRamdhani/ppkpt-backend/internal/platform/captcha"
	"github.com/RidwanRamdhani/ppkpt-backend/internal/platform/httpx"
	"github.com/RidwanRamdhani/ppkpt-backend/internal/report"
	"github.com/jackc/pgx/v5/pgxpool"
	"golang.org/x/crypto/bcrypt"
)

func TestIntegrasiSubmitReport(t *testing.T) {
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
	defer pool.Close()
	if err := pool.Ping(ctx); err != nil {
		t.Fatalf("ping: %v", err)
	}

	store := newFakeObjectStore()
	sender := &fakeSender{}
	svc := report.NewService(
		pool,
		evidence.NewService(store, audit.NewService(), nil, evidence.Options{MaxUploadBytes: 1 << 20, MaxFiles: 5}),
		audit.NewService(),
		notification.New(sender, nil),
		nil,
		report.Options{BcryptCost: bcrypt.MinCost},
	)
	captchaVerifier, err := captcha.New(false, "", "")
	if err != nil {
		t.Fatalf("captcha: %v", err)
	}
	handler := report.NewHandler(svc, captchaVerifier, nil, nil, 20<<20)

	payload, err := json.Marshal(validRequest())
	if err != nil {
		t.Fatalf("marshal payload: %v", err)
	}

	req := newMultipartRequest(t, string(payload), "",
		[]upload{{name: "bukti.png", content: pngContent()}})
	rec := httptest.NewRecorder()
	handler.Submit(rec, req)

	if rec.Code != 201 {
		t.Fatalf("status = %d (%s), ingin 201", rec.Code, rec.Body.String())
	}

	var result report.Result
	if err := json.Unmarshal(rec.Body.Bytes(), &result); err != nil {
		t.Fatalf("decode hasil: %v", err)
	}
	if len(result.CaseNumber) != report.TicketDigits || strings.Trim(result.CaseNumber, "0123456789") != "" {
		t.Errorf("kode tiket tidak valid: %q", result.CaseNumber)
	}
	if len(result.PIN) != report.PINDigits || strings.Trim(result.PIN, "0123456789") != "" {
		t.Errorf("PIN tidak valid: %q", result.PIN)
	}

	var (
		reportID int64
		status   string
		pinHash  string
		email    string
	)
	err = pool.QueryRow(ctx, `
		SELECT r.id, r.status, r.pin_hash, rp.email
		FROM reports r
		JOIN reporters rp ON rp.report_id = r.id
		WHERE r.case_number = $1`, result.CaseNumber).
		Scan(&reportID, &status, &pinHash, &email)
	if err != nil {
		t.Fatalf("baca laporan: %v", err)
	}
	defer func() {
		if _, err := pool.Exec(context.Background(), `DELETE FROM reports WHERE id = $1`, reportID); err != nil {
			t.Logf("bersihkan laporan uji gagal (abaikan): %v", err)
		}
	}()

	if status != "DILAPORKAN" {
		t.Errorf("status = %q, ingin DILAPORKAN", status)
	}
	if email != "pelapor@example.test" {
		t.Errorf("email pelapor = %q", email)
	}
	if pinHash == result.PIN {
		t.Error("PIN tersimpan sebagai plaintext")
	}
	if err := bcrypt.CompareHashAndPassword([]byte(pinHash), []byte(result.PIN)); err != nil {
		t.Errorf("hash PIN tidak cocok dengan PIN respons: %v", err)
	}

	var consentFilled bool
	if err := pool.QueryRow(ctx, `SELECT consent_at IS NOT NULL FROM reports WHERE id = $1`, reportID).
		Scan(&consentFilled); err != nil {
		t.Fatalf("baca consent_at: %v", err)
	}
	if !consentFilled {
		t.Error("consent_at tidak diisi server")
	}

	counts := map[string]int{}
	queries := map[string]string{
		"korban":   `SELECT count(*) FROM parties WHERE report_id = $1 AND party_type = 'KORBAN'`,
		"terlapor": `SELECT count(*) FROM parties WHERE report_id = $1 AND party_type = 'TERLAPOR'`,
		"kejadian": `SELECT count(*) FROM incidents WHERE report_id = $1`,
		"jenis":    `SELECT count(*) FROM report_violation_types WHERE report_id = $1`,
		"bukti":    `SELECT count(*) FROM evidences WHERE report_id = $1 AND category = 'BUKTI'`,
	}
	for name, q := range queries {
		var n int
		if err := pool.QueryRow(ctx, q, reportID).Scan(&n); err != nil {
			t.Fatalf("hitung %s: %v", name, err)
		}
		counts[name] = n
	}
	want := map[string]int{"korban": 1, "terlapor": 1, "kejadian": 1, "jenis": 1, "bukti": 1}
	for name, expected := range want {
		if counts[name] != expected {
			t.Errorf("jumlah %s = %d, ingin %d", name, counts[name], expected)
		}
	}

	var storedName, originalName, mimeType string
	if err := pool.QueryRow(ctx, `
		SELECT stored_name, original_name, mime_type FROM evidences WHERE report_id = $1`, reportID).
		Scan(&storedName, &originalName, &mimeType); err != nil {
		t.Fatalf("baca evidence: %v", err)
	}
	if storedName == originalName || originalName != "bukti.png" {
		t.Errorf("nama objek tidak disamarkan: stored=%q original=%q", storedName, originalName)
	}
	if mimeType != "image/png" {
		t.Errorf("mime terdeteksi = %q, ingin image/png", mimeType)
	}
	if _, err := store.Get(ctx, storedName); err != nil {
		t.Errorf("objek %q tidak ada di storage: %v", storedName, err)
	}

	var historyCount int
	if err := pool.QueryRow(ctx, `
		SELECT count(*) FROM status_history
		WHERE report_id = $1 AND from_status IS NULL AND to_status = 'DILAPORKAN'`, reportID).
		Scan(&historyCount); err != nil {
		t.Fatalf("baca status_history: %v", err)
	}
	if historyCount != 1 {
		t.Errorf("status_history awal = %d, ingin 1", historyCount)
	}

	var auditCount int
	if err := pool.QueryRow(ctx, `
		SELECT count(*) FROM audit_logs
		WHERE action = 'REPORT_SUBMITTED' AND entity = 'report' AND entity_id = $1`, reportID).
		Scan(&auditCount); err != nil {
		t.Fatalf("baca audit: %v", err)
	}
	if auditCount != 1 {
		t.Errorf("audit REPORT_SUBMITTED = %d, ingin 1", auditCount)
	}

	sender.mu.Lock()
	emails := append([]sentEmail(nil), sender.emails...)
	sender.mu.Unlock()
	if len(emails) != 1 {
		t.Fatalf("email terkirim = %d, ingin 1", len(emails))
	}
	if !strings.Contains(emails[0].body, result.CaseNumber) || !strings.Contains(emails[0].body, result.PIN) {
		t.Error("email bukti tidak memuat tiket/PIN")
	}
}

// TestIntegrasiSubmitInvalidTidakMenyisakanData memastikan payload tidak valid
// tidak membuat baris laporan maupun objek di storage.
func TestIntegrasiSubmitInvalidTidakMenyisakanData(t *testing.T) {
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("TEST_DATABASE_URL tidak diisi; tes integrasi dilewati")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatalf("koneksi: %v", err)
	}
	defer pool.Close()

	store := newFakeObjectStore()
	svc := report.NewService(
		pool,
		evidence.NewService(store, audit.NewService(), nil, evidence.Options{MaxUploadBytes: 1 << 20, MaxFiles: 5}),
		nil, nil, nil,
		report.Options{BcryptCost: bcrypt.MinCost},
	)
	handler := report.NewHandler(svc, nil, nil, nil, 20<<20)

	var before int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM reports`).Scan(&before); err != nil {
		t.Fatalf("hitung laporan awal: %v", err)
	}

	req := validRequest()
	req.Chronology = ""
	payload, err := json.Marshal(req)
	if err != nil {
		t.Fatalf("marshal payload: %v", err)
	}

	rec := httptest.NewRecorder()
	handler.Submit(rec, newMultipartRequest(t, string(payload), "",
		[]upload{{name: "bukti.png", content: pngContent()}}))

	if rec.Code != 422 {
		t.Fatalf("status = %d (%s), ingin 422", rec.Code, rec.Body.String())
	}
	var env errorEnvelope
	if err := json.Unmarshal(rec.Body.Bytes(), &env); err != nil {
		t.Fatalf("decode error: %v", err)
	}
	if env.Error.Code != httpx.CodeValidation {
		t.Errorf("kode error = %q, ingin %q", env.Error.Code, httpx.CodeValidation)
	}

	var after int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM reports`).Scan(&after); err != nil {
		t.Fatalf("hitung laporan akhir: %v", err)
	}
	if after != before {
		t.Errorf("jumlah laporan berubah %d -> %d padahal payload tidak valid", before, after)
	}
	if store.putCount() != 0 {
		t.Errorf("ada %d objek terunggah padahal payload tidak valid", store.putCount())
	}
}
