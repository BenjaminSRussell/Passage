package api_test

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/platform/dbx"
	"github.com/BenjaminSRussell/passage/platform/fieldcrypt"
	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/platform/tenant"
	"github.com/BenjaminSRussell/passage/platform/testkit"
	"github.com/BenjaminSRussell/passage/services/payables"
	"github.com/BenjaminSRussell/passage/services/payables/internal/api"
	"github.com/BenjaminSRussell/passage/services/payables/internal/domain"
	"github.com/BenjaminSRussell/passage/services/payables/internal/fx"
	"github.com/BenjaminSRussell/passage/services/payables/internal/ledger"
	"github.com/BenjaminSRussell/passage/services/payables/internal/party"
	"github.com/BenjaminSRussell/passage/services/payables/internal/store"
	"github.com/BenjaminSRussell/passage/services/payables/internal/tax"
	"github.com/BenjaminSRussell/passage/services/payables/migrations"
)

const (
	runLedgerID = "00000000-0000-0000-0000-0000000000b1"
	runsPath    = "/v1/payables/payment-runs"
	runDuties   = "payment-run.execute,payment-run.read"
	approveAll  = "open-item.read,payment-proposal.write,payment-proposal.read,payment-proposal.approve"
)

type fakeJournalLedger struct {
	mu           sync.Mutex
	entries      map[string]string
	bodies       []map[string]any
	keys         []string
	rejectWith   int
	rejectCode   string
	dropReplies  int
	entity       string
	gate         chan struct{}
	entered      chan struct{}
	inflight     map[string]bool
	foreignEntry string
	lookupFails  bool
	lookups      []string
}

func (f *fakeJournalLedger) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	if r.Method == http.MethodGet && r.URL.Path == "/v1/ledger/journal-lines" {
		defer f.mu.Unlock()
		q := r.URL.Query()
		f.lookups = append(f.lookups, q.Get("documentId"))
		if f.lookupFails {
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		if q.Get("ledger") != runLedgerID || q.Get("source_system") != "payables" || q.Get("documentId") == "" {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		var items []string
		if f.foreignEntry != "" {
			items = append(items, fmt.Sprintf(`{"entryId":%q,"account":"2000","debitCredit":"D","amount":"1.00","currency":"USD","sourceType":"manual","sourceId":%q}`, f.foreignEntry, q.Get("documentId")))
		}
		for _, key := range f.keys {
			if len(f.bodies) == 0 {
				break
			}
			body := f.bodies[0]
			src, _ := body["source"].(map[string]any)
			lines, _ := body["lines"].([]any)
			for _, raw := range lines {
				l, _ := raw.(map[string]any)
				amount, _ := l["amount"].(map[string]any)
				items = append(items, fmt.Sprintf(`{"entryId":%q,"account":%q,"debitCredit":%q,"amount":%q,"currency":%q,"sourceType":%q,"sourceId":%q}`,
					f.entries[key], l["account"], l["debitCredit"], amount["value"], amount["currency"], src["type"], src["id"]))
			}
			break
		}
		_, _ = fmt.Fprintf(w, `{"items":[%s]}`, strings.Join(items, ","))
		return
	}
	if r.Method == http.MethodGet {
		defer f.mu.Unlock()
		_, _ = fmt.Fprintf(w, `{"legalEntityId":%q}`, f.entity)
		return
	}
	key := r.Header.Get("Idempotency-Key")
	if f.rejectWith != 0 {
		defer f.mu.Unlock()
		w.WriteHeader(f.rejectWith)
		if f.rejectCode != "" {
			_, _ = fmt.Fprintf(w, `{"code":%q}`, f.rejectCode)
		}
		return
	}
	if f.inflight[key] {
		f.mu.Unlock()
		w.Header().Set("Retry-After", "1")
		w.WriteHeader(http.StatusConflict)
		_, _ = w.Write([]byte(`{"code":"LEDGER_IDEMPOTENCY_IN_PROGRESS"}`))
		return
	}
	_, replay := f.entries[key]
	if !replay && f.gate != nil {
		f.inflight[key] = true
		gate := f.gate
		f.mu.Unlock()
		f.entered <- struct{}{}
		<-gate
		f.mu.Lock()
		delete(f.inflight, key)
	}
	defer f.mu.Unlock()
	id, replay := f.entries[key]
	if !replay {
		id = fmt.Sprintf("entry-%d", len(f.entries)+1)
		f.entries[key] = id
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		f.bodies = append(f.bodies, body)
	}
	f.keys = append(f.keys, key)
	if f.dropReplies > 0 {
		f.dropReplies--
		w.WriteHeader(http.StatusInternalServerError)
		return
	}
	status := http.StatusCreated
	if replay {
		status = http.StatusOK
	}
	w.WriteHeader(status)
	_, _ = fmt.Fprintf(w, `{"entries":[{"id":%q}]}`, id)
}

func (f *fakeJournalLedger) posted() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.entries)
}

type fakeParty struct {
	mu       sync.Mutex
	accounts map[string][]map[string]any
	failures int
	calls    []string
	tokens   []string
}

func partyAccount(holder, number, routing, currency string) map[string]any {
	return map[string]any{"id": "acct-" + number, "accountHolder": holder, "bankCountry": "US", "currencyCode": currency, "accountNumber": number, "routingNumber": routing}
}

func newFakeParty() *fakeParty {
	return &fakeParty{accounts: map[string][]map[string]any{
		vendorOne: {partyAccount("Acme Supply Co", "000111222333", "021000021", "USD")},
		vendorTwo: {partyAccount("Example Widgets LLC", "000444555666", "011000015", "USD")},
	}}
}

func (f *fakeParty) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.tokens = append(f.tokens, r.Header.Get(tenant.HeaderServiceToken))
	vendor := strings.TrimSuffix(strings.TrimPrefix(r.URL.Path, "/v1/party/vendors/"), "/bank-accounts:reveal")
	f.calls = append(f.calls, vendor)
	if f.failures > 0 {
		f.failures--
		w.WriteHeader(http.StatusInternalServerError)
		return
	}
	accounts, ok := f.accounts[vendor]
	if !ok {
		w.WriteHeader(http.StatusNotFound)
		return
	}
	_ = json.NewEncoder(w).Encode(map[string]any{"bankAccounts": accounts})
}

func (f *fakeParty) callCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.calls)
}

type fixtureOptions struct {
	validator api.FileValidator
	foreign   bool
	noKeys    bool
	noProfile bool
	noParty   bool

	withholdingCodes     map[string]string
	skipProposal         bool
	noTax                bool
	noWithholdingAccount bool
}

type runFixture struct {
	t        *testing.T
	router   http.Handler
	api      *api.API
	ledger   *fakeJournalLedger
	party    *fakeParty
	tax      *fakeTax
	signer   *tenant.ServiceSigner
	verify   ed25519.PublicKey
	partyURL string
	pg       *store.Postgres
	proposal string
	erin     caller
	alice    caller
	bob      caller
	itemA    store.OpenItem
	created  *httptest.ResponseRecorder
	clock    *time.Time
}

