package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"strings"
	"time"
	"unicode"

	"github.com/go-chi/chi/v5"

	"github.com/p1ngu/rmm/internal/auth"
	"github.com/p1ngu/rmm/internal/models"
	"github.com/p1ngu/rmm/internal/settings"
	"github.com/p1ngu/rmm/internal/store"
)

const sessionTTL = 7 * 24 * time.Hour

const minPasswordLength = 12

// passwordRequirement is the single statement of the rule, shown to the operator
// and enforced here. One string so the form, the error and the check cannot
// drift into telling the user three different things.
const passwordRequirement = "password must be at least 12 characters and include an uppercase letter and a symbol, and must not be a common password"

// passwordProblem returns why a password is unacceptable, or "" when it is fine.
//
// It names the specific failure rather than restating the whole rule: an
// operator who typed eleven lowercase letters is helped by "needs an uppercase
// letter", not by reading the policy again and guessing which part they missed.
func passwordProblem(password, username string) string {
	if len([]rune(password)) < minPasswordLength {
		return fmt.Sprintf("password must be at least %d characters", minPasswordLength)
	}
	var hasUpper, hasSymbol bool
	for _, r := range password {
		switch {
		case unicode.IsUpper(r):
			hasUpper = true
		// A symbol is anything that is not a letter, a digit or a space. Defining
		// it by exclusion accepts the accented and non-Latin keyboards this fleet
		// actually uses, instead of an ASCII allowlist that would reject them.
		case !unicode.IsLetter(r) && !unicode.IsDigit(r) && !unicode.IsSpace(r):
			hasSymbol = true
		}
	}
	if !hasUpper {
		return "password must include at least one uppercase letter"
	}
	if !hasSymbol {
		return "password must include at least one symbol"
	}
	lower := strings.ToLower(password)
	if username != "" && lower == strings.ToLower(username) {
		return "password must not be the username"
	}
	for _, common := range []string{"passwordpassword", "123456789012", "qwertyuiop12"} {
		if lower == common {
			return "password must not be a common password"
		}
	}
	return ""
}

func validPassword(password, username string) bool {
	return passwordProblem(password, username) == ""
}

