package report_test

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/RidwanRamdhani/ppkpt-backend/internal/platform/httpx"
	"github.com/RidwanRamdhani/ppkpt-backend/internal/report"
)

func fixedNow() time.Time {
	return time.Date(2026, 11, 21, 10, 0, 0, 0, time.UTC)
}

func validRequest() report.SubmitRequest {
	age := 21
	return report.SubmitRequest{
		Reporter: report.ReporterInput{
			Name:        "Pelapor Uji",
			Age:         &age,
			Gender:      "PEREMPUAN",
			Category:    "MAHASISWA",
			IDNumber:    "1234567890",
			Institution: "Telkom University",
			Faculty:     "Fakultas Rekayasa Industri",
			Program:     "Informatika",
			Phone:       "081234567890",
			Email:       "pelapor@example.test",
		},
		ReporterIsVictim: false,
		Victim: &report.PersonInput{
			Name:            "Korban Uji",
			Gender:          "PEREMPUAN",
			Category:        "MAHASISWA",
			Faculty:         "Fakultas Rekayasa Industri",
			Program:         "Informatika",
			Contact:         "081298765432",
			RelationToOther: "teman sekelas",
		},
		Accused: []report.PersonInput{{
			Name:            "Terlapor Uji",
			Gender:          "LAKI_LAKI",
			RelationToOther: "senior",
		}},
		Incidents: []report.IncidentRequest{{
			DateStart:   "2026-09-15",
			DateEnd:     "2026-09-18",
			Location:    "Gedung A",
			Description: "Kejadian uji",
		}},
		ViolationTypes:     []string{"PERUNDUNGAN"},
		Chronology:         "Kronologi untuk pengujian.",
		ReportedElsewhere:  false,
		Consent:            true,
		ConsentTextVersion: "1.0",
	}
}

func appErrorOf(t *testing.T, err error) *httpx.AppError {
	t.Helper()
	var appErr *httpx.AppError
	if !errors.As(err, &appErr) {
		t.Fatalf("error = %v (%T), ingin *httpx.AppError", err, err)
	}
	return appErr
}

func requireFields(t *testing.T, appErr *httpx.AppError, fields ...string) {
	t.Helper()
	if appErr.Status != 422 {
		t.Fatalf("status = %d, ingin 422 (fields: %v)", appErr.Status, appErr.Fields)
	}
	for _, field := range fields {
		if _, ok := appErr.Fields[field]; !ok {
			t.Errorf("field %q tidak ada pada error; fields = %v", field, appErr.Fields)
		}
	}
}

func TestValidateValid(t *testing.T) {
	req := validRequest()
	// Tambahkan spasi dan variasi penulisan agar normalisasi ikut diuji.
	req.Reporter.Name = "  Pelapor Uji  "
	req.Reporter.Phone = "0812-3456-7890"
	req.Reporter.Category = "mahasiswa"
	req.ViolationTypes = []string{" perundungan ", "PERUNDUNGAN"}

	sub, appErr := req.Validate(fixedNow())
	if appErr != nil {
		t.Fatalf("Validate = %v (fields: %v), ingin valid", appErr, appErr.Fields)
	}

	if sub.Reporter.Name != "Pelapor Uji" {
		t.Errorf("nama tidak di-trim: %q", sub.Reporter.Name)
	}
	if sub.Reporter.Category != "MAHASISWA" {
		t.Errorf("kategori tidak dinormalkan: %q", sub.Reporter.Category)
	}
	if sub.Reporter.Phone != "+6281234567890" {
		t.Errorf("telepon tidak dinormalkan: %q", sub.Reporter.Phone)
	}
	if len(sub.ViolationTypes) != 1 || sub.ViolationTypes[0] != "PERUNDUNGAN" {
		t.Errorf("jenis dugaan tidak dirapikan: %v", sub.ViolationTypes)
	}
	if sub.Reporter.Email != "pelapor@example.test" {
		t.Errorf("email berubah: %q", sub.Reporter.Email)
	}
}

func TestValidateReportsAllInvalidFields(t *testing.T) {
	req := validRequest()
	req.Reporter.Name = ""
	req.Reporter.Email = "bukan-email"
	req.Chronology = ""
	req.Consent = false

	_, appErr := req.Validate(fixedNow())
	requireFields(t, appErr, "reporter.name", "reporter.email", "chronology", "consent")
}

