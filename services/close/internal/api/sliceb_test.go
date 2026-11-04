package api_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/platform/ledgerclient"
	"github.com/BenjaminSRussell/passage/platform/testkit"
	"github.com/BenjaminSRussell/passage/services/close/internal/api"
	"github.com/BenjaminSRussell/passage/services/close/internal/domain"
)

type memPeriods struct {
	rows    map[string][]domain.Row
	matrix  []domain.PeriodStatus
	reopens map[string]domain.StoredReopen
	expired int
}

func (m *memPeriods) PeriodMatrix(context.Context, string, string) ([]domain.PeriodStatus, error) {
	return m.matrix, nil
}
func (m *memPeriods) TransitionPeriod(_ context.Context, in domain.PeriodTransition) (domain.PeriodStatus, error) {
	if err := domain.ValidateTransition(m.rows[in.FiscalPeriodID], in.To, in.Module); err != nil {
		return domain.PeriodStatus{}, err
	}
	m.rows[in.FiscalPeriodID] = append(m.rows[in.FiscalPeriodID], domain.Row{Module: in.Module, Status: in.To})
	return domain.PeriodStatus{FiscalPeriodID: in.FiscalPeriodID, Module: in.Module, Status: in.To, LedgerID: in.LedgerID}, nil
}
func (m *memPeriods) RequestReopen(_ context.Context, in domain.StoredReopen) (domain.StoredReopen, error) {
	if err := domain.ValidateReopenReason(in.Reason); err != nil {
		return domain.StoredReopen{}, err
	}
	in.ID, in.Status, in.Approvals = ids.New(), "pending", []domain.ReopenApproval{}
	if m.reopens == nil {
		m.reopens = map[string]domain.StoredReopen{}
	}
	m.reopens[in.ID] = in
	return in, nil
}
func (m *memPeriods) ApproveReopen(_ context.Context, id, actor string) (domain.StoredReopen, error) {
	req := m.reopens[id]
	updated, done, err := domain.ApproveReopen(domain.ReopenRequest{RequestedBy: req.RequestedBy, Required: req.Required, Reason: req.Reason, Approvals: req.Approvals}, actor)
	if err != nil {
		return domain.StoredReopen{}, err
	}
	req.Approvals = updated.Approvals
	if done {
		req.Status = "approved"
	}
	m.reopens[id] = req
	return req, nil
}
func (m *memPeriods) RejectReopen(_ context.Context, id, actor string) (domain.StoredReopen, error) {
	req := m.reopens[id]
	req.Status = "rejected"
	m.reopens[id] = req
	return req, nil
}
func (m *memPeriods) ExpireReopened(context.Context) (int, error) { return m.expired, nil }

type memChecklists struct {
	templates map[string]domain.Template
	runs      map[string]domain.CloseRun
	tasks     map[string][]domain.BoardTask
}

func (m *memChecklists) PutTemplate(_ context.Context, in domain.Template) (domain.Template, error) {
	in.Version++
	if m.templates == nil {
		m.templates = map[string]domain.Template{}
	}
	m.templates[in.ID] = in
	return in, nil
}
func (m *memChecklists) GetTemplate(_ context.Context, id string) (domain.Template, error) {
	t, ok := m.templates[id]
	if !ok {
		return domain.Template{}, &domain.Error{Code: domain.CodeNoRun, Detail: "template not found"}
	}
	return t, nil
}
func (m *memChecklists) CreateRun(_ context.Context, templateID, fiscalPeriodID, actor string, entities []string, _, _ string) (domain.CloseRun, error) {
	tmpl, err := m.GetTemplate(context.Background(), templateID)
	if err != nil {
		return domain.CloseRun{}, err
	}
	specs := make([]domain.TaskTemplate, len(tmpl.Tasks))
	for i, t := range tmpl.Tasks {
		specs[i] = domain.TaskTemplate{ID: t.Key, Name: t.Name, DependsOn: t.DependsOn}
	}
	tasks, err := domain.Instantiate(specs, entities)
	if err != nil {
		return domain.CloseRun{}, err
	}
	run := domain.CloseRun{ID: ids.New(), TemplateID: templateID, FiscalPeriodID: fiscalPeriodID, LegalEntityIDs: entities, Status: "in_progress", TaskCount: len(tasks), CreatedBy: actor}
	board := make([]domain.BoardTask, len(tasks))
	for i, t := range tasks {
		board[i] = domain.BoardTask{ID: ids.New(), RunID: run.ID, LegalEntityID: t.Entity, TaskKey: t.TemplateID, Name: t.ID, Status: "not_started", DueAt: time.Now()}
	}
	if m.runs == nil {
		m.runs, m.tasks = map[string]domain.CloseRun{}, map[string][]domain.BoardTask{}
	}
	m.runs[run.ID], m.tasks[run.ID] = run, board
	return run, nil
}
func (m *memChecklists) ListTasks(_ context.Context, runID, _, _ string) ([]domain.BoardTask, error) {
	return m.tasks[runID], nil
}
func (m *memChecklists) find(taskID string) (string, int, error) {
	for runID, tasks := range m.tasks {
		for i, t := range tasks {
			if t.ID == taskID {
				return runID, i, nil
			}
		}
	}
	return "", 0, &domain.Error{Code: domain.CodeNoRun, Detail: "task not found"}
}
func (m *memChecklists) StartTask(_ context.Context, taskID, _ string) (domain.BoardTask, error) {
	runID, i, err := m.find(taskID)
	if err != nil {
		return domain.BoardTask{}, err
	}
	m.tasks[runID][i].Status = "in_progress"
	return m.tasks[runID][i], nil
}
func (m *memChecklists) CompleteTask(_ context.Context, taskID, _, _ string) (domain.BoardTask, error) {
	runID, i, err := m.find(taskID)
	if err != nil {
		return domain.BoardTask{}, err
	}
	m.tasks[runID][i].Status = "completed"
	return m.tasks[runID][i], nil
}
func (m *memChecklists) SkipTask(_ context.Context, taskID, _, reason string) (domain.BoardTask, error) {
	if reason == "" {
		return domain.BoardTask{}, &domain.Error{Code: domain.CodeValidation, Detail: "a skip reason is required"}
	}
	runID, i, err := m.find(taskID)
	if err != nil {
		return domain.BoardTask{}, err
	}
	m.tasks[runID][i].Status = "skipped"
	return m.tasks[runID][i], nil
}
func (m *memChecklists) ReassignTask(_ context.Context, taskID, _, assignee string) (domain.BoardTask, error) {
	runID, i, err := m.find(taskID)
	if err != nil {
		return domain.BoardTask{}, err
	}
	m.tasks[runID][i].AssigneeUserID = assignee
	return m.tasks[runID][i], nil
}

