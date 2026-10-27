package domain

import (
	"fmt"
	"time"
)

// Query limits from §9.46.5: at most 10 measures, 8 dimensions and a five-year
// day-grain range.
const (
	MaxMeasures    = 10
	MaxDimensions  = 8
	MaxSpanYears   = 5
	CodeValidation = "COM-0001"
)

// Error is a domain failure that the API maps to an RFC 9457 problem.
type Error struct {
	Code   string
	Status int
	Detail string
	Fields map[string]string
}

func (e *Error) Error() string { return e.Code + ": " + e.Detail }

// Query is a request for measures by dimensions over a day-grain range.
type Query struct {
	Measures   []string
	Dimensions []string
	From       time.Time
	To         time.Time
}

// Plan is what the analytics service asks Cube to run. Filters always carry the
// caller's scope. Reason NO_SCOPE means the executor returns no rows and sends
// nothing to Cube.
type Plan struct {
	Model      string   `json:"model"`
	Grain      string   `json:"grain"`
	Measures   []string `json:"measures"`
	Dimensions []string `json:"dimensions"`
	From       string   `json:"from"`
	To         string   `json:"to"`
	Filters    []Filter `json:"filters"`
	Reason     string   `json:"reason,omitempty"`
}

func validation(fields map[string]string) *Error {
	return &Error{Code: CodeValidation, Status: 400, Detail: "request failed validation", Fields: fields}
}

// ValidateShape checks the limits that do not depend on the catalog.
func ValidateShape(q Query) *Error {
	fields := map[string]string{}
	switch {
	case len(q.Measures) == 0:
		fields["measures"] = "at least one measure is required"
	case len(q.Measures) > MaxMeasures:
		fields["measures"] = fmt.Sprintf("at most %d measures", MaxMeasures)
	}
	if len(q.Dimensions) > MaxDimensions {
		fields["dimensions"] = fmt.Sprintf("at most %d dimensions", MaxDimensions)
	}
	switch {
	case q.From.IsZero() || q.To.IsZero():
		fields["from"] = "from and to are required as YYYY-MM-DD"
	case q.To.Before(q.From):
		fields["to"] = "to must not be before from"
	case q.To.After(q.From.AddDate(MaxSpanYears, 0, 0)):
		fields["to"] = fmt.Sprintf("range must not exceed %d years", MaxSpanYears)
	}
	if len(fields) > 0 {
		return validation(fields)
	}
	return nil
}

// BuildPlan validates q against the published catalog and the caller's scope.
// All measures must be published and share one grain; every dimension must be
// allowed by every measure.
func BuildPlan(q Query, catalog []Metric, s Scope) (Plan, *Error) {
	if err := ValidateShape(q); err != nil {
		return Plan{}, err
	}
	var grain string
	allowed := map[string]bool{}
	for i, id := range q.Measures {
		m, ok := CatalogMetric(catalog, id)
		if !ok {
			return Plan{}, validation(map[string]string{"measures": "unknown or unpublished metric " + id})
		}
		if i == 0 {
			grain = m.Grain
		} else if m.Grain != grain {
			return Plan{}, validation(map[string]string{"measures": "measures must share one grain"})
		}
		next := map[string]bool{}
		for _, d := range m.AllowedDimensions {
			if i == 0 || allowed[d] {
				next[d] = true
			}
		}
		allowed = next
	}
	for _, d := range q.Dimensions {
		if !allowed[d] {
			return Plan{}, validation(map[string]string{"dimensions": "dimension " + d + " is not allowed for these measures"})
		}
	}
	model := ModelForGrain(grain)
	scope, err := CompileScope(model, s)
	if err != nil {
		return Plan{}, err
	}
	// Lists are never nil, so the plan always encodes them as JSON arrays.
	return Plan{
		Model: model, Grain: grain,
		Measures:   append([]string{}, q.Measures...),
		Dimensions: append([]string{}, q.Dimensions...),
		From:       q.From.Format(time.DateOnly), To: q.To.Format(time.DateOnly),
		Filters: append([]Filter{}, scope.Filters...), Reason: scope.Reason,
	}, nil
}

// ModelForGrain names the Cube model that serves a metric grain.
func ModelForGrain(grain string) string {
	switch grain {
	case GrainSalesLine:
		return "sales_line"
	case GrainResourceShift:
		return "prod_resource_shift"
	}
	return "fin_journal_line"
}
