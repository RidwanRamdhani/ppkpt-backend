// Package evidence mengelola validasi, penyimpanan, metadata, dan pengambilan
// berkas bukti.
//
// Tipe berkas selalu dideteksi dari isi, bukan dari ekstensi atau Content-Type
// yang dikirim klien. Object key dibuat acak agar nama asli tidak pernah menjadi
// path di object storage.
package evidence

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"mime"
	"mime/multipart"
	"net/http"
	"path"
	"strings"
	"sync"
	"time"

	"github.com/RidwanRamdhani/ppkpt-backend/internal/audit"
	"github.com/RidwanRamdhani/ppkpt-backend/internal/platform/httpx"
	"github.com/RidwanRamdhani/ppkpt-backend/internal/platform/storage"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

// Kategori berkas sesuai tabel evidences.
const (
	CategoryBukti    = "BUKTI"
	CategoryBAI      = "BAI"
	CategoryLampiran = "LAMPIRAN"
)

const (
	defaultMaxUploadBytes int64 = 10 << 20 // 10 MB
	defaultMaxFiles             = 10
	detectBufferSize            = 512
	cleanupTimeout              = 10 * time.Second
)

// allowedMIME memetakan MIME hasil deteksi isi ke ekstensi object key.
var allowedMIME = map[string]string{
	"application/pdf": ".pdf",
	"image/jpeg":      ".jpg",
	"image/png":       ".png",
}

// ObjectStore adalah operasi storage yang dibutuhkan layanan evidence. Interface
// sempit ini memudahkan pengujian dengan penyimpanan palsu.
type ObjectStore interface {
	Put(ctx context.Context, key string, r io.Reader, size int64, contentType string) error
	Get(ctx context.Context, key string) (*storage.Object, error)
	Remove(ctx context.Context, key string) error
}

// DBTX adalah subset koneksi database yang dibutuhkan layanan evidence.
type DBTX interface {
	Exec(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error)
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
}

// Options membatasi berkas yang diterima.
type Options struct {
	MaxUploadBytes int64 // ukuran maksimum per berkas
	MaxFiles       int   // jumlah maksimum berkas per permintaan
}

// Service menyimpan berkas ke object storage dan metadata ke database.
type Service struct {
	store          ObjectStore
	audit          *audit.Service
	logger         *slog.Logger
	maxUploadBytes int64
	maxFiles       int
}

// NewService membuat layanan evidence. Nilai opsi yang tidak masuk akal diganti
// dengan nilai default agar service tetap aman dipakai.
func NewService(store ObjectStore, auditor *audit.Service, logger *slog.Logger, opts Options) *Service {
	if opts.MaxUploadBytes <= 0 {
		opts.MaxUploadBytes = defaultMaxUploadBytes
	}
	if opts.MaxFiles <= 0 {
		opts.MaxFiles = defaultMaxFiles
	}
	if logger == nil {
		logger = slog.Default()
	}
	if auditor == nil {
		auditor = audit.NewService()
	}
	return &Service{
		store:          store,
		audit:          auditor,
		logger:         logger,
		maxUploadBytes: opts.MaxUploadBytes,
		maxFiles:       opts.MaxFiles,
	}
}

// File adalah berkas multipart yang lolos validasi.
type File struct {
	Header *multipart.FileHeader
	MIME   string // hasil deteksi isi berkas
	Ext    string // ekstensi yang dipakai pada object key
}

// Stored adalah metadata objek yang sudah berhasil diunggah.
type Stored struct {
	StoredName   string // object key di bucket
	OriginalName string // nama asli yang sudah disanitasi
	MimeType     string
	SizeBytes    int64
}

// Meta adalah metadata berkas yang tersimpan di database.
type Meta struct {
	ID           int64
	ReportID     int64
	OriginalName string
	MimeType     string
	SizeBytes    int64
	StoredName   string
}

// Actor adalah pemakai yang melakukan unduhan.
type Actor struct {
	UserID int64
	IP     string
}

// Validate memeriksa jumlah, ukuran, dan tipe seluruh berkas berdasarkan isi.
// Semua kesalahan dikembalikan sebagai AppError 422 agar handler cukup
// meneruskannya ke httpx.WriteError.
func (s *Service) Validate(headers []*multipart.FileHeader) ([]File, error) {
	if len(headers) > s.maxFiles {
		return nil, validationError("evidences",
			fmt.Sprintf("Jumlah berkas melebihi batas %d berkas.", s.maxFiles))
	}

	files := make([]File, 0, len(headers))
	for _, header := range headers {
		file, err := s.validateOne(header)
		if err != nil {
			return nil, err
		}
		files = append(files, file)
	}
	return files, nil
}

