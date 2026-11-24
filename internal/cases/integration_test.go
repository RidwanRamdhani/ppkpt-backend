package cases_test

// Tes integrasi ini membutuhkan PostgreSQL dengan migrasi 0001 terpasang. Isi
// TEST_DATABASE_URL untuk menjalankannya, misalnya:
//
//	TEST_DATABASE_URL=postgres://ppkpt_app:secret@localhost:5432/ppkpt?sslmode=disable go test ./internal/cases
//
// Akun uji (username berawalan "test_cases_") tidak dihapus karena audit_logs
// append-only mereferensikan users. Laporan uji dihapus di akhir tes.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/RidwanRamdhani/ppkpt-backend/internal/audit"
	"github.com/RidwanRamdhani/ppkpt-backend/internal/auth"
	"github.com/RidwanRamdhani/ppkpt-backend/internal/cases"
	"github.com/RidwanRamdhani/ppkpt-backend/internal/platform/httpx"
	"github.com/jackc/pgx/v5/pgxpool"
)

type integrationEnv struct {
	t       *testing.T
	pool    *pgxpool.Pool
	svc     *cases.Service
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
	if _, err := pool.Exec(ctx, `DELETE FROM reports WHERE case_number LIKE $1`, prefix+"%"); err != nil {
		t.Fatalf("bersihkan laporan lama: %v", err)
	}
	t.Cleanup(func() {
		if _, err := pool.Exec(context.Background(), `DELETE FROM reports WHERE case_number LIKE $1`, prefix+"%"); err != nil {
			t.Logf("bersihkan laporan uji gagal (abaikan): %v", err)
		}
	})

	env := &integrationEnv{
		t:      t,
		pool:   pool,
		prefix: prefix,
		svc: cases.NewService(pool, audit.NewService(),
			slog.New(slog.NewTextHandler(io.Discard, nil)),
			cases.Options{WarnReviewDays: 2, WarnExamDays: 7}),
	}
	env.satgasA = env.user(auth.RoleSatgas, "satgas_a")
	env.satgasB = env.user(auth.RoleSatgas, "satgas_b")
	env.pejabat = env.user(auth.RolePejabat, "pejabat")
	return env
}

func (e *integrationEnv) user(role, label string) int64 {
	e.t.Helper()
	username := fmt.Sprintf("test_cases_%s_%s", e.prefix, label)
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
	suffix       string
	status       string
	condition    *string
	assigned     *int64
	confidential bool
	reporterName string
	violation    string
	reviewDue    *time.Time
	conclusion   bool
	caseTaken    bool
}

func (e *integrationEnv) report(seed reportSeed) int64 {
	e.t.Helper()
	ctx := context.Background()
	caseNumber := e.prefix + seed.suffix

	var id int64
	err := e.pool.QueryRow(ctx, `
		INSERT INTO reports
			(case_number, pin_hash, channel, status, case_condition, reporter_is_victim,
			 chronology, assigned_to, received_at, review_due_at, reporter_confidential)
		VALUES ($1, 'x', 'HOTLINE', $2, $3, TRUE, 'Kronologi uji', $4, now(), $5, $6)
		RETURNING id`,
		caseNumber, seed.status, seed.condition, seed.assigned, seed.reviewDue, seed.confidential,
	).Scan(&id)
	if err != nil {
		e.t.Fatalf("buat laporan %s: %v", caseNumber, err)
	}

	reporterName := seed.reporterName
	if reporterName == "" {
		reporterName = "Pelapor-" + e.prefix
	}
	if _, err := e.pool.Exec(ctx, `
		INSERT INTO reporters (report_id, name, age, gender, category, institution, faculty, program, phone, email)
		VALUES ($1, $2, 21, 'PEREMPUAN', 'MAHASISWA', 'Telkom University', 'FRI', 'Informatika',
		        '+628123456789', $3)`,
		id, reporterName, "pelapor-"+e.prefix+"@test.local",
	); err != nil {
		e.t.Fatalf("buat pelapor: %v", err)
	}

	for i, partyType := range []string{"KORBAN", "TERLAPOR"} {
		if _, err := e.pool.Exec(ctx, `
			INSERT INTO parties (report_id, party_type, name, gender, category, institution)
			VALUES ($1, $2, $3, 'PEREMPUAN', 'MAHASISWA', 'Telkom University')`,
			id, partyType, fmt.Sprintf("%s-%s-%d", partyType, e.prefix, i),
		); err != nil {
			e.t.Fatalf("buat pihak: %v", err)
		}
	}

	if _, err := e.pool.Exec(ctx, `
		INSERT INTO incidents (report_id, date_start, location, description)
		VALUES ($1, CURRENT_DATE - 1, 'Kampus', 'Kejadian uji')`, id,
	); err != nil {
		e.t.Fatalf("buat kejadian: %v", err)
	}

	if _, err := e.pool.Exec(ctx, `
		INSERT INTO report_violation_types (report_id, violation_type_id)
		SELECT $1, id FROM violation_types WHERE code = $2`, id, seed.violation,
	); err != nil {
		e.t.Fatalf("buat jenis dugaan: %v", err)
	}

	if _, err := e.pool.Exec(ctx, `
		INSERT INTO evidences
			(report_id, category, original_name, stored_name, mime_type, size_bytes, uploaded_by)
		VALUES ($1, 'BUKTI', 'bukti.pdf', $2, 'application/pdf', 1024, $3)`,
		id, e.prefix+"-"+seed.suffix, e.satgasA,
	); err != nil {
		e.t.Fatalf("buat bukti: %v", err)
	}

	if _, err := e.pool.Exec(ctx, `
		INSERT INTO status_history (report_id, from_status, to_status, changed_by)
		VALUES ($1, NULL, $2, NULL)`, id, seed.status,
	); err != nil {
		e.t.Fatalf("buat riwayat status: %v", err)
	}

	if seed.conclusion {
		if _, err := e.pool.Exec(ctx, `
			INSERT INTO conclusions
				(report_id, result, summary, submission_status, submitted_at, created_by)
			VALUES ($1, 'TERBUKTI', 'Ringkasan kesimpulan uji', 'DIAJUKAN', now(), $2)`,
			id, e.satgasA,
		); err != nil {
			e.t.Fatalf("buat kesimpulan: %v", err)
		}
	}

	if seed.caseTaken {
		if _, err := e.pool.Exec(ctx, `
			INSERT INTO audit_logs (user_id, actor_type, action, entity, entity_id)
			VALUES ($1, 'USER', 'CASE_TAKEN', 'report', $2)`,
			e.satgasA, id,
		); err != nil {
			e.t.Fatalf("buat audit pengambilan: %v", err)
		}
	}

	return id
}

