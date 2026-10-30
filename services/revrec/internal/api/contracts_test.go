package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/platform/tenant"
	"github.com/BenjaminSRussell/passage/platform/testkit"
	"github.com/BenjaminSRussell/passage/services/revrec/internal/domain"
	"github.com/BenjaminSRussell/passage/services/revrec/internal/store"
)

const (
	entityID   = "018f0000-0000-7000-8000-00000000000e"
	customerID = "018f0000-0000-7000-8000-0000000000c1"
	allDuties  = "revrec.view,revrec.maintain,revrec.post"
	idemKey    = "order-1234-invoice-1"
)

// bundleContract is the BR-29.1 bundle: hardware at shipment, installation on
// milestone, 12-month support ratable, sold together for 10,800.00.
const bundleContract = `{
  "legalEntityId": "` + entityID + `",
  "orderId": "018f0000-0000-7000-8000-00000000000f",
  "customerId": "` + customerID + `",
  "currency": "USD",
  "transactionPrice": "10800.00",
  "contractDate": "2027-03-10",
  "obligations": [
    {"productId": "018f0000-0000-7000-8000-0000000000a1", "ssp": "8000", "sspSource": "EXPLICIT", "method": "POINT_IN_TIME", "satisfiedOn": "2027-03-10"},
    {"productId": "018f0000-0000-7000-8000-0000000000a2", "ssp": "1500", "sspSource": "EXPLICIT", "method": "MILESTONE"},
    {"productId": "018f0000-0000-7000-8000-0000000000a3", "ssp": "2500", "sspSource": "EXPLICIT", "method": "RATABLE",
     "termStart": "2027-03-10", "termEnd": "2028-03-09"}
  ]
}`

// fakeStore keeps contracts in memory and applies the same domain rules as the
// Postgres store (CheckSatisfy, version bump, run key), so handler tests see
// real state transitions. The SQL itself is covered by the store tests.
type fakeStore struct {
	contracts map[string]store.Contract
	keys      map[string]string
	runs      map[string]store.Run
	lastRun   string
	creates   int
}

func newFakeStore() *fakeStore {
	return &fakeStore{contracts: map[string]store.Contract{}, keys: map[string]string{}, runs: map[string]store.Run{}}
}

func (f *fakeStore) Ping(context.Context) error { return nil }

func (f *fakeStore) FindContractByKey(_ context.Context, key string) (store.Contract, bool, error) {
	id, ok := f.keys[key]
	if !ok {
		return store.Contract{}, false, nil
	}
	return f.contracts[id], true, nil
}

func (f *fakeStore) CreateContract(_ context.Context, c store.Contract) (store.Contract, error) {
	f.creates++
	c.Status, c.Version = domain.ContractOpen, 1
	f.contracts[c.ID] = c
	f.keys[c.IdempotencyKey] = c.ID
	return c, nil
}

func (f *fakeStore) GetContract(_ context.Context, id string) (store.Contract, error) {
	c, ok := f.contracts[id]
	if !ok {
		return store.Contract{}, httpx.NewProblem(http.StatusNotFound, store.CodeNotFound, "contract not found")
	}
	return c, nil
}

// ListContracts filters like the store and pages newest first; contract ids are
// UUIDv7, so descending id order stands in for (created_at, id).
func (f *fakeStore) ListContracts(_ context.Context, filter store.ContractFilter) (store.ContractPage, error) {
	var all []store.Contract
	for _, c := range f.contracts {
		if filter.CustomerID != "" && filter.CustomerID != c.CustomerID {
			continue
		}
		if filter.Method != "" && !hasMethod(c, filter.Method) {
			continue
		}
		if filter.After != "" && c.ID >= filter.After {
			continue
		}
		c.Obligations = nil
		c.Recognized = decimal.RequireFromString("7200")
		all = append(all, c)
	}
	sort.Slice(all, func(i, j int) bool { return all[i].ID > all[j].ID })
	page := store.ContractPage{Items: all}
	if len(all) > filter.Limit {
		page.Items = all[:filter.Limit]
		page.NextAfter = page.Items[filter.Limit-1].ID
	}
	return page, nil
}

func hasMethod(c store.Contract, method string) bool {
	for _, o := range c.Obligations {
		if o.Method == method {
			return true
		}
	}
	return false
}

func (f *fakeStore) ContractRollForward(_ context.Context, contractID string, start, end time.Time) (store.ContractRoll, error) {
	c, ok := f.contracts[contractID]
	if !ok {
		return store.ContractRoll{}, httpx.NewProblem(http.StatusNotFound, store.CodeNotFound, "contract not found")
	}
	rf, _ := f.RollForward(context.Background(), c.LegalEntityID, c.Currency, start, end)
	return store.ContractRoll{LegalEntityID: c.LegalEntityID, Currency: c.Currency, RollForward: rf}, nil
}

