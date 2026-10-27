package domain

import (
	"fmt"

	"github.com/shopspring/decimal"
)

// CostGroup is the cost group of a planned or actual cost row.
type CostGroup string

const (
	GroupMaterial    CostGroup = "material"
	GroupLabor       CostGroup = "labor"
	GroupOverhead    CostGroup = "overhead"
	GroupSubcontract CostGroup = "subcontract"
)

// costScale is the internal precision of amounts; they round to 2 places on posting (FR-21.3).
const costScale = 4

// CostRow is one planned or actual cost line. Key is the item id for material
// and resource@operation for labor, overhead and subcontract. Fixed is the
// fixed part of a planned Amount (setup), which drives the lot-size variance.
type CostRow struct {
	Group  CostGroup       `json:"group"`
	Key    string          `json:"key"`
	Qty    decimal.Decimal `json:"qty"`
	Amount decimal.Decimal `json:"amount"`
	Fixed  decimal.Decimal `json:"fixed"`
}

func money(d decimal.Decimal) decimal.Decimal { return d.Round(costScale) }

// Estimate computes planned cost (FR-21.3): material from the exploded lines
// (Σ qty × (1 + scrap%) × unit cost, one row per item, because an item can sit
// on several lines, for example under two phantoms), labor and overhead from
// operation hours (setup + run × process quantity) at the resource rates, and
// subcontract at the vendor unit price × process quantity.
func Estimate(lines []OrderLine, ops []Operation) []CostRow {
	rows := []CostRow{}
	byItem := map[string]int{}
	for _, l := range lines {
		amount := l.QtyRequired.Mul(l.UnitCost)
		if i, seen := byItem[l.ItemID]; seen {
			rows[i].Qty = rows[i].Qty.Add(l.QtyRequired)
			rows[i].Amount = rows[i].Amount.Add(amount)
			continue
		}
		byItem[l.ItemID] = len(rows)
		rows = append(rows, CostRow{Group: GroupMaterial, Key: l.ItemID, Qty: l.QtyRequired, Amount: amount})
	}
	for i := range rows {
		rows[i].Amount = money(rows[i].Amount)
	}
	for _, op := range ops {
		key := fmt.Sprintf("%s@%d", op.ResourceID, op.OperNumber)
		if op.Subcontract {
			rows = append(rows, CostRow{Group: GroupSubcontract, Key: key, Qty: op.ProcessQty, Amount: money(op.SubcontractPrice.Mul(op.ProcessQty))})
			continue
		}
		hours := op.SetupH.Add(op.RunHPer.Mul(op.ProcessQty))
		rows = append(rows,
			CostRow{Group: GroupLabor, Key: key, Qty: hours, Amount: money(hours.Mul(op.LaborRate)), Fixed: money(op.SetupH.Mul(op.LaborRate))},
			CostRow{Group: GroupOverhead, Key: key, Qty: hours, Amount: money(hours.Mul(op.OverheadRate)), Fixed: money(op.SetupH.Mul(op.OverheadRate))},
		)
	}
	return rows
}

// VarianceType is one of the four variance types of BR-21.6.
type VarianceType string

const (
	VarLotSize      VarianceType = "lot-size"
	VarQuantity     VarianceType = "production-quantity"
	VarPrice        VarianceType = "production-price"
	VarSubstitution VarianceType = "production-substitution"
)

// Variance is one GL amount posted at End. Positive is unfavourable (debit),
// negative is favourable (credit).
type Variance struct {
	Type   VarianceType    `json:"type"`
	Group  CostGroup       `json:"costGroup"`
	Amount decimal.Decimal `json:"amount"`
}

// Settlement is the result of End: the WIP balance before settlement and the
// variances that clear it. The variances always sum to WIPBefore.
type Settlement struct {
	WIPBefore decimal.Decimal `json:"wipBefore"`
	Variances []Variance      `json:"variances"`
}

type cellKey struct {
	group CostGroup
	key   string
}

