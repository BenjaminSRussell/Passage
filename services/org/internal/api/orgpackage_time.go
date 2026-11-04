package api

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"sort"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/platform/tenant"
	"github.com/BenjaminSRussell/passage/services/org/internal/domain"
	"github.com/BenjaminSRussell/passage/services/org/internal/store"
)

const (
	kindFiscalCalendar    = "fiscalCalendars"
	kindFiscalYear        = "fiscalYears"
	kindHierarchyVersion  = "hierarchyVersions"
	nodeKindLegalEntity   = "legalEntity"
	nodeKindOperatingUnit = "operatingUnit"
	importPublishReason   = "Imported from an organization package"
	securityPurpose       = "security"
)

// packageFiscalCalendar is a fiscal calendar with the years generated for it. Periods are
// listed only for a custom calendar (its operating ranges); every other pattern is generated
// by the service from the settings, so the package carries no period rows for it.
type packageFiscalCalendar struct {
	Code           string              `json:"code"`
	Name           string              `json:"name"`
	Pattern        string              `json:"pattern"`
	StartMonth     int                 `json:"startMonth,omitempty"`
	WeekStart      string              `json:"weekStart,omitempty"`
	OpeningClosing *bool               `json:"openingClosing,omitempty"`
	SpecialPeriods int                 `json:"specialPeriods,omitempty"`
	Years          []packageFiscalYear `json:"years"`
}

type packageFiscalYear struct {
	FiscalYear int                `json:"fiscalYear"`
	Periods    []packageDateRange `json:"periods,omitempty"`
}

type packageDateRange struct {
	Start string `json:"start"`
	End   string `json:"end"`
}

// packageHierarchyVersion is a published version of a hierarchy type. Its natural key within
// the type is effectiveFrom; nodes name org units by code.
type packageHierarchyVersion struct {
	EffectiveFrom string        `json:"effectiveFrom"`
	Nodes         []packageNode `json:"nodes"`
}

type packageNode struct {
	Unit      packageNodeRef  `json:"unit"`
	Parent    *packageNodeRef `json:"parent,omitempty"`
	SortOrder int             `json:"sortOrder"`
}

// packageNodeRef names a legal entity (kind legalEntity) or an operating unit (kind
// operatingUnit, with its type) by code.
type packageNodeRef struct {
	Kind string `json:"kind"`
	Type string `json:"type,omitempty"`
	Code string `json:"code"`
}

func (r packageNodeRef) key() string {
	if r.Kind == nodeKindLegalEntity {
		return nodeKindLegalEntity + "/" + strings.ToUpper(r.Code)
	}
	return nodeKindOperatingUnit + "/" + r.Type + "/" + strings.ToUpper(r.Code)
}

func (r packageNodeRef) label() string {
	if r.Kind == nodeKindLegalEntity {
		return "legal entity " + r.Code
	}
	return "operating unit " + r.Type + "/" + r.Code
}

// calendarSettings applies the create defaults of POST /v1/org/fiscal-calendars.
func calendarSettings(c packageFiscalCalendar) store.FiscalCalendar {
	out := store.FiscalCalendar{Code: strings.ToUpper(c.Code), Name: strings.TrimSpace(c.Name), Pattern: c.Pattern, StartMonth: c.StartMonth, WeekStart: c.WeekStart,
		OpeningClosing: true, SpecialPeriods: c.SpecialPeriods}
	if out.StartMonth == 0 {
		out.StartMonth = 1
	}
	if out.WeekStart == "" {
		out.WeekStart = "monday"
	}
	if c.OpeningClosing != nil {
		out.OpeningClosing = *c.OpeningClosing
	}
	return out
}

func customRanges(periods []packageDateRange) ([]domain.DateRange, bool) {
	out := make([]domain.DateRange, 0, len(periods))
	for _, p := range periods {
		start, startOK := parseDate(p.Start)
		end, endOK := parseDate(p.End)
		if !startOK || !endOK {
			return nil, false
		}
		out = append(out, domain.DateRange{Start: start, End: end})
	}
	return out, true
}

