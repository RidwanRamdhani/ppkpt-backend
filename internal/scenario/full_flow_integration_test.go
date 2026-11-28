package scenario_test

// Tes skenario penuh lintas modul: lapor → cek status → ambil kasus →
// verifikasi → penelaahan → pemeriksaan (sesi + BAI) → kesimpulan → ajukan →
// keputusan pejabat → penyampaian hasil → tutup. Ini adalah Definition of Done
// Phase 10 ("alur penuh lapor → … → selesai lolos di tes integrasi") sekaligus
// pondasi untuk skenario end-to-end Phase 13.
//
// Membutuhkan PostgreSQL dengan migrasi terpasang; Silo tidak dibutuhkan
// karena objek berkas disimpan oleh fake store:
//
//	TEST_DATABASE_URL=postgres://ppkpt_app:secret@localhost:5432/ppkpt?sslmode=disable go test ./internal/scenario
//
// Laporan uji dihapus di akhir tes. Pengguna uji tidak dihapus karena
// audit_logs bersifat append-only dan menunjuk users (konvensi tes integrasi
// modul lain).

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/RidwanRamdhani/ppkpt-backend/internal/audit"
	"github.com/RidwanRamdhani/ppkpt-backend/internal/auth"
	"github.com/RidwanRamdhani/ppkpt-backend/internal/cases"
	"github.com/RidwanRamdhani/ppkpt-backend/internal/decision"
	"github.com/RidwanRamdhani/ppkpt-backend/internal/evidence"
	"github.com/RidwanRamdhani/ppkpt-backend/internal/platform/captcha"
	"github.com/RidwanRamdhani/ppkpt-backend/internal/platform/httpx"
	"github.com/RidwanRamdhani/ppkpt-backend/internal/platform/storage"
	"github.com/RidwanRamdhani/ppkpt-backend/internal/report"
	"github.com/RidwanRamdhani/ppkpt-backend/internal/review"
	"github.com/jackc/pgx/v5/pgxpool"
	"golang.org/x/crypto/bcrypt"
)

// scenarioIP adalah alamat klien tetap untuk seluruh permintaan pada tes ini.
const scenarioIP = "203.0.113.9:4444"

// filePart adalah satu berkas pada formulir multipart.
type filePart struct {
	name    string
	content []byte
}

