package api

import (
	"encoding/json"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/p1ngu/rmm/internal/auth"
	"github.com/p1ngu/rmm/internal/models"
	"github.com/p1ngu/rmm/internal/settings"
)

const sessionTTL = 7 * 24 * time.Hour

// bootstrapAdmin creates the very first admin user, only when no users exist.
func (s *Server) bootstrapAdmin(w http.ResponseWriter, r *http.Request) {
	n, err := s.store.CountUsers(r.Context())
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	if n > 0 {
		writeErr(w, http.StatusForbidden, "users already exist; use an admin to create more")
		return
	}
	var in struct {
		Username string `json:"username"`
		Password string `json:"password"`
	}
	if err := decode(r, &in); err != nil || in.Username == "" || in.Password == "" {
		writeErr(w, http.StatusBadRequest, "username and password required")
		return
	}
	hash, err := auth.HashPassword(in.Password)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	u, err := s.store.CreateUser(r.Context(), in.Username, hash, models.RoleAdmin)
	if err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	// Issue a session immediately so the caller is logged in (matches login shape).
	token := auth.NewToken()
	if err := s.store.CreateSession(r.Context(), u.ID, auth.Hash(token), time.Now().Add(sessionTTL)); err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusCreated, map[string]any{"token": token, "role": u.Role, "user": u})
}

// login verifies credentials and returns a session bearer token.
func (s *Server) login(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Username string `json:"username"`
		Password string `json:"password"`
		Code     string `json:"code"` // TOTP, required when the user has 2FA enabled
	}
	if err := decode(r, &in); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid body")
		return
	}
	id, role, hash, err := s.store.GetUserAuth(r.Context(), in.Username)
	if err != nil {
		// Run a dummy bcrypt compare so response time doesn't reveal whether the
		// username exists (mitigates user enumeration).
		auth.CheckPassword("$2a$10$invalidinvalidinvalidinvalidinvalidinvalidinvalidinvalidin", in.Password)
		writeErr(w, http.StatusUnauthorized, "invalid credentials")
		return
	}
	if !auth.CheckPassword(hash, in.Password) {
		writeErr(w, http.StatusUnauthorized, "invalid credentials")
		return
	}
	// Second factor: if enabled, a valid TOTP code — or a one-time recovery
	// code — is required.
	if encSecret, enabled, _ := s.store.GetUserTOTP(r.Context(), id); enabled {
		if in.Code == "" {
			writeJSON(w, http.StatusUnauthorized, map[string]any{"error": "two-factor code required", "totp_required": true})
			return
		}
		secret, derr := settings.Decrypt(s.cfg, encSecret)
		ok := derr == nil && auth.ValidateTOTP(secret, in.Code)
		if !ok {
			// Fall back to a recovery code (consumed on use).
			used, uerr := s.store.UseRecoveryCode(r.Context(), id, auth.Hash(auth.NormalizeRecoveryCode(in.Code)))
			if uerr == nil && used {
				ok = true
				_ = s.store.AddAudit(r.Context(), id, "user.2fa.recovery.used", id, nil)
			}
		}
		if !ok {
			writeJSON(w, http.StatusUnauthorized, map[string]any{"error": "invalid two-factor code", "totp_required": true})
			return
		}
	}
	token := auth.NewToken()
	if err := s.store.CreateSession(r.Context(), id, auth.Hash(token), time.Now().Add(sessionTTL)); err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	user, _ := s.store.GetUserByID(r.Context(), id)
	if user == nil {
		user = &models.User{ID: id, Username: in.Username, Role: role}
	}
	writeJSON(w, http.StatusOK, map[string]any{"token": token, "role": role, "user": user})
}

// logout revokes the caller's session token.
func (s *Server) logout(w http.ResponseWriter, r *http.Request) {
	_ = s.store.DeleteSession(r.Context(), auth.Hash(bearer(r)))
	writeJSON(w, http.StatusOK, map[string]string{"status": "logged out"})
}

// me returns the current principal as a User (resolves real users; synthesizes
// one for the bootstrap admin token / API tokens).
func (s *Server) me(w http.ResponseWriter, r *http.Request) {
	actor, role := actorOf(r), roleOf(r)
	if u, err := s.store.GetUserByID(r.Context(), actor); err == nil {
		writeJSON(w, http.StatusOK, u)
		return
	}
	name := actor
	if actor == "admin" {
		name = "admin"
	} else if actor == "api" {
		name = "api-token"
	}
	writeJSON(w, http.StatusOK, models.User{ID: actor, Username: name, Role: role})
}

