package domain

import (
	"sort"
	"time"

	"github.com/shopspring/decimal"
)

// Free goods calculation rules (FR-26.22; SAP free_goods_calc_rule).
const (
	CalcProportional  = "PROPORTIONAL"
	CalcUnitReference = "UNIT_REFERENCE"
	CalcWholeUnits    = "WHOLE_UNITS"
)

// ValidCalcRule reports whether s is a free goods calculation rule.
func ValidCalcRule(s string) bool {
	return s == CalcProportional || s == CalcUnitReference || s == CalcWholeUnits
}

// CodeFreeGoodsRuleNotFound is raised for an unknown free goods rule id.
const CodeFreeGoodsRuleNotFound = "PRC-0047"

// CodeFreeGoodsWithFullDiscount is the PRC-0014 warning: a line carries a free goods
// proposal and is already priced at nothing, so the customer would get the goods twice.
const CodeFreeGoodsWithFullDiscount = "PRC-0014"

// FreeGoodsDef is a free goods rule (condition type FREE_GOODS, FR-26.22): for every Every
// units of one product ordered in Unit, Free units are given, from MinQty up. Kind decides
// whether the free units come out of the ordered quantity (INCLUSIVE, "1 free within every
// 10") or are added on top of it (EXCLUSIVE, "1 extra per 10").
type FreeGoodsDef struct {
	ID            string
	LegalEntityID string
	CustomerKind  CustomerKind
	CustomerKey   string
	ProductID     string
	Unit          string
	Kind          string
	Calc          string
	Every         decimal.Decimal
	Free          decimal.Decimal
	MinQty        decimal.Decimal
	ValidFrom     time.Time
	ValidTo       time.Time
	Withdrawn     bool
	Version       int
}

// ValidateFreeGoods returns field errors for a rule definition; an empty map means valid.
func ValidateFreeGoods(r FreeGoodsDef) map[string]string {
	f := map[string]string{}
	switch r.CustomerKind {
	case CustomerKindAll:
		if r.CustomerKey != "" {
			f["customerKey"] = "must be empty when customerKind is all"
		}
	case CustomerKindCustomer, CustomerKindGroup:
		if r.CustomerKey == "" || len(r.CustomerKey) > 64 {
			f["customerKey"] = "is required, at most 64 characters"
		}
	default:
		f["customerKind"] = "must be customer, customer_group or all"
	}
	if r.ProductID == "" || len(r.ProductID) > 64 {
		f["productId"] = "is required, at most 64 characters"
	}
	if r.Unit == "" || len(r.Unit) > 16 {
		f["unit"] = "is required, at most 16 characters"
	}
	if r.Kind != FreeGoodsInclusive && r.Kind != FreeGoodsExclusive {
		f["kind"] = "must be INCLUSIVE or EXCLUSIVE"
	}
	if !ValidCalcRule(r.Calc) {
		f["calcRule"] = "must be PROPORTIONAL, UNIT_REFERENCE or WHOLE_UNITS"
	}
	if !r.Every.IsPositive() {
		f["everyQuantity"] = "must be greater than 0"
	}
	if !r.Free.IsPositive() {
		f["freeQuantity"] = "must be greater than 0"
	}
	if r.Kind == FreeGoodsInclusive && r.Free.GreaterThanOrEqual(r.Every) {
		f["freeQuantity"] = "must be less than everyQuantity for an inclusive rule"
	}
	if r.MinQty.IsNegative() {
		f["minQuantity"] = "must not be negative"
	}
	switch {
	case r.ValidFrom.IsZero() || r.ValidTo.IsZero():
		f["validity"] = "validFrom and validTo are required"
	case r.ValidTo.Before(r.ValidFrom):
		f["validity"] = "validTo must not be before validFrom"
	}
	return f
}

// FreeGoodsRevision is the change a revise applies to a live free goods rule: the quantities,
// the calculation rule and the validity. The customer, product, unit and kind identify the rule
// and never change (withdraw it and create another for that).
type FreeGoodsRevision struct {
	ID        string
	Version   int
	Calc      string
	Every     decimal.Decimal
	Free      decimal.Decimal
	MinQty    decimal.Decimal
	ValidFrom time.Time
	ValidTo   time.Time
}

// Apply returns cur with the revision's values.
func (v FreeGoodsRevision) Apply(cur FreeGoodsDef) FreeGoodsDef {
	cur.Calc, cur.Every, cur.Free, cur.MinQty, cur.ValidFrom, cur.ValidTo = v.Calc, v.Every, v.Free, v.MinQty, v.ValidFrom, v.ValidTo
	return cur
}

