package api

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/services/purchasing/internal/domain"
	"github.com/BenjaminSRussell/passage/services/purchasing/internal/store"
)

// svcLog is what the service line methods of the fake received; err, when set, is what they answer.
type svcLog struct {
	puts, deletes, checks int
	poID                  string
	lineNo, ifMatch       int
	in                    store.ServiceInput
	unplanned, entry      decimal.Decimal
	models                map[string]domain.ModelService
	createdModel          *domain.ModelService
	createdNodes          []domain.OutlineNode
	updatedModel          *domain.ModelService
	err                   error
}

func (f *fakeStore) PutServiceSpec(_ context.Context, poID string, lineNo, ifMatch int, in store.ServiceInput) (store.ServiceResult, error) {
	f.svc.puts++
	f.svc.poID, f.svc.lineNo, f.svc.ifMatch, f.svc.in = poID, lineNo, ifMatch, in
	if f.svc.err != nil {
		return store.ServiceResult{}, f.svc.err
	}
	return store.ServiceResult{ServiceSpec: domain.ServiceSpec{PurchaseOrderID: poID, LineNo: lineNo, Outline: []domain.OutlineRow{}}, PORowVersion: ifMatch + 1}, nil
}

func (f *fakeStore) GetServiceSpec(_ context.Context, poID string, lineNo int) (store.ServiceResult, error) {
	if f.svc.err != nil {
		return store.ServiceResult{}, f.svc.err
	}
	return store.ServiceResult{ServiceSpec: domain.ServiceSpec{PurchaseOrderID: poID, LineNo: lineNo, Outline: []domain.OutlineRow{}}, PORowVersion: 7}, nil
}

func (f *fakeStore) ListServiceSpecs(_ context.Context, poID string) ([]domain.ServiceSpec, error) {
	f.svc.poID = poID
	return []domain.ServiceSpec{{PurchaseOrderID: poID, LineNo: 3, Outline: []domain.OutlineRow{}}}, f.svc.err
}

func (f *fakeStore) DeleteServiceSpec(_ context.Context, poID string, lineNo, ifMatch int) (int, error) {
	f.svc.deletes++
	f.svc.poID, f.svc.lineNo, f.svc.ifMatch = poID, lineNo, ifMatch
	return ifMatch + 1, f.svc.err
}

func (f *fakeStore) CheckServiceLimit(_ context.Context, poID string, lineNo int, unplanned, entry decimal.Decimal) (domain.LimitCheck, error) {
	f.svc.checks++
	f.svc.poID, f.svc.lineNo, f.svc.unplanned, f.svc.entry = poID, lineNo, unplanned, entry
	if f.svc.err != nil {
		return domain.LimitCheck{}, f.svc.err
	}
	return domain.CheckUnplanned(decimal.NewFromInt(2000), unplanned, entry, nil, lineNo)
}

func (f *fakeStore) CreateModelServiceSpec(_ context.Context, in domain.ModelService, nodes []domain.OutlineNode, _ store.Idem) (domain.ModelService, error) {
	in.ID, in.Version, in.Outline = ids.New(), 1, []domain.OutlineRow{}
	f.svc.createdModel, f.svc.createdNodes = &in, nodes
	return in, f.svc.err
}

func (f *fakeStore) GetModelServiceSpec(_ context.Context, id string) (domain.ModelService, error) {
	if m, ok := f.svc.models[id]; ok {
		return m, nil
	}
	return domain.ModelService{}, httpx.NewProblem(http.StatusNotFound, store.CodeModelServiceNotFound, "not found")
}

func (f *fakeStore) ListModelServiceSpecs(context.Context, store.ModelServiceFilter, string, int) (store.Page[domain.ModelService], error) {
	out := []domain.ModelService{}
	for _, m := range f.svc.models {
		out = append(out, m)
	}
	return store.Page[domain.ModelService]{Items: out}, f.svc.err
}

func (f *fakeStore) UpdateModelServiceSpec(_ context.Context, id string, _ int, in domain.ModelService, _ []domain.OutlineNode) (domain.ModelService, error) {
	in.ID, in.Version, in.Outline = id, 2, []domain.OutlineRow{}
	f.svc.updatedModel = &in
	return in, f.svc.err
}

const (
	svcPath    = "/v1/procurement/purchase-orders/" + planPO + "/lines/3/service"
	modelsPath = "/v1/procurement/model-service-specs"
)