func (c packageFiscalCalendar) spec(in store.FiscalCalendar, custom []domain.DateRange) domain.CalendarSpec {
	weekStart, _ := store.WeekdayNumber(in.WeekStart)
	return domain.CalendarSpec{Pattern: domain.Pattern(in.Pattern), StartMonth: time.Month(in.StartMonth), WeekStart: time.Weekday(weekStart),
		OpeningClosing: in.OpeningClosing, SpecialPeriods: in.SpecialPeriods, Custom: custom}
}

func validatePackageCalendar(path string, c packageFiscalCalendar, p packageProblems, dup func(path, kind, key string)) {
	in := calendarSettings(c)
	if !siteCode.MatchString(in.Code) {
		p.add(path+".code", "must be 1 to 10 characters of A-Z, 0-9, _ or -, starting with A-Z or 0-9")
	}
	for field, rule := range validateShared(&in.Name, nil, nil, nil) {
		p.add(path+"."+field, rule)
	}
	settingsOK := true
	if _, err := domain.ParsePattern(c.Pattern); err != nil {
		p.add(path+".pattern", "must be calendar, 4-4-5, 4-5-4, 5-4-4, 13-period or custom")
		settingsOK = false
	}
	if in.StartMonth < 1 || in.StartMonth > 12 {
		p.add(path+".startMonth", "must be 1 to 12")
		settingsOK = false
	}
	if !validWeekday(in.WeekStart) {
		p.add(path+".weekStart", "must be a lower-case day name from sunday to saturday")
		settingsOK = false
	}
	if in.SpecialPeriods < 0 || in.SpecialPeriods > 4 {
		p.add(path+".specialPeriods", "must be 0 to 4")
		settingsOK = false
	}
	dup(path+".code", "fiscal calendar", in.Code)
	for j, y := range c.Years {
		ypath := fmt.Sprintf("%s.years[%d]", path, j)
		if y.FiscalYear < minFiscalYear || y.FiscalYear > maxFiscalYear {
			p.add(ypath+".fiscalYear", fmt.Sprintf("must be an integer from %d to %d", minFiscalYear, maxFiscalYear))
			continue
		}
		dup(ypath+".fiscalYear", "fiscal year", fmt.Sprintf("%s/%d", in.Code, y.FiscalYear))
		custom, datesOK := customRanges(y.Periods)
		if !datesOK {
			p.add(ypath+".periods", "every start and end must be a date as YYYY-MM-DD")
			continue
		}
		if !settingsOK {
			continue
		}
		if c.Pattern != string(domain.PatternCustom) && len(y.Periods) > 0 {
			p.add(ypath+".periods", "periods are accepted for a custom calendar only")
			continue
		}
		if _, err := domain.GenerateYear(c.spec(in, custom), y.FiscalYear); err != nil {
			p.add(ypath, err.Error())
		}
	}
}

// validatePackageVersions checks each version's dates and tree shape against the limits the
// store applies (ORG_HIERARCHY_MAX_DEPTH and _NODES), so a too-large tree is refused before any write.
func validatePackageVersions(path string, t packageHierarchyType, p packageProblems, dup func(path, kind, key string), limits domain.TreeLimits) {
	code := strings.ToUpper(t.Code)
	for j, v := range t.Versions {
		vpath := fmt.Sprintf("%s.versions[%d]", path, j)
		if _, ok := parseDate(v.EffectiveFrom); !ok {
			p.add(vpath+".effectiveFrom", "must be a date as YYYY-MM-DD")
		} else {
			dup(vpath+".effectiveFrom", "hierarchy version", code+"/"+v.EffectiveFrom)
		}
		nodes := make([]domain.Node, len(v.Nodes))
		shapeOK := true
		for k, n := range v.Nodes {
			npath := fmt.Sprintf("%s.nodes[%d]", vpath, k)
			if !validNodeRef(npath+".unit", n.Unit, p) {
				shapeOK = false
			}
			nodes[k].ID = n.Unit.key()
			if n.Parent != nil {
				if !validNodeRef(npath+".parent", *n.Parent, p) {
					shapeOK = false
				}
				nodes[k].ParentID = n.Parent.key()
			}
		}
		if shapeOK {
			if err := domain.ValidateTree(nodes, limits); err != nil {
				p.add(vpath+".nodes", err.Error())
			}
		}
	}
}

