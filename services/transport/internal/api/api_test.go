package api_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/platform/testkit"
	"github.com/BenjaminSRussell/passage/services/transport/internal/api"
	"github.com/BenjaminSRussell/passage/services/transport/internal/domain"
	"github.com/BenjaminSRussell/passage/services/transport/internal/store"
)

const (
	carrierID = "0190f0a0-0000-7000-8000-0000000000c1"
	serviceID = "0190f0a0-0000-7000-8000-0000000000e1"
	loadID    = "0190f0a0-0000-7000-8000-0000000000a1"
	billID    = "0190f0a0-0000-7000-8000-0000000000b1"
	invoiceID = "0190f0a0-0000-7000-8000-0000000000d1"
	base      = "/v1/transport"
)

// fakeStore satisfies store.Store without a database so handler behaviour can be
// tested in every environment. It records what the handlers pass in.
type fakeStore struct {
	cards       []domain.RateCard
	bill        domain.FreightBill
	billErr     error
	invoice     domain.CarrierInvoice
	invoiceErr  error
	registerErr error
	idems       []store.Idem
	billFilter  store.FreightBillFilter
	registered  domain.CarrierInvoice
}

func (f *fakeStore) CreateRateCard(_ context.Context, in domain.RateCard, idem store.Idem) (domain.RateCard, error) {
	f.idems = append(f.idems, idem)
	return in, nil
}

func (f *fakeStore) ListRateCards(_ context.Context, _ store.RateCardFilter) ([]domain.RateCard, error) {
	return f.cards, nil
}

func (f *fakeStore) CreateFreightBill(_ context.Context, in domain.FreightBill, idem store.Idem) (domain.FreightBill, error) {
	f.idems = append(f.idems, idem)
	in.ID, in.MatchStatus, in.ExpectedTotal = billID, domain.BillUnmatched, domain.SumLines(in.ExpectedLines)
	return in, nil
}

func (f *fakeStore) GetFreightBill(_ context.Context, _ string) (domain.FreightBill, error) {
	return f.bill, f.billErr
}

func (f *fakeStore) ListFreightBills(_ context.Context, filter store.FreightBillFilter) ([]domain.FreightBill, error) {
	f.billFilter = filter
	return []domain.FreightBill{f.bill}, nil
}

func (f *fakeStore) RegisterCarrierInvoice(_ context.Context, in domain.CarrierInvoice) (domain.CarrierInvoice, *domain.FreightBill, error) {
	f.registered = in
	if f.registerErr != nil {
		return domain.CarrierInvoice{}, nil, f.registerErr
	}
	in.ID, in.MatchStatus = invoiceID, domain.MatchUnexpected
	return in, nil, nil
}

func (f *fakeStore) GetCarrierInvoice(_ context.Context, _ string) (domain.CarrierInvoice, error) {
	return f.invoice, f.invoiceErr
}

func (f *fakeStore) ListCarrierInvoices(_ context.Context, _ store.InvoiceFilter) ([]domain.CarrierInvoice, error) {
	return []domain.CarrierInvoice{f.invoice}, nil
}

func (f *fakeStore) Ping(_ context.Context) error { return nil }

func router(st store.Store) http.Handler {
	r := chi.NewRouter()
	api.New(st).Register(r)
	return r
}

func do(t *testing.T, h http.Handler, method, path, body string, duties ...string) *httptest.ResponseRecorder {
	t.Helper()
	return doWith(t, h, method, path, body, nil, duties...)
}

func doWith(t *testing.T, h http.Handler, method, path, body string, header map[string]string, duties ...string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	for k, v := range header {
		req.Header.Set(k, v)
	}
	req = req.WithContext(testkit.Ctx(testkit.TenantID(1), duties...))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

func problem(t *testing.T, rec *httptest.ResponseRecorder) httpx.Problem {
	t.Helper()
	var p httpx.Problem
	if err := json.Unmarshal(rec.Body.Bytes(), &p); err != nil {
		t.Fatalf("decode problem: %v (%s)", err, rec.Body.String())
	}
	return p
}

func dec(s string) decimal.Decimal { return decimal.RequireFromString(s) }

func card(carrier string, rate string) domain.RateCard {
	return domain.RateCard{
		ID: "rc-" + carrier, CarrierID: carrier, ServiceID: serviceID, Basis: domain.BasisLane, Currency: "USD",
		ValidFrom: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC), DimDivisor: 5000,
		Rows: []domain.RateRow{{Origin: "*", Destination: "*", BreakFrom: dec("0"), Rate: dec(rate), Unit: domain.UnitKG}},
	}
}

