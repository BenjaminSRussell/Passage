package api

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/services/purchasing/internal/domain"
	"github.com/BenjaminSRussell/passage/services/purchasing/internal/store"
)

// rfqLog records what the RFQ store methods received.
type rfqLog struct {
	created    *domain.RFQ
	createIdem store.Idem
	filter     store.RFQFilter
	sent       []string
	action     string
	ifMatch    int
	replies    []domain.RFQReply
	replyBy    string
	awardCalls int
	awards     []domain.Award
	orders     []store.AwardedOrder
	today      domain.Day
	scoredBy   string
	scores     []domain.VendorScore
	openings   []string // users the opening of a sealed RFQ was logged for
}

func (f *fakeStore) CreateRFQ(_ context.Context, in domain.RFQ, idem store.Idem) (domain.RFQ, error) {
	in.ID, in.Number, in.Status, in.Version = ids.New(), "RFQ-00000001", domain.RFQDraft, 1
	f.rfqLog.created, f.rfqLog.createIdem = &in, idem
	return in, nil
}

func (f *fakeStore) GetRFQ(_ context.Context, id string) (domain.RFQ, error) {
	if f.rfq == nil || f.rfq.ID != id {
		return domain.RFQ{}, httpx.NewProblem(http.StatusNotFound, store.CodeRFQNotFound, "RFQ not found")
	}
	return *f.rfq, nil
}

func (f *fakeStore) ListRFQs(_ context.Context, rf store.RFQFilter, after string, limit int) (store.Page[store.RFQSummary], error) {
	f.rfqLog.filter, f.lastAfter, f.lastLimit = rf, after, limit
	return store.Page[store.RFQSummary]{Items: []store.RFQSummary{}}, nil
}

// checkVersion mimics the store's If-Match check.
func (f *fakeStore) checkVersion(id string, ifMatch int) (domain.RFQ, error) {
	cur, err := f.GetRFQ(context.Background(), id)
	if err != nil {
		return cur, err
	}
	if cur.Version != ifMatch {
		return cur, httpx.NewProblem(http.StatusConflict, store.CodeVersionConflict, "stale")
	}
	return cur, nil
}

func (f *fakeStore) SendRFQ(_ context.Context, id string, ifMatch int, vendors []string, _ store.Idem) (domain.RFQ, error) {
	f.rfqLog.sent, f.rfqLog.ifMatch = vendors, ifMatch
	return f.checkVersion(id, ifMatch)
}

func (f *fakeStore) ActOnRFQ(_ context.Context, id string, ifMatch int, action string, _ store.Idem) (domain.RFQ, error) {
	f.rfqLog.action, f.rfqLog.ifMatch = action, ifMatch
	return f.checkVersion(id, ifMatch)
}

func (f *fakeStore) RecordRFQReplies(_ context.Context, id, vendorID string, replies []domain.RFQReply, _ store.Idem) (domain.RFQ, error) {
	f.rfqLog.replies, f.rfqLog.replyBy = replies, vendorID
	return f.GetRFQ(context.Background(), id)
}

func (f *fakeStore) AwardRFQ(_ context.Context, id string, ifMatch int, awards []domain.Award, orders []store.AwardedOrder, today domain.Day, _ store.Idem) (domain.RFQ, []store.PurchaseOrder, error) {
	f.rfqLog.awardCalls++
	f.rfqLog.awards, f.rfqLog.orders, f.rfqLog.today = awards, orders, today
	cur, err := f.checkVersion(id, ifMatch)
	if err != nil {
		return cur, nil, err
	}
	pos := make([]store.PurchaseOrder, 0, len(orders))
	for _, o := range orders {
		pos = append(pos, o.Order)
	}
	return cur, pos, nil
}