func validNodeRef(path string, r packageNodeRef, p packageProblems) bool {
	ok := true
	switch r.Kind {
	case nodeKindLegalEntity:
	case nodeKindOperatingUnit:
		if !slices.Contains(operatingUnitTypes, r.Type) {
			p.add(path+".type", "must be department, cost_center, business_unit or value_stream")
			ok = false
		}
	default:
		p.add(path+".kind", "must be legalEntity or operatingUnit")
		return false
	}
	if c := strings.TrimSpace(r.Code); c == "" || utf8.RuneCountInString(c) > 20 {
		p.add(path+".code", "must be 1 to 20 characters")
		ok = false
	}
	return ok
}

// ----- export -----

func exportFiscalCalendars(ctx context.Context, st store.Store, pkg *OrgPackage) error {
	page, err := st.ListFiscalCalendars(ctx, store.FiscalCalendarFilter{}, "", packageRowCap)
	if err != nil {
		return err
	}
	if page.NextCode != "" {
		return tooLarge("fiscal calendars")
	}
	for _, c := range page.Items {
		oc := c.OpeningClosing
		out := packageFiscalCalendar{Code: c.Code, Name: c.Name, Pattern: c.Pattern, StartMonth: c.StartMonth, WeekStart: c.WeekStart, OpeningClosing: &oc,
			SpecialPeriods: c.SpecialPeriods, Years: []packageFiscalYear{}}
		for _, year := range c.GeneratedYears {
			y := packageFiscalYear{FiscalYear: year}
			if c.Pattern == string(domain.PatternCustom) {
				periods, err := st.ListFiscalPeriods(ctx, c.ID, year)
				if err != nil {
					return err
				}
				for _, fp := range periods {
					if fp.Type == string(domain.PeriodOperating) {
						y.Periods = append(y.Periods, packageDateRange{Start: fp.StartDate, End: fp.EndDate})
					}
				}
			}
			out.Years = append(out.Years, y)
		}
		pkg.FiscalCalendars = append(pkg.FiscalCalendars, out)
	}
	return nil
}

// exportVersions returns the non-draft versions of a type, oldest first, with their nodes
// named by code. A draft is work in progress and is not part of the package.
func exportVersions(ctx context.Context, st store.Store, typeID string, refs map[string]packageNodeRef) ([]packageHierarchyVersion, error) {
	page, err := st.ListHierarchyVersions(ctx, typeID, store.HierarchyVersionFilter{}, 0, packageRowCap)
	if err != nil {
		return nil, err
	}
	if page.NextVersionNo != 0 {
		return nil, tooLarge("hierarchy versions")
	}
	out := []packageHierarchyVersion{}
	for _, s := range page.Items {
		if s.Status == "draft" || s.EffectiveFrom == nil {
			continue
		}
		full, err := st.GetHierarchyVersion(ctx, s.ID)
		if err != nil {
			return nil, err
		}
		v := packageHierarchyVersion{EffectiveFrom: *s.EffectiveFrom, Nodes: []packageNode{}}
		for _, n := range full.Nodes {
			unit, ok := refs[n.OrgUnitID]
			if !ok {
				return nil, fmt.Errorf("hierarchy version %s names org unit %s that is neither a listed legal entity nor an operating unit", s.ID, n.OrgUnitID)
			}
			node := packageNode{Unit: unit, SortOrder: n.SortOrder}
			if n.ParentOrgUnitID != nil {
				parent, ok := refs[*n.ParentOrgUnitID]
				if !ok {
					return nil, fmt.Errorf("hierarchy version %s names parent %s that is neither a listed legal entity nor an operating unit", s.ID, *n.ParentOrgUnitID)
				}
				node.Parent = &parent
			}
			v.Nodes = append(v.Nodes, node)
		}
		sort.SliceStable(v.Nodes, func(i, j int) bool { return v.Nodes[i].SortOrder < v.Nodes[j].SortOrder })
		out = append(out, v)
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].EffectiveFrom < out[j].EffectiveFrom })
	return out, nil
}

