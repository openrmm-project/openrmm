// Package supportlink connects a self-hosted console to an OpenRMM support
// plan bought at the portal.
//
// The plan covers a number of devices. The customer pastes the support key the
// portal gave them into the console, and from then on the console reports, every
// few hours, how many devices it holds and which version it runs. That is all it
// sends: three values, only while a key is configured. A console without a key —
// every self-hosted console by default — never contacts the portal.
//
// Nothing here limits the console. The answer only tells the administrators
// whether support is active and whether the plan still covers the fleet; the
// software stays free (AGPL) whatever the plan says.
package supportlink

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"time"
)

const (
	// KeySetting holds the support key, encrypted (settings.Encrypt).
	KeySetting = "support.key"
	// InstanceSetting is a random id for this console, made once, so the portal
	// can tell one console from two sharing a key. It identifies nothing else.
	InstanceSetting = "support.instance_id"
	// StateSetting is the portal's last answer, shown on the settings screen.
	StateSetting = "support.state"

	// DefaultEndpoint is where reports go unless RMM_SUPPORT_ENDPOINT says
	// otherwise (a staging portal).
	DefaultEndpoint = "https://openrmm.io/api/support/usage"

	// MaxDevices bounds what an answer may claim.
	MaxDevices = 1_000_000
)

var keyPattern = regexp.MustCompile(`^ors_[A-Za-z0-9_-]{43}$`)

// ValidKey reports whether key has the shape the portal issues: "ors_" and 32
// random bytes in unpadded base64url.
func ValidKey(key string) bool { return keyPattern.MatchString(key) }

// ErrKeyRejected is the portal refusing the key: revoked, replaced, or not one
// of its keys.
var ErrKeyRejected = errors.New("the portal does not accept this support key; generate a new one in the portal")

// Report is everything a console sends.
type Report struct {
	DevicesUsed int    `json:"devices_used"`
	Version     string `json:"version"`
	InstanceID  string `json:"instance_id"`
}

// State is the portal's answer, as the console keeps it.
type State struct {
	// Status is active, ending (canceled or due to end at PaidThrough) or ended.
	Status          string    `json:"status"`
	PlanDevices     int       `json:"plan_devices"`
	BillingInterval string    `json:"billing_interval"`
	PaidThrough     time.Time `json:"paid_through,omitempty"`
	DevicesUsed     int       `json:"devices_used"`
	// OverLimit: the fleet is larger than the plan. ActionRequiredBy is when
	// the plan's terms take effect if it is not adjusted (renewal canceled, or
	// support ended), when the portal has set one.
	OverLimit        bool      `json:"over_limit"`
	ActionRequiredBy time.Time `json:"action_required_by,omitempty"`
	// PortalURL is where the plan is managed and support is reached.
	PortalURL string    `json:"portal_url"`
	CheckedAt time.Time `json:"checked_at"`
	// LastError is the last report's failure; the fields above stay as they
	// were last known.
	LastError string `json:"last_error,omitempty"`
}

// Client reports to the portal.
type Client struct {
	HTTP     *http.Client
	Endpoint string
	Key      string
}

type answer struct {
	Status           string `json:"status"`
	PlanDevices      *int   `json:"plan_devices"`
	BillingInterval  string `json:"billing_interval"`
	PaidThrough      string `json:"paid_through"`
	OverLimit        bool   `json:"over_limit"`
	ActionRequiredBy string `json:"action_required_by"`
	PortalURL        string `json:"portal_url"`
}

// ValidEndpoint reports whether endpoint can receive reports: HTTPS, with no
// credentials, query or fragment.
func ValidEndpoint(endpoint string) bool {
	u, err := url.Parse(endpoint)
	return err == nil && u.Scheme == "https" && u.Host != "" && u.User == nil &&
		u.RawQuery == "" && u.Fragment == ""
}

// Send reports r and returns the portal's answer. An answer it cannot make
// sense of is an error, never a state.
func (c *Client) Send(ctx context.Context, r Report, now time.Time) (State, error) {
	if !ValidEndpoint(c.Endpoint) {
		return State{}, errors.New("supportlink: the support endpoint must be an https URL")
	}
	if !ValidKey(c.Key) {
		return State{}, errors.New("supportlink: no valid support key")
	}
	body, _ := json.Marshal(r)
	ctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.Endpoint, bytes.NewReader(body))
	if err != nil {
		return State{}, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+c.Key)
	client := c.HTTP
	if client == nil {
		client = &http.Client{Timeout: 20 * time.Second}
	}
	// The request carries the key: a redirect could take it elsewhere.
	noRedirect := *client
	noRedirect.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	resp, err := noRedirect.Do(req)
	if err != nil {
		return State{}, fmt.Errorf("supportlink: portal unreachable: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden {
		return State{}, ErrKeyRejected
	}
	if resp.StatusCode != http.StatusOK {
		return State{}, fmt.Errorf("supportlink: portal answered HTTP %d", resp.StatusCode)
	}
	var a answer
	if err := json.NewDecoder(io.LimitReader(resp.Body, 64<<10)).Decode(&a); err != nil {
		return State{}, fmt.Errorf("supportlink: unreadable answer: %w", err)
	}
	switch a.Status {
	case "active", "ending", "ended":
	default:
		return State{}, errors.New("supportlink: answer has no valid status")
	}
	if a.PlanDevices == nil || *a.PlanDevices < 0 || *a.PlanDevices > MaxDevices {
		return State{}, errors.New("supportlink: answer has no valid plan_devices")
	}
	if a.BillingInterval != "month" && a.BillingInterval != "year" {
		return State{}, errors.New("supportlink: answer has no valid billing_interval")
	}
	st := State{
		Status: a.Status, PlanDevices: *a.PlanDevices, BillingInterval: a.BillingInterval,
		DevicesUsed: r.DevicesUsed, OverLimit: a.OverLimit, CheckedAt: now.UTC(),
		PortalURL: sameHostHTTPS(a.PortalURL, c.Endpoint),
	}
	for _, f := range []struct {
		raw string
		dst *time.Time
	}{{a.PaidThrough, &st.PaidThrough}, {a.ActionRequiredBy, &st.ActionRequiredBy}} {
		if f.raw == "" {
			continue
		}
		t, err := time.Parse(time.RFC3339, f.raw)
		if err != nil {
			return State{}, errors.New("supportlink: answer has an invalid date")
		}
		*f.dst = t.UTC()
	}
	return st, nil
}

// sameHostHTTPS returns raw when it is an https URL on the endpoint's host and
// "" otherwise: the console shows it as a link, and an answer must not be able
// to point that link anywhere else.
func sameHostHTTPS(raw, endpoint string) string {
	u, err := url.Parse(raw)
	e, eerr := url.Parse(endpoint)
	if raw == "" || err != nil || eerr != nil || u.Scheme != "https" || u.User != nil ||
		!strings.EqualFold(u.Host, e.Host) {
		return ""
	}
	return u.String()
}
