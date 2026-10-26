// Package devicelimit is how many devices an OpenRMM Cloud console may hold.
//
// The plan a customer pays for covers a number of devices. The console learns
// it from the portal that provisioned it, over the entitlement endpoint the
// portal names in its OIDC discovery document, authenticated with the console's
// own client credentials. A console with no such endpoint — every self-hosted
// installation — has no limit at all.
//
// The limit stops new enrolments only. Devices already enrolled keep working
// whatever the plan says, including after a downgrade: an RMM that stops
// managing machines because of a billing change hurts the people it manages.
package devicelimit

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// SettingKey is the app_settings row holding the last answer, so a restart or
// a portal outage keeps the limit the console last knew.
const SettingKey = "device_limit"

// MaxLimit bounds what a portal answer may claim, so a corrupted reply cannot
// overflow anything downstream. Self-service plans stop far below it.
const MaxLimit = 1_000_000

// ErrLimitReached refuses an enrolment that would exceed the plan.
var ErrLimitReached = errors.New("device limit reached")

// State is the portal's last answer.
type State struct {
	// Limit is the number of devices the plan covers; 0 means no limit.
	Limit int `json:"limit"`
	// EnforceFrom is when enrolments beyond Limit start being refused. Existing
	// customers got a notice period before the rule applied to them.
	EnforceFrom time.Time `json:"enforce_from"`
	// UpgradeURL is the portal page where the plan is changed.
	UpgradeURL string    `json:"upgrade_url"`
	FetchedAt  time.Time `json:"fetched_at"`
}

// Enforced reports whether enrolments beyond Limit are refused at now.
func (s State) Enforced(now time.Time) bool {
	return s.Limit > 0 && !now.Before(s.EnforceFrom)
}

// Level is how close a console is to its limit.
type Level string

const (
	LevelNone    Level = "none"    // no limit
	LevelOK      Level = "ok"      // below 80%
	LevelWarning Level = "warning" // 80% or more
	LevelLimit   Level = "limit"   // at or above the limit
)

// LevelFor places used devices against limit. 80% is computed in integers:
// 80 of 100 is a warning, 79 is not.
func LevelFor(used, limit int) Level {
	switch {
	case limit <= 0:
		return LevelNone
	case used >= limit:
		return LevelLimit
	case used*5 >= limit*4:
		return LevelWarning
	default:
		return LevelOK
	}
}

// RefusalMessage is what the installer prints when an enrolment is refused.
// It reaches the person installing the agent, who is often not the one who
// can change the plan, so it says who to ask.
func RefusalMessage(limit int) string {
	return fmt.Sprintf("device limit reached: this console's plan covers %d devices. "+
		"Ask the console's administrator to increase the plan, or delete a device you no longer manage.", limit)
}

// Client asks a portal for the current entitlement.
type Client struct {
	HTTP         *http.Client
	Endpoint     string
	ClientID     string
	ClientSecret string
}

type answer struct {
	DeviceLimit *int   `json:"device_limit"`
	EnforceFrom string `json:"enforce_from"`
	UpgradeURL  string `json:"upgrade_url"`
}

// Fetch reports used devices and returns the entitlement. A reply it cannot
// make sense of is an error, never a limit of zero: zero means unlimited.
func (c *Client) Fetch(ctx context.Context, used int, now time.Time) (State, error) {
	if c.Endpoint == "" || c.ClientID == "" || c.ClientSecret == "" {
		return State{}, errors.New("devicelimit: no entitlement endpoint or credentials")
	}
	body, _ := json.Marshal(map[string]int{"devices_used": used})
	ctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.Endpoint, bytes.NewReader(body))
	if err != nil {
		return State{}, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.SetBasicAuth(url.QueryEscape(c.ClientID), url.QueryEscape(c.ClientSecret))
	client := c.HTTP
	if client == nil {
		client = &http.Client{Timeout: 20 * time.Second}
	}
	// The endpoint was checked to be on the issuer's host; a redirect could
	// take the credentials elsewhere, so none is followed.
	noRedirect := *client
	noRedirect.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	resp, err := noRedirect.Do(req)
	if err != nil {
		return State{}, fmt.Errorf("devicelimit: portal unreachable: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return State{}, fmt.Errorf("devicelimit: portal answered HTTP %d", resp.StatusCode)
	}
	var a answer
	if err := json.NewDecoder(io.LimitReader(resp.Body, 64<<10)).Decode(&a); err != nil {
		return State{}, fmt.Errorf("devicelimit: unreadable answer: %w", err)
	}
	if a.DeviceLimit == nil || *a.DeviceLimit < 0 || *a.DeviceLimit > MaxLimit {
		return State{}, errors.New("devicelimit: answer has no valid device_limit")
	}
	st := State{Limit: *a.DeviceLimit, FetchedAt: now.UTC()}
	if a.EnforceFrom != "" {
		t, err := time.Parse(time.RFC3339, a.EnforceFrom)
		if err != nil {
			return State{}, errors.New("devicelimit: answer has an invalid enforce_from")
		}
		st.EnforceFrom = t.UTC()
	}
	st.UpgradeURL = sameHostHTTPS(a.UpgradeURL, c.Endpoint)
	return st, nil
}

// sameHostHTTPS returns raw when it is an https URL on the endpoint's host, and
// "" otherwise: the link is shown to administrators as "Increase plan", and a
// portal answer must not be able to point it anywhere else.
func sameHostHTTPS(raw, endpoint string) string {
	u, err := url.Parse(raw)
	e, eerr := url.Parse(endpoint)
	if raw == "" || err != nil || eerr != nil || u.Scheme != "https" || u.User != nil ||
		!strings.EqualFold(u.Host, e.Host) {
		return ""
	}
	return u.String()
}
