package domain

import (
	"bytes"
	"crypto/sha256"
	"encoding/base64"
	"encoding/csv"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"

	"github.com/BenjaminSRussell/passage/platform/ids"
)

// Audit trail kinds (PRD §2.15.4 AuditTrailEntry, FR-15.10). The ledger kinds
// come from ledger, approval, period, rate, tax, allocation and year-end events
// through PutAuditEntries; the reporting kinds are written by this service in
// the same transaction as the change they record.
const (
	AuditPosted         = "posted"
	AuditReplayed       = "replayed"
	AuditReversed       = "reversed"
	AuditBatchSubmitted = "batch_submitted"
	AuditBatchApproved  = "batch_approved"
	AuditBatchRejected  = "batch_rejected"
	AuditPeriodChanged  = "period_changed"
	AuditReopenApproved = "reopen_approved"
	AuditRateOverride   = "rate_override"
	AuditTaxFallback    = "tax_fallback"
	AuditAllocationRun  = "allocation_run"
	AuditYearEndRun     = "year_end_run"
	AuditRebuild        = "rebuild"

	AuditStatementRun        = "statement_run"
	AuditDefinitionPublished = "definition_published"
	AuditDefinitionRetired   = "definition_retired"
	AuditHierarchyPublished  = "hierarchy_published"
	AuditScheduleSaved       = "schedule_saved"
	AuditExported            = "audit_exported"
	AuditLegalHoldPlaced     = "legal_hold_placed"
	AuditLegalHoldReleased   = "legal_hold_released"
	AuditLibrarySwept        = "library_swept"
)

// AuditLedgerKinds are the PRD kinds, fed from other services' events.
var AuditLedgerKinds = []string{
	AuditPosted, AuditReplayed, AuditReversed, AuditBatchSubmitted, AuditBatchApproved, AuditBatchRejected,
	AuditPeriodChanged, AuditReopenApproved, AuditRateOverride, AuditTaxFallback, AuditAllocationRun,
	AuditYearEndRun, AuditRebuild,
}

// AuditReportingKinds are the kinds this service writes about its own changes.
var AuditReportingKinds = []string{
	AuditStatementRun, AuditDefinitionPublished, AuditDefinitionRetired, AuditHierarchyPublished,
	AuditScheduleSaved, AuditExported, AuditLegalHoldPlaced, AuditLegalHoldReleased, AuditLibrarySwept,
}

// CodeAuditInvalid is a slice-local code for an audit entry the trail refuses
// (unknown kind, missing actor or object, bad ledger or period); it is not in
// the PRD catalog yet.
const CodeAuditInvalid = "REPORT-0029"

// CodeAuditExportTooLarge is a slice-local code: the filter matches more
// entries than one export file holds, so the caller narrows it. An export is
// audit evidence and is never cut short silently.
const CodeAuditExportTooLarge = "REPORT-0030"

// Audit trail limits. The PRD enforces a narrowed date range on the query
// (§2.15.19): a filter names a period, or a from/to window of at most a year.
const (
	AuditDefaultPage = 100
	AuditMaxPage     = 1000
	AuditMaxExport   = 50000
	AuditMaxWindow   = 366 * 24 * time.Hour
)

// ErrAuditFilter reports an invalid audit trail filter; its message is user facing.
var ErrAuditFilter = errors.New("invalid audit trail filter")

var periodLabelPattern = regexp.MustCompile(`^[0-9]{4}-[0-9]{2,3}$`)

// ValidPeriodLabel reports whether s is a period label as balance keys and
// runs write it: the fiscal year, a dash and the period number (00 is the
// opening period), such as 2026-03.
func ValidPeriodLabel(s string) bool { return periodLabelPattern.MatchString(s) }

// ValidAuditKind reports whether k is an audit trail kind.
func ValidAuditKind(k string) bool {
	for _, set := range [][]string{AuditLedgerKinds, AuditReportingKinds} {
		for _, known := range set {
			if k == known {
				return true
			}
		}
	}
	return false
}

// AuditEntry is one row of the audit trail (AuditTrailEntry): when, which
// ledger and period (empty for changes to reporting objects), what kind of
// event, who did it, the object it concerns, its details and its trace.
// SourceEventID is the id of the event the entry was built from; the trail
// keeps one entry per source event, so a redelivered event is a replay.
type AuditEntry struct {
	ID            string
	OccurredAt    time.Time
	LedgerID      string
	LegalEntityID string
	Period        string
	Kind          string
	ActorID       string
	ObjectRef     string
	Details       map[string]any
	TraceID       string
	SourceEventID string
}

