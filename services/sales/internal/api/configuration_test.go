package api

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"
	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/platform/tenant"
	"github.com/BenjaminSRussell/passage/services/sales/internal/domain"
	"github.com/BenjaminSRussell/passage/services/sales/internal/store"
	"github.com/BenjaminSRussell/passage/services/sales/internal/upstream"
)

type configStore struct {
	fakeStore
	created  []store.NewOrder
	recorded []store.LineProduction
}

func (c *configStore) CreateOrder(_ context.Context, in store.NewOrder) (store.Order, bool, error) {
	c.created = append(c.created, in)
	return store.Order{ID: ids.New(), Lines: []store.Line{}, Holds: []store.Hold{}}, true, nil
}

func (c *configStore) RecordLineProduction(_ context.Context, in store.LineProduction) error {
	c.recorded = append(c.recorded, in)
	return nil
}

type fakeChecker struct {
	err   error
	asked [][2]string
}

func (f *fakeChecker) Check(_ context.Context, _, configurationID, productID string) error {
	f.asked = append(f.asked, [2]string{configurationID, productID})
	return f.err
}

type fakePlanner struct {
	requests []upstream.ProductionRequest
	failAt   int
	err      error
}

func (f *fakePlanner) CreateForLine(_ context.Context, in upstream.ProductionRequest) (string, bool, error) {
	f.requests = append(f.requests, in)
	if f.err != nil && len(f.requests) == f.failAt {
		return "", false, f.err
	}
	return ids.New(), true, nil
}

func serveWith(a *API, method, path, body string, p tenant.Principal) *httptest.ResponseRecorder {
	r := chi.NewRouter()
	r.Use(func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
			next.ServeHTTP(w, req.WithContext(tenant.With(req.Context(), p)))
		})
	})
	a.Register(r)
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	req.Header.Set("Idempotency-Key", "k-1")
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)
	return rec
}

func configuredOrderBody(configuration, product string) string {
	line := map[string]any{"productId": product, "quantity": "2", "unit": "EA", "siteId": ids.New(), "warehouseId": ids.New(), "unitPrice": "100", "requestedShipDate": "2027-03-04"}
	if configuration != "" {
		line["configurationId"] = configuration
	}
	raw, _ := json.Marshal(map[string]any{"legalEntityId": testLE, "customerId": ids.New(), "invoiceAccountId": ids.New(), "currency": "USD", "requestedShipDate": "2027-03-04",
		"lines": []any{line, line}})
	return string(raw)
}

func problemCode(t *testing.T, rec *httptest.ResponseRecorder) string {
	t.Helper()
	var p httpx.Problem
	if err := json.Unmarshal(rec.Body.Bytes(), &p); err != nil {
		t.Fatalf("not a problem (%d): %s", rec.Code, rec.Body.String())
	}
	return p.Code
}

func TestCreateOrder_ConfigurationMustBeConfirmedForTheLinesProduct(t *testing.T) {
	product, configuration := ids.New(), ids.New()
	tests := []struct {
		name    string
		checker *fakeChecker
		status  int
		code    string
		stored  int
	}{
		{"valid", &fakeChecker{}, http.StatusCreated, "", 1},
		{"unknown, not valid or of another product", &fakeChecker{err: upstream.ErrConfigurationInvalid}, http.StatusUnprocessableEntity, codeConfigurationInvalid, 0},
		{"product service down", &fakeChecker{err: errors.New("connection refused")}, http.StatusServiceUnavailable, codeConfigurationUnavailable, 0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			st := &configStore{}
			rec := serveWith(New(st).WithConfigurations(tt.checker), http.MethodPost, orders, configuredOrderBody(configuration, product), principal(dutyWrite))
			if rec.Code != tt.status || len(st.created) != tt.stored {
				t.Fatalf("status %d stored %d: %s", rec.Code, len(st.created), rec.Body.String())
			}
			if tt.code != "" && problemCode(t, rec) != tt.code {
				t.Fatalf("code %s", rec.Body.String())
			}
			if len(tt.checker.asked) != 1 || tt.checker.asked[0] != [2]string{configuration, product} {
				t.Fatalf("one check per configuration and product, got %v", tt.checker.asked)
			}
			if tt.stored == 1 && (st.created[0].Lines[0].ConfigurationID == nil || *st.created[0].Lines[0].ConfigurationID != configuration) {
				t.Fatalf("configuration not passed to the store: %+v", st.created[0].Lines)
			}
		})
	}
}

func TestCreateOrder_ConfiguredLinesFailClosedWithoutAChecker(t *testing.T) {
	st := &configStore{}
	rec := serveWith(New(st), http.MethodPost, orders, configuredOrderBody(ids.New(), ids.New()), principal(dutyWrite))
	if rec.Code != http.StatusServiceUnavailable || problemCode(t, rec) != codeConfigurationUnavailable || len(st.created) != 0 {
		t.Fatalf("%d %s", rec.Code, rec.Body.String())
	}
	if rec = serveWith(New(st), http.MethodPost, orders, configuredOrderBody("", ids.New()), principal(dutyWrite)); rec.Code != http.StatusCreated {
		t.Fatalf("lines without a configuration need no checker: %d %s", rec.Code, rec.Body.String())
	}
	if rec = serveWith(New(st), http.MethodPost, orders, configuredOrderBody("not-a-uuid", ids.New()), principal(dutyWrite)); rec.Code != http.StatusBadRequest {
		t.Fatalf("malformed configurationId: %d", rec.Code)
	}
}

