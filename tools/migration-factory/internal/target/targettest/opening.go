package targettest

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sort"
	"strconv"
	"strings"
	"sync"
	"testing"

	"github.com/shopspring/decimal"
)

// FakeOpening is an httptest fake of the inventory opening-stock route (services/inventory/internal/api/opening.go) or of
// the treasury opening-balance route (services/treasury/internal/api/opening.go): a batch key makes a resend a replay,
// the same key with other content is 422, rows named in Refuse are refused with the row-keyed errors of the real
// handler, and GET lists the stored batches in key order.
type FakeOpening struct {
	*httptest.Server
	kind string // "inventory" or "treasury"

	mu      sync.Mutex
	batches map[string]*openingBatch
	calls   []Call
	// Refuse names item references the fake refuses as invalid rows.
	Refuse map[string]bool
	// Drift is added to the quantity (inventory) or the first balance (treasury) of every listed batch, to prove the
	// reconciliation catches a target that holds something else than was sent.
	Drift    int64
	failNext []int
}

type openingBatch struct {
	LegalEntityID, Key, Hash, Currency string
	Count                              int
	Qty, Value                         decimal.Decimal
	Minor                              map[string]int64
}

// NewInventoryOpening starts the fake inventory service; it closes with the test.
func NewInventoryOpening(t testing.TB) *FakeOpening { return newOpening(t, "inventory") }

// NewTreasuryOpening starts the fake treasury service; it closes with the test.
func NewTreasuryOpening(t testing.TB) *FakeOpening { return newOpening(t, "treasury") }

func newOpening(t testing.TB, kind string) *FakeOpening {
	f := &FakeOpening{kind: kind, batches: map[string]*openingBatch{}, Refuse: map[string]bool{}}
	path := "/v1/inventory/opening-batches"
	if kind == "treasury" {
		path = "/v1/treasury/opening-balances"
	}
	mux := http.NewServeMux()
	mux.HandleFunc("POST "+path, f.post)
	mux.HandleFunc("GET "+path, f.list)
	f.Server = httptest.NewServer(mux)
	t.Cleanup(f.Close)
	return f
}

// FailNext queues statuses (500, 503, 429 ...) the next requests answer, in order.
func (f *FakeOpening) FailNext(statuses ...int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.failNext = append(f.failNext, statuses...)
}

// Calls returns the requests received so far.
func (f *FakeOpening) Calls() []Call {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]Call(nil), f.calls...)
}

