package api_test

import (
	"context"
	"net/http"
	"strings"
	"testing"

	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/services/engineering/internal/domain"
	"github.com/BenjaminSRussell/passage/services/engineering/internal/store"
)

func (f *fakeStore) ActivateBomVersion(_ context.Context, id string, check func(store.Snapshot, domain.BomVersion) error) (domain.BomVersion, error) {
	if f.err != nil {
		return domain.BomVersion{}, f.err
	}
	for i, v := range f.listed {
		if v.ID != id {
			continue
		}
		if v.Status == domain.StatusActive {
			return v, nil
		}
		if err := v.EnsureDraft(); err != nil {
			return v, err
		}
		if err := check(f.catalog, v); err != nil {
			return domain.BomVersion{}, err
		}
		f.listed[i].Status = domain.StatusActive
		return f.listed[i], nil
	}
	return domain.BomVersion{}, httpx.NewProblem(http.StatusNotFound, store.CodeBomNotFound, "BOM version not found")
}

func (f *fakeStore) CancelBomVersion(_ context.Context, id string) (domain.BomVersion, error) {
	if f.err != nil {
		return domain.BomVersion{}, f.err
	}
	for i, v := range f.listed {
		if v.ID != id {
			continue
		}
		if v.Status == domain.StatusCancelled {
			return v, nil
		}
		if err := v.EnsureDraft(); err != nil {
			return v, err
		}
		f.listed[i].Status = domain.StatusCancelled
		return f.listed[i], nil
	}
	return domain.BomVersion{}, httpx.NewProblem(http.StatusNotFound, store.CodeBomNotFound, "BOM version not found")
}

func lifecycleFixtures() []domain.BomVersion {
	line := domain.BomLine{LineNo: 1, ComponentProductID: "WHEEL", Quantity: dec("2"), Series: dec("1"), LineType: domain.LineItem}
	return []domain.BomVersion{
		{ID: rid(1), ProductID: "BIKE", EffectiveFrom: day("2026-01-01"), Status: domain.StatusDraft, Lines: []domain.BomLine{line}},
		{ID: rid(2), ProductID: "BIKE", EffectiveFrom: day("2025-01-01"), Status: domain.StatusActive, Lines: []domain.BomLine{line}},
		{ID: rid(3), ProductID: "TRIKE", EffectiveFrom: day("2026-01-01"), Status: domain.StatusDraft, Lines: []domain.BomLine{line}},
	}
}

func TestActivateBomVersion(t *testing.T) {
	tests := []struct {
		name    string
		id      string
		catalog store.Snapshot
		duties  string
		status  int
		want    string
	}{
		{"first version of a product activates", rid(3), nil, "engineering.bom.write", http.StatusOK, `"status":"active"`},
		{"already active is unchanged", rid(2), nil, "engineering.bom.write", http.StatusOK, `"status":"active"`},
		{"a product that has a version needs an ECO", rid(1), store.Snapshot{"BIKE": {{ID: rid(2), ProductID: "BIKE", EffectiveFrom: day("2025-01-01"), EffectiveTo: day("2025-12-31"), Status: domain.StatusExpired}}}, "engineering.bom.write", http.StatusUnprocessableEntity, domain.CodeBomEcoRequired},
		{"an overlapping active version is refused", rid(1), store.Snapshot{"BIKE": {{ID: rid(2), ProductID: "BIKE", EffectiveFrom: day("2025-01-01"), Status: domain.StatusActive}}}, "engineering.bom.write", http.StatusUnprocessableEntity, domain.CodeBomOverlap},
		{"a cycle is refused", rid(3), store.Snapshot{"WHEEL": {{ID: rid(9), ProductID: "WHEEL", EffectiveFrom: day("2025-01-01"), Status: domain.StatusActive, Lines: []domain.BomLine{{LineNo: 1, ComponentProductID: "TRIKE", Quantity: dec("1"), Series: dec("1"), LineType: domain.LineItem}}}}}, "engineering.bom.write", http.StatusUnprocessableEntity, domain.CodeBomCycle},
		{"unknown id", rid(99), nil, "engineering.bom.write", http.StatusNotFound, store.CodeBomNotFound},
		{"read duty alone is refused", rid(3), nil, "engineering.read", http.StatusForbidden, "AUTH-0003"},
		{"not a uuid", "nope", nil, "engineering.bom.write", http.StatusBadRequest, "COM-0001"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			r, _ := newRouter(&fakeStore{listed: lifecycleFixtures(), catalog: tc.catalog}, approves)
			rec := do(r, http.MethodPost, versions+"/"+tc.id+":activate", "", tc.duties, true)
			if rec.Code != tc.status || !strings.Contains(rec.Body.String(), tc.want) {
				t.Fatalf("activate = %d %s; want %d containing %s", rec.Code, rec.Body, tc.status, tc.want)
			}
		})
	}
}

