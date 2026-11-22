// Command seed membuat akun awal saat deployment. Idempotent: akun yang sudah
// ada dilewati, sehingga aman dijalankan ulang.
//
// Akun awal (semua lingkungan) dibaca dari environment, satu grup per role:
//
//	SEED_SATGAS_NAME / SEED_SATGAS_USERNAME / SEED_SATGAS_EMAIL / SEED_SATGAS_PASSWORD
//	SEED_PEJABAT_NAME / SEED_PEJABAT_USERNAME / SEED_PEJABAT_EMAIL / SEED_PEJABAT_PASSWORD
//
// Grup dilewati bila USERNAME atau EMAIL kosong. Bila PASSWORD kosong dibuat
// password sementara acak yang ditampilkan sekali. Akun awal wajib mengganti
// password saat login pertama.
//
// Dengan -demo (hanya di luar production) dibuat juga akun demo untuk
// pengembangan dan demonstrasi (Satgas A/B/C dan Pejabat A) yang tidak
// diwajibkan mengganti password. Password demo: SEED_DEMO_PASSWORD, atau nilai
// bawaan di bawah. Jangan pernah dipakai dengan data nyata.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"os"
	"sort"
	"strings"
	"time"

	"github.com/RidwanRamdhani/ppkpt-backend/internal/audit"
	"github.com/RidwanRamdhani/ppkpt-backend/internal/auth"
	"github.com/RidwanRamdhani/ppkpt-backend/internal/config"
	"github.com/RidwanRamdhani/ppkpt-backend/internal/platform/db"
	"github.com/RidwanRamdhani/ppkpt-backend/internal/platform/httpx"
	"github.com/joho/godotenv"
)

const (
	source         = "seed"
	connectTimeout = 15 * time.Second
	opTimeout      = 60 * time.Second

	// defaultDemoPassword hanya dipakai bila SEED_DEMO_PASSWORD kosong dan
	// -demo aktif di lingkungan non-production.
	defaultDemoPassword = "Demo-Sandi-2026" //nolint:gosec // hanya akun demo non-production

	exitOK      = 0
	exitFailure = 1
	exitUsage   = 2
)

// accountCreator adalah bagian auth.Service yang dipakai seeder.
type accountCreator interface {
	CreateUser(ctx context.Context, in auth.CreateUserInput) (*auth.User, error)
}

type account struct {
	in        auth.CreateUserInput
	generated bool // password dibuat acak dan perlu ditampilkan
}

func main() {
	os.Exit(run(os.Args[1:], os.Stdout, os.Stderr, os.Getenv))
}

func run(args []string, stdout, stderr io.Writer, getenv func(string) string) int {
	fs := flag.NewFlagSet("seed", flag.ContinueOnError)
	fs.SetOutput(stderr)
	demo := fs.Bool("demo", false, "buat juga akun demo (hanya di luar production)")
	if err := fs.Parse(args); err != nil {
		return exitUsage
	}
	if fs.NArg() > 0 {
		say(stderr, "argumen tidak dikenal:", strings.Join(fs.Args(), " "))
		return exitUsage
	}

	_ = godotenv.Load()
	cfg, err := config.Load()
	if err != nil {
		say(stderr, "konfigurasi tidak valid:", err)
		return exitFailure
	}
	if *demo && isProduction(cfg.AppEnv) {
		say(stderr, "-demo ditolak: APP_ENV production tidak boleh memakai akun demo")
		return exitFailure
	}

	accounts, err := plan(getenv, *demo)
	if err != nil {
		say(stderr, "gagal menyiapkan daftar akun:", err)
		return exitFailure
	}

	ctx, cancel := context.WithTimeout(context.Background(), connectTimeout)
	pool, err := db.Connect(ctx, cfg.DatabaseURL)
	cancel()
	if err != nil {
		say(stderr, "database tidak siap:", err)
		return exitFailure
	}
	defer pool.Close()

	logger := slog.New(slog.NewTextHandler(stderr, &slog.HandlerOptions{Level: slog.LevelWarn}))
	svc, err := auth.NewService(pool, audit.NewService(), logger, auth.OptionsFromConfig(cfg))
	if err != nil {
		say(stderr, "gagal menyiapkan layanan autentikasi:", err)
		return exitFailure
	}

	ctx, cancel = context.WithTimeout(context.Background(), opTimeout)
	defer cancel()
	return seed(ctx, svc, accounts, stdout, stderr)
}

