package domain

import (
	"fmt"

	"github.com/shopspring/decimal"

	platformmoney "github.com/BenjaminSRussell/passage/platform/money"
)

// Stable error codes raised by journal, posting-profile and settlement rules.
const (
	CodeNoPostingProfile = "PRODUCTION_POSTING_PROFILE_MISSING"
	CodeOverConsumption  = "PRODUCTION_OVER_CONSUMPTION"
	CodeInvalidHours     = "PRODUCTION_INVALID_HOURS"
	CodeUnbalanced       = "PRODUCTION_POSTING_NOT_BALANCED"
)

// AllKey is the wildcard item group or site of a posting profile (FR-21.13).
const AllKey = "All"

// HoursScale is the precision of reported hours; MinHours the smallest postable value (FR-21.6).
const HoursScale = 2

// MinHours is 0.01 h.
var MinHours = decimal.New(1, -HoursScale)

// JournalType is the kind of production journal (FR-21.5 to FR-21.7). Job cards
// need the ProductionJob rows that scheduling creates and are not posted yet.
type JournalType string

const (
	JournalPickingList    JournalType = "picking-list"
	JournalRouteCard      JournalType = "route-card"
	JournalReportFinished JournalType = "report-as-finished"
)

// Valid reports whether t is a journal type this service posts.
func (t JournalType) Valid() bool {
	return t == JournalPickingList || t == JournalRouteCard || t == JournalReportFinished
}

// Flushing is the flushing principle of a BOM line (FR-21.5): when its
// material is consumed. Manual and available-at-location lines are issued by
// picking-list journals; start lines at Start; finish lines at report as finished.
type Flushing string

const (
	FlushManual    Flushing = "manual"
	FlushStart     Flushing = "start"
	FlushFinish    Flushing = "finish"
	FlushAvailable Flushing = "available-at-location"
)

// Valid reports whether f is one of the four flushing principles.
func (f Flushing) Valid() bool {
	return f == FlushManual || f == FlushStart || f == FlushFinish || f == FlushAvailable
}

// Proposal is how a journal proposes its lines (FR-21.5, PRD 4.21.13).
type Proposal string

const (
	ProposeRemaining    Proposal = "remaining"
	ProposeProportional Proposal = "proportional"
	ProposeNone         Proposal = "none"
)

// Valid reports whether p is a proposal mode.
func (p Proposal) Valid() bool {
	return p == ProposeRemaining || p == ProposeProportional || p == ProposeNone
}

// ProposeQty is the quantity still to issue on a BOM line. remaining targets
// the full requirement; proportional targets the requirement × share, where
// share is the order's reported-as-finished quantity over its quantity. The
// result is rounded up to QtyScale (BR-21.2) and never negative.
func ProposeQty(mode Proposal, required, consumed, share decimal.Decimal) decimal.Decimal {
	return propose(mode, required, consumed, share, CeilQty)
}

// StartIssueQty is the quantity the Released → Started backflush issues on a
// BOM line (FR-21.5, BR-21.1): the remaining requirement of a start-flushed
// line, and nothing for any other flushing principle (finish lines are
// consumed at report as finished; manual and available-at-location lines by
// picking lists).
func StartIssueQty(f Flushing, required, consumed decimal.Decimal) decimal.Decimal {
	if f != FlushStart {
		return decimal.Zero
	}
	return ProposeQty(ProposeRemaining, required, consumed, decimal.Zero)
}

// ProposeHours is the route-card proposal for an operation on the same basis,
// rounded to 2 places (FR-21.6).
func ProposeHours(mode Proposal, planned, reported, share decimal.Decimal) decimal.Decimal {
	return propose(mode, planned, reported, share, func(d decimal.Decimal) decimal.Decimal { return d.Round(HoursScale) })
}

func propose(mode Proposal, target, done, share decimal.Decimal, round func(decimal.Decimal) decimal.Decimal) decimal.Decimal {
	switch mode {
	case ProposeRemaining:
	case ProposeProportional:
		target = target.Mul(share)
	default:
		return decimal.Zero
	}
	return decimal.Max(round(target.Sub(done)), decimal.Zero)
}

