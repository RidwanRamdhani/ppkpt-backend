// Package mailer mengirim email melalui SMTP + STARTTLS dengan antrean async
// dan retry terbatas.
//
// Pengiriman email tidak boleh membatalkan transaksi utama. Kegagalan setelah
// seluruh percobaan hanya dicatat ke log. Antrean bersifat in-memory sehingga
// email yang belum terkirim hilang saat proses berhenti.
package mailer

import (
	"bytes"
	"context"
	"crypto/tls"
	"fmt"
	"log/slog"
	"mime"
	"net"
	"net/mail"
	"net/smtp"
	"strconv"
	"strings"
	"sync"
	"time"
)

const (
	defaultQueueSize = 100
	dialTimeout      = 10 * time.Second
	smtpTimeout      = 30 * time.Second
)

// Config adalah parameter koneksi SMTP.
type Config struct {
	Host string
	Port int
	User string
	Pass string
	From string
}

// outgoing adalah email yang sudah divalidasi dan disusun menjadi bytes siap
// kirim. Alamat amplop dipisahkan dari header agar aman terhadap injeksi.
type outgoing struct {
	to   string
	data []byte
}

// Mailer mengelola antrean email dan worker pengiriman.
type Mailer struct {
	cfg         Config
	port        int
	from        mail.Address
	logger      *slog.Logger
	queue       chan outgoing
	retryDelays []time.Duration
	send        func(context.Context, outgoing) error
	wg          sync.WaitGroup
}

// New memvalidasi konfigurasi dan membuat mailer. Mailer belum berjalan sampai
// Start dipanggil.
func New(cfg Config, logger *slog.Logger) (*Mailer, error) {
	if strings.TrimSpace(cfg.Host) == "" {
		return nil, fmt.Errorf("mailer: SMTP_HOST wajib diisi")
	}
	if logger == nil {
		logger = slog.Default()
	}

	from, err := mail.ParseAddress(cfg.From)
	if err != nil {
		return nil, fmt.Errorf("mailer: SMTP_FROM tidak valid: %w", err)
	}

	port := cfg.Port
	if port <= 0 {
		port = 25
	}

	m := &Mailer{
		cfg:         cfg,
		port:        port,
		from:        *from,
		logger:      logger,
		queue:       make(chan outgoing, defaultQueueSize),
		retryDelays: []time.Duration{5 * time.Second, 30 * time.Second, 2 * time.Minute},
	}
	m.send = m.deliverSMTP
	return m, nil
}

// Start menjalankan satu worker yang mengirim email dari antrean. Worker
// berhenti ketika ctx dibatalkan; panggil Wait setelah pembatalan untuk
// memastikan goroutine selesai.
func (m *Mailer) Start(ctx context.Context) {
	m.wg.Add(1)
	go func() {
		defer m.wg.Done()
		for {
			select {
			case <-ctx.Done():
				return
			case msg := <-m.queue:
				m.deliverWithRetry(ctx, msg)
			}
		}
	}()
}

// Wait menunggu worker berhenti.
func (m *Mailer) Wait() {
	m.wg.Wait()
}

// Send memasukkan email ke antrean tanpa memblokir handler HTTP. Bila antrean
// penuh, email dibuang dan dicatat ke log agar respons pengguna tetap cepat.
func (m *Mailer) Send(to, subject, body string) {
	if m == nil {
		return
	}

	msg, err := m.compose(to, subject, body)
	if err != nil {
		m.logger.Warn("email tidak dapat disusun", "to", maskEmail(to), "err", err)
		return
	}

	select {
	case m.queue <- msg:
	default:
		m.logger.Warn("antrean email penuh; email dibuang", "to", maskEmail(to))
	}
}

// compose memvalidasi alamat, mencegah injeksi header, dan menyusun pesan MIME.
func (m *Mailer) compose(to, subject, body string) (outgoing, error) {
	recipient, err := mail.ParseAddress(strings.TrimSpace(to))
	if err != nil {
		return outgoing{}, fmt.Errorf("alamat penerima tidak valid: %w", err)
	}

	subject = sanitizeHeader(subject)
	if subject == "" {
		subject = "Notifikasi PPKPT"
	}

	var buf bytes.Buffer
	writeHeader(&buf, "From", m.from.String())
	writeHeader(&buf, "To", recipient.String())
	writeHeader(&buf, "Subject", mime.QEncoding.Encode("utf-8", subject))
	writeHeader(&buf, "Date", time.Now().Format(time.RFC1123Z))
	writeHeader(&buf, "MIME-Version", "1.0")
	writeHeader(&buf, "Content-Type", `text/html; charset="utf-8"`)
	writeHeader(&buf, "Content-Transfer-Encoding", "8bit")
	buf.WriteString("\r\n")
	buf.WriteString(normalizeNewlines(body))

	return outgoing{to: recipient.Address, data: buf.Bytes()}, nil
}