func (f *fakeStore) SatisfyMilestone(_ context.Context, obligationID string, on time.Time, expected int) (store.Satisfaction, error) {
	for id, c := range f.contracts {
		for i, o := range c.Obligations {
			if o.ID != obligationID {
				continue
			}
			var line store.Schedule
			if len(o.Schedule) > 0 {
				line = o.Schedule[0]
			}
			replay, err := domain.CheckSatisfy(o.Method, line.Status, line.RecognizedOn, on, c.ContractDate)
			if err != nil {
				return store.Satisfaction{}, err
			}
			out := store.Satisfaction{ContractID: id, ContractVersion: c.Version, ObligationID: obligationID, ScheduleID: line.ID,
				Currency: c.Currency, Amount: o.Allocated, RecognizedOn: on, Replayed: replay}
			if replay {
				return out, nil
			}
			if expected != 0 && expected != c.Version {
				return store.Satisfaction{}, httpx.NewProblem(http.StatusPreconditionFailed, store.CodeVersion, "stale")
			}
			c.Obligations[i].Schedule[0].Status = domain.ScheduleRecognized
			c.Obligations[i].Schedule[0].RecognizedOn = &on
			c.Version++
			f.contracts[id] = c
			out.ContractVersion = c.Version
			return out, nil
		}
	}
	return store.Satisfaction{}, httpx.NewProblem(http.StatusNotFound, store.CodeNotFound, "obligation not found")
}

func (f *fakeStore) RunRecognition(_ context.Context, legalEntityID string, end time.Time) (store.Run, bool, error) {
	k := legalEntityID + "|" + end.Format(dateLayout)
	f.lastRun = k
	if r, ok := f.runs[k]; ok {
		return r, false, nil
	}
	r := store.Run{ID: ids.New(), LegalEntityID: legalEntityID, PeriodEnd: end, LineCount: 1, CreatedAt: time.Date(2027, 4, 1, 1, 0, 0, 0, time.UTC)}
	f.runs[k] = r
	return r, true, nil
}

func (f *fakeStore) ListRuns(_ context.Context, legalEntityID string, limit int) ([]store.Run, error) {
	var out []store.Run
	for _, r := range f.runs {
		if legalEntityID == "" || r.LegalEntityID == legalEntityID {
			out = append(out, r)
		}
	}
	return out, nil
}

func (f *fakeStore) RollForward(_ context.Context, _, _ string, start, _ time.Time) (domain.RollForward, error) {
	if start.Month() != time.March {
		return domain.NewRollForward(decimal.Zero, decimal.Zero, decimal.Zero), nil
	}
	return domain.NewRollForward(decimal.Zero, decimal.RequireFromString("3600"), decimal.RequireFromString("1483.06")), nil
}

func newAPI(f Store) (*API, http.Handler) {
	a := New(f)
	a.now = func() time.Time { return time.Date(2027, 4, 2, 9, 30, 0, 0, time.UTC) }
	r := httpx.NewRouter()
	a.Register(r)
	return a, r
}

func newHandler(f Store) http.Handler {
	_, h := newAPI(f)
	return h
}

