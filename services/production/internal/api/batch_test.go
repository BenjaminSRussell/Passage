package api

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"testing"

	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/services/production/internal/domain"
	"github.com/BenjaminSRussell/passage/services/production/internal/store"
)

func (f *fakeStore) SplitOrder(_ context.Context, in store.Split) (store.SplitResult, error) {
	f.splits = append(f.splits, in)
	if f.batchErr != nil {
		return store.SplitResult{}, f.batchErr
	}
	return store.SplitResult{Original: store.Order{ID: in.OrderID, Version: in.IfMatch + 1}, New: store.Order{ID: ids.New(), Version: 1},
		Replayed: len(f.splits) > 1}, nil
}

func (f *fakeStore) MergeOrders(_ context.Context, in store.Merge) (store.MergeResult, error) {
	f.merges = append(f.merges, in)
	if f.batchErr != nil {
		return store.MergeResult{}, f.batchErr
	}
	return store.MergeResult{Survivor: store.Order{ID: in.Orders[0].OrderID, Version: in.Orders[0].Version + 1}, Merged: []string{in.Orders[1].OrderID}}, nil
}

// BulkTransition fails every second order, so the response mixes results.
func (f *fakeStore) BulkTransition(_ context.Context, in store.Bulk) []store.BulkItem {
	f.bulks = append(f.bulks, in)
	out := []store.BulkItem{}
	for i, id := range in.OrderIDs {
		if i%2 == 1 {
			out = append(out, store.BulkItem{OrderID: id, Code: domain.CodeIllegalTransition, Detail: "not allowed"})
			continue
		}
		out = append(out, store.BulkItem{OrderID: id, OK: true, Status: domain.Released, Version: 2})
	}
	return out
}

func bulkCall(body string, duties ...string) call {
	return call{method: http.MethodPost, path: "/v1/production/orders:bulk-status", body: body, duties: duties,
		headers: map[string]string{idempotencyName: "bulk-1"}}
}

func TestBulkStatus_ReportsPerOrderResults(t *testing.T) {
	f := &fakeStore{}
	_, h := newServer(t, f)
	a, b, c := ids.New(), ids.New(), ids.New()
	rec := do(h, bulkCall(fmt.Sprintf(`{"command":"release","orderIds":[%q,%q,%q]}`, a, b, c), DutyRelease))
	if rec.Code != http.StatusOK {
		t.Fatalf("bulk: %d %s", rec.Code, rec.Body.String())
	}
	var resp bulkResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	if resp.Succeeded != 2 || resp.Failed != 1 || resp.Code != domain.CodeBulkPartial || !strings.Contains(resp.Detail, "2 succeeded, 1 failed") ||
		len(resp.Results) != 3 || resp.Results[1].OrderID != b || resp.Results[1].OK {
		t.Fatalf("response: %+v", resp)
	}
	in := f.bulks[0]
	if in.Command != domain.CmdRelease || in.IdempotencyKey != "bulk-1" || in.Actor != "planner-1" || len(in.OrderIDs) != 3 ||
		in.RequestHash != commandHash(domain.CmdRelease, "", decimal.Zero, decimal.Zero) {
		t.Fatalf("store input: %+v", in)
	}
}

func TestBulkStatus_Validation(t *testing.T) {
	many := make([]string, domain.MaxBulkOrders+1)
	for i := range many {
		many[i] = ids.New()
	}
	tooMany, _ := json.Marshal(map[string]any{"command": "release", "orderIds": many})
	id := ids.New()
	cases := map[string]struct{ body, field string }{
		"no orders":           {`{"command":"release","orderIds":[]}`, "orderIds"},
		"501 orders":          {string(tooMany), "orderIds"},
		"a duplicate":         {fmt.Sprintf(`{"command":"release","orderIds":[%q,%q]}`, id, id), "orderIds"},
		"not a UUID":          {`{"command":"release","orderIds":["PO-1"]}`, "orderIds[0]"},
		"unknown command":     {fmt.Sprintf(`{"command":"explode","orderIds":[%q]}`, id), "command"},
		"report needs input":  {fmt.Sprintf(`{"command":"report-finished","orderIds":[%q]}`, id), "command"},
		"reset needs a state": {fmt.Sprintf(`{"command":"reset-status","orderIds":[%q]}`, id), "command"},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			f := &fakeStore{}
			_, h := newServer(t, f)
			p := expectProblem(t, do(h, bulkCall(tc.body, DutyRelease, DutyEnd)), http.StatusBadRequest, validationCode)
			if _, ok := p.Errors[tc.field]; !ok {
				t.Fatalf("errors %v lack %s", p.Errors, tc.field)
			}
			if len(f.bulks) != 0 {
				t.Fatal("store called on an invalid batch")
			}
		})
	}
	f := &fakeStore{}
	_, h := newServer(t, f)
	noKey := bulkCall(fmt.Sprintf(`{"command":"release","orderIds":[%q]}`, id), DutyRelease)
	delete(noKey.headers, idempotencyName)
	expectProblem(t, do(h, noKey), http.StatusBadRequest, validationCode)
}

