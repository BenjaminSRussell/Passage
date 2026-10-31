package api

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/services/costing/internal/domain"
	"github.com/BenjaminSRussell/passage/services/costing/internal/store"
)

// slice4 fakes the valuation setup and aging stores in memory, with the same
// uniqueness rules as the tables.
type slice4 struct {
	ledgers    []store.ValuationLedger
	categories []store.ValuationCategory
	bucketSets []store.BucketSet
	snapshots  []store.AgingSnapshot
	snapKeys   map[string]string // idempotency key -> snapshot hash
	snapAfter  string
}

func conflict(code string) error { return httpx.NewProblem(http.StatusConflict, code, "conflict") }

func (f *slice4) CreateValuationLedger(_ context.Context, in store.ValuationLedger) (store.ValuationLedger, error) {
	for _, v := range f.ledgers {
		switch {
		case v.LegalEntityID != in.LegalEntityID:
		case v.Primary && in.Primary:
			return store.ValuationLedger{}, conflict(store.CodePrimaryLedgerTaken)
		case v.LedgerGroupID == in.LedgerGroupID:
			return store.ValuationLedger{}, conflict(store.CodeLedgerGroupTaken)
		case v.Code == in.Code:
			return store.ValuationLedger{}, conflict(store.CodeLedgerDuplicate)
		}
	}
	in.ID = ids.New()
	f.ledgers = append(f.ledgers, in)
	return in, nil
}

func (f *slice4) ListValuationLedgers(_ context.Context, legalEntityID string) ([]store.ValuationLedger, error) {
	out := []store.ValuationLedger{}
	for _, v := range f.ledgers {
		if legalEntityID == "" || v.LegalEntityID == legalEntityID {
			out = append(out, v)
		}
	}
	return out, nil
}

func (f *slice4) GetValuationLedger(_ context.Context, id string) (store.ValuationLedger, error) {
	for _, v := range f.ledgers {
		if v.ID == id {
			return v, nil
		}
	}
	return store.ValuationLedger{}, httpx.NewProblem(http.StatusNotFound, store.CodeLedgerNotFound, "not found")
}

func (f *slice4) CreateValuationCategory(_ context.Context, in store.ValuationCategory) (store.ValuationCategory, error) {
	for _, c := range f.categories {
		if c.Code == in.Code {
			return store.ValuationCategory{}, conflict(store.CodeCategoryDuplicate)
		}
	}
	in.ID = ids.New()
	f.categories = append(f.categories, in)
	return in, nil
}

func (f *slice4) ListValuationCategories(context.Context) ([]store.ValuationCategory, error) {
	return append([]store.ValuationCategory{}, f.categories...), nil
}

func (f *slice4) CreateBucketSet(_ context.Context, in store.BucketSet) (store.BucketSet, error) {
	for _, b := range f.bucketSets {
		if b.LegalEntityID == in.LegalEntityID && b.Name == in.Name {
			return store.BucketSet{}, conflict(store.CodeBucketSetDuplicate)
		}
	}
	in.ID = ids.New()
	f.bucketSets = append(f.bucketSets, in)
	return in, nil
}

func (f *slice4) ListBucketSets(_ context.Context, legalEntityID string) ([]store.BucketSet, error) {
	out := []store.BucketSet{}
	for _, b := range f.bucketSets {
		if legalEntityID == "" || b.LegalEntityID == legalEntityID {
			out = append(out, b)
		}
	}
	return out, nil
}

func (f *slice4) GetBucketSet(_ context.Context, id string) (store.BucketSet, error) {
	for _, b := range f.bucketSets {
		if b.ID == id {
			return b, nil
		}
	}
	return store.BucketSet{}, httpx.NewProblem(http.StatusNotFound, store.CodeBucketSetNotFound, "not found")
}

func (f *slice4) CreateAgingSnapshot(_ context.Context, in store.AgingSnapshot, key string) (store.AgingSnapshot, bool, error) {
	hash, err := store.SnapshotHash(in)
	if err != nil {
		return store.AgingSnapshot{}, false, err
	}
	if f.snapKeys == nil {
		f.snapKeys = map[string]string{}
	}
	if stored, ok := f.snapKeys[key]; ok {
		if stored != hash {
			return store.AgingSnapshot{}, false, httpx.NewProblem(http.StatusUnprocessableEntity, store.CodeIdempotencyReuse, "reuse")
		}
		for _, s := range f.snapshots {
			if h, _ := store.SnapshotHash(s); h == hash {
				return s, false, nil
			}
		}
	}
	f.snapKeys[key] = hash
	in.ID, in.LineCount = ids.New(), len(in.Lines)
	f.snapshots = append(f.snapshots, in)
	return in, true, nil
}

