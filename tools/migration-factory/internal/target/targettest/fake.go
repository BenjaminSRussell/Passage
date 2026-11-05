// Package targettest is an httptest fake of the receivables service's opening-item routes. It mirrors the real
// handler (services/receivables/internal/api/opening.go): strict JSON decoding of the request, a 1 to 1000 row cap,
// the importSource rule, per-row validation and results in request order, rows keyed by (importSource, legacyKey)
// with CREATED / UPDATED / UNCHANGED, the AR-OPENING-* codes, and GET /open-item-imports totals.
package targettest

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/platform/tenant"
)

var (
	sourceName = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]*$`)
	uuidRe     = regexp.MustCompile(`^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$`)
)

type row struct {
	LegacyKey          string          `json:"legacyKey"`
	DocumentNumber     string          `json:"documentNumber"`
	CustomerID         string          `json:"customerId"`
	Kind               string          `json:"kind"`
	Side               string          `json:"side"`
	DocumentDate       string          `json:"documentDate"`
	DueDate            string          `json:"dueDate"`
	Currency           string          `json:"currency"`
	OriginalAmount     decimal.Decimal `json:"originalAmount"`
	OpenAmount         decimal.Decimal `json:"openAmount"`
	CashDiscountDate   *string         `json:"cashDiscountDate"`
	CashDiscountAmount decimal.Decimal `json:"cashDiscountAmount"`
}

type request struct {
	LegalEntityID string `json:"legalEntityId"`
	ImportSource  string `json:"importSource"`
	Atomic        bool   `json:"atomic"`
	Items         []row  `json:"items"`
}

type stored struct {
	id     string
	le     string
	source string
	row    row
}

// Call records one request the fake received.
type Call struct {
	Method, Path   string
	IdempotencyKey string
	Duties         []string
	Issuer, Act    string
	Rows           int
	Status         int
}

// Fake is the fake receivables service.
type Fake struct {
	*httptest.Server

	mu    sync.Mutex
	items map[string]*stored // importSource|legacyKey
	// calls lists every request in arrival order.
	calls []Call
	// Failures queued per path: the next requests answer these statuses before behaving normally.
	failNext []int
	// RetryAfter is sent with a 429 or 503 fail.
	RetryAfter string
	// Settled lists legacy keys (importSource|legacyKey) that were settled in Passage: an update is AR-OPENING-SETTLED.
	Settled map[string]bool
	// Drift is added to every debitOpen the totals report (a defect, to prove the reconciliation catches it).
	Drift decimal.Decimal
	// DenyImports makes GET /open-item-imports answer 403.
	DenyImports bool
	// ExtraGroups adds that many groups of other import sources to GET /open-item-imports.
	ExtraGroups int
	// LoseAnswers makes the next batches store their rows and then answer 504, as a reply lost after the commit.
	LoseAnswers int
	// GarbleAnswers makes the next batches store their rows and then answer 200 with a body that is not a result.
	GarbleAnswers int
	seq           int
}

// New starts the fake; it closes with the test.
func New(t testing.TB) *Fake {
	f := &Fake{items: map[string]*stored{}, Settled: map[string]bool{}}
	mux := http.NewServeMux()
	mux.HandleFunc("POST /v1/receivables/open-items:batch", f.batch)
	mux.HandleFunc("GET /v1/receivables/open-item-imports", f.imports)
	f.Server = httptest.NewServer(mux)
	t.Cleanup(f.Close)
	return f
}

// FailNext queues statuses (500, 503, 429 ...) the next requests answer, in order.
func (f *Fake) FailNext(statuses ...int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.failNext = append(f.failNext, statuses...)
}

// Calls returns the requests received so far.
func (f *Fake) Calls() []Call {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]Call(nil), f.calls...)
}

// Batches returns the batch calls that were processed (not the injected failures).
func (f *Fake) Batches() []Call {
	var out []Call
	for _, c := range f.Calls() {
		if c.Method == http.MethodPost && c.Status/100 == 2 {
			out = append(out, c)
		}
	}
	return out
}

// Held returns how many items the fake holds for an import source.
func (f *Fake) Held(source string) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	n := 0
	for _, s := range f.items {
		if s.source == source {
			n++
		}
	}
	return n
}

// Item returns the stored row of (source, legacyKey).
func (f *Fake) Item(source, key string) (id string, ok bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	s, ok := f.items[source+"|"+key]
	if !ok {
		return "", false
	}
	return s.id, true
}

func claims(r *http.Request) (tenant.ServiceClaims, bool) {
	var c tenant.ServiceClaims
	body, _, ok := strings.Cut(r.Header.Get(tenant.HeaderServiceToken), ".")
	if !ok {
		return c, false
	}
	raw, err := base64.RawURLEncoding.DecodeString(body)
	if err != nil || json.Unmarshal(raw, &c) != nil {
		return c, false
	}
	return c, true
}

func problem(w http.ResponseWriter, status int, code, detail string) {
	w.Header().Set("Content-Type", "application/problem+json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]any{"status": status, "code": code, "detail": detail, "title": http.StatusText(status)})
}

// admit records the call, applies queued failures and checks the service token. It returns false when it answered.
func (f *Fake) admit(w http.ResponseWriter, r *http.Request, duty string, rows int) (*Call, bool) {
	c, ok := claims(r)
	call := Call{Method: r.Method, Path: r.URL.Path, IdempotencyKey: r.Header.Get("Idempotency-Key"), Rows: rows}
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
		if f.RetryAfter != "" {
			w.Header().Set("Retry-After", f.RetryAfter)
		}
		problem(w, status, "COM-0500", "injected failure")
		return nil, false
	}
	if !ok {
		record(http.StatusUnauthorized)
		problem(w, http.StatusUnauthorized, "AUTH-0001", "no service token")
		return nil, false
	}
	held := false
	for _, d := range c.Duties {
		held = held || d == duty
	}
	if !held || c.Aud != "receivables" {
		record(http.StatusForbidden)
		problem(w, http.StatusForbidden, "AUTH-0002", "duty "+duty+" required")
		return nil, false
	}
	return &call, true
}

func (f *Fake) finish(call *Call, status int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	call.Status = status
	f.calls = append(f.calls, *call)
}

func (f *Fake) batch(w http.ResponseWriter, r *http.Request) {
	call, ok := f.admit(w, r, "receivables.import.write", 0)
	if !ok {
		return
	}
	var req request
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4<<20))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&req); err != nil {
		f.finish(call, http.StatusBadRequest)
		problem(w, http.StatusBadRequest, "COM-0001", "malformed JSON body: "+err.Error())
		return
	}
	call.Rows = len(req.Items)
	if !uuidRe.MatchString(req.LegalEntityID) || !sourceName.MatchString(req.ImportSource) || len(req.ImportSource) > 40 || len(req.Items) == 0 || len(req.Items) > 1000 {
		f.finish(call, http.StatusBadRequest)
		problem(w, http.StatusBadRequest, "COM-0001", "request failed validation")
		return
	}
	if c, _ := claims(r); !strings.EqualFold(c.Le, req.LegalEntityID) {
		f.finish(call, http.StatusForbidden)
		problem(w, http.StatusForbidden, "AUTH-0003", "legal entity is outside the token's scope")
		return
	}
	type result struct {
		Index      int     `json:"index"`
		LegacyKey  string  `json:"legacyKey"`
		Outcome    string  `json:"outcome"`
		OpenItemID *string `json:"openItemId,omitempty"`
		Code       string  `json:"code,omitempty"`
		Detail     string  `json:"detail,omitempty"`
	}
	f.mu.Lock()
	results := make([]result, len(req.Items))
	created, updated, unchanged, rejected := 0, 0, 0, 0
	seen := map[string]int{}
	pending := map[string]*stored{}
	for i, it := range req.Items {
		res := result{Index: i, LegacyKey: it.LegacyKey}
		reject := func(code, detail string) { res.Outcome, res.Code, res.Detail = "REJECTED", code, detail; rejected++ }
		key := req.ImportSource + "|" + it.LegacyKey
		switch bad := validate(it); {
		case bad != "":
			reject("AR-OPENING-INVALID", bad)
		case func() bool { _, dup := seen[it.LegacyKey]; return dup }():
			reject("AR-OPENING-DUPLICATE-KEY", fmt.Sprintf("legacy key %s already appears at items[%d] of this batch", it.LegacyKey, seen[it.LegacyKey]))
		default:
			seen[it.LegacyKey] = i
			cur := f.items[key]
			switch {
			case cur == nil:
				f.seq++
				s := &stored{id: fmt.Sprintf("00000000-0000-4000-8000-%012d", f.seq), le: req.LegalEntityID, source: req.ImportSource, row: it}
				pending[key] = s
				res.Outcome, res.OpenItemID = "CREATED", &s.id
				created++
			case cur.le != req.LegalEntityID || cur.row.CustomerID != it.CustomerID || cur.row.Currency != it.Currency || cur.row.Kind != it.Kind || cur.row.Side != it.Side:
				reject("AR-OPENING-IDENTITY", "a known legacy key cannot move to another legal entity, customer, currency, kind or side")
			case same(cur.row, it):
				res.Outcome, res.OpenItemID = "UNCHANGED", &cur.id
				unchanged++
			case f.Settled[key]:
				reject("AR-OPENING-SETTLED", "the item was settled in Passage; the snapshot no longer describes it")
			default:
				pending[key] = &stored{id: cur.id, le: cur.le, source: cur.source, row: it}
				res.Outcome, res.OpenItemID = "UPDATED", &cur.id
				updated++
			}
		}
		results[i] = res
	}
	committed := !(req.Atomic && rejected > 0)
	if committed {
		for k, v := range pending {
			f.items[k] = v
		}
	}
	lose, garble := f.LoseAnswers > 0, f.LoseAnswers == 0 && f.GarbleAnswers > 0
	if lose {
		f.LoseAnswers--
	} else if garble {
		f.GarbleAnswers--
	}
	f.mu.Unlock()
	if lose {
		f.finish(call, http.StatusGatewayTimeout)
		problem(w, http.StatusGatewayTimeout, "COM-0504", "the answer was lost after the rows were stored")
		return
	}
	if garble {
		f.finish(call, http.StatusOK)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"results":[]}`))
		return
	}
	status := http.StatusOK
	if !committed {
		status = http.StatusUnprocessableEntity
	} else if created > 0 && updated == 0 && unchanged == 0 && rejected == 0 {
		status = http.StatusCreated
	}
	f.finish(call, status)
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]any{"batchId": "b", "legalEntityId": req.LegalEntityID, "importSource": req.ImportSource, "atomic": req.Atomic,
		"committed": committed, "created": created, "updated": updated, "unchanged": unchanged, "rejected": rejected, "results": results})
}