// bootstrapAdmin creates the very first admin user, only when no users exist.
//
// It takes the operator's RMM_ADMIN_TOKEN. "Only while empty" is not a guard
// on its own: a new instance is empty from the moment it answers until someone
// signs up, and whoever calls this first owns it. A hosted tenant is reachable
// before its customer knows the URL — its certificate reaches the Certificate
// Transparency logs, which scanners read within seconds, and the load balancer
// answers for the name before any DNS record exists. The token turns the race
// into a step only the operator can take.
func (s *Server) bootstrapAdmin(w http.ResponseWriter, r *http.Request) {
	if s.cfg.AdminToken == "" || !auth.ConstantTimeEqual(bearer(r), s.cfg.AdminToken) {
		writeErr(w, http.StatusUnauthorized, "bootstrap requires the server's admin token (Authorization: Bearer $RMM_ADMIN_TOKEN)")
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
	if !validPassword(in.Password, in.Username) {
		writeErr(w, http.StatusBadRequest, passwordProblem(in.Password, in.Username))
		return
	}
	hash, err := auth.HashPassword(in.Password)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	token := auth.NewToken()
	u, err := s.store.BootstrapAdmin(r.Context(), in.Username, hash, auth.Hash(token), time.Now().Add(sessionTTL))
	if errors.Is(err, store.ErrConflict) {
		writeErr(w, http.StatusForbidden, "users already exist; use an admin to create more")
		return
	}
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	perms, err := s.permissionsForRole(r.Context(), u.Role)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	u.Permissions = perms
	writeJSON(w, http.StatusCreated, map[string]any{"token": token, "role": u.Role, "permissions": perms, "user": u})
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
	// Checked after the password, so it tells nothing to someone who does not
	// already hold it. See signsInWithSSO.
	if sso, err := s.signsInWithSSO(r.Context(), id); err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	} else if sso {
		writeJSON(w, http.StatusForbidden, map[string]string{"error": "this account signs in with single sign-on", "code": "sso_required"})
		return
	}
	// Second factor: if enabled, a valid TOTP code — or a one-time recovery
	// code — is required.
	encSecret, enabled, err := s.store.GetUserTOTP(r.Context(), id)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	if enabled {
		if in.Code == "" {
			writeJSON(w, http.StatusUnauthorized, map[string]any{"error": "two-factor code required", "totp_required": true})
			return
		}
		secret, derr := settings.Decrypt(s.cfg, encSecret)
		ok := false
		if derr == nil {
			if ok, err = s.spendTOTP(r.Context(), id, secret, in.Code); err != nil {
				writeErr(w, http.StatusInternalServerError, err.Error())
				return
			}
		}
		if !ok {
			// Fall back to a recovery code (consumed on use).
			used, uerr := s.store.UseRecoveryCode(r.Context(), id, auth.Hash(auth.NormalizeRecoveryCode(in.Code)))
			if uerr != nil {
				writeErr(w, http.StatusInternalServerError, uerr.Error())
				return
			}
			if used {
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
	user := &models.User{ID: id, Username: in.Username, Role: role, TOTPEnabled: enabled}
	perms, err := s.permissionsForRole(r.Context(), role)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	user.Permissions = perms
	writeJSON(w, http.StatusOK, map[string]any{"token": token, "role": role, "permissions": perms, "user": user})
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
	perms, err := s.permissionsForRole(r.Context(), role)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	if u, err := s.store.GetUserByID(r.Context(), actor); err == nil {
		u.Permissions = perms
		writeJSON(w, http.StatusOK, u)
		return
	}
	name := actor
	if actor == "admin" {
		name = "admin"
	} else if actor == "api" {
		name = "api-token"
	}
	writeJSON(w, http.StatusOK, models.User{ID: actor, Username: name, Role: role, Permissions: perms})
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
	if !validPassword(in.NewPassword, "") {
		writeErr(w, http.StatusBadRequest, passwordProblem(in.NewPassword, ""))
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
	if s.scopeUserID(r) != "" {
		filtered := users[:0]
		for _, user := range users {
			ok, err := s.userManageAllowed(r, &user)
			if err != nil {
				writeErr(w, http.StatusInternalServerError, err.Error())
				return
			}
			if ok {
				filtered = append(filtered, user)
			}
		}
		users = filtered
	}
	writeJSON(w, http.StatusOK, map[string]any{"users": users})
}

// spendTOTP checks a two-factor code and spends its time step. A code stays
// valid for about ninety seconds; spending it means one seen over a shoulder,
// or relayed by a phishing page, does not work a second time in that window.
func (s *Server) spendTOTP(ctx context.Context, userID, secret, code string) (bool, error) {
	step, ok := auth.MatchTOTP(secret, code)
	if !ok {
		return false, nil
	}
	return s.store.ConsumeTOTPStep(ctx, userID, step)
}

// spendTOTPOrFail is spendTOTP for handlers: it writes the refusal itself.
func (s *Server) spendTOTPOrFail(w http.ResponseWriter, r *http.Request, userID, secret, code string, status int, msg string) bool {
	ok, err := s.spendTOTP(r.Context(), userID, secret, code)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return false
	}
	if !ok {
		writeErr(w, status, msg)
		return false
	}
	return true
}

// userManageAllowed reports whether the caller may act on user. Nobody manages
// an account that holds a permission they lack — scoped or not. Without that,
// an unscoped custom role with users.manage could change an administrator's
// email, reset their second factor and take the account over.
func (s *Server) userManageAllowed(r *http.Request, user *models.User) (bool, error) {
	perms, err := s.permissionsForRole(r.Context(), user.Role)
	if err != nil || !permissionsSubset(perms, permissionsOf(r)) {
		return false, err
	}
	uid := s.scopeUserID(r)
	if uid == "" {
		return true, nil
	}
	scopes, err := s.store.ListUserScopes(r.Context(), user.ID)
	if err != nil || len(scopes) == 0 {
		return false, err
	}
	for _, scope := range scopes {
		ok, err := s.store.ScopeAllowedForUser(r.Context(), uid, scope.ScopeType, scope.ScopeID)
		if err != nil || !ok {
			return false, err
		}
	}
	return true, nil
}

func (s *Server) requireUserManage(w http.ResponseWriter, r *http.Request, userID string) (*models.User, bool) {
	user, err := s.store.GetUserByID(r.Context(), userID)
	if err != nil {
		writeErr(w, httpStatusFor(err), err.Error())
		return nil, false
	}
	ok, err := s.userManageAllowed(r, user)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return nil, false
	}
	if !ok {
		writeErr(w, http.StatusForbidden, "user is outside your delegated scope")
		return nil, false
	}
	return user, true
}

func (s *Server) validateDelegatedScopes(w http.ResponseWriter, r *http.Request, scopes []models.UserScope) bool {
	if s.scopeUserID(r) != "" && len(scopes) == 0 {
		writeErr(w, http.StatusForbidden, "scoped users cannot delegate full fleet access")
		return false
	}
	for _, scope := range scopes {
		if !s.requireScope(w, r, scope.ScopeType, scope.ScopeID) {
			return false
		}
	}
	return true
}

func (s *Server) validateDelegatedRole(w http.ResponseWriter, r *http.Request, role string) bool {
	if s.scopeUserID(r) != "" && role == models.RoleAdmin {
		writeErr(w, http.StatusForbidden, "restricted principals cannot delegate the global admin role")
		return false
	}
	perms, err := s.permissionsForRole(r.Context(), role)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return false
	}
	if !permissionsSubset(perms, permissionsOf(r)) {
		writeErr(w, http.StatusForbidden, "role exceeds the issuer's permissions")
		return false
	}
	return true
}

func (s *Server) createUser(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Username string             `json:"username"`
		Password string             `json:"password"`
		Role     string             `json:"role"`
		Email    string             `json:"email"`  // optional: alerts + password reset
		Scopes   []models.UserScope `json:"scopes"` // empty = full access
	}
	if err := decode(r, &in); err != nil || in.Username == "" || in.Password == "" {
		writeErr(w, http.StatusBadRequest, "username and password required")
		return
	}
	// Same gate as the dedicated address route: creating an account is not a way
	// around users.email.manage. Checked before anything is written so the
	// request fails whole rather than leaving a half-configured user behind.
	if in.Email != "" && !hasPermission(r, models.PermUsersEmailManage) {
		writeErr(w, http.StatusForbidden, "setting an email address requires the users.email.manage permission")
		return
	}
	// Checked before the account exists so a typo fails the whole request rather
	// than leaving a new user created but unreachable by alerts.
	if in.Email != "" && !validEmail(in.Email) {
		writeErr(w, http.StatusBadRequest, "invalid email address")
		return
	}
	if !validPassword(in.Password, in.Username) {
		writeErr(w, http.StatusBadRequest, passwordProblem(in.Password, in.Username))
		return
	}
	if in.Role == "" {
		in.Role = models.RoleTech
	} else if ok, err := s.roleExists(r.Context(), in.Role); err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	} else if !ok {
		writeErr(w, http.StatusBadRequest, "invalid role")
		return
	}
	if !s.validateDelegatedRole(w, r, in.Role) || !s.validateDelegatedScopes(w, r, in.Scopes) {
		return
	}
	hash, err := auth.HashPassword(in.Password)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	u, err := s.store.CreateUserWithScopes(r.Context(), in.Username, hash, in.Role, in.Scopes)
	if err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	if in.Email != "" {
		updated, err := s.setEmail(r, u.ID, in.Email)
		if err != nil {
			// The account is already created; report the address failure rather
			// than a misleading success, and let the operator set it separately.
			writeErr(w, emailStatus(err), emailMessage(err))
			return
		}
		u = updated
	}
	writeJSON(w, http.StatusCreated, u)
}

