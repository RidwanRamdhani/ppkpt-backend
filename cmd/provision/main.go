// Command provision mengelola akun petugas dan pejabat dari baris perintah.
// Tidak ada halaman manajemen akun di aplikasi: pembuatan, penonaktifan, dan
// reset password dilakukan oleh sistem melalui perintah ini, dan semuanya
// tercatat di audit log dengan aktor SYSTEM.
//
// Pemakaian:
//
//	provision create -name "Satgas A" -username satgas.a -email a@kampus.ac.id -role SATGAS
//	provision deactivate <username|email>
//	provision reset-password <username|email>
//	provision list
//
// Password tidak pernah diterima lewat argumen baris perintah (terlihat di
// daftar proses). Isi PROVISION_PASSWORD di lingkungan untuk menentukan
// password sendiri; bila kosong, password sementara acak dibuat dan
// ditampilkan SEKALI. Akun baru dan akun yang di-reset wajib mengganti
// password saat login pertama.
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
	"text/tabwriter"
	"time"

	"github.com/RidwanRamdhani/ppkpt-backend/internal/audit"
	"github.com/RidwanRamdhani/ppkpt-backend/internal/auth"
	"github.com/RidwanRamdhani/ppkpt-backend/internal/config"
	"github.com/RidwanRamdhani/ppkpt-backend/internal/platform/db"
	"github.com/RidwanRamdhani/ppkpt-backend/internal/platform/httpx"
	"github.com/joho/godotenv"
)

const (
	source         = "provision"
	passwordEnvVar = "PROVISION_PASSWORD" //nolint:gosec // nama variabel lingkungan
	connectTimeout = 15 * time.Second
	opTimeout      = 30 * time.Second

	exitOK      = 0
	exitFailure = 1
	exitUsage   = 2
)

// accounts adalah bagian auth.Service yang dipakai CLI (memudahkan pengujian).
type accounts interface {
	CreateUser(ctx context.Context, in auth.CreateUserInput) (*auth.User, error)
	DeactivateUser(ctx context.Context, identifier, source string) (bool, error)
	ResetPassword(ctx context.Context, identifier, newPassword, source string) error
	ListUsers(ctx context.Context) ([]auth.User, error)
}

type request struct {
	cmd        string
	create     auth.CreateUserInput
	identifier string
}

func main() {
	os.Exit(run(os.Args[1:], os.Stdout, os.Stderr, os.Getenv))
}