func TestPutServiceSpec_ReachesTheStore(t *testing.T) {
	h := newHarness(t)
	item := ids.New()
	body := `{"valueLimit":"2000","expectedValue":"500","contractAgreementLineId":"` + item + `","outline":[
		{"kind":"LEVEL","description":"Maintenance","total":"350","children":[
			{"kind":"ITEM","description":"Inspection","qty":"3","unit":"h","unitPrice":"100"},
			{"kind":"ITEM","productId":"` + item + `","qty":"1","unit":"ea","unitPrice":"50"}]}]}`
	rec := h.do(call{method: "PUT", path: svcPath, duties: maintainDuty, body: body, headers: ifMatch4})
	if rec.Code != http.StatusOK || rec.Header().Get("ETag") != `"5"` {
		t.Fatalf("status %d ETag %q: %s", rec.Code, rec.Header().Get("ETag"), rec.Body)
	}
	got := h.fake.svc
	if got.poID != planPO || got.lineNo != 3 || got.ifMatch != 4 || !got.in.Terms.ValueLimit.Equal(decimal.NewFromInt(2000)) ||
		!got.in.Terms.ExpectedValue.Equal(decimal.NewFromInt(500)) || got.in.Terms.ContractAgreementLineID != item {
		t.Errorf("store got %+v", got)
	}
	lvl := got.in.Outline[0]
	if lvl.Kind != domain.OutlineLevel || lvl.Total == nil || !lvl.Total.Equal(decimal.NewFromInt(350)) || len(lvl.Children) != 2 ||
		!lvl.Children[0].UnitPrice.Equal(decimal.NewFromInt(100)) || lvl.Children[1].ProductID != item {
		t.Errorf("outline got %+v", lvl)
	}
}

func TestPutServiceSpec_Rejections(t *testing.T) {
	model := ids.New()
	deep := `{"kind":"LEVEL","description":"a","children":[`
	closing := ``
	for i := 0; i < domain.MaxOutlineDepth; i++ {
		deep += `{"kind":"LEVEL","description":"a","children":[`
		closing += `]}`
	}
	deep += `{"kind":"ITEM","description":"x","qty":"1","unit":"h","unitPrice":"1"}` + closing + `]}`
	tests := []struct {
		name, body, field string
		headers           map[string]string
		duties            []string
		status            int
	}{
		{"no limit", `{}`, "valueLimit", ifMatch4, maintainDuty, 422},
		{"limit not a decimal", `{"valueLimit":"lots"}`, "valueLimit", ifMatch4, maintainDuty, 422},
		{"expected not a decimal", `{"valueLimit":"1","expectedValue":"x"}`, "expectedValue", ifMatch4, maintainDuty, 422},
		{"contract line not a uuid", `{"valueLimit":"1","contractAgreementLineId":"x"}`, "contractAgreementLineId", ifMatch4, maintainDuty, 422},
		{"model and outline", `{"valueLimit":"1","modelId":"` + model + `","outline":[{"kind":"ITEM","description":"x","qty":"1","unit":"h"}]}`, "outline", ifMatch4, maintainDuty, 422},
		{"qty not a decimal", `{"valueLimit":"1","outline":[{"kind":"ITEM","qty":"x"}]}`, "outline[0].qty", ifMatch4, maintainDuty, 422},
		{"product not a uuid", `{"valueLimit":"1","outline":[{"kind":"ITEM","productId":"p"}]}`, "outline[0].productId", ifMatch4, maintainDuty, 422},
		{"nested too deep", `{"valueLimit":"1","outline":[` + deep + `]}`, "outline", ifMatch4, maintainDuty, 422},
		{"line number not a number", `{"valueLimit":"1"}`, "lineNo", ifMatch4, maintainDuty, 422},
		{"no If-Match", `{"valueLimit":"1"}`, "", nil, maintainDuty, http.StatusPreconditionRequired},
		{"inquire duty only", `{"valueLimit":"1"}`, "", ifMatch4, []string{dutyPurchaseOrderInquire}, http.StatusForbidden},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			h := newHarness(t)
			path := svcPath
			if tc.field == "lineNo" {
				path = "/v1/procurement/purchase-orders/" + planPO + "/lines/x/service"
			}
			rec := h.do(call{method: "PUT", path: path, duties: tc.duties, body: tc.body, headers: tc.headers})
			if rec.Code != tc.status {
				t.Fatalf("status %d, want %d: %s", rec.Code, tc.status, rec.Body)
			}
			if tc.field != "" && !strings.Contains(rec.Body.String(), tc.field) {
				t.Errorf("problem does not name %q: %s", tc.field, rec.Body)
			}
			if h.fake.svc.puts != 0 {
				t.Errorf("the store was called %d times", h.fake.svc.puts)
			}
		})
	}
}

