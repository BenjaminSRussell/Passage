package api_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/platform/testkit"
	"github.com/BenjaminSRussell/passage/services/inventory/internal/api"
	"github.com/BenjaminSRussell/passage/services/inventory/internal/domain"
	"github.com/BenjaminSRussell/passage/services/inventory/internal/store"
)

// fakeStore records the calls the HTTP layer makes; it never touches Postgres.
type fakeStore struct {
	postVersion int
	onHand      store.OnHandFilter
	journals    store.JournalFilter
	keys        map[string]domain.Journal
	lines       []domain.JournalLine
	err         error
}

func (f *fakeStore) CreateJournal(_ context.Context, j domain.Journal, key string) (domain.Journal, bool, error) {
	if f.keys == nil {
		f.keys = map[string]domain.Journal{}
	}
	if prior, ok := f.keys[key]; ok {
		return prior, false, nil
	}
	j.ID, j.Status, j.Version = ids.New(), domain.JournalDraft, 1
	f.keys[key] = j
	return j, true, nil
}

func (f *fakeStore) AddJournalLines(_ context.Context, id string, lines []domain.JournalLine) (domain.Journal, error) {
	if f.err != nil {
		return domain.Journal{}, f.err
	}
	f.lines = lines
	return domain.Journal{ID: id, Status: domain.JournalDraft, Version: 2, Lines: lines}, nil
}

func (f *fakeStore) GetJournal(_ context.Context, id string) (domain.Journal, error) {
	posted := time.Date(2026, 9, 1, 12, 0, 0, 0, time.UTC)
	return domain.Journal{ID: id, Status: domain.JournalPosted, Version: 4, Currency: "USD", PostedAt: &posted,
		Lines: []domain.JournalLine{{ID: ids.New(), LineNo: 1, ProductID: ids.New(), Qty: decimal.NewFromInt(2), EnteredQty: decimal.NewFromInt(2)}},
		GLEntry: &domain.JournalEntry{Currency: "USD", Lines: []domain.GLLine{
			{Account: "1300", Debit: decimal.RequireFromString("8.5"), Credit: decimal.Zero},
			{Account: "500100", Debit: decimal.Zero, Credit: decimal.RequireFromString("8.5")},
		}}}, nil
}

func (f *fakeStore) ListJournals(_ context.Context, flt store.JournalFilter) (store.JournalPage, error) {
	f.journals = flt
	return store.JournalPage{Items: []domain.Journal{{ID: ids.New(), Type: domain.JournalAdjustment, Status: domain.JournalDraft, LineCount: 3}},
		NextCursor: "next-page"}, nil
}

func (f *fakeStore) PostJournal(_ context.Context, id string, version int, _ store.PostParams) (domain.Journal, error) {
	if f.err != nil {
		return domain.Journal{}, f.err
	}
	f.postVersion = version
	return domain.Journal{ID: id, Status: domain.JournalPosted, Version: version + 1}, nil
}

func (f *fakeStore) ListOnHand(_ context.Context, flt store.OnHandFilter) (store.OnHandResult, error) {
	f.onHand = flt
	// The group total is positive but an underlying row is negative: the API
	// must pass the store's flag through rather than recompute it from the total.
	return store.OnHandResult{Truncated: true, Rows: []store.OnHandRow{{Group: map[string]string{"site": "S1"}, PhysicalInvent: decimal.NewFromInt(3),
		AvailPhysical: decimal.NewFromInt(3), AvailOrdered: decimal.NewFromInt(3), PostedValue: decimal.Zero, Currency: "USD", Negative: true}}}, nil
}

func (f *fakeStore) Ping(context.Context) error { return nil }

func newServer(t *testing.T, fs *fakeStore) http.Handler {
	t.Helper()
	r := chi.NewRouter()
	api.New(fs, store.PostParams{InventoryAccount: "1300", AdjustmentAccount: "5100", OpeningAccount: "3900"}).Register(r)
	return r
}

