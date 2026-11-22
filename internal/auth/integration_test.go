package auth

// Tes integrasi: butuh PostgreSQL dengan migrasi 0001 terpasang. Isi
// TEST_DATABASE_URL (memakai role aplikasi ppkpt_app), misalnya:
//
//	TEST_DATABASE_URL=postgres://ppkpt_app:secret@localhost:5432/ppkpt?sslmode=disable go test ./internal/auth
//
// Catatan: audit_logs append-only dan users direferensikan audit_logs, sehingga
// akun uji (username berawalan "test_auth_") tidak dapat dihapus setelah tes.

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/RidwanRamdhani/ppkpt-backend/internal/audit"
	"github.com/RidwanRamdhani/ppkpt-backend/internal/platform/httpx"
	"github.com/RidwanRamdhani/ppkpt-backend/internal/platform/ratelimit"
	"github.com/jackc/pgx/v5/pgxpool"
	"golang.org/x/crypto/bcrypt"
)

const testPassword = "Sandi-Uji-12345" //nolint:gosec // kredensial uji, bukan rahasia

var counter atomic.Int64

type env struct {
	t     *testing.T
	pool  *pgxpool.Pool
	svc   *Service
	clock time.Time
}

func newEnv(t *testing.T, opts ...func(*Options)) *env {
	t.Helper()
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("TEST_DATABASE_URL tidak diisi; tes integrasi dilewati")
	}
	pool, err := pgxpool.New(context.Background(), dsn)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	t.Cleanup(pool.Close)

	o := Options{BcryptCost: bcrypt.MinCost, LoginMaxAttempts: 3, LoginLockDuration: 10 * time.Minute,
		SessionIdle: 30 * time.Minute, SessionAbsolute: 8 * time.Hour}
	for _, f := range opts {
		f(&o)
	}
	svc, err := NewService(pool, audit.NewService(), slog.New(slog.NewTextHandler(io.Discard, nil)), o)
	if err != nil {
		t.Fatal(err)
	}
	e := &env{t: t, pool: pool, svc: svc, clock: time.Now().UTC().Truncate(time.Microsecond)}
	svc.now = func() time.Time { return e.clock }
	return e
}

func (e *env) advance(d time.Duration) { e.clock = e.clock.Add(d) }

func (e *env) newUser(role string, mustChange bool) (username, email string) {
	e.t.Helper()
	n := fmt.Sprintf("test_auth_%d_%d", time.Now().UnixNano(), counter.Add(1))
	_, err := e.svc.CreateUser(context.Background(), CreateUserInput{
		Name: "Pengguna Uji", Username: n, Email: n + "@test.local", Role: role,
		Password: testPassword, MustChangePassword: mustChange, Source: "test",
	})
	if err != nil {
		e.t.Fatalf("CreateUser: %v", err)
	}
	return n, n + "@test.local"
}

func (e *env) login(ident, pw string) (*LoginResult, error) {
	return e.svc.Login(context.Background(), LoginInput{Identifier: ident, Password: pw, IP: "203.0.113.9", UserAgent: "go-test"})
}

func (e *env) count(q string, args ...any) int {
	e.t.Helper()
	var n int
	if err := e.pool.QueryRow(context.Background(), q, args...).Scan(&n); err != nil {
		e.t.Fatalf("count: %v", err)
	}
	return n
}

func (e *env) auditCount(username, action string) int {
	return e.count(`SELECT count(*) FROM audit_logs a JOIN users u ON u.id = a.user_id WHERE u.username = $1 AND a.action = $2`, username, action)
}

// wantStatus memastikan err adalah AppError dengan status tertentu.
func wantStatus(t *testing.T, err error, status int) {
	t.Helper()
	_ = appErr(t, err, status)
}

func appErr(t *testing.T, err error, status int) *httpx.AppError {
	t.Helper()
	var ae *httpx.AppError
	if !errors.As(err, &ae) || ae.Status != status {
		t.Fatalf("error = %v, mau AppError status %d", err, status)
	}
	return ae
}