func TestPutServiceSpec_DomainRuleIs422WithItsCode(t *testing.T) {
	h := newHarness(t)
	h.fake.svc.err = &domain.Error{Code: domain.CodeServiceOutline, Detail: "items do not sum"}
	rec := h.do(call{method: "PUT", path: svcPath, duties: maintainDuty, body: `{"valueLimit":"1"}`, headers: ifMatch4})
	if rec.Code != 422 || problemCode(t, rec) != domain.CodeServiceOutline {
		t.Fatalf("status %d code %s, want 422 %s", rec.Code, problemCode(t, rec), domain.CodeServiceOutline)
	}
}

func TestServiceSpec_ReadAndDelete(t *testing.T) {
	h := newHarness(t)
	for _, duty := range []string{dutyPurchaseOrderInquire, dutyPurchaseOrderLookup} {
		rec := h.do(call{method: "GET", path: svcPath, duties: []string{duty}})
		if rec.Code != 200 || rec.Header().Get("ETag") != `"7"` {
			t.Fatalf("get with %s: %d ETag %q: %s", duty, rec.Code, rec.Header().Get("ETag"), rec.Body)
		}
	}
	rec := h.do(call{method: "GET", path: "/v1/procurement/purchase-orders/" + planPO + "/service-lines", duties: []string{dutyPurchaseOrderLookup}})
	var page struct{ Items []domain.ServiceSpec }
	if err := json.Unmarshal(rec.Body.Bytes(), &page); err != nil || rec.Code != 200 || len(page.Items) != 1 || page.Items[0].LineNo != 3 {
		t.Fatalf("list: %d %s", rec.Code, rec.Body)
	}
	if rec := h.do(call{method: "DELETE", path: svcPath, duties: []string{dutyPurchaseOrderInquire}, headers: ifMatch4}); rec.Code != http.StatusForbidden || h.fake.svc.deletes != 0 {
		t.Fatalf("delete with the inquire duty: %d", rec.Code)
	}
	rec = h.do(call{method: "DELETE", path: svcPath, duties: maintainDuty, headers: ifMatch4})
	if rec.Code != http.StatusNoContent || rec.Header().Get("ETag") != `"5"` || h.fake.svc.ifMatch != 4 {
		t.Fatalf("delete: %d ETag %q", rec.Code, rec.Header().Get("ETag"))
	}
	h.fake.svc.err = httpx.NewProblem(http.StatusNotFound, store.CodeServiceSpecNotFound, "line 3 has no service specification")
	if rec := h.do(call{method: "GET", path: svcPath, duties: []string{dutyPurchaseOrderInquire}}); rec.Code != 404 || problemCode(t, rec) != store.CodeServiceSpecNotFound {
		t.Fatalf("missing specification: %d %s", rec.Code, rec.Body)
	}
}

// AC-17.17: limit 2,000.00, 1,900.00 entered, a further 200.00 is refused with PRC-SRV-003 and
// 100.00 fits.
func TestCheckServiceLimit_AC1717(t *testing.T) {
	h := newHarness(t)
	path := svcPath + "/limit-check"
	rec := h.do(call{method: "GET", path: path + "?unplanned=1900&entry=200", duties: []string{dutyPurchaseOrderLookup}})
	var refused map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &refused); err != nil || rec.Code != 200 || refused["allowed"] != false ||
		refused["code"] != domain.CodeServiceLimit || refused["remaining"] != "100" || refused["lineNo"] != float64(3) {
		t.Fatalf("200 over 1900: %d %s", rec.Code, rec.Body)
	}
	rec = h.do(call{method: "GET", path: path + "?unplanned=1900&entry=100", duties: []string{dutyPurchaseOrderInquire}})
	var out domain.LimitCheck
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil || rec.Code != 200 || !out.Allowed || !out.ValueRemaining.Equal(decimal.NewFromInt(100)) {
		t.Fatalf("100 over 1900: %d %s", rec.Code, rec.Body)
	}
	if !h.fake.svc.unplanned.Equal(decimal.NewFromInt(1900)) || h.fake.svc.lineNo != 3 {
		t.Errorf("store got %+v", h.fake.svc)
	}
	for name, c := range map[string]call{
		"no entry":        {method: "GET", path: path, duties: []string{dutyPurchaseOrderLookup}},
		"entry not a num": {method: "GET", path: path + "?entry=x", duties: []string{dutyPurchaseOrderLookup}},
		"negative":        {method: "GET", path: path + "?entry=-1", duties: []string{dutyPurchaseOrderLookup}},
		"no duty":         {method: "GET", path: path + "?entry=1", duties: []string{dutyAgreementMaintain}},
	} {
		before := h.fake.svc.checks
		if rec := h.do(c); rec.Code < 400 || h.fake.svc.checks != before {
			t.Errorf("%s: status %d, store calls %d -> %d", name, rec.Code, before, h.fake.svc.checks)
		}
	}
}