// RFQ fixture: the harness clock is 2026-03-01T15:00Z and the deadline is
// 2026-03-05T17:00Z. Line 1 (10 EA) has two on-time replies (vendors A and B)
// and one late, cheaper reply (vendor C) — the AC-17.8 shape. Line 2 (4 HR)
// has one on-time reply from vendor A.
var (
	rfqID                  = "00000000-0000-7000-8000-00000000f001"
	rfqLine1, rfqLine2     = "00000000-0000-7000-8000-00000000f101", "00000000-0000-7000-8000-00000000f102"
	rfqVendorA, rfqVendorB = "00000000-0000-7000-8000-00000000fa01", "00000000-0000-7000-8000-00000000fb01"
	rfqVendorC             = "00000000-0000-7000-8000-00000000fc01"
	replyA1, replyB1       = "00000000-0000-7000-8000-00000000fa11", "00000000-0000-7000-8000-00000000fb11"
	replyC1, replyA2       = "00000000-0000-7000-8000-00000000fc11", "00000000-0000-7000-8000-00000000fa12"
	rfqDeadline            = time.Date(2026, 3, 5, 17, 0, 0, 0, time.UTC)
	rfqProduct             = "00000000-0000-7000-8000-00000000f0a1"
	rfqActionHeaders       = map[string]string{"Idempotency-Key": "k-1", "If-Match": `"3"`}
	rfqMaintainDuty        = []string{dutyRFQMaintain}
	rfqAwardDuty           = []string{dutyRFQAward}
	rfqRequiredLateInLine  = domain.Day{Time: time.Date(2026, 4, 1, 0, 0, 0, 0, time.UTC)}
)

func rfqFixture() *domain.RFQ {
	reply := func(id, line, vendor, price string, lead int, late bool) domain.RFQReply {
		return domain.RFQReply{ID: id, LineID: line, VendorID: vendor, UnitPrice: decimal.RequireFromString(price),
			PriceUnit: decimal.NewFromInt(1), Currency: "USD", LeadTimeDays: lead, Late: late}
	}
	return &domain.RFQ{
		ID: rfqID, Number: "RFQ-00000007", LegalEntityID: leA, Status: domain.RFQRepliesOpen, Version: 3,
		RankingRule: domain.RankLowestPrice, ReplyDeadline: rfqDeadline,
		Lines: []domain.RFQLine{
			{ID: rfqLine1, LineNo: 1, ProductID: rfqProduct, Qty: decimal.NewFromInt(10), Unit: "EA"},
			{ID: rfqLine2, LineNo: 2, Description: "Calibration", Qty: decimal.NewFromInt(4), Unit: "HR", RequiredDate: &rfqRequiredLateInLine},
		},
		Invitations: []domain.RFQInvitation{{VendorID: rfqVendorA}, {VendorID: rfqVendorB}, {VendorID: rfqVendorC}},
		Replies: []domain.RFQReply{
			reply(replyA1, rfqLine1, rfqVendorA, "10.00", 5, false),
			reply(replyB1, rfqLine1, rfqVendorB, "9.50", 12, false),
			reply(replyC1, rfqLine1, rfqVendorC, "8.00", 3, true),
			reply(replyA2, rfqLine2, rfqVendorA, "120.00", 2, false),
		},
	}
}

func rfqHarness(t *testing.T) *harness {
	h := newHarness(t)
	h.fake.rfq = rfqFixture()
	return h
}

func rfqPath(suffix string) string { return "/v1/procurement/rfqs/" + rfqID + suffix }

