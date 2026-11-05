package domain

import (
	"fmt"
	"sort"
	"strings"

	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/platform/money"
)

// StagedLine is one line of a staged-only pack (wip_prod, fixed_assets, payroll_ytd) in the shape
// the factory stores and reconciles: the legacy id, the natural key Passage holds the line under
// (unique per system and company), the currency, the dimensions the control totals group by and
// the measured amounts. None of these packs has a target import API yet (D1a), so the lines stay
// in `staged_opening_line` with their legacy_xref rows, like bank_balances before H-S10-1.
type StagedLine struct {
	LegacyID   string
	NaturalKey string
	Currency   string
	Group      map[string]string
	Measures   map[string]decimal.Decimal
}

// Record maps a staged line onto the shared quarantine record shape.
func (l StagedLine) Record() Record {
	return Record{LegacyID: l.LegacyID, Account: l.NaturalKey, Currency: l.Currency}
}

// StagedTotal is one control total of a staged pack: Measure summed per the Group fields named in
// By (plus the company), or the line count when Measure is empty.
type StagedTotal struct {
	Name    string
	By      []string
	Measure string
}

// StagedSpec describes a staged pack: its legacy_xref source table, the totals FR-49.2 lists for it
// in the catalog, and the order its measures are hashed in.
type StagedSpec struct {
	Pack     Pack
	Table    string
	Measures []string
	Totals   []StagedTotal
}

// Staged pack specs.
var (
	WIPProdSpec = StagedSpec{Pack: PackWIPProd, Table: "wip_prod", Measures: []string{"material", "labor", "overhead", "wip"},
		Totals: []StagedTotal{
			{Name: "wip_prod.order_count"},
			{Name: "wip_prod.wip_value", By: []string{"production_order", "currency"}, Measure: "wip"},
			{Name: "wip_prod.wip_entity", By: []string{"currency"}, Measure: "wip"},
		}}
	FixedAssetsSpec = StagedSpec{Pack: PackFixedAssets, Table: "fixed_assets", Measures: []string{"acquisition_cost", "accumulated_depreciation", "nbv", "next_depreciation"},
		Totals: []StagedTotal{
			{Name: "fixed_assets.count", By: []string{"book"}},
			{Name: "fixed_assets.acquisition_cost", By: []string{"book", "currency"}, Measure: "acquisition_cost"},
			{Name: "fixed_assets.accumulated_depreciation", By: []string{"book", "currency"}, Measure: "accumulated_depreciation"},
			{Name: "fixed_assets.nbv", By: []string{"book", "currency"}, Measure: "nbv"},
		}}
	PayrollYTDSpec = StagedSpec{Pack: PackPayrollYTD, Table: "payroll_ytd", Measures: []string{"gross", "tax", "deductions", "net"},
		Totals: []StagedTotal{
			{Name: "payroll_ytd.worker_count", By: []string{"tax_year"}},
			{Name: "payroll_ytd.gross", By: []string{"worker", "tax_year", "currency"}, Measure: "gross"},
			{Name: "payroll_ytd.tax", By: []string{"worker", "tax_year", "currency"}, Measure: "tax"},
			{Name: "payroll_ytd.net", By: []string{"worker", "tax_year", "currency"}, Measure: "net"},
		}}
)

// StagedSpecFor returns the spec of a staged pack.
func StagedSpecFor(p Pack) (StagedSpec, bool) {
	for _, s := range []StagedSpec{WIPProdSpec, FixedAssetsSpec, PayrollYTDSpec} {
		if s.Pack == p {
			return s, true
		}
	}
	return StagedSpec{}, false
}

// HashStaged returns the run's input hash over the lines' legacy id, natural key, currency and
// measures in the spec's order (FR-49.10).
func HashStaged(spec StagedSpec, in []StagedLine) string {
	keyed := make([]Keyed, len(in))
	for i, l := range in {
		parts := []string{l.LegacyID, l.NaturalKey, l.Currency}
		for _, m := range spec.Measures {
			parts = append(parts, l.Measures[m].StringFixed(6))
		}
		keyed[i] = Keyed{LegacyID: strings.Join(parts, "|")}
	}
	return HashRecords(keyed)
}

