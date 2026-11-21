package report

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"mime/multipart"
	"time"

	"github.com/RidwanRamdhani/ppkpt-backend/internal/audit"
	"github.com/RidwanRamdhani/ppkpt-backend/internal/evidence"
	"github.com/RidwanRamdhani/ppkpt-backend/internal/notification"
	"github.com/RidwanRamdhani/ppkpt-backend/internal/platform/db"
	"github.com/RidwanRamdhani/ppkpt-backend/internal/platform/httpx"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"golang.org/x/crypto/bcrypt"
)

const (
	// maxTicketAttempts membatasi pengulangan saat kode tiket kebetulan sudah
	// dipakai laporan lain (peluangnya sangat kecil).
	maxTicketAttempts = 5
)

// errTicketCollision menandai bentrok case_number sehingga transaksi
// diulang dengan tiket baru.
var errTicketCollision = errors.New("report: kode tiket bentrok")

// Options mengatur perilaku layanan pelaporan.
type Options struct {
	BcryptCost int // biaya bcrypt untuk hash PIN; default 12
}

// Result adalah respons 201 berisi tiket dan PIN. PIN hanya diberikan sekali.
type Result struct {
	CaseNumber string `json:"case_number"`
	PIN        string `json:"pin"`
}

// Service mengelola pengiriman laporan portal.
type Service struct {
	pool       *pgxpool.Pool
	evidence   *evidence.Service
	auditor    *audit.Service
	notifier   *notification.Service
	logger     *slog.Logger
	bcryptCost int
	now        func() time.Time
}

// NewService membuat layanan pelaporan. auditor/notifier/logger boleh nil agar
// mudah diuji; evidence wajib karena seluruh laporan portal menyertakan bukti.
func NewService(
	pool *pgxpool.Pool,
	evidenceSvc *evidence.Service,
	auditor *audit.Service,
	notifier *notification.Service,
	logger *slog.Logger,
	opts Options,
) *Service {
	if auditor == nil {
		auditor = audit.NewService()
	}
	if logger == nil {
		logger = slog.Default()
	}
	cost := opts.BcryptCost
	if cost < bcrypt.MinCost || cost > bcrypt.MaxCost {
		cost = bcrypt.DefaultCost
	}
	return &Service{
		pool:       pool,
		evidence:   evidenceSvc,
		auditor:    auditor,
		notifier:   notifier,
		logger:     logger,
		bcryptCost: cost,
		now:        time.Now,
	}
}

// Submit memvalidasi payload, menyimpan berkas bukti, lalu menulis seluruh data
// laporan dalam satu transaksi.
//
// Urutan penting (NFR-21): validasi dulu, unggah objek, transaksi database, dan
// bila transaksi gagal objek yang sudah terunggah dihapus. Email bukti
// pelaporan dikirim setelah commit; kegagalannya tidak memengaruhi respons.
func (s *Service) Submit(ctx context.Context, req SubmitRequest, files []*multipart.FileHeader, ip string) (Result, error) {
	sub, appErr := req.Validate(s.now())
	if appErr != nil {
		return Result{}, appErr
	}

	validated, err := s.evidence.Validate(files)
	if err != nil {
		return Result{}, err
	}
	if len(validated) == 0 {
		appErr = httpx.ErrValidation("Minimal satu berkas bukti wajib dilampirkan.")
		appErr.Fields = map[string]string{"evidences": appErr.Message}
		return Result{}, appErr
	}

	// Siapkan tiket, PIN, dan hash sebelum mengunggah agar kegagalan acak/hash
	// tidak meninggalkan objek di bucket.
	ticket, pin, pinHash, err := s.newCredentials()
	if err != nil {
		return Result{}, err
	}

	stored, cleanup, err := s.evidence.StoreFiles(ctx, validated)
	if err != nil {
		return Result{}, err
	}

	var reportID int64
	for attempt := 0; attempt < maxTicketAttempts; attempt++ {
		if attempt > 0 {
			if ticket, err = GenerateTicket(); err != nil {
				cleanup()
				return Result{}, err
			}
		}
		reportID, err = s.insert(ctx, sub, ticket, pinHash, stored, ip)
		if errors.Is(err, errTicketCollision) {
			continue
		}
		break
	}
	if err != nil {
		cleanup()
		if errors.Is(err, errTicketCollision) {
			// Praktis tidak mungkin terjadi; jangan bocorkan detail ke klien.
			s.logger.Error("kode tiket terus bentrok", "attempts", maxTicketAttempts)
			return Result{}, httpx.ErrInternal()
		}
		return Result{}, err
	}

	if s.notifier != nil {
		s.notifier.SendReportReceipt(sub.Reporter.Email, ticket, pin)
	}

	s.logger.Info("laporan diterima", "report_id", reportID)
	return Result{CaseNumber: ticket, PIN: pin}, nil
}

