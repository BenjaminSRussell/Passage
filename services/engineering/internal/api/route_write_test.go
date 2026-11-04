package api_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/services/engineering/internal/domain"
	"github.com/BenjaminSRussell/passage/services/engineering/internal/store"
)

func (f *fakeStore) CreateRouteVersion(_ context.Context, v domain.RouteVersion, _ store.Idem) (domain.RouteVersion, error) {
	if f.err != nil {
		return domain.RouteVersion{}, f.err
	}
	if v.VersionNo == 0 {
		v.VersionNo = len(f.routes) + 1
	}
	if err := domain.ValidateRouteVersion(v); err != nil {
		return domain.RouteVersion{}, err
	}
	f.routes = append(f.routes, v)
	return v, nil
}

const routeOps = `[{"no":10,"nextNo":20,"setupH":"0.5","runH":"0.25","processQty":"1","requirements":[{"kind":"capability","refId":"weld","level":1}]},{"no":20,"runH":"1"}]`

func createRoute(r http.Handler, body, duties string) *httptest.ResponseRecorder {
	return do(r, http.MethodPost, routesPath, body, duties, true)
}

func TestCreateRouteVersion(t *testing.T) {
	head := `"productId":"BIKE","siteId":"S1","routeCode":"R1","effectiveFrom":"2026-07-01"`
	tests := []struct {
		name, body, duties string
		err                error
		status             int
		want               string
	}{
		{"valid draft", `{` + head + `,"operations":` + routeOps + `}`, routeWriter, nil, http.StatusCreated, `"status":"draft"`},
		{"read duty alone is refused", `{` + head + `,"operations":` + routeOps + `}`, "engineering.read", nil, http.StatusForbidden, "AUTH-0003"},
		{"migration loader imports a draft", `{` + head + `,"operations":` + routeOps + `}`, "engineering.import.write", nil, http.StatusCreated, `"status":"draft"`},
		{"import read duty alone is refused", `{` + head + `,"operations":` + routeOps + `}`, "engineering.import.read", nil, http.StatusForbidden, "AUTH-0003"},
		{"missing operations", `{` + head + `}`, routeWriter, nil, http.StatusBadRequest, "operations"},
		{"bad date", `{"productId":"BIKE","siteId":"S1","routeCode":"R1","effectiveFrom":"07/01","operations":` + routeOps + `}`, routeWriter, nil, http.StatusBadRequest, "effectiveFrom"},
		{"end before start", `{` + head + `,"effectiveTo":"2026-01-01","operations":` + routeOps + `}`, routeWriter, nil, http.StatusBadRequest, "effectiveTo"},
		{"unknown field", `{` + head + `,"bogus":1,"operations":` + routeOps + `}`, routeWriter, nil, http.StatusBadRequest, "COM-"},
		{"next points nowhere", `{` + head + `,"operations":[{"no":10,"nextNo":99}]}`, routeWriter, nil, http.StatusUnprocessableEntity, domain.CodeRouteNext},
		{"duplicate operation", `{` + head + `,"operations":[{"no":10},{"no":10}]}`, routeWriter, nil, http.StatusUnprocessableEntity, domain.CodeRouteOpDup},
		{"cycle", `{` + head + `,"operations":[{"no":10,"nextNo":20},{"no":20,"nextNo":10}]}`, routeWriter, nil, http.StatusUnprocessableEntity, domain.CodeRouteCycle},
		{"bad requirement kind", `{` + head + `,"operations":[{"no":10,"requirements":[{"kind":"wizard","refId":"x"}]}]}`, routeWriter, nil, http.StatusUnprocessableEntity, domain.CodeRouteReqKind},
		{"secondary without a primary at its join", `{` + head + `,"operations":[{"no":10,"nextNo":40},{"no":20,"nextNo":30,"priority":"secondary1"},{"no":30,"nextNo":40},{"no":40}]}`, routeWriter, nil, http.StatusUnprocessableEntity, domain.CodeRouteNoPrimary},
		{"negative setup time", `{` + head + `,"operations":[{"no":10,"setupH":"-1"}]}`, routeWriter, nil, http.StatusUnprocessableEntity, domain.CodeRouteQty},
		{"hours past numeric(14,4)", `{` + head + `,"operations":[{"no":10,"runH":"10000000000"}]}`, routeWriter, nil, http.StatusBadRequest, "operations[0]"},
		{"process qty with seven decimals", `{` + head + `,"operations":[{"no":10,"processQty":"0.0000001"}]}`, routeWriter, nil, http.StatusBadRequest, "operations[0]"},
		{"efficiency past numeric(9,6)", `{` + head + `,"operations":[{"no":10,"efficiencyPct":"1000"}]}`, routeWriter, nil, http.StatusBadRequest, "operations[0]"},
		{"operation number past int4", `{` + head + `,"operations":[{"no":2147483648}]}`, routeWriter, nil, http.StatusBadRequest, "operations[0]"},
		{"version number past int4", `{` + head + `,"versionNo":2147483648,"operations":[{"no":10}]}`, routeWriter, nil, http.StatusBadRequest, "versionNo"},
		{"requirement reference too long", `{` + head + `,"operations":[{"no":10,"requirements":[{"kind":"capability","refId":"` + strings.Repeat("x", 65) + `"}]}]}`, routeWriter, nil, http.StatusBadRequest, "operations[0]"},
		{"version number already taken", `{` + head + `,"versionNo":1,"operations":[{"no":10}]}`, routeWriter, httpx.NewProblem(http.StatusConflict, store.CodeDuplicate, "taken"), http.StatusConflict, store.CodeDuplicate},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			r, _ := newRouter(&fakeStore{err: tc.err}, approves)
			rec := createRoute(r, tc.body, tc.duties)
			if rec.Code != tc.status || !strings.Contains(rec.Body.String(), tc.want) {
				t.Fatalf("create = %d %s; want %d containing %s", rec.Code, rec.Body, tc.status, tc.want)
			}
		})
	}
}

func TestCreateRouteVersion_DefaultsAreApplied(t *testing.T) {
	f := &fakeStore{}
	r, _ := newRouter(f, approves)
	rec := createRoute(r, `{"productId":"BIKE","siteId":"S1","routeCode":"R1","effectiveFrom":"2026-07-01","operations":[{"no":10}]}`, routeWriter)
	if rec.Code != http.StatusCreated || len(f.routes) != 1 {
		t.Fatalf("create = %d %s", rec.Code, rec.Body)
	}
	op := f.routes[0].Operations[0]
	if !op.ProcessQty.Equal(dec("1")) || f.routes[0].Status != domain.StatusDraft {
		t.Fatalf("operation %+v status %s", op, f.routes[0].Status)
	}
}
