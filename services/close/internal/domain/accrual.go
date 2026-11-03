package domain

import (
	"time"

	"github.com/shopspring/decimal"
)

type SpreadMethod string

const (
	SpreadEqual  SpreadMethod = "equal"
	SpreadByDays SpreadMethod = "by_days"
	SpreadCustom SpreadMethod = "custom"
)

type AccrualScheme struct {
	Occurrences int
	Spread      SpreadMethod
	Custom      []decimal.Decimal
	AutoReverse bool
}

type AccrualOccurrence struct {
	Date      time.Time       `json:"date"`
	Amount    decimal.Decimal `json:"amount"`
	ReverseOn time.Time       `json:"reverseOn"`
}

func SpreadAccrual(total decimal.Decimal, start time.Time, scheme AccrualScheme, scale int32) ([]AccrualOccurrence, error) {
	n := scheme.Occurrences
	if n < 1 || n > 120 {
		return nil, fail(CodeValidation, "occurrences must be between 1 and 120")
	}
	dates := make([]time.Time, n)
	days := make([]int, n)
	totalDays := 0
	for i := 0; i < n; i++ {
		dates[i] = start.AddDate(0, i, 0)
		next := start.AddDate(0, i+1, 0)
		days[i] = int(next.Sub(dates[i]).Hours() / 24)
		totalDays += days[i]
	}
	amounts := make([]decimal.Decimal, n)
	switch scheme.Spread {
	case SpreadEqual:
		each := total.DivRound(decimal.NewFromInt(int64(n)), scale)
		assigned := decimal.Zero
		for i := 0; i < n-1; i++ {
			amounts[i] = each
			assigned = assigned.Add(each)
		}
		amounts[n-1] = total.Sub(assigned)
	case SpreadByDays:
		if totalDays == 0 {
			return nil, fail(CodeValidation, "by_days spread needs a positive number of days")
		}
		den := decimal.NewFromInt(int64(totalDays))
		assigned := decimal.Zero
		for i := 0; i < n-1; i++ {
			amounts[i] = total.Mul(decimal.NewFromInt(int64(days[i]))).DivRound(den, scale)
			assigned = assigned.Add(amounts[i])
		}
		amounts[n-1] = total.Sub(assigned)
	case SpreadCustom:
		if len(scheme.Custom) != n {
			return nil, fail(CodeValidation, "custom spread must list one amount per occurrence")
		}
		sum := decimal.Zero
		for i, a := range scheme.Custom {
			amounts[i] = a.Round(scale)
			sum = sum.Add(amounts[i])
		}
		if !sum.Equal(total.Round(scale)) {
			return nil, fail(CodeValidation, "custom spread amounts must sum to the source amount")
		}
	default:
		return nil, fail(CodeValidation, "spread must be equal, by_days or custom")
	}
	out := make([]AccrualOccurrence, n)
	for i := 0; i < n; i++ {
		out[i] = AccrualOccurrence{Date: dates[i], Amount: amounts[i], ReverseOn: start.AddDate(0, i+1, 0)}
	}
	return out, nil
}
