package api

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/services/purchasing/internal/domain"
	"github.com/BenjaminSRussell/passage/services/purchasing/internal/store"
)

func releaseCall(h *harness, body, key string, duties ...string) (code int, run store.ReleaseRun, raw string) {
	if len(duties) == 0 {
		duties = []string{dutyPurchaseOrderMaintain}
	}
	rec := h.do(call{method: "POST", path: "/v1/procurement/requisition-releases", duties: duties, body: body, headers: idem(key)})
	_ = json.Unmarshal(rec.Body.Bytes(), &run)
	return rec.Code, run, rec.Body.String()
}

// Lines with a vendor are grouped by vendor and sent to the store as priced draft POs; a line with no vendor is
// listed as skipped and never reaches it.
func TestReleaseRun_GroupsByVendorAndSkipsLinesWithoutVendor(t *testing.T) {
	h := newHarness(t)
	v1, v2, site := ids.New(), ids.New(), ids.New()
	h.fake.sourcing = sourcing(t, leA, []fixtureLine{{v1, site, "2027-03-01"}, {v1, site, "2027-03-02"}, {v2, site, "2027-03-01"}, {"", site, "2027-03-01"}})
	code, run, raw := releaseCall(h, `{"legalEntityId":"`+leA+`"}`, "r1")
	if code != http.StatusCreated {
		t.Fatalf("status %d: %s", code, raw)
	}
	in := h.fake.release.in
	if in.LegalEntityID != leA || in.WindowDays != 7 || in.MaxLines != 200 || in.Considered != 4 {
		t.Fatalf("input = %+v", in)
	}
	if len(in.Groups) != 2 || in.Groups[0].VendorID > in.Groups[1].VendorID {
		t.Fatalf("groups = %+v; want one per vendor in vendor id order", in.Groups)
	}
	for _, g := range in.Groups {
		want := 1
		if g.VendorID == v1 {
			want = 2
		}
		if len(g.Lines) != want || len(g.Orders) != 1 || len(g.Orders[0].Lines) != want || g.Orders[0].Lines[0].PriceSource != "REQUISITION_ESTIMATE" {
			t.Fatalf("group %s = %+v", g.VendorID, g)
		}
	}
	if len(in.Skipped) != 1 || in.Skipped[0].Code != codeNeedsSourcing || in.Skipped[0].RequisitionLineID != h.fake.sourcing[3].ID {
		t.Fatalf("skipped = %+v", in.Skipped)
	}
	if h.fake.release.idem.Key != "r1" || h.fake.lastSourcing.LegalEntityID != leA {
		t.Fatalf("idem %+v, filter %+v", h.fake.release.idem, h.fake.lastSourcing)
	}
	if run.LinesReleased != 3 || run.LinesSkipped != 1 || len(run.PurchaseOrders) != 2 {
		t.Fatalf("run = %+v", run)
	}
}

// A vendor whose POs cannot be built (a negative quantity stands in for any refusal while pricing) is skipped with the
// problem's code and reason; the other vendors go on.
func TestReleaseRun_VendorThatCannotBeBuiltIsSkippedAndTheRestRelease(t *testing.T) {
	h := newHarness(t)
	v1, v2, site := ids.New(), ids.New(), ids.New()
	h.fake.sourcing = sourcing(t, leA, []fixtureLine{{v1, site, "2027-03-01"}, {v2, site, "2027-03-01"}})
	h.fake.sourcing[0].Qty = h.fake.sourcing[0].Qty.Neg() // a negative quantity cannot be priced into a PO line
	code, _, raw := releaseCall(h, `{"legalEntityId":"`+leA+`","windowDays":14,"maxLines":50}`, "r2")
	if code != http.StatusCreated {
		t.Fatalf("status %d: %s", code, raw)
	}
	in := h.fake.release.in
	if in.WindowDays != 14 || in.MaxLines != 50 || len(in.Groups) != 1 || in.Groups[0].VendorID != v2 {
		t.Fatalf("input = %+v; want only vendor 2 released", in)
	}
	if len(in.Skipped) != 1 || in.Skipped[0].VendorID != v1 || in.Skipped[0].Code == "" || in.Skipped[0].Reason == "" {
		t.Fatalf("skipped = %+v; want vendor 1's line with its problem", in.Skipped)
	}
}

