package audit

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgconn"
)

// fakeDB merekam query beserta argumen yang dikirim oleh Service.Log.
type fakeDB struct {
	sql  string
	args []any
	err  error
}

func (f *fakeDB) Exec(_ context.Context, sql string, args ...any) (pgconn.CommandTag, error) {
	f.sql = sql
	f.args = args
	return pgconn.CommandTag{}, f.err
}

func TestLog_MenulisInsertDenganDetailBersih(t *testing.T) {
	db := &fakeDB{}
	svc := NewService()
	userID := int64(7)
	entityID := int64(42)

	err := svc.Log(context.Background(), db, Event{
		ActorType: ActorUser,
		UserID:    &userID,
		Action:    ActionCaseTaken,
		Entity:    "report",
		EntityID:  &entityID,
		IP:        "203.0.113.5",
		Detail: map[string]any{
			"old_status": "DILAPORKAN",
			"pin":        "123456",  // harus dibuang
			"Password":   "rahasia", // case-insensitive
		},
	})
	if err != nil {
		t.Fatalf("Log: %v", err)
	}

	if !strings.Contains(strings.ToUpper(db.sql), "INSERT INTO AUDIT_LOGS") {
		t.Fatalf("query bukan INSERT: %s", db.sql)
	}

	// Argumen terakhir adalah detail JSON; kunci sensitif harus hilang.
	raw, ok := db.args[len(db.args)-1].([]byte)
	if !ok {
		t.Fatalf("detail bukan []byte: %T", db.args[len(db.args)-1])
	}
	var detail map[string]any
	if err := json.Unmarshal(raw, &detail); err != nil {
		t.Fatalf("detail bukan JSON valid: %v", err)
	}
	if _, ada := detail["pin"]; ada {
		t.Error("kunci pin masih ada di detail")
	}
	if _, ada := detail["Password"]; ada {
		t.Error("kunci Password masih ada di detail")
	}
	if detail["old_status"] != "DILAPORKAN" {
		t.Errorf("old_status = %v", detail["old_status"])
	}
}

func TestLog_MemvalidasiActorType(t *testing.T) {
	svc := NewService()
	userID := int64(1)

	cases := []Event{
		{ActorType: "ORANG", UserID: &userID, Action: ActionLoginSuccess},
		{ActorType: ActorUser, Action: ActionLoginSuccess}, // user_id nil
		{ActorType: ActorSystem, Action: ""},               // action kosong
	}
	for i, e := range cases {
		if err := svc.Log(context.Background(), &fakeDB{}, e); err == nil {
			t.Errorf("kasus %d: mau error validasi", i)
		}
	}
}

func TestLog_DetailKosongMenjadiNull(t *testing.T) {
	db := &fakeDB{}
	svc := NewService()
	if err := svc.Log(context.Background(), db, Event{
		ActorType: ActorSystem,
		Action:    ActionNotificationSent,
	}); err != nil {
		t.Fatalf("Log: %v", err)
	}
	if db.args[len(db.args)-1] != nil {
		t.Errorf("detail kosong = %v, mau nil", db.args[len(db.args)-1])
	}
}

func TestLog_MeneruskanErrorDatabase(t *testing.T) {
	want := errors.New("db mati")
	err := NewService().Log(context.Background(), &fakeDB{err: want}, Event{
		ActorType: ActorSystem,
		Action:    ActionNotificationSent,
	})
	if !errors.Is(err, want) {
		t.Fatalf("err = %v, mau membungkus %v", err, want)
	}
}