const rateBody = `{"originZone":"ORD","destinationZone":"ATL","weightKg":"100","volumeM3":"0","pallets":1,"shipDate":"2026-03-01"}`

func TestRateShipment_RanksOptionsAndChoosesCheapest(t *testing.T) {
	h := router(&fakeStore{cards: []domain.RateCard{card("c-dear", "0.90"), card("c-cheap", "0.50")}})
	rec := do(t, h, http.MethodPost, base+"/shipments/"+loadID+":rate", rateBody, api.DutyRate)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body %s", rec.Code, rec.Body.String())
	}
	var resp struct {
		ShipmentID string         `json:"shipmentId"`
		Options    []domain.Quote `json:"options"`
		Chosen     *domain.Quote  `json:"chosen"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	if resp.ShipmentID != loadID || len(resp.Options) != 2 || resp.Options[0].CarrierID != "c-cheap" {
		t.Fatalf("options = %+v", resp.Options)
	}
	if resp.Chosen == nil || resp.Chosen.CarrierID != "c-cheap" || !resp.Chosen.Total.Equal(dec("50")) {
		t.Fatalf("chosen = %+v, want c-cheap at 50.00", resp.Chosen)
	}
}

func TestRateShipment_NoCardsReturnsEmptyOptions(t *testing.T) {
	h := router(&fakeStore{})
	rec := do(t, h, http.MethodPost, base+"/shipments/"+loadID+":rate", rateBody, api.DutyRate)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body %s", rec.Code, rec.Body.String())
	}
	var resp struct {
		Options []json.RawMessage `json:"options"`
		Chosen  *json.RawMessage  `json:"chosen"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	if len(resp.Options) != 0 || resp.Chosen != nil {
		t.Fatalf("expected no options and no chosen quote, got %s", rec.Body.String())
	}
}

func TestRateShipment_ValidationReturnsFieldErrors(t *testing.T) {
	h := router(&fakeStore{})
	body := `{"destinationZone":"ATL","weightKg":"-1","distanceKm":"-2","shipDate":"not-a-date"}`
	rec := do(t, h, http.MethodPost, base+"/shipments/"+loadID+":rate", body, api.DutyRate)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", rec.Code)
	}
	p := problem(t, rec)
	if p.Code != "COM-0001" {
		t.Fatalf("code = %q, want COM-0001", p.Code)
	}
	for _, field := range []string{"originZone", "weightKg", "distanceKm", "shipDate"} {
		if p.Errors[field] == "" {
			t.Fatalf("errors = %v, missing %s", p.Errors, field)
		}
	}
}

func TestRoutes_RequireTheirDuty(t *testing.T) {
	h := router(&fakeStore{})
	tests := []struct {
		method, path, duty string
	}{
		{http.MethodPost, base + "/shipments/" + loadID + ":rate", api.DutyRate},
		{http.MethodPost, base + "/rate-cards", api.DutyRateApprove},
		{http.MethodGet, base + "/rate-cards", api.DutyRate},
		{http.MethodPost, base + "/freight-bills", api.DutyReconcile},
		{http.MethodGet, base + "/freight-bills", api.DutyView},
		{http.MethodGet, base + "/freight-bills/" + billID, api.DutyView},
		{http.MethodPost, base + "/carrier-invoices", api.DutyReconcile},
		{http.MethodGet, base + "/carrier-invoices", api.DutyReconcile},
		{http.MethodGet, base + "/carrier-invoices/" + invoiceID, api.DutyReconcile},
	}
	for _, tc := range tests {
		t.Run(tc.method+" "+tc.path, func(t *testing.T) {
			// A different duty is not enough: the route needs its own.
			if rec := do(t, h, tc.method, tc.path, `{}`, "TMS_OTHER"); rec.Code != http.StatusForbidden {
				t.Fatalf("status = %d, want 403 without %s", rec.Code, tc.duty)
			}
		})
	}
}