func (s *Server) updateUser(w http.ResponseWriter, r *http.Request) {
	target := chi.URLParam(r, "id")
	if _, ok := s.requireUserManage(w, r, target); !ok {
		return
	}
	var in struct {
		Role string `json:"role"`
	}
	if err := decode(r, &in); err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	if ok, err := s.roleExists(r.Context(), in.Role); err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	} else if !ok {
		writeErr(w, http.StatusBadRequest, "invalid role")
		return
	}
	if !s.validateDelegatedRole(w, r, in.Role) {
		return
	}
	if target == actorOf(r) {
		writeErr(w, http.StatusBadRequest, "cannot change your own role")
		return
	}
	old, err := s.store.GetUserByID(r.Context(), target)
	if err != nil {
		writeErr(w, httpStatusFor(err), err.Error())
		return
	}
	if old.Username == "admin" && in.Role != models.RoleAdmin {
		writeErr(w, http.StatusBadRequest, "cannot change the built-in admin role")
		return
	}
	u, err := s.store.UpdateUserRole(r.Context(), target, in.Role)
	if err != nil {
		writeErr(w, httpStatusFor(err), err.Error())
		return
	}
	detail, _ := json.Marshal(map[string]string{"from": old.Role, "to": in.Role})
	_ = s.store.AddAudit(r.Context(), actorOf(r), "user.role.update", target, detail)
	writeJSON(w, http.StatusOK, u)
}

