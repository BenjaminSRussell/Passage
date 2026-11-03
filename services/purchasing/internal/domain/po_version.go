package domain

import (
	"sort"

	"github.com/shopspring/decimal"
)

// SnapshotLine is the commercial content of one PO line as recorded in a
// version. Money and quantities are decimal strings and the delivery date is a
// Day, so the JSON is stable and a patch value is what the API shows.
type SnapshotLine struct {
	LineNo       int             `json:"lineNo"`
	ProductID    string          `json:"productId,omitempty"`
	Description  string          `json:"description,omitempty"`
	Qty          decimal.Decimal `json:"qty"`
	Unit         string          `json:"unit"`
	UnitPrice    decimal.Decimal `json:"unitPrice"`
	PriceUnit    decimal.Decimal `json:"priceUnit"`
	DiscountPct  decimal.Decimal `json:"discountPct"`
	LineAmount   decimal.Decimal `json:"lineAmount"`
	TaxAmount    decimal.Decimal `json:"taxAmount"`
	DeliveryDate Day             `json:"deliveryDate"`
	Status       string          `json:"status"`
	// SiteID and WarehouseID are recorded only when the line is delivered somewhere other than the
	// PO header's site and warehouse, so a version taken before lines had a destination still compares
	// equal and a move of one line shows as /lines/<i>/warehouseId.
	SiteID      string `json:"siteId,omitempty"`
	WarehouseID string `json:"warehouseId,omitempty"`
}

// SnapshotCharge is one header charge in a version.
type SnapshotCharge struct {
	Calc   string          `json:"calc"`
	Value  decimal.Decimal `json:"value"`
	Amount decimal.Decimal `json:"amount"`
	// ChargeCode is the charge code the charge was entered with; empty for a charge without one.
	ChargeCode string `json:"chargeCode,omitempty"`
}

// Snapshot is the full commercial content of a PO at one version (FR-17.8).
// Lines are sorted by line number and keep cancelled lines (BR-17.9), so an
// array index such as /lines/0/qty names the same line in every version.
type Snapshot struct {
	Currency string           `json:"currency"`
	Total    decimal.Decimal  `json:"total"`
	Lines    []SnapshotLine   `json:"lines"`
	Charges  []SnapshotCharge `json:"charges"`
}

// NewSnapshot copies lines and charges into a snapshot with lines sorted by
// line number.
func NewSnapshot(currency string, total decimal.Decimal, lines []SnapshotLine, charges []SnapshotCharge) Snapshot {
	out := make([]SnapshotLine, len(lines))
	copy(out, lines)
	sort.SliceStable(out, func(i, j int) bool { return out[i].LineNo < out[j].LineNo })
	ch := make([]SnapshotCharge, len(charges))
	copy(ch, charges)
	return Snapshot{Currency: currency, Total: total, Lines: out, Charges: ch}
}

// LineStates projects the live (not cancelled) lines onto the fields change
// management compares. A line cancelled by the change is therefore missing
// from the after state, which only ANY_CHANGE treats as a change.
func (s Snapshot) LineStates() []LineState {
	out := make([]LineState, 0, len(s.Lines))
	for _, l := range s.Lines {
		if l.Status == POLineCancelled {
			continue
		}
		out = append(out, LineState{LineNo: l.LineNo, Qty: l.Qty, UnitPrice: l.UnitPrice, DeliveryDate: l.DeliveryDate})
	}
	return out
}

// VersionDiff returns the RFC 6902 patch that turns the base snapshot into the
// next one. An empty result means the commercial content did not change.
func VersionDiff(base, next Snapshot) ([]PatchOp, error) {
	return Diff(base, next)
}

// ChangeNeedsReapproval applies the reapproval rules to a change from base to
// next whose patch is given (FR-17.8). The line rules compare live lines; ANY_CHANGE
// also fires on a change the line rules do not see, such as a charge.
func ChangeNeedsReapproval(base, next Snapshot, patch []PatchOp, rules []ReapprovalRule) bool {
	if len(patch) == 0 {
		return false
	}
	for _, r := range rules {
		if r.Kind == RuleAnyChange {
			return true
		}
	}
	return ReapprovalRequired(base.LineStates(), next.LineStates(), rules)
}
