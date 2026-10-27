package api

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/platform/money"
	"github.com/BenjaminSRussell/passage/platform/tenant"
	"github.com/BenjaminSRussell/passage/services/treasury/internal/domain"
	"github.com/BenjaminSRussell/passage/services/treasury/internal/store"
)

// ---- in-memory store methods for checks, facilities, instruments and loans ----

func (f *fakeStore) IssueCheck(_ context.Context, in store.NewCheck, key string) (store.Check, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if key != "" {
		if id, ok := f.byIdem["check:"+key]; ok {
			return f.checks[id], nil
		}
	}
	acct, ok := f.accounts[in.BankAccountID]
	switch {
	case !ok:
		return store.Check{}, httpx.NewProblem(http.StatusNotFound, store.CodeAccountNotFound, "bank account not found")
	case acct.Status != "active":
		return store.Check{}, httpx.NewProblem(http.StatusUnprocessableEntity, store.CodeAccountInactive, "checks can only be issued on an active bank account")
	case acct.Currency != in.Currency:
		return store.Check{}, httpx.NewProblem(http.StatusUnprocessableEntity, domain.CodeCheckCurrency, "check currency differs from the bank account currency")
	}
	n := f.nextNumber(acct.ID)
	c := store.Check{
		ID: ids.New(), LegalEntityID: acct.LegalEntityID, BankAccountID: acct.ID, CheckNumber: n,
		Payee: in.Payee, Amount: in.Amount, Currency: in.Currency, Status: string(domain.CheckCreated), Version: 1,
	}
	f.checks[c.ID] = c
	if key != "" {
		f.byIdem["check:"+key] = c.ID
	}
	return c, nil
}

// nextNumber returns the next gap-free check number; the caller holds f.mu.
func (f *fakeStore) nextNumber(accountID string) int64 {
	n := f.nextNo[accountID]
	if n == 0 {
		n = 1001
	}
	f.nextNo[accountID] = n + 1
	return n
}

func (f *fakeStore) ListChecks(_ context.Context, bankAccountID string) ([]store.Check, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := []store.Check{}
	for _, c := range f.checks {
		if c.BankAccountID == bankAccountID {
			out = append(out, c)
		}
	}
	return out, nil
}

func (f *fakeStore) GetCheck(_ context.Context, id string) (store.Check, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	c, ok := f.checks[id]
	if !ok {
		return store.Check{}, httpx.NewProblem(http.StatusNotFound, store.CodeCheckNotFound, "check not found")
	}
	return c, nil
}

// transitionCheck moves a check under the version check; the caller holds f.mu.
func (f *fakeStore) transitionCheck(id string, version int, to domain.CheckStatus) (store.Check, error) {
	c, ok := f.checks[id]
	if !ok {
		return store.Check{}, httpx.NewProblem(http.StatusNotFound, store.CodeCheckNotFound, "check not found")
	}
	if c.Version != version {
		return store.Check{}, httpx.NewProblem(http.StatusPreconditionFailed, store.CodeVersionConflict, "stale version")
	}
	if err := domain.TransitionCheck(domain.CheckStatus(c.Status), to); err != nil {
		return store.Check{}, err
	}
	c.Status = string(to)
	c.Version++
	f.checks[id] = c
	return c, nil
}

func (f *fakeStore) PrintCheck(_ context.Context, id string, version int) (store.Check, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.transitionCheck(id, version, domain.CheckPrinted)
}

func (f *fakeStore) SendCheck(_ context.Context, id string, version int) (store.Check, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.transitionCheck(id, version, domain.CheckSent)
}

func (f *fakeStore) ClearCheck(_ context.Context, id string, version int) (store.Check, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.transitionCheck(id, version, domain.CheckCleared)
}

func (f *fakeStore) StopCheck(_ context.Context, id string, version int) (store.Check, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.transitionCheck(id, version, domain.CheckStopped)
}

