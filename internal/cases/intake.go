package cases

import (
	"context"
	"errors"
	"fmt"
	"mime/multipart"
	"strings"
	"time"

	"github.com/RidwanRamdhani/ppkpt-backend/internal/audit"
	"github.com/RidwanRamdhani/ppkpt-backend/internal/auth"
	"github.com/RidwanRamdhani/ppkpt-backend/internal/deadline"
	"github.com/RidwanRamdhani/ppkpt-backend/internal/evidence"
	"github.com/RidwanRamdhani/ppkpt-backend/internal/platform/db"
	"github.com/RidwanRamdhani/ppkpt-backend/internal/platform/httpx"
	"github.com/RidwanRamdhani/ppkpt-backend/internal/report"
	"github.com/jackc/pgx/v5"
	"golang.org/x/crypto/bcrypt"
)

const maxTicketAttempts = 5

// errTicketCollision menandai bentrok case_number sehingga transaksi diulang
// dengan tiket baru.
var errTicketCollision = errors.New("cases: kode tiket bentrok")

// CreateResult adalah respons POST /api/cases. PIN hanya ditampilkan sekali
// dan harus diberikan petugas kepada pelapor.
type CreateResult struct {
	CaseNumber string `json:"case_number"`
	PIN        string `json:"pin"`
}

// CreateManual menyimpan laporan yang diinput petugas Satgas.
//
// Berkas bersifat opsional. Bila ada, objek diunggah lebih dulu lalu metadata
// ditulis dalam transaksi yang sama; kegagalan transaksi menghapus objek yang
// sudah terunggah. Tiket dan PIN tetap dibuat agar pelapor dapat
// mengecek status seperti laporan portal.
func (s *Service) CreateManual(
	ctx context.Context,
	actor Actor,
	req ManualRequest,
	files []*multipart.FileHeader,
	ip string,
) (CreateResult, error) {
	if actor.Role != auth.RoleSatgas {
		return CreateResult{}, httpx.ErrForbidden()
	}

	input, appErr := req.Validate(s.now())
	if appErr != nil {
		return CreateResult{}, appErr
	}

	var validated []evidence.File
	if len(files) > 0 {
		if s.evidence == nil {
			return CreateResult{}, httpx.ErrInternal()
		}
		var err error
		validated, err = s.evidence.Validate(files)
		if err != nil {
			return CreateResult{}, err
		}
	}

	ticket, pin, pinHash, err := s.newCredentials()
	if err != nil {
		return CreateResult{}, err
	}

	var stored []evidence.Stored
	cleanup := func() {}
	if len(validated) > 0 {
		stored, cleanup, err = s.evidence.StoreFiles(ctx, validated)
		if err != nil {
			return CreateResult{}, err
		}
	}

	additional := mergeOfficerNote(input.Submission.AdditionalDescription, input.OfficerNote, s.now())

	var reportID int64
	for attempt := 0; attempt < maxTicketAttempts; attempt++ {
		if attempt > 0 {
			if ticket, err = report.GenerateTicket(); err != nil {
				cleanup()
				return CreateResult{}, err
			}
		}
		reportID, err = s.insertManual(ctx, actor.UserID, input, ticket, pinHash, stored, additional, ip)
		if errors.Is(err, errTicketCollision) {
			continue
		}
		break
	}
	if err != nil {
		cleanup()
		if errors.Is(err, errTicketCollision) {
			s.logger.Error("kode tiket input manual terus bentrok", "attempts", maxTicketAttempts)
			return CreateResult{}, httpx.ErrInternal()
		}
		return CreateResult{}, err
	}

	// Email bukti hanya dikirim bila petugas mengisi email pelapor. Kegagalan
	// email tidak memengaruhi keberhasilan input.
	if s.notifier != nil {
		s.notifier.SendReportReceipt(input.Submission.Reporter.Email, ticket, pin)
	}

	s.logger.Info("laporan input manual diterima", "report_id", reportID, "channel", input.Channel)
	return CreateResult{CaseNumber: ticket, PIN: pin}, nil
}

// newCredentials membuat tiket, PIN, dan hash PIN untuk laporan manual.
func (s *Service) newCredentials() (ticket, pin, pinHash string, err error) {
	if ticket, err = report.GenerateTicket(); err != nil {
		return "", "", "", err
	}
	if pin, err = report.GeneratePIN(); err != nil {
		return "", "", "", err
	}
	hash, err := bcrypt.GenerateFromPassword([]byte(pin), s.bcryptCost)
	if err != nil {
		return "", "", "", fmt.Errorf("cases: hash PIN: %w", err)
	}
	return ticket, pin, string(hash), nil
}

// mergeOfficerNote menggabungkan catatan petugas ke additional_description
// dengan penanda waktu (keputusan D4).
func mergeOfficerNote(base, note string, now time.Time) string {
	base = strings.TrimSpace(base)
	note = strings.TrimSpace(note)
	if note == "" {
		return base
	}
	marker := fmt.Sprintf("[Petugas %s WIB] %s",
		now.In(deadline.Jakarta).Format("2006-01-02 15:04"), note)
	if base == "" {
		return marker
	}
	return base + "\n\n" + marker
}