// ----- import -----

// loadTime reads the fiscal calendars of the tenant.
func (im *importer) loadCalendars() error {
	im.cals = map[string]store.FiscalCalendar{}
	page, err := im.st.ListFiscalCalendars(im.ctx, store.FiscalCalendarFilter{}, "", packageRowCap)
	if err != nil {
		return err
	}
	if page.NextCode != "" {
		return tooLarge("fiscal calendars")
	}
	for _, c := range page.Items {
		im.cals[c.Code] = c
	}
	return nil
}

func settingsDifference(existing, wanted store.FiscalCalendar) string {
	switch {
	case existing.Pattern != wanted.Pattern:
		return "pattern"
	case existing.StartMonth != wanted.StartMonth:
		return "startMonth"
	case existing.WeekStart != wanted.WeekStart:
		return "weekStart"
	case existing.OpeningClosing != wanted.OpeningClosing:
		return "openingClosing"
	case existing.SpecialPeriods != wanted.SpecialPeriods:
		return "specialPeriods"
	}
	return ""
}

// resolveCalendars refuses a package whose years would be generated for an existing calendar
// with other settings, or into an inactive calendar.
func (im *importer) resolveCalendars(pkg OrgPackage, p packageProblems) {
	for i, c := range pkg.FiscalCalendars {
		wanted := calendarSettings(c)
		existing, ok := im.cals[wanted.Code]
		if !ok {
			continue
		}
		generated := map[int]bool{}
		for _, y := range existing.GeneratedYears {
			generated[y] = true
		}
		for j, y := range c.Years {
			if generated[y.FiscalYear] {
				continue
			}
			ypath := fmt.Sprintf("%s.years[%d]", indexPath(kindFiscalCalendar, i), j)
			if field := settingsDifference(existing, wanted); field != "" {
				p.add(ypath, fmt.Sprintf("fiscal calendar %s exists with another %s, so year %d would not match the package", wanted.Code, field, y.FiscalYear))
			} else if existing.Status != statusActive {
				p.add(ypath, "fiscal calendar "+wanted.Code+" is inactive; a year cannot be generated for it")
			}
		}
	}
}

func (im *importer) fiscalCalendars(pkg OrgPackage) error {
	for _, c := range pkg.FiscalCalendars {
		wanted := calendarSettings(c)
		existing, ok := im.cals[wanted.Code]
		generated := map[int]bool{}
		id := "dry:" + wanted.Code
		if ok {
			id = existing.ID
			for _, y := range existing.GeneratedYears {
				generated[y] = true
			}
			im.unchanged(kindFiscalCalendar, wanted.Code, record{name: existing.Name}, wanted.Name)
			if field := settingsDifference(existing, wanted); field != "" {
				im.report.Differences = append(im.report.Differences, ImportDifference{Kind: kindFiscalCalendar, Code: wanted.Code, Field: field})
			}
		} else {
			if !im.dryRun {
				created, err := im.st.CreateFiscalCalendar(im.ctx, wanted, keyPrefix+"fiscal-calendar:"+wanted.Code)
				if err != nil {
					return err
				}
				id = created.ID
			}
			im.tally(kindFiscalCalendar, false)
		}
		for _, y := range c.Years {
			code := fmt.Sprintf("%s/%d", wanted.Code, y.FiscalYear)
			if generated[y.FiscalYear] {
				im.tally(kindFiscalYear, true)
				continue
			}
			if !im.dryRun {
				custom, _ := customRanges(y.Periods)
				if _, err := im.st.GenerateFiscalYear(im.ctx, id, y.FiscalYear, custom, false); err != nil {
					return fmt.Errorf("generate fiscal year %s: %w", code, err)
				}
			}
			im.tally(kindFiscalYear, false)
		}
	}
	return nil
}