// stagedCheck is a pack-specific domain or referential rule over one line; it returns the rule and
// the reason when the line fails, or "" when it passes.
type stagedCheck func(StagedLine) (rule, reason string)

// validateStaged applies the shared suite (blank and duplicate legacy id and natural key, ISO 4217,
// six decimals) and then the pack's own checks. A failing line is quarantined with the rule and
// reason; the caller loads the rest.
func validateStaged(spec StagedSpec, in []StagedLine, check stagedCheck) (accepted []StagedLine, quarantined []Quarantined) {
	seenID, seenKey := map[string]struct{}{}, map[string]struct{}{}
	hold := func(l StagedLine, rule, reason string) {
		quarantined = append(quarantined, Quarantined{Record: l.Record(), Rule: rule, Reason: reason, Line: l})
	}
	for _, l := range in {
		if strings.TrimSpace(l.LegacyID) == "" {
			hold(l, RuleUniqueness, "legacy id is blank")
			continue
		}
		if _, dup := seenID[l.LegacyID]; dup {
			hold(l, RuleUniqueness, "legacy id appears twice in the extract")
			continue
		}
		seenID[l.LegacyID] = struct{}{}
		if _, dup := seenKey[l.NaturalKey]; dup {
			hold(l, RuleUniqueness, fmt.Sprintf("%s %q appears twice in the extract", spec.Pack, l.NaturalKey))
			continue
		}
		seenKey[l.NaturalKey] = struct{}{}
		if _, ok := iso4217[l.Currency]; !ok {
			hold(l, RuleDomain, fmt.Sprintf("currency %q is not ISO 4217", l.Currency))
			continue
		}
		if m := firstFine(spec, l); m != "" {
			hold(l, RuleDomain, fmt.Sprintf("%s %s has more than 6 decimal places", m, l.Measures[m]))
			continue
		}
		if rule, reason := check(l); rule != "" {
			hold(l, rule, reason)
			continue
		}
		accepted = append(accepted, l)
	}
	return accepted, quarantined
}

func firstFine(spec StagedSpec, l StagedLine) string {
	for _, m := range spec.Measures {
		if v := l.Measures[m]; !v.Equal(v.Round(6)) {
			return m
		}
	}
	return ""
}

// ReconcileStaged finishes a staged pack run once the accepted lines are held: loaded is how many
// lines this run inserted and held maps each accepted legacy id to the line Passage holds. Every
// total of the spec is computed over the whole extract against what Passage holds, exactly unless a
// tolerance is configured. The run moves from VALIDATED to LOADED and, without a MISMATCH, to
// RECONCILED; a run with quarantine keeps a mismatch and stays LOADED.
func ReconcileStaged(r *PackRun, spec StagedSpec, source, accepted []StagedLine, loaded int, held map[string]StagedLine) error {
	if r.Status != StatusValidated {
		return fmt.Errorf("%w: reconcile needs a VALIDATED run, got %s", ErrTransition, r.Status)
	}
	r.RecordsLoaded = loaded
	r.AlreadyPresent = len(accepted) - loaded
	if err := r.Advance(StatusLoaded); err != nil {
		return err
	}
	var target []StagedLine
	for _, l := range accepted {
		if h, ok := held[l.LegacyID]; ok {
			target = append(target, h)
		}
	}
	var totals []ControlTotal
	for _, t := range spec.Totals {
		src, keys := sumStaged(source, t)
		tgt, tkeys := sumStaged(target, t)
		for k, v := range tkeys {
			if _, ok := keys[k]; !ok {
				keys[k] = v
			}
		}
		for _, k := range sortedKeys(keys) {
			key := map[string]string{"company": r.SourceCompany}
			for f, v := range keys[k] {
				key[f] = v
			}
			ct, err := NewControlTotal(t.Name, key, src[k], tgt[k], Tolerance{})
			if err != nil {
				return err
			}
			totals = append(totals, ct)
		}
	}
	r.Totals = totals
	if err := r.applyTolerances(); err != nil {
		return err
	}
	if !r.HasMismatch() {
		return r.Advance(StatusReconciled)
	}
	return nil
}

