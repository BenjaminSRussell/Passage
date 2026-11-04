package api

import (
	"net/http"
	"testing"

	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/services/purchasing/internal/store"
)

func splitPath(id string) string { return "/v1/procurement/requisition-lines/" + id + ":split" }

func splitHeaders(key, version string) map[string]string {
	return map[string]string{"Idempotency-Key": key, "If-Match": version}
}

func TestSplitRequisitionLine_PassesQuantitiesVersionAndMinorUnits(t *testing.T) {
	h := newHarness(t)
	h.fake.sourcing = sourcing(t, leA, []fixtureLine{{"", "", "2027-03-01"}})
	line := h.fake.sourcing[0]
	rec := h.do(call{method: "POST", path: splitPath(line.ID), user: ids.New(), duties: []string{dutyPurchaseOrderMaintain},
		body: `{"quantities":["1"," 0.5 "]}`, headers: splitHeaders("k-split", `"3"`)})
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d: %s", rec.Code, rec.Body)
	}
	got := h.fake.lastSplit
	if got.lineID != line.ID || got.version != 3 || len(got.quantities) != 2 || got.quantities[1].String() != "0.5" ||
		got.minor != 2 || got.idem.Key != "k-split" || got.by == "" {
		t.Fatalf("store call = %+v", got)
	}
	if rec.Header().Get("ETag") != `"4"` {
		t.Errorf("ETag = %q, want the new requisition version", rec.Header().Get("ETag"))
	}
}

func TestSplitRequisitionLine_Rejections(t *testing.T) {
	tests := []struct {
		name    string
		path    func(id string) string
		body    string
		headers map[string]string
		duties  []string
		scope   string
		unknown bool
		status  int
		code    string
	}{
		{"no quantities", splitPath, `{"quantities":[]}`, splitHeaders("k", `"1"`), []string{dutyPurchaseOrderMaintain}, "", false, 422, codeInvalid},
		{"zero quantity", splitPath, `{"quantities":["0"]}`, splitHeaders("k", `"1"`), []string{dutyPurchaseOrderMaintain}, "", false, 422, codeInvalid},
		{"not a number", splitPath, `{"quantities":["many"]}`, splitHeaders("k", `"1"`), []string{dutyPurchaseOrderMaintain}, "", false, 422, codeInvalid},
		{"seven decimals", splitPath, `{"quantities":["0.1234567"]}`, splitHeaders("k", `"1"`), []string{dutyPurchaseOrderMaintain}, "", false, 422, codeInvalid},
		{"no key", splitPath, `{"quantities":["1"]}`, map[string]string{"If-Match": `"1"`}, []string{dutyPurchaseOrderMaintain}, "", false, 400, codeIdemRequired},
		{"no version", splitPath, `{"quantities":["1"]}`, idem("k"), []string{dutyPurchaseOrderMaintain}, "", false, 428, codeIfMatchMissing},
		{"unknown line", func(string) string { return splitPath(ids.New()) }, `{"quantities":["1"]}`, splitHeaders("k", `"1"`), []string{dutyPurchaseOrderMaintain}, "", false, 404, store.CodeRequisitionLineNotFound},
		{"unknown action", func(id string) string { return "/v1/procurement/requisition-lines/" + id + ":merge" }, `{"quantities":["1"]}`, splitHeaders("k", `"1"`), []string{dutyPurchaseOrderMaintain}, "", false, 404, codeUnknownAction},
		{"legal entity outside scope", splitPath, `{"quantities":["1"]}`, splitHeaders("k", `"1"`), []string{dutyPurchaseOrderMaintain}, ids.New(), false, 403, codeScope},
		{"needs PurchaseOrderMaintain", splitPath, `{"quantities":["1"]}`, splitHeaders("k", `"1"`), []string{dutyPurchaseOrderInquire}, "", false, 403, codeScope},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			h := newHarness(t)
			h.fake.sourcing = sourcing(t, leA, []fixtureLine{{"", "", "2027-03-01"}})
			rec := h.do(call{method: "POST", path: tc.path(h.fake.sourcing[0].ID), user: ids.New(), duties: tc.duties, scope: tc.scope, body: tc.body, headers: tc.headers})
			if rec.Code != tc.status || problemCode(t, rec) != tc.code {
				t.Fatalf("status %d code %s, want %d %s: %s", rec.Code, problemCode(t, rec), tc.status, tc.code, rec.Body)
			}
			if h.fake.lastSplit.lineID != "" {
				t.Errorf("the store was called: %+v", h.fake.lastSplit)
			}
		})
	}
}
