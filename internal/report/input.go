package report

import (
	"fmt"
	"net/mail"
	"regexp"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/RidwanRamdhani/ppkpt-backend/internal/platform/httpx"
)

// SubmitRequest adalah bentuk JSON bagian "payload" pada POST /api/reports.
// Nama field mengikuti kontrak frontend (Phase 6.1 panduan).
type SubmitRequest struct {
	Reporter              ReporterInput     `json:"reporter"`
	ReporterIsVictim      bool              `json:"reporter_is_victim"`
	Victim                *PersonInput      `json:"victim"`
	Accused               []PersonInput     `json:"accused"`
	Incidents             []IncidentRequest `json:"incidents"`
	ViolationTypes        []string          `json:"violation_types"`
	Chronology            string            `json:"chronology"`
	AdditionalDescription string            `json:"additional_description"`
	ReportedElsewhere     bool              `json:"reported_elsewhere"`
	ReportedTo            string            `json:"reported_to"`
	EffortsMade           string            `json:"efforts_made"`
	Expectation           string            `json:"expectation"`
	Consent               bool              `json:"consent"`
	ConsentTextVersion    string            `json:"consent_text_version"`
}

// ReporterInput adalah identitas pelapor.
type ReporterInput struct {
	Name        string `json:"name"`
	Age         *int   `json:"age"`
	Gender      string `json:"gender"`
	Category    string `json:"category"`
	IDNumber    string `json:"id_number"`
	Institution string `json:"institution"`
	Faculty     string `json:"faculty"`
	Program     string `json:"program"`
	Phone       string `json:"phone"`
	Email       string `json:"email"`
}