// sumStaged sums one total over lines; keys maps each group key to its field values.
func sumStaged(in []StagedLine, t StagedTotal) (map[string]decimal.Decimal, map[string]map[string]string) {
	sums, keys := map[string]decimal.Decimal{}, map[string]map[string]string{}
	by := append([]string(nil), t.By...)
	sort.Strings(by)
	for _, l := range in {
		fields := map[string]string{}
		parts := make([]string, 0, len(by))
		for _, f := range by {
			v := l.Group[f]
			if f == "currency" {
				v = l.Currency
			}
			fields[f] = v
			parts = append(parts, v)
		}
		k := strings.Join(parts, "\x1f")
		keys[k] = fields
		if t.Measure == "" {
			sums[k] = sums[k].Add(decimal.NewFromInt(1))
		} else {
			sums[k] = sums[k].Add(l.Measures[t.Measure])
		}
	}
	return sums, keys
}

// WIPProdLine is one open production order's work in progress at the cutoff (AX ProdTable with its
// ProdTableJour WIP, SAP AUFK with the order's WIP from KKAX): the material, labor and overhead
// issued to the order and not yet received as finished goods, in the order's currency.
type WIPProdLine struct {
	LegacyID        string
	ProductionOrder string
	Item            string
	Currency        string
	Material        decimal.Decimal
	Labor           decimal.Decimal
	Overhead        decimal.Decimal
}

// Staged maps the order onto the stored line; WIP is the sum of its three components.
func (w WIPProdLine) Staged() StagedLine {
	return StagedLine{LegacyID: w.LegacyID, NaturalKey: w.ProductionOrder, Currency: w.Currency,
		Group: map[string]string{"production_order": w.ProductionOrder, "item": w.Item},
		Measures: map[string]decimal.Decimal{"material": w.Material, "labor": w.Labor, "overhead": w.Overhead,
			"wip": w.Material.Add(w.Labor).Add(w.Overhead)}}
}

// ValidateWIPProd applies the wip_prod suite: the shared rules, the item in the item master given
// with the run (REFERENTIAL), and no negative cost component (DOMAIN: a credit WIP is a broken extract,
// the over-receipt belongs in a variance, not an opening balance).
func ValidateWIPProd(in []WIPProdLine, items map[string]struct{}) ([]StagedLine, []Quarantined) {
	lines := make([]StagedLine, len(in))
	for i, w := range in {
		lines[i] = w.Staged()
	}
	return validateStaged(WIPProdSpec, lines, func(l StagedLine) (string, string) {
		if strings.TrimSpace(l.NaturalKey) == "" {
			return RuleDomain, "production order is blank"
		}
		if _, ok := items[l.Group["item"]]; !ok {
			return RuleReferential, fmt.Sprintf("item %q is not in the item master", l.Group["item"])
		}
		for _, m := range []string{"material", "labor", "overhead"} {
			if l.Measures[m].IsNegative() {
				return RuleDomain, fmt.Sprintf("%s %s is negative", m, l.Measures[m])
			}
		}
		return "", ""
	})
}

// Depreciation methods of the fixed_assets pack.
const (
	DepStraightLine      = "SL"
	DepDecliningBalance  = "DB"
	MsgNextDepreciation  = "MIG-1507"
	nextDepreciationDiff = "0.01"
)

