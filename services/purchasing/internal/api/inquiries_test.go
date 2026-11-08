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
	h.fake.backordered = []store.BackorderedLine{{OpenOrderLine: store.OpenOrderLine{Number: "PO-00000002", LineNo: 2, VendorID: vendor, Unit: "EA",
		Qty: decimal.NewFromInt(10), ReceivedQty: decimal.NewFromInt(4), OpenQty: decimal.NewFromInt(6), DeliveryDate: mustDay(t, "2027-01-02")},
		DueDate: mustDay(t, "2027-01-02"), Currency: "USD", OpenValue: decimal.NewFromInt(60)}}
	rec = h.do(call{method: "GET", path: "/v1/procurement/inquiries/backordered-lines?productId=" + product + "&cursor=2027-01-02|x&limit=7", duties: []string{dutyPurchaseOrderInquire}})
	if rec.Code != http.StatusOK || h.fake.lastInquiry.ProductID != product || h.fake.lastLimit != 7 || h.fake.lastAfter != "2027-01-02|x" ||
		!strings.Contains(rec.Body.String(), `"receivedQty":"4"`) || !strings.Contains(rec.Body.String(), `"openValue":"60"`) {
		t.Fatalf("backordered: %d %s", rec.Code, rec.Body)
	}
	h.fake.expiring = []store.ExpiringAgreement{{ID: ids.New(), Number: "PA-00000001", VendorID: vendor, Currency: "USD", Status: "EFFECTIVE",
		ValidTo: mustDay(t, "2027-01-20"), DaysLeft: 13, CommittedAmount: decimal.NewFromInt(1000), ReleasedAmount: decimal.NewFromInt(250)}}
	rec = h.do(call{method: "GET", path: "/v1/procurement/inquiries/expiring-agreements?vendorId=" + vendor + "&withinDays=45&asOf=2027-01-07&cursor=2027-01-01|x&limit=3", duties: []string{dutyPurchaseOrderInquire}})
	if rec.Code != http.StatusOK || h.fake.lastInquiry.VendorID != vendor || h.fake.lastInquiry.WithinDays == nil || *h.fake.lastInquiry.WithinDays != 45 || h.fake.lastLimit != 3 || h.fake.lastAfter != "2027-01-01|x" ||
		!strings.Contains(rec.Body.String(), `"daysLeft":13`) || !strings.Contains(rec.Body.String(), `"releasedAmount":"250"`) {
		t.Fatalf("expiring: %d %s", rec.Code, rec.Body)
	}
	// No withinDays leaves the default to the store; withinDays=0 is a real window, not the default.
	if rec = h.do(call{method: "GET", path: "/v1/procurement/inquiries/expiring-agreements", duties: []string{dutyPurchaseOrderInquire}}); rec.Code != http.StatusOK || h.fake.lastInquiry.WithinDays != nil {
		t.Fatalf("expiring, no window: %d %v", rec.Code, h.fake.lastInquiry.WithinDays)
	}
	if rec = h.do(call{method: "GET", path: "/v1/procurement/inquiries/expiring-agreements?withinDays=0", duties: []string{dutyPurchaseOrderInquire}}); rec.Code != http.StatusOK ||
		h.fake.lastInquiry.WithinDays == nil || *h.fake.lastInquiry.WithinDays != 0 {
		t.Fatalf("expiring, zero window: %d %v", rec.Code, h.fake.lastInquiry.WithinDays)
	}
	h.fake.nearLimit = []store.NearLimitLine{{AgreementID: ids.New(), Number: "PA-00000002", VendorID: vendor, Currency: "USD", LineID: ids.New(), LineNo: 2,
		CommitmentType: "TOTAL_VALUE", MaxEnforced: "SOFT", Committed: decimal.NewFromInt(500), Released: decimal.NewFromInt(520), Remaining: decimal.NewFromInt(-20),
		PctReleased: decimal.NewFromInt(104), Overrun: true}}
	rec = h.do(call{method: "GET", path: "/v1/procurement/inquiries/near-limit-agreements?vendorId=" + vendor + "&thresholdPct=95&cursor=104.00|x&limit=4", duties: []string{dutyPurchaseOrderInquire}})
	if rec.Code != http.StatusOK || h.fake.lastInquiry.VendorID != vendor || h.fake.lastInquiry.ThresholdPct == nil || *h.fake.lastInquiry.ThresholdPct != 95 || h.fake.lastLimit != 4 ||
		h.fake.lastAfter != "104.00|x" || !strings.Contains(rec.Body.String(), `"overrun":true`) || !strings.Contains(rec.Body.String(), `"remaining":"-20"`) {
		t.Fatalf("near limit: %d %s", rec.Code, rec.Body)
	}
	if rec = h.do(call{method: "GET", path: "/v1/procurement/inquiries/near-limit-agreements", duties: []string{dutyPurchaseOrderInquire}}); rec.Code != http.StatusOK || h.fake.lastInquiry.ThresholdPct != nil {
		t.Fatalf("near limit, default threshold: %d %v", rec.Code, h.fake.lastInquiry.ThresholdPct)
	}
	h.fake.changeOrders = []store.ChangeOrder{{PurchaseOrderID: ids.New(), Number: "PO-00000009", VendorID: vendor, Currency: "USD", ConfirmedVersion: 1, PendingVersion: 2,
		ChangeCount: 1, ConfirmedTotal: decimal.NewFromInt(1000), ProposedTotal: decimal.NewFromInt(1100)}}
	rec = h.do(call{method: "GET", path: "/v1/procurement/inquiries/change-orders?vendorId=" + vendor + "&cursor=2027-01-01T00:00:00Z|x&limit=2", duties: []string{dutyPurchaseOrderInquire}})
	if rec.Code != http.StatusOK || h.fake.lastInquiry.VendorID != vendor || h.fake.lastLimit != 2 || h.fake.lastAfter != "2027-01-01T00:00:00Z|x" ||
		!strings.Contains(rec.Body.String(), `"pendingVersion":2`) || !strings.Contains(rec.Body.String(), `"proposedTotal":"1100"`) {
		t.Fatalf("change orders: %d %s", rec.Code, rec.Body)
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
		"open-orders-by-vendor?vendorId=nope", "open-orders-by-vendor?asOf=yesterday", "overdue-lines?limit=0", "backordered-lines?vendorId=nope", "backordered-lines?asOf=x", "backordered-lines?limit=0",
		"expiring-agreements?withinDays=-1", "expiring-agreements?withinDays=367", "expiring-agreements?withinDays=x", "expiring-agreements?vendorId=nope", "expiring-agreements?limit=0",
		"near-limit-agreements?thresholdPct=49", "near-limit-agreements?thresholdPct=101", "near-limit-agreements?thresholdPct=x", "near-limit-agreements?thresholdPct=90.5",
		"near-limit-agreements?vendorId=nope", "near-limit-agreements?limit=0", "change-orders?vendorId=nope", "change-orders?limit=0",
		"price-history", "price-history?productId=" + product + "&from=2027-02-01&to=2027-01-01", "price-history?productId=" + product + "&from=x",
	} {
		if rec := h.do(call{method: "GET", path: "/v1/procurement/inquiries/" + q, duties: []string{dutyPurchaseOrderInquire}}); problemCode(t, rec) != codeInvalid {
			t.Fatalf("%s: %d %s", q, rec.Code, rec.Body)
		}
	}
	for _, p := range []string{"open-orders-by-vendor?", "overdue-lines?", "backordered-lines?", "expiring-agreements?", "near-limit-agreements?", "change-orders?", "price-history?productId=" + product + "&"} {
		if rec := h.do(call{method: "GET", path: "/v1/procurement/inquiries/" + p, duties: []string{dutyRequisitionCreate}}); rec.Code != http.StatusForbidden {
			t.Fatalf("%s without procurement.purchase-order.inquire: %d", p, rec.Code)
		}
		// A legal entity outside the caller's scope is refused before the store is asked.
		h.fake.lastInquiry = store.InquiryFilter{}
		rec := h.do(call{method: "GET", path: "/v1/procurement/inquiries/" + p + "legalEntityId=" + ids.New(), duties: []string{dutyPurchaseOrderInquire}})
		if rec.Code != http.StatusForbidden || h.fake.lastInquiry.LegalEntityID != "" {
			t.Fatalf("%s other legal entity: %d %s", p, rec.Code, rec.Body)
		}
	}
}
