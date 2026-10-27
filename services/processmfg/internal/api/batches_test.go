package api

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"
)

func post(path, body, duties string) call {
	return call{method: "POST", path: path, body: body, duties: duties}
}

func decodeBody[T any](t *testing.T, raw []byte) T {
	t.Helper()
	var v T
	if err := json.Unmarshal(raw, &v); err != nil {
		t.Fatalf("body %s: %v", raw, err)
	}
	return v
}

const derivePath = "/v1/production/batch-derivation:preview"

// AC-24.11: weighted-average potency of 200 kg at 40% and 300 kg at 45% is
// 43.0000%, and the response names no missing sender.
func TestDerive_AC2411(t *testing.T) {
	body := `{"rule":{"attribute":"potency","aggregation":"weighted-average"},
		"senders":[{"batchId":"B-1","value":{"num":"40"},"qty":"200"},{"batchId":"B-2","value":{"num":"45"},"qty":"300"}]}`
	rec := post(derivePath, body, DutyBatchAttributeWrite).do(newRouter(&fakeStore{}))
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d: %s", rec.Code, rec.Body)
	}
	got := decodeBody[deriveResponse](t, rec.Body.Bytes())
	if got.Value.Num == nil || got.Value.Num.StringFixed(4) != "43.0000" || len(got.MissingSenders) != 0 || got.WarningCode != "" {
		t.Fatalf("derived %+v", got)
	}
}

// FR-24.15: a sender without a value is left out and reported as PMF-0007.
func TestDerive_MissingSenderWarns(t *testing.T) {
	body := `{"rule":{"attribute":"expiry","aggregation":"earliest-date"},
		"senders":[{"batchId":"B-1","value":{"date":"2027-03-01"},"qty":"1"},{"batchId":"B-2","value":{},"qty":"1"}]}`
	rec := post(derivePath, body, DutyBatchAttributeWrite).do(newRouter(&fakeStore{}))
	got := decodeBody[deriveResponse](t, rec.Body.Bytes())
	if rec.Code != http.StatusOK || got.WarningCode != "PMF-0007" || len(got.MissingSenders) != 1 || got.MissingSenders[0] != "B-2" ||
		got.Value.Date == nil || *got.Value.Date != "2027-03-01" {
		t.Fatalf("status %d: %+v", rec.Code, got)
	}
}

func TestDerive_Rejects(t *testing.T) {
	tests := []struct{ name, body string }{
		{"no attribute", `{"rule":{"aggregation":"min"},"senders":[]}`},
		{"unknown aggregation", `{"rule":{"attribute":"a","aggregation":"median"},"senders":[]}`},
		{"two kinds in one value", `{"rule":{"attribute":"a","aggregation":"min"},"senders":[{"batchId":"B","value":{"num":"1","text":"x"},"qty":"1"}]}`},
		{"bad date", `{"rule":{"attribute":"a","aggregation":"earliest-date"},"senders":[{"batchId":"B","value":{"date":"01/02/2027"},"qty":"1"}]}`},
		{"negative quantity", `{"rule":{"attribute":"a","aggregation":"weighted-average"},"senders":[{"batchId":"B","value":{"num":"1"},"qty":"-1"}]}`},
		{"no batch id", `{"rule":{"attribute":"a","aggregation":"min"},"senders":[{"batchId":"","value":{"num":"1"},"qty":"1"}]}`},
		{"fixed without value", `{"rule":{"attribute":"a","aggregation":"fixed"},"senders":[]}`},
		{"unknown field", `{"rule":{"attribute":"a","aggregation":"min"},"senders":[],"colour":"red"}`},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			rec := post(derivePath, tc.body, DutyBatchAttributeWrite).do(newRouter(&fakeStore{}))
			if rec.Code != http.StatusBadRequest || problemOf(t, rec).Code != "COM-0001" {
				t.Fatalf("got %d %s", rec.Code, rec.Body)
			}
		})
	}
}

const mergePath = "/v1/production/batch-merge:preview"

// AC-24.12: 100 kg expiring 2027-03-01 and 50 kg expiring 2027-02-01 merge into
// 150 kg expiring 2027-02-01 with both batches as genealogy parents.
func TestMerge_AC2412(t *testing.T) {
	body := `{"batches":[
		{"id":"A","itemId":"i","warehouseId":"w","disposition":"available","qty":"100","unitCost":"2","values":{"expiry":{"date":"2027-03-01"}}},
		{"id":"B","itemId":"i","warehouseId":"w","disposition":"available","qty":"50","unitCost":"2.6","values":{"expiry":{"date":"2027-02-01"}}}]}`
	rec := post(mergePath, body, DutyBatchMerge).do(newRouter(&fakeStore{}))
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d: %s", rec.Code, rec.Body)
	}
	got := decodeBody[mergeResponse](t, rec.Body.Bytes())
	if got.Qty.String() != "150" || got.UnitCost.String() != "2.2" || *got.Values["expiry"].Date != "2027-02-01" ||
		strings.Join(got.Parents, ",") != "A,B" || got.Disposition != "available" {
		t.Fatalf("merged %+v", got)
	}
}