// FixedAssetLine is one asset in one depreciation book at the cutoff (AX AssetBook, SAP ANLA/ANLC):
// acquisition cost, accumulated depreciation, salvage value, the depreciation method with its
// remaining life in months (and the useful life for declining balance), and the legacy system's
// projection of the next period's depreciation.
type FixedAssetLine struct {
	LegacyID                string
	Asset                   string
	Book                    string
	Currency                string
	Method                  string
	UsefulLifeMonths        int
	RemainingLifeMonths     int
	AcquisitionCost         decimal.Decimal
	AccumulatedDepreciation decimal.Decimal
	SalvageValue            decimal.Decimal
	NextDepreciation        decimal.Decimal
}

// NBV is the net book value: acquisition cost less accumulated depreciation.
func (a FixedAssetLine) NBV() decimal.Decimal {
	return a.AcquisitionCost.Sub(a.AccumulatedDepreciation)
}

// ProjectNextDepreciation recomputes the next period's depreciation from the loaded values (BR-49.6):
// straight line spreads the depreciable NBV (NBV less salvage) over the remaining months; declining
// balance takes NBV times twice the monthly straight-line rate of the useful life, never below salvage.
// Both round to the currency scale of the control totals (2 decimals).
func (a FixedAssetLine) ProjectNextDepreciation() decimal.Decimal {
	base := a.NBV().Sub(a.SalvageValue)
	if !base.IsPositive() || a.RemainingLifeMonths <= 0 {
		return decimal.Zero
	}
	var d decimal.Decimal
	switch a.Method {
	case DepDecliningBalance:
		d = a.NBV().Mul(decimal.NewFromInt(2)).Div(decimal.NewFromInt(int64(a.UsefulLifeMonths)))
		if a.RemainingLifeMonths == 1 || d.GreaterThan(base) {
			d = base
		}
	default:
		d = base.Div(decimal.NewFromInt(int64(a.RemainingLifeMonths)))
	}
	return d.Round(2)
}

// Staged maps the asset book onto the stored line; the natural key is (asset, book).
func (a FixedAssetLine) Staged() StagedLine {
	return StagedLine{LegacyID: a.LegacyID, NaturalKey: a.Asset + "/" + a.Book, Currency: a.Currency,
		Group: map[string]string{"asset": a.Asset, "book": a.Book, "method": a.Method},
		Measures: map[string]decimal.Decimal{"acquisition_cost": a.AcquisitionCost, "accumulated_depreciation": a.AccumulatedDepreciation,
			"nbv": a.NBV(), "next_depreciation": a.NextDepreciation, "salvage": a.SalvageValue,
			"useful_life_months": decimal.NewFromInt(int64(a.UsefulLifeMonths)), "remaining_life_months": decimal.NewFromInt(int64(a.RemainingLifeMonths))}}
}

// ValidateFixedAssets applies the fixed_assets suite: the shared rules, the book in the book list
// given with the run (REFERENTIAL), a known method with a positive life, 0 <= accumulated <= cost and
// NBV >= salvage (DOMAIN), and BR-49.6: the next depreciation recomputed from the loaded values lies
// within 0.01 of the legacy projection, or the asset is quarantined with MIG-1507.
func ValidateFixedAssets(in []FixedAssetLine, books map[string]struct{}) ([]StagedLine, []Quarantined) {
	byID := make(map[string]FixedAssetLine, len(in))
	lines := make([]StagedLine, len(in))
	for i, a := range in {
		lines[i] = a.Staged()
		byID[a.LegacyID] = a
	}
	limit := decimal.RequireFromString(nextDepreciationDiff)
	return validateStaged(FixedAssetsSpec, lines, func(l StagedLine) (string, string) {
		a := byID[l.LegacyID]
		switch {
		case strings.TrimSpace(a.Asset) == "":
			return RuleDomain, "asset is blank"
		case func() bool { _, ok := books[a.Book]; return !ok }():
			return RuleReferential, fmt.Sprintf("book %q is not in the depreciation book list", a.Book)
		case a.Method != DepStraightLine && a.Method != DepDecliningBalance:
			return RuleDomain, fmt.Sprintf("method %q is not SL or DB", a.Method)
		case a.RemainingLifeMonths < 0 || a.UsefulLifeMonths <= 0 || a.RemainingLifeMonths > a.UsefulLifeMonths:
			return RuleDomain, "useful life must be positive and remaining life between 0 and the useful life"
		case a.AcquisitionCost.IsNegative() || a.AccumulatedDepreciation.IsNegative() || a.AccumulatedDepreciation.GreaterThan(a.AcquisitionCost):
			return RuleDomain, "accumulated depreciation must be between 0 and the acquisition cost"
		case a.SalvageValue.IsNegative() || a.NBV().LessThan(a.SalvageValue):
			return RuleDomain, fmt.Sprintf("net book value %s is below the salvage value %s", a.NBV(), a.SalvageValue)
		}
		if got := a.ProjectNextDepreciation(); got.Sub(a.NextDepreciation).Abs().GreaterThan(limit) {
			return RuleDomain, fmt.Sprintf("%s Asset %s book %s next depreciation %s recomputed from the loaded values differs from the legacy projection %s.",
				MsgNextDepreciation, a.Asset, a.Book, got.StringFixed(2), a.NextDepreciation.StringFixed(2))
		}
		return "", ""
	})
}

