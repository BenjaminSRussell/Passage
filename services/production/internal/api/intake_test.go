package api

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/platform/eventing"
	"github.com/BenjaminSRussell/passage/platform/events"
	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/platform/tenant"
	"github.com/BenjaminSRussell/passage/services/production/internal/store"
)

type intakeFake struct {
	rows       map[string]store.PlannedIntake
	listFilter []store.IntakeFilter
	dismissed  []store.IntakeDismiss
	recorded   []store.PlannedIntakeIn
}

func (f *fakeStore) GetPlannedIntake(_ context.Context, id string) (store.PlannedIntake, error) {
	if row, ok := f.intake.rows[id]; ok {
		return row, nil
	}
	return store.PlannedIntake{}, httpx.NewProblem(http.StatusNotFound, store.CodeIntakeNotFound, "not found")
}

func (f *fakeStore) ListPlannedIntake(_ context.Context, lf store.IntakeFilter) (store.IntakePage, error) {
	f.intake.listFilter = append(f.intake.listFilter, lf)
	page := store.IntakePage{Items: []store.PlannedIntake{}}
	for _, row := range f.intake.rows {
		page.Items = append(page.Items, row)
	}
	return page, nil
}

func (f *fakeStore) DismissPlannedIntake(_ context.Context, in store.IntakeDismiss) (store.PlannedIntake, error) {
	f.intake.dismissed = append(f.intake.dismissed, in)
	row := f.intake.rows[in.ID]
	row.Status, row.Version = store.IntakeDismissed, row.Version+1
	return row, nil
}

func (f *fakeStore) RecordPlannedOrder(_ context.Context, _ pgx.Tx, in store.PlannedIntakeIn) (bool, error) {
	f.intake.recorded = append(f.intake.recorded, in)
	return true, nil
}

func openRow(warehouse *string) store.PlannedIntake {
	return store.PlannedIntake{
		ID: ids.New(), PlannedOrderID: ids.New(), TargetID: ids.New(), ProductID: ids.New(), SiteID: ids.New(), WarehouseID: warehouse,
		Quantity: decimal.NewFromInt(40), OrderDate: "2026-11-02", DeliveryDate: "2026-11-20", Status: store.IntakeOpen, Version: 1,
	}
}

func intakeServer(t *testing.T, rows ...store.PlannedIntake) (*fakeStore, http.Handler) {
	f := &fakeStore{}
	f.intake.rows = map[string]store.PlannedIntake{}
	for _, r := range rows {
		f.intake.rows[r.ID] = r
	}
	_, h := newServer(t, f)
	return f, h
}

// A conversion takes product, site, quantity, delivery date and origin from the firmed planned order,
// the order id from its target id and the warehouse from the row (FR-22.10, FR-22.18).
func TestConvertIntake_TakesTheFirmedOrderOverTheBody(t *testing.T) {
	warehouse := ids.New()
	row := openRow(&warehouse)
	f, h := intakeServer(t, row)
	body := createBody(func(m map[string]any) {
		m["quantity"] = "5"
		m["warehouseId"] = ids.New()
		m["deliveryDate"] = "2027-01-01"
	})
	rec := do(h, call{method: http.MethodPost, path: "/v1/production/planned-intake/" + row.ID + ":convert", body: body, duties: []string{DutyWrite},
		headers: map[string]string{idempotencyName: "conv-1"}})
	if rec.Code != http.StatusCreated {
		t.Fatalf("want 201, got %d: %s", rec.Code, rec.Body.String())
	}
	if len(f.created) != 1 {
		t.Fatalf("store called %d times", len(f.created))
	}
	in := f.created[0]
	if in.ID != row.TargetID || in.PlannedIntakeID != row.ID || in.OriginType != "PlannedOrder" || in.OriginID == nil || *in.OriginID != row.PlannedOrderID {
		t.Fatalf("origin: %+v", in)
	}
	if in.ProductID != row.ProductID || in.SiteID != row.SiteID || in.WarehouseID != warehouse || !in.Quantity.Equal(decimal.NewFromInt(40)) ||
		in.DeliveryDate == nil || *in.DeliveryDate != "2026-11-20" {
		t.Fatalf("header not taken from the row: %+v", in)
	}
	if !in.Lines[0].QtyRequired.Equal(decimal.NewFromInt(126)) {
		t.Fatalf("lines exploded for the planned quantity of 40, got %s", in.Lines[0].QtyRequired)
	}
}

