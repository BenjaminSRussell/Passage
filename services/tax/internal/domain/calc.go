package domain

import (
	"fmt"
	"sort"
	"time"

	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/platform/money"
)

const divisionPrecision = 20

var (
	hundred = decimal.NewFromInt(100)
	one     = decimal.NewFromInt(1)

	euMembers = map[string]bool{
		"AT": true, "BE": true, "BG": true, "HR": true, "CY": true, "CZ": true, "DK": true, "EE": true, "FI": true,
		"FR": true, "DE": true, "GR": true, "HU": true, "IE": true, "IT": true, "LV": true, "LT": true, "LU": true,
		"MT": true, "NL": true, "PL": true, "PT": true, "RO": true, "SK": true, "SI": true, "ES": true, "SE": true,
	}
)

type cell struct {
	code Code
	rate Rate
	raw  decimal.Decimal
	tax  decimal.Decimal
}

type lineCalc struct {
	amount    decimal.Decimal
	inclusive bool
	cells     []*cell
}

type calculation struct {
	doc     Document
	cfg     Config
	exp     int32
	taxDate time.Time
	codes   map[string]Code
	exempt  map[string]bool
	rcOK    bool
	rcWhy   string
	warned  map[string]bool
	res     Result
}

func Calculate(doc Document, cfg Config) (Result, error) {
	if err := validate(doc, cfg); err != nil {
		return Result{}, err
	}
	c := &calculation{
		doc: doc, cfg: cfg, exp: money.MinorUnits(doc.Currency), codes: map[string]Code{}, warned: map[string]bool{},
		res: Result{Engine: EngineInternal, Lines: []LineResult{}, DocumentTaxesByCode: []CodeTotal{}, Warnings: []Warning{}, DeterminationTrace: []string{}},
	}
	for _, code := range cfg.Codes {
		c.codes[code.Code] = code
	}
	c.taxDate = doc.TaxDate.Time
	if doc.TaxDate.IsZero() {
		c.taxDate = doc.DocumentDate.Time
	}
	c.trace("step 1: tax_date %s", c.taxDate.Format(dateLayout))
	c.rcOK, c.rcWhy = reverseChargeEligible(doc.Party, doc.ShipFromCountry)
	if err := c.resolveExemption(); err != nil {
		return Result{}, err
	}
	lines := make([]*lineCalc, len(doc.Lines))
	docBasis := map[string]decimal.Decimal{}
	for i, l := range doc.Lines {
		codes, err := c.determine(l)
		if err != nil {
			return Result{}, err
		}
		lines[i] = &lineCalc{amount: l.Amount.Decimal, inclusive: l.PriceIncludesTax}
		for _, code := range codes {
			lines[i].cells = append(lines[i].cells, &cell{code: code})
			docBasis[code.Code] = docBasis[code.Code].Add(l.Amount.Abs())
		}
	}
	for i, l := range lines {
		if err := c.price(doc.Lines[i].LineRef, l, docBasis); err != nil {
			return Result{}, err
		}
	}
	c.capDocumentBasis(lines)
	c.round(lines)
	c.assemble(lines)
	return c.res, nil
}

func (c *calculation) trace(format string, a ...any) {
	c.res.DeterminationTrace = append(c.res.DeterminationTrace, fmt.Sprintf(format, a...))
}

func (c *calculation) warn(code, format string, a ...any) {
	detail := fmt.Sprintf(format, a...)
	if c.warned[code+detail] {
		return
	}
	c.warned[code+detail] = true
	c.res.Warnings = append(c.res.Warnings, Warning{Code: code, Detail: detail})
	c.trace("warning %s: %s", code, detail)
}

func reverseChargeEligible(p Party, shipFrom string) (bool, string) {
	switch {
	case p.TaxID == "":
		return false, "counterparty tax id missing"
	case !euMembers[p.Country] || !euMembers[shipFrom]:
		return false, "both parties must be in EU member states"
	case p.Country == shipFrom:
		return false, "parties are in the same member state"
	}
	return true, ""
}

func selfAssessed(mechanism string) bool {
	return mechanism == MechReverseCharge || mechanism == MechUseTax
}