func runProposalBody(runID string, withholdingCodes map[string]string) string {
	body := map[string]any{"legalEntityId": entityA, "legalEntityCurrency": "USD", "runDate": "2026-01-10", "runId": runID, "paymentMethod": "TRANSFER"}
	if withholdingCodes != nil {
		body["withholdingCodes"] = withholdingCodes
	}
	raw, _ := json.Marshal(body)
	return string(raw)
}

func profiles() api.PostingProfiles {
	return api.PostingProfiles{entityA: api.PostingProfile{
		LedgerID: runLedgerID, BankAccount: "1010", ControlAccount: "2000", DiscountAccount: "4900", WithholdingAccount: "2300",
		WithholdingCodes: []tax.Code{
			{Code: "WHT30", Jurisdiction: "US", IncomeType: "services", Rates: []tax.Rate{{ValidFrom: "2020-01-01", RatePct: "30"}}},
			{Code: "WHT10T", Jurisdiction: "US", IncomeType: "royalties", Rates: []tax.Rate{{ValidFrom: "2020-01-01", RatePct: "10", Threshold: ptr("120")}}},
		},
		File: domain.RunFileConfig{
			Debtor:        domain.PartyDTO{Name: "Passage Holding Inc", Address: domain.PostalAddressDTO{StreetName: "1 Market Street", PostCode: "94105", TownName: "San Francisco", Country: "US"}},
			DebtorAccount: domain.AccountDTO{OtherID: "000123456789"}, DebtorBIC: "CHASUS33",
			Profile: domain.BankProfileDTO{Variant: "US", Overrides: map[string]json.RawMessage{"charset": json.RawMessage(`"ascii"`), "initiating_party_id": json.RawMessage(`"PASSAGEUS01"`)}},
		},
	}}
}

func (f *runFixture) startBody() string {
	return fmt.Sprintf(`{"legalEntityId":%q,"proposalId":%q}`, entityA, f.proposal)
}

func (f *runFixture) step(c caller, runID, action string) *httptest.ResponseRecorder {
	f.t.Helper()
	return c.send(f.router, http.MethodPost, runsPath+"/"+runID+":"+action, "")
}

func (f *runFixture) start() map[string]any {
	f.t.Helper()
	rec := f.erin.send(f.router, http.MethodPost, runsPath, f.startBody())
	if rec.Code != http.StatusCreated {
		f.t.Fatalf("start: %d %s", rec.Code, rec.Body)
	}
	return decode(f.t, rec)
}

func (f *runFixture) ctx() context.Context {
	return tenant.With(context.Background(), tenant.Principal{TenantID: testkit.TenantID(1), LegalEntityID: entityA, UserID: "seeder", Duties: testkit.ScopeDuties(entityA)})
}

func newRunFixture(t *testing.T, opts fixtureOptions) *runFixture {
	t.Helper()
	if opts.validator == nil {
		opts.validator = fakeValidator{}
	}
	clock := fixedNow()
	admin, app := testkit.DBWithApp(t)
	if err := dbx.Migrate(context.Background(), admin, migrations.FS); err != nil {
		t.Fatal(err)
	}
	pg := store.New(app)
	if !opts.noKeys {
		keys, err := fieldcrypt.NewKeys("k1", map[string][]byte{"k1": bytes.Repeat([]byte{9}, 32)})
		if err != nil {
			t.Fatal(err)
		}
		pg.WithFieldCrypt(keys)
	}
	seedCtx := tenant.With(context.Background(), tenant.Principal{TenantID: testkit.TenantID(1), LegalEntityID: entityA, UserID: "seeder", Duties: testkit.ScopeDuties(entityA)})
	seed := func(source, vendor, due, currency, amount, discount string) store.OpenItem {
		in := store.OpenItem{LegalEntityID: entityA, VendorID: vendor, SourceInvoiceID: source, InvoiceNumber: "INV-" + source[len(source)-2:],
			InvoiceDate: "2025-12-01", DueDate: due, Currency: currency, Amount: decimal.RequireFromString(amount), DiscountAmount: decimal.RequireFromString(discount)}
		if discount != "0" {
			day := "2026-01-10"
			in.DiscountDate = &day
		}
		o, _, err := pg.CreateFromInvoice(seedCtx, in)
		if err != nil {
			t.Fatal(err)
		}
		return o
	}
	itemA := seed(itemOne, vendorOne, "2026-01-05", "USD", "100.00", "2.00")
	seed(itemTwo, vendorOne, "2026-01-06", "USD", "50.00", "0")
	third := "USD"
	if opts.foreign {
		third = "EUR"
	}
	seed(itemThree, vendorTwo, "2026-01-08", third, "200.00", "0")

	fl := &fakeJournalLedger{entries: map[string]string{}, entity: entityA, inflight: map[string]bool{}, entered: make(chan struct{}, 4)}
	srv := httptest.NewServer(fl)
	t.Cleanup(srv.Close)
	verifyKey, signingKey, _ := ed25519.GenerateKey(nil)
	signer, _ := tenant.NewServiceSigner("payables", signingKey)
	runProfiles := profiles()
	if opts.noWithholdingAccount {
		p := runProfiles[entityA]
		p.WithholdingAccount = ""
		runProfiles[entityA] = p
	}
	if opts.noProfile {
		runProfiles = nil
	}
	fp := newFakeParty()
	partySrv := httptest.NewServer(fp)
	t.Cleanup(partySrv.Close)
	ft := newFakeTax()
	taxSrv := httptest.NewServer(ft)
	t.Cleanup(taxSrv.Close)
	var banks api.VendorBanks
	if !opts.noParty {
		banks = party.New(partySrv.URL, nil).WithSigner(signer)
	}
	a := api.New(app, opts.validator, func() time.Time { return clock }).WithProposals(pg, fx.New(fxServer(t).URL, "", nil)).WithPaymentRuns(pg, ledger.New(srv.URL, nil).WithSigner(signer), runProfiles).WithVendorBanks(banks)
	if !opts.noTax {
		a.WithWithholding(tax.New(taxSrv.URL, nil).WithSigner(signer), pg, runProfiles)
	}
	router := httpx.NewRouter()
	a.Register(router)

	f := &runFixture{t: t, router: router, api: a, ledger: fl, party: fp, tax: ft, signer: signer, verify: verifyKey, partyURL: partySrv.URL, pg: pg, itemA: itemA, clock: &clock,
		erin: caller{duties: runDuties, user: "erin", entity: entityA}, alice: caller{duties: approveAll, user: "alice", entity: entityA}, bob: caller{duties: approveAll, user: "bob", entity: entityA}}
	if opts.skipProposal {
		return f
	}
	f.created = f.alice.send(router, http.MethodPost, "/v1/payables/payment-proposals", runProposalBody("run-1", opts.withholdingCodes))
	if f.created.Code != http.StatusCreated {
		t.Fatalf("create proposal: %d %s", f.created.Code, f.created.Body)
	}
	f.proposal = decode(t, f.created)["id"].(string)
	return f
}

func (f *runFixture) approve() {
	f.t.Helper()
	if rec := f.bob.send(f.router, http.MethodPost, "/v1/payables/payment-proposals/"+f.proposal+":approve", ""); rec.Code != http.StatusOK {
		f.t.Fatalf("approve: %d %s", rec.Code, rec.Body)
	}
}

func (f *runFixture) proposalStatus() string {
	return decode(f.t, f.bob.send(f.router, http.MethodGet, "/v1/payables/payment-proposals/"+f.proposal, ""))["status"].(string)
}

