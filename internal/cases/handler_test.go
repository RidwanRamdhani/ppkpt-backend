package cases

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/RidwanRamdhani/ppkpt-backend/internal/auth"
	"github.com/RidwanRamdhani/ppkpt-backend/internal/platform/httpx"
)

type fakeBackend struct {
	listCalled  bool
	listActor   Actor
	listFilter  Filter
	listResult  ListPage
	listErr     error
	detailID    int64
	detailErr   error
	historyID   int64
	historyErr  error
	createReq   ManualRequest
	createFiles int
	createErr   error
	takeID      int64
	takeErr     error
}

func (f *fakeBackend) List(_ context.Context, actor Actor, filter Filter) (ListPage, error) {
	f.listCalled = true
	f.listActor = actor
	f.listFilter = filter
	return f.listResult, f.listErr
}

func (f *fakeBackend) Detail(_ context.Context, _ Actor, reportID int64, _ string) (any, error) {
	f.detailID = reportID
	if f.detailErr != nil {
		return nil, f.detailErr
	}
	return map[string]any{"id": reportID}, nil
}

func (f *fakeBackend) History(_ context.Context, _ Actor, reportID int64) (HistoryResponse, error) {
	f.historyID = reportID
	if f.historyErr != nil {
		return HistoryResponse{}, f.historyErr
	}
	return HistoryResponse{Items: []HistoryEntry{}}, nil
}

func (f *fakeBackend) CreateManual(_ context.Context, _ Actor, req ManualRequest, files []*multipart.FileHeader, _ string) (CreateResult, error) {
	f.createReq = req
	f.createFiles = len(files)
	if f.createErr != nil {
		return CreateResult{}, f.createErr
	}
	return CreateResult{CaseNumber: "12345678", PIN: "123456"}, nil
}

func (f *fakeBackend) Take(_ context.Context, _ Actor, reportID int64, _ string) (TakeResult, error) {
	f.takeID = reportID
	if f.takeErr != nil {
		return TakeResult{}, f.takeErr
	}
	return TakeResult{AssignedTo: Assignee{ID: 7, Name: "Satgas Uji"}}, nil
}

func newTestHandler(backend Backend) *Handler {
	return NewHandler(backend, slog.New(slog.NewTextHandler(io.Discard, nil)), 0)
}

func authenticatedRequest(method, target string) *http.Request {
	req := httptest.NewRequest(method, target, nil)
	principal := &auth.Principal{UserID: 7, Role: auth.RoleSatgas}
	return req.WithContext(auth.WithPrincipal(req.Context(), principal))
}

func TestHandlerListMenolakTanpaPrincipal(t *testing.T) {
	h := newTestHandler(&fakeBackend{})
	rec := httptest.NewRecorder()

	h.List(rec, httptest.NewRequest(http.MethodGet, "/api/cases", nil))

	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, ingin 401", rec.Code)
	}
}

func TestHandlerListMemvalidasiFilter(t *testing.T) {
	backend := &fakeBackend{}
	h := newTestHandler(backend)
	rec := httptest.NewRecorder()

	h.List(rec, authenticatedRequest(http.MethodGet, "/api/cases?page=0"))

	if rec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("status = %d (%s), ingin 422", rec.Code, rec.Body.String())
	}
	if backend.listCalled {
		t.Fatal("backend tidak boleh dipanggil saat filter tidak valid")
	}
	if !strings.Contains(rec.Body.String(), `"page"`) {
		t.Fatalf("body 422 tanpa field page: %s", rec.Body.String())
	}
}

