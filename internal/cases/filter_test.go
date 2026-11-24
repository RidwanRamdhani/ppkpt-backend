package cases

import (
	"net/url"
	"testing"

	"github.com/RidwanRamdhani/ppkpt-backend/internal/workflow"
)

func TestParseFilterDefault(t *testing.T) {
	f, appErr := ParseFilter(url.Values{}, 7)
	if appErr != nil {
		t.Fatalf("ParseFilter: %v", appErr)
	}
	if f.Page != 1 || f.PageSize != defaultPageSize {
		t.Fatalf("default page = %d/%d", f.Page, f.PageSize)
	}
	if f.Query != "" || f.Status != "" || f.NeedsAction {
		t.Fatalf("filter default tidak kosong: %+v", f)
	}
}

func TestParseFilterLengkap(t *testing.T) {
	values := url.Values{
		"q":              {"12345678"},
		"status":         {"penelaahan"},
		"violation_type": {"perundungan"},
		"channel":        {"hotline"},
		"date_from":      {"2026-11-01"},
		"date_to":        {"2026-11-20"},
		"assigned":       {"me"},
		"needs_action":   {"true"},
		"page":           {"2"},
		"page_size":      {"50"},
	}
	f, appErr := ParseFilter(values, 7)
	if appErr != nil {
		t.Fatalf("ParseFilter: %v", appErr)
	}
	if f.Query != "12345678" || f.Status != workflow.Penelaahan {
		t.Errorf("q/status = %q/%q", f.Query, f.Status)
	}
	if f.ViolationType != "PERUNDUNGAN" || f.Channel != "HOTLINE" {
		t.Errorf("violation/channel = %q/%q", f.ViolationType, f.Channel)
	}
	if f.DateFrom == nil || f.DateTo == nil {
		t.Fatal("tanggal tidak terisi")
	}
	if f.DateTo.Sub(*f.DateFrom) <= 0 {
		t.Error("rentang tanggal terbalik")
	}
	if f.Assigned != "me" || !f.NeedsAction || f.Page != 2 || f.PageSize != 50 {
		t.Errorf("filter = %+v", f)
	}
}

func TestParseFilterMenolakNilaiTidakValid(t *testing.T) {
	tests := []struct {
		name   string
		values url.Values
		field  string
	}{
		{"q bukan angka", url.Values{"q": {"abc"}}, "q"},
		{"q terlalu panjang", url.Values{"q": {"123456789"}}, "q"},
		{"status asing", url.Values{"status": {"HILANG"}}, "status"},
		{"violation asing", url.Values{"violation_type": {"LAIN"}}, "violation_type"},
		{"channel asing", url.Values{"channel": {"EMAIL"}}, "channel"},
		{"date_from rusak", url.Values{"date_from": {"2026-13-01"}}, "date_from"},
		{"date_to rusak", url.Values{"date_to": {"kemarin"}}, "date_to"},
		{"rentang terbalik", url.Values{"date_from": {"2026-11-20"}, "date_to": {"2026-11-01"}}, "date_to"},
		{"assigned asing", url.Values{"assigned": {"other"}}, "assigned"},
		{"needs_action rusak", url.Values{"needs_action": {"ya"}}, "needs_action"},
		{"page nol", url.Values{"page": {"0"}}, "page"},
		{"page bukan angka", url.Values{"page": {"x"}}, "page"},
		{"page_size nol", url.Values{"page_size": {"0"}}, "page_size"},
		{"page_size terlalu besar", url.Values{"page_size": {"51"}}, "page_size"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, appErr := ParseFilter(tt.values, 7)
			if appErr == nil {
				t.Fatal("ingin error validasi")
			}
			if appErr.Fields[tt.field] == "" {
				t.Fatalf("field %q tidak ada di %+v", tt.field, appErr.Fields)
			}
		})
	}
}

func TestParseFilterAssignedMeButuhUser(t *testing.T) {
	_, appErr := ParseFilter(url.Values{"assigned": {"me"}}, 0)
	if appErr == nil || appErr.Fields["assigned"] == "" {
		t.Fatalf("ingin error assigned, dapat %+v", appErr)
	}
}

func TestParseFilterAssignedNone(t *testing.T) {
	f, appErr := ParseFilter(url.Values{"assigned": {"none"}}, 7)
	if appErr != nil {
		t.Fatalf("ParseFilter: %v", appErr)
	}
	if f.Assigned != "none" {
		t.Fatalf("assigned = %q", f.Assigned)
	}
}