func TestReleaseRun_MaxLinesCapsWhatIsRead(t *testing.T) {
	h := newHarness(t)
	v, site := ids.New(), ids.New()
	rows := make([]fixtureLine, 5)
	for i := range rows {
		rows[i] = fixtureLine{v, site, "2027-03-01"}
	}
	h.fake.sourcing = sourcing(t, leA, rows)
	if code, _, raw := releaseCall(h, `{"legalEntityId":"`+leA+`","maxLines":3}`, "r3"); code != http.StatusCreated {
		t.Fatalf("status %d: %s", code, raw)
	}
	if h.fake.release.in.Considered != 3 || len(h.fake.release.in.Groups[0].Lines) != 3 {
		t.Fatalf("considered %d, want 3", h.fake.release.in.Considered)
	}
}

func TestReleaseRun_Rejections(t *testing.T) {
	tests := []struct {
		name, body, scope string
		status            int
		code              string
	}{
		{"legal entity required", `{}`, "", 422, codeInvalid},
		{"legal entity not a uuid", `{"legalEntityId":"x"}`, "", 422, codeInvalid},
		{"window out of range", `{"legalEntityId":"` + leA + `","windowDays":61}`, "", 422, domain.CodeConsolidationWindow},
		{"max lines out of range", `{"legalEntityId":"` + leA + `","maxLines":501}`, "", 422, codeInvalid},
		{"max lines zero", `{"legalEntityId":"` + leA + `","maxLines":0}`, "", 422, codeInvalid},
		{"outside scope", `{"legalEntityId":"` + ids.New() + `"}`, "", 403, codeScope},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			h := newHarness(t)
			rec := h.do(call{method: "POST", path: "/v1/procurement/requisition-releases", duties: []string{dutyPurchaseOrderMaintain}, body: tc.body, headers: idem("k")})
			if rec.Code != tc.status || problemCode(t, rec) != tc.code {
				t.Fatalf("got %d %s, want %d %s: %s", rec.Code, problemCode(t, rec), tc.status, tc.code, rec.Body)
			}
			if h.fake.release.runs != nil {
				t.Fatal("a rejected release reached the store")
			}
		})
	}
	h := newHarness(t)
	if rec := h.do(call{method: "POST", path: "/v1/procurement/requisition-releases", duties: []string{dutyPurchaseOrderMaintain}, body: `{"legalEntityId":"` + leA + `"}`}); rec.Code != http.StatusBadRequest {
		t.Fatalf("without Idempotency-Key: %d", rec.Code)
	}
	if code, _, _ := releaseCall(h, `{"legalEntityId":"`+leA+`"}`, "k", dutyPurchaseOrderInquire); code != http.StatusForbidden {
		t.Fatalf("without procurement.purchase-order.maintain: %d", code)
	}
}

func TestReleaseRun_NothingToReleaseIsAnEmptyRun(t *testing.T) {
	h := newHarness(t)
	code, run, raw := releaseCall(h, `{"legalEntityId":"`+leA+`"}`, "r5")
	if code != http.StatusCreated || run.LinesConsidered != 0 || len(run.PurchaseOrders) != 0 || !strings.Contains(raw, `"purchaseOrders":[]`) {
		t.Fatalf("status %d: %s", code, raw)
	}
}