// The order is created in the planned order's legal entity: a body naming another one is refused, and a
// body naming none takes the row's, not the caller's default.
func TestConvertIntake_TakesThePlannedOrdersLegalEntity(t *testing.T) {
	warehouse := ids.New()
	row := openRow(&warehouse)
	le := entityID
	row.LegalEntityID = &le
	f, h := intakeServer(t, row)
	c := call{method: http.MethodPost, path: "/v1/production/planned-intake/" + row.ID + ":convert", duties: []string{DutyWrite},
		headers: map[string]string{idempotencyName: "conv-le"}, body: createBody(func(m map[string]any) { m["legalEntityId"] = ids.New() })}
	p := expectProblem(t, do(h, c), http.StatusBadRequest, validationCode)
	if _, ok := p.Errors["legalEntityId"]; !ok || len(f.created) != 0 {
		t.Fatalf("another legal entity in the body: %+v, created %d", p.Errors, len(f.created))
	}
	c.body = createBody(func(m map[string]any) { delete(m, "legalEntityId") })
	if rec := do(h, c); rec.Code != http.StatusCreated || f.created[0].LegalEntityID != entityID {
		t.Fatalf("row's legal entity: %d %+v %s", rec.Code, f.created, rec.Body.String())
	}
}

// A retry of a conversion that succeeded (same Idempotency-Key and body) replays the order although
// the row is converted by then; a new key on the converted row is refused.
func TestConvertIntake_RetryReplaysTheOrder(t *testing.T) {
	warehouse := ids.New()
	row := openRow(&warehouse)
	f, h := intakeServer(t, row)
	c := call{method: http.MethodPost, path: "/v1/production/planned-intake/" + row.ID + ":convert", duties: []string{DutyWrite},
		headers: map[string]string{idempotencyName: "conv-retry"}, body: createBody(nil)}
	if rec := do(h, c); rec.Code != http.StatusCreated {
		t.Fatalf("first conversion: %d %s", rec.Code, rec.Body.String())
	}
	converted := f.intake.rows[row.ID]
	converted.Status = store.IntakeConverted
	f.intake.rows[row.ID] = converted
	rec := do(h, c)
	if rec.Code != http.StatusOK || rec.Header().Get("Idempotent-Replayed") != "true" || len(f.created) != 1 {
		t.Fatalf("retry: %d replayed=%q created=%d %s", rec.Code, rec.Header().Get("Idempotent-Replayed"), len(f.created), rec.Body.String())
	}
	c.headers = map[string]string{idempotencyName: "conv-other"}
	expectProblem(t, do(h, c), http.StatusConflict, store.CodeIntakeNotOpen)
	if len(f.created) != 1 {
		t.Fatalf("a second order was created: %d", len(f.created))
	}
}

func TestConvertIntake_SiteScopeRowNeedsAWarehouseInTheBody(t *testing.T) {
	row := openRow(nil)
	f, h := intakeServer(t, row)
	call1 := call{method: http.MethodPost, path: "/v1/production/planned-intake/" + row.ID + ":convert", duties: []string{DutyWrite},
		headers: map[string]string{idempotencyName: "conv-2"}, body: createBody(func(m map[string]any) { delete(m, "warehouseId") })}
	rec := do(h, call1)
	p := expectProblem(t, rec, http.StatusBadRequest, validationCode)
	if _, ok := p.Errors["warehouseId"]; !ok {
		t.Fatalf("errors: %+v", p.Errors)
	}
	wh := ids.New()
	call1.body = createBody(func(m map[string]any) { m["warehouseId"] = wh })
	if rec := do(h, call1); rec.Code != http.StatusCreated || f.created[0].WarehouseID != wh {
		t.Fatalf("site scope row with a warehouse: %d %s", rec.Code, rec.Body.String())
	}
}

func TestConvertIntake_Refusals(t *testing.T) {
	done := openRow(nil)
	done.Status = store.IntakeConverted
	open := openRow(nil)
	f, h := intakeServer(t, done, open)
	base := call{method: http.MethodPost, duties: []string{DutyWrite}, headers: map[string]string{idempotencyName: "k"}, body: createBody(nil)}
	for name, tc := range map[string]struct {
		path       string
		mut        func(c *call)
		status     int
		code       string
		duties     []string
		wantCreate int
	}{
		"already converted": {path: done.ID + ":convert", status: http.StatusConflict, code: store.CodeIntakeNotOpen},
		"unknown row":       {path: ids.New() + ":convert", status: http.StatusNotFound, code: store.CodeIntakeNotFound},
		"unknown command":   {path: open.ID + ":explode", status: http.StatusNotFound, code: unknownActionCode},
		"no key": {path: open.ID + ":convert", status: http.StatusBadRequest, code: validationCode,
			mut: func(c *call) { c.headers = nil }},
		"rework reference": {path: open.ID + ":convert", status: http.StatusBadRequest, code: validationCode,
			mut: func(c *call) {
				c.body = createBody(func(m map[string]any) { m["reworkReference"] = map[string]any{"type": "order", "id": ids.New()} })
			}},
		"read duty only": {path: open.ID + ":convert", status: http.StatusForbidden, code: "AUTH-0003", duties: []string{DutyRead}},
	} {
		c := base
		c.path = "/v1/production/planned-intake/" + tc.path
		if tc.mut != nil {
			tc.mut(&c)
		}
		if tc.duties != nil {
			c.duties = tc.duties
		}
		expectProblem(t, do(h, c), tc.status, tc.code)
		if len(f.created) != 0 {
			t.Fatalf("%s reached the store as a create", name)
		}
	}
}

