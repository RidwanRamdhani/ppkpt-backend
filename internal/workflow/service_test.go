package workflow_test

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/RidwanRamdhani/ppkpt-backend/internal/audit"
	"github.com/RidwanRamdhani/ppkpt-backend/internal/platform/httpx"
	"github.com/RidwanRamdhani/ppkpt-backend/internal/workflow"
	"github.com/jackc/pgx/v5/pgconn"
)

type execCall struct {
	sql  string
	args []any
}

// fakeDB mencatat setiap Exec agar SQL, argumen, dan urutannya bisa diperiksa
// tanpa database sungguhan.
type fakeDB struct {
	calls []execCall
	rows  int64
	err   error
}

func (f *fakeDB) Exec(_ context.Context, sql string, args ...any) (pgconn.CommandTag, error) {
	f.calls = append(f.calls, execCall{sql: sql, args: args})
	if f.err != nil {
		return pgconn.CommandTag{}, f.err
	}
	return pgconn.NewCommandTag(fmt.Sprintf("UPDATE %d", f.rows)), nil
}

type fakeAuditor struct {
	events []audit.Event
	err    error
}

func (f *fakeAuditor) Log(_ context.Context, _ audit.DBTX, e audit.Event) error {
	f.events = append(f.events, e)
	return f.err
}

func appError(t *testing.T, err error) *httpx.AppError {
	t.Helper()
	var appErr *httpx.AppError
	if !errors.As(err, &appErr) {
		t.Fatalf("error = %v (%T), ingin *httpx.AppError", err, err)
	}
	return appErr
}

// TestTransitionSuccess memeriksa urutan langkah: UPDATE status + tenggat,
// INSERT status_history, lalu audit STATUS_CHANGED — semuanya dengan argumen
// yang benar dan tenggat default 7/30 hari.
func TestTransitionSuccess(t *testing.T) {
	db := &fakeDB{rows: 1}
	aud := &fakeAuditor{}
	svc := workflow.NewService(aud, workflow.Options{})
	by := int64(42)

	err := svc.Transition(context.Background(), db, 10,
		workflow.Penelaahan, workflow.Pemeriksaan, &by)
	if err != nil {
		t.Fatalf("Transition: %v", err)
	}

	if len(db.calls) != 2 {
		t.Fatalf("jumlah Exec = %d, ingin 2 (UPDATE + status_history)", len(db.calls))
	}

	update := db.calls[0]
	if !strings.Contains(update.sql, "UPDATE reports") {
		t.Fatalf("Exec pertama bukan UPDATE reports:\n%s", update.sql)
	}
	for _, fragment := range []string{"exam_started_at", "exam_due_at", "make_interval"} {
		if !strings.Contains(update.sql, fragment) {
			t.Errorf("SQL UPDATE tidak memuat %q", fragment)
		}
	}
	// Tahap lain tidak boleh ikut mengisi tenggat penelaahan/penutupan.
	for _, fragment := range []string{"review_due_at", "closed_at"} {
		if strings.Contains(update.sql, fragment) {
			t.Errorf("SQL UPDATE untuk PEMERIKSAAN tidak boleh memuat %q", fragment)
		}
	}
	if len(update.args) != 4 {
		t.Fatalf("argumen UPDATE = %v, ingin 4", update.args)
	}
	if update.args[0] != int64(10) {
		t.Errorf("argumen id = %v, ingin 10", update.args[0])
	}
	if update.args[1] != "PENELAAHAN" || update.args[2] != "PEMERIKSAAN" {
		t.Errorf("argumen status = %v -> %v, ingin PENELAAHAN -> PEMERIKSAAN", update.args[1], update.args[2])
	}
	if update.args[3] != 30 {
		t.Errorf("exam due days = %v, ingin 30", update.args[3])
	}

	history := db.calls[1]
	if !strings.Contains(history.sql, "INSERT INTO status_history") {
		t.Fatalf("Exec kedua bukan INSERT status_history:\n%s", history.sql)
	}
	if len(history.args) != 4 {
		t.Fatalf("argumen status_history = %v, ingin 4", history.args)
	}
	if history.args[0] != int64(10) || history.args[1] != "PENELAAHAN" || history.args[2] != "PEMERIKSAAN" {
		t.Errorf("argumen status_history = %v", history.args)
	}
	gotBy, ok := history.args[3].(*int64)
	if !ok || gotBy == nil || *gotBy != by {
		t.Errorf("changed_by = %v, ingin %d", history.args[3], by)
	}

	if len(aud.events) != 1 {
		t.Fatalf("jumlah audit = %d, ingin 1", len(aud.events))
	}
	event := aud.events[0]
	if event.Action != audit.ActionStatusChanged {
		t.Errorf("action = %q, ingin %q", event.Action, audit.ActionStatusChanged)
	}
	if event.ActorType != audit.ActorUser || event.UserID == nil || *event.UserID != by {
		t.Errorf("aktor audit = %+v, ingin USER dengan user_id %d", event, by)
	}
	if event.Entity != "report" || event.EntityID == nil || *event.EntityID != 10 {
		t.Errorf("entitas audit = %q/%v, ingin report/10", event.Entity, event.EntityID)
	}
	if event.Detail["from"] != "PENELAAHAN" || event.Detail["to"] != "PEMERIKSAAN" {
		t.Errorf("detail audit = %v", event.Detail)
	}
}

