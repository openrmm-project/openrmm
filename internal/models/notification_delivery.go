package models

// NotificationDeliveryContext is the immutable provenance of a queued message.
// It is deliberately separate from live agent membership and mutable alert rules.
// Version 1 supports the existing automatic recipient route; routing settings can
// extend this closed model without changing the meaning of already queued mail.
type NotificationDeliveryContext struct {
	Version         int                       `json:"version"`
	EventType       string                    `json:"event_type"`
	AgentID         string                    `json:"agent_id"`
	ClientID        string                    `json:"client_id,omitempty"`
	SiteID          string                    `json:"site_id,omitempty"`
	GroupID         string                    `json:"group_id,omitempty"`
	AlertID         string                    `json:"alert_id,omitempty"`
	NotificationSeq int64                     `json:"notification_seq,omitempty"`
	SourceType      string                    `json:"source_type,omitempty"`
	RuleID          string                    `json:"rule_id,omitempty"`
	Route           NotificationRouteSnapshot `json:"route"`
}

type NotificationRouteSnapshot struct {
	Mode string `json:"mode"`
}