func TestDismissIntake(t *testing.T) {
	row := openRow(nil)
	f, h := intakeServer(t, row)
	path := "/v1/production/planned-intake/" + row.ID + ":dismiss"
	expectProblem(t, do(h, call{method: http.MethodPost, path: path, body: `{"reason":"x"}`, duties: []string{DutyWrite}}), http.StatusPreconditionRequired, "precondition_required")
	expectProblem(t, do(h, call{method: http.MethodPost, path: path, body: `{"reason":" "}`, duties: []string{DutyWrite}, headers: map[string]string{"If-Match": `"1"`}}), http.StatusBadRequest, validationCode)
	rec := do(h, call{method: http.MethodPost, path: path, body: `{"reason":"Customer cancelled"}`, duties: []string{DutyWrite}, headers: map[string]string{"If-Match": `"1"`}})
	if rec.Code != http.StatusOK || rec.Header().Get("ETag") != `"2"` {
		t.Fatalf("dismiss: %d etag %q %s", rec.Code, rec.Header().Get("ETag"), rec.Body.String())
	}
	if len(f.intake.dismissed) != 1 || f.intake.dismissed[0].Reason != "Customer cancelled" || f.intake.dismissed[0].IfMatch != 1 || f.intake.dismissed[0].Actor != "planner-1" {
		t.Fatalf("dismissed: %+v", f.intake.dismissed)
	}
}

func TestListAndGetIntake(t *testing.T) {
	row := openRow(nil)
	f, h := intakeServer(t, row)
	rec := do(h, call{method: http.MethodGet, path: "/v1/production/planned-intake?status=open&limit=10", duties: []string{DutyRead}})
	if rec.Code != http.StatusOK || len(f.intake.listFilter) != 1 || f.intake.listFilter[0].Status != "open" || f.intake.listFilter[0].Limit != 10 {
		t.Fatalf("list: %d %+v", rec.Code, f.intake.listFilter)
	}
	for _, q := range []string{"status=done", "siteId=x", "limit=0", "cursor=x"} {
		expectProblem(t, do(h, call{method: http.MethodGet, path: "/v1/production/planned-intake?" + q, duties: []string{DutyRead}}), http.StatusBadRequest, validationCode)
	}
	rec = do(h, call{method: http.MethodGet, path: "/v1/production/planned-intake/" + row.ID, duties: []string{DutyRead}})
	if rec.Code != http.StatusOK || rec.Header().Get("ETag") != `"1"` {
		t.Fatalf("get: %d %s", rec.Code, rec.Body.String())
	}
	expectProblem(t, do(h, call{method: http.MethodGet, path: "/v1/production/planned-intake/not-a-uuid", duties: []string{DutyRead}}), http.StatusNotFound, store.CodeIntakeNotFound)
	expectProblem(t, do(h, call{method: http.MethodGet, path: "/v1/production/planned-intake", duties: []string{DutyCostRead}}), http.StatusForbidden, "AUTH-0003")
}

// ---- the planning consumer ----

func consumerEvent(t *testing.T, mut func(m map[string]any)) events.Event {
	t.Helper()
	m := map[string]any{
		"planned_order_id": ids.New(), "plan_version_id": ids.New(), "batch_id": ids.New(), "order_type": "production", "target_type": "production-order",
		"target_id": ids.New(), "item_id": ids.New(), "warehouse_id": ids.New(), "site_id": ids.New(), "vendor_id": nil, "qty": "12.5",
		"order_date": "2026-11-03", "delivery_date": "2026-11-12", "quota_arrangement_id": nil, "quota_source_id": nil,
	}
	if mut != nil {
		mut(m)
	}
	raw, _ := json.Marshal(m)
	return events.Event{ID: ids.New(), Type: PlannedOrderFirmedType, Source: PlannedOrderSource, TenantID: tenantID, Data: raw}
}

func consumerCtx(duties ...string) context.Context {
	set := map[string]struct{}{}
	for _, d := range duties {
		set[d] = struct{}{}
	}
	return tenant.With(context.Background(), tenant.Principal{TenantID: tenantID, UserID: "service:" + PlanningConsumerName, Duties: set})
}