func TestPaymentRun_EndToEndWithRealXSDAndLedgerReplay(t *testing.T) {
	validator, err := domain.NewXSDValidator(payables.Pain001XSD)
	if err != nil {
		t.Skipf("xmllint unavailable: %v", err)
	}
	t.Cleanup(func() { _ = validator.Close() })
	f := newRunFixture(t, fixtureOptions{validator: validator})
	f.approve()

	run := f.start()
	runID := run["id"].(string)
	if run["status"] != "RUNNING" || f.proposalStatus() != "EXECUTING" || run["requestedBy"] != "erin" || run["ledgerId"] != runLedgerID || run["bankAccount"] != "1010" {
		t.Fatalf("run = %v, proposal = %s", run, f.proposalStatus())
	}
	replay := f.erin.send(f.router, http.MethodPost, runsPath, f.startBody())
	if replay.Code != http.StatusOK || replay.Header().Get("Idempotent-Replayed") != "true" || decode(t, replay)["id"] != runID {
		t.Fatalf("replayed start: %d %s", replay.Code, replay.Body)
	}

	for pass := 0; pass < 2; pass++ {
		if gen := f.step(f.erin, runID, "generate-file"); gen.Code != http.StatusOK {
			t.Fatalf("generate-file pass %d: %d %s", pass, gen.Code, gen.Body)
		}
	}
	file := f.erin.send(f.router, http.MethodGet, runsPath+"/"+runID+"/file", "")
	if file.Code != http.StatusOK || !strings.HasPrefix(file.Header().Get("Content-Type"), "application/xml") || !strings.Contains(file.Body.String(), "pain.001.001.09") || !strings.Contains(file.Body.String(), "000111222333") {
		t.Fatalf("file: %d %s", file.Code, file.Body)
	}
	got := decode(t, f.erin.send(f.router, http.MethodGet, runsPath+"/"+runID, ""))
	payments := got["payments"].([]any)
	if got["paymentCount"] != float64(2) || len(payments) != 2 {
		t.Fatalf("run after file = %v", got)
	}
	first, second := payments[0].(map[string]any), payments[1].(map[string]any)
	if first["amount"] != "148" || first["valuedAmount"] != "148" || second["amount"] != "200" || second["valuedAmount"] != "200" {
		t.Fatalf("payments = %v, want 148 (150 less 2 discount) and 200", payments)
	}

	for pass := 0; pass < 2; pass++ {
		if rec := f.step(f.erin, runID, "post-journal"); rec.Code != http.StatusOK {
			t.Fatalf("post-journal pass %d: %d %s", pass, rec.Code, rec.Body)
		}
	}
	if f.ledger.posted() != 1 || len(f.ledger.keys) != 1 {
		t.Fatalf("ledger entries = %d, post calls = %d, want the journal posted once", f.ledger.posted(), len(f.ledger.keys))
	}
	lines := map[string]bool{}
	for _, l := range f.ledger.bodies[0]["lines"].([]any) {
		line := l.(map[string]any)
		lines[fmt.Sprintf("%s %s %s", line["account"], line["debitCredit"], line["amount"].(map[string]any)["value"])] = true
	}
	for _, want := range []string{"2000 D 150.00", "2000 D 200.00", "1010 C 348.00", "4900 C 2.00"} {
		if !lines[want] {
			t.Errorf("journal lacks %q: %v", want, lines)
		}
	}
	if f.ledger.keys[0] != "payables:payment-run:"+runID || f.ledger.bodies[0]["postingDate"] != "2026-01-10" || f.ledger.bodies[0]["ledgerId"] != runLedgerID {
		t.Errorf("journal key/date/ledger = %s / %v / %v", f.ledger.keys[0], f.ledger.bodies[0]["postingDate"], f.ledger.bodies[0]["ledgerId"])
	}

	for pass := 0; pass < 2; pass++ {
		if rec := f.step(f.erin, runID, "settle"); rec.Code != http.StatusOK {
			t.Fatalf("settle pass %d: %d %s", pass, rec.Code, rec.Body)
		}
	}
	settled, err := f.pg.Get(f.ctx(), f.itemA.ID)
	if err != nil || settled.Status != "SETTLED" || !settled.OpenAmount.IsZero() || settled.Version != 2 {
		t.Fatalf("item after settle = %+v err=%v", settled, err)
	}
	for pass := 0; pass < 2; pass++ {
		done := f.step(f.erin, runID, "complete")
		if done.Code != http.StatusOK || decode(t, done)["status"] != "COMPLETED" {
			t.Fatalf("complete pass %d: %d %s", pass, done.Code, done.Body)
		}
	}
	if f.proposalStatus() != "EXECUTED" {
		t.Fatalf("proposal = %s, want EXECUTED", f.proposalStatus())
	}
	metrics := scrape(f.api)
	for _, want := range []string{`payables_payment_runs_total{outcome="started"} 1`, `payables_payment_runs_total{outcome="replayed"} 1`, `payables_payment_runs_total{outcome="completed"} 2`, "payables_payment_file_reads_total 1", "payables_payment_run_stuck_total 0"} {
		if !strings.Contains(metrics, want) {
			t.Errorf("metrics lack %q:\n%s", want, metrics)
		}
	}
}

func TestPaymentRun_ProposalNoLongerAcceptsBankDetailsFromItsCreator(t *testing.T) {
	f := newRunFixture(t, fixtureOptions{})
	if strings.Contains(f.created.Body.String(), "payees") || strings.Contains(f.created.Body.String(), "000111222333") {
		t.Fatalf("a proposal carries no bank details: %s", f.created.Body)
	}
	body := strings.TrimSuffix(runProposalBody("run-2", nil), "}") + `,"creditors":{"` + vendorOne + `":{"name":"Mallory","otherId":"1"}}}`
	wantProblem(t, f.alice.send(f.router, http.MethodPost, "/v1/payables/payment-proposals", body), http.StatusBadRequest, "COM-0001")
	if f.party.callCount() != 0 {
		t.Fatalf("party was called %d times before file generation", f.party.callCount())
	}
}

func TestPaymentRun_GenerateFileRevealsBankDetailsFromPartyWithASignedTokenAndStoresNoPlaintext(t *testing.T) {
	f := newRunFixture(t, fixtureOptions{})
	f.approve()
	runID := f.start()["id"].(string)
	if f.party.callCount() != 0 {
		t.Fatal("run start must not fetch bank details")
	}
	if rec := f.step(f.erin, runID, "generate-file"); rec.Code != http.StatusOK {
		t.Fatalf("generate-file: %d %s", rec.Code, rec.Body)
	}
	file := f.erin.send(f.router, http.MethodGet, runsPath+"/"+runID+"/file", "").Body.String()
	if !strings.Contains(file, "000111222333") || !strings.Contains(file, "000444555666") || !strings.Contains(file, "Acme Supply Co") {
		t.Fatalf("the file must carry the party bank details: %s", file)
	}
	f.party.mu.Lock()
	calls, tokens := append([]string(nil), f.party.calls...), append([]string(nil), f.party.tokens...)
	f.party.mu.Unlock()
	if len(calls) != 2 {
		t.Fatalf("party calls = %v, want one per vendor", calls)
	}
	verifier := tenant.NewServiceVerifier("party", map[string]ed25519.PublicKey{"payables": f.verify})
	for _, token := range tokens {
		claims, err := verifier.Verify(token)
		if err != nil || claims.Aud != "party" || claims.Le != entityA || len(claims.Duties) != 1 || claims.Duties[0] != "party.vendor_bank.reveal" {
			t.Fatalf("claims %+v err %v: the reveal needs a service token carrying only party.vendor_bank.reveal", claims, err)
		}
	}
	for _, view := range []string{
		f.alice.send(f.router, http.MethodGet, "/v1/payables/payment-proposals/"+f.proposal, "").Body.String(),
		f.erin.send(f.router, http.MethodGet, runsPath+"/"+runID, "").Body.String(),
	} {
		if strings.Contains(view, "000111222333") || strings.Contains(view, "Acme Supply Co") {
			t.Fatalf("bank details must not appear in proposal or run reads: %s", view)
		}
	}
}

