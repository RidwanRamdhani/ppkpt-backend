package cases

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"unicode/utf8"

	"github.com/RidwanRamdhani/ppkpt-backend/internal/audit"
	"github.com/RidwanRamdhani/ppkpt-backend/internal/auth"
	"github.com/RidwanRamdhani/ppkpt-backend/internal/platform/db"
	"github.com/RidwanRamdhani/ppkpt-backend/internal/platform/httpx"
	"github.com/jackc/pgx/v5"
)

const (
	maxPartyNameRunes     = 100
	maxPartyIDNumberRunes = 30
	maxPartyGenderRunes   = 15
	maxPartyCategoryRunes = 30
	maxPartyUnitRunes     = 100
	maxPartyContactRunes  = 120
	maxPartyRelationRunes = 100
)

// validAddPartyTypes adalah tipe pihak yang boleh ditambahkan setelah intake.
// Korban dan terlapor sudah dibuat saat laporan disimpan; endpoint ini dipakai
// untuk saksi dan ahli (FR-27).
var validAddPartyTypes = map[string]struct{}{
	"SAKSI": {},
	"AHLI":  {},
}

// validPartyGenders dan validPartyCategories mengikuti CHECK constraint
// parties serta enum form pelaporan.
var validPartyGenders = map[string]struct{}{
	"PEREMPUAN": {},
	"LAKI_LAKI": {},
	"LAINNYA":   {},
}

var validPartyCategories = map[string]struct{}{
	"MAHASISWA":           {},
	"DOSEN":               {},
	"TENAGA_KEPENDIDIKAN": {},
	"LAINNYA":             {},
}

// PartyInput adalah body POST /api/cases/{id}/parties.
type PartyInput struct {
	PartyType       string `json:"party_type"`
	Name            string `json:"name"`
	Gender          string `json:"gender"`
	Category        string `json:"category"`
	IDNumber        string `json:"id_number"`
	Institution     string `json:"institution"`
	Faculty         string `json:"faculty"`
	Program         string `json:"program"`
	Contact         string `json:"contact"`
	RelationToOther string `json:"relation_to_other"`
}

// PartyUpdate adalah body PUT /api/cases/{id}/parties/{partyId}. Field yang
// tidak dikirim tidak diubah; string kosong mengosongkan kolom opsional.
type PartyUpdate struct {
	Name            *string `json:"name"`
	Gender          *string `json:"gender"`
	Category        *string `json:"category"`
	IDNumber        *string `json:"id_number"`
	Institution     *string `json:"institution"`
	Faculty         *string `json:"faculty"`
	Program         *string `json:"program"`
	Contact         *string `json:"contact"`
	RelationToOther *string `json:"relation_to_other"`
}

// Validate memeriksa pihak baru (saksi/ahli).
func (p PartyInput) Validate() (PartyInput, *httpx.AppError) {
	fields := make(map[string]string)

	normalized := PartyInput{
		PartyType:       strings.ToUpper(strings.TrimSpace(p.PartyType)),
		Name:            strings.TrimSpace(p.Name),
		Gender:          strings.TrimSpace(p.Gender),
		Category:        strings.ToUpper(strings.TrimSpace(p.Category)),
		IDNumber:        strings.TrimSpace(p.IDNumber),
		Institution:     strings.TrimSpace(p.Institution),
		Faculty:         strings.TrimSpace(p.Faculty),
		Program:         strings.TrimSpace(p.Program),
		Contact:         strings.TrimSpace(p.Contact),
		RelationToOther: strings.TrimSpace(p.RelationToOther),
	}

	if _, ok := validAddPartyTypes[normalized.PartyType]; !ok {
		fields["party_type"] = "Tipe pihak harus SAKSI atau AHLI."
	}
	if normalized.Name == "" {
		fields["name"] = "Nama pihak wajib diisi."
	}
	validatePartyFields(fields, normalized.Gender, normalized.Category,
		normalized.IDNumber, normalized.Institution, normalized.Faculty,
		normalized.Program, normalized.Contact, normalized.RelationToOther)
	maxRunesField(fields, "name", normalized.Name, maxPartyNameRunes)

	if len(fields) > 0 {
		appErr := httpx.ErrValidation("Data pihak belum lengkap atau tidak valid.")
		appErr.Fields = fields
		return PartyInput{}, appErr
	}
	return normalized, nil
}