func (f *slice4) GetAgingSnapshot(_ context.Context, id string) (store.AgingSnapshot, error) {
	for _, s := range f.snapshots {
		if s.ID == id {
			return s, nil
		}
	}
	return store.AgingSnapshot{}, httpx.NewProblem(http.StatusNotFound, store.CodeSnapshotNotFound, "not found")
}

func (f *slice4) ListAgingSnapshots(_ context.Context, legalEntityID, after string, _ int) (store.SnapshotPage, error) {
	f.snapAfter = after
	page := store.SnapshotPage{Items: []store.AgingSnapshot{}}
	for _, s := range f.snapshots {
		if legalEntityID == "" || s.LegalEntityID == legalEntityID {
			s.Lines = nil
			page.Items = append(page.Items, s)
		}
	}
	return page, nil
}

const setupDuties = allDuties + "," + dutyValuationWrite + "," + dutyAgingWrite

func code(rec *httptest.ResponseRecorder) string {
	m, _ := decodeMap(rec)
	c, _ := m["code"].(string)
	return c
}

func fieldErrors(rec *httptest.ResponseRecorder) map[string]any {
	m, _ := decodeMap(rec)
	e, _ := m["errors"].(map[string]any)
	return e
}

func items(t *testing.T, rec *httptest.ResponseRecorder) []any {
	t.Helper()
	if rec.Code != http.StatusOK {
		t.Fatalf("list status %d: %s", rec.Code, rec.Body.String())
	}
	m, _ := decodeMap(rec)
	list, _ := m["items"].([]any)
	return list
}

func ledgerBody(le, group, code, method string, primary bool) string {
	p := "false"
	if primary {
		p = "true"
	}
	return `{"legalEntityId":"` + le + `","ledgerGroupId":"` + group + `","code":"` + code + `","method":"` + method +
		`","currency":"EUR","rateType":"AVERAGE","primary":` + p + `}`
}

// FR-20.15: an IFRS primary and a US GAAP group ledger; a second primary, a
// reused code and a ledger group carrying two valuation ledgers are refused.
func TestValuationLedgers_RegisterAndRefuse(t *testing.T) {
	h, _ := server()
	ifrs, gaap := ids.New(), ids.New()
	le := testLE
	post := func(body string) *httptest.ResponseRecorder {
		return call(h, http.MethodPost, "/v1/costing/valuation-ledgers", body, setupDuties, nil)
	}
	if rec := post(ledgerBody(le, ifrs, "IFRS", "WEIGHTED_AVG_PERIODIC", true)); rec.Code != http.StatusCreated {
		t.Fatalf("primary: %d %s", rec.Code, rec.Body.String())
	}
	if rec := post(ledgerBody(le, gaap, "USGAAP", "FIFO", false)); rec.Code != http.StatusCreated {
		t.Fatalf("group ledger: %d %s", rec.Code, rec.Body.String())
	}
	refusals := []struct {
		name, body, code string
	}{
		{"second primary", ledgerBody(le, ids.New(), "TAX", "LIFO_DATE", true), store.CodePrimaryLedgerTaken},
		{"ledger group already bound", ledgerBody(le, gaap, "TAX", "LIFO_DATE", false), store.CodeLedgerGroupTaken},
		{"code reused", ledgerBody(le, ids.New(), "IFRS", "FIFO", false), store.CodeLedgerDuplicate},
	}
	for _, tc := range refusals {
		if rec := post(tc.body); rec.Code != http.StatusConflict || code(rec) != tc.code {
			t.Errorf("%s: %d %q, want 409 %s", tc.name, rec.Code, code(rec), tc.code)
		}
	}
	// Another legal entity may reuse the code.
	otherLE := ids.New()
	if rec := call(h, http.MethodPost, "/v1/costing/valuation-ledgers", ledgerBody(otherLE, ids.New(), "IFRS", "FIFO", true), setupDuties, scopedTo(otherLE)); rec.Code != http.StatusCreated {
		t.Errorf("other legal entity: %d", rec.Code)
	}
	if got := items(t, call(h, http.MethodGet, "/v1/costing/valuation-ledgers?legalEntityId="+le, "", allDuties, nil)); len(got) != 2 {
		t.Errorf("legal entity ledgers = %d, want 2", len(got))
	}
	if rec := call(h, http.MethodGet, "/v1/costing/valuation-ledgers", "", allDuties, scopedTo("")); rec.Code != http.StatusForbidden {
		t.Errorf("unfiltered list without legal-entity scope: %d, want 403", rec.Code)
	}
	allScope := map[string]string{"X-Passage-LegalEntity-Scope": "ALL"}
	if got := items(t, call(h, http.MethodGet, "/v1/costing/valuation-ledgers", "", allDuties+",org.switcher.all_scope", allScope)); len(got) != 3 {
		t.Errorf("all ledgers = %d, want 3", len(got))
	}
}