func TestCreateRateCard_FingerprintsIdempotencyKey(t *testing.T) {
	st := &fakeStore{}
	h := router(st)
	body := `{"carrierId":"` + carrierID + `","serviceId":"` + serviceID + `","basis":"LANE","currency":"USD","validFrom":"2026-01-01",` +
		`"rows":[{"origin":"*","destination":"*","breakFrom":"0","rate":"0.5","unit":"KG"}]}`
	for i := 0; i < 2; i++ {
		rec := doWith(t, h, http.MethodPost, base+"/rate-cards", body, map[string]string{"Idempotency-Key": "k1"}, api.DutyRateApprove)
		if rec.Code != http.StatusCreated {
			t.Fatalf("status = %d, body %s", rec.Code, rec.Body.String())
		}
	}
	other := strings.Replace(body, `"0.5"`, `"0.6"`, 1)
	doWith(t, h, http.MethodPost, base+"/rate-cards", other, map[string]string{"Idempotency-Key": "k1"}, api.DutyRateApprove)
	if len(st.idems) != 3 || st.idems[0].Key != "k1" || st.idems[0].Fingerprint == "" {
		t.Fatalf("idems = %+v", st.idems)
	}
	if st.idems[0].Fingerprint != st.idems[1].Fingerprint || st.idems[0].Fingerprint == st.idems[2].Fingerprint {
		t.Fatalf("same body must fingerprint the same and another body differently: %+v", st.idems)
	}
}

func TestCreateRateCard_RejectsUnknownBasisAndUnit(t *testing.T) {
	h := router(&fakeStore{})
	body := `{"carrierId":"` + carrierID + `","serviceId":"` + serviceID + `","basis":"BOGUS","currency":"USD","validFrom":"2026-01-01",` +
		`"rows":[{"origin":"*","destination":"*","breakFrom":"0","rate":"0.5","unit":"STONE"}]}`
	rec := do(t, h, http.MethodPost, base+"/rate-cards", body, api.DutyRateApprove)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400, body %s", rec.Code, rec.Body.String())
	}
	if p := problem(t, rec); p.Errors["basis"] == "" || p.Errors["rows[0]"] == "" {
		t.Fatalf("errors = %v", p.Errors)
	}
}

func TestCreateRateCard_RejectsOverlongIdempotencyKey(t *testing.T) {
	h := router(&fakeStore{})
	rec := doWith(t, h, http.MethodPost, base+"/rate-cards", `{}`, map[string]string{"Idempotency-Key": strings.Repeat("k", 201)}, api.DutyRateApprove)
	if rec.Code != http.StatusBadRequest || problem(t, rec).Errors["Idempotency-Key"] == "" {
		t.Fatalf("status = %d, body %s", rec.Code, rec.Body.String())
	}
}

func TestCreateFreightBill_ComputesExpectedTotal(t *testing.T) {
	h := router(&fakeStore{})
	body := `{"loadId":"` + loadID + `","carrierId":"` + carrierID + `","currency":"USD",` +
		`"expectedLines":[{"chargeCode":"BASE","amount":"400.00"},{"chargeCode":"FUEL","amount":"12.50"}]}`
	rec := do(t, h, http.MethodPost, base+"/freight-bills", body, api.DutyReconcile)
	if rec.Code != http.StatusCreated {
		t.Fatalf("status = %d, body %s", rec.Code, rec.Body.String())
	}
	var got domain.FreightBill
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if !got.ExpectedTotal.Equal(dec("412.50")) || got.MatchStatus != domain.BillUnmatched {
		t.Fatalf("bill = %+v", got)
	}
}

