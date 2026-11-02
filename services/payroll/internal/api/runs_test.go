package api

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"

	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/platform/dbx"
	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/platform/ledgerclient"
	"github.com/BenjaminSRussell/passage/platform/tenant"
	"github.com/BenjaminSRussell/passage/platform/testkit"
	"github.com/BenjaminSRussell/passage/services/payroll/internal/domain"
	"github.com/BenjaminSRussell/passage/services/payroll/internal/store"
	"github.com/BenjaminSRussell/passage/services/payroll/migrations"
)

const (
	legalEntity = "00000000-0000-0000-0000-0000000000e1"
	ledgerID    = "00000000-0000-0000-0000-0000000000a9"
	workerA     = "00000000-0000-0000-0000-0000000000a1"
	workerB     = "00000000-0000-0000-0000-0000000000a2"
	workerC     = "00000000-0000-0000-0000-0000000000a3"
)

type runServer struct {
	t      *testing.T
	h      http.Handler
	ledger *ledgerclient.Fake
	api    *API
}

var payrollRoles = []string{domain.AccountSalaryExpense, domain.AccountEmployerTaxExpense, domain.AccountNetPayClearing, domain.AccountFederalWHPayable,
	domain.AccountFICAPayable, domain.AccountFUTAPayable, domain.AccountPayable("401K"), domain.AccountMatchExpense("401K"), domain.AccountPayable("STATE_CO_WH")}

func glAccount(role string) string { return "GL" + strings.TrimPrefix(role, "PAYROLL") }

func profileBody(roles ...string) string {
	pairs := make([]string, len(roles))
	for i, r := range roles {
		pairs[i] = `"` + r + `":"` + glAccount(r) + `"`
	}
	return `{"accounts":{` + strings.Join(pairs, ",") + `}}`
}

func newRunServer(t *testing.T, withLedger bool) *runServer {
	t.Helper()
	return withProfile(t, newBareRunServer(t, withLedger))
}

func withProfile(t *testing.T, s *runServer) *runServer {
	t.Helper()
	if rec := s.do(http.MethodPut, "/v1/payroll/posting-profiles/"+legalEntity, profileBody(payrollRoles...), "dave", "payroll.posting-profile.maintain", ""); rec.Code != http.StatusOK {
		t.Fatalf("profile %d: %s", rec.Code, rec.Body.String())
	}
	return s
}

func newBareRunServer(t *testing.T, withLedger bool) *runServer {
	t.Helper()
	if !withLedger {
		return buildRunServer(t, nil)
	}
	return buildRunServer(t, func(f *ledgerclient.Fake) ledgerclient.Ledger { return f })
}

type lostAnswer struct {
	*ledgerclient.Fake
	lose bool
}

func (l *lostAnswer) Post(ctx context.Context, e ledgerclient.Entry) (string, error) {
	id, err := l.Fake.Post(ctx, e)
	if err == nil && l.lose {
		return "", errors.New("answer lost")
	}
	return id, err
}

func buildRunServer(t *testing.T, ledgerFor func(*ledgerclient.Fake) ledgerclient.Ledger) *runServer {
	t.Helper()
	admin, app := testkit.DBWithApp(t)
	if err := dbx.Migrate(context.Background(), admin, migrations.FS); err != nil {
		t.Fatal(err)
	}
	s := &runServer{t: t, ledger: ledgerclient.NewFake()}
	s.ledger.AddLedger(ledgerID, legalEntity)
	a := New(nil)
	s.api = a
	if ledgerFor != nil {
		a.WithRuns(store.New(app), ledgerFor(s.ledger))
	} else {
		a.WithRuns(store.New(app), nil)
	}
	router := httpx.NewRouter()
	a.Register(router)
	s.h = router
	return s
}

func (s *runServer) do(method, path, body, user, duties, key string) *httptest.ResponseRecorder {
	s.t.Helper()
	return s.doAt(legalEntity, method, path, body, user, duties, key)
}

