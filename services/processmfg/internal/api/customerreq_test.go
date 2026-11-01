package api

import (
	"context"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/services/processmfg/internal/domain"
	"github.com/BenjaminSRussell/passage/services/processmfg/internal/store"
)

const (
	reqCustomer = "66666666-0000-4000-8000-0000000000c1"
	reqGroup    = "66666666-0000-4000-8000-0000000000d1"
	reqID       = "66666666-0000-4000-8000-0000000000e1"
	reqPath     = "/v1/production/customer-batch-requirements"
)

func (f *fakeStore) CreateCustomerRequirement(_ context.Context, in store.CustomerRequirement, idem store.Idem) (store.CustomerRequirement, bool, error) {
	f.gotReq, f.gotIdem = in, idem
	in.ID = reqID
	return in, !f.replay, f.err
}

func (f *fakeStore) ListCustomerRequirements(_ context.Context, filter store.RequirementFilter) ([]store.CustomerRequirement, error) {
	f.gotReqFilt = filter
	return f.reqs, f.err
}

func (f *fakeStore) GetCustomerRequirement(context.Context, string) (store.CustomerRequirement, error) {
	return store.CustomerRequirement{ID: reqID, Version: 1}, f.err
}

func (f *fakeStore) RetireCustomerRequirement(_ context.Context, id string, version int, idem store.Idem) (store.CustomerRequirement, bool, error) {
	f.gotVersion, f.gotIdem = version, idem
	return store.CustomerRequirement{ID: id, Version: version + 1}, !f.replay, f.err
}

func (f *fakeStore) ApplicableRequirements(_ context.Context, legalEntityID, itemID string, who store.ReqScope, shipDate string) ([]store.CustomerRequirement, error) {
	f.gotReqEntity, f.gotItem, f.gotReqWho, f.gotReqDate = legalEntityID, itemID, who, shipDate
	return f.reqs, f.err
}

func TestCreateRequirement_PassesRuleAndKey(t *testing.T) {
	f := &fakeStore{}
	body := `{"itemId":"` + pickItem + `","customerId":"` + reqCustomer + `","sellableDays":60,"attribute":"potency","min":"40","max":"60","validFrom":"2027-01-01"}`
	rec := call{method: "POST", path: reqPath, body: body, duties: DutyBatchRequirementWrite, headers: key("r-1")}.do(newRouter(f))
	if rec.Code != http.StatusCreated {
		t.Fatalf("status %d: %s", rec.Code, rec.Body)
	}
	g := f.gotReq
	if g.ItemID != pickItem || g.CustomerID == nil || *g.CustomerID != reqCustomer || g.SellableDays == nil || *g.SellableDays != 60 ||
		g.Attribute != "potency" || !g.Min.Equal(decimal.NewFromInt(40)) || g.ValidFrom == nil || f.gotIdem.Key != "r-1" || g.LegalEntityID == "" {
		t.Fatalf("stored %+v idem %+v", g, f.gotIdem)
	}
	f.replay = true
	rec = call{method: "POST", path: reqPath, body: body, duties: DutyBatchRequirementWrite, headers: key("r-1")}.do(newRouter(f))
	if rec.Code != http.StatusOK || rec.Header().Get("Idempotent-Replayed") != "true" {
		t.Fatalf("replay %d %v", rec.Code, rec.Header())
	}
}