func TestBulkStatus_Duties(t *testing.T) {
	f := &fakeStore{}
	_, h := newServer(t, f)
	body := fmt.Sprintf(`{"command":"end","orderIds":[%q]}`, ids.New())
	// The route needs the release duty, and :end its own End duty as well.
	expectProblem(t, do(h, bulkCall(body, DutyEnd)), http.StatusForbidden, "AUTH-0003")
	expectProblem(t, do(h, bulkCall(body, DutyRelease)), http.StatusForbidden, "AUTH-0003")
	if rec := do(h, bulkCall(body, DutyRelease, DutyEnd, DutyZeroOutput)); rec.Code != http.StatusOK {
		t.Fatalf("with both duties: %d %s", rec.Code, rec.Body.String())
	}
	if len(f.bulks) != 1 || !f.bulks[0].ZeroOutputDuty {
		t.Fatalf("store input: %+v", f.bulks)
	}
}

func splitCall(id, body string, duties ...string) call {
	return call{method: http.MethodPost, path: "/v1/production/orders/" + id + ":split", body: body, duties: duties,
		headers: map[string]string{idempotencyName: "split-1", "If-Match": `"3"`}}
}

func TestSplit_PassesTheRequestToTheStore(t *testing.T) {
	f := &fakeStore{}
	_, h := newServer(t, f)
	id := ids.New()
	body := `{"splitQty":"40","minimumQty":"10","multiple":"5"}`
	rec := do(h, splitCall(id, body, DutyRelease))
	if rec.Code != http.StatusCreated || rec.Header().Get("ETag") != `"4"` || !strings.HasPrefix(rec.Header().Get("Location"), "/v1/production/orders/") {
		t.Fatalf("split: %d %s %v", rec.Code, rec.Body.String(), rec.Header())
	}
	var res struct {
		Original store.Order `json:"original"`
		New      store.Order `json:"new"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &res); err != nil || res.Original.ID != id || res.New.ID == "" {
		t.Fatalf("body: %s", rec.Body.String())
	}
	in := f.splits[0]
	if in.OrderID != id || in.IfMatch != 3 || !in.SplitQty.Equal(decimal.NewFromInt(40)) || !in.MinQty.Equal(decimal.NewFromInt(10)) ||
		!in.Multiple.Equal(decimal.NewFromInt(5)) || in.IdempotencyKey != "split-1" || in.RequestHash == "" {
		t.Fatalf("store input: %+v", in)
	}
	again := do(h, splitCall(id, body, DutyRelease))
	if again.Code != http.StatusOK || again.Header().Get("Idempotent-Replayed") != "true" || f.splits[1].RequestHash != in.RequestHash {
		t.Fatalf("replay: %d %v", again.Code, again.Header())
	}
	do(h, splitCall(id, `{"splitQty":"30"}`, DutyRelease))
	if len(f.splits) != 3 || f.splits[2].RequestHash == in.RequestHash {
		t.Fatal("another split quantity must fingerprint differently")
	}
}

// An allocation the request does not name stays empty: the store takes it from the site's
// production parameters (proportional without any).
func TestSplit_AllocationLeftToTheSiteParameters(t *testing.T) {
	f := &fakeStore{}
	_, h := newServer(t, f)
	id := ids.New()
	do(h, splitCall(id, `{"splitQty":"40"}`, DutyRelease))
	do(h, splitCall(id, `{"splitQty":"40","allocation":"keep-on-original"}`, DutyRelease))
	if len(f.splits) != 2 || f.splits[0].Allocation != "" || f.splits[1].Allocation != domain.AllocKeepOnOriginal ||
		f.splits[0].RequestHash == f.splits[1].RequestHash {
		t.Fatalf("allocations: %+v", f.splits)
	}
}

func TestSplit_ValidationDutyAndRefusals(t *testing.T) {
	id := ids.New()
	for body, field := range map[string]string{
		`{}`:                                 "splitQty",
		`{"splitQty":"0"}`:                   "splitQty",
		`{"splitQty":"1.1234567"}`:           "splitQty",
		`{"splitQty":"5","minimumQty":"-1"}`: "minimumQty",
		`{"splitQty":"5","multiple":"-2"}`:   "multiple",
		`{"splitQty":"5","allocation":"x"}`:  "allocation",
	} {
		f := &fakeStore{}
		_, h := newServer(t, f)
		p := expectProblem(t, do(h, splitCall(id, body, DutyRelease)), http.StatusBadRequest, validationCode)
		if _, ok := p.Errors[field]; !ok || len(f.splits) != 0 {
			t.Fatalf("%s: errors %v", body, p.Errors)
		}
	}
	f := &fakeStore{}
	_, h := newServer(t, f)
	expectProblem(t, do(h, splitCall(id, `{"splitQty":"5"}`, DutyWrite)), http.StatusForbidden, "AUTH-0003")
	noMatch := splitCall(id, `{"splitQty":"5"}`, DutyRelease)
	delete(noMatch.headers, "If-Match")
	expectProblem(t, do(h, noMatch), http.StatusPreconditionRequired, "precondition_required")
	expectProblem(t, do(h, splitCall("not-a-uuid", `{"splitQty":"5"}`, DutyRelease)), http.StatusNotFound, unknownActionCode)

	f.batchErr = httpx.NewProblem(http.StatusUnprocessableEntity, domain.CodeSplitQuantity, "split quantity 5 is not a multiple of 4")
	expectProblem(t, do(h, splitCall(id, `{"splitQty":"5"}`, DutyRelease)), http.StatusUnprocessableEntity, domain.CodeSplitQuantity)
}

func mergeCall(body string, duties ...string) call {
	return call{method: http.MethodPost, path: "/v1/production/orders:merge", body: body, duties: duties,
		headers: map[string]string{idempotencyName: "merge-1"}}
}

func TestMerge_PassesTheRequestToTheStore(t *testing.T) {
	f := &fakeStore{}
	_, h := newServer(t, f)
	a, b := ids.New(), ids.New()
	rec := do(h, mergeCall(fmt.Sprintf(`{"orders":[{"orderId":%q,"version":2},{"orderId":%q,"version":1}]}`, a, b), DutyRelease))
	if rec.Code != http.StatusOK || rec.Header().Get("ETag") != `"3"` {
		t.Fatalf("merge: %d %s", rec.Code, rec.Body.String())
	}
	var res struct {
		Survivor store.Order `json:"survivor"`
		Merged   []string    `json:"merged"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &res); err != nil || res.Survivor.ID != a || len(res.Merged) != 1 || res.Merged[0] != b {
		t.Fatalf("body: %s", rec.Body.String())
	}
	in := f.merges[0]
	if len(in.Orders) != 2 || in.Orders[0] != (store.MergeRef{OrderID: a, Version: 2}) || in.IdempotencyKey != "merge-1" || in.RequestHash == "" {
		t.Fatalf("store input: %+v", in)
	}
}

