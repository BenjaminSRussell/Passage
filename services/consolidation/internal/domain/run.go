package domain

import (
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"sort"

	"github.com/shopspring/decimal"
)

const MaxRunUnits = 200

const (
	AcctOpeningNetAssets = "OPENING_NET_ASSETS"
	AcctNetIncome        = "NET_INCOME"
	AcctClosingNetAssets = "CLOSING_NET_ASSETS"
	AcctTranslationCTA   = "CTA"
	AcctCTANCIShare      = "CTA_NCI_SHARE"
	AcctCTAGroupShare    = "CTA_GROUP_SHARE"
)

const (
	StatementNetAssets = "net_assets"
	StatementIncome    = "income"
)

const (
	LineOpeningNetAssets   = "OPENING_NET_ASSETS"
	LineUnitNetIncome      = "UNIT_NET_INCOME"
	LineCTA                = "CTA"
	LineClosingNetAssets   = "CLOSING_NET_ASSETS"
	LineCTANCIShare        = "CTA_NCI_SHARE"
	LineCTAGroupShare      = "CTA_GROUP_SHARE"
	LineUnrealizedProfitE3 = "UNREALIZED_PROFIT_E3"
	LineConsolidatedIncome = "CONSOLIDATED_NET_INCOME"
	LineNCIProfit          = "NCI_PROFIT"
	LineOwnersProfit       = "OWNERS_PROFIT"
)

var (
	periodPattern = regexp.MustCompile(`^[0-9]{4}-(0[1-9]|1[0-2])$`)
	codePattern   = regexp.MustCompile(`^[A-Za-z0-9._:-]{1,64}$`)
)

// RunUnit is one subsidiary of the group: its pair inputs (amounts in the
// unit's currency, Pair.Currency the group currency) and the net assets that
// translation needs. The unit's net income is Pair.NetIncome.
type RunUnit struct {
	ID               string
	Pair             Pair
	OpeningNetAssets decimal.Decimal
	ClosingNetAssets decimal.Decimal
}

// RunInput is everything one consolidation run is computed from.
type RunInput struct {
	GroupID  string
	Period   string
	Currency string
	Units    []RunUnit
}

type Balance struct {
	UnitID  string
	Account string
	Amount  decimal.Decimal
}

type UnitEntry struct {
	UnitID string
	Entry
}

type StatementLine struct {
	Statement string
	Code      string
	Amount    decimal.Decimal
}

// RunResult is the engine output a run stores: translated balances per unit,
// the E1–E5 entries per unit and the group statements derived from both.
type RunResult struct {
	Balances   []Balance
	Entries    []UnitEntry
	Statements []StatementLine
	CTA        decimal.Decimal
}

// Validate checks the run header and that unit ids are unique.
func (in RunInput) Validate() error {
	switch {
	case !codePattern.MatchString(in.GroupID):
		return invalid("group id must be 1 to 64 letters, digits or . _ : -")
	case !periodPattern.MatchString(in.Period):
		return invalid("period must be YYYY-MM")
	case len(in.Currency) != 3:
		return invalid("group currency must be an ISO 4217 code")
	case len(in.Units) == 0 || len(in.Units) > MaxRunUnits:
		return invalid(fmt.Sprintf("a run needs 1 to %d units", MaxRunUnits))
	}
	seen := map[string]bool{}
	for _, u := range in.Units {
		switch {
		case !codePattern.MatchString(u.ID):
			return invalid("unit id must be 1 to 64 letters, digits or . _ : -")
		case seen[u.ID]:
			return invalid("unit " + u.ID + " appears twice")
		case u.Pair.Currency != in.Currency:
			return invalid("unit " + u.ID + " must use the group currency " + in.Currency)
		}
		seen[u.ID] = true
	}
	return nil
}