func TestIntegrasiAlurPenuhLaporSampaiSelesai(t *testing.T) {
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("TEST_DATABASE_URL tidak diisi; tes integrasi dilewati")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatalf("koneksi: %v", err)
	}
	t.Cleanup(pool.Close)
	if err := pool.Ping(ctx); err != nil {
		t.Fatalf("ping: %v", err)
	}

	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	auditor := audit.NewService()
	store := newFakeStore()
	evidenceSvc := evidence.NewService(store, auditor, logger, evidence.Options{
		MaxUploadBytes: 1 << 20,
		MaxFiles:       5,
	})

	// Akun uji: Satgas menjadi PJ kasus, Pejabat mencatat keputusan.
	prefix := fmt.Sprintf("scn%d", time.Now().UnixNano())
	satgas := scenarioUser(t, pool, prefix, "satgas", auth.RoleSatgas)
	pejabat := scenarioUser(t, pool, prefix, "pejabat", auth.RolePejabat)

	// Tahap 1 — pelapor mengirim laporan portal beserta bukti (FR-01..FR-11).
	reportSvc := report.NewService(pool, evidenceSvc, auditor, nil, logger,
		report.Options{BcryptCost: bcrypt.MinCost})
	verifier, err := captcha.New(false, "", "")
	if err != nil {
		t.Fatalf("captcha: %v", err)
	}
	reportHandler := report.NewHandler(reportSvc, verifier, nil, logger, 20<<20)

	payload, err := json.Marshal(portalPayload())
	if err != nil {
		t.Fatalf("marshal payload: %v", err)
	}
	submitRec := httptest.NewRecorder()
	reportHandler.Submit(submitRec, formRequest(t, "evidences", string(payload),
		[]filePart{{name: "bukti.png", content: pngContent()}}))
	if submitRec.Code != http.StatusCreated {
		t.Fatalf("submit = %d (%s), ingin 201", submitRec.Code, submitRec.Body.String())
	}
	var ticket report.Result
	if err := json.Unmarshal(submitRec.Body.Bytes(), &ticket); err != nil {
		t.Fatalf("decode hasil submit: %v", err)
	}
	t.Cleanup(func() { cleanupReport(t, pool, ticket.CaseNumber) })

	// Tahap 2 — pelapor mengecek status dengan tiket dan PIN (FR-13).
	// Result dan StatusRequest berbagi pasangan (tiket, PIN) sehingga cukup
	// dikonversi.
	statusBody, err := json.Marshal(report.StatusRequest(ticket))
	if err != nil {
		t.Fatalf("marshal permintaan status: %v", err)
	}
	statusReq := httptest.NewRequest(http.MethodPost, "/api/status", bytes.NewReader(statusBody))
	statusReq.Header.Set("Content-Type", "application/json")
	statusReq.RemoteAddr = scenarioIP
	statusRec := httptest.NewRecorder()
	reportHandler.Status(statusRec, statusReq)
	if statusRec.Code != http.StatusOK {
		t.Fatalf("cek status = %d (%s), ingin 200", statusRec.Code, statusRec.Body.String())
	}
	if !strings.Contains(statusRec.Body.String(), ticket.CaseNumber) {
		t.Errorf("respons status tidak memuat kode tiket: %s", statusRec.Body.String())
	}

	var reportID int64
	if err := pool.QueryRow(ctx,
		`SELECT id FROM reports WHERE case_number = $1`, ticket.CaseNumber,
	).Scan(&reportID); err != nil {
		t.Fatalf("cari laporan: %v", err)
	}

	// Tahap 3 — Satgas mengambil kasus (FR-25) lalu memverifikasi kelengkapan
	// (FR-26) sehingga kasus masuk penelaahan.
	casesSvc := cases.NewService(pool, evidenceSvc, auditor, nil, logger,
		cases.Options{BcryptCost: bcrypt.MinCost})
	satgasCase := cases.Actor{UserID: satgas, Role: auth.RoleSatgas}
	if _, err := casesSvc.Take(ctx, satgasCase, reportID, scenarioIP); err != nil {
		t.Fatalf("ambil kasus: %v", err)
	}
	verification, err := casesSvc.Verify(ctx, satgasCase, reportID,
		cases.VerificationRequest{Result: "LENGKAP"}, scenarioIP)
	if err != nil {
		t.Fatalf("verifikasi: %v", err)
	}
	if verification.Status != "PENELAAHAN" {
		t.Fatalf("status setelah verifikasi = %q, ingin PENELAAHAN", verification.Status)
	}

	// Tahap 4 — penelaahan dicatat lalu dilanjutkan ke pemeriksaan (FR-28).
	reviewSvc := review.NewService(pool, evidenceSvc, auditor, nil, logger,
		review.Options{ReviewDueDays: 7, ExamDueDays: 30})
	satgasReview := review.Actor{UserID: satgas, Role: auth.RoleSatgas}
	if _, err := reviewSvc.SaveReview(ctx, satgasReview, reportID, review.ReviewRequest{
		VictimIdentified:  "Korban teridentifikasi dan bersedia dimintai keterangan.",
		WitnessIdentified: "Satu saksi sekelas dikonfirmasi.",
	}, scenarioIP); err != nil {
		t.Fatalf("simpan penelaahan: %v", err)
	}
	continued, err := reviewSvc.ContinueReview(ctx, satgasReview, reportID, scenarioIP)
	if err != nil {
		t.Fatalf("lanjutkan penelaahan: %v", err)
	}
	if continued.Status != "PEMERIKSAAN" {
		t.Fatalf("status setelah penelaahan = %q, ingin PEMERIKSAAN", continued.Status)
	}

	// Tahap 5 — sesi pemeriksaan beserta peserta (FR-30) dan unggah BAI (FR-31).
	participants := sessionParticipants(t, pool, reportID)
	if len(participants) == 0 {
		t.Fatal("pihak kasus tidak ditemukan untuk peserta sesi")
	}
	if _, err := reviewSvc.CreateSession(ctx, satgasReview, reportID, review.SessionRequest{
		SessionDate:  todayWIB(),
		Summary:      "Pemeriksaan korban dan terlapor.",
		Participants: participants,
	}, scenarioIP); err != nil {
		t.Fatalf("sesi pemeriksaan: %v", err)
	}
	upload, err := reviewSvc.UploadEvidence(ctx, satgasReview, reportID, "BAI",
		fileHeaders(t, []filePart{{name: "bai.png", content: pngContent()}}), scenarioIP)
	if err != nil {
		t.Fatalf("unggah BAI: %v", err)
	}
	if upload.Uploaded != 1 {
		t.Fatalf("berkas terunggah = %d, ingin 1", upload.Uploaded)
	}

	// Tahap 6 — kesimpulan diajukan sehingga kasus masuk tahap rekomendasi
	// (FR-36/FR-37).
	decisionSvc := decision.NewService(pool, evidenceSvc, auditor, nil, logger)
	satgasDecision := decision.Actor{UserID: satgas, Role: auth.RoleSatgas}
	conclusion, err := decisionSvc.SaveConclusion(ctx, satgasDecision, reportID,
		decision.ConclusionRequest{
			Result:  "TERBUKTI",
			Summary: "Perbuatan terbukti berdasarkan BAI dan keterangan peserta pemeriksaan.",
			Recommendations: []decision.RecommendationRequest{
				{Category: "ADMINISTRATIF", Severity: "SEDANG", Description: "Teguran tertulis"},
			},
		}, scenarioIP)
	if err != nil {
		t.Fatalf("simpan kesimpulan: %v", err)
	}
	if conclusion.SubmissionStatus != "DRAF" {
		t.Fatalf("submission_status = %q, ingin DRAF", conclusion.SubmissionStatus)
	}
	submitted, err := decisionSvc.SubmitConclusion(ctx, satgasDecision, reportID, scenarioIP)
	if err != nil {
		t.Fatalf("ajukan kesimpulan: %v", err)
	}
	if submitted.Status != "REKOMENDASI" || submitted.SubmittedAt == nil {
		t.Fatalf("hasil ajukan = %+v", submitted)
	}

	// Tahap 7 — Pejabat mencatat keputusan sanksi (FR-39).
	pejabatDecision := decision.Actor{UserID: pejabat, Role: auth.RolePejabat}
	recorded, err := decisionSvc.RecordDecision(ctx, pejabatDecision, reportID,
		decision.DecisionRequest{
			DecisionType:   "SANKSI",
			SanctionDetail: "Teguran tertulis dan pembinaan",
			DecidedAt:      todayWIB(),
		}, scenarioIP)
	if err != nil {
		t.Fatalf("catat keputusan: %v", err)
	}
	if recorded.Status != "KEPUTUSAN" {
		t.Fatalf("status setelah keputusan = %q, ingin KEPUTUSAN", recorded.Status)
	}

	// Tahap 8 — penyampaian hasil pertama memindahkan kasus ke pemantauan
	// (FR-41, temuan #11), lalu PJ menutup kasus (FR-40).
	delivery, err := decisionSvc.RecordDelivery(ctx, satgasDecision, reportID,
		decision.DeliveryRequest{
			DeliveredTo:  "Korban dan pendamping",
			DeliveredAt:  todayWIB(),
			NameRestored: true,
			Note:         "Disampaikan langsung.",
		}, scenarioIP)
	if err != nil {
		t.Fatalf("catat penyampaian: %v", err)
	}
	if delivery.Status != "PEMANTAUAN" {
		t.Fatalf("status setelah penyampaian = %q, ingin PEMANTAUAN", delivery.Status)
	}
	closed, err := decisionSvc.CloseCase(ctx, satgasDecision, reportID, scenarioIP)
	if err != nil {
		t.Fatalf("tutup kasus: %v", err)
	}
	if closed.Status != "SELESAI" || closed.ClosedAt == nil {
		t.Fatalf("penutupan = %+v", closed)
	}

	// Tahap 9 — bukti: seluruh transisi tercatat berurutan pada status_history.
	wantHistory := []string{
		"->DILAPORKAN",
		"DILAPORKAN->PENELAAHAN",
		"PENELAAHAN->PEMERIKSAAN",
		"PEMERIKSAAN->REKOMENDASI",
		"REKOMENDASI->KEPUTUSAN",
		"KEPUTUSAN->PEMANTAUAN",
		"PEMANTAUAN->SELESAI",
	}
	gotHistory := statusHistory(t, pool, reportID)
	if fmt.Sprint(gotHistory) != fmt.Sprint(wantHistory) {
		t.Errorf("status_history = %v, ingin %v", gotHistory, wantHistory)
	}

	// Setiap tahap mencatat audit masing-masing; transisi status dicatat
	// sekali per perpindahan (6 transisi, tahap awal memakai REPORT_SUBMITTED).
	for _, action := range []string{
		"REPORT_SUBMITTED", "CASE_TAKEN", "VERIFICATION_RECORDED",
		"REVIEW_RECORDED", "REVIEW_CONTINUED", "EXAM_SESSION_CREATED",
		"EVIDENCE_UPLOADED", "CONCLUSION_SAVED", "CONCLUSION_SUBMITTED",
		"DECISION_RECORDED", "DELIVERY_RECORDED", "CASE_CLOSED",
	} {
		if count := auditCount(t, pool, reportID, action); count == 0 {
			t.Errorf("audit %s tidak tercatat", action)
		}
	}
	if count := auditCount(t, pool, reportID, "STATUS_CHANGED"); count != 6 {
		t.Errorf("audit STATUS_CHANGED = %d, ingin 6", count)
	}

	// Kasus tertutup bersifat hanya-baca: penulisan berikutnya ditolak 409.
	_, err = decisionSvc.RecordDelivery(ctx, satgasDecision, reportID,
		decision.DeliveryRequest{
			DeliveredTo: "Korban", DeliveredAt: todayWIB(),
		}, scenarioIP)
	appErrStatus(t, err, http.StatusConflict)
}