// versionState is what the tenant holds of one hierarchy type before the import.
type versionState struct {
	published map[string]string // effectiveFrom -> version id
	keys      map[string]string // version id -> the idempotency key it was drafted with
	latest    string
	draftID   string // the open draft, if any
	security  bool
}

// versionDraftKey is the idempotency key of the draft an import makes for one version; it lets a
// re-run of the same package find and finish a draft an earlier run left open.
func versionDraftKey(code, effectiveFrom string) string {
	return keyPrefix + "hier-draft:" + code + "/" + effectiveFrom
}

// refIDs maps an org unit id of the tenant back to its package reference key.
func (im *importer) refIDs() map[string]string {
	out := map[string]string{}
	for code, e := range im.entities {
		out[e.id] = packageNodeRef{Kind: nodeKindLegalEntity, Code: code}.key()
	}
	for key, u := range im.units {
		typ, code, _ := strings.Cut(key, "/")
		out[u.id] = packageNodeRef{Kind: nodeKindOperatingUnit, Type: typ, Code: code}.key()
	}
	return out
}

func (im *importer) knownUnit(r packageNodeRef, inPackage map[string]bool) bool {
	if r.Kind == nodeKindLegalEntity {
		_, ok := im.entities[strings.ToUpper(r.Code)]
		return ok || inPackage[r.key()]
	}
	_, ok := im.units[r.Type+"/"+strings.ToUpper(r.Code)]
	return ok || inPackage[r.key()]
}

func (im *importer) loadVersionState(typeID string) (versionState, error) {
	state := versionState{published: map[string]string{}, keys: map[string]string{}}
	page, err := im.st.ListHierarchyVersions(im.ctx, typeID, store.HierarchyVersionFilter{}, 0, packageRowCap)
	if err != nil {
		return state, err
	}
	if page.NextVersionNo != 0 {
		return state, tooLarge("hierarchy versions")
	}
	for _, v := range page.Items {
		switch {
		case v.Status == "draft":
			state.draftID = v.ID
		case v.EffectiveFrom != nil:
			state.published[*v.EffectiveFrom] = v.ID
			state.latest = max(state.latest, *v.EffectiveFrom)
		}
	}
	state.keys, err = im.st.HierarchyVersionDraftKeys(im.ctx, typeID)
	return state, err
}

// ownHistory reports whether every published version of the type was drafted by a package
// import at its own date and is in this package. Loading the rest of such a history into the
// tenant (a first import, or a re-run after a failure) changes no as-of answer that anything
// but the import wrote, so a date in the past is not a backdate under BR-6.3.
func (state versionState) ownHistory(code string, inPackage map[string]bool) bool {
	for date, id := range state.published {
		if state.keys[id] != versionDraftKey(code, date) || !inPackage[date] {
			return false
		}
	}
	return true
}

