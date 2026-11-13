package domain

import (
	"fmt"
	"sort"
	"time"

	"github.com/shopspring/decimal"
)

// UnitTerms are the stored acquisition terms of a subsidiary: the group's investment in it and what the run cannot read from a
// trial balance (share capital and retained earnings at the acquisition, the rate of that day, which way the IC goods flow).
type UnitTerms struct {
	InvestmentCost     decimal.Decimal // group currency
	ShareCapital       decimal.Decimal // unit currency, at acquisition
	PreAcquisitionRE   decimal.Decimal // unit currency, at acquisition
	HistoricalRate     decimal.Decimal
	SellerIsSubsidiary bool
	ICNetInvestment    bool
	FXGainLossAccount  string
	// Method is how the group consolidates the unit: MethodFull (the default) line by line with eliminations E1 to E6, or
	// MethodEquity for an associate or joint venture, which the equity method takes into one line (FR-40.18).
	Method string
	// ICReceivableAccount is the main account in the unit's own ledger that holds its receivable from the holder (the parent); a
	// run reads the IC receivable of the pair from it instead of from every ic_receivable account of the unit.
	ICReceivableAccount string
	// ParentPayableAccount is the main account in the holder's ledger that holds the holder's payable to this unit; a run reads
	// the pair's payable (E2, group currency as collected) from it, so the request no longer has to give it.
	ParentPayableAccount string
}

// Consolidation methods of a unit.
const (
	MethodFull   = "full"
	MethodEquity = "equity"
)

// ValidMethod reports whether m is a consolidation method or empty (full).
func ValidMethod(m string) bool { return m == "" || m == MethodFull || m == MethodEquity }

// ICOverride supplies the pair amounts a trial balance cannot attribute to one partner: the parent's payable to this unit in
// group currency and the share of the IC goods still unsold, and may replace any amount read from the unit's own balances. A
// nil field leaves the derived value.
type ICOverride struct {
	ICSales      *decimal.Decimal
	ICCost       *decimal.Decimal
	UnsoldShare  *decimal.Decimal
	ICReceivable *decimal.Decimal
	PayableGroup *decimal.Decimal
}

// OpeningBasis is what a run takes from the collection at the end of the unit's previous fiscal year: the unit's net assets and
// the closing rate of its currency then.
type OpeningBasis struct {
	NetAssets decimal.Decimal
	Rate      decimal.Decimal
}

// DeriveInput is everything RunUnits needs.
type DeriveInput struct {
	GroupCurrency string
	Period        string // YYYY-MM
	Units         []Unit
	Lines         []CollectLine
	// Rates are the average and closing rates the collection translated at, per unit currency.
	Rates map[string]CollectRate
	Terms map[string]UnitTerms
	// Opening is the basis of each unit's opening net assets and opening rate, by unit id, when a collection of its previous
	// fiscal year end exists.
	Opening map[string]OpeningBasis
	// OpeningRates fills the opening rate of a currency the previous collection did not give.
	OpeningRates map[string]decimal.Decimal
	IC           map[string]ICOverride
	// Layers are the IC receipt layers still held at the end of the period, by the id of the subsidiary that sold them. A unit with
	// layers has its E3 unrealized profit summed over them instead of from the unsold share.
	Layers map[string][]ICLayer
	// PreviousCarrying is the carrying amount (group currency) of each equity-method unit's investment at the end of its previous
	// fiscal year; a unit without one starts from its investment cost.
	PreviousCarrying map[string]decimal.Decimal
	// PreviousCarryingUnit is the same carrying amount in the associate's own currency, when the register holds it (a foreign
	// associate, IAS 21.44 and IAS 28): it is the base the carrying amount is retranslated from.
	PreviousCarryingUnit map[string]decimal.Decimal
}

// Derived is what a collection gives a run.
type Derived struct {
	Units      []RunUnit
	Associates []RunAssociate
	// Warnings are what the run did on a fallback, such as the seller's margin for a layer with no group cost (CNS-0908).
	Warnings []string
}

