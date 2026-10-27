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
	"github.com/BenjaminSRussell/passage/services/fulfillment/internal/store"
)

const packingDuties = "WarehouseView,WarehousePack,PackingSlipPost,PackingRuleMaintain,PackagingFeeMaintain,WarehouseReceive"

var packingHeaders = map[string]string{"X-Passage-Duties": packingDuties}

func withKey(key string, extra map[string]string) map[string]string {
	out := map[string]string{"Idempotency-Key": key}
	for k, v := range packingHeaders {
		out[k] = v
	}
	for k, v := range extra {
		out[k] = v
	}
	return out
}

// packingCalls records what the packing handlers passed to the store.
type packingCalls struct {
	containerType store.ContainerType
	rule          store.PackingRule
	listProduct   string
	listLimit     int
	packaging     store.ProductPackaging
	feeRate       store.FeeRate
	report        store.ReportFilter
	weights       map[string]decimal.Decimal
	ret           store.ReturnInput
	balancesOf    string
}

func (f *fakeStore) ListContainerTypes(_ context.Context, limit int) ([]store.ContainerType, error) {
	f.packing.listLimit = limit
	return []store.ContainerType{{Code: "PALLET", TareKg: decimal.RequireFromString("25"), Returnable: true}}, nil
}

func (f *fakeStore) SaveContainerType(_ context.Context, ct store.ContainerType, idem store.Idem) (store.ContainerType, error) {
	f.lastIdem = idem
	f.packing.containerType = ct
	return ct, nil
}

func (f *fakeStore) ListPackingRules(_ context.Context, productID string, limit int) ([]store.PackingRule, error) {
	f.packing.listProduct, f.packing.listLimit = productID, limit
	return []store.PackingRule{}, nil
}

func (f *fakeStore) SavePackingRule(_ context.Context, rule store.PackingRule, idem store.Idem) (store.PackingRule, error) {
	f.lastIdem = idem
	f.packing.rule = rule
	rule.ID = ids.New()
	return rule, nil
}

func (f *fakeStore) ListProductPackaging(_ context.Context, productID string, limit int) ([]store.ProductPackaging, error) {
	f.packing.listProduct, f.packing.listLimit = productID, limit
	return []store.ProductPackaging{}, nil
}

func (f *fakeStore) SaveProductPackaging(_ context.Context, pp store.ProductPackaging, idem store.Idem) (store.ProductPackaging, error) {
	f.lastIdem = idem
	f.packing.packaging = pp
	return pp, nil
}

func (f *fakeStore) ListFeeRates(_ context.Context, _ string, limit int) ([]store.FeeRate, error) {
	f.packing.listLimit = limit
	return []store.FeeRate{}, nil
}

func (f *fakeStore) SaveFeeRate(_ context.Context, rate store.FeeRate, idem store.Idem) (store.FeeRate, error) {
	f.lastIdem = idem
	f.packing.feeRate = rate
	return rate, nil
}

func (f *fakeStore) PackagingReport(_ context.Context, rf store.ReportFilter) ([]store.PackagingReportRow, error) {
	f.packing.report = rf
	return []store.PackagingReportRow{{MaterialCode: "CARDBOARD", WeightKg: decimal.RequireFromString("20"),
		FeeAmount: decimal.RequireFromString("10.00"), Currency: "EUR"}}, nil
}

func (f *fakeStore) AutoPack(_ context.Context, id string, version int, weights map[string]decimal.Decimal, idem store.Idem) (store.AutoPackResult, error) {
	f.lastIdem = idem
	f.packing.weights = weights
	if version != 3 {
		return store.AutoPackResult{}, httpx.NewProblem(http.StatusPreconditionFailed, store.CodeVersion, "shipment version changed")
	}
	return store.AutoPackResult{Shipment: store.Shipment{ID: id, Version: 4}, Containers: []store.Container{}, Unpacked: []string{}, Warnings: []store.AutoPackWarning{}}, nil
}

func (f *fakeStore) ListReturnableBalances(_ context.Context, customerID string, _ int) ([]store.ReturnableBalance, error) {
	f.packing.balancesOf = customerID
	return []store.ReturnableBalance{{CustomerID: customerID, ContainerType: "PALLET", Balance: decimal.RequireFromString("6")}}, nil
}