type memAllocs struct {
	rules map[string]domain.AllocationRule
	runs  map[string]domain.AllocationRun
}

func (m *memAllocs) PutAllocationRule(_ context.Context, in domain.AllocationRule) (domain.AllocationRule, error) {
	if m.rules == nil {
		m.rules = map[string]domain.AllocationRule{}
	}
	m.rules[in.ID] = in
	return in, nil
}
func (m *memAllocs) AllocationRule(_ context.Context, id string) (domain.AllocationRule, error) {
	r, ok := m.rules[id]
	if !ok {
		return domain.AllocationRule{}, &domain.Error{Code: domain.CodeNoRun, Detail: "allocation rule not found"}
	}
	return r, nil
}
func (m *memAllocs) SaveAllocationRun(_ context.Context, in domain.AllocationRun, _, hash string) (domain.AllocationRun, error) {
	if in.ID == "" {
		in.ID = ids.New()
	}
	if m.runs == nil {
		m.runs = map[string]domain.AllocationRun{}
	}
	for _, existing := range m.runs {
		if existing.RuleID == in.RuleID && existing.FiscalPeriodID == in.FiscalPeriodID && existing.PostingDate == in.PostingDate && existing.Mode == in.Mode && hash != "" {
			return existing, nil
		}
	}
	m.runs[in.ID] = in
	return in, nil
}
func (m *memAllocs) AllocationRun(_ context.Context, id string) (domain.AllocationRun, error) {
	r, ok := m.runs[id]
	if !ok {
		return domain.AllocationRun{}, &domain.Error{Code: domain.CodeNoRun, Detail: "allocation run not found"}
	}
	return r, nil
}
func (m *memAllocs) MarkAllocationPosted(_ context.Context, runID, entryID string) (domain.AllocationRun, error) {
	r := m.runs[runID]
	r.Status, r.EntryID = "posted", entryID
	m.runs[runID] = r
	return r, nil
}
func (m *memAllocs) MarkAllocationReversed(_ context.Context, runID, entryID string) (domain.AllocationRun, error) {
	r := m.runs[runID]
	r.Status, r.ReversalEntryID = "reversed", entryID
	m.runs[runID] = r
	return r, nil
}

type memAccruals struct {
	schemes map[string]domain.AccrualDefinition
	runs    map[string]domain.AccrualRun
	byKey   map[string]string
	parked  map[string]string
}

func (m *memAccruals) PutAccrualScheme(_ context.Context, in domain.AccrualDefinition) (domain.AccrualDefinition, error) {
	if m.schemes == nil {
		m.schemes = map[string]domain.AccrualDefinition{}
	}
	m.schemes[in.LedgerID+"/"+in.Code] = in
	return in, nil
}
func (m *memAccruals) AccrualScheme(_ context.Context, ledgerID, code string) (domain.AccrualDefinition, error) {
	s, ok := m.schemes[ledgerID+"/"+code]
	if !ok {
		return domain.AccrualDefinition{}, &domain.Error{Code: domain.CodeNoRun, Detail: "accrual scheme not found"}
	}
	return s, nil
}
func (m *memAccruals) SaveAccrualRun(_ context.Context, in domain.AccrualRun, key, _ string) (domain.AccrualRun, error) {
	if m.runs == nil {
		m.runs, m.byKey = map[string]domain.AccrualRun{}, map[string]string{}
	}
	if _, known := m.runs[in.ID]; known && in.ID != "" {
		m.runs[in.ID] = in
		return in, nil
	}
	if id, replay := m.byKey[key]; replay && key != "" {
		return m.runs[id], nil
	}
	in.ID, in.TenantID = ids.New(), testkit.TenantID(1)
	m.runs[in.ID], m.byKey[key] = in, in.ID
	return in, nil
}
func (m *memAccruals) AccrualRun(_ context.Context, id string) (domain.AccrualRun, error) {
	return m.runs[id], nil
}
func (m *memAccruals) DueAccrualRuns(context.Context) ([]domain.AccrualRun, error) {
	var out []domain.AccrualRun
	for _, run := range m.runs {
		if run.Status == "scheduled" {
			out = append(out, run)
		}
	}
	return out, nil
}
func (m *memAccruals) RecordAccrualFailure(_ context.Context, id, message string, park bool) error {
	if park {
		run := m.runs[id]
		run.Status = "parked"
		m.runs[id] = run
		if m.parked == nil {
			m.parked = map[string]string{}
		}
		m.parked[id] = message
	}
	return nil
}

type memRegroup struct {
	rules   map[string]domain.StoredRegroupingRule
	runs    map[string]domain.RegroupingRun
	claimed map[string]bool
}

