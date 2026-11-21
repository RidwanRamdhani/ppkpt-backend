package report_test

import (
	"bytes"
	"context"
	"encoding/json"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/RidwanRamdhani/ppkpt-backend/internal/platform/captcha"
	"github.com/RidwanRamdhani/ppkpt-backend/internal/platform/httpx"
	"github.com/RidwanRamdhani/ppkpt-backend/internal/report"
)

type fakeSubmitter struct {
	calls  int
	req    report.SubmitRequest
	files  []*multipart.FileHeader
	ip     string
	result report.Result
	err    error
}

func (f *fakeSubmitter) Submit(_ context.Context, req report.SubmitRequest, files []*multipart.FileHeader, ip string) (report.Result, error) {
	f.calls++
	f.req = req
	f.files = files
	f.ip = ip
	return f.result, f.err
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
	sub := &fakeSubmitter{result: report.Result{CaseNumber: "20481735", PIN: "493027"}}
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
	sub := &fakeSubmitter{}
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
	sub := &fakeSubmitter{}
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
			sub := &fakeSubmitter{}
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
	sub := &fakeSubmitter{err: appErr}
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
	sub := &fakeSubmitter{}
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