func (f *fakeStore) VoidCheck(_ context.Context, id string, version int, reason string) (store.Check, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	c, err := f.transitionCheck(id, version, domain.CheckVoided)
	if err != nil {
		return c, err
	}
	c.VoidReason = &reason
	f.checks[id] = c
	return c, nil
}

func (f *fakeStore) ReprintCheck(_ context.Context, id string, version int, reason string) (store.Check, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	old, ok := f.checks[id]
	if !ok {
		return store.Check{}, httpx.NewProblem(http.StatusNotFound, store.CodeCheckNotFound, "check not found")
	}
	if old.Version != version {
		return store.Check{}, httpx.NewProblem(http.StatusPreconditionFailed, store.CodeVersionConflict, "stale version")
	}
	if err := domain.TransitionCheck(domain.CheckStatus(old.Status), domain.CheckVoided); err != nil {
		return store.Check{}, err
	}
	oldID := old.ID
	fresh := store.Check{
		ID: ids.New(), LegalEntityID: old.LegalEntityID, BankAccountID: old.BankAccountID, CheckNumber: f.nextNumber(old.BankAccountID),
		Payee: old.Payee, Amount: old.Amount, Currency: old.Currency, Status: string(domain.CheckPrinted), ReplacesID: &oldID, Version: 1,
	}
	f.checks[fresh.ID] = fresh
	replacedBy := fresh.ID
	old.Status = string(domain.CheckVoided)
	old.VoidReason = &reason
	old.ReplacedByID = &replacedBy
	old.Version++
	f.checks[id] = old
	return fresh, nil
}

// facilityView fills utilization and headroom from open instruments; the caller holds f.mu.
func (f *fakeStore) facilityView(fac store.Facility) store.Facility {
	utilized := decimal.Zero
	for _, i := range f.instruments {
		if i.FacilityID == fac.ID && i.Status == "open" {
			utilized = utilized.Add(decimal.RequireFromString(i.Amount))
		}
	}
	limit := decimal.RequireFromString(fac.Limit)
	places := money.MinorUnits(fac.Currency)
	fac.Utilized = utilized.StringFixed(places)
	fac.Headroom = domain.Headroom(limit, utilized).StringFixed(places)
	return fac
}

func (f *fakeStore) CreateFacility(_ context.Context, in store.NewFacility, key string) (store.Facility, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if key != "" {
		if id, ok := f.byIdem["facility:"+key]; ok {
			return f.facilityView(f.facilities[id]), nil
		}
	}
	fac := store.Facility{
		ID: ids.New(), LegalEntityID: in.LegalEntityID, Code: in.Code, BankName: in.BankName, Currency: in.Currency,
		Limit: in.Limit, ExpiryDate: in.ExpiryDate, Status: "active", Version: 1,
	}
	f.facilities[fac.ID] = fac
	if key != "" {
		f.byIdem["facility:"+key] = fac.ID
	}
	return f.facilityView(fac), nil
}

func (f *fakeStore) ListFacilities(_ context.Context, legalEntityID string) ([]store.Facility, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := []store.Facility{}
	for _, fac := range f.facilities {
		if fac.LegalEntityID == legalEntityID {
			out = append(out, f.facilityView(fac))
		}
	}
	return out, nil
}

func (f *fakeStore) GetFacility(_ context.Context, id string) (store.Facility, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	fac, ok := f.facilities[id]
	if !ok {
		return store.Facility{}, httpx.NewProblem(http.StatusNotFound, store.CodeFacilityNotFound, "facility not found")
	}
	return f.facilityView(fac), nil
}