func same(a, b row) bool {
	da, db := "", ""
	if a.CashDiscountDate != nil {
		da = *a.CashDiscountDate
	}
	if b.CashDiscountDate != nil {
		db = *b.CashDiscountDate
	}
	return a.DocumentNumber == b.DocumentNumber && a.DocumentDate == b.DocumentDate && a.DueDate == b.DueDate &&
		a.OriginalAmount.Equal(b.OriginalAmount) && a.OpenAmount.Equal(b.OpenAmount) && da == db && a.CashDiscountAmount.Equal(b.CashDiscountAmount)
}

func validate(r row) string {
	var bad []string
	if strings.TrimSpace(r.LegacyKey) == "" || len(r.LegacyKey) > 120 {
		bad = append(bad, "legacyKey must be 1 to 120 characters")
	}
	if strings.TrimSpace(r.DocumentNumber) == "" || len(r.DocumentNumber) > 60 {
		bad = append(bad, "documentNumber must be 1 to 60 characters")
	}
	if !uuidRe.MatchString(r.CustomerID) {
		bad = append(bad, "customerId must be a UUID")
	}
	sides := map[string][]string{"INVOICE": {"DEBIT"}, "CREDIT_NOTE": {"CREDIT"}, "PAYMENT": {"CREDIT"}, "ADJUSTMENT": {"DEBIT", "CREDIT"}}
	allowed, ok := sides[r.Kind]
	switch {
	case !ok:
		bad = append(bad, "kind must be INVOICE, CREDIT_NOTE, PAYMENT or ADJUSTMENT")
	case !contains(allowed, r.Side):
		bad = append(bad, fmt.Sprintf("a %s is a %s item", r.Kind, strings.Join(allowed, " or ")))
	}
	for name, v := range map[string]string{"documentDate": r.DocumentDate, "dueDate": r.DueDate} {
		if _, err := time.Parse("2006-01-02", v); err != nil {
			bad = append(bad, name+" must be a YYYY-MM-DD date")
		}
	}
	if len(r.Currency) != 3 || strings.ToUpper(r.Currency) != r.Currency {
		bad = append(bad, "currency must be an ISO 4217 alpha-3 code")
	}
	if !r.OriginalAmount.IsPositive() || !r.OpenAmount.IsPositive() {
		bad = append(bad, "amounts must be positive decimals; an item with nothing open is not an open item")
	} else if r.OpenAmount.GreaterThan(r.OriginalAmount) {
		bad = append(bad, "openAmount must not exceed originalAmount")
	}
	sort.Strings(bad)
	return strings.Join(bad, "; ")
}