// FreeGoodsPastChanges returns field errors where a revision (cur to next) would change what a
// day before today proposed: resolve stays reproducible for a past pricing date (§5.26.4
// invariant), so a document priced or re-priced on that day keeps the rule's answer, as a
// campaign revise does (CampaignPastChanges). A rule that has not started may change anything
// but may not start in the past. Once it has started, its offer (calculation, every and free
// quantities, minimum) and its start date are fixed, and its end date moves only within
// yesterday and later (ending it yesterday stops it now); an ended rule keeps its end date. To
// change the offer of a running rule, end it and create another rule from today. An empty map
// means the revision keeps the past.
func FreeGoodsPastChanges(cur, next FreeGoodsDef, today time.Time) map[string]string {
	fields := map[string]string{}
	t, yesterday := dayOf(today), dayOf(today.AddDate(0, 0, -1))
	if dayOf(cur.ValidFrom) >= t {
		if dayOf(next.ValidFrom) < t {
			fields["validFrom"] = "must not be before today (" + t + "): a past day cannot gain free goods"
		}
		return fields
	}
	since := dayOf(cur.ValidFrom)
	fixed := "cannot change: the rule has applied since " + since + "; end it and create another rule from today"
	if next.Calc != cur.Calc {
		fields["calcRule"] = fixed
	}
	if !next.Every.Equal(cur.Every) {
		fields["everyQuantity"] = fixed
	}
	if !next.Free.Equal(cur.Free) {
		fields["freeQuantity"] = fixed
	}
	if !next.MinQty.Equal(cur.MinQty) {
		fields["minQuantity"] = fixed
	}
	if dayOf(next.ValidFrom) != since {
		fields["validFrom"] = "cannot change: the rule has applied since " + since
	}
	if min(dayOf(next.ValidTo), yesterday) != min(dayOf(cur.ValidTo), yesterday) {
		if dayOf(cur.ValidTo) < t {
			fields["validTo"] = "cannot change: the rule ended on " + dayOf(cur.ValidTo)
		} else {
			fields["validTo"] = "must be " + yesterday + " or later: the days the rule already applied to keep it"
		}
	}
	return fields
}

// FreeGoodsKey is the identity two live rules must not share on overlapping dates.
func FreeGoodsKey(r FreeGoodsDef) string {
	return r.LegalEntityID + "|" + string(r.CustomerKind) + "|" + r.CustomerKey + "|" + r.ProductID + "|" + r.Unit
}

// FreeGoodsOverlap returns the first live rule with the same key whose validity overlaps r.
func FreeGoodsOverlap(r FreeGoodsDef, live []FreeGoodsDef) (FreeGoodsDef, bool) {
	for _, o := range live {
		if o.Withdrawn || o.ID == r.ID || FreeGoodsKey(o) != FreeGoodsKey(r) {
			continue
		}
		if !r.ValidFrom.After(o.ValidTo) && !o.ValidFrom.After(r.ValidTo) {
			return o, true
		}
	}
	return FreeGoodsDef{}, false
}

// FreeGoodsProposal is a zero-priced supplementary line proposal for one priced line
// (FR-26.22). w27 adds it as a supplementary line (FR-27.14). For an INCLUSIVE rule the
// FreeQuantity is part of OrderedQuantity and PricedQuantity is what the customer pays
// for; for an EXCLUSIVE rule PricedQuantity equals OrderedQuantity and the free units are
// extra. Warning is PRC-0014 when the line is already priced at nothing.
type FreeGoodsProposal struct {
	LineNo          int             `json:"lineNo"`
	ProductID       string          `json:"productId"`
	Unit            string          `json:"unit"`
	RuleID          string          `json:"ruleId"`
	Kind            string          `json:"kind"`
	OrderedQuantity decimal.Decimal `json:"orderedQuantity"`
	PricedQuantity  decimal.Decimal `json:"pricedQuantity"`
	FreeQuantity    decimal.Decimal `json:"freeQuantity"`
	Warning         string          `json:"warning,omitempty"`
}

func freeGoodsRank(r FreeGoodsDef, h Header) int {
	switch {
	case r.CustomerKind == CustomerKindCustomer && r.CustomerKey == h.CustomerID:
		return 3
	case r.CustomerKind == CustomerKindGroup && h.CustomerGroupID != "" && r.CustomerKey == h.CustomerGroupID:
		return 2
	case r.CustomerKind == CustomerKindAll:
		return 1
	}
	return 0
}