// resolveVersions checks, before the first write, every rule publishing applies: the unit
// references, the strictly later effective date, the one-open-draft rule (a draft an earlier
// run of this import left open is finished, not refused), backdating and the maker-checker rule
// of a security-purpose hierarchy (whose new versions are not imported).
func (im *importer) resolveVersions(pkg OrgPackage, p packageProblems) error {
	inPackage := map[string]bool{}
	for _, e := range pkg.LegalEntities {
		inPackage[packageNodeRef{Kind: nodeKindLegalEntity, Code: e.Code}.key()] = true
	}
	for _, u := range pkg.OperatingUnits {
		inPackage[packageNodeRef{Kind: nodeKindOperatingUnit, Type: u.Type, Code: u.Code}.key()] = true
	}
	im.versions, im.backdated = map[string]versionState{}, map[string]bool{}
	principal, _ := tenant.FromContext(im.ctx)
	for i, t := range pkg.HierarchyTypes {
		if len(t.Versions) == 0 {
			continue
		}
		code := strings.ToUpper(t.Code)
		tpath := indexPath(kindHierarchyType, i)
		existing, exists := im.hier[code]
		purposes, backdateAllowed := t.Purposes, t.BackdateAllowed
		state := versionState{published: map[string]string{}, keys: map[string]string{}}
		if exists {
			purposes, backdateAllowed = existing.purposes, existing.backdate
			var err error
			if state, err = im.loadVersionState(existing.id); err != nil {
				return err
			}
		}
		state.security = slices.Contains(purposes, securityPurpose)
		im.versions[code] = state
		dates := map[string]bool{}
		for _, v := range t.Versions {
			dates[v.EffectiveFrom] = true
		}
		own := state.ownHistory(code, dates)
		// owners maps a draft key to a version listed as published; the keys are read after the
		// listing, so a version a concurrent run made in between is not mistaken for one.
		owners := map[string]string{}
		for _, id := range state.published {
			if key, ok := state.keys[id]; ok {
				owners[key] = id
			}
		}
		latest, first := state.latest, true
		for _, j := range versionOrder(t.Versions) {
			v := t.Versions[j]
			vpath := fmt.Sprintf("%s.versions[%d]", tpath, j)
			if _, ok := state.published[v.EffectiveFrom]; ok {
				continue
			}
			if state.security {
				im.report.Skipped = append(im.report.Skipped, ImportSkip{Kind: kindHierarchyVersion, Code: code + "/" + v.EffectiveFrom,
					Reason: "a security-purpose hierarchy must be published by someone other than its editor; publish this version in the designer"})
				continue
			}
			for k, n := range v.Nodes {
				npath := fmt.Sprintf("%s.nodes[%d]", vpath, k)
				if !im.knownUnit(n.Unit, inPackage) {
					p.add(npath+".unit", "unknown "+n.Unit.label())
				}
				if n.Parent != nil && !im.knownUnit(*n.Parent, inPackage) {
					p.add(npath+".parent", "unknown "+n.Parent.label())
				}
			}
			key := versionDraftKey(code, v.EffectiveFrom)
			if first && state.draftID != "" && state.keys[state.draftID] != key {
				p.add(tpath+".versions", "hierarchy type "+code+" has an open draft; publish or delete it in the designer before importing versions")
			}
			first = false
			if _, taken := owners[key]; taken {
				p.add(vpath+".effectiveFrom", "an earlier import drafted this version and it was published in the designer with another date; take the version out of the package")
			}
			if latest != "" && v.EffectiveFrom <= latest {
				p.add(vpath+".effectiveFrom", "must be later than "+latest+", the latest version of hierarchy type "+code)
			}
			latest = max(latest, v.EffectiveFrom)
			if v.EffectiveFrom >= im.today || own {
				continue
			}
			im.backdated[code+"/"+v.EffectiveFrom] = true
			if !principal.HasDuty(dutyBackdate) {
				p.add(vpath+".effectiveFrom", "a version dated in the past, after versions the tenant already has, needs the "+dutyBackdate+" duty")
			} else if !backdateAllowed {
				p.add(vpath+".effectiveFrom", "hierarchy type "+code+" does not allow backdated versions")
			}
		}
	}
	return nil
}

// versionOrder returns the indexes of the versions, oldest first, so each is published before
// the next one is drafted and problems still name the version's place in the package.
func versionOrder(in []packageHierarchyVersion) []int {
	order := make([]int, len(in))
	for i := range order {
		order[i] = i
	}
	sort.SliceStable(order, func(a, b int) bool { return in[order[a]].EffectiveFrom < in[order[b]].EffectiveFrom })
	return order
}