func TestPaymentRun_PartyOutageIsARetryable502AndTheStepSucceedsOnRetry(t *testing.T) {
	f := newRunFixture(t, fixtureOptions{})
	f.approve()
	runID := f.start()["id"].(string)
	f.party.mu.Lock()
	f.party.failures = 1
	f.party.mu.Unlock()
	rec := f.step(f.erin, runID, "generate-file")
	wantProblem(t, rec, http.StatusBadGateway, "COM-0502")
	if rec.Header().Get("Retry-After") == "" || !strings.Contains(scrape(f.api), "payables_payment_run_party_faults_total 1") {
		t.Fatalf("retry-after %q, metrics:\n%s", rec.Header().Get("Retry-After"), scrape(f.api))
	}
	if got := decode(t, f.erin.send(f.router, http.MethodGet, runsPath+"/"+runID, "")); got["status"] != "RUNNING" || got["fileGeneratedAt"] != nil {
		t.Fatalf("a party outage must leave the run untouched: %v", got)
	}
	if rec := f.step(f.erin, runID, "generate-file"); rec.Code != http.StatusOK {
		t.Fatalf("retry: %d %s", rec.Code, rec.Body)
	}
}

func TestPaymentRun_VendorWithoutAUsableBankAccountIsRefusedWithAPCode(t *testing.T) {
	cases := map[string]struct {
		mutate func(f *fakeParty)
		detail string
	}{
		"unknown vendor": {func(f *fakeParty) { delete(f.accounts, vendorTwo) }, "not in the party vendor master"},
		"no accounts":    {func(f *fakeParty) { f.accounts[vendorTwo] = nil }, "has no USD bank account"},
		"other currency": {func(f *fakeParty) {
			f.accounts[vendorTwo] = []map[string]any{partyAccount("Widgets", "123456789", "011000015", "EUR")}
		}, "has no USD bank account"},
		"ambiguous": {func(f *fakeParty) {
			f.accounts[vendorTwo] = append(f.accounts[vendorTwo], partyAccount("Widgets 2", "999888777", "011000015", "USD"))
		}, "ambiguous"},
		"no account holder": {func(f *fakeParty) {
			f.accounts[vendorTwo] = []map[string]any{partyAccount("", "123456789", "011000015", "USD")}
		}, "no account holder"},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			f := newRunFixture(t, fixtureOptions{})
			f.approve()
			runID := f.start()["id"].(string)
			tc.mutate(f.party)
			body := wantProblem(t, f.step(f.erin, runID, "generate-file"), http.StatusUnprocessableEntity, "AP-RUN-017")
			if !strings.Contains(body["detail"].(string), tc.detail) || !strings.Contains(body["detail"].(string), vendorTwo) {
				t.Fatalf("detail %q, want it to name the vendor and %q", body["detail"], tc.detail)
			}
			if got := decode(t, f.erin.send(f.router, http.MethodGet, runsPath+"/"+runID, "")); got["fileGeneratedAt"] != nil {
				t.Fatalf("no file may be saved: %v", got)
			}
		})
	}
}

func TestPaymentRun_PartyNotConfiguredRefusesStartAndFileGeneration(t *testing.T) {
	f := newRunFixture(t, fixtureOptions{noParty: true})
	f.approve()
	wantProblem(t, f.erin.send(f.router, http.MethodPost, runsPath, f.startBody()), http.StatusUnprocessableEntity, "AP-RUN-013")
}

func TestPaymentRun_StartRefusesAnythingTheRequestCouldRedirect(t *testing.T) {
	f := newRunFixture(t, fixtureOptions{})
	f.approve()
	for name, extra := range map[string]string{
		"ledger":     `,"ledgerId":"` + runLedgerID + `"`,
		"accounts":   `,"bankAccount":"9999","controlAccount":"2000","discountAccount":"4900"`,
		"creditors":  `,"file":{"creditors":{"` + vendorOne + `":{"name":"Mallory","otherId":"1"}}}`,
		"bank file":  `,"file":{"debtorBic":"EVILUS33"}`,
		"top-level":  `,"creditors":{}`,
		"profile id": `,"profile":"x"`,
	} {
		body := strings.TrimSuffix(f.startBody(), "}") + extra + "}"
		wantProblem(t, f.erin.send(f.router, http.MethodPost, runsPath, body), http.StatusBadRequest, "COM-0001")
		if f.proposalStatus() != "APPROVED" {
			t.Fatalf("%s: a refused start moved the proposal to %s", name, f.proposalStatus())
		}
	}
	runID := f.start()["id"].(string)
	f.step(f.erin, runID, "generate-file")
	file := f.erin.send(f.router, http.MethodGet, runsPath+"/"+runID+"/file", "").Body.String()
	if !strings.Contains(file, "000111222333") || strings.Contains(file, "Mallory") || strings.Contains(file, "EVILUS33") {
		t.Fatalf("the file must carry the approved bank details and the server profile only: %s", file)
	}
}

func TestPaymentRun_ExecutorMustBeNeitherCreatorNorApprover(t *testing.T) {
	f := newRunFixture(t, fixtureOptions{})
	f.approve()
	executor := "payment-run.execute,payment-run.read"
	for _, user := range []string{"alice", "bob"} {
		c := caller{duties: executor, user: user, entity: entityA}
		wantProblem(t, c.send(f.router, http.MethodPost, runsPath, f.startBody()), http.StatusForbidden, "AP-PRP-003")
	}
	if f.proposalStatus() != "APPROVED" {
		t.Fatalf("proposal = %s", f.proposalStatus())
	}
	if f.start()["status"] != "RUNNING" {
		t.Fatal("a third user starts the run")
	}
}

func TestPaymentRun_NeedsAPostingProfileAndFieldKeys(t *testing.T) {
	bare := newRunFixture(t, fixtureOptions{noProfile: true})
	bare.approve()
	wantProblem(t, bare.erin.send(bare.router, http.MethodPost, runsPath, bare.startBody()), http.StatusUnprocessableEntity, "AP-RUN-013")
	if bare.proposalStatus() != "APPROVED" {
		t.Fatalf("proposal = %s", bare.proposalStatus())
	}
}

