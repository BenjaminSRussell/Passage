package targettest

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sort"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

// FakePayables is an httptest fake of payables' opening-batch routes. It mirrors the real handler
// (services/payables/internal/api/opening.go): strict JSON decoding, 1 to 1000 items, a batch key, a control total per
// currency, row errors keyed items[row].field (422 AP-OPB-001), a mismatching control total (422 AP-OPB-003), the same
// key with the same content replayed as 200 and with other content a 409 AP-OPB-002, a reference held by another
// batch (422 AP-OPB-004), vendors checked against a known set, and GET with limit and cursor paging.
type FakePayables struct {
	*httptest.Server

	mu      sync.Mutex
	batches []*storedBatch
	byKey   map[string]*storedBatch
	refs    map[string]string // reference -> batch key
	calls   []Call
	// Vendors are the known vendor ids; a row naming another is refused. Nil accepts any vendor.
	Vendors map[string]bool
	// FailNext queues statuses the next requests answer before behaving normally.
	failNext []int
	// Drift is added to the first currency total of every listed batch (a defect, to prove the reconciliation catches it).
	Drift int64
	seq   int
}

type storedBatch struct {
	ID, LegalEntityID, BatchKey string
	Count                       int
	Totals                      map[string]int64
	Hash                        string
	At                          time.Time
}

type openingItem struct {
	Reference     string `json:"reference"`
	VendorID      string `json:"vendorId"`
	InvoiceNumber string `json:"invoiceNumber"`
	InvoiceDate   string `json:"invoiceDate"`
	DueDate       string `json:"dueDate"`
	Currency      string `json:"currency"`
	AmountMinor   int64  `json:"amountMinor"`
}

type openingRequest struct {
	LegalEntityID string           `json:"legalEntityId"`
	BatchKey      string           `json:"batchKey"`
	ControlTotals map[string]int64 `json:"controlTotalsMinor"`
	Items         []openingItem    `json:"items"`
}

// NewPayables starts the fake; it closes with the test.
func NewPayables(t testing.TB) *FakePayables {
	f := &FakePayables{byKey: map[string]*storedBatch{}, refs: map[string]string{}}
	mux := http.NewServeMux()
	mux.HandleFunc("POST /v1/payables/opening-batches", f.post)
	mux.HandleFunc("GET /v1/payables/opening-batches", f.list)
	f.Server = httptest.NewServer(mux)
	t.Cleanup(f.Close)
	return f
}

// FailNext queues statuses (500, 503, 429 ...) the next requests answer, in order.
func (f *FakePayables) FailNext(statuses ...int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.failNext = append(f.failNext, statuses...)
}

// Calls returns the requests received so far.
func (f *FakePayables) Calls() []Call {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]Call(nil), f.calls...)
}

// Posts returns the number of batch POSTs that were processed (not injected failures).
func (f *FakePayables) Posts() int {
	n := 0
	for _, c := range f.Calls() {
		if c.Method == http.MethodPost && c.Status != 0 && c.Status < 500 {
			n++
		}
	}
	return n
}

// Held returns how many items the fake holds.
func (f *FakePayables) Held() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.refs)
}

// Batches returns the stored batch keys, oldest first.
func (f *FakePayables) Batches() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]string, len(f.batches))
	for i, b := range f.batches {
		out[i] = b.BatchKey
	}
	return out
}

func (f *FakePayables) admit(w http.ResponseWriter, r *http.Request, duty string) (*Call, bool) {
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
	if !ok {
		record(http.StatusUnauthorized)
		problem(w, http.StatusUnauthorized, "AUTH-0001", "no service token")
		return nil, false
	}
	held := false
	for _, d := range c.Duties {
		held = held || d == duty
	}
	if !held || c.Aud != "payables" {
		record(http.StatusForbidden)
		problem(w, http.StatusForbidden, "AUTH-0002", "duty "+duty+" required")
		return nil, false
	}
	return &call, true
}

func (f *FakePayables) finish(call *Call, status int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	call.Status = status
	f.calls = append(f.calls, *call)
}