func TestLogin_BerhasilDenganUsernameDanEmail(t *testing.T) {
	e := newEnv(t)
	username, email := e.newUser(RoleSatgas, false)

	for _, ident := range []string{username, strings.ToUpper(email), "  " + username + " "} {
		res, err := e.login(ident, testPassword)
		if err != nil {
			t.Fatalf("login %q: %v", ident, err)
		}
		if res.User.Role != RoleSatgas || res.User.Username != username || res.Token == "" {
			t.Fatalf("hasil login salah: %+v", res)
		}
		if got := e.count(`SELECT count(*) FROM sessions WHERE id = $1`, hashToken(res.Token)); got != 1 {
			t.Fatal("sesi tersimpan harus berupa hash token")
		}
		if got := e.count(`SELECT count(*) FROM sessions WHERE id = $1`, res.Token); got != 0 {
			t.Fatal("token mentah tidak boleh tersimpan")
		}
		if !res.ExpiresAt.Equal(e.clock.Add(8 * time.Hour)) {
			t.Fatalf("expires = %v", res.ExpiresAt)
		}
	}
	if e.auditCount(username, audit.ActionLoginSuccess) != 3 {
		t.Fatal("LOGIN_SUCCESS harus tercatat tiap login")
	}
	var h string
	_ = e.pool.QueryRow(context.Background(), `SELECT password_hash FROM users WHERE username=$1`, username).Scan(&h)
	if !strings.HasPrefix(h, "$2") || h == testPassword {
		t.Fatalf("password harus tersimpan sebagai hash bcrypt, dapat %q", h)
	}
}

func TestLogin_GagalPesanSeragam(t *testing.T) {
	e := newEnv(t)
	username, _ := e.newUser(RoleSatgas, false)
	inactive, _ := e.newUser(RolePejabat, false)
	if _, err := e.svc.DeactivateUser(context.Background(), inactive, "test"); err != nil {
		t.Fatal(err)
	}

	_, errUnknown := e.login("tidak_ada_"+username, testPassword)
	_, errWrong := e.login(username, "Salah-Sandi-1")
	_, errInactive := e.login(inactive, testPassword)
	a, b, c := appErr(t, errUnknown, 401), appErr(t, errWrong, 401), appErr(t, errInactive, 401)
	if a.Message != b.Message || b.Message != c.Message {
		t.Fatal("pesan galat harus sama agar keberadaan akun tidak bocor")
	}
	if e.auditCount(username, audit.ActionLoginFailed) != 1 {
		t.Fatal("LOGIN_FAILED harus tercatat")
	}
	if e.count(`SELECT count(*) FROM audit_logs WHERE actor_type = 'SYSTEM' AND action = $1 AND detail->>'reason' = 'unknown_identifier'`,
		audit.ActionLoginFailed) < 1 {
		t.Fatal("login identitas tak dikenal harus tercatat sebagai SYSTEM")
	}

	if _, err := e.login("", ""); err == nil {
		t.Fatal("kredensial kosong harus ditolak")
	} else if ae := appErr(t, err, 422); ae.Fields["identifier"] == "" || ae.Fields["password"] == "" {
		t.Fatalf("fields = %v", ae.Fields)
	}
	if _, err := e.login(username, strings.Repeat("x", 100)); err == nil {
		t.Fatal("password terlalu panjang harus ditolak")
	}
}

func TestLogin_KunciAkunDanBukaSetelahWaktu(t *testing.T) {
	e := newEnv(t) // 3 percobaan, kunci 10 menit
	username, _ := e.newUser(RoleSatgas, false)

	for i := 0; i < 2; i++ {
		_, err := e.login(username, "Salah-Sandi-1")
		wantStatus(t, err, 401)
	}
	_, err := e.login(username, "Salah-Sandi-1")
	ae := appErr(t, err, 423)
	if ae.Fields["locked_until"] == "" {
		t.Fatal("respons 423 harus memuat locked_until")
	}
	if e.auditCount(username, audit.ActionAccountLocked) != 1 {
		t.Fatal("ACCOUNT_LOCKED harus tercatat")
	}

	// Password benar pun ditolak selama dikunci.
	_, err = e.login(username, testPassword)
	wantStatus(t, err, 423)

	e.advance(11 * time.Minute)
	if _, err := e.login(username, testPassword); err != nil {
		t.Fatalf("setelah kunci berakhir login harus berhasil: %v", err)
	}
	// Penghitung direset: dua kegagalan lagi belum mengunci.
	for i := 0; i < 2; i++ {
		_, err := e.login(username, "Salah-Sandi-1")
		wantStatus(t, err, 401)
	}
}