func TestValuationLedgers_ValidationAndDuty(t *testing.T) {
	h, _ := server()
	bad := `{"legalEntityId":"x","ledgerGroupId":"","code":"ifrs","method":"NEWEST","currency":"eur","rateType":"CLOSING"}`
	rec := call(h, http.MethodPost, "/v1/costing/valuation-ledgers", bad, setupDuties, nil)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status %d, want 400", rec.Code)
	}
	errs := fieldErrors(rec)
	for _, f := range []string{"legalEntityId", "ledgerGroupId", "code", "method", "currency", "rateType"} {
		if _, ok := errs[f]; !ok {
			t.Errorf("no field error for %s: %v", f, errs)
		}
	}
	good := ledgerBody(ids.New(), ids.New(), "IFRS", "FIFO", true)
	if rec := call(h, http.MethodPost, "/v1/costing/valuation-ledgers", good, allDuties, nil); rec.Code != http.StatusForbidden {
		t.Errorf("without %s: %d, want 403", dutyValuationWrite, rec.Code)
	}
	if rec := call(h, http.MethodGet, "/v1/costing/valuation-ledgers?legalEntityId=nope", "", allDuties, nil); rec.Code != http.StatusBadRequest {
		t.Errorf("bad filter: %d, want 400", rec.Code)
	}
}

func TestValuationLedgers_LegalEntityScope(t *testing.T) {
	h, _ := server()
	mine, other := ids.New(), ids.New()
	scoped := map[string]string{"X-Passage-Legal-Entity": mine}
	if rec := call(h, http.MethodPost, "/v1/costing/valuation-ledgers", ledgerBody(other, ids.New(), "IFRS", "FIFO", true), setupDuties, scoped); rec.Code != http.StatusForbidden {
		t.Errorf("create in another legal entity: %d, want 403", rec.Code)
	}
	if rec := call(h, http.MethodGet, "/v1/costing/valuation-ledgers?legalEntityId="+other, "", allDuties, scoped); rec.Code != http.StatusForbidden {
		t.Errorf("list another legal entity: %d, want 403", rec.Code)
	}
}

// FR-20.16, BR-20.14: ORIGIN splits into DOMESTIC and IMPORT with an account
// each; BATCH has no fixed types.
func TestValuationCategories(t *testing.T) {
	h, _ := server()
	post := func(body string) *httptest.ResponseRecorder {
		return call(h, http.MethodPost, "/v1/costing/valuation-categories", body, setupDuties, nil)
	}
	origin := `{"code":"ORIGIN","types":[{"code":"DOMESTIC","account":"1300"},{"code":"IMPORT","account":"1301"}]}`
	rec := post(origin)
	if rec.Code != http.StatusCreated {
		t.Fatalf("ORIGIN: %d %s", rec.Code, rec.Body.String())
	}
	if m, _ := decodeMap(rec); len(m["types"].([]any)) != 2 {
		t.Errorf("ORIGIN = %v", m)
	}
	if rec := post(`{"code":"BATCH","batch":true}`); rec.Code != http.StatusCreated {
		t.Errorf("BATCH: %d %s", rec.Code, rec.Body.String())
	}
	cases := []struct {
		name, body string
		status     int
		code       string
	}{
		{"batch with fixed types", `{"code":"LOT","batch":true,"types":[{"code":"L1","account":"1"}]}`, http.StatusUnprocessableEntity, domain.CodeCategoryTypes},
		{"split without types", `{"code":"COND","types":[]}`, http.StatusUnprocessableEntity, domain.CodeCategoryTypes},
		{"type listed twice", `{"code":"COND","types":[{"code":"NEW","account":"1"},{"code":"NEW","account":"2"}]}`, http.StatusUnprocessableEntity, domain.CodeCategoryTypes},
		{"type without account", `{"code":"COND","types":[{"code":"NEW","account":" "}]}`, http.StatusBadRequest, "COM-0001"},
		{"lowercase code", `{"code":"cond","types":[{"code":"NEW","account":"1"}]}`, http.StatusBadRequest, "COM-0001"},
		{"code reused", origin, http.StatusConflict, store.CodeCategoryDuplicate},
	}
	for _, tc := range cases {
		if rec := post(tc.body); rec.Code != tc.status || code(rec) != tc.code {
			t.Errorf("%s: %d %q, want %d %s", tc.name, rec.Code, code(rec), tc.status, tc.code)
		}
	}
	if got := items(t, call(h, http.MethodGet, "/v1/costing/valuation-categories", "", allDuties, nil)); len(got) != 2 {
		t.Errorf("categories = %d, want 2", len(got))
	}
}

