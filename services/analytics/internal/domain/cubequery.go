package domain

import (
	"fmt"
	"slices"
	"strings"
	"time"
)

// Cube execution (46-3): a Plan becomes the Cube load query, and a dashboard tile's period
// becomes a date range. The scope filters of the plan are added to the query as well as
// to the security context the executor signs, so a result can never be wider than the
// caller's legal-entity scope (BR-46.9), whichever of the two a Cube deployment honours.

const (
	CodeSemanticLayer = "ANA-0120" // the semantic layer (Cube) did not answer or is not configured

	// Tile statuses.
	TileOK         = "OK"
	TileNoScope    = "NO_SCOPE"
	TileNotModeled = "NOT_MODELLED"
	TileUnavail    = "UNAVAILABLE"
	// TileMixedCurrency: the tile's scope holds amounts in more than one currency, which are
	// never added together (amount_reporting is in each ledger's own currency).
	TileMixedCurrency = "MIXED_CURRENCY"
)

// cubeMeasures are the metrics the Cube model serves today. cube_contract_test.go keeps this
// list equal to the measures of data/cube/model; a metric of the catalog that is not here
// has no executable model yet, and a tile bound to it says so instead of showing a number.
var cubeMeasures = map[string]bool{"revenue": true, "cogs": true, "gross_margin": true, "gross_margin_pct": true}

// CubeServes reports whether the Cube model has a measure for the metric.
func CubeServes(metricID string) bool { return cubeMeasures[metricID] }

// CubeQuery is the body of Cube's /cubejs-api/v1/load, minus the envelope.
type CubeQuery struct {
	Measures       []string          `json:"measures"`
	Dimensions     []string          `json:"dimensions,omitempty"`
	TimeDimensions []CubeTimeDim     `json:"timeDimensions"`
	Filters        []CubeFilter      `json:"filters,omitempty"`
	Limit          int               `json:"limit,omitempty"`
	Order          map[string]string `json:"order,omitempty"`
}

// CubeTimeDim restricts a time dimension to a date range.
type CubeTimeDim struct {
	Dimension string   `json:"dimension"`
	DateRange []string `json:"dateRange"`
}

// CubeFilter is an equals filter.
type CubeFilter struct {
	Member   string   `json:"member"`
	Operator string   `json:"operator"`
	Values   []string `json:"values"`
}

// MaxCubeRows caps what one query returns to the browser.
const MaxCubeRows = 10000

// ToCubeQuery turns a plan into the Cube query. Measures and dimensions are qualified with
// the plan's model; the date range is on posting_date. It always carries the tenant filter
// (also for an unrestricted caller) besides the plan's legal-entity filters. It refuses a
// plan whose metrics have no Cube measure, a plan without the currency dimension (the
// amounts of a ledger are in its own currency, so a total across currencies is meaningless
// and the caller must group by it), and a plan that has no scope (callers answer that case
// without calling Cube at all).
func (p Plan) ToCubeQuery(tenantID string) (CubeQuery, *Error) {
	if p.Reason == ReasonNoScope {
		return CubeQuery{}, &Error{Code: CodeNoScope, Status: 403, Detail: "the caller has no legal-entity scope"}
	}
	if tenantID == "" {
		return CubeQuery{}, &Error{Code: CodeNoScope, Status: 403, Detail: "the caller has no tenant"}
	}
	q := CubeQuery{Limit: MaxCubeRows}
	for _, m := range p.Measures {
		if !CubeServes(m) || p.Model != "fin_journal_line" {
			return CubeQuery{}, validation(map[string]string{"measures": "metric " + m + " has no semantic-layer model yet"})
		}
		q.Measures = append(q.Measures, p.Model+"."+m)
	}
	if !slices.Contains(p.Dimensions, "currency") {
		return CubeQuery{}, validation(map[string]string{"dimensions": "include currency: amounts are in each ledger's own currency and are never added across currencies"})
	}
	for _, d := range p.Dimensions {
		q.Dimensions = append(q.Dimensions, p.Model+"."+d)
	}
	q.TimeDimensions = []CubeTimeDim{{Dimension: p.Model + ".posting_date", DateRange: []string{p.From, p.To}}}
	for _, f := range p.Filters {
		q.Filters = append(q.Filters, CubeFilter(f))
	}
	q.Filters = append(q.Filters, CubeFilter{Member: p.Model + ".tenant_id", Operator: "equals", Values: []string{tenantID}})
	return q, nil
}

// ShortMember drops the model prefix of a Cube member name.
func ShortMember(member string) string {
	_, short, ok := strings.Cut(member, ".")
	if !ok {
		return member
	}
	return short
}

// PeriodRange is the date range a tile period covers on the given day: month, quarter or
// year to date, or the 12 months up to and including today.
func PeriodRange(period string, now time.Time) (from, to string, err error) {
	d := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, time.UTC)
	var start time.Time
	switch period {
	case "MTD":
		start = time.Date(d.Year(), d.Month(), 1, 0, 0, 0, 0, time.UTC)
	case "QTD":
		start = time.Date(d.Year(), time.Month((int(d.Month())-1)/3*3+1), 1, 0, 0, 0, 0, time.UTC)
	case "YTD":
		start = time.Date(d.Year(), 1, 1, 0, 0, 0, 0, time.UTC)
	case "TRAILING_12M":
		start = d.AddDate(0, 0, 1).AddDate(-1, 0, 0)
	default:
		return "", "", fmt.Errorf("unknown period %q", period)
	}
	return start.Format(time.DateOnly), d.Format(time.DateOnly), nil
}

// TileValue is one dashboard tile with its current value. Value is a decimal string (Cube
// returns numbers as text; a measure without rows is null), so no float rounds a sum of money.
type TileValue struct {
	Title      string  `json:"title"`
	Metric     string  `json:"metric"`
	MetricName string  `json:"metricName"`
	Period     string  `json:"period"`
	From       string  `json:"from"`
	To         string  `json:"to"`
	Status     string  `json:"status"`
	Value      *string `json:"value"`
	Unit       string  `json:"unit,omitempty"`
	// Currency is the currency of Value when there is one (a ratio has none).
	Currency string `json:"currency,omitempty"`
}
