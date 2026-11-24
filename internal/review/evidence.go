package review

import (
	"context"
	"errors"
	"mime/multipart"
	"strings"

	"github.com/RidwanRamdhani/ppkpt-backend/internal/audit"
	"github.com/RidwanRamdhani/ppkpt-backend/internal/auth"
	"github.com/RidwanRamdhani/ppkpt-backend/internal/evidence"
	"github.com/RidwanRamdhani/ppkpt-backend/internal/platform/db"
	"github.com/RidwanRamdhani/ppkpt-backend/internal/platform/httpx"
	"github.com/RidwanRamdhani/ppkpt-backend/internal/workflow"
	"github.com/jackc/pgx/v5"
)

// caseEvidenceCategories adalah kategori berkas yang boleh diunggah petugas
// melalui endpoint kasus. BUKTI hanya berasal dari pelapor.
var caseEvidenceCategories = map[string]struct{}{
	evidence.CategoryBAI:      {},
	evidence.CategoryLampiran: {},
}

// UploadResult adalah respons POST /api/cases/{id}/evidences.
type UploadResult struct {
	Category string `json:"category"`
	Uploaded int    `json:"uploaded"`
}

// caseEvidenceCategory menormalkan dan memeriksa kategori berkas.
func caseEvidenceCategory(raw string) (string, *httpx.AppError) {
	category := strings.ToUpper(trimText(raw))
	if _, ok := caseEvidenceCategories[category]; !ok {
		appErr := httpx.ErrValidation("Kategori berkas harus BAI atau LAMPIRAN.")
		appErr.Fields = map[string]string{
			"category": "Kategori berkas harus BAI atau LAMPIRAN.",
		}
		return "", appErr
	}
	return category, nil
}

// caseEvidenceWritable membatasi unggahan BAI/lampiran pada tahap sebelum
// keputusan pejabat. Setelah kasus diputuskan, berkas tidak boleh ditambah.
func caseEvidenceWritable(status string) bool {
	switch workflow.Status(status) {
	case workflow.Penelaahan, workflow.Pemeriksaan, workflow.Rekomendasi:
		return true
	default:
		return false
	}
}

// UploadEvidence menyimpan BAI atau lampiran untuk kasus (FR-31).
//
// Validasi tipe berkas memakai aturan Phase 4.2 (deteksi isi, batas ukuran,
// object key acak). Objek diunggah lebih dulu, lalu metadata dan audit ditulis
// dalam satu transaksi; bila transaksi gagal, objek yang sudah terunggah
// dibersihkan.
func (s *Service) UploadEvidence(
	ctx context.Context,
	actor Actor,
	reportID int64,
	rawCategory string,
	files []*multipart.FileHeader,
	ip string,
) (UploadResult, error) {
	if actor.Role != auth.RoleSatgas {
		return UploadResult{}, httpx.ErrForbidden()
	}
	category, appErr := caseEvidenceCategory(rawCategory)
	if appErr != nil {
		return UploadResult{}, appErr
	}
	if len(files) == 0 {
		appErr := httpx.ErrValidation("Minimal satu berkas harus diunggah.")
		appErr.Fields = map[string]string{
			"evidences": "Minimal satu berkas harus diunggah.",
		}
		return UploadResult{}, appErr
	}
	if s.evidence == nil {
		return UploadResult{}, errors.New("review: layanan evidence tidak tersedia")
	}

	// Periksa akses sebelum mengunggah objek agar permintaan tanpa hak tidak
	// membuang ruang penyimpanan.
	access, err := s.loadCase(ctx, s.pool, reportID)
	if err != nil {
		return UploadResult{}, err
	}
	if err := checkAssignee(access, actor.UserID); err != nil {
		return UploadResult{}, err
	}
	if !caseEvidenceWritable(access.status) {
		return UploadResult{}, httpx.ErrConflict("Berkas tidak dapat ditambahkan pada tahap kasus ini.")
	}

	validated, err := s.evidence.Validate(files)
	if err != nil {
		return UploadResult{}, err
	}

	stored, cleanup, err := s.evidence.StoreFiles(ctx, validated)
	if err != nil {
		return UploadResult{}, err
	}
	committed := false
	defer func() {
		if !committed {
			cleanup()
		}
	}()

	err = db.WithTx(ctx, s.pool, func(tx pgx.Tx) error {
		current, err := s.requireAssignee(ctx, tx, reportID, actor.UserID)
		if err != nil {
			return err
		}
		if !caseEvidenceWritable(current.status) {
			return httpx.ErrConflict("Berkas tidak dapat ditambahkan pada tahap kasus ini.")
		}

		if err := s.evidence.SaveMetadata(ctx, tx, reportID, category, stored, &actor.UserID); err != nil {
			return err
		}

		return s.audit.Log(ctx, tx, audit.Event{
			ActorType: audit.ActorUser,
			UserID:    &actor.UserID,
			Action:    audit.ActionEvidenceUploaded,
			Entity:    "report",
			EntityID:  &reportID,
			IP:        ip,
			Detail: map[string]any{
				"category": category,
				"count":    len(stored),
			},
		})
	})
	if err != nil {
		return UploadResult{}, err
	}
	committed = true

	return UploadResult{Category: category, Uploaded: len(stored)}, nil
}
