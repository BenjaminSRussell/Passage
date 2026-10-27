package api

import (
	"net/http"
	"testing"
)

const attributesPath = "/v1/production/batch-attributes:check"

// FR-24.5: a value outside min/max is accepted and flagged as an exception; the
// other values pass.
func TestCheckAttributes_FlagsOutOfLimits(t *testing.T) {
	body := `{"definitions":[
		{"name":"potency","kind":"numeric","target":"50","min":"40","max":"60"},
		{"name":"grade","kind":"enum","options":["A","B"]}],
		"values":{"potency":{"num":"38.5"},"grade":{"text":"A"}}}`
	rec := post(attributesPath, body, DutyBatchAttributeWrite).do(newRouter(&fakeStore{}))
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d: %s", rec.Code, rec.Body)
	}
	got := decodeBody[attributesResponse](t, rec.Body.Bytes())
	if got.Exceptions != 1 || len(got.Results) != 2 ||
		got.Results[0] != (attributeResultDTO{Attribute: "grade", Pass: true}) ||
		got.Results[1] != (attributeResultDTO{Attribute: "potency", Pass: false, Detail: "potency 38.5 is below the minimum 40"}) {
		t.Fatalf("got %+v", got)
	}
}

func TestCheckAttributes_Rejects(t *testing.T) {
	tests := []struct{ name, body string }{
		{"undefined attribute", `{"definitions":[],"values":{"potency":{"num":"1"}}}`},
		{"wrong type", `{"definitions":[{"name":"potency","kind":"numeric"}],"values":{"potency":{"text":"high"}}}`},
		{"not an option", `{"definitions":[{"name":"grade","kind":"enum","options":["A"]}],"values":{"grade":{"text":"Z"}}}`},
		{"unknown kind", `{"definitions":[{"name":"x","kind":"colour"}],"values":{}}`},
		{"min above max", `{"definitions":[{"name":"p","kind":"numeric","min":"5","max":"4"}],"values":{}}`},
		{"seven decimals", `{"definitions":[{"name":"p","kind":"numeric","min":"0.0000001"}],"values":{}}`},
		{"unnamed", `{"definitions":[{"name":"","kind":"text"}],"values":{}}`},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			rec := post(attributesPath, tc.body, DutyBatchAttributeWrite).do(newRouter(&fakeStore{}))
			if rec.Code != http.StatusBadRequest || problemOf(t, rec).Code != "COM-0001" {
				t.Fatalf("got %d %s", rec.Code, rec.Body)
			}
		})
	}
}

const pickingPath = "/v1/production/batch-picking:proposal"

const pickingBatches = `"batches":[
	{"id":"OLD","disposition":"available","qty":"50","values":{"expiry":{"date":"2027-01-10"},"potency":{"num":"45"}}},
	{"id":"HOLD","disposition":"on-hold","qty":"100","values":{"expiry":{"date":"2027-02-01"},"potency":{"num":"45"}}},
	{"id":"WEAK","disposition":"available","qty":"100","values":{"expiry":{"date":"2027-03-01"},"potency":{"num":"30"}}},
	{"id":"GOOD","disposition":"available","qty":"100","values":{"expiry":{"date":"2027-06-01"},"potency":{"num":"50"}}}]`

// AC-24.4, AC-24.10 and FR-24.3: without the duty the expired, on-hold and
// out-of-band batches are excluded with their codes; the rest is proposed.
func TestProposePicking_ExcludesWithCodes(t *testing.T) {
	body := `{"postingDate":"2027-01-15","qty":"80","potency":{"attribute":"potency","min":"40","max":"60"},
		"overrideReason":"QA approved use",` + pickingBatches + `}`
	rec := post(pickingPath, body, DutyRead).do(newRouter(&fakeStore{}))
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d: %s", rec.Code, rec.Body)
	}
	got := decodeBody[pickingResponse](t, rec.Body.Bytes())
	codes := map[string]string{}
	for _, e := range got.Excluded {
		codes[e.BatchID] = e.Code
	}
	if got.OverrideHeld || codes["OLD"] != "PMF-0003" || codes["HOLD"] != "PMF-0016" || codes["WEAK"] != "PMF-0005" || len(codes) != 3 {
		t.Fatalf("excluded %+v, override %v", got.Excluded, got.OverrideHeld)
	}
	if len(got.Picked) != 1 || got.Picked[0].BatchID != "GOOD" || got.Picked[0].Qty.String() != "80" ||
		got.Picked[0].Expiry == nil || *got.Picked[0].Expiry != "2027-06-01" || !got.Shortfall.IsZero() {
		t.Fatalf("picked %+v shortfall %s", got.Picked, got.Shortfall)
	}
}

// BR-24.10: with the consume-expired duty and a reason the expired batch is
// taken first (FEFO) and flagged as an override.
func TestProposePicking_OverrideWithDuty(t *testing.T) {
	body := `{"postingDate":"2027-01-15","qty":"200","overrideReason":"QA approved use",` + pickingBatches + `}`
	rec := post(pickingPath, body, DutyRead+","+DutyConsumeExpired).do(newRouter(&fakeStore{}))
	got := decodeBody[pickingResponse](t, rec.Body.Bytes())
	if rec.Code != http.StatusOK || !got.OverrideHeld || len(got.Picked) != 3 || got.Picked[0].BatchID != "OLD" || !got.Picked[0].ExpiredOverride ||
		got.Picked[1].BatchID != "WEAK" || got.Picked[1].ExpiredOverride || got.Picked[2].Qty.String() != "50" || !got.Shortfall.IsZero() {
		t.Fatalf("status %d: %+v", rec.Code, got)
	}
}