// Validate memeriksa perubahan identitas pihak. Minimal satu field harus
// dikirim agar permintaan kosong tidak menghasilkan UPDATE tanpa efek.
func (p PartyUpdate) Validate() (PartyUpdate, *httpx.AppError) {
	fields := make(map[string]string)

	normalized := PartyUpdate{
		Name:            trimPtr(p.Name),
		Gender:          trimPtr(p.Gender),
		Category:        upperPtr(p.Category),
		IDNumber:        trimPtr(p.IDNumber),
		Institution:     trimPtr(p.Institution),
		Faculty:         trimPtr(p.Faculty),
		Program:         trimPtr(p.Program),
		Contact:         trimPtr(p.Contact),
		RelationToOther: trimPtr(p.RelationToOther),
	}

	if normalized.Name == nil && normalized.Gender == nil && normalized.Category == nil &&
		normalized.IDNumber == nil && normalized.Institution == nil && normalized.Faculty == nil &&
		normalized.Program == nil && normalized.Contact == nil && normalized.RelationToOther == nil {
		fields["_"] = "Minimal satu field harus diisi."
	}
	if normalized.Name != nil && *normalized.Name == "" {
		fields["name"] = "Nama pihak tidak boleh kosong."
	}
	if normalized.Name != nil {
		maxRunesField(fields, "name", *normalized.Name, maxPartyNameRunes)
	}

	gender, category := "", ""
	if normalized.Gender != nil {
		gender = *normalized.Gender
	}
	if normalized.Category != nil {
		category = *normalized.Category
	}
	validatePartyFields(fields, gender, category,
		valueOrEmpty(normalized.IDNumber), valueOrEmpty(normalized.Institution),
		valueOrEmpty(normalized.Faculty), valueOrEmpty(normalized.Program),
		valueOrEmpty(normalized.Contact), valueOrEmpty(normalized.RelationToOther))

	if len(fields) > 0 {
		appErr := httpx.ErrValidation("Data pihak belum lengkap atau tidak valid.")
		appErr.Fields = fields
		return PartyUpdate{}, appErr
	}
	return normalized, nil
}

// validatePartyFields memeriksa enum dan panjang field identitas. Nilai kosong
// diperbolehkan untuk field opsional.
func validatePartyFields(
	fields map[string]string,
	gender, category, idNumber, institution, faculty, program, contact, relation string,
) {
	if gender != "" {
		if _, ok := validPartyGenders[gender]; !ok {
			fields["gender"] = "Jenis kelamin harus PEREMPUAN, LAKI_LAKI, atau LAINNYA."
		}
	}
	if category != "" {
		if _, ok := validPartyCategories[category]; !ok {
			fields["category"] = "Kategori pihak tidak dikenal."
		}
	}
	maxRunesField(fields, "gender", gender, maxPartyGenderRunes)
	maxRunesField(fields, "category", category, maxPartyCategoryRunes)
	maxRunesField(fields, "id_number", idNumber, maxPartyIDNumberRunes)
	maxRunesField(fields, "institution", institution, maxPartyUnitRunes)
	maxRunesField(fields, "faculty", faculty, maxPartyUnitRunes)
	maxRunesField(fields, "program", program, maxPartyUnitRunes)
	maxRunesField(fields, "contact", contact, maxPartyContactRunes)
	maxRunesField(fields, "relation_to_other", relation, maxPartyRelationRunes)
}

// AddParty menambahkan saksi/ahli ke kasus (FR-27).
func (s *Service) AddParty(
	ctx context.Context,
	actor Actor,
	reportID int64,
	input PartyInput,
	ip string,
) (PartyView, error) {
	if actor.Role != auth.RoleSatgas {
		return PartyView{}, httpx.ErrForbidden()
	}
	party, appErr := input.Validate()
	if appErr != nil {
		return PartyView{}, appErr
	}

	var partyID int64
	err := db.WithTx(ctx, s.pool, func(tx pgx.Tx) error {
		if _, err := s.requireAssignee(ctx, tx, reportID, actor.UserID); err != nil {
			return err
		}

		if err := tx.QueryRow(ctx, `
			INSERT INTO parties
				(report_id, party_type, name, gender, id_number, category,
				 institution, faculty, program, contact, relation_to_other, added_by)
			VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12)
			RETURNING id`,
			reportID,
			party.PartyType,
			party.Name,
			nullIfEmpty(party.Gender),
			nullIfEmpty(party.IDNumber),
			nullIfEmpty(party.Category),
			nullIfEmpty(party.Institution),
			nullIfEmpty(party.Faculty),
			nullIfEmpty(party.Program),
			nullIfEmpty(party.Contact),
			nullIfEmpty(party.RelationToOther),
			actor.UserID,
		).Scan(&partyID); err != nil {
			return fmt.Errorf("cases: simpan pihak baru: %w", err)
		}

		return s.audit.Log(ctx, tx, audit.Event{
			ActorType: audit.ActorUser,
			UserID:    &actor.UserID,
			Action:    audit.ActionPartyAdded,
			Entity:    "report",
			EntityID:  &reportID,
			IP:        ip,
			Detail:    map[string]any{"party_id": partyID, "party_type": party.PartyType},
		})
	})
	if err != nil {
		return PartyView{}, err
	}

	return PartyView{
		ID:              partyID,
		PartyType:       party.PartyType,
		Name:            party.Name,
		Gender:          party.Gender,
		IDNumber:        party.IDNumber,
		Category:        party.Category,
		Institution:     party.Institution,
		Faculty:         party.Faculty,
		Program:         party.Program,
		Contact:         party.Contact,
		RelationToOther: party.RelationToOther,
	}, nil
}

