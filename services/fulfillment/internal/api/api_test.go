package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/services/fulfillment/internal/domain"
	"github.com/BenjaminSRussell/passage/services/fulfillment/internal/store"
)

const tenant = "00000000-0000-0000-0000-000000000001"

// fakeStore records calls and returns canned results; it never touches Postgres.
type fakeStore struct {
	runs      int
	lastIdem  store.Idem
	released  []domain.ReleaseGroup
	shipment  store.Shipment
	notFound  bool
	stalePick bool

	warehouse       string
	storeSkips      []domain.Skip
	shipmentFilter  store.ShipmentFilter
	slipFilter      store.PackingSlipFilter
	picks           []store.PickInput
	assigned        string
	assignedVersion int
	closedVersion   int
	lastPost        store.PostInput
	packing         packingCalls
	workbench       workbenchCalls
	dniFilter       store.DNIFilter
	dniRows         []domain.DNIRow
	ssccFilter      store.SSCCFilter
	ssccRows        []store.SSCCRow
	ssccCalls       int
	reports         reportCalls
	orderTypes      map[string]string
	exceptionFilter store.ExceptionFilter
	resolved        string
	resolvedVersion int
	heldOrders      map[string]bool
}

func (f *fakeStore) RunRelease(_ context.Context, warehouseID string, groups []domain.ReleaseGroup, idem store.Idem) (store.ReleaseResult, error) {
	f.runs++
	f.lastIdem = idem
	f.released = groups
	f.warehouse = warehouseID
	out := store.ReleaseResult{Shipments: []store.Shipment{}, Skipped: f.storeSkips}
	for range groups {
		out.Shipments = append(out.Shipments, store.Shipment{ID: ids.New(), Status: domain.StatusReleased, Version: 1})
	}
	return out, nil
}

func (f *fakeStore) ListShipments(_ context.Context, filter store.ShipmentFilter) ([]store.ShipmentSummary, error) {
	f.shipmentFilter = filter
	return []store.ShipmentSummary{{ID: ids.New(), ShipmentNumber: "SH-0000001", Status: domain.StatusPacked, Version: 4, LineCount: 2}}, nil
}

func (f *fakeStore) ListPackingSlips(_ context.Context, filter store.PackingSlipFilter) ([]store.PackingSlipSummary, error) {
	f.slipFilter = filter
	return []store.PackingSlipSummary{}, nil
}

func (f *fakeStore) GetPackingSlip(_ context.Context, id string) (store.PackingSlip, error) {
	if f.notFound {
		return store.PackingSlip{}, httpx.NewProblem(http.StatusNotFound, store.CodeSlipNotFound, "packing slip not found")
	}
	return store.PackingSlip{ID: id, Number: "PS-0000001", Status: "POSTED", Currency: "USD",
		Journal: []store.JournalEntry{{Account: "1405", Amount: decimal.RequireFromString("125"), Currency: "USD"}}}, nil
}

func (f *fakeStore) GetShipment(_ context.Context, id string) (store.Shipment, error) {
	if f.notFound {
		return store.Shipment{}, httpx.NewProblem(http.StatusNotFound, store.CodeShipmentNotFound, "shipment not found")
	}
	f.shipment.ID = id
	f.shipment.Version = 3
	return f.shipment, nil
}

func (f *fakeStore) ConfirmPick(_ context.Context, id string, version int, picks []store.PickInput, idem store.Idem) (store.Shipment, error) {
	f.lastIdem = idem
	f.picks = picks
	if f.stalePick || version != 3 {
		return store.Shipment{}, httpx.NewProblem(http.StatusPreconditionFailed, store.CodeVersion, "shipment version changed")
	}
	return store.Shipment{ID: id, Version: 4, Status: domain.StatusPicked}, nil
}

func (f *fakeStore) PackContainer(_ context.Context, id string, _ int, _ string, _ []store.PackInput, idem store.Idem) (store.PackResult, error) {
	f.lastIdem = idem
	return store.PackResult{Shipment: store.Shipment{ID: id, Version: 5}}, nil
}

