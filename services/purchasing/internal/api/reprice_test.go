package api

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"
	"time"

	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/services/purchasing/internal/domain"
	"github.com/BenjaminSRussell/passage/services/purchasing/internal/store"
)

// repriceLog records what the reprice store methods received.
type repriceLog struct {
	filter  store.RepriceFilter
	after   string
	limit   int
	calls   []repriceCall
	replay  map[string]bool // idempotency keys already applied
	failFor map[string]error
}

type repriceCall struct {
	id      string
	ifMatch int
	next    store.PurchaseOrder
	plan    domain.RepricePlan
	idem    store.Idem
}

func (f *fakeStore) RepriceCandidates(_ context.Context, rf store.RepriceFilter, after string, limit int) (store.Page[string], error) {
	f.reprice.filter, f.reprice.after, f.reprice.limit = rf, after, limit
	out := []string{}
	for _, o := range f.orders {
		out = append(out, o.ID)
	}
	return store.Page[string]{Items: out}, nil
}

// RepricePurchaseOrder mimics the store: a known key replays, a stale
// If-Match is 412, otherwise next becomes the PO with a new version.
func (f *fakeStore) RepricePurchaseOrder(_ context.Context, id string, ifMatch int, next store.PurchaseOrder,
	plan domain.RepricePlan, idem store.Idem) (store.RepriceOutcome, error) {
	if err := f.reprice.failFor[id]; err != nil {
		return store.RepriceOutcome{}, err
	}
	cur := f.poByID[id]
	if f.reprice.replay[idem.Key] {
		return store.RepriceOutcome{Order: cur, VersionNo: cur.VersionNo, Replayed: true}, nil
	}
	if cur.RowVersion != ifMatch {
		return store.RepriceOutcome{}, httpx.NewProblem(http.StatusPreconditionFailed, store.CodeStaleVersion, "stale")
	}
	f.reprice.calls = append(f.reprice.calls, repriceCall{id: id, ifMatch: ifMatch, next: next, plan: plan, idem: idem})
	if len(plan.Changes) == 0 && len(plan.Invoiced) == 0 {
		return store.RepriceOutcome{Order: cur, VersionNo: cur.VersionNo}, nil
	}
	next.RowVersion, next.VersionNo = ifMatch+1, cur.VersionNo+1
	f.poByID[id] = next
	if f.reprice.replay == nil {
		f.reprice.replay = map[string]bool{}
	}
	f.reprice.replay[idem.Key] = true
	return store.RepriceOutcome{Order: next, VersionNo: next.VersionNo}, nil
}

const productB = "00000000-0000-7000-8000-0000000000d2"

// repricePO is a CONFIRMED PO created 2026-02-10 with: line 1, 100 EA of
// productA at 10.00 from trade agreement T1; line 2 manual; line 3 of productB
// from T1 with 40 of 100 invoiced; line 4 of productA drawn against a purchase
// agreement; a cancelled line 5; and a 2 % charge.
func repricePO(h *harness, t1 string) store.PurchaseOrder {
	d := decimal.RequireFromString
	trade := domain.PriceSourceTradePrefix + t1
	line := func(no int, product, price, source string) store.PurchaseOrderLine {
		return store.PurchaseOrderLine{LineNo: no, ProductID: product, Qty: d("100"), Unit: "EA", UnitPrice: d(price), PriceUnit: d("1"),
			DiscountPct: d("0"), TaxAmount: d("0"), LineAmount: d(price).Mul(d("100")), PriceSource: source, Status: domain.POLineOpen,
			DeliveryDate: domain.Day{Time: time.Date(2026, 4, 1, 0, 0, 0, 0, time.UTC)}}
	}
	l2 := line(2, productA, "11", domain.PriceSourceManual)
	l3 := line(3, productB, "10", trade)
	l3.InvoicedQty = d("40")
	l4 := line(4, productA, "10", trade)
	l4.AgreementLineID = ids.New()
	l5 := line(5, productA, "10", trade)
	l5.Status = domain.POLineCancelled
	po := store.PurchaseOrder{ID: ids.New(), Number: "PO-00000042", LegalEntityID: leA, VendorID: vendorA, Currency: "USD",
		ApprovalStatus: domain.POStatusConfirmed, VersionNo: 1, RowVersion: 7, ChangeManagement: true,
		CreatedAt: time.Date(2026, 2, 10, 9, 30, 0, 0, time.UTC),
		Lines:     []store.PurchaseOrderLine{line(1, productA, "10", trade), l2, l3, l4, l5},
		Charges:   []store.PurchaseOrderCharge{{Seq: 1, Calc: "PERCENT", Value: d("2"), Amount: d("0")}}}
	h.fake.poByID[po.ID] = po
	h.fake.orders = append(h.fake.orders, po)
	return po
}

