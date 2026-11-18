package httpx

import (
	"net/http"
	"net/url"
)

// CSRF menolak request yang mengubah data (POST/PUT/PATCH/DELETE) bila header
// Origin ada dan tidak sama dengan origin aplikasi. Dikombinasikan dengan cookie
// SameSite=Lax (Phase 5), ini cukup untuk aplikasi same-origin (T7).
//
// Origin kosong dianggap aman (mis. klien non-browser/CLI tanpa header Origin).
func CSRF(appOrigin string) func(http.Handler) http.Handler {
	origin, err := url.Parse(appOrigin)
	if err != nil {
		origin = nil
	}
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			switch r.Method {
			case http.MethodPost, http.MethodPut, http.MethodPatch, http.MethodDelete:
				if o := r.Header.Get("Origin"); o != "" {
					if origin == nil {
						WriteError(w, ErrForbidden())
						return
					}
					u, err := url.Parse(o)
					if err != nil || !sameOrigin(origin, u) {
						WriteError(w, ErrForbidden())
						return
					}
				}
			}
			next.ServeHTTP(w, r)
		})
	}
}

func sameOrigin(a, b *url.URL) bool {
	return a.Scheme == b.Scheme && a.Host == b.Host
}