func (m *memRegroup) PutRegroupingRule(_ context.Context, in domain.StoredRegroupingRule) (domain.StoredRegroupingRule, error) {
	if m.rules == nil {
		m.rules = map[string]domain.StoredRegroupingRule{}
	}
	m.rules[in.LedgerID+"/"+in.Code] = in
	return in, nil
}
func (m *memRegroup) RegroupingRule(_ context.Context, ledgerID, code string) (domain.StoredRegroupingRule, error) {
	r, ok := m.rules[ledgerID+"/"+code]
	if !ok {
		return domain.StoredRegroupingRule{}, &domain.Error{Code: domain.CodeNoRun, Detail: "regrouping rule not found"}
	}
	return r, nil
}
func (m *memRegroup) ListRegroupingRules(ctx context.Context, ledgerID string, codes []string) ([]domain.StoredRegroupingRule, error) {
	out := make([]domain.StoredRegroupingRule, 0, len(codes))
	for _, c := range codes {
		r, err := m.RegroupingRule(ctx, ledgerID, c)
		if err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, nil
}
func (m *memRegroup) SaveRegroupingRun(_ context.Context, in domain.RegroupingRun, _, _ string) (domain.RegroupingRun, error) {
	if in.ID == "" {
		in.ID = ids.New()
	}
	if m.runs == nil {
		m.runs = map[string]domain.RegroupingRun{}
	}
	m.runs[in.ID] = in
	return in, nil
}
func (m *memRegroup) ClaimRegroupingRules(_ context.Context, runID string) error {
	run := m.runs[runID]
	for otherID, other := range m.runs {
		if otherID == runID || !m.claimed[otherID] || other.LedgerID != run.LedgerID || other.KeyDate != run.KeyDate {
			continue
		}
		for _, code := range run.RuleCodes {
			if slices.Contains(other.RuleCodes, code) {
				return &domain.Error{Code: domain.CodeRegroupingExists, Detail: "already claimed"}
			}
		}
	}
	if m.claimed == nil {
		m.claimed = map[string]bool{}
	}
	m.claimed[runID] = true
	return nil
}
func (m *memRegroup) ReleaseRegroupingRules(_ context.Context, runID string) error {
	delete(m.claimed, runID)
	return nil
}
func (m *memRegroup) RegroupingRun(_ context.Context, id string) (domain.RegroupingRun, error) {
	r, ok := m.runs[id]
	if !ok {
		return domain.RegroupingRun{}, &domain.Error{Code: domain.CodeNoRun, Detail: "regrouping run not found"}
	}
	return r, nil
}
func (m *memRegroup) MarkRegroupingPosted(_ context.Context, runID, entryID string) (domain.RegroupingRun, error) {
	r := m.runs[runID]
	r.Status, r.EntryID = "posted", entryID
	m.runs[runID] = r
	return r, nil
}
func (m *memRegroup) MarkRegroupingReversed(_ context.Context, runID, entryID string) (domain.RegroupingRun, error) {
	r := m.runs[runID]
	r.Status, r.ReversalEntryID = "reversed", entryID
	m.runs[runID] = r
	return r, nil
}

type memItems struct{ items []domain.OpenItem }

func (m memItems) List(context.Context, string, string, string) ([]domain.OpenItem, error) {
	return m.items, nil
}

type sliceB struct {
	clock   *time.Time
	api     *api.API
	periods *memPeriods
	checks  *memChecklists
	allocs  *memAllocs
	accrual *memAccruals
	regroup *memRegroup
	ledger  *chartLedger
	router  http.Handler
}

func newSliceB() sliceB { return newSliceBWith(memItems{}) }

func newSliceBWith(items api.OpenItemSource) sliceB {
	period := testkit.TenantID(8)
	clock := time.Date(2026, 3, 15, 12, 0, 0, 0, time.UTC)
	f := sliceB{
		clock: &clock,
		periods: &memPeriods{rows: map[string][]domain.Row{period: {
			{Module: domain.ModuleAll, Status: domain.StatusOpen},
			{Module: domain.ModuleGL, Status: domain.StatusOpen},
			{Module: domain.ModuleAP, Status: domain.StatusOpen},
		}}},
		checks:  &memChecklists{},
		allocs:  &memAllocs{},
		accrual: &memAccruals{},
		regroup: &memRegroup{},
		ledger: &chartLedger{
			Fake:       ledgerclient.NewFake(),
			info:       ledgerclient.Info{LegalEntityID: testkit.TenantID(9), ChartCode: "COA", Currency: "USD"},
			accounts:   map[string]ledgerclient.ChartAccount{"6100": {Type: "expense", Postable: true}, "1600": {Type: "asset", Postable: true}, "2100": {Type: "liability", Postable: true}},
			cumulative: []ledgerclient.BalanceRow{net("6100", 10000, nil)},
			thisYear:   []ledgerclient.BalanceRow{net("6100", 10000, nil)},
		},
	}
	store := fakeStore{spans: []domain.Span{span(period, domain.Row{Module: domain.ModuleAll, Status: domain.StatusOpen}, domain.Row{Module: domain.ModuleGL, Status: domain.StatusOpen})}}
	a := api.New(store).WithLedgers(fakeLedger{owner: testkit.TenantID(9)}).WithYearEnd(&memoryRuns{periods: closedYear()}, f.ledger).
		WithSliceB(f.periods, f.checks, f.allocs, f.accrual, f.regroup, items).WithClock(func() time.Time { return clock })
	r := httpx.NewRouter()
	a.Register(r)
	f.router, f.api = r, a
	return f
}

func (f sliceB) send(method, target, payload, duties, key string) *httptest.ResponseRecorder {
	return f.sendAs("alice", method, target, payload, duties, key)
}

func (f sliceB) sendAs(user, method, target, payload, duties, key string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, target, strings.NewReader(payload))
	req.Header.Set("X-Passage-Tenant", testkit.TenantID(1))
	req.Header.Set("X-Passage-Duties", duties)
	req.Header.Set("X-Passage-Legal-Entity", testkit.TenantID(9))
	req.Header.Set("X-Passage-User", user)
	if key != "" {
		req.Header.Set("Idempotency-Key", key)
	}
	rec := httptest.NewRecorder()
	f.router.ServeHTTP(rec, req)
	return rec
}

