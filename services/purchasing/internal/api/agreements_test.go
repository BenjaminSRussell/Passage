package api

import (
	"encoding/json"
	"errors"
	"net/http"
	"testing"

	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/services/purchasing/internal/domain"
	"github.com/BenjaminSRussell/passage/services/purchasing/internal/store"
)

func validAgreement(t *testing.T) purchaseAgreementBody {
	t.Helper()
	return purchaseAgreementBody{
		LegalEntityID: leA, VendorID: ids.New(), Currency: "USD", ValidFrom: mustDay(t, "2027-01-01"),
		Lines: []purchaseAgreementLineBody{
			{CommitmentType: domain.CommitQuantity, ProductID: ids.New(), Unit: "EA", CommittedQty: "1000", UnitPrice: "2.50"},
		},
	}
}

func TestBuildPurchaseAgreement_Validation(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*purchaseAgreementBody)
		field  string
	}{
		{"valid quantity line", func(*purchaseAgreementBody) {}, ""},
		{"bad currency", func(b *purchaseAgreementBody) { b.Currency = "usd" }, "currency"},
		{"missing vendor", func(b *purchaseAgreementBody) { b.VendorID = "" }, "vendorId"},
		{"validTo before validFrom", func(b *purchaseAgreementBody) {
			to := mustDay(t, "2026-12-31")
			b.ValidTo = &to
		}, "validTo"},
		{"status other than DRAFT or EFFECTIVE", func(b *purchaseAgreementBody) { b.Status = domain.AgreementClosed }, "status"},
		{"no lines", func(b *purchaseAgreementBody) { b.Lines = nil }, "lines"},
		{"quantity without unit", func(b *purchaseAgreementBody) { b.Lines[0].Unit = "" }, "lines[0].unit"},
		{"unknown commitment", func(b *purchaseAgreementBody) { b.Lines[0].CommitmentType = "BULK" }, "lines[0].commitmentType"},
		{"unknown enforcement", func(b *purchaseAgreementBody) { b.Lines[0].MaxEnforced = "MAYBE" }, "lines[0].maxEnforced"},
		{"discount above 100", func(b *purchaseAgreementBody) { b.Lines[0].DiscountPct = "101" }, "lines[0].discountPct"},
		{"negative price", func(b *purchaseAgreementBody) { b.Lines[0].UnitPrice = "-1" }, "lines[0].unitPrice"},
		{"not a decimal", func(b *purchaseAgreementBody) { b.Lines[0].CommittedQty = "lots" }, "lines[0].committedQty"},
		{"value line without amount", func(b *purchaseAgreementBody) {
			b.Lines[0] = purchaseAgreementLineBody{CommitmentType: domain.CommitValue, ProductID: ids.New()}
		}, "lines[0].committedAmount"},
		{"price override without a price", func(b *purchaseAgreementBody) {
			b.Lines[0].PriceOverride, b.Lines[0].UnitPrice = true, ""
		}, "lines[0].unitPrice"},
		{"price override on a category line", func(b *purchaseAgreementBody) {
			b.Lines[0] = purchaseAgreementLineBody{CommitmentType: domain.CommitCategoryValue, Category: "IT", CommittedAmount: "10", UnitPrice: "1", PriceOverride: true}
		}, "lines[0].priceOverride"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			body := validAgreement(t)
			tc.mutate(&body)
			_, err := buildPurchaseAgreement(body)
			if tc.field == "" {
				if err != nil {
					t.Fatalf("err = %v", err)
				}
				return
			}
			if got := problemField(err); got != tc.field {
				t.Fatalf("err = %v names %q, want %q", err, got, tc.field)
			}
		})
	}
}

