package report_test

import (
	"bytes"
	"context"
	"encoding/json"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/RidwanRamdhani/ppkpt-backend/internal/platform/captcha"
	"github.com/RidwanRamdhani/ppkpt-backend/internal/platform/httpx"
	"github.com/RidwanRamdhani/ppkpt-backend/internal/report"
)

type fakeBackend struct {
	calls  int
	req    report.SubmitRequest
	files  []*multipart.FileHeader
	ip     string
	result report.Result
	err    error

	verifyCalls  int
	verifyReport report.Report
	verifyErr    error
	lastTicket   string
	lastPIN      string

	statusCalls int
	statusView  report.StatusView
	statusErr   error
	statusIP    string

	completeCalls int
	completeText  string
	completeFiles []*multipart.FileHeader
	completeIP    string
	completeReply report.Completion
	completeErr   error
}

func (f *fakeBackend) Submit(_ context.Context, req report.SubmitRequest, files []*multipart.FileHeader, ip string) (report.Result, error) {
	f.calls++
	f.req = req
	f.files = files
	f.ip = ip
	return f.result, f.err
}

func (f *fakeBackend) VerifyPIN(_ context.Context, ticket, pin string) (report.Report, error) {
	f.verifyCalls++
	f.lastTicket = ticket
	f.lastPIN = pin
	return f.verifyReport, f.verifyErr
}

func (f *fakeBackend) Status(_ context.Context, _ report.Report, ip string) (report.StatusView, error) {
	f.statusCalls++
	f.statusIP = ip
	return f.statusView, f.statusErr
}

func (f *fakeBackend) Complete(_ context.Context, _ report.Report, text string, files []*multipart.FileHeader, ip string) (report.Completion, error) {
	f.completeCalls++
	f.completeText = text
	f.completeFiles = files
	f.completeIP = ip
	return f.completeReply, f.completeErr
}

type fakeCaptcha struct {
	err   error
	token string
}

func (f *fakeCaptcha) Verify(_ context.Context, token, _ string) error {
	f.token = token
	return f.err
}

type fakeLimiter struct {
	allow bool
	key   string
}

func (f *fakeLimiter) Allow(key string) bool {
	f.key = key
	return f.allow
}

type upload struct {
	name    string
	content []byte
}

