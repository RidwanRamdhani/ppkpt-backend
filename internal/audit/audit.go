// Package audit menyediakan layanan pencatatan audit log append-only.
//
// Semua perubahan data penting harus menulis satu baris audit di dalam transaksi
// yang sama dengan perubahannya. Paket ini sengaja dibuat kecil dan tidak
// menyimpan state agar mudah dipakai baik oleh pool maupun pgx.Tx.
package audit

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/jackc/pgx/v5/pgconn"
)

// Jenis aktor yang boleh tercatat pada audit log. Nilai ini dibatasi oleh CHECK
// constraint pada tabel audit_logs.
const (
	ActorUser     = "USER"
	ActorReporter = "REPORTER"
	ActorSystem   = "SYSTEM"
)

// DBTX adalah subset koneksi database yang dibutuhkan layanan audit, sehingga
// repository dapat memanggil Log baik di luar maupun di dalam transaksi.
type DBTX interface {
	Exec(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error)
}

// Event adalah satu kejadian yang akan dicatat ke audit_logs.
//
// Detail tidak boleh memuat data sensitif seperti PIN, password, token sesi,
// isi laporan, atau nama pihak. Service tetap membuang kunci yang dikenal
// sensitif sebagai lapisan pertahanan tambahan.
type Event struct {
	ActorType string         // USER | REPORTER | SYSTEM
	UserID    *int64         // wajib untuk ActorType USER
	Action    string         // gunakan konstanta Action* di actions.go
	Entity    string         // nama entitas, mis. "report" atau "evidence"
	EntityID  *int64         // ID entitas terkait, bila ada
	IP        string         // alamat IP aktor, bila diketahui
	Detail    map[string]any // metadata tambahan tanpa data sensitif
}

// Service menulis event audit ke database.
type Service struct{}

// NewService membuat layanan audit tanpa state.
func NewService() *Service {
	return &Service{}
}

const insertSQL = `
	INSERT INTO audit_logs (user_id, actor_type, action, entity, entity_id, ip_address, detail)
	VALUES ($1, $2, $3, $4, $5, $6, $7)`

// Log menulis satu event audit. Bila dipanggil dengan pgx.Tx, kegagalan audit
// akan membatalkan transaksi sehingga perubahan data tidak pernah tercatat
// tanpa jejak audit.
func (s *Service) Log(ctx context.Context, q DBTX, e Event) error {
	if err := e.validate(); err != nil {
		return err
	}

	detail, err := marshalDetail(e.Detail)
	if err != nil {
		return fmt.Errorf("audit: marshal detail: %w", err)
	}

	// Gunakan any agar detail kosong benar-benar dikirim sebagai NULL, bukan
	// []byte(nil) yang bergantung pada encoder driver.
	var detailArg any
	if detail != nil {
		detailArg = detail
	}

	var ip *string
	if v := strings.TrimSpace(e.IP); v != "" {
		ip = &v
	}

	if _, err := q.Exec(ctx, insertSQL,
		e.UserID,
		e.ActorType,
		e.Action,
		nullString(e.Entity),
		e.EntityID,
		ip,
		detailArg,
	); err != nil {
		return fmt.Errorf("audit: tulis log: %w", err)
	}
	return nil
}

// validate memastikan invariant minimum terpenuhi sebelum menulis ke database.
func (e Event) validate() error {
	switch e.ActorType {
	case ActorUser, ActorReporter, ActorSystem:
	default:
		return fmt.Errorf("audit: actor_type %q tidak valid", e.ActorType)
	}
	if strings.TrimSpace(e.Action) == "" {
		return fmt.Errorf("audit: action wajib diisi")
	}
	if e.ActorType == ActorUser && e.UserID == nil {
		return fmt.Errorf("audit: user_id wajib untuk aktor USER")
	}
	return nil
}

// sensitiveKeys adalah kunci detail yang tidak boleh tersimpan di audit log.
// Pemeriksaan dilakukan case-insensitive agar variasi penulisan tetap tertangkap.
var sensitiveKeys = map[string]struct{}{
	"pin":                    {},
	"pin_hash":               {},
	"password":               {},
	"password_hash":          {},
	"token":                  {},
	"session_token":          {},
	"cookie":                 {},
	"authorization":          {},
	"chronology":             {},
	"report_content":         {},
	"additional_text":        {},
	"additional_description": {},
}

// marshalDetail membersihkan detail dari kunci sensitif lalu mengubahnya menjadi
// JSONB. Detail kosong dikirim sebagai NULL agar kolom tetap ringkas.
func marshalDetail(detail map[string]any) ([]byte, error) {
	clean := make(map[string]any, len(detail))
	for k, v := range detail {
		if _, blocked := sensitiveKeys[strings.ToLower(k)]; blocked {
			continue
		}
		clean[k] = v
	}
	if len(clean) == 0 {
		return nil, nil
	}
	return json.Marshal(clean)
}

// nullString mengubah string kosong menjadi NULL agar kolom opsional tidak
// menyimpan nilai kosong yang tidak bermakna.
func nullString(v string) *string {
	v = strings.TrimSpace(v)
	if v == "" {
		return nil
	}
	return &v
}