// ProposeFreeGoods picks, per line, the most specific live rule (customer, then customer
// group, then all; ties go to the later start, then the id) and returns the lines that earn
// free units, in line order. It is pure: the caller supplies the rules loaded for the
// header's legal entity and pricing date. A line whose net in lineNet is zero (already
// priced at nothing) carries the PRC-0014 warning.
func ProposeFreeGoods(h Header, lines []Line, rules []FreeGoodsDef, lineNet map[int]decimal.Decimal) []FreeGoodsProposal {
	out := []FreeGoodsProposal{}
	for _, l := range lines {
		var cands []FreeGoodsDef
		for _, r := range rules {
			if r.Withdrawn || r.ProductID != l.ProductID || r.Unit != l.Unit ||
				h.PricingDate.Before(r.ValidFrom) || h.PricingDate.After(r.ValidTo) || freeGoodsRank(r, h) == 0 {
				continue
			}
			cands = append(cands, r)
		}
		if len(cands) == 0 {
			continue
		}
		sort.SliceStable(cands, func(i, j int) bool {
			ri, rj := freeGoodsRank(cands[i], h), freeGoodsRank(cands[j], h)
			if ri != rj {
				return ri > rj
			}
			if !cands[i].ValidFrom.Equal(cands[j].ValidFrom) {
				return cands[i].ValidFrom.After(cands[j].ValidFrom)
			}
			return cands[i].ID < cands[j].ID
		})
		r := cands[0]
		res := FreeGoods(l.Quantity, FreeGoodsRule{Kind: r.Kind, Every: r.Every, Free: r.Free, MinQty: r.MinQty, Calc: r.Calc})
		if !res.Free.IsPositive() {
			continue
		}
		p := FreeGoodsProposal{LineNo: l.No, ProductID: l.ProductID, Unit: l.Unit, RuleID: r.ID, Kind: r.Kind,
			OrderedQuantity: l.Quantity, PricedQuantity: res.Priced, FreeQuantity: res.Free}
		if net, ok := lineNet[l.No]; ok && net.IsZero() {
			p.Warning = CodeFreeGoodsWithFullDiscount
		}
		out = append(out, p)
	}
	return out
}

// ResolveWithFreeGoods prices the document, proposes free goods for the lines that priced,
// and charges an INCLUSIVE line only for its priced quantity (BR-26.13: 25 ordered with 1
// free in every 10, WHOLE_UNITS, gives a line charging 23 units and a proposal of 2 free
// ones, AC-26.13). Quantity breaks and multiline groups still count the ordered quantity,
// so a free goods rule never moves a line to a worse break or leaves it unpriced; the
// document is priced again so the subtotal and the total discount follow the charged
// lines. An EXCLUSIVE rule leaves the line as ordered and the free units come on top.
// The PRC-0014 warning comes from the line net at the ordered quantity.
func ResolveWithFreeGoods(in Input, rules []FreeGoodsDef) (Result, []FreeGoodsProposal) {
	res := Resolve(in)
	proposals := ProposeFreeGoods(in.Header, pricedLines(in.Lines, res), rules, lineNets(in.Lines, res))
	charged := map[int]decimal.Decimal{}
	for _, p := range proposals {
		if p.Kind == FreeGoodsInclusive {
			charged[p.LineNo] = p.PricedQuantity
		}
	}
	if len(charged) == 0 {
		return res, proposals
	}
	again := in
	again.Lines = make([]Line, len(in.Lines))
	copy(again.Lines, in.Lines)
	for i, l := range again.Lines {
		if q, ok := charged[l.No]; ok {
			again.Lines[i].Charged = q
		}
	}
	return Resolve(again), proposals
}

func pricedLines(lines []Line, res Result) []Line {
	out := make([]Line, 0, len(lines))
	for i, l := range lines {
		if i < len(res.Lines) && res.Lines[i].Error == "" {
			out = append(out, l)
		}
	}
	return out
}

// lineNets maps each line that priced to its rounded net.
func lineNets(lines []Line, res Result) map[int]decimal.Decimal {
	out := map[int]decimal.Decimal{}
	for i, l := range lines {
		if i < len(res.Lines) && res.Lines[i].Error == "" {
			out[l.No] = res.Lines[i].LineNet
		}
	}
	return out
}
