package domain

import "github.com/shopspring/decimal"

// Rates are the subsidiary-to-group rates for one period (IAS 21).
// Closing translates assets and liabilities, Average translates income and
// expense, Opening translates opening net assets and Historical translates
// equity at acquisition.
type Rates struct {
	Closing    decimal.Decimal
	Average    decimal.Decimal
	Opening    decimal.Decimal
	Historical decimal.Decimal
}

// Statement is a unit's net assets and net income in its functional currency.
type Statement struct {
	OpeningNetAssets decimal.Decimal
	NetIncome        decimal.Decimal
	ClosingNetAssets decimal.Decimal
	// Dividends is what the unit declared to its shareholders in the year (unit currency): it left the net assets at the average
	// rate, so it is not part of the translation difference.
	Dividends decimal.Decimal
}

// Translated is a statement in group currency plus the CTA that closes it.
type Translated struct {
	OpeningNetAssets decimal.Decimal
	NetIncome        decimal.Decimal
	ClosingNetAssets decimal.Decimal
	CTA              decimal.Decimal
}

// Translate applies IAS 21 to a unit statement and derives the CTA:
// CTA = closing net assets at closing rate − (opening net assets at opening
// rate + net income at average rate). Rounding happens when entries are built.
func Translate(s Statement, r Rates) Translated {
	open := s.OpeningNetAssets.Mul(r.Opening)
	income := s.NetIncome.Mul(r.Average)
	closing := s.ClosingNetAssets.Mul(r.Closing)
	return Translated{
		OpeningNetAssets: open,
		NetIncome:        income,
		ClosingNetAssets: closing,
		CTA:              closing.Sub(open.Add(income)),
	}
}

// NCIShare returns the non-controlling share of an amount: amount × (100 − ownership %) / 100 (BR-40.6).
func NCIShare(amount, ownershipPct decimal.Decimal) decimal.Decimal {
	return amount.Mul(nciFraction(ownershipPct))
}

func nciFraction(ownershipPct decimal.Decimal) decimal.Decimal {
	return decimal.NewFromInt(100).Sub(ownershipPct).Div(decimal.NewFromInt(100))
}

// UnitTranslation is a unit's IAS 21 translation in group currency, rounded to
// the group currency minor unit per line (BR-40.9), with the CTA split between
// the group and the non-controlling interest (BR-40.6).
type UnitTranslation struct {
	Currency         string
	OpeningNetAssets decimal.Decimal
	NetIncome        decimal.Decimal
	Dividends        decimal.Decimal
	ClosingNetAssets decimal.Decimal
	CTA              decimal.Decimal
	NCIShareOfCTA    decimal.Decimal
	GroupShareOfCTA  decimal.Decimal
}

// TranslateUnit translates one unit's statement into group currency. Each
// translated amount rounds once; the CTA is derived from the rounded amounts,
// so opening + net income - dividends + CTA equals closing exactly. The NCI share of the
// CTA rounds once and the group share takes the remainder, so the two add up
// to the CTA.
func TranslateUnit(s Statement, r Rates, ownershipPct decimal.Decimal, currency string) (UnitTranslation, error) {
	switch {
	case len(currency) != 3:
		return UnitTranslation{}, invalid("group currency must be an ISO 4217 code")
	case !ownershipPct.IsPositive() || ownershipPct.GreaterThan(decimal.NewFromInt(100)):
		return UnitTranslation{}, invalid("ownership must be above 0 and at most 100 percent")
	case !r.Opening.IsPositive() || !r.Average.IsPositive() || !r.Closing.IsPositive():
		return UnitTranslation{}, invalid("opening, average and closing rates must be positive")
	}
	open := round(s.OpeningNetAssets.Mul(r.Opening), currency)
	income := round(s.NetIncome.Mul(r.Average), currency)
	closing := round(s.ClosingNetAssets.Mul(r.Closing), currency)
	dividends := round(s.Dividends.Mul(r.Average), currency)
	cta := closing.Sub(open.Add(income).Sub(dividends))
	nci := round(NCIShare(cta, ownershipPct), currency)
	return UnitTranslation{
		Currency:         currency,
		OpeningNetAssets: open,
		NetIncome:        income,
		Dividends:        dividends,
		ClosingNetAssets: closing,
		CTA:              cta,
		NCIShareOfCTA:    nci,
		GroupShareOfCTA:  cta.Sub(nci),
	}, nil
}