// CreateInstrument applies the facility limit rule of FR-36.7 under the store lock.
func (f *fakeStore) CreateInstrument(_ context.Context, in store.NewInstrument, override bool, key string) (store.Instrument, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if key != "" {
		if id, ok := f.byIdem["instrument:"+key]; ok {
			return f.instruments[id], nil
		}
	}
	fac, ok := f.facilities[in.FacilityID]
	switch {
	case !ok:
		return store.Instrument{}, httpx.NewProblem(http.StatusNotFound, store.CodeFacilityNotFound, "facility not found")
	case fac.Status != "active":
		return store.Instrument{}, httpx.NewProblem(http.StatusUnprocessableEntity, store.CodeFacilityState, "facility is closed")
	case in.IssueDate > fac.ExpiryDate:
		return store.Instrument{}, httpx.NewProblem(http.StatusUnprocessableEntity, store.CodeFacilityState, "facility expired")
	case fac.Currency != in.Currency:
		return store.Instrument{}, httpx.NewProblem(http.StatusUnprocessableEntity, store.CodeInstrumentCurrency, "instrument currency must equal the facility currency")
	}
	view := f.facilityView(fac)
	limit, utilized, amount := decimal.RequireFromString(fac.Limit), decimal.RequireFromString(view.Utilized), decimal.RequireFromString(in.Amount)
	if err := domain.CheckFacilityLimit(limit, utilized, amount, override); err != nil {
		return store.Instrument{}, err
	}
	breach := utilized.Add(amount).GreaterThan(limit)
	inst := store.Instrument{
		ID: ids.New(), FacilityID: fac.ID, LegalEntityID: fac.LegalEntityID, Kind: in.Kind, Reference: in.Reference,
		Beneficiary: in.Beneficiary, Currency: in.Currency, Amount: in.Amount, IssueDate: in.IssueDate, ExpiryDate: in.ExpiryDate,
		LinkedOrderRef: in.LinkedOrderRef, Status: "open", LimitOverride: breach, Version: 1,
	}
	f.instruments[inst.ID] = inst
	if key != "" {
		f.byIdem["instrument:"+key] = inst.ID
	}
	return inst, nil
}

func (f *fakeStore) ListInstruments(_ context.Context, facilityID string) ([]store.Instrument, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := []store.Instrument{}
	for _, i := range f.instruments {
		if i.FacilityID == facilityID {
			out = append(out, i)
		}
	}
	return out, nil
}

func (f *fakeStore) CloseInstrument(_ context.Context, id string, version int) (store.Instrument, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	i, ok := f.instruments[id]
	if !ok {
		return store.Instrument{}, httpx.NewProblem(http.StatusNotFound, store.CodeInstrumentNotFound, "instrument not found")
	}
	if i.Version != version {
		return store.Instrument{}, httpx.NewProblem(http.StatusPreconditionFailed, store.CodeVersionConflict, "stale version")
	}
	if i.Status != "open" {
		return store.Instrument{}, httpx.NewProblem(http.StatusUnprocessableEntity, store.CodeInstrumentState, "only an open instrument can be closed")
	}
	i.Status = "closed"
	i.Version++
	f.instruments[id] = i
	return i, nil
}

func (f *fakeStore) CreateLoan(_ context.Context, in store.NewLoan, key string) (store.Loan, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if key != "" {
		if id, ok := f.byIdem["loan:"+key]; ok {
			return f.loans[id], nil
		}
	}
	l := store.Loan{
		ID: ids.New(), LegalEntityID: in.LegalEntityID, Role: in.Role, CounterpartyName: in.CounterpartyName,
		Principal: in.Principal, Currency: in.Currency, StartDate: in.StartDate, MaturityDate: in.MaturityDate,
		ScheduleType: in.ScheduleType, FixedRate: in.FixedRate, DayCount: in.DayCount, PeriodMonths: in.PeriodMonths,
		Status: "active", Version: 1, Schedule: in.Schedule,
	}
	f.loans[l.ID] = l
	if key != "" {
		f.byIdem["loan:"+key] = l.ID
	}
	return l, nil
}

func (f *fakeStore) ListLoans(_ context.Context, legalEntityID string) ([]store.Loan, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := []store.Loan{}
	for _, l := range f.loans {
		if l.LegalEntityID == legalEntityID {
			out = append(out, l)
		}
	}
	return out, nil
}

func (f *fakeStore) GetLoan(_ context.Context, id string) (store.Loan, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	l, ok := f.loans[id]
	if !ok {
		return store.Loan{}, httpx.NewProblem(http.StatusNotFound, store.CodeLoanNotFound, "loan contract not found")
	}
	return l, nil
}