// CheckHours validates a reported hours value: at least 0.01 h, at most 2 decimals.
func CheckHours(h decimal.Decimal) error {
	if h.LessThan(MinHours) || !h.Equal(h.Round(HoursScale)) {
		return &Error{Code: CodeInvalidHours, Detail: "hours must be at least 0.01 with at most 2 decimals"}
	}
	return nil
}

// CheckConsumption refuses an issue that takes a line's consumption beyond
// its requirement plus the over-consumption tolerance, unless the caller holds
// the over-consumption duty (FR-21.5, PROD-0007).
func CheckConsumption(lineNo string, required, consumedAfter, tolerancePct decimal.Decimal, duty bool) error {
	limit := required.Mul(one.Add(tolerancePct.Div(hundred)))
	if duty || !consumedAfter.GreaterThan(limit) {
		return nil
	}
	return &Error{Code: CodeOverConsumption, Detail: fmt.Sprintf("line %s: consumption %s exceeds required %s plus tolerance %s%%",
		lineNo, consumedAfter, required, tolerancePct)}
}

// Profile holds the ledger accounts of one legal entity × item group × site
// (FR-21.13). ItemGroup and SiteID may be AllKey.
type Profile struct {
	ItemGroup          string `json:"itemGroup"`
	SiteID             string `json:"siteId"`
	WIP                string `json:"wipAccount"`
	Inventory          string `json:"inventoryAccount"`
	FinishedGoods      string `json:"finishedGoodsAccount"`
	LaborAbsorption    string `json:"laborAbsorptionAccount"`
	OverheadAbsorption string `json:"overheadAbsorptionAccount"`
	VarLotSize         string `json:"lotSizeVarianceAccount"`
	VarQuantity        string `json:"quantityVarianceAccount"`
	VarPrice           string `json:"priceVarianceAccount"`
	VarSubstitution    string `json:"substitutionVarianceAccount"`
	// VarRework is the account of the rework variance (FR-21.20). It may stay
	// empty until the legal entity runs rework orders; ending one needs it.
	VarRework string `json:"reworkVarianceAccount"`
}

// ResolveProfile picks the most specific profile for an item group and site.
// The item relation outranks the site, as in AX posting setup: item group ×
// site, item group × All, All × site, then All × All. An order without an
// item group resolves through the All rows only.
func ResolveProfile(profiles []Profile, itemGroup, siteID string) (Profile, error) {
	if itemGroup == "" {
		itemGroup = AllKey
	}
	for _, want := range [][2]string{{itemGroup, siteID}, {itemGroup, AllKey}, {AllKey, siteID}, {AllKey, AllKey}} {
		for _, p := range profiles {
			if p.ItemGroup == want[0] && p.SiteID == want[1] {
				return p, nil
			}
		}
	}
	return Profile{}, &Error{Code: CodeNoPostingProfile, Detail: fmt.Sprintf("no posting profile resolves for item group %s at site %s", itemGroup, siteID)}
}

func (p Profile) varianceAccount(t VarianceType) string {
	switch t {
	case VarLotSize:
		return p.VarLotSize
	case VarQuantity:
		return p.VarQuantity
	case VarPrice:
		return p.VarPrice
	case VarRework:
		return p.VarRework
	}
	return p.VarSubstitution
}

// Posting types of the WIP subledger rows. The wip-* rows are the order's WIP
// account; their balance is the order's work in progress.
const (
	PostWIPMaterial    = "wip-material"
	PostInventoryIssue = "inventory-issue"
	PostWIPLabor       = "wip-labor"
	PostLaborAbsorb    = "labor-absorption"
	PostWIPOverhead    = "wip-overhead"
	PostOverheadAbsorb = "overhead-absorption"
	PostFinishedGoods  = "raf-receipt"
	PostWIPReceipt     = "wip-raf-offset"
	PostWIPSettlement  = "wip-settlement"
	postVariancePrefix = "variance-"
)

// Posting is one debit or credit line; exactly one of Dr and Cr is non-zero.
// WIP marks the lines on the order's WIP account.
type Posting struct {
	Type    string          `json:"postingType"`
	Account string          `json:"account"`
	Dr      decimal.Decimal `json:"dr"`
	Cr      decimal.Decimal `json:"cr"`
	WIP     bool            `json:"wip"`
}

