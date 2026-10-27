package domain

import (
	"time"

	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/platform/money"
)

// Contract statuses (revenue_contract.status).
const (
	ContractOpen      = "OPEN"
	ContractComplete  = "COMPLETE"
	ContractCancelled = "CANCELLED"
)

// Schedule statuses (BR-29.7). REVERSED is set only by a credit note (later slice).
const (
	ScheduleScheduled  = "SCHEDULED"
	ScheduleRecognized = "RECOGNIZED"
	ScheduleReversed   = "REVERSED"
)

// Recognition methods (FR-29.8) that give a schedule line at contract creation.
const (
	MethodPointInTime = "POINT_IN_TIME"
	MethodRatable     = "RATABLE"
	MethodMilestone   = "MILESTONE"
	MethodUsage       = "USAGE"
	SSPResidual       = "RESIDUAL"
)

// ScheduleLine is one planned recognition of an obligation before it is stored.
// Date is nil for a milestone that has not been satisfied yet.
type ScheduleLine struct {
	Seq    int
	Date   *time.Time
	Amount decimal.Decimal
}

// Warning is a non-blocking finding returned with a successful request.
type Warning struct {
	Code   string
	Detail string
}

// SSPWarnings reports BIL-0009 for every obligation priced by the residual
// method (BR-29.2). products[i] names the product of obs[i] for the message.
func SSPWarnings(obs []Obligation, products []string) []Warning {
	var out []Warning
	for i, o := range obs {
		if o.SSPSource == SSPResidual {
			out = append(out, Warning{Code: "BIL-0009", Detail: "SSP missing for " + products[i] + "; residual method used"})
		}
	}
	return out
}

// CheckSSPSources enforces that at most one obligation of a contract takes its
// SSP by the residual method (BR-29.2).
func CheckSSPSources(obs []Obligation) error {
	residual := 0
	for _, o := range obs {
		if o.SSPSource == SSPResidual {
			residual++
		}
	}
	if residual > 1 {
		return &Error{Code: "REV-0003", Detail: "at most one obligation per contract may use the RESIDUAL SSP source"}
	}
	return nil
}

// ScheduleLines plans the recognition of one allocated obligation (FR-29.8):
// RATABLE spreads Allocated over the term by RatableSchedule on the contract's
// ratable basis (ignored by the other methods), each line dated at
// the end of its period; POINT_IN_TIME is a single line dated at satisfiedOn,
// the transfer date; MILESTONE is a single undated line awaiting satisfaction;
// USAGE has no line until usage posts (later slice).
func ScheduleLines(o Obligation, currency, basis string, termStart, termEnd, satisfiedOn *time.Time) ([]ScheduleLine, error) {
	switch o.Method {
	case MethodRatable:
		if termStart == nil || termEnd == nil {
			return nil, &Error{Code: "REV-0002", Detail: "a RATABLE obligation needs a term start and end"}
		}
		periods, err := RatableSchedule(money.Amount{Value: o.Allocated, Currency: currency}, *termStart, *termEnd, basis)
		if err != nil {
			return nil, err
		}
		out := make([]ScheduleLine, len(periods))
		for i, p := range periods {
			end := p.End
			out[i] = ScheduleLine{Seq: i + 1, Date: &end, Amount: p.Amount}
		}
		return out, nil
	case MethodPointInTime:
		if satisfiedOn == nil {
			return nil, &Error{Code: "REV-0001", Detail: "a POINT_IN_TIME obligation needs its transfer date"}
		}
		return []ScheduleLine{{Seq: 1, Date: satisfiedOn, Amount: o.Allocated}}, nil
	case MethodMilestone:
		return []ScheduleLine{{Seq: 1, Amount: o.Allocated}}, nil
	case MethodUsage:
		return nil, nil
	}
	return nil, &Error{Code: "REV-0001", Detail: "unknown recognition method " + o.Method}
}

// CheckSatisfy decides whether a MILESTONE obligation may be satisfied on `on`
// (FR-29.8). It refuses a non-milestone obligation (REV-0005) and a date before
// the contract date, when nothing is deferred yet (REV-0006). A milestone
// already satisfied on the same date is an idempotent retry (replay=true); one
// satisfied on another date is a conflict (REV-0004), so it is never
// recognized twice.
func CheckSatisfy(method, scheduleStatus string, recognizedOn *time.Time, on, contractDate time.Time) (replay bool, err error) {
	if method != MethodMilestone {
		return false, &Error{Code: "REV-0005", Detail: "only a MILESTONE obligation can be satisfied"}
	}
	switch scheduleStatus {
	case ScheduleScheduled:
	case ScheduleRecognized:
		if recognizedOn != nil && recognizedOn.Equal(on) {
			return true, nil
		}
		return false, &Error{Code: "REV-0004", Detail: "the milestone is already satisfied on another date"}
	default:
		return false, &Error{Code: "REV-0004", Detail: "the milestone schedule is " + scheduleStatus + " and cannot be satisfied"}
	}
	if on.Before(contractDate) {
		return false, &Error{Code: "REV-0006", Detail: "a milestone cannot be satisfied before the contract date " + contractDate.Format("2006-01-02")}
	}
	return false, nil
}
