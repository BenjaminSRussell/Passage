package domain

import (
	"fmt"
	"strings"

	"github.com/shopspring/decimal"
)

// Quarantine row states (FR-49.11).
const (
	QuarantineOpen     = "OPEN"
	QuarantineReleased = "RELEASED"
)

// Correction is an operator's fix to a quarantined open item. Only the party and the control
// account are correctable; a blank field keeps the extracted value. Amount, currency and legacy
// id are the legacy truth and change only by re-extract, so the control totals keep comparing
// against the source (FR-49.2).
type Correction struct {
	Party   string
	Account string
}

// IsZero reports whether the correction changes nothing (blank or whitespace-only fields).
func (c Correction) IsZero() bool {
	return strings.TrimSpace(c.Party) == "" && strings.TrimSpace(c.Account) == ""
}

// Apply returns the record with the correction applied.
func (c Correction) Apply(r Record) Record {
	if p := strings.TrimSpace(c.Party); p != "" {
		r.Party = p
	}
	if a := strings.TrimSpace(c.Account); a != "" {
		r.Account = a
	}
	return r
}

// ReleaseOpenItem re-runs the open-items suite on a corrected quarantined record and, when it
// passes, books the record into the run as loaded: the run's control totals gain the record on
// the Passage side, the counts move from quarantined to loaded, and the run moves to RECONCILED
// when no total is MISMATCH any more. parties is the master data to check the party against.
//
// A corrected control account moves the record's legacy amount from the extracted account's
// subtotal to the corrected one, as if the extract had mapped it there; the correction itself is
// kept with the quarantine row. It returns the accepted record, or ErrInvalid naming the rule
// that still fails, or ErrTransition when the run is not LOADED.
func ReleaseOpenItem(r *PackRun, original Record, fix Correction, parties map[string]struct{}) (Record, error) {
	if r.Pack != PackOpenItems {
		return Record{}, fmt.Errorf("%w: pack %s has no record release; only open_items does", ErrUnknownPack, r.Pack)
	}
	if r.Status != StatusLoaded {
		return Record{}, fmt.Errorf("%w: a record can be released only while the run is LOADED, run is %s", ErrTransition, r.Status)
	}
	corrected := fix.Apply(original)
	accepted, quarantined := Validate([]Record{corrected}, parties)
	if len(quarantined) > 0 {
		q := quarantined[0]
		return Record{}, fmt.Errorf("%w: %s rule still fails: %s", ErrInvalid, q.Rule, q.Reason)
	}
	rec := accepted[0]
	company := r.SourceCompany
	amt := rec.Amount
	byAccount := func(acct string) map[string]string {
		return map[string]string{"company": company, "account": acct, "currency": rec.Currency}
	}
	steps := []totalMove{
		{"open_items.amount", map[string]string{"company": company, "currency": rec.Currency}, decimal.Zero, amt},
		{"open_items.count", map[string]string{"company": company}, decimal.Zero, decimal.NewFromInt(1)},
	}
	if rec.Account != original.Account {
		steps = append(steps,
			totalMove{"open_items.amount_by_account", byAccount(original.Account), amt.Neg(), decimal.Zero},
			totalMove{"open_items.amount_by_account", byAccount(rec.Account), amt, amt})
	} else {
		steps = append(steps, totalMove{"open_items.amount_by_account", byAccount(rec.Account), decimal.Zero, amt})
	}
	if err := applyMoves(r, steps); err != nil {
		return Record{}, err
	}
	r.RecordsLoaded++
	r.RecordsQuarantined--
	if !r.HasMismatch() {
		if err := r.Advance(StatusReconciled); err != nil {
			return Record{}, err
		}
	}
	return rec, nil
}

func sameKey(a, b map[string]string) bool {
	if len(a) != len(b) {
		return false
	}
	for k, v := range a {
		if bv, ok := b[k]; !ok || bv != v {
			return false
		}
	}
	return true
}

// totalMove adds to the legacy (source) and Passage (target) sides of one control total, creating
// the total when the run has none for the key.
type totalMove struct {
	name           string
	key            map[string]string
	source, target decimal.Decimal
}

// applyMoves books the moves into the run's totals. A move of zero on both sides is skipped, so a
// correction that changes nothing for a total does not create it.
func applyMoves(r *PackRun, moves []totalMove) error {
	for _, m := range moves {
		if m.source.IsZero() && m.target.IsZero() {
			continue
		}
		found := false
		for i := range r.Totals {
			t := &r.Totals[i]
			if t.Name != m.name || !sameKey(t.Key, m.key) {
				continue
			}
			next, err := NewControlTotal(t.Name, t.Key, t.Source.Add(m.source), t.Target.Add(m.target), t.Tolerance)
			if err != nil {
				return err
			}
			*t = next
			found = true
			break
		}
		if found {
			continue
		}
		t, err := NewControlTotal(m.name, m.key, m.source, m.target, Tolerance{})
		if err != nil {
			return err
		}
		r.Totals = append(r.Totals, t)
	}
	return nil
}