func (f *fakeStore) ReturnPackaging(_ context.Context, in store.ReturnInput, idem store.Idem) (store.ReturnableBalance, error) {
	f.lastIdem = idem
	f.packing.ret = in
	if in.Qty.GreaterThan(decimal.NewFromInt(6)) {
		return store.ReturnableBalance{}, httpx.NewProblem(http.StatusUnprocessableEntity, store.CodeRTPNegative, "returnable packaging balance would become negative")
	}
	return store.ReturnableBalance{CustomerID: in.CustomerID, ContainerType: in.ContainerType, Balance: decimal.NewFromInt(6).Sub(in.Qty)}, nil
}

func expectField(t *testing.T, rec interface {
	Result() *http.Response
}, body, field string) {
	t.Helper()
	if rec.Result().StatusCode != http.StatusBadRequest || !strings.Contains(body, CodeValidation) || !strings.Contains(body, `"`+field+`"`) {
		t.Fatalf("status = %d body = %s, want 400 %s naming %s", rec.Result().StatusCode, body, CodeValidation, field)
	}
}

func TestSaveContainerType_ValidatesAndPassesMaterial(t *testing.T) {
	fs := &fakeStore{}
	h := newServer(t, fs)
	path := "/api/v1/fulfillment/container-types"
	rec := do(t, h, http.MethodPost, path, `{"code":"","tareKg":"0.5"}`, withKey("ct-1", nil))
	expectField(t, rec, rec.Body.String(), "code")
	rec = do(t, h, http.MethodPost, path, `{"code":"PALLET","tareKg":"-1"}`, withKey("ct-2", nil))
	expectField(t, rec, rec.Body.String(), "tareKg")
	rec = do(t, h, http.MethodPost, path, `{"code":"PALLET","tareKg":"1","packagingMaterial":"wood pallet!"}`, withKey("ct-3", nil))
	expectField(t, rec, rec.Body.String(), "packagingMaterial")

	rec = do(t, h, http.MethodPost, path, `{"code":"C12","tareKg":"0.5","packagingMaterial":" CARDBOARD "}`, withKey("ct-4", nil))
	if rec.Code != http.StatusCreated {
		t.Fatalf("status = %d body = %s, want 201", rec.Code, rec.Body.String())
	}
	got := fs.packing.containerType
	if got.Code != "C12" || !got.TareKg.Equal(decimal.RequireFromString("0.5")) || got.PackagingMaterial == nil || *got.PackagingMaterial != "CARDBOARD" {
		t.Fatalf("store got %+v, want C12 0.5 kg CARDBOARD", got)
	}
	rec = do(t, h, http.MethodPost, path, `{"code":"PALLET","tareKg":"25","returnable":true,"packagingMaterial":""}`, withKey("ct-5", nil))
	if rec.Code != http.StatusCreated || fs.packing.containerType.PackagingMaterial != nil || !fs.packing.containerType.Returnable {
		t.Fatalf("status = %d store got %+v, want returnable with no material", rec.Code, fs.packing.containerType)
	}
}

func TestSavePackingRule_ValidatesIdsAndQuantities(t *testing.T) {
	fs := &fakeStore{}
	h := newServer(t, fs)
	path := "/api/v1/fulfillment/packing-rules"
	product := ids.New()
	cases := []struct{ body, field string }{
		{`{"productId":"nope","containerType":"C12","qtyPerContainer":"12","maxWeightKg":"15"}`, "productId"},
		{`{"productId":"` + product + `","customerId":"c1","containerType":"C12","qtyPerContainer":"12","maxWeightKg":"15"}`, "customerId"},
		{`{"productId":"` + product + `","containerType":"","qtyPerContainer":"12","maxWeightKg":"15"}`, "containerType"},
		{`{"productId":"` + product + `","containerType":"C12","qtyPerContainer":"0","maxWeightKg":"15"}`, "qtyPerContainer"},
		{`{"productId":"` + product + `","containerType":"C12","qtyPerContainer":"12","maxWeightKg":"0.0000001"}`, "maxWeightKg"},
	}
	for i, tc := range cases {
		rec := do(t, h, http.MethodPost, path, tc.body, withKey("pr-bad-"+tc.field, nil))
		if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), `"`+tc.field+`"`) {
			t.Fatalf("case %d: status = %d body = %s, want 400 naming %s", i, rec.Code, rec.Body.String(), tc.field)
		}
	}
	// An empty customerId is the product's general rule.
	good := `{"productId":"` + product + `","customerId":"","containerType":"C12","qtyPerContainer":"12","maxWeightKg":"15"}`
	rec := do(t, h, http.MethodPost, path, good, withKey("pr-ok", nil))
	if rec.Code != http.StatusCreated || fs.packing.rule.CustomerID != nil || fs.packing.rule.ProductID != product {
		t.Fatalf("status = %d store got %+v, want a general rule for %s", rec.Code, fs.packing.rule, product)
	}
}