// ComputeRun translates and eliminates every unit with the existing rules and
// derives the group statements. It changes no rule or rate treatment.
func ComputeRun(in RunInput) (RunResult, error) {
	if err := in.Validate(); err != nil {
		return RunResult{}, err
	}
	var res RunResult
	open, income, closing, cta, ctaNCI, ctaGroup := decimal.Zero, decimal.Zero, decimal.Zero, decimal.Zero, decimal.Zero, decimal.Zero
	for _, u := range in.Units {
		t, err := TranslateUnit(Statement{OpeningNetAssets: u.OpeningNetAssets, NetIncome: u.Pair.NetIncome, ClosingNetAssets: u.ClosingNetAssets},
			u.Pair.Rates, u.Pair.OwnershipPct, in.Currency)
		if err != nil {
			return RunResult{}, withUnit(u.ID, err)
		}
		entries, err := Eliminate(u.Pair)
		if err != nil {
			return RunResult{}, withUnit(u.ID, err)
		}
		res.Balances = append(res.Balances,
			Balance{u.ID, AcctOpeningNetAssets, t.OpeningNetAssets}, Balance{u.ID, AcctNetIncome, t.NetIncome},
			Balance{u.ID, AcctTranslationCTA, t.CTA}, Balance{u.ID, AcctClosingNetAssets, t.ClosingNetAssets},
			Balance{u.ID, AcctCTANCIShare, t.NCIShareOfCTA}, Balance{u.ID, AcctCTAGroupShare, t.GroupShareOfCTA})
		for _, e := range entries {
			res.Entries = append(res.Entries, UnitEntry{UnitID: u.ID, Entry: e})
		}
		open, income, closing = open.Add(t.OpeningNetAssets), income.Add(t.NetIncome), closing.Add(t.ClosingNetAssets)
		cta, ctaNCI, ctaGroup = cta.Add(t.CTA), ctaNCI.Add(t.NCIShareOfCTA), ctaGroup.Add(t.GroupShareOfCTA)
	}
	e3, nci := decimal.Zero, decimal.Zero
	for _, e := range res.Entries {
		switch {
		case e.RuleCode == RuleE3 && e.Account == AcctCOGS:
			e3 = e3.Add(e.Debit).Sub(e.Credit)
		case e.Account == AcctNCIProfit:
			nci = nci.Add(e.Debit).Sub(e.Credit)
		}
	}
	consolidated := income.Sub(e3)
	res.CTA = cta
	res.Statements = []StatementLine{
		{StatementNetAssets, LineOpeningNetAssets, open}, {StatementNetAssets, LineUnitNetIncome, income},
		{StatementNetAssets, LineCTA, cta}, {StatementNetAssets, LineClosingNetAssets, closing},
		{StatementNetAssets, LineCTANCIShare, ctaNCI}, {StatementNetAssets, LineCTAGroupShare, ctaGroup},
		{StatementIncome, LineUnitNetIncome, income}, {StatementIncome, LineUnrealizedProfitE3, e3.Neg()},
		{StatementIncome, LineConsolidatedIncome, consolidated}, {StatementIncome, LineNCIProfit, nci},
		{StatementIncome, LineOwnersProfit, consolidated.Sub(nci)},
	}
	return res, nil
}

func withUnit(unit string, err error) error {
	var de *Error
	if errors.As(err, &de) {
		return &Error{Code: de.Code, Detail: "unit " + unit + ": " + de.Detail}
	}
	return err
}

// RulesVersion names the elimination and translation rules a run was computed
// with. It is part of the inputs hash, so a rule change never matches an older run.
const RulesVersion = "consol-rules-2"

const hashScale = 6

type canonicalUnit struct {
	ID               string `json:"id"`
	OwnershipPct     string `json:"ownershipPct"`
	Closing          string `json:"closing"`
	Average          string `json:"average"`
	Opening          string `json:"opening"`
	Historical       string `json:"historical"`
	ICSales          string `json:"icSales"`
	ICCost           string `json:"icCost"`
	UnsoldShare      string `json:"unsoldShare"`
	ICReceivable     string `json:"icReceivable"`
	PayableGroup     string `json:"payableGroup"`
	ShareCapital     string `json:"shareCapital"`
	PreAcqRE         string `json:"preAcqRe"`
	InvestmentCost   string `json:"investmentCost"`
	NetIncome        string `json:"netIncome"`
	SellerIsSub      bool   `json:"sellerIsSubsidiary"`
	ICNetInvestment  bool   `json:"icNetInvestment"`
	FXGainLossAcct   string `json:"fxGainLossAccount"`
	OpeningNetAssets string `json:"openingNetAssets"`
	ClosingNetAssets string `json:"closingNetAssets"`
}

type canonicalInput struct {
	Rules    string          `json:"rules"`
	GroupID  string          `json:"groupId"`
	Period   string          `json:"period"`
	Currency string          `json:"currency"`
	Units    []canonicalUnit `json:"units"`
}

// Canonical returns the inputs as stable JSON: units sorted by id, every
// decimal at a fixed scale, and the rules version. Equal inputs give equal
// bytes whatever the unit order or decimal spelling.
func (in RunInput) Canonical() ([]byte, error) {
	units := make([]canonicalUnit, 0, len(in.Units))
	for _, u := range in.Units {
		f := func(d decimal.Decimal) string { return d.StringFixed(hashScale) }
		p := u.Pair
		units = append(units, canonicalUnit{
			ID: u.ID, OwnershipPct: f(p.OwnershipPct), Closing: f(p.Rates.Closing), Average: f(p.Rates.Average),
			Opening: f(p.Rates.Opening), Historical: f(p.Rates.Historical), ICSales: f(p.ICSales), ICCost: f(p.ICCost),
			UnsoldShare: f(p.UnsoldShare), ICReceivable: f(p.ICReceivable), PayableGroup: f(p.PayableGroup),
			ShareCapital: f(p.ShareCapital), PreAcqRE: f(p.PreAcqRE), InvestmentCost: f(p.InvestmentCost),
			NetIncome: f(p.NetIncome), SellerIsSub: p.SellerIsSubsidiary, ICNetInvestment: p.ICNetInvestment, FXGainLossAcct: p.icDifferenceAccount(),
			OpeningNetAssets: f(u.OpeningNetAssets), ClosingNetAssets: f(u.ClosingNetAssets),
		})
	}
	sort.Slice(units, func(i, j int) bool { return units[i].ID < units[j].ID })
	return json.Marshal(canonicalInput{Rules: RulesVersion, GroupID: in.GroupID, Period: in.Period, Currency: in.Currency, Units: units})
}
