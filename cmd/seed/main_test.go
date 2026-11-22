package main

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/RidwanRamdhani/ppkpt-backend/internal/auth"
	"github.com/RidwanRamdhani/ppkpt-backend/internal/platform/httpx"
)

type fakeCreator struct {
	existing map[string]bool
	fail     map[string]error
	created  []auth.CreateUserInput
}

func (f *fakeCreator) CreateUser(_ context.Context, in auth.CreateUserInput) (*auth.User, error) {
	if err := f.fail[in.Username]; err != nil {
		return nil, err
	}
	if f.existing[in.Username] {
		return nil, auth.ErrUserExists
	}
	f.created = append(f.created, in)
	return &auth.User{Username: in.Username, Email: in.Email, Role: in.Role}, nil
}

func env(m map[string]string) func(string) string { return func(k string) string { return m[k] } }

func TestPlan_TanpaEnvDanTanpaDemo(t *testing.T) {
	a, err := plan(env(nil), false)
	if err != nil || len(a) != 0 {
		t.Fatalf("plan kosong = %v, %v", a, err)
	}
	var out, errb bytes.Buffer
	if code := seed(context.Background(), &fakeCreator{}, a, &out, &errb); code != exitOK || !strings.Contains(out.String(), "Tidak ada akun") {
		t.Fatalf("seed kosong: %d %s", code, out.String())
	}
}

func TestPlan_AkunAwalDariEnv(t *testing.T) {
	a, err := plan(env(map[string]string{
		"SEED_SATGAS_USERNAME": "satgas.utama", "SEED_SATGAS_EMAIL": "satgas@kampus.ac.id", "SEED_SATGAS_NAME": "Ketua",
		"SEED_PEJABAT_USERNAME": "rektor", "SEED_PEJABAT_EMAIL": "rektor@kampus.ac.id", "SEED_PEJABAT_PASSWORD": "Sandi-Rektor-123",
		"SEED_DEMO_PASSWORD": "tidak-dipakai-tanpa-demo",
	}), false)
	if err != nil || len(a) != 2 {
		t.Fatalf("plan = %v, %v", a, err)
	}
	s, p := a[0], a[1]
	if s.in.Role != auth.RoleSatgas || s.in.Name != "Ketua" || !s.generated || auth.ValidatePassword(s.in.Password) != nil || !s.in.MustChangePassword {
		t.Fatalf("satgas: %+v", s)
	}
	if p.in.Role != auth.RolePejabat || p.generated || p.in.Password != "Sandi-Rektor-123" || p.in.Name != "Pejabat Berwenang" {
		t.Fatalf("pejabat: %+v", p)
	}

	// Grup tanpa email dilewati.
	a, _ = plan(env(map[string]string{"SEED_SATGAS_USERNAME": "x"}), false)
	if len(a) != 0 {
		t.Fatal("grup tanpa email harus dilewati")
	}
}

func TestPlan_Demo(t *testing.T) {
	a, err := plan(env(nil), true)
	if err != nil || len(a) != 4 {
		t.Fatalf("plan demo = %d, %v", len(a), err)
	}
	for _, x := range a {
		if x.in.MustChangePassword || x.in.Password != defaultDemoPassword || !strings.HasSuffix(x.in.Email, "@demo.ppkpt.local") || x.generated {
			t.Fatalf("akun demo salah: %+v", x)
		}
	}
	if auth.ValidatePassword(defaultDemoPassword) != nil {
		t.Fatal("password demo bawaan harus lolos kebijakan agar akun demo bisa dibuat")
	}
	a, _ = plan(env(map[string]string{"SEED_DEMO_PASSWORD": "Demo-Lain-2026"}), true)
	if a[0].in.Password != "Demo-Lain-2026" {
		t.Fatal("SEED_DEMO_PASSWORD tidak dipakai")
	}
}

func TestSeed_IdempotenDanMelaporkanGalat(t *testing.T) {
	accts, _ := plan(env(nil), true)
	valErr := httpx.ErrValidation("Data akun tidak valid.")
	valErr.Fields = map[string]string{"password": "lemah"}
	f := &fakeCreator{existing: map[string]bool{"satgas.a": true}, fail: map[string]error{"satgas.c": valErr}}
	var out, errb bytes.Buffer

	code := seed(context.Background(), f, accts, &out, &errb)
	if code != exitFailure {
		t.Fatalf("exit %d, mau %d karena satu akun gagal", code, exitFailure)
	}
	if !strings.Contains(out.String(), "Dilewati (sudah ada): satgas.a") {
		t.Fatalf("akun yang ada harus dilewati: %s", out.String())
	}
	if len(f.created) != 2 { // satgas.b dan pejabat.a tetap dibuat walau satgas.c gagal
		t.Fatalf("dibuat %d akun, mau 2", len(f.created))
	}
	if !strings.Contains(errb.String(), "password: lemah") {
		t.Fatalf("detail field harus tampil: %s", errb.String())
	}

	// Dijalankan ulang tanpa galat buatan: semua sudah ada -> sukses.
	f2 := &fakeCreator{existing: map[string]bool{"satgas.a": true, "satgas.b": true, "satgas.c": true, "pejabat.a": true}}
	out.Reset()
	if code := seed(context.Background(), f2, accts, &out, &errb); code != exitOK || strings.Count(out.String(), "Dilewati") != 4 {
		t.Fatalf("seed ulang: %d %s", code, out.String())
	}
}

func TestSeed_PasswordAcakHanyaDicetakBilaDibuat(t *testing.T) {
	accts, _ := plan(env(map[string]string{"SEED_SATGAS_USERNAME": "satgas.utama", "SEED_SATGAS_EMAIL": "s@kampus.ac.id"}), false)
	var out, errb bytes.Buffer
	seed(context.Background(), &fakeCreator{}, accts, &out, &errb)
	if !strings.Contains(out.String(), accts[0].in.Password) {
		t.Fatal("password acak harus dicetak saat akun dibuat")
	}
	out.Reset()
	seed(context.Background(), &fakeCreator{existing: map[string]bool{"satgas.utama": true}}, accts, &out, &errb)
	if strings.Contains(out.String(), accts[0].in.Password) {
		t.Fatal("password tidak boleh dicetak bila akun dilewati")
	}
	_ = errors.New
}

func TestIsProduction(t *testing.T) {
	for env, want := range map[string]bool{"production": true, "PROD": true, "prod": true, "dev": false, "staging": false, "": false} {
		if isProduction(env) != want {
			t.Errorf("isProduction(%q) != %v", env, want)
		}
	}
}