func TestModelServiceSpec_CreateValidatesAndNeedsTheDuty(t *testing.T) {
	good := `{"legalEntityId":"` + leA + `","code":"MAINT","name":" Maintenance ","currency":"USD","outline":[
		{"kind":"ITEM","description":"Inspection","qty":"2","unit":"h","unitPrice":"90"}]}`
	maintain := []string{dutyPolicyMaintain}
	for _, tc := range []struct {
		name, body string
		duties     []string
		status     int
	}{
		{"created", good, maintain, 201},
		{"no duty", good, []string{dutyPurchaseOrderInquire}, 403},
		{"no legal entity", `{"code":"MAINT","name":"n","currency":"USD"}`, maintain, 422},
		{"other legal entity", `{"legalEntityId":"` + ids.New() + `","code":"MAINT","name":"n","currency":"USD"}`, maintain, 403},
		{"bad code", `{"legalEntityId":"` + leA + `","code":"main t","name":"n","currency":"USD"}`, maintain, 422},
		{"bad currency", `{"legalEntityId":"` + leA + `","code":"MAINT","name":"n","currency":"usd"}`, maintain, 422},
		{"no name", `{"legalEntityId":"` + leA + `","code":"MAINT","name":" ","currency":"USD"}`, maintain, 422},
		{"bad status", `{"legalEntityId":"` + leA + `","code":"MAINT","name":"n","currency":"USD","status":"OLD"}`, maintain, 422},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h := newHarness(t)
			rec := h.do(call{method: "POST", path: modelsPath, duties: tc.duties, body: tc.body, headers: map[string]string{"Idempotency-Key": "k1"}})
			if rec.Code != tc.status {
				t.Fatalf("status %d, want %d: %s", rec.Code, tc.status, rec.Body)
			}
			m := h.fake.svc.createdModel
			if tc.status == 201 && (m == nil || m.Name != "Maintenance" || m.Status != domain.ModelActive || len(h.fake.svc.createdNodes) != 1) {
				t.Errorf("store got %+v %+v", m, h.fake.svc.createdNodes)
			}
			if tc.status != 201 && m != nil {
				t.Errorf("the store was called")
			}
		})
	}
}

func TestModelServiceSpec_ChangeKeepsCodeAndEntity(t *testing.T) {
	h := newHarness(t)
	id := ids.New()
	h.fake.svc.models = map[string]domain.ModelService{id: {ID: id, LegalEntityID: leA, Code: "MAINT", Name: "Old", Currency: "USD", Status: domain.ModelActive, Version: 1}}
	body := `{"legalEntityId":"` + ids.New() + `","code":"OTHER","name":"New","currency":"EUR","status":"INACTIVE"}`
	rec := h.do(call{method: "PUT", path: modelsPath + "/" + id, duties: []string{dutyPolicyMaintain}, body: body, headers: ifMatch4})
	u := h.fake.svc.updatedModel
	if rec.Code != 200 || rec.Header().Get("ETag") != `"2"` || u == nil || u.Code != "MAINT" || u.LegalEntityID != leA || u.Currency != "EUR" || u.Status != domain.ModelInactive {
		t.Fatalf("put: %d %s %+v", rec.Code, rec.Body, u)
	}
	if rec := h.do(call{method: "PUT", path: modelsPath + "/" + id, duties: []string{dutyPolicyMaintain}, body: body}); rec.Code != http.StatusPreconditionRequired {
		t.Errorf("no If-Match: %d", rec.Code)
	}
	if rec := h.do(call{method: "GET", path: modelsPath + "/" + id, duties: []string{dutyPurchaseOrderInquire}}); rec.Code != 200 {
		t.Errorf("get: %d", rec.Code)
	}
	if rec := h.do(call{method: "GET", path: modelsPath + "/" + ids.New(), duties: []string{dutyPurchaseOrderInquire}}); rec.Code != 404 || problemCode(t, rec) != store.CodeModelServiceNotFound {
		t.Errorf("missing model: %d %s", rec.Code, rec.Body)
	}
	if rec := h.do(call{method: "GET", path: modelsPath + "?status=OLD", duties: []string{dutyPurchaseOrderInquire}}); rec.Code != 422 {
		t.Errorf("bad filter: %d", rec.Code)
	}
	if rec := h.do(call{method: "GET", path: modelsPath + "?legalEntityId=" + leA + "&status=ACTIVE&q=main", duties: []string{dutyPurchaseOrderInquire}}); rec.Code != 200 {
		t.Errorf("list: %d", rec.Code)
	}
}