func do(t *testing.T, h http.Handler, method, path, body string, duties []string, headers map[string]string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	req = req.WithContext(testkit.Ctx(testkit.TenantID(1), duties...))
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

type problemBody struct {
	Code   string            `json:"code"`
	Errors map[string]string `json:"errors"`
}

func decodeProblem(t *testing.T, rec *httptest.ResponseRecorder) problemBody {
	t.Helper()
	var p problemBody
	if err := json.Unmarshal(rec.Body.Bytes(), &p); err != nil {
		t.Fatalf("decode problem: %v (%s)", err, rec.Body.String())
	}
	return p
}

func TestPostRouteMatchesColonMethodAndRequiresIfMatch(t *testing.T) {
	fs := &fakeStore{}
	h := newServer(t, fs)
	path := "/v1/inventory/journals/" + ids.New() + ":post"

	rec := do(t, h, http.MethodPost, path, "", []string{"inventory.journal.post"}, nil)
	if rec.Code != http.StatusPreconditionRequired || decodeProblem(t, rec).Code != "COM-0428" {
		t.Fatalf("no If-Match: status %d body %s, want 428 COM-0428", rec.Code, rec.Body.String())
	}
	rec = do(t, h, http.MethodPost, path, "", []string{"inventory.journal.post"}, map[string]string{"If-Match": `"x"`})
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("malformed If-Match: status %d, want 400", rec.Code)
	}
	rec = do(t, h, http.MethodPost, path, "", []string{"inventory.journal.post"}, map[string]string{"If-Match": `"3"`})
	if rec.Code != http.StatusOK {
		t.Fatalf("with If-Match: status %d body %s", rec.Code, rec.Body.String())
	}
	if fs.postVersion != 3 || rec.Header().Get("ETag") != `"4"` {
		t.Fatalf("store got version %d, ETag %q; want 3 and \"4\"", fs.postVersion, rec.Header().Get("ETag"))
	}
}

func TestPostPassesBusinessRuleProblemThrough(t *testing.T) {
	fs := &fakeStore{err: httpx.NewProblem(http.StatusUnprocessableEntity, domain.CodeNegativePhysical, "shortfall 1")}
	h := newServer(t, fs)
	rec := do(t, h, http.MethodPost, "/v1/inventory/journals/"+ids.New()+":post", "", []string{"inventory.journal.post"}, map[string]string{"If-Match": "1"})
	if rec.Code != http.StatusUnprocessableEntity || decodeProblem(t, rec).Code != domain.CodeNegativePhysical {
		t.Fatalf("status %d body %s, want 422 %s", rec.Code, rec.Body.String(), domain.CodeNegativePhysical)
	}
}

func TestDutiesAreEnforcedPerRoute(t *testing.T) {
	h := newServer(t, &fakeStore{})
	id := ids.New()
	cases := []struct {
		name, method, path, duty string
	}{
		{"post needs post duty", http.MethodPost, "/v1/inventory/journals/" + id + ":post", "inventory.journal.write"},
		{"create needs write duty", http.MethodPost, "/v1/inventory/journals", "inventory.read"},
		{"lines need write duty", http.MethodPost, "/v1/inventory/journals/" + id + "/lines:bulk", "inventory.read"},
		{"list needs read duty", http.MethodGet, "/v1/inventory/journals", "inventory.journal.write"},
		{"on-hand needs read duty", http.MethodGet, "/v1/inventory/onhand", "inventory.journal.post"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			rec := do(t, h, c.method, c.path, "{}", []string{c.duty}, map[string]string{"If-Match": "1"})
			if rec.Code != http.StatusForbidden {
				t.Fatalf("status %d, want 403", rec.Code)
			}
		})
	}
}

func TestInvalidJournalIDIsNotFound(t *testing.T) {
	h := newServer(t, &fakeStore{})
	rec := do(t, h, http.MethodPost, "/v1/inventory/journals/not-a-uuid:post", "", []string{"inventory.journal.post"},
		map[string]string{"If-Match": "1"})
	if rec.Code != http.StatusNotFound || decodeProblem(t, rec).Code != store.CodeJournalNotFound {
		t.Fatalf("status %d body %s, want 404", rec.Code, rec.Body.String())
	}
}

