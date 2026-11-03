package api

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/services/planning/internal/domain"
	"github.com/BenjaminSRussell/passage/services/planning/internal/store"
)

const wh2ID = "00000000-0000-0000-0000-0000000000c2"

// warehouseBody is a site item with 100 on hand and one warehouse scope WH-2 with 3
// on hand and 8 demanded on 2026-11-12 (10 days after asOf); the scope leaves coverage,
// order type and lead time out, so they come from the stored warehouse coverage.
const warehouseBody = `{"asOf":"2026-11-02","siteId":"` + siteID + `","items":[
 {"itemId":"` + fgID + `","coverage":{"code":"requirement"},"onHand":"100","orderType":"purchase","leadTimeDays":3,"lot":{},
  "demand":[{"ref":"SO-1","refType":"sales-line","date":"2026-11-06","qty":"5"}],"receipts":[],"components":[],
  "warehouses":[{"warehouseId":"` + wh2ID + `","onHand":"3","lot":{},
   "demand":[{"ref":"SO-2","refType":"sales-line","date":"2026-11-12","qty":"8"}]}]}]}`

func transferCoverage() store.RunCoverage {
	lead := 2
	return store.RunCoverage{ItemID: fgID, GroupCode: "REQ", CoverageCode: "requirement", MinQty: decimal.Zero, MaxQty: decimal.Zero,
		LeadTimeDays: &lead, PlannedOrderType: "transfer"}
}

// AC-22.16 through the API: WH-2 demand is covered by a planned transfer from the
// site, dated the lead time before delivery, and WH-2 on-hand is its own.
func TestRun_WarehouseScopePlansTransfer(t *testing.T) {
	st := &fakeStore{runWhCovs: map[string]store.RunCoverage{domain.WarehouseKey(fgID, wh2ID): transferCoverage()}}
	_, req := runVersion(t, st, warehouseBody, "k-wh")
	o := onlyOrder(t, req)
	if o.WarehouseID != wh2ID || o.ItemID != fgID || o.OrderType != domain.OrderTransfer || !o.Qty.Equal(decimal.NewFromInt(5)) ||
		o.DeliveryDate != 10 || o.OrderDate != 8 {
		t.Fatalf("planned order %+v; want a transfer of 5 to WH-2, ordered day 8, delivered day 10", o)
	}
	sawWarehouseNet := false
	for _, n := range req.Result.Net {
		if n.ItemID != fgID {
			t.Fatalf("net row %+v is not keyed by the item", n)
		}
		sawWarehouseNet = sawWarehouseNet || n.WarehouseID == wh2ID
	}
	if !sawWarehouseNet {
		t.Fatalf("no net requirement row for WH-2: %+v", req.Result.Net)
	}
}

func TestRun_WarehouseScopeInlineWinsOverStored(t *testing.T) {
	body := strings.Replace(warehouseBody, `"onHand":"3","lot":{}`, `"onHand":"3","orderType":"purchase","leadTimeDays":4,"lot":{}`, 1)
	st := &fakeStore{runWhCovs: map[string]store.RunCoverage{domain.WarehouseKey(fgID, wh2ID): transferCoverage()}}
	_, req := runVersion(t, st, body, "k-wh-inline")
	o := onlyOrder(t, req)
	if o.OrderType != domain.OrderPurchase || o.DeliveryDate-o.OrderDate != 4 || o.WarehouseID != wh2ID {
		t.Fatalf("planned order %+v; want a direct purchase with the inline lead time of 4", o)
	}
}

func TestRun_WarehouseScopeNeedsCoverage(t *testing.T) {
	rec := do(t, &fakeStore{}, http.MethodPost, "/v1/planning/plans/MAIN:run", warehouseBody, "planning.run", true, "k-wh-bare")
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status %d body %s", rec.Code, rec.Body.String())
	}
	for _, field := range []string{"items[0].warehouses[0].coverage.code", "items[0].warehouses[0].orderType"} {
		if !strings.Contains(rec.Body.String(), field) {
			t.Fatalf("no field error %s in %s", field, rec.Body.String())
		}
	}
}

func TestRun_WarehouseScopeValidation(t *testing.T) {
	one := `{"warehouseId":"` + wh2ID + `","onHand":"3","coverage":{"code":"requirement"},"orderType":"purchase","lot":{}}`
	cases := map[string]string{
		"items[0].warehouses[1].warehouseId": strings.Replace(warehouseBody, `"warehouses":[`, `"warehouses":[`+one+`,`, 1),
		"items[0].warehouses[0].warehouseId": strings.Replace(warehouseBody, `"warehouseId":"`+wh2ID+`"`, `"warehouseId":"WH-2"`, 1),
		"items[0].warehouses[0].quantities":  strings.Replace(warehouseBody, `"onHand":"3"`, `"onHand":"-3"`, 1),
	}
	for field, body := range cases {
		rec := do(t, &fakeStore{}, http.MethodPost, "/v1/planning/plans/MAIN:run", body, "planning.run", true, "k-"+field)
		if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), field) {
			t.Fatalf("%s: status %d body %s", field, rec.Code, rec.Body.String())
		}
	}
}

