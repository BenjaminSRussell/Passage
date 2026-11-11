package api_test

import (
	"crypto/ed25519"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/platform/tenant"
	"github.com/BenjaminSRussell/passage/services/payables/internal/api"
	"github.com/BenjaminSRussell/passage/services/payables/internal/store"
	"github.com/BenjaminSRussell/passage/services/payables/internal/tax"
)

func ptr(s string) *string { return &s }

type fakeTax struct {
	mu       sync.Mutex
	requests []map[string]any
	tokens   []string
	status   int
	body     string
	// entries are the withholding entries recorded through POST /v1/tax/withholding/entries, one slice per request;
	// entriesStatus, when set, answers those requests with it.
	entries       [][]map[string]any
	entriesStatus int
}

func newFakeTax() *fakeTax { return &fakeTax{} }

func (f *fakeTax) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if r.URL.Path == "/v1/tax/withholding/entries" {
		var in struct {
			Entries []map[string]any `json:"entries"`
		}
		_ = json.NewDecoder(r.Body).Decode(&in)
		f.tokens = append(f.tokens, r.Header.Get(tenant.HeaderServiceToken))
		f.entries = append(f.entries, in.Entries)
		if f.entriesStatus != 0 {
			w.WriteHeader(f.entriesStatus)
			return
		}
		w.WriteHeader(http.StatusCreated)
		_ = json.NewEncoder(w).Encode(map[string]int{"recorded": len(in.Entries), "duplicates": 0})
		return
	}
	var req struct {
		Currency    string     `json:"currency"`
		PartyType   string     `json:"party_type"`
		PaymentDate string     `json:"payment_date"`
		Code        string     `json:"code"`
		GrossAmount string     `json:"gross_amount"`
		PaidToDate  string     `json:"paid_to_date"`
		Codes       []tax.Code `json:"codes"`
	}
	_ = json.NewDecoder(r.Body).Decode(&req)
	f.tokens = append(f.tokens, r.Header.Get(tenant.HeaderServiceToken))
	f.requests = append(f.requests, map[string]any{"currency": req.Currency, "party_type": req.PartyType, "payment_date": req.PaymentDate, "code": req.Code,
		"gross_amount": req.GrossAmount, "paid_to_date": req.PaidToDate, "codes": len(req.Codes), "path": r.URL.Path})
	if f.status != 0 {
		w.WriteHeader(f.status)
		_, _ = w.Write([]byte(f.body))
		return
	}
	gross, paid := decimal.RequireFromString(req.GrossAmount), decimal.RequireFromString(req.PaidToDate)
	rate := req.Codes[0].Rates[0]
	base := gross
	if rate.Threshold != nil {
		cumulative := paid.Add(gross)
		threshold := decimal.RequireFromString(*rate.Threshold)
		base = decimal.Zero
		if cumulative.GreaterThan(threshold) {
			base = decimal.Min(gross, cumulative.Sub(threshold))
		}
	}
	withheld := base.Mul(decimal.RequireFromString(rate.RatePct)).Div(decimal.NewFromInt(100)).Round(2)
	_ = json.NewEncoder(w).Encode(map[string]any{"taxable_base": base.String(), "withholding_amount": withheld.String(), "net_payable": gross.Sub(withheld).String()})
}

func (f *fakeTax) calls() []map[string]any {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]map[string]any(nil), f.requests...)
}

func vendorGroup(t *testing.T, proposal map[string]any, vendor string) map[string]any {
	t.Helper()
	for _, g := range proposal["groups"].([]any) {
		if g.(map[string]any)["vendorId"] == vendor {
			return g.(map[string]any)
		}
	}
	t.Fatalf("no group for %s in %v", vendor, proposal)
	return nil
}

