// Package consumer folds ledger events into the reporting read model (FR-15.1).
// The handlers take a decoded CloudEvent and run under the platform eventing
// consumer (platform/go/eventing), which puts the consumer's service principal
// in the context; wiring them to the Kafka topic is a later slice
// (NOT-IMPLEMENTED.md).
package consumer

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"regexp"

	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/platform/eventing"
	"github.com/BenjaminSRussell/passage/platform/events"
	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/platform/tenant"

	"github.com/BenjaminSRussell/passage/services/finreport/internal/domain"
)

const (
	// Name is the consumer's service principal and Kafka group. It acts as
	// tenant.ServiceActor(Name), scoped to the legal entity of each entry.
	Name = "finreport-ledger"
	// dutyEntryLookup is the service-only ledger duty the consumer reads an
	// entry's lines with (GET /v1/ledger/journal-lines); its service principal
	// holds exactly this duty.
	dutyEntryLookup = "ledger.entry.lookup"
	// LedgerSource is the CloudEvents source the ledger writes on its events.
	LedgerSource = "/passage/ledger/ledger"
)

var (
	JournalPostedType = events.Type("ledger", "journal", "posted", 1)
	// ErrInvalidEvent marks an event that can never be applied; it is wrapped
	// with eventing.Permanent so the consumer dead-letters it at once.
	ErrInvalidEvent = errors.New("invalid journal.posted event")
	// ErrLinesNotReady marks lines the ledger has not (yet) returned in full; retry later.
	ErrLinesNotReady = errors.New("ledger lines do not match the entry")
)

var periodKey = regexp.MustCompile(`^[0-9]{4}-(0[1-9]|1[0-2])$`)

// LineSource reads the posted lines of one entry from the ledger (the journal
// lines inquiry, FR-11.14, filtered by ledger and documentId, service duty
// ledger.entry.lookup). The event carries totals only, so the lines are
// fetched. A reversal shares its original's documentId, so the source must
// return only the lines of entryID, every page of them. The context carries
// the consumer's principal scoped to the entry's legal entity, which the
// service token needs.
type LineSource interface {
	EntryLines(ctx context.Context, ledgerID, documentID, entryID string) ([]domain.SourceLine, error)
}

// Applier is the read model's write side (store.Postgres).
type Applier interface {
	ApplyEntry(ctx context.Context, e domain.PostedEntry) (int, error)
}

type journalPosted struct {
	EntryID         string `json:"entryId"`
	LedgerID        string `json:"ledgerId"`
	LegalEntityID   string `json:"legalEntityId"`
	DocumentID      string `json:"documentId"`
	Voucher         string `json:"voucher"`
	PostingSequence int64  `json:"postingSequence"`
	PostingDate     string `json:"postingDate"`
	PeriodKey       string `json:"periodKey"`
	LineCount       int    `json:"lineCount"`
	Totals          struct {
		Debit  string `json:"debit"`
		Credit string `json:"credit"`
	} `json:"totals"`
	Source struct {
		System string `json:"system"`
		Type   string `json:"type"`
	} `json:"source"`
}

func invalid(format string, args ...any) error {
	return eventing.Permanent(fmt.Errorf("%w: %s", ErrInvalidEvent, fmt.Sprintf(format, args...)))
}

// HandleJournalPosted applies one dev.passage.ledger.journal.posted.v1 event.
// A reversal is announced as journal.posted too (its lines carry
// reversalOfEntryId), so reversals need no handler of their own. A replay, and
// an entry that arrives before an earlier one of its ledger, return nil (the
// store keeps the early entry until the gap closes). Malformed events and
// refusals of the read model are eventing.Permanent; a ledger or database
// failure is returned as is for the consumer to retry.
func HandleJournalPosted(ctx context.Context, ap Applier, src LineSource, evt events.Event) error {
	actor, ok := tenant.FromContext(ctx)
	if !ok || actor.UserID != tenant.ServiceActor(Name) || !actor.HasDuty(dutyEntryLookup) {
		return eventing.Permanent(errors.New("journal.posted consumer runs only as service principal " + tenant.ServiceActor(Name)))
	}
	if evt.Type != JournalPostedType || evt.Source != LedgerSource {
		return invalid("type %q from source %q", evt.Type, evt.Source)
	}
	var d journalPosted
	if err := json.Unmarshal(evt.Data, &d); err != nil {
		return invalid("%v", err)
	}
	for field, id := range map[string]string{"entryId": d.EntryID, "ledgerId": d.LedgerID, "documentId": d.DocumentID} {
		if !ids.Valid(id) {
			return invalid("%s %q is not a uuid", field, id)
		}
	}
	tid, ok := ids.Canonical(evt.TenantID)
	if !ok || tid != actor.TenantID {
		return invalid("tenant %q is not the consumer's tenant", evt.TenantID)
	}
	// The ledger's legal entity scopes everything the consumer does: the rows it
	// writes and the service token for the ledger. journal.posted does not carry
	// it yet (handoff to wave 11, NOT-IMPLEMENTED.md); the payload field and the
	// CloudEvent extension (copied from it) are both read.
	entity := d.LegalEntityID
	if entity == "" {
		entity = evt.LegalEntityID
	}
	if !ids.Valid(entity) {
		return invalid("entry %s names no legal entity", d.EntryID)
	}
	if d.PostingSequence < 1 || d.LineCount < 1 {
		return invalid("postingSequence %d, lineCount %d", d.PostingSequence, d.LineCount)
	}
	if !periodKey.MatchString(d.PeriodKey) {
		return invalid("period key %q is not YYYY-MM", d.PeriodKey)
	}
	for field, v := range map[string]string{"debit": d.Totals.Debit, "credit": d.Totals.Credit} {
		if _, err := decimal.NewFromString(v); v != "" && err != nil {
			return invalid("totals.%s %q", field, v)
		}
	}
	scoped := actor
	scoped.LegalEntityID, _ = ids.Canonical(entity)
	ctx = tenant.With(ctx, scoped)
	lines, err := src.EntryLines(ctx, d.LedgerID, d.DocumentID, d.EntryID)
	if err != nil {
		return fmt.Errorf("read lines of entry %s: %w", d.EntryID, err)
	}
	if len(lines) != d.LineCount {
		return fmt.Errorf("%w: entry %s has %d lines, the event says %d", ErrLinesNotReady, d.EntryID, len(lines), d.LineCount)
	}
	reversalOf := lines[0].ReversalOfEntryID
	for _, l := range lines[1:] {
		if l.ReversalOfEntryID != reversalOf {
			return invalid("lines of entry %s reverse different entries", d.EntryID)
		}
	}
	_, err = ap.ApplyEntry(ctx, domain.PostedEntry{
		LedgerID: d.LedgerID, LegalEntityID: scoped.LegalEntityID, EntryID: d.EntryID, PostingSequence: d.PostingSequence,
		PostingDate: d.PostingDate, PeriodKey: d.PeriodKey, Voucher: d.Voucher, SourceSystem: d.Source.System, SourceType: d.Source.Type,
		ReversalOfEntryID: reversalOf, Lines: lines,
	})
	return classify(err)
}

// classify marks the read model's refusals (a domain error or a 4xx problem)
// as permanent: retrying the same entry cannot change the answer.
func classify(err error) error {
	if err == nil {
		return nil
	}
	var de *domain.Error
	var p *httpx.Problem
	if errors.As(err, &de) || (errors.As(err, &p) && p.Status >= http.StatusBadRequest && p.Status < http.StatusInternalServerError) {
		return eventing.Permanent(err)
	}
	return err
}