func TestPlanningConsumer_StoresAFirmedProductionOrderWithItsWarehouse(t *testing.T) {
	f := &fakeStore{}
	h := PlanningConsumerHandlers(f)[PlannedOrderFirmedType]
	evt := consumerEvent(t, nil)
	if err := h.Handle(consumerCtx(DutyPlannedIntake), nil, evt); err != nil {
		t.Fatal(err)
	}
	var d map[string]any
	_ = json.Unmarshal(evt.Data, &d)
	if len(f.intake.recorded) != 1 {
		t.Fatalf("recorded %d", len(f.intake.recorded))
	}
	got := f.intake.recorded[0]
	if got.PlannedOrderID != d["planned_order_id"] || got.TargetID != d["target_id"] || got.ProductID != d["item_id"] ||
		got.WarehouseID == nil || *got.WarehouseID != d["warehouse_id"] || !got.Quantity.Equal(decimal.RequireFromString("12.5")) ||
		got.DeliveryDate != "2026-11-12" || got.EventID != evt.ID {
		t.Fatalf("recorded: %+v", got)
	}
}

// The planned order's legal entity is stored when the event names it (payload legal_entity_id or the
// CloudEvents legalentityid extension); planning's current event names none and the row has none.
func TestPlanningConsumer_KeepsThePlannedOrdersLegalEntity(t *testing.T) {
	f := &fakeStore{}
	h := PlanningConsumerHandlers(f)[PlannedOrderFirmedType]
	inPayload, inExtension := ids.New(), ids.New()
	withPayload := consumerEvent(t, func(m map[string]any) { m["legal_entity_id"] = inPayload })
	withExtension := consumerEvent(t, nil)
	withExtension.LegalEntityID = inExtension
	for _, evt := range []events.Event{withPayload, withExtension, consumerEvent(t, nil)} {
		if err := h.Handle(consumerCtx(DutyPlannedIntake), nil, evt); err != nil {
			t.Fatal(err)
		}
	}
	got := f.intake.recorded
	if len(got) != 3 || got[0].LegalEntityID == nil || *got[0].LegalEntityID != inPayload ||
		got[1].LegalEntityID == nil || *got[1].LegalEntityID != inExtension || got[2].LegalEntityID != nil {
		t.Fatalf("legal entities: %+v", got)
	}
}

func TestPlanningConsumer_IgnoresPurchaseAndTransferOrders(t *testing.T) {
	f := &fakeStore{}
	h := PlanningConsumerHandlers(f)[PlannedOrderFirmedType]
	for _, typ := range []string{"purchase", "transfer"} {
		if err := h.Handle(consumerCtx(DutyPlannedIntake), nil, consumerEvent(t, func(m map[string]any) { m["order_type"] = typ })); err != nil {
			t.Fatal(err)
		}
	}
	if len(f.intake.recorded) != 0 {
		t.Fatalf("recorded %d", len(f.intake.recorded))
	}
}

func TestPlanningConsumer_RefusesWhatItCannotStore(t *testing.T) {
	h := PlanningConsumerHandlers(&fakeStore{})[PlannedOrderFirmedType]
	other := consumerEvent(t, nil)
	other.Source = "sales"
	for name, tc := range map[string]struct {
		ctx context.Context
		evt events.Event
	}{
		"no duty":          {consumerCtx("production.order.read"), consumerEvent(t, nil)},
		"foreign source":   {consumerCtx(DutyPlannedIntake), other},
		"zero quantity":    {consumerCtx(DutyPlannedIntake), consumerEvent(t, func(m map[string]any) { m["qty"] = "0" })},
		"seven decimals":   {consumerCtx(DutyPlannedIntake), consumerEvent(t, func(m map[string]any) { m["qty"] = "1.0000001" })},
		"bad target":       {consumerCtx(DutyPlannedIntake), consumerEvent(t, func(m map[string]any) { m["target_id"] = "x" })},
		"bad warehouse":    {consumerCtx(DutyPlannedIntake), consumerEvent(t, func(m map[string]any) { m["warehouse_id"] = "x" })},
		"bad delivery day": {consumerCtx(DutyPlannedIntake), consumerEvent(t, func(m map[string]any) { m["delivery_date"] = "soon" })},
		"bad legal entity": {consumerCtx(DutyPlannedIntake), consumerEvent(t, func(m map[string]any) { m["legal_entity_id"] = "le-1" })},
		"not json":         {consumerCtx(DutyPlannedIntake), events.Event{ID: ids.New(), Source: PlannedOrderSource, Data: []byte("[")}},
	} {
		err := h.Handle(tc.ctx, nil, tc.evt)
		if err == nil || !eventing.IsPermanent(err) {
			t.Errorf("%s: err = %v, want a permanent error", name, err)
		}
	}
}