func (s *runServer) doAt(scope, method, path, body, user, duties, key string) *httptest.ResponseRecorder {
	s.t.Helper()
	req := httptest.NewRequest(method, path, bytes.NewBufferString(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set(tenant.HeaderTenant, testTenant)
	req.Header.Set(tenant.HeaderLegalEntity, scope)
	req.Header.Set(tenant.HeaderUser, user)
	req.Header.Set(tenant.HeaderDuties, duties)
	if key != "" {
		req.Header.Set("Idempotency-Key", key)
	}
	rec := httptest.NewRecorder()
	s.h.ServeHTTP(rec, req)
	return rec
}

func runBody(workers ...string) string {
	statements := make([]string, len(workers))
	for i, w := range workers {
		statements[i] = strings.Replace(br431, "00000000-0000-0000-0000-0000000000a1", w, 1)
	}
	return `{"legalEntityId":"` + legalEntity + `","payDate":"2025-03-14","periodStart":"2025-03-01","periodEnd":"2025-03-14","statements":[` + strings.Join(statements, ",") + `]}`
}

func decodeRun(t *testing.T, rec *httptest.ResponseRecorder) runView {
	t.Helper()
	var v runView
	if err := json.Unmarshal(rec.Body.Bytes(), &v); err != nil {
		t.Fatalf("%v: %s", err, rec.Body.String())
	}
	return v
}

func problemCode(rec *httptest.ResponseRecorder) string {
	var p struct {
		Code string `json:"code"`
	}
	_ = json.Unmarshal(rec.Body.Bytes(), &p)
	return p.Code
}

func (s *runServer) finalizedRun(workers ...string) runView {
	s.t.Helper()
	created := s.do(http.MethodPost, "/v1/payroll/runs", runBody(workers...), "alice", "payroll.run.maintain", "k-"+workers[0])
	if created.Code != http.StatusCreated {
		s.t.Fatalf("create %d: %s", created.Code, created.Body.String())
	}
	run := decodeRun(s.t, created)
	if rec := s.do(http.MethodPost, "/v1/payroll/runs/"+run.ID+"/review", "", "alice", "payroll.run.maintain", ""); rec.Code != http.StatusOK {
		s.t.Fatalf("review %d: %s", rec.Code, rec.Body.String())
	}
	if rec := s.do(http.MethodPost, "/v1/payroll/runs/"+run.ID+"/finalize", "", "bob", "payroll.run.finalize", ""); rec.Code != http.StatusOK {
		s.t.Fatalf("finalize %d: %s", rec.Code, rec.Body.String())
	}
	return run
}

func TestRunLifecycle_SeparationOfDutiesAndIdempotentCreate(t *testing.T) {
	s := newRunServer(t, true)
	body := runBody(workerA, workerB)
	first := s.do(http.MethodPost, "/v1/payroll/runs", body, "alice", "payroll.run.maintain", "key-1")
	if first.Code != http.StatusCreated {
		t.Fatalf("create %d: %s", first.Code, first.Body.String())
	}
	run := decodeRun(t, first)
	if run.Status != "CALCULATED" || len(run.Statements) != 2 || run.Gross != "8000.00" || run.Net != "5567.80" {
		t.Fatalf("run %+v", run)
	}
	replay := s.do(http.MethodPost, "/v1/payroll/runs", body, "alice", "payroll.run.maintain", "key-1")
	if replay.Code != http.StatusCreated || decodeRun(t, replay).ID != run.ID {
		t.Fatalf("replay %d: %s", replay.Code, replay.Body.String())
	}
	if other := s.do(http.MethodPost, "/v1/payroll/runs", runBody(workerA), "alice", "payroll.run.maintain", "key-1"); other.Code != http.StatusConflict || problemCode(other) != store.CodeKeyConflict {
		t.Fatalf("same key other body: %d %s", other.Code, other.Body.String())
	}
	if rec := s.do(http.MethodPost, "/v1/payroll/runs/"+run.ID+"/finalize", "", "bob", "payroll.run.finalize", ""); rec.Code != http.StatusUnprocessableEntity || problemCode(rec) != domain.CodeRunTransition {
		t.Fatalf("finalize before review: %d %s", rec.Code, rec.Body.String())
	}
	if rec := s.do(http.MethodPost, "/v1/payroll/runs/"+run.ID+"/review", "", "alice", "payroll.run.maintain", ""); rec.Code != http.StatusOK {
		t.Fatalf("review %d: %s", rec.Code, rec.Body.String())
	}
	if rec := s.do(http.MethodPost, "/v1/payroll/runs/"+run.ID+"/finalize", "", "alice", "payroll.run.finalize", ""); rec.Code != http.StatusUnprocessableEntity || problemCode(rec) != domain.CodeRunSoD {
		t.Fatalf("calculator finalizing: %d %s", rec.Code, rec.Body.String())
	}
	if rec := s.do(http.MethodPost, "/v1/payroll/runs/"+run.ID+"/finalize", "", "bob", "payroll.run.maintain", ""); rec.Code != http.StatusForbidden {
		t.Fatalf("finalize without its duty: %d", rec.Code)
	}
	done := s.do(http.MethodPost, "/v1/payroll/runs/"+run.ID+"/finalize", "", "bob", "payroll.run.finalize", "")
	if got := decodeRun(t, done); done.Code != http.StatusOK || got.Status != "FINALIZED" || got.FinalizedBy != "bob" {
		t.Fatalf("finalize %d: %s", done.Code, done.Body.String())
	}
	if rec := s.do(http.MethodPost, "/v1/payroll/runs/"+run.ID+"/review", "", "alice", "payroll.run.maintain", ""); rec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("review after finalize: %d", rec.Code)
	}
}

func TestRunCreateValidation(t *testing.T) {
	s := newRunServer(t, true)
	cases := map[string]string{
		"duplicate worker": runBody(workerA, workerA),
		"no statements":    `{"legalEntityId":"` + legalEntity + `","payDate":"2025-03-14","periodStart":"2025-03-01","periodEnd":"2025-03-14","statements":[]}`,
		"period reversed":  strings.Replace(runBody(workerA), `"periodEnd":"2025-03-14"`, `"periodEnd":"2025-02-01"`, 1),
		"no worker":        strings.Replace(runBody(workerA), workerA, "", 1),
	}
	for name, body := range cases {
		if rec := s.do(http.MethodPost, "/v1/payroll/runs", body, "alice", "payroll.run.maintain", "k-"+name); rec.Code != http.StatusBadRequest || problemCode(rec) != "COM-0001" {
			t.Errorf("%s: %d %s", name, rec.Code, rec.Body.String())
		}
	}
	if rec := s.do(http.MethodPost, "/v1/payroll/runs", runBody(workerA), "alice", "payroll.run.maintain", ""); rec.Code != http.StatusBadRequest {
		t.Errorf("missing Idempotency-Key: %d", rec.Code)
	}
	other := strings.Replace(runBody(workerA), legalEntity, "00000000-0000-0000-0000-0000000000e2", 1)
	if rec := s.do(http.MethodPost, "/v1/payroll/runs", other, "alice", "payroll.run.maintain", "k-other"); rec.Code != http.StatusForbidden {
		t.Errorf("legal entity outside scope: %d", rec.Code)
	}
}

func TestRunPosting_ReconcilesToGLPayrollAccounts(t *testing.T) {
	s := newRunServer(t, true)
	run := s.finalizedRun(workerA, workerB, workerC)
	if rec := s.do(http.MethodPost, "/v1/payroll/runs/"+run.ID+"/post", `{"ledgerId":"`+ledgerID+`"}`, "carol", "payroll.run.maintain", ""); rec.Code != http.StatusForbidden {
		t.Fatalf("post without its duty: %d", rec.Code)
	}
	posted := s.do(http.MethodPost, "/v1/payroll/runs/"+run.ID+"/post", `{"ledgerId":"`+ledgerID+`"}`, "carol", "payroll.run.post", "")
	view := decodeRun(t, posted)
	if posted.Code != http.StatusOK || view.Status != "POSTED" || view.JournalEntryID == "" || view.LedgerID != ledgerID {
		t.Fatalf("post %d: %s", posted.Code, posted.Body.String())
	}
	entries := s.ledger.Entries()
	if len(entries) != 1 || entries[0].DocumentID != run.ID || entries[0].PostingDate != "2025-03-14" {
		t.Fatalf("ledger entries %+v", entries)
	}
	debit, credit, netClearing := decimal.Zero, decimal.Zero, decimal.Zero
	for _, l := range entries[0].Lines {
		if l.Credit {
			credit = credit.Add(l.Amount)
		} else {
			debit = debit.Add(l.Amount)
		}
		if !strings.HasPrefix(l.Account, "GL_") {
			t.Errorf("ledger account %q was not taken from the posting profile", l.Account)
		}
		if l.Account == glAccount(domain.AccountNetPayClearing) {
			netClearing = l.Amount
		}
	}
	gross, net, employer := decimal.RequireFromString(view.Gross), decimal.RequireFromString(view.Net), decimal.RequireFromString(view.EmployerCost)
	if !debit.Equal(credit) || !debit.Equal(gross.Add(employer)) || !netClearing.Equal(net) {
		t.Fatalf("debit %s credit %s want %s; net clearing %s want %s", debit, credit, gross.Add(employer), netClearing, net)
	}

	again := s.do(http.MethodPost, "/v1/payroll/runs/"+run.ID+"/post", `{"ledgerId":"`+ledgerID+`"}`, "carol", "payroll.run.post", "")
	if again.Code != http.StatusOK || decodeRun(t, again).JournalEntryID != view.JournalEntryID || len(s.ledger.Entries()) != 1 {
		t.Fatalf("repost %d: %s", again.Code, again.Body.String())
	}

	recon := func() reconView {
		rec := s.do(http.MethodGet, "/v1/payroll/gl-reconciliation?legalEntityId="+legalEntity+"&ledgerId="+ledgerID, "", "carol", "payroll.run.read", "")
		var v reconView
		if rec.Code != http.StatusOK || json.Unmarshal(rec.Body.Bytes(), &v) != nil {
			t.Fatalf("reconciliation %d: %s", rec.Code, rec.Body.String())
		}
		return v
	}
	good := recon()
	if !good.Reconciled || good.PostedRuns != 1 || len(good.Accounts) < 6 {
		t.Fatalf("reconciliation %+v", good)
	}
	for _, a := range good.Accounts {
		if a.Difference != "0.00" || a.Register != a.GL {
			t.Errorf("account %+v", a)
		}
	}

	if _, err := s.ledger.Post(tenantCtx(), ledgerclient.Entry{LegalEntityID: legalEntity, LedgerID: ledgerID, DocumentID: "stray", PostingDate: "2025-03-15", Currency: "USD",
		IdempotencyKey: "stray", Lines: []ledgerclient.Line{
			{Account: glAccount(domain.AccountSalaryExpense), Amount: decimal.RequireFromString("12.34")},
			{Account: "OTHER", Credit: true, Amount: decimal.RequireFromString("12.34")}}}); err != nil {
		t.Fatal(err)
	}
	drift := recon()
	if drift.Reconciled {
		t.Fatalf("a stray ledger line must break reconciliation: %+v", drift)
	}
	for _, a := range drift.Accounts {
		if a.Account == glAccount(domain.AccountSalaryExpense) && a.Difference != "12.34" {
			t.Errorf("salary expense difference %s, want 12.34", a.Difference)
		}
	}
}

func TestRunPosting_RefusalLeavesRunFinalized(t *testing.T) {
	s := newRunServer(t, true)
	run := s.finalizedRun(workerA)
	s.ledger.FailPosts(&ledgerclient.Error{Op: "post", Status: http.StatusUnprocessableEntity, Code: ledgerclient.CodePeriodClosed})
	rec := s.do(http.MethodPost, "/v1/payroll/runs/"+run.ID+"/post", `{"ledgerId":"`+ledgerID+`"}`, "carol", "payroll.run.post", "")
	if rec.Code != http.StatusBadGateway || problemCode(rec) != store.CodeGLRefused {
		t.Fatalf("refused post: %d %s", rec.Code, rec.Body.String())
	}
	if got := decodeRun(t, s.do(http.MethodGet, "/v1/payroll/runs/"+run.ID, "", "carol", "payroll.run.read", "")); got.Status != "FINALIZED" || got.JournalEntryID != "" {
		t.Fatalf("run after refusal %+v", got)
	}
	s.ledger.FailPosts(nil)
	if rec := s.do(http.MethodPost, "/v1/payroll/runs/"+run.ID+"/post", `{"ledgerId":"`+ledgerID+`"}`, "carol", "payroll.run.post", ""); rec.Code != http.StatusOK {
		t.Fatalf("retry %d: %s", rec.Code, rec.Body.String())
	}
}

func TestRunPosting_RequiresFinalizedRun(t *testing.T) {
	s := newRunServer(t, true)
	created := decodeRun(t, s.do(http.MethodPost, "/v1/payroll/runs", runBody(workerA), "alice", "payroll.run.maintain", "k-draft"))
	rec := s.do(http.MethodPost, "/v1/payroll/runs/"+created.ID+"/post", `{"ledgerId":"`+ledgerID+`"}`, "carol", "payroll.run.post", "")
	if rec.Code != http.StatusUnprocessableEntity || problemCode(rec) != domain.CodeRunTransition || len(s.ledger.Entries()) != 0 {
		t.Fatalf("post of a CALCULATED run: %d %s", rec.Code, rec.Body.String())
	}
}

func TestRunEndpoints_UnavailableWithoutLedger(t *testing.T) {
	s := newRunServer(t, false)
	run := s.finalizedRun(workerA)
	if rec := s.do(http.MethodPost, "/v1/payroll/runs/"+run.ID+"/post", `{"ledgerId":"`+ledgerID+`"}`, "carol", "payroll.run.post", ""); rec.Code != http.StatusServiceUnavailable || problemCode(rec) != CodeGLUnavailable {
		t.Fatalf("post without ledger: %d %s", rec.Code, rec.Body.String())
	}
}

func TestRunEndpoints_UnavailableWithoutStore(t *testing.T) {
	rec := post(t, newServer(t), "/v1/payroll/runs", runBody(workerA), "payroll.run.maintain")
	if rec.Code != http.StatusServiceUnavailable || problemCode(rec) != CodeRunsUnavailable {
		t.Fatalf("%d %s", rec.Code, rec.Body.String())
	}
}

func TestRunGet_UnknownRunIsNotFound(t *testing.T) {
	s := newRunServer(t, true)
	if rec := s.do(http.MethodGet, "/v1/payroll/runs/00000000-0000-0000-0000-00000000ffff", "", "carol", "payroll.run.read", ""); rec.Code != http.StatusNotFound || problemCode(rec) != store.CodeRunNotFound {
		t.Fatalf("%d %s", rec.Code, rec.Body.String())
	}
}

func tenantCtx() context.Context {
	return tenant.With(context.Background(), tenant.Principal{TenantID: testTenant, LegalEntityID: legalEntity, UserID: "carol", Duties: map[string]struct{}{}})
}

const otherEntity = "00000000-0000-0000-0000-0000000000e2"

func TestRunCreate_IdempotencyKeyIsScopedToCallerAndLegalEntity(t *testing.T) {
	s := newRunServer(t, true)
	body := runBody(workerA)
	alice := decodeRun(t, s.do(http.MethodPost, "/v1/payroll/runs", body, "alice", "payroll.run.maintain", "shared-key"))
	bob := s.do(http.MethodPost, "/v1/payroll/runs", body, "bob", "payroll.run.maintain", "shared-key")
	if bob.Code != http.StatusCreated || decodeRun(t, bob).ID == alice.ID || decodeRun(t, bob).CalculatedBy != "bob" {
		t.Fatalf("another user must get its own run, not alice's: %d %s", bob.Code, bob.Body.String())
	}
	bobReplay := s.do(http.MethodPost, "/v1/payroll/runs", body, "bob", "payroll.run.maintain", "shared-key")
	if bobReplay.Code != http.StatusCreated || decodeRun(t, bobReplay).ID != decodeRun(t, bob).ID {
		t.Fatalf("bob's replay must return bob's run, not alice's: %d %s", bobReplay.Code, bobReplay.Body.String())
	}
	probe := s.do(http.MethodPost, "/v1/payroll/runs", runBody(workerA, workerB), "bob", "payroll.run.maintain", "shared-key")
	if probe.Code != http.StatusConflict || problemCode(probe) != store.CodeKeyConflict || strings.Contains(probe.Body.String(), alice.ID) {
		t.Fatalf("bob's own key with another body: %d %s", probe.Code, probe.Body.String())
	}
	elsewhere := strings.Replace(body, legalEntity, otherEntity, 1)
	same := s.doAt(otherEntity, http.MethodPost, "/v1/payroll/runs", elsewhere, "alice", "payroll.run.maintain", "shared-key")
	if same.Code != http.StatusCreated || decodeRun(t, same).ID == alice.ID || decodeRun(t, same).LegalEntityID != otherEntity {
		t.Fatalf("another legal entity must get its own run: %d %s", same.Code, same.Body.String())
	}
	sameReplay := s.doAt(otherEntity, http.MethodPost, "/v1/payroll/runs", elsewhere, "alice", "payroll.run.maintain", "shared-key")
	if sameReplay.Code != http.StatusCreated || decodeRun(t, sameReplay).ID != decodeRun(t, same).ID {
		t.Fatalf("replay in the other legal entity: %d %s", sameReplay.Code, sameReplay.Body.String())
	}
	replay := s.do(http.MethodPost, "/v1/payroll/runs", body, "alice", "payroll.run.maintain", "shared-key")
	if replay.Code != http.StatusCreated || decodeRun(t, replay).ID != alice.ID {
		t.Fatalf("alice's own replay: %d %s", replay.Code, replay.Body.String())
	}
}

func TestPostingProfile_GLAccountsComeFromStoredConfigNeverTheRequest(t *testing.T) {
	s := newRunServer(t, true)
	run := s.finalizedRun(workerA)
	smuggled := `{"ledgerId":"` + ledgerID + `","accounts":{"PAYROLL_SALARY_EXPENSE":"EVIL"}}`
	if rec := s.do(http.MethodPost, "/v1/payroll/runs/"+run.ID+"/post", smuggled, "carol", "payroll.run.post", ""); rec.Code != http.StatusBadRequest || len(s.ledger.Entries()) != 0 {
		t.Fatalf("request-supplied accounts on post: %d %s", rec.Code, rec.Body.String())
	}
	withAccounts := strings.Replace(runBody(workerB), `"statements"`, `"accounts":{"PAYROLL_SALARY_EXPENSE":"EVIL"},"statements"`, 1)
	if rec := s.do(http.MethodPost, "/v1/payroll/runs", withAccounts, "alice", "payroll.run.maintain", "k-accounts"); rec.Code != http.StatusBadRequest {
		t.Fatalf("request-supplied accounts on create: %d %s", rec.Code, rec.Body.String())
	}
	if rec := s.do(http.MethodPost, "/v1/payroll/runs/"+run.ID+"/post", `{"ledgerId":"`+ledgerID+`"}`, "carol", "payroll.run.post", ""); rec.Code != http.StatusOK {
		t.Fatalf("post %d: %s", rec.Code, rec.Body.String())
	}
	for _, l := range s.ledger.Entries()[0].Lines {
		if l.Account == "EVIL" || strings.HasPrefix(l.Account, "PAYROLL_") {
			t.Errorf("ledger line account %q", l.Account)
		}
	}
}

func TestPostingProfile_MissingRoleRefusesPostingAndMergesRoles(t *testing.T) {
	s := newBareRunServer(t, true)
	run := s.finalizedRun(workerA)
	rec := s.do(http.MethodPost, "/v1/payroll/runs/"+run.ID+"/post", `{"ledgerId":"`+ledgerID+`"}`, "carol", "payroll.run.post", "")
	if rec.Code != http.StatusUnprocessableEntity || problemCode(rec) != domain.CodeNoPostingAccount || len(s.ledger.Entries()) != 0 {
		t.Fatalf("post without profile: %d %s", rec.Code, rec.Body.String())
	}
	put := func(body string) *httptest.ResponseRecorder {
		return s.do(http.MethodPut, "/v1/payroll/posting-profiles/"+legalEntity, body, "dave", "payroll.posting-profile.maintain", "")
	}
	put(profileBody(payrollRoles[:3]...))
	if rec := s.do(http.MethodPost, "/v1/payroll/runs/"+run.ID+"/post", `{"ledgerId":"`+ledgerID+`"}`, "carol", "payroll.run.post", ""); rec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("post with partial profile: %d", rec.Code)
	}
	put(profileBody(payrollRoles[3:]...))
	var got profileView
	read := s.do(http.MethodGet, "/v1/payroll/posting-profiles/"+legalEntity, "", "carol", "payroll.run.read", "")
	if err := json.Unmarshal(read.Body.Bytes(), &got); err != nil || len(got.Accounts) != len(payrollRoles) || got.Accounts[domain.AccountSalaryExpense] != glAccount(domain.AccountSalaryExpense) {
		t.Fatalf("merged profile %d: %s", read.Code, read.Body.String())
	}
	if rec := s.do(http.MethodPost, "/v1/payroll/runs/"+run.ID+"/post", `{"ledgerId":"`+ledgerID+`"}`, "carol", "payroll.run.post", ""); rec.Code != http.StatusOK {
		t.Fatalf("post with full profile: %d %s", rec.Code, rec.Body.String())
	}
}