func newMultipartRequest(t *testing.T, payload, captchaToken string, files []upload) *http.Request {
	t.Helper()
	var buf bytes.Buffer
	w := multipart.NewWriter(&buf)

	if payload != "" {
		if err := w.WriteField("payload", payload); err != nil {
			t.Fatalf("tulis payload: %v", err)
		}
	}
	if captchaToken != "" {
		if err := w.WriteField("captcha_token", captchaToken); err != nil {
			t.Fatalf("tulis captcha: %v", err)
		}
	}
	for _, f := range files {
		part, err := w.CreateFormFile("evidences", f.name)
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

	req := httptest.NewRequest(http.MethodPost, "/api/reports", &buf)
	req.Header.Set("Content-Type", w.FormDataContentType())
	req.RemoteAddr = "198.51.100.7:4321"
	return req
}

type errorEnvelope struct {
	Error struct {
		Code    string            `json:"code"`
		Message string            `json:"message"`
		Fields  map[string]string `json:"fields"`
	} `json:"error"`
}

func decodeError(t *testing.T, rec *httptest.ResponseRecorder) errorEnvelope {
	t.Helper()
	var env errorEnvelope
	if err := json.Unmarshal(rec.Body.Bytes(), &env); err != nil {
		t.Fatalf("respons bukan JSON error: %v (%s)", err, rec.Body.String())
	}
	return env
}

func TestHandlerSubmitSuccess(t *testing.T) {
	sub := &fakeBackend{result: report.Result{CaseNumber: "20481735", PIN: "493027"}}
	cap := &fakeCaptcha{}
	h := report.NewHandler(sub, cap, &fakeLimiter{allow: true}, nil, 1<<20)

	req := newMultipartRequest(t, `{"chronology":"x"}`, "token-uji",
		[]upload{{name: "bukti.png", content: pngContent()}})
	rec := httptest.NewRecorder()

	h.Submit(rec, req)

	if rec.Code != http.StatusCreated {
		t.Fatalf("status = %d (%s), ingin 201", rec.Code, rec.Body.String())
	}

	var result report.Result
	if err := json.Unmarshal(rec.Body.Bytes(), &result); err != nil {
		t.Fatalf("respons bukan JSON hasil: %v", err)
	}
	if result.CaseNumber != "20481735" || result.PIN != "493027" {
		t.Errorf("hasil = %+v", result)
	}
	if sub.calls != 1 {
		t.Fatalf("Submit dipanggil %d kali, ingin 1", sub.calls)
	}
	if sub.req.Chronology != "x" {
		t.Errorf("payload tidak diteruskan: %+v", sub.req)
	}
	if len(sub.files) != 1 {
		t.Fatalf("berkas diteruskan = %d, ingin 1", len(sub.files))
	}
	if sub.ip != "198.51.100.7" {
		t.Errorf("ip = %q, ingin 198.51.100.7", sub.ip)
	}
	if cap.token != "token-uji" {
		t.Errorf("token captcha = %q", cap.token)
	}
}

func TestHandlerRejectsRateLimited(t *testing.T) {
	sub := &fakeBackend{}
	h := report.NewHandler(sub, nil, &fakeLimiter{allow: false}, nil, 1<<20)

	rec := httptest.NewRecorder()
	h.Submit(rec, newMultipartRequest(t, `{}`, "", nil))

	if rec.Code != http.StatusTooManyRequests {
		t.Fatalf("status = %d, ingin 429", rec.Code)
	}
	if rec.Header().Get("Retry-After") == "" {
		t.Error("header Retry-After tidak ada")
	}
	if sub.calls != 0 {
		t.Error("Submit dipanggil padahal rate limited")
	}
}

func TestHandlerRejectsCaptchaFailure(t *testing.T) {
	sub := &fakeBackend{}
	h := report.NewHandler(sub, &fakeCaptcha{err: captcha.ErrFailed}, nil, nil, 1<<20)

	rec := httptest.NewRecorder()
	h.Submit(rec, newMultipartRequest(t, `{}`, "salah", nil))

	if rec.Code != http.StatusTooManyRequests {
		t.Fatalf("status = %d, ingin 429", rec.Code)
	}
	if sub.calls != 0 {
		t.Error("Submit dipanggil padahal captcha gagal")
	}
}

func TestHandlerRejectsBadPayload(t *testing.T) {
	cases := []struct {
		name    string
		payload string
	}{
		{name: "kosong", payload: ""},
		{name: "bukan json", payload: "bukan-json"},
		{name: "field asing", payload: `{"unknown":true}`},
		{name: "dua objek", payload: `{} {}`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			sub := &fakeBackend{}
			h := report.NewHandler(sub, nil, nil, nil, 1<<20)

			rec := httptest.NewRecorder()
			h.Submit(rec, newMultipartRequest(t, tc.payload, "", nil))

			if rec.Code != http.StatusUnprocessableEntity {
				t.Fatalf("status = %d (%s), ingin 422", rec.Code, rec.Body.String())
			}
			env := decodeError(t, rec)
			if _, ok := env.Error.Fields["payload"]; !ok {
				t.Errorf("field payload tidak ada: %+v", env.Error)
			}
			if sub.calls != 0 {
				t.Error("Submit dipanggil padahal payload tidak valid")
			}
		})
	}
}

func TestHandlerPropagatesServiceError(t *testing.T) {
	appErr := httpx.ErrValidation("Data laporan belum lengkap.")
	appErr.Fields = map[string]string{"reporter.name": "Wajib diisi."}
	sub := &fakeBackend{err: appErr}
	h := report.NewHandler(sub, nil, nil, nil, 1<<20)

	rec := httptest.NewRecorder()
	h.Submit(rec, newMultipartRequest(t, `{}`, "", nil))

	if rec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("status = %d, ingin 422", rec.Code)
	}
	env := decodeError(t, rec)
	if env.Error.Fields["reporter.name"] == "" {
		t.Errorf("fields tidak diteruskan: %+v", env.Error)
	}
}

