package domain

import (
	"fmt"
	"slices"
	"sort"
	"strings"
	"time"

	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/services/engineering/internal/events"
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

const (
	LinkHard = "hard"
	LinkSoft = "soft"

	maxRouteOperations   = 500
	maxOperationIDLength = 20
	timeDecimals         = 4
)

type OperationRequirement struct {
	Kind     string
	RefID    string
	Level    int
	Quantity int
}

type VersionOperation struct {
	RouteOperation
	OperationID  string
	LinkType     string
	OverlapQty   decimal.Decimal
	ScrapPercent decimal.Decimal
	Requirements []OperationRequirement
}

type RouteVersion struct {
	ID            string
	ProductID     string
	SiteID        string
	RouteCode     string
	VersionNo     int
	EffectiveFrom time.Time
	EffectiveTo   time.Time
	Status        string
	Operations    []VersionOperation
}

func (v RouteVersion) ActivatedEvent() events.RouteVersionActivatedData {
	return events.RouteVersionActivatedData{
		RouteVersionID: v.ID, ProductID: v.ProductID, SiteID: v.SiteID, EffectiveFrom: v.EffectiveFrom.Format(dateLayout),
	}
}

func (v RouteVersion) EnsureDraft() error {
	if v.Status != StatusDraft {
		return errf(CodeRouteState, "route version %s is %s, only a draft can be activated", v.ID, v.Status)
	}
	return nil
}

func validateVersionOperation(o VersionOperation) error {
	if err := validateOperation(o.RouteOperation); err != nil {
		return err
	}
	for _, h := range []decimal.Decimal{o.SetupH, o.RunH, o.QueueBeforeH, o.QueueAfterH, o.TransitH} {
		if !h.Round(timeDecimals).Equal(h) {
			return errf(CodeRouteQty, "operation %d times are in hours with at most %d decimals", o.No, timeDecimals)
		}
	}
	if !slices.Contains([]string{"", PriorityPrimary, "secondary1", "secondary2", "secondary3"}, o.Priority) || (o.LinkType != "" && o.LinkType != LinkHard && o.LinkType != LinkSoft) ||
		len(o.OperationID) > maxOperationIDLength || o.OverlapQty.IsNegative() || o.ScrapPercent.IsNegative() || o.ScrapPercent.GreaterThan(hundred) {
		return errf(CodeRouteQty, "operation %d has an invalid priority, link type, operation id, overlap quantity or scrap percent", o.No)
	}
	for i, r := range o.Requirements {
		if !slices.Contains([]string{ReqResource, ReqGroup, ReqCapability, ReqType}, r.Kind) || r.RefID == "" || r.Quantity < 1 || r.Level < 0 {
			return errf(CodeRouteReqKind, "operation %d requirement[%d] needs a supported kind, a reference, quantity of at least 1 and a level of 0 or more", o.No, i)
		}
	}
	return nil
}

func ValidateRouteVersion(v RouteVersion) error {
	switch {
	case v.ProductID == "" || v.SiteID == "" || v.RouteCode == "" || v.VersionNo < 1 || v.EffectiveFrom.IsZero() ||
		(!v.EffectiveTo.IsZero() && v.EffectiveTo.Before(v.EffectiveFrom)):
		return errf(CodeRouteVersion, "route version needs product, site, route code, version number and a valid date range")
	case len(v.Operations) == 0:
		return errf(CodeRouteQty, "route version needs at least one operation")
	case len(v.Operations) > maxRouteOperations:
		return errf(CodeRouteOpsLimit, "route version has %d operations, the limit is %d", len(v.Operations), maxRouteOperations)
	}
	next := make(map[int]int, len(v.Operations))
	for _, o := range v.Operations {
		if _, dup := next[o.No]; dup {
			return errf(CodeRouteOpDup, "operation number %d appears more than once", o.No)
		}
		next[o.No] = o.NextNo
	}
	for _, o := range v.Operations {
		if _, ok := next[o.NextNo]; o.NextNo != 0 && !ok {
			return errf(CodeRouteNext, "operation %d points to operation %d, which is not in this version", o.No, o.NextNo)
		}
		if err := validateVersionOperation(o); err != nil {
			return err
		}
	}
	for start := range next {
		for step, at := 0, start; at != 0; step, at = step+1, next[at] {
			if step > len(next) {
				return errf(CodeRouteCycle, "operation %d is part of a cycle", start)
			}
		}
	}
	return nil
}

func CheckRequirements(v RouteVersion, resources []Resource) error {
	var atSite []Resource
	var groups []ResourceGroup
	for _, r := range resources {
		if r.SiteID == v.SiteID {
			atSite = append(atSite, r)
			groups = append(groups, ResourceGroup{ID: r.GroupID})
		}
	}
	var unmet []string
	for _, o := range v.Operations {
		for i, r := range o.Requirements {
			need := r.Quantity
			if r.Kind == ReqResource {
				need = 1
			}
			req := Requirement{Kind: r.Kind, ID: r.RefID, MinLevel: r.Level, Type: r.RefID}
			if len(Satisfies(req, atSite, groups, v.EffectiveFrom)) < need {
				unmet = append(unmet, fmt.Sprintf("operation %d requirement[%d]", o.No, i))
			}
		}
	}
	if len(unmet) > 0 {
		return errf(CodeRouteUnsatisfied, "no stored resource at site %s satisfies: %s", v.SiteID, strings.Join(unmet, ", "))
	}
	return nil
}

func CheckRouteNoOverlap(existing []RouteVersion, candidate RouteVersion) error {
	for _, other := range existing {
		if other.ID != candidate.ID && other.Status == StatusActive && other.ProductID == candidate.ProductID &&
			other.SiteID == candidate.SiteID && rangesOverlap(other.EffectiveFrom, other.EffectiveTo, candidate.EffectiveFrom, candidate.EffectiveTo) {
			return errf(CodeRouteOverlap, "Version overlaps active route version %s for this site and date.", other.ID)
		}
	}
	return nil
}
