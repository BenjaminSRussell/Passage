package api_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/platform/tenant"
	"github.com/BenjaminSRussell/passage/platform/testkit"
	"github.com/BenjaminSRussell/passage/services/payables/internal/api"
	"github.com/BenjaminSRussell/passage/services/payables/internal/domain"
	"github.com/BenjaminSRussell/passage/services/payables/internal/fx"
	"github.com/BenjaminSRussell/passage/services/payables/internal/store"
)

const (
	vendorOne  = "00000000-0000-0000-0000-0000000000c1"
	vendorTwo  = "00000000-0000-0000-0000-0000000000c2"
	itemOne    = "00000000-0000-0000-0000-0000000000f1"
	itemTwo    = "00000000-0000-0000-0000-0000000000f2"
	itemThree  = "00000000-0000-0000-0000-0000000000f3"
	proposalID = "00000000-0000-0000-0000-0000000000d1"

	dutyRead    = "open-item.read"
	dutyWrite   = "payment-proposal.write"
	dutyView    = "payment-proposal.read"
	dutyApprove = "payment-proposal.approve"

	proposalBody = `{"legalEntityId":"` + entityA + `","legalEntityCurrency":"USD","runDate":"2026-01-10","runId":"run-1","paymentMethod":"TRANSFER"}`
)

type fakeStore struct {
	items      []store.OpenItem
	listErr    error
	listFilter store.Filter
	listLimit  int
	getErr     error

	candidates []domain.ProposalCandidate
	candErr    error
	candCalls  int
	candDueBy  string

	proposals   map[string]store.Proposal
	createErr   error
	createCalls int
	created     store.NewProposal
	raceWinner  *store.Proposal
	findErr     error

	approveErr error
	approver   string
	approveDay string
	cancelErr  error
	canceller  string
}

func newFakeStore() *fakeStore { return &fakeStore{proposals: map[string]store.Proposal{}} }

func (f *fakeStore) List(_ context.Context, filter store.Filter, limit int) ([]store.OpenItem, error) {
	f.listFilter, f.listLimit = filter, limit
	return f.items[:min(limit, len(f.items))], f.listErr
}

func (f *fakeStore) Get(_ context.Context, id string) (store.OpenItem, error) {
	if f.getErr != nil {
		return store.OpenItem{}, f.getErr
	}
	for _, it := range f.items {
		if it.ID == id {
			return it, nil
		}
	}
	return store.OpenItem{}, store.ErrNotFound
}

func (f *fakeStore) Candidates(_ context.Context, _, dueBy string) ([]domain.ProposalCandidate, error) {
	f.candCalls++
	f.candDueBy = dueBy
	return f.candidates, f.candErr
}

func (f *fakeStore) FindProposal(_ context.Context, le, runDate, runID string) (store.Proposal, error) {
	if f.findErr != nil {
		return store.Proposal{}, f.findErr
	}
	if p, ok := f.proposals[le+runDate+runID]; ok {
		return p, nil
	}
	return store.Proposal{}, store.ErrProposalNotFound
}

func (f *fakeStore) CreateProposal(_ context.Context, in store.NewProposal) (store.Proposal, bool, error) {
	f.createCalls++
	f.created = in
	if f.createErr != nil {
		return store.Proposal{}, false, f.createErr
	}
	if f.raceWinner != nil {
		return *f.raceWinner, false, nil
	}
	p := store.Proposal{ID: proposalID, LegalEntityID: in.LegalEntityID, RunDate: in.RunDate, RunID: in.RunID, Status: domain.ProposalDraft,
		CreatedBy: in.CreatedBy, RequestHash: in.RequestHash, Groups: in.Groups, PaymentMethod: in.PaymentMethod}
	f.proposals[in.LegalEntityID+in.RunDate+in.RunID] = p
	return p, true, nil
}

func (f *fakeStore) GetProposal(_ context.Context, id string) (store.Proposal, error) {
	for _, p := range f.proposals {
		if p.ID == id {
			return p, nil
		}
	}
	return store.Proposal{}, store.ErrProposalNotFound
}

func (f *fakeStore) CancelProposal(_ context.Context, id, user string) (store.Proposal, error) {
	f.canceller = user
	if f.cancelErr != nil {
		return store.Proposal{}, f.cancelErr
	}
	p, err := f.GetProposal(context.Background(), id)
	p.Status = domain.ProposalCancelled
	p.CancelledBy = &user
	return p, err
}

func (f *fakeStore) ApproveProposal(_ context.Context, id, approver, today string) (store.Proposal, error) {
	f.approver, f.approveDay = approver, today
	if f.approveErr != nil {
		return store.Proposal{}, f.approveErr
	}
	p, err := f.GetProposal(context.Background(), id)
	p.Status = domain.ProposalApproved
	p.ApprovedBy = &approver
	return p, err
}

type fakeRates struct {
	rates map[string]string
	err   error
	calls []string
}