func TestHandlerRejectsOversizedBody(t *testing.T) {
	sub := &fakeBackend{}
	h := report.NewHandler(sub, nil, nil, nil, 128)

	payload := `{"chronology":"` + string(bytes.Repeat([]byte("a"), 512)) + `"}`

	rec := httptest.NewRecorder()
	h.Submit(rec, newMultipartRequest(t, payload, "", nil))

	if rec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("status = %d (%s), ingin 422", rec.Code, rec.Body.String())
	}
	if sub.calls != 0 {
		t.Error("Submit dipanggil padahal body melebihi batas")
	}
}

type completeUpload struct {
	ticket string
	pin    string
	text   string
	files  []upload
}

func newCompleteRequest(t *testing.T, c completeUpload) *http.Request {
	t.Helper()
	var buf bytes.Buffer
	w := multipart.NewWriter(&buf)
	if c.pin != "" {
		if err := w.WriteField("pin", c.pin); err != nil {
			t.Fatalf("tulis pin: %v", err)
		}
	}
	if c.text != "" {
		if err := w.WriteField("additional_text", c.text); err != nil {
			t.Fatalf("tulis teks: %v", err)
		}
	}
	for _, f := range c.files {
		part, err := w.CreateFormFile("evidences", f.name)
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

	req := httptest.NewRequest(http.MethodPut, "/api/reports/"+c.ticket+"/complete", &buf)
	req.Header.Set("Content-Type", w.FormDataContentType())
	req.SetPathValue("ticket", c.ticket)
	req.RemoteAddr = "198.51.100.7:4321"
	return req
}

func jsonRequest(t *testing.T, method, target, body string) *http.Request {
	t.Helper()
	req := httptest.NewRequest(method, target, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.RemoteAddr = "198.51.100.7:4321"
	return req
}

func TestHandlerStatusSuccess(t *testing.T) {
	backend := &fakeBackend{
		verifyReport: report.Report{CaseNumber: "20481735"},
		statusView: report.StatusView{
			CaseNumber:  "20481735",
			Stage:       3,
			Status:      "PENELAAHAN",
			StatusLabel: "Penelaahan",
		},
	}
	h := report.NewHandler(backend, nil, nil, nil, 1<<20)

	rec := httptest.NewRecorder()
	h.Status(rec, jsonRequest(t, http.MethodPost, "/api/status",
		`{"case_number":"20481735","pin":"493027"}`))

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d (%s), ingin 200", rec.Code, rec.Body.String())
	}
	if backend.verifyCalls != 1 || backend.statusCalls != 1 {
		t.Errorf("verify=%d status=%d, ingin 1/1", backend.verifyCalls, backend.statusCalls)
	}
	if backend.lastPIN != "493027" {
		t.Errorf("PIN diteruskan = %q", backend.lastPIN)
	}
	if backend.statusIP != "198.51.100.7" {
		t.Errorf("ip status = %q", backend.statusIP)
	}

	var view report.StatusView
	if err := json.Unmarshal(rec.Body.Bytes(), &view); err != nil {
		t.Fatalf("decode view: %v", err)
	}
	if view.Stage != 3 || view.StatusLabel != "Penelaahan" {
		t.Errorf("view = %+v", view)
	}
}

func TestHandlerStatusRequiresFields(t *testing.T) {
	backend := &fakeBackend{}
	h := report.NewHandler(backend, nil, nil, nil, 1<<20)

	rec := httptest.NewRecorder()
	h.Status(rec, jsonRequest(t, http.MethodPost, "/api/status", `{"case_number":"  ","pin":""}`))

	if rec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("status = %d, ingin 422", rec.Code)
	}
	env := decodeError(t, rec)
	if env.Error.Fields["case_number"] == "" || env.Error.Fields["pin"] == "" {
		t.Errorf("fields tidak lengkap: %+v", env.Error)
	}
	if backend.verifyCalls != 0 {
		t.Error("VerifyPIN dipanggil padahal field wajib kosong")
	}
}

