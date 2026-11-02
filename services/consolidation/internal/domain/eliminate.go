package domain

import (
	"fmt"

	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/platform/money"
)

// Rule codes for the elimination entries (FR-40.9).
const (
	RuleE1 = "E1"
	RuleE2 = "E2"
	RuleE3 = "E3"
	RuleE4 = "E4"
	RuleE5 = "E5"
)

// Consolidation accounts used by the rules.
const (
	AcctICRevenue    = "IC_REVENUE"
	AcctICCOGS       = "IC_COGS"
	AcctICPayable    = "IC_PAYABLE"
	AcctICReceivable = "IC_RECEIVABLE"
	AcctCTA          = "CTA"
	AcctFXGainLoss   = "FX_GAIN_LOSS"
	AcctCOGS         = "COGS"
	AcctInventory    = "INVENTORY"
	AcctNCIEquity    = "NCI_EQUITY"
	AcctNCIProfit    = "NCI_PROFIT"
	AcctShareCapital = "SHARE_CAPITAL"
	AcctPreAcqRE     = "PRE_ACQ_RE"
	AcctGoodwill     = "GOODWILL"
	AcctInvestment   = "INVESTMENT"
)

// Entry is one consolidation line. Debit and Credit are never both non-zero.
type Entry struct {
	RuleCode string
	Account  string
	Debit    decimal.Decimal
	Credit   decimal.Decimal
}

// Pair holds the inputs for one parent and one subsidiary (the worked example
// in FR-40.9). Subsidiary amounts are in the subsidiary's currency; Currency is
// the group currency. A sale from the subsidiary to the parent is upstream
// (SellerIsSubsidiary) and its unrealized profit is shared with the NCI.
type Pair struct {
	Currency           string
	OwnershipPct       decimal.Decimal
	Rates              Rates
	ICSales            decimal.Decimal
	ICCost             decimal.Decimal
	UnsoldShare        decimal.Decimal // fraction of the IC goods still held by the parent
	ICReceivable       decimal.Decimal // subsidiary receivable from the parent
	PayableGroup       decimal.Decimal // parent payable to the subsidiary, group currency as recorded
	ShareCapital       decimal.Decimal // at acquisition
	PreAcqRE           decimal.Decimal // at acquisition
	InvestmentCost     decimal.Decimal // group currency
	NetIncome          decimal.Decimal
	SellerIsSubsidiary bool
	ICNetInvestment    bool   // IC balance is part of the net investment in the foreign operation (IAS 21.32)
	FXGainLossAccount  string // profit or loss account for IC exchange differences (IAS 21.45); AcctFXGainLoss when empty
}

// Eliminate builds the entries for E1–E5 and checks that every rule balances.
// Each line is rounded once to the group currency minor unit (BR-40.9); the
// balance holds by construction because every rule reuses the same rounded values.
func Eliminate(p Pair) ([]Entry, error) {
	if err := p.validate(); err != nil {
		return nil, err
	}
	cur := p.Currency
	var out []Entry

	// E1: intercompany revenue against intercompany COGS at the average rate.
	rev := round(p.ICSales.Mul(p.Rates.Average), cur)
	out = leg(out, RuleE1, AcctICRevenue, rev)
	out = leg(out, RuleE1, AcctICCOGS, rev.Neg())

	// E2: intercompany balances at the closing rate. The difference is profit or
	// loss (IAS 21.45) unless the balance is part of a net investment (IAS 21.32).
	ap := round(p.PayableGroup, cur)
	ar := round(p.ICReceivable.Mul(p.Rates.Closing), cur)
	out = leg(out, RuleE2, AcctICPayable, ap)
	out = leg(out, RuleE2, AcctICReceivable, ar.Neg())
	out = leg(out, RuleE2, p.icDifferenceAccount(), ar.Sub(ap))

	// E3: unrealized profit in closing inventory at the average rate. Upstream
	// profit is shared with the NCI; downstream profit belongs to the parent.
	if p.UnsoldShare.IsPositive() {
		profit := p.ICSales.Sub(p.ICCost).Mul(p.UnsoldShare)
		up := round(profit.Mul(p.Rates.Average), cur)
		out = leg(out, RuleE3, AcctCOGS, up)
		out = leg(out, RuleE3, AcctInventory, up.Neg())
		if p.SellerIsSubsidiary {
			nci := round(NCIShare(up, p.OwnershipPct), cur)
			out = leg(out, RuleE3, AcctNCIEquity, nci)
			out = leg(out, RuleE3, AcctNCIProfit, nci.Neg())
		}
	}

	// E4: investment against subsidiary equity at acquisition, goodwill as the plug
	// and retranslated at the closing rate (IAS 21.47) with the difference to CTA.
	sc := round(p.ShareCapital.Mul(p.Rates.Historical), cur)
	re := round(p.PreAcqRE.Mul(p.Rates.Historical), cur)
	inv := round(p.InvestmentCost, cur)
	nci := round(NCIShare(sc.Add(re), p.OwnershipPct), cur)
	goodwill := inv.Add(nci).Sub(sc).Sub(re)
	if goodwill.IsNegative() {
		return nil, &Error{Code: CodeBargainPurchase, Detail: fmt.Sprintf("investment and NCI are below acquired equity by %s; bargain purchase is not supported", goodwill.Neg().StringFixed(2))}
	}
	out = leg(out, RuleE4, AcctShareCapital, sc)
	out = leg(out, RuleE4, AcctPreAcqRE, re)
	closingGoodwill := round(goodwill.Div(p.Rates.Historical).Mul(p.Rates.Closing), cur)
	out = leg(out, RuleE4, AcctGoodwill, closingGoodwill)
	out = leg(out, RuleE4, AcctCTA, goodwill.Sub(closingGoodwill))
	out = leg(out, RuleE4, AcctInvestment, inv.Neg())
	out = leg(out, RuleE4, AcctNCIEquity, nci.Neg())

	// E5: NCI share of the subsidiary's current-year profit.
	nciProfit := round(NCIShare(round(p.NetIncome.Mul(p.Rates.Average), cur), p.OwnershipPct), cur)
	out = leg(out, RuleE5, AcctNCIProfit, nciProfit)
	out = leg(out, RuleE5, AcctNCIEquity, nciProfit.Neg())

	if err := CheckBalanced(out); err != nil {
		return nil, err
	}
	return out, nil
}