func TestAuthenticate_IdleDanAbsolut(t *testing.T) {
	e := newEnv(t)
	username, _ := e.newUser(RoleSatgas, false)

	res, _ := e.login(username, testPassword)
	p, err := e.svc.Authenticate(context.Background(), res.Token)
	if err != nil || p.Role != RoleSatgas || p.Username != username {
		t.Fatalf("Authenticate = %+v, %v", p, err)
	}

	// Idle: tidak aktif > 30 menit -> sesi mati dan baris dihapus.
	e.advance(31 * time.Minute)
	_, err = e.svc.Authenticate(context.Background(), res.Token)
	wantStatus(t, err, 401)
	if e.count(`SELECT count(*) FROM sessions WHERE id = $1`, hashToken(res.Token)) != 0 {
		t.Fatal("sesi idle harus dihapus")
	}

	// Aktivitas menjaga sesi tetap hidup, tetapi batas mutlak 8 jam tetap berlaku.
	res, _ = e.login(username, testPassword)
	for i := 1; i <= 23; i++ { // 23 x 20 menit = 7j40m
		e.advance(20 * time.Minute)
		if _, err := e.svc.Authenticate(context.Background(), res.Token); err != nil {
			t.Fatalf("langkah %d: sesi aktif seharusnya masih berlaku: %v", i, err)
		}
	}
	e.advance(25 * time.Minute) // lewat 8 jam sejak login
	_, err = e.svc.Authenticate(context.Background(), res.Token)
	wantStatus(t, err, 401)

	// Token acak yang tidak pernah dibuat.
	tok, _, _ := newToken()
	_, err = e.svc.Authenticate(context.Background(), tok)
	wantStatus(t, err, 401)
}

func TestLogout(t *testing.T) {
	e := newEnv(t)
	username, _ := e.newUser(RoleSatgas, false)
	res, _ := e.login(username, testPassword)

	if err := e.svc.Logout(context.Background(), res.Token, "203.0.113.9"); err != nil {
		t.Fatal(err)
	}
	_, err := e.svc.Authenticate(context.Background(), res.Token)
	wantStatus(t, err, 401)
	if e.auditCount(username, audit.ActionLogout) != 1 {
		t.Fatal("LOGOUT harus tercatat")
	}
	// Idempoten.
	if err := e.svc.Logout(context.Background(), res.Token, ""); err != nil {
		t.Fatalf("logout ulang: %v", err)
	}
	if err := e.svc.Logout(context.Background(), "cacat", ""); err != nil {
		t.Fatalf("logout token cacat: %v", err)
	}
}

func TestChangePassword(t *testing.T) {
	e := newEnv(t)
	username, _ := e.newUser(RoleSatgas, true)
	ctx := context.Background()

	s1, _ := e.login(username, testPassword)
	s2, _ := e.login(username, testPassword)
	p1, _ := e.svc.Authenticate(ctx, s1.Token)
	if !p1.MustChangePassword {
		t.Fatal("akun baru harus must_change_password")
	}

	// Kebijakan & validasi tidak menghitung sebagai percobaan gagal.
	for _, tt := range []struct {
		name, cur, nw, field string
	}{
		{"sama", testPassword, testPassword, "new_password"},
		{"lemah", testPassword, "pendek1", "new_password"},
		{"sama dengan username", testPassword, strings.ToUpper(username), "new_password"},
		{"current kosong", "", "Sandi-Baru-98765", "current_password"},
	} {
		err := e.svc.ChangePassword(ctx, p1, ChangePasswordInput{Current: tt.cur, New: tt.nw})
		if ae := appErr(t, err, 422); ae.Fields[tt.field] == "" {
			t.Fatalf("%s: fields = %v", tt.name, ae.Fields)
		}
	}

	// Password saat ini salah -> 422 dan dihitung sebagai kegagalan.
	err := e.svc.ChangePassword(ctx, p1, ChangePasswordInput{Current: "Salah-Sandi-1", New: "Sandi-Baru-98765"})
	if ae := appErr(t, err, 422); ae.Fields["current_password"] == "" {
		t.Fatal("pesan current_password kosong")
	}

	if err := e.svc.ChangePassword(ctx, p1, ChangePasswordInput{Current: testPassword, New: "Sandi-Baru-98765", IP: "203.0.113.9"}); err != nil {
		t.Fatalf("ChangePassword: %v", err)
	}
	if e.auditCount(username, audit.ActionPasswordChanged) != 1 {
		t.Fatal("PASSWORD_CHANGED harus tercatat")
	}
	// Sesi saat ini tetap hidup dan flag lepas; sesi lain dicabut.
	p1b, err := e.svc.Authenticate(ctx, s1.Token)
	if err != nil || p1b.MustChangePassword {
		t.Fatalf("sesi saat ini: %+v %v", p1b, err)
	}
	_, err = e.svc.Authenticate(ctx, s2.Token)
	wantStatus(t, err, 401)

	_, err = e.login(username, testPassword)
	wantStatus(t, err, 401)
	if _, err := e.login(username, "Sandi-Baru-98765"); err != nil {
		t.Fatalf("login dengan password baru: %v", err)
	}
}