func TestPeriodMaintainAndReopen(t *testing.T) {
	f := newSliceB()
	period, ledger := testkit.TenantID(8), testkit.TenantID(7)
	rec := f.send(http.MethodPatch, "/v1/close/periods/"+period+"/modules/ap", `{"ledgerId":"`+ledger+`","status":"closed"}`, "period.maintain", "")
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"status":"closed"`) {
		t.Fatalf("close module: %d %s", rec.Code, rec.Body)
	}
	rec = f.send(http.MethodPost, "/v1/close/periods/"+period+"/modules/ap:request-reopen", `{"ledgerId":"`+ledger+`","reason":"audit adjustment after close"}`, "period.reopen", "reopen-1")
	if rec.Code != http.StatusCreated {
		t.Fatalf("request reopen: %d %s", rec.Code, rec.Body)
	}
	var req domain.StoredReopen
	if err := json.Unmarshal(rec.Body.Bytes(), &req); err != nil {
		t.Fatal(err)
	}
	rec = f.send(http.MethodPost, "/v1/close/reopen-requests/"+req.ID+":approve", `{}`, "period.reopen", "")
	if rec.Code != http.StatusConflict {
		t.Fatalf("requester cannot approve: %d %s", rec.Code, rec.Body)
	}
}

func TestChecklistBoard(t *testing.T) {
	f := newSliceB()
	tmpl := testkit.TenantID(21)
	rec := f.send(http.MethodPut, "/v1/close/templates/"+tmpl, `{"name":"month-end","tasks":[{"key":"prep","name":"Prep","module":"gl","assigneeRole":"controller","dependsOn":[]}]}`, "close.checklist.write", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("template: %d %s", rec.Code, rec.Body)
	}
	rec = f.send(http.MethodPost, "/v1/close/runs", `{"templateId":"`+tmpl+`","fiscalPeriodId":"`+testkit.TenantID(8)+`","legalEntityIds":["`+testkit.TenantID(9)+`"]}`, "close.checklist.write", "run-1")
	if rec.Code != http.StatusCreated {
		t.Fatalf("run: %d %s", rec.Code, rec.Body)
	}
	var run domain.CloseRun
	if err := json.Unmarshal(rec.Body.Bytes(), &run); err != nil || run.TaskCount != 1 {
		t.Fatalf("run %+v %v", run, err)
	}
	rec = f.send(http.MethodGet, "/v1/close/runs/"+run.ID+"/tasks", "", "close.checklist.read", "")
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"items"`) {
		t.Fatalf("board: %d %s", rec.Code, rec.Body)
	}
}

func TestAllocationPostsThroughLedgerWithoutPostClosed(t *testing.T) {
	f := newSliceB()
	ruleID, ledger, period := testkit.TenantID(31), testkit.TenantID(7), testkit.TenantID(8)
	body := `{"ledgerId":"` + ledger + `","name":"rent","method":"fixed_weight","sourceAccounts":["6100"],"destinations":[{"key":"A","account":"6100","weight":"3"},{"key":"B","account":"6100","weight":"5"},{"key":"C","account":"6100","weight":"2"}],"offsetAccount":"6100"}`
	rec := f.send(http.MethodPut, "/v1/close/allocation-rules/"+ruleID, body, "close.allocation.write", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("rule: %d %s", rec.Code, rec.Body)
	}
	rec = f.send(http.MethodPost, "/v1/close/allocation-runs", `{"ledgerId":"`+ledger+`","ruleId":"`+ruleID+`","fiscalPeriodId":"`+period+`","postingDate":"2026-01-31","mode":"post"}`, "close.allocation.run", "alloc-1")
	if rec.Code != http.StatusCreated {
		t.Fatalf("run: %d %s", rec.Code, rec.Body)
	}
	posted := f.ledger.Entries()
	if len(posted) != 1 || posted[0].PostClosed || posted[0].SourceType != "allocation" {
		t.Fatalf("posted %+v", posted)
	}
	if posted[0].PostingDate != "2026-01-31" {
		t.Fatalf("date %s", posted[0].PostingDate)
	}
}

func TestAllocationClosedPeriodIsRejectedBeforeTheLedger(t *testing.T) {
	f := newSliceB()
	f.router = nil
	closed := fakeStore{spans: []domain.Span{span(testkit.TenantID(8), domain.Row{Module: domain.ModuleAll, Status: domain.StatusClosed})}}
	a := api.New(closed).WithLedgers(fakeLedger{owner: testkit.TenantID(9)}).WithYearEnd(&memoryRuns{}, f.ledger).
		WithSliceB(f.periods, f.checks, f.allocs, f.accrual, f.regroup, nil)
	r := httpx.NewRouter()
	a.Register(r)
	f.router = r
	ruleID, ledger, period := testkit.TenantID(31), testkit.TenantID(7), testkit.TenantID(8)
	f.send(http.MethodPut, "/v1/close/allocation-rules/"+ruleID, `{"ledgerId":"`+ledger+`","name":"rent","sourceAccounts":["6100"],"destinations":[{"key":"A","account":"6100","weight":"1"}],"offsetAccount":"6100"}`, "close.allocation.write", "")
	rec := f.send(http.MethodPost, "/v1/close/allocation-runs", `{"ledgerId":"`+ledger+`","ruleId":"`+ruleID+`","fiscalPeriodId":"`+period+`","postingDate":"2026-01-31","mode":"post"}`, "close.allocation.run", "alloc-closed")
	if rec.Code != http.StatusUnprocessableEntity || !strings.Contains(rec.Body.String(), "LEDGER-0013") {
		t.Fatalf("closed period must reject allocation posting: %d %s", rec.Code, rec.Body)
	}
	if n := len(f.ledger.Entries()); n != 0 {
		t.Fatalf("ledger must not receive a closed-period allocation, got %d entries", n)
	}
}

