package api_test

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/BenjaminSRussell/passage/services/org/internal/api"
	"github.com/BenjaminSRussell/passage/services/org/internal/domain"
	"github.com/BenjaminSRussell/passage/services/org/internal/store"
)

type memVersion struct {
	typeID  string
	version store.HierarchyVersion
	key     string
}

func (m *memOrg) CreateFiscalCalendar(_ context.Context, in store.FiscalCalendar, _ string) (store.FiscalCalendar, error) {
	in.ID, in.Status, in.Version, in.GeneratedYears = m.id(), "active", 1, []int{}
	m.cals = append(m.cals, in)
	return in, nil
}

func (m *memOrg) ListFiscalCalendars(context.Context, store.FiscalCalendarFilter, string, int) (store.FiscalCalendarPage, error) {
	return store.FiscalCalendarPage{Items: m.cals}, nil
}

func (m *memOrg) GenerateFiscalYear(_ context.Context, id string, year int, custom []domain.DateRange, _ bool) (store.FiscalYear, error) {
	for i := range m.cals {
		if m.cals[i].ID != id {
			continue
		}
		m.creates++
		m.cals[i].GeneratedYears = append(m.cals[i].GeneratedYears, year)
		if m.custom == nil {
			m.custom = map[string][]store.FiscalPeriod{}
		}
		for _, c := range custom {
			key := fmt.Sprintf("%s/%d", id, year)
			m.custom[key] = append(m.custom[key], store.FiscalPeriod{Type: "operating", StartDate: c.Start.Format("2006-01-02"), EndDate: c.End.Format("2006-01-02")})
		}
		return store.FiscalYear{CalendarID: id, FiscalYear: year, Generated: true}, nil
	}
	return store.FiscalYear{}, fmt.Errorf("calendar %s not found", id)
}

func (m *memOrg) ListFiscalPeriods(_ context.Context, id string, year int) ([]store.FiscalPeriod, error) {
	return m.custom[fmt.Sprintf("%s/%d", id, year)], nil
}

func (m *memOrg) ListHierarchyVersions(_ context.Context, typeID string, _ store.HierarchyVersionFilter, _, _ int) (store.HierarchyVersionPage, error) {
	page := store.HierarchyVersionPage{}
	for _, v := range m.versions {
		if v.typeID == typeID {
			page.Items = append(page.Items, store.HierarchyVersionSummary{ID: v.version.ID, Status: v.version.Status, EffectiveFrom: v.version.EffectiveFrom, VersionNo: v.version.VersionNo})
		}
	}
	return page, nil
}

func (m *memOrg) GetHierarchyVersion(_ context.Context, id string) (store.HierarchyVersion, error) {
	for _, v := range m.versions {
		if v.version.ID == id {
			return v.version, nil
		}
	}
	return store.HierarchyVersion{}, fmt.Errorf("version %s not found", id)
}

func (m *memOrg) CreateHierarchyDraft(_ context.Context, typeID, key, _ string) (store.HierarchyVersion, error) {
	for _, v := range m.versions {
		if key != "" && v.key == key {
			return v.version, nil
		}
	}
	v := store.HierarchyVersion{ID: m.id(), HierarchyTypeID: typeID, Status: "draft", Version: 1, VersionNo: len(m.versions) + 1, Nodes: []store.HierarchyNode{}}
	m.versions = append(m.versions, memVersion{typeID, v, key})
	return v, nil
}

func (m *memOrg) HierarchyVersionDraftKeys(_ context.Context, typeID string) (map[string]string, error) {
	out := map[string]string{}
	for _, v := range m.versions {
		if v.typeID == typeID && v.key != "" {
			out[v.version.ID] = v.key
		}
	}
	return out, nil
}

func (m *memOrg) TreeLimits() domain.TreeLimits {
	if m.limits == (domain.TreeLimits{}) {
		return domain.DefaultTreeLimits
	}
	return m.limits
}