func TestReleaseRun_ListAndGet(t *testing.T) {
	h := newHarness(t)
	v, site := ids.New(), ids.New()
	h.fake.sourcing = sourcing(t, leA, []fixtureLine{{v, site, "2027-03-01"}})
	_, run, _ := releaseCall(h, `{"legalEntityId":"`+leA+`"}`, "r6")
	rec := h.do(call{method: "GET", path: "/v1/procurement/requisition-releases?legalEntityId=" + leA + "&status=COMPLETED&limit=10", duties: []string{dutyPurchaseOrderInquire}})
	if rec.Code != http.StatusOK || h.fake.release.lastRun.LegalEntityID != leA || h.fake.release.lastRun.Status != "COMPLETED" || h.fake.lastLimit != 10 || !strings.Contains(rec.Body.String(), run.ID) {
		t.Fatalf("list %d %+v: %s", rec.Code, h.fake.release.lastRun, rec.Body)
	}
	if rec := h.do(call{method: "GET", path: "/v1/procurement/requisition-releases/" + run.ID, duties: []string{dutyPurchaseOrderInquire}}); rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"linesReleased":1`) {
		t.Fatalf("get %d: %s", rec.Code, rec.Body)
	}
	if rec := h.do(call{method: "GET", path: "/v1/procurement/requisition-releases/" + ids.New(), duties: []string{dutyPurchaseOrderInquire}}); rec.Code != http.StatusNotFound {
		t.Fatalf("unknown: %d", rec.Code)
	}
	for _, q := range []string{"status=DONE", "legalEntityId=x"} {
		if rec := h.do(call{method: "GET", path: "/v1/procurement/requisition-releases?" + q, duties: []string{dutyPurchaseOrderInquire}}); problemCode(t, rec) != codeInvalid {
			t.Fatalf("%s: %d %s", q, rec.Code, rec.Body)
		}
	}
	if rec := h.do(call{method: "GET", path: "/v1/procurement/requisition-releases", duties: []string{dutyRequisitionCreate}}); rec.Code != http.StatusForbidden {
		t.Fatalf("list without inquire: %d", rec.Code)
	}
}

// The run reads the queue oldest first, and lines with no vendor never use up maxLines: a backlog of lines waiting for
// sourcing must not stop the lines that have a vendor from being ordered. They are still reported, up to maxLines.
func TestReleaseRun_LinesWithoutVendorDoNotUseUpMaxLines(t *testing.T) {
	h := newHarness(t)
	v, site := ids.New(), ids.New()
	h.fake.sourcing = sourcing(t, leA, []fixtureLine{{"", site, "2027-03-01"}, {"", site, "2027-03-01"}, {"", site, "2027-03-01"},
		{v, site, "2027-03-01"}, {v, site, "2027-03-02"}})
	if code, _, raw := releaseCall(h, `{"legalEntityId":"`+leA+`","maxLines":2}`, "r7"); code != http.StatusCreated {
		t.Fatalf("status %d: %s", code, raw)
	}
	in := h.fake.release.in
	if len(in.Groups) != 1 || len(in.Groups[0].Lines) != 2 || in.Groups[0].Lines[0].ID != h.fake.sourcing[3].ID {
		t.Fatalf("groups = %+v; want both lines with a vendor released", in.Groups)
	}
	if len(in.Skipped) != 2 || in.Considered != 4 || in.Skipped[0].RequisitionLineID != h.fake.sourcing[0].ID {
		t.Fatalf("skipped %+v, considered %d; want the first two lines without a vendor reported", in.Skipped, in.Considered)
	}
	seen := map[string]bool{}
	for _, f := range h.fake.sourcingCalls {
		if !f.OldestFirst || f.LegalEntityID != leA {
			t.Fatalf("queue read with %+v; want oldest first for the legal entity", f)
		}
		seen[f.Sourcing] = true
	}
	if !seen[store.SourcingHasVendor] || !seen[store.SourcingNeedsSourcing] || len(seen) != 2 {
		t.Fatalf("queue reads = %+v", h.fake.sourcingCalls)
	}
}
