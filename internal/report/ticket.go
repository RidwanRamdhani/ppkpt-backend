// Package report menangani pelaporan publik (M01) dan cek status (M02):
// validasi formulir, pembuatan kode tiket + PIN, penyimpanan transaksional,
// serta notifikasi bukti pelaporan.
package report

import (
	"crypto/rand"
	"fmt"
	"math/big"
)

const (
	// TicketDigits adalah panjang kode tiket (8 digit, boleh berawalan nol).
	TicketDigits = 8
	// PINDigits adalah panjang PIN cek status (6 digit, boleh berawalan nol).
	PINDigits = 6

	ticketSpace = 100_000_000 // 00000000..99999999
	pinSpace    = 1_000_000   // 000000..999999
)

// GenerateTicket membuat kode tiket acak 8 digit memakai crypto/rand (NFR-06:
// tiket tidak boleh bisa ditebak). Kode dikembalikan sebagai string agar nol di
// depan tidak hilang.
func GenerateTicket() (string, error) {
	return newDigits(ticketSpace, TicketDigits)
}

// GeneratePIN membuat PIN acak 6 digit memakai crypto/rand. PIN hanya
// ditampilkan sekali kepada pelapor dan disimpan sebagai bcrypt hash.
func GeneratePIN() (string, error) {
	return newDigits(pinSpace, PINDigits)
}

// newDigits mengambil bilangan acak seragam [0, max) lalu memformatnya dengan
// lebar tetap (nol di depan dipertahankan).
func newDigits(max int64, width int) (string, error) {
	n, err := rand.Int(rand.Reader, big.NewInt(max))
	if err != nil {
		return "", fmt.Errorf("report: gagal membuat angka acak: %w", err)
	}
	return fmt.Sprintf("%0*d", width, n.Int64()), nil
}