// portalPayload membangun payload laporan portal yang valid. Tanggal kejadian
// dihitung relatif terhadap hari ini agar tes tidak kedaluwarsa.
func portalPayload() report.SubmitRequest {
	age := 21
	dateStart := time.Now().AddDate(0, 0, -14).Format("2006-01-02")
	dateEnd := time.Now().AddDate(0, 0, -10).Format("2006-01-02")
	return report.SubmitRequest{
		Reporter: report.ReporterInput{
			Name:        "Pelapor Skenario",
			Age:         &age,
			Gender:      "PEREMPUAN",
			Category:    "MAHASISWA",
			IDNumber:    "1234567890",
			Institution: "Telkom University",
			Faculty:     "Fakultas Rekayasa Industri",
			Program:     "Informatika",
			Phone:       "081234567890",
			Email:       "pelapor.skenario@example.test",
		},
		ReporterIsVictim: false,
		Victim: &report.PersonInput{
			Name:            "Korban Skenario",
			Gender:          "PEREMPUAN",
			Category:        "MAHASISWA",
			Faculty:         "Fakultas Rekayasa Industri",
			Program:         "Informatika",
			Contact:         "081298765432",
			RelationToOther: "teman sekelas",
		},
		Accused: []report.PersonInput{{
			Name:            "Terlapor Skenario",
			Gender:          "LAKI_LAKI",
			RelationToOther: "senior",
		}},
		Incidents: []report.IncidentRequest{{
			DateStart:   dateStart,
			DateEnd:     dateEnd,
			Location:    "Gedung A",
			Description: "Kejadian pada skenario penuh.",
		}},
		ViolationTypes:     []string{"PERUNDUNGAN"},
		Chronology:         "Kronologi skenario penuh untuk pengujian integrasi.",
		ReportedElsewhere:  false,
		Consent:            true,
		ConsentTextVersion: "1.0",
	}
}

