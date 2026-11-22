package auth

import (
	"context"
	"errors"
	"net/http"

	"github.com/RidwanRamdhani/ppkpt-backend/internal/audit"
	"github.com/RidwanRamdhani/ppkpt-backend/internal/platform/httpx"
)

type ctxKey struct{}

// WithPrincipal menyimpan Principal ke context. Dipakai middleware dan pengujian.
func WithPrincipal(ctx context.Context, p *Principal) context.Context {
	return context.WithValue(ctx, ctxKey{}, p)
}

// PrincipalFrom mengambil Principal dari context. Handler di belakang
// RequireAuth/RequireSession selalu mendapatkan nilai non-nil.
func PrincipalFrom(ctx context.Context) (*Principal, bool) {
	p, ok := ctx.Value(ctxKey{}).(*Principal)
	return p, ok && p != nil
}

// RequireSession memvalidasi cookie sesi lalu menaruh Principal di context.
// Tanpa sesi yang valid, responsnya 401 dan cookie dibersihkan. Middleware ini
// TIDAK memaksa penggantian password; pakai RequireAuth untuk endpoint fitur.
func (s *Service) RequireSession(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		c, err := r.Cookie(SessionCookieName)
		if err != nil || c.Value == "" {
			httpx.WriteError(w, httpx.ErrUnauthorized())
			return
		}
		p, err := s.Authenticate(r.Context(), c.Value)
		if err != nil {
			var ae *httpx.AppError
			if errors.As(err, &ae) && ae.Status == http.StatusUnauthorized {
				http.SetCookie(w, s.clearCookie())
			} else {
				s.logger.Error("autentikasi sesi gagal", "request_id", httpx.IDFromContext(r.Context()), "err", err)
			}
			httpx.WriteError(w, err)
			return
		}
		next.ServeHTTP(w, r.WithContext(WithPrincipal(r.Context(), p)))
	})
}

// RequireAuth adalah RequireSession ditambah kewajiban mengganti password:
// akun yang masih must_change_password ditolak (403 PASSWORD_CHANGE_REQUIRED)
// pada semua endpoint selain endpoint /api/auth.
func (s *Service) RequireAuth(next http.Handler) http.Handler {
	return s.RequireSession(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if p, _ := PrincipalFrom(r.Context()); p != nil && p.MustChangePassword {
			httpx.WriteError(w, httpx.NewAppError(http.StatusForbidden, CodePasswordChangeRequired,
				"Anda harus mengganti password terlebih dahulu."))
			return
		}
		next.ServeHTTP(w, r)
	}))
}

// RequireRole hanya meneruskan request bila role pengguna termasuk roles.
// Harus dipasang di dalam RequireAuth/RequireSession. Penolakan dicatat di
// audit log (FORBIDDEN_ACCESS).
func (s *Service) RequireRole(roles ...string) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			p, ok := PrincipalFrom(r.Context())
			if !ok {
				httpx.WriteError(w, httpx.ErrUnauthorized())
				return
			}
			for _, role := range roles {
				if p.Role == role {
					next.ServeHTTP(w, r)
					return
				}
			}
			s.denyForbidden(w, r, p, map[string]any{"required_roles": roles})
		})
	}
}

// RequirePermission hanya meneruskan request bila role pengguna memiliki
// permission menurut matriks hak akses.
func (s *Service) RequirePermission(perm Permission) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			p, ok := PrincipalFrom(r.Context())
			if !ok {
				httpx.WriteError(w, httpx.ErrUnauthorized())
				return
			}
			if !Allowed(p.Role, perm) {
				s.denyForbidden(w, r, p, map[string]any{"permission": string(perm)})
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}

// Protect adalah jalan pintas untuk route fitur: sesi valid, password sudah
// diganti, dan role memiliki permission.
//
//	mux.Handle("POST /api/cases/{id}/take", svc.Protect(auth.PermCaseTake, h))
func (s *Service) Protect(perm Permission, h http.Handler) http.Handler {
	return s.RequireAuth(s.RequirePermission(perm)(h))
}

func (s *Service) denyForbidden(w http.ResponseWriter, r *http.Request, p *Principal, extra map[string]any) {
	if s.pool != nil && s.audit != nil {
		detail := map[string]any{"method": r.Method, "path": r.URL.Path}
		for k, v := range extra {
			detail[k] = v
		}
		if err := s.audit.Log(r.Context(), s.pool, audit.Event{
			ActorType: audit.ActorUser, UserID: userIDPtr(p.UserID), Action: audit.ActionForbiddenAccess,
			Entity: "endpoint", IP: clientIP(r), Detail: detail,
		}); err != nil {
			s.logger.Error("audit akses ditolak", "err", err)
		}
	}
	httpx.WriteError(w, httpx.ErrForbidden())
}

// clientIP memakai resolver IP tepercaya dari context bila tersedia.
func clientIP(r *http.Request) string {
	if res := httpx.ResolverFromContext(r.Context()); res != nil {
		if ip := res.Resolve(r); ip != "" {
			return ip
		}
	}
	return httpx.ClientIP(r)
}
