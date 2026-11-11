package api

import (
	"net/http"
	"strings"
	"testing"

	"github.com/BenjaminSRussell/passage/services/processmfg/internal/store"
)

const (
	batchID   = "55555555-0000-4000-8000-000000000001"
	batchPath = "/v1/production/batches/" + batchID
)

func TestDefineAttribute_PassesDefinitionAndKey(t *testing.T) {
	fs := &fakeStore{}
	body := `{"itemId":"` + itemA + `","name":"potency","kind":"numeric","role":"potency","target":"50","min":"40","max":"60"}`
	rec := call{method: "POST", path: "/v1/production/batch-attribute-definitions", body: body, duties: DutyBatchAttributeWrite, headers: key("k1")}.do(newRouter(fs))
	if rec.Code != http.StatusCreated {
		t.Fatalf("%d %s", rec.Code, rec.Body)
	}
	if fs.gotDef.ItemID != itemA || fs.gotDef.Name != "potency" || fs.gotDef.Role != "potency" || !fs.gotDef.Min.Equal(dec("40")) || fs.gotIdem.Key != "k1" {
		t.Fatalf("store saw %+v %+v", fs.gotDef, fs.gotIdem)
	}
}

func TestDefineAttribute_Rejects(t *testing.T) {
	tests := []struct {
		name, body, duties string
		hdr                map[string]string
		status             int
	}{
		{"no key", `{"itemId":"` + itemA + `","name":"x","kind":"text"}`, DutyBatchAttributeWrite, nil, 400},
		{"bad item", `{"itemId":"x","name":"x","kind":"text"}`, DutyBatchAttributeWrite, key("k"), 400},
		{"no name", `{"itemId":"` + itemA + `","name":"","kind":"text"}`, DutyBatchAttributeWrite, key("k"), 400},
		{"seven decimals", `{"itemId":"` + itemA + `","name":"x","kind":"numeric","min":"0.0000001"}`, DutyBatchAttributeWrite, key("k"), 400},
		{"read duty", `{"itemId":"` + itemA + `","name":"x","kind":"text"}`, DutyRead, key("k"), 403},
		{"potency on text", `{"itemId":"` + itemA + `","name":"x","kind":"text","role":"potency"}`, DutyBatchAttributeWrite, key("k"), 400},
		{"unknown role", `{"itemId":"` + itemA + `","name":"x","kind":"numeric","role":"assay"}`, DutyBatchAttributeWrite, key("k"), 400},
		{"long option", `{"itemId":"` + itemA + `","name":"x","kind":"enum","options":["` + repeat("o", 201) + `"]}`, DutyBatchAttributeWrite, key("k"), 400},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			rec := call{method: "POST", path: "/v1/production/batch-attribute-definitions", body: tc.body, duties: tc.duties, headers: tc.hdr}.do(newRouter(&fakeStore{}))
			if rec.Code != tc.status {
				t.Fatalf("got %d %s", rec.Code, rec.Body)
			}
		})
	}
}

func TestListAttributeDefinitions_FiltersByItem(t *testing.T) {
	fs := &fakeStore{}
	rec := call{method: "GET", path: "/v1/production/batch-attribute-definitions?itemId=" + itemA + "&limit=10", duties: DutyRead}.do(newRouter(fs))
	if rec.Code != http.StatusOK || fs.gotItem != itemA || fs.gotLimit != 10 {
		t.Fatalf("%d %s item=%q limit=%d", rec.Code, rec.Body, fs.gotItem, fs.gotLimit)
	}
	if rec := (call{method: "GET", path: "/v1/production/batch-attribute-definitions?itemId=zz", duties: DutyRead}).do(newRouter(fs)); rec.Code != 400 {
		t.Fatalf("bad item: %d", rec.Code)
	}
}

func TestRegisterBatch_DefaultsEntityAndDisposition(t *testing.T) {
	fs := &fakeStore{}
	rec := call{method: "POST", path: "/v1/production/batches", body: `{"itemId":"` + itemA + `","batchNumber":" B-100 "}`, duties: DutyBatchAttributeWrite, headers: key("k")}.do(newRouter(fs))
	if rec.Code != http.StatusCreated {
		t.Fatalf("%d %s", rec.Code, rec.Body)
	}
	if fs.gotNew.LegalEntityID != entityID || fs.gotNew.BatchNumber != "B-100" || fs.gotNew.Disposition != "available" {
		t.Fatalf("store saw %+v", fs.gotNew)
	}
}

func TestRegisterBatch_Rejects(t *testing.T) {
	tests := []struct {
		name, body string
		status     int
	}{
		{"no number", `{"itemId":"` + itemA + `","batchNumber":"  "}`, 400},
		{"bad disposition", `{"itemId":"` + itemA + `","batchNumber":"B","disposition":"lost"}`, 400},
		{"bad order", `{"itemId":"` + itemA + `","batchNumber":"B","batchOrderId":"x"}`, 400},
		{"other entity", `{"itemId":"` + itemA + `","batchNumber":"B","legalEntityId":"` + otherEntity + `"}`, 403},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			c := call{method: "POST", path: "/v1/production/batches", body: tc.body, duties: DutyBatchAttributeWrite, headers: key("k")}
			rec := c.do(newRouter(&fakeStore{}))
			if rec.Code != tc.status {
				t.Fatalf("got %d %s", rec.Code, rec.Body)
			}
		})
	}
}

