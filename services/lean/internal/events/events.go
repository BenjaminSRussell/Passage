// Package events holds the event types and payloads the lean service publishes
// through its outbox. Types follow the kernel rule
// dev.passage.<context>.<entity>.<verb>.v<n>. PRD 4.24.5 names the card
// transition passage.lean.kanban.state-changed.v1; the kernel rule allows no
// hyphen, so the verb is "statechanged".
package events

// Source is the CloudEvents source of every event this service writes.
const Source = "lean"

// Event types.
const (
	FlowActivated      = "dev.passage.lean.flow.activated.v1"
	KanbanCreated      = "dev.passage.lean.kanban.created.v1"
	KanbanStateChanged = "dev.passage.lean.kanban.statechanged.v1"
)

// All lists every event type this service publishes.
func All() []string { return []string{FlowActivated, KanbanCreated, KanbanStateChanged} }

// FlowActivatedData is the payload of FlowActivated.
type FlowActivatedData struct {
	FlowID        string `json:"flowId"`
	ValueStreamID string `json:"valueStreamId"`
	SiteID        string `json:"siteId"`
	Name          string `json:"name"`
	ActiveFrom    string `json:"activeFrom"`
	Activities    int    `json:"activities"`
}

// KanbanCreatedData is the payload of KanbanCreated (PRD 4.24.5: kanbanId,
// ruleId, qty, eventRefType, eventRefId, due).
type KanbanCreatedData struct {
	KanbanID     string  `json:"kanbanId"`
	RuleID       string  `json:"ruleId"`
	CardNumber   string  `json:"cardNumber"`
	ItemID       string  `json:"itemId"`
	RuleType     string  `json:"ruleType"`
	Qty          string  `json:"qty"`
	State        string  `json:"state"`
	EventRefType *string `json:"eventRefType"`
	EventRefID   *string `json:"eventRefId"`
	Due          *string `json:"due"`
}

// KanbanStateChangedData is the payload of KanbanStateChanged (PRD 4.24.5:
// kanbanId, from, to, qty). A manufacturing card reaching completed is the
// trigger for the production order's report as finished (wave 21); a
// withdrawal card reaching completed triggers the inventory transfer (wave 16).
type KanbanStateChangedData struct {
	KanbanID   string `json:"kanbanId"`
	RuleID     string `json:"ruleId"`
	CardNumber string `json:"cardNumber"`
	ItemID     string `json:"itemId"`
	RuleType   string `json:"ruleType"`
	From       string `json:"from"`
	To         string `json:"to"`
	Qty        string `json:"qty"`
	Version    int    `json:"version"`
}