func TestBucketSets(t *testing.T) {
	h, _ := server()
	le := testLE
	post := func(body string) *httptest.ResponseRecorder {
		return call(h, http.MethodPost, "/v1/costing/aging/bucket-sets", body, setupDuties, nil)
	}
	monthly := `{"legalEntityId":"` + le + `","name":"Monthly","bounds":[30,60,90]}`
	if rec := post(monthly); rec.Code != http.StatusCreated {
		t.Fatalf("create: %d %s", rec.Code, rec.Body.String())
	}
	if rec := post(monthly); rec.Code != http.StatusConflict || code(rec) != store.CodeBucketSetDuplicate {
		t.Errorf("same name: %d %q", rec.Code, code(rec))
	}
	for name, bounds := range map[string]string{"descending": "[90,30]", "repeated": "[30,30]", "empty": "[]", "negative": "[-1]"} {
		rec := post(`{"legalEntityId":"` + le + `","name":"` + name + `","bounds":` + bounds + `}`)
		if _, ok := fieldErrors(rec)["bounds"]; rec.Code != http.StatusBadRequest || !ok {
			t.Errorf("%s bounds: %d %s", name, rec.Code, rec.Body.String())
		}
	}
	if rec := post(`{"legalEntityId":"` + le + `","name":"  ","bounds":[30]}`); rec.Code != http.StatusBadRequest {
		t.Errorf("blank name: %d", rec.Code)
	}
	if rec := call(h, http.MethodPost, "/v1/costing/aging/bucket-sets", monthly, allDuties, nil); rec.Code != http.StatusForbidden {
		t.Errorf("without %s: %d", dutyAgingWrite, rec.Code)
	}
	if got := items(t, call(h, http.MethodGet, "/v1/costing/aging/bucket-sets?legalEntityId="+le, "", allDuties, nil)); len(got) != 1 {
		t.Errorf("bucket sets = %d, want 1", len(got))
	}
}

// snapshotBody ages the AC-20.13 layers as of 2026-01-31: 10 units received
// 400 days before at 5.00, 50 units 100 days before and 100 units 20 days
// before, under FIFO.
func snapshotBody(le, product, site, extra string) string {
	return `{"legalEntityId":"` + le + `","asOf":"2026-01-31"` + extra + `,"lines":[{"productId":"` + product + `","siteId":"` + site + `","transactions":[
	  {"id":"OLD","kind":"RECEIPT","qty":"10","value":"50.00","financialDate":"2024-12-27"},
	  {"id":"MID","kind":"RECEIPT","qty":"50","value":"250.00","financialDate":"2025-10-23"},
	  {"id":"NEW","kind":"RECEIPT","qty":"100","value":"500.00","financialDate":"2026-01-11"}]}]}`
}

func bucketValues(t *testing.T, m map[string]any) map[string]string {
	t.Helper()
	out := map[string]string{}
	for _, b := range m["buckets"].([]any) {
		bm := b.(map[string]any)
		out[bm["label"].(string)] = bm["value"].(string)
	}
	return out
}