// TestTransitionReporterActor memastikan pelapor (by nil) tercatat sebagai
// REPORTER tanpa user_id, dan changed_by disimpan NULL.
func TestTransitionReporterActor(t *testing.T) {
	db := &fakeDB{rows: 1}
	aud := &fakeAuditor{}
	svc := workflow.NewService(aud, workflow.Options{})

	err := svc.Transition(context.Background(), db, 7,
		workflow.PerluDilengkapi, workflow.Dilaporkan, nil)
	if err != nil {
		t.Fatalf("Transition: %v", err)
	}

	history := db.calls[1]
	if p, ok := history.args[3].(*int64); !ok || p != nil {
		t.Errorf("changed_by = %v, ingin nil", history.args[3])
	}

	event := aud.events[0]
	if event.ActorType != audit.ActorReporter || event.UserID != nil {
		t.Errorf("aktor audit = %+v, ingin REPORTER tanpa user_id", event)
	}
}

// TestTransitionUsesConfiguredDeadline memastikan nilai konfigurasi dipakai,
// bukan hanya default, dan hanya tenggat tahap tujuan yang diisi.
func TestTransitionUsesConfiguredDeadline(t *testing.T) {
	db := &fakeDB{rows: 1}
	svc := workflow.NewService(&fakeAuditor{}, workflow.Options{ReviewDueDays: 3, ExamDueDays: 10})

	if err := svc.Transition(context.Background(), db, 1,
		workflow.Dilaporkan, workflow.Penelaahan, nil); err != nil {
		t.Fatalf("Transition: %v", err)
	}
	update := db.calls[0]
	if !strings.Contains(update.sql, "review_due_at") || !strings.Contains(update.sql, "make_interval") {
		t.Errorf("SQL penelaahan tidak memasang review_due_at:\n%s", update.sql)
	}
	if len(update.args) != 4 || update.args[3] != 3 {
		t.Errorf("argumen tenggat = %v, ingin 4 argumen dengan review due 3 hari", update.args)
	}
}

// TestTransitionSetsClosedAt memastikan penutupan hanya mengisi closed_at dan
// tidak menyentuh tenggat penelaahan/pemeriksaan.
func TestTransitionSetsClosedAt(t *testing.T) {
	db := &fakeDB{rows: 1}
	svc := workflow.NewService(&fakeAuditor{}, workflow.Options{})

	if err := svc.Transition(context.Background(), db, 1,
		workflow.Keputusan, workflow.Selesai, nil); err != nil {
		t.Fatalf("Transition: %v", err)
	}
	update := db.calls[0]
	if !strings.Contains(update.sql, "closed_at = now()") {
		t.Errorf("SQL penutupan tidak mengisi closed_at:\n%s", update.sql)
	}
	if strings.Contains(update.sql, "make_interval") || strings.Contains(update.sql, "review_due_at") || strings.Contains(update.sql, "exam_due_at") {
		t.Errorf("SQL penutupan tidak boleh menyentuh tenggat:\n%s", update.sql)
	}
	if len(update.args) != 3 {
		t.Errorf("argumen = %v, ingin 3 (tanpa parameter tenggat)", update.args)
	}
}