func TestCreateRequirement_Rejects(t *testing.T) {
	base := `"itemId":"` + pickItem + `"`
	tests := map[string]string{
		"sets nothing":       `{` + base + `}`,
		"bad item":           `{"itemId":"x","sameBatch":true}`,
		"customer and group": `{` + base + `,"customerId":"` + reqCustomer + `","customerGroupId":"` + reqGroup + `","sameBatch":true}`,
		"bad customer":       `{` + base + `,"customerId":"x","sameBatch":true}`,
		"days too high":      `{` + base + `,"sellableDays":3651}`,
		"negative days":      `{` + base + `,"sellableDays":-1}`,
		"range no attr":      `{` + base + `,"min":"1"}`,
		"attr no range":      `{` + base + `,"attribute":"potency"}`,
		"min above max":      `{` + base + `,"attribute":"potency","min":"5","max":"1"}`,
		"huge max":           `{` + base + `,"attribute":"potency","max":"1000000000000"}`,
		"long attr":          `{` + base + `,"sameBatch":true,"attribute":"` + strings.Repeat("x", 41) + `","min":"1"}`,
		"bad date":           `{` + base + `,"sameBatch":true,"validFrom":"01/02/2027"}`,
		"dates reversed":     `{` + base + `,"sameBatch":true,"validFrom":"2027-02-01","validTo":"2027-01-01"}`,
		"unknown field":      `{` + base + `,"sameBatch":true,"extra":1}`,
	}
	for name, body := range tests {
		f := &fakeStore{}
		rec := call{method: "POST", path: reqPath, body: body, duties: DutyBatchRequirementWrite, headers: key("k")}.do(newRouter(f))
		if rec.Code != http.StatusBadRequest || problemOf(t, rec).Code != "COM-0001" || f.gotReq.ItemID != "" {
			t.Errorf("%s: %d %s", name, rec.Code, rec.Body)
		}
	}
}

func TestCreateRequirement_DutyKeyAndScope(t *testing.T) {
	body := `{"itemId":"` + pickItem + `","sameBatch":true,"legalEntityId":"` + entityID + `"}`
	f := &fakeStore{}
	if rec := post(reqPath, body, DutyBatchRequirementWrite).do(newRouter(f)); rec.Code != http.StatusBadRequest {
		t.Errorf("missing key: %d", rec.Code)
	}
	if rec := (call{method: "POST", path: reqPath, body: body, duties: DutyRead, headers: key("k")}).do(newRouter(f)); rec.Code != http.StatusForbidden {
		t.Errorf("read duty wrote: %d", rec.Code)
	}
	h := key("k")
	h["X-Passage-Legal-Entity"] = ""
	rec := call{method: "POST", path: reqPath, body: body, duties: DutyBatchRequirementWrite, headers: h}.do(newRouter(f))
	if rec.Code != http.StatusForbidden || f.gotReq.ItemID != "" {
		t.Errorf("no scope: %d %s", rec.Code, rec.Body)
	}
}

func TestRequirements_ListGetRetire(t *testing.T) {
	f := &fakeStore{reqs: []store.CustomerRequirement{{ID: reqID, Level: 3}}}
	rec := call{method: "GET", path: reqPath + "?itemId=" + pickItem + "&customerId=" + strings.ToUpper(reqCustomer) + "&legalEntityId=" + entityID + "&activeOn=2027-03-01&limit=5", duties: DutyRead}.do(newRouter(f))
	if rec.Code != http.StatusOK || f.gotReqFilt.ItemID != pickItem || f.gotReqFilt.CustomerID != reqCustomer || f.gotReqFilt.LegalEntityID != entityID ||
		f.gotReqFilt.ActiveOn != "2027-03-01" || f.gotReqFilt.Limit != 5 {
		t.Fatalf("list %d %s filter %+v", rec.Code, rec.Body, f.gotReqFilt)
	}
	for _, q := range []string{"?itemId=x", "?customerId=x", "?legalEntityId=x", "?customerGroupId=x", "?activeOn=soon", "?limit=0"} {
		if rec := (call{method: "GET", path: reqPath + q, duties: DutyRead}).do(newRouter(f)); rec.Code != http.StatusBadRequest {
			t.Errorf("%s: %d", q, rec.Code)
		}
	}
	if rec := (call{method: "GET", path: reqPath + "/" + reqID, duties: DutyRead}).do(newRouter(f)); rec.Code != http.StatusOK {
		t.Errorf("get: %d", rec.Code)
	}
	if rec := (call{method: "GET", path: reqPath + "/nope", duties: DutyRead}).do(newRouter(f)); rec.Code != http.StatusBadRequest {
		t.Errorf("bad id: %d", rec.Code)
	}
	retire := call{method: "POST", path: reqPath + "/" + reqID + ":retire", body: `{"version":2}`, duties: DutyBatchRequirementWrite, headers: key("x-1")}
	if rec := retire.do(newRouter(f)); rec.Code != http.StatusOK || f.gotVersion != 2 || f.gotIdem.Key != "x-1" {
		t.Errorf("retire: %d %s version %d", rec.Code, rec.Body, f.gotVersion)
	}
	retire.body = `{"version":0}`
	if rec := retire.do(newRouter(f)); rec.Code != http.StatusBadRequest {
		t.Errorf("retire without version: %d", rec.Code)
	}
	retire.body, retire.duties = `{"version":2}`, DutyRead
	if rec := retire.do(newRouter(f)); rec.Code != http.StatusForbidden {
		t.Errorf("retire with read duty: %d", rec.Code)
	}
	f.err = httpx.NewProblem(http.StatusNotFound, store.CodeRequirementNotFound, "missing")
	if rec := (call{method: "GET", path: reqPath + "/" + reqID, duties: DutyRead}).do(newRouter(f)); rec.Code != http.StatusNotFound {
		t.Errorf("missing: %d", rec.Code)
	}
}