// getUserScopes returns a user's access scopes (empty = full access).
func (s *Server) getUserScopes(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	if _, ok := s.requireUserManage(w, r, id); !ok {
		return
	}
	scopes, err := s.store.ListUserScopes(r.Context(), id)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"scopes": scopes})
}

// setUserScopes replaces a user's access scopes.
//
// An empty list is full fleet access, which is the widest grant this endpoint
// can make and is byte-identical to "I meant to restrict them but picked
// nothing". The console refuses to send the second one; here both are audited,
// because a scope change is otherwise the only user mutation that leaves no
// trace of who widened whose access.
func (s *Server) setUserScopes(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	target, ok := s.requireUserManage(w, r, id)
	if !ok {
		return
	}
	var in struct {
		Scopes []models.UserScope `json:"scopes"`
	}
	if err := decode(r, &in); err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	if !s.validateDelegatedScopes(w, r, in.Scopes) {
		return
	}
	before, err := s.store.ListUserScopes(r.Context(), id)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	if err := s.store.ReplaceUserScopes(r.Context(), id, in.Scopes); err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	detail, _ := json.Marshal(map[string]any{
		"username": target.Username,
		"from":     len(before),
		"to":       len(in.Scopes),
		// The interesting transition for an incident review is the one into full
		// access; the counts alone would bury it.
		"full_access": len(in.Scopes) == 0,
	})
	_ = s.store.AddAudit(r.Context(), actorOf(r), "user.scopes.update", id, detail)
	writeJSON(w, http.StatusOK, map[string]any{"scopes": in.Scopes})
}