// TestTransitionRejectsInvalid memastikan perpindahan yang tidak ada di state
// machine ditolak 409 tanpa menyentuh database maupun audit.
func TestTransitionRejectsInvalid(t *testing.T) {
	db := &fakeDB{rows: 1}
	aud := &fakeAuditor{}
	svc := workflow.NewService(aud, workflow.Options{})

	err := svc.Transition(context.Background(), db, 1, workflow.Dilaporkan, workflow.Selesai, nil)
	if got := appError(t, err).Status; got != 409 {
		t.Fatalf("status = %d, ingin 409", got)
	}
	if len(db.calls) != 0 || len(aud.events) != 0 {
		t.Fatalf("perubahan tidak sah tetap menyentuh DB/audit (exec=%d, audit=%d)",
			len(db.calls), len(aud.events))
	}
}

// TestTransitionRejectsConcurrentChange meniru dua permintaan bersamaan:
// UPDATE tidak menemukan baris dengan status lama sehingga harus 409 dan
// langkah berikutnya tidak dijalankan.
func TestTransitionRejectsConcurrentChange(t *testing.T) {
	db := &fakeDB{rows: 0}
	aud := &fakeAuditor{}
	svc := workflow.NewService(aud, workflow.Options{})

	err := svc.Transition(context.Background(), db, 1, workflow.Dilaporkan, workflow.Penelaahan, nil)
	if got := appError(t, err).Status; got != 409 {
		t.Fatalf("status = %d, ingin 409", got)
	}
	if len(db.calls) != 1 {
		t.Fatalf("jumlah Exec = %d, ingin 1 (hanya UPDATE)", len(db.calls))
	}
	if len(aud.events) != 0 {
		t.Fatalf("audit ditulis padahal transisi gagal")
	}
}

// TestTransitionPropagatesAuditError memastikan kegagalan audit membatalkan
// transisi (transaksi pemanggil akan rollback).
func TestTransitionPropagatesAuditError(t *testing.T) {
	boom := errors.New("audit mati")
	db := &fakeDB{rows: 1}
	svc := workflow.NewService(&fakeAuditor{err: boom}, workflow.Options{})

	err := svc.Transition(context.Background(), db, 1, workflow.Penelaahan, workflow.Pemeriksaan, nil)
	if !errors.Is(err, boom) {
		t.Fatalf("error = %v, ingin membungkus %v", err, boom)
	}
}

// TestTransitionRejectsUnknownStatus memastikan status di luar enum adalah bug
// pemanggil dan tidak pernah dikirim ke database.
func TestTransitionRejectsUnknownStatus(t *testing.T) {
	db := &fakeDB{rows: 1}
	svc := workflow.NewService(&fakeAuditor{}, workflow.Options{})

	cases := []struct {
		from, to workflow.Status
	}{
		{workflow.Status("ASING"), workflow.Dilaporkan},
		{workflow.Dilaporkan, workflow.Status("ASING")},
	}
	for _, tc := range cases {
		if err := svc.Transition(context.Background(), db, 1, tc.from, tc.to, nil); err == nil {
			t.Errorf("Transition(%q, %q) = nil, ingin error", tc.from, tc.to)
		}
	}
	if len(db.calls) != 0 {
		t.Fatalf("status asing tetap dikirim ke database")
	}
}

// TestTransitionRejectsInvalidReportID memastikan id tidak valid tidak pernah
// masuk ke query.
func TestTransitionRejectsInvalidReportID(t *testing.T) {
	db := &fakeDB{rows: 1}
	svc := workflow.NewService(&fakeAuditor{}, workflow.Options{})

	if err := svc.Transition(context.Background(), db, 0, workflow.Dilaporkan, workflow.Penelaahan, nil); err == nil {
		t.Fatal("Transition dengan id 0 = nil, ingin error")
	}
	if len(db.calls) != 0 {
		t.Fatal("id tidak valid tetap dikirim ke database")
	}
}