func TestRFQRoutes_EnforceDuties(t *testing.T) {
	award := `{"awards":[{"lineId":"` + rfqLine1 + `","replyId":"` + replyB1 + `"}]}`
	tests := []struct {
		name   string
		c      call
		status int
	}{
		{"create needs RFQMaintain", call{method: "POST", path: "/v1/procurement/rfqs", duties: rfqAwardDuty, body: `{}`, headers: idem("k")}, 403},
		{"replies need RFQMaintain", call{method: "POST", path: rfqPath("/replies"), duties: rfqAwardDuty, body: `{}`, headers: idem("k")}, 403},
		{"send needs RFQMaintain", call{method: "POST", path: rfqPath(":send"), duties: rfqAwardDuty, body: `{"vendorIds":["` + rfqVendorA + `"]}`, headers: rfqActionHeaders}, 403},
		{"award needs RFQAward", call{method: "POST", path: rfqPath(":award"), duties: rfqMaintainDuty, body: award, headers: rfqActionHeaders}, 403},
		{"award with RFQAward", call{method: "POST", path: rfqPath(":award"), duties: rfqAwardDuty, body: award, headers: rfqActionHeaders}, 200},
		{"read with RFQAward", call{method: "GET", path: rfqPath(""), duties: rfqAwardDuty}, 200},
		{"compare with RFQMaintain", call{method: "GET", path: rfqPath("/compare"), duties: rfqMaintainDuty}, 200},
		{"list needs an RFQ duty", call{method: "GET", path: "/v1/procurement/rfqs", duties: []string{dutyPurchaseOrderMaintain}}, 403},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			h := rfqHarness(t)
			rec := h.do(tc.c)
			if rec.Code != tc.status {
				t.Fatalf("status %d, want %d: %s", rec.Code, tc.status, rec.Body)
			}
			if tc.status == 403 && (h.fake.rfqLog.awardCalls != 0 || h.fake.rfqLog.sent != nil || h.fake.rfqLog.created != nil) {
				t.Errorf("a refused request reached the store")
			}
		})
	}
}

const rfqCreateBody = `{"legalEntityId":"00000000-0000-7000-8000-0000000001e1","replyDeadline":"2026-03-05T17:00:00Z",
 "lines":[{"productId":"00000000-0000-7000-8000-00000000f0a1","qty":"10","unit":" EA "},{"description":"  Calibration ","qty":"4","unit":"HR","requiredDate":"2026-04-01"}]}`

func TestCreateRFQ_StoresTrimmedDraftWithDefaultRule(t *testing.T) {
	h := newHarness(t)
	rec := h.do(call{method: "POST", path: "/v1/procurement/rfqs", duties: rfqMaintainDuty, body: rfqCreateBody, headers: idem("k-create")})
	if rec.Code != http.StatusCreated {
		t.Fatalf("status %d: %s", rec.Code, rec.Body)
	}
	if rec.Header().Get("ETag") != `"1"` {
		t.Errorf("ETag %q, want \"1\"", rec.Header().Get("ETag"))
	}
	got := h.fake.rfqLog.created
	if got.RankingRule != domain.RankLowestPrice || !got.ReplyDeadline.Equal(rfqDeadline) {
		t.Errorf("rule %s deadline %s, want LOWEST_PRICE at %s", got.RankingRule, got.ReplyDeadline, rfqDeadline)
	}
	if len(got.Lines) != 2 || got.Lines[0].Unit != "EA" || got.Lines[1].Description != "Calibration" || !got.Lines[0].Qty.Equal(decimal.NewFromInt(10)) {
		t.Errorf("lines not validated and trimmed: %+v", got.Lines)
	}
	if got.Lines[1].RequiredDate == nil || got.Lines[1].RequiredDate.String() != "2026-04-01" {
		t.Errorf("required date lost: %+v", got.Lines[1].RequiredDate)
	}
	if h.fake.rfqLog.createIdem.Key != "k-create" || h.fake.rfqLog.createIdem.Hash != hashBody("rfq.create", mustDecode[rfqBody](t, rfqCreateBody)) {
		t.Errorf("idempotency claim %+v not scoped to rfq.create", h.fake.rfqLog.createIdem)
	}
}

