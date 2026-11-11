package api_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/platform/testkit"
	"github.com/BenjaminSRussell/passage/services/tax/internal/api"
	"github.com/BenjaminSRussell/passage/services/tax/internal/domain"
	"github.com/BenjaminSRussell/passage/services/tax/internal/store"
)

// fakeConfig implements only what a test needs; the embedded nil interface panics on anything else,
// which fails a test that reaches the store when it should have been refused earlier.
type fakeConfig struct {
	api.ConfigStore
	cfg       domain.Config
	jur       []domain.Jurisdiction
	boxes     []domain.Box
	master    []domain.MasterEntry
	export    store.ExportData
	err       error
	putCodes  []domain.ConfigCode
	putGroups []domain.Group
	putCerts  []domain.Certificate
}

func (f *fakeConfig) PutCertificate(_ context.Context, c domain.Certificate) error {
	f.putCerts = append(f.putCerts, c)
	return f.err
}
func (f *fakeConfig) ListCertificates(context.Context) ([]domain.Certificate, error) {
	return f.putCerts, f.err
}

func (f *fakeConfig) LoadConfig(context.Context) (domain.Config, error) { return f.cfg, f.err }
func (f *fakeConfig) ListJurisdictions(context.Context) ([]domain.Jurisdiction, error) {
	return f.jur, f.err
}
func (f *fakeConfig) PutJurisdiction(_ context.Context, j domain.Jurisdiction) error {
	f.jur = append(f.jur, j)
	return f.err
}
func (f *fakeConfig) PutCode(_ context.Context, c domain.ConfigCode) error {
	f.putCodes = append(f.putCodes, c)
	return f.err
}
func (f *fakeConfig) DeleteCode(context.Context, string) error { return f.err }
func (f *fakeConfig) PutGroup(_ context.Context, g domain.Group) error {
	f.putGroups = append(f.putGroups, g)
	return f.err
}
func (f *fakeConfig) PutBoxes(_ context.Context, b []domain.Box) error {
	f.boxes = b
	return f.err
}
func (f *fakeConfig) PutMaster(_ context.Context, e domain.MasterEntry) error {
	f.master = append(f.master, e)
	return f.err
}
func (f *fakeConfig) PutEntitySetting(context.Context, store.EntitySetting) error { return f.err }
func (f *fakeConfig) VATReturn(context.Context, domain.Period) (domain.VATReturn, error) {
	return domain.VATReturn{Boxes: []domain.BoxResult{{Box: "81", Kind: "tax"}}}, f.err
}
func (f *fakeConfig) ESL(context.Context, domain.Period) (domain.ESL, error) {
	return domain.ESL{}, f.err
}
func (f *fakeConfig) CreateJournal(_ context.Context, flow string, from domain.Date) (store.Journal, error) {
	return store.Journal{ID: "j", Flow: flow, PeriodFrom: from, Status: "draft"}, f.err
}
func (f *fakeConfig) Transfer(_ context.Context, _ string, lines []domain.IntrastatLine) (store.TransferResult, error) {
	return store.TransferResult{Added: len(lines)}, f.err
}
func (f *fakeConfig) Export(context.Context, string) (store.ExportData, error) {
	return f.export, f.err
}

func configRouter(c api.ConfigStore) http.Handler {
	a := api.New(fakeDB{}, nil, 100*time.Millisecond).WithConfigStore(c)
	r := httpx.NewRouter()
	a.Register(r)
	return r
}

var tenantOne = testkit.TenantID(1)

func TestConfigRoutesAnswer503WithoutAStore(t *testing.T) {
	r, _ := newRouter(fakeDB{}, nil)
	for _, path := range []string{"/v1/tax/jurisdictions", "/v1/tax/codes", "/v1/tax/groups/party", "/v1/tax/vat-boxes", "/v1/tax/intrastat/journals"} {
		duty := "tax.config.read"
		if strings.Contains(path, "intrastat") {
			duty = "tax.intrastat.read"
		}
		if rec := do(r, http.MethodGet, path, ``, tenantOne, duty); rec.Code != http.StatusServiceUnavailable {
			t.Errorf("%s: %d, want 503", path, rec.Code)
		}
	}
}

