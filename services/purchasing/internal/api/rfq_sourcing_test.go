package api

import (
	"net/http"
	"strings"
	"testing"

	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/services/purchasing/internal/domain"
	"github.com/BenjaminSRussell/passage/services/purchasing/internal/store"
)

const fromReqPath = "/v1/procurement/rfqs:from-requisitions"

func fromReqBody(lines []store.SourcingLine, extra string) string {
	parts := make([]string, 0, len(lines))
	for _, l := range lines {
		parts = append(parts, `"`+l.ID+`"`)
	}
	return `{` + extra + `"replyDeadline":"2026-03-05T17:00:00Z","requisitionLineIds":[` + strings.Join(parts, ",") + `]}`
}

func TestCreateRFQFromRequisitions_CopiesLinesAndKeepsLinks(t *testing.T) {
	h := newHarness(t)
	h.fake.sourcing = sourcing(t, leA, []fixtureLine{{"", "", "2027-03-01"}, {"", "", "2027-04-01"}})
	rec := h.do(call{method: "POST", path: fromReqPath, duties: rfqMaintainDuty, body: fromReqBody(h.fake.sourcing, ""), headers: idem("k-from")})
	if rec.Code != http.StatusCreated {
		t.Fatalf("status %d: %s", rec.Code, rec.Body)
	}
	got := h.fake.rfqLog.created
	if got == nil || got.LegalEntityID != leA || len(got.Lines) != 2 {
		t.Fatalf("created %+v", got)
	}
	for i, l := range got.Lines {
		src := h.fake.sourcing[i]
		if l.RequisitionLineID != src.ID || l.ProductID != src.ProductID || l.Unit != "EA" || !l.Qty.Equal(src.Qty) ||
			l.RequiredDate == nil || l.RequiredDate.String() != src.RequestedDate.String() {
			t.Errorf("line %d = %+v; want the requisition line copied", i, l)
		}
	}
	if h.fake.rfqLog.createIdem.Key != "k-from" {
		t.Errorf("idempotency claim %+v", h.fake.rfqLog.createIdem)
	}
}

func TestCreateRFQFromRequisitions_Rejections(t *testing.T) {
	tests := []struct {
		name   string
		body   func(l []store.SourcingLine) string
		duties []string
		scope  string
		status int
		code   string
	}{
		{"no lines", func([]store.SourcingLine) string {
			return `{"replyDeadline":"2026-03-05T17:00:00Z","requisitionLineIds":[]}`
		}, rfqMaintainDuty, "", 422, codeInvalid},
		{"unknown line", func([]store.SourcingLine) string {
			return `{"replyDeadline":"2026-03-05T17:00:00Z","requisitionLineIds":["` + ids.New() + `"]}`
		}, rfqMaintainDuty, "", 404, store.CodeRequisitionLineNotFound},
		{"deadline passed", func(l []store.SourcingLine) string {
			return strings.Replace(fromReqBody(l, ""), "2026-03-05T17:00:00Z", "2026-03-01T14:00:00Z", 1)
		}, rfqMaintainDuty, "", 422, codeInvalid},
		{"legal entity outside scope", func(l []store.SourcingLine) string { return fromReqBody(l, "") }, rfqMaintainDuty, ids.New(), 403, codeScope},
		{"needs RFQMaintain", func(l []store.SourcingLine) string { return fromReqBody(l, "") }, []string{dutyRFQAward}, "", 403, codeScope},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			h := newHarness(t)
			h.fake.sourcing = sourcing(t, leA, []fixtureLine{{"", "", "2027-03-01"}})
			rec := h.do(call{method: "POST", path: fromReqPath, duties: tc.duties, scope: tc.scope, body: tc.body(h.fake.sourcing), headers: idem("k")})
			if rec.Code != tc.status {
				t.Fatalf("got %d, want %d: %s", rec.Code, tc.status, rec.Body)
			}
			if tc.code != "" && problemCode(t, rec) != tc.code && tc.status != 403 {
				t.Errorf("code %s, want %s", problemCode(t, rec), tc.code)
			}
			if h.fake.rfqLog.created != nil {
				t.Error("a rejected request reached the store")
			}
		})
	}
	t.Run("two legal entities", func(t *testing.T) {
		h := newHarness(t)
		h.fake.sourcing = append(sourcing(t, leA, []fixtureLine{{"", "", "2027-03-01"}}), sourcing(t, ids.New(), []fixtureLine{{"", "", "2027-03-01"}})...)
		rec := h.do(call{method: "POST", path: fromReqPath, duties: rfqMaintainDuty, body: fromReqBody(h.fake.sourcing, ""), headers: idem("k")})
		if rec.Code != 422 || problemCode(t, rec) != store.CodeRequisitionLegalEntity {
			t.Fatalf("got %d %s", rec.Code, rec.Body)
		}
	})
}

func TestBuildAwardOrders_CarriesRequisitionLine(t *testing.T) {
	rfq := *rfqFixture()
	reqLine := ids.New()
	rfq.Lines[0].RequisitionLineID = reqLine
	orders, err := buildAwardOrders(rfq, []domain.Award{{LineID: rfqLine1, ReplyID: replyB1}, {LineID: rfqLine2, ReplyID: replyA2}}, mustDay(t, "2026-03-01"))
	if err != nil {
		t.Fatal(err)
	}
	var withLink, without int
	for _, o := range orders {
		for _, l := range o.Order.Lines {
			switch l.RequisitionLineID {
			case reqLine:
				withLink++
			case "":
				without++
			}
		}
	}
	if withLink != 1 || without != 1 {
		t.Fatalf("PO lines with the requisition line %d, without %d; want 1 and 1: %+v", withLink, without, orders)
	}
}