func TestCreateRFQ_Rejections(t *testing.T) {
	mutate := func(from, to string) string { return strings.Replace(rfqCreateBody, from, to, 1) }
	tests := []struct {
		name, body, field string
		status            int
		scope             string
	}{
		{"deadline already passed", mutate("2026-03-05T17:00:00Z", "2026-03-01T14:59:59Z"), "replyDeadline", 422, ""},
		{"deadline missing", mutate(`"replyDeadline":"2026-03-05T17:00:00Z",`, ""), "replyDeadline", 422, ""},
		{"unknown ranking rule", mutate(`"lines"`, `"rankingRule":"BOGUS","lines"`), "rankingRule", 422, ""},
		{"weighted score without criteria", mutate(`"lines"`, `"rankingRule":"WEIGHTED_SCORE","lines"`), "criteria", 422, ""},
		{"no lines", `{"legalEntityId":"00000000-0000-7000-8000-0000000001e1","replyDeadline":"2026-03-05T17:00:00Z","lines":[]}`, "lines", 422, ""},
		{"line with neither product nor description", mutate(`"description":"  Calibration ",`, ""), "lines[1].productId", 422, ""},
		{"zero quantity", mutate(`"qty":"10"`, `"qty":"0"`), "lines[0].qty", 422, ""},
		{"quantity not a decimal", mutate(`"qty":"10"`, `"qty":"ten"`), "lines[0].qty", 422, ""},
		{"blank unit", mutate(`"unit":"HR"`, `"unit":" "`), "lines[1].unit", 422, ""},
		{"product not a UUID", mutate(`"productId":"00000000-0000-7000-8000-00000000f0a1"`, `"productId":"widget"`), "lines[0].productId", 422, ""},
		{"legal entity outside scope", rfqCreateBody, "", 403, "00000000-0000-7000-8000-0000000001e2"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			h := newHarness(t)
			rec := h.do(call{method: "POST", path: "/v1/procurement/rfqs", duties: rfqMaintainDuty, body: tc.body, headers: idem("k"), scope: tc.scope})
			if rec.Code != tc.status {
				t.Fatalf("status %d, want %d: %s", rec.Code, tc.status, rec.Body)
			}
			if tc.field != "" {
				var p httpx.Problem
				_ = json.Unmarshal(rec.Body.Bytes(), &p)
				if _, ok := p.Errors[tc.field]; !ok {
					t.Errorf("errors %v do not name %s", p.Errors, tc.field)
				}
			}
			if h.fake.rfqLog.created != nil {
				t.Errorf("a rejected RFQ reached the store")
			}
		})
	}
}

func TestRFQAction_HeadersAndUnknownAction(t *testing.T) {
	h := rfqHarness(t)
	body := `{"vendorIds":["` + rfqVendorA + `"]}`
	if rec := h.do(call{method: "POST", path: rfqPath(":send"), duties: rfqMaintainDuty, body: body, headers: map[string]string{"If-Match": `"3"`}}); rec.Code != 400 || problemCode(t, rec) != codeIdemRequired {
		t.Errorf("missing Idempotency-Key: %d %s", rec.Code, rec.Body)
	}
	if rec := h.do(call{method: "POST", path: rfqPath(":send"), duties: rfqMaintainDuty, body: body, headers: idem("k")}); rec.Code != 428 || problemCode(t, rec) != codeIfMatchMissing {
		t.Errorf("missing If-Match: %d %s", rec.Code, rec.Body)
	}
	if rec := h.do(call{method: "POST", path: rfqPath(":explode"), duties: rfqMaintainDuty, body: body, headers: rfqActionHeaders}); rec.Code != 404 || problemCode(t, rec) != codeUnknownAction {
		t.Errorf("unknown action: %d %s", rec.Code, rec.Body)
	}
	if rec := h.do(call{method: "POST", path: rfqPath(":close"), duties: rfqMaintainDuty, headers: map[string]string{"Idempotency-Key": "k", "If-Match": `"2"`}}); rec.Code != 409 || problemCode(t, rec) != store.CodeVersionConflict {
		t.Errorf("stale close: %d %s", rec.Code, rec.Body)
	}
	if rec := h.do(call{method: "POST", path: rfqPath(":cancel"), duties: rfqMaintainDuty, headers: rfqActionHeaders}); rec.Code != 200 || h.fake.rfqLog.action != domain.RFQActionCancel {
		t.Errorf("cancel: %d %s (store saw %q)", rec.Code, rec.Body, h.fake.rfqLog.action)
	}
}