func within(from, to Date, d time.Time) bool {
	return (from.IsZero() || !d.Before(from.Time)) && (to.IsZero() || !d.After(to.Time))
}

func (c *calculation) resolveExemption() error {
	c.exempt = map[string]bool{}
	number := c.doc.Party.Certificate
	if number == "" {
		return nil
	}
	for _, e := range c.cfg.Exemptions {
		if e.Number != number {
			continue
		}
		if !within(e.ValidFrom, e.ValidTo, c.taxDate) {
			c.warn(CodeExemptionExpired, "Exemption certificate %s is not valid on %s; full tax applied.", number, c.taxDate.Format(dateLayout))
			return nil
		}
		for _, code := range e.Codes {
			c.exempt[code] = true
		}
		c.trace("step 4: certificate %s exempts %v", number, e.Codes)
		return nil
	}
	return invalid("party.certificate", "unknown exemption certificate %q", number)
}

func (c *calculation) determine(l Line) ([]Code, error) {
	partyCodes, okParty := c.cfg.Groups[c.doc.Party.TaxGroup]
	itemCodes, okItem := c.cfg.ItemGroups[l.ItemTaxGroup]
	if !okParty || !okItem {
		return nil, &Error{Code: CodeGroupUnresolved, Field: "lines." + l.LineRef,
			Detail: fmt.Sprintf("No tax group or item tax group could be resolved for line %s.", l.LineRef)}
	}
	c.trace("step 2: line %s party_group=%s item_group=%s", l.LineRef, c.doc.Party.TaxGroup, l.ItemTaxGroup)
	inItem := map[string]bool{}
	for _, name := range itemCodes {
		inItem[name] = true
	}
	var out []Code
	seen := map[string]bool{}
	for _, name := range partyCodes {
		if !inItem[name] || seen[name] {
			continue
		}
		seen[name] = true
		code, ok := c.codes[name]
		if !ok {
			return nil, invalid("config.groups", "group references unknown tax code %q", name)
		}
		if c.exempt[name] {
			c.trace("step 4: line %s code %s exempt", l.LineRef, name)
			continue
		}
		if code.Mechanism == "" {
			code.Mechanism = MechStandard
		}
		if code.CalcBasis == "" {
			code.CalcBasis = BasisLine
		}
		if code.Mechanism == MechReverseCharge && !c.rcOK {
			c.warn(CodeReverseChargeIneligible, "Reverse charge not applicable: %s; standard rate applied.", c.rcWhy)
			code.Mechanism = MechStandard
		}
		if selfAssessed(code.Mechanism) && c.doc.Direction == DirOutput {
			c.trace("step 10: line %s code %s is self-assessed by the customer", l.LineRef, name)
			continue
		}
		out = append(out, code)
	}
	if len(seen) == 0 {
		c.warn(CodeNoIntersection, "Groups %s and %s share no tax code; line %s is untaxed.", c.doc.Party.TaxGroup, l.ItemTaxGroup, l.LineRef)
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].CompoundSequence < out[j].CompoundSequence })
	return out, nil
}

func (c *calculation) rateFor(code Code, basis decimal.Decimal) (Rate, error) {
	var hits []Rate
	for _, r := range code.Rates {
		if r.RatePct.IsNegative() || r.RatePct.GreaterThan(hundred) {
			return Rate{}, &Error{Code: CodeRateInvalid, Field: "config.codes." + code.Code,
				Detail: fmt.Sprintf("Rate %s is not valid; rates must be between 0 and 100 with intervals that do not overlap.", r.RatePct)}
		}
		if !within(r.ValidFrom, r.ValidTo, c.taxDate) {
			continue
		}
		if (r.FromAmount != nil && basis.LessThan(r.FromAmount.Decimal)) || (r.ToAmount != nil && basis.GreaterThanOrEqual(r.ToAmount.Decimal)) {
			continue
		}
		hits = append(hits, r)
	}
	switch len(hits) {
	case 0:
		return Rate{}, &Error{Code: CodeRateNotFound, Field: "config.codes." + code.Code,
			Detail: fmt.Sprintf("Tax code %s has no rate valid on %s.", code.Code, c.taxDate.Format(dateLayout))}
	case 1:
		return hits[0], nil
	}
	return Rate{}, &Error{Code: CodeRateInvalid, Field: "config.codes." + code.Code,
		Detail: fmt.Sprintf("Tax code %s has overlapping rates on %s.", code.Code, c.taxDate.Format(dateLayout))}
}