// vendorPrice is an ACTIVE vendor/product price row valid from 2026-01-01.
func vendorPrice(product, price string) domain.TradeAgreement {
	return domain.TradeAgreement{ID: ids.New(), Relation: domain.RelationPrice, AccountCode: domain.AccountVendor, AccountID: vendorA,
		ItemCode: domain.ItemProduct, ItemID: product, Currency: "USD", Unit: "EA", FromQty: decimal.Zero,
		Price: decimal.RequireFromString(price), PriceUnit: decimal.NewFromInt(1),
		ValidFrom: domain.Day{Time: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)}, Status: domain.StatusActive}
}

func decodeReprice(t *testing.T, body []byte) repriceResult {
	t.Helper()
	var out repriceResult
	if err := json.Unmarshal(body, &out); err != nil {
		t.Fatalf("body %s: %v", body, err)
	}
	return out
}

var (
	inquire = []string{dutyPurchaseOrderInquire}
	confirm = []string{dutyPurchaseOrderConfirm}
)

// AC-17.16 on one PO: the price row for productA and productB now wins at 9.50.
// Line 1 moves (10.00 → 9.50, −50.00), the manual and agreement-drawn lines
// stay, the invoiced line is handed over on its 40 invoiced units, and the
// preview writes nothing.
func TestReprice_PreviewShowsMovesSkipsAndHandOvers(t *testing.T) {
	h := newHarness(t)
	// The fake returns both rows for every line; FR-17.6 matching keeps the
	// row of the line's own product.
	h.fake.agreements = []domain.TradeAgreement{vendorPrice(productA, "9.5"), vendorPrice(productB, "9.5")}
	po := repricePO(h, ids.New())
	rec := h.do(call{method: "POST", path: poPath(po.ID) + ":reprice", body: `{}`, user: userA, duties: inquire})
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d: %s", rec.Code, rec.Body)
	}
	out := decodeReprice(t, rec.Body.Bytes())
	if out.Applied || len(h.fake.reprice.calls) != 0 || rec.Header().Get("ETag") != `"7"` {
		t.Fatalf("preview applied=%v writes=%d etag=%s", out.Applied, len(h.fake.reprice.calls), rec.Header().Get("ETag"))
	}
	if out.PricingDate.String() != "2026-02-10" || h.fake.lastPriceFilter.Date.String() != "2026-02-10" {
		t.Fatalf("priced on %s / %s, want the order date 2026-02-10", out.PricingDate, h.fake.lastPriceFilter.Date)
	}
	if len(out.Changes) != 1 || out.Changes[0].LineNo != 1 || out.Changes[0].OldPrice != "10" || out.Changes[0].NewPrice != "9.5" ||
		out.Changes[0].Delta != "-50" || out.TotalDelta != "-50" {
		t.Fatalf("changes %+v total %s", out.Changes, out.TotalDelta)
	}
	if len(out.Invoiced) != 1 || out.Invoiced[0].LineNo != 3 || out.Invoiced[0].Qty != "40" || out.Invoiced[0].Delta != "-20" {
		t.Fatalf("invoiced %+v", out.Invoiced)
	}
	want := map[int]string{2: domain.SkipManualPrice, 3: domain.SkipInvoiced, 4: domain.SkipAgreementLine}
	if len(out.Skipped) != len(want) {
		t.Fatalf("skipped %+v, want %v (cancelled line 5 is not listed)", out.Skipped, want)
	}
	for _, s := range out.Skipped {
		if want[s.LineNo] != s.Reason {
			t.Fatalf("line %d skipped as %s, want %s", s.LineNo, s.Reason, want[s.LineNo])
		}
	}
}

