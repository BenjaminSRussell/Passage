package domain

import (
	"github.com/shopspring/decimal"
)

// JournalSplitTransfer is the system journal a split of a Started order posts
// on both orders to move posted consumption, hours and WIP (BR-21.11). Users
// cannot create it, so it is not a Valid journal type.
const JournalSplitTransfer JournalType = "split-transfer"

// PostSplitTransfer is the counter row of a split transfer. Both orders post
// it on the profile's WIP account with WIP false, so the transfer nets to zero
// in the general ledger while each order's own WIP balance moves.
const PostSplitTransfer = "split-transfer"

// Allocation is how a split of a Started order shares what was posted before
// it (FR-21.15, BR-21.11).
type Allocation string

const (
	// AllocProportional moves the split's share of posted consumption, hours and WIP.
	AllocProportional Allocation = "proportional"
	// AllocKeepOnOriginal leaves everything posted on the original order.
	AllocKeepOnOriginal Allocation = "keep-on-original"
)

// Valid reports whether a is an allocation this service knows.
func (a Allocation) Valid() bool { return a == AllocProportional || a == AllocKeepOnOriginal }

// TransferLineIn is a BOM line's posted consumption: its quantity and the value
// the posted issue lines carry.
type TransferLineIn struct {
	LineNo string
	ItemID string
	Qty    decimal.Decimal
	Amount decimal.Decimal
}

// TransferOpIn is an operation's posted feedback: hours, good and error
// quantities and the labor and overhead the posted route cards carry.
type TransferOpIn struct {
	OperNumber int
	Hours      decimal.Decimal
	Good       decimal.Decimal
	Error      decimal.Decimal
	Labor      decimal.Decimal
	Overhead   decimal.Decimal
}

// TransferLine is what moves for one BOM line (Kind issue) or operation (Kind
// hours) from the original to the new order.
type TransferLine struct {
	Kind       string
	LineNo     string
	ItemID     string
	OperNumber int
	Qty        decimal.Decimal
	Good       decimal.Decimal
	Error      decimal.Decimal
	Amount     decimal.Decimal
	Overhead   decimal.Decimal
}

// Transfer is the whole move with its totals by WIP group.
type Transfer struct {
	Lines    []TransferLine
	Material decimal.Decimal
	Labor    decimal.Decimal
	Overhead decimal.Decimal
}

// Empty reports whether nothing moves.
func (t Transfer) Empty() bool { return len(t.Lines) == 0 }

// exactShare is x × splitQty ÷ orderQty to 24 places, before any rounding.
func exactShare(x, splitQty, orderQty decimal.Decimal) decimal.Decimal {
	return x.Mul(splitQty).DivRound(orderQty, 24)
}

// share is x × splitQty ÷ orderQty rounded to places decimals.
func share(x, splitQty, orderQty decimal.Decimal, places int32) decimal.Decimal {
	return exactShare(x, splitQty, orderQty).Round(places)
}

// moneyShare is the share of an amount rounded once to the currency's minor unit.
func moneyShare(x, splitQty, orderQty decimal.Decimal, currency string) decimal.Decimal {
	return RoundMoney(exactShare(x, splitQty, orderQty), currency)
}

// PlanTransfer computes the share of the posted consumption, hours and amounts
// that moves to the new order of a split (BR-21.11): with r = splitQty ÷
// orderQty, quantities move at round(x × r, 6), hours at round(x × r, 2) (the
// precision they are stored in) and amounts at round(x × r) in the currency's
// minor unit. The rounding residual stays on the original, so the two orders
// always add up to what was posted. Lines and operations whose share rounds to
// nothing are left out.
func PlanTransfer(orderQty, splitQty decimal.Decimal, currency string, lines []TransferLineIn, ops []TransferOpIn) Transfer {
	var t Transfer
	for _, l := range lines {
		qty, amount := share(l.Qty, splitQty, orderQty, QtyScale), moneyShare(l.Amount, splitQty, orderQty, currency)
		if qty.IsZero() && amount.IsZero() {
			continue
		}
		t.Lines = append(t.Lines, TransferLine{Kind: "issue", LineNo: l.LineNo, ItemID: l.ItemID, Qty: qty, Amount: amount})
		t.Material = t.Material.Add(amount)
	}
	for _, o := range ops {
		hours, good, bad := share(o.Hours, splitQty, orderQty, HoursScale), share(o.Good, splitQty, orderQty, QtyScale), share(o.Error, splitQty, orderQty, QtyScale)
		labor, overhead := moneyShare(o.Labor, splitQty, orderQty, currency), moneyShare(o.Overhead, splitQty, orderQty, currency)
		if hours.IsZero() && good.IsZero() && bad.IsZero() && labor.IsZero() && overhead.IsZero() {
			continue
		}
		t.Lines = append(t.Lines, TransferLine{Kind: "hours", OperNumber: o.OperNumber, Qty: hours, Good: good, Error: bad, Amount: labor, Overhead: overhead})
		t.Labor, t.Overhead = t.Labor.Add(labor), t.Overhead.Add(overhead)
	}
	return t
}

// TransferPostings books the transfer on one order: out takes the moved WIP off
// the original (Cr WIP / Dr transfer), in puts it on the new order (Dr WIP / Cr
// transfer), by cost group so each order's WIP balance by group stays right.
func TransferPostings(p Profile, t Transfer, out bool) []Posting {
	var ps []Posting
	for _, g := range []struct {
		typ    string
		amount decimal.Decimal
	}{{PostWIPMaterial, t.Material}, {PostWIPLabor, t.Labor}, {PostWIPOverhead, t.Overhead}} {
		if g.amount.IsZero() {
			continue
		}
		wip := Posting{Type: g.typ, Account: p.WIP, WIP: true}
		counter := Posting{Type: PostSplitTransfer, Account: p.WIP}
		if out {
			wip.Cr, counter.Dr = g.amount, g.amount
		} else {
			wip.Dr, counter.Cr = g.amount, g.amount
		}
		ps = append(ps, wip, counter)
	}
	return ps
}

// SplitJobHours divides the moved hours of an operation reported on job cards
// between its setup and process jobs (FR-21.6 with BR-21.11): the setup job moves
// its own share, rounded like the operation's hours, and the process job the rest
// of the operation's moved hours, so on both orders the jobs still add up to the
// operation. Neither moves more than it holds.
func SplitJobHours(setupHours, processHours, movedHours, splitQty, orderQty decimal.Decimal) (setup, process decimal.Decimal) {
	setup = decimal.Min(share(setupHours, splitQty, orderQty, HoursScale), movedHours, setupHours)
	process = decimal.Min(decimal.Max(movedHours.Sub(setup), decimal.Zero), processHours)
	return setup, process
}
