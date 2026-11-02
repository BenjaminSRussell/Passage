package api_test

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/BenjaminSRussell/passage/services/engineering/internal/domain"
)

func altBom(extra string, lines ...string) string {
	return `{"productId":"PACK","effectiveFrom":"2026-01-01"` + extra + `,"lines":[` + strings.Join(lines, ",") + `]}`
}

const (
	caseLine = `{"componentProductId":"CASE","quantity":"1"}`
	cellA    = `{"componentProductId":"CELL-A","quantity":"4","altGroup":"A1","altStrategy":"usage_probability","usageProbabilityPct":"60"}`
	cellB60  = `{"componentProductId":"CELL-B","quantity":"4","altGroup":"A1","altStrategy":"usage_probability","usageProbabilityPct":"40"}`
	cellB30  = `{"componentProductId":"CELL-B","quantity":"4","altGroup":"A1","altStrategy":"usage_probability","usageProbabilityPct":"30"}`
)

func TestCreateBomVersion_AlternativeGroups(t *testing.T) {
	tests := []struct {
		name   string
		body   string
		status int
		want   string
	}{
		{"60 and 40 is accepted", altBom("", caseLine, cellA, cellB60), http.StatusCreated, `"altGroup":"A1"`},
		{"60 and 30 is ENG-ALT-CONFLICT", altBom("", caseLine, cellA, cellB30), http.StatusUnprocessableEntity, domain.CodeAltConflict},
		{"a probability group needs a strategy", altBom("", caseLine, `{"componentProductId":"CELL-A","quantity":"4","altGroup":"A1"}`, cellB60), http.StatusUnprocessableEntity, domain.CodeAltConflict},
		{"priorities must differ", altBom("", `{"componentProductId":"A","quantity":"1","altGroup":"P","altStrategy":"priority","altPriority":1}`, `{"componentProductId":"B","quantity":"1","altGroup":"P","altStrategy":"priority","altPriority":1}`), http.StatusUnprocessableEntity, domain.CodeAltConflict},
		{"discontinuation with follow-up", altBom("", `{"componentProductId":"CELL-A","quantity":"4","discontinueType":"single","discontinueOn":"2027-01-01","followUpComponentId":"CELL-C"}`), http.StatusCreated, `"discontinueOn":"2027-01-01"`},
		{"discontinuation needs a type", altBom("", `{"componentProductId":"CELL-A","quantity":"4","discontinuationGroup":"D1"}`), http.StatusUnprocessableEntity, domain.CodeBomLine},
		{"a bad discontinue date is invalid", altBom("", `{"componentProductId":"CELL-A","quantity":"4","discontinueType":"single","discontinueOn":"soon"}`), http.StatusBadRequest, "lines[0]"},
		{"usage and category are stored", altBom(`,"usage":"costing","category":"document"`, caseLine), http.StatusCreated, `"usage":"costing","category":"document"`},
		{"usage defaults to production", altBom("", caseLine), http.StatusCreated, `"usage":"production","category":"material"`},
		{"unknown usage", altBom(`,"usage":"fun"`, caseLine), http.StatusBadRequest, "usage"},
		{"equipment BOMs wait for wave 44", altBom(`,"category":"equipment"`, caseLine), http.StatusBadRequest, "category"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			r, _ := newRouter(&fakeStore{}, approves)
			rec := do(r, http.MethodPost, versions, tc.body, allDuties, true)
			if rec.Code != tc.status || !strings.Contains(rec.Body.String(), tc.want) {
				t.Fatalf("create = %d %s; want %d containing %q", rec.Code, rec.Body, tc.status, tc.want)
			}
		})
	}
}

func TestUpdateBomVersion_AlternativeConflictIsRefused(t *testing.T) {
	r, _ := newRouter(&fakeStore{listed: editFixtures()}, approves)
	body := `{"productId":"BIKE","effectiveFrom":"2026-03-01","version":3,"lines":[` + cellA + `,` + cellB30 + `]}`
	if rec := do(r, http.MethodPut, versions+"/"+rid(1), body, "engineering.bom.write", true); rec.Code != http.StatusUnprocessableEntity || !strings.Contains(rec.Body.String(), domain.CodeAltConflict) {
		t.Fatalf("update = %d %s; want 422 %s", rec.Code, rec.Body, domain.CodeAltConflict)
	}
}

func TestListBomVersions_UsageFilter(t *testing.T) {
	f := &fakeStore{}
	r, _ := newRouter(f, approves)
	if rec := do(r, http.MethodGet, versions+"?usage=costing&category=material", "", allDuties, true); rec.Code != http.StatusOK || f.lastFilter.Usage != "costing" || f.lastFilter.Category != "material" {
		t.Fatalf("list = %d, filter %+v; want usage and category passed on", rec.Code, f.lastFilter)
	}
	if rec := do(r, http.MethodGet, versions+"?usage=nope", "", allDuties, true); rec.Code != http.StatusBadRequest {
		t.Fatalf("bad usage = %d; want 400", rec.Code)
	}
}

