package api

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/BenjaminSRussell/passage/platform/eventing"
	"github.com/BenjaminSRussell/passage/platform/events"
	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/platform/ids"
	platformtenant "github.com/BenjaminSRussell/passage/platform/tenant"
	"github.com/BenjaminSRussell/passage/services/purchasing/internal/domain"
	"github.com/BenjaminSRussell/passage/services/purchasing/internal/store"
)

// inboundLog is what the inbound methods of the fake store received and answer.
type inboundLog struct {
	changes   []store.InboundChange
	filter    store.InboundFilter
	closed    []store.DropShipClosedIn
	cancelled []store.DropShipCancelledIn
	acks      []string
	retries   []string
	last      store.Idem
	applied   []store.PlanningMessageIn
	err       error
}

func (f *fakeStore) GetInboundChange(_ context.Context, id string) (store.InboundChange, error) {
	for _, c := range f.inbound.changes {
		if c.ID == id {
			return c, nil
		}
	}
	return store.InboundChange{}, httpx.NewProblem(http.StatusNotFound, store.CodeInboundChangeNotFound, "not found")
}

func (f *fakeStore) ListInboundChanges(_ context.Context, flt store.InboundFilter, _ string, _ int) (store.Page[store.InboundChange], error) {
	f.inbound.filter = flt
	return store.Page[store.InboundChange]{Items: f.inbound.changes}, nil
}

func (f *fakeStore) AcknowledgeInboundChange(_ context.Context, id, note string, idem store.Idem) (store.InboundChange, error) {
	f.inbound.acks = append(f.inbound.acks, id+"|"+note)
	f.inbound.last = idem
	c := store.InboundChange{ID: id, Status: domain.InboundRefused, Resolution: domain.ResolutionAcknowledged, ResolutionNote: note}
	return c, f.inbound.err
}

func (f *fakeStore) RetryInboundChange(_ context.Context, id, note string, idem store.Idem) (store.InboundChange, error) {
	f.inbound.retries = append(f.inbound.retries, id+"|"+note)
	f.inbound.last = idem
	return store.InboundChange{ID: id, Status: domain.InboundApplied, Resolution: domain.ResolutionRetried, Attempts: 2}, f.inbound.err
}

func (f *fakeStore) ApplyDropShipCancelled(_ context.Context, _ pgx.Tx, in store.DropShipCancelledIn) (store.InboundChange, bool, error) {
	f.inbound.cancelled = append(f.inbound.cancelled, in)
	return store.InboundChange{Status: domain.InboundApplied}, true, f.inbound.err
}

func (f *fakeStore) ApplyDropShipClosed(_ context.Context, _ pgx.Tx, in store.DropShipClosedIn) (store.InboundChange, bool, error) {
	f.inbound.closed = append(f.inbound.closed, in)
	return store.InboundChange{Status: domain.InboundApplied}, true, f.inbound.err
}

func (f *fakeStore) ApplyPlanningMessage(_ context.Context, _ pgx.Tx, in store.PlanningMessageIn) (store.InboundChange, bool, error) {
	f.inbound.applied = append(f.inbound.applied, in)
	return store.InboundChange{Status: domain.InboundApplied}, true, f.inbound.err
}