func (m *memOrg) ReplaceHierarchyNodes(_ context.Context, id string, _ int, nodes []store.HierarchyNode, _ string) (store.HierarchyVersion, error) {
	for i := range m.versions {
		if m.versions[i].version.ID == id {
			m.versions[i].version.Nodes = nodes
			m.versions[i].version.Version++
			return m.versions[i].version, nil
		}
	}
	return store.HierarchyVersion{}, fmt.Errorf("version %s not found", id)
}

func (m *memOrg) PublishHierarchyVersion(_ context.Context, id string, _ int, effectiveFrom time.Time, reason, _, _ string, backdated bool) (store.HierarchyVersion, error) {
	for i := range m.versions {
		if m.versions[i].version.ID == id {
			date := effectiveFrom.Format("2006-01-02")
			if date == m.failPublish {
				m.failPublish = ""
				return store.HierarchyVersion{}, fmt.Errorf("publish %s: connection reset", date)
			}
			m.versions[i].version.Status, m.versions[i].version.EffectiveFrom = "published", &date
			m.publishes = append(m.publishes, publishCall{date, reason, backdated})
			return m.versions[i].version, nil
		}
	}
	return store.HierarchyVersion{}, fmt.Errorf("version %s not found", id)
}

type publishCall struct {
	date, reason string
	backdated    bool
}

const timeBody = `{"schemaVersion":1,
 "legalEntities":[{"code":"USMF","name":"Demo USA","country":"US","currency":"USD","timeZone":"America/Chicago"}],
 "operatingUnits":[{"type":"department","code":"FIN","name":"Finance"},{"type":"cost_center","code":"CC-1100","name":"Assembly"}],
 "hierarchyTypes":[{"code":"REPORTING","name":"Reporting","purposes":["reporting"],"backdateAllowed":true,"versions":[
   {"effectiveFrom":"2099-01-01","nodes":[
     {"unit":{"kind":"legalEntity","code":"USMF"},"sortOrder":0},
     {"unit":{"kind":"operatingUnit","type":"department","code":"FIN"},"parent":{"kind":"legalEntity","code":"USMF"},"sortOrder":1}]},
   {"effectiveFrom":"2099-07-01","nodes":[
     {"unit":{"kind":"legalEntity","code":"USMF"},"sortOrder":0},
     {"unit":{"kind":"operatingUnit","type":"department","code":"FIN"},"parent":{"kind":"legalEntity","code":"USMF"},"sortOrder":1},
     {"unit":{"kind":"operatingUnit","type":"cost_center","code":"CC-1100"},"parent":{"kind":"operatingUnit","type":"department","code":"FIN"},"sortOrder":2}]}]}],
 "fiscalCalendars":[{"code":"STD","name":"Standard","pattern":"4-4-5","years":[{"fiscalYear":2026},{"fiscalYear":2027}]},
   {"code":"CUS","name":"Custom","pattern":"custom","years":[{"fiscalYear":2026,"periods":[{"start":"2026-01-01","end":"2026-06-30"},{"start":"2026-07-01","end":"2026-12-31"}]}]}]}`

// 1 entity, 2 units, 1 hierarchy type, 2 versions, 2 calendars, 3 years
const timeRecords = 11

