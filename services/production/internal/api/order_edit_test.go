package api

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"

	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/services/production/internal/domain"
	"github.com/BenjaminSRussell/passage/services/production/internal/store"
)

func (f *fakeStore) EditOrder(_ context.Context, in store.OrderEdit) (store.Order, error) {
	f.edits = append(f.edits, in)
	if f.editErr != nil {
		return store.Order{}, f.editErr
	}
	return store.Order{ID: in.OrderID, Version: in.IfMatch + 1, OrderBOMModified: true}, nil
}

func (f *fakeStore) OrderEdits(context.Context, string) ([]store.OrderEditEntry, error) {
	return f.editLog, f.getErr
}

func (f *fakeStore) WIPReport(_ context.Context, wf store.WIPFilter) (store.WIPReport, error) {
	f.wipFilters = append(f.wipFilters, wf)
	return f.wip, f.getErr
}

func (f *fakeStore) VarianceReport(_ context.Context, vf store.VarianceFilter) (store.VarianceReport, error) {
	f.varFilters = append(f.varFilters, vf)
	return f.variances, f.getErr
}

func editCall(orderID, body string, duties ...string) call {
	return call{method: http.MethodPatch, path: "/v1/production/orders/" + orderID + "/bom", body: body, duties: duties,
		headers: map[string]string{"If-Match": `"3"`}}
}

func TestEditOrder_PassesTheEditsAndTheReleaseDuty(t *testing.T) {
	f := &fakeStore{}
	_, h := newServer(t, f)
	id, item := ids.New(), ids.New()
	body := `{"lines":[{"op":"add","lineNo":"40","itemId":"` + item + `","qtyPer":"0.25","scrapPct":"2","flushing":"finish"},
		{"op":"change","lineNo":"10","qtyPer":"3.1"}],"operations":[{"op":"change","operNumber":10,"runTimeHPer":"0.4"}]}`
	rec := do(h, editCall(id, body, DutyWrite, DutyBOMAfterRelease))
	if rec.Code != http.StatusOK || rec.Header().Get("ETag") != `"4"` {
		t.Fatalf("edit: %d %s", rec.Code, rec.Body.String())
	}
	var order store.Order
	if err := json.Unmarshal(rec.Body.Bytes(), &order); err != nil || !order.OrderBOMModified {
		t.Fatalf("body: %s", rec.Body.String())
	}
	if len(f.edits) != 1 {
		t.Fatalf("store calls: %d", len(f.edits))
	}
	e := f.edits[0]
	if e.OrderID != id || e.IfMatch != 3 || !e.ReleaseDuty || e.Actor != "planner-1" || len(e.Edits.Lines) != 2 || len(e.Edits.Operations) != 1 {
		t.Fatalf("edit passed = %+v", e)
	}
	added := e.Edits.Lines[0]
	if added.Op != domain.EditAdd || *added.ItemID != item || !added.QtyPer.Equal(decimal.RequireFromString("0.25")) || *added.Flushing != domain.FlushFinish {
		t.Fatalf("added line = %+v", added)
	}
	if e.Edits.Lines[1].ItemID != nil || e.Edits.Lines[1].ScrapPct != nil {
		t.Fatal("a change must leave the fields it does not carry unset")
	}

	// Without the after-release duty the request still reaches the store, which refuses only Released orders.
	do(h, editCall(id, body, DutyWrite))
	if f.edits[1].ReleaseDuty {
		t.Fatal("release duty passed without the duty")
	}
}