func contains(s []string, v string) bool {
	for _, x := range s {
		if x == v {
			return true
		}
	}
	return false
}

func (f *Fake) imports(w http.ResponseWriter, r *http.Request) {
	call, ok := f.admit(w, r, "receivables.view", 0)
	if !ok {
		return
	}
	if f.DenyImports {
		f.finish(call, http.StatusForbidden)
		problem(w, http.StatusForbidden, "AUTH-0002", "duty receivables.view required")
		return
	}
	type key struct{ source, le, cur string }
	type sum struct {
		count, dc, cc int
		debit, credit decimal.Decimal
	}
	// Receivables' row-level security shows the token's legal entity only.
	le := ""
	if c, ok := claims(r); ok {
		le = c.Le
	}
	f.mu.Lock()
	agg := map[key]*sum{}
	for _, s := range f.items {
		if !strings.EqualFold(s.le, le) {
			continue
		}
		k := key{s.source, s.le, s.row.Currency}
		a := agg[k]
		if a == nil {
			a = &sum{}
			agg[k] = a
		}
		a.count++
		if s.row.Side == "CREDIT" {
			a.cc++
			a.credit = a.credit.Sub(s.row.OpenAmount)
		} else {
			a.dc++
			a.debit = a.debit.Add(s.row.OpenAmount)
		}
	}
	drift := f.Drift
	f.mu.Unlock()
	keys := make([]key, 0, len(agg))
	for k := range agg {
		keys = append(keys, k)
	}
	sort.Slice(keys, func(i, j int) bool { return fmt.Sprint(keys[i]) < fmt.Sprint(keys[j]) })
	limit := 500
	if n, err := strconv.Atoi(r.URL.Query().Get("limit")); err == nil && n > 0 && n <= 500 {
		limit = n
	}
	if len(keys) > limit {
		keys = keys[:limit]
	}
	for i := 0; i < f.ExtraGroups && len(keys) < limit; i++ {
		keys = append(keys, key{fmt.Sprintf("other-%04d", i), le, "USD"})
		agg[keys[len(keys)-1]] = &sum{count: 1, dc: 1, debit: decimal.NewFromInt(1)}
	}
	items := []map[string]any{}
	for _, k := range keys {
		a := agg[k]
		debit := a.debit.Add(drift)
		items = append(items, map[string]any{"importSource": k.source, "legalEntityId": k.le, "currency": k.cur, "itemCount": a.count,
			"debitCount": a.dc, "creditCount": a.cc, "debitOpen": debit.String(), "creditOpen": a.credit.String(), "netOpen": debit.Add(a.credit).String()})
	}
	f.finish(call, http.StatusOK)
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{"items": items})
}

// HeaderSigner is a Signer for tests: it writes an unsigned token carrying the claims the fake reads.
type HeaderSigner struct{ Name string }

// ApplyDuties implements target.Signer.
func (h HeaderSigner) ApplyDuties(req *http.Request, p tenant.Principal, aud, legalEntity string, only []string) (bool, error) {
	c := tenant.ServiceClaims{Iss: h.Name, Aud: aud, Tid: p.TenantID, Le: legalEntity, Act: p.UserID, Duties: only}
	raw, _ := json.Marshal(c)
	req.Header.Set(tenant.HeaderServiceToken, base64.RawURLEncoding.EncodeToString(raw)+".x")
	return true, nil
}