func TestCreateFreightBill_RejectsEmptyLinesAndBadIDs(t *testing.T) {
	h := router(&fakeStore{})
	body := `{"loadId":"L-1","carrierId":"` + strings.ToUpper(carrierID) + `","currency":"usd","expectedLines":[]}`
	rec := do(t, h, http.MethodPost, base+"/freight-bills", body, api.DutyReconcile)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", rec.Code)
	}
	p := problem(t, rec)
	for _, field := range []string{"loadId", "carrierId", "currency", "expectedLines"} {
		if p.Errors[field] == "" {
			t.Fatalf("errors = %v, missing %s", p.Errors, field)
		}
	}
}

func TestListFreightBills_FilterValidationAndPassThrough(t *testing.T) {
	st := &fakeStore{bill: domain.FreightBill{ID: billID, MatchStatus: domain.MatchVariance}}
	h := router(st)
	if rec := do(t, h, http.MethodGet, base+"/freight-bills?matchStatus=WHATEVER", "", api.DutyView); rec.Code != http.StatusBadRequest {
		t.Fatalf("unknown status filter = %d, want 400", rec.Code)
	}
	rec := do(t, h, http.MethodGet, base+"/freight-bills?matchStatus=VARIANCE&carrierId="+carrierID, "", api.DutyView)
	if rec.Code != http.StatusOK || st.billFilter.MatchStatus != domain.MatchVariance || st.billFilter.CarrierID != carrierID {
		t.Fatalf("status = %d, filter = %+v", rec.Code, st.billFilter)
	}
	if !strings.Contains(rec.Body.String(), `"items":[`) {
		t.Fatalf("body = %s, want an items list", rec.Body.String())
	}
}