func TestChangePassword_SalahBerulangMengunciDanMencabutSesi(t *testing.T) {
	e := newEnv(t)
	username, _ := e.newUser(RoleSatgas, false)
	ctx := context.Background()
	res, _ := e.login(username, testPassword)
	p, _ := e.svc.Authenticate(ctx, res.Token)

	var last error
	for i := 0; i < 3; i++ {
		last = e.svc.ChangePassword(ctx, p, ChangePasswordInput{Current: "Salah-Sandi-1", New: "Sandi-Baru-98765"})
	}
	wantStatus(t, last, 423)
	_, err := e.svc.Authenticate(ctx, res.Token)
	wantStatus(t, err, 401)
	_, err = e.login(username, testPassword)
	wantStatus(t, err, 423)
}

func TestProvisioning(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	n := fmt.Sprintf("test_auth_%d_%d", time.Now().UnixNano(), counter.Add(1))

	// Validasi.
	_, err := e.svc.CreateUser(ctx, CreateUserInput{Name: "", Username: "a", Email: "bukan-email", Role: "ADMIN", Password: "x"})
	ae := appErr(t, err, 422)
	for _, f := range []string{"name", "username", "email", "role", "password"} {
		if ae.Fields[f] == "" {
			t.Errorf("field %s seharusnya ditolak: %v", f, ae.Fields)
		}
	}

	u, err := e.svc.CreateUser(ctx, CreateUserInput{Name: " Satgas Uji ", Username: strings.ToUpper(n), Email: strings.ToUpper(n) + "@Test.Local",
		Role: RoleSatgas, Password: testPassword, MustChangePassword: true, Source: "test"})
	if err != nil {
		t.Fatal(err)
	}
	if u.Username != n || u.Email != n+"@test.local" || u.Name != "Satgas Uji" || !u.MustChangePassword || !u.IsActive {
		t.Fatalf("user = %+v", u)
	}
	if e.count(`SELECT count(*) FROM audit_logs WHERE action = $1 AND entity_id = $2 AND actor_type = 'SYSTEM'`, audit.ActionUserCreated, u.ID) != 1 {
		t.Fatal("USER_CREATED harus tercatat sebagai SYSTEM")
	}

	// Duplikat (tanpa membedakan huruf besar/kecil).
	_, err = e.svc.CreateUser(ctx, CreateUserInput{Name: "X", Username: n, Email: "lain_" + n + "@test.local", Role: RoleSatgas, Password: testPassword})
	if !errors.Is(err, ErrUserExists) {
		t.Fatalf("username ganda: %v", err)
	}
	_, err = e.svc.CreateUser(ctx, CreateUserInput{Name: "X", Username: "lain_" + n, Email: strings.ToUpper(n) + "@test.local", Role: RoleSatgas, Password: testPassword})
	if !errors.Is(err, ErrUserExists) {
		t.Fatalf("email ganda: %v", err)
	}

	// Daftar tidak memuat hash.
	users, err := e.svc.ListUsers(ctx)
	if err != nil {
		t.Fatal(err)
	}
	var found bool
	for _, x := range users {
		found = found || x.Username == n
	}
	if !found {
		t.Fatal("akun baru tidak muncul di ListUsers")
	}

	// Reset password: buka kunci, cabut sesi, wajib ganti lagi.
	if _, err := e.svc.CreateUser(ctx, CreateUserInput{Name: "Reset", Username: "r_" + n, Email: "r_" + n + "@test.local", Role: RolePejabat, Password: testPassword}); err != nil {
		t.Fatal(err)
	}
	_, _ = e.login("r_"+n, "Salah-Sandi-1")
	_, _ = e.login("r_"+n, "Salah-Sandi-1")
	_, err = e.login("r_"+n, "Salah-Sandi-1")
	wantStatus(t, err, 423)
	if err := e.svc.ResetPassword(ctx, "r_"+n, "Reset-Sandi-54321", "test"); err != nil {
		t.Fatal(err)
	}
	res, err := e.login("r_"+n, "Reset-Sandi-54321")
	if err != nil || !res.User.MustChangePassword {
		t.Fatalf("login setelah reset: %+v %v", res, err)
	}
	if err := e.svc.ResetPassword(ctx, "r_"+n, "lemah", "test"); err == nil {
		t.Fatal("password lemah harus ditolak")
	}
	if err := e.svc.ResetPassword(ctx, "tidak_ada_"+n, "Reset-Sandi-54321", "test"); !errors.Is(err, ErrUserNotFound) {
		t.Fatalf("akun tak ada: %v", err)
	}

	// Nonaktifkan: sesi dicabut, tidak bisa login, idempoten.
	changed, err := e.svc.DeactivateUser(ctx, "r_"+n, "test")
	if err != nil || !changed {
		t.Fatalf("DeactivateUser = %v %v", changed, err)
	}
	_, err = e.svc.Authenticate(ctx, res.Token)
	wantStatus(t, err, 401)
	_, err = e.login("r_"+n, "Reset-Sandi-54321")
	wantStatus(t, err, 401)
	if changed, err := e.svc.DeactivateUser(ctx, "r_"+n, "test"); err != nil || changed {
		t.Fatalf("nonaktif ulang = %v %v", changed, err)
	}
	if _, err := e.svc.DeactivateUser(ctx, "tidak_ada_"+n, "test"); !errors.Is(err, ErrUserNotFound) {
		t.Fatalf("akun tak ada: %v", err)
	}
}