func (f *fakeRates) Rate(_ context.Context, from, to string, date time.Time) (decimal.Decimal, error) {
	f.calls = append(f.calls, from+">"+to+"@"+date.Format(time.DateOnly))
	if f.err != nil {
		return decimal.Decimal{}, f.err
	}
	return decimal.RequireFromString(f.rates[from]), nil
}

func proposalRouter(s *fakeStore, rates api.RateSource) (*chi.Mux, *api.API) {
	a := api.New(fakeDB{}, fakeValidator{}, fixedNow).WithProposals(s, rates)
	r := httpx.NewRouter()
	a.Register(r)
	return r, a
}

type caller struct {
	duties, user, entity string
	noScope              bool
}

func (c caller) send(r http.Handler, method, path, body string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	req.Header.Set("X-Passage-Tenant", testkit.TenantID(1))
	duties := c.duties
	if c.entity == "" && !c.noScope {
		duties += "," + tenant.DutyAllScope
	}
	req.Header.Set("X-Passage-Duties", duties)
	req.Header.Set(tenant.HeaderLegalEntityScopeAll, tenant.LegalEntityScopeAll)
	if c.user != "" {
		req.Header.Set("X-Passage-User", c.user)
	}
	if c.entity != "" {
		req.Header.Set("X-Passage-Legal-Entity", c.entity)
	}
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)
	return rec
}

func decode(t *testing.T, rec *httptest.ResponseRecorder) map[string]any {
	t.Helper()
	var out map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("body is not JSON: %v: %s", err, rec.Body)
	}
	return out
}

func wantProblem(t *testing.T, rec *httptest.ResponseRecorder, status int, serviceCode string) map[string]any {
	t.Helper()
	body := decode(t, rec)
	got := body["serviceCode"]
	if got == nil {
		got = body["code"]
	}
	if rec.Code != status || got != serviceCode || !strings.HasPrefix(rec.Header().Get("Content-Type"), "application/problem+json") {
		t.Fatalf("status %d (%s) serviceCode=%v, want %d %s: %s", rec.Code, rec.Header().Get("Content-Type"), got, status, serviceCode, rec.Body)
	}
	return body
}

func openItem(id, vendor, due, currency, open string) store.OpenItem {
	return store.OpenItem{ID: id, LegalEntityID: entityA, VendorID: vendor, DueDate: due, Currency: currency, Amount: dec(open), OpenAmount: dec(open), DiscountAmount: dec("0"), Status: "OPEN", Version: 1}
}

var reader = caller{duties: dutyRead, user: "reader", entity: entityA}

func TestListOpenItems_PassesFiltersAndPages(t *testing.T) {
	s := newFakeStore()
	s.items = []store.OpenItem{
		openItem(itemOne, vendorOne, "2026-02-01", "USD", "100.00"),
		openItem(itemTwo, vendorOne, "2026-02-02", "USD", "200.00"),
		openItem(itemThree, vendorOne, "2026-02-03", "USD", "300.00"),
	}
	r, _ := proposalRouter(s, nil)
	query := "?vendor=" + vendorOne + "&due_from=2026-02-01&due_to=2026-03-01&currency=USD&status=OPEN&limit=2"
	rec := reader.send(r, http.MethodGet, "/v1/payables/open-items"+query, "")
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d: %s", rec.Code, rec.Body)
	}
	f := s.listFilter
	if f.LegalEntityID != entityA || f.VendorID != vendorOne || f.DueFrom != "2026-02-01" || f.DueOnOrBy != "2026-03-01" || f.Currency != "USD" || f.Status != "OPEN" || s.listLimit != 3 {
		t.Fatalf("filter = %+v limit=%d, want every query parameter forwarded and one extra row fetched", f, s.listLimit)
	}
	body := decode(t, rec)
	items := body["items"].([]any)
	next, _ := body["nextCursor"].(string)
	if len(items) != 2 || next == "" || items[1].(map[string]any)["id"] != itemTwo {
		t.Fatalf("page = %s", rec.Body)
	}
	first := items[0].(map[string]any)
	if first["openAmount"] != "100" || first["paymentHold"] != false || first["status"] != "OPEN" {
		t.Fatalf("item body = %v", first)
	}

	s.items = s.items[2:]
	rec = reader.send(r, http.MethodGet, "/v1/payables/open-items?cursor="+next, "")
	if rec.Code != http.StatusOK || s.listFilter.AfterDue != "2026-02-02" || s.listFilter.AfterID != itemTwo || s.listLimit != defaultListLimit+1 {
		t.Fatalf("status %d after=%s/%s limit=%d: %s", rec.Code, s.listFilter.AfterDue, s.listFilter.AfterID, s.listLimit, rec.Body)
	}
	if last := decode(t, rec); last["nextCursor"] != nil || len(last["items"].([]any)) != 1 {
		t.Fatalf("last page = %s", rec.Body)
	}
}

const defaultListLimit = 50