func refuse(w http.ResponseWriter, status int, code, detail string, fields map[string]string) {
	w.Header().Set("Content-Type", "application/problem+json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]any{"status": status, "code": code, "detail": detail, "title": http.StatusText(status), "errors": fields})
}

func itemsHash(req openingRequest) string {
	items := append([]openingItem(nil), req.Items...)
	sort.Slice(items, func(i, j int) bool { return items[i].Reference < items[j].Reference })
	raw, _ := json.Marshal(struct {
		LE     string
		Totals map[string]int64
		Items  []openingItem
	}{req.LegalEntityID, req.ControlTotals, items})
	return string(raw)
}

func (f *FakePayables) post(w http.ResponseWriter, r *http.Request) {
	call, ok := f.admit(w, r, "opening-item.import")
	if !ok {
		return
	}
	var req openingRequest
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4<<20))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&req); err != nil {
		f.finish(call, http.StatusBadRequest)
		problem(w, http.StatusBadRequest, "COM-0001", "malformed JSON body: "+err.Error())
		return
	}
	if c, _ := claims(r); !strings.EqualFold(c.Le, req.LegalEntityID) {
		f.finish(call, http.StatusForbidden)
		problem(w, http.StatusForbidden, "AUTH-0003", "legal entity is outside the token's scope")
		return
	}
	if req.BatchKey == "" || len(req.BatchKey) > 100 || len(req.Items) == 0 || len(req.Items) > 1000 || len(req.ControlTotals) == 0 {
		f.finish(call, http.StatusBadRequest)
		problem(w, http.StatusBadRequest, "COM-0001", "request failed validation")
		return
	}
	bad := map[string]string{}
	seen := map[string]int{}
	actual := map[string]int64{}
	for i, it := range req.Items {
		row := func(field string) string { return "items[" + strconv.Itoa(i+1) + "]." + field }
		if it.Reference == "" || len(it.Reference) > 100 {
			bad[row("reference")] = "is required, at most 100 characters"
		} else if first, dup := seen[it.Reference]; dup {
			bad[row("reference")] = fmt.Sprintf("appears again; first at row %d", first)
		} else {
			seen[it.Reference] = i + 1
		}
		if !uuidRe.MatchString(it.VendorID) {
			bad[row("vendorId")] = "must be a UUID"
		} else if f.Vendors != nil && !f.Vendors[strings.ToLower(it.VendorID)] {
			bad[row("vendorId")] = "vendor " + it.VendorID + " is not in the party vendor master"
		}
		if it.InvoiceNumber == "" {
			bad[row("invoiceNumber")] = "is required, at most 100 characters"
		}
		id, e1 := time.Parse("2006-01-02", it.InvoiceDate)
		dd, e2 := time.Parse("2006-01-02", it.DueDate)
		if e1 != nil {
			bad[row("invoiceDate")] = "must be a YYYY-MM-DD date"
		}
		if e2 != nil {
			bad[row("dueDate")] = "must be a YYYY-MM-DD date"
		}
		if e1 == nil && e2 == nil && dd.Before(id) {
			bad[row("dueDate")] = "must not be before the invoice date"
		}
		if len(it.Currency) != 3 || strings.ToUpper(it.Currency) != it.Currency {
			bad[row("currency")] = "must be a 3-letter ISO 4217 code in capitals"
		}
		if it.AmountMinor <= 0 {
			bad[row("amountMinor")] = "must be a positive whole number of minor units"
		}
		actual[it.Currency] += it.AmountMinor
	}
	if len(bad) > 0 {
		f.finish(call, http.StatusUnprocessableEntity)
		refuse(w, http.StatusUnprocessableEntity, "AP-OPB-001", "the batch has rows that cannot be imported; nothing was stored", bad)
		return
	}
	mismatch := map[string]string{}
	for cur, want := range req.ControlTotals {
		if actual[cur] != want {
			mismatch["controlTotalsMinor."+cur] = fmt.Sprintf("declared %d but the items add up to %d", want, actual[cur])
		}
	}
	for cur, got := range actual {
		if _, ok := req.ControlTotals[cur]; !ok {
			mismatch["controlTotalsMinor."+cur] = fmt.Sprintf("no total declared; the items add up to %d", got)
		}
	}
	if len(mismatch) > 0 {
		f.finish(call, http.StatusUnprocessableEntity)
		refuse(w, http.StatusUnprocessableEntity, "AP-OPB-003", "the items do not add up to the declared control totals; nothing was stored", mismatch)
		return
	}
	hash := itemsHash(req)
	f.mu.Lock()
	if cur, ok := f.byKey[req.BatchKey]; ok {
		f.mu.Unlock()
		if cur.Hash != hash {
			f.finish(call, http.StatusConflict)
			refuse(w, http.StatusConflict, "AP-OPB-002", "batch key "+req.BatchKey+" was already imported with different content", nil)
			return
		}
		f.finish(call, http.StatusOK)
		w.Header().Set("Idempotent-Replayed", "true")
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(batchJSON(cur, 0))
		return
	}
	taken := map[string]string{}
	for i, it := range req.Items {
		if key, ok := f.refs[it.Reference]; ok {
			taken["items["+strconv.Itoa(i+1)+"].reference"] = "already imported by batch " + key
		}
	}
	if len(taken) > 0 {
		f.mu.Unlock()
		f.finish(call, http.StatusUnprocessableEntity)
		refuse(w, http.StatusUnprocessableEntity, "AP-OPB-004", "item references were already imported by another batch; nothing was stored", taken)
		return
	}
	f.seq++
	b := &storedBatch{ID: fmt.Sprintf("00000000-0000-4000-9000-%012d", f.seq), LegalEntityID: strings.ToLower(req.LegalEntityID), BatchKey: req.BatchKey,
		Count: len(req.Items), Totals: actual, Hash: hash, At: time.Unix(1_700_000_000+int64(f.seq), 0).UTC()}
	f.batches = append(f.batches, b)
	f.byKey[b.BatchKey] = b
	for _, it := range req.Items {
		f.refs[it.Reference] = b.BatchKey
	}
	f.mu.Unlock()
	f.finish(call, http.StatusCreated)
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusCreated)
	_ = json.NewEncoder(w).Encode(batchJSON(b, 0))
}

