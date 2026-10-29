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
	add := func(name string, key map[string]string, source, target decimal.Decimal) error {
		for i := range r.Totals {
			t := &r.Totals[i]
			if t.Name != name || !sameKey(t.Key, key) {
				continue
			}
			next, err := NewControlTotal(t.Name, t.Key, t.Source.Add(source), t.Target.Add(target), t.Tolerance)
			if err != nil {
				return err
			}
			*t = next
			return nil
		}
		t, err := NewControlTotal(name, key, source, target, Tolerance{})
		if err != nil {
			return err
		}
		r.Totals = append(r.Totals, t)
		return nil
	}
	type move struct {
		name           string
		key            map[string]string
		source, target decimal.Decimal
	}
	amt := rec.Amount
	byAccount := func(acct string) map[string]string {
		return map[string]string{"company": company, "account": acct, "currency": rec.Currency}
	}
	steps := []move{
		{"open_items.amount", map[string]string{"company": company, "currency": rec.Currency}, decimal.Zero, amt},
		{"open_items.count", map[string]string{"company": company}, decimal.Zero, decimal.NewFromInt(1)},
	}
	if rec.Account != original.Account {
		steps = append(steps,
			move{"open_items.amount_by_account", byAccount(original.Account), amt.Neg(), decimal.Zero},
			move{"open_items.amount_by_account", byAccount(rec.Account), amt, amt})
	} else {
		steps = append(steps, move{"open_items.amount_by_account", byAccount(rec.Account), decimal.Zero, amt})
	}
	for _, s := range steps {
		if err := add(s.name, s.key, s.source, s.target); err != nil {
			return Record{}, err
		}
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