func TestAccrualSpreadsAndPostsWithoutPostClosed(t *testing.T) {
	f := newSliceB()
	ledger := testkit.TenantID(7)
	rec := f.send(http.MethodPut, "/v1/close/accrual-schemes/rent", `{"ledgerId":"`+ledger+`","debitAccount":"6100","creditAccount":"2100","amount":"120.00","occurrences":2,"spread":"equal","autoReverse":false}`, "close.accrual.write", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("scheme: %d %s", rec.Code, rec.Body)
	}
	rec = f.send(http.MethodPost, "/v1/close/accrual-schemes/rent:apply", `{"ledgerId":"`+ledger+`","startDate":"2026-01-01"}`, "close.accrual.run", "accrual-1")
	if rec.Code != http.StatusCreated {
		t.Fatalf("apply: %d %s", rec.Code, rec.Body)
	}
	posted := f.ledger.Entries()
	if len(posted) != 2 {
		t.Fatalf("want 2 occurrences, got %d", len(posted))
	}
	for _, e := range posted {
		if e.PostClosed {
			t.Fatalf("accrual must not set PostClosed: %+v", e.Entry)
		}
	}
}

func TestRegroupingAccountSignPostsWithoutPostClosed(t *testing.T) {
	f := newSliceB()
	f.ledger.cumulative = []ledgerclient.BalanceRow{net("1100", -500, nil)}
	ledger := testkit.TenantID(7)
	rec := f.send(http.MethodPut, "/v1/close/regrouping-rules/BANK", `{"ledgerId":"`+ledger+`","sortMethod":"account_balance_sign","sourceAccount":"1100","adjustmentAccount":"2500","targetAccount":"1100","normalBalance":"debit","sequence":1}`, "close.regrouping.write", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("rule: %d %s", rec.Code, rec.Body)
	}
	rec = f.send(http.MethodPost, "/v1/close/regroupings", `{"ledgerId":"`+ledger+`","keyDate":"2026-01-31","ruleCodes":["BANK"],"mode":"post"}`, "close.regrouping.run", "rg-1")
	if rec.Code != http.StatusCreated {
		t.Fatalf("run: %d %s", rec.Code, rec.Body)
	}
	posted := f.ledger.Entries()
	if len(posted) != 1 || posted[0].PostClosed || posted[0].SourceType != "regrouping" {
		t.Fatalf("posted %+v", posted)
	}
	assertSides(t, posted[0].Lines, "1100", "2500", "500")
}

func assertSides(t *testing.T, lines []ledgerclient.Line, debit, credit, amount string) {
	t.Helper()
	if len(lines) != 2 || lines[0].Credit || !lines[1].Credit || lines[0].Account != debit || lines[1].Account != credit ||
		!lines[0].Amount.Equal(decimal.RequireFromString(amount)) || !lines[1].Amount.Equal(decimal.RequireFromString(amount)) {
		t.Fatalf("want Dr %s / Cr %s for %s, got %+v", debit, credit, amount, lines)
	}
}

func postRegroupRule(f sliceB, rule, balanceAccount string, balance int64, normal string) *httptest.ResponseRecorder {
	f.ledger.cumulative = []ledgerclient.BalanceRow{net(balanceAccount, balance, nil)}
	ledger := testkit.TenantID(7)
	f.send(http.MethodPut, "/v1/close/regrouping-rules/"+rule, `{"ledgerId":"`+ledger+`","sortMethod":"account_balance_sign","sourceAccount":"`+balanceAccount+`","adjustmentAccount":"2500","targetAccount":"`+balanceAccount+`","normalBalance":"`+normal+`","sequence":1}`, "close.regrouping.write", "")
	return f.send(http.MethodPost, "/v1/close/regroupings", `{"ledgerId":"`+ledger+`","keyDate":"2026-01-31","ruleCodes":["`+rule+`"],"mode":"post"}`, "close.regrouping.run", "rg-"+rule)
}

func TestRegroupingDebitBalanceOnCreditAccountDebitsTheAdjustment(t *testing.T) {
	f := newSliceB()
	if rec := postRegroupRule(f, "VEND", "2000", 300, "credit"); rec.Code != http.StatusCreated {
		t.Fatalf("run: %d %s", rec.Code, rec.Body)
	}
	assertSides(t, f.ledger.Entries()[0].Lines, "2500", "2000", "300")
}

func TestRegroupingRuleRunsClaimTheirRulesPerKeyDate(t *testing.T) {
	f := newSliceB()
	if rec := postRegroupRule(f, "BANK", "1100", -500, "debit"); rec.Code != http.StatusCreated {
		t.Fatalf("first: %d %s", rec.Code, rec.Body)
	}
	rec := f.send(http.MethodPost, "/v1/close/regroupings", `{"ledgerId":"`+testkit.TenantID(7)+`","keyDate":"2026-01-31","ruleCodes":["BANK"],"mode":"post"}`, "close.regrouping.run", "rg-again")
	if rec.Code != http.StatusConflict || !strings.Contains(rec.Body.String(), "CLOSE-0010") {
		t.Fatalf("second run on the same key date: %d %s", rec.Code, rec.Body)
	}
	if n := len(f.ledger.Entries()); n != 1 {
		t.Fatalf("the ledger must receive one regrouping, got %d", n)
	}
}

