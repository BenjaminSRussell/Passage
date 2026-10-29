package api_test

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/platform/tenant"
	"github.com/BenjaminSRussell/passage/services/expense/internal/api"
	"github.com/BenjaminSRussell/passage/services/expense/internal/store"
)

const (
	dutyApprove = "expense.approve"
	dutyAdmin   = "expense.advance.admin"
	dutyImport  = "expense.card.import"
)

// fakeTravel is an in-memory travelStore that records what the handlers pass it and
// replays creates by key like the Postgres store.
type fakeTravel struct {
	advances     map[string]store.Advance
	requisitions map[string]store.Requisition
	imports      map[string]store.Import
	hashes       map[string]string

	lastPolicy  store.AdvancePolicy
	lastAsOf    time.Time
	lastReq     store.NewRequisition
	lastImport  store.NewImport
	lastCommand store.Command
	lastApprove bool
	lastAmount  decimal.Decimal
	lastPrepay  string
	lastAdvF    store.AdvanceFilter
	lastReqF    store.RequisitionFilter
	lastCardF   store.CardFilter
	lastAfter   string
	lastLimit   int
	fail        error
}

func newFakeTravel() *fakeTravel {
	return &fakeTravel{advances: map[string]store.Advance{}, requisitions: map[string]store.Requisition{}, imports: map[string]store.Import{}, hashes: map[string]string{}}
}

func (f *fakeTravel) replay(key, hash string) (bool, error) {
	stored, ok := f.hashes[key]
	if !ok {
		f.hashes[key] = hash
		return false, nil
	}
	if stored != hash {
		return false, httpx.NewProblem(http.StatusUnprocessableEntity, store.CodeIdempotencyReuse, "reused")
	}
	return true, nil
}

func paidAdvance(id string) store.Advance {
	thousand := decimal.NewFromInt(1000)
	return store.Advance{ID: id, LegalEntityID: ids.New(), WorkerID: ids.New(), Amount: thousand, Currency: "USD", Purpose: "Trip",
		TripEnd: time.Date(2025, 2, 10, 0, 0, 0, 0, time.UTC), DueDate: time.Date(2025, 3, 12, 0, 0, 0, 0, time.UTC), Status: "PAID",
		Paid: thousand, Settled: decimal.RequireFromString("820"), Returned: decimal.Zero, Recovered: decimal.Zero, Version: 3,
		CreatedAt: time.Date(2025, 2, 1, 9, 0, 0, 0, time.UTC)}
}

func (f *fakeTravel) RequestAdvance(_ context.Context, key, hash string, in store.NewAdvance, pol store.AdvancePolicy, asOf time.Time) (store.Advance, bool, error) {
	f.lastPolicy, f.lastAsOf = pol, asOf
	if f.fail != nil {
		return store.Advance{}, false, f.fail
	}
	replayed, err := f.replay(key, hash)
	if err != nil {
		return store.Advance{}, false, err
	}
	if replayed {
		return f.advances[key], true, nil
	}
	adv := store.Advance{ID: ids.New(), LegalEntityID: in.LegalEntityID, WorkerID: in.WorkerID, Amount: in.Amount, Currency: in.Currency,
		Purpose: in.Purpose, TravelRequisitionID: in.TravelRequisitionID, TripEnd: in.TripEnd, DueDate: in.TripEnd.AddDate(0, 0, pol.DueDays),
		Status: "SUBMITTED", Version: 1}
	f.advances[key] = adv
	return adv, false, nil
}

func (f *fakeTravel) GetAdvance(_ context.Context, id string) (store.Advance, error) {
	return paidAdvance(id), nil
}

func (f *fakeTravel) ListAdvances(_ context.Context, filter store.AdvanceFilter, after string, limit int) ([]store.Advance, string, error) {
	f.lastAdvF, f.lastAfter, f.lastLimit = filter, after, limit
	return []store.Advance{paidAdvance(ids.New())}, "", nil
}

