package domain

import (
	"encoding/json"
	"fmt"
	"regexp"
	"sort"
	"strings"

	"github.com/shopspring/decimal"
)

// Tenant cost statements (PSG-50, FR-50.9, BR-50.10, AC-50.7). The rules are those of docs/ops/tools/cost_meter.py, and
// cost_test.go pins the same figures as its tests:
//
//	cost_usd = sum(unit_rate[dim] * quantity[dim]) * (1 + overhead)
//
// Rounding happens once per statement: USD to 0.01 from the unrounded total, and EUR from that same unrounded total
// divided by the ECB month-average rate (USD per 1 EUR), also to 0.01.
const (
	CodeCostInvalid     = "COST-1902"
	CodeCostUnknown     = "COST-1903"
	CodeCostUnavailable = "COST-1904"
	// DefaultOverhead is the shared control-plane share of docs/ops/cost-model.md [ASSUMED 12%].
	DefaultOverhead = "0.12"
)

// CostDimensions are the metered dimensions, in statement order.
var CostDimensions = []string{"cpu_core_hours", "memory_gb_hours", "storage_gb", "kafka_gb", "lakehouse_gb", "ai_tokens"}

var monthRe = regexp.MustCompile(`^\d{4}-(0[1-9]|1[0-2])$`)

// ValidMonth reports whether m is YYYY-MM.
func ValidMonth(m string) bool { return monthRe.MatchString(m) }

// CostRates are the USD unit rates per dimension and the overhead share, from OPS_COST_RATES.
type CostRates struct {
	UnitUSD  map[string]decimal.Decimal
	Overhead decimal.Decimal
}

// ParseCostRates reads `{"cpu_core_hours":"0.04",...,"overhead":"0.12"}`. Every dimension is required and every value is a
// non-negative decimal string; overhead is optional (0.12) and between 0 and 1.
func ParseCostRates(raw string) (CostRates, error) {
	var m map[string]string
	if err := json.Unmarshal([]byte(raw), &m); err != nil {
		return CostRates{}, fail(CodeCostInvalid, "cost rates must be a JSON object of decimal strings: %v", err)
	}
	r := CostRates{UnitUSD: map[string]decimal.Decimal{}, Overhead: decimal.RequireFromString(DefaultOverhead)}
	if v, ok := m["overhead"]; ok {
		d, err := nonNegative("overhead", v)
		if err != nil {
			return r, err
		}
		if d.GreaterThan(decimal.NewFromInt(1)) {
			return r, fail(CodeCostInvalid, "overhead must be between 0 and 1")
		}
		r.Overhead = d
		delete(m, "overhead")
	}
	q, err := parseDimensions("rate", m)
	if err != nil {
		return r, err
	}
	r.UnitUSD = q
	return r, nil
}

// ParseQuantities reads one tenant's metered month: every dimension, as a non-negative decimal string (send "0" explicitly).
func ParseQuantities(m map[string]string) (map[string]decimal.Decimal, error) {
	return parseDimensions("quantity", m)
}

func parseDimensions(what string, m map[string]string) (map[string]decimal.Decimal, error) {
	var unknown, missing []string
	known := map[string]bool{}
	for _, d := range CostDimensions {
		known[d] = true
		if _, ok := m[d]; !ok {
			missing = append(missing, d)
		}
	}
	for k := range m {
		if !known[k] {
			unknown = append(unknown, k)
		}
	}
	sort.Strings(unknown)
	switch {
	case len(unknown) > 0:
		return nil, fail(CodeCostInvalid, "unknown %s dimension(s): %s", what, strings.Join(unknown, ", "))
	case len(missing) > 0:
		return nil, fail(CodeCostInvalid, "missing %s dimension(s): %s; send 0 explicitly", what, strings.Join(missing, ", "))
	}
	out := map[string]decimal.Decimal{}
	for _, d := range CostDimensions {
		v, err := nonNegative(what+" "+d, m[d])
		if err != nil {
			return nil, err
		}
		out[d] = v
	}
	return out, nil
}