func batchJSON(b *storedBatch, drift int64) map[string]any {
	totals := map[string]int64{}
	cur := make([]string, 0, len(b.Totals))
	for c, v := range b.Totals {
		totals[c] = v
		cur = append(cur, c)
	}
	sort.Strings(cur)
	if drift != 0 && len(cur) > 0 {
		totals[cur[0]] += drift
	}
	return map[string]any{"id": b.ID, "legalEntityId": b.LegalEntityID, "batchKey": b.BatchKey, "itemCount": b.Count, "totalsMinor": totals,
		"importedBy": "migration-loader", "importedAt": b.At.Format(time.RFC3339)}
}

func (f *FakePayables) list(w http.ResponseWriter, r *http.Request) {
	call, ok := f.admit(w, r, "opening-item.import.read")
	if !ok {
		return
	}
	le := strings.ToLower(r.URL.Query().Get("legal_entity"))
	limit := 50
	if n, err := strconv.Atoi(r.URL.Query().Get("limit")); err == nil && n >= 1 && n <= 200 {
		limit = n
	}
	after := 0
	if c := r.URL.Query().Get("cursor"); c != "" {
		after, _ = strconv.Atoi(c)
	}
	f.mu.Lock()
	var matching []*storedBatch
	for i := len(f.batches) - 1; i >= 0; i-- { // newest first
		if f.batches[i].LegalEntityID == le {
			matching = append(matching, f.batches[i])
		}
	}
	drift := f.Drift
	f.mu.Unlock()
	if after > len(matching) {
		after = len(matching)
	}
	page := matching[after:]
	next := ""
	if len(page) > limit {
		page, next = page[:limit], strconv.Itoa(after+limit)
	}
	items := make([]map[string]any, 0, len(page))
	for _, b := range page {
		items = append(items, batchJSON(b, drift))
	}
	f.finish(call, http.StatusOK)
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{"items": items, "nextCursor": next, "asOf": time.Now().UTC().Format(time.RFC3339)})
}
