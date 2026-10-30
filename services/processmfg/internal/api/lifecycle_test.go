package api

import (
	"net/http"
	"strings"
	"testing"

	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/services/processmfg/internal/domain"
	"github.com/BenjaminSRussell/passage/services/processmfg/internal/store"
)

const orderPath = "/v1/production/batch-orders/" + orderID

func TestAdvance_ReleaseAndStartPassCommandVersionAndKey(t *testing.T) {
	for _, cmd := range []string{domain.CmdRelease, domain.CmdStart} {
		fs := &fakeStore{}
		rec := call{method: "POST", path: orderPath + ":" + cmd, body: `{"version":3}`, duties: DutyBatchOrderWrite, headers: key("k-" + cmd)}.do(newRouter(fs))
		if rec.Code != http.StatusOK {
			t.Fatalf("%s: %d %s", cmd, rec.Code, rec.Body)
		}
		if fs.gotCmd != cmd || fs.gotVersion != 3 || fs.gotIdem.Key != "k-"+cmd {
			t.Fatalf("%s: store saw %q v%d %+v", cmd, fs.gotCmd, fs.gotVersion, fs.gotIdem)
		}
	}
}

func TestAdvance_Rejects(t *testing.T) {
	tests := []struct {
		name, body, duties string
		hdr                map[string]string
		status             int
	}{
		{"no key", `{"version":1}`, DutyBatchOrderWrite, nil, 400},
		{"no version", `{}`, DutyBatchOrderWrite, key("k"), 400},
		{"read duty", `{"version":1}`, DutyRead, key("k"), 403},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			rec := call{method: "POST", path: orderPath + ":release", body: tc.body, duties: tc.duties, headers: tc.hdr}.do(newRouter(&fakeStore{}))
			if rec.Code != tc.status {
				t.Fatalf("got %d %s", rec.Code, rec.Body)
			}
		})
	}
}

func TestAdvance_StateConflictAndReplayHeader(t *testing.T) {
	fs := &fakeStore{err: httpxConflict(store.CodeOrderState)}
	rec := call{method: "POST", path: orderPath + ":start", body: `{"version":1}`, duties: DutyBatchOrderWrite, headers: key("k")}.do(newRouter(fs))
	if rec.Code != http.StatusConflict || problemOf(t, rec).Code != store.CodeOrderState {
		t.Fatalf("got %d %s", rec.Code, rec.Body)
	}
	rep := call{method: "POST", path: orderPath + ":start", body: `{"version":1}`, duties: DutyBatchOrderWrite, headers: key("k")}.do(newRouter(&fakeStore{replay: true}))
	if rep.Header().Get("Idempotent-Replayed") != "true" {
		t.Fatalf("replay header missing: %v", rep.Header())
	}
}

func TestReportFinished_ParsesOutputsAndRefusesBadQuantities(t *testing.T) {
	fs := &fakeStore{}
	body := `{"version":2,"mainQty":"980.5","outputs":[{"lineNumber":4,"qty":"240"},{"lineNumber":5,"qty":"0"}]}`
	rec := call{method: "POST", path: orderPath + ":report-finished", body: body, duties: DutyBatchOrderWrite, headers: key("k")}.do(newRouter(fs))
	if rec.Code != http.StatusOK {
		t.Fatalf("%d %s", rec.Code, rec.Body)
	}
	if !fs.gotReport.MainQty.Equal(dec("980.5")) || len(fs.gotReport.Outputs) != 2 || !fs.gotReport.Outputs[4].Equal(dec("240")) || fs.gotVersion != 2 {
		t.Fatalf("store saw %+v v%d", fs.gotReport, fs.gotVersion)
	}
	for name, bad := range map[string]string{
		"zero main":      `{"version":1,"mainQty":"0"}`,
		"negative line":  `{"version":1,"mainQty":"1","outputs":[{"lineNumber":4,"qty":"-1"}]}`,
		"duplicate line": `{"version":1,"mainQty":"1","outputs":[{"lineNumber":4,"qty":"1"},{"lineNumber":4,"qty":"2"}]}`,
		"7 decimals":     `{"version":1,"mainQty":"0.0000001"}`,
	} {
		fs := &fakeStore{}
		rec := call{method: "POST", path: orderPath + ":report-finished", body: bad, duties: DutyBatchOrderWrite, headers: key("k")}.do(newRouter(fs))
		if rec.Code != 400 || fs.gotReport.Outputs != nil {
			t.Errorf("%s: %d %s", name, rec.Code, rec.Body)
		}
	}
}

