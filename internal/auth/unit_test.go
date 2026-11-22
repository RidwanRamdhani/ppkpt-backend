package auth

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/RidwanRamdhani/ppkpt-backend/internal/config"
	"github.com/RidwanRamdhani/ppkpt-backend/internal/platform/httpx"
	"golang.org/x/crypto/bcrypt"
)

func newUnitService(t *testing.T) *Service {
	t.Helper()
	s, err := NewService(nil, nil, nil, Options{BcryptCost: bcrypt.MinCost})
	if err != nil {
		t.Fatalf("NewService: %v", err)
	}
	return s
}

func TestValidatePassword(t *testing.T) {
	tests := []struct {
		name string
		pw   string
		forb []string
		ok   bool
	}{
		{"valid", "Sandi-Uji-12345", nil, true},
		{"tepat 8 karakter", "Sandi123", nil, true},
		{"kurang dari 8", "Sandi12", nil, false},
		{"terlalu pendek", "Ab1", nil, false},
		{"tanpa angka", "HurufSajaPanjang", nil, false},
		{"tanpa huruf", "1234567890123", nil, false},
		{"lebih dari 72 byte", strings.Repeat("a1", 40), nil, false},
		{"sama dengan username", "petugas12345", []string{"Petugas12345"}, false},
		{"tepat 72 byte", strings.Repeat("a", 71) + "1", nil, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := ValidatePassword(tt.pw, tt.forb...)
			if (err == nil) != tt.ok {
				t.Fatalf("ValidatePassword(%q) = %v, ok=%v", tt.pw, err, tt.ok)
			}
			if err != nil && err.Fields["password"] == "" {
				t.Fatal("pesan field password kosong")
			}
		})
	}
}

func TestGeneratePassword_LolosKebijakanDanUnik(t *testing.T) {
	seen := map[string]bool{}
	for i := 0; i < 50; i++ {
		p, err := GeneratePassword()
		if err != nil {
			t.Fatalf("GeneratePassword: %v", err)
		}
		if ValidatePassword(p) != nil {
			t.Fatalf("password tidak lolos kebijakan: %q", p)
		}
		if strings.ContainsAny(p, "0O1lI") {
			t.Fatalf("password memuat karakter ambigu: %q", p)
		}
		if seen[p] {
			t.Fatalf("password berulang: %q", p)
		}
		seen[p] = true
	}
}

func TestHasher(t *testing.T) {
	s := newUnitService(t)
	h, err := s.hasher.Hash("Sandi-Uji-12345")
	if err != nil {
		t.Fatal(err)
	}
	if h == "Sandi-Uji-12345" {
		t.Fatal("hash sama dengan password")
	}
	if !s.hasher.Verify(h, "Sandi-Uji-12345") {
		t.Fatal("password benar ditolak")
	}
	if s.hasher.Verify(h, "salah") {
		t.Fatal("password salah diterima")
	}
	if s.hasher.Verify(h, strings.Repeat("x", 80)) {
		t.Fatal("password > 72 byte diterima")
	}
	if s.hasher.Verify("bukan-hash", "apa saja") {
		t.Fatal("hash rusak diterima")
	}
	hi, _ := newHasher(bcrypt.MinCost + 1)
	if !hi.NeedsRehash(h) {
		t.Fatal("hash ber-cost rendah seharusnya perlu rehash")
	}
	if s.hasher.NeedsRehash(h) {
		t.Fatal("hash ber-cost sama tidak perlu rehash")
	}
	if _, err := newHasher(99); err == nil {
		t.Fatal("cost tidak valid seharusnya ditolak")
	}
}

func TestToken(t *testing.T) {
	tok, id, err := newToken()
	if err != nil {
		t.Fatal(err)
	}
	if !validTokenFormat(tok) {
		t.Fatalf("token baru tidak lolos format: %q", tok)
	}
	if len(id) != 64 {
		t.Fatalf("panjang id sesi = %d, mau 64 (CHAR(64))", len(id))
	}
	if id == tok || id != hashToken(tok) {
		t.Fatal("id sesi harus hash dari token, bukan token itu sendiri")
	}
	tok2, _, _ := newToken()
	if tok == tok2 {
		t.Fatal("token tidak acak")
	}
	for _, bad := range []string{"", "abc", tok + "x", strings.Repeat("!", len(tok))} {
		if validTokenFormat(bad) {
			t.Fatalf("token cacat diterima: %q", bad)
		}
	}
}

func TestCookieAttributes(t *testing.T) {
	s, _ := NewService(nil, nil, nil, Options{BcryptCost: bcrypt.MinCost, CookieSecure: true})
	c := s.sessionCookie("abc")
	if !c.HttpOnly || !c.Secure || c.SameSite != http.SameSiteLaxMode || c.Path != "/" || c.Name != SessionCookieName {
		t.Fatalf("atribut cookie sesi salah: %+v", c)
	}
	if c.MaxAge != 0 || !c.Expires.IsZero() {
		t.Fatal("cookie sesi seharusnya cookie browser-session")
	}
	cc := s.clearCookie()
	if cc.MaxAge >= 0 || cc.Value != "" || !cc.HttpOnly {
		t.Fatalf("cookie penghapus salah: %+v", cc)
	}
	dev, _ := NewService(nil, nil, nil, Options{BcryptCost: bcrypt.MinCost, CookieSecure: false})
	if dev.sessionCookie("x").Secure {
		t.Fatal("COOKIE_SECURE=false harus menghasilkan cookie tanpa Secure")
	}
}