func TestRFQSend_DeduplicatesAndValidatesVendors(t *testing.T) {
	h := rfqHarness(t)
	rec := h.do(call{method: "POST", path: rfqPath(":send"), duties: rfqMaintainDuty, headers: rfqActionHeaders,
		body: `{"vendorIds":["` + rfqVendorA + `","` + rfqVendorB + `","` + rfqVendorA + `"]}`})
	if rec.Code != 200 {
		t.Fatalf("status %d: %s", rec.Code, rec.Body)
	}
	if got := h.fake.rfqLog.sent; len(got) != 2 || got[0] != rfqVendorA || got[1] != rfqVendorB {
		t.Errorf("vendors sent %v, want A then B once each", got)
	}
	for name, body := range map[string]string{"none": `{"vendorIds":[]}`, "not a UUID": `{"vendorIds":["acme"]}`} {
		h := rfqHarness(t)
		rec := h.do(call{method: "POST", path: rfqPath(":send"), duties: rfqMaintainDuty, headers: rfqActionHeaders, body: body})
		if rec.Code != 422 || h.fake.rfqLog.sent != nil {
			t.Errorf("%s: status %d, store saw %v", name, rec.Code, h.fake.rfqLog.sent)
		}
	}
}

func compare(t *testing.T, h *harness, query string) rfqComparison {
	t.Helper()
	rec := h.do(call{method: "GET", path: rfqPath("/compare" + query), duties: rfqMaintainDuty})
	if rec.Code != 200 {
		t.Fatalf("status %d: %s", rec.Code, rec.Body)
	}
	var cmp rfqComparison
	if err := json.Unmarshal(rec.Body.Bytes(), &cmp); err != nil {
		t.Fatal(err)
	}
	return cmp
}

// AC-17.8: two replies before the deadline and one after; the late reply is
// excluded and the award to the lowest creates a PO with the reply price and
// lead time.
func TestRFQ_AC178_CompareExcludesLateAndAwardPricesFromReply(t *testing.T) {
	h := rfqHarness(t)
	cmp := compare(t, h, "")
	line := cmp.Lines[0]
	if len(line.Ranked) != 2 || line.Ranked[0].ReplyID != replyB1 || line.Ranked[1].ReplyID != replyA1 {
		t.Fatalf("ranking %+v, want B (9.50) then A (10.00)", line.Ranked)
	}
	if len(line.Excluded) != 1 || line.Excluded[0].ReplyID != replyC1 || line.Excluded[0].Reason != domain.ReasonLate {
		t.Fatalf("excluded %+v, want the late reply from C", line.Excluded)
	}
	if !line.Ranked[0].Total.Equal(decimal.RequireFromString("95")) || line.Unit != "EA" || !line.Qty.Equal(decimal.NewFromInt(10)) {
		t.Errorf("line 1 total %s for %s %s, want 95 for 10 EA", line.Ranked[0].Total, line.Qty, line.Unit)
	}

	rec := h.do(call{method: "POST", path: rfqPath(":award"), duties: rfqAwardDuty, headers: rfqActionHeaders,
		body: `{"awards":[{"lineId":"` + rfqLine1 + `","replyId":"` + replyB1 + `"}]}`})
	if rec.Code != 200 {
		t.Fatalf("award status %d: %s", rec.Code, rec.Body)
	}
	orders := h.fake.rfqLog.orders
	if len(orders) != 1 || len(orders[0].Order.Lines) != 1 {
		t.Fatalf("orders %+v, want one PO with one line", orders)
	}
	po, l := orders[0].Order, orders[0].Order.Lines[0]
	if po.VendorID != rfqVendorB || po.Currency != "USD" || po.LegalEntityID != leA || !po.ChangeManagement {
		t.Errorf("PO header %+v not taken from the RFQ and the winning reply", po)
	}
	if !l.UnitPrice.Equal(decimal.RequireFromString("9.50")) || l.PriceSource != "RFQ_REPLY:"+replyB1 || l.ProductID != rfqProduct {
		t.Errorf("line %+v not priced from reply B", l)
	}
	// Today (2026-03-01) plus B's 12-day lead time.
	if l.DeliveryDate.String() != "2026-03-13" {
		t.Errorf("delivery %s, want 2026-03-13", l.DeliveryDate)
	}
	if !po.Total.Equal(decimal.RequireFromString("95")) || !l.LineAmount.Equal(po.Total) {
		t.Errorf("total %s line %s, want 95.00", po.Total, l.LineAmount)
	}
	if got := orders[0].ReplyIDs; len(got) != 1 || got[0] != replyB1 {
		t.Errorf("reply links %v", got)
	}
	if h.fake.rfqLog.today.String() != "2026-03-01" {
		t.Errorf("store got today %s", h.fake.rfqLog.today)
	}
	var res rfqAwardResult
	if err := json.Unmarshal(rec.Body.Bytes(), &res); err != nil || len(res.PurchaseOrders) != 1 {
		t.Errorf("response %s does not carry the created PO", rec.Body)
	}
}

