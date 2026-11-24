package review

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
	"time"

	"github.com/RidwanRamdhani/ppkpt-backend/internal/auth"
	"github.com/RidwanRamdhani/ppkpt-backend/internal/platform/httpx"
)

type fakeBackend struct {
	saveID      int64
	saveReq     ReviewRequest
	saveErr     error
	continueID  int64
	continueErr error
	closeID     int64
	closeReq    CloseRequest
	closeErr    error
	sessionID   int64
	sessionReq  SessionRequest
	sessionErr  error
	uploadID    int64
	uploadCat   string
	uploadFiles int
	uploadErr   error
}

func (f *fakeBackend) SaveReview(_ context.Context, _ Actor, reportID int64, req ReviewRequest, _ string) (ReviewView, error) {
	f.saveID = reportID
	f.saveReq = req
	if f.saveErr != nil {
		return ReviewView{}, f.saveErr
	}
	return ReviewView{VictimIdentified: req.VictimIdentified}, nil
}

func (f *fakeBackend) ContinueReview(_ context.Context, _ Actor, reportID int64, _ string) (ContinueResult, error) {
	f.continueID = reportID
	if f.continueErr != nil {
		return ContinueResult{}, f.continueErr
	}
	due := time.Date(2026, time.December, 20, 0, 0, 0, 0, time.UTC)
	return ContinueResult{Status: "PEMERIKSAAN", ExamDueAt: &due}, nil
}

func (f *fakeBackend) CloseReview(_ context.Context, _ Actor, reportID int64, req CloseRequest, _ string) (CloseResult, error) {
	f.closeID = reportID
	f.closeReq = req
	if f.closeErr != nil {
		return CloseResult{}, f.closeErr
	}
	return CloseResult{Status: "SELESAI"}, nil
}

func (f *fakeBackend) CreateSession(_ context.Context, _ Actor, reportID int64, req SessionRequest, _ string) (SessionView, error) {
	f.sessionID = reportID
	f.sessionReq = req
	if f.sessionErr != nil {
		return SessionView{}, f.sessionErr
	}
	return SessionView{ID: 3, SessionDate: req.SessionDate}, nil
}

func (f *fakeBackend) UploadEvidence(_ context.Context, _ Actor, reportID int64, category string, files []*multipart.FileHeader, _ string) (UploadResult, error) {
	f.uploadID = reportID
	f.uploadCat = category
	f.uploadFiles = len(files)
	if f.uploadErr != nil {
		return UploadResult{}, f.uploadErr
	}
	return UploadResult{Category: category, Uploaded: len(files)}, nil
}

func newTestHandler(backend Backend) *Handler {
	return NewHandler(backend, slog.New(slog.NewTextHandler(io.Discard, nil)), 0)
}

func authenticatedRequest(method, target string, body io.Reader) *http.Request {
	req := httptest.NewRequest(method, target, body)
	principal := &auth.Principal{UserID: 7, Role: auth.RoleSatgas}
	return req.WithContext(auth.WithPrincipal(req.Context(), principal))
}

func jsonRequest(method, target, body string) *http.Request {
	req := authenticatedRequest(method, target, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	return req
}

func TestHandlerSaveReview(t *testing.T) {
	backend := &fakeBackend{}
	h := newTestHandler(backend)
	req := jsonRequest(http.MethodPost, "/api/cases/42/review",
		`{"victim_identified":"Korban dikenali"}`)
	req.SetPathValue("id", "42")
	rec := httptest.NewRecorder()

	h.SaveReview(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d (%s), ingin 200", rec.Code, rec.Body.String())
	}
	if backend.saveID != 42 || backend.saveReq.VictimIdentified != "Korban dikenali" {
		t.Fatalf("backend menerima id=%d req=%+v", backend.saveID, backend.saveReq)
	}
}

func TestHandlerSaveReviewTanpaPrincipal(t *testing.T) {
	h := newTestHandler(&fakeBackend{})
	req := httptest.NewRequest(http.MethodPost, "/api/cases/42/review", strings.NewReader("{}"))
	req.SetPathValue("id", "42")
	rec := httptest.NewRecorder()

	h.SaveReview(rec, req)

	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, ingin 401", rec.Code)
	}
}

func TestHandlerSaveReviewIDTidakValid(t *testing.T) {
	h := newTestHandler(&fakeBackend{})
	for _, id := range []string{"abc", "0", "-1"} {
		req := jsonRequest(http.MethodPost, "/api/cases/"+id+"/review", `{}`)
		req.SetPathValue("id", id)
		rec := httptest.NewRecorder()

		h.SaveReview(rec, req)
		if rec.Code != http.StatusNotFound {
			t.Errorf("id %q: status = %d, ingin 404", id, rec.Code)
		}
	}
}

func TestHandlerSaveReviewJSONTidakValid(t *testing.T) {
	h := newTestHandler(&fakeBackend{})
	req := jsonRequest(http.MethodPost, "/api/cases/42/review", `{`)
	req.SetPathValue("id", "42")
	rec := httptest.NewRecorder()

	h.SaveReview(rec, req)

	if rec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("status = %d (%s), ingin 422", rec.Code, rec.Body.String())
	}
}