func TestExplode_ReturnsAlternativeGroupsUnresolved(t *testing.T) {
	v := domain.BomVersion{ID: rid(1), ProductID: "PACK", EffectiveFrom: day("2026-01-01"), Status: domain.StatusActive}
	create, _ := newRouter(&fakeStore{}, approves)
	rec := do(create, http.MethodPost, versions, altBom("", caseLine, cellA, cellB60), allDuties, true)
	var created struct {
		Lines []struct {
			LineNo                int
			ComponentProductID    string
			AltGroup, AltStrategy string
		}
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &created); err != nil || len(created.Lines) != 3 {
		t.Fatalf("create = %s (%v)", rec.Body, err)
	}
	for _, l := range []struct {
		no   int
		comp string
		qty  string
	}{{1, "CASE", "1"}, {2, "CELL-A", "4"}, {3, "CELL-B", "4"}} {
		v.Lines = append(v.Lines, domain.BomLine{LineNo: l.no, ComponentProductID: l.comp, Quantity: dec(l.qty), Series: dec("1"), LineType: domain.LineItem})
	}
	p60, p40 := dec("60"), dec("40")
	v.Lines[1].Alt = domain.Alternative{Group: "A1", Strategy: domain.AltProbability, ProbabilityPct: &p60}
	v.Lines[2].Alt = domain.Alternative{Group: "A1", Strategy: domain.AltProbability, ProbabilityPct: &p40}
	r, _ := newRouter(&fakeStore{catalog: map[string][]domain.BomVersion{"PACK": {v}}}, approves)
	rec = do(r, http.MethodPost, "/v1/engineering/boms:explode", `{"productId":"PACK","asOf":"2026-03-01","quantity":"10","levels":2}`, allDuties, true)
	var got struct {
		Root struct {
			Children []struct {
				ProductID   string
				Alternative *struct {
					Group               string
					UsageProbabilityPct string
				}
			}
		}
		Flat         []struct{ ProductID string }
		Alternatives []struct {
			ProductID, ParentProductID string
			Quantity                   string
		}
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil || rec.Code != http.StatusOK {
		t.Fatalf("explode = %d %s (%v)", rec.Code, rec.Body, err)
	}
	c := got.Root.Children
	if len(c) != 3 || c[0].Alternative != nil || c[1].Alternative == nil || c[1].Alternative.Group != "A1" || c[2].Alternative.UsageProbabilityPct != "40" {
		t.Fatalf("children = %+v; want both group lines with metadata", c)
	}
	if len(got.Flat) != 1 || len(got.Alternatives) != 2 || got.Alternatives[0].ParentProductID != "PACK" || got.Alternatives[0].Quantity != "40" {
		t.Fatalf("flat %+v alternatives %+v; want CASE only in flat and two unresolved members", got.Flat, got.Alternatives)
	}
}

func TestUpdateBomVersion_OmittedUsageKeepsTheStoredOne(t *testing.T) {
	fixtures := editFixtures()
	fixtures[0].Usage, fixtures[0].Category = domain.UsageCosting, domain.CategoryDocument
	r, _ := newRouter(&fakeStore{listed: fixtures}, approves)
	if rec := do(r, http.MethodPut, versions+"/"+rid(1), editBody("3", ""), "engineering.bom.write", true); rec.Code != http.StatusOK ||
		!strings.Contains(rec.Body.String(), `"usage":"costing","category":"document"`) {
		t.Fatalf("update without usage = %d %s; want the stored costing document BOM kept", rec.Code, rec.Body)
	}
	if rec := do(r, http.MethodPut, versions+"/"+rid(1), editBody("4", `,"usage":"sales"`), "engineering.bom.write", true); rec.Code != http.StatusOK ||
		!strings.Contains(rec.Body.String(), `"usage":"sales","category":"document"`) {
		t.Fatalf("update to sales = %d %s; want usage changed and category kept", rec.Code, rec.Body)
	}
}

func TestCopyBomVersion_KeepsUsageAndGroups(t *testing.T) {
	p60, p40 := dec("60"), dec("40")
	src := domain.BomVersion{ID: rid(5), ProductID: "PACK", EffectiveFrom: day("2026-01-01"), Status: domain.StatusActive, Usage: domain.UsageCosting, Category: domain.CategoryMaterial}
	for i, c := range []string{"CASE", "CELL-A", "CELL-B"} {
		src.Lines = append(src.Lines, domain.BomLine{LineNo: i + 1, ComponentProductID: c, Quantity: dec("1"), Series: dec("1"), LineType: domain.LineItem})
	}
	src.Lines[1].Alt = domain.Alternative{Group: "A1", Strategy: domain.AltProbability, ProbabilityPct: &p60}
	src.Lines[2].Alt = domain.Alternative{Group: "A1", Strategy: domain.AltProbability, ProbabilityPct: &p40}
	r, _ := newRouter(&fakeStore{listed: []domain.BomVersion{src}}, approves)
	rec := do(r, http.MethodPost, versions+"/"+rid(5)+":copy", `{"effectiveFrom":"2027-01-01"}`, "engineering.bom.write", true)
	if rec.Code != http.StatusCreated || !strings.Contains(rec.Body.String(), `"usage":"costing"`) || strings.Count(rec.Body.String(), `"altGroup":"A1"`) != 2 {
		t.Fatalf("copy = %d %s; want a costing draft with both A1 members", rec.Code, rec.Body)
	}
	// Keeping one member of A1 leaves a group of one, and a probability group of 60.
	rec = do(r, http.MethodPost, versions+"/"+rid(5)+":copy", `{"effectiveFrom":"2027-01-01","lineNos":[1,2]}`, "engineering.bom.write", true)
	if rec.Code != http.StatusUnprocessableEntity || !strings.Contains(rec.Body.String(), domain.CodeAltConflict) {
		t.Fatalf("partial copy = %d %s; want 422 %s", rec.Code, rec.Body, domain.CodeAltConflict)
	}
}
