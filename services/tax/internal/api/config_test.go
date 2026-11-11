package api_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/shopspring/decimal"

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
	policies  []domain.RecoveryPolicy
	movements []domain.Movement
	defaults  domain.MovementDefaults
	reclaim   *domain.Reclaim
	imported  []domain.MasterEntry
	overwrite bool
	vat       *domain.VATReturn
	esl       *domain.ESL
}

func (f *fakeConfig) PutIntrastatProduct(context.Context, domain.IntrastatProduct) error {
	return f.err
}
func (f *fakeConfig) StageMovements(_ context.Context, m []domain.Movement) (int, error) {
	f.movements = append(f.movements, m...)
	return len(m), f.err
}
func (f *fakeConfig) TransferMovements(_ context.Context, id string, d domain.MovementDefaults) (store.MovementTransfer, error) {
	f.defaults = d
	return store.MovementTransfer{Added: 1}, f.err
}

func (f *fakeConfig) RunRemittance(context.Context, domain.Period) (store.RemittanceRun, error) {
	return store.RemittanceRun{}, f.err
}
func (f *fakeConfig) RecordWithholding(_ context.Context, e []domain.WithholdingEntry) (int, error) {
	return len(e), f.err
}

func (f *fakeConfig) PutRecoveryPolicy(_ context.Context, p domain.RecoveryPolicy) error {
	f.policies = append(f.policies, p)
	return f.err
}
func (f *fakeConfig) ListRecoveryPolicies(context.Context) ([]domain.RecoveryPolicy, error) {
	return f.policies, f.err
}
func (f *fakeConfig) DeleteRecoveryPolicy(context.Context, string, string, domain.Date) error {
	return f.err
}
func (f *fakeConfig) Reclaim(context.Context, domain.Period) (domain.Reclaim, error) {
	if f.reclaim != nil {
		return *f.reclaim, f.err
	}
	return domain.Reclaim{}, f.err
}