func TestEditOrder_Refusals(t *testing.T) {
	id := ids.New()
	cases := []struct {
		name   string
		call   call
		status int
		code   string
		fields []string
	}{
		{name: "missing duty", call: editCall(id, `{"lines":[{"op":"remove","lineNo":"10"}]}`, DutyRead), status: http.StatusForbidden, code: "AUTH-0003"},
		{name: "missing If-Match", call: call{method: http.MethodPatch, path: "/v1/production/orders/" + id + "/bom", body: `{"lines":[{"op":"remove","lineNo":"10"}]}`,
			duties: []string{DutyWrite}}, status: http.StatusPreconditionRequired, code: "precondition_required"},
		{name: "unknown order id", call: editCall("nope", `{}`, DutyWrite), status: http.StatusNotFound, code: store.CodeOrderNotFound},
		{name: "unknown field", call: editCall(id, `{"lines":[],"bom":[]}`, DutyWrite), status: http.StatusBadRequest, code: validationCode},
		{name: "no edits", call: editCall(id, `{"lines":[],"operations":[]}`, DutyWrite), status: http.StatusBadRequest, code: validationCode, fields: []string{"lines"}},
		{name: "bad shapes", call: editCall(id, `{"lines":[{"op":"swap","lineNo":"","itemId":"x","qtyPer":"-1","flushing":"weekly"},
			{"op":"add","lineNo":"50"},{"op":"remove","lineNo":"10","qtyPer":"1"}],
			"operations":[{"op":"add","operNumber":0,"resourceId":"x","scrapPct":"0.1234567"},{"op":"add","operNumber":30}]}`, DutyWrite),
			status: http.StatusBadRequest, code: validationCode,
			fields: []string{"lines[0].op", "lines[0].lineNo", "lines[0].itemId", "lines[0].qtyPer", "lines[0].flushing", "lines[1]", "lines[2]",
				"operations[0].operNumber", "operations[0].resourceId", "operations[0].scrapPct", "operations[1]"}},
		{name: "queue and transit hours", call: editCall(id, `{"operations":[{"op":"change","operNumber":10,"queueBeforeH":"-1","transitH":"0.1234567"},
			{"op":"remove","operNumber":20,"queueAfterH":"1"}]}`, DutyWrite),
			status: http.StatusBadRequest, code: validationCode, fields: []string{"operations[0].queueBeforeH", "operations[0].transitH", "operations[1]"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := &fakeStore{}
			_, h := newServer(t, f)
			p := expectProblem(t, do(h, tc.call), tc.status, tc.code)
			for _, name := range tc.fields {
				if _, ok := p.Errors[name]; !ok {
					t.Errorf("no error for %s: %v", name, p.Errors)
				}
			}
			if len(f.edits) != 0 {
				t.Fatal("a refused request reached the store")
			}
		})
	}
}

func TestEditOrder_StoreRefusalIsPassedThrough(t *testing.T) {
	f := &fakeStore{editErr: httpx.NewProblem(http.StatusConflict, domain.CodeEditNotAllowed, "started")}
	a, h := newServer(t, f)
	expectProblem(t, do(h, editCall(ids.New(), `{"lines":[{"op":"remove","lineNo":"10"}]}`, DutyWrite)), http.StatusConflict, domain.CodeEditNotAllowed)
	if a.refused.Load() != 1 {
		t.Fatalf("refusals counted = %d", a.refused.Load())
	}
}

