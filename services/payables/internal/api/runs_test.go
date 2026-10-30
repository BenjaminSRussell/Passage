package api_test

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"testing"

	"github.com/go-chi/chi/v5"
	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/platform/dbx"
	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/platform/tenant"
	"github.com/BenjaminSRussell/passage/platform/testkit"
	"github.com/BenjaminSRussell/passage/services/payables"
	"github.com/BenjaminSRussell/passage/services/payables/internal/api"
	"github.com/BenjaminSRussell/passage/services/payables/internal/domain"
	"github.com/BenjaminSRussell/passage/services/payables/internal/fx"
	"github.com/BenjaminSRussell/passage/services/payables/internal/ledger"
	"github.com/BenjaminSRussell/passage/services/payables/internal/store"
	"github.com/BenjaminSRussell/passage/services/payables/migrations"
)

const (
	runLedgerID = "00000000-0000-0000-0000-0000000000b1"
	runsPath    = "/v1/payables/payment-runs"
	runDuties   = "payment-run.execute,payment-run.read"
	approveAll  = "open-item.read,payment-proposal.write,payment-proposal.read,payment-proposal.approve"
)

type fakeJournalLedger struct {
	mu          sync.Mutex
	entries     map[string]string
	bodies      []map[string]any
	keys        []string
	rejectWith  int
	dropReplies int
	entity      string
}

func (f *fakeJournalLedger) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if r.Method == http.MethodGet {
		_, _ = fmt.Fprintf(w, `{"legalEntityId":%q}`, f.entity)
		return
	}
	if f.rejectWith != 0 {
		w.WriteHeader(f.rejectWith)
		return
	}
	key := r.Header.Get("Idempotency-Key")
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

type runFixture struct {
	t        *testing.T
	router   http.Handler
	api      *api.API
	ledger   *fakeJournalLedger
	pg       *store.Postgres
	proposal string
	erin     caller
	alice    caller
	bob      caller
	itemA    store.OpenItem
}

func bankDetails(extra map[string]any) map[string]any {
	file := map[string]any{
		"debtor":        map[string]any{"name": "Passage Holding Inc", "address": map[string]any{"streetName": "1 Market Street", "postCode": "94105", "townName": "San Francisco", "country": "US"}},
		"debtorAccount": map[string]any{"otherId": "000123456789"},
		"debtorBic":     "CHASUS33",
		"profile":       map[string]any{"variant": "US", "overrides": map[string]any{"charset": "ascii", "initiating_party_id": "PASSAGEUS01"}},
		"creditors": map[string]any{
			vendorOne: map[string]any{"name": "Acme Supply Co", "address": map[string]any{"streetName": "100 Main Street", "postCode": "10001", "townName": "New York", "country": "US"}, "otherId": "000111222333", "routingNumber": "021000021"},
			vendorTwo: map[string]any{"name": "Example Widgets LLC", "address": map[string]any{"streetName": "200 Main Street", "postCode": "10001", "townName": "New York", "country": "US"}, "otherId": "000444555666", "routingNumber": "011000015"},
		},
	}
	for k, v := range extra {
		file[k] = v
	}
	return file
}

func (f *runFixture) startBody(mutate func(map[string]any)) string {
	body := map[string]any{"legalEntityId": entityA, "proposalId": f.proposal, "ledgerId": runLedgerID, "bankAccount": "1010", "discountAccount": "4900", "file": bankDetails(nil)}
	if mutate != nil {
		mutate(body)
	}
	raw, _ := json.Marshal(body)
	return string(raw)
}

func (f *runFixture) step(c caller, runID, action string) *httptest.ResponseRecorder {
	f.t.Helper()
	return c.send(f.router, http.MethodPost, runsPath+"/"+runID+":"+action, "")
}

