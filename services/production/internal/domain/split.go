package domain

import (
	"fmt"

	"github.com/shopspring/decimal"
)

// Stable error codes of split and merge (FR-21.15).
const (
	// CodeSplitQuantity is PROD-0015: the split quantity is not above zero and
	// below the order quantity, is below the minimum order quantity, or is not a
	// multiple of the item's multiple.
	CodeSplitQuantity = "PRODUCTION_SPLIT_QUANTITY"
	// CodeMergeRefused is PROD-0016 and the other merge preconditions.
	CodeMergeRefused = "PRODUCTION_MERGE_REFUSED"
	// CodeSplitRefused is a split of a Started order that BR-21.11 cannot move
	// the posted figures of: reported output, draft journals, or a transfer.
	CodeSplitRefused = "PRODUCTION_SPLIT_REFUSED"
)

// OriginSplit is the origin type of an order a split created (PRD 4.21.4).
const OriginSplit = "Split"

// Split and merge are recorded in the status log under these commands; they are
// not `:command` transitions of BR-21.1.
const (
	CmdSplit Command = "split"
	CmdMerge Command = "merge"
)

// maxOrderQty keeps a quantity inside numeric(18,6).
var maxOrderQty = decimal.New(1, 12)

// CheckMergeable reports whether an order in status s may be merged: Created
// to Released, before anything is consumed or reported.
func CheckMergeable(s Status) error {
	if rank(s) < 0 || rank(s) > rank(Released) {
		return &Error{Code: CodeIllegalTransition, Detail: fmt.Sprintf("a %s order cannot be merged; only Created to Released orders can", s)}
	}
	return nil
}

// CheckSplittable reports whether an order in status s may be split: Created
// to Started (FR-21.15). A Started order moves its posted consumption, hours
// and WIP to the new order (BR-21.11); a later status has reported output.
func CheckSplittable(s Status) error {
	if rank(s) < 0 || rank(s) > rank(Started) {
		return &Error{Code: CodeIllegalTransition, Detail: fmt.Sprintf("a %s order cannot be split; only Created to Started orders can", s)}
	}
	return nil
}

// CheckSplitQty applies the quantity rules of FR-21.15 (PROD-0015): the split
// quantity is above zero and below the order quantity, at least the minimum
// order quantity and a multiple of the item's multiple. A zero minimum or
// multiple disables that check.
func CheckSplitQty(orderQty, splitQty, minQty, multiple decimal.Decimal) error {
	if !splitQty.IsPositive() || !splitQty.LessThan(orderQty) {
		return &Error{Code: CodeSplitQuantity, Detail: fmt.Sprintf("split quantity %s must be above zero and below the order quantity %s", splitQty, orderQty)}
	}
	if minQty.IsPositive() && splitQty.LessThan(minQty) {
		return &Error{Code: CodeSplitQuantity, Detail: fmt.Sprintf("split quantity %s is below the minimum order quantity %s", splitQty, minQty)}
	}
	if multiple.IsPositive() && !splitQty.Mod(multiple).IsZero() {
		return &Error{Code: CodeSplitQuantity, Detail: fmt.Sprintf("split quantity %s is not a multiple of %s", splitQty, multiple)}
	}
	return nil
}

// SplitPlan is an order split in two: the original keeps OriginalQty and the
// new order receives SplitQty, each with its own lines and operations.
type SplitPlan struct {
	OriginalQty   decimal.Decimal
	SplitQty      decimal.Decimal
	OriginalLines []OrderLine
	OriginalOps   []Operation
	NewLines      []OrderLine
	NewOps        []Operation
}

// SplitOrder splits an order of orderQty (FR-21.15). Both orders keep the BOM
// and route snapshot and are re-exploded for their own quantity, as a quantity
// change is: line requirements by BR-21.2 and process quantities by BR-21.8,
// so the variable requirement is shared in proportion and each order carries
// its own setup. The new order starts with nothing consumed or reported.
func SplitOrder(orderQty, splitQty, minQty, multiple decimal.Decimal, lines []OrderLine, ops []Operation) (SplitPlan, error) {
	if err := CheckSplitQty(orderQty, splitQty, minQty, multiple); err != nil {
		return SplitPlan{}, err
	}
	plan := SplitPlan{OriginalQty: orderQty.Sub(splitQty), SplitQty: splitQty}
	var err error
	if plan.OriginalLines, err = Requantify(lines, plan.OriginalQty); err != nil {
		return SplitPlan{}, err
	}
	if plan.OriginalOps, err = PlanOperations(ops, plan.OriginalQty); err != nil {
		return SplitPlan{}, err
	}
	if plan.NewLines, err = Requantify(lines, splitQty); err != nil {
		return SplitPlan{}, err
	}
	for i := range plan.NewLines {
		plan.NewLines[i].QtyConsumed = decimal.Zero
	}
	if plan.NewOps, err = PlanOperations(ops, splitQty); err != nil {
		return SplitPlan{}, err
	}
	for i := range plan.NewOps {
		op := &plan.NewOps[i]
		op.HoursReported, op.QtyGood, op.QtyError, op.Status = decimal.Zero, decimal.Zero, decimal.Zero, OperOpen
	}
	return plan, nil
}