// ---- test helpers ----

// wave36Call sends one request through the tenant middleware and the treasury routes.
func wave36Call(t *testing.T, fs *fakeStore, duties, method, path string, body any, headers map[string]string) *httptest.ResponseRecorder {
	t.Helper()
	router := httpx.NewRouter()
	New(fs).Register(router)
	var buf bytes.Buffer
	if body != nil {
		if err := json.NewEncoder(&buf).Encode(body); err != nil {
			t.Fatal(err)
		}
	}
	req := httptest.NewRequest(method, path, &buf)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set(tenant.HeaderTenant, testTenant)
	req.Header.Set(tenant.HeaderLegalEntity, testLE)
	req.Header.Set(tenant.HeaderDuties, duties)
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	rec := httptest.NewRecorder()
	tenant.Middleware(router).ServeHTTP(rec, req)
	return rec
}

func wantStatus(t *testing.T, rec *httptest.ResponseRecorder, status int) {
	t.Helper()
	if rec.Code != status {
		t.Fatalf("status = %d, want %d; body: %s", rec.Code, status, rec.Body.String())
	}
}

func decodeInto(t *testing.T, rec *httptest.ResponseRecorder, v any) {
	t.Helper()
	if err := json.Unmarshal(rec.Body.Bytes(), v); err != nil {
		t.Fatalf("decode: %v; body: %s", err, rec.Body.String())
	}
}

func seedActiveAccount(fs *fakeStore, currency string) string {
	id := ids.New()
	fs.accounts[id] = store.BankAccount{ID: id, LegalEntityID: testLE, AccountCode: "MAIN", Currency: currency,
		MainAccountID: testMainAcct, Status: "active", Version: 1}
	return id
}

func issueCheck(t *testing.T, fs *fakeStore, account, key string) store.Check {
	t.Helper()
	rec := wave36Call(t, fs, allDuties, http.MethodPost, "/v1/treasury/checks",
		map[string]string{"bankAccountId": account, "payee": "Acme Supplies", "amount": "1500", "currency": "USD"},
		map[string]string{"Idempotency-Key": key})
	wantStatus(t, rec, http.StatusCreated)
	var c store.Check
	decodeInto(t, rec, &c)
	return c
}

// ---- checks (FR-36.6, AC-36.5) ----

func TestChecks_ReprintVoidsOldAndIssuesNextNumber(t *testing.T) {
	fs := newFakeStore()
	acct := seedActiveAccount(fs, "USD")
	created := issueCheck(t, fs, acct, "chk-1")
	if created.CheckNumber != 1001 || created.Status != "created" {
		t.Fatalf("created = %+v", created)
	}
	base := "/v1/treasury/checks/" + created.ID

	rec := wave36Call(t, fs, allDuties, http.MethodPost, base+":print", nil, map[string]string{"If-Match": `"1"`})
	wantStatus(t, rec, http.StatusOK)

	rec = wave36Call(t, fs, allDuties, http.MethodPost, base+":reprint", map[string]string{"reason": "printer jam"},
		map[string]string{"If-Match": `"2"`})
	wantStatus(t, rec, http.StatusCreated)
	var fresh store.Check
	decodeInto(t, rec, &fresh)
	if fresh.CheckNumber != 1002 || fresh.Status != "printed" || fresh.Payee != "Acme Supplies" || fresh.Amount != "1500" {
		t.Fatalf("reprinted check = %+v", fresh)
	}
	old := fs.checks[created.ID]
	if old.Status != "voided" || old.ReplacedByID == nil || *old.ReplacedByID != fresh.ID {
		t.Fatalf("old check = %+v", old)
	}
}

