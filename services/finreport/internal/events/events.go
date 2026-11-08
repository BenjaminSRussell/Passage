package events

import "github.com/BenjaminSRussell/passage/platform/events"

// Context is the bounded context name used in event types (PRD §2.15.5).
const Context = "reporting"

var (
	StatementGenerated  = events.Type(Context, "statement", "generated", 1)
	DefinitionPublished = events.Type(Context, "definition", "published", 1)
	HierarchyUnassigned = events.Type(Context, "hierarchy", "unassigned", 1)
	BalanceVerified     = events.Type(Context, "balance", "verified", 1)
	LagExceeded         = events.Type(Context, "lag", "exceeded", 1)
	StatementFailed     = events.Type(Context, "statement", "failed", 1)
	DistributionSent    = events.Type(Context, "distribution", "sent", 1)
	LegalHoldPlaced     = events.Type(Context, "legalhold", "placed", 1)
	LegalHoldReleased   = events.Type(Context, "legalhold", "released", 1)
	LibrarySwept        = events.Type(Context, "library", "swept", 1)
)

// All lists every event type this service publishes.
func All() []string {
	return []string{StatementGenerated, DefinitionPublished, HierarchyUnassigned, BalanceVerified, LagExceeded, StatementFailed, DistributionSent,
		LegalHoldPlaced, LegalHoldReleased, LibrarySwept}
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

// BalanceVerifiedData is the payload of balance.verified.v1: the reporting read
// model of a ledger was compared with the ledger's trial balance (FR-15.1).
// Status is matched, differs or inconclusive (the high-water mark moved
// during the check, or entries waited ahead of it); DiffCount is the number of
// accounts that differ. legal_entity_id (the ledger's owner) also becomes the
// event's legalentityid extension. Account amounts are not in the event: they
// are ledger balances, served only to callers that may read the ledger.
type BalanceVerifiedData struct {
	VerificationID   string `json:"verification_id"`
	LedgerID         string `json:"ledger_id"`
	LegalEntityID    string `json:"legal_entity_id,omitempty"`
	Status           string `json:"status"`
	HWMBefore        int64  `json:"hwm_before"`
	HWMAfter         int64  `json:"hwm_after"`
	EntriesPending   int    `json:"entries_pending"`
	AccountsCompared int    `json:"accounts_compared"`
	DiffCount        int    `json:"diff_count"`
	Actor            string `json:"actor"`
}

// LagExceededData is the payload of lag.exceeded.v1 (PRD §2.15.5): the read
// model of a ledger stayed behind by more than the lag gate for the alert
// window. HWMLedger is the highest posting sequence the reporting service has
// seen for the ledger (applied, or received and waiting for an earlier entry);
// HWMReporting is the sequence it has applied.
//
// legal_entity_id (the ledger's owner) also becomes the event's legalentityid
// extension, so a close board scoped to an entity sees its own ledgers only.
type LagExceededData struct {
	LedgerID      string `json:"ledger_id"`
	LegalEntityID string `json:"legal_entity_id,omitempty"`
	LagSeconds    int64  `json:"lag_seconds"`
	HWMLedger     int64  `json:"hwm_ledger"`
	HWMReporting  int64  `json:"hwm_reporting"`
}

// StatementFailedData is the payload of statement.failed.v1 (PRD §2.15.5): a
// scheduled firing could not produce its statement. Stage is where it stopped
// (run, render or store); Code and Detail are the problem the last attempt
// ended with. RunID is empty when the run itself failed. legal_entity_id (the
// ledger's owner, when known) also becomes the event's legalentityid extension.
type StatementFailedData struct {
	ScheduleCode   string `json:"schedule_code"`
	DefinitionCode string `json:"definition_code"`
	LedgerID       string `json:"ledger_id"`
	LegalEntityID  string `json:"legal_entity_id,omitempty"`
	Period         string `json:"period"`
	FireTime       string `json:"fire_time"`
	RunID          string `json:"run_id,omitempty"`
	Stage          string `json:"stage"`
	Code           string `json:"code"`
	Error          string `json:"error"`
	Attempts       int    `json:"attempts"`
}

// DistributionSentData is one rendered output of a scheduled run handed to the
// distribution channel (PRD §2.15.5: run_id, recipients hashed, format, size).
// Delivery is "attachment" when the file fits the schedule's attachment limit
// and "link" when it is above it (the notification service sends the library
// link, FR-15.8). The recipients are the schedule's: the notification service
// resolves them from schedule_code, the event carries only their hash.
type DistributionSentData struct {
	ScheduleCode     string `json:"schedule_code"`
	RunID            string `json:"run_id"`
	LibraryID        string `json:"library_id"`
	LedgerID         string `json:"ledger_id"`
	LegalEntityID    string `json:"legal_entity_id,omitempty"`
	DefinitionCode   string `json:"definition_code"`
	Period           string `json:"period"`
	Format           string `json:"format"`
	Size             int64  `json:"size"`
	SHA256           string `json:"sha256"`
	Delivery         string `json:"delivery"`
	RecipientCount   int    `json:"recipient_count"`
	RecipientsSHA256 string `json:"recipients_sha256"`
}

// LegalHoldData is the payload of legalhold.placed.v1 and legalhold.released.v1
// (FR-15.8, PRD §2.15.15): the report library of a legal entity was put under
// legal hold, or released from it. While the hold is active the retention sweep
// deletes none of the entity's files. legal_entity_id also becomes the event's
// legalentityid extension. Note is the release note (released events only).
type LegalHoldData struct {
	HoldID        string `json:"hold_id"`
	LegalEntityID string `json:"legal_entity_id"`
	Reason        string `json:"reason"`
	Actor         string `json:"actor"`
	Note          string `json:"note,omitempty"`
}

// LibrarySweptData is the payload of library.swept.v1: the retention sweep
// deleted the report library files of one legal entity that were past their
// retention date. HeldCount is the number of past-retention files a legal hold
// kept (HoldID names it); a pass that deleted and held nothing writes no event.
// legal_entity_id also becomes the event's legalentityid extension.
type LibrarySweptData struct {
	SweepID       string `json:"sweep_id"`
	LegalEntityID string `json:"legal_entity_id"`
	Cutoff        string `json:"cutoff"`
	DeletedCount  int    `json:"deleted_count"`
	DeletedBytes  int64  `json:"deleted_bytes"`
	HeldCount     int    `json:"held_count"`
	HoldID        string `json:"hold_id,omitempty"`
	Actor         string `json:"actor"`
}
