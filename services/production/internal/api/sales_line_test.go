package api

import (
	"net/http"
	"testing"

	"github.com/BenjaminSRussell/passage/platform/ids"
)

func salesLineBody(line, configuration any) string {
	return createBody(func(m map[string]any) {
		delete(m, "operations")
		delete(m, "bom")
		m["originId"] = line
		if configuration != nil {
			m["configurationId"] = configuration
		}
	})
}

func salesLineCall(body string, duties ...string) call {
	return call{method: http.MethodPost, path: "/v1/production/sales-line-orders", body: body, duties: duties,
		headers: map[string]string{idempotencyName: "sales-line-1"}}
}

func TestSalesLineOrder_CarriesTheConfigurationWithoutARoute(t *testing.T) {
	f := &fakeStore{}
	_, h := newServer(t, f)
	line, configuration := ids.New(), ids.New()
	rec := do(h, salesLineCall(salesLineBody(line, configuration), DutyCreateFromSales))
	if rec.Code != http.StatusCreated {
		t.Fatalf("want 201, got %d: %s", rec.Code, rec.Body.String())
	}
	in := f.created[0]
	if in.OriginType != "SalesOrderLine" || in.OriginID == nil || *in.OriginID != line || in.ConfigurationID == nil || *in.ConfigurationID != configuration {
		t.Fatalf("create input: %+v", in)
	}
}

func TestSalesLineOrder_Refusals(t *testing.T) {
	line, configuration := ids.New(), ids.New()
	tests := []struct {
		name   string
		call   call
		status int
		code   string
	}{
		{"missing duty", salesLineCall(salesLineBody(line, configuration), DutyWrite), http.StatusForbidden, "AUTH-0003"},
		{"missing configuration", salesLineCall(salesLineBody(line, nil), DutyCreateFromSales), http.StatusBadRequest, validationCode},
		{"malformed configuration", salesLineCall(salesLineBody(line, "x"), DutyCreateFromSales), http.StatusBadRequest, validationCode},
		{"missing line", salesLineCall(salesLineBody(nil, configuration), DutyCreateFromSales), http.StatusBadRequest, validationCode},
		{"configuration on the general route", call{method: http.MethodPost, path: "/v1/production/orders", duties: []string{DutyWrite},
			body: createBody(func(m map[string]any) { m["configurationId"] = configuration }), headers: map[string]string{idempotencyName: "manual-1"}},
			http.StatusForbidden, "AUTH-0003"},
		{"sales line origin on the general route", call{method: http.MethodPost, path: "/v1/production/orders", duties: []string{DutyWrite},
			body: createBody(func(m map[string]any) { m["originType"] = "SalesOrderLine"; m["originId"] = line }), headers: map[string]string{idempotencyName: "manual-2"}},
			http.StatusForbidden, "AUTH-0003"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := &fakeStore{}
			_, h := newServer(t, f)
			expectProblem(t, do(h, tt.call), tt.status, tt.code)
			if len(f.created) != 0 {
				t.Fatal("a refused request reached the store")
			}
		})
	}
}