func (f *fakeTravel) DecideAdvance(_ context.Context, id string, approve bool, c store.Command) (store.Advance, bool, error) {
	f.lastApprove, f.lastCommand = approve, c
	adv := paidAdvance(id)
	adv.Status, adv.Paid, adv.Settled, adv.Version = "APPROVED", decimal.Zero, decimal.Zero, 2
	return adv, false, nil
}

func (f *fakeTravel) PayAdvance(_ context.Context, id, prepaymentID string, c store.Command) (store.Advance, bool, error) {
	f.lastPrepay, f.lastCommand = prepaymentID, c
	adv := paidAdvance(id)
	adv.Settled, adv.APPrepaymentID = decimal.Zero, prepaymentID
	return adv, false, nil
}

func (f *fakeTravel) ReturnAdvance(_ context.Context, id string, amount decimal.Decimal, c store.Command) (store.Advance, bool, error) {
	f.lastAmount, f.lastCommand = amount, c
	adv := paidAdvance(id)
	adv.Returned, adv.Status, adv.Version = amount, "SETTLED", 4
	return adv, false, nil
}

func (f *fakeTravel) CreateRequisition(_ context.Context, key, hash string, in store.NewRequisition) (store.Requisition, bool, error) {
	f.lastReq = in
	replayed, err := f.replay(key, hash)
	if err != nil {
		return store.Requisition{}, false, err
	}
	if replayed {
		return f.requisitions[key], true, nil
	}
	total := decimal.Zero
	for _, e := range in.Estimates {
		total = total.Add(e.Amount)
	}
	rq := store.Requisition{ID: ids.New(), LegalEntityID: in.LegalEntityID, WorkerID: in.WorkerID, Purpose: in.Purpose, Currency: in.Currency,
		Status: "DRAFT", TripFrom: in.TripFrom, TripTo: in.TripTo, Destinations: in.Destinations, Estimates: in.Estimates, Estimated: total, Version: 1}
	f.requisitions[key] = rq
	return rq, false, nil
}

func (f *fakeTravel) GetRequisition(_ context.Context, id string) (store.Requisition, error) {
	return store.Requisition{ID: id, Status: "APPROVED", Currency: "EUR", Estimated: decimal.RequireFromString("2000"), Version: 3}, nil
}

func (f *fakeTravel) ListRequisitions(_ context.Context, filter store.RequisitionFilter, after string, limit int) ([]store.Requisition, string, error) {
	f.lastReqF, f.lastAfter, f.lastLimit = filter, after, limit
	return nil, "", nil
}

func (f *fakeTravel) SubmitRequisition(_ context.Context, id string, c store.Command) (store.Requisition, bool, error) {
	f.lastCommand = c
	return store.Requisition{ID: id, Status: "SUBMITTED", Currency: "USD", Version: 2}, false, nil
}

func (f *fakeTravel) DecideRequisition(_ context.Context, id string, approve bool, c store.Command) (store.Requisition, bool, error) {
	f.lastApprove, f.lastCommand = approve, c
	return store.Requisition{ID: id, Status: "APPROVED", Currency: "USD", Version: 3}, false, nil
}

func (f *fakeTravel) ImportCardFeed(_ context.Context, key, hash string, in store.NewImport) (store.Import, bool, error) {
	f.lastImport = in
	replayed, err := f.replay(key, hash)
	if err != nil {
		return store.Import{}, false, err
	}
	if replayed {
		return f.imports[key], true, nil
	}
	imp := store.Import{ID: ids.New(), LegalEntityID: in.LegalEntityID, Format: in.Format, FileHash: in.FileHash, Imported: len(in.Rows), Quarantined: in.Quarantined}
	f.imports[key] = imp
	return imp, false, nil
}

func (f *fakeTravel) ListCardTransactions(_ context.Context, filter store.CardFilter, after string, limit int) ([]store.CardTransaction, string, error) {
	f.lastCardF, f.lastAfter, f.lastLimit = filter, after, limit
	return []store.CardTransaction{{ID: ids.New(), ImportID: ids.New(), WorkerID: ids.New(), Line: 2, CardLast4: "4242",
		TxnDate: time.Date(2025, 3, 1, 0, 0, 0, 0, time.UTC), PostDate: time.Date(2025, 3, 2, 0, 0, 0, 0, time.UTC),
		Merchant: "Hotel", Amount: decimal.RequireFromString("120.5"), Currency: "USD", Status: "UNASSIGNED"}}, "", nil
}