// FR-24.16: text attributes that differ refuse the merge with 422 PMF-0006
// unless the attribute is merge-override.
func TestMerge_RefusalAndOverride(t *testing.T) {
	batches := `[{"id":"A","itemId":"i","warehouseId":"w","qty":"1","values":{"grade":{"text":"X"}}},
		{"id":"B","itemId":"i","warehouseId":"w","qty":"3","values":{"grade":{"text":"Y"}}}]`
	refused := post(mergePath, `{"batches":`+batches+`}`, DutyBatchMerge).do(newRouter(&fakeStore{}))
	if refused.Code != http.StatusUnprocessableEntity || problemOf(t, refused).Code != "PMF-0006" {
		t.Fatalf("got %d %s", refused.Code, refused.Body)
	}
	allowed := post(mergePath, `{"batches":`+batches+`,"mergeOverride":["grade"]}`, DutyBatchMerge).do(newRouter(&fakeStore{}))
	got := decodeBody[mergeResponse](t, allowed.Body.Bytes())
	if allowed.Code != http.StatusOK || *got.Values["grade"].Text != "Y" {
		t.Fatalf("override: %d %s", allowed.Code, allowed.Body)
	}
	bad := post(mergePath, `{"batches":[{"id":"A","qty":"1","disposition":"lost"},{"id":"B","qty":"1"}]}`, DutyBatchMerge).do(newRouter(&fakeStore{}))
	if bad.Code != http.StatusBadRequest {
		t.Fatalf("unknown disposition: %d %s", bad.Code, bad.Body)
	}
}

const checkPath = "/v1/production/batch-requirements:check"

// AC-24.14: customer sellable days 60; of the batches expiring in 45 and 90
// days only the 90-day batch is proposed, and the on-hold batch is excluded
// (AC-24.10). The customer row beats the item default (BR-24.13).
func TestCheckRequirements_AC2414(t *testing.T) {
	body := `{"requirements":[{"level":1,"sellableDays":10},{"level":3,"sellableDays":60}],"shipDate":"2027-01-01","qty":"80",
		"batches":[{"id":"short","qty":"100","values":{"expiry":{"date":"2027-02-15"}}},
		{"id":"long","qty":"100","values":{"expiry":{"date":"2027-04-01"}}},
		{"id":"held","qty":"500","disposition":"on-hold","values":{"expiry":{"date":"2028-01-01"}}}]}`
	rec := post(checkPath, body, DutyRead).do(newRouter(&fakeStore{}))
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d: %s", rec.Code, rec.Body)
	}
	got := decodeBody[checkResponse](t, rec.Body.Bytes())
	if got.Requirement.SellableDays != 60 || len(got.Qualifying) != 1 || got.Qualifying[0].BatchID != "long" ||
		got.QtyAvailable.String() != "100" || got.SameBatchID != nil {
		t.Fatalf("checked %+v", got)
	}
	if len(got.Excluded) != 2 || got.Excluded[0].BatchID != "short" || got.Excluded[0].WarningCode != "PMF-0009" ||
		!strings.Contains(got.Excluded[0].Reason, "45 days") || got.Excluded[1].Reason != "disposition is on-hold" {
		t.Fatalf("excluded %+v", got.Excluded)
	}
}

// FR-24.19: a same-batch line names the batch that supplies it, or answers
// 409 PMF-0008 with the largest available quantity.
func TestCheckRequirements_SameBatch(t *testing.T) {
	batches := `[{"id":"a","qty":"60","values":{"expiry":{"date":"2027-06-01"}}},{"id":"b","qty":"150","values":{"expiry":{"date":"2027-05-01"}}}]`
	ok := post(checkPath, `{"requirements":[{"level":2,"sameBatch":true}],"shipDate":"2027-01-01","qty":"100","batches":`+batches+`}`, DutyRead).do(newRouter(&fakeStore{}))
	got := decodeBody[checkResponse](t, ok.Body.Bytes())
	if ok.Code != http.StatusOK || got.SameBatchID == nil || *got.SameBatchID != "b" {
		t.Fatalf("same batch: %d %s", ok.Code, ok.Body)
	}
	short := post(checkPath, `{"requirements":[{"level":2,"sameBatch":true}],"shipDate":"2027-01-01","qty":"200","batches":`+batches+`}`, DutyRead).do(newRouter(&fakeStore{}))
	p := problemOf(t, short)
	if short.Code != http.StatusConflict || p.Code != "PMF-0008" || !strings.Contains(p.Detail, "largest 150") {
		t.Fatalf("shortfall: %d %s", short.Code, short.Body)
	}
}