func TestCalculateCanUseTheStoredConfig(t *testing.T) {
	fc := &fakeConfig{cfg: domain.AssembleConfig(
		[]domain.ConfigCode{{Name: "VAT", Code: domain.Code{Code: "VAT", Jurisdiction: "DE", Rates: []domain.Rate{{ValidFrom: domain.Day("2020-01-01"), RatePct: domain.D("19")}}}}},
		[]domain.Group{{Kind: "party", Code: "G", TaxCodes: []string{"VAT"}}, {Kind: "item", Code: "I", TaxCodes: []string{"VAT"}}}, "per_document")}
	r := configRouter(fc)
	body := `{"currency":"EUR","direction":"output","document_date":"2026-03-15","stored_config":true,"party":{"tax_group":"G"},` +
		`"lines":[{"line_ref":"L1","amount":"100.00","item_tax_group":"I"}]}`
	rec := do(r, http.MethodPost, calculatePath, body, tenantOne, duty)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"tax_amount":"19"`) {
		t.Fatalf("got %d %s", rec.Code, rec.Body.String())
	}
	both := strings.Replace(body, `"stored_config":true,`, `"stored_config":true,"config":{"groups":{"G":["VAT"]}},`, 1)
	if rec := do(r, http.MethodPost, calculatePath, both, tenantOne, duty); rec.Code != http.StatusBadRequest {
		t.Errorf("stored and inline config together: %d", rec.Code)
	}
	noStore, _ := newRouter(fakeDB{}, nil)
	if rec := do(noStore, http.MethodPost, calculatePath, body, tenantOne, duty); rec.Code != http.StatusServiceUnavailable {
		t.Errorf("no config store: %d", rec.Code)
	}
}

func TestPutRoutesValidateBeforeTouchingTheStore(t *testing.T) {
	fc := &fakeConfig{}
	r := configRouter(fc)
	put := func(path, body string) int {
		return do(r, http.MethodPut, path, body, tenantOne, "tax.config.manage").Code
	}
	if code := put("/v1/tax/codes/VAT19", `{"name":"VAT","jurisdiction":"DE","rates":[{"valid_from":"2020-01-01","rate_pct":"150"}]}`); code != http.StatusUnprocessableEntity && code != http.StatusBadRequest {
		t.Errorf("a rate of 150 percent: %d", code)
	}
	if code := put("/v1/tax/codes/VAT19", `{"code":"OTHER","name":"VAT","jurisdiction":"DE"}`); code != http.StatusBadRequest {
		t.Errorf("a body code that differs from the path: %d", code)
	}
	if code := put("/v1/tax/groups/sideways/G", `{"name":"G"}`); code != http.StatusBadRequest {
		t.Errorf("an unknown group kind: %d", code)
	}
	if code := put("/v1/tax/groups/party/G", `{"name":"G","kind":"item"}`); code != http.StatusBadRequest {
		t.Errorf("a group kind that differs from the path: %d", code)
	}
	if code := put("/v1/tax/entity-settings", `{"home_country":"de"}`); code != http.StatusBadRequest {
		t.Errorf("a lowercase country: %d", code)
	}
	if code := put("/v1/tax/vat-boxes", `{"boxes":[{"box":"1","label":"x","kind":"tax"}]}`); code != http.StatusBadRequest {
		t.Errorf("a tax box without codes: %d", code)
	}
	if code := do(r, http.MethodPut, "/v1/tax/intrastat/master/commodity/8413", `{"description":"Pumps"}`, tenantOne, "tax.intrastatmaster.manage").Code; code != http.StatusBadRequest {
		t.Errorf("a short commodity code: %d", code)
	}
	if code := put("/v1/tax/codes/VAT19", `{"name":"VAT","unknown":1}`); code != http.StatusBadRequest {
		t.Errorf("an unknown field: %d", code)
	}
	if len(fc.putCodes)+len(fc.putGroups)+len(fc.boxes)+len(fc.master) != 0 {
		t.Error("an invalid body reached the store")
	}

	if code := put("/v1/tax/codes/VAT19", `{"name":"VAT","jurisdiction":"DE","rates":[{"valid_from":"2020-01-01","rate_pct":"19"}]}`); code != http.StatusOK || len(fc.putCodes) != 1 || fc.putCodes[0].Code.Code != "VAT19" {
		t.Errorf("a valid code: %d, stored %+v", code, fc.putCodes)
	}
	if code := put("/v1/tax/groups/item/STANDARD", `{"name":"Standard","tax_codes":["VAT19"]}`); code != http.StatusOK || fc.putGroups[0].Kind != "item" || fc.putGroups[0].Code != "STANDARD" {
		t.Errorf("a valid group: %d, stored %+v", code, fc.putGroups)
	}
	if code := put("/v1/tax/jurisdictions/US-OH", `{"name":"Ohio","kind":"state","country":"US","parent_code":"US"}`); code != http.StatusOK || fc.jur[0].Code != "US-OH" {
		t.Errorf("a valid jurisdiction: %d, stored %+v", code, fc.jur)
	}
}

func TestStoreProblemsMapToPlainStatuses(t *testing.T) {
	for _, c := range []struct {
		err    error
		status int
		code   string
	}{
		{store.ErrNotFound, http.StatusNotFound, "COM-0404"},
		{store.ErrNoEntitySettings, http.StatusUnprocessableEntity, "COM-0503"},
		{&domain.Error{Code: domain.CodeConfigInUse, Detail: "in use"}, http.StatusUnprocessableEntity, "TAX-0013"},
	} {
		r := configRouter(&fakeConfig{err: c.err})
		rec := do(r, http.MethodDelete, "/v1/tax/codes/VAT19", ``, tenantOne, "tax.config.manage")
		if rec.Code != c.status || !strings.Contains(rec.Body.String(), `"serviceCode":"`+c.code+`"`) {
			t.Errorf("%v: %d %s", c.err, rec.Code, rec.Body.String())
		}
	}
}

func TestVATReturnNeedsAPeriod(t *testing.T) {
	r := configRouter(&fakeConfig{})
	for _, q := range []string{"", "?period_from=2026-01-01", "?period_from=2026-02-01&period_to=2026-01-01", "?period_from=x&period_to=y"} {
		if rec := do(r, http.MethodGet, "/v1/tax/vat-return"+q, ``, tenantOne, "tax.vatreturn.read"); rec.Code != http.StatusBadRequest {
			t.Errorf("%q: %d", q, rec.Code)
		}
	}
	rec := do(r, http.MethodGet, "/v1/tax/vat-return?period_from=2026-01-01&period_to=2026-01-31", ``, tenantOne, "tax.vatreturn.read")
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"box":"81"`) {
		t.Errorf("%d %s", rec.Code, rec.Body.String())
	}
}