func TestPaymentRun_ForeignCurrencyGroupsAreRefusedUntilRealizedFXExists(t *testing.T) {
	f := newRunFixture(t, fixtureOptions{foreign: true})
	f.approve()
	rec := f.erin.send(f.router, http.MethodPost, runsPath, f.startBody())
	body := wantProblem(t, rec, http.StatusUnprocessableEntity, "AP-RUN-013")
	if !strings.Contains(body["detail"].(string), "realized FX") || f.proposalStatus() != "APPROVED" {
		t.Fatalf("detail %v proposal %s", body["detail"], f.proposalStatus())
	}
}

func TestPaymentRun_RunDateMustNotHavePassedAtStart(t *testing.T) {
	f := newRunFixture(t, fixtureOptions{})
	f.approve()
	*f.clock = fixedNow().AddDate(0, 0, 1)
	wantProblem(t, f.erin.send(f.router, http.MethodPost, runsPath, f.startBody()), http.StatusConflict, "AP-PRP-004")
	if f.proposalStatus() != "APPROVED" {
		t.Fatalf("a refused start moved the proposal to %s", f.proposalStatus())
	}
	*f.clock = fixedNow()
	runID := f.start()["id"].(string)
	*f.clock = fixedNow().AddDate(0, 0, 1)
	if rec := f.erin.send(f.router, http.MethodPost, runsPath, f.startBody()); rec.Code != http.StatusOK || decode(t, rec)["id"] != runID {
		t.Fatalf("replaying an existing run stays allowed: %d %s", rec.Code, rec.Body)
	}
}

func TestPaymentRun_LedgerAcceptedButReplyLostPostsOnce(t *testing.T) {
	f := newRunFixture(t, fixtureOptions{})
	f.approve()
	runID := f.start()["id"].(string)
	if rec := f.step(f.erin, runID, "generate-file"); rec.Code != http.StatusOK {
		t.Fatalf("generate-file: %d %s", rec.Code, rec.Body)
	}
	f.ledger.dropReplies = 1
	wantProblem(t, f.step(f.erin, runID, "post-journal"), http.StatusBadGateway, "COM-0502")
	wantProblem(t, f.erin.send(f.router, http.MethodPost, runsPath+"/"+runID+":fail", `{"reason":"the ledger errored"}`), http.StatusConflict, "AP-RUN-011")
	if rec := f.step(f.erin, runID, "post-journal"); rec.Code != http.StatusOK || decode(t, rec)["journalEntryId"] != "entry-1" {
		t.Fatalf("retried post-journal: %d %s", rec.Code, rec.Body)
	}
	if f.ledger.posted() != 1 || len(f.ledger.keys) != 2 {
		t.Fatalf("ledger entries = %d from %d calls, want 1 entry from 2 calls with one idempotency key", f.ledger.posted(), len(f.ledger.keys))
	}
	if !strings.Contains(scrape(f.api), "payables_payment_run_ledger_faults_total 1") {
		t.Fatalf("ledger fault not counted:\n%s", scrape(f.api))
	}
}

func TestPaymentRun_SlowFirstPostThenInProgressRetryNeverFailsTheRun(t *testing.T) {
	f := newRunFixture(t, fixtureOptions{})
	f.approve()
	runID := f.start()["id"].(string)
	f.step(f.erin, runID, "generate-file")
	f.ledger.gate = make(chan struct{})

	firstDone := make(chan *httptest.ResponseRecorder, 1)
	go func() { firstDone <- f.step(f.erin, runID, "post-journal") }()
	<-f.ledger.entered

	retry := f.step(f.erin, runID, "post-journal")
	if retry.Code != http.StatusBadGateway || retry.Header().Get("Retry-After") != "1" {
		t.Fatalf("a 409 in-progress from the ledger must be retryable, got %d %s", retry.Code, retry.Body)
	}
	wantProblem(t, f.erin.send(f.router, http.MethodPost, runsPath+"/"+runID+":fail", `{"reason":"retry saw 409"}`), http.StatusConflict, "AP-RUN-011")
	if got := decode(t, f.erin.send(f.router, http.MethodGet, runsPath+"/"+runID, "")); got["status"] != "RUNNING" || got["journalRequestedAt"] == nil {
		t.Fatalf("run = %v, want RUNNING with the journal requested", got)
	}
	if f.proposalStatus() != "EXECUTING" {
		t.Fatalf("proposal = %s: the lines must stay reserved while the first post is in flight", f.proposalStatus())
	}
	again := f.alice.send(f.router, http.MethodPost, "/v1/payables/payment-proposals", runProposalBody("run-2", nil))
	wantProblem(t, again, http.StatusUnprocessableEntity, "AP-PRP-005")

	close(f.ledger.gate)
	first := <-firstDone
	if first.Code != http.StatusOK || decode(t, first)["journalEntryId"] != "entry-1" {
		t.Fatalf("first post: %d %s", first.Code, first.Body)
	}
	if f.step(f.erin, runID, "settle").Code != http.StatusOK || f.step(f.erin, runID, "complete").Code != http.StatusOK || f.ledger.posted() != 1 {
		t.Fatalf("the run must finish with one ledger entry, got %d", f.ledger.posted())
	}
}

func TestPaymentRun_LedgerRejectionIsTerminalAndFailReleasesItems(t *testing.T) {
	f := newRunFixture(t, fixtureOptions{})
	f.approve()
	runID := f.start()["id"].(string)
	f.step(f.erin, runID, "generate-file")
	f.ledger.rejectWith = http.StatusUnprocessableEntity
	wantProblem(t, f.step(f.erin, runID, "post-journal"), http.StatusUnprocessableEntity, "AP-RUN-014")

	wantProblem(t, f.erin.send(f.router, http.MethodPost, runsPath+"/"+runID+":fail", `{"reason":""}`), http.StatusBadRequest, "COM-0001")
	failed := f.erin.send(f.router, http.MethodPost, runsPath+"/"+runID+":fail", `{"reason":"ledger rejected the journal"}`)
	if got := decode(t, failed); failed.Code != http.StatusOK || got["status"] != "FAILED" || got["failureReason"] != "ledger rejected the journal" {
		t.Fatalf("fail: %d %s", failed.Code, failed.Body)
	}
	if f.proposalStatus() != "FAILED" {
		t.Fatalf("proposal = %s", f.proposalStatus())
	}
	wantProblem(t, f.step(f.erin, runID, "generate-file"), http.StatusConflict, "AP-RUN-011")
	wantProblem(t, f.step(f.erin, runID, "post-journal"), http.StatusConflict, "AP-RUN-011")
	again := f.alice.send(f.router, http.MethodPost, "/v1/payables/payment-proposals", runProposalBody("run-2", nil))
	if got := decode(t, again); again.Code != http.StatusCreated || len(got["groups"].([]any)) != 2 {
		t.Fatalf("a new proposal must pick the released items up again: %d %s", again.Code, again.Body)
	}
}