func TestPostingProfile_AccessAndValidation(t *testing.T) {
	s := newBareRunServer(t, true)
	path := "/v1/payroll/posting-profiles/" + legalEntity
	if rec := s.do(http.MethodPut, path, profileBody(domain.AccountSalaryExpense), "carol", "payroll.run.post", ""); rec.Code != http.StatusForbidden {
		t.Errorf("write without its duty: %d", rec.Code)
	}
	if rec := s.do(http.MethodGet, path, "", "carol", "payroll.run.maintain", ""); rec.Code != http.StatusForbidden {
		t.Errorf("read without its duty: %d", rec.Code)
	}
	if rec := s.doAt(legalEntity, http.MethodPut, "/v1/payroll/posting-profiles/"+otherEntity, profileBody(domain.AccountSalaryExpense), "dave", "payroll.posting-profile.maintain", ""); rec.Code != http.StatusForbidden {
		t.Errorf("legal entity outside scope: %d", rec.Code)
	}
	bad := map[string]string{
		"empty":        `{"accounts":{}}`,
		"bad role":     `{"accounts":{"salary":"GL1"}}`,
		"bad account":  `{"accounts":{"PAYROLL_SALARY_EXPENSE":"a b"}}`,
		"empty acct":   `{"accounts":{"PAYROLL_SALARY_EXPENSE":""}}`,
		"unknown prop": `{"accounts":{"PAYROLL_SALARY_EXPENSE":"GL1"},"extra":1}`,
	}
	for name, body := range bad {
		if rec := s.do(http.MethodPut, path, body, "dave", "payroll.posting-profile.maintain", ""); rec.Code != http.StatusBadRequest || problemCode(rec) != "COM-0001" {
			t.Errorf("%s: %d %s", name, rec.Code, rec.Body.String())
		}
	}
	if rec := s.do(http.MethodPut, "/v1/payroll/posting-profiles/not-a-uuid", profileBody(domain.AccountSalaryExpense), "dave", "payroll.posting-profile.maintain", ""); rec.Code != http.StatusBadRequest {
		t.Errorf("bad legal entity id: %d", rec.Code)
	}
}