func TestInboundChanges_ReadRoutes(t *testing.T) {
	h := newHarness(t)
	po := ids.New()
	row := store.InboundChange{ID: ids.New(), LegalEntityID: leA, Source: domain.SourcePlanningMessage, SourceKey: ids.New(), Action: "postpone",
		PurchaseOrderID: po, PONumber: "PO-0000001", LineNo: 1, Status: domain.InboundApplied, VersionNo: 2, CreatedAt: time.Date(2027, 3, 1, 9, 0, 0, 0, time.UTC)}
	h.fake.inbound.changes = []store.InboundChange{row}
	inquire := []string{dutyPurchaseOrderInquire}

	for _, tc := range []struct {
		name   string
		c      call
		status int
		code   string
	}{
		{"no duty", call{method: "GET", path: "/v1/procurement/inbound-changes", user: userA}, 403, "AUTH-0003"},
		{"maintain alone cannot read", call{method: "GET", path: "/v1/procurement/inbound-changes", user: userA, duties: []string{dutyPurchaseOrderMaintain}}, 403, "AUTH-0003"},
		{"bad source", call{method: "GET", path: "/v1/procurement/inbound-changes?source=EDI", user: userA, duties: inquire}, 422, domain.CodeValidation},
		{"bad status", call{method: "GET", path: "/v1/procurement/inbound-changes?status=DONE", user: userA, duties: inquire}, 422, domain.CodeValidation},
		{"bad purchase order id", call{method: "GET", path: "/v1/procurement/inbound-changes?purchaseOrderId=x", user: userA, duties: inquire}, 422, domain.CodeValidation},
		{"unknown change", call{method: "GET", path: "/v1/procurement/inbound-changes/" + ids.New(), user: userA, duties: inquire}, 404, store.CodeInboundChangeNotFound},
	} {
		rec := h.do(tc.c)
		if rec.Code != tc.status || problemCode(t, rec) != tc.code {
			t.Errorf("%s: status %d code %s: %s", tc.name, rec.Code, problemCode(t, rec), rec.Body)
		}
	}

	rec := h.do(call{method: "GET", path: "/v1/procurement/inbound-changes?source=PLANNING_MESSAGE&status=APPLIED&purchaseOrderId=" + po, user: userA, duties: inquire})
	var page struct {
		Items []store.InboundChange `json:"items"`
	}
	if rec.Code != http.StatusOK || json.Unmarshal(rec.Body.Bytes(), &page) != nil || len(page.Items) != 1 || page.Items[0].ID != row.ID {
		t.Fatalf("list: status %d: %s", rec.Code, rec.Body)
	}
	if f := h.fake.inbound.filter; f.Source != domain.SourcePlanningMessage || f.Status != domain.InboundApplied || f.PurchaseOrderID != po {
		t.Fatalf("filter = %+v", f)
	}
	rec = h.do(call{method: "GET", path: "/v1/procurement/inbound-changes/" + row.ID, user: userA, duties: inquire})
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"poNumber":"PO-0000001"`) {
		t.Fatalf("get: status %d: %s", rec.Code, rec.Body)
	}
}

func inboundCtx(duties ...string) context.Context {
	d := map[string]struct{}{}
	for _, x := range duties {
		d[x] = struct{}{}
	}
	return platformtenant.With(context.Background(), platformtenant.Principal{TenantID: tenantA, UserID: "service:purchasing-inbound", Duties: d})
}

func inboundEvent(typ, source, data string) events.Event {
	return events.Event{ID: ids.New(), Type: typ, Source: source, TenantID: tenantA, Data: json.RawMessage(data)}
}

func TestInboundConsumer_DropShipClosed(t *testing.T) {
	f := newFake()
	h := InboundConsumerHandlers(f)[DropShipClosedType]
	ctx := inboundCtx(DutyInboundApply)
	good := `{"drop_ship_id":"` + ids.New() + `","drop_ship_number":"DS-0000007","legal_entity_id":"` + leA + `","purchase_order_id":"` + ids.New() +
		`","purchase_order_line_id":"` + ids.New() + `","qty_closed":"4.5","qty_confirmed":"5.5","reason":"order_remainder_cancelled"}`
	if err := h.Handle(ctx, nil, inboundEvent(DropShipClosedType, DropShipSource, good)); err != nil {
		t.Fatal(err)
	}
	if len(f.inbound.closed) != 1 || f.inbound.closed[0].QtyClosed.String() != "4.5" || f.inbound.closed[0].DropShipNumber != "DS-0000007" || f.inbound.closed[0].LegalEntityID != leA {
		t.Fatalf("store got %+v", f.inbound.closed)
	}

	for name, tc := range map[string]struct {
		ctx  context.Context
		evt  events.Event
		perm bool
	}{
		"no duty":       {inboundCtx(), inboundEvent(DropShipClosedType, DropShipSource, good), true},
		"other source":  {ctx, inboundEvent(DropShipClosedType, "sales", good), true},
		"not json":      {ctx, inboundEvent(DropShipClosedType, DropShipSource, `[`), true},
		"no line id":    {ctx, inboundEvent(DropShipClosedType, DropShipSource, strings.Replace(good, `"purchase_order_line_id"`, `"x"`, 1)), true},
		"bad quantity":  {ctx, inboundEvent(DropShipClosedType, DropShipSource, strings.Replace(good, `"4.5"`, `"lots"`, 1)), true},
		"negative":      {ctx, inboundEvent(DropShipClosedType, DropShipSource, strings.Replace(good, `"4.5"`, `"-1"`, 1)), true},
		"no legal ent.": {ctx, inboundEvent(DropShipClosedType, DropShipSource, strings.Replace(good, leA, "", 1)), true},
	} {
		if err := h.Handle(tc.ctx, nil, tc.evt); err == nil || eventing.IsPermanent(err) != tc.perm {
			t.Errorf("%s: err = %v", name, err)
		}
	}
	if len(f.inbound.closed) != 1 {
		t.Fatalf("a refused event reached the store: %d calls", len(f.inbound.closed))
	}
	// A store failure is retried, not dead-lettered.
	f.inbound.err = context.DeadlineExceeded
	if err := h.Handle(ctx, nil, inboundEvent(DropShipClosedType, DropShipSource, good)); err == nil || eventing.IsPermanent(err) {
		t.Fatalf("store failure = %v", err)
	}
}

func TestInboundConsumer_MessageApplied(t *testing.T) {
	f := newFake()
	h := InboundConsumerHandlers(f)[MessageAppliedType]
	ctx := inboundCtx(DutyInboundApply)
	msg := func(action, supply, le, warehouse, qty string) string {
		return `{"message_id":"` + ids.New() + `","legal_entity_id":` + le + `,"action":"` + action + `","item_id":"` + ids.New() +
			`","warehouse_id":` + warehouse + `,"supply_ref":"` + supply + `","suggested_date":"2027-03-17","qty":"` + qty + `","decided_by":"u","note":"n"}`
	}
	le := `"` + leA + `"`
	if err := h.Handle(ctx, nil, inboundEvent(MessageAppliedType, MessageSource, msg("postpone", "PO-0000001", le, "null", "100"))); err != nil {
		t.Fatal(err)
	}
	wh := ids.New()
	if err := h.Handle(ctx, nil, inboundEvent(MessageAppliedType, MessageSource, msg("decrease", "PO-0000002", le, `"`+wh+`"`, "12.5"))); err != nil {
		t.Fatal(err)
	}
	if len(f.inbound.applied) != 2 {
		t.Fatalf("store got %d messages", len(f.inbound.applied))
	}
	if a, b := f.inbound.applied[0], f.inbound.applied[1]; a.SuggestedDate.String() != "2027-03-17" || a.WarehouseID != "" || a.DecidedBy != "u" ||
		b.WarehouseID != wh || b.Qty.String() != "12.5" || b.LegalEntityID != leA || b.SupplyRef != "PO-0000002" {
		t.Fatalf("store got %+v and %+v", a, b)
	}

	// Not for a purchase order: no store call and no error.
	for name, data := range map[string]string{
		"shortage":       msg("shortage", "PO-0000003", le, "null", "5"),
		"planned order":  msg("cancel", "", le, "null", "5"),
		"no legal ent.":  msg("cancel", "PO-0000003", "null", "null", "5"),
		"unknown action": msg("teleport", "PO-0000003", le, "null", "5"),
	} {
		if err := h.Handle(ctx, nil, inboundEvent(MessageAppliedType, MessageSource, data)); err != nil {
			t.Errorf("%s: %v", name, err)
		}
	}
	if len(f.inbound.applied) != 2 {
		t.Fatalf("skipped messages reached the store: %d", len(f.inbound.applied))
	}
	for name, tc := range map[string]struct {
		ctx context.Context
		evt events.Event
	}{
		"no duty":       {inboundCtx(), inboundEvent(MessageAppliedType, MessageSource, msg("cancel", "PO-1", le, "null", "5"))},
		"other source":  {ctx, inboundEvent(MessageAppliedType, "fulfillment", msg("cancel", "PO-1", le, "null", "5"))},
		"bad json":      {ctx, inboundEvent(MessageAppliedType, MessageSource, `{`)},
		"bad item":      {ctx, inboundEvent(MessageAppliedType, MessageSource, strings.Replace(msg("cancel", "PO-1", le, "null", "5"), `"item_id"`, `"x"`, 1))},
		"bad quantity":  {ctx, inboundEvent(MessageAppliedType, MessageSource, msg("cancel", "PO-1", le, "null", "-5"))},
		"bad date":      {ctx, inboundEvent(MessageAppliedType, MessageSource, strings.Replace(msg("cancel", "PO-1", le, "null", "5"), "2027-03-17", "soon", 1))},
		"bad entity":    {ctx, inboundEvent(MessageAppliedType, MessageSource, msg("cancel", "PO-1", `"nope"`, "null", "5"))},
		"bad warehouse": {ctx, inboundEvent(MessageAppliedType, MessageSource, msg("cancel", "PO-1", le, `"nope"`, "5"))},
	} {
		if err := h.Handle(tc.ctx, nil, tc.evt); err == nil || !eventing.IsPermanent(err) {
			t.Errorf("%s: err = %v", name, err)
		}
	}
}

func TestInboundChanges_SettleActions(t *testing.T) {
	h := newHarness(t)
	row := store.InboundChange{ID: ids.New(), LegalEntityID: leA, Source: domain.SourcePlanningMessage, Status: domain.InboundRefused, Attempts: 1}
	h.fake.inbound.changes = []store.InboundChange{row}
	path := "/v1/procurement/inbound-changes/" + row.ID
	maintain := []string{dutyPurchaseOrderMaintain}
	key := map[string]string{"Idempotency-Key": "k-" + row.ID}

	for _, tc := range []struct {
		name   string
		c      call
		status int
		code   string
	}{
		{"inquire alone cannot settle", call{method: "POST", path: path + ":retry", user: userA, duties: []string{dutyPurchaseOrderInquire}, headers: key}, 403, "AUTH-0003"},
		{"no duty", call{method: "POST", path: path + ":acknowledge", user: userA, headers: key}, 403, "AUTH-0003"},
		{"unknown action", call{method: "POST", path: path + ":delete", user: userA, duties: maintain, headers: key}, 404, codeUnknownAction},
		{"no action", call{method: "POST", path: path, user: userA, duties: maintain, headers: key}, 404, codeUnknownAction},
		{"no idempotency key", call{method: "POST", path: path + ":retry", user: userA, duties: maintain}, 400, ""},
		{"note too long", call{method: "POST", path: path + ":retry", user: userA, duties: maintain, headers: key, body: `{"note":"` + strings.Repeat("x", 1001) + `"}`}, 422, domain.CodeValidation},
		{"unknown field", call{method: "POST", path: path + ":retry", user: userA, duties: maintain, headers: key, body: `{"force":true}`}, 422, domain.CodeValidation},
		{"unknown change", call{method: "POST", path: "/v1/procurement/inbound-changes/" + ids.New() + ":retry", user: userA, duties: maintain, headers: key}, 404, store.CodeInboundChangeNotFound},
		{"other legal entity", call{method: "POST", path: path + ":retry", user: userA, scope: "00000000-0000-7000-8000-0000000001e3", duties: maintain, headers: key}, 403, "AUTH-0003"},
	} {
		rec := h.do(tc.c)
		if rec.Code != tc.status || (tc.code != "" && problemCode(t, rec) != tc.code) {
			t.Errorf("%s: status %d code %s: %s", tc.name, rec.Code, problemCode(t, rec), rec.Body)
		}
	}
	if len(h.fake.inbound.acks)+len(h.fake.inbound.retries) != 0 {
		t.Fatal("a refused request reached the store")
	}

	rec := h.do(call{method: "POST", path: path + ":acknowledge", user: userA, duties: maintain, headers: key, body: `{"note":"changed by hand"}`})
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"resolution":"ACKNOWLEDGED"`) || h.fake.inbound.acks[0] != row.ID+"|changed by hand" {
		t.Fatalf("acknowledge: status %d: %s", rec.Code, rec.Body)
	}
	rec = h.do(call{method: "POST", path: path + ":retry", user: userA, duties: maintain, headers: key})
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"resolution":"RETRIED"`) || h.fake.inbound.retries[0] != row.ID+"|" {
		t.Fatalf("retry: status %d: %s", rec.Code, rec.Body)
	}
	if h.fake.inbound.last.Key != key["Idempotency-Key"] || h.fake.inbound.last.Hash == "" {
		t.Fatalf("idempotency = %+v", h.fake.inbound.last)
	}

	h.fake.inbound.err = httpx.NewProblem(http.StatusConflict, domain.CodeInboundNotOpen, "settled")
	rec = h.do(call{method: "POST", path: path + ":retry", user: userA, duties: maintain, headers: key})
	if rec.Code != http.StatusConflict || problemCode(t, rec) != domain.CodeInboundNotOpen {
		t.Fatalf("settled change: status %d: %s", rec.Code, rec.Body)
	}
}