// validateOne memvalidasi satu berkas: ukuran, keterbacaan, dan tipe isi.
func (s *Service) validateOne(header *multipart.FileHeader) (File, error) {
	if header == nil {
		return File{}, validationError("evidences", "Berkas tidak valid.")
	}

	name := SanitizeFilename(header.Filename)
	if header.Size <= 0 {
		return File{}, validationError("evidences",
			fmt.Sprintf("Berkas %q kosong.", name))
	}
	if header.Size > s.maxUploadBytes {
		return File{}, validationError("evidences",
			fmt.Sprintf("Berkas %q melebihi batas %d MB.", name, s.maxUploadBytes/(1<<20)))
	}

	mimeType, err := detectContentType(header)
	if err != nil {
		return File{}, validationError("evidences",
			fmt.Sprintf("Berkas %q tidak dapat dibaca.", name))
	}

	ext, ok := allowedMIME[mimeType]
	if !ok {
		return File{}, validationError("evidences",
			"Tipe berkas tidak didukung. Gunakan PDF, JPG, atau PNG.")
	}

	return File{Header: header, MIME: mimeType, Ext: ext}, nil
}

// detectContentType membaca awal berkas dan mengembalikan MIME berdasarkan isi.
// Berkas dibuka pada handle terpisah lalu ditutup; unggahan membuka handle baru
// sehingga isi tetap utuh dari awal.
func detectContentType(header *multipart.FileHeader) (string, error) {
	f, err := header.Open()
	if err != nil {
		return "", fmt.Errorf("buka berkas: %w", err)
	}
	defer func() { _ = f.Close() }()

	buf := make([]byte, detectBufferSize)
	n, err := io.ReadFull(f, buf)
	if err != nil && !errors.Is(err, io.ErrUnexpectedEOF) && !errors.Is(err, io.EOF) {
		return "", fmt.Errorf("baca berkas: %w", err)
	}
	if n == 0 {
		return "", errors.New("berkas kosong")
	}

	detected := http.DetectContentType(buf[:n])
	return strings.TrimSpace(strings.Split(detected, ";")[0]), nil
}

// StoreFiles mengunggah seluruh berkas ke object storage.
//
// Urutan yang benar di handler: panggil StoreFiles, jalankan transaksi database,
// lalu panggil cleanup bila transaksi gagal. StoreFiles juga membersihkan objek
// yang sudah terunggah bila salah satu unggahan gagal di tengah jalan.
func (s *Service) StoreFiles(ctx context.Context, files []File) ([]Stored, func(), error) {
	if len(files) > s.maxFiles {
		return nil, nil, validationError("evidences",
			fmt.Sprintf("Jumlah berkas melebihi batas %d berkas.", s.maxFiles))
	}

	stored := make([]Stored, 0, len(files))
	uploaded := make([]string, 0, len(files))

	var cleanupOnce sync.Once
	cleanup := func() {
		cleanupOnce.Do(func() {
			// Gunakan context baru agar objek tetap dibersihkan ketika request
			// dibatalkan atau context transaksi sudah selesai.
			rmCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), cleanupTimeout)
			defer cancel()
			for _, key := range uploaded {
				if err := s.store.Remove(rmCtx, key); err != nil {
					s.logger.Error("gagal menghapus objek yatim",
						"object_key", key, "err", err)
				}
			}
		})
	}

	for _, file := range files {
		if file.Header == nil || file.Header.Size <= 0 || file.Header.Size > s.maxUploadBytes {
			cleanup()
			return nil, nil, validationError("evidences", "Berkas tidak valid atau melebihi batas ukuran.")
		}

		key, err := newObjectKey(file.Ext)
		if err != nil {
			cleanup()
			return nil, nil, fmt.Errorf("evidence: buat object key: %w", err)
		}

		src, err := file.Header.Open()
		if err != nil {
			cleanup()
			return nil, nil, fmt.Errorf("evidence: buka berkas: %w", err)
		}

		// Catat key sebelum unggah agar objek yang gagal di tengah jalan ikut
		// dibersihkan; Remove bersifat idempoten bila objek ternyata tidak ada.
		uploaded = append(uploaded, key)

		err = s.store.Put(ctx, key, src, file.Header.Size, file.MIME)
		closeErr := src.Close()
		if err != nil {
			cleanup()
			return nil, nil, fmt.Errorf("evidence: unggah berkas: %w", err)
		}
		if closeErr != nil {
			s.logger.Warn("gagal menutup berkas multipart", "err", closeErr)
		}

		stored = append(stored, Stored{
			StoredName:   key,
			OriginalName: SanitizeFilename(file.Header.Filename),
			MimeType:     file.MIME,
			SizeBytes:    file.Header.Size,
		})
	}

	return stored, cleanup, nil
}

// SaveMetadata menulis metadata seluruh berkas dalam satu transaksi milik
// pemanggil. uploadedBy bernilai nil untuk berkas yang diunggah pelapor.
func (s *Service) SaveMetadata(ctx context.Context, q DBTX, reportID int64, category string, stored []Stored, uploadedBy *int64) error {
	if !validCategory(category) {
		return fmt.Errorf("evidence: kategori %q tidak valid", category)
	}

	const insertSQL = `
		INSERT INTO evidences
			(report_id, category, original_name, stored_name, mime_type, size_bytes, uploaded_by)
		VALUES ($1, $2, $3, $4, $5, $6, $7)`

	for _, item := range stored {
		if _, err := q.Exec(ctx, insertSQL,
			reportID,
			category,
			item.OriginalName,
			item.StoredName,
			item.MimeType,
			item.SizeBytes,
			uploadedBy,
		); err != nil {
			return fmt.Errorf("evidence: simpan metadata: %w", err)
		}
	}
	return nil
}