func TestRegroupingItemMethodsRefuseWithoutAnOpenItemSource(t *testing.T) {
	f := newSliceBWith(nil)
	ledger := testkit.TenantID(7)
	f.send(http.MethodPut, "/v1/close/regrouping-rules/LT", `{"ledgerId":"`+ledger+`","sortMethod":"remaining_term","sourceAccount":"2100","buckets":[{"fromDays":366,"toDays":0,"adjustmentAccount":"1600","targetAccount":"2100"}],"sequence":1}`, "close.regrouping.write", "")
	rec := f.send(http.MethodPost, "/v1/close/regroupings", `{"ledgerId":"`+ledger+`","keyDate":"2026-01-31","ruleCodes":["LT"],"mode":"post"}`, "close.regrouping.run", "rg-lt")
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("got %d %s", rec.Code, rec.Body)
	}
	if len(f.regroup.runs) != 0 || len(f.ledger.Entries()) != 0 {
		t.Fatalf("nothing may be recorded or posted: %d runs, %d entries", len(f.regroup.runs), len(f.ledger.Entries()))
	}
}

func TestRegroupingWithNothingToReclassifyIsNotPosted(t *testing.T) {
	f := newSliceB()
	if rec := postRegroupRule(f, "BANK", "1100", 500, "debit"); rec.Code != http.StatusUnprocessableEntity || !strings.Contains(rec.Body.String(), "CLOSE-0011") {
		t.Fatalf("got %d %s", rec.Code, rec.Body)
	}
	if len(f.regroup.runs) != 0 {
		t.Fatal("an empty post run must not be recorded")
	}
}

func TestRegroupingRejectsDuplicateRuleCodes(t *testing.T) {
	f := newSliceB()
	rec := f.send(http.MethodPost, "/v1/close/regroupings", `{"ledgerId":"`+testkit.TenantID(7)+`","keyDate":"2026-01-31","ruleCodes":["A","A"]}`, "close.regrouping.run", "rg-dup")
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("got %d %s", rec.Code, rec.Body)
	}
}

func TestWriteEndpointsRequireIdempotencyKey(t *testing.T) {
	f := newSliceB()
	rec := f.send(http.MethodPost, "/v1/close/allocation-runs", `{"ledgerId":"`+testkit.TenantID(7)+`","ruleId":"`+testkit.TenantID(31)+`","fiscalPeriodId":"`+testkit.TenantID(8)+`","postingDate":"2026-01-31"}`, "close.allocation.run", "")
	if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), "Idempotency-Key") {
		t.Fatalf("got %d %s", rec.Code, rec.Body)
	}
}

func TestAllocationDutyDoesNotAllowRun(t *testing.T) {
	f := newSliceB()
	rec := f.send(http.MethodPost, "/v1/close/allocation-runs", `{"ledgerId":"`+testkit.TenantID(7)+`"}`, "close.allocation.write", "k")
	if rec.Code != http.StatusForbidden {
		t.Fatalf("got %d %s", rec.Code, rec.Body)
	}
}

func TestProposeAllocationMatchesFixtureWeights(t *testing.T) {
	got, err := domain.ProposeAllocation(decimal.NewFromInt(10000), []domain.AllocationDest{
		{Key: "A", Account: "6100", Weight: decimal.NewFromInt(3)},
		{Key: "B", Account: "6100", Weight: decimal.NewFromInt(5)},
		{Key: "C", Account: "6100", Weight: decimal.NewFromInt(2)},
	}, "6100", "", 2)
	if err != nil || !got.Destinations[0].Amount.Equal(decimal.NewFromInt(3000)) || !got.Destinations[1].Amount.Equal(decimal.NewFromInt(5000)) {
		t.Fatalf("%+v %v", got, err)
	}
}

func TestTwoApproverReopenOverHTTP(t *testing.T) {
	f := newSliceB()
	period, ledger := testkit.TenantID(8), testkit.TenantID(7)
	target := "/v1/close/periods/" + period + "/modules/ap:request-reopen"
	rec := f.send(http.MethodPost, target, `{"ledgerId":"`+ledger+`","reason":"audit adjustment after close"}`, "period.reopen", "reopen-http")
	if rec.Code != http.StatusCreated {
		t.Fatalf("request: %d %s", rec.Code, rec.Body)
	}
	var req domain.StoredReopen
	if err := json.Unmarshal(rec.Body.Bytes(), &req); err != nil || req.Required != 2 {
		t.Fatalf("request body %+v %v", req, err)
	}
	approve := func(user string) (*httptest.ResponseRecorder, domain.StoredReopen) {
		rec := f.sendAs(user, http.MethodPost, "/v1/close/reopen-requests/"+req.ID+":approve", `{}`, "period.reopen", "")
		var out domain.StoredReopen
		_ = json.Unmarshal(rec.Body.Bytes(), &out)
		return rec, out
	}
	if rec, _ := approve("alice"); rec.Code != http.StatusConflict {
		t.Fatalf("the requester cannot approve: %d %s", rec.Code, rec.Body)
	}
	if rec, out := approve("bob"); rec.Code != http.StatusOK || out.Status != "pending" {
		t.Fatalf("first approval keeps the request pending: %d %s", rec.Code, rec.Body)
	}
	if rec, out := approve("cara"); rec.Code != http.StatusOK || out.Status != "approved" {
		t.Fatalf("second approver completes it: %d %s", rec.Code, rec.Body)
	}
}

func TestReopenNeedsAnEndUser(t *testing.T) {
	f := newSliceB()
	rec := f.sendAs("", http.MethodPost, "/v1/close/periods/"+testkit.TenantID(8)+"/modules/ap:request-reopen", `{"ledgerId":"`+testkit.TenantID(7)+`","reason":"audit adjustment after close"}`, "period.reopen", "reopen-nouser")
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("got %d %s", rec.Code, rec.Body)
	}
}

func putAccrualScheme(f sliceB, code, body string) *httptest.ResponseRecorder {
	return f.send(http.MethodPut, "/v1/close/accrual-schemes/"+code, body, "close.accrual.write", "")
}