func TestListOpenItems_RejectsBadQueries(t *testing.T) {
	r, _ := proposalRouter(newFakeStore(), nil)
	tests := []struct{ name, query, field string }{
		{"vendor", "vendor=nope", "vendor"},
		{"due_from", "due_from=2026-13-01", "due_from"},
		{"due_to", "due_to=soon", "due_to"},
		{"range", "due_from=2026-03-01&due_to=2026-02-01", "due_from"},
		{"currency", "currency=usd", "currency"},
		{"status", "status=PAID", "status"},
		{"limit zero", "limit=0", "limit"},
		{"limit large", "limit=201", "limit"},
		{"limit text", "limit=ten", "limit"},
		{"cursor", "cursor=zzzz", "cursor"},
		{"value_in", "value_in=dollars", "value_in"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			rec := reader.send(r, http.MethodGet, "/v1/payables/open-items?"+tc.query, "")
			body := wantProblem(t, rec, http.StatusBadRequest, "COM-0001")
			if errs, _ := body["errors"].(map[string]any); errs[tc.field] == nil {
				t.Fatalf("errors lack %s: %s", tc.field, rec.Body)
			}
		})
	}
}

func TestOpenItemRoutes_AuthAndEntityScope(t *testing.T) {
	s := newFakeStore()
	r, _ := proposalRouter(s, nil)
	wantProblem(t, caller{duties: "aging.read", entity: entityA}.send(r, http.MethodGet, "/v1/payables/open-items", ""), http.StatusForbidden, "AUTH-0003")
	wantProblem(t, caller{duties: dutyRead}.send(r, http.MethodGet, "/v1/payables/open-items", ""), http.StatusBadRequest, "COM-0001")
	wantProblem(t, reader.send(r, http.MethodGet, "/v1/payables/open-items?legal_entity="+entityB, ""), http.StatusForbidden, "AUTH-0003")
	wantProblem(t, caller{duties: dutyRead, noScope: true}.send(r, http.MethodGet, "/v1/payables/open-items?legal_entity="+entityB, ""), http.StatusForbidden, "AUTH-0003")
	wantProblem(t, caller{duties: dutyWrite, noScope: true}.send(r, http.MethodPost, "/v1/payables/payment-proposals", proposalBody), http.StatusForbidden, "AUTH-0003")
	if rec := (caller{duties: dutyRead}).send(r, http.MethodGet, "/v1/payables/open-items?legal_entity="+entityB, ""); rec.Code != http.StatusOK || s.listFilter.LegalEntityID != entityB {
		t.Fatalf("an unscoped caller names the entity explicitly: %d %s", rec.Code, rec.Body)
	}
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/v1/payables/open-items", nil))
	wantProblem(t, rec, http.StatusUnauthorized, "AUTH-0001")
}

func TestListOpenItems_ValuesForeignCurrencyInLegalEntityCurrency(t *testing.T) {
	s := newFakeStore()
	s.items = []store.OpenItem{
		openItem(itemOne, vendorOne, "2026-02-01", "EUR", "200.00"),
		openItem(itemTwo, vendorOne, "2026-02-02", "USD", "50.00"),
		openItem(itemThree, vendorOne, "2026-02-03", "EUR", "0.40"),
	}
	rates := &fakeRates{rates: map[string]string{"EUR": "1.0825"}}
	r, _ := proposalRouter(s, rates)
	rec := reader.send(r, http.MethodGet, "/v1/payables/open-items?value_in=USD", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d: %s", rec.Code, rec.Body)
	}
	items := decode(t, rec)["items"].([]any)
	want := []struct{ amount, rate string }{{"216.5", "1.0825"}, {"50", "1"}, {"0.43", "1.0825"}}
	for i, w := range want {
		v := items[i].(map[string]any)["valuation"].(map[string]any)
		if v["currency"] != "USD" || v["amount"] != w.amount || v["rate"] != w.rate || v["rateDate"] != "2026-01-10" {
			t.Errorf("item %d valuation = %v, want %s at %s", i, v, w.amount, w.rate)
		}
	}
	if len(rates.calls) != 1 || rates.calls[0] != "EUR>USD@2026-01-10" {
		t.Fatalf("rate calls = %v, want one EUR>USD lookup for the day", rates.calls)
	}
	if plain := reader.send(r, http.MethodGet, "/v1/payables/open-items", ""); strings.Contains(plain.Body.String(), "valuation") {
		t.Fatalf("valuation present without value_in: %s", plain.Body)
	}
}