func TestWithholding_TaxIsCalledPerLineWithASignedTokenAndTheAmountsAreRecorded(t *testing.T) {
	f := newRunFixture(t, fixtureOptions{withholdingCodes: map[string]string{vendorOne: "WHT30"}})
	proposal := decode(t, f.created)
	group := vendorGroup(t, proposal, vendorOne)
	if group["withheld"] != "44.4" || group["net"] != "103.6" || group["gross"] != "150" || group["discount"] != "2" || fmt.Sprint(proposal["totalWithheld"]) != "map[USD:44.4]" || proposal["totalValued"] != "303.6" {
		t.Fatalf("group %v proposal %v, want 30%% of 98 and 50 withheld", group, proposal)
	}
	lines := group["lines"].([]any)
	first, second := lines[0].(map[string]any), lines[1].(map[string]any)
	if first["withheld"] != "29.4" || first["withholdingCode"] != "WHT30" || first["pay"] != "98" || second["withheld"] != "15" || second["valued"] != "35" {
		t.Fatalf("lines = %v", lines)
	}
	if other := vendorGroup(t, proposal, vendorTwo); other["withheld"] != "0" || other["net"] != "200" {
		t.Fatalf("a vendor without a code is untouched: %v", other)
	}
	calls := f.tax.calls()
	if len(calls) != 2 || calls[0]["gross_amount"] != "98" || calls[0]["paid_to_date"] != "0" || calls[1]["gross_amount"] != "50" || calls[1]["paid_to_date"] != "98" {
		t.Fatalf("tax calls = %v, want one per withheld line carrying the running paid-to-date", calls)
	}
	for _, c := range calls {
		if c["currency"] != "USD" || c["party_type"] != "vendor" || c["payment_date"] != "2026-01-10" || c["code"] != "WHT30" || c["codes"] != 1 || c["path"] != "/v1/tax/withholding/calculate" {
			t.Errorf("call = %v", c)
		}
	}
	verifier := tenant.NewServiceVerifier("tax", map[string]ed25519.PublicKey{"payables": f.verify})
	claims, err := verifier.Verify(f.tax.tokens[0])
	if err != nil || claims.Aud != "tax" || claims.Le != entityA || len(claims.Duties) != 1 || claims.Duties[0] != "tax.withholding.calculate" {
		t.Fatalf("claims %+v err %v", claims, err)
	}
}

func TestWithholding_ThresholdUsesPaidToDateAcrossTheVendorsLines(t *testing.T) {
	f := newRunFixture(t, fixtureOptions{withholdingCodes: map[string]string{vendorOne: "WHT10T"}})
	group := vendorGroup(t, decode(t, f.created), vendorOne)
	lines := group["lines"].([]any)
	if group["withheld"] != "2.8" || lines[0].(map[string]any)["withheld"] != "0" || lines[0].(map[string]any)["withholdingCode"] != nil || lines[1].(map[string]any)["withheld"] != "2.8" {
		t.Fatalf("group %v: only the part of 148 above the 120 threshold is withheld", group)
	}
}