func TestHandlerListMeneruskanAktorDanFilter(t *testing.T) {
	backend := &fakeBackend{listResult: ListPage{
		Items:    []SatgasListItem{{ID: 1, CaseNumber: "12345678"}},
		Page:     2,
		PageSize: 10,
		Total:    1,
	}}
	h := newTestHandler(backend)
	rec := httptest.NewRecorder()

	h.List(rec, authenticatedRequest(http.MethodGet, "/api/cases?page=2&page_size=10&status=penelaahan"))

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d (%s), ingin 200", rec.Code, rec.Body.String())
	}
	if !backend.listCalled || backend.listActor.UserID != 7 || backend.listActor.Role != auth.RoleSatgas {
		t.Fatalf("aktor tidak diteruskan: %+v", backend.listActor)
	}
	if backend.listFilter.Page != 2 || backend.listFilter.PageSize != 10 || backend.listFilter.Status != "PENELAAHAN" {
		t.Fatalf("filter tidak diteruskan: %+v", backend.listFilter)
	}

	var page ListPage
	if err := json.Unmarshal(rec.Body.Bytes(), &page); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if page.Total != 1 {
		t.Fatalf("total = %d", page.Total)
	}
}

func TestHandlerDetailIDTidakValid(t *testing.T) {
	h := newTestHandler(&fakeBackend{})
	for _, id := range []string{"abc", "0", "-1"} {
		req := authenticatedRequest(http.MethodGet, "/api/cases/"+id)
		req.SetPathValue("id", id)
		rec := httptest.NewRecorder()

		h.Detail(rec, req)
		if rec.Code != http.StatusNotFound {
			t.Errorf("id %q: status = %d, ingin 404", id, rec.Code)
		}
	}
}

func TestHandlerDetailMeneruskanID(t *testing.T) {
	backend := &fakeBackend{}
	h := newTestHandler(backend)
	req := authenticatedRequest(http.MethodGet, "/api/cases/42")
	req.SetPathValue("id", "42")
	rec := httptest.NewRecorder()

	h.Detail(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d (%s), ingin 200", rec.Code, rec.Body.String())
	}
	if backend.detailID != 42 {
		t.Fatalf("id diteruskan = %d, ingin 42", backend.detailID)
	}
}

func TestHandlerErrorLayananDipetakan(t *testing.T) {
	backend := &fakeBackend{detailErr: httpx.ErrForbidden()}
	h := newTestHandler(backend)
	req := authenticatedRequest(http.MethodGet, "/api/cases/42")
	req.SetPathValue("id", "42")
	rec := httptest.NewRecorder()

	h.Detail(rec, req)

	if rec.Code != http.StatusForbidden {
		t.Fatalf("status = %d (%s), ingin 403", rec.Code, rec.Body.String())
	}
}

func TestHandlerHistoryMeneruskanID(t *testing.T) {
	backend := &fakeBackend{}
	h := newTestHandler(backend)
	req := authenticatedRequest(http.MethodGet, "/api/cases/9/history")
	req.SetPathValue("id", "9")
	rec := httptest.NewRecorder()

	h.History(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d (%s), ingin 200", rec.Code, rec.Body.String())
	}
	if backend.historyID != 9 {
		t.Fatalf("id = %d, ingin 9", backend.historyID)
	}
	if strings.TrimSpace(rec.Body.String()) != `{"items":[]}` {
		t.Fatalf("body = %s", rec.Body.String())
	}
}

func TestHandlerHistoryError(t *testing.T) {
	backend := &fakeBackend{historyErr: httpx.ErrNotFound()}
	h := newTestHandler(backend)
	req := authenticatedRequest(http.MethodGet, "/api/cases/9/history")
	req.SetPathValue("id", "9")
	rec := httptest.NewRecorder()

	h.History(rec, req)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, ingin 404", rec.Code)
	}
}

func TestPathReportIDMengembalikanErrorNotFound(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/api/cases/0", nil)
	req.SetPathValue("id", "0")
	rec := httptest.NewRecorder()

	if _, ok := pathReportID(rec, req); ok {
		t.Fatal("id 0 seharusnya ditolak")
	}
	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, ingin 404", rec.Code)
	}
}