func TestCheckRequirements_Rejects(t *testing.T) {
	for name, body := range map[string]string{
		"bad ship date": `{"requirements":[],"shipDate":"tomorrow","qty":"1","batches":[]}`,
		"zero qty":      `{"requirements":[],"shipDate":"2027-01-01","qty":"0","batches":[]}`,
		"bad level":     `{"requirements":[{"level":7,"sellableDays":1}],"shipDate":"2027-01-01","qty":"1","batches":[]}`,
		"min above max": `{"requirements":[{"level":3,"attribute":"p","min":"5","max":"1"}],"shipDate":"2027-01-01","qty":"1","batches":[]}`,
	} {
		rec := post(checkPath, body, DutyRead).do(newRouter(&fakeStore{}))
		if rec.Code != http.StatusBadRequest {
			t.Errorf("%s: %d %s", name, rec.Code, rec.Body)
		}
	}
}

const consolidatePath = "/v1/production/bulk-consolidation:preview"

// AC-24.13: packed orders for 400 and 600 bottles at 0.5 kg with a 100 kg
// formula multiple give one 500 kg bulk order with both pegged to it.
func TestConsolidate_AC2413(t *testing.T) {
	body := `{"relation":{"packQtyPerUnit":"0.5","multiple":"100","max":"0"},"packedOrders":[
		{"orderId":"P-1","itemId":"bottle","units":"400","due":"2027-01-05"},
		{"orderId":"P-2","itemId":"bottle","units":"600","due":"2027-01-04"}]}`
	rec := post(consolidatePath, body, DutyBatchOrderWrite).do(newRouter(&fakeStore{}))
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d: %s", rec.Code, rec.Body)
	}
	got := decodeBody[consolidateResponse](t, rec.Body.Bytes())
	if len(got.BulkOrders) != 1 || got.BulkOrders[0].Qty.String() != "500" || len(got.BulkOrders[0].Pegs) != 2 ||
		got.BulkOrders[0].Pegs[0].OrderID != "P-2" || got.BulkOrders[0].Pegs[0].BulkQty.String() != "300" {
		t.Fatalf("consolidated %+v", got)
	}
	bad := post(consolidatePath, `{"relation":{"packQtyPerUnit":"0.5","multiple":"100","max":"150"},"packedOrders":[{"orderId":"P","units":"1","due":"2027-01-01"}]}`,
		DutyBatchOrderWrite).do(newRouter(&fakeStore{}))
	if bad.Code != http.StatusBadRequest {
		t.Fatalf("max not a multiple: %d %s", bad.Code, bad.Body)
	}
}

const approvePath = "/v1/production/batch-records:approve-check"

// BR-24.15, AC-24.16: approval is refused (422 PMF-0014) while an exception has
// no disposition or no signature.
func TestApproveCheck_BR2415(t *testing.T) {
	open := post(approvePath, `{"exceptions":[{"id":"E-1","disposition":"accepted"},{"id":"E-2","disposition":"accepted","signatureId":"S-1"}]}`,
		DutyBatchRecordApprove).do(newRouter(&fakeStore{}))
	if open.Code != http.StatusUnprocessableEntity || problemOf(t, open).Code != "PMF-0014" {
		t.Fatalf("got %d %s", open.Code, open.Body)
	}
	ok := post(approvePath, `{"exceptions":[{"id":"E-1","disposition":"deviation-raised","signatureId":"S-1"}]}`, DutyBatchRecordApprove).do(newRouter(&fakeStore{}))
	if got := decodeBody[approvableResponse](t, ok.Body.Bytes()); ok.Code != http.StatusOK || !got.Approvable || got.Exceptions != 1 {
		t.Fatalf("got %d %s", ok.Code, ok.Body)
	}
}

// Every slice-2 preview needs its own duty (PRD 4.24.17).
func TestBatchPreviewDuties(t *testing.T) {
	for path, duty := range map[string]string{
		derivePath:      DutyBatchAttributeWrite,
		mergePath:       DutyBatchMerge,
		consolidatePath: DutyBatchOrderWrite,
		approvePath:     DutyBatchRecordApprove,
	} {
		if rec := post(path, `{}`, DutyRead).do(newRouter(&fakeStore{})); rec.Code != http.StatusForbidden {
			t.Errorf("%s without %s: %d", path, duty, rec.Code)
		}
	}
	if rec := post(checkPath, `{}`, "").do(newRouter(&fakeStore{})); rec.Code != http.StatusForbidden {
		t.Errorf("check without read duty: %d", rec.Code)
	}
}