// callAs sends a request as tenant 1 with the given duties, legal-entity scope and headers.
func callAs(t *testing.T, h http.Handler, method, path, body, duties, scope string, headers map[string]string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set(tenant.HeaderTenant, testkit.TenantID(1))
	req.Header.Set(tenant.HeaderDuties, duties)
	if scope != "" {
		req.Header.Set(tenant.HeaderLegalEntity, scope)
	}
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

func call(t *testing.T, h http.Handler, method, path, body string, headers map[string]string) *httptest.ResponseRecorder {
	t.Helper()
	return callAs(t, h, method, path, body, allDuties, entityID, headers)
}

func postContract(t *testing.T, h http.Handler, body, key string) *httptest.ResponseRecorder {
	t.Helper()
	headers := map[string]string{}
	if key != "" {
		headers[idempotencyKey] = key
	}
	return call(t, h, http.MethodPost, "/v1/revrec/contracts", body, headers)
}

func decodeContract(t *testing.T, rec *httptest.ResponseRecorder) contractOut {
	t.Helper()
	var c contractOut
	if err := json.Unmarshal(rec.Body.Bytes(), &c); err != nil {
		t.Fatalf("decode contract: %v: %s", err, rec.Body)
	}
	return c
}

func decodeProblem(t *testing.T, rec *httptest.ResponseRecorder) httpx.Problem {
	t.Helper()
	var p httpx.Problem
	if err := json.Unmarshal(rec.Body.Bytes(), &p); err != nil {
		t.Fatalf("decode problem: %v: %s", err, rec.Body)
	}
	return p
}

// TestCreateContract_BundleMatchesBR29_1 checks the worked example through the
// API: 7,200.00 / 1,350.00 / 2,250.00, hardware recognized at its transfer
// date, installation awaiting its milestone, support over 13 monthly lines.
func TestCreateContract_BundleMatchesBR29_1(t *testing.T) {
	h := newHandler(newFakeStore())
	rec := postContract(t, h, bundleContract, idemKey)
	if rec.Code != http.StatusCreated {
		t.Fatalf("status %d: %s", rec.Code, rec.Body)
	}
	c := decodeContract(t, rec)
	want := []string{"7200.00", "1350.00", "2250.00"}
	for i, o := range c.Obligations {
		if o.Allocated != want[i] || o.LineNo != i+1 {
			t.Errorf("obligation %d: line %d allocated %s, want line %d and %s", i, o.LineNo, o.Allocated, i+1, want[i])
		}
	}
	if s := c.Obligations[0].Schedule; len(s) != 1 || s[0].Status != domain.ScheduleRecognized || *s[0].RecognizedOn != "2027-03-10" {
		t.Errorf("hardware schedule %+v, want one RECOGNIZED line on 2027-03-10", s)
	}
	if c.Obligations[0].Recognized != "7200.00" {
		t.Errorf("hardware recognized %s, want 7200.00", c.Obligations[0].Recognized)
	}
	if s := c.Obligations[1].Schedule; len(s) != 1 || s[0].Status != domain.ScheduleScheduled || s[0].Date != nil {
		t.Errorf("installation schedule %+v, want one undated SCHEDULED line", s)
	}
	support := c.Obligations[2].Schedule
	if len(support) != 13 || support[0].Amount != "133.06" || *support[0].Date != "2027-03-31" {
		t.Errorf("support schedule %d lines starting %+v, want 13 starting 133.06 on 2027-03-31", len(support), support[0])
	}
	if len(c.Warnings) != 0 || rec.Header().Get("ETag") != `"1"` {
		t.Errorf("warnings %+v ETag %q, want none and \"1\"", c.Warnings, rec.Header().Get("ETag"))
	}
}

func TestCreateContract_ReplayAndKeyReuse(t *testing.T) {
	f := newFakeStore()
	h := newHandler(f)
	first := decodeContract(t, postContract(t, h, bundleContract, idemKey))

	replay := postContract(t, h, bundleContract, idemKey)
	if replay.Code != http.StatusOK || replay.Header().Get("Idempotent-Replayed") != "true" {
		t.Fatalf("replay status %d header %q", replay.Code, replay.Header().Get("Idempotent-Replayed"))
	}
	if got := decodeContract(t, replay); got.ID != first.ID || f.creates != 1 {
		t.Errorf("replay returned contract %s after %d creates, want %s and 1", got.ID, f.creates, first.ID)
	}

	changed := strings.Replace(bundleContract, `"10800.00"`, `"10799.99"`, 1)
	reuse := postContract(t, h, changed, idemKey)
	if p := decodeProblem(t, reuse); reuse.Code != http.StatusUnprocessableEntity || p.Code != "idempotency_key_reuse" {
		t.Errorf("reuse %d %s, want 422 idempotency_key_reuse", reuse.Code, p.Code)
	}
}

func TestCreateContract_ValidationProblems(t *testing.T) {
	tests := []struct {
		name   string
		body   string
		key    string
		field  string
		code   string
		status int
	}{
		{"missing idempotency key", bundleContract, "", "", "COM-0001", http.StatusBadRequest},
		{"point in time without transfer date", strings.Replace(bundleContract, `, "satisfiedOn": "2027-03-10"`, "", 1), "k-pit", "obligations[0].satisfiedOn", "COM-0001", http.StatusBadRequest},
		{"milestone with a satisfiedOn", strings.Replace(bundleContract, `"method": "MILESTONE"`, `"method": "MILESTONE", "satisfiedOn": "2027-03-25"`, 1), "k-ms", "obligations[1].satisfiedOn", "COM-0001", http.StatusBadRequest},
		{"non uuid product", strings.Replace(bundleContract, "0000000000a2", "not-a-uuid", 1), "k-uuid", "obligations[1].productId", "COM-0001", http.StatusBadRequest},
		{"bad contract date", strings.Replace(bundleContract, `"contractDate": "2027-03-10"`, `"contractDate": "10/03/2027"`, 1), "k-date", "contractDate", "COM-0001", http.StatusBadRequest},
		{"ratable without a term", strings.Replace(bundleContract, `"termStart": "2027-03-10", "termEnd": "2028-03-09"`, `"termStart": "2027-03-10"`, 1), "k-term", "obligations[2].term", "COM-0001", http.StatusBadRequest},
		{"zero SSP", strings.Replace(bundleContract, `"ssp": "1500"`, `"ssp": "0"`, 1), "k-ssp", "obligations[1].ssp", "COM-0001", http.StatusBadRequest},
		{"two residual obligations", strings.Replace(bundleContract, `"sspSource": "EXPLICIT"`, `"sspSource": "RESIDUAL"`, 2), "k-res2", "", "REV-0003", http.StatusUnprocessableEntity},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := newFakeStore()
			rec := postContract(t, newHandler(f), tt.body, tt.key)
			if rec.Code != tt.status {
				t.Fatalf("status %d, want %d: %s", rec.Code, tt.status, rec.Body)
			}
			p := decodeProblem(t, rec)
			if p.Code != tt.code || f.creates != 0 {
				t.Errorf("code %s after %d creates, want %s and nothing stored", p.Code, f.creates, tt.code)
			}
			if tt.field != "" {
				if _, ok := p.Errors[tt.field]; !ok {
					t.Errorf("no field error for %s: %v", tt.field, p.Errors)
				}
			}
		})
	}
}