func TestCreateJournalValidation(t *testing.T) {
	h := newServer(t, &fakeStore{})
	tests := []struct {
		name   string
		body   string
		key    string
		fields []string
	}{
		{"every field malformed", `{"journalType":"SALE","legalEntityId":"nope","postingDate":"2026-13-01","currency":"usd"}`, "",
			[]string{"legalEntityId", "journalType", "postingDate", "currency", "Idempotency-Key"}},
		{"movement needs an offset account", `{"journalType":"MOVEMENT","legalEntityId":"` + ids.New() + `","postingDate":"2026-09-01","currency":"USD"}`, "k",
			[]string{"offsetAccountId"}},
		{"adjustment takes no offset account", `{"journalType":"ADJUSTMENT","legalEntityId":"` + ids.New() + `","postingDate":"2026-09-01","currency":"USD","offsetAccountId":"500100"}`, "k",
			[]string{"offsetAccountId"}},
		{"offset account format", `{"journalType":"MOVEMENT","legalEntityId":"` + ids.New() + `","postingDate":"2026-09-01","currency":"USD","offsetAccountId":"50 01"}`, "k",
			[]string{"offsetAccountId"}},
		{"over-long idempotency key", `{"journalType":"ADJUSTMENT","legalEntityId":"` + ids.New() + `","postingDate":"2026-09-01","currency":"USD"}`, strings.Repeat("k", 256),
			[]string{"Idempotency-Key"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			headers := map[string]string{}
			if tt.key != "" {
				headers["Idempotency-Key"] = tt.key
			}
			rec := do(t, h, http.MethodPost, "/v1/inventory/journals", tt.body, []string{"inventory.journal.write"}, headers)
			if rec.Code != http.StatusBadRequest {
				t.Fatalf("status %d, want 400 (%s)", rec.Code, rec.Body.String())
			}
			p := decodeProblem(t, rec)
			for _, f := range tt.fields {
				if _, ok := p.Errors[f]; !ok {
					t.Errorf("missing field error for %s: %v", f, p.Errors)
				}
			}
		})
	}
}

func TestCreateJournalReplaysIdempotencyKey(t *testing.T) {
	h := newServer(t, &fakeStore{})
	body := `{"journalType":"MOVEMENT","legalEntityId":"` + ids.New() + `","postingDate":"2026-09-01","currency":"USD","offsetAccountId":"500100"}`
	headers := map[string]string{"Idempotency-Key": "k-1"}
	first := do(t, h, http.MethodPost, "/v1/inventory/journals", body, []string{"inventory.journal.write"}, headers)
	if first.Code != http.StatusCreated || first.Header().Get("ETag") != `"1"` {
		t.Fatalf("create: status %d ETag %q body %s", first.Code, first.Header().Get("ETag"), first.Body.String())
	}
	again := do(t, h, http.MethodPost, "/v1/inventory/journals", body, []string{"inventory.journal.write"}, headers)
	if again.Code != http.StatusOK || again.Header().Get("Idempotent-Replayed") != "true" {
		t.Fatalf("replay: status %d replayed %q", again.Code, again.Header().Get("Idempotent-Replayed"))
	}
	var a, b struct {
		ID string `json:"id"`
	}
	_ = json.Unmarshal(first.Body.Bytes(), &a)
	_ = json.Unmarshal(again.Body.Bytes(), &b)
	if a.ID == "" || a.ID != b.ID {
		t.Fatalf("replay returned journal %q, want %q", b.ID, a.ID)
	}
}

