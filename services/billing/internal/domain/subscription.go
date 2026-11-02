package domain

import (
	"time"

	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/platform/money"
)

const (
	SubscriptionActive    = "ACTIVE"
	SubscriptionCancelled = "CANCELLED"

	CodeNothingToBill      = "BIL-0105"
	CodePeriodInvoiced     = "BIL-0106"
	CodeEffectiveInvoiced  = "BIL-0107"
	CodeSubscriptionClosed = "BIL-0108"
	maxSubscriptionPeriods = 12
)

type PlanTerm struct {
	PlanCode string
	Price    decimal.Decimal
	From     time.Time
}

type Subscription struct {
	Currency        string
	PeriodMonths    int
	Start           time.Time
	CancelEffective *time.Time
	Terms           []PlanTerm
}

type BilledLine struct {
	PlanCode string
	From     time.Time
	To       time.Time
	Amount   decimal.Decimal
}

func ValidPeriodMonths(n int) bool { return n == 1 || n == 3 || n == 6 || n == maxSubscriptionPeriods }

func (s Subscription) PeriodEnd(periodFrom time.Time) time.Time {
	return dateOnly(periodFrom).AddDate(0, s.PeriodMonths, -1)
}

func (s Subscription) Bill(periodFrom time.Time) ([]BilledLine, error) {
	pf := dateOnly(periodFrom)
	if pf.Day() != 1 {
		return nil, &Error{Code: "COM-0001", Detail: "a billing period opens on the first of a month"}
	}
	pt := s.PeriodEnd(pf)
	var out []BilledLine
	for i, term := range s.Terms {
		from, to := dateOnly(term.From), pt
		if i+1 < len(s.Terms) {
			to = dateOnly(s.Terms[i+1].From).AddDate(0, 0, -1)
		}
		if s.CancelEffective != nil && dateOnly(*s.CancelEffective).Before(to) {
			to = dateOnly(*s.CancelEffective)
		}
		if to.Before(from) {
			continue
		}
		amount, err := Prorate(money.Amount{Value: term.Price, Currency: s.Currency}, pf, pt, from, to)
		if err != nil {
			return nil, err
		}
		if amount.Value.IsPositive() {
			out = append(out, BilledLine{PlanCode: term.PlanCode, From: maxDate(from, pf), To: minDate(to, pt), Amount: amount.Value})
		}
	}
	if len(out) == 0 {
		return nil, &Error{Code: CodeNothingToBill, Detail: "the subscription has no billable amount in the period starting " + pf.Format("2006-01-02")}
	}
	return out, nil
}

func (s Subscription) CheckChange(from time.Time) error {
	if s.CancelEffective != nil {
		return &Error{Code: CodeSubscriptionClosed, Detail: "a cancelled subscription cannot change plan"}
	}
	if !dateOnly(from).After(dateOnly(s.Terms[len(s.Terms)-1].From)) {
		return &Error{Code: "COM-0001", Detail: "a plan change must take effect after the current plan began"}
	}
	return nil
}

func (s Subscription) CheckCancel(on time.Time) error {
	if s.CancelEffective != nil {
		return &Error{Code: CodeSubscriptionClosed, Detail: "the subscription is already cancelled"}
	}
	if dateOnly(on).Before(dateOnly(s.Terms[len(s.Terms)-1].From)) {
		return &Error{Code: "COM-0001", Detail: "the last service day must not be before the current plan began"}
	}
	return nil
}