func capTax(tax decimal.Decimal, r Rate) decimal.Decimal {
	if tax.IsZero() {
		return tax
	}
	magnitude := tax.Abs()
	if r.MaxTax != nil && magnitude.GreaterThan(r.MaxTax.Decimal) {
		magnitude = r.MaxTax.Decimal
	}
	if r.MinTax != nil && magnitude.LessThan(r.MinTax.Decimal) {
		magnitude = r.MinTax.Decimal
	}
	if tax.IsNegative() {
		return magnitude.Neg()
	}
	return magnitude
}

func (c *calculation) price(ref string, l *lineCalc, docBasis map[string]decimal.Decimal) error {
	factorParallel, factorCompound := decimal.Zero, one
	for _, x := range l.cells {
		basis := l.amount.Abs()
		if x.code.CalcBasis == BasisDocument {
			basis = docBasis[x.code.Code]
		}
		rate, err := c.rateFor(x.code, basis)
		if err != nil {
			return err
		}
		x.rate = rate
		if x.code.Mechanism != MechStandard {
			continue
		}
		if x.code.CompoundSequence == 0 {
			factorParallel = factorParallel.Add(rate.RatePct.Div(hundred))
		} else {
			factorCompound = factorCompound.Mul(one.Add(rate.RatePct.Div(hundred)))
		}
	}
	base := l.amount
	if l.inclusive {
		base = l.amount.DivRound(one.Add(factorParallel).Mul(factorCompound), divisionPrecision)
		c.trace("step 6: line %s inclusive gross %s backs out to base %s", ref, l.amount, base)
	}
	earlier := decimal.Zero
	for _, x := range l.cells {
		onto := base
		if x.code.CompoundSequence > 0 {
			onto = base.Add(earlier)
		}
		x.raw = onto.Mul(x.rate.RatePct.Decimal).Div(hundred)
		if x.code.CalcBasis != BasisDocument {
			x.raw = capTax(x.raw, x.rate)
		}
		earlier = earlier.Add(x.raw)
		c.trace("step 8: line %s code %s rate %s%% raw tax %s", ref, x.code.Code, x.rate.RatePct, x.raw)
	}
	return nil
}

func (c *calculation) capDocumentBasis(lines []*lineCalc) {
	byCode := map[string][]*cell{}
	for _, l := range lines {
		for _, x := range l.cells {
			if x.code.CalcBasis == BasisDocument {
				byCode[x.code.Code] = append(byCode[x.code.Code], x)
			}
		}
	}
	for name, cells := range byCode {
		total := decimal.Zero
		for _, x := range cells {
			total = total.Add(x.raw)
		}
		capped := capTax(total, cells[0].rate)
		if total.IsZero() || capped.Equal(total) {
			continue
		}
		ratio := capped.DivRound(total, divisionPrecision)
		for _, x := range cells {
			x.raw = x.raw.Mul(ratio)
		}
		c.trace("step 9: document total for %s capped from %s to %s", name, total, capped)
	}
}

func (c *calculation) round(lines []*lineCalc) {
	byCode := map[string][]*cell{}
	for _, l := range lines {
		for _, x := range l.cells {
			byCode[x.code.Code] = append(byCode[x.code.Code], x)
		}
	}
	for _, code := range c.cfg.Codes {
		cells := byCode[code.Code]
		if len(cells) == 0 {
			continue
		}
		mode := code.Rounding
		if mode == "" {
			mode = c.cfg.DefaultRounding
		}
		if mode == RoundPerLine {
			for _, x := range cells {
				x.tax = x.raw.Round(c.exp)
			}
			continue
		}
		raws := make([]decimal.Decimal, len(cells))
		total := decimal.Zero
		for i, x := range cells {
			raws[i] = x.raw
			total = total.Add(x.raw)
		}
		total = total.Round(c.exp)
		for i, share := range distribute(total, raws, c.exp) {
			cells[i].tax = share
		}
		c.trace("step 12: code %s per_document total %s distributed over %d lines", code.Code, total, len(cells))
	}
}