func run(args []string, stdout, stderr io.Writer, getenv func(string) string) int {
	req, code := parse(args, stderr)
	if code != exitOK {
		return code
	}

	_ = godotenv.Load()
	cfg, err := config.Load()
	if err != nil {
		say(stderr, "konfigurasi tidak valid:", err)
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
	return execute(ctx, svc, req, stdout, stderr, getenv)
}

const usageText = `Pemakaian:
  provision create -name NAMA -username USERNAME -email EMAIL -role SATGAS|PEJABAT
  provision deactivate USERNAME_ATAU_EMAIL
  provision reset-password USERNAME_ATAU_EMAIL
  provision list

Password: isi PROVISION_PASSWORD untuk menentukan sendiri; bila kosong dibuat
password sementara acak dan ditampilkan sekali.
`

// parse membaca argumen tanpa menyentuh database.
func parse(args []string, stderr io.Writer) (request, int) {
	usage := func(msg string) (request, int) {
		if msg != "" {
			say(stderr, msg)
		}
		sayp(stderr, usageText)
		return request{}, exitUsage
	}
	if len(args) == 0 {
		return usage("")
	}
	req := request{cmd: args[0]}
	rest := args[1:]

	switch req.cmd {
	case "create":
		fs := flag.NewFlagSet("create", flag.ContinueOnError)
		fs.SetOutput(stderr)
		name := fs.String("name", "", "nama lengkap")
		username := fs.String("username", "", "username")
		email := fs.String("email", "", "email")
		role := fs.String("role", "", "SATGAS atau PEJABAT")
		if err := fs.Parse(rest); err != nil {
			return request{}, exitUsage
		}
		if fs.NArg() > 0 {
			return usage("argumen tidak dikenal: " + strings.Join(fs.Args(), " "))
		}
		if *name == "" || *username == "" || *email == "" || *role == "" {
			return usage("create: -name, -username, -email, dan -role wajib diisi")
		}
		req.create = auth.CreateUserInput{
			Name: *name, Username: *username, Email: *email,
			Role: strings.ToUpper(strings.TrimSpace(*role)), MustChangePassword: true, Source: source,
		}
	case "deactivate", "reset-password":
		if len(rest) != 1 || strings.HasPrefix(rest[0], "-") {
			return usage(req.cmd + ": tepat satu argumen (username atau email) diperlukan")
		}
		req.identifier = rest[0]
	case "list":
		if len(rest) != 0 {
			return usage("list: tidak menerima argumen")
		}
	case "-h", "--help", "help":
		sayp(stderr, usageText)
		return request{}, exitUsage
	default:
		return usage("perintah tidak dikenal: " + req.cmd)
	}
	return req, exitOK
}

// execute menjalankan perintah. Dipisahkan dari run agar bisa diuji dengan fake.
func execute(ctx context.Context, acc accounts, req request, stdout, stderr io.Writer, getenv func(string) string) int {
	switch req.cmd {
	case "create":
		password, generated, err := resolvePassword(getenv)
		if err != nil {
			say(stderr, "gagal membuat password:", err)
			return exitFailure
		}
		in := req.create
		in.Password = password
		u, err := acc.CreateUser(ctx, in)
		if err != nil {
			return report(stderr, err)
		}
		sayf(stdout, "Akun dibuat: %s (%s), role %s\n", u.Username, u.Email, u.Role)
		printPassword(stdout, password, generated)

	case "deactivate":
		changed, err := acc.DeactivateUser(ctx, req.identifier, source)
		if err != nil {
			return report(stderr, err)
		}
		if changed {
			sayf(stdout, "Akun %s dinonaktifkan dan seluruh sesinya dicabut.\n", req.identifier)
		} else {
			sayf(stdout, "Akun %s sudah nonaktif; tidak ada perubahan.\n", req.identifier)
		}

	case "reset-password":
		password, generated, err := resolvePassword(getenv)
		if err != nil {
			say(stderr, "gagal membuat password:", err)
			return exitFailure
		}
		if err := acc.ResetPassword(ctx, req.identifier, password, source); err != nil {
			return report(stderr, err)
		}
		sayf(stdout, "Password akun %s direset; kunci dibuka dan seluruh sesi dicabut.\n", req.identifier)
		printPassword(stdout, password, generated)

	case "list":
		users, err := acc.ListUsers(ctx)
		if err != nil {
			return report(stderr, err)
		}
		tw := tabwriter.NewWriter(stdout, 0, 4, 2, ' ', 0)
		say(tw, "ID\tUSERNAME\tEMAIL\tROLE\tSTATUS\tGANTI PASSWORD\tLOGIN TERAKHIR")
		for _, u := range users {
			status, last := "aktif", "-"
			if !u.IsActive {
				status = "nonaktif"
			}
			if u.LastLoginAt != nil {
				last = u.LastLoginAt.Local().Format("2006-01-02 15:04")
			}
			must := "tidak"
			if u.MustChangePassword {
				must = "ya"
			}
			sayf(tw, "%d\t%s\t%s\t%s\t%s\t%s\t%s\n", u.ID, u.Username, u.Email, u.Role, status, must, last)
		}
		_ = tw.Flush()
	}
	return exitOK
}

// resolvePassword memakai PROVISION_PASSWORD bila ada, selain itu membuat acak.
func resolvePassword(getenv func(string) string) (password string, generated bool, err error) {
	if p := getenv(passwordEnvVar); p != "" {
		return p, false, nil
	}
	p, err := auth.GeneratePassword()
	return p, true, err
}

func printPassword(w io.Writer, password string, generated bool) {
	if !generated {
		say(w, "Password sesuai PROVISION_PASSWORD. Akun wajib menggantinya saat login pertama.")
		return
	}
	sayf(w, "Password sementara: %s\n", password)
	say(w, "Tampil sekali ini saja. Serahkan lewat saluran aman; akun wajib menggantinya saat login pertama.")
}

// report mencetak galat bagi operator. Galat validasi menampilkan setiap field.
func report(stderr io.Writer, err error) int {
	var app *httpx.AppError
	switch {
	case errors.As(err, &app):
		say(stderr, "gagal:", app.Message)
		keys := make([]string, 0, len(app.Fields))
		for k := range app.Fields {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			sayf(stderr, "  - %s: %s\n", k, app.Fields[k])
		}
	case errors.Is(err, context.DeadlineExceeded):
		say(stderr, "gagal: waktu habis")
	default:
		say(stderr, "gagal:", err)
	}
	return exitFailure
}

// say/sayf/sayp menulis ke w; galat tulis (mis. pipe putus) sengaja diabaikan
// karena tidak ada yang bisa dilakukan CLI selain keluar.
func say(w io.Writer, a ...any)                 { _, _ = fmt.Fprintln(w, a...) }
func sayf(w io.Writer, format string, a ...any) { _, _ = fmt.Fprintf(w, format, a...) }
func sayp(w io.Writer, a ...any)                { _, _ = fmt.Fprint(w, a...) }
