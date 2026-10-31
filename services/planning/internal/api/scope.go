package api

import (
	"fmt"
	"time"

	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/services/planning/internal/domain"
)

const (
	maxScopeValues = 1000
	maxAttrLen     = 64
)

// scopeDTO is the plan scope filter of a run (FR-22.13): the version reports
// only the items that match every given criterion. The whole snapshot is still
// planned, so dependent demand from items outside the scope is not lost.
type scopeDTO struct {
	ItemIDs    []string `json:"itemIds,omitempty"`
	ItemGroups []string `json:"itemGroups,omitempty"`
	Planners   []string `json:"planners,omitempty"`
	// LLCMin and LLCMax bound the low-level code (0 is an end item), inclusive.
	LLCMin *int `json:"llcMin,omitempty"`
	LLCMax *int `json:"llcMax,omitempty"`
}

func (s *scopeDTO) validate(fields map[string]string) {
	if s == nil {
		return
	}
	for name, vals := range map[string][]string{"itemGroups": s.ItemGroups, "planners": s.Planners} {
		if len(vals) > maxScopeValues {
			fields["scope."+name] = fmt.Sprintf("holds at most %d values", maxScopeValues)
		}
		for _, v := range vals {
			if v == "" || len(v) > maxAttrLen {
				fields["scope."+name] = fmt.Sprintf("each value must be 1 to %d characters", maxAttrLen)
			}
		}
	}
	if len(s.ItemIDs) > maxScopeValues {
		fields["scope.itemIds"] = fmt.Sprintf("holds at most %d ids", maxScopeValues)
	}
	for _, id := range s.ItemIDs {
		if !ids.Valid(id) {
			fields["scope.itemIds"] = "each id must be a UUID"
		}
	}
	for name, n := range map[string]*int{"llcMin": s.LLCMin, "llcMax": s.LLCMax} {
		if n != nil && (*n < 0 || *n > 99) {
			fields["scope."+name] = "must be 0 to 99"
		}
	}
	if s.LLCMin != nil && s.LLCMax != nil && *s.LLCMin > *s.LLCMax {
		fields["scope.llcMax"] = "must be at least llcMin"
	}
}

func (s *scopeDTO) toDomain() domain.Scope {
	if s == nil {
		return domain.Scope{}
	}
	return domain.Scope{ItemIDs: s.ItemIDs, ItemGroups: s.ItemGroups, Planners: s.Planners, LLCMin: s.LLCMin, LLCMax: s.LLCMax}
}

// validateHorizon checks the optional per-run horizon (PRD 4.22.13: 30 to 1,095 days).
func validateHorizon(fields map[string]string, days int) {
	if days != 0 && (days < domain.MinHorizonDays || days > domain.MaxHorizonDays) {
		fields["horizonDays"] = fmt.Sprintf("must be 0 (service default) or %d to %d", domain.MinHorizonDays, domain.MaxHorizonDays)
	}
}

// WithHorizon sets the service default planning horizon (PLANNING_HORIZON_DAYS);
// demand dated beyond it is ignored by a run that names no horizon of its own.
// 0 means no limit.
func (a *API) WithHorizon(days int) *API {
	a.horizonDays = days
	return a
}

// plan runs the engine over a snapshot like every run does: demand beyond the
// planning horizon is ignored (FR-22.13, 4.22.13), the snapshot is planned and
// the result is restricted to the plan scope. It returns the number of items
// the version reports.
func (a *API) plan(req runRequest, asOf time.Time, items []domain.Item) (domain.Result, int, error) {
	horizon := req.HorizonDays
	if horizon == 0 {
		horizon = a.horizonDays
	}
	res, clipped, err := domain.RunWithinHorizon(0, req.Calendar.toDomain(asOf), items, horizon)
	if err != nil {
		return domain.Result{}, 0, err
	}
	attrs := make(map[string]domain.ItemAttrs, len(req.Items))
	for _, it := range req.Items {
		attrs[it.ItemID] = domain.ItemAttrs{Group: it.ItemGroup, Planner: it.Planner}
	}
	return domain.ApplyScope(res, clipped, attrs, req.Scope.toDomain())
}