func TestRunCreate_PayDateBeforePeriodStartIsRefused(t *testing.T) {
	s := newRunServer(t, true)
	body := strings.Replace(runBody(workerA), `"payDate":"2025-03-14"`, `"payDate":"2025-02-28"`, 1)
	if rec := s.do(http.MethodPost, "/v1/payroll/runs", body, "alice", "payroll.run.maintain", "k-early"); rec.Code != http.StatusUnprocessableEntity || problemCode(rec) != domain.CodeInvalidInput {
		t.Fatalf("pay date before the period: %d %s", rec.Code, rec.Body.String())
	}
}

func TestRunCreate_ReplayIgnoresWhitespaceAndKeyOrder(t *testing.T) {
	s := newRunServer(t, true)
	first := decodeRun(t, s.do(http.MethodPost, "/v1/payroll/runs", runBody(workerA), "alice", "payroll.run.maintain", "k-canon"))
	var generic map[string]any
	if err := json.Unmarshal([]byte(runBody(workerA)), &generic); err != nil {
		t.Fatal(err)
	}
	reshaped := map[string]any{"statements": generic["statements"], "periodEnd": generic["periodEnd"], "periodStart": generic["periodStart"], "payDate": generic["payDate"], "legalEntityId": generic["legalEntityId"]}
	pretty, err := json.MarshalIndent(reshaped, "", "   ")
	if err != nil {
		t.Fatal(err)
	}
	replay := s.do(http.MethodPost, "/v1/payroll/runs", "\n  "+string(pretty)+"\n", "alice", "payroll.run.maintain", "k-canon")
	if replay.Code != http.StatusCreated || decodeRun(t, replay).ID != first.ID {
		t.Fatalf("reordered replay: %d %s", replay.Code, replay.Body.String())
	}
}

