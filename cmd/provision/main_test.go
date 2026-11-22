package main

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/RidwanRamdhani/ppkpt-backend/internal/auth"
	"github.com/RidwanRamdhani/ppkpt-backend/internal/platform/httpx"
)

type fakeAccounts struct {
	created     []auth.CreateUserInput
	createErr   error
	deactivated []string
	changed     bool
	deactErr    error
	resetID     string
	resetPW     string
	resetErr    error
	users       []auth.User
}

func (f *fakeAccounts) CreateUser(_ context.Context, in auth.CreateUserInput) (*auth.User, error) {
	if f.createErr != nil {
		return nil, f.createErr
	}
	f.created = append(f.created, in)
	return &auth.User{ID: 1, Name: in.Name, Username: in.Username, Email: in.Email, Role: in.Role}, nil
}
func (f *fakeAccounts) DeactivateUser(_ context.Context, id, _ string) (bool, error) {
	f.deactivated = append(f.deactivated, id)
	return f.changed, f.deactErr
}
func (f *fakeAccounts) ResetPassword(_ context.Context, id, pw, _ string) error {
	f.resetID, f.resetPW = id, pw
	return f.resetErr
}
func (f *fakeAccounts) ListUsers(context.Context) ([]auth.User, error) { return f.users, nil }

func noEnv(string) string { return "" }

func TestParse(t *testing.T) {
	var errb bytes.Buffer
	req, code := parse([]string{"create", "-name", "Satgas A", "-username", "satgas.a", "-email", "a@x.id", "-role", "satgas"}, &errb)
	if code != exitOK || req.create.Role != "SATGAS" || !req.create.MustChangePassword || req.create.Source != "provision" {
		t.Fatalf("create: %+v code=%d err=%s", req, code, errb.String())
	}
	for name, args := range map[string][]string{
		"kosong":                  nil,
		"tidak dikenal":           {"hapus"},
		"create kurang":           {"create", "-name", "A"},
		"create sisa argumen":     {"create", "-name", "A", "-username", "u", "-email", "e", "-role", "SATGAS", "extra"},
		"deactivate tanpa arg":    {"deactivate"},
		"deactivate terlalu":      {"deactivate", "a", "b"},
		"reset tanpa arg":         {"reset-password"},
		"password lewat flag":     {"reset-password", "-password=rahasia"},
		"list dengan argumen":     {"list", "x"},
		"flag create tak dikenal": {"create", "-password", "x"},
	} {
		errb.Reset()
		if _, code := parse(args, &errb); code != exitUsage {
			t.Errorf("%s: exit %d, mau %d", name, code, exitUsage)
		}
	}
	if _, code := parse([]string{"deactivate", "satgas.a"}, &errb); code != exitOK {
		t.Fatal("deactivate valid ditolak")
	}
}

func TestExecuteCreate_PasswordAcakDitampilkanSekali(t *testing.T) {
	f := &fakeAccounts{}
	var out, errb bytes.Buffer
	req := request{cmd: "create", create: auth.CreateUserInput{Name: "A", Username: "a.b", Email: "a@x.id", Role: "SATGAS", MustChangePassword: true}}
	if code := execute(context.Background(), f, req, &out, &errb, noEnv); code != exitOK {
		t.Fatalf("exit %d: %s", code, errb.String())
	}
	pw := f.created[0].Password
	if auth.ValidatePassword(pw) != nil {
		t.Fatalf("password acak tidak lolos kebijakan: %q", pw)
	}
	if !strings.Contains(out.String(), pw) || strings.Count(out.String(), pw) != 1 {
		t.Fatalf("password harus tampil tepat sekali: %q", out.String())
	}
}

func TestExecuteCreate_PasswordDariEnvTidakDicetak(t *testing.T) {
	f := &fakeAccounts{}
	var out, errb bytes.Buffer
	getenv := func(k string) string {
		if k == "PROVISION_PASSWORD" {
			return "Sandi-Env-12345"
		}
		return ""
	}
	req := request{cmd: "create", create: auth.CreateUserInput{Name: "A", Username: "a.b", Email: "a@x.id", Role: "PEJABAT"}}
	if code := execute(context.Background(), f, req, &out, &errb, getenv); code != exitOK {
		t.Fatalf("exit %d", code)
	}
	if f.created[0].Password != "Sandi-Env-12345" {
		t.Fatal("password dari env tidak dipakai")
	}
	if strings.Contains(out.String(), "Sandi-Env-12345") {
		t.Fatal("password dari env tidak boleh dicetak ulang")
	}
}