func TestImportPackage_VersionsAndCalendars_RoundTrip(t *testing.T) {
	mem := &memOrg{}
	r, _ := memRouter(mem)
	code, first, raw := importPackage(t, r, "", timeBody)
	if code != http.StatusOK || first.Created != timeRecords {
		t.Fatalf("first import = %d %+v %v", code, first, raw)
	}
	if first.Kinds["hierarchyVersions"].Created != 2 || first.Kinds["fiscalCalendars"].Created != 2 || first.Kinds["fiscalYears"].Created != 3 {
		t.Fatalf("kinds = %+v", first.Kinds)
	}
	if len(mem.publishes) != 2 || mem.publishes[0].backdated || mem.publishes[0].date != "2099-01-01" {
		t.Fatalf("publishes = %+v", mem.publishes)
	}
	creates := mem.creates
	code, second, _ := importPackage(t, r, "", timeBody)
	if code != http.StatusOK || second.Created != 0 || second.Unchanged != timeRecords || len(second.Differences) != 0 || mem.creates != creates {
		t.Fatalf("second import = %d %+v (wrote %d)", code, second, mem.creates-creates)
	}

	exported := do(r, http.MethodPost, "/v1/org/export", "", packageDuties, true)
	var pkg struct {
		HierarchyTypes []struct {
			Versions []struct {
				EffectiveFrom string
				Nodes         []map[string]any
			}
		}
		FiscalCalendars []struct {
			Code  string
			Years []struct {
				FiscalYear int
				Periods    []map[string]string
			}
		}
	}
	if err := json.Unmarshal(exported.Body.Bytes(), &pkg); err != nil || exported.Code != http.StatusOK {
		t.Fatalf("export = %d %s %v", exported.Code, exported.Body, err)
	}
	versions := pkg.HierarchyTypes[0].Versions
	if len(versions) != 2 || versions[0].EffectiveFrom != "2099-01-01" || len(versions[1].Nodes) != 3 {
		t.Fatalf("exported versions = %+v", versions)
	}
	if len(pkg.FiscalCalendars) != 2 || len(pkg.FiscalCalendars[0].Years) != 2 || len(pkg.FiscalCalendars[1].Years[0].Periods) != 2 || len(pkg.FiscalCalendars[0].Years[0].Periods) != 0 {
		t.Fatalf("exported calendars = %+v", pkg.FiscalCalendars)
	}

	other := &memOrg{}
	or, _ := memRouter(other)
	code, again, raw := importPackage(t, or, "", exported.Body.String())
	if code != http.StatusOK || again.Created != timeRecords {
		t.Fatalf("import of the export = %d %+v %v", code, again, raw)
	}
	fresh := &memOrg{}
	fr, _ := memRouter(fresh)
	code, dry, _ := importPackage(t, fr, "?dryRun=true", timeBody)
	if code != http.StatusOK || dry.Created != timeRecords || fresh.creates != 0 {
		t.Fatalf("dry run = %d %+v (wrote %d)", code, dry, fresh.creates)
	}
}