func TestChecks_VoidRequiresIfMatchAndReason(t *testing.T) {
	fs := newFakeStore()
	acct := seedActiveAccount(fs, "USD")
	c := issueCheck(t, fs, acct, "chk-2")
	path := "/v1/treasury/checks/" + c.ID + ":void"

	wantStatus(t, wave36Call(t, fs, allDuties, http.MethodPost, path, map[string]string{"reason": "lost"}, nil), http.StatusPreconditionRequired)
	wantStatus(t, wave36Call(t, fs, allDuties, http.MethodPost, path, map[string]string{"reason": "  "},
		map[string]string{"If-Match": `"1"`}), http.StatusBadRequest)
	wantStatus(t, wave36Call(t, fs, allDuties, http.MethodPost, path, map[string]string{"reason": "lost in post"},
		map[string]string{"If-Match": `"1"`}), http.StatusOK)
}

func TestChecks_IssueRefusesInactiveAccount(t *testing.T) {
	fs := newFakeStore()
	acct := seedActiveAccount(fs, "USD")
	fs.accounts[acct] = store.BankAccount{ID: acct, LegalEntityID: testLE, Currency: "USD", Status: "draft", Version: 1}
	rec := wave36Call(t, fs, allDuties, http.MethodPost, "/v1/treasury/checks",
		map[string]string{"bankAccountId": acct, "payee": "Acme", "amount": "10", "currency": "USD"},
		map[string]string{"Idempotency-Key": "chk-3"})
	wantStatus(t, rec, http.StatusUnprocessableEntity)
	if !strings.Contains(rec.Body.String(), store.CodeAccountInactive) {
		t.Fatalf("body lacks %s: %s", store.CodeAccountInactive, rec.Body.String())
	}
}

// ---- facilities and instruments (FR-36.7, AC-36.6) ----

func instrumentBody(ref, amount string, override bool) map[string]any {
	return map[string]any{
		"kind": "lc_import", "reference": ref, "beneficiary": "Supplier GmbH", "currency": "EUR", "amount": amount,
		"issueDate": "2026-01-01", "expiryDate": "2026-12-31", "overrideLimit": override,
	}
}

func TestFacility_LimitBlockedWithoutOverride(t *testing.T) {
	fs := newFakeStore()
	rec := wave36Call(t, fs, allDuties, http.MethodPost, "/v1/treasury/facilities", map[string]any{
		"legalEntityId": testLE, "code": "FAC-DB-01", "bankName": "Deutsche Bank", "currency": "EUR",
		"limit": "1000000", "expiryDate": "2030-12-31",
	}, map[string]string{"Idempotency-Key": "fac-1"})
	wantStatus(t, rec, http.StatusCreated)
	var fac store.Facility
	decodeInto(t, rec, &fac)
	path := "/v1/treasury/facilities/" + fac.ID + "/instruments"

	// Seed 950,000 utilization, then try 100,000 more.
	wantStatus(t, wave36Call(t, fs, allDuties, http.MethodPost, path, instrumentBody("LC-1", "950000", false),
		map[string]string{"Idempotency-Key": "ins-1"}), http.StatusCreated)

	blocked := wave36Call(t, fs, allDuties, http.MethodPost, path, instrumentBody("LC-2", "100000", false),
		map[string]string{"Idempotency-Key": "ins-2"})
	wantStatus(t, blocked, http.StatusUnprocessableEntity)
	if !strings.Contains(blocked.Body.String(), domain.CodeFacilityLimit) {
		t.Fatalf("body lacks %s: %s", domain.CodeFacilityLimit, blocked.Body.String())
	}

	// Override requested without the duty is forbidden.
	forbidden := wave36Call(t, fs, "treasury.view,treasury.facility.write", http.MethodPost, path,
		instrumentBody("LC-2", "100000", true), map[string]string{"Idempotency-Key": "ins-3"})
	wantStatus(t, forbidden, http.StatusForbidden)
	if !strings.Contains(forbidden.Body.String(), "AUTH-0003") {
		t.Fatalf("body lacks AUTH-0003: %s", forbidden.Body.String())
	}

	// With the override duty the breach is recorded on the instrument.
	allowed := wave36Call(t, fs, allDuties, http.MethodPost, path, instrumentBody("LC-2", "100000", true),
		map[string]string{"Idempotency-Key": "ins-4"})
	wantStatus(t, allowed, http.StatusCreated)
	var inst store.Instrument
	decodeInto(t, allowed, &inst)
	if !inst.LimitOverride {
		t.Fatalf("instrument = %+v, want limitOverride", inst)
	}

	view := wave36Call(t, fs, allDuties, http.MethodGet, "/v1/treasury/facilities/"+fac.ID, nil, nil)
	wantStatus(t, view, http.StatusOK)
	var got store.Facility
	decodeInto(t, view, &got)
	if got.Utilized != "1050000.00" || got.Headroom != "0.00" {
		t.Fatalf("facility = %+v", got)
	}
}