func (f *fakeConfig) ImportMaster(_ context.Context, e []domain.MasterEntry, overwrite bool) (store.ImportResult, error) {
	f.imported, f.overwrite = e, overwrite
	return store.ImportResult{Added: len(e)}, f.err
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
	if f.vat != nil {
		return *f.vat, f.err
	}
	return domain.VATReturn{Boxes: []domain.BoxResult{{Box: "81", Kind: "tax"}}}, f.err
}
func (f *fakeConfig) ESL(context.Context, domain.Period) (domain.ESL, error) {
	if f.esl != nil {
		return *f.esl, f.err
	}
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

func TestMasterSeedAndImportRoutes(t *testing.T) {
	fc := &fakeConfig{}
	r := configRouter(fc)
	post := func(path, body string) *httptest.ResponseRecorder {
		return do(r, http.MethodPost, path, body, tenantOne, "tax.intrastatmaster.manage")
	}
	if rec := post("/v1/tax/intrastat/master:seed", `{"kinds":["sideways"]}`); rec.Code != http.StatusBadRequest {
		t.Errorf("an unknown kind: %d", rec.Code)
	}
	if rec := post("/v1/tax/intrastat/master:seed", `{"kinds":["transport_mode"]}`); rec.Code != http.StatusOK || len(fc.imported) != 8 || fc.overwrite {
		t.Errorf("seed: %d, %d rows, overwrite %v", rec.Code, len(fc.imported), fc.overwrite)
	}
	if rec := post("/v1/tax/intrastat/master:seed", ``); rec.Code != http.StatusOK || len(fc.imported) < 50 {
		t.Errorf("seed with no body: %d, %d rows", rec.Code, len(fc.imported))
	}
	if rec := post("/v1/tax/intrastat/master:import", `{"entries":[{"kind":"commodity","code":"8413","description":"Pumps"}]}`); rec.Code != http.StatusBadRequest {
		t.Errorf("a short commodity code: %d", rec.Code)
	}
	if rec := post("/v1/tax/intrastat/master:import", `{"overwrite":true,"entries":[{"kind":"commodity","code":"84131100","description":"Pumps"}]}`); rec.Code != http.StatusOK || !fc.overwrite || len(fc.imported) != 1 {
		t.Errorf("import: %d overwrite %v rows %d", rec.Code, fc.overwrite, len(fc.imported))
	}
}

func TestFilingFormatsAndLayoutRoutes(t *testing.T) {
	zero := decimal.Zero
	vat := &domain.VATReturn{PeriodFrom: domain.Day("2026-04-01"), PeriodTo: domain.Day("2026-06-30"), Boxes: []domain.BoxResult{
		{Box: "81", Amount: decimal.RequireFromString("1000.5")}, {Box: "86", Amount: zero}, {Box: "41", Amount: zero},
		{Box: "61", Amount: zero}, {Box: "66", Amount: decimal.RequireFromString("19")}, {Box: "83", Amount: decimal.RequireFromString("171")}}}
	esl := &domain.ESL{PeriodFrom: domain.Day("2026-04-01"), PeriodTo: domain.Day("2026-06-30"),
		Rows: []domain.ESLRow{{Country: "FR", VATID: "FR40303265045", SupplyKind: "goods", Base: decimal.NewFromInt(100)}}}
	fc := &fakeConfig{vat: vat, esl: esl}
	r := configRouter(fc)
	get := func(path, duty string) *httptest.ResponseRecorder {
		return do(r, http.MethodGet, path, ``, tenantOne, duty)
	}
	const period = "?period_from=2026-04-01&period_to=2026-06-30"

	rec := get("/v1/tax/vat-return"+period+"&format=de-ustva-csv", "tax.vatreturn.read")
	if rec.Code != http.StatusOK || !strings.HasPrefix(rec.Header().Get("Content-Type"), "text/csv") || !strings.Contains(rec.Header().Get("Content-Disposition"), "ustva-2026-04-01_2026-06-30.csv") ||
		!strings.Contains(rec.Body.String(), "81;1000\n") {
		t.Errorf("UStVA: %d %v %q", rec.Code, rec.Header(), rec.Body.String())
	}
	if rec := get("/v1/tax/vat-return"+period+"&format=gb-mtd-json&period_key=26A2", "tax.vatreturn.read"); rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), "apply the layout") {
		t.Errorf("a GB file from a German box set: %d %s", rec.Code, rec.Body.String())
	}
	if rec := get("/v1/tax/vat-return"+period+"&format=nl-xml", "tax.vatreturn.read"); rec.Code != http.StatusBadRequest {
		t.Errorf("an unknown format: %d", rec.Code)
	}
	if rec := get("/v1/tax/vat-return"+period, "tax.vatreturn.read"); rec.Code != http.StatusOK || !strings.HasPrefix(rec.Header().Get("Content-Type"), "application/json") {
		t.Errorf("no format keeps JSON: %d %v", rec.Code, rec.Header())
	}
	if rec := get("/v1/tax/esl"+period+"&format=de-zm-csv", "tax.vatreturn.read"); rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "FR;40303265045;100;") {
		t.Errorf("ZM: %d %q", rec.Code, rec.Body.String())
	}
	if rec := get("/v1/tax/vat-layouts", "tax.config.read"); rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"country":"GB"`) {
		t.Errorf("layouts: %d %s", rec.Code, rec.Body.String())
	}
	apply := func(country, body string) int {
		return do(r, http.MethodPost, "/v1/tax/vat-layouts/"+country+"/apply", body, tenantOne, "tax.config.manage").Code
	}
	if code := apply("XX", `{"mapping":{}}`); code != http.StatusBadRequest {
		t.Errorf("an unknown country: %d", code)
	}
	if code := apply("GB", `{"mapping":{"1":[{"code":"UK20","direction":"output"}]}}`); code != http.StatusBadRequest || fc.boxes != nil {
		t.Errorf("a half-mapped layout: %d, stored %v", code, fc.boxes)
	}
	full := `{"mapping":{"1":[{"code":"UK20","direction":"output"}],"2":[{"code":"UK20","direction":"use"}],"4":[{"code":"UK20","direction":"input"}],` +
		`"6":[{"code":"UK20","direction":"output"}],"7":[{"code":"UK20","direction":"input"}],"8":[{"code":"UK0","direction":"output"}],"9":[{"code":"UK0","direction":"input"}]}}`
	if code := apply("GB", full); code != http.StatusOK || len(fc.boxes) != 8 {
		t.Errorf("a full mapping: %d, stored %d boxes", code, len(fc.boxes))
	}
}

func TestRecoveryAndReclaimRoutes(t *testing.T) {
	fc := &fakeConfig{reclaim: &domain.Reclaim{PeriodFrom: domain.Day("2026-04-01"), PeriodTo: domain.Day("2026-04-30"), TotalInputTax: decimal.NewFromInt(150),
		TotalRecoverable: decimal.NewFromInt(80), TotalNonRecoverable: decimal.NewFromInt(70), Rows: []domain.ReclaimRow{{Code: "VAT19"}}}}
	r := configRouter(fc)
	put := func(path, body string) int {
		return do(r, http.MethodPut, path, body, tenantOne, "tax.config.manage").Code
	}
	if code := put("/v1/tax/recovery-policies/VAT19", `{"code":"OTHER","recoverable_pct":"50","valid_from":"2026-01-01"}`); code != http.StatusBadRequest {
		t.Errorf("a body code that differs from the path: %d", code)
	}
	if code := put("/v1/tax/recovery-policies/VAT19", `{"recoverable_pct":"50","valid_from":"2026-01-01","surprise":1}`); code != http.StatusBadRequest {
		t.Errorf("an unknown field: %d", code)
	}
	if len(fc.policies) != 0 {
		t.Fatal("an invalid body reached the store")
	}
	if code := put("/v1/tax/recovery-policies/VAT19", `{"item_group":"CARS","recoverable_pct":"50","valid_from":"2026-01-01"}`); code != http.StatusOK || fc.policies[0].Code != "VAT19" {
		t.Errorf("a valid policy: %d %+v", code, fc.policies)
	}
	if rec := do(r, http.MethodDelete, "/v1/tax/recovery-policies/VAT19?item_group=CARS", ``, tenantOne, "tax.config.manage"); rec.Code != http.StatusBadRequest {
		t.Errorf("delete without valid_from: %d", rec.Code)
	}
	if rec := do(r, http.MethodDelete, "/v1/tax/recovery-policies/VAT19?item_group=CARS&valid_from=2026-01-01", ``, tenantOne, "tax.config.manage"); rec.Code != http.StatusNoContent {
		t.Errorf("delete: %d", rec.Code)
	}
	rec := do(r, http.MethodGet, "/v1/tax/vat-reclaim?period_from=2026-04-01&period_to=2026-04-30", ``, tenantOne, "tax.vatreturn.read")
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"total_recoverable":"80"`) {
		t.Errorf("reclaim: %d %s", rec.Code, rec.Body.String())
	}
	if rec := do(r, http.MethodGet, "/v1/tax/vat-reclaim?period_from=2026-04-01", ``, tenantOne, "tax.vatreturn.read"); rec.Code != http.StatusBadRequest {
		t.Errorf("reclaim without a period end: %d", rec.Code)
	}
}