func TestMerge_Validation(t *testing.T) {
	a, b := ids.New(), ids.New()
	cases := map[string]struct{ body, field string }{
		"one order":     {fmt.Sprintf(`{"orders":[{"orderId":%q,"version":1}]}`, a), "orders"},
		"twice":         {fmt.Sprintf(`{"orders":[{"orderId":%q,"version":1},{"orderId":%q,"version":1}]}`, a, a), "orders[1].orderId"},
		"not a UUID":    {fmt.Sprintf(`{"orders":[{"orderId":%q,"version":1},{"orderId":"x","version":1}]}`, a), "orders[1].orderId"},
		"no version":    {fmt.Sprintf(`{"orders":[{"orderId":%q,"version":1},{"orderId":%q}]}`, a, b), "orders[1].version"},
		"unknown field": {`{"orders":[],"survivor":"x"}`, ""},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			f := &fakeStore{}
			_, h := newServer(t, f)
			p := expectProblem(t, do(h, mergeCall(tc.body, DutyRelease)), http.StatusBadRequest, validationCode)
			if _, ok := p.Errors[tc.field]; tc.field != "" && !ok {
				t.Fatalf("errors %v lack %s", p.Errors, tc.field)
			}
			if len(f.merges) != 0 {
				t.Fatal("store called on an invalid merge")
			}
		})
	}
	f := &fakeStore{}
	_, h := newServer(t, f)
	body := fmt.Sprintf(`{"orders":[{"orderId":%q,"version":1},{"orderId":%q,"version":1}]}`, a, b)
	expectProblem(t, do(h, mergeCall(body, DutyWrite)), http.StatusForbidden, "AUTH-0003")
	f.batchErr = httpx.NewProblem(http.StatusUnprocessableEntity, domain.CodeMergeRefused, "merge refused: order PO-000002 has posted journals")
	p := expectProblem(t, do(h, mergeCall(body, DutyRelease)), http.StatusUnprocessableEntity, domain.CodeMergeRefused)
	if !strings.Contains(p.Detail, "posted journals") {
		t.Fatalf("detail: %s", p.Detail)
	}
}

func TestList_FiltersSplitOrders(t *testing.T) {
	f := &fakeStore{}
	_, h := newServer(t, f)
	rec := do(h, call{method: http.MethodGet, path: "/v1/production/orders?originType=Split", duties: []string{DutyRead}})
	if rec.Code != http.StatusOK || f.listFilter.OriginType != domain.OriginSplit {
		t.Fatalf("list split orders: %d %s filter %+v", rec.Code, rec.Body.String(), f.listFilter)
	}
	// Split is set by :split only; an order cannot be created with it.
	create := do(h, call{method: http.MethodPost, path: "/v1/production/orders", duties: []string{DutyWrite},
		body: createBody(func(m map[string]any) { m["originType"] = domain.OriginSplit }), headers: map[string]string{idempotencyName: "c-split"}})
	p := expectProblem(t, create, http.StatusBadRequest, validationCode)
	if _, ok := p.Errors["originType"]; !ok {
		t.Fatalf("errors: %v", p.Errors)
	}
}