func TestOrderEdits_ServesTheAuditTrail(t *testing.T) {
	f := &fakeStore{editLog: []store.OrderEditEntry{{OrderVersion: 2, Seq: 1, Target: domain.TargetLine, Op: domain.EditRemove, Key: "10",
		Before: json.RawMessage(`{"lineNo":"10"}`), After: json.RawMessage("null"), Actor: "planner-1"}}}
	_, h := newServer(t, f)
	id := ids.New()
	expectProblem(t, do(h, call{method: http.MethodGet, path: "/v1/production/orders/" + id + "/bom-edits"}), http.StatusForbidden, "AUTH-0003")
	rec := do(h, call{method: http.MethodGet, path: "/v1/production/orders/" + id + "/bom-edits", duties: []string{DutyRead}})
	var body struct {
		OrderID string `json:"orderId"`
		Items   []struct {
			Op     string          `json:"op"`
			Before json.RawMessage `json:"before"`
			After  json.RawMessage `json:"after"`
		} `json:"items"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil || rec.Code != http.StatusOK || body.OrderID != id || len(body.Items) != 1 {
		t.Fatalf("edits: %d %s", rec.Code, rec.Body.String())
	}
	if body.Items[0].Op != "remove" || string(body.Items[0].After) != "null" || string(body.Items[0].Before) != `{"lineNo":"10"}` {
		t.Fatalf("entry = %+v", body.Items[0])
	}
}

func TestWIPReport_FormatsAndFilters(t *testing.T) {
	order, site := ids.New(), ids.New()
	f := &fakeStore{wip: store.WIPReport{
		Items: []store.WIPRow{{OrderID: order, OrderNumber: "PO-000007", Status: domain.Started, Currency: "JPY",
			Consumption: decimal.RequireFromString("3050"), RAFValue: decimal.RequireFromString("1200"), Balance: decimal.RequireFromString("1850")}},
		Totals:     []store.WIPTotal{{Currency: "JPY", Orders: 1, Consumption: decimal.RequireFromString("3050"), RAFValue: decimal.RequireFromString("1200"), Balance: decimal.RequireFromString("1850")}},
		NextCursor: order,
	}}
	_, h := newServer(t, f)
	expectProblem(t, do(h, call{method: http.MethodGet, path: "/v1/production/wip-balances", duties: []string{DutyRead}}), http.StatusForbidden, "AUTH-0003")
	rec := do(h, call{method: http.MethodGet, path: "/v1/production/wip-balances?status=Started&asOf=2026-10-31&siteId=" + site + "&limit=10", duties: []string{DutyCostRead}})
	var v wipReportView
	if err := json.Unmarshal(rec.Body.Bytes(), &v); err != nil || rec.Code != http.StatusOK {
		t.Fatalf("report: %d %s", rec.Code, rec.Body.String())
	}
	// JPY has no minor unit: amounts are whole yen.
	if v.AsOf != "2026-10-31" || len(v.Items) != 1 || v.Items[0].WIPBalance != "1850" || v.Items[0].Settled != "0" || v.Totals[0].Orders != 1 || v.NextCursor != order {
		t.Fatalf("view = %+v", v)
	}
	got := f.wipFilters[0]
	if got.Status != domain.Started || got.AsOf != "2026-10-31" || got.SiteID != site || got.Limit != 10 {
		t.Fatalf("filter = %+v", got)
	}

	// Without asOf the report is as of today.
	do(h, call{method: http.MethodGet, path: "/v1/production/wip-balances", duties: []string{DutyCostRead}})
	if !isDate(f.wipFilters[1].AsOf) || f.wipFilters[1].Limit != defaultPageSize {
		t.Fatalf("default filter = %+v", f.wipFilters[1])
	}

	p := expectProblem(t, do(h, call{method: http.MethodGet, path: "/v1/production/wip-balances?status=Open&asOf=31.10.2026&siteId=x&limit=0",
		duties: []string{DutyCostRead}}), http.StatusBadRequest, validationCode)
	for _, name := range []string{"status", "asOf", "siteId", "limit"} {
		if _, ok := p.Errors[name]; !ok {
			t.Errorf("no error for %s: %v", name, p.Errors)
		}
	}
}

func TestVarianceReport_FormatsTotalsAndNet(t *testing.T) {
	pct := decimal.RequireFromString("1.666666")
	f := &fakeStore{variances: store.VarianceReport{
		Items: []store.VarianceRow{
			{ID: ids.New(), OrderID: ids.New(), OrderNumber: "PO-000001", Currency: "USD", Type: "lot-size", Group: domain.GroupMaterial,
				Amount: decimal.RequireFromString("50"), Planned: decimal.RequireFromString("3000"), PctOfPlanned: &pct, PostedOn: "2026-10-20"},
			{ID: ids.New(), OrderID: ids.New(), OrderNumber: "PO-000001", Currency: "USD", Type: "production-price", Group: domain.GroupSubcontract,
				Amount: decimal.RequireFromString("-4"), Planned: decimal.Zero, PostedOn: "2026-10-20"},
		},
		Totals: []store.VarianceTotal{{Currency: "USD", Type: "lot-size", Amount: decimal.RequireFromString("50")},
			{Currency: "USD", Type: "production-price", Amount: decimal.RequireFromString("-99")}},
	}}
	_, h := newServer(t, f)
	rec := do(h, call{method: http.MethodGet, path: "/v1/production/variances?varianceType=lot-size&from=2026-10-01&to=2026-10-31&itemGroup=FG",
		duties: []string{DutyCostRead}})
	var v varianceReportView
	if err := json.Unmarshal(rec.Body.Bytes(), &v); err != nil || rec.Code != http.StatusOK {
		t.Fatalf("report: %d %s", rec.Code, rec.Body.String())
	}
	if v.Items[0].Amount != "50.00" || v.Items[0].PctOfPlanned == nil || *v.Items[0].PctOfPlanned != "1.67" || v.Items[1].PctOfPlanned != nil {
		t.Fatalf("items = %+v", v.Items)
	}
	if len(v.Net) != 1 || v.Net[0].Amount != "-49.00" || len(v.Totals) != 2 {
		t.Fatalf("totals %+v net %+v", v.Totals, v.Net)
	}
	got := f.varFilters[0]
	if got.Type != "lot-size" || got.From != "2026-10-01" || got.To != "2026-10-31" || got.ItemGroup != "FG" {
		t.Fatalf("filter = %+v", got)
	}
	p := expectProblem(t, do(h, call{method: http.MethodGet, path: "/v1/production/variances?varianceType=scrap&from=2026-11-01&to=2026-10-01&orderId=x",
		duties: []string{DutyCostRead}}), http.StatusBadRequest, validationCode)
	for _, name := range []string{"varianceType", "to", "orderId"} {
		if _, ok := p.Errors[name]; !ok {
			t.Errorf("no error for %s: %v", name, p.Errors)
		}
	}
}
