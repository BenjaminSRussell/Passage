package api

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"

	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/services/planning/internal/store"
)

func (f *fakeStore) DiffVersions(_ context.Context, code string, from, to, limit int) (store.VersionDiff, error) {
	if code != "MAIN" || from > 2 || to > 2 {
		return store.VersionDiff{}, httpx.NewProblem(http.StatusNotFound, store.CodeVersionNotFound, "plan version not found")
	}
	return store.VersionDiff{From: store.Version{Number: from}, To: store.Version{Number: to}, Identical: from == to, AddedCount: limit,
		Added: []store.DiffOrder{{Number: "P-9", Qty: "5"}}, Removed: []store.DiffOrder{}, Changed: []store.DiffChange{}, QtyDelta: map[string]string{"purchase": "5"}}, nil
}

func TestDiffVersions_OK(t *testing.T) {
	rec := do(t, &fakeStore{}, http.MethodGet, "/v1/planning/plans/MAIN/diff?from=1&to=2", "", "planning.read", true, "")
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d: %s", rec.Code, rec.Body)
	}
	var got store.VersionDiff
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil || got.From.Number != 1 || got.To.Number != 2 || len(got.Added) != 1 || got.QtyDelta["purchase"] != "5" {
		t.Fatalf("body %s (%v)", rec.Body, err)
	}
	if got.AddedCount != store.MaxPageSize {
		t.Fatalf("default limit = %d, want %d", got.AddedCount, store.MaxPageSize)
	}
	rec = do(t, &fakeStore{}, http.MethodGet, "/v1/planning/plans/MAIN/diff?from=1&to=2&limit=20", "", "planning.read", true, "")
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil || got.AddedCount != 20 {
		t.Fatalf("limit not passed: %s (%v)", rec.Body, err)
	}
}

func TestDiffVersions_Validation(t *testing.T) {
	for _, tc := range []struct{ path, field string }{
		{"/v1/planning/plans/MAIN/diff?to=2", "from"},
		{"/v1/planning/plans/MAIN/diff?from=x&to=2", "from"},
		{"/v1/planning/plans/MAIN/diff?from=1&to=0", "to"},
		{"/v1/planning/plans/main/diff?from=1&to=2", "planCode"},
		{"/v1/planning/plans/MAIN/diff?from=1&to=2&limit=0", "limit"},
		{"/v1/planning/plans/MAIN/diff?from=1&to=2&limit=501", "limit"},
	} {
		assertFieldError(t, do(t, &fakeStore{}, http.MethodGet, tc.path, "", "planning.read", true, ""), tc.field)
	}
}

func TestDiffVersions_NotFoundAndAuth(t *testing.T) {
	if rec := do(t, &fakeStore{}, http.MethodGet, "/v1/planning/plans/MAIN/diff?from=1&to=9", "", "planning.read", true, ""); rec.Code != http.StatusNotFound {
		t.Fatalf("status %d", rec.Code)
	}
	if rec := do(t, &fakeStore{}, http.MethodGet, "/v1/planning/plans/MAIN/diff?from=1&to=2", "", "", true, ""); rec.Code != http.StatusForbidden {
		t.Fatalf("status %d", rec.Code)
	}
	if rec := do(t, &fakeStore{}, http.MethodGet, "/v1/planning/plans/MAIN/diff?from=1&to=2", "", "planning.read", false, ""); rec.Code != http.StatusUnauthorized {
		t.Fatalf("status %d", rec.Code)
	}
}