// AC-24.14: sellable days 60 with batches expiring in 45 and 90 days propose
// only the 90-day batch; the rules and batch states come from the stores.
func storedReqFake() *fakeStore {
	exp := func(s string) *time.Time { t, _ := time.Parse("2006-01-02", s); return &t }
	days := func(n int) *int { return &n }
	return &fakeStore{
		reqs: []store.CustomerRequirement{
			{ID: "r-item", Level: 1, SellableDays: days(10)},
			{ID: "r-cust", Level: 3, SellableDays: days(60)},
		},
		cands: []store.PickCandidate{
			{BatchNumber: "L-45", Batch: domain.Batch{ID: batchOld, Disposition: "available", Qty: decimal.NewFromInt(50), Values: map[string]domain.Value{"expiry": {Date: exp("2027-02-15")}}}},
			{BatchNumber: "L-90", Batch: domain.Batch{ID: batchNew, Disposition: "available", Qty: decimal.NewFromInt(100), Values: map[string]domain.Value{"expiry": {Date: exp("2027-03-31")}}}},
			{BatchNumber: "L-HOLD", Batch: domain.Batch{ID: batchOnH, Disposition: "on-hold", Qty: decimal.NewFromInt(100), Values: map[string]domain.Value{"expiry": {Date: exp("2027-06-01")}}}},
		},
	}
}

const checkStoredBody = `{"itemId":"` + pickItem + `","customerId":"` + reqCustomer + `","shipDate":"2027-01-01","qty":"80",
	"onHand":[{"batchId":"` + batchOld + `","qty":"50"},{"batchId":"` + batchNew + `","qty":"100"},{"batchId":"` + batchOnH + `","qty":"100"}]}`

func TestCheckStored_AC2414(t *testing.T) {
	f := storedReqFake()
	rec := post(checkStoredPath, checkStoredBody, DutyRead).do(newRouter(f))
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d: %s", rec.Code, rec.Body)
	}
	got := decodeBody[checkStoredResponse](t, rec.Body.Bytes())
	if got.Requirement.SellableDays != 60 || len(got.Qualifying) != 1 || got.Qualifying[0].BatchID != batchNew || !got.QtyAvailable.Equal(decimal.NewFromInt(100)) {
		t.Fatalf("answer %+v", got)
	}
	if len(got.Excluded) != 2 || got.Excluded[0].BatchID != batchOld || !strings.Contains(got.Excluded[0].Reason, "45 days") ||
		got.Excluded[1].BatchID != batchOnH || got.Excluded[1].WarningCode != "PMF-0009" {
		t.Fatalf("excluded %+v", got.Excluded)
	}
	// The item default's 10 days are overridden by the customer's 60, so only
	// the customer's rule is named as applied; the rules are those of the
	// caller's legal entity.
	if len(got.Applied) != 1 || got.Applied[0] != "r-cust" || got.BatchNumbers[batchNew] != "L-90" || f.gotReqWho.CustomerID != reqCustomer ||
		f.gotReqDate != "2027-01-01" || f.gotItem != pickItem || f.gotReqEntity != entityID {
		t.Fatalf("applied %v numbers %v who %+v date %s item %s entity %s", got.Applied, got.BatchNumbers, f.gotReqWho, f.gotReqDate, f.gotItem, f.gotReqEntity)
	}
}