// newCredentials membuat tiket, PIN, dan hash PIN sekali per pengiriman.
func (s *Service) newCredentials() (ticket, pin, pinHash string, err error) {
	if ticket, err = GenerateTicket(); err != nil {
		return "", "", "", err
	}
	if pin, err = GeneratePIN(); err != nil {
		return "", "", "", err
	}
	hash, err := bcrypt.GenerateFromPassword([]byte(pin), s.bcryptCost)
	if err != nil {
		return "", "", "", fmt.Errorf("report: hash PIN: %w", err)
	}
	return ticket, pin, string(hash), nil
}

// insert menulis laporan lengkap dalam satu transaksi. Mengembalikan
// errTicketCollision bila case_number sudah dipakai.
func (s *Service) insert(ctx context.Context, sub Submission, caseNumber, pinHash string, stored []evidence.Stored, ip string) (int64, error) {
	var reportID int64
	err := db.WithTx(ctx, s.pool, func(tx pgx.Tx) error {
		id, err := insertReport(ctx, tx, sub, caseNumber, pinHash)
		if err != nil {
			return err
		}
		reportID = id

		if err := insertReporter(ctx, tx, id, sub.Reporter); err != nil {
			return err
		}
		if err := insertParties(ctx, tx, id, sub); err != nil {
			return err
		}
		for _, inc := range sub.Incidents {
			if err := insertIncident(ctx, tx, id, inc); err != nil {
				return err
			}
		}
		if err := insertViolationTypes(ctx, tx, id, sub.ViolationTypes); err != nil {
			return err
		}
		if err := s.evidence.SaveMetadata(ctx, tx, id, evidence.CategoryBukti, stored, nil); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `
			INSERT INTO status_history (report_id, from_status, to_status, changed_by)
			VALUES ($1, NULL, 'DILAPORKAN', NULL)`, id); err != nil {
			return fmt.Errorf("report: catat status awal: %w", err)
		}

		return s.auditor.Log(ctx, tx, audit.Event{
			ActorType: audit.ActorReporter,
			Action:    audit.ActionReportSubmitted,
			Entity:    "report",
			EntityID:  &id,
			IP:        ip,
			Detail: map[string]any{
				"channel":         "PORTAL",
				"violation_types": sub.ViolationTypes,
			},
		})
	})
	if err != nil {
		return 0, err
	}
	return reportID, nil
}

const insertReportSQL = `
	INSERT INTO reports
		(case_number, pin_hash, channel, status, reporter_is_victim, chronology,
		 additional_description, reported_elsewhere, reported_to, efforts_made,
		 expectation, consent_at, consent_text_version)
	VALUES ($1, $2, 'PORTAL', 'DILAPORKAN', $3, $4, $5, $6, $7, $8, $9, now(), $10)
	ON CONFLICT (case_number) DO NOTHING
	RETURNING id`

func insertReport(ctx context.Context, tx pgx.Tx, sub Submission, caseNumber, pinHash string) (int64, error) {
	var id int64
	err := tx.QueryRow(ctx, insertReportSQL,
		caseNumber,
		pinHash,
		sub.ReporterIsVictim,
		sub.Chronology,
		nullIfEmpty(sub.AdditionalDescription),
		sub.ReportedElsewhere,
		nullIfEmpty(sub.ReportedTo),
		nullIfEmpty(sub.EffortsMade),
		nullIfEmpty(sub.Expectation),
		sub.ConsentTextVersion,
	).Scan(&id)
	if errors.Is(err, pgx.ErrNoRows) {
		return 0, errTicketCollision
	}
	if err != nil {
		return 0, fmt.Errorf("report: simpan laporan: %w", err)
	}
	return id, nil
}

