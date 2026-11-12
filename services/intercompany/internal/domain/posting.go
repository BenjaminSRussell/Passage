package domain

import (
	"fmt"
	"time"
)

// Codes of the posting, charge and reconciliation records. They are not in the
// PRD catalog and are listed in NOT-IMPLEMENTED.md for the catalog owner.
const (
	CodePostingNotFound   = "IC-0844"
	CodeSecondSideFailed  = "IC-0845"
	CodeLedgerUnavailable = "IC-0846"
	CodeLedgerMismatch    = "IC-0847"
	CodeChargeNotFound    = "IC-0848"
	CodeReconRunNotFound  = "IC-0850"
	CodeReconItemNotFound = "IC-0851"
	CodeReconNotOpen      = "IC-0852"
)

// PostingStatus is where a posted intercompany journal stands.
type PostingStatus string

const (
	// PostingPending: some side is not in a ledger yet; a retry with the same key resumes it.
	PostingPending PostingStatus = "pending"
	// PostingPosted: every entity's entry is in its ledger.
	PostingPosted PostingStatus = "posted"
	// PostingCompensated: a later side was refused, so the earlier sides were reversed.
	PostingCompensated PostingStatus = "compensated"
)

// Source types of a posted journal.
const (
	SourceJournal = "journal"
	SourceCharge  = "charge"
)

// Target is the legal entity and ledger one entity code posts to.
type Target struct {
	LegalEntityID string
	LedgerID      string
}

// PostedEntry is the entry one entity's legs make in its own ledger.
type PostedEntry struct {
	Entity          string
	Target          Target
	Currency        string
	Legs            []Leg
	LedgerEntryID   string // empty until posted
	ReversalEntryID string // empty unless the entry was compensated
	// PostedOn is the date the ledger took the entry; ShiftedFrom is the date it was meant for when that period was closed
	// (zero when the entry went in on its own date).
	PostedOn    time.Time
	ShiftedFrom time.Time
}

// JournalRecord is a stored intercompany journal with the ledger entries it made.
type JournalRecord struct {
	ID            string
	PairID        string
	Source        string
	SourceRef     string
	PostingDate   time.Time
	Status        PostingStatus
	FailureCode   string
	FailureDetail string
	Entries       []PostedEntry
	CreatedBy     string
	CreatedAt     time.Time
}

// EntryGroups splits a derived journal into one entry per entity, in the order
// the entities first appear (the line's entity first, then the offset entity),
// each in a single currency. Every entity needs a target; a journal whose
// entity legs mix currencies is refused.
func EntryGroups(j Journal, targets map[string]Target) ([]PostedEntry, error) {
	var out []PostedEntry
	index := map[string]int{}
	for _, l := range j.Legs {
		i, ok := index[l.Entity]
		if !ok {
			t, found := targets[l.Entity]
			if !found || t.LegalEntityID == "" || t.LedgerID == "" {
				return nil, invalid(fmt.Sprintf("entity %s needs a legal entity and ledger to post to", l.Entity))
			}
			index[l.Entity] = len(out)
			i = len(out)
			out = append(out, PostedEntry{Entity: l.Entity, Target: t, Currency: l.Amount.Currency})
		}
		if out[i].Currency != l.Amount.Currency {
			return nil, invalid(fmt.Sprintf("entity %s has legs in %s and %s; an entry has one currency", l.Entity, out[i].Currency, l.Amount.Currency))
		}
		out[i].Legs = append(out[i].Legs, Leg{Entity: l.Entity, Account: l.Account, Side: l.Side, Amount: l.Amount.Round()})
	}
	return out, nil
}