// insertManual menulis laporan manual lengkap dalam satu transaksi.
func (s *Service) insertManual(
	ctx context.Context,
	userID int64,
	input ManualInput,
	caseNumber, pinHash string,
	stored []evidence.Stored,
	additional string,
	ip string,
) (int64, error) {
	var reportID int64
	err := db.WithTx(ctx, s.pool, func(tx pgx.Tx) error {
		id, err := insertManualReport(ctx, tx, userID, input, caseNumber, pinHash, additional)
		if err != nil {
			return err
		}
		reportID = id

		if err := insertManualReporter(ctx, tx, id, input.Submission.Reporter); err != nil {
			return err
		}
		if err := insertManualParties(ctx, tx, id, input.Submission); err != nil {
			return err
		}
		for _, incident := range input.Submission.Incidents {
			if err := insertManualIncident(ctx, tx, id, incident); err != nil {
				return err
			}
		}
		if err := insertManualViolationTypes(ctx, tx, id, input.Submission.ViolationTypes); err != nil {
			return err
		}
		if len(stored) > 0 {
			if err := s.evidence.SaveMetadata(ctx, tx, id, evidence.CategoryBukti, stored, &userID); err != nil {
				return err
			}
		}

		if _, err := tx.Exec(ctx, `
			INSERT INTO status_history (report_id, from_status, to_status, changed_by)
			VALUES ($1, NULL, 'DILAPORKAN', $2)`, id, userID); err != nil {
			return fmt.Errorf("cases: catat status awal: %w", err)
		}

		return s.audit.Log(ctx, tx, audit.Event{
			ActorType: audit.ActorUser,
			UserID:    &userID,
			Action:    audit.ActionCaseCreated,
			Entity:    "report",
			EntityID:  &id,
			IP:        ip,
			Detail: map[string]any{
				"channel":   input.Channel,
				"take_case": input.TakeCase,
			},
		})
	})
	if err != nil {
		return 0, err
	}
	return reportID, nil
}

const insertManualReportSQL = `
	INSERT INTO reports
		(case_number, pin_hash, channel, referral_unit, status, reporter_confidential,
		 reporter_is_victim, chronology, additional_description, reported_elsewhere,
		 reported_to, efforts_made, expectation, assigned_to, entered_by, received_at)
	VALUES ($1, $2, $3, $4, 'DILAPORKAN', $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15)
	ON CONFLICT (case_number) DO NOTHING
	RETURNING id`

func insertManualReport(
	ctx context.Context,
	tx pgx.Tx,
	userID int64,
	input ManualInput,
	caseNumber, pinHash, additional string,
) (int64, error) {
	var assignedTo any
	if input.TakeCase {
		assignedTo = userID
	}

	var id int64
	err := tx.QueryRow(ctx, insertManualReportSQL,
		caseNumber,
		pinHash,
		input.Channel,
		nullIfEmpty(input.ReferralUnit),
		input.ReporterConfidential,
		input.Submission.ReporterIsVictim,
		input.Submission.Chronology,
		nullIfEmpty(additional),
		input.Submission.ReportedElsewhere,
		nullIfEmpty(input.Submission.ReportedTo),
		nullIfEmpty(input.Submission.EffortsMade),
		nullIfEmpty(input.Submission.Expectation),
		assignedTo,
		userID,
		input.ReceivedAt,
	).Scan(&id)
	if errors.Is(err, pgx.ErrNoRows) {
		return 0, errTicketCollision
	}
	if err != nil {
		return 0, fmt.Errorf("cases: simpan laporan manual: %w", err)
	}
	return id, nil
}

func insertManualReporter(ctx context.Context, tx pgx.Tx, reportID int64, r report.ReporterInput) error {
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
		return fmt.Errorf("cases: simpan pelapor manual: %w", err)
	}
	return nil
}

func insertManualParties(ctx context.Context, tx pgx.Tx, reportID int64, sub report.Submission) error {
	victim := partyFromReporter(sub.Reporter)
	if !sub.ReporterIsVictim && sub.Victim != nil {
		victim = *sub.Victim
	}
	if err := insertManualParty(ctx, tx, reportID, "KORBAN", victim); err != nil {
		return err
	}
	for _, accused := range sub.Accused {
		if err := insertManualParty(ctx, tx, reportID, "TERLAPOR", accused); err != nil {
			return err
		}
	}
	return nil
}

func partyFromReporter(r report.ReporterInput) report.PersonInput {
	return report.PersonInput{
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

func insertManualParty(ctx context.Context, tx pgx.Tx, reportID int64, partyType string, p report.PersonInput) error {
	_, err := tx.Exec(ctx, `
		INSERT INTO parties
			(report_id, party_type, name, gender, id_number, category, institution, faculty, program, contact, relation_to_other)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11)`,
		reportID,
		partyType,
		nullIfEmpty(p.Name),
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
		return fmt.Errorf("cases: simpan pihak %s: %w", partyType, err)
	}
	return nil
}

func insertManualIncident(ctx context.Context, tx pgx.Tx, reportID int64, inc report.Incident) error {
	var dateEnd any
	if inc.DateEnd != nil {
		dateEnd = *inc.DateEnd
	}
	_, err := tx.Exec(ctx, `
		INSERT INTO incidents (report_id, date_start, date_end, description, location)
		VALUES ($1, $2, $3, $4, $5)`,
		reportID, inc.DateStart, dateEnd, inc.Description, inc.Location)
	if err != nil {
		return fmt.Errorf("cases: simpan kejadian manual: %w", err)
	}
	return nil
}

func insertManualViolationTypes(ctx context.Context, tx pgx.Tx, reportID int64, codes []string) error {
	for _, code := range codes {
		tag, err := tx.Exec(ctx, `
			INSERT INTO report_violation_types (report_id, violation_type_id)
			SELECT $1, id FROM violation_types WHERE code = $2`, reportID, code)
		if err != nil {
			return fmt.Errorf("cases: simpan jenis dugaan: %w", err)
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
func nullIfEmpty(value string) *string {
	if value == "" {
		return nil
	}
	return &value
}