// Find mengambil metadata berkas berdasarkan ID.
func (s *Service) Find(ctx context.Context, q DBTX, id int64) (Meta, error) {
	const selectSQL = `
		SELECT id, report_id, original_name, mime_type, size_bytes, stored_name
		FROM evidences
		WHERE id = $1`

	var meta Meta
	err := q.QueryRow(ctx, selectSQL, id).Scan(
		&meta.ID,
		&meta.ReportID,
		&meta.OriginalName,
		&meta.MimeType,
		&meta.SizeBytes,
		&meta.StoredName,
	)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return Meta{}, httpx.ErrNotFound()
		}
		return Meta{}, fmt.Errorf("evidence: cari metadata: %w", err)
	}
	return meta, nil
}

// Download membuka berkas untuk diunduh dan mencatat audit EVIDENCE_DOWNLOADED
// secara fail-closed: bila audit gagal, unduhan dibatalkan.
//
// Handler wajib memeriksa hak akses terhadap Meta.ReportID sebelum memanggil
// metode ini.
func (s *Service) Download(ctx context.Context, q DBTX, id int64, actor Actor) (Meta, *storage.Object, error) {
	if actor.UserID <= 0 {
		return Meta{}, nil, errors.New("evidence: user unduhan tidak valid")
	}

	meta, err := s.Find(ctx, q, id)
	if err != nil {
		return Meta{}, nil, err
	}

	event := audit.Event{
		ActorType: audit.ActorUser,
		UserID:    &actor.UserID,
		Action:    audit.ActionEvidenceDownloaded,
		Entity:    "evidence",
		EntityID:  &meta.ID,
		IP:        actor.IP,
		Detail:    map[string]any{"report_id": meta.ReportID},
	}
	if err := s.audit.Log(ctx, q, event); err != nil {
		return Meta{}, nil, fmt.Errorf("evidence: audit unduhan: %w", err)
	}

	obj, err := s.store.Get(ctx, meta.StoredName)
	if err != nil {
		if errors.Is(err, storage.ErrNotFound) {
			s.logger.Error("metadata berkas ada tetapi objek tidak ditemukan di storage",
				"evidence_id", meta.ID, "report_id", meta.ReportID)
			return Meta{}, nil, httpx.ErrNotFound()
		}
		return Meta{}, nil, err
	}
	return meta, obj, nil
}

// WriteDownloadHeaders memasang header unduhan yang aman. Nama berkas selalu
// disanitasi dan dikirim sebagai attachment agar tidak dirender oleh browser.
func WriteDownloadHeaders(w http.ResponseWriter, meta Meta, obj *storage.Object) {
	disposition := mime.FormatMediaType("attachment", map[string]string{
		"filename": SanitizeFilename(meta.OriginalName),
	})
	w.Header().Set("Content-Type", meta.MimeType)
	w.Header().Set("Content-Disposition", disposition)
	w.Header().Set("Content-Length", fmt.Sprintf("%d", obj.Size))
	w.Header().Set("X-Content-Type-Options", "nosniff")
}

// SanitizeFilename membuang direktori dan karakter kontrol dari nama berkas.
// Nama hasil sanitasi aman dipakai pada header unduhan dan kolom original_name.
func SanitizeFilename(name string) string {
	// Samakan separator Windows dan Unix sebelum mengambil elemen terakhir.
	name = strings.ReplaceAll(name, "\\", "/")
	name = path.Base(name)
	name = strings.Map(func(r rune) rune {
		if r < 32 || r == 127 {
			return -1
		}
		return r
	}, name)
	name = strings.TrimSpace(name)

	switch name {
	case "", ".", "..", "/":
		return "berkas"
	}

	const maxRunes = 200 // beri ruang untuk kolom VARCHAR(255)
	if runes := []rune(name); len(runes) > maxRunes {
		name = string(runes[:maxRunes])
	}
	return name
}

// newObjectKey membuat object key acak 16 byte (hex) dengan ekstensi dari tipe
// yang sudah dideteksi server.
func newObjectKey(ext string) (string, error) {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", err
	}
	return hex.EncodeToString(b[:]) + ext, nil
}

// validCategory memastikan kategori sesuai CHECK constraint tabel evidences.
func validCategory(category string) bool {
	switch category {
	case CategoryBukti, CategoryBAI, CategoryLampiran:
		return true
	default:
		return false
	}
}

// validationError membuat AppError 422 dengan detail per field.
func validationError(field, message string) *httpx.AppError {
	appErr := httpx.ErrValidation(message)
	appErr.Fields = map[string]string{field: message}
	return appErr
}