func TestHandlerStatusLocked(t *testing.T) {
	backend := &fakeBackend{verifyErr: httpx.ErrTooMany("terkunci")}
	h := report.NewHandler(backend, nil, nil, nil, 1<<20)

	rec := httptest.NewRecorder()
	h.Status(rec, jsonRequest(t, http.MethodPost, "/api/status",
		`{"case_number":"20481735","pin":"000000"}`))

	if rec.Code != http.StatusTooManyRequests {
		t.Fatalf("status = %d, ingin 429", rec.Code)
	}
	if rec.Header().Get("Retry-After") == "" {
		t.Error("header Retry-After tidak ada")
	}
	if backend.statusCalls != 0 {
		t.Error("Status dipanggil padahal PIN terkunci")
	}
}

func TestHandlerStatusRateLimited(t *testing.T) {
	backend := &fakeBackend{}
	h := report.NewHandler(backend, nil, &fakeLimiter{allow: false}, nil, 1<<20)

	rec := httptest.NewRecorder()
	h.Status(rec, jsonRequest(t, http.MethodPost, "/api/status",
		`{"case_number":"20481735","pin":"493027"}`))

	if rec.Code != http.StatusTooManyRequests {
		t.Fatalf("status = %d, ingin 429", rec.Code)
	}
	if backend.verifyCalls != 0 {
		t.Error("VerifyPIN dipanggil padahal rate limited")
	}
}

func TestHandlerCompleteSuccess(t *testing.T) {
	backend := &fakeBackend{
		verifyReport:  report.Report{CaseNumber: "20481735"},
		completeReply: report.Completion{CaseNumber: "20481735", Status: "DILAPORKAN"},
	}
	h := report.NewHandler(backend, nil, nil, nil, 1<<20)

	req := newCompleteRequest(t, completeUpload{
		ticket: "20481735",
		pin:    "493027",
		text:   "Ini tambahan saya.",
		files:  []upload{{name: "tambahan.png", content: pngContent()}},
	})
	rec := httptest.NewRecorder()
	h.Complete(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d (%s), ingin 200", rec.Code, rec.Body.String())
	}
	if backend.completeCalls != 1 {
		t.Fatalf("Complete dipanggil %d kali, ingin 1", backend.completeCalls)
	}
	if backend.completeText != "Ini tambahan saya." {
		t.Errorf("teks = %q", backend.completeText)
	}
	if len(backend.completeFiles) != 1 {
		t.Errorf("berkas = %d, ingin 1", len(backend.completeFiles))
	}
	if backend.completeIP != "198.51.100.7" {
		t.Errorf("ip = %q", backend.completeIP)
	}
}

func TestHandlerCompleteRequiresPIN(t *testing.T) {
	backend := &fakeBackend{}
	h := report.NewHandler(backend, nil, nil, nil, 1<<20)

	req := newCompleteRequest(t, completeUpload{ticket: "20481735", text: "tambahan"})
	rec := httptest.NewRecorder()
	h.Complete(rec, req)

	if rec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("status = %d, ingin 422", rec.Code)
	}
	env := decodeError(t, rec)
	if env.Error.Fields["pin"] == "" {
		t.Errorf("field pin tidak ada: %+v", env.Error)
	}
	if backend.verifyCalls != 0 || backend.completeCalls != 0 {
		t.Error("backend dipanggil padahal PIN kosong")
	}
}

func TestHandlerCompleteConflict(t *testing.T) {
	backend := &fakeBackend{
		verifyReport: report.Report{CaseNumber: "20481735"},
		completeErr:  httpx.ErrConflict("Laporan tidak sedang menunggu pelengkapan."),
	}
	h := report.NewHandler(backend, nil, nil, nil, 1<<20)

	req := newCompleteRequest(t, completeUpload{ticket: "20481735", pin: "493027", text: "tambahan"})
	rec := httptest.NewRecorder()
	h.Complete(rec, req)

	if rec.Code != http.StatusConflict {
		t.Fatalf("status = %d, ingin 409", rec.Code)
	}
}