func TestRFQAward_GroupsByVendorAndHonoursRequiredDate(t *testing.T) {
	h := rfqHarness(t)
	rec := h.do(call{method: "POST", path: rfqPath(":award"), duties: rfqAwardDuty, headers: rfqActionHeaders,
		body: `{"awards":[{"lineId":"` + rfqLine1 + `","replyId":"` + replyA1 + `"},{"lineId":"` + rfqLine2 + `","replyId":"` + replyA2 + `"}]}`})
	if rec.Code != 200 {
		t.Fatalf("status %d: %s", rec.Code, rec.Body)
	}
	orders := h.fake.rfqLog.orders
	if len(orders) != 1 || len(orders[0].Order.Lines) != 2 {
		t.Fatalf("one vendor and currency should give one PO with two lines, got %+v", orders)
	}
	l2 := orders[0].Order.Lines[1]
	// Line 2 is required on 2026-04-01, later than today + 2 days.
	if l2.LineNo != 2 || l2.DeliveryDate.String() != "2026-04-01" || l2.Description != "Calibration" {
		t.Errorf("line 2 %+v, want line 2 due 2026-04-01", l2)
	}
	// 10 × 10.00 + 4 × 120.00 = 580.00
	if !orders[0].Order.Total.Equal(decimal.RequireFromString("580")) {
		t.Errorf("total %s, want 580", orders[0].Order.Total)
	}
}

func TestRFQAward_Rejections(t *testing.T) {
	awardOf := func(line, reply, reason string) string {
		return `{"awards":[{"lineId":"` + line + `","replyId":"` + reply + `","overrideReason":"` + reason + `"}]}`
	}
	tests := []struct {
		name    string
		body    string
		status  int
		code    string
		headers map[string]string
		setup   func(*domain.RFQ)
	}{
		{"late reply without a reason", awardOf(rfqLine1, replyC1, ""), 422, domain.CodeRFQLate, rfqActionHeaders, nil},
		{"reply of another line", awardOf(rfqLine2, replyA1, ""), 422, domain.CodeRFQAward, rfqActionHeaders, nil},
		{"no awards", `{"awards":[]}`, 422, domain.CodeRFQAward, rfqActionHeaders, nil},
		{"line id not a UUID", awardOf("x", replyA1, ""), 422, codeInvalid, rfqActionHeaders, nil},
		{"RFQ still SENT", awardOf(rfqLine1, replyA1, ""), 409, domain.CodeRFQState, rfqActionHeaders, func(r *domain.RFQ) { r.Status = domain.RFQSent }},
		{"expired reply", awardOf(rfqLine1, replyA1, ""), 422, domain.CodeRFQAward, rfqActionHeaders, func(r *domain.RFQ) {
			d := domain.Day{Time: time.Date(2026, 2, 28, 0, 0, 0, 0, time.UTC)}
			r.Replies[0].ValidUntil = &d
		}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			h := rfqHarness(t)
			if tc.setup != nil {
				tc.setup(h.fake.rfq)
			}
			rec := h.do(call{method: "POST", path: rfqPath(":award"), duties: rfqAwardDuty, headers: tc.headers, body: tc.body})
			if rec.Code != tc.status || problemCode(t, rec) != tc.code {
				t.Fatalf("got %d %s, want %d %s: %s", rec.Code, problemCode(t, rec), tc.status, tc.code, rec.Body)
			}
			if h.fake.rfqLog.awardCalls != 0 {
				t.Errorf("a refused award reached the store")
			}
		})
	}
}

