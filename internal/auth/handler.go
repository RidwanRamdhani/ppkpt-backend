package auth

import (
	"net/http"

	"github.com/RidwanRamdhani/ppkpt-backend/internal/platform/httpx"
)

// Handler menyediakan endpoint HTTP autentikasi.
type Handler struct {
	svc *Service
}

// NewHandler membuat Handler.
func NewHandler(svc *Service) *Handler { return &Handler{svc: svc} }

// Register memasang route autentikasi ke mux.
//
//	POST /api/auth/login     publik (dibatasi limit)
//	POST /api/auth/logout    publik/idempoten: selalu membersihkan cookie
//	PUT  /api/auth/password  perlu sesi (dibatasi limit)
//	GET  /api/auth/me        perlu sesi
//
// limit adalah middleware rate limit per IP (AuthLimiter); boleh nil.
func (h *Handler) Register(mux *http.ServeMux, limit func(http.Handler) http.Handler) {
	if limit == nil {
		limit = func(next http.Handler) http.Handler { return next }
	}
	mux.Handle("POST /api/auth/login", limit(http.HandlerFunc(h.login)))
	mux.Handle("POST /api/auth/logout", http.HandlerFunc(h.logout))
	mux.Handle("PUT /api/auth/password", limit(h.svc.RequireSession(http.HandlerFunc(h.changePassword))))
	mux.Handle("GET /api/auth/me", h.svc.RequireSession(http.HandlerFunc(h.me)))
}

type loginRequest struct {
	Identifier string `json:"identifier"` // username atau email
	Password   string `json:"password"`
}

type userEnvelope struct {
	User User `json:"user"`
}

func (h *Handler) login(w http.ResponseWriter, r *http.Request) {
	var req loginRequest
	if err := httpx.ReadJSON(w, r, &req); err != nil {
		httpx.WriteError(w, err)
		return
	}
	res, err := h.svc.Login(r.Context(), LoginInput{
		Identifier: req.Identifier,
		Password:   req.Password,
		IP:         clientIP(r),
		UserAgent:  r.UserAgent(),
	})
	if err != nil {
		h.writeErr(w, r, err)
		return
	}
	http.SetCookie(w, h.svc.sessionCookie(res.Token))
	res.User.IsActive = true
	httpx.WriteJSON(w, http.StatusOK, userEnvelope{User: res.User})
}

func (h *Handler) logout(w http.ResponseWriter, r *http.Request) {
	if c, err := r.Cookie(SessionCookieName); err == nil {
		if err := h.svc.Logout(r.Context(), c.Value, clientIP(r)); err != nil {
			// Gagal mencatat logout tidak boleh menahan pengguna di sesi lama:
			// cookie tetap dibersihkan, tetapi kegagalannya dilaporkan.
			h.svc.logger.Error("logout gagal", "request_id", httpx.IDFromContext(r.Context()), "err", err)
		}
	}
	http.SetCookie(w, h.svc.clearCookie())
	httpx.WriteJSON(w, http.StatusNoContent, nil)
}

type changePasswordRequest struct {
	CurrentPassword string `json:"current_password"`
	NewPassword     string `json:"new_password"`
}

func (h *Handler) changePassword(w http.ResponseWriter, r *http.Request) {
	p, _ := PrincipalFrom(r.Context())
	var req changePasswordRequest
	if err := httpx.ReadJSON(w, r, &req); err != nil {
		httpx.WriteError(w, err)
		return
	}
	if err := h.svc.ChangePassword(r.Context(), p, ChangePasswordInput{
		Current: req.CurrentPassword,
		New:     req.NewPassword,
		IP:      clientIP(r),
	}); err != nil {
		// Sesi dicabut bila akun terkunci karena salah password saat ini.
		if ae := httpx.FromError(err); ae.Status == http.StatusLocked {
			http.SetCookie(w, h.svc.clearCookie())
		}
		h.writeErr(w, r, err)
		return
	}
	httpx.WriteJSON(w, http.StatusNoContent, nil)
}

func (h *Handler) me(w http.ResponseWriter, r *http.Request) {
	p, _ := PrincipalFrom(r.Context())
	httpx.WriteJSON(w, http.StatusOK, userEnvelope{User: User{
		ID: p.UserID, Name: p.Name, Username: p.Username, Email: p.Email,
		Role: p.Role, IsActive: true, MustChangePassword: p.MustChangePassword,
	}})
}

// writeErr menulis error; galat tak terduga dicatat di log sebelum dijadikan 500 generik.
func (h *Handler) writeErr(w http.ResponseWriter, r *http.Request, err error) {
	if ae := httpx.FromError(err); ae.Status == http.StatusInternalServerError {
		h.svc.logger.Error("galat autentikasi", "request_id", httpx.IDFromContext(r.Context()), "err", err)
	}
	httpx.WriteError(w, err)
}
