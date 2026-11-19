// Package notification menyusun dan mengirim email notifikasi kepada pelapor.
// Isi email sengaja minimal dan tidak memuat detail kasus
// sensitif; pelapor diarahkan membuka halaman cek status.
package notification

import (
	"bytes"
	"html/template"
	"log/slog"
	"strings"
)

// Sender adalah abstraksi pengirim email agar mudah diuji. Implementasi
// produksinya adalah *mailer.Mailer.
type Sender interface {
	Send(to, subject, body string)
}

// Service merender template dan meneruskan email ke Sender.
type Service struct {
	sender Sender
	logger *slog.Logger
}

// New membuat layanan notifikasi. logger boleh nil (memakai slog.Default).
func New(sender Sender, logger *slog.Logger) *Service {
	if logger == nil {
		logger = slog.Default()
	}
	return &Service{sender: sender, logger: logger}
}

// SendReportReceipt mengirim bukti pelaporan berisi kode tiket dan PIN. PIN
// hanya dikirim sekali dan tidak pernah ditulis ke log.
func (s *Service) SendReportReceipt(to, caseNumber, pin string) {
	if strings.TrimSpace(to) == "" {
		return
	}

	data := receiptData{CaseNumber: caseNumber, PIN: pin}
	body, err := render(receiptTemplate, data)
	if err != nil {
		s.logger.Error("gagal merender email bukti pelaporan", "err", err)
		return
	}
	s.sender.Send(to, "Bukti pelaporan kasus PPKPT", body)
}

// SendStatusUpdate memberi tahu pelapor bahwa status laporan berubah tanpa
// menyebutkan status atau isi kasus.
func (s *Service) SendStatusUpdate(to, caseNumber string) {
	if strings.TrimSpace(to) == "" {
		return
	}

	data := statusData{CaseNumber: caseNumber}
	body, err := render(statusTemplate, data)
	if err != nil {
		s.logger.Error("gagal merender email notifikasi status", "err", err)
		return
	}
	s.sender.Send(to, "Pembaruan status laporan PPKPT", body)
}

type receiptData struct {
	CaseNumber string
	PIN        string
}

type statusData struct {
	CaseNumber string
}

// Template memakai html/template agar data dinamis selalu di-escape.
var (
	receiptTemplate = template.Must(template.New("receipt").Parse(`<!doctype html>
<html lang="id">
<body>
<p>Halo,</p>
<p>Laporan Anda telah kami terima. Kami akan menanganinya dengan hati-hati dan menjaga kerahasiaan Anda.</p>
<p><strong>Kode tiket:</strong> {{.CaseNumber}}<br>
<strong>PIN:</strong> {{.PIN}}</p>
<p>Simpan kode tiket dan PIN ini. Keduanya diperlukan untuk memeriksa status laporan dan melengkapi laporan bila diminta.</p>
<p>Jangan bagikan PIN kepada siapa pun. Jika Anda merasa tidak aman, segera hubungi tim PPKPT.</p>
<p>Salam,<br>Tim PPKPT</p>
</body>
</html>`))

	statusTemplate = template.Must(template.New("status").Parse(`<!doctype html>
<html lang="id">
<body>
<p>Halo,</p>
<p>Status laporan dengan kode tiket <strong>{{.CaseNumber}}</strong> telah diperbarui.</p>
<p>Silakan buka halaman cek status dan masukkan kode tiket serta PIN Anda untuk melihat informasi terbaru.</p>
<p>Salam,<br>Tim PPKPT</p>
</body>
</html>`))
)

// render menjalankan template ke buffer agar kegagalan render tidak mengirim
// email setengah jadi.
func render(tmpl *template.Template, data any) (string, error) {
	var buf bytes.Buffer
	if err := tmpl.Execute(&buf, data); err != nil {
		return "", err
	}
	return buf.String(), nil
}