func TestPackingRuleMaintainIsRequired(t *testing.T) {
	h := newServer(t, &fakeStore{})
	body := `{"productId":"` + ids.New() + `","containerType":"C12","qtyPerContainer":"12","maxWeightKg":"15"}`
	rec := do(t, h, http.MethodPost, "/api/v1/fulfillment/packing-rules", body,
		map[string]string{"Idempotency-Key": "k", "X-Passage-Duties": "WarehouseView,WarehousePack"})
	if rec.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403 without PackingRuleMaintain", rec.Code)
	}
	rec = do(t, h, http.MethodPost, "/api/v1/fulfillment/returnable-packaging:return", `{"customerId":"`+ids.New()+`","containerType":"PALLET","qty":"1"}`,
		map[string]string{"Idempotency-Key": "k2", "X-Passage-Duties": "WarehouseView"})
	if rec.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403 without WarehouseReceive", rec.Code)
	}
}

func TestLists_FilterAndValidate(t *testing.T) {
	fs := &fakeStore{}
	h := newServer(t, fs)
	product := ids.New()
	rec := do(t, h, http.MethodGet, "/api/v1/fulfillment/packing-rules?productId="+product+"&limit=5", "", packingHeaders)
	if rec.Code != http.StatusOK || fs.packing.listProduct != product || fs.packing.listLimit != 5 || !strings.Contains(rec.Body.String(), `"items":[]`) {
		t.Fatalf("status = %d body = %s filter = %+v", rec.Code, rec.Body.String(), fs.packing)
	}
	for _, path := range []string{
		"/api/v1/fulfillment/packing-rules?productId=x",
		"/api/v1/fulfillment/product-packaging?productId=x",
		"/api/v1/fulfillment/packaging-fee-rates?countryCode=deu",
		"/api/v1/fulfillment/returnable-packaging?customerId=x",
		"/api/v1/fulfillment/container-types?limit=0",
	} {
		rec := do(t, h, http.MethodGet, path, "", packingHeaders)
		if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), CodeValidation) {
			t.Fatalf("%s: status = %d body = %s, want 400", path, rec.Code, rec.Body.String())
		}
	}
	rec = do(t, h, http.MethodGet, "/api/v1/fulfillment/container-types", "", packingHeaders)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"code":"PALLET"`) || fs.packing.listLimit != 100 {
		t.Fatalf("status = %d body = %s limit = %d", rec.Code, rec.Body.String(), fs.packing.listLimit)
	}
}

func TestAutoPack_RequiresIfMatchValidatesWeightsAndSetsETag(t *testing.T) {
	fs := &fakeStore{}
	h := newServer(t, fs)
	path := "/api/v1/fulfillment/shipments/" + ids.New() + ":auto-pack"
	rec := do(t, h, http.MethodPost, path, `{}`, withKey("ap-1", nil))
	if rec.Code != http.StatusPreconditionRequired {
		t.Fatalf("status = %d, want 428", rec.Code)
	}
	line := ids.New()
	rec = do(t, h, http.MethodPost, path, `{"unitWeights":{"`+line+`":"0.2"}}`, withKey("ap-2", map[string]string{"If-Match": `"3"`}))
	if rec.Code != http.StatusCreated || rec.Header().Get("ETag") != `"4"` {
		t.Fatalf("status = %d etag = %q body = %s, want 201 \"4\"", rec.Code, rec.Header().Get("ETag"), rec.Body.String())
	}
	if w, ok := fs.packing.weights[line]; !ok || !w.Equal(decimal.RequireFromString("0.2")) {
		t.Fatalf("weights passed = %v, want %s: 0.2", fs.packing.weights, line)
	}
	rec = do(t, h, http.MethodPost, path, `{"unitWeights":{"not-a-line":"0.2"}}`, withKey("ap-3", map[string]string{"If-Match": `"3"`}))
	expectField(t, rec, rec.Body.String(), "unitWeights")
	rec = do(t, h, http.MethodPost, path, `{"unitWeights":{"`+line+`":"-1"}}`, withKey("ap-4", map[string]string{"If-Match": `"3"`}))
	expectField(t, rec, rec.Body.String(), "unitWeights."+line)
	rec = do(t, h, http.MethodPost, path, `{}`, withKey("ap-5", map[string]string{"If-Match": `"2"`}))
	if rec.Code != http.StatusPreconditionFailed || !strings.Contains(rec.Body.String(), store.CodeVersion) {
		t.Fatalf("status = %d, want 412 conflict_version", rec.Code)
	}
}

func TestProductPackaging_Validates(t *testing.T) {
	fs := &fakeStore{}
	h := newServer(t, fs)
	path := "/api/v1/fulfillment/product-packaging"
	product := ids.New()
	rec := do(t, h, http.MethodPost, path, `{"productId":"`+product+`","materialCode":"","weightPerUnitKg":"0.2"}`, withKey("pp-1", nil))
	expectField(t, rec, rec.Body.String(), "materialCode")
	rec = do(t, h, http.MethodPost, path, `{"productId":"`+product+`","materialCode":"CARDBOARD","weightPerUnitKg":"0.1234567"}`, withKey("pp-2", nil))
	expectField(t, rec, rec.Body.String(), "weightPerUnitKg")
	rec = do(t, h, http.MethodPost, path, `{"productId":"`+product+`","materialCode":"CARDBOARD","weightPerUnitKg":"0.2"}`, withKey("pp-3", nil))
	if rec.Code != http.StatusCreated || fs.packing.packaging.MaterialCode != "CARDBOARD" || !fs.packing.packaging.WeightPerUnitKg.Equal(decimal.RequireFromString("0.2")) {
		t.Fatalf("status = %d store got %+v", rec.Code, fs.packing.packaging)
	}
}

func TestFeeRate_ValidatesCountryCurrencyAndDate(t *testing.T) {
	fs := &fakeStore{}
	h := newServer(t, fs)
	path := "/api/v1/fulfillment/packaging-fee-rates"
	base := func(country, cur, from string) string {
		return `{"countryCode":"` + country + `","materialCode":"CARDBOARD","currency":"` + cur +
			`","ratePerKg":"0.50","validFrom":"` + from + `"}`
	}
	cases := []struct{ name, body, field string }{
		{"country", base("germany", "EUR", "2026-01-01"), "countryCode"},
		{"currency", base("DE", "euro", "2026-01-01"), "currency"},
		{"date", base("DE", "EUR", "01/01/2026"), "validFrom"},
	}
	for _, tc := range cases {
		rec := do(t, h, http.MethodPost, path, tc.body, withKey("fr-"+tc.name, nil))
		expectField(t, rec, rec.Body.String(), tc.field)
	}
	rec := do(t, h, http.MethodPost, path, base("DE", "EUR", "2026-01-01"), withKey("fr-ok", nil))
	if rec.Code != http.StatusCreated || fs.packing.feeRate.ValidFrom != "2026-01-01" || !fs.packing.feeRate.RatePerKg.Equal(decimal.RequireFromString("0.5")) {
		t.Fatalf("status = %d store got %+v", rec.Code, fs.packing.feeRate)
	}
}

