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
	RecordEntry(ctx context.Context, journalID, entity, ledgerEntryID string, reversal bool, postedOn, shiftedFrom time.Time) error
	FinishJournal(ctx context.Context, journalID string, status domain.PostingStatus, failureCode, failureDetail string) (domain.JournalRecord, error)
}

// NextPeriod returns the first day of the period after the one that holds date, for the legal entity. The poster asks it when a
// ledger refuses a date because its period is closed.
type NextPeriod func(ctx context.Context, legalEntityID string, date time.Time) (time.Time, error)

// MaxShifts bounds how many periods a posting moves forward before the closed period is reported as a refusal.
const MaxShifts = 12

// Poster posts journals through a ledger.
type Poster struct {
	ledger ledgerclient.Ledger
	store  Store
	next   NextPeriod
}

// New returns a poster over a ledger and a journal store. A side whose period is closed is posted on the first day of the next
// calendar month, and on the next one when that is closed too; WithNextPeriod swaps in the fiscal calendar.
func New(l ledgerclient.Ledger, s Store) *Poster {
	return &Poster{ledger: l, store: s, next: nextMonth}
}

// WithNextPeriod makes the poster find the first open period with the fiscal calendar instead of calendar months.
func (p *Poster) WithNextPeriod(f NextPeriod) *Poster {
	if f != nil {
		p.next = f
	}
	return p
}

func nextMonth(_ context.Context, _ string, date time.Time) (time.Time, error) {
	return time.Date(date.Year(), date.Month()+1, 1, 0, 0, 0, 0, time.UTC), nil
}

func periodClosed(err error) bool {
	return ledgerclient.Rejected(err) && ledgerclient.CodeOf(err) == ledgerclient.CodePeriodClosed
}

// PostShifting posts the entry build(date) makes for a legal entity. Only close may post into a closed period, so a ledger that
// refuses the date because its period is closed is asked again for the first day of the next period, and so on up to MaxShifts
// periods; build is told the date so the entry can say which date it was meant for. The idempotency key must be the same for every
// date, so an answer lost after a post is found again by the retry. It returns the date the ledger took.
func (p *Poster) PostShifting(ctx context.Context, legalEntityID string, wanted time.Time, build func(date time.Time) ledgerclient.Entry) (id string, on time.Time, err error) {
	date := wanted
	for shifts := 0; ; shifts++ {
		id, err = p.ledger.Post(ctx, build(date))
		if err == nil || !periodClosed(err) || shifts >= MaxShifts {
			return id, date, err
		}
		next, nerr := p.next(ctx, legalEntityID, date)
		if nerr != nil || !next.After(date) {
			slog.WarnContext(ctx, "intercompany cannot find the next period of a closed one", "legalEntityId", legalEntityID, "err", nerr)
			return "", date, err
		}
		date = next
	}
}

func (p *Poster) postSide(ctx context.Context, rec domain.JournalRecord, e domain.PostedEntry, wanted time.Time) (string, time.Time, error) {
	return p.PostShifting(ctx, e.Target.LegalEntityID, wanted, func(date time.Time) ledgerclient.Entry { return ledgerEntry(rec, e, date, wanted) })
}

// reverseSide reverses one entity's entry on the date it was meant for, moving forward past closed periods the same way.
func (p *Poster) reverseSide(ctx context.Context, rec domain.JournalRecord, e domain.PostedEntry) (id string, on time.Time, err error) {
	wanted := rec.PostingDate
	if !e.PostedOn.IsZero() {
		wanted = e.PostedOn
	}
	date := wanted
	for shifts := 0; ; shifts++ {
		id, err = p.ledger.Reverse(ctx, ledgerclient.Reversal{LegalEntityID: e.Target.LegalEntityID, EntryID: e.LedgerEntryID, PostingDate: date.Format(dateLayout),
			Description: "Intercompany compensation " + rec.PairID, IdempotencyKey: "ic-rev:" + rec.PairID + ":" + e.Entity})
		if err == nil || !periodClosed(err) || shifts >= MaxShifts {
			return id, date, err
		}
		next, nerr := p.next(ctx, e.Target.LegalEntityID, date)
		if nerr != nil || !next.After(date) {
			return "", date, err
		}
		date = next
	}
}

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
		id, on, err := p.postSide(ctx, rec, *e, in.PostingDate)
		if err != nil {
			done, err := p.refused(ctx, rec, e.Entity, err)
			return done, replayed, err
		}
		shifted := time.Time{}
		if !on.Equal(in.PostingDate) {
			shifted = in.PostingDate
		}
		if err := p.store.RecordEntry(ctx, rec.ID, e.Entity, id, false, on, shifted); err != nil {
			return rec, replayed, err
		}
		e.LedgerEntryID, e.PostedOn, e.ShiftedFrom = id, on, shifted
	}
	done, err := p.store.FinishJournal(ctx, rec.ID, domain.PostingPosted, "", "")
	return done, replayed, err
}

// ShiftNote is the text of a line posted after its period closed: the pair, the date it was meant for and the date it took.
func ShiftNote(pairID string, original, posted time.Time) string {
	if original.Equal(posted) {
		return fmt.Sprintf("Intercompany %s", pairID)
	}
	return fmt.Sprintf("Intercompany %s (dated %s, posted %s because that period is closed)", pairID, original.Format(dateLayout), posted.Format(dateLayout))
}

func ledgerEntry(rec domain.JournalRecord, e domain.PostedEntry, date, original time.Time) ledgerclient.Entry {
	lines := make([]ledgerclient.Line, 0, len(e.Legs))
	for _, l := range e.Legs {
		lines = append(lines, ledgerclient.Line{Account: l.Account, Credit: l.Side == domain.Credit, Amount: l.Amount.Value, Text: ShiftNote(rec.PairID, original, date)})
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
		id, on, err := p.reverseSide(ctx, rec, *e)
		if err != nil {
			slog.ErrorContext(ctx, "intercompany compensation failed; the journal stays pending", "journal", rec.ID, "entity", e.Entity, "err", err)
			return rec, ledgerDown("the first side could not be reversed; retry with the same Idempotency-Key to finish the compensation")
		}
		if err := p.store.RecordEntry(ctx, rec.ID, e.Entity, id, true, on, time.Time{}); err != nil {
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
