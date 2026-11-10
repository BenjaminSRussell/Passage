// Package events holds the event types and payloads the mobile service
// publishes (PRD 6.32.5). Schemas are in events/asyncapi.yaml. Payload fields
// are snake_case like the PRD.
package events

import "github.com/BenjaminSRussell/passage/platform/events"

const Context = "mobile"

const Source = "mobile"

var (
	FlowPublished   = events.Type(Context, "flow", "published", 1)
	SessionStarted  = events.Type(Context, "session", "started", 1)
	SessionEnded    = events.Type(Context, "session", "ended", 1)
	OfflineConflict = events.Type(Context, "offline", "conflict", 1)
	OfflineResolved = events.Type(Context, "offline", "resolved", 1)
)

func All() []string {
	return []string{FlowPublished, SessionStarted, SessionEnded, OfflineConflict, OfflineResolved}
}

type FlowPublishedData struct {
	FlowID     string   `json:"flow_id"`
	Code       string   `json:"code"`
	Version    int      `json:"version"`
	Warehouses []string `json:"warehouses"`
}

type SessionData struct {
	SessionID string `json:"session_id"`
	DeviceID  string `json:"device_id"`
	UserID    string `json:"user_id"`
	Warehouse string `json:"warehouse"`
	Reason    string `json:"reason,omitempty"`
}

type OfflineConflictData struct {
	IdempotencyKey string `json:"idempotency_key"`
	Command        string `json:"command"`
	Reason         string `json:"reason"`
}

type OfflineResolvedData struct {
	IdempotencyKey string `json:"idempotency_key"`
	Command        string `json:"command"`
	Action         string `json:"action"`
	ResolvedBy     string `json:"resolved_by"`
}
