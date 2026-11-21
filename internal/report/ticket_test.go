package report_test

import (
	"strings"
	"testing"

	"github.com/RidwanRamdhani/ppkpt-backend/internal/report"
)

func TestGenerateTicket_Format(t *testing.T) {
	seen := make(map[string]struct{}, 1000)
	for i := 0; i < 1000; i++ {
		ticket, err := report.GenerateTicket()
		if err != nil {
			t.Fatalf("GenerateTicket: %v", err)
		}
		if len(ticket) != report.TicketDigits {
			t.Fatalf("panjang tiket = %d (%q), ingin %d", len(ticket), ticket, report.TicketDigits)
		}
		if strings.Trim(ticket, "0123456789") != "" {
			t.Fatalf("tiket %q memuat karakter non-angka", ticket)
		}
		seen[ticket] = struct{}{}
	}
	// Peluang tabrakan pada 1000 undian dari 10^8 sangat kecil; ini terutama
	// memastikan generator tidak mengembalikan nilai konstan.
	if len(seen) < 990 {
		t.Fatalf("hanya %d tiket unik dari 1000, generator tidak acak", len(seen))
	}
}

func TestGeneratePIN_Format(t *testing.T) {
	for i := 0; i < 1000; i++ {
		pin, err := report.GeneratePIN()
		if err != nil {
			t.Fatalf("GeneratePIN: %v", err)
		}
		if len(pin) != report.PINDigits {
			t.Fatalf("panjang PIN = %d (%q), ingin %d", len(pin), pin, report.PINDigits)
		}
		if strings.Trim(pin, "0123456789") != "" {
			t.Fatalf("PIN %q memuat karakter non-angka", pin)
		}
	}
}