// Batches returns the stored batch keys in key order.
func (f *FakeOpening) Batches() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	keys := make([]string, 0, len(f.batches))
	for k := range f.batches {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

// Posts returns the number of batch POSTs that were processed (not injected failures).
func (f *FakeOpening) Posts() int {
	n := 0
	for _, c := range f.Calls() {
		if c.Method == http.MethodPost && c.Status != 0 && c.Status < 500 {
			n++
		}
	}
	return n
}

func (f *FakeOpening) admit(w http.ResponseWriter, r *http.Request, duty string) (*Call, bool) {
	c, ok := claims(r)
	call := Call{Method: r.Method, Path: r.URL.Path, IdempotencyKey: r.Header.Get("Idempotency-Key")}
	if ok {
		call.Duties, call.Issuer, call.Act = c.Duties, c.Iss, c.Act
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	record := func(status int) { call.Status = status; f.calls = append(f.calls, call) }
	if len(f.failNext) > 0 {
		status := f.failNext[0]
		f.failNext = f.failNext[1:]
		record(status)
		problem(w, status, "COM-0500", "injected failure")
		return nil, false
	}
	held := false
	for _, d := range c.Duties {
		held = held || d == duty
	}
	switch {
	case !ok:
		record(http.StatusUnauthorized)
		problem(w, http.StatusUnauthorized, "AUTH-0001", "no service token")
		return nil, false
	case !held || c.Aud != f.kind:
		record(http.StatusForbidden)
		problem(w, http.StatusForbidden, "AUTH-0002", "duty "+duty+" required")
		return nil, false
	}
	return &call, true
}

func (f *FakeOpening) finish(call *Call, status int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	call.Status = status
	f.calls = append(f.calls, *call)
}

func (f *FakeOpening) refuse(w http.ResponseWriter, status int, code, detail string, fields map[string]string) {
	w.Header().Set("Content-Type", "application/problem+json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]any{"status": status, "code": code, "detail": detail, "title": http.StatusText(status), "errors": fields})
}

type openingReq struct {
	LegalEntityID string            `json:"legalEntityId"`
	BatchKey      string            `json:"batchKey"`
	Currency      string            `json:"currency"`
	Items         []json.RawMessage `json:"items"`
}

func (f *FakeOpening) post(w http.ResponseWriter, r *http.Request) {
	duty, rowCode := "inventory.import.write", "INV-OPB-001"
	if f.kind == "treasury" {
		duty, rowCode = "treasury.import.write", "TRS-0601"
	}
	call, ok := f.admit(w, r, duty)
	if !ok {
		return
	}
	var req openingReq
	var raw json.RawMessage
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4<<20)).Decode(&raw); err != nil || json.Unmarshal(raw, &req) != nil {
		f.finish(call, http.StatusBadRequest)
		problem(w, http.StatusBadRequest, "COM-0001", "malformed JSON body")
		return
	}
	if c, _ := claims(r); !strings.EqualFold(c.Le, req.LegalEntityID) {
		f.finish(call, http.StatusForbidden)
		problem(w, http.StatusForbidden, "AUTH-0003", "legal entity is outside the token's scope")
		return
	}
	if req.BatchKey == "" || len(req.Items) == 0 || len(req.Items) > 1000 {
		f.finish(call, http.StatusBadRequest)
		problem(w, http.StatusBadRequest, "COM-0001", "request failed validation")
		return
	}
	b := &openingBatch{LegalEntityID: strings.ToLower(req.LegalEntityID), Key: req.BatchKey, Currency: req.Currency, Count: len(req.Items), Minor: map[string]int64{}}
	bad := map[string]string{}
	seen := map[string]bool{}
	for i, rawItem := range req.Items {
		var it struct {
			Reference    string `json:"reference"`
			Qty          string `json:"qty"`
			CostPrice    string `json:"costPrice"`
			Currency     string `json:"currency"`
			BalanceMinor int64  `json:"balanceMinor"`
		}
		if json.Unmarshal(rawItem, &it) != nil || it.Reference == "" || seen[it.Reference] {
			bad["items["+strconv.Itoa(i+1)+"].reference"] = "is required and unique in the batch"
			continue
		}
		seen[it.Reference] = true
		if f.Refuse[it.Reference] {
			bad["items["+strconv.Itoa(i+1)+"].reference"] = "refused by the fake"
			continue
		}
		if f.kind == "inventory" {
			q, e1 := decimal.NewFromString(it.Qty)
			c, e2 := decimal.NewFromString(it.CostPrice)
			if e1 != nil || e2 != nil || !q.IsPositive() || c.IsNegative() {
				bad["items["+strconv.Itoa(i+1)+"].qty"] = "must be positive"
				continue
			}
			b.Qty, b.Value = b.Qty.Add(q), b.Value.Add(q.Mul(c))
		} else {
			b.Minor[it.Currency] += it.BalanceMinor
		}
	}
	if len(bad) > 0 {
		f.finish(call, http.StatusUnprocessableEntity)
		f.refuse(w, http.StatusUnprocessableEntity, rowCode, "the batch has rows that cannot be imported; nothing was stored", bad)
		return
	}
	b.Hash = string(raw)
	f.mu.Lock()
	if held, ok := f.batches[req.BatchKey]; ok {
		f.mu.Unlock()
		if held.Hash != b.Hash {
			f.finish(call, http.StatusUnprocessableEntity)
			f.refuse(w, http.StatusUnprocessableEntity, "FAKE-KEY", "the batch key was already used with other content", nil)
			return
		}
		f.finish(call, http.StatusOK)
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(f.batchJSON(held, 0))
		return
	}
	f.batches[req.BatchKey] = b
	f.mu.Unlock()
	f.finish(call, http.StatusCreated)
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusCreated)
	_ = json.NewEncoder(w).Encode(f.batchJSON(b, 0))
}

func (f *FakeOpening) batchJSON(b *openingBatch, drift int64) map[string]any {
	if f.kind == "inventory" {
		return map[string]any{"batchKey": b.Key, "legalEntityId": b.LegalEntityID, "currency": b.Currency, "itemCount": b.Count,
			"qty": b.Qty.Add(decimal.NewFromInt(drift)).StringFixed(6), "value": b.Value.StringFixed(12)}
	}
	minor := map[string]int64{}
	cur := make([]string, 0, len(b.Minor))
	for c, v := range b.Minor {
		minor[c] = v
		cur = append(cur, c)
	}
	sort.Strings(cur)
	if drift != 0 && len(cur) > 0 {
		minor[cur[0]] += drift
	}
	return map[string]any{"batchKey": b.Key, "legalEntityId": b.LegalEntityID, "itemCount": b.Count, "totalsMinor": minor}
}

func (f *FakeOpening) list(w http.ResponseWriter, r *http.Request) {
	duty := "inventory.import.read"
	if f.kind == "treasury" {
		duty = "treasury.import.read"
	}
	call, ok := f.admit(w, r, duty)
	if !ok {
		return
	}
	le := strings.ToLower(r.URL.Query().Get("legal_entity"))
	limit := 50
	if n, err := strconv.Atoi(r.URL.Query().Get("limit")); err == nil && n >= 1 && n <= 200 {
		limit = n
	}
	after := r.URL.Query().Get("cursor")
	f.mu.Lock()
	var keys []string
	for k, b := range f.batches {
		if b.LegalEntityID == le && k > after {
			keys = append(keys, k)
		}
	}
	sort.Strings(keys)
	drift := f.Drift
	body := map[string]any{}
	if len(keys) > limit {
		keys = keys[:limit]
		body["nextCursor"] = keys[limit-1]
	}
	items := make([]map[string]any, 0, len(keys))
	for _, k := range keys {
		items = append(items, f.batchJSON(f.batches[k], drift))
	}
	f.mu.Unlock()
	body["items"] = items
	f.finish(call, http.StatusOK)
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(body)
}