func nonNegative(field, v string) (decimal.Decimal, error) {
	d, err := decimal.NewFromString(strings.TrimSpace(v))
	if err != nil {
		return d, fail(CodeCostInvalid, "%s: not a decimal: %q", field, v)
	}
	if d.IsNegative() {
		return d, fail(CodeCostInvalid, "%s: must not be negative", field)
	}
	return d, nil
}

// CostStatement is one tenant's month (the tenant_cost row of cost-model.md) with its resource lines.
type CostStatement struct {
	TenantID     string            `json:"tenantId"`
	Month        string            `json:"month"`
	Quantities   map[string]string `json:"quantities"`
	Lines        map[string]string `json:"lines"`
	CostUSD      string            `json:"costUsd"`
	CostEUR      string            `json:"costEur"`
	USDPerEUR    string            `json:"usdPerEur"`
	FXSource     string            `json:"fxSource"`
	Overhead     string            `json:"overhead"`
	ModelVersion string            `json:"modelVersion"`
}

func roundCent(d decimal.Decimal) decimal.Decimal { return d.Round(2) }

// ComputeCost is cost_meter.compute: pure, the same inputs give the same statement.
func ComputeCost(tenantID, month string, qty map[string]decimal.Decimal, rates CostRates, usdPerEUR, modelVersion string) (CostStatement, error) {
	if !ValidMonth(month) {
		return CostStatement{}, fail(CodeCostInvalid, "month must be YYYY-MM")
	}
	fx, err := nonNegative("usdPerEur", usdPerEUR)
	if err != nil {
		return CostStatement{}, err
	}
	if fx.IsZero() {
		return CostStatement{}, fail(CodeCostInvalid, "usdPerEur must be greater than zero")
	}
	if strings.TrimSpace(modelVersion) == "" {
		return CostStatement{}, fail(CodeCostInvalid, "modelVersion is required (the Prometheus query version the quantities came from)")
	}
	st := CostStatement{TenantID: tenantID, Month: month, Quantities: map[string]string{}, Lines: map[string]string{},
		USDPerEUR: fx.String(), FXSource: "ECB_MONTH_AVG", Overhead: rates.Overhead.String(), ModelVersion: modelVersion}
	subtotal := decimal.Zero
	for _, d := range CostDimensions {
		line := qty[d].Mul(rates.UnitUSD[d])
		subtotal = subtotal.Add(line)
		st.Quantities[d] = qty[d].String()
		st.Lines[d] = roundCent(line).StringFixed(2)
	}
	raw := subtotal.Mul(decimal.NewFromInt(1).Add(rates.Overhead))
	st.CostUSD = roundCent(raw).StringFixed(2)
	// DivRound keeps enough digits that the one rounding to cents is the only rounding.
	st.CostEUR = raw.DivRound(fx, 16).Round(2).StringFixed(2)
	return st, nil
}

// CSV renders a statement as a CSV document: one line per dimension, then the totals (the CSV form of cost-model.md's
// monthly statement).
func (s CostStatement) CSV() string {
	var b strings.Builder
	b.WriteString("tenant_id,month,dimension,quantity,cost_usd\n")
	for _, d := range CostDimensions {
		fmt.Fprintf(&b, "%s,%s,%s,%s,%s\n", s.TenantID, s.Month, d, s.Quantities[d], s.Lines[d])
	}
	fmt.Fprintf(&b, "%s,%s,overhead,%s,\n", s.TenantID, s.Month, s.Overhead)
	fmt.Fprintf(&b, "%s,%s,total_usd,,%s\n", s.TenantID, s.Month, s.CostUSD)
	fmt.Fprintf(&b, "%s,%s,total_eur,,%s\n", s.TenantID, s.Month, s.CostEUR)
	fmt.Fprintf(&b, "%s,%s,usd_per_eur,%s,\n", s.TenantID, s.Month, s.USDPerEUR)
	fmt.Fprintf(&b, "%s,%s,model_version,%s,\n", s.TenantID, s.Month, s.ModelVersion)
	return b.String()
}
