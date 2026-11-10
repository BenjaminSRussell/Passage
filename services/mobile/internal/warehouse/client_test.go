package warehouse

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/BenjaminSRussell/passage/platform/tenant"
)

const tenantID = "11111111-1111-1111-1111-111111111111"

func signer(t *testing.T) *tenant.ServiceSigner {
	t.Helper()
	_, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	s, err := tenant.NewServiceSigner("mobile", priv)
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func ctx() context.Context {
	return ctxWith("warehouse.work.execute", "warehouse.inquiry.view", "warehouse.receipt.register")
}

func ctxWith(duties ...string) context.Context {
	held := map[string]struct{}{}
	for _, d := range duties {
		held[d] = struct{}{}
	}
	return tenant.With(context.Background(), tenant.Principal{TenantID: tenantID, LegalEntityID: "22222222-2222-2222-2222-222222222222", UserID: "alice", Duties: held})
}

type countingCaller struct{ calls int }

func (c *countingCaller) Do(context.Context, string, string, map[string]string) (Outcome, error) {
	c.calls++
	return Outcome{Status: 200}, nil
}

func TestGuardRefusesAnOperationTheUserLacksTheDutyFor(t *testing.T) {
	inner := &countingCaller{}
	g := Guard(inner)
	cases := []struct{ op, holds string }{
		{"receive", "warehouse.work.execute"},
		{"confirm", "warehouse.inquiry.view"},
		{"exception", "warehouse.receipt.register"},
		{"assign", "warehouse.receipt.register"},
		{"get-work", "warehouse.work.execute"},
	}
	for _, c := range cases {
		out, err := g.Do(ctxWith(c.holds), c.op, "k", nil)
		if err != nil || out.Status != http.StatusForbidden || out.Code != CodeMissingDuty {
			t.Errorf("%s holding only %s: %+v %v", c.op, c.holds, out, err)
		}
	}
	if inner.calls != 0 {
		t.Fatalf("the warehouse was called %d times without the user's duty", inner.calls)
	}
	if out, err := g.Do(ctxWith("warehouse.receipt.register"), "receive", "k", nil); err != nil || !out.OK() || inner.calls != 1 {
		t.Fatalf("a user holding the duty passes: %+v %v %d", out, err, inner.calls)
	}
	if _, err := g.Do(ctxWith(), "nuke", "k", nil); !errors.Is(err, ErrUnknownOp) {
		t.Fatalf("unknown op: %v", err)
	}
}

func TestDoSendsSignedIdempotentRequestPerOperation(t *testing.T) {
	cases := []struct {
		op     string
		params map[string]string
		method string
		path   string
		body   map[string]any
	}{
		{"assign", map[string]string{"warehouseId": "w1"}, "POST", "/v1/warehouse/work:assign", map[string]any{"warehouseId": "w1"}},
		{"confirm", map[string]string{"workId": "wk", "lineNo": "2", "qty": "5"}, "POST", "/v1/warehouse/work/wk/lines/2:confirm", map[string]any{"qty": float64(5)}},
		{"exception", map[string]string{"workId": "wk", "action": "SHORT_PICK", "lineNo": "1", "qty": "3", "reason": "DAMAGED"}, "POST", "/v1/warehouse/work/wk:exception",
			map[string]any{"action": "SHORT_PICK", "lineNo": float64(1), "confirmedQty": float64(3), "reason": "DAMAGED"}},
		{"receive", map[string]string{"receiptId": "r1", "lineNo": "4", "qty": "9"}, "POST", "/v1/warehouse/receipts/r1/lines/4:receive", map[string]any{"qty": float64(9)}},
		{"get-work", map[string]string{"workId": "wk"}, "GET", "/v1/warehouse/work/wk", nil},
	}
	for _, c := range cases {
		var got *http.Request
		var body map[string]any
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			got = r
			_ = json.NewDecoder(r.Body).Decode(&body)
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"id":"wk"}`))
		}))
		cl, err := New(srv.URL, signer(t), nil)
		if err != nil {
			t.Fatal(err)
		}
		out, err := cl.Do(ctx(), c.op, "dev1:7", c.params)
		srv.Close()
		if err != nil || !out.OK() {
			t.Fatalf("%s: %+v %v", c.op, out, err)
		}
		if got.Method != c.method || got.URL.Path != c.path {
			t.Errorf("%s: %s %s", c.op, got.Method, got.URL.Path)
		}
		if c.body != nil && got.Header.Get("Idempotency-Key") != "dev1:7" {
			t.Errorf("%s: idempotency key %q", c.op, got.Header.Get("Idempotency-Key"))
		}
		if !strings.HasPrefix(got.Header.Get("Authorization"), "Bearer ") && got.Header.Get(tenant.HeaderServiceToken) == "" {
			t.Errorf("%s: request is not signed: %v", c.op, got.Header)
		}
		if c.body != nil && !jsonEqual(body, c.body) {
			t.Errorf("%s: body %v want %v", c.op, body, c.body)
		}
	}
}

func jsonEqual(a, b map[string]any) bool {
	x, _ := json.Marshal(a)
	y, _ := json.Marshal(b)
	return string(x) == string(y)
}

func TestDoClassifiesStatuses(t *testing.T) {
	cases := []struct {
		status   int
		conflict bool
		err      bool
	}{{200, false, false}, {409, true, false}, {422, false, false}, {404, false, false}, {503, false, true}, {401, false, true}, {429, false, true}}
	for _, c := range cases {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(c.status)
			_, _ = w.Write([]byte(`{"code":"WMS-0110","detail":"line 1 is CANCELLED"}`))
		}))
		cl, _ := New(srv.URL, signer(t), nil)
		out, err := cl.Do(ctx(), "confirm", "k", map[string]string{"workId": "w", "lineNo": "1", "qty": "1"})
		srv.Close()
		if (err != nil) != c.err {
			t.Fatalf("status %d: err=%v", c.status, err)
		}
		if err == nil && out.Conflict() != c.conflict {
			t.Errorf("status %d: conflict=%v", c.status, out.Conflict())
		}
		if err == nil && c.status >= 400 && (out.Code != "WMS-0110" || out.Detail != "line 1 is CANCELLED") {
			t.Errorf("status %d: problem not read: %+v", c.status, out)
		}
	}
}

func TestDoRefusesUnknownOpAndMissingPrincipal(t *testing.T) {
	cl, _ := New("http://x", signer(t), nil)
	if _, err := cl.Do(ctx(), "nuke", "k", nil); !errors.Is(err, ErrUnknownOp) {
		t.Errorf("unknown op: %v", err)
	}
	if _, err := cl.Do(context.Background(), "get-work", "k", map[string]string{"workId": "w"}); err == nil {
		t.Error("a call without a principal must fail")
	}
	if _, err := New("", signer(t), nil); err == nil {
		t.Error("empty base URL")
	}
	if _, err := New("http://x", nil, nil); err == nil {
		t.Error("nil signer")
	}
}

func TestWorkFromReadsAssignAndGetShapes(t *testing.T) {
	assign := `{"work":{"id":"wk","status":"ASSIGNED","version":3,"lines":[{"lineNo":1,"itemId":"i","qty":"4","status":"COMPLETED"},{"lineNo":2,"itemId":"j","qty":"6","status":"OPEN"}]}}`
	w, ok := WorkFrom([]byte(assign))
	if !ok || w.Values()["line_no"] != "2" || w.Values()["line_qty"] != "6" || w.Values()["work_version"] != "3" {
		t.Fatalf("assign shape: %+v %v", w, ok)
	}
	if _, ok := WorkFrom([]byte(`{"work":null}`)); ok {
		t.Error("nothing assignable must report no work")
	}
	if w, ok := WorkFrom([]byte(`{"id":"wk","status":"OPEN","version":1,"lines":[]}`)); !ok || w.ID != "wk" {
		t.Errorf("get shape: %+v", w)
	}
}