func travelRouter(s *fakeTravel) http.Handler {
	r := httpx.NewRouter()
	a := api.New(nil).WithClock(func() time.Time { return clock })
	if s != nil {
		a.WithTravel(s)
	}
	a.Register(r)
	return r
}

func duties(d ...string) header { return header{tenant.HeaderDuties, strings.Join(d, ",")} }

func decode(t *testing.T, body []byte, v any) {
	t.Helper()
	if err := json.Unmarshal(body, v); err != nil {
		t.Fatalf("decode %q: %v", body, err)
	}
}

func goodAdvance() map[string]string {
	return map[string]string{"legalEntityId": ids.New(), "workerId": ids.New(), "amount": "1000.00", "currency": "USD",
		"purpose": "Trip to Boston", "tripEnd": "2025-03-10"}
}

func TestTravelRoutesAnswer503WithoutAStore(t *testing.T) {
	h := travelRouter(nil)
	for _, path := range []string{"/v1/expense/cash-advances", "/v1/expense/travel-requisitions", "/v1/expense/card-transactions"} {
		rec := send(t, h, http.MethodGet, path, "", nil)
		if rec.Code != http.StatusServiceUnavailable || problemOf(t, rec).Code != "COM-0503" {
			t.Errorf("GET %s = %d %s; want 503 COM-0503", path, rec.Code, rec.Body.String())
		}
	}
}

// Decisions need MssApprove, payment and returns ExpenseAdmin, import the card duty;
// the EssExpense duty alone is refused on each.
func TestTravelRoutesEnforceTheirDuties(t *testing.T) {
	h := travelRouter(newFakeTravel())
	id := ids.New()
	for _, c := range []struct{ path, duty string }{
		{"/v1/expense/cash-advances/" + id + "/decide", dutyApprove},
		{"/v1/expense/cash-advances/" + id + "/pay", dutyAdmin},
		{"/v1/expense/cash-advances/" + id + "/return", dutyAdmin},
		{"/v1/expense/travel-requisitions/" + id + "/decide", dutyApprove},
		{"/v1/expense/card-transactions/import", dutyImport},
	} {
		if rec := send(t, h, http.MethodPost, c.path, "k", map[string]string{}); rec.Code != http.StatusForbidden {
			t.Errorf("POST %s with only %s = %d; want 403", c.path, dutyWrite, rec.Code)
		}
		if rec := send(t, h, http.MethodPost, c.path, "k", map[string]string{}, duties(c.duty)); rec.Code == http.StatusForbidden {
			t.Errorf("POST %s with %s = 403; want the duty to be enough", c.path, c.duty)
		}
	}
	for _, path := range []string{"/v1/expense/cash-advances", "/v1/expense/travel-requisitions", "/v1/expense/card-transactions"} {
		if rec := send(t, h, http.MethodGet, path, "", nil, duties(dutyApprove)); rec.Code != http.StatusForbidden {
			t.Errorf("GET %s without %s = %d; want 403", path, dutyWrite, rec.Code)
		}
	}
}

