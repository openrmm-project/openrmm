package models

import (
	"fmt"
	"github.com/google/uuid"
	"sort"
)

const MaxNotificationRouteRecipients = 200

type NotificationRouteUpdate struct {
	Mode    string   `json:"mode"`
	UserIDs []string `json:"user_ids"`
}

func (r *NotificationRouteUpdate) Validate() error {
	if r.Mode != "inherit" && r.Mode != "selected" {
		return fmt.Errorf("mode must be inherit or selected")
	}
	if len(r.UserIDs) > MaxNotificationRouteRecipients {
		return fmt.Errorf("at most %d recipients", MaxNotificationRouteRecipients)
	}
	unique := map[string]bool{}
	for _, id := range r.UserIDs {
		parsed, err := uuid.Parse(id)
		if err != nil {
			return fmt.Errorf("invalid recipient ID")
		}
		unique[parsed.String()] = true
	}
	r.UserIDs = make([]string, 0, len(unique))
	for id := range unique {
		r.UserIDs = append(r.UserIDs, id)
	}
	sort.Strings(r.UserIDs)
	if r.Mode == "selected" && len(r.UserIDs) == 0 {
		return fmt.Errorf("selected mode requires recipients")
	}
	if r.Mode == "inherit" && len(r.UserIDs) != 0 {
		return fmt.Errorf("inherit mode cannot select recipients")
	}
	return nil
}

type NotificationRouteRecipient struct {
	ID       string `json:"id"`
	Username string `json:"username"`
	Email    string `json:"email"`
}

type NotificationRouteView struct {
	Mode             string                       `json:"mode"`
	Version          int64                        `json:"version"`
	UserIDs          []string                     `json:"user_ids"`
	Recipients       []NotificationRouteRecipient `json:"recipients"`
	UnavailableCount int                          `json:"unavailable_count"`
}

type EffectiveNotificationRouteView struct {
	Mode             string                       `json:"mode"`
	ScopeType        string                       `json:"scope_type,omitempty"`
	ScopeID          string                       `json:"scope_id,omitempty"`
	SourceName       string                       `json:"source_name,omitempty"`
	Version          int64                        `json:"version"`
	Recipients       []NotificationRouteRecipient `json:"recipients"`
	UnavailableCount int                          `json:"unavailable_count"`
}

type NotificationRoutingView struct {
	Local     NotificationRouteView          `json:"local"`
	Effective EffectiveNotificationRouteView `json:"effective"`
}