func TestAccrualMonthEndPostsOnlyDueOccurrencesAndTheSweeperPostsTheRest(t *testing.T) {
	f := newSliceB()
	ledger := testkit.TenantID(7)
	if rec := putAccrualScheme(f, "rent", `{"ledgerId":"`+ledger+`","debitAccount":"6100","creditAccount":"2100","amount":"300.00","occurrences":3,"spread":"equal","autoReverse":true}`); rec.Code != http.StatusOK {
		t.Fatalf("scheme: %d %s", rec.Code, rec.Body)
	}
	*f.clock = time.Date(2026, 2, 15, 9, 0, 0, 0, time.UTC)
	rec := f.send(http.MethodPost, "/v1/close/accrual-schemes/rent:apply", `{"ledgerId":"`+ledger+`","startDate":"2026-01-31"}`, "close.accrual.run", "accrual-eom")
	if rec.Code != http.StatusCreated {
		t.Fatalf("apply: %d %s", rec.Code, rec.Body)
	}
	var run domain.AccrualRun
	if err := json.Unmarshal(rec.Body.Bytes(), &run); err != nil || run.Status != "scheduled" || run.Currency != "USD" {
		t.Fatalf("run %+v %v", run, err)
	}
	if got := postingDates(f); !slices.Equal(got, []string{"2026-01-31"}) {
		t.Fatalf("only the January occurrence is due on 15 February, posted %v", got)
	}
	replay := f.send(http.MethodPost, "/v1/close/accrual-schemes/rent:apply", `{"ledgerId":"`+ledger+`","startDate":"2026-01-31"}`, "close.accrual.run", "accrual-eom")
	if replay.Code != http.StatusCreated || len(f.ledger.Entries()) != 1 {
		t.Fatalf("replay must not post again: %d, %d entries", replay.Code, len(f.ledger.Entries()))
	}

	*f.clock = time.Date(2026, 3, 1, 9, 0, 0, 0, time.UTC)
	if done, err := f.api.PostDueAccruals(context.Background()); err != nil || done != 0 {
		t.Fatalf("sweep: %d %v", done, err)
	}
	if got := postingDates(f); !slices.Equal(got, []string{"2026-01-31", "2026-02-28", "2026-02-28"}) {
		t.Fatalf("the sweeper posts February's occurrence and January's reversal on 28 February, got %v", got)
	}

	*f.clock = time.Date(2026, 6, 1, 9, 0, 0, 0, time.UTC)
	if done, err := f.api.PostDueAccruals(context.Background()); err != nil || done != 1 {
		t.Fatalf("final sweep: %d %v", done, err)
	}
	stored := f.accrual.runs[run.ID]
	if stored.Status != "posted" || len(stored.EntryIDs) != 6 {
		t.Fatalf("run %s with %d entries", stored.Status, len(stored.EntryIDs))
	}
	for _, e := range f.ledger.Entries() {
		if e.SourceType == "accrual" && !e.Lines[0].Amount.Equal(decimal.NewFromInt(100)) {
			t.Fatalf("each occurrence is 100.00, got %+v", e.Lines)
		}
	}
}

func postingDates(f sliceB) []string {
	var dates []string
	for _, e := range f.ledger.Entries() {
		dates = append(dates, e.PostingDate)
	}
	slices.Sort(dates)
	return dates
}

func TestAccrualApplyIgnoresCallerAmountAndRefusesForeignCurrency(t *testing.T) {
	f := newSliceB()
	ledger := testkit.TenantID(7)
	putAccrualScheme(f, "rent", `{"ledgerId":"`+ledger+`","debitAccount":"6100","creditAccount":"2100","amount":"120.00","occurrences":2,"spread":"equal","autoReverse":false}`)
	rec := f.send(http.MethodPost, "/v1/close/accrual-schemes/rent:apply", `{"ledgerId":"`+ledger+`","amount":"999999.00","startDate":"2026-01-01"}`, "close.accrual.run", "accrual-amount")
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("a request amount is not accepted: %d %s", rec.Code, rec.Body)
	}
	rec = f.send(http.MethodPost, "/v1/close/accrual-schemes/rent:apply", `{"ledgerId":"`+ledger+`","currency":"EUR","startDate":"2026-01-01"}`, "close.accrual.run", "accrual-eur")
	if rec.Code != http.StatusUnprocessableEntity || !strings.Contains(rec.Body.String(), "CLOSE-0011") {
		t.Fatalf("a foreign currency is refused: %d %s", rec.Code, rec.Body)
	}
	if n := len(f.ledger.Entries()); n != 0 {
		t.Fatalf("nothing may post, got %d", n)
	}
	rec = f.send(http.MethodPost, "/v1/close/accrual-schemes/rent:apply", `{"ledgerId":"`+ledger+`","startDate":"2026-01-01"}`, "close.accrual.run", "accrual-ok")
	if rec.Code != http.StatusCreated {
		t.Fatalf("apply: %d %s", rec.Code, rec.Body)
	}
	for _, e := range f.ledger.Entries() {
		if !e.Lines[0].Amount.Equal(decimal.NewFromInt(60)) || e.Currency != "USD" {
			t.Fatalf("the stored amount spreads in the ledger currency: %+v", e)
		}
	}
}

func TestAccrualSchemeRequiresAPositiveAmount(t *testing.T) {
	f := newSliceB()
	rec := putAccrualScheme(f, "rent", `{"ledgerId":"`+testkit.TenantID(7)+`","debitAccount":"6100","creditAccount":"2100","occurrences":2}`)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("got %d %s", rec.Code, rec.Body)
	}
}