func TestImportPackage_VersionAndCalendarValidation(t *testing.T) {
	node := func(code, parent string) string {
		p := ""
		if parent != "" {
			p = `,"parent":{"kind":"operatingUnit","type":"department","code":"` + parent + `"}`
		}
		return `{"unit":{"kind":"operatingUnit","type":"department","code":"` + code + `"}` + p + `}`
	}
	wrap := func(versions, calendars string) string {
		return `{"schemaVersion":1,"operatingUnits":[{"type":"department","code":"AA","name":"A"},{"type":"department","code":"BB","name":"B"}],` +
			`"hierarchyTypes":[{"code":"REP","name":"Rep","purposes":["reporting"],"versions":[` + versions + `]}],"fiscalCalendars":[` + calendars + `]}`
	}
	tests := []struct {
		name, body, path string
	}{
		{"cycle", wrap(`{"effectiveFrom":"2099-01-01","nodes":[`+node("AA", "BB")+`,`+node("BB", "AA")+`]}`, ""), "hierarchyTypes[0].versions[0].nodes"},
		{"unknown unit", wrap(`{"effectiveFrom":"2099-01-01","nodes":[`+node("ZZ", "")+`]}`, ""), "hierarchyTypes[0].versions[0].nodes[0].unit"},
		{"bad date", wrap(`{"effectiveFrom":"01/01/2099","nodes":[`+node("AA", "")+`]}`, ""), "hierarchyTypes[0].versions[0].effectiveFrom"},
		{"duplicate date", wrap(`{"effectiveFrom":"2099-01-01","nodes":[`+node("AA", "")+`]},{"effectiveFrom":"2099-01-01","nodes":[`+node("AA", "")+`]}`, ""), "hierarchyTypes[0].versions[1].effectiveFrom"},
		{"unknown unit in a version listed out of date order", wrap(`{"effectiveFrom":"2099-07-01","nodes":[`+node("AA", "")+`]},{"effectiveFrom":"2099-01-01","nodes":[`+node("ZZ", "")+`]}`, ""), "hierarchyTypes[0].versions[1].nodes[0].unit"},
		{"bad node kind", wrap(`{"effectiveFrom":"2099-01-01","nodes":[{"unit":{"kind":"site","code":"AA"}}]}`, ""), "hierarchyTypes[0].versions[0].nodes[0].unit.kind"},
		{"custom without periods", wrap("", `{"code":"C","name":"C","pattern":"custom","years":[{"fiscalYear":2026}]}`), "fiscalCalendars[0].years[0]"},
		{"periods on a generated pattern", wrap("", `{"code":"C","name":"C","pattern":"4-4-5","years":[{"fiscalYear":2026,"periods":[{"start":"2026-01-01","end":"2026-12-31"}]}]}`), "fiscalCalendars[0].years[0].periods"},
		{"bad pattern", wrap("", `{"code":"C","name":"C","pattern":"weekly","years":[]}`), "fiscalCalendars[0].pattern"},
		{"year out of range", wrap("", `{"code":"C","name":"C","pattern":"calendar","years":[{"fiscalYear":1800}]}`), "fiscalCalendars[0].years[0].fiscalYear"},
		{"duplicate year", wrap("", `{"code":"C","name":"C","pattern":"calendar","years":[{"fiscalYear":2026},{"fiscalYear":2026}]}`), "fiscalCalendars[0].years[1].fiscalYear"},
		{"bad month", wrap("", `{"code":"C","name":"C","pattern":"calendar","startMonth":13,"years":[]}`), "fiscalCalendars[0].startMonth"},
		{"duplicate calendar", wrap("", `{"code":"C","name":"C","pattern":"calendar","years":[]},{"code":"c","name":"C","pattern":"calendar","years":[]}`), "fiscalCalendars[1].code"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			mem := &memOrg{}
			r, _ := memRouter(mem)
			code, _, raw := importPackage(t, r, "", tt.body)
			errs, _ := raw["errors"].(map[string]any)
			if code != http.StatusUnprocessableEntity || raw["code"] != "ORG-PKG-INVALID" || errs[tt.path] == nil || mem.creates != 0 {
				t.Fatalf("import = %d %v (want a problem at %s, wrote %d)", code, raw, tt.path, mem.creates)
			}
		})
	}
}

