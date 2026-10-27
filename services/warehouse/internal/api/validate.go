package api

import (
	"fmt"
	"slices"
	"strings"

	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/services/warehouse/internal/domain"
)

// validateSimulate returns field errors keyed by a JSON-like path, or none.
func validateSimulate(req simulateRequest) map[string]string {
	fields := map[string]string{}
	l := req.Line
	if strings.TrimSpace(l.ItemID) == "" {
		fields["line.itemId"] = "is required"
	}
	if l.WorkType == "" {
		fields["line.workType"] = "is required"
	}
	if !domain.ValidDirection(l.Direction) {
		fields["line.direction"] = "must be PUT or PICK"
	}
	if !ids.Valid(l.Warehouse) {
		fields["line.warehouseId"] = "must be a UUID"
	}
	if !l.Qty.IsPositive() {
		fields["line.qty"] = "must be greater than zero"
	}
	if l.Weight.IsNegative() {
		fields["line.weight"] = "must not be negative"
	}
	if l.Volume.IsNegative() {
		fields["line.volume"] = "must not be negative"
	}
	if l.LPs < 0 {
		fields["line.lps"] = "must not be negative"
	}
	if len(req.Directives) > maxDirectives {
		fields["directives"] = fmt.Sprintf("must hold at most %d directives", maxDirectives)
	}
	directiveIDs := map[string]bool{}
	for i, d := range req.Directives {
		path := fmt.Sprintf("directives[%d]", i)
		validateDirective(fields, path, d)
		if d.ID != "" && directiveIDs[d.ID] {
			fields[path+".id"] = "is used by another directive"
		}
		directiveIDs[d.ID] = true
	}
	if len(req.Locations) > maxLocations {
		fields["locations"] = fmt.Sprintf("must hold at most %d locations", maxLocations)
	}
	locationIDs := map[string]bool{}
	for i, loc := range req.Locations {
		path := fmt.Sprintf("locations[%d]", i)
		validateLocation(fields, path, loc)
		if loc.ID != "" && locationIDs[loc.ID] {
			fields[path+".id"] = "is used by another location"
		}
		locationIDs[loc.ID] = true
	}
	return fields
}

func validateDirective(fields map[string]string, path string, d domain.Directive) {
	if d.ID == "" {
		fields[path+".id"] = "is required"
	}
	if d.WorkType == "" {
		fields[path+".workType"] = "is required"
	}
	if !domain.ValidDirection(d.Direction) {
		fields[path+".direction"] = "must be PUT or PICK"
	}
	if !ids.Valid(d.Warehouse) {
		fields[path+".warehouseId"] = "must be a UUID"
	}
	if len(d.Bands) == 0 {
		fields[path+".lines"] = "must hold at least one line"
	}
	for j, b := range d.Bands {
		bp := fmt.Sprintf("%s.lines[%d]", path, j)
		if b.FromQty.IsNegative() {
			fields[bp+".fromQty"] = "must not be negative"
		}
		if b.ToQty != nil && !b.ToQty.GreaterThan(b.FromQty) {
			fields[bp+".toQty"] = "must be greater than fromQty"
		}
		if len(b.Actions) == 0 {
			fields[bp+".actions"] = "must hold at least one action"
		}
		for k, a := range b.Actions {
			if !domain.ValidStrategy(a.Strategy) {
				fields[fmt.Sprintf("%s.actions[%d].strategy", bp, k)] = "is not a known strategy"
			}
		}
	}
	if j, k, ok := overlappingBands(d.Bands); ok {
		fields[fmt.Sprintf("%s.lines[%d].fromQty", path, k)] = fmt.Sprintf("overlaps the quantity range of lines[%d]", j)
	}
}

// overlappingBands returns the indexes of two bands whose [fromQty, toQty)
// ranges intersect. BR-31.1 step 2 needs the bands of one directive to be
// mutually exclusive, or the band picked would depend on its seq alone.
func overlappingBands(bands []domain.Band) (int, int, bool) {
	order := make([]int, len(bands))
	for i := range order {
		order[i] = i
	}
	slices.SortStableFunc(order, func(a, b int) int { return bands[a].FromQty.Cmp(bands[b].FromQty) })
	for n := 1; n < len(order); n++ {
		prev, next := bands[order[n-1]], bands[order[n]]
		if prev.ToQty == nil || prev.ToQty.GreaterThan(next.FromQty) {
			return order[n-1], order[n], true
		}
	}
	return 0, 0, false
}

var locationStatuses = []domain.LocationStatus{domain.StatusActive, domain.StatusBlockedIn, domain.StatusBlockedOut, domain.StatusBlocked, domain.StatusInactive}

func validateLocation(fields map[string]string, path string, loc domain.Location) {
	if loc.ID == "" {
		fields[path+".id"] = "is required"
	}
	if loc.Name == "" {
		fields[path+".name"] = "is required"
	}
	if loc.Zone == "" {
		fields[path+".zone"] = "is required"
	}
	known := false
	for _, s := range locationStatuses {
		known = known || loc.Status == s
	}
	if !known {
		fields[path+".status"] = "is not a known status"
	}
	c := loc.Capacity
	if (c.MaxWeight != nil && c.MaxWeight.IsNegative()) || (c.MaxVolume != nil && c.MaxVolume.IsNegative()) ||
		(c.MaxLPs != nil && *c.MaxLPs < 0) || (c.MaxItems != nil && *c.MaxItems < 0) {
		fields[path+".capacity"] = "limits must not be negative"
	}
	if loc.Used.Weight.IsNegative() || loc.Used.Volume.IsNegative() || loc.Used.LPs < 0 {
		fields[path+".used"] = "must not be negative"
	}
	for _, c := range loc.AllowedStorageClasses {
		if strings.TrimSpace(c) == "" {
			fields[path+".allowedStorageClasses"] = "must not hold a blank storage class"
			break
		}
	}
	for item, q := range loc.Stock {
		if item == "" || q.IsNegative() {
			fields[path+".stock"] = "must map item ids to quantities of zero or more"
			break
		}
	}
}