func TestAgingSnapshot_StoresReplaysAndRefusesKeyReuse(t *testing.T) {
	h, fs := server()
	p, s := ids.New(), ids.New()
	le := testLE
	key := map[string]string{idempotencyKey: "snap-2026-01"}
	body := snapshotBody(le, p, s, `,"method":"FIFO","currency":"EUR"`)
	rec := call(h, http.MethodPost, "/v1/costing/aging/snapshots", body, setupDuties, key)
	if rec.Code != http.StatusCreated {
		t.Fatalf("create: %d %s", rec.Code, rec.Body.String())
	}
	m, _ := decodeMap(rec)
	want := map[string]string{"0-30": "500.00", "31-90": "0.00", "91-180": "250.00", "181-365": "0.00", "> 365": "50.00"}
	if got := bucketValues(t, m); len(got) != len(want) {
		t.Errorf("buckets = %v", got)
	} else {
		for k, v := range want {
			if got[k] != v {
				t.Errorf("bucket %s = %s, want %s", k, got[k], v)
			}
		}
	}
	if m["totalValue"] != "800.00" || m["remainingValue"] != "800.00" || m["tiesToRemaining"] != true || m["asOf"] != "2026-01-31" {
		t.Errorf("snapshot = %v", m)
	}
	id := m["id"].(string)

	replay := call(h, http.MethodPost, "/v1/costing/aging/snapshots", body, setupDuties, key)
	if rm, _ := decodeMap(replay); replay.Code != http.StatusOK || rm["id"] != id || len(fs.snapshots) != 1 {
		t.Errorf("replay: %d id %v, stored %d", replay.Code, rm["id"], len(fs.snapshots))
	}
	other := snapshotBody(le, p, s, `,"method":"LIFO_DATE","currency":"EUR"`)
	if rec := call(h, http.MethodPost, "/v1/costing/aging/snapshots", other, setupDuties, key); rec.Code != http.StatusUnprocessableEntity || code(rec) != store.CodeIdempotencyReuse {
		t.Errorf("key reuse: %d %q", rec.Code, code(rec))
	}

	got := call(h, http.MethodGet, "/v1/costing/aging/snapshots/"+id, "", allDuties, nil)
	if gm, _ := decodeMap(got); got.Code != http.StatusOK || len(gm["lines"].([]any)) != 1 {
		t.Errorf("get: %d %v", got.Code, gm)
	}
	list := items(t, call(h, http.MethodGet, "/v1/costing/aging/snapshots?legalEntityId="+le, "", allDuties, nil))
	if len(list) != 1 || list[0].(map[string]any)["lines"] != nil || list[0].(map[string]any)["lineCount"] != float64(1) {
		t.Errorf("list = %v, want one summary without lines", list)
	}
	if rec := call(h, http.MethodGet, "/v1/costing/aging/snapshots/"+ids.New(), "", allDuties, nil); rec.Code != http.StatusNotFound || code(rec) != store.CodeSnapshotNotFound {
		t.Errorf("unknown snapshot: %d %q", rec.Code, code(rec))
	}
}