func TestExecuteErrors(t *testing.T) {
	var out, errb bytes.Buffer
	valErr := httpx.ErrValidation("Data akun tidak valid.")
	valErr.Fields = map[string]string{"username": "tidak valid", "email": "tidak valid"}
	f := &fakeAccounts{createErr: valErr}
	req := request{cmd: "create", create: auth.CreateUserInput{Role: "SATGAS"}}
	if code := execute(context.Background(), f, req, &out, &errb, noEnv); code != exitFailure {
		t.Fatalf("exit %d", code)
	}
	for _, want := range []string{"Data akun tidak valid.", "username: tidak valid", "email: tidak valid"} {
		if !strings.Contains(errb.String(), want) {
			t.Errorf("stderr tanpa %q: %s", want, errb.String())
		}
	}

	errb.Reset()
	f = &fakeAccounts{createErr: auth.ErrUserExists}
	if code := execute(context.Background(), f, req, &out, &errb, noEnv); code != exitFailure || !strings.Contains(errb.String(), "sudah ada") {
		t.Fatalf("akun ganda: %d %s", code, errb.String())
	}

	errb.Reset()
	f = &fakeAccounts{deactErr: auth.ErrUserNotFound}
	if code := execute(context.Background(), f, request{cmd: "deactivate", identifier: "x"}, &out, &errb, noEnv); code != exitFailure {
		t.Fatalf("akun tak ada: %d", code)
	}
	errb.Reset()
	f = &fakeAccounts{resetErr: errors.New("db mati")}
	if code := execute(context.Background(), f, request{cmd: "reset-password", identifier: "x"}, &out, &errb, noEnv); code != exitFailure || !strings.Contains(errb.String(), "db mati") {
		t.Fatalf("galat umum: %d %s", code, errb.String())
	}
}

func TestExecuteDeactivateResetList(t *testing.T) {
	var out, errb bytes.Buffer
	f := &fakeAccounts{changed: true}
	execute(context.Background(), f, request{cmd: "deactivate", identifier: "satgas.a"}, &out, &errb, noEnv)
	if !strings.Contains(out.String(), "dinonaktifkan") || f.deactivated[0] != "satgas.a" {
		t.Fatalf("deactivate: %s", out.String())
	}
	out.Reset()
	f.changed = false
	execute(context.Background(), f, request{cmd: "deactivate", identifier: "satgas.a"}, &out, &errb, noEnv)
	if !strings.Contains(out.String(), "sudah nonaktif") {
		t.Fatalf("deactivate ulang: %s", out.String())
	}

	out.Reset()
	execute(context.Background(), f, request{cmd: "reset-password", identifier: "satgas.a"}, &out, &errb, noEnv)
	if f.resetID != "satgas.a" || auth.ValidatePassword(f.resetPW) != nil || !strings.Contains(out.String(), f.resetPW) {
		t.Fatalf("reset: %+v / %s", f, out.String())
	}

	out.Reset()
	login := time.Date(2026, 11, 7, 9, 30, 0, 0, time.UTC)
	f.users = []auth.User{
		{ID: 1, Username: "satgas.a", Email: "a@x.id", Role: "SATGAS", IsActive: true, LastLoginAt: &login},
		{ID: 2, Username: "pejabat.a", Email: "p@x.id", Role: "PEJABAT", IsActive: false, MustChangePassword: true},
	}
	execute(context.Background(), f, request{cmd: "list"}, &out, &errb, noEnv)
	for _, want := range []string{"satgas.a", "pejabat.a", "aktif", "nonaktif", "ya"} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("list tanpa %q:\n%s", want, out.String())
		}
	}
	if strings.Contains(strings.ToLower(out.String()), "hash") {
		t.Fatal("list tidak boleh menampilkan hash")
	}
}