func TestListBatches_FiltersAndRejects(t *testing.T) {
	fs := &fakeStore{}
	rec := call{method: "GET", path: "/v1/production/batches?disposition=quarantine&q=+b-7+&itemId=" + itemA, duties: DutyRead}.do(newRouter(fs))
	if rec.Code != 200 || fs.gotBatches.Disposition != "quarantine" || fs.gotBatches.ItemID != itemA || fs.gotBatches.Q != "b-7" {
		t.Fatalf("%d %s %+v", rec.Code, rec.Body, fs.gotBatches)
	}
	if rec := (call{method: "GET", path: "/v1/production/batches?disposition=lost", duties: DutyRead}).do(newRouter(fs)); rec.Code != 400 {
		t.Fatalf("bad disposition: %d", rec.Code)
	}
	if rec := (call{method: "GET", path: "/v1/production/batches?q=" + strings.Repeat("x", 101), duties: DutyRead}).do(newRouter(fs)); rec.Code != 400 {
		t.Fatalf("a long q: %d", rec.Code)
	}
}

func TestGetBatch_NotFoundIsProblem(t *testing.T) {
	fs := &fakeStore{err: httpxConflict(store.CodeBatchNotFound)}
	rec := call{method: "GET", path: batchPath, duties: DutyRead}.do(newRouter(fs))
	if problemOf(t, rec).Code != store.CodeBatchNotFound {
		t.Fatalf("%d %s", rec.Code, rec.Body)
	}
	if rec := (call{method: "GET", path: "/v1/production/batches/nope", duties: DutyRead}).do(newRouter(&fakeStore{})); rec.Code != 400 {
		t.Fatalf("bad id: %d", rec.Code)
	}
}

func TestRecordAttributes_ParsesValuesVersionAndKey(t *testing.T) {
	fs := &fakeStore{}
	body := `{"version":2,"values":{"potency":{"num":"38.5"},"grade":{"text":"A"},"expiry":{"date":"2027-03-01"}}}`
	rec := call{method: "PUT", path: batchPath + "/attributes", body: body, duties: DutyBatchAttributeWrite, headers: key("k")}.do(newRouter(fs))
	if rec.Code != http.StatusOK {
		t.Fatalf("%d %s", rec.Code, rec.Body)
	}
	if fs.gotVersion != 2 || len(fs.gotValues) != 3 || !fs.gotValues["potency"].Num.Equal(dec("38.5")) || fs.gotValues["expiry"].Date == nil {
		t.Fatalf("store saw v%d %+v", fs.gotVersion, fs.gotValues)
	}
}

func TestRecordAttributes_Rejects(t *testing.T) {
	for name, body := range map[string]string{
		"no version":  `{"values":{"a":{"text":"x"}}}`,
		"no values":   `{"version":1,"values":{}}`,
		"empty value": `{"version":1,"values":{"a":{}}}`,
		"two kinds":   `{"version":1,"values":{"a":{"text":"x","num":"1"}}}`,
		"bad date":    `{"version":1,"values":{"a":{"date":"03/01/2027"}}}`,
	} {
		fs := &fakeStore{}
		rec := call{method: "PUT", path: batchPath + "/attributes", body: body, duties: DutyBatchAttributeWrite, headers: key("k")}.do(newRouter(fs))
		if rec.Code != 400 || fs.gotValues != nil {
			t.Errorf("%s: %d %s", name, rec.Code, rec.Body)
		}
	}
	if rec := (call{method: "PUT", path: batchPath + "/attributes", body: `{"version":1,"values":{"a":{"text":"x"}}}`, duties: DutyBatchAttributeWrite}).do(newRouter(&fakeStore{})); rec.Code != 400 {
		t.Errorf("no key: %d", rec.Code)
	}
}

// The disposition is a separate duty from recording attributes (AC-24.10).
func TestChangeDisposition_NeedsItsOwnDuty(t *testing.T) {
	body := `{"version":3,"disposition":"on-hold","reason":" failed potency "}`
	fs := &fakeStore{}
	rec := call{method: "POST", path: batchPath + ":disposition", body: body, duties: DutyBatchAttributeWrite, headers: key("k")}.do(newRouter(fs))
	if rec.Code != http.StatusForbidden || fs.gotTo != "" {
		t.Fatalf("attribute duty changed the disposition: %d %s", rec.Code, rec.Body)
	}
	rec = call{method: "POST", path: batchPath + ":disposition", body: body, duties: DutyBatchDisposition, headers: key("k")}.do(newRouter(fs))
	if rec.Code != http.StatusOK || fs.gotTo != "on-hold" || fs.gotReason != "failed potency" || fs.gotVersion != 3 {
		t.Fatalf("%d %s saw %q %q v%d", rec.Code, rec.Body, fs.gotTo, fs.gotReason, fs.gotVersion)
	}
}

func TestChangeDisposition_Rejects(t *testing.T) {
	for name, body := range map[string]string{
		"no reason":   `{"version":1,"disposition":"rejected","reason":" "}`,
		"unknown":     `{"version":1,"disposition":"lost","reason":"x"}`,
		"no version":  `{"disposition":"rejected","reason":"x"}`,
		"long reason": `{"version":1,"disposition":"rejected","reason":"` + repeat("x", 501) + `"}`,
	} {
		rec := call{method: "POST", path: batchPath + ":disposition", body: body, duties: DutyBatchDisposition, headers: key("k")}.do(newRouter(&fakeStore{}))
		if rec.Code != 400 {
			t.Errorf("%s: %d %s", name, rec.Code, rec.Body)
		}
	}
}

func repeat(s string, n int) string {
	out := ""
	for i := 0; i < n; i++ {
		out += s
	}
	return out
}