// Settle decomposes actual cost against the estimate at End (BR-21.6). With
// r = rafQty / orderQty, the variances sum to actual − r × planned. Each
// variance is rounded to cents and the rounding residual lands on the largest
// variance, so the sum is exact at cent precision.
//
// Matched rows: quantity variance = (actual qty − r × planned qty) × standard
// price, less the lot-size share; price variance = actual − actual qty × standard;
// lot-size variance = fixed × (1 − r). Planned rows with no actual, and actual
// rows with no plan, go to the substitution variance. Rows sharing a group and
// key are summed first.
func Settle(orderQty, rafQty decimal.Decimal, planned, actual []CostRow) (Settlement, error) {
	if !orderQty.IsPositive() || rafQty.IsNegative() {
		return Settlement{}, &Error{Code: CodeInvalidInput, Detail: "order quantity must be positive and RAF quantity non-negative"}
	}
	r := rafQty.Div(orderQty)
	planned, actual = mergeRows(planned), mergeRows(actual)
	actualByKey := map[cellKey]CostRow{}
	for _, a := range actual {
		actualByKey[cellKey{a.Group, a.Key}] = a
	}

	var vars []Variance
	add := func(t VarianceType, g CostGroup, amt decimal.Decimal) {
		for i := range vars {
			if vars[i].Type == t && vars[i].Group == g {
				vars[i].Amount = vars[i].Amount.Add(amt)
				return
			}
		}
		vars = append(vars, Variance{Type: t, Group: g, Amount: amt})
	}

	plannedTotal := decimal.Zero
	matched := map[cellKey]bool{}
	for _, p := range planned {
		plannedTotal = plannedTotal.Add(p.Amount)
		k := cellKey{p.Group, p.Key}
		a, ok := actualByKey[k]
		if !ok || !p.Qty.IsPositive() {
			add(VarSubstitution, p.Group, p.Amount.Mul(r).Neg())
			continue
		}
		matched[k] = true
		std := p.Amount.Div(p.Qty)
		lot := p.Fixed.Mul(one.Sub(r))
		add(VarQuantity, p.Group, a.Qty.Sub(p.Qty.Mul(r)).Mul(std).Sub(lot))
		add(VarPrice, p.Group, a.Amount.Sub(a.Qty.Mul(std)))
		add(VarLotSize, p.Group, lot)
	}

	actualTotal := decimal.Zero
	for _, a := range actual {
		actualTotal = actualTotal.Add(a.Amount)
		if !matched[cellKey{a.Group, a.Key}] {
			add(VarSubstitution, a.Group, a.Amount)
		}
	}

	target := actualTotal.Sub(r.Mul(plannedTotal)).Round(2)
	sum := decimal.Zero
	largest := -1
	for i := range vars {
		vars[i].Amount = vars[i].Amount.Round(2)
		sum = sum.Add(vars[i].Amount)
		if largest < 0 || vars[i].Amount.Abs().GreaterThan(vars[largest].Amount.Abs()) {
			largest = i
		}
	}
	residual := target.Sub(sum)
	if !residual.IsZero() {
		if largest < 0 {
			return Settlement{}, &Error{Code: CodeInvalidInput, Detail: "cannot place rounding residual without a variance row"}
		}
		vars[largest].Amount = vars[largest].Amount.Add(residual)
	}

	out := make([]Variance, 0, len(vars))
	for _, v := range vars {
		if !v.Amount.IsZero() {
			out = append(out, v)
		}
	}
	return Settlement{WIPBefore: target, Variances: out}, nil
}

// mergeRows sums rows that share a cost group and key, keeping first-seen
// order, so a cell is matched once however many journal lines fed it.
func mergeRows(rows []CostRow) []CostRow {
	out := make([]CostRow, 0, len(rows))
	at := map[cellKey]int{}
	for _, row := range rows {
		k := cellKey{row.Group, row.Key}
		if i, seen := at[k]; seen {
			out[i].Qty = out[i].Qty.Add(row.Qty)
			out[i].Amount = out[i].Amount.Add(row.Amount)
			out[i].Fixed = out[i].Fixed.Add(row.Fixed)
			continue
		}
		at[k] = len(out)
		out = append(out, row)
	}
	return out
}