func TestRunByID_OtherLegalEntityScopeSeesNotFound(t *testing.T) {
	s := newRunServer(t, true)
	run := s.finalizedRun(workerA)
	created := decodeRun(t, s.do(http.MethodPost, "/v1/payroll/runs", runBody(workerB), "alice", "payroll.run.maintain", "k-open"))
	steps := []struct{ method, path, body, duty string }{
		{http.MethodGet, "/v1/payroll/runs/" + run.ID, "", "payroll.run.read"},
		{http.MethodPost, "/v1/payroll/runs/" + created.ID + "/review", "", "payroll.run.maintain"},
		{http.MethodPost, "/v1/payroll/runs/" + created.ID + "/finalize", "", "payroll.run.finalize"},
		{http.MethodPost, "/v1/payroll/runs/" + run.ID + "/post", `{"ledgerId":"` + ledgerID + `"}`, "payroll.run.post"},
	}
	for _, st := range steps {
		if rec := s.doAt(otherEntity, st.method, st.path, st.body, "zed", st.duty, ""); rec.Code != http.StatusNotFound || problemCode(rec) != store.CodeRunNotFound {
			t.Errorf("%s %s from another legal entity: %d %s", st.method, st.path, rec.Code, rec.Body.String())
		}
	}
	if len(s.ledger.Entries()) != 0 {
		t.Fatalf("a foreign caller posted: %+v", s.ledger.Entries())
	}
}