func TestWithholding_RunPaysTheNetPostsTheWithheldTaxAndSettlesTheFullItem(t *testing.T) {
	f := newRunFixture(t, fixtureOptions{withholdingCodes: map[string]string{vendorOne: "WHT30"}})
	f.approve()
	run := f.start()
	if run["withholdingAccount"] != "2300" {
		t.Fatalf("run = %v, want the withholding account snapshotted", run)
	}
	runID := run["id"].(string)
	if rec := f.step(f.erin, runID, "generate-file"); rec.Code != http.StatusOK {
		t.Fatalf("generate-file: %d %s", rec.Code, rec.Body)
	}
	got := decode(t, f.erin.send(f.router, http.MethodGet, runsPath+"/"+runID, ""))
	payments := got["payments"].([]any)
	if first := payments[0].(map[string]any); first["amount"] != "103.6" || first["valuedAmount"] != "103.6" {
		t.Fatalf("payments = %v, want the vendor paid 148 less 44.40 withheld", payments)
	}
	if file := f.erin.send(f.router, http.MethodGet, runsPath+"/"+runID+"/file", "").Body.String(); !strings.Contains(file, "103.60") || strings.Contains(file, "148.00") {
		t.Fatalf("the bank file must pay the net: %s", file)
	}
	if rec := f.step(f.erin, runID, "post-journal"); rec.Code != http.StatusOK {
		t.Fatalf("post-journal: %d %s", rec.Code, rec.Body)
	}
	lines := map[string]bool{}
	for _, l := range f.ledger.bodies[0]["lines"].([]any) {
		line := l.(map[string]any)
		lines[fmt.Sprintf("%s %s %s", line["account"], line["debitCredit"], line["amount"].(map[string]any)["value"])] = true
	}
	for _, want := range []string{"2000 D 150.00", "2000 D 200.00", "1010 C 303.60", "2300 C 44.40", "4900 C 2.00"} {
		if !lines[want] {
			t.Errorf("journal lacks %q: %v", want, lines)
		}
	}
	if rec := f.step(f.erin, runID, "settle"); rec.Code != http.StatusOK {
		t.Fatalf("settle: %d %s", rec.Code, rec.Body)
	}
	settled, err := f.pg.Get(f.ctx(), f.itemA.ID)
	if err != nil || settled.Status != "SETTLED" || !settled.OpenAmount.IsZero() {
		t.Fatalf("the item settles in full, withheld tax included: %+v err=%v", settled, err)
	}
}

func TestWithholding_RunStartNeedsAWithholdingAccountOnlyWhenTaxWasWithheld(t *testing.T) {
	withheld := newRunFixture(t, fixtureOptions{withholdingCodes: map[string]string{vendorOne: "WHT30"}, noWithholdingAccount: true})
	withheld.approve()
	wantProblem(t, withheld.erin.send(withheld.router, http.MethodPost, runsPath, withheld.startBody()), http.StatusUnprocessableEntity, "AP-RUN-013")
	if withheld.proposalStatus() != "APPROVED" {
		t.Fatalf("proposal = %s", withheld.proposalStatus())
	}
	plain := newRunFixture(t, fixtureOptions{noWithholdingAccount: true})
	plain.approve()
	if plain.start()["status"] != "RUNNING" {
		t.Fatal("a proposal without withholding needs no withholding account")
	}
}

func TestWithholding_ProposalRefusals(t *testing.T) {
	f := newRunFixture(t, fixtureOptions{skipProposal: true})
	create := func(runID string, codes map[string]string) *httptest.ResponseRecorder {
		return f.alice.send(f.router, http.MethodPost, "/v1/payables/payment-proposals", runProposalBody(runID, codes))
	}

	wantProblem(t, create("run-1", map[string]string{vendorOne: "NOPE"}), http.StatusUnprocessableEntity, "AP-PRP-006")
	wantProblem(t, create("run-1", map[string]string{"not-a-uuid": "WHT30"}), http.StatusBadRequest, "COM-0001")
	wantProblem(t, create("run-1", map[string]string{vendorOne: ""}), http.StatusBadRequest, "COM-0001")
	if n := len(f.tax.calls()); n != 0 {
		t.Fatalf("validation failures called tax %d times", n)
	}

	f.tax.mu.Lock()
	f.tax.status, f.tax.body = http.StatusUnprocessableEntity, `{"code":"TAX-0001","serviceCode":"TAX-0001"}`
	f.tax.mu.Unlock()
	body := wantProblem(t, create("run-1", map[string]string{vendorOne: "WHT30"}), http.StatusUnprocessableEntity, "AP-PRP-006")
	if !strings.Contains(body["detail"].(string), "TAX-0001") {
		t.Fatalf("detail %v, want the tax rule code", body["detail"])
	}

	f.tax.mu.Lock()
	f.tax.status, f.tax.body = http.StatusInternalServerError, ""
	f.tax.mu.Unlock()
	rec := create("run-1", map[string]string{vendorOne: "WHT30"})
	wantProblem(t, rec, http.StatusBadGateway, "COM-0502")
	if rec.Header().Get("Retry-After") == "" || !strings.Contains(scrape(f.api), "payables_withholding_faults_total 1") {
		t.Fatalf("retry-after %q metrics:\n%s", rec.Header().Get("Retry-After"), scrape(f.api))
	}

	f.tax.mu.Lock()
	f.tax.status = 0
	f.tax.mu.Unlock()
	if rec := create("run-1", map[string]string{vendorOne: "WHT30"}); rec.Code != http.StatusCreated {
		t.Fatalf("after tax recovers nothing was left behind by the failures: %d %s", rec.Code, rec.Body)
	}
	wantProblem(t, create("run-1", map[string]string{vendorOne: "WHT10T"}), http.StatusConflict, "AP-PRP-001")
	if replay := create("run-1", map[string]string{vendorOne: "WHT30"}); replay.Code != http.StatusOK || len(f.tax.calls()) != 4 {
		t.Fatalf("replay %d, tax calls %d: a replay must not call tax again", replay.Code, len(f.tax.calls()))
	}
}

