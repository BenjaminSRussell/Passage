package api

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
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
}

func newRunServer(t *testing.T, withLedger bool) *runServer {
	t.Helper()
	admin, app := testkit.DBWithApp(t)
	if err := dbx.Migrate(context.Background(), admin, migrations.FS); err != nil {
		t.Fatal(err)
	}
	s := &runServer{t: t, ledger: ledgerclient.NewFake()}
	a := New(nil)
	if withLedger {
		a.WithRuns(store.New(app), s.ledger)
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
	req := httptest.NewRequest(method, path, bytes.NewBufferString(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set(tenant.HeaderTenant, testTenant)
	req.Header.Set(tenant.HeaderLegalEntity, legalEntity)
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
		if l.Account == domain.AccountNetPayClearing {
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
			{Account: domain.AccountSalaryExpense, Amount: decimal.RequireFromString("12.34")},
			{Account: "OTHER", Credit: true, Amount: decimal.RequireFromString("12.34")}}}); err != nil {
		t.Fatal(err)
	}
	drift := recon()
	if drift.Reconciled {
		t.Fatalf("a stray ledger line must break reconciliation: %+v", drift)
	}
	for _, a := range drift.Accounts {
		if a.Account == domain.AccountSalaryExpense && a.Difference != "12.34" {
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