const endBody = `{"version":3,"method":"tca","currency":"USD","batchCost":"10000.00","main":{"standardCost":"40"},
 "coProducts":[{"lineNumber":4,"standardCost":"20"}],"byProducts":[{"lineNumber":5,"costType":"per-quantity","rate":"1.5"}]}`

func TestEnd_BuildsAllocationInputUnderJournalDuty(t *testing.T) {
	fs := &fakeStore{}
	rec := call{method: "POST", path: orderPath + ":end", body: endBody, duties: DutyCoProductAllocate, headers: key("k")}.do(newRouter(fs))
	if rec.Code != http.StatusOK {
		t.Fatalf("%d %s", rec.Code, rec.Body)
	}
	in := fs.gotEnd
	if in.Method != domain.MethodTCA || in.Currency != "USD" || !in.BatchCost.Equal(dec("10000")) || !in.MainBasis.StandardCost.Equal(dec("40")) ||
		!in.CoBasis[4].StandardCost.Equal(dec("20")) || in.ByRates[5].CostType != "per-quantity" {
		t.Fatalf("store saw %+v", in)
	}
	denied := call{method: "POST", path: orderPath + ":end", body: endBody, duties: DutyBatchOrderWrite, headers: key("k")}.do(newRouter(&fakeStore{}))
	if denied.Code != 403 {
		t.Fatalf("batch-order writers must not end orders: %d", denied.Code)
	}
}

func TestEnd_Rejects(t *testing.T) {
	tests := []struct{ name, from, to, field string }{
		{"unknown method", `"method":"tca"`, `"method":"fifo"`, "method"},
		{"bad currency", `"currency":"USD"`, `"currency":"usd"`, "currency"},
		{"cost beyond the minor unit", `"10000.00"`, `"10000.005"`, "batchCost"},
		{"yen with decimals", `"currency":"USD","batchCost":"10000.00"`, `"currency":"JPY","batchCost":"10000.5"`, "batchCost"},
		{"negative cost", `"10000.00"`, `"-1"`, "batchCost"},
		{"negative main standard cost", `"main":{"standardCost":"40"}`, `"main":{"standardCost":"-40"}`, "main.standardCost"},
		{"co-product percent above 100", `"coProducts":[{"lineNumber":4,"standardCost":"20"}]`, `"coProducts":[{"lineNumber":4,"percent":"100.5"}]`, "coProducts[0].percent"},
		{"unknown by-product cost type", `"costType":"per-quantity"`, `"costType":"per-pallet"`, "byProducts[0].costType"},
		{"negative by-product rate", `"rate":"1.5"`, `"rate":"-1.5"`, "byProducts[0].rate"},
		{"duplicate co-product", `"coProducts":[{"lineNumber":4,"standardCost":"20"}]`, `"coProducts":[{"lineNumber":4},{"lineNumber":4}]`, "coProducts[1].lineNumber"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			fs := &fakeStore{}
			rec := call{method: "POST", path: orderPath + ":end", body: strings.Replace(endBody, tc.from, tc.to, 1), duties: DutyCoProductAllocate, headers: key("k")}.do(newRouter(fs))
			p := problemOf(t, rec)
			if rec.Code != 400 || p.Errors[tc.field] == "" || fs.gotEnd.Currency != "" {
				t.Fatalf("got %d %s", rec.Code, rec.Body)
			}
		})
	}
}

func TestEnd_AllocationRuleIsUnprocessable(t *testing.T) {
	fs := &fakeStore{err: &domain.Error{Code: domain.CodeAllocation, Detail: "by-product burden 600.0000 exceeds batch cost 100.0000"}}
	rec := call{method: "POST", path: orderPath + ":end", body: endBody, duties: DutyCoProductAllocate, headers: key("k")}.do(newRouter(fs))
	if rec.Code != 422 || problemOf(t, rec).Code != domain.CodeAllocation {
		t.Fatalf("got %d %s", rec.Code, rec.Body)
	}
}

func httpxConflict(code string) error {
	return httpx.NewProblem(http.StatusConflict, code, "batch order BO-000001 is planned; start needs a released order")
}