// With a valuation ledger the snapshot takes the ledger's method and currency;
// a saved bucket set of the same legal entity sets the buckets.
func TestAgingSnapshot_LedgerAndBucketSet(t *testing.T) {
	h, fs := server()
	p, s := ids.New(), ids.New()
	le := testLE
	fs.ledgers = []store.ValuationLedger{{ID: ids.New(), LegalEntityID: le, Code: "USGAAP", Method: "LIFO_DATE", Currency: "USD", Primary: true}}
	fs.bucketSets = []store.BucketSet{{ID: ids.New(), LegalEntityID: le, Name: "Quarter", Bounds: []int{90}}, {ID: ids.New(), LegalEntityID: ids.New(), Name: "Other", Bounds: []int{30}}}
	ledger := fs.ledgers[0].ID
	extra := `,"valuationLedgerId":"` + ledger + `","bucketSetId":"` + fs.bucketSets[0].ID + `"`
	rec := call(h, http.MethodPost, "/v1/costing/aging/snapshots", snapshotBody(le, p, s, extra), setupDuties, map[string]string{idempotencyKey: "k1"})
	if rec.Code != http.StatusCreated {
		t.Fatalf("create: %d %s", rec.Code, rec.Body.String())
	}
	m, _ := decodeMap(rec)
	if m["method"] != "LIFO_DATE" || m["currency"] != "USD" || m["valuationLedgerId"] != ledger {
		t.Errorf("snapshot = %v", m)
	}
	if got := bucketValues(t, m); got["0-90"] != "500.00" || got["> 90"] != "300.00" {
		t.Errorf("buckets = %v, want 0-90 500.00 and > 90 300.00", got)
	}

	refusals := map[string]struct{ extra, field string }{
		"method differs from ledger":       {`,"valuationLedgerId":"` + ledger + `","method":"FIFO"`, "method"},
		"bucket set of another entity":     {`,"method":"FIFO","currency":"EUR","bucketSetId":"` + fs.bucketSets[1].ID + `"`, "bucketSetId"},
		"bucket set and bounds":            {`,"method":"FIFO","currency":"EUR","bucketSetId":"` + fs.bucketSets[0].ID + `","bucketBounds":[30]`, "bucketSetId"},
		"no method without a ledger":       {`,"currency":"EUR"`, "method"},
		"unknown fallback":                 {`,"method":"FIFO","currency":"EUR","fallback":"GUESS"`, "fallback"},
		"descending bounds":                {`,"method":"FIFO","currency":"EUR","bucketBounds":[90,30]`, "bucketBounds"},
		"ledger of another legal entity":   {`,"valuationLedgerId":"` + ledger + `"`, "valuationLedgerId"},
		"transactions dated after the day": {`,"method":"FIFO","currency":"EUR","asOf":"2026-01-10"`, "lines[0].transactions[2].financialDate"},
	}
	for name, tc := range refusals {
		body := snapshotBody(le, p, s, tc.extra)
		headers := map[string]string{idempotencyKey: name}
		if name == "ledger of another legal entity" {
			otherLE := ids.New()
			body = snapshotBody(otherLE, p, s, tc.extra)
			headers["X-Passage-Legal-Entity"] = otherLE
		}
		if name == "transactions dated after the day" {
			body = strings.Replace(body, `"asOf":"2026-01-31",`, "", 1)
		}
		rec := call(h, http.MethodPost, "/v1/costing/aging/snapshots", body, setupDuties, headers)
		if _, ok := fieldErrors(rec)[tc.field]; rec.Code != http.StatusBadRequest || !ok {
			t.Errorf("%s: %d %s", name, rec.Code, rec.Body.String())
		}
	}
	unknown := snapshotBody(le, p, s, `,"valuationLedgerId":"`+ids.New()+`"`)
	if rec := call(h, http.MethodPost, "/v1/costing/aging/snapshots", unknown, setupDuties, map[string]string{idempotencyKey: "k2"}); rec.Code != http.StatusNotFound || code(rec) != store.CodeLedgerNotFound {
		t.Errorf("unknown ledger: %d %q", rec.Code, code(rec))
	}
}

func TestAgingSnapshot_RequestRefusals(t *testing.T) {
	h, _ := server()
	p, s := ids.New(), ids.New()
	le := testLE
	body := snapshotBody(le, p, s, `,"method":"FIFO","currency":"EUR"`)
	if rec := call(h, http.MethodPost, "/v1/costing/aging/snapshots", body, setupDuties, nil); rec.Code != http.StatusBadRequest {
		t.Errorf("no Idempotency-Key: %d", rec.Code)
	}
	if rec := call(h, http.MethodPost, "/v1/costing/aging/snapshots", body, allDuties, map[string]string{idempotencyKey: "k"}); rec.Code != http.StatusForbidden {
		t.Errorf("without %s: %d", dutyAgingWrite, rec.Code)
	}
	twice := `{"legalEntityId":"` + le + `","asOf":"2026-01-31","method":"FIFO","currency":"EUR","lines":[
	  {"productId":"` + p + `","siteId":"` + s + `","transactions":[{"id":"R","kind":"RECEIPT","qty":"1","value":"1","financialDate":"2026-01-01"}]},
	  {"productId":"` + strings.ToUpper(p) + `","siteId":"` + s + `","transactions":[{"id":"R","kind":"RECEIPT","qty":"1","value":"1","financialDate":"2026-01-01"}]}]}`
	if rec := call(h, http.MethodPost, "/v1/costing/aging/snapshots", twice, setupDuties, map[string]string{idempotencyKey: "k"}); rec.Code != http.StatusUnprocessableEntity || code(rec) != domain.CodeInvalidTxn {
		t.Errorf("same product and site twice: %d %q", rec.Code, code(rec))
	}
	empty := `{"legalEntityId":"` + le + `","asOf":"2026-01-31","method":"FIFO","currency":"EUR","lines":[{"productId":"` + p + `","siteId":"` + s + `","transactions":[]}]}`
	if rec := call(h, http.MethodPost, "/v1/costing/aging/snapshots", empty, setupDuties, map[string]string{idempotencyKey: "k"}); rec.Code != http.StatusBadRequest {
		t.Errorf("line without transactions: %d", rec.Code)
	}
	if rec := call(h, http.MethodGet, "/v1/costing/aging/snapshots?cursor=bm9wZQ", "", allDuties, nil); rec.Code != http.StatusBadRequest {
		t.Errorf("bad cursor: %d", rec.Code)
	}
}