func TestOptionsDefaults(t *testing.T) {
	o := Options{}.withDefaults()
	if o.BcryptCost != 12 || o.LoginMaxAttempts != 5 || o.LoginLockDuration != 15*time.Minute ||
		o.SessionIdle != 30*time.Minute || o.SessionAbsolute != 8*time.Hour {
		t.Fatalf("default tidak sesuai: %+v", o)
	}
}

// Matriks hak akses harus sama dengan Lampiran 9.2 SDD.
func TestPermissionMatrix(t *testing.T) {
	satgasOnly := []Permission{PermManualReportCreate, PermCaseTake, PermCaseVerify, PermCaseReview, PermCaseExamine,
		PermCaseCondition, PermCaseReferral, PermCaseRecovery, PermConclusionWrite, PermConclusionSubmit,
		PermDeliveryRecord, PermCaseClose}
	pejabatOnly := []Permission{PermDecisionView, PermDecisionRecord, PermDecisionHistory}
	both := []Permission{PermCaseList, PermCaseView, PermEvidenceDownload, PermDashboardView}

	for _, p := range satgasOnly {
		if !Allowed(RoleSatgas, p) || Allowed(RolePejabat, p) {
			t.Errorf("%s harus khusus Satgas", p)
		}
	}
	for _, p := range pejabatOnly {
		if Allowed(RoleSatgas, p) || !Allowed(RolePejabat, p) {
			t.Errorf("%s harus khusus Pejabat", p)
		}
	}
	for _, p := range both {
		if !Allowed(RoleSatgas, p) || !Allowed(RolePejabat, p) {
			t.Errorf("%s harus untuk Satgas dan Pejabat", p)
		}
	}
	if Allowed(RoleSatgas, Permission("tidak.ada")) || Allowed("PELAPOR", PermCaseList) || Allowed("", PermCaseList) {
		t.Error("default deny dilanggar")
	}
}

func okHandler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusNoContent) })
}

func serve(h http.Handler, r *http.Request) *httptest.ResponseRecorder {
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, r)
	return rec
}

func TestRequireSession_TanpaDanDenganCookieCacat(t *testing.T) {
	s := newUnitService(t)
	h := s.RequireSession(okHandler())

	rec := serve(h, httptest.NewRequest(http.MethodGet, "/x", nil))
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("tanpa cookie: status %d, mau 401", rec.Code)
	}

	req := httptest.NewRequest(http.MethodGet, "/x", nil)
	req.AddCookie(&http.Cookie{Name: SessionCookieName, Value: "cacat"})
	rec = serve(h, req)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("cookie cacat: status %d, mau 401", rec.Code)
	}
	var cleared bool
	for _, c := range rec.Result().Cookies() {
		if c.Name == SessionCookieName && c.MaxAge < 0 {
			cleared = true
		}
	}
	if !cleared {
		t.Fatal("cookie cacat seharusnya dibersihkan")
	}
}

func TestRequireRoleDanPermission(t *testing.T) {
	s := newUnitService(t)
	satgas := &Principal{UserID: 1, Role: RoleSatgas}
	pejabat := &Principal{UserID: 2, Role: RolePejabat}

	withP := func(h http.Handler, p *Principal) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodPost, "/x", nil)
		return serve(h, req.WithContext(WithPrincipal(req.Context(), p)))
	}

	role := s.RequireRole(RoleSatgas)(okHandler())
	if rec := withP(role, satgas); rec.Code != http.StatusNoContent {
		t.Fatalf("Satgas ke route Satgas: %d", rec.Code)
	}
	rec := withP(role, pejabat)
	if rec.Code != http.StatusForbidden || !strings.Contains(rec.Body.String(), httpx.CodeForbidden) {
		t.Fatalf("Pejabat ke route Satgas: %d %s", rec.Code, rec.Body.String())
	}

	perm := s.RequirePermission(PermDecisionRecord)(okHandler())
	if rec := withP(perm, pejabat); rec.Code != http.StatusNoContent {
		t.Fatalf("Pejabat catat keputusan: %d", rec.Code)
	}
	if rec := withP(perm, satgas); rec.Code != http.StatusForbidden {
		t.Fatalf("Satgas catat keputusan harus 403: %d", rec.Code)
	}

	// Tanpa principal (RequireAuth lupa dipasang) -> 401, bukan lolos.
	if rec := serve(perm, httptest.NewRequest(http.MethodGet, "/x", nil)); rec.Code != http.StatusUnauthorized {
		t.Fatalf("tanpa principal: %d, mau 401", rec.Code)
	}
}

func TestOptionsFromConfig(t *testing.T) {
	o := OptionsFromConfig(config.Config{
		BcryptCost: 11, LoginMaxAttempts: 4, LoginLockMinutes: 20,
		SessionIdleMinutes: 15, SessionAbsoluteHours: 6, CookieSecure: true,
	})
	if o.BcryptCost != 11 || o.LoginMaxAttempts != 4 || o.LoginLockDuration != 20*time.Minute ||
		o.SessionIdle != 15*time.Minute || o.SessionAbsolute != 6*time.Hour || !o.CookieSecure {
		t.Fatalf("OptionsFromConfig salah: %+v", o)
	}
}