// TestCreateContract_SameProductOnTwoLines: obligations are identified by line,
// so one product may appear twice in a contract.
func TestCreateContract_SameProductOnTwoLines(t *testing.T) {
	body := strings.Replace(bundleContract, "0000000000a2", "0000000000a1", 1)
	rec := postContract(t, newHandler(newFakeStore()), body, "k-same-product")
	if rec.Code != http.StatusCreated {
		t.Fatalf("status %d: %s", rec.Code, rec.Body)
	}
}

// TestCreateContract_ResidualWarns checks BIL-0009: the residual obligation is
// returned with a warning and the contract is still created.
func TestCreateContract_ResidualWarns(t *testing.T) {
	body := strings.Replace(bundleContract, `"ssp": "1500", "sspSource": "EXPLICIT"`, `"ssp": "1500", "sspSource": "RESIDUAL"`, 1)
	rec := postContract(t, newHandler(newFakeStore()), body, "k-residual")
	if rec.Code != http.StatusCreated {
		t.Fatalf("status %d: %s", rec.Code, rec.Body)
	}
	c := decodeContract(t, rec)
	if len(c.Warnings) != 1 || c.Warnings[0].Code != "BIL-0009" || !strings.Contains(c.Warnings[0].Detail, "line 2") {
		t.Errorf("warnings %+v, want one BIL-0009 naming line 2", c.Warnings)
	}
}

func TestCreateContract_OtherLegalEntityScopeIsForbidden(t *testing.T) {
	f := newFakeStore()
	rec := callAs(t, newHandler(f), http.MethodPost, "/v1/revrec/contracts", bundleContract, allDuties, ids.New(), map[string]string{idempotencyKey: "k-scope"})
	if p := decodeProblem(t, rec); rec.Code != http.StatusForbidden || p.Code != "AUTH-0003" || f.creates != 0 {
		t.Errorf("status %d code %s creates %d, want 403 AUTH-0003 and nothing stored", rec.Code, p.Code, f.creates)
	}
}

func TestGetAndListContracts(t *testing.T) {
	h := newHandler(newFakeStore())
	created := decodeContract(t, postContract(t, h, bundleContract, idemKey))

	ok := call(t, h, http.MethodGet, "/v1/revrec/contracts/"+created.ID, "", nil)
	if ok.Code != http.StatusOK || decodeContract(t, ok).ID != created.ID || ok.Header().Get("ETag") != `"1"` {
		t.Fatalf("get status %d ETag %q: %s", ok.Code, ok.Header().Get("ETag"), ok.Body)
	}
	for _, path := range []string{"/v1/revrec/contracts/" + ids.New(), "/v1/revrec/contracts/not-a-uuid"} {
		missing := call(t, h, http.MethodGet, path, "", nil)
		if p := decodeProblem(t, missing); missing.Code != http.StatusNotFound || p.Code != store.CodeNotFound {
			t.Errorf("%s: %d %s, want 404 %s", path, missing.Code, p.Code, store.CodeNotFound)
		}
	}

	list := call(t, h, http.MethodGet, "/v1/revrec/contracts?customerId="+customerID, "", nil)
	var page struct {
		Items []contractSummaryOut `json:"items"`
	}
	if err := json.Unmarshal(list.Body.Bytes(), &page); err != nil || list.Code != http.StatusOK || len(page.Items) != 1 {
		t.Fatalf("list %d %s", list.Code, list.Body)
	}
	if row := page.Items[0]; row.Recognized != "7200.00" || row.Remaining != "3600.00" || row.TransactionPrice != "10800.00" {
		t.Errorf("list row %+v, want price 10800.00, recognized 7200.00, remaining 3600.00", row)
	}
	bad := call(t, h, http.MethodGet, "/v1/revrec/contracts?status=DONE&limit=500", "", nil)
	if p := decodeProblem(t, bad); bad.Code != http.StatusBadRequest || p.Errors["status"] == "" || p.Errors["limit"] == "" {
		t.Errorf("bad filters %d %+v, want 400 with status and limit errors", bad.Code, p.Errors)
	}
}