// deliverWithRetry mencoba mengirim email satu kali, lalu mengulang sesuai
// retryDelays dengan backoff. Percobaan berhenti saat ctx dibatalkan.
func (m *Mailer) deliverWithRetry(ctx context.Context, msg outgoing) {
	for attempt := 0; ; attempt++ {
		err := m.send(ctx, msg)
		if err == nil {
			return
		}
		if attempt >= len(m.retryDelays) {
			m.logger.Error("email gagal terkirim",
				"to", maskEmail(msg.to), "attempts", attempt+1, "err", err)
			return
		}

		m.logger.Warn("email gagal, akan dicoba lagi",
			"to", maskEmail(msg.to), "attempt", attempt+1, "err", err)
		if !sleepContext(ctx, m.retryDelays[attempt]) {
			return
		}
	}
}

// deliverSMTP menghubungi server SMTP, memakai STARTTLS bila tersedia, lalu
// mengirim pesan. PlainAuth hanya dipakai bila kredensial diisi.
func (m *Mailer) deliverSMTP(ctx context.Context, msg outgoing) error {
	addr := net.JoinHostPort(m.cfg.Host, strconv.Itoa(m.port))
	dialer := &net.Dialer{Timeout: dialTimeout}
	conn, err := dialer.DialContext(ctx, "tcp", addr)
	if err != nil {
		return fmt.Errorf("dial smtp: %w", err)
	}
	defer func() { _ = conn.Close() }()

	deadline := time.Now().Add(smtpTimeout)
	if d, ok := ctx.Deadline(); ok && d.Before(deadline) {
		deadline = d
	}
	if err := conn.SetDeadline(deadline); err != nil {
		return fmt.Errorf("set deadline smtp: %w", err)
	}

	client, err := smtp.NewClient(conn, m.cfg.Host)
	if err != nil {
		return fmt.Errorf("buat klien smtp: %w", err)
	}
	defer func() { _ = client.Close() }()

	if ok, _ := client.Extension("STARTTLS"); ok {
		if err := client.StartTLS(&tls.Config{
			ServerName: m.cfg.Host,
			MinVersion: tls.VersionTLS12,
		}); err != nil {
			return fmt.Errorf("starttls: %w", err)
		}
	}

	if m.cfg.User != "" {
		auth := smtp.PlainAuth("", m.cfg.User, m.cfg.Pass, m.cfg.Host)
		if err := client.Auth(auth); err != nil {
			return fmt.Errorf("autentikasi smtp: %w", err)
		}
	}

	if err := client.Mail(m.from.Address); err != nil {
		return fmt.Errorf("mail from: %w", err)
	}
	if err := client.Rcpt(msg.to); err != nil {
		return fmt.Errorf("rcpt to: %w", err)
	}

	w, err := client.Data()
	if err != nil {
		return fmt.Errorf("data smtp: %w", err)
	}
	if _, err := w.Write(msg.data); err != nil {
		_ = w.Close()
		return fmt.Errorf("tulis email: %w", err)
	}
	if err := w.Close(); err != nil {
		return fmt.Errorf("tutup data smtp: %w", err)
	}

	if err := client.Quit(); err != nil {
		return fmt.Errorf("quit smtp: %w", err)
	}
	return nil
}

// sleepContext menunggu durasi atau pembatalan context. Mengembalikan false bila
// context selesai lebih dulu.
func sleepContext(ctx context.Context, d time.Duration) bool {
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-timer.C:
		return true
	}
}

// writeHeader menulis satu header dengan akhir baris CRLF.
func writeHeader(buf *bytes.Buffer, key, value string) {
	buf.WriteString(key)
	buf.WriteString(": ")
	buf.WriteString(value)
	buf.WriteString("\r\n")
}

// sanitizeHeader membuang CR/LF dan merapatkan whitespace agar nilai tidak
// dapat menyisipkan header baru.
func sanitizeHeader(v string) string {
	v = strings.ReplaceAll(v, "\r", " ")
	v = strings.ReplaceAll(v, "\n", " ")
	return strings.Join(strings.Fields(v), " ")
}

// normalizeNewlines menyamakan akhir baris body menjadi CRLF sesuai RFC 5322.
func normalizeNewlines(s string) string {
	s = strings.ReplaceAll(s, "\r\n", "\n")
	s = strings.ReplaceAll(s, "\r", "\n")
	return strings.ReplaceAll(s, "\n", "\r\n")
}

// maskEmail menyamarkan alamat email agar tidak tercatat utuh di log.
func maskEmail(addr string) string {
	at := strings.LastIndex(addr, "@")
	if at <= 0 {
		return "***"
	}
	name := addr[:at]
	if len(name) >= 1 {
		name = name[:1] + "***"
	}
	return name + addr[at:]
}