func (p Pair) icDifferenceAccount() string {
	switch {
	case p.ICNetInvestment:
		return AcctCTA
	case p.FXGainLossAccount != "":
		return p.FXGainLossAccount
	}
	return AcctFXGainLoss
}

// leg appends a line with a signed debit: positive debits go to Debit, negative
// ones to Credit, and zero is dropped.
func leg(out []Entry, rule, account string, debit decimal.Decimal) []Entry {
	switch {
	case debit.IsZero():
		return out
	case debit.IsNegative():
		return append(out, Entry{RuleCode: rule, Account: account, Credit: debit.Neg()})
	}
	return append(out, Entry{RuleCode: rule, Account: account, Debit: debit})
}

// CheckBalanced verifies that debits equal credits for each rule and overall (CNS-0903).
func CheckBalanced(entries []Entry) error {
	net := map[string]decimal.Decimal{}
	total := decimal.Zero
	for _, e := range entries {
		n := e.Debit.Sub(e.Credit)
		net[e.RuleCode] = net[e.RuleCode].Add(n)
		total = total.Add(n)
	}
	for rule, n := range net {
		if !n.IsZero() {
			return &Error{Code: CodeUnbalancedRule, Detail: fmt.Sprintf("rule %s unbalanced by %s", rule, n.Abs().StringFixed(2))}
		}
	}
	if !total.IsZero() {
		return &Error{Code: CodeUnbalancedRule, Detail: "entries unbalanced overall"}
	}
	return nil
}

// Totals returns the sum of debits and of credits.
func Totals(entries []Entry) (debit, credit decimal.Decimal) {
	debit, credit = decimal.Zero, decimal.Zero
	for _, e := range entries {
		debit = debit.Add(e.Debit)
		credit = credit.Add(e.Credit)
	}
	return debit, credit
}

func (p Pair) validate() error {
	switch {
	case len(p.Currency) != 3:
		return invalid("group currency must be an ISO 4217 code")
	case !p.OwnershipPct.IsPositive() || p.OwnershipPct.GreaterThan(decimal.NewFromInt(100)):
		return invalid("ownership must be above 0 and at most 100 percent")
	case !p.Rates.Closing.IsPositive() || !p.Rates.Average.IsPositive() || !p.Rates.Historical.IsPositive():
		return invalid("closing, average and historical rates must be positive")
	case p.UnsoldShare.IsNegative() || p.UnsoldShare.GreaterThan(decimal.NewFromInt(1)):
		return invalid("unsold share must be between 0 and 1")
	case p.InvestmentCost.IsNegative():
		return invalid("investment cost must not be negative")
	case p.ICSales.IsNegative() || p.ICCost.IsNegative() || p.ICReceivable.IsNegative() || p.PayableGroup.IsNegative() || p.ShareCapital.IsNegative():
		return invalid("IC sales, IC cost, IC balances and share capital must not be negative")
	}
	return nil
}

func round(d decimal.Decimal, currency string) decimal.Decimal {
	return money.Amount{Value: d, Currency: currency}.Round().Value
}