// TestSatisfyMilestone satisfies the installation milestone (200), replays a
// retry on the same date, refuses another date (409 REV-0004), a date before
// the contract (422 REV-0006), a non-milestone (422 REV-0005) and a stale
// If-Match (412 REV-0103).
func TestSatisfyMilestone(t *testing.T) {
	a, h := newAPI(newFakeStore())
	created := decodeContract(t, postContract(t, h, bundleContract, idemKey))
	path := "/v1/revrec/milestones/" + created.Obligations[1].ID + ":satisfy"
	body := `{"satisfiedOn": "2027-03-25"}`

	stale := call(t, h, http.MethodPost, path, body, map[string]string{"If-Match": `"5"`})
	if p := decodeProblem(t, stale); stale.Code != http.StatusPreconditionFailed || p.Code != store.CodeVersion {
		t.Errorf("stale If-Match %d %s, want 412 %s", stale.Code, p.Code, store.CodeVersion)
	}
	early := call(t, h, http.MethodPost, path, `{"satisfiedOn": "2027-03-09"}`, nil)
	if p := decodeProblem(t, early); early.Code != http.StatusUnprocessableEntity || p.Code != "REV-0006" {
		t.Errorf("before the contract date %d %s, want 422 REV-0006", early.Code, p.Code)
	}

	first := call(t, h, http.MethodPost, path, body, map[string]string{"If-Match": `"1"`})
	if first.Code != http.StatusOK {
		t.Fatalf("satisfy status %d: %s", first.Code, first.Body)
	}
	var s satisfactionOut
	if err := json.Unmarshal(first.Body.Bytes(), &s); err != nil {
		t.Fatal(err)
	}
	if s.Amount != "1350.00" || s.RecognizedOn != "2027-03-25" || s.ContractVersion != 2 || first.Header().Get("ETag") != `"2"` {
		t.Errorf("satisfaction %+v ETag %q, want 1350.00 on 2027-03-25 at version 2", s, first.Header().Get("ETag"))
	}

	retry := call(t, h, http.MethodPost, path, body, map[string]string{"If-Match": `"1"`})
	if retry.Code != http.StatusOK || retry.Header().Get("Idempotent-Replayed") != "true" {
		t.Errorf("retry %d replayed %q, want 200 replay", retry.Code, retry.Header().Get("Idempotent-Replayed"))
	}
	again := call(t, h, http.MethodPost, path, `{"satisfiedOn": "2027-04-01"}`, nil)
	if p := decodeProblem(t, again); again.Code != http.StatusConflict || p.Code != "REV-0004" {
		t.Errorf("another date %d %s, want 409 REV-0004", again.Code, p.Code)
	}
	pit := call(t, h, http.MethodPost, "/v1/revrec/milestones/"+created.Obligations[0].ID+":satisfy", body, nil)
	if p := decodeProblem(t, pit); pit.Code != http.StatusUnprocessableEntity || p.Code != "REV-0005" {
		t.Errorf("point in time %d %s, want 422 REV-0005", pit.Code, p.Code)
	}
	badDate := call(t, h, http.MethodPost, path, `{"satisfiedOn": "25.03.2027"}`, nil)
	if p := decodeProblem(t, badDate); badDate.Code != http.StatusBadRequest || p.Errors["satisfiedOn"] == "" {
		t.Errorf("bad date %d %+v, want 400 with a satisfiedOn error", badDate.Code, p.Errors)
	}
	if bad := call(t, h, http.MethodPost, path, body, map[string]string{"If-Match": "soon"}); bad.Code != http.StatusBadRequest {
		t.Errorf("malformed If-Match %d, want 400", bad.Code)
	}
	if got := a.milestonesSatisfied.Load(); got != 1 {
		t.Errorf("milestones satisfied counter %d, want 1 (replays not counted)", got)
	}
}