func TestReprice_ApplyWritesTheRepricedDocument(t *testing.T) {
	h := newHarness(t)
	t2 := vendorPrice(productA, "9.5")
	h.fake.agreements = []domain.TradeAgreement{t2}
	po := repricePO(h, ids.New())
	rec := h.do(call{method: "POST", path: poPath(po.ID) + ":reprice", body: `{"apply":true}`, user: userA, duties: confirm,
		headers: ifMatchIdem("7", "rp-1")})
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d: %s", rec.Code, rec.Body)
	}
	out := decodeReprice(t, rec.Body.Bytes())
	if !out.Applied || out.VersionNo != 2 || rec.Header().Get("ETag") != `"8"` {
		t.Fatalf("applied=%v version=%d etag=%s", out.Applied, out.VersionNo, rec.Header().Get("ETag"))
	}
	if len(h.fake.reprice.calls) != 1 {
		t.Fatalf("store calls %d", len(h.fake.reprice.calls))
	}
	c := h.fake.reprice.calls[0]
	if c.ifMatch != 7 || c.idem.Key != "rp-1" || c.idem.Hash == "" {
		t.Fatalf("ifMatch %d idem %+v", c.ifMatch, c.idem)
	}
	// Line 1 moved with its new source; everything else kept; cancelled line 5 left out.
	got := map[int]store.PurchaseOrderLine{}
	for _, l := range c.next.Lines {
		got[l.LineNo] = l
	}
	if _, ok := got[5]; ok || len(got) != 4 {
		t.Fatalf("next lines %v", got)
	}
	l1 := got[1]
	if !l1.UnitPrice.Equal(decimal.RequireFromString("9.5")) || l1.PriceSource != domain.PriceSourceTradePrefix+t2.ID ||
		!l1.LineAmount.Equal(decimal.RequireFromString("950")) {
		t.Fatalf("line 1 = %s %s %s", l1.UnitPrice, l1.PriceSource, l1.LineAmount)
	}
	if !got[2].UnitPrice.Equal(decimal.RequireFromString("11")) || !got[3].UnitPrice.Equal(decimal.RequireFromString("10")) {
		t.Fatal("a line the run must not move was moved")
	}
	// Totals are priced again: lines 950 + 1100 + 1000 + 1000 = 4050, 2 % charge 81.00.
	if !c.next.Subtotal.Equal(decimal.RequireFromString("4050")) || !c.next.ChargesTotal.Equal(decimal.RequireFromString("81")) ||
		!c.next.Total.Equal(decimal.RequireFromString("4131")) {
		t.Fatalf("subtotal %s charges %s total %s", c.next.Subtotal, c.next.ChargesTotal, c.next.Total)
	}
}

func TestReprice_ReplayedKeyIsAnsweredNotRefused(t *testing.T) {
	h := newHarness(t)
	h.fake.agreements = []domain.TradeAgreement{vendorPrice(productA, "9.5")}
	po := repricePO(h, ids.New())
	first := h.do(call{method: "POST", path: poPath(po.ID) + ":reprice", body: `{"apply":true}`, user: userA, duties: confirm,
		headers: ifMatchIdem("7", "rp-1")})
	if first.Code != http.StatusOK {
		t.Fatalf("first apply %d: %s", first.Code, first.Body)
	}
	again := h.do(call{method: "POST", path: poPath(po.ID) + ":reprice", body: `{"apply":true}`, user: userA, duties: confirm,
		headers: ifMatchIdem("7", "rp-1")})
	if again.Code != http.StatusOK {
		t.Fatalf("replay %d: %s", again.Code, again.Body)
	}
	out := decodeReprice(t, again.Body.Bytes())
	if !out.Applied || !out.Replayed || len(h.fake.reprice.calls) != 1 {
		t.Fatalf("replay applied=%v replayed=%v writes=%d", out.Applied, out.Replayed, len(h.fake.reprice.calls))
	}
	stale := h.do(call{method: "POST", path: poPath(po.ID) + ":reprice", body: `{"apply":true}`, user: userA, duties: confirm,
		headers: ifMatchIdem("7", "rp-2")})
	if stale.Code != http.StatusPreconditionFailed || problemCode(t, stale) != store.CodeStaleVersion {
		t.Fatalf("new key on a stale version: %d %s", stale.Code, stale.Body)
	}
}

func TestReprice_RefusesBeforeTheStore(t *testing.T) {
	h := newHarness(t)
	po := repricePO(h, ids.New())
	draft := repricePO(h, ids.New())
	d := h.fake.poByID[draft.ID]
	d.ApprovalStatus = domain.POStatusDraft
	h.fake.poByID[draft.ID] = d
	path := poPath(po.ID) + ":reprice"
	tests := []struct {
		name string
		c    call
		want int
		code string
	}{
		{"preview without inquire", call{method: "POST", path: path, body: `{}`, duties: []string{dutyPurchaseOrderMaintain}}, 403, codeScope},
		{"apply with inquire only", call{method: "POST", path: path, body: `{"apply":true}`, duties: inquire, headers: ifMatchIdem("7", "k")}, 403, codeScope},
		{"apply without key", call{method: "POST", path: path, body: `{"apply":true}`, duties: confirm, headers: map[string]string{"If-Match": `"7"`}}, 400, codeIdemRequired},
		{"apply without If-Match", call{method: "POST", path: path, body: `{"apply":true}`, duties: confirm, headers: idem("k")}, 428, codeIfMatchMissing},
		{"apply outside scope", call{method: "POST", path: path, body: `{"apply":true}`, duties: confirm, scope: ids.New(), headers: ifMatchIdem("7", "k")}, 403, codeScope},
		{"draft PO", call{method: "POST", path: poPath(draft.ID) + ":reprice", body: `{}`, duties: inquire}, 422, domain.CodePOState},
		{"unknown PO", call{method: "POST", path: poPath(ids.New()) + ":reprice", body: `{}`, duties: inquire}, 404, store.CodePurchaseOrderNotFound},
		{"unknown field", call{method: "POST", path: path, body: `{"aply":true}`, duties: inquire}, 400, codeInvalid},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			tc.c.user = userA
			rec := h.do(tc.c)
			if rec.Code != tc.want {
				t.Fatalf("status %d, want %d: %s", rec.Code, tc.want, rec.Body)
			}
			if tc.code != "" && problemCode(t, rec) != tc.code {
				t.Fatalf("code %s, want %s", problemCode(t, rec), tc.code)
			}
			if len(h.fake.reprice.calls) != 0 {
				t.Fatal("the store was written")
			}
		})
	}
}