func TestWithholdingRemittanceRoutesValidate(t *testing.T) {
	r := configRouter(&fakeConfig{err: &domain.Error{Code: domain.CodeNothingToRemit, Detail: "nothing"}})
	good := `{"id":"` + tenantOne + `","party_type":"vendor","party_ref":"v1","code":"NEC","payment_date":"2026-01-10","currency":"USD","gross_amount":"100","taxable_base":"100","withholding_amount":"24"}`
	post := func(path, body, duty string) *httptest.ResponseRecorder {
		return do(r, http.MethodPost, path, body, tenantOne, duty)
	}
	for name, body := range map[string]string{
		"no entries":          `{"entries":[]}`,
		"bad id":              `{"entries":[` + strings.Replace(good, tenantOne, "x", 1) + `]}`,
		"tax above the base":  `{"entries":[` + strings.Replace(good, `"withholding_amount":"24"`, `"withholding_amount":"101"`, 1) + `]}`,
		"base above gross":    `{"entries":[` + strings.Replace(good, `"taxable_base":"100"`, `"taxable_base":"101"`, 1) + `]}`,
		"lower case currency": `{"entries":[` + strings.Replace(good, `"USD"`, `"usd"`, 1) + `]}`,
		"unknown party type":  `{"entries":[` + strings.Replace(good, `"vendor"`, `"employee"`, 1) + `]}`,
		"twice":               `{"entries":[` + good + `,` + good + `]}`,
	} {
		if rec := post("/v1/tax/withholding/entries", body, "tax.withholding.record"); rec.Code != http.StatusBadRequest {
			t.Errorf("%s: %d, want 400", name, rec.Code)
		}
	}
	for name, body := range map[string]string{
		"no dates":         `{}`,
		"reversed":         `{"period_from":"2026-02-01","period_to":"2026-01-01"}`,
		"across two years": `{"period_from":"2026-12-01","period_to":"2027-01-31"}`,
	} {
		if rec := post("/v1/tax/withholding/remittance-runs", body, "tax.withholding.remit"); rec.Code != http.StatusBadRequest {
			t.Errorf("%s: %d, want 400", name, rec.Code)
		}
	}
	rec := post("/v1/tax/withholding/remittance-runs", `{"period_from":"2026-01-01","period_to":"2026-01-31"}`, "tax.withholding.remit")
	if rec.Code != http.StatusUnprocessableEntity || !strings.Contains(rec.Body.String(), `"serviceCode":"TAX-0014"`) {
		t.Errorf("nothing to remit: %d %s", rec.Code, rec.Body.String())
	}
	if rec := do(r, http.MethodGet, "/v1/tax/withholding/remittance-runs/not-a-uuid", ``, tenantOne, "tax.withholding.read"); rec.Code != http.StatusBadRequest {
		t.Errorf("a bad run id: %d", rec.Code)
	}
}