func TestImportPackage_VersionsRefusedBeforeTheFirstWrite(t *testing.T) {
	seed := func() *memOrg {
		m := &memOrg{}
		r, _ := memRouter(m)
		if code, _, raw := importPackage(t, r, "", timeBody); code != http.StatusOK {
			t.Fatalf("seed import = %d %v", code, raw)
		}
		return m
	}
	later := strings.Replace(timeBody, "2099-07-01", "2099-09-01", 1)
	t.Run("a version dated before the tenant's latest", func(t *testing.T) {
		m := seed()
		r, _ := memRouter(m)
		creates := m.creates
		body := strings.Replace(timeBody, `"effectiveFrom":"2099-01-01"`, `"effectiveFrom":"2098-01-01"`, 1)
		code, _, raw := importPackage(t, r, "", body)
		errs, _ := raw["errors"].(map[string]any)
		if code != http.StatusUnprocessableEntity || errs["hierarchyTypes[0].versions[0].effectiveFrom"] == nil || m.creates != creates {
			t.Fatalf("import = %d %v wrote %d", code, raw, m.creates-creates)
		}
	})
	t.Run("an open draft", func(t *testing.T) {
		m := seed()
		m.versions = append(m.versions, memVersion{typeID: m.hier[0].ID, version: store.HierarchyVersion{ID: "draft-1", Status: "draft"}})
		r, _ := memRouter(m)
		creates := m.creates
		code, _, raw := importPackage(t, r, "", strings.Replace(later, "2099-07-01", "2100-01-01", 1))
		errs, _ := raw["errors"].(map[string]any)
		if code != http.StatusUnprocessableEntity || errs["hierarchyTypes[0].versions"] == nil || m.creates != creates {
			t.Fatalf("import = %d %v wrote %d", code, raw, m.creates-creates)
		}
	})
	t.Run("a different node set is reported, not applied", func(t *testing.T) {
		m := seed()
		r, _ := memRouter(m)
		creates := m.creates
		body := strings.Replace(timeBody, `,
     {"unit":{"kind":"operatingUnit","type":"cost_center","code":"CC-1100"},"parent":{"kind":"operatingUnit","type":"department","code":"FIN"},"sortOrder":2}`, "", 1)
		code, report, raw := importPackage(t, r, "", body)
		if code != http.StatusOK || report.Created != 0 || len(report.Differences) != 1 || report.Differences[0].Field != "nodes" || report.Differences[0].Code != "REPORTING/2099-07-01" || m.creates != creates {
			t.Fatalf("import = %d %+v %v", code, report, raw)
		}
	})
	t.Run("a past-dated history loads into a fresh type without the backdate duty", func(t *testing.T) {
		past := strings.Replace(strings.Replace(timeBody, "2099-01-01", "2020-01-01", 1), `"backdateAllowed":true`, `"backdateAllowed":false`, 1)
		m := &memOrg{}
		r, _ := memRouter(m)
		code, report, raw := importPackage(t, r, "", past)
		if code != http.StatusOK || report.Kinds["hierarchyVersions"].Created != 2 || len(m.publishes) != 2 {
			t.Fatalf("import = %d %v", code, raw)
		}
		if m.publishes[0].backdated || m.publishes[0].reason == "" || m.publishes[1].backdated || m.publishes[1].reason != "" {
			t.Fatalf("publishes = %+v, want no backdate, a reason on the past date only", m.publishes)
		}
	})
	t.Run("a past date after versions published in the designer needs the duty and an allowing type", func(t *testing.T) {
		history := strings.Replace(strings.Replace(strings.Replace(timeBody, "2099-01-01", "2019-01-01", 1), "2099-07-01", "2021-01-01", 1), `"backdateAllowed":true`, `"backdateAllowed":false`, 1)
		m := &memOrg{}
		r, _ := memRouter(m)
		if code, _, raw := importPackage(t, r, "", history); code != http.StatusOK {
			t.Fatalf("seed = %d %v", code, raw)
		}
		for i := range m.versions {
			m.versions[i].key = "" // as if published in the designer
		}
		next := strings.Replace(history, "2021-01-01", "2022-01-01", 1)
		creates, publishes := m.creates, len(m.publishes)
		code, _, raw := importPackage(t, r, "", next)
		errs, _ := raw["errors"].(map[string]any)
		if code != http.StatusUnprocessableEntity || errs["hierarchyTypes[0].versions[1].effectiveFrom"] == nil || m.creates != creates {
			t.Fatalf("without the duty = %d %v wrote %d", code, raw, m.creates-creates)
		}
		rec := do(r, http.MethodPost, "/v1/org/import", next, packageDuties+",org.hierarchy.backdate", true)
		if rec.Code != http.StatusUnprocessableEntity || !strings.Contains(rec.Body.String(), "does not allow backdated versions") || m.creates != creates {
			t.Fatalf("type without backdating = %d %s", rec.Code, rec.Body)
		}
		m.hier[0].BackdateAllowed = true
		rec = do(r, http.MethodPost, "/v1/org/import", next, packageDuties+",org.hierarchy.backdate", true)
		if rec.Code != http.StatusOK || len(m.publishes) != publishes+1 || !m.publishes[publishes].backdated || m.publishes[publishes].reason == "" {
			t.Fatalf("with the duty = %d %s %+v", rec.Code, rec.Body, m.publishes)
		}
	})
	t.Run("a re-run finishes the draft a failed run left open", func(t *testing.T) {
		past := strings.Replace(strings.Replace(strings.Replace(timeBody, "2099-01-01", "2020-01-01", 1), "2099-07-01", "2021-06-01", 1), `"backdateAllowed":true`, `"backdateAllowed":false`, 1)
		m := &memOrg{failPublish: "2021-06-01"}
		r, _ := memRouter(m)
		if code, _, raw := importPackage(t, r, "", past); code != http.StatusInternalServerError {
			t.Fatalf("failing import = %d %v", code, raw)
		}
		if len(m.versions) != 2 || m.versions[1].version.Status != "draft" || len(m.publishes) != 1 {
			t.Fatalf("after the failure versions = %+v publishes = %+v", m.versions, m.publishes)
		}
		code, report, raw := importPackage(t, r, "", past)
		if code != http.StatusOK || report.Kinds["hierarchyVersions"] != (api.ImportCounts{Created: 1, Unchanged: 1}) {
			t.Fatalf("re-run = %d %v", code, raw)
		}
		if len(m.versions) != 2 || m.versions[1].version.Status != "published" || len(m.publishes) != 2 || m.publishes[1].backdated {
			t.Fatalf("after the re-run versions = %+v publishes = %+v", m.versions, m.publishes)
		}
	})
	t.Run("a security hierarchy's new versions are skipped one by one", func(t *testing.T) {
		body := strings.Replace(timeBody, `"purposes":["reporting"]`, `"purposes":["security"]`, 1)
		m := &memOrg{}
		r, _ := memRouter(m)
		code, report, raw := importPackage(t, r, "", body)
		if code != http.StatusOK || len(report.Skipped) != 2 || report.Skipped[0].Code != "REPORTING/2099-01-01" || report.Skipped[1].Code != "REPORTING/2099-07-01" ||
			len(m.publishes) != 0 || len(m.versions) != 0 || report.Kinds["hierarchyVersions"].Created != 0 {
			t.Fatalf("import = %d %+v %v", code, report, raw)
		}
	})
	t.Run("a tree larger than the configured limit", func(t *testing.T) {
		m := &memOrg{limits: domain.TreeLimits{MaxDepth: 15, MaxNodes: 2}}
		r, _ := memRouter(m)
		code, _, raw := importPackage(t, r, "", timeBody)
		errs, _ := raw["errors"].(map[string]any)
		if code != http.StatusUnprocessableEntity || errs["hierarchyTypes[0].versions[1].nodes"] == nil || errs["hierarchyTypes[0].versions[0].nodes"] != nil || m.creates != 0 {
			t.Fatalf("import = %d %v wrote %d", code, raw, m.creates)
		}
	})
	t.Run("an existing calendar with other settings", func(t *testing.T) {
		m := seed()
		r, _ := memRouter(m)
		creates := m.creates
		body := strings.Replace(timeBody, `"pattern":"4-4-5","years":[{"fiscalYear":2026},{"fiscalYear":2027}]`, `"pattern":"4-4-5","startMonth":4,"years":[{"fiscalYear":2026},{"fiscalYear":2028}]`, 1)
		code, _, raw := importPackage(t, r, "", body)
		errs, _ := raw["errors"].(map[string]any)
		if code != http.StatusUnprocessableEntity || errs["fiscalCalendars[0].years[1]"] == nil || m.creates != creates {
			t.Fatalf("import = %d %v wrote %d", code, raw, m.creates-creates)
		}
	})
	t.Run("an inactive calendar cannot get a new year", func(t *testing.T) {
		m := seed()
		m.cals[0].Status = "inactive"
		r, _ := memRouter(m)
		body := strings.Replace(timeBody, `{"fiscalYear":2027}`, `{"fiscalYear":2028}`, 1)
		code, _, raw := importPackage(t, r, "", body)
		errs, _ := raw["errors"].(map[string]any)
		if code != http.StatusUnprocessableEntity || errs["fiscalCalendars[0].years[1]"] == nil {
			t.Fatalf("import = %d %v", code, raw)
		}
	})
}