func TestRequestAdvancePassesPolicyDefaultsAndTheClockDay(t *testing.T) {
	f := newFakeTravel()
	h := travelRouter(f)
	body := goodAdvance()
	rec := send(t, h, http.MethodPost, "/v1/expense/cash-advances", "adv-1", body)
	if rec.Code != http.StatusCreated || rec.Header().Get("ETag") != `"1"` {
		t.Fatalf("request = %d etag %q %s; want 201 with ETag \"1\"", rec.Code, rec.Header().Get("ETag"), rec.Body.String())
	}
	if !f.lastPolicy.MaxAmount.Equal(decimal.NewFromInt(2000)) || f.lastPolicy.DueDays != 30 {
		t.Errorf("policy = %+v; want the 8.43.13 defaults 2000.00 and 30 days", f.lastPolicy)
	}
	// The clock is 23:30 EST on 4 March, which is 5 March in UTC.
	if want := time.Date(2025, 3, 5, 0, 0, 0, 0, time.UTC); !f.lastAsOf.Equal(want) {
		t.Errorf("asOf = %s; want %s (UTC calendar day of the clock)", f.lastAsOf, want)
	}
	var got map[string]any
	decode(t, rec.Body.Bytes(), &got)
	if got["status"] != "SUBMITTED" || got["dueDate"] != "2025-04-09" || got["open"] != "0.00" || got["travelRequisitionId"] != nil {
		t.Errorf("body = %v; want SUBMITTED, due 2025-04-09, open 0.00, no requisition", got)
	}
	again := send(t, h, http.MethodPost, "/v1/expense/cash-advances", "adv-1", body)
	if again.Code != http.StatusOK || again.Header().Get("Idempotent-Replayed") != "true" {
		t.Fatalf("replay = %d %v; want 200 Idempotent-Replayed", again.Code, again.Header())
	}
	body["amount"] = "999.00"
	if rec := send(t, h, http.MethodPost, "/v1/expense/cash-advances", "adv-1", body); problemOf(t, rec).Code != store.CodeIdempotencyReuse {
		t.Fatalf("reused key with another body = %d %s; want %s", rec.Code, rec.Body.String(), store.CodeIdempotencyReuse)
	}
}

func TestRequestAdvanceValidates(t *testing.T) {
	h := travelRouter(newFakeTravel())
	if rec := send(t, h, http.MethodPost, "/v1/expense/cash-advances", "", goodAdvance()); problemOf(t, rec).Errors["Idempotency-Key"] == "" {
		t.Fatalf("missing key = %d %s; want 400 naming Idempotency-Key", rec.Code, rec.Body.String())
	}
	for _, c := range []struct{ field, value string }{
		{"amount", "0"},
		{"amount", "-5"},
		{"amount", "10.005"},
		{"currency", "usd"},
		{"purpose", "  "},
		{"tripEnd", "10/03/2025"},
		{"travelRequisitionId", "R-1"},
		{"workerId", "w1"},
	} {
		body := goodAdvance()
		body[c.field] = c.value
		rec := send(t, h, http.MethodPost, "/v1/expense/cash-advances", "k-"+c.field+c.value, body)
		if p := problemOf(t, rec); rec.Code != http.StatusBadRequest || p.Errors[c.field] == "" {
			t.Errorf("%s=%q: %d %v; want 400 naming %s", c.field, c.value, rec.Code, p.Errors, c.field)
		}
	}
	// JPY has no minor unit, so 1000.5 JPY is refused rather than rounded.
	body := goodAdvance()
	body["currency"], body["amount"] = "JPY", "1000.5"
	if rec := send(t, h, http.MethodPost, "/v1/expense/cash-advances", "k-jpy", body); problemOf(t, rec).Errors["amount"] == "" {
		t.Errorf("1000.5 JPY = %d %s; want an amount error", rec.Code, rec.Body.String())
	}
}

func TestRequestAdvanceSurfacesStoreRefusals(t *testing.T) {
	f := newFakeTravel()
	f.fail = httpx.NewProblem(http.StatusUnprocessableEntity, store.CodeAdvanceBlocked, "blocked")
	rec := send(t, travelRouter(f), http.MethodPost, "/v1/expense/cash-advances", "k", goodAdvance())
	if rec.Code != http.StatusUnprocessableEntity || problemOf(t, rec).Code != store.CodeAdvanceBlocked {
		t.Fatalf("blocked = %d %s; want 422 %s", rec.Code, rec.Body.String(), store.CodeAdvanceBlocked)
	}
}

