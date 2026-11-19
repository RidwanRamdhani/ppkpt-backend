package evidence

import (
	"bytes"
	"context"
	"errors"
	"io"
	"log/slog"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/RidwanRamdhani/ppkpt-backend/internal/audit"
	"github.com/RidwanRamdhani/ppkpt-backend/internal/platform/storage"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

const (
	pdfContent  = "%PDF-1.7\n1 0 obj\n<<>>\nendobj\ntrailer\n<<>>\n%%EOF\n"
	pngContent  = "\x89PNG\r\n\x1a\n\x00\x00\x00\rIHDR"
	jpegContent = "\xff\xd8\xff\xe0\x00\x10JFIF\x00\x01\x01\x00\x00\x01\x00\x01\x00\x00"
	exeContent  = "MZ\x90\x00\x03\x00\x00\x00\x04\x00\x00\x00\xff\xff"
)

type testFile struct {
	name    string
	content []byte
}

// multipartHeaders membuat FileHeader asli melalui parsing multipart, sehingga
// pengujian memakai jalur yang sama dengan handler HTTP.
func multipartHeaders(t *testing.T, files []testFile) []*multipart.FileHeader {
	t.Helper()

	var body bytes.Buffer
	mw := multipart.NewWriter(&body)
	for _, f := range files {
		part, err := mw.CreateFormFile("evidences", f.name)
		if err != nil {
			t.Fatalf("buat form file: %v", err)
		}
		if _, err := part.Write(f.content); err != nil {
			t.Fatalf("tulis form file: %v", err)
		}
	}
	if err := mw.Close(); err != nil {
		t.Fatalf("tutup multipart: %v", err)
	}

	req := httptest.NewRequest(http.MethodPost, "/api/reports", &body)
	req.Header.Set("Content-Type", mw.FormDataContentType())
	if err := req.ParseMultipartForm(8 << 20); err != nil {
		t.Fatalf("parse multipart: %v", err)
	}
	t.Cleanup(func() { _ = req.MultipartForm.RemoveAll() })

	return req.MultipartForm.File["evidences"]
}

// fakeStore merekam operasi agar urutan unggah/hapus dapat diperiksa.
type fakeStore struct {
	mu      sync.Mutex
	puts    []string
	removed []string
	putErr  error
	getErr  error
	gets    int
}

func (f *fakeStore) Put(_ context.Context, key string, _ io.Reader, _ int64, _ string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.putErr != nil {
		return f.putErr
	}
	f.puts = append(f.puts, key)
	return nil
}

func (f *fakeStore) Get(_ context.Context, _ string) (*storage.Object, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.gets++
	if f.getErr != nil {
		return nil, f.getErr
	}
	return &storage.Object{
		Body:        io.NopCloser(strings.NewReader("isi")),
		Size:        3,
		ContentType: "application/pdf",
	}, nil
}

func (f *fakeStore) Remove(_ context.Context, key string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.removed = append(f.removed, key)
	return nil
}

func newTestService(t *testing.T, store ObjectStore, opts Options) *Service {
	t.Helper()
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	return NewService(store, audit.NewService(), logger, opts)
}

func TestValidate_MenerimaBerkasYangDidukung(t *testing.T) {
	svc := newTestService(t, &fakeStore{}, Options{MaxUploadBytes: 1 << 20, MaxFiles: 10})
	headers := multipartHeaders(t, []testFile{
		{name: "bukti.pdf", content: []byte(pdfContent)},
		{name: "foto.png", content: []byte(pngContent)},
		{name: "foto.jpg", content: []byte(jpegContent)},
	})

	files, err := svc.Validate(headers)
	if err != nil {
		t.Fatalf("Validate: %v", err)
	}
	if len(files) != 3 {
		t.Fatalf("jumlah berkas = %d, mau 3", len(files))
	}

	wantExt := []string{".pdf", ".png", ".jpg"}
	for i, f := range files {
		if f.Ext != wantExt[i] {
			t.Errorf("berkas %d: ext = %q, mau %q", i, f.Ext, wantExt[i])
		}
	}
}

func TestValidate_MenolakExeYangDiubahNamanya(t *testing.T) {
	svc := newTestService(t, &fakeStore{}, Options{MaxUploadBytes: 1 << 20, MaxFiles: 10})
	headers := multipartHeaders(t, []testFile{
		{name: "laporan.pdf", content: []byte(exeContent)},
	})

	_, err := svc.Validate(headers)
	if err == nil {
		t.Fatal("exe yang dinamai .pdf harus ditolak")
	}
	if !strings.Contains(err.Error(), "Tipe berkas tidak didukung") {
		t.Fatalf("pesan error = %v", err)
	}
}

func TestValidate_MenolakJumlahBerkasBerlebih(t *testing.T) {
	svc := newTestService(t, &fakeStore{}, Options{MaxUploadBytes: 1 << 20, MaxFiles: 1})
	headers := multipartHeaders(t, []testFile{
		{name: "a.pdf", content: []byte(pdfContent)},
		{name: "b.pdf", content: []byte(pdfContent)},
	})

	if _, err := svc.Validate(headers); err == nil {
		t.Fatal("jumlah berkas melebihi batas harus ditolak")
	}
}

func TestValidate_MenolakUkuranBerlebih(t *testing.T) {
	svc := newTestService(t, &fakeStore{}, Options{MaxUploadBytes: 10, MaxFiles: 10})
	headers := multipartHeaders(t, []testFile{
		{name: "besar.pdf", content: []byte(pdfContent)},
	})

	if _, err := svc.Validate(headers); err == nil {
		t.Fatal("berkas melebihi batas ukuran harus ditolak")
	}
}

func TestValidate_MenolakBerkasKosong(t *testing.T) {
	svc := newTestService(t, &fakeStore{}, Options{MaxUploadBytes: 1 << 20, MaxFiles: 10})
	headers := multipartHeaders(t, []testFile{{name: "kosong.pdf", content: []byte{}}})

	if _, err := svc.Validate(headers); err == nil {
		t.Fatal("berkas kosong harus ditolak")
	}
}

func TestStoreFiles_CleanupMenghapusSemuaObjek(t *testing.T) {
	store := &fakeStore{}
	svc := newTestService(t, store, Options{MaxUploadBytes: 1 << 20, MaxFiles: 10})
	headers := multipartHeaders(t, []testFile{
		{name: "a.pdf", content: []byte(pdfContent)},
		{name: "b.png", content: []byte(pngContent)},
	})

	files, err := svc.Validate(headers)
	if err != nil {
		t.Fatalf("Validate: %v", err)
	}
	stored, cleanup, err := svc.StoreFiles(context.Background(), files)
	if err != nil {
		t.Fatalf("StoreFiles: %v", err)
	}
	if len(stored) != 2 {
		t.Fatalf("jumlah stored = %d, mau 2", len(stored))
	}

	cleanup() // simulasi transaksi database gagal
	if len(store.removed) != 2 {
		t.Fatalf("objek dihapus = %d, mau 2", len(store.removed))
	}
	for i, key := range store.puts {
		if store.removed[i] != key {
			t.Errorf("cleanup[%d] = %q, mau %q", i, store.removed[i], key)
		}
	}
}

func TestStoreFiles_GagalUnggahMembersihkanObjekSebelumnya(t *testing.T) {
	store := &fakeStore{putErr: errors.New("storage penuh")}
	svc := newTestService(t, store, Options{MaxUploadBytes: 1 << 20, MaxFiles: 10})
	headers := multipartHeaders(t, []testFile{
		{name: "a.pdf", content: []byte(pdfContent)},
	})

	files, err := svc.Validate(headers)
	if err != nil {
		t.Fatalf("Validate: %v", err)
	}
	if _, _, err := svc.StoreFiles(context.Background(), files); err == nil {
		t.Fatal("StoreFiles harus gagal")
	}
	// Key yang gagal diunggah tetap dicoba dibersihkan (Remove idempoten).
	if len(store.removed) != 1 {
		t.Fatalf("objek dibersihkan = %d, mau 1", len(store.removed))
	}
}

func TestSanitizeFilename(t *testing.T) {
	cases := map[string]string{
		"../../etc/passwd":            "passwd",
		`C:\Users\orang\bukti.pdf`:    "bukti.pdf",
		"..":                          "berkas",
		"":                            "berkas",
		"bukti\x00.pdf":               "bukti.pdf",
		"nama yang sangat normal.pdf": "nama yang sangat normal.pdf",
	}
	for input, want := range cases {
		if got := SanitizeFilename(input); got != want {
			t.Errorf("SanitizeFilename(%q) = %q, mau %q", input, got, want)
		}
	}
}

func TestNewObjectKey_AcakDanBerextensi(t *testing.T) {
	seen := map[string]bool{}
	for i := 0; i < 50; i++ {
		key, err := newObjectKey(".pdf")
		if err != nil {
			t.Fatalf("newObjectKey: %v", err)
		}
		if !strings.HasSuffix(key, ".pdf") {
			t.Fatalf("key = %q, mau berakhiran .pdf", key)
		}
		if len(key) != 32+len(".pdf") {
			t.Fatalf("panjang key = %d, mau %d", len(key), 32+len(".pdf"))
		}
		if seen[key] {
			t.Fatalf("key duplikat: %q", key)
		}
		seen[key] = true
	}
}

// fakeDB memenuhi evidence.DBTX untuk pengujian metadata dan audit.
type fakeDB struct {
	row     Meta
	execErr error
	execs   []string
}

func (f *fakeDB) Exec(_ context.Context, sql string, _ ...any) (pgconn.CommandTag, error) {
	f.execs = append(f.execs, sql)
	return pgconn.CommandTag{}, f.execErr
}

func (f *fakeDB) QueryRow(_ context.Context, _ string, _ ...any) pgx.Row {
	return fakeRow{meta: f.row}
}

type fakeRow struct {
	meta Meta
}

func (r fakeRow) Scan(dest ...any) error {
	*dest[0].(*int64) = r.meta.ID
	*dest[1].(*int64) = r.meta.ReportID
	*dest[2].(*string) = r.meta.OriginalName
	*dest[3].(*string) = r.meta.MimeType
	*dest[4].(*int64) = r.meta.SizeBytes
	*dest[5].(*string) = r.meta.StoredName
	return nil
}

func TestDownload_AuditGagalMembatalkanUnduhan(t *testing.T) {
	store := &fakeStore{}
	svc := newTestService(t, store, Options{})
	db := &fakeDB{
		row:     Meta{ID: 1, ReportID: 2, OriginalName: "bukti.pdf", StoredName: "abc.pdf"},
		execErr: errors.New("audit mati"),
	}

	_, _, err := svc.Download(context.Background(), db, 1, Actor{UserID: 9, IP: "127.0.0.1"})
	if err == nil {
		t.Fatal("Download harus gagal saat audit gagal")
	}
	if store.gets != 0 {
		t.Fatalf("storage.Get dipanggil %d kali, harus 0 (fail-closed)", store.gets)
	}
}

func TestDownload_MembukaObjekSetelahAudit(t *testing.T) {
	store := &fakeStore{}
	svc := newTestService(t, store, Options{})
	db := &fakeDB{
		row: Meta{ID: 1, ReportID: 2, OriginalName: "bukti.pdf", StoredName: "abc.pdf"},
	}

	meta, obj, err := svc.Download(context.Background(), db, 1, Actor{UserID: 9, IP: "127.0.0.1"})
	if err != nil {
		t.Fatalf("Download: %v", err)
	}
	defer func() { _ = obj.Body.Close() }()

	if meta.ID != 1 {
		t.Errorf("meta.ID = %d, mau 1", meta.ID)
	}
	if store.gets != 1 {
		t.Errorf("storage.Get dipanggil %d kali, mau 1", store.gets)
	}
	if len(db.execs) != 1 {
		t.Fatalf("audit ditulis %d kali, mau 1", len(db.execs))
	}
}