// UpdateParty melengkapi identitas pihak (FR-27). partyId harus milik kasus
// pada path; ID pihak dari kasus lain menghasilkan 404 (bukan 403) agar tidak
// membocorkan keberadaan data.
func (s *Service) UpdateParty(
	ctx context.Context,
	actor Actor,
	reportID, partyID int64,
	input PartyUpdate,
	ip string,
) (PartyView, error) {
	if actor.Role != auth.RoleSatgas {
		return PartyView{}, httpx.ErrForbidden()
	}
	update, appErr := input.Validate()
	if appErr != nil {
		return PartyView{}, appErr
	}

	var party PartyView
	err := db.WithTx(ctx, s.pool, func(tx pgx.Tx) error {
		if _, err := s.requireAssignee(ctx, tx, reportID, actor.UserID); err != nil {
			return err
		}

		query, args := buildPartyUpdateSQL(partyID, reportID, update)
		tag, err := tx.Exec(ctx, query, args...)
		if err != nil {
			return fmt.Errorf("cases: ubah pihak: %w", err)
		}
		if tag.RowsAffected() == 0 {
			return httpx.ErrNotFound()
		}

		party, err = loadPartyByID(ctx, tx, reportID, partyID)
		if err != nil {
			return err
		}

		return s.audit.Log(ctx, tx, audit.Event{
			ActorType: audit.ActorUser,
			UserID:    &actor.UserID,
			Action:    audit.ActionPartyUpdated,
			Entity:    "report",
			EntityID:  &reportID,
			IP:        ip,
			Detail:    map[string]any{"party_id": partyID},
		})
	})
	if err != nil {
		return PartyView{}, err
	}
	return party, nil
}

// buildPartyUpdateSQL menyusun UPDATE dari daftar kolom tetap; nilai selalu
// dikirim sebagai parameter. Hanya field yang dikirim klien yang diubah.
func buildPartyUpdateSQL(partyID, reportID int64, update PartyUpdate) (string, []any) {
	args := []any{partyID, reportID}
	sets := make([]string, 0, 9)

	add := func(column string, value *string) {
		if value == nil {
			return
		}
		args = append(args, nullIfEmpty(*value))
		sets = append(sets, fmt.Sprintf("%s = $%d", column, len(args)))
	}

	add("name", update.Name)
	add("gender", update.Gender)
	add("category", update.Category)
	add("id_number", update.IDNumber)
	add("institution", update.Institution)
	add("faculty", update.Faculty)
	add("program", update.Program)
	add("contact", update.Contact)
	add("relation_to_other", update.RelationToOther)

	return "UPDATE parties SET " + strings.Join(sets, ", ") +
		" WHERE id = $1 AND report_id = $2", args
}

const partyByIDSQL = `
SELECT id, party_type, COALESCE(name, ''), COALESCE(gender, ''),
       COALESCE(id_number, ''), COALESCE(category, ''),
       COALESCE(institution, ''), COALESCE(faculty, ''),
       COALESCE(program, ''), COALESCE(contact, ''),
       COALESCE(relation_to_other, '')
FROM parties
WHERE report_id = $1 AND id = $2`

func loadPartyByID(ctx context.Context, q db.DBTX, reportID, partyID int64) (PartyView, error) {
	var party PartyView
	err := q.QueryRow(ctx, partyByIDSQL, reportID, partyID).Scan(
		&party.ID,
		&party.PartyType,
		&party.Name,
		&party.Gender,
		&party.IDNumber,
		&party.Category,
		&party.Institution,
		&party.Faculty,
		&party.Program,
		&party.Contact,
		&party.RelationToOther,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return PartyView{}, httpx.ErrNotFound()
	}
	if err != nil {
		return PartyView{}, fmt.Errorf("cases: baca pihak: %w", err)
	}
	return party, nil
}

func trimPtr(value *string) *string {
	if value == nil {
		return nil
	}
	trimmed := strings.TrimSpace(*value)
	return &trimmed
}

func upperPtr(value *string) *string {
	if value == nil {
		return nil
	}
	trimmed := strings.ToUpper(strings.TrimSpace(*value))
	return &trimmed
}

func valueOrEmpty(value *string) string {
	if value == nil {
		return ""
	}
	return *value
}

func maxRunesField(fields map[string]string, field, value string, limit int) {
	if utf8.RuneCountInString(value) > limit {
		fields[field] = fmt.Sprintf("Maksimal %d karakter.", limit)
	}
}
