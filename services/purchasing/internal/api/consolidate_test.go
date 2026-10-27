package api

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/services/purchasing/internal/domain"
	"github.com/BenjaminSRussell/passage/services/purchasing/internal/store"
)

type fixtureLine struct {
	vendor, site, date string
}

// sourcing builds approved requisition lines (2 EA at an estimate of 10.00
// each) whose preferred vendor is the fixture vendor.
func sourcing(t *testing.T, le string, rows []fixtureLine) []store.SourcingLine {
	t.Helper()
	var out []store.SourcingLine
	for i, r := range rows {
		out = append(out, store.SourcingLine{
			RequisitionLine: store.RequisitionLine{
				ID: ids.New(), LineNo: i + 1, ProductID: ids.New(), Description: "widget", Qty: decimal.NewFromInt(2), Unit: "EA",
				EstimatedUnitPrice: decimal.RequireFromString("10.00"), SiteID: r.site, PreferredVendorID: r.vendor,
				RequestedDate: mustDay(t, r.date), Status: "OPEN",
			},
			RequisitionID: ids.New(), RequisitionNumber: "PR-00000001", RequisitionStatus: store.RequisitionApproved,
			LegalEntityID: le, Currency: "USD",
		})
	}
	return out
}

func consolidateJSON(lines []store.SourcingLine, extra func(i int) string, window string) string {
	parts := make([]string, 0, len(lines))
	for i, l := range lines {
		parts = append(parts, `{"requisitionLineId":"`+l.ID+`"`+extra(i)+`}`)
	}
	return `{` + window + `"lines":[` + strings.Join(parts, ",") + `]}`
}

func none(int) string { return "" }

// AC-17.9: 10 approved lines across 2 vendors and 2 sites with the default
// window become 4 POs, and every PO line keeps its requisition line.
func TestConsolidate_AC179TenLinesMakeFourPOs(t *testing.T) {
	h := newHarness(t)
	v1, v2, s1, s2 := ids.New(), ids.New(), ids.New(), ids.New()
	h.fake.sourcing = sourcing(t, leA, []fixtureLine{
		{v1, s1, "2027-03-01"}, {v1, s1, "2027-03-03"},
		{v1, s2, "2027-03-02"}, {v1, s2, "2027-03-04"}, {v1, s2, "2027-03-05"},
		{v2, s1, "2027-03-01"}, {v2, s1, "2027-03-06"}, {v2, s1, "2027-03-07"},
		{v2, s2, "2027-03-02"}, {v2, s2, "2027-03-03"},
	})
	body := consolidateJSON(h.fake.sourcing, none, "")
	rec := h.do(call{method: "POST", path: "/v1/procurement/requisitions:consolidate", duties: []string{dutyPurchaseOrderMaintain}, body: body, headers: idem("c1")})
	if rec.Code != http.StatusCreated {
		t.Fatalf("status %d: %s", rec.Code, rec.Body)
	}
	var out consolidateResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	if len(out.PurchaseOrders) != 4 || len(h.fake.lastConsolidated) != 10 || h.fake.lastIdem.Key != "c1" {
		t.Fatalf("POs = %d, lines passed = %d, idem %+v", len(out.PurchaseOrders), len(h.fake.lastConsolidated), h.fake.lastIdem)
	}
	linked := map[string]bool{}
	for _, po := range out.PurchaseOrders {
		if po.LegalEntityID != leA || po.Currency != "USD" || !po.ChangeManagement {
			t.Fatalf("PO header = %+v", po)
		}
		sum := decimal.Zero
		for i, l := range po.Lines {
			if l.LineNo != i+1 || l.PriceSource != "REQUISITION_ESTIMATE" || !l.LineAmount.Equal(decimal.NewFromInt(20)) {
				t.Fatalf("line = %+v; want numbered lines at the 10.00 estimate", l)
			}
			linked[l.RequisitionLineID] = true
			sum = sum.Add(l.LineAmount)
		}
		if !po.Subtotal.Equal(sum) || !po.Total.Equal(sum) {
			t.Fatalf("PO total %s subtotal %s, lines %s", po.Total, po.Subtotal, sum)
		}
	}
	if len(linked) != 10 {
		t.Fatalf("linked requisition lines = %d, want 10", len(linked))
	}
}