// ---- loan contracts (FR-36.19, AC-36.15) ----

func loanBody(periodMonths int, maturity string) map[string]any {
	return map[string]any{
		"legalEntityId": testLE, "role": "lender", "counterpartyName": "Passage Holdings BV",
		"principal": "1000000", "currency": "EUR", "startDate": "2026-06-01", "maturityDate": maturity,
		"scheduleType": "Bullet", "fixedRate": "0.03", "dayCount": "ACT/360", "periodMonths": periodMonths,
	}
}

func TestLoan_BulletWorkedExample(t *testing.T) {
	fs := newFakeStore()
	rec := wave36Call(t, fs, allDuties, http.MethodPost, "/v1/treasury/loans", loanBody(1, "2026-07-01"),
		map[string]string{"Idempotency-Key": "loan-1"})
	wantStatus(t, rec, http.StatusCreated)
	var loan store.Loan
	decodeInto(t, rec, &loan)
	if len(loan.Schedule) != 1 {
		t.Fatalf("schedule = %d lines, want 1", len(loan.Schedule))
	}
	line := loan.Schedule[0]
	if line.Days != 30 || !line.Interest.Equal(decimal.RequireFromString("2500")) ||
		!line.PrincipalRepayment.Equal(decimal.NewFromInt(1000000)) {
		t.Fatalf("line = %+v", line)
	}

	got := wave36Call(t, fs, allDuties, http.MethodGet, "/v1/treasury/loans/"+loan.ID, nil, nil)
	wantStatus(t, got, http.StatusOK)
	var reread store.Loan
	decodeInto(t, got, &reread)
	if len(reread.Schedule) != 1 {
		t.Fatalf("reread schedule = %d lines", len(reread.Schedule))
	}
}

func TestLoan_RejectsInvalidTerms(t *testing.T) {
	fs := newFakeStore()
	wantStatus(t, wave36Call(t, fs, allDuties, http.MethodPost, "/v1/treasury/loans", loanBody(5, "2026-07-01"),
		map[string]string{"Idempotency-Key": "loan-2"}), http.StatusBadRequest)

	partial := wave36Call(t, fs, allDuties, http.MethodPost, "/v1/treasury/loans", loanBody(1, "2026-08-15"),
		map[string]string{"Idempotency-Key": "loan-3"})
	wantStatus(t, partial, http.StatusUnprocessableEntity)
	if !strings.Contains(partial.Body.String(), domain.CodeLoanTerms) {
		t.Fatalf("body lacks %s: %s", domain.CodeLoanTerms, partial.Body.String())
	}
}