func TestIntrastatFeedRoutes(t *testing.T) {
	fc := &fakeConfig{}
	r := configRouter(fc)
	const product = "0190f0a0-0000-7000-8000-0000000000a1"
	movement := `{"flow":"dispatch","source_ref":"SHIP-1","movement_date":"2026-03-04","partner_country":"FR","lines":[{"line_no":1,"product_id":"` + product + `","qty":"4","value":"2000"}]}`
	post := func(path, body, duty string) int { return do(r, http.MethodPost, path, body, tenantOne, duty).Code }
	for name, body := range map[string]string{
		"no movements":  `{"movements":[]}`,
		"bad flow":      `{"movements":[` + strings.Replace(movement, "dispatch", "sideways", 1) + `]}`,
		"no lines":      `{"movements":[` + strings.Replace(movement, `"lines":[{"line_no":1,"product_id":"`+product+`","qty":"4","value":"2000"}]`, `"lines":[]`, 1) + `]}`,
		"repeated ref":  `{"movements":[` + movement + `,` + movement + `]}`,
		"unknown field": `{"movements":[],"x":1}`,
	} {
		if code := post("/v1/tax/intrastat/movements", body, "tax.intrastat.feed"); code != http.StatusBadRequest {
			t.Errorf("%s: %d, want 400", name, code)
		}
	}
	if len(fc.movements) != 0 {
		t.Fatal("an invalid body reached the store")
	}
	if code := post("/v1/tax/intrastat/movements", `{"movements":[`+movement+`]}`, "tax.intrastat.feed"); code != http.StatusCreated || len(fc.movements) != 1 {
		t.Errorf("a valid movement: %d %d", code, len(fc.movements))
	}
	if code := post("/v1/tax/intrastat/movements", `{"movements":[`+movement+`]}`, "tax.intrastat.manage"); code != http.StatusForbidden {
		t.Errorf("staging needs the feed duty: %d", code)
	}
	if code := post("/v1/tax/intrastat/journals/j-1/transfer-movements", `{"transaction_code":"11","transport_mode":"3"}`, "tax.intrastat.manage"); code != http.StatusOK || fc.defaults.TransactionCode != "11" {
		t.Errorf("transfer: %d %+v", code, fc.defaults)
	}
	if code := post("/v1/tax/intrastat/journals/j-1/transfer-movements", ``, "tax.intrastat.manage"); code != http.StatusOK {
		t.Errorf("transfer without defaults: %d", code)
	}
	put := func(path, body string) int {
		return do(r, http.MethodPut, path, body, tenantOne, "tax.intrastatmaster.manage").Code
	}
	if code := put("/v1/tax/intrastat/products/"+product, `{"product_id":"other","commodity_code":"84137030","origin_country":"US","net_mass_per_unit":"1"}`); code != http.StatusBadRequest {
		t.Errorf("a body id that differs from the path: %d", code)
	}
	if code := put("/v1/tax/intrastat/products/"+product, `{"commodity_code":"8413","origin_country":"US","net_mass_per_unit":"1"}`); code != http.StatusBadRequest {
		t.Errorf("a short commodity code: %d", code)
	}
	if code := put("/v1/tax/intrastat/products/"+product, `{"commodity_code":"84137030","origin_country":"US","net_mass_per_unit":"1.5"}`); code != http.StatusOK {
		t.Errorf("a valid product: %d", code)
	}
	if rec := do(r, http.MethodGet, "/v1/tax/intrastat/movements?status=bogus", ``, tenantOne, "tax.intrastat.read"); rec.Code != http.StatusBadRequest {
		t.Errorf("a bad status: %d", rec.Code)
	}
}
