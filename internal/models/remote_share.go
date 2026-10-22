package models

import (
	"strings"
	"time"
)

// RemoteShare is a one-time invitation to a device's screen for someone who has
// no account (docs/08-remote-access.md). The token, the code and the guest's
// credential are never stored or returned in plaintext after creation.
type RemoteShare struct {
	ID             string     `json:"id"`
	AgentID        string     `json:"agent_id"`
	CreatedBy      string     `json:"created_by"`
	Mode           string     `json:"mode"`
	Status         string     `json:"status"`
	FailedAttempts int        `json:"failed_attempts"`
	GuestName      string     `json:"guest_name"`
	GuestIP        string     `json:"guest_ip"`
	GuestUserAgent string     `json:"guest_user_agent"`
	CreatedAt      time.Time  `json:"created_at"`
	ExpiresAt      time.Time  `json:"expires_at"`
	RequestedAt    *time.Time `json:"requested_at,omitempty"`
	DecidedAt      *time.Time `json:"decided_at,omitempty"`
	EndedAt        *time.Time `json:"ended_at,omitempty"`
}

// What a guest may do. View only passes keyframe requests and the monitor
// picker to the endpoint; control adds pointer and keyboard input.
const (
	RemoteShareView    = "view"
	RemoteShareControl = "control"
)

// The life of an invitation. A share past its expiry is expired whatever its
// stored status says (Live), so "expired" is never written.
const (
	RemoteShareOpen     = "open"     // waiting for the guest's code
	RemoteSharePending  = "pending"  // code accepted; waiting for the technician
	RemoteShareAccepted = "accepted" // the technician let the guest in
	RemoteShareRejected = "rejected" // the technician turned the guest away
	RemoteShareRevoked  = "revoked"  // withdrawn before the guest got in
	RemoteShareEnded    = "ended"    // the guest was in, and was removed
	RemoteShareExpired  = "expired"  // reported only, never stored
)

// Live reports whether the share can still lead to a session.
func (s *RemoteShare) Live(now time.Time) bool {
	if s == nil || !now.Before(s.ExpiresAt) {
		return false
	}
	switch s.Status {
	case RemoteShareOpen, RemoteSharePending, RemoteShareAccepted:
		return true
	}
	return false
}

// EffectiveStatus is the status a reader should act on: an open, pending or
// accepted share past its expiry has expired.
func (s *RemoteShare) EffectiveStatus(now time.Time) string {
	switch s.Status {
	case RemoteShareOpen, RemoteSharePending, RemoteShareAccepted:
		if !now.Before(s.ExpiresAt) {
			return RemoteShareExpired
		}
	}
	return s.Status
}

// GuestPrincipal is the remote-session owner recorded for a guest's session.
// It cannot collide with a user id (a UUID), the admin token ("admin") or an
// AI principal ("ai:…").
func GuestPrincipal(shareID string) string { return "guest:" + shareID }

// GuestShareID is GuestPrincipal's inverse: the share behind a session owner,
// and whether the owner is a guest at all.
func GuestShareID(principal string) (string, bool) {
	id, ok := strings.CutPrefix(principal, "guest:")
	return id, ok && id != ""
}
