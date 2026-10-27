package domain

import (
	"sort"
	"time"

	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/platform/money"
)

// CodeNoAgreement is LEAN-0005: no purchase agreement prices the service item on
// the job completion date (FR-24.20).
const CodeNoAgreement = "LEAN-0005"

// Subcontract PO modes (FR-24.20, PRD 4.24.13).
const (
	ModePOPerKanban = "po-per-kanban"
	ModePeriodicPO  = "periodic-po"
)

// Agreement is a purchase agreement price for a service item from a vendor (w17).
// ValidFrom and ValidTo are calendar days, both inclusive; ValidTo zero means
// open-ended.
type Agreement struct {
	VendorID  string
	ItemID    string
	UnitPrice decimal.Decimal
	Currency  string
	ValidFrom time.Time
	ValidTo   time.Time
}

// civil truncates t to its calendar date in UTC.
func civil(t time.Time) time.Time {
	y, m, d := t.UTC().Date()
	return time.Date(y, m, d, 0, 0, 0, 0, time.UTC)
}

// covers reports whether the agreement is valid on the calendar day of t
// (BR-24.16); a job completed at any time on ValidTo is still covered.
func (a Agreement) covers(t time.Time) bool {
	d := civil(t)
	if d.Before(civil(a.ValidFrom)) {
		return false
	}
	return a.ValidTo.IsZero() || !d.After(civil(a.ValidTo))
}

// ServiceJob is a completed kanban job of a service activity on a vendor work cell.
// Qty is the completed job quantity (BR-24.16).
type ServiceJob struct {
	JobID       string
	VendorID    string
	ItemID      string
	Qty         decimal.Decimal
	CompletedOn time.Time
}

// POLine is a purchase order line generated from service receipts (FR-24.20). In
// periodic-po mode one line carries every job of its vendor, item and price.
// Amount is qty × unit price rounded to the currency's minor unit.
type POLine struct {
	VendorID  string
	ItemID    string
	Qty       decimal.Decimal
	UnitPrice decimal.Decimal
	Currency  string
	PriceDate time.Time
	Amount    decimal.Decimal
	JobIDs    []string
}

// SubcontractLines turns completed service jobs into purchase order lines.
// po-per-kanban yields one line per job. periodic-po accumulates the jobs of one
// vendor, item and unit price into one line (BR-24.16). Each job is priced by the
// agreement valid on its completion date; without one the result is LEAN-0005.
func SubcontractLines(mode string, agreements []Agreement, jobs []ServiceJob) ([]POLine, error) {
	if mode != ModePOPerKanban && mode != ModePeriodicPO {
		return nil, errorf(CodeInvalid, "unknown subcontract mode %q", mode)
	}
	for _, a := range agreements {
		if a.UnitPrice.IsNegative() || len(a.Currency) != 3 {
			return nil, errorf(CodeInvalid, "agreement for %s needs a price of 0 or more and an ISO 4217 currency", a.ItemID)
		}
		if !a.ValidTo.IsZero() && civil(a.ValidTo).Before(civil(a.ValidFrom)) {
			return nil, errorf(CodeInvalid, "agreement for %s ends before it starts", a.ItemID)
		}
	}
	lines := []POLine{}
	index := map[string]int{}
	seen := map[string]bool{}
	for _, j := range jobs {
		if seen[j.JobID] {
			return nil, errorf(CodeInvalid, "job %s listed twice", j.JobID)
		}
		seen[j.JobID] = true
		if !j.Qty.IsPositive() {
			return nil, errorf(CodeInvalid, "job %s has no completed quantity", j.JobID)
		}
		ag, ok := priceFor(agreements, j)
		if !ok {
			return nil, errorf(CodeNoAgreement, "no valid purchase agreement for service %s on %s", j.ItemID, j.CompletedOn.Format("2006-01-02"))
		}
		if mode == ModePOPerKanban {
			lines = append(lines, newLine(ag, j))
			continue
		}
		key := j.VendorID + "|" + j.ItemID + "|" + ag.UnitPrice.String() + "|" + ag.Currency
		if i, seen := index[key]; seen {
			l := &lines[i]
			l.Qty = l.Qty.Add(j.Qty)
			l.JobIDs = append(l.JobIDs, j.JobID)
			if d := civil(j.CompletedOn); d.After(l.PriceDate) {
				l.PriceDate = d
			}
			l.Amount = amount(l.Qty, l.UnitPrice, l.Currency)
			continue
		}
		index[key] = len(lines)
		lines = append(lines, newLine(ag, j))
	}
	sort.SliceStable(lines, func(a, b int) bool {
		if lines[a].VendorID != lines[b].VendorID {
			return lines[a].VendorID < lines[b].VendorID
		}
		return lines[a].ItemID < lines[b].ItemID
	})
	return lines, nil
}

// priceFor returns the agreement for the job's vendor and item that is valid on
// the completion date. When several overlap, the one that started last wins
// (it supersedes the older one); ties keep the first.
func priceFor(agreements []Agreement, j ServiceJob) (Agreement, bool) {
	var best Agreement
	found := false
	for _, a := range agreements {
		if a.VendorID != j.VendorID || a.ItemID != j.ItemID || !a.covers(j.CompletedOn) {
			continue
		}
		if !found || civil(a.ValidFrom).After(civil(best.ValidFrom)) {
			best, found = a, true
		}
	}
	return best, found
}

// amount is qty × price rounded once to the currency's minor unit.
func amount(qty, price decimal.Decimal, currency string) decimal.Decimal {
	return money.Amount{Value: qty.Mul(price), Currency: currency}.Round().Value
}

func newLine(ag Agreement, j ServiceJob) POLine {
	return POLine{VendorID: j.VendorID, ItemID: j.ItemID, Qty: j.Qty, UnitPrice: ag.UnitPrice, Currency: ag.Currency,
		PriceDate: civil(j.CompletedOn), Amount: amount(j.Qty, ag.UnitPrice, ag.Currency), JobIDs: []string{j.JobID}}
}
