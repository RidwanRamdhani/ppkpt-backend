package cases

import (
	"encoding/json"
	"strings"
	"testing"
	"time"
)

func TestMaskReporter(t *testing.T) {
	age := 21
	view := &ReporterView{
		Name:     "Nama Asli",
		Age:      &age,
		Gender:   "PEREMPUAN",
		Category: "MAHASISWA",
		IDNumber: "123456789",
		Phone:    "+628123456789",
		Email:    "asli@example.test",
	}

	maskReporter(view)

	if view.Name != maskedReporterName || !view.Masked {
		t.Fatalf("identitas tidak disamarkan: %+v", view)
	}
	for field, value := range map[string]string{
		"id_number": view.IDNumber,
		"phone":     view.Phone,
		"email":     view.Email,
	} {
		if value != "" {
			t.Errorf("%s masih terisi: %q", field, value)
		}
	}
	if view.Age == nil || *view.Age != age {
		t.Error("usia pelapor seharusnya tetap ada")
	}
	if view.Category != "MAHASISWA" {
		t.Error("kategori pelapor seharusnya tetap ada")
	}
}

func TestMaskReporterNilAman(t *testing.T) {
	maskReporter(nil)
}

func TestOfficialListTidakMemuatDataPihak(t *testing.T) {
	submitted := time.Now()
	page := ListPage{
		Items: []OfficialListItem{{
			ID:             1,
			CaseNumber:     "12345678",
			ViolationTypes: []string{"PERUNDUNGAN"},
			Channel:        "PORTAL",
			Status:         "REKOMENDASI",
			AssignedTo:     &Assignee{ID: 9, Name: "Satgas A"},
			Conclusion: &ConclusionSummary{
				Result:           "TERBUKTI",
				SubmissionStatus: "DIAJUKAN",
				SubmittedAt:      &submitted,
			},
			CanReview: true,
			CreatedAt: submitted,
		}},
		Page:     1,
		PageSize: 20,
		Total:    1,
	}

	raw, err := json.Marshal(page)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	body := string(raw)

	for _, forbidden := range []string{
		`"reporter"`, `"parties"`, `"victim"`, `"accused"`,
		`"chronology"`, `"incidents"`, `"evidences"`, `"relatives"`,
	} {
		if strings.Contains(body, forbidden) {
			t.Errorf("proyeksi Pejabat memuat %s: %s", forbidden, body)
		}
	}
	if !strings.Contains(body, `"can_review":true`) {
		t.Errorf("can_review tidak ada: %s", body)
	}
}

func TestReadyToReview(t *testing.T) {
	tests := []struct {
		name       string
		status     string
		conclusion *ConclusionSummary
		want       bool
	}{
		{"diajukan", "REKOMENDASI", &ConclusionSummary{SubmissionStatus: "DIAJUKAN"}, true},
		{"masih draf", "REKOMENDASI", &ConclusionSummary{SubmissionStatus: "DRAF"}, false},
		{"belum rekomendasi", "PEMERIKSAAN", &ConclusionSummary{SubmissionStatus: "DIAJUKAN"}, false},
		{"tanpa kesimpulan", "REKOMENDASI", nil, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := readyToReview(tt.status, tt.conclusion); got != tt.want {
				t.Fatalf("readyToReview() = %v, ingin %v", got, tt.want)
			}
		})
	}
}

func TestConclusionSummary(t *testing.T) {
	result := "TERBUKTI"
	status := "DIAJUKAN"
	at := time.Now()
	got := conclusionSummary(&result, &status, &at)
	if got == nil || got.Result != result || got.SubmissionStatus != status {
		t.Fatalf("conclusionSummary() = %+v", got)
	}
	if conclusionSummary(nil, nil, nil) != nil {
		t.Fatal("kesimpulan kosong seharusnya nil")
	}
}

func TestNonNilStrings(t *testing.T) {
	got := nonNilStrings(nil)
	if got == nil || len(got) != 0 {
		t.Fatalf("nonNilStrings(nil) = %#v", got)
	}
}