func TestListOpenItems_RateFailures(t *testing.T) {
	foreign := []store.OpenItem{openItem(itemOne, vendorOne, "2026-02-01", "EUR", "200.00")}
	tests := []struct {
		name        string
		rates       api.RateSource
		wantStatus  int
		wantService string
	}{
		{"not configured", nil, http.StatusServiceUnavailable, "COM-0503"},
		{"rate missing", &fakeRates{err: fmt.Errorf("%w: EUR/USD", fx.ErrRateUnavailable)}, http.StatusUnprocessableEntity, domain.CodeCurrency},
		{"fx denies the caller", &fakeRates{err: &fx.StatusError{Status: http.StatusForbidden}}, http.StatusForbidden, "AUTH-0003"},
		{"fx down", &fakeRates{err: errors.New("connection refused")}, http.StatusBadGateway, "COM-0502"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			s := newFakeStore()
			s.items = foreign
			r, _ := proposalRouter(s, tc.rates)
			wantProblem(t, reader.send(r, http.MethodGet, "/v1/payables/open-items?value_in=USD", ""), tc.wantStatus, tc.wantService)
		})
	}
	s := newFakeStore()
	s.items = []store.OpenItem{openItem(itemOne, vendorOne, "2026-02-01", "USD", "10.00")}
	r, _ := proposalRouter(s, nil)
	if rec := reader.send(r, http.MethodGet, "/v1/payables/open-items?value_in=USD", ""); rec.Code != http.StatusOK {
		t.Fatalf("same-currency items need no rate source: %d %s", rec.Code, rec.Body)
	}
}

func TestListOpenItems_StoreFaultHidesDetail(t *testing.T) {
	s := newFakeStore()
	s.listErr = errors.New("pq: password authentication failed")
	r, _ := proposalRouter(s, nil)
	rec := reader.send(r, http.MethodGet, "/v1/payables/open-items", "")
	wantProblem(t, rec, http.StatusInternalServerError, "COM-0500")
	if strings.Contains(rec.Body.String(), "password") {
		t.Fatalf("store error leaked: %s", rec.Body)
	}
}

func TestGetOpenItem(t *testing.T) {
	s := newFakeStore()
	s.items = []store.OpenItem{openItem(itemOne, vendorOne, "2026-02-01", "EUR", "200.00")}
	r, _ := proposalRouter(s, &fakeRates{rates: map[string]string{"EUR": "1.0825"}})
	rec := reader.send(r, http.MethodGet, "/v1/payables/open-items/"+itemOne+"?value_in=USD", "")
	body := decode(t, rec)
	if rec.Code != http.StatusOK || body["id"] != itemOne || body["valuation"].(map[string]any)["amount"] != "216.5" {
		t.Fatalf("status %d: %s", rec.Code, rec.Body)
	}
	wantProblem(t, reader.send(r, http.MethodGet, "/v1/payables/open-items/"+itemTwo, ""), http.StatusNotFound, "COM-0404")
	wantProblem(t, reader.send(r, http.MethodGet, "/v1/payables/open-items/not-a-uuid", ""), http.StatusBadRequest, "COM-0001")
	wantProblem(t, reader.send(r, http.MethodGet, "/v1/payables/open-items/"+itemOne+"?value_in=eur", ""), http.StatusBadRequest, "COM-0001")
	s.getErr = errors.New("boom")
	wantProblem(t, reader.send(r, http.MethodGet, "/v1/payables/open-items/"+itemOne, ""), http.StatusInternalServerError, "COM-0500")
}

var creator = caller{duties: dutyWrite, user: "alice", entity: entityA}

func dueCandidates() []domain.ProposalCandidate {
	run := time.Date(2026, 1, 10, 0, 0, 0, 0, time.UTC)
	return []domain.ProposalCandidate{
		{ID: itemOne, VendorID: vendorOne, Currency: "USD", Version: 3, OpenAmount: dec("1000.00"), DiscountAmount: dec("20.00"), DiscountDate: &run},
		{ID: itemTwo, VendorID: vendorOne, Currency: "EUR", Version: 1, OpenAmount: dec("200.00"), DiscountAmount: dec("0")},
	}
}

func TestCreateProposal_BuildsGroupsAndPersistsDraft(t *testing.T) {
	s := newFakeStore()
	s.candidates = dueCandidates()
	rates := &fakeRates{rates: map[string]string{"EUR": "1.0825"}}
	r, a := proposalRouter(s, rates)
	rec := creator.send(r, http.MethodPost, "/v1/payables/payment-proposals", proposalBody)
	if rec.Code != http.StatusCreated || rec.Header().Get("Location") != "/v1/payables/payment-proposals/"+proposalID {
		t.Fatalf("status %d location=%s: %s", rec.Code, rec.Header().Get("Location"), rec.Body)
	}
	in := s.created
	if in.CreatedBy != "alice" || in.LegalEntityID != entityA || in.RunID != "run-1" || in.DueBy != "2026-01-10" || in.RateDate != "2026-01-10" || in.PaymentMethod != "TRANSFER" || s.candDueBy != "2026-01-10" {
		t.Fatalf("persisted request = %+v dueBy=%s", in, s.candDueBy)
	}
	if len(in.Groups) != 2 {
		t.Fatalf("groups = %+v", in.Groups)
	}
	eur, usd := in.Groups[0], in.Groups[1]
	if !usd.Net.Equal(dec("980")) || !usd.Discount.Equal(dec("20")) || !eur.Valued.Equal(dec("216.5")) || !eur.Net.Equal(dec("200")) {
		t.Fatalf("usd=%+v eur=%+v, want usd net 980 after 20 discount and eur valued 216.50", usd, eur)
	}
	if usd.Lines[0].OpenItemVersion != 3 {
		t.Fatalf("line must carry the item version it was selected at: %+v", usd.Lines[0])
	}
	if len(rates.calls) != 1 || rates.calls[0] != "EUR>USD@2026-01-10" {
		t.Fatalf("rate calls = %v", rates.calls)
	}
	m := httptest.NewRecorder()
	a.Metrics().ServeHTTP(m, httptest.NewRequest(http.MethodGet, "/metrics", nil))
	if !strings.Contains(m.Body.String(), `payables_payment_proposals_total{outcome="created"} 1`) {
		t.Fatalf("metrics lack the created counter: %s", m.Body)
	}
}