// periodEnd is the last day of the period (YYYY-MM).
func periodEnd(period string) (time.Time, error) {
	first, err := time.Parse("2006-01", period)
	if err != nil {
		return time.Time{}, invalid("period must be YYYY-MM")
	}
	return first.AddDate(0, 1, -1), nil
}

// RunUnits derives the units of a consolidation run from a stored collection (FR-40.10): the subsidiaries are the units the
// group holds a share of on the last day of the period; each one's net income and net assets come from its collected balances
// by account role, its rates from the collection (closing, average) and the previous fiscal year end (opening), its
// acquisition figures from its stored terms, and its IC amounts from its balances with the overrides applied.
//
//	net income      = - sum of the unit's income accounts (debit less credit, unit currency)
//	closing assets  = - sum of the unit's equity accounts - dividends declared + net income
//	opening assets  = closing assets at the previous fiscal year end, or closing less net income plus dividends declared when there is none
func RunUnits(in DeriveInput) ([]RunUnit, error) {
	d, err := DeriveRun(in)
	return d.Units, err
}

// DeriveRun is RunUnits that also derives the equity-method units, the warnings and takes the layers and dividends into account:
// a unit whose role dividend_paid holds a debit balance gets E6, an associate gets the equity method, and the net assets of a
// unit are its equity less the dividends it declared plus its profit.
func DeriveRun(in DeriveInput) (Derived, error) {
	end, err := periodEnd(in.Period)
	if err != nil {
		return Derived{}, err
	}
	byUnit := map[string][]CollectLine{}
	for _, l := range in.Lines {
		byUnit[l.UnitID] = append(byUnit[l.UnitID], l)
	}
	one := decimal.NewFromInt(1)
	var out Derived
	for _, u := range in.Units {
		pct := u.PercentOn(end)
		if !pct.IsPositive() {
			continue // the parent, or a unit the group no longer holds
		}
		fail := func(code, detail string) error { return &Error{Code: code, Detail: "unit " + u.ID + ": " + detail} }
		terms, ok := in.Terms[u.ID]
		if !ok {
			return Derived{}, fail(CodeUnitTerms, "has no acquisition terms; store its investment cost, share capital, retained earnings and rate at acquisition first")
		}
		lines := byUnit[u.ID]
		if len(lines) == 0 {
			return Derived{}, fail(CodeRunFromCollection, "has no balances in the collection")
		}
		var income, equity, dividends, icSales, icCost, icRecv decimal.Decimal
		hasEquity := false
		for _, l := range lines {
			if l.Type == AccountIncome {
				income = income.Add(l.UnitAmount)
			}
			switch l.Role {
			case RoleEquity:
				equity, hasEquity = equity.Add(l.UnitAmount), true
			case RoleDividendPaid:
				dividends = dividends.Add(l.UnitAmount)
			case RoleICSales:
				icSales = icSales.Add(l.UnitAmount)
			case RoleICCost:
				icCost = icCost.Add(l.UnitAmount)
			case RoleICReceivable:
				icRecv = icRecv.Add(l.UnitAmount)
			}
		}
		if !hasEquity {
			return Derived{}, fail(CodeRunFromCollection, "has no balance on an account with the role equity, so its net assets cannot be read; map its equity accounts with the role equity")
		}
		profit := income.Neg()
		closing := equity.Neg().Sub(dividends).Add(profit)

		rates := Rates{Closing: one, Average: one, Opening: one, Historical: terms.HistoricalRate}
		if u.Currency != in.GroupCurrency {
			r, ok := in.Rates[u.Currency]
			if !ok || !r.Closing.IsPositive() || !r.Average.IsPositive() {
				return Derived{}, fail(CodeCollectionError, "the collection holds no average and closing rate for "+u.Currency)
			}
			rates.Closing, rates.Average = r.Closing, r.Average
			rates.Opening = in.OpeningRates[u.Currency]
			if b, ok := in.Opening[u.ID]; ok && b.Rate.IsPositive() {
				rates.Opening = b.Rate
			}
			if terms.Method != MethodEquity && !rates.Opening.IsPositive() {
				return Derived{}, fail(CodeRunFromCollection, "needs the opening rate of "+u.Currency+": collect the end of its previous fiscal year, or give the opening rate")
			}
		}

		if terms.Method == MethodEquity {
			a, err := deriveAssociate(in, u, pct, terms, rates, profit, dividends)
			if err != nil {
				return Derived{}, fail(CodeRunFromCollection, err.Error())
			}
			out.Associates = append(out.Associates, a)
			continue
		}

		opening := closing.Sub(profit).Add(dividends)
		if b, ok := in.Opening[u.ID]; ok {
			opening = b.NetAssets
		}
		pair := Pair{
			Currency: in.GroupCurrency, OwnershipPct: pct, Rates: rates,
			ICSales: icSales.Neg(), ICCost: icCost, ICReceivable: icRecv,
			ShareCapital: terms.ShareCapital, PreAcqRE: terms.PreAcquisitionRE, InvestmentCost: terms.InvestmentCost,
			NetIncome: profit, SellerIsSubsidiary: terms.SellerIsSubsidiary, ICNetInvestment: terms.ICNetInvestment, FXGainLossAccount: terms.FXGainLossAccount,
			Dividend: dividends,
		}
		if err := readPairAccounts(&pair, in, u, terms, end); err != nil {
			return Derived{}, fail(CodeRunFromCollection, err.Error())
		}
		if o, ok := in.IC[u.ID]; ok {
			if terms.ICReceivableAccount != "" && o.ICReceivable != nil {
				return Derived{}, fail(CodeRunFromCollection, "its IC receivable is read from account "+terms.ICReceivableAccount+" of its ledger; leave icReceivable out of the request")
			}
			if terms.ParentPayableAccount != "" && o.PayableGroup != nil {
				return Derived{}, fail(CodeRunFromCollection, "the payable to it is read from account "+terms.ParentPayableAccount+" of its holder's ledger; leave payableGroup out of the request")
			}
			set := func(dst *decimal.Decimal, v *decimal.Decimal) {
				if v != nil {
					*dst = *v
				}
			}
			set(&pair.ICSales, o.ICSales)
			set(&pair.ICCost, o.ICCost)
			set(&pair.UnsoldShare, o.UnsoldShare)
			set(&pair.ICReceivable, o.ICReceivable)
			set(&pair.PayableGroup, o.PayableGroup)
		}
		if layers := in.Layers[u.ID]; len(layers) > 0 {
			// The seller's margin is the fallback for a layer with no group cost (BR-40.7).
			margin := decimal.Zero
			if pair.ICSales.IsPositive() {
				margin = pair.ICSales.Sub(pair.ICCost).Div(pair.ICSales).Mul(decimal.NewFromInt(100)).Round(4)
				if margin.IsNegative() {
					margin = decimal.Zero
				}
			}
			profits, err := UnrealizedProfitLayers(layers, u.Currency, margin)
			if err != nil {
				return Derived{}, fail(CodeRunFromCollection, err.Error())
			}
			total := profits.Total
			pair.UnrealizedProfit = &total
			if w := profits.Warning(); w != nil {
				out.Warnings = append(out.Warnings, "unit "+u.ID+": "+w.Error())
			}
		}
		out.Units = append(out.Units, RunUnit{ID: u.ID, Pair: pair, OpeningNetAssets: opening, ClosingNetAssets: closing})
	}
	if len(out.Units) == 0 && len(out.Associates) == 0 {
		return Derived{}, &Error{Code: CodeRunFromCollection, Detail: "the group holds no unit on the last day of " + in.Period + ", so there is nothing to consolidate"}
	}
	sort.Slice(out.Units, func(i, j int) bool { return out.Units[i].ID < out.Units[j].ID })
	sort.Slice(out.Associates, func(i, j int) bool { return out.Associates[i].ID < out.Associates[j].ID })
	return out, nil
}