func TestProposePicking_Rejects(t *testing.T) {
	tests := []struct{ name, body string }{
		{"short reason with the duty", `{"postingDate":"2027-01-15","qty":"1","overrideReason":"too short",` + pickingBatches + `}`},
		{"bad posting date", `{"postingDate":"15.01.2027","qty":"1","batches":[]}`},
		{"zero quantity", `{"postingDate":"2027-01-15","qty":"0","batches":[]}`},
		{"band upside down", `{"postingDate":"2027-01-15","qty":"1","potency":{"attribute":"potency","min":"60","max":"40"},"batches":[]}`},
		{"band without attribute", `{"postingDate":"2027-01-15","qty":"1","potency":{"attribute":"","min":"40","max":"60"},"batches":[]}`},
		{"unknown disposition", `{"postingDate":"2027-01-15","qty":"1","batches":[{"id":"A","disposition":"lost","qty":"1"}]}`},
		{"duplicate batch", `{"postingDate":"2027-01-15","qty":"1","batches":[{"id":"A","qty":"1"},{"id":"A","qty":"1"}]}`},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			rec := post(pickingPath, tc.body, DutyRead+","+DutyConsumeExpired).do(newRouter(&fakeStore{}))
			if rec.Code != http.StatusBadRequest || problemOf(t, rec).Code != "COM-0001" {
				t.Fatalf("got %d %s", rec.Code, rec.Body)
			}
		})
	}
}

const reworkPath = "/v1/production/rework-orders:preview"

// AC-24.18: rework of B-77 with the defaults keeps the batch number, puts the
// output in quarantine behind a quality order and lists B-77 as parent.
func TestPreviewRework_AC2418(t *testing.T) {
	body := `{"batch":{"id":"B-77","disposition":"on-hold","qty":"500"},"reworkQty":"500","outputQty":"480","params":{}}`
	rec := post(reworkPath, body, DutyBatchOrderWrite).do(newRouter(&fakeStore{}))
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d: %s", rec.Code, rec.Body)
	}
	got := decodeBody[reworkResponse](t, rec.Body.Bytes())
	if got.OutputBatchID == nil || *got.OutputBatchID != "B-77" || got.OutputDisposition != "quarantine" || !got.QualityOrderRequired ||
		got.VarianceType != "rework" || len(got.Parents) != 1 || got.Parents[0] != "B-77" || got.OutputQty.String() != "480" {
		t.Fatalf("got %+v", got)
	}
}

func TestPreviewRework_ParametersAndRefusals(t *testing.T) {
	body := `{"batch":{"id":"B-78","qty":"100"},"reworkQty":"40","outputQty":"40",
		"params":{"keepBatchNumber":false,"requireQualityOrder":false,"outputDisposition":"available","capitalize":true}}`
	rec := post(reworkPath, body, DutyBatchOrderWrite).do(newRouter(&fakeStore{}))
	got := decodeBody[reworkResponse](t, rec.Body.Bytes())
	if rec.Code != http.StatusOK || got.OutputBatchID != nil || got.QualityOrderRequired || got.VarianceType != "capitalized" {
		t.Fatalf("status %d: %+v", rec.Code, got)
	}
	for name, body := range map[string]string{
		"more than the batch":           `{"batch":{"id":"B","qty":"10"},"reworkQty":"11","outputQty":"1","params":{}}`,
		"available behind quality":      `{"batch":{"id":"B","qty":"10"},"reworkQty":"1","outputQty":"1","params":{"outputDisposition":"available"}}`,
		"rejected output":               `{"batch":{"id":"B","qty":"10"},"reworkQty":"1","outputQty":"1","params":{"outputDisposition":"rejected"}}`,
		"zero output":                   `{"batch":{"id":"B","qty":"10"},"reworkQty":"1","outputQty":"0","params":{}}`,
		"batch without id":              `{"batch":{"id":"","qty":"10"},"reworkQty":"1","outputQty":"1","params":{}}`,
		"seven decimals on the rework":  `{"batch":{"id":"B","qty":"10"},"reworkQty":"0.0000001","outputQty":"1","params":{}}`,
		"unknown parameter is rejected": `{"batch":{"id":"B","qty":"10"},"reworkQty":"1","outputQty":"1","params":{"keep":true}}`,
	} {
		t.Run(name, func(t *testing.T) {
			rec := post(reworkPath, body, DutyBatchOrderWrite).do(newRouter(&fakeStore{}))
			if rec.Code != http.StatusBadRequest || problemOf(t, rec).Code != "COM-0001" {
				t.Fatalf("got %d %s", rec.Code, rec.Body)
			}
		})
	}
}

// Each slice-3 route needs its duty.
func TestQualityRoutesNeedTheirDuty(t *testing.T) {
	for path, duty := range map[string]string{attributesPath: DutyBatchAttributeWrite, pickingPath: DutyRead, reworkPath: DutyBatchOrderWrite} {
		rec := post(path, `{}`, "production.something-else").do(newRouter(&fakeStore{}))
		if rec.Code != http.StatusForbidden {
			t.Fatalf("%s without %s: %d", path, duty, rec.Code)
		}
	}
}