func TestRunPosting_JournalIsFixedBeforeTheLedgerCall(t *testing.T) {
	var lost *lostAnswer
	s := withProfile(t, buildRunServer(t, func(f *ledgerclient.Fake) ledgerclient.Ledger {
		lost = &lostAnswer{Fake: f, lose: true}
		return lost
	}))
	run := s.finalizedRun(workerA)
	postBody := `{"ledgerId":"` + ledgerID + `"}`
	if rec := s.do(http.MethodPost, "/v1/payroll/runs/"+run.ID+"/post", postBody, "carol", "payroll.run.post", ""); rec.Code != http.StatusBadGateway {
		t.Fatalf("lost answer: %d %s", rec.Code, rec.Body.String())
	}
	sent := s.ledger.Entries()
	if len(sent) != 1 {
		t.Fatalf("entries %+v", sent)
	}
	rewired := `{"accounts":{"` + domain.AccountSalaryExpense + `":"MOVED"}}`
	if rec := s.do(http.MethodPut, "/v1/payroll/posting-profiles/"+legalEntity, rewired, "dave", "payroll.posting-profile.maintain", ""); rec.Code != http.StatusOK {
		t.Fatalf("profile edit %d", rec.Code)
	}
	otherLedger := `{"ledgerId":"00000000-0000-0000-0000-0000000000b9"}`
	if rec := s.do(http.MethodPost, "/v1/payroll/runs/"+run.ID+"/post", otherLedger, "carol", "payroll.run.post", ""); rec.Code != http.StatusConflict || problemCode(rec) != store.CodeLedgerPinned {
		t.Fatalf("retry against another ledger: %d %s", rec.Code, rec.Body.String())
	}
	lost.lose = false
	rec := s.do(http.MethodPost, "/v1/payroll/runs/"+run.ID+"/post", postBody, "carol", "payroll.run.post", "")
	if got := decodeRun(t, rec); rec.Code != http.StatusOK || got.Status != "POSTED" || got.JournalEntryID != sent[0].ID {
		t.Fatalf("retry %d: %s", rec.Code, rec.Body.String())
	}
	if after := s.ledger.Entries(); len(after) != 1 || !reflect.DeepEqual(after[0].Lines, sent[0].Lines) {
		t.Fatalf("the retry must resend the identical journal: %+v", after)
	}
}

