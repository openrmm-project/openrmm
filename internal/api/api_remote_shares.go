package api

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"math/big"
	"net/http"
	"strings"
	"sync"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/go-chi/chi/v5"

	"github.com/p1ngu/rmm/internal/auth"
	"github.com/p1ngu/rmm/internal/models"
	"github.com/p1ngu/rmm/internal/remote"
	"github.com/p1ngu/rmm/internal/settings"
	"github.com/p1ngu/rmm/internal/store"
)

// One-time guest access to a remote session (docs/08-remote-access.md).
//
// A technician creates an invitation for a device: a link with a 256-bit token
// and a six-digit code they send by another channel. The guest opens the link,
// types the code and their name, and waits. The technician who sent it accepts
// or rejects them from the top of their own remote session on that device, and
// a guest only ever sees the screen while that session is open.
//
// The guest gets a session of their own, brokered like any other, and the relay
// filters every frame of it (remote.GuestPolicy): the technician's stream is
// untouched, and the restrictions do not depend on the guest's browser or on
// the endpoint's sidecar.

const (
	// guestCodeAttempts wrong codes revoke an invitation. With a six-digit code
	// that is a 1 in 200 000 chance of guessing, on top of needing the link.
	guestCodeAttempts = 5
	// guestNameMax bounds the name a guest gives, which is shown to the
	// technician and written to the audit log.
	guestNameMax = 60
	// guestPollRateLimit is per IP and per minute for the guest page's polling
	// and reconnects. The link and code endpoints use the login limiter.
	guestPollRateLimit = 120
	// guestUserAgentMax and guestIPMax bound what is kept of the guest's user
	// agent and address: both come from request headers.
	guestUserAgentMax = 300
	guestIPMax        = 64
	// guestBrokerInterval is the shortest time between two sessions brokered
	// for one guest. Each one starts a sidecar on the device, so an accepted
	// guest must not be able to make it start one after another.
	guestBrokerInterval = 3 * time.Second
	// guestSweepInterval is how often live guest sessions are checked against
	// their technician: still enabled, still allowed, the share still live.
	guestSweepInterval = 30 * time.Second
)

// guestShareLifetimes are the validity periods an invitation can have, in
// minutes: 15 minutes to a day.
var guestShareLifetimes = map[int]bool{15: true, 60: true, 240: true, 480: true, 1440: true}

// ---------- live guest sessions ----------

// guestSessions tracks the one live session each accepted guest may have, so
// it can be ended the moment its share is withdrawn, expires, or its sponsor
// leaves. In memory on purpose, like presence: a restart drops every relay
// connection with it.
type guestSessions struct {
	mu   sync.Mutex
	live map[string]*guestSession // share id -> session
	// brokered is when each share last got a session, for guestBrokerInterval.
	brokered map[string]time.Time
	sweeping bool
}

type guestSession struct {
	shareID, sessionID, agentID, sponsor string
	timer                                *time.Timer
}

func newGuestSessions() *guestSessions {
	return &guestSessions{live: map[string]*guestSession{}, brokered: map[string]time.Time{}}
}