func TestGetAdvanceShowsOpenBalanceAndOverdue(t *testing.T) {
	h := travelRouter(newFakeTravel())
	rec := send(t, h, http.MethodGet, "/v1/expense/cash-advances/"+ids.New(), "", nil)
	if rec.Code != http.StatusOK || rec.Header().Get("ETag") != `"3"` {
		t.Fatalf("get = %d %s", rec.Code, rec.Body.String())
	}
	var got map[string]any
	decode(t, rec.Body.Bytes(), &got)
	// Paid 1,000.00, settled 820.00: open 180.00 (AC-43.9), due 12 March, clock 5 March.
	if got["open"] != "180.00" || got["settled"] != "820.00" || got["overdue"] != false {
		t.Errorf("body = %v; want open 180.00, settled 820.00, not overdue", got)
	}
	if rec := send(t, h, http.MethodGet, "/v1/expense/cash-advances/nope", "", nil); rec.Code != http.StatusBadRequest {
		t.Errorf("bad id = %d; want 400", rec.Code)
	}
}

func TestListAdvancesFilters(t *testing.T) {
	f := newFakeTravel()
	h := travelRouter(f)
	worker, cursor := ids.New(), ids.New()
	rec := send(t, h, http.MethodGet, "/v1/expense/cash-advances?status=PAID&workerId="+worker+"&cursor="+cursor+"&limit=10", "", nil)
	if rec.Code != http.StatusOK || f.lastAdvF.Status != "PAID" || f.lastAdvF.WorkerID != worker || f.lastAfter != cursor || f.lastLimit != 10 {
		t.Fatalf("list = %d filter %+v after %q limit %d", rec.Code, f.lastAdvF, f.lastAfter, f.lastLimit)
	}
	rec = send(t, h, http.MethodGet, "/v1/expense/cash-advances?status=OPEN&limit=0&workerId=x", "", nil)
	p := problemOf(t, rec)
	if rec.Code != http.StatusBadRequest || p.Errors["status"] == "" || p.Errors["limit"] == "" || p.Errors["workerId"] == "" {
		t.Fatalf("bad filters = %d %v; want 400 naming status, limit and workerId", rec.Code, p.Errors)
	}
}

// Custom methods take an Idempotency-Key and If-Match, and the request hash binds the
// key to the operation, the record and the body.
func TestAdvanceCommandsCarryKeyVersionAndADistinctHash(t *testing.T) {
	f := newFakeTravel()
	h := travelRouter(f)
	id := ids.New()
	path := "/v1/expense/cash-advances/" + id
	if rec := send(t, h, http.MethodPost, path+"/decide", "", map[string]string{"decision": "APPROVE"}, duties(dutyApprove)); problemOf(t, rec).Errors["Idempotency-Key"] == "" {
		t.Fatalf("decide without key = %d %s; want 400 naming Idempotency-Key", rec.Code, rec.Body.String())
	}
	if rec := send(t, h, http.MethodPost, path+"/decide", "k", map[string]string{"decision": "MAYBE"}, duties(dutyApprove)); problemOf(t, rec).Errors["decision"] == "" {
		t.Fatalf("decision MAYBE = %d %s; want 400 naming decision", rec.Code, rec.Body.String())
	}
	if rec := send(t, h, http.MethodPost, path+"/decide", "k", map[string]string{"decision": "APPROVE"}, duties(dutyApprove), header{"If-Match", "abc"}); problemOf(t, rec).Errors["If-Match"] == "" {
		t.Fatalf("bad If-Match = %d %s; want 400 naming If-Match", rec.Code, rec.Body.String())
	}
	rec := send(t, h, http.MethodPost, path+"/decide", "k1", map[string]string{"decision": "REJECT"}, duties(dutyApprove), header{"If-Match", `"1"`})
	if rec.Code != http.StatusOK || f.lastApprove || f.lastCommand.Key != "k1" || f.lastCommand.ExpectedVersion != 1 {
		t.Fatalf("decide = %d approve=%v command %+v; want reject with key k1 at version 1", rec.Code, f.lastApprove, f.lastCommand)
	}
	rejectHash := f.lastCommand.Hash
	send(t, h, http.MethodPost, path+"/decide", "k1", map[string]string{"decision": "APPROVE"}, duties(dutyApprove), header{"If-Match", `"1"`})
	if !f.lastApprove || f.lastCommand.Hash == rejectHash {
		t.Errorf("approve and reject hashed alike; a key reused for the other decision must not replay")
	}

	if rec := send(t, h, http.MethodPost, path+"/pay", "k2", map[string]string{"prepaymentId": "AP-1"}, duties(dutyAdmin)); problemOf(t, rec).Errors["prepaymentId"] == "" {
		t.Fatalf("pay with a non-UUID prepayment = %d %s; want 400", rec.Code, rec.Body.String())
	}
	prepay := ids.New()
	rec = send(t, h, http.MethodPost, path+"/pay", "k2", map[string]string{"prepaymentId": prepay}, duties(dutyAdmin))
	var got map[string]any
	decode(t, rec.Body.Bytes(), &got)
	if rec.Code != http.StatusOK || f.lastPrepay != prepay || got["apPrepaymentId"] != prepay || got["status"] != "PAID" {
		t.Fatalf("pay = %d %v; want PAID with the prepayment id", rec.Code, got)
	}

	if rec := send(t, h, http.MethodPost, path+"/return", "k3", map[string]string{"amount": "0"}, duties(dutyAdmin)); problemOf(t, rec).Errors["amount"] == "" {
		t.Fatalf("return 0 = %d %s; want 400 naming amount", rec.Code, rec.Body.String())
	}
	rec = send(t, h, http.MethodPost, path+"/return", "k3", map[string]string{"amount": "180.00"}, duties(dutyAdmin))
	if rec.Code != http.StatusOK || !f.lastAmount.Equal(decimal.NewFromInt(180)) || rec.Header().Get("ETag") != `"4"` {
		t.Fatalf("return = %d amount %s etag %s", rec.Code, f.lastAmount, rec.Header().Get("ETag"))
	}
}