func confirmedOrder(linked string) store.Order {
	cfgA, cfgB := ids.New(), ids.New()
	date := "2027-03-05"
	lines := []store.Line{
		{ID: ids.New(), LineNo: 1, ProductID: ids.New(), Quantity: decimal.NewFromInt(2), SiteID: ids.New(), WarehouseID: ids.New(), RequestedShipDate: "2027-03-04", ConfirmedShipDate: &date, ConfigurationID: &cfgA, LineStatus: "OPEN"},
		{ID: ids.New(), LineNo: 2, ProductID: ids.New(), Quantity: decimal.NewFromInt(1), SiteID: ids.New(), WarehouseID: ids.New(), RequestedShipDate: "2027-03-04", LineStatus: "OPEN"},
		{ID: ids.New(), LineNo: 3, ProductID: ids.New(), Quantity: decimal.NewFromInt(4), SiteID: ids.New(), WarehouseID: ids.New(), RequestedShipDate: "2027-03-04", ConfigurationID: &cfgB, LineStatus: "OPEN"},
	}
	if linked != "" {
		lines[2].ProductionOrderID = &linked
	}
	return store.Order{ID: ids.New(), LegalEntityID: testLE, Status: domain.OrderConfirmed, Currency: "USD", Lines: lines}
}

func TestPlanProduction_CreatesOneProductionOrderPerUnlinkedConfiguredLine(t *testing.T) {
	linked := ids.New()
	st := &configStore{fakeStore: fakeStore{order: confirmedOrder(linked)}}
	planner := &fakePlanner{}
	rec := serveWith(New(st).WithProduction(planner), http.MethodPost, orders+"/"+st.order.ID+":plan-production", "", principal(dutyPlanProduction))
	if rec.Code != http.StatusOK {
		t.Fatalf("%d %s", rec.Code, rec.Body.String())
	}
	var out struct{ Lines []PlannedLine }
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil || len(out.Lines) != 2 {
		t.Fatalf("want the two configured lines, got %s", rec.Body.String())
	}
	line := st.order.Lines[0]
	if len(planner.requests) != 1 {
		t.Fatalf("only the unlinked configured line is planned, got %d requests", len(planner.requests))
	}
	got := planner.requests[0]
	if got.OriginID != line.ID || got.ConfigurationID != *line.ConfigurationID || got.ProductID != line.ProductID || got.Quantity != "2" ||
		got.DeliveryDate != "2027-03-05" || got.LegalEntityID != testLE || got.Currency != "USD" {
		t.Fatalf("production request %+v", got)
	}
	if !out.Lines[0].Created || out.Lines[1].Created || out.Lines[1].ProductionOrderID != linked {
		t.Fatalf("outcome %+v", out.Lines)
	}
	if len(st.recorded) != 1 || st.recorded[0].LineID != line.ID || st.recorded[0].ProductionOrderID != out.Lines[0].ProductionOrderID || st.recorded[0].Actor != "u1" {
		t.Fatalf("link not recorded: %+v", st.recorded)
	}
}

func TestPlanProduction_Refusals(t *testing.T) {
	order := func(status string) *configStore {
		o := confirmedOrder("")
		o.Status = status
		return &configStore{fakeStore: fakeStore{order: o}}
	}
	t.Run("order not confirmed", func(t *testing.T) {
		st, planner := order(domain.OrderOpen), &fakePlanner{}
		rec := serveWith(New(st).WithProduction(planner), http.MethodPost, orders+"/"+st.order.ID+":plan-production", "", principal(dutyPlanProduction))
		if rec.Code != http.StatusUnprocessableEntity || problemCode(t, rec) != codeNotPlannable || len(planner.requests) != 0 {
			t.Fatalf("%d %s", rec.Code, rec.Body.String())
		}
	})
	t.Run("missing duty", func(t *testing.T) {
		st, planner := order(domain.OrderConfirmed), &fakePlanner{}
		rec := serveWith(New(st).WithProduction(planner), http.MethodPost, orders+"/"+st.order.ID+":plan-production", "", principal(dutyWrite, dutyConfirm))
		if rec.Code != http.StatusForbidden || len(planner.requests) != 0 {
			t.Fatalf("%d", rec.Code)
		}
	})
	t.Run("production not configured", func(t *testing.T) {
		st := order(domain.OrderConfirmed)
		rec := serveWith(New(st), http.MethodPost, orders+"/"+st.order.ID+":plan-production", "", principal(dutyPlanProduction))
		if rec.Code != http.StatusServiceUnavailable || problemCode(t, rec) != codeProductionUnavailable {
			t.Fatalf("%d %s", rec.Code, rec.Body.String())
		}
	})
	t.Run("production fails on the second line: the first stays recorded", func(t *testing.T) {
		st, planner := order(domain.OrderConfirmed), &fakePlanner{err: errors.New("boom"), failAt: 2}
		rec := serveWith(New(st).WithProduction(planner), http.MethodPost, orders+"/"+st.order.ID+":plan-production", "", principal(dutyPlanProduction))
		if rec.Code != http.StatusBadGateway || problemCode(t, rec) != codeProductionUnavailable || len(st.recorded) != 1 {
			t.Fatalf("%d recorded=%d %s", rec.Code, len(st.recorded), rec.Body.String())
		}
	})
	t.Run("production already built the line for another configuration", func(t *testing.T) {
		st, planner := order(domain.OrderConfirmed), &fakePlanner{err: upstream.ErrOriginConflict, failAt: 1}
		rec := serveWith(New(st).WithProduction(planner), http.MethodPost, orders+"/"+st.order.ID+":plan-production", "", principal(dutyPlanProduction))
		if rec.Code != http.StatusConflict || problemCode(t, rec) != codeProductionConflict || len(st.recorded) != 0 {
			t.Fatalf("%d %s", rec.Code, rec.Body.String())
		}
	})
}