func strPtr(v string) *string { return &v }
func int64Ptr(v int64) *int64 { return &v }

func appErrStatus(t *testing.T, err error, want int) {
	t.Helper()
	var appErr *httpx.AppError
	if !errors.As(err, &appErr) {
		t.Fatalf("error bukan AppError: %v", err)
	}
	if appErr.Status != want {
		t.Fatalf("status AppError = %d, ingin %d", appErr.Status, want)
	}
}

func TestIntegrasiDaftarKasus(t *testing.T) {
	e := newIntegrationEnv(t)
	ctx := context.Background()
	due := time.Now().Add(24 * time.Hour)

	other := e.report(reportSeed{
		suffix: "01", status: "PENELAAHAN", condition: strPtr("DARURAT"),
		assigned: int64Ptr(e.satgasB), violation: "FISIK", reviewDue: &due,
	})
	confidential := e.report(reportSeed{
		suffix: "02", status: "DILAPORKAN", assigned: int64Ptr(e.satgasA),
		confidential: true, violation: "PERUNDUNGAN",
	})
	_ = confidential

	submitted := e.report(reportSeed{
		suffix: "03", status: "REKOMENDASI", assigned: int64Ptr(e.satgasA),
		violation: "PERUNDUNGAN", conclusion: true,
	})
	fresh := e.report(reportSeed{
		suffix: "04", status: "DILAPORKAN", violation: "SEKSUAL",
	})
	_ = confidential

	satgasA := cases.Actor{UserID: e.satgasA, Role: auth.RoleSatgas}

	t.Run("total, urutan darurat, dan proyeksi Satgas", func(t *testing.T) {
		page, err := e.svc.List(ctx, satgasA, cases.Filter{Query: e.prefix, Page: 1, PageSize: 10})
		if err != nil {
			t.Fatalf("List: %v", err)
		}
		if page.Total != 4 {
			t.Fatalf("total = %d, ingin 4", page.Total)
		}
		items, ok := page.Items.([]cases.SatgasListItem)
		if !ok {
			t.Fatalf("tipe items = %T", page.Items)
		}
		if len(items) != 4 {
			t.Fatalf("jumlah items = %d", len(items))
		}
		if items[0].ID != other {
			t.Errorf("kasus DARURAT tidak di urutan pertama: %+v", items[0])
		}
		if items[0].CaseCondition == nil || *items[0].CaseCondition != "DARURAT" {
			t.Errorf("case_condition tidak terbaca: %+v", items[0])
		}
		if items[0].Deadline == nil || items[0].Deadline.State != "MENDEKATI" {
			t.Errorf("tenggat penelaahan tidak dihitung: %+v", items[0].Deadline)
		}
	})

	t.Run("halaman kosong tetap melaporkan total", func(t *testing.T) {
		page, err := e.svc.List(ctx, satgasA, cases.Filter{Query: e.prefix, Page: 2, PageSize: 10})
		if err != nil {
			t.Fatalf("List halaman 2: %v", err)
		}
		if page.Total != 4 || len(page.Items.([]cases.SatgasListItem)) != 0 {
			t.Fatalf("halaman 2 = total %d, items %d", page.Total, len(page.Items.([]cases.SatgasListItem)))
		}
	})

	t.Run("filter status dan assigned", func(t *testing.T) {
		page, err := e.svc.List(ctx, satgasA, cases.Filter{
			Query: e.prefix, Status: "PENELAAHAN", Page: 1, PageSize: 10,
		})
		if err != nil {
			t.Fatalf("List status: %v", err)
		}
		if page.Total != 1 {
			t.Fatalf("filter status total = %d, ingin 1", page.Total)
		}

		page, err = e.svc.List(ctx, satgasA, cases.Filter{
			Query: e.prefix, Assigned: "me", Page: 1, PageSize: 10,
		})
		if err != nil {
			t.Fatalf("List assigned: %v", err)
		}
		if page.Total != 2 {
			t.Fatalf("filter assigned=me total = %d, ingin 2", page.Total)
		}
	})

	t.Run("needs_action hanya untuk Satgas", func(t *testing.T) {
		page, err := e.svc.List(ctx, satgasA, cases.Filter{
			Query: e.prefix, NeedsAction: true, Page: 1, PageSize: 10,
		})
		if err != nil {
			t.Fatalf("List needs_action: %v", err)
		}
		items := page.Items.([]cases.SatgasListItem)
		if len(items) != 1 || items[0].ID != fresh {
			t.Fatalf("needs_action = %+v, ingin hanya kasus %d", items, fresh)
		}

		_, err = e.svc.List(ctx, cases.Actor{UserID: e.pejabat, Role: auth.RolePejabat}, cases.Filter{
			Query: e.prefix, NeedsAction: true, Page: 1, PageSize: 10,
		})
		appErrStatus(t, err, 403)
	})

	t.Run("proyeksi Pejabat tanpa identitas pihak", func(t *testing.T) {
		page, err := e.svc.List(ctx, cases.Actor{UserID: e.pejabat, Role: auth.RolePejabat}, cases.Filter{
			Query: e.prefix, Page: 1, PageSize: 10,
		})
		if err != nil {
			t.Fatalf("List Pejabat: %v", err)
		}
		items, ok := page.Items.([]cases.OfficialListItem)
		if !ok {
			t.Fatalf("tipe items = %T", page.Items)
		}
		if len(items) != 4 {
			t.Fatalf("jumlah items = %d", len(items))
		}

		var reviewed bool
		for _, item := range items {
			if item.ID == submitted {
				reviewed = item.CanReview
			}
		}
		if !reviewed {
			t.Error("kasus yang kesimpulannya diajukan harus can_review")
		}

		raw, err := json.Marshal(items)
		if err != nil {
			t.Fatalf("marshal: %v", err)
		}
		body := string(raw)
		for _, forbidden := range []string{
			"Pelapor-" + e.prefix, "KORBAN-" + e.prefix, "TERLAPOR-" + e.prefix,
			`"reporter"`, `"parties"`, `"chronology"`,
		} {
			if strings.Contains(body, forbidden) {
				t.Errorf("daftar Pejabat membocorkan %q: %s", forbidden, body)
			}
		}
	})
}

