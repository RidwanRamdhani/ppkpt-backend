package cases

import (
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/RidwanRamdhani/ppkpt-backend/internal/deadline"
	"github.com/RidwanRamdhani/ppkpt-backend/internal/platform/httpx"
	"github.com/RidwanRamdhani/ppkpt-backend/internal/workflow"
)

const (
	defaultPageSize = 20
	maxPageSize     = 50
)

// Filter adalah parameter GET /api/cases yang sudah divalidasi. DateTo bersifat
// eksklusif (awal hari berikutnya) agar rentang tanggal inklusif di sisi API.
type Filter struct {
	Query         string
	Status        workflow.Status
	ViolationType string
	Channel       string
	DateFrom      *time.Time
	DateTo        *time.Time
	Assigned      string // "", "me", "none"
	NeedsAction   bool
	Page          int
	PageSize      int
}

// validChannels sesuai CHECK constraint reports.channel.
var validChannels = map[string]struct{}{
	"PORTAL":     {},
	"HOTLINE":    {},
	"TATAP_MUKA": {},
	"RUJUKAN":    {},
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

// ParseFilter memvalidasi seluruh query param. Semua kesalahan dikumpulkan ke
// Fields agar frontend dapat menandai isian yang salah (422).
func ParseFilter(values url.Values, userID int64) (Filter, *httpx.AppError) {
	f := Filter{Page: 1, PageSize: defaultPageSize}
	fields := make(map[string]string)

	if raw := strings.TrimSpace(values.Get("q")); raw != "" {
		if len(raw) > 8 || strings.Trim(raw, "0123456789") != "" {
			fields["q"] = "Kode tiket terdiri dari maksimal 8 digit angka."
		} else {
			f.Query = raw
		}
	}

	if raw := strings.ToUpper(strings.TrimSpace(values.Get("status"))); raw != "" {
		status := workflow.Status(raw)
		if !status.Valid() {
			fields["status"] = "Status kasus tidak dikenal."
		} else {
			f.Status = status
		}
	}

	if raw := strings.ToUpper(strings.TrimSpace(values.Get("violation_type"))); raw != "" {
		if _, ok := validViolationCodes[raw]; !ok {
			fields["violation_type"] = "Jenis dugaan kekerasan tidak dikenal."
		} else {
			f.ViolationType = raw
		}
	}

	if raw := strings.ToUpper(strings.TrimSpace(values.Get("channel"))); raw != "" {
		if _, ok := validChannels[raw]; !ok {
			fields["channel"] = "Kanal laporan tidak dikenal."
		} else {
			f.Channel = raw
		}
	}

	if raw := strings.TrimSpace(values.Get("date_from")); raw != "" {
		t, err := time.ParseInLocation("2006-01-02", raw, deadline.Jakarta)
		if err != nil {
			fields["date_from"] = "Tanggal awal tidak valid (YYYY-MM-DD)."
		} else {
			f.DateFrom = &t
		}
	}

	if raw := strings.TrimSpace(values.Get("date_to")); raw != "" {
		t, err := time.ParseInLocation("2006-01-02", raw, deadline.Jakarta)
		if err != nil {
			fields["date_to"] = "Tanggal akhir tidak valid (YYYY-MM-DD)."
		} else {
			end := t.AddDate(0, 0, 1)
			f.DateTo = &end
		}
	}

	if f.DateFrom != nil && f.DateTo != nil && !f.DateFrom.Before(*f.DateTo) {
		fields["date_to"] = "Tanggal akhir tidak boleh sebelum tanggal awal."
	}

	if raw := strings.ToLower(strings.TrimSpace(values.Get("assigned"))); raw != "" {
		switch raw {
		case "me":
			if userID <= 0 {
				fields["assigned"] = "Filter assigned=me membutuhkan sesi pengguna."
			} else {
				f.Assigned = "me"
			}
		case "none":
			f.Assigned = "none"
		default:
			fields["assigned"] = "Nilai assigned harus me atau none."
		}
	}

	if raw := strings.TrimSpace(values.Get("needs_action")); raw != "" {
		v, err := strconv.ParseBool(raw)
		if err != nil {
			fields["needs_action"] = "Nilai needs_action harus true atau false."
		} else {
			f.NeedsAction = v
		}
	}

	if raw := strings.TrimSpace(values.Get("page")); raw != "" {
		v, err := strconv.Atoi(raw)
		if err != nil || v < 1 {
			fields["page"] = "Halaman harus bilangan bulat minimal 1."
		} else {
			f.Page = v
		}
	}

	if raw := strings.TrimSpace(values.Get("page_size")); raw != "" {
		v, err := strconv.Atoi(raw)
		if err != nil || v < 1 || v > maxPageSize {
			fields["page_size"] = "Ukuran halaman harus 1 sampai 50."
		} else {
			f.PageSize = v
		}
	}

	if len(fields) > 0 {
		appErr := httpx.ErrValidation("Parameter daftar kasus tidak valid.")
		appErr.Fields = fields
		return Filter{}, appErr
	}
	return f, nil
}