func TestAddLinesParsesAndValidates(t *testing.T) {
	fs := &fakeStore{}
	h := newServer(t, fs)
	path := "/v1/inventory/journals/" + ids.New() + "/lines:bulk"
	product := ids.New()
	body := `{"lines":[{"clientKey":"l-1","productId":"` + product + `","dimensions":{"site":"S1"},"qty":"-2.5","enteredUnit":"EA","costPrice":"4"}]}`
	rec := do(t, h, http.MethodPost, path, body, []string{"inventory.journal.write"}, nil)
	if rec.Code != http.StatusCreated {
		t.Fatalf("status %d body %s", rec.Code, rec.Body.String())
	}
	if len(fs.lines) != 1 || fs.lines[0].ClientKey != "l-1" || !fs.lines[0].EnteredQty.Equal(decimal.RequireFromString("-2.5")) ||
		fs.lines[0].Dims[domain.DimSite] != "S1" {
		t.Fatalf("store got %+v; entered qty must default to qty", fs.lines)
	}

	bad := `{"lines":[{"productId":"x","qty":"abc","enteredUnit":"each box","costPrice":"-1"}]}`
	rec = do(t, h, http.MethodPost, path, bad, []string{"inventory.journal.write"}, nil)
	p := decodeProblem(t, rec)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status %d, want 400", rec.Code)
	}
	for _, f := range []string{"lines[0].productId", "lines[0].qty", "lines[0].enteredUnit", "lines[0].costPrice"} {
		if _, ok := p.Errors[f]; !ok {
			t.Errorf("missing field error %s: %v", f, p.Errors)
		}
	}

	rec = do(t, h, http.MethodPost, path, `{"lines":[]}`, []string{"inventory.journal.write"}, nil)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("empty lines: status %d, want 400", rec.Code)
	}

	fs.err = httpx.NewProblem(http.StatusUnprocessableEntity, domain.CodeDimensionMandatory, "site is mandatory")
	rec = do(t, h, http.MethodPost, path, body, []string{"inventory.journal.write"}, nil)
	if rec.Code != http.StatusUnprocessableEntity || decodeProblem(t, rec).Code != domain.CodeDimensionMandatory {
		t.Fatalf("store rule: status %d body %s", rec.Code, rec.Body.String())
	}
}

