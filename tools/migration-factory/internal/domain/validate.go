package domain

import (
	"fmt"
	"strings"

	"github.com/shopspring/decimal"
)

// Record is one legacy open item as extracted, before load (open_items pack shape).
type Record struct {
	LegacyID string
	Party    string
	Account  string
	Currency string
	Amount   decimal.Decimal
}

// Rule kinds from FR-49.11 Great Expectations suites.
const (
	RuleReferential = "REFERENTIAL"
	RuleDomain      = "DOMAIN"
	RuleUniqueness  = "UNIQUENESS"
)

// iso4217 is the currency subset the domain check accepts. The org service serves the
// full ISO 4217 list; the migration factory keeps its own copy so it has no runtime dependency.
var iso4217 = map[string]struct{}{
	"USD": {}, "EUR": {}, "GBP": {}, "CAD": {}, "MXN": {}, "INR": {}, "JPY": {}, "CHF": {},
	"AUD": {}, "CNY": {}, "SEK": {}, "NOK": {}, "DKK": {}, "BRL": {}, "SGD": {}, "HKD": {},
}

// Quarantined is a record held back from load with the rule and reason (FR-49.11).
type Quarantined struct {
	Record Record
	Rule   string
	Reason string
}

// Validate applies the open-items suite. parties is the set of vendor/customer ids known to
// master data. It returns the accepted records and the quarantined ones; the caller loads
// accepted records only.
func Validate(records []Record, parties map[string]struct{}) (accepted []Record, quarantined []Quarantined) {
	seen := make(map[string]struct{}, len(records))
	for _, r := range records {
		if strings.TrimSpace(r.LegacyID) == "" {
			quarantined = append(quarantined, Quarantined{r, RuleUniqueness, "legacy id is blank"})
			continue
		}
		if _, dup := seen[r.LegacyID]; dup {
			quarantined = append(quarantined, Quarantined{r, RuleUniqueness, "legacy id appears twice in the extract"})
			continue
		}
		seen[r.LegacyID] = struct{}{}
		if _, ok := parties[r.Party]; !ok {
			quarantined = append(quarantined, Quarantined{r, RuleReferential, fmt.Sprintf("party %q is not in master data", r.Party)})
			continue
		}
		if _, ok := iso4217[r.Currency]; !ok {
			quarantined = append(quarantined, Quarantined{r, RuleDomain, fmt.Sprintf("currency %q is not ISO 4217", r.Currency)})
			continue
		}
		if strings.TrimSpace(r.Account) == "" {
			quarantined = append(quarantined, Quarantined{r, RuleDomain, "control account is blank"})
			continue
		}
		accepted = append(accepted, r)
	}
	return accepted, quarantined
}

// ParseQuarantineMaxPct reads migration.quarantine_max_pct: blank means the 1.0 default, and the
// value must lie in 0-10 (9.49.13).
func ParseQuarantineMaxPct(s string) (decimal.Decimal, error) {
	if strings.TrimSpace(s) == "" {
		return decimal.NewFromInt(1), nil
	}
	v, err := decimal.NewFromString(strings.TrimSpace(s))
	if err != nil || v.IsNegative() || v.GreaterThan(decimal.NewFromInt(10)) {
		return decimal.Decimal{}, fmt.Errorf("%w: quarantineMaxPct %q must be a number from 0 to 10", ErrInvalid, s)
	}
	return v, nil
}

// QuarantineShareExceeded returns MIG-1504 when quarantined records exceed maxPct of the
// extract. maxPct is the tenant parameter migration.quarantine_max_pct (0-10).
func QuarantineShareExceeded(quarantined, total int, maxPct decimal.Decimal) error {
	if total == 0 {
		return nil
	}
	pct := decimal.NewFromInt(int64(quarantined)).Mul(decimal.NewFromInt(100)).Div(decimal.NewFromInt(int64(total)))
	if pct.GreaterThan(maxPct) {
		return fmt.Errorf("%w: MIG-1504 quarantine share %s%% exceeds %s%%; load stopped", ErrInvalid, pct.StringFixed(2), maxPct.StringFixed(2))
	}
	return nil
}