func TestPaymentRun_RejectionAdoptsTheRunsOwnEntryButNeverAForeignOne(t *testing.T) {
	f := newRunFixture(t, fixtureOptions{})
	f.approve()
	runID := f.start()["id"].(string)
	f.step(f.erin, runID, "generate-file")

	f.ledger.dropReplies = 1
	wantProblem(t, f.step(f.erin, runID, "post-journal"), http.StatusBadGateway, "COM-0502")
	f.ledger.rejectWith = http.StatusUnprocessableEntity

	f.ledger.lookupFails = true
	wantProblem(t, f.step(f.erin, runID, "post-journal"), http.StatusBadGateway, "COM-0502")
	if got := decode(t, f.erin.send(f.router, http.MethodGet, runsPath+"/"+runID, "")); got["journalRejectedAt"] != nil {
		t.Fatalf("a failed lookup must not record a rejection: %v", got)
	}

	f.ledger.lookupFails = false
	rec := f.step(f.erin, runID, "post-journal")
	if got := decode(t, rec); rec.Code != http.StatusOK || got["journalEntryId"] != "entry-1" || got["journalRejectedAt"] != nil {
		t.Fatalf("the entry this run posted earlier is the journal: %d %s", rec.Code, rec.Body)
	}
	if len(f.ledger.lookups) != 2 || f.ledger.lookups[1] != runID {
		t.Fatalf("lookups = %v, want the run id as document id", f.ledger.lookups)
	}
	if f.step(f.erin, runID, "settle").Code != http.StatusOK || f.step(f.erin, runID, "complete").Code != http.StatusOK {
		t.Fatal("the run must finish after adopting its entry")
	}
}

func TestPaymentRun_RejectionIgnoresAForeignEntrySharingTheRunsDocumentID(t *testing.T) {
	f := newRunFixture(t, fixtureOptions{})
	f.approve()
	runID := f.start()["id"].(string)
	f.step(f.erin, runID, "generate-file")
	f.ledger.rejectWith = http.StatusUnprocessableEntity
	f.ledger.foreignEntry = "entry-by-a-user"
	wantProblem(t, f.step(f.erin, runID, "post-journal"), http.StatusUnprocessableEntity, "AP-RUN-014")
	if got := decode(t, f.erin.send(f.router, http.MethodGet, runsPath+"/"+runID, "")); got["journalRejectedAt"] == nil || got["journalEntryId"] != nil {
		t.Fatalf("a foreign entry must not be adopted: %v", got)
	}
}

func TestPaymentRun_PostedJournalCannotBeFailed(t *testing.T) {
	f := newRunFixture(t, fixtureOptions{})
	f.approve()
	runID := f.start()["id"].(string)
	f.step(f.erin, runID, "generate-file")
	f.step(f.erin, runID, "post-journal")
	wantProblem(t, f.erin.send(f.router, http.MethodPost, runsPath+"/"+runID+":fail", `{"reason":"too late"}`), http.StatusConflict, "AP-RUN-011")
	wantProblem(t, f.step(f.erin, runID, "complete"), http.StatusConflict, "AP-RUN-012")
}

func TestPaymentRun_StepsNeedTheirPredecessor(t *testing.T) {
	f := newRunFixture(t, fixtureOptions{})
	f.approve()
	runID := f.start()["id"].(string)
	wantProblem(t, f.step(f.erin, runID, "post-journal"), http.StatusConflict, "AP-RUN-012")
	wantProblem(t, f.step(f.erin, runID, "settle"), http.StatusConflict, "AP-RUN-012")
	wantProblem(t, f.step(f.erin, runID, "complete"), http.StatusConflict, "AP-RUN-012")
	if f.ledger.posted() != 0 {
		t.Fatal("an out-of-order journal step must not reach the ledger")
	}
}

func TestPaymentRun_HoldPlacedAfterStartIsCaughtBeforeTheFileIsBuilt(t *testing.T) {
	f := newRunFixture(t, fixtureOptions{})
	f.approve()
	runID := f.start()["id"].(string)
	if _, err := f.pg.SetPaymentHold(f.ctx(), f.itemA.ID, true, ""); err != nil {
		t.Fatal(err)
	}
	body := wantProblem(t, f.step(f.erin, runID, "generate-file"), http.StatusConflict, "AP-RUN-015")
	if !strings.Contains(body["detail"].(string), f.itemA.ID) {
		t.Fatalf("detail = %v", body["detail"])
	}
	if got := decode(t, f.erin.send(f.router, http.MethodGet, runsPath+"/"+runID, "")); got["fileGeneratedAt"] != nil {
		t.Fatalf("no file may exist for a held item: %v", got)
	}
	if rec := f.erin.send(f.router, http.MethodPost, runsPath+"/"+runID+":fail", `{"reason":"item held"}`); rec.Code != http.StatusOK || f.proposalStatus() != "FAILED" {
		t.Fatalf("the workflow's compensation must release the lines: %d %s", rec.Code, rec.Body)
	}
}

func TestPaymentRun_ItemChangedAfterTheJournalIsStuckAndAlerted(t *testing.T) {
	f := newRunFixture(t, fixtureOptions{})
	f.approve()
	runID := f.start()["id"].(string)
	f.step(f.erin, runID, "generate-file")
	f.step(f.erin, runID, "post-journal")
	if _, err := f.pg.Settle(f.ctx(), f.itemA.ID, f.itemA.Version, decimal.RequireFromString("1.00"), "other"); err != nil {
		t.Fatal(err)
	}
	wantProblem(t, f.step(f.erin, runID, "settle"), http.StatusConflict, "AP-RUN-015")
	if !strings.Contains(scrape(f.api), "payables_payment_run_stuck_total 1") {
		t.Fatalf("a stuck run must raise the alert metric:\n%s", scrape(f.api))
	}
	if got := decode(t, f.erin.send(f.router, http.MethodGet, runsPath+"/"+runID, "")); got["status"] != "RUNNING" || got["settledAt"] != nil {
		t.Fatalf("run = %v: nothing may be settled", got)
	}
}

func TestPaymentRun_GeneratedFileFailingXSDOrBankRulesIsTerminal(t *testing.T) {
	invalid := newRunFixture(t, fixtureOptions{validator: fakeValidator{found: []domain.XSDError{{Path: "InstdAmt", Reason: "bad amount"}}}})
	invalid.approve()
	runID := invalid.start()["id"].(string)
	wantProblem(t, invalid.step(invalid.erin, runID, "generate-file"), http.StatusUnprocessableEntity, "AP-FIL-002")
}