func TestGetJournalReturnsLinesAndGLEntry(t *testing.T) {
	h := newServer(t, &fakeStore{})
	rec := do(t, h, http.MethodGet, "/v1/inventory/journals/"+ids.New(), "", []string{"inventory.read"}, nil)
	if rec.Code != http.StatusOK || rec.Header().Get("ETag") != `"4"` {
		t.Fatalf("status %d ETag %q", rec.Code, rec.Header().Get("ETag"))
	}
	var out struct {
		LineCount int `json:"lineCount"`
		PostedAt  string
		GLEntry   struct {
			Lines []struct {
				Account, Debit, Credit string
			} `json:"lines"`
			LedgerEntryID *string `json:"ledgerEntryId"`
		} `json:"glEntry"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	if out.LineCount != 1 || len(out.GLEntry.Lines) != 2 || out.GLEntry.Lines[0].Debit != "8.5" || out.GLEntry.LedgerEntryID != nil {
		t.Fatalf("unexpected body %s", rec.Body.String())
	}
}

func TestListJournalsFiltersAndPages(t *testing.T) {
	fs := &fakeStore{}
	h := newServer(t, fs)
	rec := do(t, h, http.MethodGet, "/v1/inventory/journals?status=DRAFT&type=ADJUSTMENT&limit=10&cursor=abc", "", []string{"inventory.read"}, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d body %s", rec.Code, rec.Body.String())
	}
	if fs.journals.Status != domain.JournalDraft || fs.journals.Type != domain.JournalAdjustment || fs.journals.Limit != 10 || fs.journals.Cursor != "abc" {
		t.Fatalf("store got filter %+v", fs.journals)
	}
	var out struct {
		Items []struct {
			LineCount int             `json:"lineCount"`
			Lines     json.RawMessage `json:"lines"`
		} `json:"items"`
		NextCursor string `json:"nextCursor"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	if len(out.Items) != 1 || out.Items[0].LineCount != 3 || out.Items[0].Lines != nil || out.NextCursor != "next-page" {
		t.Fatalf("unexpected body %s", rec.Body.String())
	}
	for _, q := range []string{"status=OPEN", "type=TRANSFER", "limit=0", "limit=500"} {
		if rec := do(t, h, http.MethodGet, "/v1/inventory/journals?"+q, "", []string{"inventory.read"}, nil); rec.Code != http.StatusBadRequest {
			t.Errorf("%s: status %d, want 400", q, rec.Code)
		}
	}
}

func TestOnHandRejectsUnknownGrouping(t *testing.T) {
	h := newServer(t, &fakeStore{})
	for _, q := range []string{"groupBy=colour", "groupBy=segment", "product=not-a-uuid"} {
		if rec := do(t, h, http.MethodGet, "/v1/inventory/onhand?"+q, "", []string{"inventory.read"}, nil); rec.Code != http.StatusBadRequest {
			t.Errorf("%s: status %d, want 400", q, rec.Code)
		}
	}
}

func TestOnHandGroupsByAnyDimensionSubset(t *testing.T) {
	fs := &fakeStore{}
	h := newServer(t, fs)
	rec := do(t, h, http.MethodGet, "/v1/inventory/onhand?groupBy=site,batch,product,legal_entity&site=S1&warehouse=W1", "", []string{"inventory.read"}, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d body %s", rec.Code, rec.Body.String())
	}
	if strings.Join(fs.onHand.GroupBy, ",") != "site,batch,product,legal_entity" || fs.onHand.SiteID != "S1" || fs.onHand.WarehouseID != "W1" {
		t.Fatalf("filter %+v", fs.onHand)
	}
	var out struct {
		Items []struct {
			Negative bool   `json:"negative"`
			Currency string `json:"currency"`
		} `json:"items"`
		Truncated bool `json:"truncated"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	if len(out.Items) != 1 || !out.Items[0].Negative || out.Items[0].Currency != "USD" || !out.Truncated {
		t.Fatalf("negative flag, currency or truncation not passed through: %s", rec.Body.String())
	}
}

var specOperation = regexp.MustCompile(`^    (get|put|post|patch|delete):$`)

// declaredOperations maps "path method" to whether the operation names a duty.
func declaredOperations(spec string) map[string]bool {
	declared := map[string]bool{}
	inPaths, path, operation := false, "", ""
	for _, line := range strings.Split(spec, "\n") {
		switch {
		case line == "paths:":
			inPaths = true
		case inPaths && line != "" && !strings.HasPrefix(line, " "):
			inPaths = false
		case !inPaths:
		case strings.HasPrefix(line, "  /") && strings.HasSuffix(line, ":"):
			path, operation = strings.TrimSuffix(strings.TrimSpace(line), ":"), ""
		case specOperation.MatchString(line):
			operation = path + " " + specOperation.FindStringSubmatch(line)[1]
			declared[operation] = false
		case operation != "" && strings.HasPrefix(line, "      x-passage-duty: "):
			declared[operation] = true
		}
	}
	return declared
}

// The OpenAPI document lists exactly the served operations, each with a duty.
func TestOpenAPIDocumentsEveryOperation(t *testing.T) {
	spec, err := os.ReadFile("../../api/openapi.yaml")
	if err != nil {
		t.Fatal(err)
	}
	served := map[string]bool{}
	_ = chi.Walk(newServer(t, &fakeStore{}).(chi.Routes), func(method, route string, _ http.Handler, _ ...func(http.Handler) http.Handler) error {
		served[route+" "+strings.ToLower(method)] = true
		return nil
	})
	if len(served) != 6 {
		t.Fatalf("router walk yielded %d operations, want 6: %v", len(served), served)
	}
	declared := declaredOperations(string(spec))
	for op := range served {
		if hasDuty, ok := declared[op]; !ok {
			t.Errorf("not documented: %s", op)
		} else if !hasDuty {
			t.Errorf("no x-passage-duty: %s", op)
		}
	}
	for op := range declared {
		if !served[op] {
			t.Errorf("documented but not served: %s", op)
		}
	}
}