func TestConsolidate_VendorOverrideTradeAgreementPriceAndWindow(t *testing.T) {
	h := newHarness(t)
	preferred, chosen, site := ids.New(), ids.New(), ids.New()
	h.fake.sourcing = sourcing(t, leA, []fixtureLine{{preferred, site, "2027-03-01"}, {preferred, site, "2027-03-20"}})
	product := h.fake.sourcing[0].ProductID
	h.fake.agreements = []domain.TradeAgreement{{ID: "ta-chosen", Relation: domain.RelationPrice, AccountCode: domain.AccountVendor, AccountID: chosen,
		ItemCode: domain.ItemProduct, ItemID: product, Currency: "USD", Unit: "EA", FromQty: decimal.Zero, Price: decimal.RequireFromString("7.5"),
		PriceUnit: decimal.NewFromInt(1), ValidFrom: mustDay(t, "2026-01-01"), Status: domain.StatusActive}}
	override := func(i int) string {
		if i == 0 {
			return `,"vendorId":"` + chosen + `"`
		}
		return ""
	}
	rec := h.do(call{method: "POST", path: "/v1/procurement/requisitions:consolidate", duties: []string{dutyPurchaseOrderMaintain},
		body: consolidateJSON(h.fake.sourcing, override, `"windowDays":30,`), headers: idem("c2")})
	if rec.Code != http.StatusCreated {
		t.Fatalf("status %d: %s", rec.Code, rec.Body)
	}
	var out consolidateResponse
	_ = json.Unmarshal(rec.Body.Bytes(), &out)
	if len(out.PurchaseOrders) != 2 {
		t.Fatalf("POs = %d, want one per vendor", len(out.PurchaseOrders))
	}
	byVendor := map[string]store.PurchaseOrder{}
	for _, po := range out.PurchaseOrders {
		byVendor[po.VendorID] = po
	}
	if l := byVendor[chosen].Lines[0]; l.PriceSource != "TRADE_AGREEMENT:ta-chosen" || !l.UnitPrice.Equal(decimal.RequireFromString("7.5")) {
		t.Fatalf("overridden line = %+v; want the chosen vendor's trade agreement price", l)
	}
	if po := byVendor[preferred]; len(po.Lines) != 1 || po.Lines[0].PriceSource != "REQUISITION_ESTIMATE" || po.SiteID != site {
		t.Fatalf("preferred-vendor PO = %+v", po)
	}
}

func TestConsolidate_Rejections(t *testing.T) {
	v, s := ids.New(), ids.New()
	tests := []struct {
		name   string
		body   func(lines []store.SourcingLine) string
		scope  string
		status int
		code   string
	}{
		{"no lines", func([]store.SourcingLine) string { return `{"lines":[]}` }, "", 422, codeInvalid},
		{"duplicate line", func(l []store.SourcingLine) string { return consolidateJSON(append(l, l[0]), none, "") }, "", 422, codeInvalid},
		{"bad vendor id", func(l []store.SourcingLine) string {
			return consolidateJSON(l, func(int) string { return `,"vendorId":"nope"` }, "")
		}, "", 422, codeInvalid},
		{"unknown line", func([]store.SourcingLine) string { return `{"lines":[{"requisitionLineId":"` + ids.New() + `"}]}` }, "", 404, store.CodeRequisitionLineNotFound},
		{"window out of range", func(l []store.SourcingLine) string { return consolidateJSON(l[:1], none, `"windowDays":61,`) }, "", 422, domain.CodeConsolidationWindow},
		{"needs sourcing", func(l []store.SourcingLine) string { return consolidateJSON(l[1:], none, "") }, "", 422, codeNeedsSourcing},
		{"legal entity outside scope", func(l []store.SourcingLine) string { return consolidateJSON(l[:1], none, "") }, ids.New(), 403, codeScope},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			h := newHarness(t)
			h.fake.sourcing = sourcing(t, leA, []fixtureLine{{v, s, "2027-03-01"}, {"", s, "2027-03-01"}})
			rec := h.do(call{method: "POST", path: "/v1/procurement/requisitions:consolidate", scope: tc.scope, duties: []string{dutyPurchaseOrderMaintain},
				body: tc.body(h.fake.sourcing), headers: idem("k")})
			if rec.Code != tc.status || problemCode(t, rec) != tc.code {
				t.Fatalf("got %d %s, want %d %s: %s", rec.Code, problemCode(t, rec), tc.status, tc.code, rec.Body)
			}
			if h.fake.lastConsolidated != nil {
				t.Fatal("a rejected consolidation reached the store")
			}
		})
	}
	h := newHarness(t)
	if rec := h.do(call{method: "POST", path: "/v1/procurement/requisitions:consolidate", duties: []string{dutyPurchaseOrderInquire}, body: `{"lines":[]}`, headers: idem("k")}); rec.Code != http.StatusForbidden {
		t.Fatalf("without PurchaseOrderMaintain: %d", rec.Code)
	}
}

func TestListOpenRequisitionLines(t *testing.T) {
	h := newHarness(t)
	h.fake.sourcing = sourcing(t, leA, []fixtureLine{{"", "", "2027-03-01"}})
	rec := h.do(call{method: "GET", path: "/v1/procurement/requisition-lines?sourcing=NEEDS_SOURCING&limit=20", duties: []string{dutyPurchaseOrderMaintain}})
	if rec.Code != http.StatusOK || h.fake.lastSourcing.Sourcing != store.SourcingNeedsSourcing || h.fake.lastLimit != 20 {
		t.Fatalf("list: %d %+v", rec.Code, h.fake.lastSourcing)
	}
	for _, key := range []string{`"requisitionNumber":"PR-00000001"`, `"legalEntityId":"` + leA + `"`, `"estimatedUnitPrice":"10"`} {
		if !strings.Contains(rec.Body.String(), key) {
			t.Fatalf("body lacks %s: %s", key, rec.Body)
		}
	}
	for _, q := range []string{"sourcing=ALL", "vendorId=nope"} {
		if rec := h.do(call{method: "GET", path: "/v1/procurement/requisition-lines?" + q, duties: []string{dutyPurchaseOrderMaintain}}); problemCode(t, rec) != codeInvalid {
			t.Fatalf("%s: %d %s", q, rec.Code, rec.Body)
		}
	}
}