func TestRecognitionRun_CreatedReplayedAndGuarded(t *testing.T) {
	f := newFakeStore()
	a, h := newAPI(f)
	body := `{"legalEntityId": "` + entityID + `", "periodEnd": "2027-03-31"}`
	first := call(t, h, http.MethodPost, "/v1/revrec/recognition-runs", body, nil)
	if first.Code != http.StatusCreated {
		t.Fatalf("first run status %d: %s", first.Code, first.Body)
	}
	second := call(t, h, http.MethodPost, "/v1/revrec/recognition-runs", body, nil)
	if second.Code != http.StatusOK || second.Header().Get("Idempotent-Replayed") != "true" {
		t.Fatalf("second run status %d, want a 200 replay", second.Code)
	}
	var r1, r2 runOut
	_ = json.Unmarshal(first.Body.Bytes(), &r1)
	_ = json.Unmarshal(second.Body.Bytes(), &r2)
	if r1.ID != r2.ID || r2.PeriodEnd != "2027-03-31" || r2.CreatedAt != "2027-04-01T01:00:00Z" {
		t.Errorf("runs %+v and %+v should be the same run for 2027-03-31", r1, r2)
	}
	if a.linesRecognized.Load() != 1 {
		t.Errorf("lines recognized counter %d, want 1", a.linesRecognized.Load())
	}

	f.lastRun = ""
	future := call(t, h, http.MethodPost, "/v1/revrec/recognition-runs", `{"legalEntityId": "`+entityID+`", "periodEnd": "2027-04-30"}`, nil)
	if p := decodeProblem(t, future); future.Code != http.StatusUnprocessableEntity || p.Code != CodeFuturePeriod || f.lastRun != "" {
		t.Errorf("future period %d %s (store called: %q), want 422 %s and no run", future.Code, p.Code, f.lastRun, CodeFuturePeriod)
	}
	scoped := callAs(t, h, http.MethodPost, "/v1/revrec/recognition-runs", body, allDuties, ids.New(), nil)
	if scoped.Code != http.StatusForbidden {
		t.Errorf("other legal-entity scope %d, want 403", scoped.Code)
	}
	invalid := call(t, h, http.MethodPost, "/v1/revrec/recognition-runs", `{"legalEntityId": "x", "periodEnd": "March"}`, nil)
	if p := decodeProblem(t, invalid); invalid.Code != http.StatusBadRequest || p.Errors["legalEntityId"] == "" || p.Errors["periodEnd"] == "" {
		t.Errorf("invalid run %d %+v", invalid.Code, p.Errors)
	}

	list := call(t, h, http.MethodGet, "/v1/revrec/recognition-runs?legalEntityId="+entityID, "", nil)
	var page struct {
		Items []runOut `json:"items"`
	}
	if err := json.Unmarshal(list.Body.Bytes(), &page); err != nil || len(page.Items) != 1 || page.Items[0].ID != r1.ID {
		t.Errorf("run list %d %s", list.Code, list.Body)
	}
}

// TestRollForward_AC29_4 checks the handler formats the AC-29.4 movement and
// rejects malformed parameters.
func TestRollForward_AC29_4(t *testing.T) {
	h := newHandler(newFakeStore())
	path := "/v1/revrec/roll-forward?legalEntityId=" + entityID + "&currency=USD&period=2027-03"
	rec := call(t, h, http.MethodGet, path, "", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d: %s", rec.Code, rec.Body)
	}
	var rf rollForwardOut
	if err := json.Unmarshal(rec.Body.Bytes(), &rf); err != nil {
		t.Fatal(err)
	}
	if rf.Opening != "0.00" || rf.Additions != "3600.00" || rf.Recognized != "1483.06" || rf.Closing != "2116.94" || rf.Start != "2027-03-01" || rf.End != "2027-03-31" {
		t.Errorf("roll-forward %+v", rf)
	}
	bad := call(t, h, http.MethodGet, "/v1/revrec/roll-forward?legalEntityId=x&currency=usd&period=2027-3-01", "", nil)
	if p := decodeProblem(t, bad); bad.Code != http.StatusBadRequest || p.Errors["period"] == "" || p.Errors["currency"] == "" || p.Errors["legalEntityId"] == "" {
		t.Errorf("bad parameters %d %+v, want 400 with three field errors", bad.Code, p.Errors)
	}
}

// TestRoutes_EnforceDuties: reads need RevRecView, contracts and milestones
// RevRecMaintain, runs RevRecPost (§5.29.17).
func TestRoutes_EnforceDuties(t *testing.T) {
	h := newHandler(newFakeStore())
	tests := []struct {
		method, path, body, duties string
	}{
		{http.MethodPost, "/v1/revrec/contracts", bundleContract, "revrec.view,revrec.post"},
		{http.MethodGet, "/v1/revrec/contracts", "", "revrec.maintain,revrec.post"},
		{http.MethodGet, "/v1/revrec/contracts/" + ids.New(), "", "revrec.maintain"},
		{http.MethodPost, "/v1/revrec/milestones/" + ids.New() + ":satisfy", `{"satisfiedOn":"2027-03-25"}`, "revrec.view,revrec.post"},
		{http.MethodPost, "/v1/revrec/recognition-runs", `{"legalEntityId":"` + entityID + `","periodEnd":"2027-03-31"}`, "revrec.view,revrec.maintain"},
		{http.MethodGet, "/v1/revrec/recognition-runs", "", "revrec.post"},
		{http.MethodGet, "/v1/revrec/roll-forward?legalEntityId=" + entityID + "&currency=USD&period=2027-03", "", "revrec.post"},
	}
	for _, tt := range tests {
		rec := callAs(t, h, tt.method, tt.path, tt.body, tt.duties, "", map[string]string{idempotencyKey: "k"})
		if p := decodeProblem(t, rec); rec.Code != http.StatusForbidden || p.Code != "AUTH-0003" {
			t.Errorf("%s %s with %q: %d %s, want 403 AUTH-0003", tt.method, tt.path, tt.duties, rec.Code, p.Code)
		}
	}
}