func TestInboundChanges_ListFiltersByResolution(t *testing.T) {
	h := newHarness(t)
	inquire := []string{dutyPurchaseOrderInquire}
	rec := h.do(call{method: "GET", path: "/v1/procurement/inbound-changes?status=REFUSED&resolution=NONE&source=DROPSHIP_CANCELLED", user: userA, duties: inquire})
	if rec.Code != http.StatusOK || h.fake.inbound.filter.Resolution != "NONE" || h.fake.inbound.filter.Source != domain.SourceDropShipCancelled {
		t.Fatalf("status %d filter %+v", rec.Code, h.fake.inbound.filter)
	}
	rec = h.do(call{method: "GET", path: "/v1/procurement/inbound-changes?resolution=DONE", user: userA, duties: inquire})
	if rec.Code != 422 || problemCode(t, rec) != domain.CodeValidation {
		t.Fatalf("bad resolution: status %d: %s", rec.Code, rec.Body)
	}
}

func TestInboundConsumer_DropShipCancelled(t *testing.T) {
	f := newFake()
	h := InboundConsumerHandlers(f)[DropShipCancelledType]
	ctx := inboundCtx(DutyInboundApply)
	good := `{"drop_ship_id":"` + ids.New() + `","drop_ship_number":"DS-0000008","status":"CANCELLED","legal_entity_id":"` + leA + `","purchase_order_id":"` + ids.New() +
		`","purchase_order_line_id":"` + ids.New() + `","qty":"10","qty_confirmed":"0","reason":"order_remainder_cancelled"}`
	if err := h.Handle(ctx, nil, inboundEvent(DropShipCancelledType, DropShipSource, good)); err != nil {
		t.Fatal(err)
	}
	if len(f.inbound.cancelled) != 1 || f.inbound.cancelled[0].Qty.String() != "10" || !f.inbound.cancelled[0].QtyConfirmed.IsZero() ||
		f.inbound.cancelled[0].DropShipNumber != "DS-0000008" || f.inbound.cancelled[0].LegalEntityID != leA {
		t.Fatalf("store got %+v", f.inbound.cancelled)
	}
	// A missing qty_confirmed counts as nothing confirmed.
	if err := h.Handle(ctx, nil, inboundEvent(DropShipCancelledType, DropShipSource, strings.Replace(good, `,"qty_confirmed":"0"`, "", 1))); err != nil {
		t.Fatal(err)
	}
	for name, tc := range map[string]struct {
		ctx context.Context
		evt events.Event
	}{
		"no duty":       {inboundCtx(), inboundEvent(DropShipCancelledType, DropShipSource, good)},
		"other source":  {ctx, inboundEvent(DropShipCancelledType, "sales", good)},
		"not json":      {ctx, inboundEvent(DropShipCancelledType, DropShipSource, `[`)},
		"no line id":    {ctx, inboundEvent(DropShipCancelledType, DropShipSource, strings.Replace(good, `"purchase_order_line_id"`, `"x"`, 1))},
		"bad quantity":  {ctx, inboundEvent(DropShipCancelledType, DropShipSource, strings.Replace(good, `"10"`, `"lots"`, 1))},
		"negative":      {ctx, inboundEvent(DropShipCancelledType, DropShipSource, strings.Replace(good, `"10"`, `"-1"`, 1))},
		"bad confirmed": {ctx, inboundEvent(DropShipCancelledType, DropShipSource, strings.Replace(good, `"qty_confirmed":"0"`, `"qty_confirmed":"-2"`, 1))},
		"no legal ent.": {ctx, inboundEvent(DropShipCancelledType, DropShipSource, strings.Replace(good, leA, "", 1))},
	} {
		if err := h.Handle(tc.ctx, nil, tc.evt); err == nil || !eventing.IsPermanent(err) {
			t.Errorf("%s: err = %v", name, err)
		}
	}
	if len(f.inbound.cancelled) != 2 {
		t.Fatalf("a refused event reached the store: %d calls", len(f.inbound.cancelled))
	}
	f.inbound.err = context.DeadlineExceeded
	if err := h.Handle(ctx, nil, inboundEvent(DropShipCancelledType, DropShipSource, good)); err == nil || eventing.IsPermanent(err) {
		t.Fatalf("store failure = %v", err)
	}
}