func TestPackagingReport_RequiresCountryAndMonth(t *testing.T) {
	fs := &fakeStore{}
	h := newServer(t, fs)
	rec := do(t, h, http.MethodGet, "/api/v1/fulfillment/packaging-report?countryCode=DE&period=2026-13", "", packingHeaders)
	expectField(t, rec, rec.Body.String(), "period")
	rec = do(t, h, http.MethodGet, "/api/v1/fulfillment/packaging-report?period=2026-10", "", packingHeaders)
	expectField(t, rec, rec.Body.String(), "countryCode")
	le := ids.New()
	rec = do(t, h, http.MethodGet, "/api/v1/fulfillment/packaging-report?countryCode=DE&period=2026-10&legalEntityId="+le, "", packingHeaders)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d body = %s", rec.Code, rec.Body.String())
	}
	var body struct {
		CountryCode   string `json:"countryCode"`
		LegalEntityID string `json:"legalEntityId"`
		Items         []struct {
			MaterialCode string `json:"materialCode"`
			FeeAmount    string `json:"feeAmount"`
		} `json:"items"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if body.CountryCode != "DE" || body.LegalEntityID != le || len(body.Items) != 1 || body.Items[0].FeeAmount != "10" {
		t.Fatalf("report = %+v", body)
	}
	if fs.packing.report != (store.ReportFilter{CountryCode: "DE", Period: "2026-10", LegalEntityID: le}) {
		t.Fatalf("filter = %+v", fs.packing.report)
	}
}

func TestReturnPackaging_ValidatesAndRejectsNegative(t *testing.T) {
	fs := &fakeStore{}
	h := newServer(t, fs)
	cust := ids.New()
	rec := do(t, h, http.MethodGet, "/api/v1/fulfillment/returnable-packaging?customerId="+cust, "", packingHeaders)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"balance":"6"`) || fs.packing.balancesOf != cust {
		t.Fatalf("status = %d body = %s, want 200 with balance 6", rec.Code, rec.Body.String())
	}
	path := "/api/v1/fulfillment/returnable-packaging:return"
	body := func(qty string) string {
		return `{"customerId":"` + cust + `","containerType":"PALLET","qty":"` + qty + `"}`
	}
	// AC-28.13: a return of 7 against a balance of 6 is refused.
	rec = do(t, h, http.MethodPost, path, body("7"), withKey("rtp-1", nil))
	if rec.Code != http.StatusUnprocessableEntity || !strings.Contains(rec.Body.String(), store.CodeRTPNegative) {
		t.Fatalf("status = %d body = %s, want 422 FUL-0015", rec.Code, rec.Body.String())
	}
	for _, qty := range []string{"0", "1.5", "-2"} {
		rec = do(t, h, http.MethodPost, path, body(qty), withKey("rtp-q"+qty, nil))
		expectField(t, rec, rec.Body.String(), "qty")
	}
	rec = do(t, h, http.MethodPost, path, `{"customerId":"c1","containerType":"PALLET","qty":"1"}`, withKey("rtp-c", nil))
	expectField(t, rec, rec.Body.String(), "customerId")
	rec = do(t, h, http.MethodPost, path, body("4"), withKey("rtp-ok", nil))
	if rec.Code != http.StatusCreated || !strings.Contains(rec.Body.String(), `"balance":"2"`) || fs.packing.ret.ContainerType != "PALLET" {
		t.Fatalf("status = %d body = %s, want 201 balance 2", rec.Code, rec.Body.String())
	}
}

func TestPostPackingSlip_CountryCode(t *testing.T) {
	fs := &fakeStore{}
	h := newServer(t, fs)
	path := "/api/v1/fulfillment/shipments/" + ids.New() + ":post-packing-slip"
	body := `{"postingDate":"2026-10-21","currency":"USD","countryCode":"usa","costs":[]}`
	rec := do(t, h, http.MethodPost, path, body, withKey("pps-1", map[string]string{"If-Match": `"3"`}))
	expectField(t, rec, rec.Body.String(), "countryCode")
	body = `{"postingDate":"2026-10-21","currency":"EUR","countryCode":"DE","costs":[]}`
	rec = do(t, h, http.MethodPost, path, body, withKey("pps-2", map[string]string{"If-Match": `"3"`}))
	if rec.Code != http.StatusCreated || fs.lastPost.CountryCode != "DE" {
		t.Fatalf("status = %d country = %q body = %s, want 201 with DE", rec.Code, fs.lastPost.CountryCode, rec.Body.String())
	}
}