// PayrollYTDLine is one worker's year-to-date payroll totals for a tax year at the cutoff (AX
// PayrollStatement lines, SAP payroll cluster RT /101, /401, /560): gross, withheld tax, other
// deductions and net pay, in the payroll currency.
type PayrollYTDLine struct {
	LegacyID   string
	Worker     string
	TaxYear    string
	Currency   string
	Gross      decimal.Decimal
	Tax        decimal.Decimal
	Deductions decimal.Decimal
	Net        decimal.Decimal
}

// Staged maps the worker year onto the stored line; the natural key is (worker, tax year).
func (p PayrollYTDLine) Staged() StagedLine {
	return StagedLine{LegacyID: p.LegacyID, NaturalKey: p.Worker + "/" + p.TaxYear, Currency: p.Currency,
		Group:    map[string]string{"worker": p.Worker, "tax_year": p.TaxYear},
		Measures: map[string]decimal.Decimal{"gross": p.Gross, "tax": p.Tax, "deductions": p.Deductions, "net": p.Net}}
}

// ValidatePayrollYTD applies the payroll_ytd suite: the shared rules, the worker in the worker master
// given with the run (REFERENTIAL), a four-digit tax year, amounts in whole minor units of the
// currency, no negative gross, tax or deductions, and net = gross - tax - deductions exactly (DOMAIN).
func ValidatePayrollYTD(in []PayrollYTDLine, workers map[string]struct{}) ([]StagedLine, []Quarantined) {
	lines := make([]StagedLine, len(in))
	for i, p := range in {
		lines[i] = p.Staged()
	}
	return validateStaged(PayrollYTDSpec, lines, func(l StagedLine) (string, string) {
		m := l.Measures
		year := l.Group["tax_year"]
		if _, ok := workers[l.Group["worker"]]; !ok {
			return RuleReferential, fmt.Sprintf("worker %q is not in the worker master", l.Group["worker"])
		}
		if len(year) != 4 || strings.Trim(year, "0123456789") != "" {
			return RuleDomain, fmt.Sprintf("tax year %q is not YYYY", year)
		}
		units := money.MinorUnits(l.Currency)
		for _, k := range []string{"gross", "tax", "deductions", "net"} {
			if k != "net" && m[k].IsNegative() {
				return RuleDomain, fmt.Sprintf("%s %s is negative", k, m[k])
			}
			if !m[k].Equal(m[k].Round(units)) {
				return RuleDomain, fmt.Sprintf("%s %s has more than the %d decimal places of %s", k, m[k], units, l.Currency)
			}
		}
		if want := m["gross"].Sub(m["tax"]).Sub(m["deductions"]); !want.Equal(m["net"]) {
			return RuleDomain, fmt.Sprintf("net %s is not gross - tax - deductions (%s)", m["net"], want)
		}
		return "", ""
	})
}