// allowBroker reports whether a share may get a new session now, and records
// it when it may.
func (g *guestSessions) allowBroker(shareID string, now time.Time) bool {
	if g == nil {
		return false
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	if last, ok := g.brokered[shareID]; ok && now.Sub(last) < guestBrokerInterval {
		return false
	}
	for id, at := range g.brokered {
		if now.Sub(at) > time.Minute {
			delete(g.brokered, id)
		}
	}
	g.brokered[shareID] = now
	return true
}

// liveShares lists the shares that hold a session, for the sweep.
func (g *guestSessions) liveShares() []string {
	if g == nil {
		return nil
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	out := make([]string, 0, len(g.live))
	for id := range g.live {
		out = append(out, id)
	}
	return out
}

// startSweeping reports whether the caller should run the sweep loop: true for
// exactly one caller while no loop runs.
func (g *guestSessions) startSweeping() bool {
	if g == nil {
		return false
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.sweeping {
		return false
	}
	g.sweeping = true
	return true
}

// stopSweeping ends the loop once no guest is left to watch.
func (g *guestSessions) stopSweeping() bool {
	g.mu.Lock()
	defer g.mu.Unlock()
	if len(g.live) > 0 {
		return false
	}
	g.sweeping = false
	return true
}

// start records a share's session and returns the one it replaces, if any.
func (g *guestSessions) start(gs *guestSession) *guestSession {
	if g == nil {
		return nil
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	prev := g.live[gs.shareID]
	g.live[gs.shareID] = gs
	return prev
}

// finish forgets a session that ended by itself, unless a newer one has
// already replaced it.
func (g *guestSessions) finish(shareID, sessionID string) {
	if g == nil {
		return
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	if gs := g.live[shareID]; gs != nil && gs.sessionID == sessionID {
		if gs.timer != nil {
			gs.timer.Stop()
		}
		delete(g.live, shareID)
	}
}

// take removes and returns a share's live session.
func (g *guestSessions) take(shareID string) *guestSession {
	if g == nil {
		return nil
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	gs := g.live[shareID]
	delete(g.live, shareID)
	if gs != nil && gs.timer != nil {
		gs.timer.Stop()
	}
	return gs
}

// sponsoredBy lists the guests one technician let onto one device.
func (g *guestSessions) sponsoredBy(agentID, sponsor string) []string {
	if g == nil {
		return nil
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	var out []string
	for id, gs := range g.live {
		if gs.agentID == agentID && gs.sponsor == sponsor {
			out = append(out, id)
		}
	}
	return out
}

func (g *guestSessions) connected(shareID string) bool {
	if g == nil {
		return false
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.live[shareID] != nil
}

// endGuest closes a share's live session, telling the guest's page why.
func (s *Server) endGuest(shareID string, code int, reason string) {
	gs := s.guests.take(shareID)
	if gs == nil {
		return
	}
	s.presence.Remove(gs.agentID, gs.sessionID)
	s.relay.CloseSession(gs.sessionID, code, reason)
	_ = s.store.SetRemoteSessionStatus(context.Background(), gs.sessionID, "ended")
}

// sponsorLeft ends the sessions of every guest a technician let onto a device
// once that technician has no desktop session there any more. Their shares
// stay accepted: the guest's page waits, and reconnects when the technician
// is back, until the invitation expires.
func (s *Server) sponsorLeft(agentID, sponsor string) {
	if len(s.presence.ForUser(agentID, sponsor, "native")) > 0 {
		return
	}
	for _, shareID := range s.guests.sponsoredBy(agentID, sponsor) {
		s.endGuest(shareID, remote.CloseGuestSponsorLeft, "guest_sponsor_left")
	}
}

// closeAllGuestShares withdraws every invitation and ends every guest session,
// for when guest access is turned off.
func (s *Server) closeAllGuestShares(ctx context.Context, actor string) {
	shares, err := s.store.CloseAllRemoteShares(ctx)
	if err != nil {
		return
	}
	for _, sh := range shares {
		s.endGuest(sh.ID, remote.CloseGuestEnded, "guest_access_disabled")
		s.auditShare(ctx, actor, "remote.share.revoke", &sh, map[string]string{"reason": "guest access turned off"})
	}
}

// sweepGuests ends the session of every guest whose technician may no longer
// supervise them — disabled, deleted, stripped of a permission or of the
// device's scope — or whose share is no longer accepted. Those are checked when
// the guest is accepted and each time it connects; this covers what changes in
// between, within guestSweepInterval.
func (s *Server) sweepGuests(ctx context.Context) {
	now := time.Now()
	for _, shareID := range s.guests.liveShares() {
		sh, err := s.store.GetRemoteShare(ctx, shareID)
		if errors.Is(err, store.ErrNotFound) || (err == nil && sh.EffectiveStatus(now) != models.RemoteShareAccepted) {
			s.endGuest(shareID, remote.CloseGuestEnded, "guest_removed")
			continue
		}
		if err != nil {
			continue // the database is unreachable: try again next round
		}
		if s.sponsorStillAllowed(ctx, sh) != nil || !s.guestAccessEnabled(ctx) {
			s.endGuest(shareID, remote.CloseGuestEnded, "guest_sponsor_revoked")
		}
	}
}

// ensureGuestSweeper runs sweepGuests while any guest has a session.
func (s *Server) ensureGuestSweeper() {
	if !s.guests.startSweeping() {
		return
	}
	go func() {
		ticker := time.NewTicker(guestSweepInterval)
		defer ticker.Stop()
		for range ticker.C {
			ctx, cancel := context.WithTimeout(context.Background(), guestSweepInterval)
			s.sweepGuests(ctx)
			cancel()
			if s.guests.stopSweeping() {
				return
			}
		}
	}()
}

// ---------- helpers ----------

func (s *Server) guestAccessEnabled(ctx context.Context) bool {
	eff, err := settings.Effective(ctx, s.store, s.cfg)
	return err == nil && eff.RemoteGuestAccess
}

// sponsorOnDevice reports whether a technician has a desktop session open on
// a device right now. Accepting a guest, and every guest connection, need one.
func (s *Server) sponsorOnDevice(agentID, sponsor string) bool {
	return len(s.presence.ForUser(agentID, sponsor, "native")) > 0
}

// sponsorStillAllowed rechecks the technician behind a share: still enabled,
// still holding both permissions, and the device still in their scope. It runs
// when the guest is accepted and every time the guest connects, so a change
// made after the invitation was sent is not outlived by it.
func (s *Server) sponsorStillAllowed(ctx context.Context, sh *models.RemoteShare) error {
	u, err := s.store.GetUserByID(ctx, sh.CreatedBy)
	if err != nil || u.DisabledAt != nil {
		return errors.New("the technician who sent this invitation can no longer use it")
	}
	perms, err := s.permissionsForRole(ctx, u.Role)
	if err != nil {
		return err
	}
	set := permissionSet(perms)
	if !set[models.PermDevicesRemote] || !set[models.PermDevicesRemoteShare] {
		return errors.New("the technician who sent this invitation no longer has the permission to share access")
	}
	scope, err := s.effectiveScopeUser(ctx, u.ID)
	if err != nil {
		return err
	}
	if scope != "" {
		ok, err := s.store.AgentAllowedForUser(ctx, scope, sh.AgentID)
		if err != nil || !ok {
			return errors.New("the device is outside the technician's access scope")
		}
	}
	return nil
}

// guestShareCode is a uniformly random six-digit code.
func guestShareCode() (string, error) {
	n, err := rand.Int(rand.Reader, big.NewInt(1_000_000))
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("%06d", n.Int64()), nil
}

// guestCodeHash binds the code to its link, so the stored hash of a six-digit
// code cannot be reversed without the token, which is never stored.
func guestCodeHash(token, code string) string {
	return auth.Hash("remote-share-code:" + token + ":" + code)
}

// cleanGuestName keeps a name readable in a banner and an audit line: no
// control characters, single spaces, and a bounded length.
func cleanGuestName(raw string) string {
	var b strings.Builder
	space := false
	for _, r := range strings.TrimSpace(raw) {
		switch {
		case unicode.IsSpace(r):
			space = true
			continue
		case unicode.IsControl(r) || r == unicode.ReplacementChar || !unicode.IsPrint(r):
			continue
		}
		if space && b.Len() > 0 {
			b.WriteByte(' ')
		}
		space = false
		b.WriteRune(r)
	}
	name := []rune(b.String())
	if len(name) > guestNameMax {
		name = name[:guestNameMax]
	}
	return strings.TrimSpace(string(name))
}

// cleanHeaderValue keeps a request header fit to store and show: valid UTF-8
// (PostgreSQL refuses anything else, and the guest would be told the link does
// not work), no control characters, and at most max bytes, cut between
// characters.
func cleanHeaderValue(raw string, max int) string {
	var b strings.Builder
	for _, r := range strings.ToValidUTF8(raw, "") {
		if unicode.IsControl(r) {
			continue
		}
		if b.Len()+utf8.RuneLen(r) > max {
			break
		}
		b.WriteRune(r)
	}
	return strings.TrimSpace(b.String())
}

// guestCredential reads a guest's credential from an HTTP request.
func guestCredential(r *http.Request) string {
	const prefix = "Guest "
	if h := r.Header.Get("Authorization"); strings.HasPrefix(h, prefix) {
		return strings.TrimSpace(strings.TrimPrefix(h, prefix))
	}
	return ""
}

// guestRelayCredential reads it from the relay handshake, where it rides in
// Sec-WebSocket-Protocol next to the ticket, as the operator's token does.
func guestRelayCredential(r *http.Request) string {
	for _, p := range strings.Split(r.Header.Get("Sec-WebSocket-Protocol"), ",") {
		p = strings.TrimSpace(p)
		if strings.HasPrefix(p, "rmm-guest.") {
			return strings.TrimPrefix(p, "rmm-guest.")
		}
	}
	return ""
}

func (s *Server) auditShare(ctx context.Context, actor, action string, sh *models.RemoteShare, extra map[string]string) {
	detail := map[string]string{"share": sh.ID, "agent": sh.AgentID, "mode": sh.Mode, "sponsor": sh.CreatedBy}
	if sh.GuestName != "" {
		detail["guest_name"] = sh.GuestName
	}
	if sh.GuestIP != "" {
		detail["guest_ip"] = sh.GuestIP
	}
	if sh.GuestUserAgent != "" {
		detail["guest_user_agent"] = sh.GuestUserAgent
	}
	if a, err := s.store.GetAgent(ctx, sh.AgentID); err == nil && a != nil {
		detail["host"] = a.Hostname
	}
	for k, v := range extra {
		detail[k] = v
	}
	raw, _ := json.Marshal(detail)
	_ = s.store.AddAudit(ctx, actor, action, sh.AgentID, raw)
}

func writeCodedErr(w http.ResponseWriter, status int, code, msg string) {
	writeJSON(w, status, map[string]string{"error": msg, "code": code})
}

// sharePrincipal is the user behind a request that manages invitations. Shares
// belong to accounts: the admin token and API tokens cannot sponsor a guest,
// because nobody would be at the screen to accept them.
func (s *Server) sharePrincipal(w http.ResponseWriter, r *http.Request) (string, bool) {
	principal := relayPrincipalOf(r)
	if principal == "" || principal == "admin" {
		writeErr(w, http.StatusForbidden, "guest invitations need a signed-in user account")
		return "", false
	}
	if u, err := s.store.GetUserByID(r.Context(), principal); err != nil || u.DisabledAt != nil {
		writeErr(w, http.StatusForbidden, "guest invitations need a signed-in user account")
		return "", false
	}
	return principal, true
}

// shareView is a share as its technician sees it, effective status included.
type shareView struct {
	models.RemoteShare
	Connected bool `json:"connected"`
}

// Connected is whether the guest's viewer is on the relay now, read from
// presence: a session brokered but never joined is not a guest watching.
func (s *Server) viewShare(sh models.RemoteShare, now time.Time) shareView {
	sh.Status = sh.EffectiveStatus(now)
	live := len(s.presence.ForUser(sh.AgentID, models.GuestPrincipal(sh.ID), "native")) > 0
	return shareView{RemoteShare: sh, Connected: live}
}

// ---------- technician endpoints ----------

// createRemoteShare issues an invitation. The token and the code are in this
// response and nowhere else.
func (s *Server) createRemoteShare(w http.ResponseWriter, r *http.Request) {
	agentID := chi.URLParam(r, "id")
	if !s.requireAgent(w, r, agentID) {
		return
	}
	if !s.guestAccessEnabled(r.Context()) {
		writeCodedErr(w, http.StatusForbidden, "guest_access_disabled", "guest access is turned off for this instance")
		return
	}
	principal, ok := s.sharePrincipal(w, r)
	if !ok {
		return
	}
	var in struct {
		Mode    string `json:"mode"`
		Minutes int    `json:"minutes"`
	}
	if err := decode(r, &in); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid body")
		return
	}
	if in.Mode == "" {
		in.Mode = models.RemoteShareView
	}
	if in.Mode != models.RemoteShareView && in.Mode != models.RemoteShareControl {
		writeErr(w, http.StatusBadRequest, "mode must be view or control")
		return
	}
	if in.Minutes == 0 {
		in.Minutes = 60
	}
	if !guestShareLifetimes[in.Minutes] {
		writeErr(w, http.StatusBadRequest, "minutes must be 15, 60, 240, 480 or 1440")
		return
	}
	agent, err := s.store.GetAgent(r.Context(), agentID)
	if err != nil {
		writeErr(w, httpStatusFor(err), err.Error())
		return
	}
	if agent.Status == models.AgentRevoked {
		writeErr(w, http.StatusConflict, "device credential is revoked; enroll it again to restore access")
		return
	}
	token := auth.NewToken()
	code, err := guestShareCode()
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	expires := time.Now().Add(time.Duration(in.Minutes) * time.Minute)
	sh, err := s.store.CreateRemoteShare(r.Context(), agentID, principal, in.Mode, auth.Hash(token), guestCodeHash(token, code), expires)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	s.auditShare(r.Context(), actorOf(r), "remote.share.create", sh, map[string]string{"expires_at": sh.ExpiresAt.UTC().Format(time.RFC3339)})
	writeJSON(w, http.StatusCreated, map[string]any{"share": s.viewShare(*sh, time.Now()), "token": token, "code": code})
}

// listRemoteShares lists the caller's live invitations on a device.
func (s *Server) listRemoteShares(w http.ResponseWriter, r *http.Request) {
	agentID := chi.URLParam(r, "id")
	if !s.requireAgent(w, r, agentID) {
		return
	}
	principal, ok := s.sharePrincipal(w, r)
	if !ok {
		return
	}
	shares, err := s.store.RemoteSharesForCreator(r.Context(), agentID, principal)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	now := time.Now()
	out := make([]shareView, 0, len(shares))
	for _, sh := range shares {
		out = append(out, s.viewShare(sh, now))
	}
	writeJSON(w, http.StatusOK, map[string]any{"shares": out})
}

// pendingRemoteShares is what an open remote session polls: guests waiting
// for this technician's decision on this device, and guests already in.
func (s *Server) pendingRemoteShares(w http.ResponseWriter, r *http.Request) {
	agentID := r.URL.Query().Get("agent_id")
	if agentID == "" {
		writeErr(w, http.StatusBadRequest, "agent_id is required")
		return
	}
	if !s.requireAgent(w, r, agentID) {
		return
	}
	principal, ok := s.sharePrincipal(w, r)
	if !ok {
		return
	}
	shares, err := s.store.RemoteSharesForCreator(r.Context(), agentID, principal)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	now := time.Now()
	pending, guests := []shareView{}, []shareView{}
	for _, sh := range shares {
		switch sh.Status {
		case models.RemoteSharePending:
			pending = append(pending, s.viewShare(sh, now))
		case models.RemoteShareAccepted:
			guests = append(guests, s.viewShare(sh, now))
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{"pending": pending, "guests": guests, "open": len(shares)})
}

// decideRemoteShare accepts or rejects a waiting guest. Only the technician
// who sent the invitation decides, and accepting needs their own desktop
// session on the device to be open at that moment.
func (s *Server) decideRemoteShare(accept bool) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		principal, ok := s.sharePrincipal(w, r)
		if !ok {
			return
		}
		sh, err := s.store.GetRemoteShare(r.Context(), chi.URLParam(r, "id"))
		if err != nil || sh.CreatedBy != principal {
			writeErr(w, http.StatusNotFound, "invitation not found")
			return
		}
		if !s.requireAgent(w, r, sh.AgentID) {
			return
		}
		if accept {
			if !s.guestAccessEnabled(r.Context()) {
				writeCodedErr(w, http.StatusForbidden, "guest_access_disabled", "guest access is turned off for this instance")
				return
			}
			if err := s.sponsorStillAllowed(r.Context(), sh); err != nil {
				writeErr(w, http.StatusForbidden, err.Error())
				return
			}
			if !s.sponsorOnDevice(sh.AgentID, principal) {
				writeCodedErr(w, http.StatusConflict, "sponsor_session_required", "open your own remote session on this device to let the guest in")
				return
			}
		}
		decided, err := s.store.DecideRemoteShare(r.Context(), sh.ID, principal, accept)
		if err != nil {
			if errors.Is(err, store.ErrNotFound) {
				writeErr(w, http.StatusConflict, "the guest is no longer waiting")
				return
			}
			writeErr(w, http.StatusInternalServerError, err.Error())
			return
		}
		action := "remote.share.reject"
		if accept {
			action = "remote.share.accept"
		}
		s.auditShare(r.Context(), actorOf(r), action, decided, nil)
		writeJSON(w, http.StatusOK, map[string]any{"share": s.viewShare(*decided, time.Now())})
	}
}

// closeRemoteShare revokes an invitation, or removes a guest who is in. The
// technician who sent it may, and so may a user manager.
func (s *Server) closeRemoteShare(w http.ResponseWriter, r *http.Request) {
	principal := relayPrincipalOf(r)
	sh, err := s.store.GetRemoteShare(r.Context(), chi.URLParam(r, "id"))
	if err != nil || (sh.CreatedBy != principal && !hasPermission(r, models.PermUsersManage)) {
		writeErr(w, http.StatusNotFound, "invitation not found")
		return
	}
	if !s.requireAgent(w, r, sh.AgentID) {
		return
	}
	closed, err := s.store.CloseRemoteShare(r.Context(), sh.ID)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			writeErr(w, http.StatusConflict, "the invitation is already closed")
			return
		}
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	s.endGuest(closed.ID, remote.CloseGuestEnded, "guest_removed")
	action := "remote.share.revoke"
	if closed.Status == models.RemoteShareEnded {
		action = "remote.share.remove"
	}
	s.auditShare(r.Context(), actorOf(r), action, closed, nil)
	writeJSON(w, http.StatusOK, map[string]any{"share": s.viewShare(*closed, time.Now())})
}

// ---------- guest endpoints (no account) ----------

// guestShareFromRequest resolves the guest's credential to their share.
func (s *Server) guestShareFromRequest(w http.ResponseWriter, r *http.Request) (*models.RemoteShare, bool) {
	cred := guestCredential(r)
	if cred == "" {
		writeErr(w, http.StatusUnauthorized, "missing guest credential")
		return nil, false
	}
	sh, err := s.store.RemoteShareByGuestSecret(r.Context(), auth.Hash(cred))
	if err != nil {
		writeErr(w, http.StatusUnauthorized, "invalid guest credential")
		return nil, false
	}
	return sh, true
}

// technicianName is how a guest learns who invited them, and nothing else
// about the instance or the device.
func (s *Server) technicianName(ctx context.Context, userID string) string {
	if u, err := s.store.GetUserByID(ctx, userID); err == nil && u != nil {
		return u.Username
	}
	return ""
}

// guestLookup tells the guest page whose invitation a link is.
func (s *Server) guestLookup(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Token string `json:"token"`
	}
	if err := decode(r, &in); err != nil || in.Token == "" {
		writeErr(w, http.StatusBadRequest, "invalid body")
		return
	}
	if !s.guestAccessEnabled(r.Context()) {
		writeCodedErr(w, http.StatusNotFound, "link_invalid", "this link does not work")
		return
	}
	sh, err := s.store.OpenRemoteShareByToken(r.Context(), auth.Hash(in.Token))
	if err != nil {
		writeCodedErr(w, http.StatusNotFound, "link_invalid", "this link does not work")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"technician": s.technicianName(r.Context(), sh.CreatedBy),
		"mode":       sh.Mode,
		"expires_at": sh.ExpiresAt,
	})
}

// guestJoin checks the code. The first correct one burns the link, and the
// guest gets a credential bound to this one request.
func (s *Server) guestJoin(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Token string `json:"token"`
		Code  string `json:"code"`
		Name  string `json:"name"`
	}
	if err := decode(r, &in); err != nil || in.Token == "" {
		writeErr(w, http.StatusBadRequest, "invalid body")
		return
	}
	name := cleanGuestName(in.Name)
	if name == "" {
		writeCodedErr(w, http.StatusBadRequest, "name_required", "enter your name")
		return
	}
	code := strings.TrimSpace(in.Code)
	if len(code) != 6 || strings.Trim(code, "0123456789") != "" {
		writeCodedErr(w, http.StatusBadRequest, "wrong_code", "the code is six digits")
		return
	}
	if !s.guestAccessEnabled(r.Context()) {
		writeCodedErr(w, http.StatusNotFound, "link_invalid", "this link does not work")
		return
	}
	tokenHash := auth.Hash(in.Token)
	secret := auth.NewToken()
	ip := cleanHeaderValue(clientIP(r, s.cfg.TrustProxyHeaders), guestIPMax)
	ua := cleanHeaderValue(r.UserAgent(), guestUserAgentMax)
	sh, err := s.store.RedeemRemoteShare(r.Context(), tokenHash, guestCodeHash(in.Token, code), auth.Hash(secret), name, ip, ua, guestCodeAttempts)
	switch {
	case errors.Is(err, store.ErrRemoteShareCode):
		if failed, lookupErr := s.store.GetRemoteShareByTokenHash(r.Context(), tokenHash); lookupErr == nil {
			s.auditShare(r.Context(), "guest", "remote.share.code_failed", failed, map[string]string{"guest_ip": ip, "attempts": fmt.Sprint(failed.FailedAttempts)})
		}
		writeCodedErr(w, http.StatusForbidden, "wrong_code", "the code is not right")
		return
	case err != nil:
		writeCodedErr(w, http.StatusNotFound, "link_invalid", "this link does not work")
		return
	}
	s.auditShare(r.Context(), "guest", "remote.share.redeem", sh, nil)
	writeJSON(w, http.StatusOK, map[string]any{
		"request_id":       sh.ID,
		"guest_credential": secret,
		"technician":       s.technicianName(r.Context(), sh.CreatedBy),
		"mode":             sh.Mode,
		"expires_at":       sh.ExpiresAt,
	})
}

// guestRequest is what the guest page polls: the decision, and whether the
// technician is on the device to supervise.
func (s *Server) guestRequest(w http.ResponseWriter, r *http.Request) {
	sh, ok := s.guestShareFromRequest(w, r)
	if !ok {
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"status":          sh.EffectiveStatus(time.Now()),
		"mode":            sh.Mode,
		"technician":      s.technicianName(r.Context(), sh.CreatedBy),
		"expires_at":      sh.ExpiresAt,
		"sponsor_present": sh.Status == models.RemoteShareAccepted && s.sponsorOnDevice(sh.AgentID, sh.CreatedBy),
	})
}

// guestSession brokers a session for an accepted guest: their own relay pair
// and sidecar, never the technician's. Called again to reconnect after a drop.
func (s *Server) guestSession(w http.ResponseWriter, r *http.Request) {
	sh, ok := s.guestShareFromRequest(w, r)
	if !ok {
		return
	}
	now := time.Now()
	if status := sh.EffectiveStatus(now); status != models.RemoteShareAccepted {
		writeCodedErr(w, http.StatusConflict, "share_"+status, "the invitation is "+status)
		return
	}
	if !s.guestAccessEnabled(r.Context()) {
		writeCodedErr(w, http.StatusConflict, "share_ended", "guest access is turned off")
		return
	}
	if err := s.sponsorStillAllowed(r.Context(), sh); err != nil {
		writeCodedErr(w, http.StatusConflict, "share_ended", err.Error())
		return
	}
	if !s.sponsorOnDevice(sh.AgentID, sh.CreatedBy) {
		writeCodedErr(w, http.StatusConflict, "sponsor_absent", "the technician is not in a session on the device right now")
		return
	}
	agent, err := s.store.GetAgent(r.Context(), sh.AgentID)
	if err != nil || agent.Status == models.AgentRevoked {
		writeCodedErr(w, http.StatusConflict, "share_ended", "the device is no longer available")
		return
	}
	if !s.guests.allowBroker(sh.ID, now) {
		writeCodedErr(w, http.StatusTooManyRequests, "too_soon", "wait a moment before reconnecting")
		return
	}
	// A reconnect replaces the guest's previous session, so a stale socket
	// cannot keep a second stream going.
	if prev := s.guests.take(sh.ID); prev != nil {
		s.presence.Remove(prev.agentID, prev.sessionID)
		s.relay.CloseSession(prev.sessionID, remote.CloseSessionReplaced, "remote_session_replaced")
		_ = s.store.SetRemoteSessionStatus(r.Context(), prev.sessionID, "ended")
	}
	rs, err := s.remote.Start(r.Context(), "native", agent, models.GuestPrincipal(sh.ID), remote.StartOptions{AllowFiles: false})
	if err != nil {
		writeCodedErr(w, http.StatusServiceUnavailable, "device_unavailable", err.Error())
		return
	}
	gs := &guestSession{shareID: sh.ID, sessionID: rs.ID, agentID: sh.AgentID, sponsor: sh.CreatedBy}
	shareID := sh.ID
	gs.timer = time.AfterFunc(time.Until(sh.ExpiresAt), func() {
		s.endGuest(shareID, remote.CloseGuestExpired, "guest_share_expired")
	})
	s.guests.start(gs)
	s.ensureGuestSweeper()
	s.auditShare(r.Context(), "guest", "remote.share.guest_start", sh, map[string]string{"session": rs.ID})
	writeJSON(w, http.StatusCreated, map[string]any{"id": rs.ID, "viewer_ticket": rs.ViewerTicket, "mode": sh.Mode})
}

// owns reports whether a session is the one a share currently holds.
func (g *guestSessions) owns(shareID, sessionID string) bool {
	if g == nil {
		return false
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	gs := g.live[shareID]
	return gs != nil && gs.sessionID == sessionID
}

// guestRelayShare admits a guest's viewer to the relay: the credential must
// belong to an accepted, unexpired share, the session must be the one brokered
// for that share, and the technician must still be on the device.
func (s *Server) guestRelayShare(ctx context.Context, sessionID, credential string) (*models.RemoteShare, error) {
	refused := errors.New("invalid or expired guest session")
	sh, err := s.store.RemoteShareByGuestSecret(ctx, auth.Hash(credential))
	if err != nil || sh.EffectiveStatus(time.Now()) != models.RemoteShareAccepted {
		return nil, refused
	}
	meta, err := s.store.GetRemoteSessionForAuthorization(ctx, sessionID)
	if err != nil || meta.Provider != "native" || meta.CreatedBy != models.GuestPrincipal(sh.ID) || meta.AgentID != sh.AgentID {
		return nil, refused
	}
	if !s.guestAccessEnabled(ctx) || !s.sponsorOnDevice(sh.AgentID, sh.CreatedBy) {
		return nil, refused
	}
	if err := s.sponsorStillAllowed(ctx, sh); err != nil {
		return nil, refused
	}
	return sh, nil
}
