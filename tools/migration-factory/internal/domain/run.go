package domain

import (
	"fmt"
	"sort"
	"time"

	"github.com/shopspring/decimal"
)

// PackRun is one extract-validate-load-reconcile execution of a pack (9.49.4).
type PackRun struct {
	RunID              string
	TenantID           string
	Pack               Pack
	SourceSystem       SourceSystem
	SourceCompany      string
	CutoffTS           time.Time
	InputHash          string
	Status             RunStatus
	RecordsIn          int
	RecordsLoaded      int
	RecordsQuarantined int
	// AlreadyPresent counts accepted records that legacy_xref already held, so the run did not
	// load them again (FR-49.10).
	AlreadyPresent int
	Totals         []ControlTotal
	Quarantined    []Quarantined
	// Parties is the master-data snapshot the run validated against; a release checks a
	// corrected party against it and the parties the release names.
	Parties []string
	// OpenItemsRunID is the open_items run a gl_balances run reconciled its control accounts
	// with (BR-49.2), or "".
	OpenItemsRunID string
	// GLRunID is the gl_balances run an onhand run reconciled the inventory account with
	// (BR-49.3), or "".
	GLRunID string
	// Version counts the run's writes; a release bumps it (optimistic concurrency).
	Version   int
	CreatedAt time.Time
	// Tolerances are the configured tolerances (FR-49.2) the run's money totals compare with. The
	// store loads them before a run reconciles; they are not stored with the run, and a total keeps
	// the tolerance it was stored with (a release never reads them again).
	Tolerances ToleranceSet
}

// TableOpenItems names the open_items pack in legacy_xref; its rows are held under the source table of
// their side (OpenItemsTable). Before migration 0016 every row was under this bare name.
const TableOpenItems = "open_items"

// The legacy_xref source tables of the open_items pack. A customer and a vendor open item come from two
// legacy tables (AX CustTransOpen and VendTransOpen, SAP BSID and BSIK) whose row ids are unique per
// table only, so one legacy id may name a receivable and a payable and the two must not collide.
const (
	TableOpenItemsReceivable = TableOpenItems + ".receivable"
	TableOpenItemsPayable    = TableOpenItems + ".payable"
)

// Side is the subledger of the record: AP when its ledger says so, AR otherwise.
func (r Record) Side() string {
	if r.Ledger == LedgerAP {
		return LedgerAP
	}
	return LedgerAR
}

// OpenItemsTable is the legacy_xref source_table of an open item of the side (AP or AR).
func OpenItemsTable(ledger string) string {
	if ledger == LedgerAP {
		return TableOpenItemsPayable
	}
	return TableOpenItemsReceivable
}

// OpenItemsSide is the side an open_items source table holds, or "" for any other table.
func OpenItemsSide(table string) string {
	switch table {
	case TableOpenItemsReceivable:
		return LedgerAR
	case TableOpenItemsPayable:
		return LedgerAP
	}
	return ""
}

// OpenItemKey is the key of an open item within a run or a company: its side and legacy id.
func OpenItemKey(side, legacyID string) string { return side + "|" + legacyID }

// SideName is the word for a side in messages.
func SideName(side string) string {
	if side == LedgerAP {
		return "payable"
	}
	return "receivable"
}

// Message returns MIG-1505 for a re-run that loaded nothing new, or "".
func (r *PackRun) Message() string {
	if r.RecordsLoaded == 0 && r.AlreadyPresent > 0 {
		return AlreadyPresentMessage(r.AlreadyPresent)
	}
	return ""
}