func TestRFQAward_LateWithReasonAndStaleVersion(t *testing.T) {
	h := rfqHarness(t)
	rec := h.do(call{method: "POST", path: rfqPath(":award"), duties: rfqAwardDuty, headers: rfqActionHeaders,
		body: `{"awards":[{"lineId":"` + rfqLine1 + `","replyId":"` + replyC1 + `","overrideReason":"Only vendor with stock"}]}`})
	if rec.Code != 200 || len(h.fake.rfqLog.orders) != 1 || h.fake.rfqLog.orders[0].Order.VendorID != rfqVendorC {
		t.Fatalf("late award with a reason: %d %s", rec.Code, rec.Body)
	}

	// A stale If-Match builds no orders; the store decides (replay or 409).
	h = rfqHarness(t)
	rec = h.do(call{method: "POST", path: rfqPath(":award"), duties: rfqAwardDuty,
		headers: map[string]string{"Idempotency-Key": "k-2", "If-Match": `"2"`},
		body:    `{"awards":[{"lineId":"` + rfqLine1 + `","replyId":"` + replyB1 + `"}]}`})
	if rec.Code != 409 || problemCode(t, rec) != store.CodeVersionConflict {
		t.Fatalf("stale award: %d %s", rec.Code, rec.Body)
	}
	if h.fake.rfqLog.awardCalls != 1 || h.fake.rfqLog.orders != nil {
		t.Errorf("stale award should reach the store once without orders, got %d calls and %v", h.fake.rfqLog.awardCalls, h.fake.rfqLog.orders)
	}
}

func TestRecordRFQReplies_ValidatesAndWarnsWhenLate(t *testing.T) {
	h := rfqHarness(t)
	body := `{"vendorId":"` + rfqVendorC + `","currency":"USD","replies":[{"lineId":"` + rfqLine1 + `","unitPrice":"8.00","leadTimeDays":3,"notes":" fast "}]}`
	rec := h.do(call{method: "POST", path: rfqPath("/replies"), duties: rfqMaintainDuty, body: body, headers: idem("k-r")})
	if rec.Code != 201 {
		t.Fatalf("status %d: %s", rec.Code, rec.Body)
	}
	got := h.fake.rfqLog.replies
	if h.fake.rfqLog.replyBy != rfqVendorC || len(got) != 1 || !got[0].PriceUnit.Equal(decimal.NewFromInt(1)) || got[0].Notes != "fast" {
		t.Errorf("store got %+v from %s", got, h.fake.rfqLog.replyBy)
	}
	if !got[0].ReceivedAt.Equal(time.Date(2026, 3, 1, 15, 0, 0, 0, time.UTC)) {
		t.Errorf("received at %s, want the API clock", got[0].ReceivedAt)
	}
	var res rfqReplyResult
	if err := json.Unmarshal(rec.Body.Bytes(), &res); err != nil {
		t.Fatal(err)
	}
	// The stored reply from C to line 1 is late, so the answer warns (BR-17.11).
	if len(res.Warnings) != 1 || res.Warnings[0].Code != domain.CodeRFQLate || res.Warnings[0].LineNo != 1 {
		t.Errorf("warnings %+v, want PRC-RFQ-003 on line 1", res.Warnings)
	}
	if res.ID != rfqID || len(res.Replies) != 4 {
		t.Errorf("response does not carry the RFQ: %s", rec.Body)
	}

	line := func(extra string) string {
		return `{"vendorId":"` + rfqVendorA + `","currency":"USD","replies":[{"lineId":"` + rfqLine1 + `",` + extra + `}]}`
	}
	rejections := map[string]struct{ body, field string }{
		"currency":         {strings.Replace(line(`"unitPrice":"1"`), `"USD"`, `"usd"`, 1), "currency"},
		"negative price":   {line(`"unitPrice":"-1"`), "replies[0].unitPrice"},
		"missing price":    {line(`"leadTimeDays":1`), "replies[0].unitPrice"},
		"zero price unit":  {line(`"unitPrice":"1","priceUnit":"0"`), "replies[0].priceUnit"},
		"negative lead":    {line(`"unitPrice":"1","leadTimeDays":-1`), "replies[0].leadTimeDays"},
		"vendor not UUID":  {strings.Replace(line(`"unitPrice":"1"`), rfqVendorA, "acme", 1), "vendorId"},
		"line twice":       {`{"vendorId":"` + rfqVendorA + `","currency":"USD","replies":[{"lineId":"` + rfqLine1 + `","unitPrice":"1"},{"lineId":"` + rfqLine1 + `","unitPrice":"2"}]}`, "replies[1].lineId"},
		"no reply lines":   {`{"vendorId":"` + rfqVendorA + `","currency":"USD","replies":[]}`, "replies"},
		"notes over limit": {line(`"unitPrice":"1","notes":"` + strings.Repeat("x", maxNotes+1) + `"`), "replies[0].notes"},
	}
	for name, tc := range rejections {
		t.Run(name, func(t *testing.T) {
			h := rfqHarness(t)
			rec := h.do(call{method: "POST", path: rfqPath("/replies"), duties: rfqMaintainDuty, body: tc.body, headers: idem("k")})
			var p httpx.Problem
			_ = json.Unmarshal(rec.Body.Bytes(), &p)
			if rec.Code != 422 || p.Errors[tc.field] == "" {
				t.Fatalf("status %d errors %v, want 422 naming %s", rec.Code, p.Errors, tc.field)
			}
			if h.fake.rfqLog.replies != nil {
				t.Errorf("a rejected reply reached the store")
			}
		})
	}
}