type downStore struct{ *fakeStore }

func (downStore) Ping(context.Context) error { return errors.New("connection refused") }

func TestReadyz_ReflectsDatabase(t *testing.T) {
	up := httptest.NewRecorder()
	newHandler(newFakeStore()).ServeHTTP(up, httptest.NewRequest(http.MethodGet, "/readyz", nil))
	down := httptest.NewRecorder()
	newHandler(downStore{newFakeStore()}).ServeHTTP(down, httptest.NewRequest(http.MethodGet, "/readyz", nil))
	if up.Code != http.StatusOK || down.Code != http.StatusServiceUnavailable {
		t.Errorf("readyz up %d down %d, want 200 and 503", up.Code, down.Code)
	}
}

// TestListContracts_PagesAndFiltersByMethod pages three contracts one at a
// time through nextCursor (newest first, none repeated), filters by obligation
// method (§5.29.14) and refuses a malformed cursor or method.
func TestListContracts_PagesAndFiltersByMethod(t *testing.T) {
	h := newHandler(newFakeStore())
	var created []string
	for i, body := range []string{bundleContract, bundleContract, pointInTimeOnly} {
		rec := postContract(t, h, body, fmt.Sprintf("list-key-%d", i))
		if rec.Code != http.StatusCreated {
			t.Fatalf("create %d: %d %s", i, rec.Code, rec.Body)
		}
		created = append(created, decodeContract(t, rec).ID)
	}
	type page struct {
		Items      []contractSummaryOut `json:"items"`
		NextCursor string               `json:"nextCursor"`
	}
	get := func(path string) page {
		t.Helper()
		rec := call(t, h, http.MethodGet, path, "", nil)
		var p page
		if rec.Code != http.StatusOK || json.Unmarshal(rec.Body.Bytes(), &p) != nil {
			t.Fatalf("%s: %d %s", path, rec.Code, rec.Body)
		}
		return p
	}
	var seen []string
	cursor := ""
	for i := 0; i < 3; i++ {
		path := "/v1/revrec/contracts?limit=1"
		if cursor != "" {
			path += "&cursor=" + cursor
		}
		p := get(path)
		if len(p.Items) != 1 {
			t.Fatalf("page %d has %d items, want 1", i, len(p.Items))
		}
		seen = append(seen, p.Items[0].ID)
		cursor = p.NextCursor
		if (i < 2) != (cursor != "") {
			t.Fatalf("page %d nextCursor %q: want one on every page but the last", i, cursor)
		}
	}
	want := append([]string(nil), created...)
	sort.Sort(sort.Reverse(sort.StringSlice(want)))
	if strings.Join(seen, ",") != strings.Join(want, ",") {
		t.Errorf("paged %v, want every contract once, newest first %v", seen, want)
	}

	if p := get("/v1/revrec/contracts?method=RATABLE"); len(p.Items) != 2 || p.NextCursor != "" {
		t.Errorf("RATABLE filter: %d items cursor %q, want the two bundles and no cursor", len(p.Items), p.NextCursor)
	}
	if p := get("/v1/revrec/contracts?method=POINT_IN_TIME"); len(p.Items) != 3 {
		t.Errorf("POINT_IN_TIME filter: %d items, want 3", len(p.Items))
	}

	bad := call(t, h, http.MethodGet, "/v1/revrec/contracts?cursor=bm9wZQ&method=BARTER", "", nil)
	if p := decodeProblem(t, bad); bad.Code != http.StatusBadRequest || p.Errors["cursor"] == "" || p.Errors["method"] == "" {
		t.Errorf("bad cursor and method %d %+v, want 400 with both field errors", bad.Code, p.Errors)
	}
}

