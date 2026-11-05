package api

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/services/purchasing/internal/store"
)

// Slice 16: requisition lines of different warehouses never share a PO, and the PO line is
// delivered to the warehouse its requisition line asked for.
func TestConsolidate_SplitsByWarehouse(t *testing.T) {
	h := newHarness(t)
	vendor, site, whA, whB := ids.New(), ids.New(), ids.New(), ids.New()
	h.fake.sourcing = sourcing(t, leA, []fixtureLine{{vendor, site, "2027-03-01"}, {vendor, site, "2027-03-01"}, {vendor, site, "2027-03-02"}})
	h.fake.sourcing[0].WarehouseID, h.fake.sourcing[1].WarehouseID, h.fake.sourcing[2].WarehouseID = whA, whB, whA
	rec := h.do(call{method: "POST", path: "/v1/procurement/requisitions:consolidate", duties: []string{dutyPurchaseOrderMaintain},
		body: consolidateJSON(h.fake.sourcing, func(int) string { return "" }, ""), headers: idem("w1")})
	if rec.Code != http.StatusCreated {
		t.Fatalf("status %d: %s", rec.Code, rec.Body)
	}
	var out consolidateResponse
	_ = json.Unmarshal(rec.Body.Bytes(), &out)
	if len(out.PurchaseOrders) != 2 {
		t.Fatalf("POs = %d, want one per warehouse", len(out.PurchaseOrders))
	}
	for _, po := range out.PurchaseOrders {
		if po.SiteID != site || (po.WarehouseID != whA && po.WarehouseID != whB) {
			t.Fatalf("PO destination %s/%s", po.SiteID, po.WarehouseID)
		}
		for _, l := range po.Lines {
			if l.WarehouseID != po.WarehouseID || l.SiteID != site {
				t.Fatalf("line %d destination %s/%s differs from the PO's", l.LineNo, l.SiteID, l.WarehouseID)
			}
		}
		if po.WarehouseID == whA && len(po.Lines) != 2 {
			t.Fatalf("warehouse A PO holds %d lines, want 2", len(po.Lines))
		}
	}
}

// A line's destination: its own, else (on an edit) the stored one unless it followed the old
// header, else the header's.
func TestLineDestination(t *testing.T) {
	siteH, whH, whNew, whLine, whOld := ids.New(), ids.New(), ids.New(), ids.New(), ids.New()
	cases := []struct {
		name string
		body orderBody
		line orderLineBody
		want string
	}{
		{"new line follows header", orderBody{SiteID: siteH, WarehouseID: whH}, orderLineBody{}, whH},
		{"own warehouse wins", orderBody{WarehouseID: whH}, orderLineBody{WarehouseID: whLine}, whLine},
		{"edit keeps an override", orderBody{WarehouseID: whH, curWarehouseID: whH},
			orderLineBody{keep: &store.PurchaseOrderLine{WarehouseID: whLine}}, whLine},
		{"edit: line that followed the old header follows the new one", orderBody{WarehouseID: whNew, curWarehouseID: whOld},
			orderLineBody{keep: &store.PurchaseOrderLine{WarehouseID: whOld}}, whNew},
		{"edit: explicit value beats keep", orderBody{WarehouseID: whH, curWarehouseID: whH},
			orderLineBody{WarehouseID: whNew, keep: &store.PurchaseOrderLine{WarehouseID: whLine}}, whNew},
	}
	for _, c := range cases {
		if _, got := lineDestination(c.body, c.line); got != c.want {
			t.Errorf("%s: warehouse %s, want %s", c.name, got, c.want)
		}
	}
	if site, _ := lineDestination(orderBody{SiteID: siteH}, orderLineBody{}); site != siteH {
		t.Errorf("site = %s, want the header's", site)
	}
}

func TestListOpenOrderLines(t *testing.T) {
	h := newHarness(t)
	wh := ids.New()
	h.fake.openLines = []store.OpenOrderLine{{PurchaseOrderID: ids.New(), Number: "PO-00000007", LineNo: 2, LegalEntityID: leA, VendorID: ids.New(),
		WarehouseID: wh, Unit: "EA", Qty: decimal.NewFromInt(10), ReceivedQty: decimal.NewFromInt(4), OpenQty: decimal.NewFromInt(6), DeliveryDate: mustDay(t, "2027-03-01")}}
	path := "/v1/procurement/purchase-order-lines?warehouseId=" + wh + "&dueFrom=2027-03-01&dueTo=2027-03-31&limit=10&cursor=2027-02-01%7C" + ids.New() + "%7C1"
	rec := h.do(call{method: "GET", path: path, duties: []string{dutyPurchaseOrderInquire}})
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d: %s", rec.Code, rec.Body)
	}
	f := h.fake.lastOpenLines
	if f.WarehouseID != wh || f.DueFrom == nil || f.DueFrom.String() != "2027-03-01" || f.DueTo == nil || h.fake.lastLimit != 10 || !strings.HasPrefix(h.fake.lastAfter, "2027-02-01|") {
		t.Fatalf("filter %+v after %q limit %d", f, h.fake.lastAfter, h.fake.lastLimit)
	}
	for _, key := range []string{`"number":"PO-00000007"`, `"openQty":"6"`, `"deliveryDate":"2027-03-01"`} {
		if !strings.Contains(rec.Body.String(), key) {
			t.Fatalf("body lacks %s: %s", key, rec.Body)
		}
	}
	for _, q := range []string{"warehouseId=nope", "dueFrom=yesterday", "dueFrom=2027-03-05&dueTo=2027-03-01", "limit=0"} {
		if rec := h.do(call{method: "GET", path: "/v1/procurement/purchase-order-lines?" + q, duties: []string{dutyPurchaseOrderInquire}}); problemCode(t, rec) != codeInvalid {
			t.Fatalf("%s: %d %s", q, rec.Code, rec.Body)
		}
	}
	if rec := h.do(call{method: "GET", path: "/v1/procurement/purchase-order-lines?legalEntityId=" + ids.New(), duties: []string{dutyPurchaseOrderInquire}}); rec.Code != http.StatusForbidden {
		t.Fatalf("other legal entity: %d", rec.Code)
	}
	if rec := h.do(call{method: "GET", path: "/v1/procurement/purchase-order-lines", duties: []string{dutyRequisitionCreate}}); rec.Code != http.StatusForbidden {
		t.Fatalf("without procurement.purchase-order.inquire: %d", rec.Code)
	}
}
