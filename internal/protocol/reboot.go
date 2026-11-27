package protocol

import (
	"strings"
	"unicode"
)

const (
	RebootRequired    = "required"
	RebootNotRequired = "not_required"
	RebootUnknown     = "unknown"
)

// RebootStatus is an OS observation, never an acknowledgement of a command.
// An absent heartbeat member comes from an older agent and changes nothing.
// Unknown deliberately differs from a conclusive not-required observation.
type RebootStatus struct {
	Status string `json:"status"`
	Reason string `json:"reason,omitempty"`
	BootID string `json:"boot_id,omitempty"`
}

func (r RebootStatus) Valid() bool {
	return (r.Status == RebootRequired || r.Status == RebootNotRequired || r.Status == RebootUnknown) && len(r.Reason) <= 2048 && len(r.BootID) <= 128
}

func (r RebootStatus) Clean() RebootStatus {
	r.Reason = strings.Map(func(c rune) rune {
		if unicode.IsControl(c) {
			return ' '
		}
		return c
	}, r.Reason)
	r.Reason = strings.Join(strings.Fields(r.Reason), " ")
	r.BootID = strings.Map(func(c rune) rune {
		if unicode.IsControl(c) {
			return -1
		}
		return c
	}, r.BootID)
	r.BootID = strings.Join(strings.Fields(r.BootID), "")
	return r
}
