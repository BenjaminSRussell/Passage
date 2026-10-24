package domain

import (
	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/platform/money"
)

type Status string

const (
	NotMatched      Status = "NOT_MATCHED"
	Matched         Status = "MATCHED"
	PriceVariance   Status = "PRICE_VARIANCE"
	QtyVariance     Status = "QTY_VARIANCE"
	TotalVariance   Status = "TOTAL_VARIANCE"
	NoReceipt       Status = "NO_RECEIPT"
	WaitingReceipt  Status = "WAITING_RECEIPT"
	PostingPPV             = "PPV"
	PostingSmallDif        = "SMALL_DIFF"
)

var statusRank = map[Status]int{
	Matched: 0, NotMatched: 1, WaitingReceipt: 2, NoReceipt: 3, QtyVariance: 4, PriceVariance: 5, TotalVariance: 6,
}

type MatchLevel string

const (
	MatchNone     MatchLevel = "NONE"
	MatchTwoWay   MatchLevel = "TWO_WAY"
	MatchThreeWay MatchLevel = "THREE_WAY"
)

type PriceMode string

const (
	UpperOnly PriceMode = "UPPER_ONLY"
	Both      PriceMode = "BOTH"
)

type Policy struct {
	MatchLevel   MatchLevel
	PriceTolPct  decimal.Decimal
	PriceTolAbs  decimal.Decimal
	QtyTolPct    decimal.Decimal
	TotalTolPct  decimal.Decimal
	TotalTolAbs  decimal.Decimal
	SmallDiffAbs decimal.Decimal
	PriceTolMode PriceMode
}

func DefaultPolicy() Policy {
	return Policy{
		MatchLevel:   MatchThreeWay,
		PriceTolPct:  decimal.RequireFromString("2.0"),
		PriceTolAbs:  decimal.RequireFromString("25.00"),
		QtyTolPct:    decimal.RequireFromString("5.0"),
		TotalTolPct:  decimal.RequireFromString("1.0"),
		TotalTolAbs:  decimal.RequireFromString("50.00"),
		SmallDiffAbs: decimal.RequireFromString("0.50"),
		PriceTolMode: UpperOnly,
	}
}

type InvoiceLine struct {
	Qty                decimal.Decimal
	UnitPrice          decimal.Decimal
	POUnitPrice        decimal.Decimal
	POCurrency         string
	ReceiptState       ReceiptState
	ReceivedUninvoiced decimal.Decimal
	POQtyRemaining     decimal.Decimal
}

type Invoice struct {
	Currency    string
	HeaderTotal decimal.Decimal
	Charges     decimal.Decimal
	Tax         decimal.Decimal
	Lines       []InvoiceLine
}

type LineResult struct {
	Status           Status
	PriceVariance    money.Amount
	CurrencyMismatch bool
}

type Posting struct {
	Kind   string
	Amount money.Amount
}

type Result struct {
	Status        Status
	Lines         []LineResult
	TotalVariance money.Amount
	Postings      []Posting
}

var hundred = decimal.NewFromInt(100)

func withinTolerance(diff, base, pct, abs decimal.Decimal) bool {
	d := diff.Abs()
	if d.LessThanOrEqual(abs) {
		return true
	}
	return base.Sign() > 0 && d.Mul(hundred).DivRound(base, 4).LessThanOrEqual(pct)
}

func worse(a, b Status) Status {
	if statusRank[b] > statusRank[a] {
		return b
	}
	return a
}

func roundedMoney(v decimal.Decimal, currency string) money.Amount {
	return money.Amount{Value: v.RoundBank(money.MinorUnits(currency)), Currency: currency}
}

func evaluateLine(p Policy, currency string, l InvoiceLine) LineResult {
	zero := money.Amount{Value: decimal.Zero, Currency: currency}
	if p.MatchLevel == MatchNone {
		return LineResult{Status: NotMatched, PriceVariance: zero}
	}
	if l.POCurrency != "" && l.POCurrency != currency {
		return LineResult{Status: PriceVariance, PriceVariance: zero, CurrencyMismatch: true}
	}
	limit := l.POQtyRemaining
	if p.MatchLevel == MatchThreeWay {
		switch l.ReceiptState {
		case Posted:
			limit = l.ReceivedUninvoiced
		case "":
			return LineResult{Status: NoReceipt, PriceVariance: zero}
		default:
			return LineResult{Status: WaitingReceipt, PriceVariance: zero}
		}
	}
	status := Matched
	if l.Qty.GreaterThan(limit.Mul(decimal.NewFromInt(1).Add(p.QtyTolPct.Div(hundred)))) {
		status = QtyVariance
	}
	diff := l.UnitPrice.Sub(l.POUnitPrice)
	failsPrice := !withinTolerance(diff, l.POUnitPrice, p.PriceTolPct, p.PriceTolAbs)
	if p.PriceTolMode == UpperOnly && diff.Sign() < 0 {
		failsPrice = false
	}
	if failsPrice {
		status = worse(status, PriceVariance)
	}
	return LineResult{Status: status, PriceVariance: roundedMoney(diff.Mul(l.Qty), currency)}
}

func Evaluate(p Policy, in Invoice) Result {
	res := Result{Status: Matched, Lines: make([]LineResult, 0, len(in.Lines))}
	sum := decimal.Zero
	for _, l := range in.Lines {
		lr := evaluateLine(p, in.Currency, l)
		res.Lines = append(res.Lines, lr)
		res.Status = worse(res.Status, lr.Status)
		sum = sum.Add(l.Qty.Mul(l.UnitPrice))
	}
	variance := in.HeaderTotal.Sub(sum).Sub(in.Charges).Sub(in.Tax)
	res.TotalVariance = roundedMoney(variance, in.Currency)
	if p.MatchLevel != MatchNone && !withinTolerance(variance, sum, p.TotalTolPct, p.TotalTolAbs) {
		res.Status = worse(res.Status, TotalVariance)
	}
	if res.Status != Matched {
		return res
	}
	for _, lr := range res.Lines {
		if !lr.PriceVariance.IsZero() {
			res.Postings = append(res.Postings, Posting{Kind: PostingPPV, Amount: lr.PriceVariance})
		}
	}
	if variance.Sign() != 0 && variance.Abs().LessThanOrEqual(p.SmallDiffAbs) {
		res.Postings = append(res.Postings, Posting{Kind: PostingSmallDif, Amount: res.TotalVariance})
	}
	return res
}