// The rules of another legal entity are refused unless it is in the caller's
// scope; an upper-case customer id is matched in canonical form.
func TestCheckStored_LegalEntityAndCanonicalIDs(t *testing.T) {
	other := "11111111-0000-4000-8000-0000000000ff"
	body := strings.Replace(checkStoredBody, `{"itemId"`, `{"legalEntityId":"`+other+`","itemId"`, 1)
	f := storedReqFake()
	if rec := post(checkStoredPath, body, DutyRead).do(newRouter(f)); rec.Code != http.StatusForbidden || problemOf(t, rec).Code != "AUTH-0003" || f.gotItem != "" {
		t.Fatalf("other legal entity: %d %s", rec.Code, rec.Body)
	}
	body = strings.Replace(checkStoredBody, reqCustomer, strings.ToUpper(reqCustomer), 1)
	if rec := post(checkStoredPath, body, DutyRead).do(newRouter(f)); rec.Code != http.StatusOK || f.gotReqWho.CustomerID != reqCustomer {
		t.Fatalf("upper-case customer: %d who %+v", rec.Code, f.gotReqWho)
	}
}

func TestCheckStored_SameBatchShortfall(t *testing.T) {
	f := storedReqFake()
	f.reqs = append(f.reqs, store.CustomerRequirement{ID: "r-same", Level: 2, SameBatch: func() *bool { b := true; return &b }()})
	body := strings.Replace(checkStoredBody, `"qty":"80"`, `"qty":"150"`, 1)
	rec := post(checkStoredPath, body, DutyRead).do(newRouter(f))
	if rec.Code != http.StatusConflict || problemOf(t, rec).Code != domain.CodeSameBatch || !strings.Contains(rec.Body.String(), "largest 100") {
		t.Fatalf("shortfall %d %s", rec.Code, rec.Body)
	}
	ok := post(checkStoredPath, checkStoredBody, DutyRead).do(newRouter(f))
	if got := decodeBody[checkStoredResponse](t, ok.Body.Bytes()); ok.Code != http.StatusOK || got.SameBatchID == nil || *got.SameBatchID != batchNew {
		t.Fatalf("same batch %d %s", ok.Code, ok.Body)
	}
}

func TestCheckStored_RejectsAndStoreErrors(t *testing.T) {
	tests := map[string]string{
		"bad item":     strings.Replace(checkStoredBody, pickItem, "x", 1),
		"bad customer": strings.Replace(checkStoredBody, reqCustomer, "x", 1),
		"bad group":    strings.Replace(checkStoredBody, `"customerId"`, `"customerGroupId":"g-1","customerId"`, 1),
		"bad entity":   strings.Replace(checkStoredBody, `{"itemId"`, `{"legalEntityId":"le-1","itemId"`, 1),
		"bad date":     strings.Replace(checkStoredBody, "2027-01-01", "soon", 1),
		"zero qty":     strings.Replace(checkStoredBody, `"qty":"80"`, `"qty":"0"`, 1),
		"no batches":   `{"itemId":"` + pickItem + `","shipDate":"2027-01-01","qty":"1","onHand":[]}`,
		"repeat batch": strings.Replace(checkStoredBody, batchNew, batchOld, 1),
	}
	for name, body := range tests {
		if rec := post(checkStoredPath, body, DutyRead).do(newRouter(storedReqFake())); rec.Code != http.StatusBadRequest || problemOf(t, rec).Code != "COM-0001" {
			t.Errorf("%s: %d %s", name, rec.Code, rec.Body)
		}
	}
	if rec := post(checkStoredPath, checkStoredBody, "x").do(newRouter(storedReqFake())); rec.Code != http.StatusForbidden {
		t.Errorf("without duty: %d", rec.Code)
	}
	f := storedReqFake()
	f.err = httpx.NewProblem(http.StatusNotFound, store.CodeBatchNotFound, "missing")
	if rec := post(checkStoredPath, checkStoredBody, DutyRead).do(newRouter(f)); rec.Code != http.StatusNotFound {
		t.Errorf("store error: %d", rec.Code)
	}
}