// BR-36.5: a check goes Created -> Printed -> Sent -> Cleared; a cleared check
// cannot be voided (TRS-0414) and a check still in hand cannot be stopped.
func TestChecks_LifecycleAndClearedCannotBeVoided(t *testing.T) {
	fs := newFakeStore()
	acct := seedActiveAccount(fs, "USD")
	c := issueCheck(t, fs, acct, "chk-life")
	base := "/v1/treasury/checks/" + c.ID
	step := func(action string, version int, body any, status int) *httptest.ResponseRecorder {
		t.Helper()
		rec := wave36Call(t, fs, allDuties, http.MethodPost, base+":"+action, body, map[string]string{"If-Match": fmt.Sprintf(`"%d"`, version)})
		wantStatus(t, rec, status)
		return rec
	}
	step("print", 1, nil, http.StatusOK)
	stop := step("stop", 2, nil, http.StatusUnprocessableEntity)
	if !strings.Contains(stop.Body.String(), domain.CodeCheckState) {
		t.Fatalf("stopping a printed check: %s", stop.Body.String())
	}
	step("send", 2, nil, http.StatusOK)
	cleared := step("clear", 3, nil, http.StatusOK)
	if cleared.Header().Get("ETag") != `"4"` {
		t.Fatalf("ETag after clear = %q", cleared.Header().Get("ETag"))
	}
	void := step("void", 4, map[string]string{"reason": "duplicate"}, http.StatusUnprocessableEntity)
	if !strings.Contains(void.Body.String(), domain.CodeCheckCleared) {
		t.Fatalf("voiding a cleared check: %s", void.Body.String())
	}
	if got := fs.checks[c.ID].Status; got != "cleared" {
		t.Fatalf("status = %s, want cleared", got)
	}
}

func TestChecks_AmountPlacesDutyAndReplay(t *testing.T) {
	fs := newFakeStore()
	acct := seedActiveAccount(fs, "USD")
	post := func(duties, key, amount, currency string) *httptest.ResponseRecorder {
		return wave36Call(t, fs, duties, http.MethodPost, "/v1/treasury/checks",
			map[string]string{"bankAccountId": acct, "payee": "Acme", "amount": amount, "currency": currency},
			map[string]string{"Idempotency-Key": key})
	}
	for _, tc := range []struct{ amount, currency string }{{"10.005", "USD"}, {"0", "USD"}, {"-5", "USD"}, {"12.5", "JPY"}, {"ten", "USD"}} {
		rec := post(allDuties, "k-"+tc.amount+tc.currency, tc.amount, tc.currency)
		wantStatus(t, rec, http.StatusBadRequest)
		if !strings.Contains(rec.Body.String(), `"amount"`) {
			t.Fatalf("%s %s: no amount field error: %s", tc.amount, tc.currency, rec.Body.String())
		}
	}
	wantStatus(t, post("treasury.view", "k-view", "10.00", "USD"), http.StatusForbidden)

	first := post(allDuties, "k-replay", "10.50", "USD")
	wantStatus(t, first, http.StatusCreated)
	again := post(allDuties, "k-replay", "10.50", "USD")
	wantStatus(t, again, http.StatusCreated)
	var a, b store.Check
	decodeInto(t, first, &a)
	decodeInto(t, again, &b)
	if a.ID != b.ID || a.CheckNumber != b.CheckNumber {
		t.Fatalf("replay issued a new check: %d then %d", a.CheckNumber, b.CheckNumber)
	}
	if next := issueCheck(t, fs, acct, "k-next"); next.CheckNumber != a.CheckNumber+1 {
		t.Fatalf("next number = %d, want %d", next.CheckNumber, a.CheckNumber+1)
	}
}