func goodRequisition() map[string]any {
	return map[string]any{
		"legalEntityId": ids.New(), "workerId": ids.New(), "purpose": "Customer workshop", "currency": "USD",
		"tripFrom": "2025-04-01", "tripTo": "2025-04-03",
		"destinations": []map[string]string{{"country": "US", "city": "Boston"}},
		"estimates":    []map[string]string{{"category": "lodging", "amount": "1200.00"}, {"category": "AIRFARE", "amount": "800"}},
	}
}

func TestCreateRequisitionNormalisesAndReplays(t *testing.T) {
	f := newFakeTravel()
	h := travelRouter(f)
	body := goodRequisition()
	rec := send(t, h, http.MethodPost, "/v1/expense/travel-requisitions", "rq-1", body)
	if rec.Code != http.StatusCreated {
		t.Fatalf("create = %d %s", rec.Code, rec.Body.String())
	}
	if f.lastReq.Estimates[0].Category != "LODGING" {
		t.Errorf("category = %q; want it upper-cased to LODGING", f.lastReq.Estimates[0].Category)
	}
	var got map[string]any
	decode(t, rec.Body.Bytes(), &got)
	if got["estimatedTotal"] != "2000.00" || got["status"] != "DRAFT" || got["tripTo"] != "2025-04-03" {
		t.Errorf("body = %v; want DRAFT with estimatedTotal 2000.00", got)
	}
	if again := send(t, h, http.MethodPost, "/v1/expense/travel-requisitions", "rq-1", body); again.Code != http.StatusOK || again.Header().Get("Idempotent-Replayed") != "true" {
		t.Fatalf("replay = %d %v; want 200 Idempotent-Replayed", again.Code, again.Header())
	}
}