func (f *fakeStore) PostPackingSlip(_ context.Context, id string, _ int, in store.PostInput, idem store.Idem) (store.PackingSlip, error) {
	f.lastIdem = idem
	f.lastPost = in
	if !in.PostingDate.Equal(time.Date(2026, 10, 21, 0, 0, 0, 0, time.UTC)) {
		return store.PackingSlip{}, httpx.NewProblem(http.StatusBadRequest, store.CodeStatus, "bad date")
	}
	return store.PackingSlip{ID: id, Status: "POSTED", Currency: in.Currency, TotalCost: decimal.RequireFromString("125.00")}, nil
}

func (f *fakeStore) CancelPackingSlip(_ context.Context, slipID string, idem store.Idem) (store.PackingSlip, error) {
	f.lastIdem = idem
	return store.PackingSlip{ID: slipID, Status: "POSTED"}, nil
}

func (f *fakeStore) Ping(context.Context) error { return nil }

// newServer mounts the API with the kernel router; duties are granted per request via headers.
func newServer(t *testing.T, s store.Store) http.Handler {
	t.Helper()
	r := httpx.NewRouter()
	New(s, Options{ReleaseHorizonDays: 2, RequireFullATP: true}).Routes(r)
	return r
}

func do(t *testing.T, h http.Handler, method, path, body string, headers map[string]string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Passage-Tenant", tenant)
	req.Header.Set("X-Passage-Duties", "WarehouseRelease,WarehouseView,WarehousePick,WarehousePack,PackingSlipPost")
	req.Header.Set("X-Passage-User", "00000000-0000-0000-0000-000000000009")
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

func TestRunRelease_ReleasesEligibleLinesAndReportsSkips(t *testing.T) {
	fs := &fakeStore{}
	h := newServer(t, fs)
	today := time.Now().UTC().Format("2006-01-02")
	far := time.Now().UTC().AddDate(0, 0, 10).Format("2006-01-02")
	line := func(qty, date string, held bool) string {
		return `{"orderId":"` + ids.New() + `","orderLineId":"` + ids.New() + `","customerId":"` + ids.New() +
			`","legalEntityId":"` + ids.New() + `","productId":"` + ids.New() + `","unit":"EA","qty":"` + qty +
			`","confirmedShipDate":"` + date + `","atpOk":true,"held":` + boolStr(held) + `}`
	}
	body := `{"warehouseId":"` + ids.New() + `","lines":[` + line("10", today, false) + `,` + line("5", far, false) + `,` + line("1", today, true) + `]}`
	rec := do(t, h, http.MethodPost, "/v1/fulfillment/releases:run", body, map[string]string{"Idempotency-Key": "rel-1"})
	if rec.Code != http.StatusCreated {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	var out releaseResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	if len(out.Shipments) != 1 || len(out.Skipped) != 2 {
		t.Fatalf("shipments = %d skipped = %d, want 1 and 2", len(out.Shipments), len(out.Skipped))
	}
	if fs.lastIdem.Key != "rel-1" || fs.lastIdem.Hash == "" {
		t.Fatalf("idempotency not passed to store: %+v", fs.lastIdem)
	}
}

func TestCommands_RequireIdempotencyKey(t *testing.T) {
	h := newServer(t, &fakeStore{})
	rec := do(t, h, http.MethodPost, "/v1/fulfillment/packing-slips/"+ids.New()+":cancel", "", nil)
	if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), "FUL-0051") {
		t.Fatalf("status = %d body = %s, want 400 FUL-0051", rec.Code, rec.Body.String())
	}
}

func TestConfirmPick_RequiresIfMatch(t *testing.T) {
	h := newServer(t, &fakeStore{})
	path := "/v1/fulfillment/shipments/" + ids.New() + ":confirm-pick"
	body := `{"picks":[{"shipmentLineId":"` + ids.New() + `","picked":"90"}]}`
	rec := do(t, h, http.MethodPost, path, body, map[string]string{"Idempotency-Key": "pick-1"})
	if rec.Code != http.StatusPreconditionRequired {
		t.Fatalf("status = %d, want 428; body %s", rec.Code, rec.Body.String())
	}
}