// readPairAccounts reads the IC pair amounts of a subsidiary from the collected ledgers when its terms name the pair accounts
// (FR-40.6, BR-40.15): the receivable from the unit's own account (unit currency, debit balance) and the payable from the holder's
// account (credit balance, in the group currency as collected). A pair account with no balance in the collection reads as zero; one
// the holder's ledger does not hold in the group currency is refused, because E2 takes the payable in the group currency.
func readPairAccounts(pair *Pair, in DeriveInput, u Unit, terms UnitTerms, end time.Time) error {
	if terms.ICReceivableAccount != "" {
		recv := decimal.Zero
		for _, l := range in.Lines {
			if l.UnitID == u.ID && l.MainAccount == terms.ICReceivableAccount {
				recv = recv.Add(l.UnitAmount)
			}
		}
		if recv.IsNegative() {
			return fmt.Errorf("account %s holds a credit balance of %s, so it is not a receivable from the holder", terms.ICReceivableAccount, recv.Neg().String())
		}
		pair.ICReceivable = recv
	}
	if terms.ParentPayableAccount != "" {
		holders := map[string]bool{}
		for _, h := range u.Holdings {
			if inForce(OwnershipInterval{From: h.From, To: h.To, Percent: h.Percent}, end) {
				holders[h.HolderUnitID] = true
			}
		}
		currency := map[string]string{}
		for _, x := range in.Units {
			currency[x.ID] = x.Currency
		}
		payable := decimal.Zero
		for _, l := range in.Lines {
			if !holders[l.UnitID] || l.MainAccount != terms.ParentPayableAccount {
				continue
			}
			if currency[l.UnitID] != in.GroupCurrency {
				return fmt.Errorf("the holder %s keeps its ledger in %s; the payable to the unit is read in the group currency %s, so give payableGroup in the request instead", l.UnitID, currency[l.UnitID], in.GroupCurrency)
			}
			payable = payable.Sub(l.GroupAmount)
		}
		if payable.IsNegative() {
			return fmt.Errorf("account %s of the holder holds a debit balance of %s, so it is not a payable to the unit", terms.ParentPayableAccount, payable.Neg().String())
		}
		pair.PayableGroup = payable
	}
	return nil
}