// formRequest membangun request multipart dengan payload pada field "payload"
// dan berkas pada field yang diberikan.
func formRequest(t *testing.T, fileField, payload string, files []filePart) *http.Request {
	t.Helper()
	var buf bytes.Buffer
	w := multipart.NewWriter(&buf)

	if payload != "" {
		if err := w.WriteField("payload", payload); err != nil {
			t.Fatalf("tulis payload: %v", err)
		}
	}
	for _, f := range files {
		part, err := w.CreateFormFile(fileField, f.name)
		if err != nil {
			t.Fatalf("buat berkas: %v", err)
		}
		if _, err := part.Write(f.content); err != nil {
			t.Fatalf("tulis berkas: %v", err)
		}
	}
	if err := w.Close(); err != nil {
		t.Fatalf("tutup multipart: %v", err)
	}

	req := httptest.NewRequest(http.MethodPost, "/api/skenario", &buf)
	req.Header.Set("Content-Type", w.FormDataContentType())
	req.RemoteAddr = scenarioIP
	return req
}

// fileHeaders mem-parse formulir berkas untuk layanan yang menerima
// []*multipart.FileHeader (unggah BAI/lampiran).
func fileHeaders(t *testing.T, files []filePart) []*multipart.FileHeader {
	t.Helper()
	req := formRequest(t, "evidences", "", files)
	if err := req.ParseMultipartForm(1 << 20); err != nil {
		t.Fatalf("parse multipart: %v", err)
	}
	return req.MultipartForm.File["evidences"]
}

// scenarioUser membuat akun petugas uji tanpa password sungguhan; tes ini
// tidak melalui jalur autentikasi (login 2FA diuji di modul auth).
func scenarioUser(t *testing.T, pool *pgxpool.Pool, prefix, label, role string) int64 {
	t.Helper()
	username := fmt.Sprintf("%s_%s", prefix, label)
	var id int64
	err := pool.QueryRow(context.Background(), `
		INSERT INTO users (name, username, email, password_hash, role, is_active, must_change_password)
		VALUES ($1, $2, $3, 'x', $4, TRUE, FALSE)
		ON CONFLICT (username) DO UPDATE SET name = EXCLUDED.name, role = EXCLUDED.role
		RETURNING id`,
		"Petugas "+label, username, username+"@test.local", role,
	).Scan(&id)
	if err != nil {
		t.Fatalf("buat user %s: %v", label, err)
	}
	return id
}