func TestCreateProposal_DueByAndFutureRunDate(t *testing.T) {
	s := newFakeStore()
	s.candidates = dueCandidates()[1:]
	rates := &fakeRates{rates: map[string]string{"EUR": "1.1"}}
	r, _ := proposalRouter(s, rates)
	body := strings.Replace(strings.Replace(proposalBody, `"2026-01-10"`, `"2026-03-01","dueBy":"2026-03-15"`, 1), `"runId":"run-1"`, `"runId":"run-2"`, 1)
	if rec := creator.send(r, http.MethodPost, "/v1/payables/payment-proposals", body); rec.Code != http.StatusCreated {
		t.Fatalf("status %d: %s", rec.Code, rec.Body)
	}
	if s.candDueBy != "2026-03-15" || s.created.RateDate != "2026-01-10" || rates.calls[0] != "EUR>USD@2026-01-10" {
		t.Fatalf("dueBy=%s rateDate=%s rate calls=%v, want a future run valued at today's rate", s.candDueBy, s.created.RateDate, rates.calls)
	}
}

func TestCreateProposal_VendorMethodOverride(t *testing.T) {
	s := newFakeStore()
	s.candidates = dueCandidates()[:1]
	r, _ := proposalRouter(s, nil)
	body := strings.Replace(proposalBody, `"paymentMethod":"TRANSFER"`, `"paymentMethod":"TRANSFER","vendorPaymentMethods":{"`+vendorOne+`":"ACH"}`, 1)
	if rec := creator.send(r, http.MethodPost, "/v1/payables/payment-proposals", body); rec.Code != http.StatusCreated {
		t.Fatalf("status %d: %s", rec.Code, rec.Body)
	}
	if got := s.created.Groups[0].Method; got != "ACH" {
		t.Fatalf("group method = %s, want the vendor override ACH", got)
	}
}

func TestCreateProposal_IdempotentPerRun(t *testing.T) {
	s := newFakeStore()
	s.candidates = dueCandidates()[:1]
	r, a := proposalRouter(s, nil)
	if rec := creator.send(r, http.MethodPost, "/v1/payables/payment-proposals", proposalBody); rec.Code != http.StatusCreated {
		t.Fatalf("first: %d %s", rec.Code, rec.Body)
	}
	s.candCalls, s.createCalls = 0, 0
	rec := creator.send(r, http.MethodPost, "/v1/payables/payment-proposals", proposalBody)
	if rec.Code != http.StatusOK || rec.Header().Get("Idempotent-Replayed") != "true" || decode(t, rec)["id"] != proposalID {
		t.Fatalf("replay: %d replayed=%s: %s", rec.Code, rec.Header().Get("Idempotent-Replayed"), rec.Body)
	}
	if s.candCalls != 0 || s.createCalls != 0 {
		t.Fatalf("a replay must not select or write again: candidates=%d creates=%d", s.candCalls, s.createCalls)
	}
	changed := strings.Replace(proposalBody, "TRANSFER", "ACH", 1)
	wantProblem(t, creator.send(r, http.MethodPost, "/v1/payables/payment-proposals", changed), http.StatusConflict, domain.CodeProposalConflict)
	m := httptest.NewRecorder()
	a.Metrics().ServeHTTP(m, httptest.NewRequest(http.MethodGet, "/metrics", nil))
	if !strings.Contains(m.Body.String(), `outcome="replayed"} 1`) {
		t.Fatalf("metrics lack the replay: %s", m.Body)
	}
}

func TestCreateProposal_ConcurrentCreateResolvesToWinner(t *testing.T) {
	s := newFakeStore()
	s.candidates = dueCandidates()[:1]
	probe := newFakeStore()
	rp, _ := proposalRouter(probe, nil)
	probe.candidates = s.candidates
	creator.send(rp, http.MethodPost, "/v1/payables/payment-proposals", proposalBody)
	winner := probe.proposals[entityA+"2026-01-10"+"run-1"]

	s.raceWinner = &winner
	r, _ := proposalRouter(s, nil)
	rec := creator.send(r, http.MethodPost, "/v1/payables/payment-proposals", proposalBody)
	if rec.Code != http.StatusOK || rec.Header().Get("Idempotent-Replayed") != "true" {
		t.Fatalf("losing the creation race with an identical request replays the winner: %d %s", rec.Code, rec.Body)
	}
	changed := strings.Replace(proposalBody, "TRANSFER", "WIRE", 1)
	wantProblem(t, creator.send(r, http.MethodPost, "/v1/payables/payment-proposals", changed), http.StatusConflict, domain.CodeProposalConflict)
}