// deriveAssociate reads an equity-method unit: the group's share of its profit and of its dividends in the group currency, at the
// average rate, and the investment's carrying amount at the start of the fiscal year.
func deriveAssociate(in DeriveInput, u Unit, pct decimal.Decimal, terms UnitTerms, rates Rates, profit, dividends decimal.Decimal) (RunAssociate, error) {
	if pct.GreaterThan(decimal.NewFromInt(50)) {
		return RunAssociate{}, fmt.Errorf("the group holds %s percent, so it controls the unit; the equity method applies up to 50 percent, so set its method to full", pct.String())
	}
	opening, ok := in.PreviousCarrying[u.ID]
	if !ok {
		opening = terms.InvestmentCost
	}
	a := Associate{
		Currency: in.GroupCurrency, OwnershipPct: pct, OpeningCarrying: opening,
		NetIncome:         round(profit.Mul(rates.Average), in.GroupCurrency),
		DividendsReceived: round(dividends.Mul(pct).Div(decimal.NewFromInt(100)).Mul(rates.Average), in.GroupCurrency),
	}
	if u.Currency != in.GroupCurrency {
		// A foreign associate is a foreign operation (IAS 21.44, IAS 28.25): its carrying amount is kept in its own currency and
		// retranslated at the closing rate, the difference going to the translation reserve.
		// A register line carries the investment from last year's closing rate, which is this year's opening rate. Without one the
		// run starts from the investment cost, which the group booked at the rate of acquisition: the opening is translated at that
		// rate, so the opening carrying amount is the cost exactly and the whole move to the closing rate goes to the reserve.
		openRate := rates.Opening
		fc, ok := in.PreviousCarryingUnit[u.ID]
		fromRegister := ok || hasPrev(in.PreviousCarrying, u.ID)
		if !fromRegister {
			openRate = terms.HistoricalRate
		}
		if !openRate.IsPositive() {
			return RunAssociate{}, fmt.Errorf("needs the opening rate of %s to retranslate the investment: collect the end of its previous fiscal year, give the opening rate, or store the rate at acquisition", u.Currency)
		}
		switch {
		case ok:
		case fromRegister:
			fc = opening.Div(openRate) // a register line from before retranslation: its group amount was at last year's closing rate
		default:
			fc = terms.InvestmentCost.Div(openRate)
		}
		a.Foreign = &ForeignAssociate{UnitCurrency: u.Currency, OpeningCarrying: fc, NetIncome: profit, Dividends: dividends.Mul(pct).Div(decimal.NewFromInt(100)),
			OpeningRate: openRate, AverageRate: rates.Average, ClosingRate: rates.Closing}
		a.OpeningCarrying = round(fc.Mul(openRate), in.GroupCurrency)
	}
	return RunAssociate{ID: u.ID, Associate: a, Cost: terms.InvestmentCost}, nil
}

