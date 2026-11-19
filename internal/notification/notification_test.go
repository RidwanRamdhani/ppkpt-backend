package notification

import (
	"strings"
	"testing"
)

type capturedEmail struct {
	to      string
	subject string
	body    string
}

type fakeSender struct {
	emails []capturedEmail
}

func (f *fakeSender) Send(to, subject, body string) {
	f.emails = append(f.emails, capturedEmail{to: to, subject: subject, body: body})
}

func TestSendReportReceipt_BerisiTiketDanPIN(t *testing.T) {
	sender := &fakeSender{}
	svc := New(sender, nil)

	svc.SendReportReceipt("pelapor@example.test", "20481735", "493027")

	if len(sender.emails) != 1 {
		t.Fatalf("email terkirim = %d, mau 1", len(sender.emails))
	}
	email := sender.emails[0]
	if email.to != "pelapor@example.test" {
		t.Errorf("to = %q", email.to)
	}
	if !strings.Contains(email.body, "20481735") || !strings.Contains(email.body, "493027") {
		t.Errorf("body tidak memuat tiket/PIN: %q", email.body)
	}
	if strings.Contains(email.subject, "493027") {
		t.Error("PIN tidak boleh ada di subject")
	}
}

func TestSendReportReceipt_TanpaEmailTidakMengirim(t *testing.T) {
	sender := &fakeSender{}
	svc := New(sender, nil)

	svc.SendReportReceipt("   ", "20481735", "493027")

	if len(sender.emails) != 0 {
		t.Fatalf("email terkirim = %d, mau 0", len(sender.emails))
	}
}

func TestSendStatusUpdate_TidakMembocorkanStatus(t *testing.T) {
	sender := &fakeSender{}
	svc := New(sender, nil)

	svc.SendStatusUpdate("pelapor@example.test", "20481735")

	if len(sender.emails) != 1 {
		t.Fatalf("email terkirim = %d, mau 1", len(sender.emails))
	}
	body := sender.emails[0].body
	if !strings.Contains(body, "20481735") {
		t.Errorf("body tidak memuat kode tiket: %q", body)
	}
	if strings.Contains(body, "PEMERIKSAAN") || strings.Contains(body, "DILAPORKAN") {
		t.Errorf("body membocorkan status internal: %q", body)
	}
}