// AC-17.16 over many POs: the run lists only POs with something to do,
// applies each at the version it read, isolates a failing PO and passes the
// filters and page down.
func TestRepriceRun_AppliesPerOrderAndIsolatesFailures(t *testing.T) {
	h := newHarness(t)
	h.fake.agreements = []domain.TradeAgreement{vendorPrice(productA, "9.5")}
	t1 := ids.New()
	a, b := repricePO(h, t1), repricePO(h, t1)
	// c is already at the current price: nothing to move, not listed.
	c := repricePO(h, t1)
	cur := h.fake.poByID[c.ID]
	cur.Lines = cur.Lines[:1]
	cur.Lines[0].UnitPrice = decimal.RequireFromString("9.5")
	h.fake.poByID[c.ID] = cur
	h.fake.reprice.failFor = map[string]error{b.ID: &domain.Error{Code: domain.CodeValidation, Detail: "boom"}}

	body := `{"apply":true,"tradeAgreementId":"` + t1 + `","vendorId":"` + vendorA + `","limit":10}`
	rec := h.do(call{method: "POST", path: "/v1/procurement/purchase-orders:reprice", body: body, user: userA, duties: confirm, headers: idem("run-1")})
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d: %s", rec.Code, rec.Body)
	}
	var out repriceRunResult
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	if h.fake.reprice.filter.TradeAgreementID != t1 || h.fake.reprice.filter.VendorID != vendorA || h.fake.reprice.limit != 10 {
		t.Fatalf("filter %+v limit %d", h.fake.reprice.filter, h.fake.reprice.limit)
	}
	if !out.Applied || out.Visited != 3 || len(out.Items) != 2 {
		t.Fatalf("applied=%v visited=%d items=%+v", out.Applied, out.Visited, out.Items)
	}
	byID := map[string]repriceResult{}
	for _, it := range out.Items {
		byID[it.PurchaseOrderID] = it
	}
	if got := byID[a.ID]; !got.Applied || got.Error != nil || got.VersionNo != 2 {
		t.Fatalf("PO a: %+v", got)
	}
	if got := byID[b.ID]; got.Applied || got.Error == nil || got.Error.Code != domain.CodeValidation {
		t.Fatalf("PO b should carry its failure: %+v", got)
	}
	if len(h.fake.reprice.calls) != 1 || h.fake.reprice.calls[0].idem.Key != "run-1/"+a.ID || h.fake.reprice.calls[0].ifMatch != 7 {
		t.Fatalf("store calls %+v", h.fake.reprice.calls)
	}
}

func TestRepriceRun_PreviewWritesNothingAndValidates(t *testing.T) {
	h := newHarness(t)
	h.fake.agreements = []domain.TradeAgreement{vendorPrice(productA, "9.5")}
	repricePO(h, ids.New())
	rec := h.do(call{method: "POST", path: "/v1/procurement/purchase-orders:reprice", body: `{}`, user: userA, duties: inquire})
	if rec.Code != http.StatusOK || len(h.fake.reprice.calls) != 0 || h.fake.reprice.limit != defaultRepriceRunLimit {
		t.Fatalf("status %d writes %d limit %d: %s", rec.Code, len(h.fake.reprice.calls), h.fake.reprice.limit, rec.Body)
	}
	for _, tc := range []struct {
		name   string
		body   string
		duties []string
		hdr    map[string]string
		want   int
	}{
		{"bad vendor", `{"vendorId":"x"}`, inquire, nil, 422},
		{"limit too high", `{"limit":101}`, inquire, nil, 422},
		{"apply needs confirm", `{"apply":true}`, inquire, idem("k"), 403},
		{"apply needs a key", `{"apply":true}`, confirm, nil, 400},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rec := h.do(call{method: "POST", path: "/v1/procurement/purchase-orders:reprice", body: tc.body, user: userA, duties: tc.duties, headers: tc.hdr})
			if rec.Code != tc.want {
				t.Fatalf("status %d, want %d: %s", rec.Code, tc.want, rec.Body)
			}
		})
	}
}