func TestReconciliation_UsesTheJournalStoredOnThePostedRun(t *testing.T) {
	s := newRunServer(t, true)
	run := s.finalizedRun(workerA)
	if rec := s.do(http.MethodPost, "/v1/payroll/runs/"+run.ID+"/post", `{"ledgerId":"`+ledgerID+`"}`, "carol", "payroll.run.post", ""); rec.Code != http.StatusOK {
		t.Fatalf("post %d: %s", rec.Code, rec.Body.String())
	}
	rewired := `{"accounts":{"` + domain.AccountSalaryExpense + `":"MOVED"}}`
	if rec := s.do(http.MethodPut, "/v1/payroll/posting-profiles/"+legalEntity, rewired, "dave", "payroll.posting-profile.maintain", ""); rec.Code != http.StatusOK {
		t.Fatalf("profile edit %d", rec.Code)
	}
	rec := s.do(http.MethodGet, "/v1/payroll/gl-reconciliation?legalEntityId="+legalEntity+"&ledgerId="+ledgerID, "", "carol", "payroll.run.read", "")
	var v reconView
	if rec.Code != http.StatusOK || json.Unmarshal(rec.Body.Bytes(), &v) != nil || !v.Reconciled {
		t.Fatalf("reconciliation after a profile edit %d: %s", rec.Code, rec.Body.String())
	}
	listed := map[string]bool{}
	for _, a := range v.Accounts {
		listed[a.Account] = true
	}
	if !listed[glAccount(domain.AccountSalaryExpense)] || listed["MOVED"] {
		t.Fatalf("accounts %+v", v.Accounts)
	}
}

const otherLedgerID = "00000000-0000-0000-0000-0000000000b9"

func (s *runServer) post(runID, ledger string) *httptest.ResponseRecorder {
	s.t.Helper()
	return s.do(http.MethodPost, "/v1/payroll/runs/"+runID+"/post", `{"ledgerId":"`+ledger+`"}`, "carol", "payroll.run.post", "")
}

func TestRunPosting_DefiniteRefusalClearsThePinSoTheCorrectedProfilePosts(t *testing.T) {
	s := newRunServer(t, true)
	run := s.finalizedRun(workerA)
	s.ledger.FailPosts(&ledgerclient.Error{Op: "post", Status: http.StatusUnprocessableEntity, Code: "LEDGER_ACCOUNT_NOT_FOUND"})
	if rec := s.post(run.ID, ledgerID); rec.Code != http.StatusBadGateway {
		t.Fatalf("refused post: %d %s", rec.Code, rec.Body.String())
	}
	if got := decodeRun(t, s.do(http.MethodGet, "/v1/payroll/runs/"+run.ID, "", "carol", "payroll.run.read", "")); got.LedgerID != "" {
		t.Fatalf("pin kept after a definite refusal: %+v", got)
	}
	fixed := `{"accounts":{"` + domain.AccountSalaryExpense + `":"FIXED"}}`
	if rec := s.do(http.MethodPut, "/v1/payroll/posting-profiles/"+legalEntity, fixed, "dave", "payroll.posting-profile.maintain", ""); rec.Code != http.StatusOK {
		t.Fatalf("profile fix %d", rec.Code)
	}
	s.ledger.FailPosts(nil)
	if rec := s.post(run.ID, ledgerID); rec.Code != http.StatusOK {
		t.Fatalf("retry %d: %s", rec.Code, rec.Body.String())
	}
	entries := s.ledger.Entries()
	if len(entries) != 1 {
		t.Fatalf("entries %+v", entries)
	}
	accounts := map[string]bool{}
	for _, l := range entries[0].Lines {
		accounts[l.Account] = true
	}
	if !accounts["FIXED"] || accounts[glAccount(domain.AccountSalaryExpense)] {
		t.Fatalf("the corrected journal was not posted: %+v", entries[0].Lines)
	}
}