func TestFacility_OverrideFlagOnlyWhenNeededAndExpiry(t *testing.T) {
	fs := newFakeStore()
	rec := wave36Call(t, fs, allDuties, http.MethodPost, "/v1/treasury/facilities", map[string]any{
		"legalEntityId": testLE, "code": "FAC-02", "bankName": "Commerzbank", "currency": "EUR",
		"limit": "500000.00", "expiryDate": "2026-06-30",
	}, map[string]string{"Idempotency-Key": "fac-2"})
	wantStatus(t, rec, http.StatusCreated)
	var fac store.Facility
	decodeInto(t, rec, &fac)
	path := "/v1/treasury/facilities/" + fac.ID + "/instruments"

	within := wave36Call(t, fs, allDuties, http.MethodPost, path, map[string]any{
		"kind": "guarantee", "reference": "G-1", "beneficiary": "Customs", "currency": "EUR", "amount": "1000.00",
		"issueDate": "2026-01-01", "expiryDate": "2026-03-31", "overrideLimit": true,
	}, map[string]string{"Idempotency-Key": "g-1"})
	wantStatus(t, within, http.StatusCreated)
	var inst store.Instrument
	decodeInto(t, within, &inst)
	if inst.LimitOverride {
		t.Fatal("an instrument within the limit is flagged as an override")
	}

	expired := wave36Call(t, fs, allDuties, http.MethodPost, path, map[string]any{
		"kind": "guarantee", "reference": "G-2", "beneficiary": "Customs", "currency": "EUR", "amount": "1000.00",
		"issueDate": "2026-07-01", "expiryDate": "2026-12-31",
	}, map[string]string{"Idempotency-Key": "g-2"})
	wantStatus(t, expired, http.StatusUnprocessableEntity)
	if !strings.Contains(expired.Body.String(), store.CodeFacilityState) {
		t.Fatalf("expired facility: %s", expired.Body.String())
	}

	backwards := wave36Call(t, fs, allDuties, http.MethodPost, path, map[string]any{
		"kind": "guarantee", "reference": "G-3", "beneficiary": "Customs", "currency": "EUR", "amount": "1000.00",
		"issueDate": "2026-03-01", "expiryDate": "2026-02-01",
	}, map[string]string{"Idempotency-Key": "g-3"})
	wantStatus(t, backwards, http.StatusBadRequest)

	closeRec := wave36Call(t, fs, allDuties, http.MethodPost, "/v1/treasury/facility-instruments/"+inst.ID+":close", nil,
		map[string]string{"If-Match": `"1"`})
	wantStatus(t, closeRec, http.StatusOK)
	again := wave36Call(t, fs, allDuties, http.MethodPost, "/v1/treasury/facility-instruments/"+inst.ID+":close", nil,
		map[string]string{"If-Match": `"2"`})
	wantStatus(t, again, http.StatusUnprocessableEntity)
	if !strings.Contains(again.Body.String(), store.CodeInstrumentState) {
		t.Fatalf("closing twice: %s", again.Body.String())
	}
}

// A caller scoped to one legal entity cannot create a facility or loan for
// another: 403, not the 500 that the row-level security refusal used to give.
func TestCreate_OutsideLegalEntityScopeIsForbidden(t *testing.T) {
	fs := newFakeStore()
	other := ids.New()
	loan := loanBody(1, "2026-07-01")
	loan["legalEntityId"] = other
	for name, tc := range map[string]struct {
		path string
		body map[string]any
	}{
		"facility": {"/v1/treasury/facilities", map[string]any{
			"legalEntityId": other, "code": "FAC-X", "bankName": "Bank", "currency": "EUR", "limit": "1000.00", "expiryDate": "2030-12-31",
		}},
		"loan": {"/v1/treasury/loans", loan},
	} {
		rec := wave36Call(t, fs, allDuties, http.MethodPost, tc.path, tc.body, map[string]string{"Idempotency-Key": "scope-" + name})
		wantStatus(t, rec, http.StatusForbidden)
		if !strings.Contains(rec.Body.String(), "AUTH-0003") {
			t.Fatalf("%s: body lacks AUTH-0003: %s", name, rec.Body.String())
		}
	}
	if len(fs.facilities) != 0 || len(fs.loans) != 0 {
		t.Fatalf("an out-of-scope create reached the store")
	}
}

func TestLoan_RateMustBeAFraction(t *testing.T) {
	fs := newFakeStore()
	for _, rate := range []string{"3", "-0.01", "0.0312345", "abc"} {
		body := loanBody(1, "2026-07-01")
		body["fixedRate"] = rate
		rec := wave36Call(t, fs, allDuties, http.MethodPost, "/v1/treasury/loans", body, map[string]string{"Idempotency-Key": "rate-" + rate})
		wantStatus(t, rec, http.StatusBadRequest)
		if !strings.Contains(rec.Body.String(), "fixedRate") {
			t.Fatalf("rate %s: %s", rate, rec.Body.String())
		}
	}
}