func TestNetRequirements_WarehouseFilter(t *testing.T) {
	st := &fakeStore{}
	rec := do(t, st, http.MethodGet, "/v1/planning/net-requirements?planCode=MAIN&itemId="+fgID+"&warehouseId="+wh2ID, "", "planning.read", true, "")
	if rec.Code != http.StatusOK || st.lastWarehouse != wh2ID {
		t.Fatalf("status %d warehouse %q body %s", rec.Code, st.lastWarehouse, rec.Body.String())
	}
	rec = do(t, st, http.MethodGet, "/v1/planning/net-requirements?planCode=MAIN&itemId="+fgID+"&warehouseId=nope", "", "planning.read", true, "")
	var p map[string]any
	if rec.Code != http.StatusBadRequest || json.Unmarshal(rec.Body.Bytes(), &p) != nil {
		t.Fatalf("status %d body %s", rec.Code, rec.Body.String())
	}
}

// Quota arrangements apply to the site scope only: the store is asked for item ids
// (it casts them to uuid), never for a warehouse scope's engine id, and the planned
// transfer keeps no quota source while the site purchase it causes is allocated.
func TestRun_WarehouseScopeLeavesQuotaToTheSite(t *testing.T) {
	body := strings.Replace(warehouseBody, `"onHand":"100"`, `"onHand":"0"`, 1)
	st := &fakeStore{runWhCovs: map[string]store.RunCoverage{domain.WarehouseKey(fgID, wh2ID): transferCoverage()},
		runQuotas: []domain.RunQuota{purchaseQuota(false)}}
	_, req := runVersion(t, st, body, "k-wh-quota")
	if len(st.quotaItems) == 0 {
		t.Fatal("the run did not read quota arrangements")
	}
	for _, id := range st.quotaItems {
		if !ids.Valid(id) {
			t.Fatalf("quota read for %q, not an item id", id)
		}
	}
	var transfer, purchase bool
	for _, o := range req.Result.Orders {
		switch {
		case o.WarehouseID == wh2ID:
			transfer = o.OrderType == domain.OrderTransfer && o.QuotaSourceID == ""
		case o.OrderType == domain.OrderPurchase:
			purchase = purchase || o.QuotaSourceID != ""
		}
	}
	if !transfer || !purchase {
		t.Fatalf("want a transfer without a source and an allocated site purchase: %+v", req.Result.Orders)
	}
}

// A firmed transfer carried forward is a receipt of its warehouse and, until its
// order date, an issue from the site: the site's on-hand still holds the stock it
// will ship, so it does not also cover the site's own demand. A transfer whose order
// date has passed has shipped and is only the warehouse's receipt.
func TestRun_CarryForwardFirmedTransferIssuesFromTheSite(t *testing.T) {
	body := strings.Replace(strings.Replace(warehouseBody, `"items"`, `"carryForward":true,"items"`, 1), `"onHand":"100"`, `"onHand":"5"`, 1)
	transfer := func(orderDate string) []store.FirmedSupply {
		od, _ := time.Parse(dateLayout, orderDate)
		return []store.FirmedSupply{{OrderID: "00000000-0000-0000-0000-0000000000f3", ItemID: fgID, WarehouseID: wh2ID, OrderType: domain.OrderTransfer,
			Qty: decimal.NewFromInt(5), OrderDate: od, DeliveryDate: time.Date(2026, 11, 12, 0, 0, 0, 0, time.UTC)}}
	}
	st := &fakeStore{runWhCovs: map[string]store.RunCoverage{domain.WarehouseKey(fgID, wh2ID): transferCoverage()}, firmedSupply: transfer("2026-11-10")}
	_, req := runVersion(t, st, body, "k-wh-carry")
	o := onlyOrder(t, req)
	if o.WarehouseID != "" || o.OrderType != domain.OrderPurchase || !o.Qty.Equal(decimal.NewFromInt(5)) || o.DeliveryDate != 8 {
		t.Fatalf("planned order %+v; want the site to buy the 5 it ships on day 8, and no second transfer", o)
	}

	st = &fakeStore{runWhCovs: map[string]store.RunCoverage{domain.WarehouseKey(fgID, wh2ID): transferCoverage()}, firmedSupply: transfer("2026-11-01")}
	_, req = runVersion(t, st, body, "k-wh-carry-shipped")
	if len(req.Result.Orders) != 0 {
		t.Fatalf("orders = %+v; a shipped transfer is only the warehouse's receipt", req.Result.Orders)
	}
}

// siteOnlyBody is a site item with 100 on hand and no warehouse scope of its own.
const siteOnlyBody = `{"asOf":"2026-11-02","siteId":"` + siteID + `","items":[
 {"itemId":"` + fgID + `","coverage":{"code":"requirement"},"onHand":"100","orderType":"purchase","leadTimeDays":3,"lot":{},
  "demand":[],"receipts":[],"components":[]}]}`

