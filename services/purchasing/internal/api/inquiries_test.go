package api

import (
	"net/http"
	"strings"
	"testing"

	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/services/purchasing/internal/store"
)

func TestInquiries_Read(t *testing.T) {
	h := newHarness(t)
	vendor, product := ids.New(), ids.New()
	h.fake.vendorOpen = []store.VendorOpenOrders{{VendorID: vendor, Currency: "USD", OrderCount: 2, LineCount: 3,
		OpenValue: decimal.NewFromInt(450), OverdueLines: 1, OverdueValue: decimal.NewFromInt(100), EarliestDue: mustDay(t, "2027-01-02")}}
	h.fake.overdue = []store.OverdueLine{{OpenOrderLine: store.OpenOrderLine{Number: "PO-00000001", LineNo: 1, VendorID: vendor, Unit: "EA",
		DeliveryDate: mustDay(t, "2026-12-20")}, DueDate: mustDay(t, "2027-01-02"), Currency: "USD", DaysLate: 5}}
	h.fake.priceHist = []store.PriceHistoryEntry{{Number: "PO-00000001", LineNo: 1, VendorID: vendor, Currency: "USD", Unit: "EA",
		UnitPrice: decimal.RequireFromString("9.5"), PriceSource: "TRADE_AGREEMENT", OrderDate: mustDay(t, "2027-01-01")}}

	rec := h.do(call{method: "GET", path: "/v1/procurement/inquiries/open-orders-by-vendor?vendorId=" + vendor + "&asOf=2027-01-07&limit=5", duties: []string{dutyPurchaseOrderInquire}})
	if rec.Code != http.StatusOK || h.fake.lastInquiry.VendorID != vendor || h.fake.lastInquiry.AsOf.Format("2006-01-02") != "2027-01-07" || h.fake.lastLimit != 5 {
		t.Fatalf("by vendor: %d %s filter %+v", rec.Code, rec.Body, h.fake.lastInquiry)
	}
	for _, key := range []string{`"openValue":"450"`, `"overdueLines":1`, `"earliestDue":"2027-01-02"`} {
		if !strings.Contains(rec.Body.String(), key) {
			t.Fatalf("body lacks %s: %s", key, rec.Body)
		}
	}
	rec = h.do(call{method: "GET", path: "/v1/procurement/inquiries/overdue-lines?productId=" + product, duties: []string{dutyPurchaseOrderInquire}})
	if rec.Code != http.StatusOK || h.fake.lastInquiry.ProductID != product || !strings.Contains(rec.Body.String(), `"daysLate":5`) ||
		!strings.Contains(rec.Body.String(), `"dueDate":"2027-01-02"`) || !strings.Contains(rec.Body.String(), `"deliveryDate":"2026-12-20"`) {
		t.Fatalf("overdue: %d %s", rec.Code, rec.Body)
	}
	rec = h.do(call{method: "GET", path: "/v1/procurement/inquiries/price-history?productId=" + product + "&from=2027-01-01&to=2027-02-01", duties: []string{dutyPurchaseOrderInquire}})
	if rec.Code != http.StatusOK || h.fake.lastInquiry.From == nil || h.fake.lastInquiry.To == nil || !strings.Contains(rec.Body.String(), `"unitPrice":"9.5"`) {
		t.Fatalf("price history: %d %s", rec.Code, rec.Body)
	}
}

func TestInquiries_Rejects(t *testing.T) {
	h := newHarness(t)
	product := ids.New()
	for _, q := range []string{
		"open-orders-by-vendor?vendorId=nope", "open-orders-by-vendor?asOf=yesterday", "overdue-lines?limit=0",
		"price-history", "price-history?productId=" + product + "&from=2027-02-01&to=2027-01-01", "price-history?productId=" + product + "&from=x",
	} {
		if rec := h.do(call{method: "GET", path: "/v1/procurement/inquiries/" + q, duties: []string{dutyPurchaseOrderInquire}}); problemCode(t, rec) != codeInvalid {
			t.Fatalf("%s: %d %s", q, rec.Code, rec.Body)
		}
	}
	for _, p := range []string{"open-orders-by-vendor?", "overdue-lines?", "price-history?productId=" + product + "&"} {
		if rec := h.do(call{method: "GET", path: "/v1/procurement/inquiries/" + p, duties: []string{dutyRequisitionCreate}}); rec.Code != http.StatusForbidden {
			t.Fatalf("%s without PurchaseOrderInquire: %d", p, rec.Code)
		}
		// A legal entity outside the caller's scope is refused before the store is asked.
		h.fake.lastInquiry = store.InquiryFilter{}
		rec := h.do(call{method: "GET", path: "/v1/procurement/inquiries/" + p + "legalEntityId=" + ids.New(), duties: []string{dutyPurchaseOrderInquire}})
		if rec.Code != http.StatusForbidden || h.fake.lastInquiry.LegalEntityID != "" {
			t.Fatalf("%s other legal entity: %d %s", p, rec.Code, rec.Body)
		}
	}
}