func isProduction(env string) bool {
	e := strings.ToLower(env)
	return e == "production" || e == "prod"
}

// plan menyusun daftar akun yang akan dibuat dari environment.
func plan(getenv func(string) string, demo bool) ([]account, error) {
	var out []account
	for _, g := range []struct{ prefix, role, defName string }{
		{"SEED_SATGAS_", auth.RoleSatgas, "Satgas PPKPT"},
		{"SEED_PEJABAT_", auth.RolePejabat, "Pejabat Berwenang"},
	} {
		username, email := getenv(g.prefix+"USERNAME"), getenv(g.prefix+"EMAIL")
		if username == "" || email == "" {
			continue
		}
		name := getenv(g.prefix + "NAME")
		if name == "" {
			name = g.defName
		}
		password, generated := getenv(g.prefix+"PASSWORD"), false
		if password == "" {
			p, err := auth.GeneratePassword()
			if err != nil {
				return nil, err
			}
			password, generated = p, true
		}
		out = append(out, account{
			in: auth.CreateUserInput{Name: name, Username: username, Email: email, Role: g.role,
				Password: password, MustChangePassword: true, Source: source},
			generated: generated,
		})
	}

	if demo {
		pw := getenv("SEED_DEMO_PASSWORD")
		if pw == "" {
			pw = defaultDemoPassword
		}
		for _, d := range []struct{ name, username, role string }{
			{"Satgas A", "satgas.a", auth.RoleSatgas},
			{"Satgas B", "satgas.b", auth.RoleSatgas},
			{"Satgas C", "satgas.c", auth.RoleSatgas},
			{"Pejabat A", "pejabat.a", auth.RolePejabat},
		} {
			out = append(out, account{in: auth.CreateUserInput{
				Name: d.name, Username: d.username, Email: d.username + "@demo.ppkpt.local", Role: d.role,
				Password: pw, MustChangePassword: false, Source: source + "-demo",
			}})
		}
	}
	return out, nil
}

// seed membuat tiap akun. Akun yang sudah ada dilewati; galat lain membuat
// exit code 1 tetapi akun lain tetap diproses.
func seed(ctx context.Context, svc accountCreator, accounts []account, stdout, stderr io.Writer) int {
	if len(accounts) == 0 {
		say(stdout, "Tidak ada akun untuk dibuat: isi SEED_SATGAS_USERNAME/EMAIL atau SEED_PEJABAT_USERNAME/EMAIL, atau pakai -demo.")
		return exitOK
	}
	code := exitOK
	for _, a := range accounts {
		u, err := svc.CreateUser(ctx, a.in)
		switch {
		case errors.Is(err, auth.ErrUserExists):
			sayf(stdout, "Dilewati (sudah ada): %s\n", a.in.Username)
		case err != nil:
			sayf(stderr, "Gagal membuat %s: %s\n", a.in.Username, describe(err))
			code = exitFailure
		default:
			sayf(stdout, "Dibuat: %s (%s), role %s\n", u.Username, u.Email, u.Role)
			if a.generated {
				sayf(stdout, "  Password sementara: %s (tampil sekali; wajib diganti saat login pertama)\n", a.in.Password)
			}
		}
	}
	return code
}

// describe menyertakan detail field pada galat validasi.
func describe(err error) string {
	var app *httpx.AppError
	if !errors.As(err, &app) {
		return err.Error()
	}
	if len(app.Fields) == 0 {
		return app.Message
	}
	parts := make([]string, 0, len(app.Fields))
	for k, v := range app.Fields {
		parts = append(parts, k+": "+v)
	}
	sort.Strings(parts)
	return app.Message + " (" + strings.Join(parts, "; ") + ")"
}

// say/sayf/sayp menulis ke w; galat tulis (mis. pipe putus) sengaja diabaikan
// karena tidak ada yang bisa dilakukan CLI selain keluar.
func say(w io.Writer, a ...any)                 { _, _ = fmt.Fprintln(w, a...) }
func sayf(w io.Writer, format string, a ...any) { _, _ = fmt.Fprintf(w, format, a...) }