func TestValidateCases(t *testing.T) {
	cases := []struct {
		name   string
		mutate func(*report.SubmitRequest)
		fields []string
	}{
		{
			name:   "usia kosong",
			mutate: func(r *report.SubmitRequest) { r.Reporter.Age = nil },
			fields: []string{"reporter.age"},
		},
		{
			name:   "usia tidak rasional",
			mutate: func(r *report.SubmitRequest) { age := 5; r.Reporter.Age = &age },
			fields: []string{"reporter.age"},
		},
		{
			name:   "gender pelapor variasi penulisan ditolak",
			mutate: func(r *report.SubmitRequest) { r.Reporter.Gender = "Perempuan" },
			fields: []string{"reporter.gender"},
		},
		{
			name:   "gender pelapor asing ditolak",
			mutate: func(r *report.SubmitRequest) { r.Reporter.Gender = "PRIA" },
			fields: []string{"reporter.gender"},
		},
		{
			name:   "gender korban asing ditolak",
			mutate: func(r *report.SubmitRequest) { r.Victim.Gender = "WANITA" },
			fields: []string{"victim.gender"},
		},
		{
			name:   "gender terlapor asing ditolak",
			mutate: func(r *report.SubmitRequest) { r.Accused[0].Gender = "PRIA" },
			fields: []string{"accused[0].gender"},
		},
		{
			name:   "gender korban dan terlapor boleh kosong",
			mutate: func(r *report.SubmitRequest) { r.Victim.Gender = ""; r.Accused[0].Gender = "" },
			fields: nil,
		},
		{
			name:   "kategori pelapor asing",
			mutate: func(r *report.SubmitRequest) { r.Reporter.Category = "GURU" },
			fields: []string{"reporter.category"},
		},
		{
			name:   "telepon tidak valid",
			mutate: func(r *report.SubmitRequest) { r.Reporter.Phone = "abcdefg" },
			fields: []string{"reporter.phone"},
		},
		{
			name:   "email tidak valid",
			mutate: func(r *report.SubmitRequest) { r.Reporter.Email = "pelapor@" },
			fields: []string{"reporter.email"},
		},
		{
			name:   "nama terlalu panjang",
			mutate: func(r *report.SubmitRequest) { r.Reporter.Name = strings.Repeat("a", 101) },
			fields: []string{"reporter.name"},
		},
		{
			name:   "korban tidak diisi",
			mutate: func(r *report.SubmitRequest) { r.Victim = nil },
			fields: []string{"victim"},
		},
		{
			name:   "nama korban kosong",
			mutate: func(r *report.SubmitRequest) { r.Victim = &report.PersonInput{} },
			fields: []string{"victim.name"},
		},
		{
			name:   "pelapor adalah korban boleh tanpa data korban",
			mutate: func(r *report.SubmitRequest) { r.ReporterIsVictim = true; r.Victim = nil },
			fields: nil,
		},
		{
			name:   "terlapor kosong",
			mutate: func(r *report.SubmitRequest) { r.Accused = nil },
			fields: []string{"accused"},
		},
		{
			name:   "nama terlapor kedua kosong",
			mutate: func(r *report.SubmitRequest) { r.Accused = append(r.Accused, report.PersonInput{}) },
			fields: []string{"accused[1].name"},
		},
		{
			name:   "kejadian kosong",
			mutate: func(r *report.SubmitRequest) { r.Incidents = nil },
			fields: []string{"incidents"},
		},
		{
			name: "tanggal mulai tidak valid",
			mutate: func(r *report.SubmitRequest) {
				r.Incidents[0].DateStart = "15-09-2026"
			},
			fields: []string{"incidents[0].date_start"},
		},
		{
			name: "tanggal akhir sebelum mulai",
			mutate: func(r *report.SubmitRequest) {
				r.Incidents[0].DateStart = "2026-09-18"
				r.Incidents[0].DateEnd = "2026-09-15"
			},
			fields: []string{"incidents[0].date_end"},
		},
		{
			name: "tanggal mulai di masa depan",
			mutate: func(r *report.SubmitRequest) {
				r.Incidents[0].DateStart = "2026-12-01"
				r.Incidents[0].DateEnd = ""
			},
			fields: []string{"incidents[0].date_start"},
		},
		{
			name: "lokasi kosong",
			mutate: func(r *report.SubmitRequest) {
				r.Incidents[0].Location = "   "
			},
			fields: []string{"incidents[0].location"},
		},
		{
			name:   "jenis dugaan kosong",
			mutate: func(r *report.SubmitRequest) { r.ViolationTypes = nil },
			fields: []string{"violation_types"},
		},
		{
			name:   "jenis dugaan asing",
			mutate: func(r *report.SubmitRequest) { r.ViolationTypes = []string{"PENIPUAN"} },
			fields: []string{"violation_types"},
		},
		{
			name:   "versi persetujuan asing",
			mutate: func(r *report.SubmitRequest) { r.ConsentTextVersion = "0.9" },
			fields: []string{"consent_text_version"},
		},
		{
			name: "lapor di tempat lain tanpa tujuan",
			mutate: func(r *report.SubmitRequest) {
				r.ReportedElsewhere = true
				r.ReportedTo = ""
			},
			fields: []string{"reported_to"},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			req := validRequest()
			tc.mutate(&req)

			_, appErr := req.Validate(fixedNow())
			if len(tc.fields) == 0 {
				if appErr != nil {
					t.Fatalf("Validate = %v (fields: %v), ingin valid", appErr, appErr.Fields)
				}
				return
			}
			if appErr == nil {
				t.Fatal("Validate = nil, ingin error validasi")
			}
			requireFields(t, appErr, tc.fields...)
		})
	}
}

func TestNormalizePhone(t *testing.T) {
	cases := map[string]string{
		"0812-3456-7890":    "+6281234567890",
		"0812 3456 7890":    "+6281234567890",
		"(0812) 3456 7890":  "+6281234567890",
		"6281234567890":     "+6281234567890",
		"+62 812 3456 7890": "+6281234567890",
	}
	for input, want := range cases {
		req := validRequest()
		req.Reporter.Phone = input
		if _, appErr := req.Validate(fixedNow()); appErr != nil {
			t.Errorf("telepon %q ditolak: %v", input, appErr.Fields)
			continue
		}
		sub, _ := req.Validate(fixedNow())
		if sub.Reporter.Phone != want {
			t.Errorf("normalisasi %q = %q, ingin %q", input, sub.Reporter.Phone, want)
		}
	}
}

// TestValidateConsentVersionRequired memastikan versi persetujuan tidak boleh
// kosong meskipun consent bernilai true.
func TestValidateConsentVersionRequired(t *testing.T) {
	req := validRequest()
	req.ConsentTextVersion = ""
	_, appErr := req.Validate(fixedNow())
	requireFields(t, appErr, "consent_text_version")
}
