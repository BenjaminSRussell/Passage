package events

import "github.com/BenjaminSRussell/passage/platform/events"

// Context is the bounded context name used in event types (PRD §2.15.5).
const Context = "reporting"

var (
	StatementGenerated  = events.Type(Context, "statement", "generated", 1)
	DefinitionPublished = events.Type(Context, "definition", "published", 1)
	HierarchyUnassigned = events.Type(Context, "hierarchy", "unassigned", 1)
)

// All lists every event type this service publishes.
func All() []string {
	return []string{StatementGenerated, DefinitionPublished, HierarchyUnassigned}
}

// HierarchyUnassignedData is the payload of hierarchy.unassigned.v1: a member
// of a published non-strict hierarchy matched no leaf (FR-15.13, BR-15.13).
// OwnerUserID is the hierarchy owner the notification goes to.
type HierarchyUnassignedData struct {
	Code        string `json:"code"`
	Version     int    `json:"version"`
	MemberID    string `json:"member_id"`
	MemberKind  string `json:"member_kind"`
	OwnerUserID string `json:"owner_user_id"`
	Actor       string `json:"actor"`
}

// OutputRef describes a rendered output file. Slice 1 renders no files, so
// the list in a statement event is empty.
type OutputRef struct {
	Format string `json:"format"`
	URI    string `json:"uri"`
	Size   int64  `json:"size"`
}

// StatementGeneratedData is the payload of statement.generated.v1.
type StatementGeneratedData struct {
	RunID             string            `json:"run_id"`
	DefinitionCode    string            `json:"definition_code"`
	DefinitionVersion int               `json:"definition_version"`
	LedgerID          string            `json:"ledger_id"`
	Period            string            `json:"period"`
	CurrencyType      string            `json:"currency_type"`
	HWMByLedger       map[string]int64  `json:"hwm_by_ledger"`
	ResultHash        string            `json:"result_hash"`
	Outputs           []OutputRef       `json:"outputs"`
	Parameters        map[string]string `json:"parameters"`
}

// DefinitionPublishedData is the payload of definition.published.v1.
type DefinitionPublishedData struct {
	Code        string `json:"code"`
	Version     int    `json:"version"`
	Actor       string `json:"actor"`
	DiffSummary string `json:"diff_summary"`
}