func TestHandlerContinueReview(t *testing.T) {
	backend := &fakeBackend{}
	h := newTestHandler(backend)
	req := authenticatedRequest(http.MethodPost, "/api/cases/7/review/continue", nil)
	req.SetPathValue("id", "7")
	rec := httptest.NewRecorder()

	h.ContinueReview(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d (%s), ingin 200", rec.Code, rec.Body.String())
	}
	if backend.continueID != 7 {
		t.Fatalf("id = %d, ingin 7", backend.continueID)
	}
	var result ContinueResult
	if err := json.Unmarshal(rec.Body.Bytes(), &result); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if result.Status != "PEMERIKSAAN" {
		t.Fatalf("status = %q", result.Status)
	}
}

func TestHandlerCloseReview(t *testing.T) {
	backend := &fakeBackend{}
	h := newTestHandler(backend)
	req := jsonRequest(http.MethodPost, "/api/cases/9/review/close", `{"reason":"Bukan ranah PPKPT"}`)
	req.SetPathValue("id", "9")
	rec := httptest.NewRecorder()

	h.CloseReview(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d (%s), ingin 200", rec.Code, rec.Body.String())
	}
	if backend.closeID != 9 || backend.closeReq.Reason != "Bukan ranah PPKPT" {
		t.Fatalf("backend menerima id=%d req=%+v", backend.closeID, backend.closeReq)
	}
}

func TestHandlerCreateSession(t *testing.T) {
	backend := &fakeBackend{}
	h := newTestHandler(backend)
	req := jsonRequest(http.MethodPost, "/api/cases/5/sessions",
		`{"session_date":"2026-11-20","summary":"Wawancara","participants":[{"party_id":1,"role":"KORBAN"}]}`)
	req.SetPathValue("id", "5")
	rec := httptest.NewRecorder()

	h.CreateSession(rec, req)

	if rec.Code != http.StatusCreated {
		t.Fatalf("status = %d (%s), ingin 201", rec.Code, rec.Body.String())
	}
	if backend.sessionID != 5 || len(backend.sessionReq.Participants) != 1 {
		t.Fatalf("backend menerima id=%d req=%+v", backend.sessionID, backend.sessionReq)
	}
}

func TestHandlerUploadEvidence(t *testing.T) {
	backend := &fakeBackend{}
	h := newTestHandler(backend)
	req := multipartRequest(t, "BAI")
	req.SetPathValue("id", "11")
	rec := httptest.NewRecorder()

	h.UploadEvidence(rec, req)

	if rec.Code != http.StatusCreated {
		t.Fatalf("status = %d (%s), ingin 201", rec.Code, rec.Body.String())
	}
	if backend.uploadID != 11 || backend.uploadCat != "BAI" || backend.uploadFiles != 1 {
		t.Fatalf("backend menerima id=%d category=%q files=%d",
			backend.uploadID, backend.uploadCat, backend.uploadFiles)
	}
}

func TestHandlerUploadEvidenceMultipartTidakValid(t *testing.T) {
	h := newTestHandler(&fakeBackend{})
	req := authenticatedRequest(http.MethodPost, "/api/cases/11/evidences", strings.NewReader("bukan multipart"))
	req.Header.Set("Content-Type", "multipart/form-data; boundary=abc")
	req.SetPathValue("id", "11")
	rec := httptest.NewRecorder()

	h.UploadEvidence(rec, req)

	if rec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("status = %d (%s), ingin 422", rec.Code, rec.Body.String())
	}
}

func TestHandlerErrorLayananDipetakan(t *testing.T) {
	backend := &fakeBackend{saveErr: httpx.ErrConflict("tidak boleh")}
	h := newTestHandler(backend)
	req := jsonRequest(http.MethodPost, "/api/cases/42/review", `{"victim_identified":"x"}`)
	req.SetPathValue("id", "42")
	rec := httptest.NewRecorder()

	h.SaveReview(rec, req)

	if rec.Code != http.StatusConflict {
		t.Fatalf("status = %d (%s), ingin 409", rec.Code, rec.Body.String())
	}
}

// multipartRequest membangun permintaan multipart dengan satu berkas PDF.
func multipartRequest(t *testing.T, category string) *http.Request {
	t.Helper()

	var buf bytes.Buffer
	writer := multipart.NewWriter(&buf)
	if err := writer.WriteField("category", category); err != nil {
		t.Fatalf("tulis category: %v", err)
	}
	part, err := writer.CreateFormFile("evidences", "bai.pdf")
	if err != nil {
		t.Fatalf("buat bagian berkas: %v", err)
	}
	if _, err := part.Write([]byte("%PDF-1.4 isi berkas uji")); err != nil {
		t.Fatalf("tulis berkas: %v", err)
	}
	if err := writer.Close(); err != nil {
		t.Fatalf("tutup writer: %v", err)
	}

	req := authenticatedRequest(http.MethodPost, "/api/cases/11/evidences", &buf)
	req.Header.Set("Content-Type", writer.FormDataContentType())
	return req
}