func TestCreateRequisitionValidates(t *testing.T) {
	h := travelRouter(newFakeTravel())
	cases := []struct {
		name, field string
		change      func(map[string]any)
	}{
		{"trip ends before it starts", "tripTo", func(b map[string]any) { b["tripTo"] = "2025-03-31" }},
		{"no destinations", "destinations", func(b map[string]any) { b["destinations"] = []map[string]string{} }},
		{"destination without city", "destinations[0]", func(b map[string]any) { b["destinations"] = []map[string]string{{"country": "US", "city": " "}} }},
		{"no estimates", "estimates", func(b map[string]any) { b["estimates"] = []map[string]string{} }},
		{"negative estimate", "estimates[0].amount", func(b map[string]any) { b["estimates"] = []map[string]string{{"category": "MEALS", "amount": "-1"}} }},
		{"estimate below the minor unit", "estimates[0].amount", func(b map[string]any) { b["estimates"] = []map[string]string{{"category": "MEALS", "amount": "1.001"}} }},
		{"estimate without category", "estimates[0].category", func(b map[string]any) { b["estimates"] = []map[string]string{{"category": "", "amount": "1"}} }},
		{"lower-case currency", "currency", func(b map[string]any) { b["currency"] = "usd" }},
		{"estimates over the stored bound", "estimates", func(b map[string]any) {
			b["estimates"] = []map[string]string{{"category": "LODGING", "amount": "1000000000000"}, {"category": "AIRFARE", "amount": "0.01"}}
		}},
	}
	for _, c := range cases {
		body := goodRequisition()
		c.change(body)
		rec := send(t, h, http.MethodPost, "/v1/expense/travel-requisitions", "k-"+c.name, body)
		if p := problemOf(t, rec); rec.Code != http.StatusBadRequest || p.Errors[c.field] == "" {
			t.Errorf("%s: %d %v; want 400 naming %s", c.name, rec.Code, p.Errors, c.field)
		}
	}
}

func TestRequisitionSubmitAndDecideTakeCommands(t *testing.T) {
	f := newFakeTravel()
	h := travelRouter(f)
	id := ids.New()
	if rec := send(t, h, http.MethodPost, "/v1/expense/travel-requisitions/"+id+"/submit", "", nil); rec.Code != http.StatusBadRequest {
		t.Fatalf("submit without key = %d; want 400", rec.Code)
	}
	rec := send(t, h, http.MethodPost, "/v1/expense/travel-requisitions/"+id+"/submit", "s1", nil, header{"If-Match", `"1"`})
	if rec.Code != http.StatusOK || f.lastCommand.Key != "s1" || f.lastCommand.ExpectedVersion != 1 {
		t.Fatalf("submit = %d command %+v", rec.Code, f.lastCommand)
	}
	submitHash := f.lastCommand.Hash
	rec = send(t, h, http.MethodPost, "/v1/expense/travel-requisitions/"+id+"/decide", "s1", map[string]string{"decision": "APPROVE"}, duties(dutyApprove), header{"If-Match", `"1"`})
	if rec.Code != http.StatusOK || !f.lastApprove || f.lastCommand.Hash == submitHash {
		t.Fatalf("decide = %d approve=%v; want approve with a hash distinct from submit", rec.Code, f.lastApprove)
	}
	worker := ids.New()
	send(t, h, http.MethodGet, "/v1/expense/travel-requisitions?status=APPROVED&workerId="+worker, "", nil)
	if f.lastReqF.Status != "APPROVED" || f.lastReqF.WorkerID != worker {
		t.Errorf("list filter = %+v", f.lastReqF)
	}
	if rec := send(t, h, http.MethodGet, "/v1/expense/travel-requisitions?status=OPEN", "", nil); rec.Code != http.StatusBadRequest {
		t.Errorf("status OPEN = %d; want 400", rec.Code)
	}
}

const feedCSV = "card_last4,txn_date,post_date,merchant,mcc,amount,currency\n" +
	"4242,2025-03-01,2025-03-02,Hotel,7011,120.50,USD\n" +
	"5555,2025-03-01,2025-03-02,Taxi,4121,30.00,USD\n" +
	"4242,2025-03-01,2025-03-02,Bad,5812,abc,USD\n"

