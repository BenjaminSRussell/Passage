package domain

import (
	"sort"

	"github.com/shopspring/decimal"
)

const PriorityPrimary = "primary"

type RouteOperation struct {
	No            int             `json:"no"`
	NextNo        int             `json:"next"`
	Priority      string          `json:"priority"`
	SetupH        decimal.Decimal `json:"setup"`
	RunH          decimal.Decimal `json:"run"`
	ProcessQty    decimal.Decimal `json:"processQty"`
	QueueBeforeH  decimal.Decimal `json:"queueBefore"`
	QueueAfterH   decimal.Decimal `json:"queueAfter"`
	TransitH      decimal.Decimal `json:"transit"`
	EfficiencyPct decimal.Decimal `json:"efficiencyPct"`
}

type LeadTimeResult struct {
	Hours        decimal.Decimal
	CalendarDays int
}

func (o RouteOperation) isPrimary() bool { return o.Priority == "" || o.Priority == PriorityPrimary }

func (o RouteOperation) duration(qty decimal.Decimal) decimal.Decimal {
	eff := o.EfficiencyPct
	if eff.IsZero() {
		eff = hundred
	}
	run := o.RunH.Mul(qty).Div(o.ProcessQty).Mul(hundred).Div(eff)
	return o.SetupH.Add(run).Add(o.QueueBeforeH).Add(o.QueueAfterH).Add(o.TransitH)
}

func validateOperation(o RouteOperation) error {
	if !o.ProcessQty.IsPositive() || o.SetupH.IsNegative() || o.RunH.IsNegative() || o.QueueBeforeH.IsNegative() ||
		o.QueueAfterH.IsNegative() || o.TransitH.IsNegative() || o.EfficiencyPct.IsNegative() {
		return errf(CodeRouteQty, "operation %d has a non-positive process quantity or a negative time", o.No)
	}
	return nil
}

func successors(ops []RouteOperation) (map[int]int, error) {
	next := make(map[int]int, len(ops))
	explicit := false
	exists := make(map[int]bool, len(ops))
	for _, o := range ops {
		exists[o.No] = true
		explicit = explicit || o.NextNo != 0
	}
	primaryJoins := map[int]bool{}
	for i, o := range ops {
		n := o.NextNo
		if !explicit && i < len(ops)-1 {
			n = ops[i+1].No
		}
		if n != 0 && !exists[n] {
			return nil, errf(CodeRouteNextMiss, "Next operation %d does not exist.", n)
		}
		next[o.No] = n
		if o.isPrimary() {
			primaryJoins[n] = true
		}
	}
	for _, o := range ops {
		if !o.isPrimary() && !primaryJoins[next[o.No]] {
			return nil, errf(CodeRouteNoPrimary, "Secondary operation %d has no primary at its join.", o.No)
		}
	}
	return next, nil
}

func LeadTime(ops []RouteOperation, qty, hoursPerDay decimal.Decimal) (LeadTimeResult, error) {
	if !qty.IsPositive() || !hoursPerDay.IsPositive() {
		return LeadTimeResult{}, errf(CodeRouteQty, "quantity and hours per day must be positive")
	}
	ordered := append([]RouteOperation(nil), ops...)
	sort.Slice(ordered, func(i, j int) bool { return ordered[i].No < ordered[j].No })
	byNo := make(map[int]RouteOperation, len(ordered))
	for _, o := range ordered {
		if err := validateOperation(o); err != nil {
			return LeadTimeResult{}, err
		}
		byNo[o.No] = o
	}
	next, err := successors(ordered)
	if err != nil {
		return LeadTimeResult{}, err
	}
	preds := map[int][]int{}
	for no, n := range next {
		preds[n] = append(preds[n], no)
	}
	finish := map[int]decimal.Decimal{}
	visiting := map[int]bool{}
	var finishOf func(no int) (decimal.Decimal, error)
	finishOf = func(no int) (decimal.Decimal, error) {
		if f, ok := finish[no]; ok {
			return f, nil
		}
		if visiting[no] {
			return decimal.Zero, errf(CodeRouteCycle, "operation %d is part of a cycle", no)
		}
		visiting[no] = true
		start := decimal.Zero
		for _, p := range preds[no] {
			pf, err := finishOf(p)
			if err != nil {
				return decimal.Zero, err
			}
			start = decimal.Max(start, pf)
		}
		visiting[no] = false
		finish[no] = start.Add(byNo[no].duration(qty))
		return finish[no], nil
	}
	total, ends := decimal.Zero, 0
	for _, o := range ordered {
		f, err := finishOf(o.No)
		if err != nil {
			return LeadTimeResult{}, err
		}
		if next[o.No] == 0 {
			total, ends = decimal.Max(total, f), ends+1
		}
	}
	if ends == 0 && len(ordered) > 0 {
		return LeadTimeResult{}, errf(CodeRouteCycle, "route has no final operation")
	}
	return LeadTimeResult{Hours: total.Round(4), CalendarDays: int(total.Div(hoursPerDay).Ceil().IntPart())}, nil
}