// changePassword lets a signed-in user change their own password after proving
// the current one. Only real users (session principals) have a password to
// change — the bootstrap admin token and API tokens cannot use this.
func (s *Server) changePassword(w http.ResponseWriter, r *http.Request) {
	var in struct {
		CurrentPassword string `json:"current_password"`
		NewPassword     string `json:"new_password"`
	}
	if err := decode(r, &in); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid body")
		return
	}
	if len(in.NewPassword) < 8 {
		writeErr(w, http.StatusBadRequest, "new password must be at least 8 characters")
		return
	}
	userID := actorOf(r)
	hash, err := s.store.GetUserPasswordHash(r.Context(), userID)
	if err != nil {
		// Synthetic principals (admin bootstrap token, API tokens) aren't users.
		writeErr(w, http.StatusForbidden, "this login has no password to change")
		return
	}
	if !auth.CheckPassword(hash, in.CurrentPassword) {
		writeErr(w, http.StatusUnauthorized, "current password is incorrect")
		return
	}
	newHash, err := auth.HashPassword(in.NewPassword)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	if err := s.store.UpdateUserPassword(r.Context(), userID, newHash); err != nil {
		writeErr(w, httpStatusFor(err), err.Error())
		return
	}
	// Drop other sessions so a changed/compromised password can't be reused
	// elsewhere; the caller keeps this session (its token is still valid).
	_ = s.store.DeleteUserSessionsExcept(r.Context(), userID, auth.Hash(bearer(r)))
	_ = s.store.AddAudit(r.Context(), userID, "user.password.change", userID, nil)
	writeJSON(w, http.StatusOK, map[string]string{"status": "password changed"})
}

func (s *Server) listUsers(w http.ResponseWriter, r *http.Request) {
	users, err := s.store.ListUsers(r.Context())
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"users": users})
}

func (s *Server) createUser(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Username string             `json:"username"`
		Password string             `json:"password"`
		Role     string             `json:"role"`
		Scopes   []models.UserScope `json:"scopes"` // empty = full access
	}
	if err := decode(r, &in); err != nil || in.Username == "" || in.Password == "" {
		writeErr(w, http.StatusBadRequest, "username and password required")
		return
	}
	if roleLevel(in.Role) == 0 {
		in.Role = models.RoleTech
	}
	hash, err := auth.HashPassword(in.Password)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	u, err := s.store.CreateUser(r.Context(), in.Username, hash, in.Role)
	if err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	if len(in.Scopes) > 0 {
		if err := s.store.ReplaceUserScopes(r.Context(), u.ID, in.Scopes); err != nil {
			writeErr(w, http.StatusInternalServerError, err.Error())
			return
		}
	}
	writeJSON(w, http.StatusCreated, u)
}

// getUserScopes returns a user's access scopes (empty = full access).
func (s *Server) getUserScopes(w http.ResponseWriter, r *http.Request) {
	scopes, err := s.store.ListUserScopes(r.Context(), chi.URLParam(r, "id"))
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"scopes": scopes})
}

// setUserScopes replaces a user's access scopes.
func (s *Server) setUserScopes(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Scopes []models.UserScope `json:"scopes"`
	}
	if err := decode(r, &in); err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	if err := s.store.ReplaceUserScopes(r.Context(), chi.URLParam(r, "id"), in.Scopes); err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"scopes": in.Scopes})
}

// setup2FA generates a pending TOTP secret for the current user (not yet
// enabled) and returns the secret + otpauth URI for the authenticator app.
func (s *Server) setup2FA(w http.ResponseWriter, r *http.Request) {
	uid := actorOf(r)
	user, err := s.store.GetUserByID(r.Context(), uid)
	if err != nil {
		writeErr(w, http.StatusBadRequest, "only real user accounts can enable 2FA")
		return
	}
	secret := auth.GenerateTOTPSecret()
	enc, err := settings.Encrypt(s.cfg, secret)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	if err := s.store.SetUserTOTP(r.Context(), uid, enc, false); err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"secret": secret,
		"uri":    auth.TOTPURI("OpenRMM", user.Username, secret),
	})
}

// enable2FA activates 2FA once the user proves they can generate a valid code.
func (s *Server) enable2FA(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Code string `json:"code"`
	}
	_ = decode(r, &in)
	uid := actorOf(r)
	enc, _, err := s.store.GetUserTOTP(r.Context(), uid)
	if err != nil || enc == "" {
		writeErr(w, http.StatusBadRequest, "run 2FA setup first")
		return
	}
	secret, err := settings.Decrypt(s.cfg, enc)
	if err != nil || !auth.ValidateTOTP(secret, in.Code) {
		writeErr(w, http.StatusBadRequest, "invalid code")
		return
	}
	if err := s.store.SetUserTOTP(r.Context(), uid, enc, true); err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	codes, err := s.issueRecoveryCodes(r, uid)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	_ = s.store.AddAudit(r.Context(), uid, "user.2fa.enable", uid, nil)
	writeJSON(w, http.StatusOK, map[string]any{"totp_enabled": true, "recovery_codes": codes})
}