func TestCompareRFQ_RuleOverrideAndErrors(t *testing.T) {
	h := rfqHarness(t)
	cmp := compare(t, h, "?rule=LEAD_TIME")
	if cmp.RankingRule != domain.RankLeadTime || cmp.Lines[0].Ranked[0].ReplyID != replyA1 {
		t.Errorf("LEAD_TIME should rank A (5 days) before B (12 days): %+v", cmp.Lines[0].Ranked)
	}
	if len(cmp.Lines[1].Ranked) != 1 || len(cmp.Lines[1].Excluded) != 0 {
		t.Errorf("line 2 %+v, want A's one reply", cmp.Lines[1])
	}
	if rec := h.do(call{method: "GET", path: rfqPath("/compare?rule=CHEAPEST"), duties: rfqMaintainDuty}); rec.Code != 422 {
		t.Errorf("unknown rule: status %d", rec.Code)
	}
	if rec := h.do(call{method: "GET", path: "/v1/procurement/rfqs/" + ids.New() + "/compare", duties: rfqMaintainDuty}); rec.Code != 404 || problemCode(t, rec) != store.CodeRFQNotFound {
		t.Errorf("missing RFQ: %d %s", rec.Code, rec.Body)
	}
}

func TestListRFQs_ParsesStatusesAndPaging(t *testing.T) {
	h := newHarness(t)
	cursor := ids.New()
	rec := h.do(call{method: "GET", path: "/v1/procurement/rfqs?status=SENT,REPLIES_OPEN&limit=20&cursor=" + cursor, duties: rfqAwardDuty})
	if rec.Code != 200 {
		t.Fatalf("status %d: %s", rec.Code, rec.Body)
	}
	if got := h.fake.rfqLog.filter.Statuses; len(got) != 2 || got[0] != domain.RFQSent || got[1] != domain.RFQRepliesOpen {
		t.Errorf("statuses %v", got)
	}
	if h.fake.lastAfter != cursor || h.fake.lastLimit != 20 {
		t.Errorf("paging %s/%d", h.fake.lastAfter, h.fake.lastLimit)
	}
	if rec := h.do(call{method: "GET", path: "/v1/procurement/rfqs?status=SENT,OPEN", duties: rfqMaintainDuty}); rec.Code != 422 {
		t.Errorf("bad status: %d", rec.Code)
	}
}
