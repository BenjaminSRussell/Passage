package api

import (
	"fmt"
	"math"
	"net/http"
	"slices"

	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/services/engineering/internal/domain"
)

// Column limits of route_operation and operation_requirement (migration 0003): values past them
// would fail in Postgres as a 500, so the handler answers 400 instead.
const (
	maxRouteInt        = math.MaxInt32
	quantityDecimals   = 6
	percentDecimals    = 6
	hoursOverflowExp   = 10 // numeric(14,4)
	qtyOverflowExp     = 14 // numeric(20,6)
	percentOverflowExp = 3  // numeric(9,6)
)

type routeVersionRequest struct {
	ProductID     string               `json:"productId"`
	SiteID        string               `json:"siteId"`
	RouteCode     string               `json:"routeCode"`
	VersionNo     int                  `json:"versionNo"`
	EffectiveFrom string               `json:"effectiveFrom"`
	EffectiveTo   string               `json:"effectiveTo"`
	Operations    []routeOperationBody `json:"operations"`
}

func below(d decimal.Decimal, exp int32) bool { return d.Abs().LessThan(decimal.New(1, exp)) }

func places(d decimal.Decimal, n int32) bool { return d.Round(n).Equal(d) }

// operationProblem reports the first value of an operation that its column cannot hold.
func operationProblem(o routeOperationBody) string {
	switch {
	case !finite(o.SetupH, o.RunH, o.ProcessQty, o.QueueBeforeH, o.QueueAfterH, o.TransitH, o.EfficiencyPct, o.OverlapQty, o.ScrapPercent):
		return "has a number outside the supported range"
	case o.No < 1 || o.No > maxRouteInt:
		return fmt.Sprintf("no must be 1 to %d", maxRouteInt)
	case o.NextNo < 0 || o.NextNo > maxRouteInt:
		return fmt.Sprintf("nextNo must be 0 to %d", maxRouteInt)
	case !below(o.SetupH, hoursOverflowExp) || !below(o.RunH, hoursOverflowExp) || !below(o.QueueBeforeH, hoursOverflowExp) ||
		!below(o.QueueAfterH, hoursOverflowExp) || !below(o.TransitH, hoursOverflowExp):
		return "times must be below 10000000000 hours"
	case !below(o.ProcessQty, qtyOverflowExp) || !below(o.OverlapQty, qtyOverflowExp) || !places(o.ProcessQty, quantityDecimals) || !places(o.OverlapQty, quantityDecimals):
		return fmt.Sprintf("processQty and overlapQty must be below 100000000000000 with at most %d decimals", quantityDecimals)
	case !below(o.EfficiencyPct, percentOverflowExp) || !places(o.EfficiencyPct, percentDecimals) || !places(o.ScrapPercent, percentDecimals):
		return fmt.Sprintf("efficiencyPct must be below 1000 and percentages have at most %d decimals", percentDecimals)
	}
	for j, r := range o.Requirements {
		if len(r.RefID) > maxIDLength || r.Level > maxRouteInt || r.Quantity > maxRouteInt {
			return fmt.Sprintf("requirements[%d] refId must be at most %d characters and level and quantity at most %d", j, maxIDLength, maxRouteInt)
		}
	}
	return ""
}

func (b routeVersionRequest) toDomain() (domain.RouteVersion, map[string]string) {
	fields := map[string]string{}
	validID("productId", b.ProductID, fields)
	validID("siteId", b.SiteID, fields)
	validID("routeCode", b.RouteCode, fields)
	if b.VersionNo < 0 || b.VersionNo > maxRouteInt {
		fields["versionNo"] = fmt.Sprintf("must be 1 to %d, or omitted to take the next number", maxRouteInt)
	}
	v := domain.RouteVersion{ID: ids.New(), ProductID: b.ProductID, SiteID: b.SiteID, RouteCode: b.RouteCode, VersionNo: b.VersionNo, Status: domain.StatusDraft}
	if from, ok := parseDate(b.EffectiveFrom); ok {
		v.EffectiveFrom = from
	} else {
		fields["effectiveFrom"] = "is required as YYYY-MM-DD"
	}
	v.EffectiveTo = optionalDate("effectiveTo", b.EffectiveTo, fields)
	if !v.EffectiveTo.IsZero() && !v.EffectiveFrom.IsZero() && v.EffectiveTo.Before(v.EffectiveFrom) {
		fields["effectiveTo"] = "must not be before effectiveFrom"
	}
	if len(b.Operations) == 0 {
		fields["operations"] = "must hold at least one operation"
	}
	for i, o := range b.Operations {
		if problem := operationProblem(o); problem != "" {
			fields[fmt.Sprintf("operations[%d]", i)] = problem
			continue
		}
		op := domain.VersionOperation{
			RouteOperation: domain.RouteOperation{No: o.No, NextNo: o.NextNo, Priority: o.Priority, SetupH: o.SetupH, RunH: o.RunH, ProcessQty: o.ProcessQty,
				QueueBeforeH: o.QueueBeforeH, QueueAfterH: o.QueueAfterH, TransitH: o.TransitH, EfficiencyPct: o.EfficiencyPct, OverlapQty: o.OverlapQty},
			OperationID: o.OperationID, LinkType: o.LinkType, ScrapPercent: o.ScrapPercent,
		}
		if op.ProcessQty.IsZero() {
			op.ProcessQty = decimal.NewFromInt(1)
		}
		for _, r := range o.Requirements {
			q := r.Quantity
			if q == 0 {
				q = 1
			}
			op.Requirements = append(op.Requirements, domain.OperationRequirement{Kind: r.Kind, RefID: r.RefID, Level: r.Level, Quantity: q})
		}
		v.Operations = append(v.Operations, op)
	}
	slices.SortStableFunc(v.Operations, func(a, b domain.VersionOperation) int { return a.No - b.No })
	return v, fields
}

func (a *API) createRouteVersion(w http.ResponseWriter, r *http.Request) {
	var body routeVersionRequest
	if !readBody(w, r, &body) {
		return
	}
	v, fields := body.toDomain()
	if len(fields) > 0 {
		invalid(w, r, fields)
		return
	}
	created, err := a.store.CreateRouteVersion(r.Context(), v, idemOf(r, body))
	if err != nil {
		a.fail(w, r, "create route version", err, &a.routeFailed)
		return
	}
	httpx.JSON(w, http.StatusCreated, bodyFromRouteVersion(created))
}
