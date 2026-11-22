package report_test

import (
	"bytes"
	"context"
	"errors"
	"io"
	"mime/multipart"
	"sync"
	"testing"

	"github.com/RidwanRamdhani/ppkpt-backend/internal/audit"
	"github.com/RidwanRamdhani/ppkpt-backend/internal/evidence"
	"github.com/RidwanRamdhani/ppkpt-backend/internal/notification"
	"github.com/RidwanRamdhani/ppkpt-backend/internal/platform/storage"
	"github.com/RidwanRamdhani/ppkpt-backend/internal/report"
	"golang.org/x/crypto/bcrypt"
)

// fakeObjectStore menyimpan objek di memori agar alur unggah/cleanup dapat
// diperiksa tanpa Silo.
type fakeObjectStore struct {
	mu      sync.Mutex
	objects map[string][]byte
	puts    int
	removed []string
	putErr  error
}

func newFakeObjectStore() *fakeObjectStore {
	return &fakeObjectStore{objects: make(map[string][]byte)}
}

func (f *fakeObjectStore) Put(_ context.Context, key string, r io.Reader, _ int64, _ string) error {
	if f.putErr != nil {
		return f.putErr
	}
	data, err := io.ReadAll(r)
	if err != nil {
		return err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	f.objects[key] = data
	f.puts++
	return nil
}

func (f *fakeObjectStore) Get(_ context.Context, key string) (*storage.Object, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	data, ok := f.objects[key]
	if !ok {
		return nil, storage.ErrNotFound
	}
	return &storage.Object{
		Body:        io.NopCloser(bytes.NewReader(data)),
		Size:        int64(len(data)),
		ContentType: "application/octet-stream",
	}, nil
}

func (f *fakeObjectStore) Remove(_ context.Context, key string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	delete(f.objects, key)
	f.removed = append(f.removed, key)
	return nil
}

func (f *fakeObjectStore) putCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.puts
}

type sentEmail struct {
	to      string
	subject string
	body    string
}

type fakeSender struct {
	mu     sync.Mutex
	emails []sentEmail
}

func (f *fakeSender) Send(to, subject, body string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.emails = append(f.emails, sentEmail{to: to, subject: subject, body: body})
}

func (f *fakeSender) count() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.emails)
}

func newTestEvidence(store *fakeObjectStore) *evidence.Service {
	return evidence.NewService(store, audit.NewService(), nil, evidence.Options{
		MaxUploadBytes: 1 << 20,
		MaxFiles:       5,
	})
}

func newSubmitService(store *fakeObjectStore, sender *fakeSender) *report.Service {
	return report.NewService(
		nil, // pool tidak dipakai pada jalur yang berhenti sebelum transaksi
		newTestEvidence(store),
		audit.NewService(),
		notification.New(sender, nil),
		nil,
		report.Options{BcryptCost: bcrypt.MinCost},
	)
}

// fileHeader membuat *multipart.FileHeader sungguhan dari body multipart.
func fileHeader(t *testing.T, field, name string, content []byte) *multipart.FileHeader {
	t.Helper()
	var buf bytes.Buffer
	w := multipart.NewWriter(&buf)
	part, err := w.CreateFormFile(field, name)
	if err != nil {
		t.Fatalf("buat form file: %v", err)
	}
	if _, err := part.Write(content); err != nil {
		t.Fatalf("tulis berkas: %v", err)
	}
	if err := w.Close(); err != nil {
		t.Fatalf("tutup writer: %v", err)
	}

	mr := multipart.NewReader(&buf, w.Boundary())
	form, err := mr.ReadForm(1 << 20)
	if err != nil {
		t.Fatalf("baca form: %v", err)
	}
	t.Cleanup(func() { _ = form.RemoveAll() })

	headers := form.File[field]
	if len(headers) == 0 {
		t.Fatalf("field %q tidak berisi berkas", field)
	}
	return headers[0]
}

func pngContent() []byte {
	return append([]byte{0x89, 'P', 'N', 'G', 0x0d, 0x0a, 0x1a, 0x0a}, []byte("IHDR-uji")...)
}

// TestSubmitRejectsInvalidPayload memastikan payload tidak valid berhenti
// sebelum ada objek yang diunggah dan sebelum email dikirim.
func TestSubmitRejectsInvalidPayload(t *testing.T) {
	store := newFakeObjectStore()
	sender := &fakeSender{}
	svc := newSubmitService(store, sender)

	req := validRequest()
	req.Chronology = ""

	_, err := svc.Submit(context.Background(), req, nil, "203.0.113.5")
	var appErr = appErrorOf(t, err)
	if appErr.Status != 422 {
		t.Fatalf("status = %d, ingin 422", appErr.Status)
	}
	if _, ok := appErr.Fields["chronology"]; !ok {
		t.Errorf("field chronology tidak ada: %v", appErr.Fields)
	}
	if store.putCount() != 0 {
		t.Errorf("ada %d objek terunggah padahal payload tidak valid", store.putCount())
	}
	if sender.count() != 0 {
		t.Errorf("email terkirim padahal payload tidak valid")
	}
}

// TestSubmitRequiresEvidence memastikan minimal satu bukti wajib.
func TestSubmitRequiresEvidence(t *testing.T) {
	store := newFakeObjectStore()
	svc := newSubmitService(store, &fakeSender{})

	_, err := svc.Submit(context.Background(), validRequest(), nil, "203.0.113.5")
	var appErr = appErrorOf(t, err)
	if appErr.Status != 422 {
		t.Fatalf("status = %d, ingin 422", appErr.Status)
	}
	if _, ok := appErr.Fields["evidences"]; !ok {
		t.Errorf("field evidences tidak ada: %v", appErr.Fields)
	}
	if store.putCount() != 0 {
		t.Errorf("ada %d objek terunggah", store.putCount())
	}
}

// TestSubmitRejectsDisallowedFileType memastikan tipe berkas diperiksa dari isi
// sebelum diunggah.
func TestSubmitRejectsDisallowedFileType(t *testing.T) {
	store := newFakeObjectStore()
	svc := newSubmitService(store, &fakeSender{})

	exe := append([]byte("MZ"), bytes.Repeat([]byte{0x90}, 64)...)
	headers := []*multipart.FileHeader{fileHeader(t, "evidences", "bukti.pdf", exe)}

	_, err := svc.Submit(context.Background(), validRequest(), headers, "203.0.113.5")
	var appErr = appErrorOf(t, err)
	if appErr.Status != 422 {
		t.Fatalf("status = %d, ingin 422", appErr.Status)
	}
	if _, ok := appErr.Fields["evidences"]; !ok {
		t.Errorf("field evidences tidak ada: %v", appErr.Fields)
	}
	if store.putCount() != 0 {
		t.Errorf("berkas terlarang tetap diunggah")
	}
}

// TestSubmitPropagatesStoreError memastikan kegagalan storage menjadi error
// (dipetakan 500 oleh handler) dan tidak mengirim email.
func TestSubmitPropagatesStoreError(t *testing.T) {
	store := newFakeObjectStore()
	store.putErr = errors.New("storage mati")
	sender := &fakeSender{}
	svc := newSubmitService(store, sender)

	headers := []*multipart.FileHeader{fileHeader(t, "evidences", "bukti.png", pngContent())}
	if _, err := svc.Submit(context.Background(), validRequest(), headers, "203.0.113.5"); err == nil {
		t.Fatal("Submit = nil, ingin error storage")
	}
	if sender.count() != 0 {
		t.Errorf("email terkirim padahal penyimpanan gagal")
	}
}