func TestCreatePurchaseAgreement_DefaultsScopeAndDuty(t *testing.T) {
	h := newHarness(t)
	vendor, product := ids.New(), ids.New()
	body := `{"legalEntityId":"` + leA + `","vendorId":"` + vendor + `","currency":"USD","validFrom":"2027-01-01","validTo":"2027-12-31","lines":[
	 {"commitmentType":"QUANTITY","productId":"` + product + `","unit":"EA","committedQty":"1000","unitPrice":"2.50"},
	 {"commitmentType":"TOTAL_VALUE","committedAmount":"5000","maxEnforced":"SOFT"}]}`
	path := "/v1/procurement/agreements"

	if rec := h.do(call{method: "POST", path: path, body: body, duties: []string{dutyPurchaseOrderMaintain}, headers: idem("a1")}); rec.Code != http.StatusForbidden {
		t.Fatalf("without AgreementMaintain: %d", rec.Code)
	}
	if rec := h.do(call{method: "POST", path: path, body: body, duties: []string{dutyAgreementMaintain}}); problemCode(t, rec) != codeIdemRequired {
		t.Fatalf("without a key: %d %s", rec.Code, rec.Body)
	}
	if rec := h.do(call{method: "POST", path: path, body: body, scope: ids.New(), duties: []string{dutyAgreementMaintain}, headers: idem("a1")}); rec.Code != http.StatusForbidden ||
		problemCode(t, rec) != codeScope {
		t.Fatalf("legal entity outside scope: %d %s", rec.Code, rec.Body)
	}
	rec := h.do(call{method: "POST", path: path, body: body, duties: []string{dutyAgreementMaintain}, headers: idem("a1")})
	if rec.Code != http.StatusCreated || rec.Header().Get("ETag") != `"1"` {
		t.Fatalf("create: %d %s", rec.Code, rec.Body)
	}
	var out purchaseAgreementDTO
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	if out.Status != domain.AgreementEffective || out.ValidTo == nil || len(out.Lines) != 2 || out.Releases == nil {
		t.Fatalf("agreement = %+v; want EFFECTIVE with 2 lines and an empty release list", out)
	}
	if out.Lines[0].MaxEnforced != domain.EnforceHard || out.Lines[1].MaxEnforced != domain.EnforceSoft {
		t.Fatalf("enforcement = %s/%s, want HARD default and SOFT", out.Lines[0].MaxEnforced, out.Lines[1].MaxEnforced)
	}
	if out.Lines[0].Remaining != "1000" || out.Lines[0].PctReleased != "0" || out.Lines[1].Remaining != "5000" {
		t.Fatalf("release position = %+v", out.Lines)
	}
	if h.fake.lastIdem.Key != "a1" || h.fake.lastIdem.Hash != hashBody("purchase-agreement.create", mustDecode[purchaseAgreementBody](t, body)) {
		t.Fatalf("idempotency = %+v", h.fake.lastIdem)
	}
	if rec := h.do(call{method: "GET", path: path + "/" + out.ID, duties: []string{dutyAgreementMaintain}}); rec.Code != http.StatusOK ||
		rec.Header().Get("ETag") != `"1"` {
		t.Fatalf("get: %d %s", rec.Code, rec.Body)
	}
	if rec := h.do(call{method: "GET", path: path + "/" + ids.New(), duties: []string{dutyAgreementMaintain}}); problemCode(t, rec) != store.CodeAgreementNotFound {
		t.Fatalf("missing: %d %s", rec.Code, rec.Body)
	}
}

func TestPurchaseAgreementActions(t *testing.T) {
	h := newHarness(t)
	id := ids.New()
	h.fake.purchaseAgreements[id] = store.AgreementDetail{Agreement: domain.Agreement{ID: id, Status: domain.AgreementDraft, Version: 2}}
	duties := []string{dutyAgreementMaintain}
	path := "/v1/procurement/agreements/" + id
	both := func(v string) map[string]string {
		return map[string]string{"Idempotency-Key": "x" + v, "If-Match": `"` + v + `"`}
	}

	for _, tc := range []struct {
		name, path string
		headers    map[string]string
		status     int
		code       string
	}{
		{"unknown action", path + ":approve", both("2"), 404, codeUnknownAction},
		{"no action", path, both("2"), 404, codeUnknownAction},
		{"no If-Match", path + ":activate", idem("k"), 428, codeIfMatchMissing},
		{"stale version", path + ":activate", both("1"), 409, store.CodeVersionConflict},
		{"illegal transition", path + ":close", both("2"), 409, domain.CodeAgreementState},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rec := h.do(call{method: "POST", path: tc.path, duties: duties, headers: tc.headers})
			if rec.Code != tc.status || problemCode(t, rec) != tc.code {
				t.Fatalf("got %d %s, want %d %s: %s", rec.Code, problemCode(t, rec), tc.status, tc.code, rec.Body)
			}
		})
	}
	rec := h.do(call{method: "POST", path: path + ":activate", duties: duties, headers: both("2")})
	if rec.Code != http.StatusOK || rec.Header().Get("ETag") != `"3"` || !json.Valid(rec.Body.Bytes()) {
		t.Fatalf("activate: %d %s", rec.Code, rec.Body)
	}
	if h.fake.purchaseAgreements[id].Agreement.Status != domain.AgreementEffective {
		t.Fatalf("status = %s", h.fake.purchaseAgreements[id].Agreement.Status)
	}
}