// issueRecoveryCodes generates a fresh set of one-time recovery codes for the
// user, persists their hashes, and returns the plaintext codes (shown once).
func (s *Server) issueRecoveryCodes(r *http.Request, uid string) ([]string, error) {
	codes := auth.GenerateRecoveryCodes(10)
	hashes := make([]string, len(codes))
	for i, c := range codes {
		hashes[i] = auth.Hash(auth.NormalizeRecoveryCode(c))
	}
	if err := s.store.ReplaceRecoveryCodes(r.Context(), uid, hashes); err != nil {
		return nil, err
	}
	return codes, nil
}

// regenRecoveryCodes re-issues recovery codes for the caller (requires a valid
// current TOTP code), invalidating any previous set.
func (s *Server) regenRecoveryCodes(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Code string `json:"code"`
	}
	_ = decode(r, &in)
	uid := actorOf(r)
	enc, enabled, err := s.store.GetUserTOTP(r.Context(), uid)
	if err != nil || !enabled {
		writeErr(w, http.StatusBadRequest, "2FA is not enabled")
		return
	}
	secret, err := settings.Decrypt(s.cfg, enc)
	if err != nil || !auth.ValidateTOTP(secret, in.Code) {
		writeErr(w, http.StatusBadRequest, "invalid code")
		return
	}
	codes, err := s.issueRecoveryCodes(r, uid)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	_ = s.store.AddAudit(r.Context(), uid, "user.2fa.recovery.regenerate", uid, nil)
	writeJSON(w, http.StatusOK, map[string]any{"recovery_codes": codes})
}

// reset2FA (admin) disables a user's 2FA and clears their recovery codes so a
// locked-out user can sign in with their password and re-enroll.
func (s *Server) reset2FA(w http.ResponseWriter, r *http.Request) {
	target := chi.URLParam(r, "id")
	if err := s.store.SetUserTOTP(r.Context(), target, "", false); err != nil {
		writeErr(w, httpStatusFor(err), err.Error())
		return
	}
	if err := s.store.DeleteRecoveryCodes(r.Context(), target); err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	_ = s.store.AddAudit(r.Context(), actorOf(r), "user.2fa.reset", target, nil)
	writeJSON(w, http.StatusOK, map[string]any{"totp_enabled": false})
}

// disable2FA turns 2FA off, requiring a current code to do so.
func (s *Server) disable2FA(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Code string `json:"code"`
	}
	_ = decode(r, &in)
	uid := actorOf(r)
	enc, enabled, err := s.store.GetUserTOTP(r.Context(), uid)
	if err != nil || !enabled {
		writeErr(w, http.StatusBadRequest, "2FA is not enabled")
		return
	}
	secret, err := settings.Decrypt(s.cfg, enc)
	if err != nil || !auth.ValidateTOTP(secret, in.Code) {
		writeErr(w, http.StatusBadRequest, "invalid code")
		return
	}
	if err := s.store.SetUserTOTP(r.Context(), uid, "", false); err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	_ = s.store.DeleteRecoveryCodes(r.Context(), uid)
	_ = s.store.AddAudit(r.Context(), uid, "user.2fa.disable", uid, nil)
	writeJSON(w, http.StatusOK, map[string]any{"totp_enabled": false})
}

func (s *Server) deleteUser(w http.ResponseWriter, r *http.Request) {
	if err := s.store.DeleteUser(r.Context(), chi.URLParam(r, "id")); err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "deleted"})
}

// updateUser changes an existing user's role (admin-only). To avoid locking
// yourself out, you cannot change your own role here.
func (s *Server) updateUser(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	if id == actorOf(r) {
		writeErr(w, http.StatusBadRequest, "you cannot change your own role")
		return
	}
	var in struct {
		Role string `json:"role"`
	}
	if err := decode(r, &in); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid body")
		return
	}
	if roleLevel(in.Role) == 0 {
		writeErr(w, http.StatusBadRequest, "invalid role")
		return
	}
	if err := s.store.SetUserRole(r.Context(), id, in.Role); err != nil {
		writeErr(w, httpStatusFor(err), err.Error())
		return
	}
	detail, _ := json.Marshal(map[string]string{"role": in.Role})
	_ = s.store.AddAudit(r.Context(), actorOf(r), "user.role.change", id, detail)
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}