// MergeInput is the part of an order a merge compares and sums.
type MergeInput struct {
	ID              string
	LegalEntityID   string
	ProductID       string
	SiteID          string
	WarehouseID     string
	Currency        string
	ItemGroup       string
	ReworkReference *ReworkRef
	Status          Status
	Quantity        decimal.Decimal
	Lines           []OrderLine
	Operations      []Operation
}

// MergedOrder is the survivor after a merge: the summed quantity and its
// lines and operations re-exploded for it.
type MergedOrder struct {
	Quantity   decimal.Decimal
	Lines      []OrderLine
	Operations []Operation
}

// MergeOrders checks that orders may be merged into the first one and returns
// the survivor (FR-21.15). The orders must each be Created to Released and
// share legal entity, product, site, warehouse, currency, item group, rework
// reference and the BOM and route snapshot (the same BOM/route version). The
// survivor's lines and operations are re-exploded for the summed quantity.
// Posted journals (PROD-0016) are checked by the store.
func MergeOrders(orders []MergeInput) (MergedOrder, error) {
	if len(orders) < 2 {
		return MergedOrder{}, refuseMerge("a merge needs at least two orders")
	}
	first := orders[0]
	total := decimal.Zero
	seen := map[string]bool{}
	for _, o := range orders {
		if seen[o.ID] {
			return MergedOrder{}, refuseMerge(fmt.Sprintf("order %s is listed twice", o.ID))
		}
		seen[o.ID] = true
		if err := CheckMergeable(o.Status); err != nil {
			return MergedOrder{}, refuseMerge(fmt.Sprintf("order %s is %s; only Created to Released orders can be merged", o.ID, o.Status))
		}
		if o.LegalEntityID != first.LegalEntityID || o.ProductID != first.ProductID || o.SiteID != first.SiteID ||
			o.WarehouseID != first.WarehouseID || o.Currency != first.Currency || o.ItemGroup != first.ItemGroup {
			return MergedOrder{}, refuseMerge(fmt.Sprintf("order %s differs in legal entity, product, site, warehouse, currency or item group", o.ID))
		}
		if !sameReworkRef(o.ReworkReference, first.ReworkReference) {
			return MergedOrder{}, refuseMerge(fmt.Sprintf("order %s reworks something else", o.ID))
		}
		if !sameSnapshot(o.Lines, o.Operations, first.Lines, first.Operations) {
			return MergedOrder{}, refuseMerge(fmt.Sprintf("order %s has a different BOM or route", o.ID))
		}
		total = total.Add(o.Quantity)
	}
	if !total.LessThan(maxOrderQty) {
		return MergedOrder{}, refuseMerge("the merged quantity is too large")
	}
	lines, err := Requantify(first.Lines, total)
	if err != nil {
		return MergedOrder{}, err
	}
	ops, err := PlanOperations(first.Operations, total)
	if err != nil {
		return MergedOrder{}, err
	}
	return MergedOrder{Quantity: total, Lines: lines, Operations: ops}, nil
}

func refuseMerge(detail string) error { return &Error{Code: CodeMergeRefused, Detail: detail} }

func sameReworkRef(a, b *ReworkRef) bool {
	if a == nil || b == nil {
		return a == nil && b == nil
	}
	return *a == *b
}

// sameSnapshot compares the BOM and route snapshot of two orders: the fields a
// BOM or route version fixes, not the quantities that depend on the order
// quantity, the unit costs of the costing version, or the feedback.
func sameSnapshot(aLines []OrderLine, aOps []Operation, bLines []OrderLine, bOps []Operation) bool {
	if len(aLines) != len(bLines) || len(aOps) != len(bOps) {
		return false
	}
	for i := range aLines {
		a, b := aLines[i], bLines[i]
		if a.LineNo != b.LineNo || a.ItemID != b.ItemID || !a.QtyPer.Equal(b.QtyPer) || !a.ScrapPct.Equal(b.ScrapPct) ||
			!a.SeriesSize.Equal(b.SeriesSize) || a.RoundUp != b.RoundUp || a.Flushing != b.Flushing {
			return false
		}
	}
	for i := range aOps {
		a, b := aOps[i], bOps[i]
		if a.OperNumber != b.OperNumber || a.ResourceID != b.ResourceID || !a.SetupH.Equal(b.SetupH) || !a.RunHPer.Equal(b.RunHPer) ||
			!a.ScrapPct.Equal(b.ScrapPct) || a.Subcontract != b.Subcontract || !a.LaborRate.Equal(b.LaborRate) ||
			!a.OverheadRate.Equal(b.OverheadRate) || !a.SubcontractPrice.Equal(b.SubcontractPrice) {
			return false
		}
	}
	return true
}