func storedWarehouseScope() map[string]store.RunCoverage {
	c := transferCoverage()
	c.WarehouseID = wh2ID
	c.CoverageCode = domain.CoverageMinMax
	c.MinQty, c.MaxQty = decimal.NewFromInt(10), decimal.NewFromInt(40)
	return map[string]store.RunCoverage{domain.WarehouseKey(fgID, wh2ID): c}
}

// FR-22.18: with warehouseScopesFromCoverage a warehouse scope that exists only as
// stored item coverage is planned too (here a transfer up to its maximum of 40, since the scope is empty below its minimum of 10).
func TestRun_WarehouseScopeDiscoveredFromStoredCoverage(t *testing.T) {
	body := strings.Replace(siteOnlyBody, `{"asOf"`, `{"warehouseScopesFromCoverage":true,"asOf"`, 1)
	_, req := runVersion(t, &fakeStore{runWhCovs: storedWarehouseScope()}, body, "k-wh-disc")
	o := onlyOrder(t, req)
	if o.WarehouseID != wh2ID || o.OrderType != domain.OrderTransfer || !o.Qty.Equal(decimal.NewFromInt(40)) {
		t.Fatalf("planned order %+v; want a transfer of 40 to the stored scope WH-2", o)
	}
}

// Without the switch the stored scope is not planned (the default is unchanged).
func TestRun_WarehouseScopeFromCoverageIsOptIn(t *testing.T) {
	_, req := runVersion(t, &fakeStore{runWhCovs: storedWarehouseScope()}, siteOnlyBody, "k-wh-off")
	if len(req.Result.Orders) != 0 {
		t.Fatalf("orders %+v; want none without the switch", req.Result.Orders)
	}
}

// A scope named inline wins over the same stored scope, which is not added twice.
func TestRun_WarehouseScopeInlineIsNotDuplicatedByDiscovery(t *testing.T) {
	body := strings.Replace(warehouseBody, `{"asOf"`, `{"warehouseScopesFromCoverage":true,"asOf"`, 1)
	_, req := runVersion(t, &fakeStore{runWhCovs: storedWarehouseScope()}, body, "k-wh-dup")
	o := onlyOrder(t, req)
	if o.WarehouseID != wh2ID || o.OrderType != domain.OrderTransfer || !o.Qty.Equal(decimal.NewFromInt(37)) {
		t.Fatalf("planned order %+v; want the inline scope's single min/max transfer of 37 (40 less its 3 on hand), not a second scope", o)
	}
}

// A discovered requirement-coverage scope has no demand of its own, so it plans no
// order and adds no issue demand at the site: the site's 100 on hand stays its own and
// nothing is planned or counted twice.
func TestRun_WarehouseScopeDiscoveredWithoutDemandPlansNothing(t *testing.T) {
	body := strings.Replace(siteOnlyBody, `{"asOf"`, `{"warehouseScopesFromCoverage":true,"asOf"`, 1)
	body = strings.Replace(body, `"demand":[]`, `"demand":[{"ref":"SO-1","refType":"sales-line","date":"2026-11-06","qty":"120"}]`, 1)
	c := transferCoverage()
	c.WarehouseID = wh2ID
	st := &fakeStore{runWhCovs: map[string]store.RunCoverage{domain.WarehouseKey(fgID, wh2ID): c}}
	_, req := runVersion(t, st, body, "k-wh-disc-req")
	o := onlyOrder(t, req)
	if o.WarehouseID != "" || o.OrderType != domain.OrderPurchase || !o.Qty.Equal(decimal.NewFromInt(20)) {
		t.Fatalf("planned order %+v; want only the site's purchase of 20 (120 demanded less 100 on hand)", o)
	}
	for _, n := range req.Result.Net {
		if n.WarehouseID != "" {
			t.Fatalf("net row %+v; an empty requirement scope has no activity", n)
		}
	}
}

// Discovery never truncates: an item whose stored scopes take it past the per-item
// limit is a 400 naming the item, not a run that silently leaves scopes out.
func TestRun_WarehouseScopesFromCoverageOverTheLimitIs400(t *testing.T) {
	covs := map[string]store.RunCoverage{}
	for i := 0; i <= maxWarehouseScopes; i++ {
		c := transferCoverage()
		c.WarehouseID = fmt.Sprintf("00000000-0000-0000-0000-%012x", 0xd00+i)
		covs[domain.WarehouseKey(fgID, c.WarehouseID)] = c
	}
	body := strings.Replace(siteOnlyBody, `{"asOf"`, `{"warehouseScopesFromCoverage":true,"asOf"`, 1)
	rec := do(t, &fakeStore{runWhCovs: covs}, http.MethodPost, "/v1/planning/plans/MAIN:run", body, "planning.run", true, "k-wh-cap")
	if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), "items[0].warehouses") {
		t.Fatalf("status %d body %s; want a 400 on items[0].warehouses", rec.Code, rec.Body.String())
	}
}