func TestRunPosting_AmbiguousFailuresKeepThePin(t *testing.T) {
	s := newRunServer(t, true)
	run := s.finalizedRun(workerA)
	for _, failure := range []error{
		&ledgerclient.Error{Op: "post", Status: http.StatusServiceUnavailable},
		&ledgerclient.Error{Op: "post", Status: http.StatusConflict, Code: ledgerclient.CodeIdempotencyConflict},
		errors.New("connection reset"),
	} {
		s.ledger.FailPosts(failure)
		if rec := s.post(run.ID, ledgerID); rec.Code != http.StatusBadGateway {
			t.Fatalf("%v: %d", failure, rec.Code)
		}
		if got := decodeRun(t, s.do(http.MethodGet, "/v1/payroll/runs/"+run.ID, "", "carol", "payroll.run.read", "")); got.LedgerID != ledgerID {
			t.Fatalf("pin lost after %v: %+v", failure, got)
		}
	}
}

func TestRunPosting_LedgerMustExistAndBelongToTheRunsLegalEntity(t *testing.T) {
	s := newRunServer(t, true)
	run := s.finalizedRun(workerA)
	if rec := s.post(run.ID, otherLedgerID); rec.Code != http.StatusUnprocessableEntity || problemCode(rec) != store.CodeLedgerForeign {
		t.Fatalf("unknown ledger: %d %s", rec.Code, rec.Body.String())
	}
	s.ledger.AddLedger(otherLedgerID, "00000000-0000-0000-0000-0000000000e2")
	if rec := s.post(run.ID, otherLedgerID); rec.Code != http.StatusUnprocessableEntity || problemCode(rec) != store.CodeLedgerForeign {
		t.Fatalf("another entity's ledger: %d %s", rec.Code, rec.Body.String())
	}
	if got := decodeRun(t, s.do(http.MethodGet, "/v1/payroll/runs/"+run.ID, "", "carol", "payroll.run.read", "")); got.LedgerID != "" || len(s.ledger.Entries()) != 0 {
		t.Fatalf("a foreign ledger pinned or posted: %+v", got)
	}
	if rec := s.post(run.ID, ledgerID); rec.Code != http.StatusOK {
		t.Fatalf("correct ledger %d: %s", rec.Code, rec.Body.String())
	}
}

func TestRunPosting_PostedRunOnAnotherLedgerIsConflictAndCountsOnce(t *testing.T) {
	s := newRunServer(t, true)
	run := s.finalizedRun(workerA)
	s.ledger.AddLedger(otherLedgerID, legalEntity)
	if rec := s.post(run.ID, ledgerID); rec.Code != http.StatusOK {
		t.Fatalf("post %d: %s", rec.Code, rec.Body.String())
	}
	if rec := s.post(run.ID, ledgerID); rec.Code != http.StatusOK {
		t.Fatalf("replay %d: %s", rec.Code, rec.Body.String())
	}
	if rec := s.post(run.ID, otherLedgerID); rec.Code != http.StatusConflict || problemCode(rec) != store.CodeLedgerPinned {
		t.Fatalf("posted run, other ledger: %d %s", rec.Code, rec.Body.String())
	}
	if n := s.api.runsPosted.Load(); n != 1 {
		t.Fatalf("runsPosted %d, want 1", n)
	}
}

func TestReconciliation_CountsOnlyRunsPostedToTheReconciledLedger(t *testing.T) {
	s := newRunServer(t, true)
	s.ledger.AddLedger(otherLedgerID, legalEntity)
	first, second := s.finalizedRun(workerA), s.finalizedRun(workerB)
	if rec := s.post(first.ID, ledgerID); rec.Code != http.StatusOK {
		t.Fatalf("post A %d: %s", rec.Code, rec.Body.String())
	}
	if rec := s.post(second.ID, otherLedgerID); rec.Code != http.StatusOK {
		t.Fatalf("post B %d: %s", rec.Code, rec.Body.String())
	}
	for _, ledger := range []string{ledgerID, otherLedgerID} {
		rec := s.do(http.MethodGet, "/v1/payroll/gl-reconciliation?legalEntityId="+legalEntity+"&ledgerId="+ledger, "", "carol", "payroll.run.read", "")
		var v reconView
		if rec.Code != http.StatusOK || json.Unmarshal(rec.Body.Bytes(), &v) != nil || !v.Reconciled || v.PostedRuns != 1 {
			t.Fatalf("ledger %s: %d %s", ledger, rec.Code, rec.Body.String())
		}
	}
}