// sessionParticipants mengambil seluruh pihak kasus sebagai peserta sesi;
// peran peserta memakai jenis pihaknya.
func sessionParticipants(t *testing.T, pool *pgxpool.Pool, reportID int64) []review.SessionParticipantInput {
	t.Helper()
	rows, err := pool.Query(context.Background(), `
		SELECT id, party_type FROM parties
		WHERE report_id = $1 ORDER BY id`, reportID)
	if err != nil {
		t.Fatalf("baca pihak: %v", err)
	}
	defer rows.Close()

	items := make([]review.SessionParticipantInput, 0, 2)
	for rows.Next() {
		var item review.SessionParticipantInput
		if err := rows.Scan(&item.PartyID, &item.Role); err != nil {
			t.Fatalf("baca pihak: %v", err)
		}
		items = append(items, item)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("iterasi pihak: %v", err)
	}
	return items
}

// statusHistory mengembalikan urutan transisi status kasus.
func statusHistory(t *testing.T, pool *pgxpool.Pool, reportID int64) []string {
	t.Helper()
	rows, err := pool.Query(context.Background(), `
		SELECT from_status, to_status FROM status_history
		WHERE report_id = $1 ORDER BY id`, reportID)
	if err != nil {
		t.Fatalf("baca status_history: %v", err)
	}
	defer rows.Close()

	items := make([]string, 0, 8)
	for rows.Next() {
		var from *string
		var to string
		if err := rows.Scan(&from, &to); err != nil {
			t.Fatalf("baca status_history: %v", err)
		}
		if from == nil {
			items = append(items, "->"+to)
			continue
		}
		items = append(items, *from+"->"+to)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("iterasi status_history: %v", err)
	}
	return items
}

// auditCount menghitung kemunculan aksi audit pada satu laporan.
func auditCount(t *testing.T, pool *pgxpool.Pool, reportID int64, action string) int {
	t.Helper()
	var count int
	err := pool.QueryRow(context.Background(), `
		SELECT COUNT(*) FROM audit_logs
		WHERE entity = 'report' AND entity_id = $1 AND action::text = $2`,
		reportID, action,
	).Scan(&count)
	if err != nil {
		t.Fatalf("hitung audit %s: %v", action, err)
	}
	return count
}

// appErrStatus memeriksa bahwa err adalah AppError berstatus yang diminta.
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

// cleanupReport menghapus laporan uji beserta seluruh anaknya. Dua tabel anak
// tidak memakai ON DELETE CASCADE (mengikuti SDD 5.2), sehingga harus dihapus
// lebih dulu.
func cleanupReport(t *testing.T, pool *pgxpool.Pool, caseNumber string) {
	t.Helper()
	ctx := context.Background()

	if _, err := pool.Exec(ctx, `
		DELETE FROM session_participants sp
		USING examination_sessions s, reports r
		WHERE sp.session_id = s.id AND s.report_id = r.id AND r.case_number = $1`,
		caseNumber,
	); err != nil {
		t.Logf("bersihkan peserta sesi gagal (abaikan): %v", err)
	}
	if _, err := pool.Exec(ctx, `
		DELETE FROM recommendations
		WHERE conclusion_id IN (
			SELECT c.id FROM conclusions c
			JOIN reports r ON r.id = c.report_id
			WHERE r.case_number = $1
		)`, caseNumber,
	); err != nil {
		t.Logf("bersihkan rekomendasi gagal (abaikan): %v", err)
	}
	if _, err := pool.Exec(ctx,
		`DELETE FROM reports WHERE case_number = $1`, caseNumber,
	); err != nil {
		t.Logf("bersihkan laporan uji gagal (abaikan): %v", err)
	}
}

// todayWIB mengembalikan hari ini dalam format YYYY-MM-DD zona WIB.
func todayWIB() string {
	return time.Now().In(time.FixedZone("WIB", 7*60*60)).Format("2006-01-02")
}

// pngContent adalah isi berkas PNG minimal yang dikenali http.DetectContentType.
func pngContent() []byte {
	return append([]byte{0x89, 'P', 'N', 'G', 0x0d, 0x0a, 0x1a, 0x0a}, []byte("IHDR-skenario")...)
}

// fakeStore menyimpan objek di memori agar alur unggah berjalan tanpa Silo.
type fakeStore struct {
	mu      sync.Mutex
	objects map[string][]byte
}

func newFakeStore() *fakeStore {
	return &fakeStore{objects: make(map[string][]byte)}
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
