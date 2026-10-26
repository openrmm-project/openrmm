package api

import (
	"context"
	"fmt"
	"log"
	"net/http"
	"sync"
	"time"

	"github.com/p1ngu/rmm/internal/devicelimit"
	"github.com/p1ngu/rmm/internal/models"
	"github.com/p1ngu/rmm/internal/oidc"
	"github.com/p1ngu/rmm/internal/store"
)

// How often the console asks its portal for the plan's device limit, and
// reports how many devices it holds. A plan change reaches the console within
// this interval, with no restart.
const deviceLimitInterval = 15 * time.Minute

// deviceLimitSource is the fixed source of the console's single device-limit
// alert, so raising it again updates that alert instead of opening another.
var deviceLimitSource = "6f70656e-726d-4d00-8000-6465766c696d" // "openrm" "M" "devlim"

type deviceLimitCache struct {
	mu    sync.RWMutex
	state devicelimit.State
}

func (c *deviceLimitCache) get() devicelimit.State {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.state
}

func (c *deviceLimitCache) set(st devicelimit.State) {
	c.mu.Lock()
	c.state = st
	c.mu.Unlock()
}

// RunDeviceLimit keeps the device limit current until ctx ends. It starts from
// the last answer stored in the database, so a portal that is down at startup
// leaves the console with the limit it last knew rather than none. A console
// whose identity provider names no entitlement endpoint — any self-hosted
// one — never asks and has no limit.
func (s *Server) RunDeviceLimit(ctx context.Context) {
	if st, err := s.store.DeviceLimitState(ctx); err != nil {
		log.Printf("device limit: could not read the stored limit: %v", err)
	} else {
		s.deviceLimit.set(st)
	}
	s.refreshDeviceLimit(ctx)
	ticker := time.NewTicker(deviceLimitInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			s.refreshDeviceLimit(ctx)
		}
	}
}

func (s *Server) refreshDeviceLimit(ctx context.Context) {
	provider := s.oidcProvider()
	endpoint := provider.EntitlementEndpoint()
	if endpoint == "" {
		return
	}
	used, err := s.store.CountAgents(ctx)
	if err != nil {
		log.Printf("device limit: could not count devices: %v", err)
		return
	}
	id, secret := provider.ClientCredentials()
	client := &devicelimit.Client{HTTP: oidc.HTTPClient(), Endpoint: endpoint, ClientID: id, ClientSecret: secret}
	st, err := client.Fetch(ctx, used, time.Now())
	if err != nil {
		// The last known limit stays in force; a portal outage must neither
		// lift it nor tighten it.
		log.Printf("device limit: %v; keeping the last known limit", err)
		return
	}
	if err := s.store.SaveDeviceLimitState(ctx, st); err != nil {
		log.Printf("device limit: could not store the limit: %v", err)
	}
	s.deviceLimit.set(st)
	s.observeDeviceLimit(ctx, used, st)
}

// enforcedDeviceLimit is the limit an enrolment must respect now, or 0.
func (s *Server) enforcedDeviceLimit(now time.Time) int {
	st := s.deviceLimit.get()
	if !st.Enforced(now) {
		return 0
	}
	return st.Limit
}

// observeDeviceLimit keeps the console's device-limit alert in step with the
// count: a warning from 80%, an error at the limit, resolved below 80%.
func (s *Server) observeDeviceLimit(ctx context.Context, used int, st devicelimit.State) {
	now := time.Now()
	level := devicelimit.LevelFor(used, st.Limit)
	if level == devicelimit.LevelNone || level == devicelimit.LevelOK {
		if _, err := s.store.ResolveAlertSource(ctx, models.AlertSourceSystem, &deviceLimitSource, nil,
			"system", "automatic", "Device count is back below 80% of the plan.", now); err != nil {
			log.Printf("device limit: could not resolve the alert: %v", err)
		}
		return
	}
	severity, message := models.SeverityWarning,
		fmt.Sprintf("%d of %d devices in use (80%% of the plan or more). Increase the plan before it is full.", used, st.Limit)
	if level == devicelimit.LevelLimit {
		severity = models.SeverityError
		message = fmt.Sprintf("%d of %d devices in use: the plan is full. New devices cannot be enrolled until the plan is increased or a device is deleted.", used, st.Limit)
		if !st.Enforced(now) {
			message = fmt.Sprintf("%d of %d devices in use: the plan is full. From %s, new devices cannot be enrolled until the plan is increased or a device is deleted.",
				used, st.Limit, st.EnforceFrom.Format("2006-01-02"))
		}
	}
	if _, err := s.store.ObserveAlert(ctx, store.AlertObservation{
		SourceType: models.AlertSourceSystem, SourceID: &deviceLimitSource, ConditionKey: "device_limit",
		Severity: severity, Message: message, ObservedAt: now,
	}); err != nil {
		log.Printf("device limit: could not raise the alert: %v", err)
	}
}

// observeDeviceLimitNow re-evaluates the alert after an enrolment changed the
// count, so reaching the limit is visible at once rather than at the next
// refresh.
func (s *Server) observeDeviceLimitNow() {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	used, err := s.store.CountAgents(ctx)
	if err != nil {
		return
	}
	s.observeDeviceLimit(ctx, used, s.deviceLimit.get())
}

// deviceLimitInfo is GET /api/devices/limit: what the console shows about the
// plan's device limit.
func (s *Server) deviceLimitInfo(w http.ResponseWriter, r *http.Request) {
	used, err := s.store.CountAgents(r.Context())
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	st := s.deviceLimit.get()
	now := time.Now()
	out := map[string]any{
		"used":     used,
		"limit":    st.Limit,
		"level":    devicelimit.LevelFor(used, st.Limit),
		"enforced": st.Enforced(now),
	}
	if st.Limit > 0 {
		out["upgrade_url"] = st.UpgradeURL
		if !st.EnforceFrom.IsZero() && now.Before(st.EnforceFrom) {
			out["enforce_from"] = st.EnforceFrom.Format(time.RFC3339)
		}
	}
	writeJSON(w, http.StatusOK, out)
}