// pointInTimeOnly is a single goods obligation recognized at transfer.
const pointInTimeOnly = `{
  "legalEntityId": "` + entityID + `",
  "orderId": "018f0000-0000-7000-8000-0000000000f2",
  "customerId": "` + customerID + `",
  "currency": "USD",
  "transactionPrice": "500.00",
  "contractDate": "2027-03-10",
  "obligations": [
    {"productId": "018f0000-0000-7000-8000-0000000000a1", "ssp": "500", "sspSource": "LIST", "method": "POINT_IN_TIME", "satisfiedOn": "2027-03-10"}
  ]
}`

// TestCreateContract_MonthlyBasis plans the BR-29.1 support (2,250.00 over
// 2027-03-10 .. 2028-03-09) on the MONTHLY basis: 13 equal shares of 173.08
// with the residual 173.04 last, where the DAILY default starts with 133.06.
// The basis is stored and returned; an unknown basis is a field error.
func TestCreateContract_MonthlyBasis(t *testing.T) {
	h := newHandler(newFakeStore())
	monthly := strings.Replace(bundleContract, `"contractDate"`, `"ratableBasis": "MONTHLY", "contractDate"`, 1)
	rec := postContract(t, h, monthly, "monthly-key")
	if rec.Code != http.StatusCreated {
		t.Fatalf("status %d: %s", rec.Code, rec.Body)
	}
	c := decodeContract(t, rec)
	if c.RatableBasis != domain.BasisMonthly {
		t.Errorf("ratableBasis %q, want MONTHLY", c.RatableBasis)
	}
	sup := c.Obligations[2].Schedule
	if len(sup) != 13 || sup[0].Amount != "173.08" || sup[11].Amount != "173.08" || sup[12].Amount != "173.04" {
		t.Errorf("monthly support schedule %+v, want 12 x 173.08 and 173.04", sup)
	}
	if d := decodeContract(t, postContract(t, h, bundleContract, "daily-key")); d.RatableBasis != domain.BasisDaily || d.Obligations[2].Schedule[0].Amount != "133.06" {
		t.Errorf("default basis %q first line %s, want DAILY and 133.06", d.RatableBasis, d.Obligations[2].Schedule[0].Amount)
	}
	bad := postContract(t, h, strings.Replace(bundleContract, `"contractDate"`, `"ratableBasis": "WEEKLY", "contractDate"`, 1), "weekly-key")
	if p := decodeProblem(t, bad); bad.Code != http.StatusBadRequest || p.Errors["ratableBasis"] == "" {
		t.Errorf("unknown basis %d %+v, want 400 with a ratableBasis error", bad.Code, p.Errors)
	}
}

// TestRollForward_ContractDrillDown reports one contract's movement in its own
// currency (FR-29.9 per contract), answers 404 for an unknown contract and
// refuses mixing contractId with the entity parameters.
func TestRollForward_ContractDrillDown(t *testing.T) {
	h := newHandler(newFakeStore())
	created := decodeContract(t, postContract(t, h, bundleContract, idemKey))
	rec := call(t, h, http.MethodGet, "/v1/revrec/roll-forward?contractId="+created.ID+"&period=2027-03", "", nil)
	var rf rollForwardOut
	if rec.Code != http.StatusOK || json.Unmarshal(rec.Body.Bytes(), &rf) != nil {
		t.Fatalf("status %d: %s", rec.Code, rec.Body)
	}
	if rf.ContractID != created.ID || rf.LegalEntityID != entityID || rf.Currency != "USD" || rf.Additions != "3600.00" || rf.Closing != "2116.94" {
		t.Errorf("contract roll-forward %+v", rf)
	}
	missing := call(t, h, http.MethodGet, "/v1/revrec/roll-forward?contractId="+ids.New()+"&period=2027-03", "", nil)
	if p := decodeProblem(t, missing); missing.Code != http.StatusNotFound || p.Code != store.CodeNotFound {
		t.Errorf("unknown contract %d %s, want 404 %s", missing.Code, p.Code, store.CodeNotFound)
	}
	mixed := call(t, h, http.MethodGet, "/v1/revrec/roll-forward?contractId="+created.ID+"&currency=EUR&period=2027-03", "", nil)
	if p := decodeProblem(t, mixed); mixed.Code != http.StatusBadRequest || p.Errors["contractId"] == "" {
		t.Errorf("contractId with currency %d %+v, want 400 with a contractId error", mixed.Code, p.Errors)
	}
}

func TestCreateContract_RefusesCallerWithoutLegalEntityScope(t *testing.T) {
	f := newFakeStore()
	rec := callAs(t, newHandler(f), http.MethodPost, "/v1/revrec/contracts", bundleContract, allDuties, "", map[string]string{idempotencyKey: "k-noscope"})
	if p := decodeProblem(t, rec); rec.Code != http.StatusForbidden || p.Code != "AUTH-0003" {
		t.Fatalf("status %d code %s, want 403 AUTH-0003", rec.Code, p.Code)
	}
}