func TestConfirmPick_StaleVersionIsRejected(t *testing.T) {
	h := newServer(t, &fakeStore{})
	path := "/v1/fulfillment/shipments/" + ids.New() + ":confirm-pick"
	body := `{"picks":[{"shipmentLineId":"` + ids.New() + `","picked":"90"}]}`
	rec := do(t, h, http.MethodPost, path, body, map[string]string{"Idempotency-Key": "pick-2", "If-Match": `"2"`})
	if rec.Code != http.StatusPreconditionFailed || !strings.Contains(rec.Body.String(), store.CodeVersion) {
		t.Fatalf("status = %d body = %s, want 412 conflict_version", rec.Code, rec.Body.String())
	}
}

func TestConfirmPick_HappyPathReturnsETag(t *testing.T) {
	h := newServer(t, &fakeStore{})
	path := "/v1/fulfillment/shipments/" + ids.New() + ":confirm-pick"
	body := `{"picks":[{"shipmentLineId":"` + ids.New() + `","picked":"90"}]}`
	rec := do(t, h, http.MethodPost, path, body, map[string]string{"Idempotency-Key": "pick-3", "If-Match": `"3"`})
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d body = %s", rec.Code, rec.Body.String())
	}
	if rec.Header().Get("ETag") != `"4"` {
		t.Fatalf("ETag = %q, want \"4\"", rec.Header().Get("ETag"))
	}
}

func TestConfirmPick_RejectsNegativePick(t *testing.T) {
	h := newServer(t, &fakeStore{})
	path := "/v1/fulfillment/shipments/" + ids.New() + ":confirm-pick"
	body := `{"picks":[{"shipmentLineId":"` + ids.New() + `","picked":"-1"}]}`
	rec := do(t, h, http.MethodPost, path, body, map[string]string{"Idempotency-Key": "pick-4", "If-Match": `"3"`})
	if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), "picks[0].picked") {
		t.Fatalf("status = %d body = %s, want 400 naming picks[0].picked", rec.Code, rec.Body.String())
	}
}

func TestPostPackingSlip_ValidatesDateAndCurrency(t *testing.T) {
	h := newServer(t, &fakeStore{})
	path := "/v1/fulfillment/shipments/" + ids.New() + ":post-packing-slip"
	bad := `{"postingDate":"21/10/2026","currency":"USD","costs":[]}`
	rec := do(t, h, http.MethodPost, path, bad, map[string]string{"Idempotency-Key": "post-1", "If-Match": `"3"`})
	if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), "postingDate") {
		t.Fatalf("status = %d body = %s, want 400 naming postingDate", rec.Code, rec.Body.String())
	}
	good := `{"postingDate":"2026-10-21","currency":"USD","cogsAtShipment":false,"costs":[{"shipmentLineId":"` + ids.New() +
		`","unitCost":"12.50","costSnapshotId":"snap-1"}]}`
	rec = do(t, h, http.MethodPost, path, good, map[string]string{"Idempotency-Key": "post-2", "If-Match": `"3"`})
	if rec.Code != http.StatusCreated {
		t.Fatalf("status = %d body = %s", rec.Code, rec.Body.String())
	}
}

func TestGetShipment_NotFoundAndETag(t *testing.T) {
	h := newServer(t, &fakeStore{notFound: true})
	rec := do(t, h, http.MethodGet, "/v1/fulfillment/shipments/not-a-uuid", "", nil)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", rec.Code)
	}
	h = newServer(t, &fakeStore{})
	rec = do(t, h, http.MethodGet, "/v1/fulfillment/shipments/"+ids.New(), "", nil)
	if rec.Code != http.StatusOK || rec.Header().Get("ETag") != `"3"` {
		t.Fatalf("status = %d etag = %q, want 200 \"3\"", rec.Code, rec.Header().Get("ETag"))
	}
}

func TestDuty_MissingDutyIsForbidden(t *testing.T) {
	h := newServer(t, &fakeStore{})
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/v1/fulfillment/shipments/"+ids.New(), nil)
	req.Header.Set("X-Passage-Tenant", tenant)
	req.Header.Set("X-Passage-Duties", "SomethingElse")
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403", rec.Code)
	}
}

