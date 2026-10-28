package api

import (
	"context"
	"encoding/json"
	"errors"
	"log"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/p1ngu/rmm/internal/settings"
	"github.com/p1ngu/rmm/internal/store"
	"github.com/p1ngu/rmm/internal/supportlink"
)

// How often a console with a support key reports to the portal. The plan's
// over-limit rules count days, so a few reports a day is plenty.
const supportInterval = 6 * time.Hour

// supportReporting serializes reports, so the periodic one and one triggered
// by saving a key never race to write the state.
var supportReporting sync.Mutex

// RunSupportLink reports to the portal every supportInterval while a support
// key is configured, until ctx ends. Without a key it sends nothing, ever.
func (s *Server) RunSupportLink(ctx context.Context) {
	s.reportSupport(ctx)
	ticker := time.NewTicker(supportInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			s.reportSupport(ctx)
		}
	}
}

// supportKey is the configured key, or "" when there is none.
func (s *Server) supportKey(ctx context.Context) (string, error) {
	setting, err := s.store.GetAppSetting(ctx, supportlink.KeySetting)
	if errors.Is(err, store.ErrNotFound) {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	return settings.Decrypt(s.cfg, setting.Value)
}

// supportInstanceID is this console's random id, made on first use.
func (s *Server) supportInstanceID(ctx context.Context) (string, error) {
	setting, err := s.store.GetAppSetting(ctx, supportlink.InstanceSetting)
	if err == nil && setting.Value != "" {
		return setting.Value, nil
	}
	if err != nil && !errors.Is(err, store.ErrNotFound) {
		return "", err
	}
	id := uuid.NewString()
	return id, s.store.UpsertAppSetting(ctx, supportlink.InstanceSetting, id, false)
}

func (s *Server) supportState(ctx context.Context) (supportlink.State, bool) {
	setting, err := s.store.GetAppSetting(ctx, supportlink.StateSetting)
	if err != nil {
		return supportlink.State{}, false
	}
	var st supportlink.State
	if json.Unmarshal([]byte(setting.Value), &st) != nil {
		return supportlink.State{}, false
	}
	return st, true
}

func (s *Server) saveSupportState(ctx context.Context, st supportlink.State) error {
	b, err := json.Marshal(st)
	if err != nil {
		return err
	}
	return s.store.UpsertAppSetting(ctx, supportlink.StateSetting, string(b), false)
}

func (s *Server) supportClient(key string) *supportlink.Client {
	httpClient := s.supportHTTP
	if httpClient == nil {
		httpClient = &http.Client{Timeout: 20 * time.Second}
	}
	return &supportlink.Client{HTTP: httpClient, Endpoint: s.cfg.SupportEndpoint, Key: key}
}

// sendSupportReport reports with key and returns the portal's answer. It
// writes nothing.
func (s *Server) sendSupportReport(ctx context.Context, key string) (supportlink.State, error) {
	used, err := s.store.CountAgents(ctx)
	if err != nil {
		return supportlink.State{}, err
	}
	instance, err := s.supportInstanceID(ctx)
	if err != nil {
		return supportlink.State{}, err
	}
	return s.supportClient(key).Send(ctx, supportlink.Report{
		DevicesUsed: used, Version: serverVersion(s.cfg), InstanceID: instance,
	}, time.Now())
}

// reportSupport is one periodic report. A failure keeps the last answer and
// records why, so the settings screen can say the link is broken.
func (s *Server) reportSupport(ctx context.Context) {
	supportReporting.Lock()
	defer supportReporting.Unlock()
	key, err := s.supportKey(ctx)
	if err != nil {
		log.Printf("support: could not read the support key: %v", err)
		return
	}
	if key == "" {
		return
	}
	st, err := s.sendSupportReport(ctx, key)
	if err != nil {
		log.Printf("support: report failed: %v", err)
		last, _ := s.supportState(ctx)
		last.LastError = err.Error()
		if err := s.saveSupportState(ctx, last); err != nil {
			log.Printf("support: could not store the report's failure: %v", err)
		}
		return
	}
	if err := s.saveSupportState(ctx, st); err != nil {
		log.Printf("support: could not store the portal's answer: %v", err)
	}
}

// supportCloud reports whether this console is an OpenRMM Cloud console: its
// support comes with the plan, and it has no support key to configure. A
// variable so tests can stand in for a portal's discovery document.
var supportCloud = func(s *Server) bool {
	return s.oidcProvider().EntitlementEndpoint() != ""
}

// GET /api/support — the support link as the settings screen shows it. The key
// itself is never returned.
func (s *Server) supportInfo(w http.ResponseWriter, r *http.Request) {
	key, err := s.supportKey(r.Context())
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "could not read the support settings")
		return
	}
	out := map[string]any{"configured": key != "", "cloud": supportCloud(s)}
	if st, ok := s.supportState(r.Context()); ok && key != "" {
		out["state"] = st
	}
	writeJSON(w, http.StatusOK, out)
}

// PUT /api/support {"key": "..."} — checks the key with the portal, then keeps
// it encrypted. A key the portal refuses is not stored.
func (s *Server) setSupportKey(w http.ResponseWriter, r *http.Request) {
	if supportCloud(s) {
		writeErr(w, http.StatusConflict, "this is an OpenRMM Cloud console: support is part of its plan, reached from the portal")
		return
	}
	var in struct {
		Key string `json:"key"`
	}
	if err := decode(r, &in); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid body")
		return
	}
	key := strings.TrimSpace(in.Key)
	if !supportlink.ValidKey(key) {
		writeErr(w, http.StatusBadRequest, "that is not a support key: copy it from the portal, it starts with ors_")
		return
	}
	supportReporting.Lock()
	defer supportReporting.Unlock()
	st, err := s.sendSupportReport(r.Context(), key)
	if errors.Is(err, supportlink.ErrKeyRejected) {
		writeErr(w, http.StatusUnprocessableEntity, err.Error())
		return
	}
	if err != nil {
		log.Printf("support: checking a new key failed: %v", err)
		writeErr(w, http.StatusBadGateway, "the OpenRMM portal could not be reached to check the key; try again")
		return
	}
	encrypted, err := settings.Encrypt(s.cfg, key)
	if err == nil {
		err = s.store.UpsertAppSetting(r.Context(), supportlink.KeySetting, encrypted, true)
	}
	if err == nil {
		err = s.saveSupportState(r.Context(), st)
	}
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "could not store the support key")
		return
	}
	_ = s.store.AddAudit(r.Context(), actorOf(r), "support.key.set", "", nil)
	writeJSON(w, http.StatusOK, map[string]any{"configured": true, "cloud": false, "state": st})
}

// DELETE /api/support — forgets the key and the last answer; reporting stops.
func (s *Server) removeSupportKey(w http.ResponseWriter, r *http.Request) {
	supportReporting.Lock()
	defer supportReporting.Unlock()
	for _, key := range []string{supportlink.KeySetting, supportlink.StateSetting} {
		if err := s.store.DeleteAppSetting(r.Context(), key); err != nil {
			writeErr(w, http.StatusInternalServerError, "could not remove the support key")
			return
		}
	}
	_ = s.store.AddAudit(r.Context(), actorOf(r), "support.key.removed", "", nil)
	writeJSON(w, http.StatusOK, map[string]any{"configured": false, "cloud": supportCloud(s)})
}