// manualBody membuat body multipart dengan bagian payload.
func manualBody(t *testing.T, payload string) (*bytes.Buffer, string) {
	t.Helper()
	var buf bytes.Buffer
	writer := multipart.NewWriter(&buf)
	if err := writer.WriteField("payload", payload); err != nil {
		t.Fatalf("tulis payload: %v", err)
	}
	if err := writer.Close(); err != nil {
		t.Fatalf("tutup multipart: %v", err)
	}
	return &buf, writer.FormDataContentType()
}

func TestHandlerCreateManual(t *testing.T) {
	backend := &fakeBackend{}
	h := newTestHandler(backend)
	body, contentType := manualBody(t, `{"channel":"HOTLINE","reporter":{"name":"Pelapor"}}`)

	req := httptest.NewRequest(http.MethodPost, "/api/cases", body)
	req.Header.Set("Content-Type", contentType)
	principal := &auth.Principal{UserID: 7, Role: auth.RoleSatgas}
	req = req.WithContext(auth.WithPrincipal(req.Context(), principal))
	rec := httptest.NewRecorder()

	h.CreateManual(rec, req)

	if rec.Code != http.StatusCreated {
		t.Fatalf("status = %d (%s), ingin 201", rec.Code, rec.Body.String())
	}
	if backend.createReq.Channel != "HOTLINE" || backend.createReq.Reporter.Name != "Pelapor" {
		t.Fatalf("payload tidak diteruskan: %+v", backend.createReq)
	}
	if !strings.Contains(rec.Body.String(), `"case_number":"12345678"`) {
		t.Fatalf("body = %s", rec.Body.String())
	}
}

func TestHandlerCreateManualPayloadTidakValid(t *testing.T) {
	h := newTestHandler(&fakeBackend{})
	body, contentType := manualBody(t, `bukan json`)

	req := httptest.NewRequest(http.MethodPost, "/api/cases", body)
	req.Header.Set("Content-Type", contentType)
	principal := &auth.Principal{UserID: 7, Role: auth.RoleSatgas}
	req = req.WithContext(auth.WithPrincipal(req.Context(), principal))
	rec := httptest.NewRecorder()

	h.CreateManual(rec, req)

	if rec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("status = %d (%s), ingin 422", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), `"payload"`) {
		t.Fatalf("body 422 tanpa field payload: %s", rec.Body.String())
	}
}

func TestHandlerCreateManualMenolakFieldTakDikenal(t *testing.T) {
	backend := &fakeBackend{}
	h := newTestHandler(backend)
	body, contentType := manualBody(t, `{"channel":"HOTLINE","rahasia":true}`)

	req := httptest.NewRequest(http.MethodPost, "/api/cases", body)
	req.Header.Set("Content-Type", contentType)
	principal := &auth.Principal{UserID: 7, Role: auth.RoleSatgas}
	req = req.WithContext(auth.WithPrincipal(req.Context(), principal))
	rec := httptest.NewRecorder()

	h.CreateManual(rec, req)

	if rec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("status = %d, ingin 422", rec.Code)
	}
}

func TestHandlerTake(t *testing.T) {
	backend := &fakeBackend{}
	h := newTestHandler(backend)
	req := authenticatedRequest(http.MethodPost, "/api/cases/12/take")
	req.SetPathValue("id", "12")
	rec := httptest.NewRecorder()

	h.Take(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d (%s), ingin 200", rec.Code, rec.Body.String())
	}
	if backend.takeID != 12 {
		t.Fatalf("id = %d, ingin 12", backend.takeID)
	}
}

func TestHandlerTakeKonflik(t *testing.T) {
	backend := &fakeBackend{takeErr: httpx.ErrConflict("Kasus sudah diambil oleh petugas lain.")}
	h := newTestHandler(backend)
	req := authenticatedRequest(http.MethodPost, "/api/cases/12/take")
	req.SetPathValue("id", "12")
	rec := httptest.NewRecorder()

	h.Take(rec, req)

	if rec.Code != http.StatusConflict {
		t.Fatalf("status = %d (%s), ingin 409", rec.Code, rec.Body.String())
	}
}