// ReconcileOpenItems finishes an open_items run once the accepted records are in legacy_xref.
// loaded is how many records this run inserted; present maps OpenItemKey (side and legacy id) to the amount
// legacy_xref holds for every accepted record, including ones loaded by earlier runs. It
// computes the FR-49.2 control totals (count, amount per currency, and amount per (company,
// account, currency)) of the whole extract against what Passage holds, and moves the run from
// VALIDATED to LOADED and, when no total is MISMATCH, to RECONCILED. Quarantined records are
// part of the source side, so a run with quarantine stays LOADED until they are released.
func ReconcileOpenItems(r *PackRun, records, accepted []Record, loaded int, present map[string]decimal.Decimal) error {
	if r.Status != StatusValidated {
		return fmt.Errorf("%w: reconcile needs a VALIDATED run, got %s", ErrTransition, r.Status)
	}
	r.RecordsLoaded = loaded
	r.AlreadyPresent = len(accepted) - loaded
	if err := r.Advance(StatusLoaded); err != nil {
		return err
	}
	type sub struct{ source, target decimal.Decimal }
	byCurrency := map[string]*sub{}
	byAccount := map[[2]string]*sub{}
	add := func(cur, acct string, source, target decimal.Decimal) {
		c, ok := byCurrency[cur]
		if !ok {
			c = &sub{}
			byCurrency[cur] = c
		}
		c.source, c.target = c.source.Add(source), c.target.Add(target)
		k := [2]string{acct, cur}
		a, ok := byAccount[k]
		if !ok {
			a = &sub{}
			byAccount[k] = a
		}
		a.source, a.target = a.source.Add(source), a.target.Add(target)
	}
	for _, rec := range records {
		add(rec.Currency, rec.Account, rec.Amount, decimal.Zero)
	}
	targetCount := 0
	for _, rec := range accepted {
		if amt, ok := present[OpenItemKey(rec.Side(), rec.LegacyID)]; ok {
			add(rec.Currency, rec.Account, decimal.Zero, amt)
			targetCount++
		}
	}
	company := r.SourceCompany
	var totals []ControlTotal
	for _, cur := range sortedKeys(byCurrency) {
		t, err := NewControlTotal("open_items.amount", map[string]string{"company": company, "currency": cur}, byCurrency[cur].source, byCurrency[cur].target, Tolerance{})
		if err != nil {
			return err
		}
		totals = append(totals, t)
	}
	acctKeys := make([][2]string, 0, len(byAccount))
	for k := range byAccount {
		acctKeys = append(acctKeys, k)
	}
	sort.Slice(acctKeys, func(i, j int) bool {
		if acctKeys[i][0] != acctKeys[j][0] {
			return acctKeys[i][0] < acctKeys[j][0]
		}
		return acctKeys[i][1] < acctKeys[j][1]
	})
	for _, k := range acctKeys {
		t, err := NewControlTotal("open_items.amount_by_account", map[string]string{"company": company, "account": k[0], "currency": k[1]}, byAccount[k].source, byAccount[k].target, Tolerance{})
		if err != nil {
			return err
		}
		totals = append(totals, t)
	}
	n, err := NewControlTotal("open_items.count", map[string]string{"company": company}, decimal.NewFromInt(int64(len(records))), decimal.NewFromInt(int64(targetCount)), Tolerance{})
	if err != nil {
		return err
	}
	r.Totals = append(totals, n)
	if err := r.applyTolerances(); err != nil {
		return err
	}
	if !r.HasMismatch() {
		return r.Advance(StatusReconciled)
	}
	return nil
}

func sortedKeys[V any](m map[string]V) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// CountTotals returns how many totals matched (MATCH or WITHIN_TOLERANCE) and how many did not.
func CountTotals(totals []ControlTotal) (match, mismatch int) {
	for _, t := range totals {
		if t.Status == StatusMismatch {
			mismatch++
		} else {
			match++
		}
	}
	return match, mismatch
}

// Advance moves the run to next when the state machine allows it. Moving to
// RECONCILED additionally requires that no control total is MISMATCH (9.49.4
// invariant).
func (r *PackRun) Advance(next RunStatus) error {
	if !CanTransition(r.Status, next) {
		return fmt.Errorf("%w: %s -> %s", ErrTransition, r.Status, next)
	}
	if next == StatusReconciled && r.HasMismatch() {
		return fmt.Errorf("%w: control totals contain MISMATCH", ErrTransition)
	}
	r.Status = next
	return nil
}

// HasMismatch reports whether any control total is MISMATCH.
func (r *PackRun) HasMismatch() bool {
	for _, t := range r.Totals {
		if t.Status == StatusMismatch {
			return true
		}
	}
	return false
}

// CheckCutoff rejects an extract whose cutoff is after the legacy freeze (MIG-1501).
func CheckCutoff(cutoff, freeze time.Time) error {
	if cutoff.After(freeze) {
		return fmt.Errorf("%w: MIG-1501 extract cutoff %s is after the legacy freeze %s", ErrInvalid, cutoff.UTC().Format(time.RFC3339), freeze.UTC().Format(time.RFC3339))
	}
	return nil
}