func TestSesiAkunNonaktifTidakValidWalauBarisTersisa(t *testing.T) {
	e := newEnv(t)
	username, _ := e.newUser(RoleSatgas, false)
	res, _ := e.login(username, testPassword)
	if _, err := e.pool.Exec(context.Background(), `UPDATE users SET is_active = FALSE WHERE username = $1`, username); err != nil {
		t.Fatal(err)
	}
	_, err := e.svc.Authenticate(context.Background(), res.Token)
	wantStatus(t, err, 401)
}

func TestCleanupSessions(t *testing.T) {
	e := newEnv(t)
	username, _ := e.newUser(RoleSatgas, false)
	res, _ := e.login(username, testPassword)
	e.advance(31 * time.Minute)
	n, err := e.svc.CleanupSessions(context.Background())
	if err != nil || n < 1 {
		t.Fatalf("CleanupSessions = %d, %v", n, err)
	}
	if e.count(`SELECT count(*) FROM sessions WHERE id = $1`, hashToken(res.Token)) != 0 {
		t.Fatal("sesi idle harus dibersihkan")
	}
}

// ------------------------------------------------------------ lewat HTTP

func (e *env) server(limit func(http.Handler) http.Handler) *httptest.Server {
	mux := http.NewServeMux()
	NewHandler(e.svc).Register(mux, limit)
	// Route contoh modul lain yang memakai middleware otorisasi.
	mux.Handle("GET /api/_cases", e.svc.Protect(PermCaseList, okHandler()))
	mux.Handle("POST /api/_decision", e.svc.Protect(PermDecisionRecord, okHandler()))
	h := httpx.Chain(mux, httpx.RequestID, httpx.CSRF("https://app.test"))
	return httptest.NewServer(h)
}

func do(t *testing.T, srv *httptest.Server, method, path, body string, cookies ...*http.Cookie) *http.Response {
	t.Helper()
	req, _ := http.NewRequest(method, srv.URL+path, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Origin", "https://app.test")
	for _, c := range cookies {
		req.AddCookie(c)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = resp.Body.Close() })
	return resp
}

func sessionCookie(resp *http.Response) *http.Cookie {
	for _, c := range resp.Cookies() {
		if c.Name == SessionCookieName {
			return c
		}
	}
	return nil
}