func TestWithholding_TaxNotConfiguredRefusesCodesButNotPlainProposals(t *testing.T) {
	f := newRunFixture(t, fixtureOptions{skipProposal: true, noTax: true})
	wantProblem(t, f.alice.send(f.router, http.MethodPost, "/v1/payables/payment-proposals", runProposalBody("run-1", map[string]string{vendorOne: "WHT30"})), http.StatusServiceUnavailable, "COM-0503")
	if rec := f.alice.send(f.router, http.MethodPost, "/v1/payables/payment-proposals", runProposalBody("run-1", nil)); rec.Code != http.StatusCreated {
		t.Fatalf("plain proposal: %d %s", rec.Code, rec.Body)
	}
}

func TestLoadPostingProfiles_WithholdingCodesAndAccount(t *testing.T) {
	profile := func(extra string) func(string) string {
		raw := `{"` + entityA + `":{"ledgerId":"` + runLedgerID + `","bankAccount":"1010","controlAccount":"2000",` + extra + `}}`
		return func(k string) string { return map[string]string{api.EnvPostingProfiles: raw}[k] }
	}
	good := `"withholdingAccount":"2300","withholdingCodes":[{"code":"WHT30","jurisdiction":"US","income_type":"services","rates":[{"valid_from":"2020-01-01","rate_pct":"30","threshold":"600"}]}]`
	profiles, err := api.LoadPostingProfiles(profile(good))
	if err != nil {
		t.Fatal(err)
	}
	p, _ := profiles.For(entityA)
	if p.WithholdingAccount != "2300" || len(p.WithholdingCodes) != 1 || p.WithholdingCodes[0].Rates[0].RatePct != "30" || *p.WithholdingCodes[0].Rates[0].Threshold != "600" {
		t.Fatalf("profile = %+v", p)
	}
	if _, err := api.LoadPostingProfiles(profile(`"withholdingAccount":"` + strings.Repeat("9", 21) + `"`)); err == nil {
		t.Error("a withholding account longer than 20 characters must fail")
	}
	if _, err := api.LoadPostingProfiles(profile(`"withholdingCodes":[{"code":"X","rate":30}]`)); err == nil {
		t.Error("an unknown withholding code field must fail")
	}
}

func TestWithholding_TaxRefusalIsANonRetryableConfigFault(t *testing.T) {
	f := newRunFixture(t, fixtureOptions{skipProposal: true})
	f.tax.mu.Lock()
	f.tax.status = http.StatusForbidden
	f.tax.mu.Unlock()
	rec := f.alice.send(f.router, http.MethodPost, "/v1/payables/payment-proposals", runProposalBody("run-1", map[string]string{vendorOne: "WHT30"}))
	wantProblem(t, rec, http.StatusInternalServerError, "COM-0500")
	if rec.Header().Get("Retry-After") != "" {
		t.Fatal("a refusal must not be retried")
	}
}