func TestActivateBomVersion_ExpiredIsConflict(t *testing.T) {
	f := &fakeStore{listed: lifecycleFixtures()}
	f.listed[0].Status = domain.StatusExpired
	r, _ := newRouter(f, approves)
	rec := do(r, http.MethodPost, versions+"/"+rid(1)+":activate", "", "engineering.bom.write", true)
	if rec.Code != http.StatusConflict || !strings.Contains(rec.Body.String(), domain.CodeBomState) {
		t.Fatalf("activate expired = %d %s; want 409 %s", rec.Code, rec.Body, domain.CodeBomState)
	}
}

func TestCancelBomVersion(t *testing.T) {
	tests := []struct {
		name   string
		id     string
		duties string
		status int
		want   string
	}{
		{"draft is cancelled", rid(1), "engineering.bom.write", http.StatusOK, `"status":"cancelled"`},
		{"active is refused", rid(2), "engineering.bom.write", http.StatusConflict, domain.CodeBomState},
		{"in an open ECO is refused", rid(1), "engineering.bom.write", http.StatusConflict, domain.CodeBomInEco},
		{"unknown id", rid(99), "engineering.bom.write", http.StatusNotFound, store.CodeBomNotFound},
		{"read duty alone is refused", rid(1), "engineering.read", http.StatusForbidden, "AUTH-0003"},
		{"not a uuid", "nope", "engineering.bom.write", http.StatusBadRequest, "COM-0001"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			f := &fakeStore{listed: lifecycleFixtures()}
			if tc.want == domain.CodeBomInEco {
				f.err = &domain.Error{Code: domain.CodeBomInEco, Detail: "an open ECO releases this draft"}
			}
			r, _ := newRouter(f, approves)
			rec := do(r, http.MethodPost, versions+"/"+tc.id+":cancel", "", tc.duties, true)
			if rec.Code != tc.status || !strings.Contains(rec.Body.String(), tc.want) {
				t.Fatalf("cancel = %d %s; want %d containing %q", rec.Code, rec.Body, tc.status, tc.want)
			}
		})
	}
}

func TestListBomVersions_AcceptsTheCancelledStatus(t *testing.T) {
	r, _ := newRouter(&fakeStore{listed: lifecycleFixtures()}, approves)
	if rec := do(r, http.MethodGet, versions+"?status=cancelled", "", "engineering.read", true); rec.Code != http.StatusOK {
		t.Fatalf("list cancelled = %d %s; want 200", rec.Code, rec.Body)
	}
	if rec := do(r, http.MethodGet, versions+"?status=deleted", "", "engineering.read", true); rec.Code != http.StatusBadRequest && rec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("list unknown status = %d; want a validation error", rec.Code)
	}
}

func TestBomLifecycle_UnexpectedFailureIsHidden(t *testing.T) {
	r, _ := newRouter(&fakeStore{listed: lifecycleFixtures(), err: context.DeadlineExceeded}, approves)
	for _, rec := range []string{
		do(r, http.MethodPost, versions+"/"+rid(1)+":activate", "", "engineering.bom.write", true).Body.String(),
		do(r, http.MethodPost, versions+"/"+rid(1)+":cancel", "", "engineering.bom.write", true).Body.String(),
	} {
		if strings.Contains(rec, "deadline") {
			t.Fatalf("internal error leaked: %s", rec)
		}
	}
}