func TestMalformedBodyIsRejected(t *testing.T) {
	h := newServer(t, &fakeStore{})
	rec := do(t, h, http.MethodPost, "/v1/fulfillment/releases:run", `{"warehouseId": 5`, map[string]string{"Idempotency-Key": "r"})
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", rec.Code)
	}
}

func relLine(orderID, customerID, legalEntityID, orderLineID string) string {
	return `{"orderId":"` + orderID + `","orderLineId":"` + orderLineID + `","customerId":"` + customerID +
		`","legalEntityId":"` + legalEntityID + `","productId":"` + ids.New() + `","unit":"EA","qty":"4","confirmedShipDate":"` +
		time.Now().UTC().Format("2006-01-02") + `","atpOk":true}`
}

// Lines the store finds already released are reported next to the domain skips,
// and the warehouse reaches the store so it can serialise runs per warehouse.
func TestRunRelease_MergesStoreSkipsAndPassesWarehouse(t *testing.T) {
	already := ids.New()
	fs := &fakeStore{storeSkips: []domain.Skip{{OrderLineID: already, Reason: domain.SkipAlreadyReleased}}}
	h := newServer(t, fs)
	wh := ids.New()
	body := `{"warehouseId":"` + wh + `","lines":[` + relLine(ids.New(), ids.New(), ids.New(), already) + `]}`
	rec := do(t, h, http.MethodPost, "/v1/fulfillment/releases:run", body, map[string]string{"Idempotency-Key": "rel-merge"})
	if rec.Code != http.StatusCreated {
		t.Fatalf("status = %d body = %s", rec.Code, rec.Body.String())
	}
	var out releaseResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	if fs.warehouse != wh {
		t.Fatalf("store got warehouse %q, want %q", fs.warehouse, wh)
	}
	if len(out.Skipped) != 1 || out.Skipped[0].Reason != "already_released" || out.Skipped[0].OrderLineID != already {
		t.Fatalf("skipped = %+v, want the already_released line", out.Skipped)
	}
}

func TestRunRelease_RejectsDuplicateLinesAndMixedOrderParties(t *testing.T) {
	h := newServer(t, &fakeStore{})
	order, customer, le, line := ids.New(), ids.New(), ids.New(), ids.New()
	cases := map[string]struct{ lines, field string }{
		"duplicate order line": {relLine(order, customer, le, line) + "," + relLine(order, customer, le, line), "lines[1].orderLineId"},
		"two customers":        {relLine(order, customer, le, ids.New()) + "," + relLine(order, ids.New(), le, ids.New()), "lines[1].customerId"},
		"two legal entities":   {relLine(order, customer, le, ids.New()) + "," + relLine(order, customer, ids.New(), ids.New()), "lines[1].legalEntityId"},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			body := `{"warehouseId":"` + ids.New() + `","lines":[` + c.lines + `]}`
			rec := do(t, h, http.MethodPost, "/v1/fulfillment/releases:run", body, map[string]string{"Idempotency-Key": "rel-" + name})
			if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), c.field) || !strings.Contains(rec.Body.String(), CodeValidation) {
				t.Fatalf("status = %d body = %s, want 400 %s naming %s", rec.Code, rec.Body.String(), CodeValidation, c.field)
			}
		})
	}
}

func TestCommands_RejectRepeatedLinesAndExcessScale(t *testing.T) {
	h := newServer(t, &fakeStore{})
	line := ids.New()
	shipment := "/v1/fulfillment/shipments/" + ids.New()
	cases := map[string]struct{ path, body, field string }{
		"pick twice": {shipment + ":confirm-pick",
			`{"picks":[{"shipmentLineId":"` + line + `","picked":"1"},{"shipmentLineId":"` + line + `","picked":"2"}]}`, "picks[1].shipmentLineId"},
		"pack twice": {shipment + "/containers",
			`{"containerType":"C12","lines":[{"shipmentLineId":"` + line + `","qty":"1"},{"shipmentLineId":"` + line + `","qty":"1"}]}`, "lines[1].shipmentLineId"},
		"cost twice": {shipment + ":post-packing-slip",
			`{"postingDate":"2026-10-21","currency":"USD","costs":[{"shipmentLineId":"` + line + `","unitCost":"1","costSnapshotId":"a"},{"shipmentLineId":"` + line + `","unitCost":"1","costSnapshotId":"b"}]}`, "costs[1].shipmentLineId"},
		"seven decimals": {shipment + ":confirm-pick",
			`{"picks":[{"shipmentLineId":"` + line + `","picked":"1.0000001"}]}`, "picks[0].picked"},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			rec := do(t, h, http.MethodPost, c.path, c.body, map[string]string{"Idempotency-Key": "k-" + name, "If-Match": `"3"`})
			if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), c.field) {
				t.Fatalf("status = %d body = %s, want 400 naming %s", rec.Code, rec.Body.String(), c.field)
			}
		})
	}
}