func TestWithholding_SecondDraftForTheSameVendorIsRefusedAtApprovalAfterTheFirstWasApproved(t *testing.T) {
	f := newRunFixture(t, fixtureOptions{withholdingCodes: map[string]string{vendorOne: "WHT10T"}})
	in := store.OpenItem{LegalEntityID: entityA, VendorID: vendorOne, SourceInvoiceID: "00000000-0000-0000-0000-0000000000f9", InvoiceNumber: "INV-F9",
		InvoiceDate: "2025-12-01", DueDate: "2026-01-04", Currency: "USD", Amount: decimal.RequireFromString("40.00")}
	if _, _, err := f.pg.CreateFromInvoice(f.ctx(), in); err != nil {
		t.Fatal(err)
	}
	second := f.alice.send(f.router, http.MethodPost, "/v1/payables/payment-proposals", runProposalBody("run-2", map[string]string{vendorOne: "WHT10T"}))
	if second.Code != http.StatusCreated {
		t.Fatalf("second draft: %d %s", second.Code, second.Body)
	}
	secondID := decode(t, second)["id"].(string)
	f.approve()
	wantProblem(t, f.bob.send(f.router, http.MethodPost, "/v1/payables/payment-proposals/"+secondID+":approve", ""), http.StatusConflict, "AP-PRP-001")
}

// #13: the tax withheld by a payment run is recorded with the tax service, one entry per invoice, so its remittance runs and certificates
// cover it. The entry ids are stable, so a retry after a failed recording sends the same entries again.
func TestWithholding_RunRecordsTheWithheldTaxWithTheTaxServiceAndRetriesIdempotently(t *testing.T) {
	f := newRunFixture(t, fixtureOptions{withholdingCodes: map[string]string{vendorOne: "WHT30"}})
	f.approve()
	runID := f.start()["id"].(string)
	if rec := f.step(f.erin, runID, "generate-file"); rec.Code != http.StatusOK {
		t.Fatalf("generate-file: %d %s", rec.Code, rec.Body)
	}
	f.tax.mu.Lock()
	f.tax.entriesStatus = http.StatusServiceUnavailable
	f.tax.mu.Unlock()
	if rec := f.step(f.erin, runID, "post-journal"); rec.Code != http.StatusBadGateway {
		t.Fatalf("post-journal while tax is down: %d %s, want 502", rec.Code, rec.Body)
	}
	if len(f.ledger.bodies) != 1 {
		t.Fatalf("the ledger journal was posted %d times", len(f.ledger.bodies))
	}
	f.tax.mu.Lock()
	f.tax.entriesStatus = 0
	f.tax.mu.Unlock()
	if rec := f.step(f.erin, runID, "post-journal"); rec.Code != http.StatusOK {
		t.Fatalf("post-journal retry: %d %s", rec.Code, rec.Body)
	}
	f.tax.mu.Lock()
	defer f.tax.mu.Unlock()
	if len(f.tax.entries) != 2 || len(f.tax.entries[1]) != 2 {
		t.Fatalf("tax received %v, want the two withheld lines twice", f.tax.entries)
	}
	for i, e := range f.tax.entries[1] {
		if e["id"] != f.tax.entries[0][i]["id"] || e["party_type"] != "vendor" || e["party_ref"] != vendorOne || e["code"] != "WHT30" || e["currency"] != "USD" {
			t.Errorf("entry %d = %v (first attempt %v)", i, e, f.tax.entries[0][i])
		}
	}
	first := f.tax.entries[1][0]
	if first["gross_amount"] != "98" || first["taxable_base"] != "98" || first["withholding_amount"] != "29.4" {
		t.Errorf("first entry = %v, want gross 98, base 98, tax 29.4", first)
	}
	if f.tax.entries[1][0]["id"] == f.tax.entries[1][1]["id"] {
		t.Error("two invoices share an entry id")
	}
}