// setup2FA generates a pending TOTP secret for the current user (not yet
// enabled) and returns the secret + otpauth URI for the authenticator app.
func (s *Server) setup2FA(w http.ResponseWriter, r *http.Request) {
	var in struct {
		CurrentPassword string `json:"current_password"`
		Code            string `json:"code"`
	}
	if err := decode(r, &in); err != nil && !errors.Is(err, io.EOF) {
		writeErr(w, http.StatusBadRequest, "invalid body")
		return
	}
	uid := actorOf(r)
	user, err := s.store.GetUserByID(r.Context(), uid)
	if err != nil {
		writeErr(w, http.StatusBadRequest, "only real user accounts can enable 2FA")
		return
	}
	currentSecret, enabled, err := s.store.GetUserTOTP(r.Context(), uid)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	hash, err := s.store.GetUserPasswordHash(r.Context(), uid)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	if !auth.CheckPassword(hash, in.CurrentPassword) {
		writeErr(w, http.StatusUnauthorized, "current password is required")
		return
	}
	if enabled {
		secret, derr := settings.Decrypt(s.cfg, currentSecret)
		if derr != nil {
			writeErr(w, http.StatusUnauthorized, "current password and two-factor code are required")
			return
		}
		if !s.spendTOTPOrFail(w, r, uid, secret, in.Code, http.StatusUnauthorized,
			"current password and two-factor code are required") {
			return
		}
	}
	secret := auth.GenerateTOTPSecret()
	enc, err := settings.Encrypt(s.cfg, secret)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	if err := s.store.SetPendingUserTOTP(r.Context(), uid, enc); err != nil {
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
	enc, err := s.store.GetPendingUserTOTP(r.Context(), uid)
	if errors.Is(err, store.ErrNotFound) || enc == "" {
		writeErr(w, http.StatusBadRequest, "run 2FA setup first")
		return
	}
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	secret, err := settings.Decrypt(s.cfg, enc)
	step, valid := auth.MatchTOTP(secret, in.Code)
	if err != nil || !valid {
		writeErr(w, http.StatusBadRequest, "invalid code")
		return
	}
	codes := auth.GenerateRecoveryCodes(10)
	hashes := make([]string, len(codes))
	for i, code := range codes {
		hashes[i] = auth.Hash(auth.NormalizeRecoveryCode(code))
	}
	if err := s.store.EnablePendingUserTOTP(r.Context(), uid, enc, hashes); err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	// The confirming code is spent too, so it cannot complete a sign-in.
	_, _ = s.store.ConsumeTOTPStep(r.Context(), uid, step)
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
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	if !enabled {
		writeErr(w, http.StatusBadRequest, "2FA is not enabled")
		return
	}
	secret, err := settings.Decrypt(s.cfg, enc)
	if err != nil {
		writeErr(w, http.StatusBadRequest, "invalid code")
		return
	}
	if !s.spendTOTPOrFail(w, r, uid, secret, in.Code, http.StatusBadRequest, "invalid code") {
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
	if _, ok := s.requireUserManage(w, r, target); !ok {
		return
	}
	if err := s.store.DisableUserTOTP(r.Context(), target); err != nil {
		writeErr(w, httpStatusFor(err), err.Error())
		return
	}
	_ = s.store.AddAudit(r.Context(), actorOf(r), "user.2fa.reset", target, nil)
	writeJSON(w, http.StatusOK, map[string]any{"totp_enabled": false})
}

// disable2FA turns 2FA off, requiring a current code to do so.
func (s *Server) disable2FA(w http.ResponseWriter, r *http.Request) {
	var in struct {
		CurrentPassword string `json:"current_password"`
		Code            string `json:"code"`
	}
	_ = decode(r, &in)
	uid := actorOf(r)
	enc, enabled, err := s.store.GetUserTOTP(r.Context(), uid)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	if !enabled {
		writeErr(w, http.StatusBadRequest, "2FA is not enabled")
		return
	}
	secret, err := settings.Decrypt(s.cfg, enc)
	hash, hashErr := s.store.GetUserPasswordHash(r.Context(), uid)
	if hashErr != nil {
		writeErr(w, http.StatusInternalServerError, hashErr.Error())
		return
	}
	if err != nil || !auth.CheckPassword(hash, in.CurrentPassword) {
		writeErr(w, http.StatusUnauthorized, "current password and two-factor code are required")
		return
	}
	if !s.spendTOTPOrFail(w, r, uid, secret, in.Code, http.StatusUnauthorized,
		"current password and two-factor code are required") {
		return
	}
	if err := s.store.DisableUserTOTP(r.Context(), uid); err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	_ = s.store.AddAudit(r.Context(), uid, "user.2fa.disable", uid, nil)
	writeJSON(w, http.StatusOK, map[string]any{"totp_enabled": false})
}

// setUserDisabled withdraws or restores an account's access.
//
// The alternative to deleting: deletion cascades away sessions, scopes, tokens
// and subscriptions, which is right for an account created by mistake and wrong
// for a person who left — there the account must stop working while the record
// of what it did survives.
//
// Live sessions are ended immediately rather than left to expire. A disable that
// leaves an open tab working is not a disable, and the operator doing it is
// usually reacting to something.
func (s *Server) setUserDisabled(disabled bool) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id := chi.URLParam(r, "id")
		target, ok := s.requireUserManage(w, r, id)
		if !ok {
			return
		}
		if disabled {
			// The same two guards the role and delete paths carry: locking
			// yourself out is never intended, and the built-in admin is the
			// account that recovers the instance.
			if id == actorOf(r) {
				writeErr(w, http.StatusBadRequest, "you cannot disable your own account")
				return
			}
			if target.Username == "admin" {
				writeErr(w, http.StatusBadRequest, "the built-in admin account cannot be disabled")
				return
			}
		}
		user, err := s.store.SetUserDisabled(r.Context(), id, disabled)
		if err != nil {
			writeErr(w, httpStatusFor(err), err.Error())
			return
		}
		action := "user.enable"
		if disabled {
			action = "user.disable"
			if err := s.store.DeleteUserSessions(r.Context(), id); err != nil {
				// The account is already barred — SessionPrincipal filters on
				// disabled_at — so a failure here is worth recording, not worth
				// failing the request and leaving the operator unsure.
				log.Printf("user.disable: could not end sessions for %s: %v", id, err)
			}
		}
		_ = s.store.AddAudit(r.Context(), actorOf(r), action, id, auditLabel("username", user.Username))
		writeJSON(w, http.StatusOK, user)
	}
}

func (s *Server) deleteUser(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	if _, ok := s.requireUserManage(w, r, id); !ok {
		return
	}
	if err := s.store.DeleteUser(r.Context(), id); err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "deleted"})
}