func TestAccrualPermanentLedgerRefusalParksTheRun(t *testing.T) {
	f := newSliceB()
	ledger := testkit.TenantID(7)
	putAccrualScheme(f, "rent", `{"ledgerId":"`+ledger+`","debitAccount":"6100","creditAccount":"2100","amount":"120.00","occurrences":2,"spread":"equal","autoReverse":false}`)
	*f.clock = time.Date(2026, 1, 15, 9, 0, 0, 0, time.UTC)
	f.send(http.MethodPost, "/v1/close/accrual-schemes/rent:apply", `{"ledgerId":"`+ledger+`","startDate":"2026-01-01"}`, "close.accrual.run", "accrual-park")
	f.ledger.FailPosts(&ledgerclient.Error{Op: "post", Status: http.StatusUnprocessableEntity, Code: "LEDGER-0001"})
	*f.clock = time.Date(2026, 2, 15, 9, 0, 0, 0, time.UTC)
	if _, err := f.api.PostDueAccruals(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(f.accrual.parked) != 1 {
		t.Fatalf("a permanent refusal parks the run, parked %v", f.accrual.parked)
	}
}

func TestAllocationRefusesALedgerOrPeriodThatDoesNotMatchTheRule(t *testing.T) {
	f := newSliceB()
	ruleID, ledger, period := testkit.TenantID(31), testkit.TenantID(7), testkit.TenantID(8)
	rule := `{"ledgerId":"` + ledger + `","name":"rent","method":"fixed_weight","sourceAccounts":["6100"],"destinations":[{"key":"A","account":"6100","weight":"1"}],"offsetAccount":"6100"}`
	if rec := f.send(http.MethodPut, "/v1/close/allocation-rules/"+ruleID, rule, "close.allocation.write", ""); rec.Code != http.StatusOK {
		t.Fatalf("rule: %d %s", rec.Code, rec.Body)
	}
	other := testkit.TenantID(12)
	rec := f.send(http.MethodPost, "/v1/close/allocation-runs", `{"ledgerId":"`+other+`","ruleId":"`+ruleID+`","fiscalPeriodId":"`+period+`","postingDate":"2026-01-31","mode":"post"}`, "close.allocation.run", "alloc-ledger")
	if rec.Code != http.StatusUnprocessableEntity || !strings.Contains(rec.Body.String(), "CLOSE-0011") {
		t.Fatalf("ledger mismatch: %d %s", rec.Code, rec.Body)
	}
	rec = f.send(http.MethodPost, "/v1/close/allocation-runs", `{"ledgerId":"`+ledger+`","ruleId":"`+ruleID+`","fiscalPeriodId":"`+testkit.TenantID(13)+`","postingDate":"2026-01-31","mode":"post"}`, "close.allocation.run", "alloc-period")
	if rec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("period mismatch: %d %s", rec.Code, rec.Body)
	}
	if n := len(f.ledger.Entries()); n != 0 {
		t.Fatalf("nothing may post, got %d", n)
	}
}

func TestAllocationRuleValidation(t *testing.T) {
	f := newSliceB()
	ledger := testkit.TenantID(7)
	base := `"ledgerId":"` + ledger + `","name":"rent","method":"fixed_weight","offsetAccount":"6100"`
	tests := map[string]string{
		"no source accounts":    `{` + base + `,"destinations":[{"key":"A","account":"6100","weight":"1"}]}`,
		"no destinations":       `{` + base + `,"sourceAccounts":["6100"],"destinations":[]}`,
		"duplicate destination": `{` + base + `,"sourceAccounts":["6100"],"destinations":[{"key":"A","account":"6100","weight":"1"},{"key":"A","account":"6100","weight":"2"}]}`,
	}
	for name, body := range tests {
		if rec := f.send(http.MethodPut, "/v1/close/allocation-rules/"+testkit.TenantID(31), body, "close.allocation.write", ""); rec.Code != http.StatusBadRequest {
			t.Errorf("%s: got %d %s", name, rec.Code, rec.Body)
		}
	}
}

func TestMalformedIdentifiersAreClientErrors(t *testing.T) {
	f := newSliceB()
	if rec := f.send(http.MethodGet, "/v1/close/allocation-rules/not-a-uuid", "", "close.allocation.write", ""); rec.Code != http.StatusNotFound {
		t.Errorf("allocation rule: %d", rec.Code)
	}
	if rec := f.send(http.MethodGet, "/v1/close/accrual-schemes/rent?ledgerId=bad", "", "close.accrual.write", ""); rec.Code != http.StatusBadRequest {
		t.Errorf("accrual scheme: %d", rec.Code)
	}
	if rec := f.send(http.MethodGet, "/v1/close/regrouping-rules/BANK?ledgerId=bad", "", "close.regrouping.write", ""); rec.Code != http.StatusBadRequest {
		t.Errorf("regrouping rule: %d", rec.Code)
	}
	if rec := f.send(http.MethodPost, "/v1/close/regroupings/not-a-uuid:post", "", "close.regrouping.run", ""); rec.Code != http.StatusNotFound {
		t.Errorf("regrouping run: %d", rec.Code)
	}
}

func TestChecklistRunRefusesLegalEntitiesOutsideTheCallerScope(t *testing.T) {
	f := newSliceB()
	tmpl := testkit.TenantID(21)
	f.send(http.MethodPut, "/v1/close/templates/"+tmpl, `{"name":"month-end","tasks":[{"key":"prep","name":"Prep","module":"gl","assigneeRole":"controller","dependsOn":[]}]}`, "close.checklist.write", "")
	rec := f.send(http.MethodPost, "/v1/close/runs", `{"templateId":"`+tmpl+`","fiscalPeriodId":"`+testkit.TenantID(8)+`","legalEntityIds":["`+testkit.TenantID(10)+`"]}`, "close.checklist.write", "run-scope")
	if rec.Code != http.StatusForbidden {
		t.Fatalf("got %d %s", rec.Code, rec.Body)
	}
}