func TestListPurchaseAgreements_Filters(t *testing.T) {
	h := newHarness(t)
	vendor := ids.New()
	rec := h.do(call{method: "GET", path: "/v1/procurement/agreements?status=EFFECTIVE&vendorId=" + vendor + "&limit=5", duties: []string{dutyAgreementMaintain}})
	if rec.Code != http.StatusOK || h.fake.lastAgrFilter.Status != domain.AgreementEffective || h.fake.lastAgrFilter.VendorID != vendor || h.fake.lastLimit != 5 {
		t.Fatalf("list: %d %+v", rec.Code, h.fake.lastAgrFilter)
	}
	if rec := h.do(call{method: "GET", path: "/v1/procurement/agreements?status=ACTIVE", duties: []string{dutyAgreementMaintain}}); problemCode(t, rec) != codeInvalid {
		t.Fatalf("bad status: %d %s", rec.Code, rec.Body)
	}
}

// FR-17.5: an agreement line with price_override prices the PO line ahead of
// trade agreements; without it the trade agreement lookup still applies.
func TestCreateOrder_AgreementPriceOverride(t *testing.T) {
	h := newHarness(t)
	vendor, product := ids.New(), ids.New()
	override := domain.AgreementLine{ID: ids.New(), LineNo: 1, CommitmentType: domain.CommitQuantity, ProductID: product, Unit: "EA",
		UnitPrice: decimal.RequireFromString("2.50"), DiscountPct: decimal.NewFromInt(10), PriceOverride: true}
	plain := override
	plain.ID, plain.LineNo, plain.PriceOverride = ids.New(), 2, false
	agrID := ids.New()
	h.fake.purchaseAgreements[agrID] = store.AgreementDetail{Agreement: domain.Agreement{ID: agrID, Lines: []domain.AgreementLine{override, plain}}}

	order := func(agreementLine, extra string) string {
		return `{"legalEntityId":"` + leA + `","vendorId":"` + vendor + `","currency":"USD","lines":[{"productId":"` + product +
			`","qty":"100","unit":"EA","deliveryDate":"2027-03-01","agreementLineId":"` + agreementLine + `"` + extra + `}]}`
	}
	rec := h.do(call{method: "POST", path: "/v1/procurement/purchase-orders", duties: []string{dutyPurchaseOrderMaintain}, body: order(override.ID, ""), headers: idem("o1")})
	if rec.Code != http.StatusCreated {
		t.Fatalf("status %d: %s", rec.Code, rec.Body)
	}
	var po store.PurchaseOrder
	_ = json.Unmarshal(rec.Body.Bytes(), &po)
	l := po.Lines[0]
	if l.PriceSource != "AGREEMENT:"+override.ID || !l.UnitPrice.Equal(decimal.RequireFromString("2.5")) || !l.DiscountPct.Equal(decimal.NewFromInt(10)) ||
		!l.LineAmount.Equal(decimal.NewFromInt(225)) || l.AgreementLineID != override.ID {
		t.Fatalf("line = %+v; want the agreement price 2.50 less 10 %% = 225", l)
	}
	rec = h.do(call{method: "POST", path: "/v1/procurement/purchase-orders", duties: []string{dutyPurchaseOrderMaintain}, body: order(override.ID, `,"discountPct":"0"`), headers: idem("o2")})
	_ = json.Unmarshal(rec.Body.Bytes(), &po)
	if !po.Lines[0].DiscountPct.IsZero() {
		t.Fatalf("an explicit discount must win over the agreement's: %+v", po.Lines[0])
	}
	if rec := h.do(call{method: "POST", path: "/v1/procurement/purchase-orders", duties: []string{dutyPurchaseOrderMaintain}, body: order(plain.ID, ""), headers: idem("o3")}); problemCode(t, rec) != codeNoPrice {
		t.Fatalf("no override and no trade agreement: %d %s", rec.Code, rec.Body)
	}
	if rec := h.do(call{method: "POST", path: "/v1/procurement/purchase-orders", duties: []string{dutyPurchaseOrderMaintain}, body: order(ids.New(), ""), headers: idem("o4")}); problemCode(t, rec) != store.CodeAgreementNotFound {
		t.Fatalf("unknown agreement line: %d %s", rec.Code, rec.Body)
	}
}

// problemField returns the field a validation problem names.
func problemField(err error) string {
	var p *httpx.Problem
	if !errors.As(err, &p) {
		return ""
	}
	for k := range p.Errors {
		return k
	}
	return ""
}

func mustDecode[T any](t *testing.T, raw string) T {
	t.Helper()
	var v T
	if err := json.Unmarshal([]byte(raw), &v); err != nil {
		t.Fatal(err)
	}
	return v
}