func TestPaymentRun_DutiesFileAuditAndLegalEntityScope(t *testing.T) {
	f := newRunFixture(t, fixtureOptions{})
	f.approve()
	runID := f.start()["id"].(string)
	f.step(f.erin, runID, "generate-file")
	reader := caller{duties: "payment-run.read", user: "rita", entity: entityA}
	executor := caller{duties: "payment-run.execute", user: "erin", entity: entityA}
	approver := caller{duties: "payment-proposal.approve", user: "bob", entity: entityA}
	outsider := caller{duties: runDuties, user: "mallory", entity: entityB}

	for _, action := range []string{"generate-file", "post-journal", "settle", "complete", "fail"} {
		wantProblem(t, f.step(reader, runID, action), http.StatusForbidden, "AUTH-0003")
		wantProblem(t, f.step(approver, runID, action), http.StatusForbidden, "AUTH-0003")
		if action != "fail" {
			wantProblem(t, f.step(outsider, runID, action), http.StatusNotFound, "COM-0404")
		}
	}
	wantProblem(t, approver.send(f.router, http.MethodPost, runsPath, f.startBody()), http.StatusForbidden, "AUTH-0003")
	wantProblem(t, reader.send(f.router, http.MethodPost, runsPath, f.startBody()), http.StatusForbidden, "AUTH-0003")
	wantProblem(t, executor.send(f.router, http.MethodGet, runsPath+"/"+runID, ""), http.StatusForbidden, "AUTH-0003")
	wantProblem(t, reader.send(f.router, http.MethodGet, runsPath+"/"+runID+"/file", ""), http.StatusForbidden, "AUTH-0003")
	if rec := executor.send(f.router, http.MethodGet, runsPath+"/"+runID+"/file", ""); rec.Code != http.StatusOK {
		t.Fatalf("the file read belongs to the execute duty: %d %s", rec.Code, rec.Body)
	}
	if !strings.Contains(scrape(f.api), "payables_payment_file_reads_total 1") {
		t.Fatalf("the file read is not audited:\n%s", scrape(f.api))
	}
	if rec := reader.send(f.router, http.MethodGet, runsPath+"/"+runID, ""); rec.Code != http.StatusOK || strings.Contains(rec.Body.String(), "000111222333") {
		t.Fatalf("reader get: %d %s", rec.Code, rec.Body)
	}
	wantProblem(t, outsider.send(f.router, http.MethodGet, runsPath+"/"+runID, ""), http.StatusNotFound, "COM-0404")
	wantProblem(t, outsider.send(f.router, http.MethodGet, runsPath+"/"+runID+"/file", ""), http.StatusNotFound, "COM-0404")
	wantProblem(t, reader.send(f.router, http.MethodGet, runsPath+"/not-a-uuid", ""), http.StatusBadRequest, "COM-0001")
	noScope := caller{duties: runDuties, user: "erin", noScope: true}
	wantProblem(t, noScope.send(f.router, http.MethodPost, runsPath, `{"proposalId":"`+f.proposal+`"}`), http.StatusBadRequest, "COM-0001")
	wantProblem(t, f.erin.send(f.router, http.MethodPost, runsPath, `{"legalEntityId":"`+entityB+`","proposalId":"`+f.proposal+`"}`), http.StatusForbidden, "AUTH-0003")
}

func TestPaymentRun_OpenAPIDocumentsEveryRunRoute(t *testing.T) {
	f := newRunFixture(t, fixtureOptions{})
	spec, err := os.ReadFile("../../api/openapi.yaml")
	if err != nil {
		t.Fatal(err)
	}
	seen := 0
	err = chi.Walk(f.router.(chi.Routes), func(_, route string, _ http.Handler, _ ...func(http.Handler) http.Handler) error {
		if strings.Contains(route, "payment-runs") {
			seen++
			if !strings.Contains(string(spec), "\n  "+route+":") {
				t.Errorf("openapi.yaml does not document %s", route)
			}
		}
		return nil
	})
	if err != nil || seen != 8 {
		t.Fatalf("walked %d run routes, err=%v, want 8", seen, err)
	}
}