func distribute(total decimal.Decimal, weights []decimal.Decimal, exp int32) []decimal.Decimal {
	out := make([]decimal.Decimal, len(weights))
	sum := decimal.Sum(decimal.Zero, weights...)
	if sum.IsZero() {
		return out
	}
	scale := decimal.New(1, exp)
	units := total.Mul(scale)
	fractions := make([]decimal.Decimal, len(weights))
	allocated := decimal.Zero
	for i, w := range weights {
		share := units.Mul(w).DivRound(sum, divisionPrecision)
		out[i] = share.Floor()
		fractions[i] = share.Sub(out[i])
		allocated = allocated.Add(out[i])
	}
	order := make([]int, len(weights))
	for i := range order {
		order[i] = i
	}
	sort.SliceStable(order, func(a, b int) bool { return fractions[order[a]].GreaterThan(fractions[order[b]]) })
	for k := int64(0); k < units.Sub(allocated).IntPart() && k < int64(len(order)); k++ {
		out[order[k]] = out[order[k]].Add(one)
	}
	for i := range out {
		out[i] = out[i].DivRound(scale, exp)
	}
	return out
}

func (c *calculation) assemble(lines []*lineCalc) {
	index := map[string]int{}
	accumulate := func(code, direction string, base, amount decimal.Decimal) {
		key := code + "|" + direction
		i, ok := index[key]
		if !ok {
			i = len(c.res.DocumentTaxesByCode)
			index[key] = i
			c.res.DocumentTaxesByCode = append(c.res.DocumentTaxesByCode, CodeTotal{Code: code, Direction: direction})
		}
		total := &c.res.DocumentTaxesByCode[i]
		total.Base = Dec{total.Base.Add(base)}
		total.TaxAmount = Dec{total.TaxAmount.Add(amount)}
	}
	for i, l := range lines {
		base := l.amount.Round(c.exp)
		if l.inclusive {
			for _, x := range l.cells {
				if x.code.Mechanism == MechStandard {
					base = base.Sub(x.tax)
				}
			}
		}
		result := LineResult{LineRef: c.doc.Lines[i].LineRef, Base: Dec{base}, Taxes: []TaxLine{}}
		leg := func(x *cell, direction string) {
			recoverable := x.tax
			if pct := c.recoveryPct(x.code, c.doc.Lines[i].ItemTaxGroup); direction != DirOutput && pct != nil {
				recoverable = x.tax.Mul(pct.Decimal).Div(hundred).Round(c.exp)
			}
			accumulate(x.code.Code, direction, base, x.tax)
			result.Taxes = append(result.Taxes, TaxLine{Code: x.code.Code, Jurisdiction: x.code.Jurisdiction, RatePct: x.rate.RatePct,
				TaxAmount: Dec{x.tax}, Recoverable: Dec{recoverable}, Nonrecoverable: Dec{x.tax.Sub(recoverable)}, Direction: direction})
		}
		for _, x := range l.cells {
			switch x.code.Mechanism {
			case MechReverseCharge:
				leg(x, DirOutput)
				leg(x, DirInput)
			case MechUseTax:
				leg(x, DirUse)
			default:
				leg(x, c.doc.Direction)
			}
		}
		c.res.Lines = append(c.res.Lines, result)
	}
}

// recoveryPct is the recoverable share of an input tax: the recovery policy that applies to the code and the line's item tax group
// on the tax date, else the code's own recoverable_pct, else nil (everything is recoverable).
func (c *calculation) recoveryPct(code Code, itemGroup string) *Dec {
	if p := RecoveryFor(c.cfg.Recovery, code.Code, itemGroup, c.taxDate); p != nil {
		return p
	}
	return code.RecoverablePct
}