func TestIntegrasiDetailKasus(t *testing.T) {
	e := newIntegrationEnv(t)
	ctx := context.Background()

	confidential := e.report(reportSeed{
		suffix: "11", status: "DILAPORKAN", assigned: int64Ptr(e.satgasA),
		confidential: true, reporterName: "Rahasia-" + e.prefix,
		violation: "PERUNDUNGAN", caseTaken: true,
	})
	submitted := e.report(reportSeed{
		suffix: "12", status: "REKOMENDASI", assigned: int64Ptr(e.satgasA),
		reporterName: "Pelapor-Umum-" + e.prefix, violation: "FISIK", conclusion: true,
	})

	t.Run("penyamaran pelapor untuk Satgas non-PJ", func(t *testing.T) {
		view, err := e.svc.Detail(ctx, cases.Actor{UserID: e.satgasB, Role: auth.RoleSatgas}, confidential, "203.0.113.5")
		if err != nil {
			t.Fatalf("Detail: %v", err)
		}
		detail := view.(*cases.SatgasDetail)
		if detail.Reporter == nil || !detail.Reporter.Masked {
			t.Fatalf("identitas pelapor tidak disamarkan: %+v", detail.Reporter)
		}
		if detail.Reporter.Name != "Dirahasiakan" || detail.Reporter.Phone != "" || detail.Reporter.Email != "" {
			t.Errorf("identitas masih bocor: %+v", detail.Reporter)
		}
		if detail.IsPJ {
			t.Error("Satgas non-PJ tidak boleh ditandai PJ")
		}
		if detail.CanTake {
			t.Error("kasus sudah punya PJ; can_take harus false")
		}

		raw, err := json.Marshal(detail)
		if err != nil {
			t.Fatalf("marshal: %v", err)
		}
		if strings.Contains(string(raw), `"stored_name"`) || strings.Contains(string(raw), e.prefix+"-11") {
			t.Error("object key bukti tidak boleh dikirim ke klien")
		}
	})

	t.Run("PJ melihat identitas lengkap", func(t *testing.T) {
		view, err := e.svc.Detail(ctx, cases.Actor{UserID: e.satgasA, Role: auth.RoleSatgas}, confidential, "")
		if err != nil {
			t.Fatalf("Detail PJ: %v", err)
		}
		detail := view.(*cases.SatgasDetail)
		if detail.Reporter == nil || detail.Reporter.Masked {
			t.Fatalf("PJ seharusnya melihat identitas asli: %+v", detail.Reporter)
		}
		if detail.Reporter.Name != "Rahasia-"+e.prefix {
			t.Errorf("nama pelapor = %q", detail.Reporter.Name)
		}
		if !detail.IsPJ || !detail.CanVerify {
			t.Errorf("flag PJ/verifikasi salah: is_pj=%v can_verify=%v", detail.IsPJ, detail.CanVerify)
		}
	})

	t.Run("Pejabat ditolak sebelum kesimpulan diajukan", func(t *testing.T) {
		_, err := e.svc.Detail(ctx, cases.Actor{UserID: e.pejabat, Role: auth.RolePejabat}, confidential, "")
		appErrStatus(t, err, 403)
	})

	t.Run("Pejabat hanya menerima proyeksi terbatas", func(t *testing.T) {
		view, err := e.svc.Detail(ctx, cases.Actor{UserID: e.pejabat, Role: auth.RolePejabat}, submitted, "")
		if err != nil {
			t.Fatalf("Detail Pejabat: %v", err)
		}
		detail := view.(*cases.OfficialDetail)
		if detail.Conclusion == nil || detail.Conclusion.Summary == "" {
			t.Fatalf("kesimpulan tidak ada: %+v", detail.Conclusion)
		}
		if !detail.CanReview {
			t.Error("can_review harus true")
		}

		raw, err := json.Marshal(detail)
		if err != nil {
			t.Fatalf("marshal: %v", err)
		}
		body := string(raw)
		for _, forbidden := range []string{
			"Pelapor-Umum-" + e.prefix,
			"KORBAN-" + e.prefix,
			"TERLAPOR-" + e.prefix,
			`"chronology"`, `"parties"`, `"reporter"`,
		} {
			if strings.Contains(body, forbidden) {
				t.Errorf("detail Pejabat membocorkan %q: %s", forbidden, body)
			}
		}
	})

	t.Run("riwayat menggabungkan status dan aksi", func(t *testing.T) {
		history, err := e.svc.History(ctx, cases.Actor{UserID: e.satgasA, Role: auth.RoleSatgas}, confidential)
		if err != nil {
			t.Fatalf("History: %v", err)
		}
		if len(history.Items) < 2 {
			t.Fatalf("riwayat terlalu pendek: %+v", history.Items)
		}
		var foundTaken, foundSubmitted bool
		for _, item := range history.Items {
			if item.Title == "Kasus diambil" {
				foundTaken = true
			}
			if item.Title == "Laporan diterima" {
				foundSubmitted = true
			}
			if item.Action == "STATUS_CHANGED" {
				t.Error("STATUS_CHANGED tidak boleh muncul sebagai aksi (duplikat status_history)")
			}
		}
		if !foundTaken || !foundSubmitted {
			t.Errorf("riwayat tidak lengkap: %+v", history.Items)
		}

		_, err = e.svc.History(ctx, cases.Actor{UserID: e.pejabat, Role: auth.RolePejabat}, confidential)
		appErrStatus(t, err, 403)
	})
}

func TestIntegrasiDetailTidakDitemukan(t *testing.T) {
	e := newIntegrationEnv(t)
	_, err := e.svc.Detail(context.Background(),
		cases.Actor{UserID: e.satgasA, Role: auth.RoleSatgas}, 9_999_999_999, "")
	appErrStatus(t, err, 404)
}