func hasPrev(m map[string]decimal.Decimal, id string) bool {
	_, ok := m[id]
	return ok
}

// NetAssets reads a unit's closing net assets and net income (unit currency) from its collected lines the way RunUnits does.
// ok is false when the unit has no equity balance.
func NetAssets(lines []CollectLine) (closing, profit decimal.Decimal, ok bool) {
	var income, equity, dividends decimal.Decimal
	for _, l := range lines {
		if l.Type == AccountIncome {
			income = income.Add(l.UnitAmount)
		}
		switch l.Role {
		case RoleEquity:
			equity, ok = equity.Add(l.UnitAmount), true
		case RoleDividendPaid:
			dividends = dividends.Add(l.UnitAmount)
		}
	}
	profit = income.Neg()
	return equity.Neg().Sub(dividends).Add(profit), profit, ok
}

// UnitValidations evaluates the balance sheet rule of the monitor (FR-40.20) over each unit's collected balances: assets less
// liabilities and equity (equity including the year's profit) must be within the rule's tolerance. A unit with unmapped or
// role-less balance sheet accounts shows the difference they leave.
func UnitValidations(lines []CollectLine, rules []ValidationRule) (map[string][]RuleResult, error) {
	byUnit := map[string]UnitFS{}
	for _, l := range lines {
		fs := byUnit[l.UnitID]
		switch l.Role {
		case RoleAsset:
			fs.TotalAssets = fs.TotalAssets.Add(l.UnitAmount)
		case RoleLiability:
			fs.TotalLiabilities = fs.TotalLiabilities.Sub(l.UnitAmount)
		case RoleEquity:
			fs.TotalEquity = fs.TotalEquity.Sub(l.UnitAmount)
		case RoleDividendPaid:
			fs.TotalEquity = fs.TotalEquity.Sub(l.UnitAmount) // declared dividends are a debit against equity
		}
		if l.Type == AccountIncome {
			fs.NetIncome = fs.NetIncome.Sub(l.UnitAmount)
		}
		byUnit[l.UnitID] = fs
	}
	out := map[string][]RuleResult{}
	for id, fs := range byUnit {
		fs.TotalEquity = fs.TotalEquity.Add(fs.NetIncome)
		res, err := EvaluateRules(rules, fs)
		if err != nil {
			return nil, fmt.Errorf("unit %s: %w", id, err)
		}
		out[id] = res
	}
	return out, nil
}
