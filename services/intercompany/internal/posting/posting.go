// Package posting posts a derived intercompany journal to the ledgers of its
// entities (FR-40.5). Each entity's legs become one ledger entry, posted in the
// order the entities appear. When a later side is refused the sides already in
// a ledger are reversed, so no entity is left with half of a pair. Every ledger
// call carries an idempotency key built from the pair id, and the progress is
// stored after each call, so a retry with the same Idempotency-Key resumes
// where it stopped instead of posting twice.
package posting

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/BenjaminSRussell/passage/platform/ledgerclient"
	"github.com/BenjaminSRussell/passage/services/intercompany/internal/domain"
)

// SourceType is the source type the ledger records on the entries.
const SourceType = "intercompany"

// Store persists the journal and the progress of its posting.
type Store interface {
	BeginJournal(ctx context.Context, rec domain.JournalRecord, key, hash string) (domain.JournalRecord, bool, error)
	RecordEntry(ctx context.Context, journalID, entity, ledgerEntryID string, reversal bool) error
	FinishJournal(ctx context.Context, journalID string, status domain.PostingStatus, failureCode, failureDetail string) (domain.JournalRecord, error)
}

// Poster posts journals through a ledger.
type Poster struct {
	ledger ledgerclient.Ledger
	store  Store
}

// New returns a poster over a ledger and a journal store.
func New(l ledgerclient.Ledger, s Store) *Poster { return &Poster{ledger: l, store: s} }

// Input is one journal to post.
type Input struct {
	Source      string // domain.SourceJournal or domain.SourceCharge
	SourceRef   string
	Key         string // the Idempotency-Key
	Hash        string // identifies the request under the key
	Journal     domain.Journal
	Targets     map[string]domain.Target
	PostingDate time.Time
}

const dateLayout = "2006-01-02"

func ledgerDown(detail string) error {
	return &domain.Error{Code: domain.CodeLedgerUnavailable, Detail: detail}
}

// Post stores the journal and posts every entity's entry. A journal already
// stored under the key is returned as it stands unless it is still pending, in
// which case posting resumes. The returned record is valid with an error
// whenever the journal was stored: IC-0845 when a side was refused and the
// earlier sides were reversed, IC-0846 when the ledger could not be reached
// (the journal stays pending; retry with the same key).
func (p *Poster) Post(ctx context.Context, in Input) (domain.JournalRecord, bool, error) {
	entries, err := domain.EntryGroups(in.Journal, in.Targets)
	if err != nil {
		return domain.JournalRecord{}, false, err
	}
	for _, e := range entries {
		owner, err := p.ledger.LegalEntity(ctx, e.Target.LegalEntityID, e.Target.LedgerID)
		switch {
		case err == nil && owner != e.Target.LegalEntityID:
			return domain.JournalRecord{}, false, &domain.Error{Code: domain.CodeLedgerMismatch, Detail: fmt.Sprintf("ledger %s does not belong to the legal entity of %s", e.Target.LedgerID, e.Entity)}
		case err != nil && ledgerclient.Rejected(err):
			return domain.JournalRecord{}, false, &domain.Error{Code: domain.CodeLedgerMismatch, Detail: fmt.Sprintf("ledger %s of %s was not found for that legal entity", e.Target.LedgerID, e.Entity)}
		case err != nil:
			return domain.JournalRecord{}, false, ledgerDown("the ledger of " + e.Entity + " could not be reached")
		}
	}
	rec, replayed, err := p.store.BeginJournal(ctx, domain.JournalRecord{
		PairID: in.Journal.PairID, Source: in.Source, SourceRef: in.SourceRef, PostingDate: in.PostingDate, Entries: entries,
	}, in.Key, in.Hash)
	if err != nil {
		return domain.JournalRecord{}, false, err
	}
	if rec.Status != domain.PostingPending {
		return rec, replayed, nil
	}
	for i := range rec.Entries {
		e := &rec.Entries[i]
		if e.LedgerEntryID != "" {
			continue
		}
		id, err := p.ledger.Post(ctx, ledgerEntry(rec, *e, in.PostingDate))
		if err != nil {
			done, err := p.refused(ctx, rec, e.Entity, err)
			return done, replayed, err
		}
		if err := p.store.RecordEntry(ctx, rec.ID, e.Entity, id, false); err != nil {
			return rec, replayed, err
		}
		e.LedgerEntryID = id
	}
	done, err := p.store.FinishJournal(ctx, rec.ID, domain.PostingPosted, "", "")
	return done, replayed, err
}

func ledgerEntry(rec domain.JournalRecord, e domain.PostedEntry, date time.Time) ledgerclient.Entry {
	lines := make([]ledgerclient.Line, 0, len(e.Legs))
	for _, l := range e.Legs {
		lines = append(lines, ledgerclient.Line{Account: l.Account, Credit: l.Side == domain.Credit, Amount: l.Amount.Value,
			Text: fmt.Sprintf("Intercompany %s", rec.PairID)})
	}
	return ledgerclient.Entry{LegalEntityID: e.Target.LegalEntityID, LedgerID: e.Target.LedgerID, DocumentID: rec.PairID, PostingDate: date.Format(dateLayout),
		Currency: e.Currency, IdempotencyKey: "ic:" + rec.PairID + ":" + e.Entity, SourceType: SourceType, Lines: lines}
}

// refused handles a side the ledger did not take. A refusal that a retry
// cannot fix compensates the sides already posted; anything else leaves the
// journal pending so the same request can resume it.
func (p *Poster) refused(ctx context.Context, rec domain.JournalRecord, entity string, cause error) (domain.JournalRecord, error) {
	if !ledgerclient.Rejected(cause) {
		slog.WarnContext(ctx, "intercompany posting paused; the ledger did not answer", "journal", rec.ID, "entity", entity, "err", cause)
		return rec, ledgerDown("the ledger of " + entity + " did not answer; retry with the same Idempotency-Key to continue")
	}
	code := ledgerclient.CodeOf(cause)
	for i := range rec.Entries {
		e := &rec.Entries[i]
		if e.LedgerEntryID == "" || e.ReversalEntryID != "" {
			continue
		}
		id, err := p.ledger.Reverse(ctx, ledgerclient.Reversal{LegalEntityID: e.Target.LegalEntityID, EntryID: e.LedgerEntryID, PostingDate: rec.PostingDate.Format(dateLayout),
			Description: "Intercompany compensation " + rec.PairID, IdempotencyKey: "ic-rev:" + rec.PairID + ":" + e.Entity})
		if err != nil {
			slog.ErrorContext(ctx, "intercompany compensation failed; the journal stays pending", "journal", rec.ID, "entity", e.Entity, "err", err)
			return rec, ledgerDown("the first side could not be reversed; retry with the same Idempotency-Key to finish the compensation")
		}
		if err := p.store.RecordEntry(ctx, rec.ID, e.Entity, id, true); err != nil {
			return rec, err
		}
		e.ReversalEntryID = id
	}
	detail := fmt.Sprintf("the ledger of %s refused its side", entity)
	if code != "" {
		detail += " (" + code + ")"
	}
	done, err := p.store.FinishJournal(ctx, rec.ID, domain.PostingCompensated, domain.CodeSecondSideFailed, detail)
	if err != nil {
		return rec, errors.Join(err, &domain.Error{Code: domain.CodeSecondSideFailed, Detail: detail})
	}
	return done, &domain.Error{Code: domain.CodeSecondSideFailed, Detail: detail + "; the entries already posted were reversed"}
}