func pair(drType, drAccount string, drWIP bool, crType, crAccount string, crWIP bool, amount decimal.Decimal) []Posting {
	if amount.IsZero() {
		return nil
	}
	if amount.IsNegative() {
		// A negative amount (a reversal) swaps the sides so both stay positive.
		return []Posting{
			{Type: drType, Account: drAccount, Cr: amount.Neg(), WIP: drWIP},
			{Type: crType, Account: crAccount, Dr: amount.Neg(), WIP: crWIP},
		}
	}
	return []Posting{
		{Type: drType, Account: drAccount, Dr: amount, WIP: drWIP},
		{Type: crType, Account: crAccount, Cr: amount, WIP: crWIP},
	}
}

// IssuePostings books a component issue: Dr WIP / Cr Inventory (FR-21.5).
func IssuePostings(p Profile, amount decimal.Decimal) []Posting {
	return pair(PostWIPMaterial, p.WIP, true, PostInventoryIssue, p.Inventory, false, amount)
}

// HoursPostings books route consumption: Dr WIP / Cr labor absorption and
// Dr WIP / Cr overhead absorption (FR-21.6).
func HoursPostings(p Profile, labor, overhead decimal.Decimal) []Posting {
	return append(pair(PostWIPLabor, p.WIP, true, PostLaborAbsorb, p.LaborAbsorption, false, labor),
		pair(PostWIPOverhead, p.WIP, true, PostOverheadAbsorb, p.OverheadAbsorption, false, overhead)...)
}

// ReceiptPostings books the finished receipt of a RAF: Dr Finished goods / Cr WIP (FR-21.7).
func ReceiptPostings(p Profile, amount decimal.Decimal) []Posting {
	return pair(PostFinishedGoods, p.FinishedGoods, false, PostWIPReceipt, p.WIP, true, amount)
}

// SettlementPostings clears the WIP balance into the variance accounts at End
// (FR-21.8). The variances must already sum to the balance (SettleWIP).
func SettlementPostings(p Profile, balance decimal.Decimal, vars []Variance) ([]Posting, error) {
	var out []Posting
	switch {
	case balance.IsPositive():
		out = append(out, Posting{Type: PostWIPSettlement, Account: p.WIP, Cr: balance, WIP: true})
	case balance.IsNegative():
		out = append(out, Posting{Type: PostWIPSettlement, Account: p.WIP, Dr: balance.Neg(), WIP: true})
	}
	for _, v := range vars {
		typ, acct := postVariancePrefix+string(v.Type), p.varianceAccount(v.Type)
		if acct == "" && !v.Amount.IsZero() {
			return nil, &Error{Code: CodeNoPostingProfile, Detail: fmt.Sprintf("the posting profile %s × %s has no %s variance account", p.ItemGroup, p.SiteID, v.Type)}
		}
		switch {
		case v.Amount.IsPositive():
			out = append(out, Posting{Type: typ, Account: acct, Dr: v.Amount})
		case v.Amount.IsNegative():
			out = append(out, Posting{Type: typ, Account: acct, Cr: v.Amount.Neg()})
		}
	}
	if !Balanced(out) {
		return nil, &Error{Code: CodeUnbalanced, Detail: "the settlement debits and credits differ"}
	}
	return out, nil
}

// Balanced reports whether the debits of the postings equal their credits.
func Balanced(ps []Posting) bool {
	return Net(ps, false).IsZero()
}

// Net is Σ Dr − Σ Cr over the postings, or over the WIP lines only.
func Net(ps []Posting, wipOnly bool) decimal.Decimal {
	sum := decimal.Zero
	for _, p := range ps {
		if !wipOnly || p.WIP {
			sum = sum.Add(p.Dr).Sub(p.Cr)
		}
	}
	return sum
}

// RoundMoney rounds an amount to the currency's minor unit for posting
// (FR-21.3: 4 decimals internally, the currency's precision on posting).
func RoundMoney(v decimal.Decimal, currency string) decimal.Decimal {
	return platformmoney.Amount{Value: v, Currency: currency}.Round().Value
}