func TestLoadPostingProfiles(t *testing.T) {
	get := func(m map[string]string) func(string) string { return func(k string) string { return m[k] } }
	if p, err := api.LoadPostingProfiles(get(nil)); p != nil || err != nil {
		t.Fatalf("unset = %v %v", p, err)
	}
	good := `{"` + strings.ToUpper(entityA) + `":{"ledgerId":"` + runLedgerID + `","bankAccount":"1010","controlAccount":"2000","file":{"debtorBic":"CHASUS33"}}}`
	p, err := api.LoadPostingProfiles(get(map[string]string{api.EnvPostingProfiles: good}))
	if err != nil {
		t.Fatal(err)
	}
	if profile, ok := p.For(entityA); !ok || profile.File.DebtorBIC != "CHASUS33" {
		t.Fatalf("profile = %+v ok=%v", profile, ok)
	}
	file := t.TempDir() + "/profiles.json"
	if err := os.WriteFile(file, []byte(good), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := api.LoadPostingProfiles(get(map[string]string{api.EnvPostingProfilesFile: file})); err != nil {
		t.Fatalf("mounted file: %v", err)
	}
	for name, raw := range map[string]string{
		"not json":           `{`,
		"bad entity":         `{"x":{"ledgerId":"` + runLedgerID + `","bankAccount":"1","controlAccount":"2"}}`,
		"no ledger":          `{"` + entityA + `":{"bankAccount":"1","controlAccount":"2"}}`,
		"no bank account":    `{"` + entityA + `":{"ledgerId":"` + runLedgerID + `","controlAccount":"2"}}`,
		"unknown field":      `{"` + entityA + `":{"ledgerId":"` + runLedgerID + `","bankAccount":"1","controlAccount":"2","extra":1}}`,
		"unknown file field": `{"` + entityA + `":{"ledgerId":"` + runLedgerID + `","bankAccount":"1","controlAccount":"2","file":{"creditors":{}}}}`,
	} {
		if _, err := api.LoadPostingProfiles(get(map[string]string{api.EnvPostingProfiles: raw})); err == nil {
			t.Errorf("%s must fail", name)
		}
	}
	envOnly := get(map[string]string{api.EnvPostingProfiles: `{"x":{}}`, api.EnvPostingProfilesFile: file})
	if p, err := api.LoadPostingProfiles(envOnly); err != nil || len(p) != 1 {
		t.Fatalf("the mounted file must win over the environment: %v %v", p, err)
	}
	if _, err := api.LoadPostingProfiles(get(map[string]string{api.EnvPostingProfilesFile: t.TempDir() + "/missing"})); err == nil {
		t.Error("a missing file must fail")
	}
}

func TestPaymentRun_OnlyDefinitiveLedgerRejectionsAreRecordedAndNothingElseReleasesLines(t *testing.T) {
	cases := map[string]struct {
		status int
		code   string
	}{
		"401": {http.StatusUnauthorized, ""}, "403": {http.StatusForbidden, ""}, "404": {http.StatusNotFound, ""},
		"408": {http.StatusRequestTimeout, ""}, "429": {http.StatusTooManyRequests, ""}, "500": {http.StatusInternalServerError, ""},
		"409 conflict": {http.StatusConflict, "LEDGER_IDEMPOTENCY_CONFLICT"}, "409 in progress": {http.StatusConflict, "LEDGER_IDEMPOTENCY_IN_PROGRESS"},
	}
	for name, tc := range cases {
		f := newRunFixture(t, fixtureOptions{})
		f.approve()
		runID := f.start()["id"].(string)
		f.step(f.erin, runID, "generate-file")
		f.ledger.dropReplies = 1
		wantProblem(t, f.step(f.erin, runID, "post-journal"), http.StatusBadGateway, "COM-0502")
		f.ledger.rejectWith, f.ledger.rejectCode = tc.status, tc.code
		wantProblem(t, f.step(f.erin, runID, "post-journal"), http.StatusBadGateway, "COM-0502")
		got := decode(t, f.erin.send(f.router, http.MethodGet, runsPath+"/"+runID, ""))
		if got["journalRejectedAt"] != nil || got["status"] != "RUNNING" {
			t.Errorf("%s: a non-definitive answer was recorded as a rejection: %v", name, got)
		}
		wantProblem(t, f.erin.send(f.router, http.MethodPost, runsPath+"/"+runID+":fail", `{"reason":"must be refused"}`), http.StatusConflict, "AP-RUN-011")
		if f.proposalStatus() != "EXECUTING" {
			t.Errorf("%s: proposal = %s, the lines must stay reserved", name, f.proposalStatus())
		}
		f.ledger.rejectWith, f.ledger.rejectCode = 0, ""
		if rec := f.step(f.erin, runID, "post-journal"); rec.Code != http.StatusOK || f.ledger.posted() != 1 {
			t.Errorf("%s: the next retry must find the committed entry: %d %s entries=%d", name, rec.Code, rec.Body, f.ledger.posted())
		}
	}
}

func TestPaymentRun_RejectedJournalCannotBeRequestedAgain(t *testing.T) {
	f := newRunFixture(t, fixtureOptions{})
	f.approve()
	runID := f.start()["id"].(string)
	f.step(f.erin, runID, "generate-file")
	f.ledger.rejectWith = http.StatusBadRequest
	wantProblem(t, f.step(f.erin, runID, "post-journal"), http.StatusUnprocessableEntity, "AP-RUN-014")
	f.ledger.rejectWith = 0
	wantProblem(t, f.step(f.erin, runID, "post-journal"), http.StatusConflict, "AP-RUN-011")
	if f.ledger.posted() != 0 {
		t.Fatal("nothing may be posted after a definitive rejection")
	}
}

func TestPaymentRun_EveryStepAndTheFileReadRefuseTheCreatorAndApprover(t *testing.T) {
	f := newRunFixture(t, fixtureOptions{})
	f.approve()
	runID := f.start()["id"].(string)
	f.step(f.erin, runID, "generate-file")
	for _, user := range []string{"alice", "BOB", " bob "} {
		c := caller{duties: "payment-run.execute,payment-run.read", user: user, entity: entityA}
		for _, action := range []string{"generate-file", "post-journal", "settle", "complete", "fail"} {
			wantProblem(t, f.step(c, runID, action), http.StatusForbidden, "AP-PRP-003")
		}
		wantProblem(t, c.send(f.router, http.MethodGet, runsPath+"/"+runID+"/file", ""), http.StatusForbidden, "AP-PRP-003")
	}
	if f.ledger.posted() != 0 {
		t.Fatal("a refused actor must not reach the ledger")
	}
}

func TestPaymentRun_FileUsesTheProfileSnapshotTakenAtStart(t *testing.T) {
	f := newRunFixture(t, fixtureOptions{})
	f.approve()
	runID := f.start()["id"].(string)
	changed := profiles()
	profile := changed[entityA]
	profile.File.DebtorBIC = "EVILUS33"
	profile.BankAccount = "9999"
	changed[entityA] = profile
	f.api.WithPaymentRuns(f.pg, nil, changed).WithVendorBanks(party.New(f.partyURL, nil).WithSigner(f.signer))
	f.step(f.erin, runID, "generate-file")
	file := f.erin.send(f.router, http.MethodGet, runsPath+"/"+runID+"/file", "").Body.String()
	if !strings.Contains(file, "CHASUS33") || strings.Contains(file, "EVILUS33") {
		t.Fatalf("the file must use the snapshot: %s", file)
	}
	if got := decode(t, f.erin.send(f.router, http.MethodGet, runsPath+"/"+runID, "")); got["bankAccount"] != "1010" {
		t.Fatalf("the run keeps its snapshot accounts: %v", got)
	}
}

func TestPaymentRun_StartReplayAppliesTheExecutorCheckBeforeReturningTheRun(t *testing.T) {
	f := newRunFixture(t, fixtureOptions{})
	f.approve()
	runID := f.start()["id"].(string)
	replay := f.erin.send(f.router, http.MethodPost, runsPath, f.startBody())
	if replay.Code != http.StatusOK || replay.Header().Get("Idempotent-Replayed") != "true" {
		t.Fatalf("the executor's own replay = %d %s", replay.Code, replay.Body)
	}
	executor := "payment-run.execute,payment-run.read"
	for _, user := range []string{"alice", "BOB", " bob "} {
		rec := caller{duties: executor, user: user, entity: entityA}.send(f.router, http.MethodPost, runsPath, f.startBody())
		wantProblem(t, rec, http.StatusForbidden, "AP-PRP-003")
		if strings.Contains(rec.Body.String(), runID) || strings.Contains(rec.Body.String(), "bankAccount") {
			t.Fatalf("%q: a refused replay must not describe the run: %s", user, rec.Body)
		}
	}
}

func TestPaymentRun_StartNeedsAnIdentifiedUserWithAnApplicationCodeNotAnAuthFault(t *testing.T) {
	f := newRunFixture(t, fixtureOptions{})
	f.approve()
	anonymous := caller{duties: runDuties, entity: entityA}
	wantProblem(t, anonymous.send(f.router, http.MethodPost, runsPath, f.startBody()), http.StatusForbidden, "AP-RUN-016")
	if f.proposalStatus() != "APPROVED" {
		t.Fatalf("proposal = %s", f.proposalStatus())
	}
}

func TestPaymentRun_MissingFieldKeysAre503NotAnInternalErrorAndNeverBlockTheExecutorCheck(t *testing.T) {
	f := newRunFixture(t, fixtureOptions{})
	f.approve()
	runID := f.start()["id"].(string)
	f.pg.WithFieldCrypt(nil)
	if rec := f.bob.send(f.router, http.MethodGet, "/v1/payables/payment-proposals/"+f.proposal, ""); rec.Code != http.StatusOK {
		t.Fatalf("a proposal read needs no field keys: %d %s", rec.Code, rec.Body)
	}
	executor := "payment-run.execute,payment-run.read"
	wantProblem(t, f.step(caller{duties: executor, user: "alice", entity: entityA}, runID, "settle"), http.StatusForbidden, "AP-PRP-003")
	wantProblem(t, f.step(f.erin, runID, "settle"), http.StatusConflict, "AP-RUN-012")
	wantProblem(t, f.step(f.erin, runID, "generate-file"), http.StatusServiceUnavailable, "COM-0503")
}

func TestPaymentRun_ClosedLedgerPeriodRejectsTheJournalAndRecordsNothingAsPosted(t *testing.T) {
	f := newRunFixture(t, fixtureOptions{})
	f.approve()
	runID := f.start()["id"].(string)
	f.step(f.erin, runID, "generate-file")
	f.ledger.rejectWith, f.ledger.rejectCode = http.StatusUnprocessableEntity, "LEDGER_PERIOD_CLOSED"
	wantProblem(t, f.step(f.erin, runID, "post-journal"), http.StatusUnprocessableEntity, "AP-RUN-014")
	got := decode(t, f.erin.send(f.router, http.MethodGet, runsPath+"/"+runID, ""))
	if got["journalPostedAt"] != nil || got["journalEntryId"] != nil || got["journalRejectedAt"] == nil {
		t.Fatalf("a closed period must leave the journal rejected and unposted: %v", got)
	}
	if f.ledger.posted() != 0 {
		t.Fatalf("the ledger holds %d entries, want none", f.ledger.posted())
	}
	failed := f.erin.send(f.router, http.MethodPost, runsPath+"/"+runID+":fail", `{"reason":"period closed"}`)
	if got := decode(t, failed); failed.Code != http.StatusOK || got["status"] != "FAILED" {
		t.Fatalf("fail: %d %s", failed.Code, failed.Body)
	}
}