func TestCreateProposal_Rejections(t *testing.T) {
	r, _ := proposalRouter(newFakeStore(), nil)
	tests := []struct {
		name, body, field string
	}{
		{"runDate missing", `{"legalEntityId":"` + entityA + `","legalEntityCurrency":"USD","runId":"r","paymentMethod":"ACH"}`, "runDate"},
		{"runDate malformed", strings.Replace(proposalBody, "2026-01-10", "10/01/2026", 1), "runDate"},
		{"dueBy before runDate", strings.Replace(proposalBody, `"runId"`, `"dueBy":"2026-01-01","runId"`, 1), "dueBy"},
		{"currency", strings.Replace(proposalBody, `"USD"`, `"usd"`, 1), "legalEntityCurrency"},
		{"run id empty", strings.Replace(proposalBody, `"run-1"`, `""`, 1), "runId"},
		{"run id long", strings.Replace(proposalBody, `"run-1"`, `"`+strings.Repeat("x", 65)+`"`, 1), "runId"},
		{"method", strings.Replace(proposalBody, "TRANSFER", "CASH", 1), "paymentMethod"},
		{"vendor override", strings.Replace(proposalBody, `"paymentMethod":"TRANSFER"`, `"paymentMethod":"TRANSFER","vendorPaymentMethods":{"v":"ACH"}`, 1), "vendorPaymentMethods"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			rec := creator.send(r, http.MethodPost, "/v1/payables/payment-proposals", tc.body)
			body := wantProblem(t, rec, http.StatusBadRequest, "COM-0001")
			if errs, _ := body["errors"].(map[string]any); errs[tc.field] == nil {
				t.Fatalf("errors lack %s: %s", tc.field, rec.Body)
			}
		})
	}
	wantProblem(t, creator.send(r, http.MethodPost, "/v1/payables/payment-proposals", `{"unknown":1}`), http.StatusBadRequest, "COM-0001")
	wantProblem(t, creator.send(r, http.MethodPost, "/v1/payables/payment-proposals", strings.Replace(proposalBody, entityA, entityB, 1)), http.StatusForbidden, "AUTH-0003")
	noEntity := strings.Replace(proposalBody, `"legalEntityId":"`+entityA+`",`, "", 1)
	wantProblem(t, caller{duties: dutyWrite, user: "alice"}.send(r, http.MethodPost, "/v1/payables/payment-proposals", noEntity), http.StatusBadRequest, "COM-0001")
	wantProblem(t, caller{duties: dutyWrite, entity: entityA}.send(r, http.MethodPost, "/v1/payables/payment-proposals", proposalBody), http.StatusForbidden, "AUTH-0003")
	wantProblem(t, caller{duties: dutyRead, user: "alice", entity: entityA}.send(r, http.MethodPost, "/v1/payables/payment-proposals", proposalBody), http.StatusForbidden, "AUTH-0003")
}

func TestCreateProposal_DependencyFailures(t *testing.T) {
	tests := []struct {
		name        string
		setup       func(*fakeStore)
		rates       api.RateSource
		wantStatus  int
		wantService string
	}{
		{"too many items", func(s *fakeStore) { s.candErr = store.ErrTooManyItems }, nil, http.StatusBadRequest, "COM-0001"},
		{"candidate query fails", func(s *fakeStore) { s.candErr = errors.New("db down") }, nil, http.StatusInternalServerError, "COM-0500"},
		{"lookup fails", func(s *fakeStore) { s.findErr = errors.New("db down") }, nil, http.StatusInternalServerError, "COM-0500"},
		{"items changed", func(s *fakeStore) { s.candidates, s.createErr = s.candidates[:1], store.ErrItemsChanged }, nil, http.StatusConflict, domain.CodeProposalConflict},
		{"create fails", func(s *fakeStore) { s.candidates, s.createErr = s.candidates[:1], errors.New("db down") }, nil, http.StatusInternalServerError, "COM-0500"},
		{"rate missing", nil, &fakeRates{err: fx.ErrRateUnavailable}, http.StatusUnprocessableEntity, domain.CodeCurrency},
		{"no rate source", nil, nil, http.StatusServiceUnavailable, "COM-0503"},
		{"fx down", nil, &fakeRates{err: errors.New("timeout")}, http.StatusBadGateway, "COM-0502"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			s := newFakeStore()
			s.candidates = dueCandidates()
			if tc.setup != nil {
				tc.setup(s)
			}
			r, _ := proposalRouter(s, tc.rates)
			wantProblem(t, creator.send(r, http.MethodPost, "/v1/payables/payment-proposals", proposalBody), tc.wantStatus, tc.wantService)
			if wantWrites := map[bool]int{true: 1, false: 0}[s.createErr != nil]; s.createCalls != wantWrites {
				t.Fatalf("%d proposal writes, want %d: nothing may be written when a dependency fails before the write", s.createCalls, wantWrites)
			}
		})
	}
}