func insertReporter(ctx context.Context, tx pgx.Tx, reportID int64, r ReporterInput) error {
	_, err := tx.Exec(ctx, `
		INSERT INTO reporters
			(report_id, name, age, gender, id_number, category, institution, faculty, program, phone, email)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11)`,
		reportID,
		r.Name,
		r.Age,
		nullIfEmpty(r.Gender),
		nullIfEmpty(r.IDNumber),
		nullIfEmpty(r.Category),
		nullIfEmpty(r.Institution),
		nullIfEmpty(r.Faculty),
		nullIfEmpty(r.Program),
		nullIfEmpty(r.Phone),
		nullIfEmpty(r.Email),
	)
	if err != nil {
		return fmt.Errorf("report: simpan pelapor: %w", err)
	}
	return nil
}

// insertParties menulis korban dan seluruh terlapor. Bila pelapor adalah
// korban, satu baris KORBAN disalin dari data pelapor agar dapat dipakai pada
// session_participants (Phase 8).
func insertParties(ctx context.Context, tx pgx.Tx, reportID int64, sub Submission) error {
	victim := partyFromReporter(sub.Reporter)
	if !sub.ReporterIsVictim && sub.Victim != nil {
		victim = *sub.Victim
	}
	if err := insertParty(ctx, tx, reportID, "KORBAN", victim); err != nil {
		return err
	}
	for _, accused := range sub.Accused {
		if err := insertParty(ctx, tx, reportID, "TERLAPOR", accused); err != nil {
			return err
		}
	}
	return nil
}

func partyFromReporter(r ReporterInput) PersonInput {
	return PersonInput{
		Name:        r.Name,
		Gender:      r.Gender,
		Category:    r.Category,
		IDNumber:    r.IDNumber,
		Institution: r.Institution,
		Faculty:     r.Faculty,
		Program:     r.Program,
		Contact:     r.Phone,
	}
}

func insertParty(ctx context.Context, tx pgx.Tx, reportID int64, partyType string, p PersonInput) error {
	_, err := tx.Exec(ctx, `
		INSERT INTO parties
			(report_id, party_type, name, gender, id_number, category, institution, faculty, program, contact, relation_to_other)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11)`,
		reportID,
		partyType,
		p.Name,
		nullIfEmpty(p.Gender),
		nullIfEmpty(p.IDNumber),
		nullIfEmpty(p.Category),
		nullIfEmpty(p.Institution),
		nullIfEmpty(p.Faculty),
		nullIfEmpty(p.Program),
		nullIfEmpty(p.Contact),
		nullIfEmpty(p.RelationToOther),
	)
	if err != nil {
		return fmt.Errorf("report: simpan pihak %s: %w", partyType, err)
	}
	return nil
}

func insertIncident(ctx context.Context, tx pgx.Tx, reportID int64, inc Incident) error {
	var dateEnd any
	if inc.DateEnd != nil {
		dateEnd = *inc.DateEnd
	}
	_, err := tx.Exec(ctx, `
		INSERT INTO incidents (report_id, date_start, date_end, description, location)
		VALUES ($1, $2, $3, $4, $5)`,
		reportID, inc.DateStart, dateEnd, inc.Description, inc.Location)
	if err != nil {
		return fmt.Errorf("report: simpan kejadian: %w", err)
	}
	return nil
}

func insertViolationTypes(ctx context.Context, tx pgx.Tx, reportID int64, codes []string) error {
	for _, code := range codes {
		tag, err := tx.Exec(ctx, `
			INSERT INTO report_violation_types (report_id, violation_type_id)
			SELECT $1, id FROM violation_types WHERE code = $2`, reportID, code)
		if err != nil {
			return fmt.Errorf("report: simpan jenis dugaan: %w", err)
		}
		if tag.RowsAffected() == 0 {
			appErr := httpx.ErrValidation("Jenis dugaan kekerasan tidak dikenal.")
			appErr.Fields = map[string]string{"violation_types": appErr.Message}
			return appErr
		}
	}
	return nil
}

// nullIfEmpty mengubah string kosong menjadi NULL agar kolom opsional tidak
// menyimpan nilai kosong yang tidak bermakna.
func nullIfEmpty(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}