// The idempotency fingerprint covers the method and path, so the same key on two
// different slips (both with an empty body) cannot replay the first answer.
func TestIdempotencyHashCoversThePath(t *testing.T) {
	fs := &fakeStore{}
	h := newServer(t, fs)
	do(t, h, http.MethodPost, "/v1/fulfillment/packing-slips/"+ids.New()+":cancel", "", map[string]string{"Idempotency-Key": "same"})
	first := fs.lastIdem.Hash
	do(t, h, http.MethodPost, "/v1/fulfillment/packing-slips/"+ids.New()+":cancel", "", map[string]string{"Idempotency-Key": "same"})
	if first == "" || fs.lastIdem.Hash == first {
		t.Fatalf("hashes for two slips are equal (%q); a reused key would replay the wrong slip", first)
	}
}

func TestListShipments_FiltersAndValidation(t *testing.T) {
	fs := &fakeStore{}
	h := newServer(t, fs)
	wh := ids.New()
	rec := do(t, h, http.MethodGet, "/v1/fulfillment/shipments?status=PACKED&warehouseId="+wh+"&limit=20", "", nil)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"items":[{`) {
		t.Fatalf("status = %d body = %s", rec.Code, rec.Body.String())
	}
	if fs.shipmentFilter != (store.ShipmentFilter{Status: "PACKED", WarehouseID: wh, Limit: 20}) {
		t.Fatalf("filter = %+v", fs.shipmentFilter)
	}
	for query, field := range map[string]string{"status=LOST": "status", "warehouseId=x": "warehouseId", "limit=0": "limit", "limit=501": "limit"} {
		rec := do(t, h, http.MethodGet, "/v1/fulfillment/shipments?"+query, "", nil)
		if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), `"`+field+`"`) {
			t.Fatalf("%s: status = %d body = %s, want 400 naming %s", query, rec.Code, rec.Body.String(), field)
		}
	}
}

func TestPackingSlips_ListAndGet(t *testing.T) {
	fs := &fakeStore{}
	h := newServer(t, fs)
	sh := ids.New()
	rec := do(t, h, http.MethodGet, "/v1/fulfillment/packing-slips?shipmentId="+sh, "", nil)
	if rec.Code != http.StatusOK || rec.Body.String() != "{\"items\":[]}\n" {
		t.Fatalf("status = %d body = %q, want an empty items list", rec.Code, rec.Body.String())
	}
	if fs.slipFilter.ShipmentID != sh || fs.slipFilter.Limit != 100 {
		t.Fatalf("filter = %+v", fs.slipFilter)
	}
	rec = do(t, h, http.MethodGet, "/v1/fulfillment/packing-slips/"+ids.New(), "", nil)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"journal":[{"account":"1405","amount":"125","currency":"USD"`) {
		t.Fatalf("status = %d body = %s, want the flat journal shape", rec.Code, rec.Body.String())
	}
	h = newServer(t, &fakeStore{notFound: true})
	rec = do(t, h, http.MethodGet, "/v1/fulfillment/packing-slips/"+ids.New(), "", nil)
	if rec.Code != http.StatusNotFound || !strings.Contains(rec.Body.String(), store.CodeSlipNotFound) {
		t.Fatalf("status = %d body = %s, want 404 %s", rec.Code, rec.Body.String(), store.CodeSlipNotFound)
	}
}

func boolStr(b bool) string {
	if b {
		return "true"
	}
	return "false"
}

// TestOpenAPIMatchesRoutes keeps api/openapi.yaml and the mounted routes in step:
// every route is documented with its duty, and every documented operation is mounted.
func TestOpenAPIMatchesRoutes(t *testing.T) {
	raw, err := os.ReadFile("../../api/openapi.yaml")
	if err != nil {
		t.Fatal(err)
	}
	pathRe := regexp.MustCompile(`^  (/\S+):$`)
	opRe := regexp.MustCompile(`^    (get|post|put|patch|delete):$`)
	dutyRe := regexp.MustCompile(`^      x-passage-duty: (\S+)$`)
	documented := map[string]string{}
	var path, op string
	for _, line := range strings.Split(string(raw), "\n") {
		if m := pathRe.FindStringSubmatch(line); m != nil {
			path = m[1]
		} else if m := opRe.FindStringSubmatch(line); m != nil && path != "" {
			op = strings.ToUpper(m[1]) + " " + path
			documented[op] = ""
		} else if m := dutyRe.FindStringSubmatch(line); m != nil && op != "" {
			documented[op] = m[1]
		}
	}
	r := chi.NewRouter()
	New(&fakeStore{}, Options{}).Routes(r)
	mounted := map[string]bool{}
	if err := chi.Walk(r, func(method, route string, _ http.Handler, _ ...func(http.Handler) http.Handler) error {
		mounted[method+" "+route] = true
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	for op := range mounted {
		duty, ok := documented[op]
		if !ok {
			t.Errorf("route %s is not in openapi.yaml", op)
		} else if duty == "" {
			t.Errorf("operation %s has no x-passage-duty", op)
		}
	}
	for op := range documented {
		if !mounted[op] {
			t.Errorf("openapi.yaml documents %s but no route serves it", op)
		}
	}
}

// A JOURNAL line is skipped before the store sees it; order type, priority and
// ATP quantity are validated and must agree within one order.
func TestRunRelease_OrderTypePriorityAndATPQty(t *testing.T) {
	fs := &fakeStore{}
	h := newServer(t, fs)
	order, customer, le := ids.New(), ids.New(), ids.New()
	with := func(extra string) string {
		return strings.Replace(relLine(order, customer, le, ids.New()), `"atpOk":true}`, `"atpOk":true,`+extra+`}`, 1)
	}
	post := func(key, lines string) *httptest.ResponseRecorder {
		return do(t, h, http.MethodPost, "/v1/fulfillment/releases:run", `{"warehouseId":"`+ids.New()+`","lines":[`+lines+`]}`, map[string]string{"Idempotency-Key": key})
	}
	rec := post("journal", with(`"orderType":"JOURNAL"`))
	if rec.Code != http.StatusCreated || len(fs.released) != 0 || !strings.Contains(rec.Body.String(), "journal_order") {
		t.Fatalf("journal: %d %s released=%v", rec.Code, rec.Body.String(), fs.released)
	}
	for name, c := range map[string]struct{ lines, field string }{
		"bad order type": {with(`"orderType":"QUOTE"`), "lines[0].orderType"},
		"mixed types":    {with(`"orderType":"ORDER"`) + "," + with(`"orderType":"JOURNAL"`), "lines[1].orderType"},
		"priority range": {with(`"priority":10`), "lines[0].priority"},
		"mixed priority": {with(`"priority":1`) + "," + with(`"priority":2`), "lines[1].priority"},
		"negative atp":   {with(`"atpQty":"-1"`), "lines[0].atpQty"},
		"atp scale":      {with(`"atpQty":"1.0000001"`), "lines[0].atpQty"},
	} {
		rec := post("k-"+name, c.lines)
		if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), c.field) {
			t.Fatalf("%s: %d %s, want 400 naming %s", name, rec.Code, rec.Body.String(), c.field)
		}
	}
	rec = post("partial", with(`"priority":2,"atpQty":"1","orderType":"ORDER"`))
	if rec.Code != http.StatusCreated {
		t.Fatalf("valid line: %d %s", rec.Code, rec.Body.String())
	}
}