var approver = caller{duties: dutyApprove, user: "bob", entity: entityA}

func TestApproveProposal(t *testing.T) {
	s := newFakeStore()
	s.proposals["k"] = store.Proposal{ID: proposalID, Status: domain.ProposalDraft, CreatedBy: "alice"}
	r, a := proposalRouter(s, nil)
	rec := approver.send(r, http.MethodPost, "/v1/payables/payment-proposals/"+proposalID+":approve", "")
	body := decode(t, rec)
	if rec.Code != http.StatusOK || body["status"] != "APPROVED" || body["approvedBy"] != "bob" || s.approver != "bob" {
		t.Fatalf("status %d: %s", rec.Code, rec.Body)
	}

	s.approveErr = store.ErrSelfApproval
	wantProblem(t, caller{duties: dutyApprove, user: "alice", entity: entityA}.send(r, http.MethodPost, "/v1/payables/payment-proposals/"+proposalID+":approve", ""), http.StatusForbidden, domain.CodeSegregation)
	s.approveErr = &store.LinesChangedError{OpenItemIDs: []string{itemOne, itemTwo}}
	conflict := approver.send(r, http.MethodPost, "/v1/payables/payment-proposals/"+proposalID+":approve", "")
	wantProblem(t, conflict, http.StatusConflict, domain.CodeProposalConflict)
	if !strings.Contains(conflict.Body.String(), itemOne) || !strings.Contains(conflict.Body.String(), itemTwo) {
		t.Fatalf("conflict must name the offending lines: %s", conflict.Body)
	}
	s.approveErr = store.ErrRunDateElapsed
	wantProblem(t, approver.send(r, http.MethodPost, "/v1/payables/payment-proposals/"+proposalID+":approve", ""), http.StatusConflict, domain.CodeRunDatePassed)
	if s.approveDay != "2026-01-10" {
		t.Fatalf("approval must compare the run date against today (UTC) from the API clock, got %q", s.approveDay)
	}
	s.approveErr = store.ErrNotDraft
	wantProblem(t, approver.send(r, http.MethodPost, "/v1/payables/payment-proposals/"+proposalID+":approve", ""), http.StatusConflict, domain.CodeProposalState)
	s.approveErr = store.ErrProposalNotFound
	wantProblem(t, approver.send(r, http.MethodPost, "/v1/payables/payment-proposals/"+proposalID+":approve", ""), http.StatusNotFound, "COM-0404")
	s.approveErr = errors.New("db down")
	wantProblem(t, approver.send(r, http.MethodPost, "/v1/payables/payment-proposals/"+proposalID+":approve", ""), http.StatusInternalServerError, "COM-0500")

	wantProblem(t, approver.send(r, http.MethodPost, "/v1/payables/payment-proposals/nope:approve", ""), http.StatusBadRequest, "COM-0001")
	wantProblem(t, caller{duties: dutyApprove, entity: entityA}.send(r, http.MethodPost, "/v1/payables/payment-proposals/"+proposalID+":approve", ""), http.StatusForbidden, "AUTH-0003")
	wantProblem(t, caller{duties: dutyWrite, user: "bob", entity: entityA}.send(r, http.MethodPost, "/v1/payables/payment-proposals/"+proposalID+":approve", ""), http.StatusForbidden, "AUTH-0003")

	m := httptest.NewRecorder()
	a.Metrics().ServeHTTP(m, httptest.NewRequest(http.MethodGet, "/metrics", nil))
	for _, want := range []string{`outcome="approved"} 1`, `outcome="self_approval_refused"} 1`} {
		if !strings.Contains(m.Body.String(), want) {
			t.Errorf("metrics lack %s: %s", want, m.Body)
		}
	}
}

func TestGetProposal(t *testing.T) {
	s := newFakeStore()
	s.proposals["k"] = store.Proposal{ID: proposalID, Status: domain.ProposalDraft, CreatedBy: "alice", Groups: []domain.ProposalGroup{}}
	r, _ := proposalRouter(s, nil)
	viewer := caller{duties: dutyView, user: "carol", entity: entityA}
	rec := viewer.send(r, http.MethodGet, "/v1/payables/payment-proposals/"+proposalID, "")
	if rec.Code != http.StatusOK || decode(t, rec)["id"] != proposalID {
		t.Fatalf("status %d: %s", rec.Code, rec.Body)
	}
	wantProblem(t, viewer.send(r, http.MethodGet, "/v1/payables/payment-proposals/00000000-0000-0000-0000-00000000dead", ""), http.StatusNotFound, "COM-0404")
	wantProblem(t, viewer.send(r, http.MethodGet, "/v1/payables/payment-proposals/x", ""), http.StatusBadRequest, "COM-0001")
	wantProblem(t, approver.send(r, http.MethodGet, "/v1/payables/payment-proposals/"+proposalID, ""), http.StatusForbidden, "AUTH-0003")
}