func TestHTTP_AlurLengkap(t *testing.T) {
	e := newEnv(t, func(o *Options) { o.CookieSecure = true })
	srv := e.server(nil)
	defer srv.Close()
	satgas, _ := e.newUser(RoleSatgas, false)
	pejabat, _ := e.newUser(RolePejabat, false)
	fresh, _ := e.newUser(RoleSatgas, true)
	body := func(u, p string) string { return fmt.Sprintf(`{"identifier":%q,"password":%q}`, u, p) }

	// Tanpa sesi.
	if r := do(t, srv, "GET", "/api/auth/me", ""); r.StatusCode != 401 {
		t.Fatalf("me tanpa sesi: %d", r.StatusCode)
	}
	if r := do(t, srv, "GET", "/api/_cases", ""); r.StatusCode != 401 {
		t.Fatalf("route terlindungi tanpa sesi: %d", r.StatusCode)
	}

	// Login gagal tidak memasang cookie; login berhasil memasang cookie aman.
	r := do(t, srv, "POST", "/api/auth/login", body(satgas, "Salah-Sandi-1"))
	if r.StatusCode != 401 || sessionCookie(r) != nil {
		t.Fatalf("login gagal: %d cookie=%v", r.StatusCode, sessionCookie(r))
	}
	r = do(t, srv, "POST", "/api/auth/login", `{"identifier":"x","password":"y","extra":1}`)
	if r.StatusCode != 422 {
		t.Fatalf("field tak dikenal harus ditolak: %d", r.StatusCode)
	}
	r = do(t, srv, "POST", "/api/auth/login", body(satgas, testPassword))
	ck := sessionCookie(r)
	if r.StatusCode != 200 || ck == nil {
		t.Fatalf("login: %d", r.StatusCode)
	}
	if !ck.HttpOnly || !ck.Secure || ck.SameSite != http.SameSiteLaxMode || ck.Path != "/" {
		t.Fatalf("atribut cookie: %+v", ck)
	}
	if cc := r.Header.Get("Cache-Control"); !strings.Contains(cc, "no-store") && cc != "" {
		t.Logf("Cache-Control = %q", cc)
	}

	// Sesi dikenali; Satgas boleh daftar kasus, tidak boleh mencatat keputusan.
	if r := do(t, srv, "GET", "/api/auth/me", "", ck); r.StatusCode != 200 {
		t.Fatalf("me: %d", r.StatusCode)
	}
	if r := do(t, srv, "GET", "/api/_cases", "", ck); r.StatusCode != 204 {
		t.Fatalf("Satgas daftar kasus: %d", r.StatusCode)
	}
	before := e.auditCount(satgas, audit.ActionForbiddenAccess)
	if r := do(t, srv, "POST", "/api/_decision", "{}", ck); r.StatusCode != 403 {
		t.Fatalf("Satgas catat keputusan: %d", r.StatusCode)
	}
	if e.auditCount(satgas, audit.ActionForbiddenAccess) != before+1 {
		t.Fatal("FORBIDDEN_ACCESS harus tercatat")
	}

	// Pejabat boleh mencatat keputusan.
	rp := do(t, srv, "POST", "/api/auth/login", body(pejabat, testPassword))
	if r := do(t, srv, "POST", "/api/_decision", "{}", sessionCookie(rp)); r.StatusCode != 204 {
		t.Fatalf("Pejabat catat keputusan: %d", r.StatusCode)
	}

	// Akun must_change_password: hanya endpoint /api/auth yang terbuka.
	rf := do(t, srv, "POST", "/api/auth/login", body(fresh, testPassword))
	fc := sessionCookie(rf)
	if r := do(t, srv, "GET", "/api/_cases", "", fc); r.StatusCode != 403 {
		t.Fatalf("must_change_password: %d", r.StatusCode)
	}
	if r := do(t, srv, "GET", "/api/auth/me", "", fc); r.StatusCode != 200 {
		t.Fatalf("me saat must_change_password: %d", r.StatusCode)
	}
	if r := do(t, srv, "PUT", "/api/auth/password", `{"current_password":"`+testPassword+`","new_password":"Sandi-Baru-98765"}`, fc); r.StatusCode != 204 {
		t.Fatalf("ganti password: %d", r.StatusCode)
	}
	if r := do(t, srv, "GET", "/api/_cases", "", fc); r.StatusCode != 204 {
		t.Fatalf("setelah ganti password: %d", r.StatusCode)
	}

	// CSRF: Origin asing ditolak pada POST login.
	req, _ := http.NewRequest("POST", srv.URL+"/api/auth/login", strings.NewReader(body(satgas, testPassword)))
	req.Header.Set("Origin", "https://jahat.example")
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != 403 {
		t.Fatalf("Origin asing: %d", resp.StatusCode)
	}

	// Logout: cookie dibersihkan dan sesi tidak berlaku lagi.
	rl := do(t, srv, "POST", "/api/auth/logout", "", ck)
	if rl.StatusCode != 204 {
		t.Fatalf("logout: %d", rl.StatusCode)
	}
	if c := sessionCookie(rl); c == nil || c.MaxAge >= 0 {
		t.Fatalf("logout harus membersihkan cookie: %+v", c)
	}
	if r := do(t, srv, "GET", "/api/auth/me", "", ck); r.StatusCode != 401 {
		t.Fatalf("sesi setelah logout: %d", r.StatusCode)
	}
}