func (f *runFixture) start(body string) map[string]any {
	f.t.Helper()
	rec := f.erin.send(f.router, http.MethodPost, runsPath, body)
	if rec.Code != http.StatusCreated {
		f.t.Fatalf("start: %d %s", rec.Code, rec.Body)
	}
	return decode(f.t, rec)
}

func newRunFixture(t *testing.T, validator api.FileValidator) *runFixture {
	t.Helper()
	admin, app := testkit.DBWithApp(t)
	if err := dbx.Migrate(context.Background(), admin, migrations.FS); err != nil {
		t.Fatal(err)
	}
	pg := store.New(app)
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
	seed(itemThree, vendorTwo, "2026-01-08", "EUR", "200.00", "0")

	fl := &fakeJournalLedger{entries: map[string]string{}, entity: entityA}
	srv := httptest.NewServer(fl)
	t.Cleanup(srv.Close)
	a := api.New(app, validator, fixedNow).WithProposals(pg, fx.New(fxServer(t).URL, "", nil)).WithPaymentRuns(pg, ledger.New(srv.URL, nil), "")
	router := httpx.NewRouter()
	a.Register(router)

	f := &runFixture{t: t, router: router, api: a, ledger: fl, pg: pg, itemA: itemA,
		erin: caller{duties: runDuties, user: "erin", entity: entityA}, alice: caller{duties: approveAll, user: "alice", entity: entityA}, bob: caller{duties: approveAll, user: "bob", entity: entityA}}
	created := decode(t, f.alice.send(router, http.MethodPost, "/v1/payables/payment-proposals", proposalBody))
	f.proposal = created["id"].(string)
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
	f := newRunFixture(t, validator)
	f.approve()

	run := f.start(f.startBody(nil))
	runID := run["id"].(string)
	if run["status"] != "RUNNING" || f.proposalStatus() != "EXECUTING" {
		t.Fatalf("run = %v, proposal = %s", run, f.proposalStatus())
	}
	replay := f.erin.send(f.router, http.MethodPost, runsPath, f.startBody(nil))
	if replay.Code != http.StatusOK || replay.Header().Get("Idempotent-Replayed") != "true" || decode(t, replay)["id"] != runID {
		t.Fatalf("replayed start: %d %s", replay.Code, replay.Body)
	}
	wantProblem(t, f.erin.send(f.router, http.MethodPost, runsPath, f.startBody(func(b map[string]any) { b["bankAccount"] = "1020" })), http.StatusConflict, "AP-RUN-010")

	for pass := 0; pass < 2; pass++ {
		gen := f.step(f.erin, runID, "generate-file")
		if gen.Code != http.StatusOK {
			t.Fatalf("generate-file pass %d: %d %s", pass, gen.Code, gen.Body)
		}
	}
	file := f.erin.send(f.router, http.MethodGet, runsPath+"/"+runID+"/file", "")
	if file.Code != http.StatusOK || !strings.HasPrefix(file.Header().Get("Content-Type"), "application/xml") || !strings.Contains(file.Body.String(), "pain.001.001.09") {
		t.Fatalf("file: %d %s", file.Code, file.Body)
	}
	got := decode(t, f.erin.send(f.router, http.MethodGet, runsPath+"/"+runID, ""))
	payments := got["payments"].([]any)
	if got["paymentCount"] != float64(2) || len(payments) != 2 {
		t.Fatalf("run after file = %v", got)
	}
	first, second := payments[0].(map[string]any), payments[1].(map[string]any)
	if first["amount"] != "148" || first["valuedAmount"] != "148" || second["amount"] != "200" || second["valuedAmount"] != "216.5" {
		t.Fatalf("payments = %v, want 148/148 USD (150 less 2 discount) and 200 EUR valued 216.5", payments)
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
	for _, want := range []string{"2000 D 150.00", "2000 D 216.50", "1010 C 364.50", "4900 C 2.00"} {
		if !lines[want] {
			t.Errorf("journal lacks %q: %v", want, lines)
		}
	}
	if f.ledger.keys[0] != "payables:payment-run:"+runID || f.ledger.bodies[0]["postingDate"] != "2026-01-10" {
		t.Errorf("journal key/date = %s / %v", f.ledger.keys[0], f.ledger.bodies[0]["postingDate"])
	}

	for pass := 0; pass < 2; pass++ {
		if rec := f.step(f.erin, runID, "settle"); rec.Code != http.StatusOK {
			t.Fatalf("settle pass %d: %d %s", pass, rec.Code, rec.Body)
		}
	}
	settled, err := f.pg.Get(tenant.With(context.Background(), tenant.Principal{TenantID: testkit.TenantID(1), LegalEntityID: entityA, Duties: testkit.ScopeDuties(entityA)}), f.itemA.ID)
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
	for _, want := range []string{`payables_payment_runs_total{outcome="started"} 1`, `payables_payment_runs_total{outcome="replayed"} 1`, `payables_payment_runs_total{outcome="completed"} 2`} {
		if !strings.Contains(metrics, want) {
			t.Errorf("metrics lack %q:\n%s", want, metrics)
		}
	}
}

func TestPaymentRun_LedgerAcceptedButReplyLostPostsOnce(t *testing.T) {
	f := newRunFixture(t, fakeValidator{})
	f.approve()
	runID := f.start(f.startBody(nil))["id"].(string)
	if rec := f.step(f.erin, runID, "generate-file"); rec.Code != http.StatusOK {
		t.Fatalf("generate-file: %d %s", rec.Code, rec.Body)
	}
	f.ledger.dropReplies = 1
	wantProblem(t, f.step(f.erin, runID, "post-journal"), http.StatusBadGateway, "COM-0502")
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

func TestPaymentRun_LedgerRejectionIsTerminalAndFailReleasesItems(t *testing.T) {
	f := newRunFixture(t, fakeValidator{})
	f.approve()
	runID := f.start(f.startBody(nil))["id"].(string)
	f.step(f.erin, runID, "generate-file")
	f.ledger.rejectWith = http.StatusUnprocessableEntity
	wantProblem(t, f.step(f.erin, runID, "post-journal"), http.StatusUnprocessableEntity, "AP-RUN-014")

	bad := f.erin.send(f.router, http.MethodPost, runsPath+"/"+runID+":fail", `{"reason":""}`)
	wantProblem(t, bad, http.StatusBadRequest, "COM-0001")
	failed := f.erin.send(f.router, http.MethodPost, runsPath+"/"+runID+":fail", `{"reason":"ledger rejected the journal"}`)
	if got := decode(t, failed); failed.Code != http.StatusOK || got["status"] != "FAILED" || got["failureReason"] != "ledger rejected the journal" {
		t.Fatalf("fail: %d %s", failed.Code, failed.Body)
	}
	if f.proposalStatus() != "FAILED" {
		t.Fatalf("proposal = %s", f.proposalStatus())
	}
	wantProblem(t, f.step(f.erin, runID, "generate-file"), http.StatusConflict, "AP-RUN-011")
	wantProblem(t, f.step(f.erin, runID, "post-journal"), http.StatusConflict, "AP-RUN-011")
	again := f.alice.send(f.router, http.MethodPost, "/v1/payables/payment-proposals", strings.Replace(proposalBody, `"run-1"`, `"run-2"`, 1))
	if got := decode(t, again); again.Code != http.StatusCreated || len(got["groups"].([]any)) != 2 {
		t.Fatalf("a new proposal must pick the released items up again: %d %s", again.Code, again.Body)
	}
}

func TestPaymentRun_PostedJournalCannotBeFailed(t *testing.T) {
	f := newRunFixture(t, fakeValidator{})
	f.approve()
	runID := f.start(f.startBody(nil))["id"].(string)
	f.step(f.erin, runID, "generate-file")
	f.step(f.erin, runID, "post-journal")
	wantProblem(t, f.erin.send(f.router, http.MethodPost, runsPath+"/"+runID+":fail", `{"reason":"too late"}`), http.StatusConflict, "AP-RUN-011")
	wantProblem(t, f.step(f.erin, runID, "complete"), http.StatusConflict, "AP-RUN-012")
}

func TestPaymentRun_StepsNeedTheirPredecessor(t *testing.T) {
	f := newRunFixture(t, fakeValidator{})
	f.approve()
	runID := f.start(f.startBody(nil))["id"].(string)
	wantProblem(t, f.step(f.erin, runID, "post-journal"), http.StatusConflict, "AP-RUN-012")
	wantProblem(t, f.step(f.erin, runID, "settle"), http.StatusConflict, "AP-RUN-012")
	wantProblem(t, f.step(f.erin, runID, "complete"), http.StatusConflict, "AP-RUN-012")
	if f.ledger.posted() != 0 {
		t.Fatal("an out-of-order journal step must not reach the ledger")
	}
}

func TestPaymentRun_GeneratedFileFailingXSDOrBankRulesIsTerminal(t *testing.T) {
	invalid := newRunFixture(t, fakeValidator{found: []domain.XSDError{{Path: "InstdAmt", Reason: "bad amount"}}})
	invalid.approve()
	runID := invalid.start(invalid.startBody(nil))["id"].(string)
	wantProblem(t, invalid.step(invalid.erin, runID, "generate-file"), http.StatusUnprocessableEntity, "AP-FIL-002")

	badBank := newRunFixture(t, fakeValidator{})
	badBank.approve()
	body := badBank.startBody(func(b map[string]any) {
		file := b["file"].(map[string]any)
		file["debtorBic"] = "NOTABIC"
	})
	runID = badBank.start(body)["id"].(string)
	rec := badBank.step(badBank.erin, runID, "generate-file")
	if rec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("bad debtor BIC: %d %s", rec.Code, rec.Body)
	}
}

func TestPaymentRun_StartRefusals(t *testing.T) {
	f := newRunFixture(t, fakeValidator{})
	wantProblem(t, f.erin.send(f.router, http.MethodPost, runsPath, f.startBody(nil)), http.StatusConflict, "AP-PRP-002")
	f.approve()

	cases := map[string]struct {
		mutate func(map[string]any)
		status int
		code   string
	}{
		"vendor without bank details": {func(b map[string]any) {
			delete(b["file"].(map[string]any)["creditors"].(map[string]any), vendorTwo)
		}, http.StatusUnprocessableEntity, "AP-RUN-013"},
		"discount without account":  {func(b map[string]any) { b["discountAccount"] = "" }, http.StatusUnprocessableEntity, "AP-RUN-013"},
		"unknown proposal":          {func(b map[string]any) { b["proposalId"] = "00000000-0000-0000-0000-00000000dead" }, http.StatusNotFound, "COM-0404"},
		"bad ledger id":             {func(b map[string]any) { b["ledgerId"] = "x" }, http.StatusBadRequest, "COM-0001"},
		"missing bank account":      {func(b map[string]any) { b["bankAccount"] = "" }, http.StatusBadRequest, "COM-0001"},
		"legal entity out of scope": {func(b map[string]any) { b["legalEntityId"] = entityB }, http.StatusForbidden, "AUTH-0003"},
	}
	for name, tc := range cases {
		wantProblem(t, f.erin.send(f.router, http.MethodPost, runsPath, f.startBody(tc.mutate)), tc.status, tc.code)
		if name == "discount without account" && f.proposalStatus() != "APPROVED" {
			t.Fatalf("a refused start moved the proposal to %s", f.proposalStatus())
		}
	}
	f.ledger.entity = entityB
	wantProblem(t, f.erin.send(f.router, http.MethodPost, runsPath, f.startBody(nil)), http.StatusUnprocessableEntity, "AP-RUN-013")
	f.ledger.entity = entityA
	noUser := caller{duties: runDuties, entity: entityA}
	wantProblem(t, noUser.send(f.router, http.MethodPost, runsPath, f.startBody(nil)), http.StatusForbidden, "AUTH-0003")
	if f.proposalStatus() != "APPROVED" {
		t.Fatalf("refused starts must leave the proposal APPROVED, got %s", f.proposalStatus())
	}
}

func TestPaymentRun_HeldItemFailsTheStartAndReleasesTheProposal(t *testing.T) {
	f := newRunFixture(t, fakeValidator{})
	f.approve()
	seedCtx := tenant.With(context.Background(), tenant.Principal{TenantID: testkit.TenantID(1), LegalEntityID: entityA, Duties: testkit.ScopeDuties(entityA)})
	if err := f.pg.SetPaymentHold(seedCtx, f.itemA.ID, true); err != nil {
		t.Fatal(err)
	}
	run := f.start(f.startBody(nil))
	if run["status"] != "FAILED" || !strings.Contains(run["failureReason"].(string), f.itemA.ID) || f.proposalStatus() != "FAILED" {
		t.Fatalf("run = %v, proposal = %s", run, f.proposalStatus())
	}
	replay := f.erin.send(f.router, http.MethodPost, runsPath, f.startBody(nil))
	if replay.Code != http.StatusOK || decode(t, replay)["status"] != "FAILED" {
		t.Fatalf("replay of a failed start: %d %s", replay.Code, replay.Body)
	}
}

func TestPaymentRun_OpenAPIDocumentsEveryRunRoute(t *testing.T) {
	f := newRunFixture(t, fakeValidator{})
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

func TestPaymentRun_DutiesAndLegalEntityScope(t *testing.T) {
	f := newRunFixture(t, fakeValidator{})
	f.approve()
	runID := f.start(f.startBody(nil))["id"].(string)
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
	wantProblem(t, approver.send(f.router, http.MethodPost, runsPath, f.startBody(nil)), http.StatusForbidden, "AUTH-0003")
	wantProblem(t, reader.send(f.router, http.MethodPost, runsPath, f.startBody(nil)), http.StatusForbidden, "AUTH-0003")
	wantProblem(t, executor.send(f.router, http.MethodGet, runsPath+"/"+runID, ""), http.StatusForbidden, "AUTH-0003")
	wantProblem(t, executor.send(f.router, http.MethodGet, runsPath+"/"+runID+"/file", ""), http.StatusForbidden, "AUTH-0003")
	if rec := reader.send(f.router, http.MethodGet, runsPath+"/"+runID, ""); rec.Code != http.StatusOK {
		t.Fatalf("reader get: %d %s", rec.Code, rec.Body)
	}
	wantProblem(t, outsider.send(f.router, http.MethodGet, runsPath+"/"+runID, ""), http.StatusNotFound, "COM-0404")
	wantProblem(t, outsider.send(f.router, http.MethodGet, runsPath+"/"+runID+"/file", ""), http.StatusNotFound, "COM-0404")
	wantProblem(t, reader.send(f.router, http.MethodGet, runsPath+"/not-a-uuid", ""), http.StatusBadRequest, "COM-0001")
	wantProblem(t, reader.send(f.router, http.MethodGet, runsPath+"/00000000-0000-0000-0000-00000000dead/file", ""), http.StatusNotFound, "COM-0404")
	wantProblem(t, reader.send(f.router, http.MethodGet, runsPath+"/"+runID+"/file", ""), http.StatusNotFound, "COM-0404")
	noScope := caller{duties: runDuties, user: "erin", noScope: true}
	wantProblem(t, noScope.send(f.router, http.MethodPost, runsPath, strings.Replace(f.startBody(nil), `"legalEntityId":"`+entityA+`",`, "", 1)), http.StatusBadRequest, "COM-0001")
}