func TestProposalRoutes_AbsentUntilConfigured(t *testing.T) {
	r, _ := newRouter(fakeDB{})
	rec := reader.send(r, http.MethodGet, "/v1/payables/open-items", "")
	if rec.Code != http.StatusNotFound && rec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("unconfigured route answered %d", rec.Code)
	}
}

func TestCreateProposal_RejectsBackdatedRunDateButReplaysOldRun(t *testing.T) {
	s := newFakeStore()
	s.candidates = dueCandidates()[:1]
	r, _ := proposalRouter(s, nil)
	past := strings.Replace(proposalBody, "2026-01-10", "2026-01-09", 1)
	body := wantProblem(t, creator.send(r, http.MethodPost, "/v1/payables/payment-proposals", past), http.StatusBadRequest, "COM-0001")
	if errs, _ := body["errors"].(map[string]any); errs["runDate"] == nil || s.createCalls != 0 || s.candCalls != 0 {
		t.Fatalf("backdated run must be refused before selection: %v creates=%d", body, s.createCalls)
	}
	s.proposals[entityA+"2026-01-09"+"run-1"] = store.Proposal{ID: proposalID, RequestHash: "x"}
	wantProblem(t, creator.send(r, http.MethodPost, "/v1/payables/payment-proposals", past), http.StatusConflict, domain.CodeProposalConflict)
}

func TestCreateProposal_NullAndEmptyVendorMethodsReplayIdentically(t *testing.T) {
	s := newFakeStore()
	s.candidates = dueCandidates()[:1]
	r, _ := proposalRouter(s, nil)
	if rec := creator.send(r, http.MethodPost, "/v1/payables/payment-proposals", proposalBody); rec.Code != http.StatusCreated {
		t.Fatalf("create: %d %s", rec.Code, rec.Body)
	}
	for name, field := range map[string]string{"null": `"vendorPaymentMethods":null,`, "empty": `"vendorPaymentMethods":{},`} {
		body := strings.Replace(proposalBody, `"runDate"`, field+`"runDate"`, 1)
		if rec := creator.send(r, http.MethodPost, "/v1/payables/payment-proposals", body); rec.Code != http.StatusOK || rec.Header().Get("Idempotent-Replayed") != "true" {
			t.Fatalf("%s vendorPaymentMethods must replay the omitted-field run: %d %s", name, rec.Code, rec.Body)
		}
	}
}

func TestCreateProposal_NothingDueIsRefusedAndNotPersisted(t *testing.T) {
	s := newFakeStore()
	r, _ := proposalRouter(s, nil)
	wantProblem(t, creator.send(r, http.MethodPost, "/v1/payables/payment-proposals", proposalBody), http.StatusUnprocessableEntity, domain.CodeNothingDue)
	if s.createCalls != 0 {
		t.Fatalf("an empty draft was persisted: %d creates", s.createCalls)
	}
}

func TestCancelProposal(t *testing.T) {
	s := newFakeStore()
	s.proposals["k"] = store.Proposal{ID: proposalID, Status: domain.ProposalDraft, CreatedBy: "alice"}
	r, a := proposalRouter(s, nil)
	path := "/v1/payables/payment-proposals/" + proposalID + ":cancel"
	rec := creator.send(r, http.MethodPost, path, "")
	if body := decode(t, rec); rec.Code != http.StatusOK || body["status"] != "CANCELLED" || body["cancelledBy"] != "alice" || s.canceller != "alice" {
		t.Fatalf("cancel: %d %s", rec.Code, rec.Body)
	}
	s.cancelErr = store.ErrNotDraft
	wantProblem(t, creator.send(r, http.MethodPost, path, ""), http.StatusConflict, domain.CodeProposalState)
	s.cancelErr = store.ErrProposalNotFound
	wantProblem(t, creator.send(r, http.MethodPost, path, ""), http.StatusNotFound, "COM-0404")
	s.cancelErr = errors.New("db down")
	wantProblem(t, creator.send(r, http.MethodPost, path, ""), http.StatusInternalServerError, "COM-0500")
	wantProblem(t, creator.send(r, http.MethodPost, "/v1/payables/payment-proposals/nope:cancel", ""), http.StatusBadRequest, "COM-0001")
	wantProblem(t, caller{duties: dutyWrite, entity: entityA}.send(r, http.MethodPost, path, ""), http.StatusForbidden, "AUTH-0003")
	wantProblem(t, approver.send(r, http.MethodPost, path, ""), http.StatusForbidden, "AUTH-0003")
	m := httptest.NewRecorder()
	a.Metrics().ServeHTTP(m, httptest.NewRequest(http.MethodGet, "/metrics", nil))
	if !strings.Contains(m.Body.String(), `outcome="cancelled"} 1`) {
		t.Fatalf("metrics lack the cancellation: %s", m.Body)
	}
}
