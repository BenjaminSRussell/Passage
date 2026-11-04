package domain

import (
	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/platform/money"
)

// Lease recognition exemptions (IFRS 16.5-8, D4c). A lease under an election is not recognized on the
// balance sheet: no liability and no ROU asset, and the payments are expensed over the lease term.
const (
	ElectionNone      = "None"
	ElectionShortTerm = "ShortTerm"
	ElectionLowValue  = "LowValue"
)

// CodeLeaseElection is raised when a short-term or low-value election is not allowed, or when an operation
// does not apply to a lease under an election (AST-0037).
const CodeLeaseElection = "AST-0037"

// ShortTermMaxMonths is the longest term of a short-term lease (IFRS 16 Appendix A: 12 months or less at the
// commencement date). It is the PRD default of lease.shortTermMonths, which is not stored per legal entity yet.
const ShortTermMaxMonths = 12

// LowValueThresholds is the most the underlying asset may be worth when new for a low-value election, by the
// currency that value is stated in: the PRD default of lease.lowValueThreshold (USD 5,000 / EUR 5,000), after
// IFRS 16.BC100 (USD 5,000 or less). The assessment is absolute (IFRS 16.B4) and the service holds no exchange
// rates, so the value is stated in one of these currencies whatever the lease currency is; the accountant
// converts a value known in another currency at the date of the assessment.
var LowValueThresholds = map[string]decimal.Decimal{"USD": decimal.NewFromInt(5000), "EUR": decimal.NewFromInt(5000)}

// ElectionInput is what an election is checked against. UnderlyingValue and UnderlyingCurrency are the value of
// the underlying asset when new and the currency it is stated in; they belong to a LowValue election only.
type ElectionInput struct {
	Election           string
	TermMonths         int
	PurchaseOption     bool
	UnderlyingValue    *decimal.Decimal
	UnderlyingCurrency string
}

// ValidateElection checks an election against the lease (IFRS 16.5-8 and Appendix A). A short-term lease runs
// 12 months or less and contains no purchase option. A low-value lease states the value of the underlying asset
// when new, at most the threshold of the currency it is stated in. The election is recorded per lease: the lease
// carries no class of underlying asset, so making the short-term election for a whole class (IFRS 16.8) is the
// accountant's policy, applied to each lease of the class; the low-value election is lease by lease (IFRS 16.8).
func ValidateElection(in ElectionInput) error {
	switch in.Election {
	case "", ElectionNone, ElectionShortTerm:
		if in.UnderlyingValue != nil || in.UnderlyingCurrency != "" {
			return fail(CodeLeaseElection, "the underlying asset value applies to a low-value election only")
		}
		if in.Election != ElectionShortTerm {
			return nil
		}
		if in.TermMonths > ShortTermMaxMonths {
			return fail(CodeLeaseElection, "a short-term lease has a term of 12 months or less")
		}
		if in.PurchaseOption {
			return fail(CodeLeaseElection, "a lease that contains a purchase option is not a short-term lease")
		}
	case ElectionLowValue:
		if in.UnderlyingValue == nil || !in.UnderlyingValue.IsPositive() {
			return fail(CodeLeaseElection, "a low-value election needs the value of the underlying asset when new")
		}
		limit, ok := LowValueThresholds[in.UnderlyingCurrency]
		if !ok {
			return fail(CodeLeaseElection, "state the value of the underlying asset when new in USD or EUR, the currencies the low-value threshold is set in")
		}
		if !in.UnderlyingValue.Equal(in.UnderlyingValue.Round(money.MinorUnits(in.UnderlyingCurrency))) {
			return fail(CodeLeaseElection, "the underlying asset value has more decimals than "+in.UnderlyingCurrency+" allows")
		}
		if in.UnderlyingValue.GreaterThan(limit) {
			return fail(CodeLeaseElection, "the underlying asset when new is worth more than "+in.UnderlyingCurrency+" "+limit.StringFixed(2)+"; it is not a low-value lease")
		}
	default:
		return fail(CodeLeaseElection, "election must be None, ShortTerm or LowValue")
	}
	return nil
}

// ComputeExemptLease builds the schedule of a lease under an election: no liability, no ROU asset, no interest
// or depreciation, and one line per payment period whose payment is that period's expense. IFRS 16.6 expenses
// the payments on a straight-line basis over the lease term; a lease here has one level payment amount for every
// period and an exempt lease cannot be modified, so the straight-line expense of each period is its payment. If
// uneven payments are ever accepted, a period's expense becomes the total payments spread over the term instead.
func ComputeExemptLease(t LeaseTerms) (LeaseSchedule, error) {
	n, err := validateLease(t)
	if err != nil {
		return LeaseSchedule{}, err
	}
	lines := make([]LeaseLine, 0, n)
	for k := 1; k <= n; k++ {
		lines = append(lines, LeaseLine{PeriodNo: k, Payment: t.Payment})
	}
	return LeaseSchedule{Lines: lines}, nil
}

// LeaseExemptPeriodNet is the net debit (credit when negative) per account of the journal of one period of a
// lease under an election: the payment is expensed against the payment clearing account.
func LeaseExemptPeriodNet(a LeaseAccounts, payment decimal.Decimal) map[string]decimal.Decimal {
	net := map[string]decimal.Decimal{}
	addNet(net, a.ExemptLeaseExpense, payment)
	addNet(net, a.PaymentClearing, payment.Neg())
	return net
}

// CanMaintainRecognized refuses a modification or termination of a lease under an election: it has no
// liability or ROU asset to remeasure or derecognize (a change of its payments or term is a new lease, IFRS 16.7).
func CanMaintainRecognized(election string) error {
	if election != "" && election != ElectionNone {
		return fail(CodeLeaseElection, "this lease is under a "+election+" election: it has no liability or ROU asset, so it cannot be modified or terminated here")
	}
	return nil
}