// PersonInput adalah identitas korban/terlapor (boleh tidak lengkap untuk
// terlapor, sesuai temuan #14).
type PersonInput struct {
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

// IncidentRequest adalah satu kejadian; tanggal memakai format YYYY-MM-DD.
type IncidentRequest struct {
	DateStart   string `json:"date_start"`
	DateEnd     string `json:"date_end"`
	Location    string `json:"location"`
	Description string `json:"description"`
}

// Submission adalah payload yang sudah dinormalisasi dan divalidasi, siap
// ditulis ke database. Tanggal kejadian sudah berbentuk time.Time.
type Submission struct {
	Reporter              ReporterInput
	ReporterIsVictim      bool
	Victim                *PersonInput
	Accused               []PersonInput
	Incidents             []Incident
	ViolationTypes        []string
	Chronology            string
	AdditionalDescription string
	ReportedElsewhere     bool
	ReportedTo            string
	EffortsMade           string
	Expectation           string
	ConsentTextVersion    string
}

// Incident adalah kejadian yang sudah divalidasi.
type Incident struct {
	DateStart   time.Time
	DateEnd     *time.Time
	Location    string
	Description string
}

const (
	maxNameRunes       = 100 // reporters.name, parties.name, institution
	maxIDNumberRunes   = 30
	maxGenderRunes     = 15
	maxCategoryRunes   = 30
	maxFacultyRunes    = 100
	maxProgramRunes    = 100
	maxPhoneRunes      = 20
	maxEmailRunes      = 120
	maxContactRunes    = 120
	maxRelationRunes   = 100
	maxLocationRunes   = 200
	maxReportedToRunes = 150
	maxShortTextRunes  = 5_000
	maxChronologyRunes = 20_000
	minVictimAge       = 15
	maxVictimAge       = 100
)

// Nilai gender yang diterima (enum ketat, tanpa normalisasi penulisan).
const (
	GenderPerempuan = "PEREMPUAN"
	GenderLakiLaki  = "LAKI_LAKI"
	GenderLainnya   = "LAINNYA"
)

// validCategories sesuai CHECK constraint reporters.category.
var validCategories = map[string]struct{}{
	"MAHASISWA":           {},
	"DOSEN":               {},
	"TENAGA_KEPENDIDIKAN": {},
	"LAINNYA":             {},
}

// validGenders adalah daftar tertutup nilai gender. Nilai lain ditolak agar
// data konsisten untuk agregasi dashboard.
var validGenders = map[string]struct{}{
	GenderPerempuan: {},
	GenderLakiLaki:  {},
	GenderLainnya:   {},
}

// validViolationCodes sesuai master violation_types pada migrasi 0001.
var validViolationCodes = map[string]struct{}{
	"FISIK":                    {},
	"PSIKIS":                   {},
	"PERUNDUNGAN":              {},
	"SEKSUAL":                  {},
	"DISKRIMINASI_INTOLERANSI": {},
	"KEBIJAKAN_KEKERASAN":      {},
}

// knownConsentVersions adalah versi teks persetujuan yang diterima server.
var knownConsentVersions = map[string]struct{}{"1.0": {}}

var (
	phonePattern = regexp.MustCompile(`^\+62[0-9]{7,13}$`)
	phoneCleaner = strings.NewReplacer(" ", "", "-", "", "(", "", ")", "", ".", "")
	jakartaZone  = time.FixedZone("WIB", 7*60*60)
)

// Validate menormalkan dan memeriksa seluruh payload sekaligus. Semua kesalahan
// dikumpulkan ke Fields agar frontend dapat menandai tiap isian (422).
//
// now dipakai untuk menolak tanggal kejadian di masa depan; pemanggil
// mengirimkannya agar mudah diuji.
//
// Catatan keputusan: field identitas yang tidak selalu dimiliki pelapor
// (id_number, faculty, program) opsional di server; name, age, gender,
// category, institution, phone, dan email wajib.
func (r SubmitRequest) Validate(now time.Time) (Submission, *httpx.AppError) {
	v := &validator{fields: make(map[string]string)}

	sub := Submission{
		Reporter:              r.Reporter.normalized(),
		ReporterIsVictim:      r.ReporterIsVictim,
		Victim:                r.Victim.normalizedPtr(),
		Accused:               normalizePeople(r.Accused),
		ViolationTypes:        normalizeCodes(r.ViolationTypes),
		Chronology:            strings.TrimSpace(r.Chronology),
		AdditionalDescription: strings.TrimSpace(r.AdditionalDescription),
		ReportedElsewhere:     r.ReportedElsewhere,
		ReportedTo:            strings.TrimSpace(r.ReportedTo),
		EffortsMade:           strings.TrimSpace(r.EffortsMade),
		Expectation:           strings.TrimSpace(r.Expectation),
		ConsentTextVersion:    strings.TrimSpace(r.ConsentTextVersion),
	}

	validateReporter(v, sub.Reporter)

	if !sub.ReporterIsVictim {
		if sub.Victim == nil {
			v.add("victim", "Data korban wajib diisi.")
		} else {
			validatePerson(v, "victim", *sub.Victim)
		}
	}

	if len(sub.Accused) == 0 {
		v.add("accused", "Minimal satu terlapor wajib diisi.")
	}
	for i, person := range sub.Accused {
		validatePerson(v, fmt.Sprintf("accused[%d]", i), person)
	}

	if len(r.Incidents) == 0 {
		v.add("incidents", "Minimal satu kejadian wajib diisi.")
	}
	sub.Incidents = make([]Incident, 0, len(r.Incidents))
	for i, inc := range r.Incidents {
		prefix := fmt.Sprintf("incidents[%d]", i)
		parsed, err := validateIncident(now, inc, prefix, v)
		if err == nil {
			sub.Incidents = append(sub.Incidents, parsed)
		}
	}

	if len(sub.ViolationTypes) == 0 {
		v.add("violation_types", "Minimal satu jenis dugaan kekerasan wajib dipilih.")
	}
	for _, code := range sub.ViolationTypes {
		if _, ok := validViolationCodes[code]; !ok {
			v.add("violation_types", "Jenis dugaan kekerasan tidak dikenal.")
			break
		}
	}

	v.required("chronology", sub.Chronology)
	v.maxRunes("chronology", sub.Chronology, maxChronologyRunes)

	v.maxRunes("additional_description", sub.AdditionalDescription, maxShortTextRunes)

	if sub.ReportedElsewhere {
		v.required("reported_to", sub.ReportedTo)
	}
	v.maxRunes("reported_to", sub.ReportedTo, maxReportedToRunes)
	v.maxRunes("efforts_made", sub.EffortsMade, maxShortTextRunes)
	v.maxRunes("expectation", sub.Expectation, maxShortTextRunes)

	if !r.Consent {
		v.add("consent", "Persetujuan wajib diberikan sebelum laporan dikirim.")
	}
	if _, ok := knownConsentVersions[sub.ConsentTextVersion]; !ok {
		v.add("consent_text_version", "Versi teks persetujuan tidak dikenal.")
	}

	if len(v.fields) > 0 {
		appErr := httpx.ErrValidation("Data laporan belum lengkap atau tidak valid.")
		appErr.Fields = v.fields
		return Submission{}, appErr
	}
	return sub, nil
}

func validateReporter(v *validator, r ReporterInput) {
	v.required("reporter.name", r.Name)
	v.maxRunes("reporter.name", r.Name, maxNameRunes)

	if r.Age == nil {
		v.add("reporter.age", "Usia wajib diisi.")
	} else if *r.Age < minVictimAge || *r.Age > maxVictimAge {
		v.add("reporter.age", "Usia tidak masuk akal.")
	}

	validateGender(v, "reporter.gender", r.Gender, true)

	v.required("reporter.category", r.Category)
	if r.Category != "" {
		if _, ok := validCategories[r.Category]; !ok {
			v.add("reporter.category", "Kategori pelapor tidak dikenal.")
		}
	}
	v.maxRunes("reporter.category", r.Category, maxCategoryRunes)

	v.maxRunes("reporter.id_number", r.IDNumber, maxIDNumberRunes)
	v.required("reporter.institution", r.Institution)
	v.maxRunes("reporter.institution", r.Institution, maxNameRunes)
	v.maxRunes("reporter.faculty", r.Faculty, maxFacultyRunes)
	v.maxRunes("reporter.program", r.Program, maxProgramRunes)

	v.required("reporter.phone", r.Phone)
	v.maxRunes("reporter.phone", r.Phone, maxPhoneRunes)
	if r.Phone != "" && !phonePattern.MatchString(r.Phone) {
		v.add("reporter.phone", "Nomor kontak tidak valid.")
	}

	v.required("reporter.email", r.Email)
	v.maxRunes("reporter.email", r.Email, maxEmailRunes)
	if r.Email != "" && !validEmail(r.Email) {
		v.add("reporter.email", "Alamat email tidak valid.")
	}
}

// validateGender memaksa nilai gender ke enum tertutup. required=false dipakai
// untuk korban/terlapor yang boleh tidak mencantumkan gender.
func validateGender(v *validator, field, value string, required bool) {
	if required {
		v.required(field, value)
	}
	if value == "" {
		return
	}
	v.maxRunes(field, value, maxGenderRunes)
	if _, ok := validGenders[value]; !ok {
		v.add(field, "Jenis kelamin harus PEREMPUAN, LAKI_LAKI, atau LAINNYA.")
	}
}

// validatePerson memeriksa korban/terlapor. Nama selalu wajib: korban wajib
// punya nama, terlapor boleh hanya nama sebagian/ciri (temuan #14).
func validatePerson(v *validator, prefix string, p PersonInput) {
	v.required(prefix+".name", p.Name)
	v.maxRunes(prefix+".name", p.Name, maxNameRunes)
	validateGender(v, prefix+".gender", p.Gender, false)
	v.maxRunes(prefix+".category", p.Category, maxCategoryRunes)
	v.maxRunes(prefix+".id_number", p.IDNumber, maxIDNumberRunes)
	v.maxRunes(prefix+".institution", p.Institution, maxNameRunes)
	v.maxRunes(prefix+".faculty", p.Faculty, maxFacultyRunes)
	v.maxRunes(prefix+".program", p.Program, maxProgramRunes)
	v.maxRunes(prefix+".contact", p.Contact, maxContactRunes)
	v.maxRunes(prefix+".relation_to_other", p.RelationToOther, maxRelationRunes)
}

func validateIncident(now time.Time, inc IncidentRequest, prefix string, v *validator) (Incident, error) {
	// Tanggal diparse di zona WIB agar perbandingan dengan "hari ini" konsisten;
	// time.Parse biasa menghasilkan tengah malam UTC yang bisa membuat tanggal
	// hari ini di WIB dianggap sebagai masa depan.
	start, err := time.ParseInLocation("2006-01-02", strings.TrimSpace(inc.DateStart), jakartaZone)
	if err != nil {
		v.add(prefix+".date_start", "Tanggal kejadian tidak valid (YYYY-MM-DD).")
		return Incident{}, err
	}

	today := startOfDay(now.In(jakartaZone))
	if start.After(today) {
		v.add(prefix+".date_start", "Tanggal kejadian tidak boleh di masa depan.")
	}

	parsed := Incident{
		DateStart:   start,
		Location:    strings.TrimSpace(inc.Location),
		Description: strings.TrimSpace(inc.Description),
	}

	if endRaw := strings.TrimSpace(inc.DateEnd); endRaw != "" {
		end, err := time.ParseInLocation("2006-01-02", endRaw, jakartaZone)
		switch {
		case err != nil:
			v.add(prefix+".date_end", "Tanggal akhir tidak valid (YYYY-MM-DD).")
		case end.Before(start):
			v.add(prefix+".date_end", "Tanggal akhir tidak boleh sebelum tanggal mulai.")
		case end.After(today):
			v.add(prefix+".date_end", "Tanggal akhir tidak boleh di masa depan.")
		default:
			parsed.DateEnd = &end
		}
	}

	v.required(prefix+".location", parsed.Location)
	v.maxRunes(prefix+".location", parsed.Location, maxLocationRunes)
	v.required(prefix+".description", parsed.Description)
	v.maxRunes(prefix+".description", parsed.Description, maxShortTextRunes)

	if _, exists := v.fields[prefix+".date_start"]; exists {
		return Incident{}, fmt.Errorf("tanggal mulai tidak valid")
	}
	return parsed, nil
}

func (r ReporterInput) normalized() ReporterInput {
	r.Name = strings.TrimSpace(r.Name)
	r.Gender = strings.TrimSpace(r.Gender)
	r.Category = strings.ToUpper(strings.TrimSpace(r.Category))
	r.IDNumber = strings.TrimSpace(r.IDNumber)
	r.Institution = strings.TrimSpace(r.Institution)
	r.Faculty = strings.TrimSpace(r.Faculty)
	r.Program = strings.TrimSpace(r.Program)
	r.Phone = normalizePhone(r.Phone)
	r.Email = strings.TrimSpace(r.Email)
	return r
}

func (p *PersonInput) normalizedPtr() *PersonInput {
	if p == nil {
		return nil
	}
	n := p.normalized()
	return &n
}

func (p PersonInput) normalized() PersonInput {
	p.Name = strings.TrimSpace(p.Name)
	p.Gender = strings.TrimSpace(p.Gender)
	p.Category = strings.ToUpper(strings.TrimSpace(p.Category))
	p.IDNumber = strings.TrimSpace(p.IDNumber)
	p.Institution = strings.TrimSpace(p.Institution)
	p.Faculty = strings.TrimSpace(p.Faculty)
	p.Program = strings.TrimSpace(p.Program)
	p.Contact = strings.TrimSpace(p.Contact)
	p.RelationToOther = strings.TrimSpace(p.RelationToOther)
	return p
}

func normalizePeople(in []PersonInput) []PersonInput {
	if len(in) == 0 {
		return nil
	}
	out := make([]PersonInput, 0, len(in))
	for _, p := range in {
		out = append(out, p.normalized())
	}
	return out
}

// normalizeCodes merapikan kode jenis dugaan: trim, huruf besar, buang
// duplikat, dan pertahankan urutan kemunculan.
func normalizeCodes(in []string) []string {
	seen := make(map[string]struct{}, len(in))
	out := make([]string, 0, len(in))
	for _, raw := range in {
		code := strings.ToUpper(strings.TrimSpace(raw))
		if code == "" {
			continue
		}
		if _, ok := seen[code]; ok {
			continue
		}
		seen[code] = struct{}{}
		out = append(out, code)
	}
	return out
}

// normalizePhone membersihkan pemisah lalu menyeragamkan awalan Indonesia ke
// +62. Nomor yang tidak dikenali dibiarkan apa adanya agar validasi yang
// memberi pesan.
func normalizePhone(raw string) string {
	s := phoneCleaner.Replace(strings.TrimSpace(raw))
	switch {
	case strings.HasPrefix(s, "+62"):
		return s
	case strings.HasPrefix(s, "62"):
		return "+" + s
	case strings.HasPrefix(s, "0"):
		return "+62" + s[1:]
	default:
		return s
	}
}

func validEmail(s string) bool {
	addr, err := mail.ParseAddress(s)
	return err == nil && addr.Address == s
}

func startOfDay(t time.Time) time.Time {
	return time.Date(t.Year(), t.Month(), t.Day(), 0, 0, 0, 0, t.Location())
}

// validator mengumpulkan pesan per field; pesan pertama untuk tiap field
// dipertahankan agar tidak menimpa kesalahan yang lebih spesifik.
type validator struct {
	fields map[string]string
}

func (v *validator) add(field, message string) {
	if _, exists := v.fields[field]; !exists {
		v.fields[field] = message
	}
}

func (v *validator) required(field, value string) {
	if strings.TrimSpace(value) == "" {
		v.add(field, "Wajib diisi.")
	}
}

func (v *validator) maxRunes(field, value string, limit int) {
	if utf8.RuneCountInString(value) > limit {
		v.add(field, fmt.Sprintf("Maksimal %d karakter.", limit))
	}
}