func (im *importer) hierarchyVersions(pkg OrgPackage) error {
	ids := im.refIDs()
	principal, _ := tenant.FromContext(im.ctx)
	idOf := func(r packageNodeRef) string {
		if r.Kind == nodeKindLegalEntity {
			return im.entities[strings.ToUpper(r.Code)].id
		}
		return im.units[r.Type+"/"+strings.ToUpper(r.Code)].id
	}
	for _, t := range pkg.HierarchyTypes {
		code := strings.ToUpper(t.Code)
		state := im.versions[code]
		for _, j := range versionOrder(t.Versions) {
			v := t.Versions[j]
			key := code + "/" + v.EffectiveFrom
			if id, ok := state.published[v.EffectiveFrom]; ok {
				same, err := im.sameNodes(id, v, ids)
				if err != nil {
					return err
				}
				if !same {
					im.report.Differences = append(im.report.Differences, ImportDifference{Kind: kindHierarchyVersion, Code: key, Field: "nodes"})
				}
				im.tally(kindHierarchyVersion, true)
				continue
			}
			if state.security {
				continue // listed under skipped by resolveVersions
			}
			if !im.dryRun {
				nodes := make([]store.HierarchyNode, len(v.Nodes))
				for i, n := range v.Nodes {
					nodes[i] = store.HierarchyNode{OrgUnitID: idOf(n.Unit), SortOrder: n.SortOrder}
					if n.Parent != nil {
						parent := idOf(*n.Parent)
						nodes[i].ParentOrgUnitID = &parent
					}
				}
				if err := im.publishVersion(code, v.EffectiveFrom, nodes, principal.UserID); err != nil {
					return err
				}
			}
			im.tally(kindHierarchyVersion, false)
		}
	}
	return nil
}

// publishVersion drafts, fills and publishes one version. The draft key returns the draft an
// earlier run of this package left open (or a concurrent run made), and the publish key replays
// a publish a concurrent run already made; a version conflict from a concurrent run writing the
// same nodes is retried once on the current version.
func (im *importer) publishVersion(code, date string, nodes []store.HierarchyNode, userID string) error {
	effectiveFrom, _ := parseDate(date)
	draft, err := im.st.CreateHierarchyDraft(im.ctx, im.hier[code].id, versionDraftKey(code, date), userID)
	if err != nil {
		return err
	}
	version := draft.Version
	if draft.Status == "draft" {
		edited, err := im.onCurrentVersion(draft.ID, version, func(version int) (store.HierarchyVersion, error) {
			return im.st.ReplaceHierarchyNodes(im.ctx, draft.ID, version, nodes, userID)
		})
		if err != nil {
			current, getErr := im.st.GetHierarchyVersion(im.ctx, draft.ID)
			if getErr != nil || current.Status == "draft" {
				return err
			}
			edited = current // published meanwhile; the publish below replays it or refuses
		}
		version = edited.Version
	}
	reason := ""
	if date < im.today {
		reason = importPublishReason
	}
	_, err = im.onCurrentVersion(draft.ID, version, func(version int) (store.HierarchyVersion, error) {
		return im.st.PublishHierarchyVersion(im.ctx, draft.ID, version, effectiveFrom, reason, keyPrefix+"hier-publish:"+code+"/"+date, userID, im.backdated[code+"/"+date])
	})
	return err
}

// onCurrentVersion runs write and, when it fails on a stale version, once more on the current one.
func (im *importer) onCurrentVersion(id string, version int, write func(int) (store.HierarchyVersion, error)) (store.HierarchyVersion, error) {
	out, err := write(version)
	var problem *httpx.Problem
	if !errors.As(err, &problem) || problem.Code != store.CodeVersionConflict {
		return out, err
	}
	current, getErr := im.st.GetHierarchyVersion(im.ctx, id)
	if getErr != nil {
		return out, err
	}
	return write(current.Version)
}

// sameNodes reports whether the tenant's version holds the package's (unit, parent) pairs.
func (im *importer) sameNodes(versionID string, v packageHierarchyVersion, ids map[string]string) (bool, error) {
	full, err := im.st.GetHierarchyVersion(im.ctx, versionID)
	if err != nil {
		return false, err
	}
	have := map[string]bool{}
	for _, n := range full.Nodes {
		parent := ""
		if n.ParentOrgUnitID != nil {
			parent = ids[*n.ParentOrgUnitID]
		}
		have[ids[n.OrgUnitID]+">"+parent] = true
	}
	if len(full.Nodes) != len(v.Nodes) {
		return false, nil
	}
	for _, n := range v.Nodes {
		parent := ""
		if n.Parent != nil {
			parent = n.Parent.key()
		}
		if !have[n.Unit.key()+">"+parent] {
			return false, nil
		}
	}
	return true, nil
}