func TestImportCardFeedAssignsCardholdersAndQuarantines(t *testing.T) {
	f := newFakeTravel()
	h := travelRouter(f)
	worker := ids.New()
	body := map[string]any{"legalEntityId": ids.New(), "format": "csv", "content": feedCSV, "cardholders": map[string]string{"4242": worker}}
	rec := send(t, h, http.MethodPost, "/v1/expense/card-transactions/import", "imp-1", body, duties(dutyImport))
	if rec.Code != http.StatusCreated {
		t.Fatalf("import = %d %s", rec.Code, rec.Body.String())
	}
	if len(f.lastImport.Rows) != 1 || f.lastImport.Rows[0].WorkerID != worker || f.lastImport.Rows[0].Line != 2 {
		t.Fatalf("rows = %+v; want only line 2 for card 4242's worker", f.lastImport.Rows)
	}
	if f.lastImport.Format != "CSV" || len(f.lastImport.FileHash) != 64 {
		t.Errorf("format %q hash %q; want CSV and a SHA-256", f.lastImport.Format, f.lastImport.FileHash)
	}
	var got struct {
		Imported    int `json:"imported"`
		Quarantined []struct {
			Line   int    `json:"line"`
			Reason string `json:"reason"`
		} `json:"quarantined"`
	}
	decode(t, rec.Body.Bytes(), &got)
	if got.Imported != 1 || len(got.Quarantined) != 2 || got.Quarantined[0].Line != 3 || got.Quarantined[1].Line != 4 ||
		!strings.Contains(got.Quarantined[0].Reason, "5555") {
		t.Fatalf("body = %+v; want 1 imported and lines 3 (unknown card 5555) and 4 (bad amount) quarantined in file order", got)
	}
	again := send(t, h, http.MethodPost, "/v1/expense/card-transactions/import", "imp-1", body, duties(dutyImport))
	if again.Code != http.StatusOK || again.Header().Get("Idempotent-Replayed") != "true" {
		t.Fatalf("replay = %d %v", again.Code, again.Header())
	}
}

func TestImportCardFeedValidates(t *testing.T) {
	h := travelRouter(newFakeTravel())
	worker := ids.New()
	feed := func(format, content string, cardholders map[string]string) map[string]any {
		return map[string]any{"legalEntityId": ids.New(), "format": format, "content": content, "cardholders": cardholders}
	}
	cases := []struct {
		name, field string
		body        map[string]any
	}{
		{"no cardholders", "cardholders", feed("CSV", feedCSV, map[string]string{})},
		{"cardholder key not 4 digits", "cardholders", feed("CSV", feedCSV, map[string]string{"42": worker})},
		{"cardholder not a uuid", "cardholders.4242", feed("CSV", feedCSV, map[string]string{"4242": "w1"})},
		{"unsupported format", "format", feed("VCF", feedCSV, map[string]string{"4242": worker})},
		{"csv without columns", "content", feed("CSV", "a,b\n1,2\n", map[string]string{"4242": worker})},
		{"empty content", "content", feed("CSV", "", map[string]string{"4242": worker})},
		{"bad legal entity", "legalEntityId", map[string]any{"legalEntityId": "LE", "format": "CSV", "content": feedCSV, "cardholders": map[string]string{"4242": worker}}},
	}
	for _, c := range cases {
		rec := send(t, h, http.MethodPost, "/v1/expense/card-transactions/import", "k-"+c.name, c.body, duties(dutyImport))
		if p := problemOf(t, rec); rec.Code != http.StatusBadRequest || p.Errors[c.field] == "" {
			t.Errorf("%s: %d %v; want 400 naming %s", c.name, rec.Code, p.Errors, c.field)
		}
	}
}

func TestListCardTransactionsFormatsAndFilters(t *testing.T) {
	f := newFakeTravel()
	h := travelRouter(f)
	worker := ids.New()
	rec := send(t, h, http.MethodGet, "/v1/expense/card-transactions?status=UNASSIGNED&workerId="+worker, "", nil)
	if rec.Code != http.StatusOK || f.lastCardF.Status != "UNASSIGNED" || f.lastCardF.WorkerID != worker {
		t.Fatalf("list = %d filter %+v", rec.Code, f.lastCardF)
	}
	var page struct {
		Items []map[string]any `json:"items"`
	}
	decode(t, rec.Body.Bytes(), &page)
	if len(page.Items) != 1 || page.Items[0]["amount"] != "120.50" || page.Items[0]["txnDate"] != "2025-03-01" {
		t.Errorf("items = %v; want the amount at the currency's minor unit", page.Items)
	}
	if rec := send(t, h, http.MethodGet, "/v1/expense/card-transactions?status=LOST", "", nil); rec.Code != http.StatusBadRequest {
		t.Errorf("status LOST = %d; want 400", rec.Code)
	}
}
