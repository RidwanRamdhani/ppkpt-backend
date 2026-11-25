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
//	POST /api/auth/login          publik (dibatasi limit) -> challenge 2FA
//	POST /api/auth/2fa/setup      butuh cookie ppkpt_mfa ENROLL (dibatasi limit)
//	POST /api/auth/2fa/enable     butuh cookie ppkpt_mfa ENROLL (dibatasi limit)
//	POST /api/auth/2fa/verify     butuh cookie ppkpt_mfa LOGIN (dibatasi limit)
//	POST /api/auth/2fa/disable    perlu sesi + password + kode (dibatasi limit)
//	POST /api/auth/2fa/recovery   perlu sesi + password + kode (dibatasi limit)
//	POST /api/auth/logout         publik/idempoten: selalu membersihkan cookie
//	PUT  /api/auth/password       perlu sesi (dibatasi limit)
//	GET  /api/auth/me             perlu sesi
//
// limit adalah middleware rate limit per IP (AuthLimiter); boleh nil.
func (h *Handler) Register(mux *http.ServeMux, limit func(http.Handler) http.Handler) {
	if limit == nil {
		limit = func(next http.Handler) http.Handler { return next }
	}
	mux.Handle("POST /api/auth/login", limit(http.HandlerFunc(h.login)))
	mux.Handle("POST /api/auth/2fa/setup", limit(http.HandlerFunc(h.mfaSetup)))
	mux.Handle("POST /api/auth/2fa/enable", limit(http.HandlerFunc(h.mfaEnable)))
	mux.Handle("POST /api/auth/2fa/verify", limit(http.HandlerFunc(h.mfaVerify)))
	mux.Handle("POST /api/auth/2fa/disable", limit(h.svc.RequireAuth(http.HandlerFunc(h.mfaDisable))))
	mux.Handle("POST /api/auth/2fa/recovery", limit(h.svc.RequireAuth(http.HandlerFunc(h.mfaRecovery))))
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

type mfaRequiredResponse struct {
	MFARequired        bool `json:"mfa_required"`
	EnrollmentRequired bool `json:"enrollment_required"`
}

type mfaSetupResponse struct {
	OTPAuthURL string `json:"otpauth_url"`
	Secret     string `json:"secret"`
}

type mfaEnabledResponse struct {
	User          User     `json:"user"`
	RecoveryCodes []string `json:"recovery_codes"`
}

type meResponse struct {
	User       User `json:"user"`
	MFAEnabled bool `json:"mfa_enabled"`
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
	if res.MFARequired {
		http.SetCookie(w, h.svc.mfaCookie(res.MFAToken, h.svc.opts.MFAChallengeTTL))
		httpx.WriteJSON(w, http.StatusOK, mfaRequiredResponse{
			MFARequired: true, EnrollmentRequired: res.EnrollmentRequired,
		})
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
	// Challenge 2FA yang belum selesai juga dibatalkan (mis. logout dari
	// halaman verifikasi sebelum sesi dibuat).
	if c, err := r.Cookie(MFACookieName); err == nil {
		if err := h.svc.DiscardMFAChallenge(r.Context(), c.Value); err != nil {
			h.svc.logger.Error("hapus challenge 2FA gagal", "request_id", httpx.IDFromContext(r.Context()), "err", err)
		}
	}
	http.SetCookie(w, h.svc.clearCookie())
	http.SetCookie(w, h.svc.clearMFACookie())
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
	httpx.WriteJSON(w, http.StatusOK, meResponse{
		User: User{
			ID: p.UserID, Name: p.Name, Username: p.Username, Email: p.Email,
			Role: p.Role, IsActive: true, MustChangePassword: p.MustChangePassword,
			TotpEnabled: p.TotpEnabled,
		},
		MFAEnabled: h.svc.opts.MFAEnabled,
	})
}

// ------------------------------------------------------------------- 2FA

func mfaTokenFrom(r *http.Request) string {
	c, err := r.Cookie(MFACookieName)
	if err != nil {
		return ""
	}
	return c.Value
}

func (h *Handler) mfaSetup(w http.ResponseWriter, r *http.Request) {
	token := mfaTokenFrom(r)
	if token == "" {
		httpx.WriteError(w, httpx.ErrUnauthorized())
		return
	}
	res, err := h.svc.MFASetup(r.Context(), token)
	if err != nil {
		h.writeErr(w, r, err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, mfaSetupResponse{OTPAuthURL: res.OTPAuthURL, Secret: res.Secret})
}

type mfaCodeRequest struct {
	Code string `json:"code"`
}

func (h *Handler) mfaEnable(w http.ResponseWriter, r *http.Request) {
	token := mfaTokenFrom(r)
	if token == "" {
		httpx.WriteError(w, httpx.ErrUnauthorized())
		return
	}
	var req mfaCodeRequest
	if err := httpx.ReadJSON(w, r, &req); err != nil {
		httpx.WriteError(w, err)
		return
	}
	res, err := h.svc.MFAEnable(r.Context(), MFAEnableInput{
		ChallengeToken: token, Code: req.Code, IP: clientIP(r), UserAgent: r.UserAgent(),
	})
	if err != nil {
		h.writeErr(w, r, err)
		return
	}
	h.completeMFA(w, res)
	httpx.WriteJSON(w, http.StatusOK, mfaEnabledResponse{User: res.User, RecoveryCodes: res.RecoveryCodes})
}

type mfaVerifyRequest struct {
	Code         string `json:"code"`
	RecoveryCode string `json:"recovery_code"`
}

func (h *Handler) mfaVerify(w http.ResponseWriter, r *http.Request) {
	token := mfaTokenFrom(r)
	if token == "" {
		httpx.WriteError(w, httpx.ErrUnauthorized())
		return
	}
	var req mfaVerifyRequest
	if err := httpx.ReadJSON(w, r, &req); err != nil {
		httpx.WriteError(w, err)
		return
	}
	res, err := h.svc.MFAVerify(r.Context(), MFAVerifyInput{
		ChallengeToken: token, Code: req.Code, RecoveryCode: req.RecoveryCode,
		IP: clientIP(r), UserAgent: r.UserAgent(),
	})
	if err != nil {
		h.writeErr(w, r, err)
		return
	}
	h.completeMFA(w, res)
	httpx.WriteJSON(w, http.StatusOK, userEnvelope{User: res.User})
}

// completeMFA memasang cookie sesi penuh dan menghapus cookie challenge.
func (h *Handler) completeMFA(w http.ResponseWriter, res *MFAVerifyResult) {
	res.User.IsActive = true
	http.SetCookie(w, h.svc.sessionCookie(res.Token))
	http.SetCookie(w, h.svc.clearMFACookie())
}

type mfaCredentialRequest struct {
	Password string `json:"password"`
	Code     string `json:"code"`
}

func (h *Handler) mfaDisable(w http.ResponseWriter, r *http.Request) {
	p, _ := PrincipalFrom(r.Context())
	var req mfaCredentialRequest
	if err := httpx.ReadJSON(w, r, &req); err != nil {
		httpx.WriteError(w, err)
		return
	}
	if err := h.svc.MFADisable(r.Context(), p, MFACredentialInput{
		Password: req.Password, Code: req.Code, IP: clientIP(r),
	}); err != nil {
		h.writeErr(w, r, err)
		return
	}
	httpx.WriteJSON(w, http.StatusNoContent, nil)
}

func (h *Handler) mfaRecovery(w http.ResponseWriter, r *http.Request) {
	p, _ := PrincipalFrom(r.Context())
	var req mfaCredentialRequest
	if err := httpx.ReadJSON(w, r, &req); err != nil {
		httpx.WriteError(w, err)
		return
	}
	codes, err := h.svc.MFARegenerateRecoveryCodes(r.Context(), p, MFACredentialInput{
		Password: req.Password, Code: req.Code, IP: clientIP(r),
	})
	if err != nil {
		h.writeErr(w, r, err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]any{"recovery_codes": codes})
}

// writeErr menulis error; galat tak terduga dicatat di log sebelum dijadikan 500 generik.
func (h *Handler) writeErr(w http.ResponseWriter, r *http.Request, err error) {
	if ae := httpx.FromError(err); ae.Status == http.StatusInternalServerError {
		h.svc.logger.Error("galat autentikasi", "request_id", httpx.IDFromContext(r.Context()), "err", err)
	}
	httpx.WriteError(w, err)
}