// Validate checks an entry before it enters the trail.
func (e AuditEntry) Validate() error {
	switch {
	case !ValidAuditKind(e.Kind):
		return errf(CodeAuditInvalid, "kind %q is not an audit trail kind", e.Kind)
	case e.OccurredAt.IsZero():
		return errf(CodeAuditInvalid, "occurred at is required")
	case strings.TrimSpace(e.ActorID) == "":
		return errf(CodeAuditInvalid, "actor is required")
	case strings.TrimSpace(e.ObjectRef) == "":
		return errf(CodeAuditInvalid, "object reference is required")
	case e.LedgerID != "" && !ids.Valid(e.LedgerID):
		return errf(CodeAuditInvalid, "ledger %q is not a UUID", e.LedgerID)
	case e.Period != "" && !ValidPeriodLabel(e.Period):
		return errf(CodeAuditInvalid, "period %q is not YYYY-PP", e.Period)
	case len(e.TraceID) > 128 || len(e.SourceEventID) > 128:
		return errf(CodeAuditInvalid, "trace id and source event id are at most 128 characters")
	}
	return nil
}

// AuditCursor is the keyset position of the last entry of a page, in report
// order (occurred at, id).
type AuditCursor struct {
	OccurredAt time.Time
	ID         string
}

// Encode renders the position as an opaque URL-safe cursor.
func (c AuditCursor) Encode() string {
	raw := c.OccurredAt.UTC().Format(time.RFC3339Nano) + "|" + c.ID
	return base64.RawURLEncoding.EncodeToString([]byte(raw))
}

// DecodeAuditCursor parses a cursor produced by Encode.
func DecodeAuditCursor(cursor string) (AuditCursor, error) {
	raw, err := base64.RawURLEncoding.DecodeString(cursor)
	if err != nil {
		return AuditCursor{}, ErrBadCursor
	}
	at, id, ok := strings.Cut(string(raw), "|")
	if !ok || !ids.Valid(id) {
		return AuditCursor{}, ErrBadCursor
	}
	t, err := time.Parse(time.RFC3339Nano, at)
	if err != nil {
		return AuditCursor{}, ErrBadCursor
	}
	return AuditCursor{OccurredAt: t, ID: id}, nil
}

// AuditFilter narrows the audit trail. Empty fields do not filter; From is
// inclusive and To exclusive.
type AuditFilter struct {
	LedgerID string
	Period   string
	Kind     string
	ActorID  string
	From     time.Time
	To       time.Time
}

// Validate checks the filter, including the narrowed range: a period, or both
// from and to no more than AuditMaxWindow apart.
func (f AuditFilter) Validate() error {
	if f.LedgerID != "" && !ids.Valid(f.LedgerID) {
		return fmt.Errorf("%w: ledger %q is not a UUID", ErrAuditFilter, f.LedgerID)
	}
	if f.Period != "" && !ValidPeriodLabel(f.Period) {
		return fmt.Errorf("%w: period %q must be the fiscal year and period number, such as 2026-03", ErrAuditFilter, f.Period)
	}
	if f.Kind != "" && !ValidAuditKind(f.Kind) {
		return fmt.Errorf("%w: unknown kind %q", ErrAuditFilter, f.Kind)
	}
	if !f.From.IsZero() && !f.To.IsZero() && !f.To.After(f.From) {
		return fmt.Errorf("%w: to must be after from", ErrAuditFilter)
	}
	if f.Period == "" {
		if f.From.IsZero() || f.To.IsZero() {
			return fmt.Errorf("%w: name a period, or both from and to", ErrAuditFilter)
		}
		if f.To.Sub(f.From) > AuditMaxWindow {
			return fmt.Errorf("%w: from and to may be at most 366 days apart", ErrAuditFilter)
		}
	}
	return nil
}

// AuditCSVHeader is the first line of an audit trail export.
var AuditCSVHeader = []string{"occurred_at", "kind", "ledger_id", "period", "actor_id", "object_ref", "details", "trace_id"}

// EncodeAuditCSV writes entries as CSV in the order given. Details are
// written as JSON with sorted keys, times in UTC with nanoseconds, and text a
// spreadsheet would read as a formula is prefixed with an apostrophe, so equal
// entries always give equal bytes and an equal digest.
func EncodeAuditCSV(entries []AuditEntry) ([]byte, error) {
	var buf bytes.Buffer
	w := csv.NewWriter(&buf)
	if err := w.Write(AuditCSVHeader); err != nil {
		return nil, err
	}
	for _, e := range entries {
		details := []byte("{}")
		if len(e.Details) > 0 {
			var err error
			if details, err = json.Marshal(e.Details); err != nil {
				return nil, err
			}
		}
		if err := w.Write([]string{
			e.OccurredAt.UTC().Format(time.RFC3339Nano), e.Kind, e.LedgerID, e.Period,
			safeText(e.ActorID), safeText(e.ObjectRef), safeText(string(details)), safeText(e.TraceID),
		}); err != nil {
			return nil, err
		}
	}
	w.Flush()
	if err := w.Error(); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

// AuditDigest returns the hex SHA-256 of an export body (FR-15.10,
// audit_trail.export_hash_algorithm = sha256).
func AuditDigest(body []byte) string {
	sum := sha256.Sum256(body)
	return hex.EncodeToString(sum[:])
}