func TestIntrastatJournalRoutes(t *testing.T) {
	fc := &fakeConfig{}
	r := configRouter(fc)
	post := func(path, body string) *httptest.ResponseRecorder {
		return do(r, http.MethodPost, path, body, tenantOne, "tax.intrastat.manage")
	}
	if rec := post("/v1/tax/intrastat/journals", `{"flow":"sideways","period_from":"2026-10-01"}`); rec.Code != http.StatusBadRequest {
		t.Errorf("an unknown flow: %d", rec.Code)
	}
	if rec := post("/v1/tax/intrastat/journals", `{"flow":"dispatch","period_from":"2026-10-15"}`); rec.Code != http.StatusBadRequest {
		t.Errorf("a period that is not a month start: %d", rec.Code)
	}
	if rec := post("/v1/tax/intrastat/journals", `{"flow":"dispatch","period_from":"2026-10-01"}`); rec.Code != http.StatusCreated {
		t.Errorf("a valid journal: %d %s", rec.Code, rec.Body.String())
	}
	if rec := post("/v1/tax/intrastat/journals/j/transfer", `{"lines":[]}`); rec.Code != http.StatusBadRequest {
		t.Errorf("an empty transfer: %d", rec.Code)
	}
	if rec := post("/v1/tax/intrastat/journals/j/transfer", `{"lines":[{"source_ref":"S1"},{"source_ref":"S1"}]}`); rec.Code != http.StatusBadRequest {
		t.Errorf("a repeated source_ref: %d", rec.Code)
	}
	if rec := post("/v1/tax/intrastat/journals/j/transfer", `{"lines":[{"source_ref":"S1","net_mass":"4.5"}]}`); rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"added":1`) {
		t.Errorf("a valid transfer: %d %s", rec.Code, rec.Body.String())
	}
	if rec := do(r, http.MethodPut, "/v1/tax/intrastat/journals/j/lines/0", `{}`, tenantOne, "tax.intrastat.manage"); rec.Code != http.StatusBadRequest {
		t.Errorf("line 0: %d", rec.Code)
	}
}

func TestIntrastatExportIsACSVFile(t *testing.T) {
	master := domain.NewMaster([]domain.MasterEntry{{Kind: "country", Code: "GR", Description: "Greece", Attrs: map[string]string{"intrastat_code": "EL"}}})
	line := domain.IntrastatLine{LineNo: 1, PartnerCountry: "GR", CommodityCode: "73181590", TransactionCode: "11", TransportMode: "3", DeliveryTerms: "EXW",
		NetMass: domain.D("2"), InvoiceValue: domain.D("40"), StatisticalValue: domain.D("40")}
	fc := &fakeConfig{export: store.ExportData{Journal: store.Journal{Flow: "dispatch", PeriodFrom: domain.Day("2026-10-01"), Lines: []domain.IntrastatLine{line}}, HomeCountry: "DE", Master: master}}
	rec := do(configRouter(fc), http.MethodGet, "/v1/tax/intrastat/journals/j/export", ``, tenantOne, "tax.intrastat.export")
	if rec.Code != http.StatusOK || !strings.HasPrefix(rec.Header().Get("Content-Type"), "text/csv") ||
		!strings.Contains(rec.Header().Get("Content-Disposition"), "intrastat-dispatch-2026-10.csv") || !strings.Contains(rec.Body.String(), "1,dispatch,N,DE,EL,73181590") {
		t.Errorf("%d %v\n%s", rec.Code, rec.Header(), rec.Body.String())
	}
	if rec := do(configRouter(fc), http.MethodGet, "/v1/tax/intrastat/journals/j/export", ``, tenantOne, "tax.intrastat.read"); rec.Code != http.StatusForbidden {
		t.Errorf("export with only the read duty: %d", rec.Code)
	}
}

func TestCertificateRoutes(t *testing.T) {
	fc := &fakeConfig{}
	r := configRouter(fc)
	put := func(path, body string) int {
		return do(r, http.MethodPut, path, body, tenantOne, "tax.config.manage").Code
	}
	if code := put("/v1/tax/exemption-certificates/C-1", `{"number":"C-2","codes":["VAT19"],"valid_from":"2026-01-01"}`); code != http.StatusBadRequest {
		t.Errorf("a body number that differs from the path: %d", code)
	}
	if code := put("/v1/tax/exemption-certificates/C-1", `{"codes":["VAT19"],"valid_from":"2026-01-01","surprise":1}`); code != http.StatusBadRequest {
		t.Errorf("an unknown field: %d", code)
	}
	if len(fc.putCerts) != 0 {
		t.Fatal("an invalid body reached the store")
	}
	if code := put("/v1/tax/exemption-certificates/C-1", `{"codes":["VAT19"],"valid_from":"2026-01-01"}`); code != http.StatusOK || fc.putCerts[0].Number != "C-1" {
		t.Errorf("a valid certificate: %d, stored %+v", code, fc.putCerts)
	}
	rec := do(r, http.MethodGet, "/v1/tax/exemption-certificates", ``, tenantOne, "tax.config.read")
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"number":"C-1"`) {
		t.Errorf("list: %d %s", rec.Code, rec.Body.String())
	}
}