// TestGetFreightBill_IncludesInvoiceAndLineComparison is AC-34.4 seen through
// the API: the bill of 412.50 against an invoice of 418.00 shows a 5.50 line
// variance outside the 5.00 allowance.
func TestGetFreightBill_IncludesInvoiceAndLineComparison(t *testing.T) {
	inv := invoiceID
	variance := dec("5.50")
	st := &fakeStore{
		bill: domain.FreightBill{ID: billID, LoadID: loadID, CarrierID: carrierID, Currency: "USD", MatchStatus: domain.MatchVariance,
			ExpectedLines: []domain.Line{{ChargeCode: "BASE", Amount: dec("412.50")}}, ExpectedTotal: dec("412.50"),
			CarrierInvoiceID: &inv, VarianceTotal: &variance},
		invoice: domain.CarrierInvoice{ID: invoiceID, CarrierID: carrierID, Currency: "USD", MatchStatus: domain.MatchVariance,
			Lines: []domain.Line{{ChargeCode: "BASE", Amount: dec("418.00")}}, Total: dec("418.00")},
	}
	rec := do(t, router(st), http.MethodGet, base+"/freight-bills/"+billID, "", api.DutyView)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body %s", rec.Code, rec.Body.String())
	}
	var got struct {
		ID             string                 `json:"id"`
		CarrierInvoice *domain.CarrierInvoice `json:"carrierInvoice"`
		Match          *domain.MatchResult    `json:"match"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if got.ID != billID || got.CarrierInvoice == nil || got.CarrierInvoice.ID != invoiceID || got.Match == nil {
		t.Fatalf("detail = %s", rec.Body.String())
	}
	line := got.Match.Lines[0]
	if got.Match.Status != domain.MatchVariance || !line.Variance.Equal(variance) || line.Within || !line.Allowed.Equal(dec("5")) {
		t.Fatalf("match = %+v", got.Match)
	}
}

func TestGetFreightBill_UnknownAndMalformedAreNotFound(t *testing.T) {
	h := router(&fakeStore{billErr: store.NotFound("freight bill")})
	for _, id := range []string{billID, "not-a-uuid"} {
		rec := do(t, h, http.MethodGet, base+"/freight-bills/"+id, "", api.DutyView)
		if rec.Code != http.StatusNotFound || problem(t, rec).Code != domain.CodeNotFound {
			t.Fatalf("%s: status = %d, body %s", id, rec.Code, rec.Body.String())
		}
	}
}

func TestRegisterCarrierInvoice_PassesTrimmedInvoice(t *testing.T) {
	st := &fakeStore{}
	body := `{"carrierId":"` + carrierID + `","invoiceNo":"  INV-1 ","invoiceDate":"2026-03-02","source":"EDI210",` +
		`"currency":"USD","lines":[{"chargeCode":"BASE","amount":"412.50"}]}`
	rec := do(t, router(st), http.MethodPost, base+"/carrier-invoices", body, api.DutyReconcile)
	if rec.Code != http.StatusCreated {
		t.Fatalf("status = %d, body %s", rec.Code, rec.Body.String())
	}
	if st.registered.InvoiceNo != "INV-1" || st.registered.FreightBillID != nil || st.registered.Source != "EDI210" {
		t.Fatalf("registered = %+v", st.registered)
	}
	if !strings.Contains(rec.Body.String(), `"matchStatus":"UNEXPECTED_INVOICE"`) {
		t.Fatalf("body = %s", rec.Body.String())
	}
}

func TestRegisterCarrierInvoice_StoreProblemsPassThrough(t *testing.T) {
	tests := []struct {
		name   string
		err    error
		status int
		code   string
	}{
		{"duplicate number", httpx.NewProblem(http.StatusConflict, domain.CodeDuplicateInvoice, "duplicate"), http.StatusConflict, domain.CodeDuplicateInvoice},
		{"bill already invoiced", httpx.NewProblem(http.StatusConflict, domain.CodeBillInvoiced, "invoiced"), http.StatusConflict, domain.CodeBillInvoiced},
		{"unknown bill", store.NotFound("freight bill"), http.StatusNotFound, domain.CodeNotFound},
	}
	body := `{"carrierId":"` + carrierID + `","invoiceNo":"INV-1","invoiceDate":"2026-03-02","source":"MANUAL","freightBillId":"` + billID + `",` +
		`"currency":"USD","lines":[{"chargeCode":"BASE","amount":"412.50"}]}`
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			rec := do(t, router(&fakeStore{registerErr: tc.err}), http.MethodPost, base+"/carrier-invoices", body, api.DutyReconcile)
			if rec.Code != tc.status || problem(t, rec).Code != tc.code {
				t.Fatalf("status = %d, body %s", rec.Code, rec.Body.String())
			}
		})
	}
}

func TestRegisterCarrierInvoice_Validation(t *testing.T) {
	body := `{"carrierId":"x","invoiceNo":"","invoiceDate":"02/03/2026","source":"FAX","freightBillId":"nope","currency":"USD","lines":[]}`
	rec := do(t, router(&fakeStore{}), http.MethodPost, base+"/carrier-invoices", body, api.DutyReconcile)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d", rec.Code)
	}
	p := problem(t, rec)
	for _, field := range []string{"carrierId", "invoiceNo", "invoiceDate", "source", "freightBillId", "lines"} {
		if p.Errors[field] == "" {
			t.Fatalf("errors = %v, missing %s", p.Errors, field)
		}
	}
}

func TestMetrics_CountsRateShopsAndInvoiceOutcomes(t *testing.T) {
	a := api.New(&fakeStore{cards: []domain.RateCard{card(carrierID, "0.50")}})
	r := chi.NewRouter()
	a.Register(r)
	do(t, r, http.MethodPost, base+"/shipments/"+loadID+":rate", rateBody, api.DutyRate)
	body := `{"carrierId":"` + carrierID + `","invoiceNo":"INV-9","invoiceDate":"2026-03-02","source":"MANUAL","currency":"USD","lines":[{"chargeCode":"BASE","amount":"1"}]}`
	do(t, r, http.MethodPost, base+"/carrier-invoices", body, api.DutyReconcile)
	rec := httptest.NewRecorder()
	a.Metrics().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/metrics", nil))
	for _, want := range []string{"transport_rate_shops_total 1", "transport_carrier_invoices_unexpected_total 1", "transport_carrier_invoices_matched_total 0"} {
		if !strings.Contains(rec.Body.String(), want) {
			t.Fatalf("metrics lack %q:\n%s", want, rec.Body.String())
		}
	}
}
