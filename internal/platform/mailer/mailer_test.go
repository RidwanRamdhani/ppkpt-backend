package mailer

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"strings"
	"testing"
	"time"
)

func newTestMailer(t *testing.T) *Mailer {
	t.Helper()
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	m, err := New(Config{
		Host: "localhost",
		Port: 1025,
		From: "noreply@ppkpt.local",
	}, logger)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return m
}

func TestNew_MenolakFromTidakValid(t *testing.T) {
	if _, err := New(Config{Host: "localhost", From: "bukan email"}, nil); err == nil {
		t.Fatal("SMTP_FROM tidak valid harus ditolak")
	}
}

func TestCompose_MenyusunHeaderDanMencegahInjeksi(t *testing.T) {
	m := newTestMailer(t)
	msg, err := m.compose("penerima@example.test", "Status\r\nBcc: evil@example.test", "Halo\nDunia")
	if err != nil {
		t.Fatalf("compose: %v", err)
	}

	data := string(msg.data)
	if !strings.Contains(data, "To: <penerima@example.test>") {
		t.Errorf("header To tidak ada: %q", data)
	}
	if strings.Contains(data, "\r\nBcc:") {
		t.Errorf("injeksi header lolos: %q", data)
	}
	if !strings.Contains(data, "Subject: Status Bcc: evil@example.test") {
		t.Errorf("subject tidak disanitasi: %q", data)
	}
	if !strings.Contains(data, "Halo\r\nDunia") {
		t.Errorf("body tidak memakai CRLF: %q", data)
	}
	if msg.to != "penerima@example.test" {
		t.Errorf("alamat amplop = %q", msg.to)
	}
}

func TestCompose_MenolakAlamatPenerimaTidakValid(t *testing.T) {
	m := newTestMailer(t)
	if _, err := m.compose("bukan-email", "Subjek", "Body"); err == nil {
		t.Fatal("alamat penerima tidak valid harus ditolak")
	}
}

func TestSend_MemasukkanKeAntrean(t *testing.T) {
	m := newTestMailer(t)
	m.Send("penerima@example.test", "Subjek", "Body")

	select {
	case msg := <-m.queue:
		if msg.to != "penerima@example.test" {
			t.Fatalf("alamat = %q", msg.to)
		}
	case <-time.After(time.Second):
		t.Fatal("email tidak masuk antrean")
	}
}

func TestDeliverWithRetry_BerhasilSetelahDuaKegagalan(t *testing.T) {
	m := newTestMailer(t)
	m.retryDelays = []time.Duration{time.Millisecond, time.Millisecond}

	attempts := 0
	m.send = func(context.Context, outgoing) error {
		attempts++
		if attempts < 3 {
			return errors.New("smtp sibuk")
		}
		return nil
	}

	m.deliverWithRetry(context.Background(), outgoing{to: "a@b.test"})
	if attempts != 3 {
		t.Fatalf("percobaan = %d, mau 3", attempts)
	}
}

func TestDeliverWithRetry_BerhentiSetelahBatasPercobaan(t *testing.T) {
	m := newTestMailer(t)
	m.retryDelays = []time.Duration{time.Millisecond, time.Millisecond}

	attempts := 0
	m.send = func(context.Context, outgoing) error {
		attempts++
		return errors.New("smtp mati")
	}

	m.deliverWithRetry(context.Background(), outgoing{to: "a@b.test"})
	if attempts != 3 {
		t.Fatalf("percobaan = %d, mau 3 (1 kirim + 2 retry)", attempts)
	}
}

func TestDeliverWithRetry_BerhentiSaatContextDibatalkan(t *testing.T) {
	m := newTestMailer(t)
	m.retryDelays = []time.Duration{time.Hour}

	ctx, cancel := context.WithCancel(context.Background())
	attempts := 0
	m.send = func(context.Context, outgoing) error {
		attempts++
		cancel()
		return errors.New("gagal")
	}

	m.deliverWithRetry(ctx, outgoing{to: "a@b.test"})
	if attempts != 1 {
		t.Fatalf("percobaan = %d, mau 1", attempts)
	}
}

func TestMaskEmail(t *testing.T) {
	cases := map[string]string{
		"nama@example.test": "n***@example.test",
		"a@example.test":    "a***@example.test",
		"bukan-email":       "***",
	}
	for input, want := range cases {
		if got := maskEmail(input); got != want {
			t.Errorf("maskEmail(%q) = %q, mau %q", input, got, want)
		}
	}
}