func TestHTTP_LoginDibatasiAuthLimiter(t *testing.T) {
	e := newEnv(t)
	lim := ratelimit.NewPerMinute(2, 2)
	srv := e.server(lim.Middleware)
	defer srv.Close()
	for i := 0; i < 2; i++ {
		if r := do(t, srv, "POST", "/api/auth/login", `{"identifier":"x","password":"y"}`); r.StatusCode != 401 {
			t.Fatalf("percobaan %d: %d", i, r.StatusCode)
		}
	}
	if r := do(t, srv, "POST", "/api/auth/login", `{"identifier":"x","password":"y"}`); r.StatusCode != 429 {
		t.Fatalf("seharusnya 429: %d", r.StatusCode)
	}
}

func TestRunCleanup_MenghapusSesiKedaluwarsaSampaiContextSelesai(t *testing.T) {
	e := newEnv(t)
	username, _ := e.newUser(RoleSatgas, false)
	res, _ := e.login(username, testPassword)
	e.advance(31 * time.Minute) // sesi kini idle-expired menurut jam service

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { e.svc.RunCleanup(ctx, 20*time.Millisecond); close(done) }()

	deadline := time.Now().Add(3 * time.Second)
	for e.count(`SELECT count(*) FROM sessions WHERE id = $1`, hashToken(res.Token)) != 0 {
		if time.Now().After(deadline) {
			t.Fatal("RunCleanup tidak menghapus sesi kedaluwarsa")
		}
		time.Sleep(20 * time.Millisecond)
	}
	cancel()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("RunCleanup tidak berhenti saat context dibatalkan")
	}
}

func TestHTTP_GantiPasswordTerkunciMembersihkanCookie(t *testing.T) {
	e := newEnv(t) // 3 percobaan
	srv := e.server(nil)
	defer srv.Close()
	username, _ := e.newUser(RoleSatgas, false)
	r := do(t, srv, "POST", "/api/auth/login", fmt.Sprintf(`{"identifier":%q,"password":%q}`, username, testPassword))
	ck := sessionCookie(r)
	if ck == nil {
		t.Fatalf("login: %d", r.StatusCode)
	}
	bad := `{"current_password":"Salah-Sandi-1","new_password":"Sandi-Baru-98765"}`
	for i := 0; i < 2; i++ {
		if r := do(t, srv, "PUT", "/api/auth/password", bad, ck); r.StatusCode != 422 {
			t.Fatalf("percobaan %d: %d, mau 422", i+1, r.StatusCode)
		}
	}
	r = do(t, srv, "PUT", "/api/auth/password", bad, ck)
	if r.StatusCode != 423 {
		t.Fatalf("percobaan terakhir: %d, mau 423", r.StatusCode)
	}
	if c := sessionCookie(r); c == nil || c.MaxAge >= 0 {
		t.Fatalf("cookie harus dibersihkan saat akun terkunci: %+v", c)
	}
	if r := do(t, srv, "GET", "/api/auth/me", "", ck); r.StatusCode != 401 {
		t.Fatalf("sesi harus dicabut: %d", r.StatusCode)
	}
}
