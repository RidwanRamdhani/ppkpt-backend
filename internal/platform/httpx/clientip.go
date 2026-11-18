package httpx

import (
	"context"
	"net"
	"net/http"
	"strings"
)

// ClientIPResolver menentukan IP klien. Header X-Forwarded-For hanya dipercaya
// bila RemoteAddr berasal dari proxy tepercaya (TRUSTED_PROXY_CIDRS); selain itu
// RemoteAddr yang dipakai. Hasilnya dipakai untuk rate limit dan audit.
type ClientIPResolver struct {
	trusted []*net.IPNet
}

// NewClientIPResolver membuat resolver dari daftar CIDR proxy tepercaya.
// CIDR yang tidak valid diabaikan agar X-Forwarded-For tidak dipercaya.
func NewClientIPResolver(cidrs []string) *ClientIPResolver {
	r := &ClientIPResolver{}
	for _, c := range cidrs {
		c = strings.TrimSpace(c)
		if c == "" {
			continue
		}
		if _, n, err := net.ParseCIDR(c); err == nil {
			r.trusted = append(r.trusted, n)
		}
	}
	return r
}

// IsTrusted melaporkan apakah remoteAddr berasal dari proxy tepercaya.
func (r *ClientIPResolver) IsTrusted(remoteAddr string) bool {
	host, _, err := net.SplitHostPort(remoteAddr)
	if err != nil {
		host = remoteAddr
	}
	ip := net.ParseIP(host)
	if ip == nil {
		return false
	}
	for _, n := range r.trusted {
		if n.Contains(ip) {
			return true
		}
	}
	return false
}

// Resolve mengembalikan IP klien untuk sebuah request.
func (r *ClientIPResolver) Resolve(req *http.Request) string {
	if r != nil && r.IsTrusted(req.RemoteAddr) {
		if xff := req.Header.Get("X-Forwarded-For"); xff != "" {
			// Ambil entri paling kiri (klien asli) dan validkan.
			first := strings.TrimSpace(strings.Split(xff, ",")[0])
			if ip := net.ParseIP(first); ip != nil {
				return ip.String()
			}
		}
	}
	host, _, err := net.SplitHostPort(req.RemoteAddr)
	if err != nil {
		return req.RemoteAddr
	}
	return host
}

// Helper paket yang memakai resolver default (tanpa proxy tepercaya).
// Gunakan ClientIPResolver bila TRUSTED_PROXY_CIDRS diisi.
func ClientIP(req *http.Request) string {
	return defaultResolver.Resolve(req)
}

var defaultResolver = NewClientIPResolver(nil)

// Kunci context untuk membawa ClientIPResolver.
type resolverCtxKey struct{}

// WithClientIPResolver menyimpan resolver ke context agar dapat dibaca handler.
func WithClientIPResolver(ctx context.Context, r *ClientIPResolver) context.Context {
	return context.WithValue(ctx, resolverCtxKey{}, r)
}

// ResolverFromContext mengambil resolver dari context, atau nil bila tidak ada.
func ResolverFromContext(ctx context.Context) *ClientIPResolver {
	if v, ok := ctx.Value(resolverCtxKey{}).(*ClientIPResolver); ok {
		return v
	}
	return nil
}