// ReceiptValue values a RAF of good quantity from qtyBefore to qtyAfter at
// the planned unit cost (planned total ÷ order quantity). Each cumulative
// value is rounded, so successive receipts sum to round(planned × qty ÷ order qty)
// with no drift.
func ReceiptValue(planned, orderQty, qtyBefore, qtyAfter decimal.Decimal, currency string) decimal.Decimal {
	cum := func(q decimal.Decimal) decimal.Decimal { return RoundMoney(planned.Mul(q).Div(orderQty), currency) }
	return cum(qtyAfter).Sub(cum(qtyBefore))
}

// SettleWIP decomposes the order's actual cost against its estimate (BR-21.6,
// Settle) and makes the variances clear the posted WIP balance exactly: each
// variance is rounded to the currency's minor unit and the residual from
// per-line rounding of the postings lands on the largest one.
func SettleWIP(orderQty, rafQty decimal.Decimal, planned, actual []CostRow, wipBalance decimal.Decimal, currency string) ([]Variance, error) {
	st, err := Settle(orderQty, rafQty, planned, actual)
	if err != nil {
		return nil, err
	}
	vars := st.Variances
	sum := decimal.Zero
	largest := -1
	for i := range vars {
		vars[i].Amount = RoundMoney(vars[i].Amount, currency)
		sum = sum.Add(vars[i].Amount)
		if largest < 0 || vars[i].Amount.Abs().GreaterThan(vars[largest].Amount.Abs()) {
			largest = i
		}
	}
	if residual := wipBalance.Sub(sum); !residual.IsZero() {
		if largest < 0 {
			// No cost moved through the estimate's cells: the whole balance is substitution.
			return []Variance{{Type: VarSubstitution, Group: GroupMaterial, Amount: wipBalance}}, nil
		}
		vars[largest].Amount = vars[largest].Amount.Add(residual)
	}
	out := vars[:0]
	for _, v := range vars {
		if !v.Amount.IsZero() {
			out = append(out, v)
		}
	}
	return out, nil
}

// ActualLine is a posted journal line as costing sees it: an issue of an item
// (material) or hours on an operation (labor and overhead). Amounts are signed;
// a reversal line carries negative quantity and amounts.
type ActualLine struct {
	Material bool
	Key      string
	Qty      decimal.Decimal
	Amount   decimal.Decimal
	Overhead decimal.Decimal
}

// ActualRows sums posted lines into actual cost rows keyed like Estimate:
// the item id for material and resource@operation for labor and overhead.
func ActualRows(lines []ActualLine) []CostRow {
	rows := []CostRow{}
	at := map[cellKey]int{}
	add := func(g CostGroup, key string, qty, amount decimal.Decimal) {
		k := cellKey{g, key}
		i, seen := at[k]
		if !seen {
			i = len(rows)
			at[k] = i
			rows = append(rows, CostRow{Group: g, Key: key})
		}
		rows[i].Qty = rows[i].Qty.Add(qty)
		rows[i].Amount = rows[i].Amount.Add(amount)
	}
	for _, l := range lines {
		if l.Material {
			add(GroupMaterial, l.Key, l.Qty, l.Amount)
			continue
		}
		add(GroupLabor, l.Key, l.Qty, l.Amount)
		add(GroupOverhead, l.Key, l.Qty, l.Overhead)
	}
	out := rows[:0]
	for _, r := range rows {
		if !r.Qty.IsZero() || !r.Amount.IsZero() {
			out = append(out, r)
		}
	}
	return out
}

// OperationKey is the cost key of a route operation, matching Estimate.
func OperationKey(resourceID string, operNumber int) string {
	return fmt.Sprintf("%s@%d", resourceID, operNumber)
}

// JournalAllowed reports whether a journal type may post on an order in
// status s: picking lists and route cards on a Started or ReportedAsFinished
// order, report as finished on a Started or ReportedAsFinished order. A route
// card on a Released order auto-starts it when autoStart is set (FR-21.6).
func JournalAllowed(t JournalType, s Status, autoStart bool) bool {
	switch s {
	case Started, ReportedAsFinished:
		return true
	case Released:
		return t == JournalRouteCard && autoStart
	}
	return false
}
