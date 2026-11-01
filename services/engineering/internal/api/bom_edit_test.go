package api_test

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"testing"

	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/services/engineering/internal/domain"
	"github.com/BenjaminSRussell/passage/services/engineering/internal/store"
)

// UpdateBomDraft mimics the store: draft only, version must match, the product is kept.
func (f *fakeStore) UpdateBomDraft(_ context.Context, v domain.BomVersion, expected int, _ store.Idem, check func(store.Snapshot) error) (domain.BomVersion, error) {
	if f.err != nil {
		return domain.BomVersion{}, f.err
	}
	for i, stored := range f.listed {
		if stored.ID != v.ID {
			continue
		}
		if err := stored.EnsureDraft(); err != nil {
			return stored, err
		}
		if stored.Version != expected {
			return stored, &domain.Error{Code: domain.CodeBomVersionConflict, Detail: fmt.Sprintf("the draft is at version %d", stored.Version)}
		}
		if err := check(f.catalog); err != nil {
			return domain.BomVersion{}, err
		}
		v.Status, v.Version = stored.Status, stored.Version+1
		f.listed[i] = v
		return v, nil
	}
	return domain.BomVersion{}, httpx.NewProblem(http.StatusNotFound, store.CodeBomNotFound, "BOM version not found")
}

const editLine = `{"lineNo":1,"componentProductId":"HUB","quantity":"3","scrapConst":"0","scrapVarPct":"2"}`

func editBody(version string, extra string) string {
	return `{"productId":"BIKE","effectiveFrom":"2026-03-01"` + extra + `,"version":` + version + `,"lines":[` + editLine + `]}`
}

func editFixtures() []domain.BomVersion {
	fixtures := lifecycleFixtures()
	for i := range fixtures {
		fixtures[i].Version = 3
	}
	return fixtures
}

func TestUpdateBomVersion(t *testing.T) {
	tests := []struct {
		name    string
		id      string
		body    string
		catalog store.Snapshot
		duties  string
		status  int
		want    string
	}{
		{"saves a draft and bumps the version", rid(1), editBody("3", ""), nil, "engineering.bom.write", http.StatusOK, `"version":4`},
		{"replaces the lines", rid(1), editBody("3", ""), nil, "engineering.bom.write", http.StatusOK, `"componentProductId":"HUB"`},
		{"stale version is a conflict", rid(1), editBody("2", ""), nil, "engineering.bom.write", http.StatusConflict, domain.CodeBomVersionConflict},
		{"version is required", rid(1), strings.Replace(editBody("3", ""), `"version":3,`, "", 1), nil, "engineering.bom.write", http.StatusBadRequest, "version"},
		{"an active version cannot be edited", rid(2), editBody("3", ""), nil, "engineering.bom.write", http.StatusConflict, domain.CodeBomState},
		{"the status cannot be edited", rid(1), editBody("3", `,"status":"active"`), nil, "engineering.bom.write", http.StatusBadRequest, "status"},
		{"the product cannot change", rid(1), strings.Replace(editBody("3", ""), "BIKE", "TRIKE", 1), nil, "engineering.bom.write", http.StatusBadRequest, "productId"},
		{"no lines is invalid", rid(1), `{"productId":"BIKE","effectiveFrom":"2026-03-01","version":3,"lines":[]}`, nil, "engineering.bom.write", http.StatusBadRequest, "lines"},
		{"a bad date is invalid", rid(1), editBody("3", `,"effectiveTo":"2026-01-01"`), nil, "engineering.bom.write", http.StatusBadRequest, "effectiveTo"},
		{"a cycle is refused", rid(1), editBody("3", ""), store.Snapshot{"HUB": {{ID: rid(9), ProductID: "HUB", EffectiveFrom: day("2025-01-01"), Status: domain.StatusActive, Lines: []domain.BomLine{{LineNo: 1, ComponentProductID: "BIKE", Quantity: dec("1"), Series: dec("1"), LineType: domain.LineItem}}}}}, "engineering.bom.write", http.StatusUnprocessableEntity, domain.CodeBomCycle},
		{"unknown id", rid(99), editBody("3", ""), nil, "engineering.bom.write", http.StatusNotFound, store.CodeBomNotFound},
		{"not a uuid", "nope", editBody("3", ""), nil, "engineering.bom.write", http.StatusBadRequest, "COM-0001"},
		{"read duty alone is refused", rid(1), editBody("3", ""), nil, "engineering.read", http.StatusForbidden, "AUTH-0003"},
		{"another id in the body is refused", rid(1), editBody("3", `,"id":"`+rid(2)+`"`), nil, "engineering.bom.write", http.StatusBadRequest, `"id"`},
		{"the path id in the body is accepted", rid(1), editBody("3", `,"id":"`+rid(1)+`"`), nil, "engineering.bom.write", http.StatusOK, `"version":4`},
		{"an unknown field is refused", rid(1), editBody("3", `,"bogus":1`), nil, "engineering.bom.write", http.StatusBadRequest, "COM-0001"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			r, _ := newRouter(&fakeStore{listed: editFixtures(), catalog: tc.catalog}, approves)
			rec := do(r, http.MethodPut, versions+"/"+tc.id, tc.body, tc.duties, true)
			if rec.Code != tc.status || !strings.Contains(rec.Body.String(), tc.want) {
				t.Fatalf("update = %d %s; want %d containing %q", rec.Code, rec.Body, tc.status, tc.want)
			}
		})
	}
}

func TestUpdateBomVersion_ReadBackShowsTheEditAndVersion(t *testing.T) {
	r, _ := newRouter(&fakeStore{listed: editFixtures()}, approves)
	if rec := do(r, http.MethodPut, versions+"/"+rid(1), editBody("3", `,"siteId":"S1"`), "engineering.bom.write", true); rec.Code != http.StatusOK {
		t.Fatalf("update = %d %s", rec.Code, rec.Body)
	}
	rec := do(r, http.MethodGet, versions+"/"+rid(1), "", "engineering.read", true)
	var got struct {
		SiteID  string `json:"siteId"`
		Version int    `json:"version"`
		Lines   []struct{ ComponentProductID string }
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil || got.SiteID != "S1" || got.Version != 4 || len(got.Lines) != 1 {
		t.Fatalf("read back = %s (%v); want site S1, version 4, one line", rec.Body, err)
	}
	if rec := do(r, http.MethodPut, versions+"/"+rid(1), editBody("3", ""), "engineering.bom.write", true); rec.Code != http.StatusConflict {
		t.Fatalf("second save of version 3 = %d; want 409", rec.Code)
	}
}

func TestUpdateBomVersion_UnexpectedFailureIsHidden(t *testing.T) {
	r, _ := newRouter(&fakeStore{listed: editFixtures(), err: context.DeadlineExceeded}, approves)
	if rec := do(r, http.MethodPut, versions+"/"+rid(1), editBody("3", ""), "engineering.bom.write", true); rec.Code != http.StatusInternalServerError || strings.Contains(rec.Body.String(), "deadline") {
		t.Fatalf("update = %d %s; want a hidden 500", rec.Code, rec.Body)
	}
}
