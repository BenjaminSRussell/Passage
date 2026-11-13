package api_test

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sort"
	"strings"
	"sync"
	"testing"

	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/platform/testkit"
	"github.com/BenjaminSRussell/passage/services/print/internal/api"
	"github.com/BenjaminSRussell/passage/services/print/internal/domain"
	"github.com/BenjaminSRussell/passage/services/print/internal/store"
)

// memDocs is an in-memory store.DocStore for handler tests.
type memDocs struct {
	mu        sync.Mutex
	templates map[string]domain.DocumentTemplate
	blocks    map[string]domain.TextBlock
	rules     []domain.OutputRule
	history   []domain.TemplateVersion
}

func (m *memDocs) ListTemplateVersions(_ context.Context, code string) ([]domain.TemplateVersion, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	var out []domain.TemplateVersion
	for i := len(m.history) - 1; i >= 0; i-- {
		if m.history[i].Code == code {
			out = append(out, m.history[i])
		}
	}
	if len(out) == 0 {
		return nil, httpx.NewProblem(http.StatusNotFound, domain.CodeTemplateNotFound, "document template not found")
	}
	return out, nil
}

func newMemDocs() *memDocs {
	return &memDocs{templates: map[string]domain.DocumentTemplate{}, blocks: map[string]domain.TextBlock{}}
}

func (m *memDocs) PutTemplate(_ context.Context, t domain.DocumentTemplate, ifMatch int, by string) (domain.DocumentTemplate, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if ifMatch == 0 && m.templates[t.Code].Version != 0 {
		return t, httpx.NewProblem(http.StatusPreconditionRequired, domain.CodeTemplateIfMatch, "if-match required")
	}
	if ifMatch != 0 && ifMatch != m.templates[t.Code].Version {
		return t, httpx.NewProblem(http.StatusPreconditionFailed, domain.CodeTemplateStale, "stale")
	}
	t.Version = m.templates[t.Code].Version + 1
	m.history = append(m.history, domain.TemplateVersion{DocumentTemplate: t, SavedBy: by})
	if t.Default {
		for c, o := range m.templates {
			if o.DocumentType == t.DocumentType && c != t.Code {
				o.Default = false
				m.templates[c] = o
			}
		}
	}
	m.templates[t.Code] = t
	return t, nil
}

func (m *memDocs) GetTemplate(_ context.Context, code string) (domain.DocumentTemplate, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	t, ok := m.templates[code]
	if !ok {
		return t, httpx.NewProblem(http.StatusNotFound, domain.CodeTemplateNotFound, "document template not found")
	}
	return t, nil
}

func (m *memDocs) DefaultTemplate(_ context.Context, docType string) (*domain.DocumentTemplate, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, t := range m.templates {
		if t.DocumentType == docType && t.Default {
			return &t, nil
		}
	}
	return nil, nil
}

func (m *memDocs) ListTemplates(_ context.Context, docType string) ([]domain.DocumentTemplate, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := []domain.DocumentTemplate{}
	for _, t := range m.templates {
		if docType == "" || t.DocumentType == docType {
			out = append(out, t)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Code < out[j].Code })
	return out, nil
}

func (m *memDocs) PutTextBlock(_ context.Context, b domain.TextBlock) (domain.TextBlock, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	k := b.Code + "/" + b.Language
	b.Version = m.blocks[k].Version + 1
	b.Placeholders = domain.Placeholders(b.Body)
	m.blocks[k] = b
	return b, nil
}

func (m *memDocs) TextBlockVersions(_ context.Context, code string) ([]domain.TextBlock, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := []domain.TextBlock{}
	for _, b := range m.blocks {
		if b.Code == code {
			out = append(out, b)
		}
	}
	return out, nil
}

func (m *memDocs) ListTextBlocks(context.Context) ([]domain.TextBlock, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := []domain.TextBlock{}
	for _, b := range m.blocks {
		out = append(out, b)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Code+out[i].Language < out[j].Code+out[j].Language })
	return out, nil
}

func (m *memDocs) CreateOutputRule(_ context.Context, r domain.OutputRule, _ string) (domain.OutputRule, bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	r.ID = fmt.Sprintf("00000000-0000-0000-0002-%012d", len(m.rules)+1)
	m.rules = append(m.rules, r)
	return r, true, nil
}

func (m *memDocs) ListOutputRules(context.Context) ([]domain.OutputRule, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return append([]domain.OutputRule{}, m.rules...), nil
}

func (m *memDocs) DeleteOutputRule(_ context.Context, id string, tenantWide bool) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	for i, r := range m.rules {
		if r.ID == id {
			if r.LegalEntityID == "" && !tenantWide {
				return httpx.NewProblem(http.StatusForbidden, "AUTH-0003", "tenant administrators only")
			}
			m.rules = append(m.rules[:i], m.rules[i+1:]...)
			return nil
		}
	}
	return httpx.NewProblem(http.StatusNotFound, domain.CodeOutputRuleNotFound, "output rule not found")
}

var _ store.DocStore = (*memDocs)(nil)

func docRouter() http.Handler {
	return router(newMemStore(), &fakeSender{}, api.WithDocStore(newMemDocs()))
}

func TestTemplates_PutVersionDefaultAndDuties(t *testing.T) {
	h := docRouter()
	body := `{"name":"Standard invoice","documentType":"CUSTOMER_INVOICE","default":true,"config":{"title":"Tax invoice","hideColumns":["Unit"]}}`
	rec := call(t, h, http.MethodPut, "/print/v1/document-templates/INV-STD", body, "print.admin", "")
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"version":1`) {
		t.Fatalf("put: %d %s", rec.Code, rec.Body)
	}
	req := httptest.NewRequest(http.MethodPut, "/print/v1/document-templates/INV-STD", strings.NewReader(body))
	req.Header.Set("X-Passage-Tenant", testkit.TenantID(1))
	req.Header.Set("X-Passage-User", "worker-7")
	req.Header.Set("X-Passage-Duties", "print.admin")
	req.Header.Set("If-Match", `"1"`)
	second := httptest.NewRecorder()
	h.ServeHTTP(second, req)
	if !strings.Contains(second.Body.String(), `"version":2`) {
		t.Fatalf("second save: %s", second.Body)
	}
	if rec := call(t, h, http.MethodPut, "/print/v1/document-templates/INV-STD", body, "print.job.submit", ""); rec.Code != http.StatusForbidden {
		t.Fatalf("a worker saved a template: %d", rec.Code)
	}
	if rec := call(t, h, http.MethodPut, "/print/v1/document-templates/inv", `{"name":"","documentType":"X"}`, "print.admin", ""); rec.Code != http.StatusBadRequest {
		t.Fatalf("invalid template: %d", rec.Code)
	}
	if rec := call(t, h, http.MethodGet, "/print/v1/document-templates?documentType=CUSTOMER_INVOICE", "", "print.job.submit", ""); rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "INV-STD") {
		t.Fatalf("list: %d %s", rec.Code, rec.Body)
	}
	if rec := call(t, h, http.MethodGet, "/print/v1/document-templates/NOPE-1", "", "print.job.submit", ""); rec.Code != http.StatusNotFound {
		t.Fatalf("missing template: %d", rec.Code)
	}
}

// A save carries the version it was based on (If-Match); a save based on an older one is 412, and the history lists
// every version with who saved it.
func TestTemplates_IfMatchAndHistory(t *testing.T) {
	h := docRouter()
	body := `{"name":"Standard invoice","documentType":"CUSTOMER_INVOICE","config":{"title":"Tax invoice"}}`
	put := func(ifMatch string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodPut, "/print/v1/document-templates/INV-STD", strings.NewReader(body))
		req.Header.Set("X-Passage-Tenant", testkit.TenantID(1))
		req.Header.Set("X-Passage-User", "designer-1")
		req.Header.Set("X-Passage-Duties", "print.admin")
		if ifMatch != "" {
			req.Header.Set("If-Match", ifMatch)
		}
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		return rec
	}
	if rec := put(""); rec.Code != http.StatusOK || rec.Header().Get("ETag") != `"1"` {
		t.Fatalf("first save: %d %v", rec.Code, rec.Header())
	}
	if rec := put(""); rec.Code != http.StatusPreconditionRequired || !strings.Contains(rec.Body.String(), domain.CodeTemplateIfMatch) {
		t.Fatalf("replace without If-Match: %d %s", rec.Code, rec.Body)
	}
	if rec := put(`"1"`); rec.Code != http.StatusOK || rec.Header().Get("ETag") != `"2"` {
		t.Fatalf("save on version 1: %d %s", rec.Code, rec.Body)
	}
	if rec := put(`"1"`); rec.Code != http.StatusPreconditionFailed || !strings.Contains(rec.Body.String(), domain.CodeTemplateStale) {
		t.Fatalf("save on a stale version: %d %s", rec.Code, rec.Body)
	}
	if rec := put(`three`); rec.Code != http.StatusBadRequest {
		t.Fatalf("malformed If-Match: %d", rec.Code)
	}
	rec := call(t, h, http.MethodGet, "/print/v1/document-templates/INV-STD", "", "print.job.submit", "")
	if rec.Header().Get("ETag") != `"2"` {
		t.Fatalf("read ETag: %v", rec.Header())
	}
	rec = call(t, h, http.MethodGet, "/print/v1/document-templates/INV-STD/versions", "", "print.job.submit", "")
	if rec.Code != http.StatusOK || strings.Count(rec.Body.String(), `"savedBy":"designer-1"`) != 2 || !strings.Contains(rec.Body.String(), `"version":2`) {
		t.Fatalf("history: %d %s", rec.Code, rec.Body)
	}
	if rec := call(t, h, http.MethodGet, "/print/v1/document-templates/NOPE-1/versions", "", "print.job.submit", ""); rec.Code != http.StatusNotFound {
		t.Fatalf("history of a missing template: %d", rec.Code)
	}
}

func TestRenderDocument_UsesNamedTemplateAndTextBlocksInTheRequestedLanguage(t *testing.T) {
	h := docRouter()
	call(t, h, http.MethodPut, "/print/v1/text-blocks/PAY-TERMS/en", `{"body":"Pay within {{days}} days."}`, "print.admin", "")
	call(t, h, http.MethodPut, "/print/v1/text-blocks/PAY-TERMS/de", `{"body":"Zahlbar innerhalb von {{days}} Tagen."}`, "print.admin", "")
	call(t, h, http.MethodPut, "/print/v1/document-templates/INV-STD",
		`{"name":"Standard","documentType":"CUSTOMER_INVOICE","default":false,"config":{"title":"Tax invoice","noteBlocks":["PAY-TERMS"]}}`, "print.admin", "")
	doc := func(extra string) string {
		return `{"documentType":"CUSTOMER_INVOICE","number":"INV-9","legalEntity":{"name":"Cedar Ridge Pumps"},"columns":[{"header":"Item"}],"lines":[["Casting"]],"templateCode":"INV-STD"` + extra + `}`
	}
	de := call(t, h, http.MethodPost, "/print/v1/documents:render", doc(`,"language":"de","values":{"days":"30"}`), "print.document.render", "")
	if de.Code != http.StatusOK || !strings.HasPrefix(de.Body.String(), "%PDF-") {
		t.Fatalf("render: %d %s", de.Code, de.Body)
	}
	en := call(t, h, http.MethodPost, "/print/v1/documents:render", doc(`,"values":{"days":"30"}`), "print.document.render", "")
	if en.Body.String() == de.Body.String() {
		t.Fatal("the language did not change the document")
	}
	// A missing placeholder value is refused, naming it.
	miss := call(t, h, http.MethodPost, "/print/v1/documents:render", doc(``), "print.document.render", "")
	if miss.Code != http.StatusUnprocessableEntity || !strings.Contains(miss.Body.String(), "PRN-0014") || !strings.Contains(miss.Body.String(), "days") {
		t.Fatalf("missing value: %d %s", miss.Code, miss.Body)
	}
	if rec := call(t, h, http.MethodPost, "/print/v1/documents:render", strings.Replace(doc(``), "INV-STD", "GONE-1", 1), "print.document.render", ""); rec.Code != http.StatusNotFound {
		t.Fatalf("unknown template: %d", rec.Code)
	}
}

func TestRenderDocument_LetterFromABodyBlock(t *testing.T) {
	h := docRouter()
	call(t, h, http.MethodPut, "/print/v1/text-blocks/BAL-CONF/en", `{"body":"Our records show {{amount}} due from {{counterpartyName}}."}`, "print.admin", "")
	body := `{"documentType":"BALANCE_CONFIRMATION","number":"BC-1","legalEntity":{"name":"Cedar Ridge Pumps"},"counterparty":{"name":"Ironbridge"},"bodyBlock":"BAL-CONF","values":{"amount":"5,676.00 USD"}}`
	rec := call(t, h, http.MethodPost, "/print/v1/documents:render", body, "print.document.render", "")
	if rec.Code != http.StatusOK || rec.Header().Get("X-Passage-Document-Pages") != "1" {
		t.Fatalf("letter: %d %s", rec.Code, rec.Body)
	}
	noBody := `{"documentType":"PAYMENT_NOTICE","number":"PN-1","legalEntity":{"name":"Cedar Ridge Pumps"}}`
	if rec := call(t, h, http.MethodPost, "/print/v1/documents:render", noBody, "print.document.render", ""); rec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("letter without body: %d", rec.Code)
	}
}

const (
	leOne   = "11111111-0000-4000-8000-0000000000e1"
	leTwo   = "11111111-0000-4000-8000-0000000000e2"
	custOne = "22222222-0000-4000-8000-000000000001"
)

// callAs is call with a legal-entity claim (empty for none).
func callAs(t *testing.T, h http.Handler, method, path, body, duties, key, le string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	req.Header.Set("X-Passage-Tenant", testkit.TenantID(1))
	req.Header.Set("X-Passage-User", "admin-1")
	req.Header.Set("X-Passage-Duties", duties)
	if le != "" {
		req.Header.Set("X-Passage-Legal-Entity", le)
	}
	req.Header.Set("Content-Type", "application/json")
	if key != "" {
		req.Header.Set("Idempotency-Key", key)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

const tenantAdmin = "print.admin,tenant.administer"

func TestOutputRules_PlanFollowsTheMostSpecificScopePerCopyKind(t *testing.T) {
	h := docRouter()
	post := func(key, body string) {
		t.Helper()
		if rec := callAs(t, h, http.MethodPost, "/print/v1/output-rules", body, tenantAdmin, key, ""); rec.Code != http.StatusCreated {
			t.Fatalf("%s: %d %s", key, rec.Code, rec.Body)
		}
	}
	post("o1", `{"documentType":"CUSTOMER_INVOICE","copyKind":"ORIGINAL","channel":"PRINTER","destination":"OFFICE","sequence":1}`)
	post("o2", `{"documentType":"CUSTOMER_INVOICE","copyKind":"COPY","channel":"ARCHIVE","sequence":1}`)
	post("o3", `{"documentType":"CUSTOMER_INVOICE","accountId":"`+strings.ToUpper(custOne)+`","copyKind":"ORIGINAL","channel":"EMAIL","destination":"ap@contoso.example","sequence":1}`)
	if rec := callAs(t, h, http.MethodPost, "/print/v1/output-rules", `{"documentType":"CUSTOMER_INVOICE","copyKind":"ORIGINAL","channel":"EMAIL","destination":"nope","sequence":1}`, tenantAdmin, "o5", ""); rec.Code != http.StatusBadRequest {
		t.Fatalf("bad address: %d", rec.Code)
	}
	if rec := callAs(t, h, http.MethodPost, "/print/v1/output-rules", `{"documentType":"CUSTOMER_INVOICE","accountId":"cust-1","copyKind":"COPY","channel":"ARCHIVE","sequence":1}`, tenantAdmin, "o5b", ""); rec.Code != http.StatusBadRequest {
		t.Fatalf("account that is not an id: %d", rec.Code)
	}
	if rec := call(t, h, http.MethodPost, "/print/v1/output-rules", `{}`, "print.job.submit", "o6"); rec.Code != http.StatusForbidden {
		t.Fatalf("worker created a rule: %d", rec.Code)
	}
	plan := func(q string) []string {
		rec := call(t, h, http.MethodGet, "/print/v1/output-plan?documentType=CUSTOMER_INVOICE"+q, "", "print.job.submit", "")
		var out struct{ Items []domain.OutputRule }
		if rec.Code != http.StatusOK || json.Unmarshal(rec.Body.Bytes(), &out) != nil {
			t.Fatalf("plan: %d %s", rec.Code, rec.Body)
		}
		var s []string
		for _, r := range out.Items {
			s = append(s, r.CopyKind+" "+r.Channel+" "+r.Destination)
		}
		return s
	}
	if got := strings.Join(plan(""), "|"); got != "ORIGINAL PRINTER OFFICE|COPY ARCHIVE " {
		t.Errorf("default plan = %s", got)
	}
	// The account's own original replaces the default printer original; the copy stays the default's.
	if got := strings.Join(plan("&accountId="+strings.ToUpper(custOne)), "|"); got != "ORIGINAL EMAIL ap@contoso.example|COPY ARCHIVE " {
		t.Errorf("account plan = %s", got)
	}
	if rec := call(t, h, http.MethodGet, "/print/v1/output-plan?documentType=INVOICE", "", "print.job.submit", ""); rec.Code != http.StatusBadRequest {
		t.Errorf("unknown type: %d", rec.Code)
	}
	listed := call(t, h, http.MethodGet, "/print/v1/output-rules", "", "print.job.submit", "")
	var l struct{ Items []domain.OutputRule }
	_ = json.Unmarshal(listed.Body.Bytes(), &l)
	if len(l.Items) != 3 || l.Items[2].AccountID != custOne {
		t.Fatalf("listed %+v", l.Items)
	}
}

func TestOutputRules_ScopeOfTheCaller(t *testing.T) {
	h := docRouter()
	rule := func(le string) string {
		return `{"documentType":"PACKING_SLIP","legalEntityId":"` + le + `","copyKind":"ORIGINAL","channel":"ARCHIVE","sequence":1}`
	}
	global := `{"documentType":"PACKING_SLIP","copyKind":"ORIGINAL","channel":"ARCHIVE","sequence":1}`
	// An administrator of entity one may add rules for entity one only, and no rule for every entity.
	if rec := callAs(t, h, http.MethodPost, "/print/v1/output-rules", rule(leOne), "print.admin", "s1", leOne); rec.Code != http.StatusCreated {
		t.Fatalf("own entity: %d %s", rec.Code, rec.Body)
	}
	if rec := callAs(t, h, http.MethodPost, "/print/v1/output-rules", rule(leTwo), "print.admin", "s2", leOne); rec.Code != http.StatusForbidden {
		t.Fatalf("other entity: %d", rec.Code)
	}
	if rec := callAs(t, h, http.MethodPost, "/print/v1/output-rules", global, "print.admin,tenant.administer", "s3", leOne); rec.Code != http.StatusForbidden {
		t.Fatalf("global rule with an entity claim: %d", rec.Code)
	}
	if rec := callAs(t, h, http.MethodPost, "/print/v1/output-rules", global, "print.admin", "s4", ""); rec.Code != http.StatusForbidden {
		t.Fatalf("global rule without tenant.administer: %d", rec.Code)
	}
	if rec := callAs(t, h, http.MethodPost, "/print/v1/output-rules", rule(leTwo), "print.admin,tenant.administer", "s5", ""); rec.Code != http.StatusForbidden {
		t.Fatalf("entity rule by a caller with no entity scope: %d", rec.Code)
	}
	rec := callAs(t, h, http.MethodPost, "/print/v1/output-rules", global, tenantAdmin, "s6", "")
	var made domain.OutputRule
	if rec.Code != http.StatusCreated || json.Unmarshal(rec.Body.Bytes(), &made) != nil {
		t.Fatalf("tenant-wide: %d %s", rec.Code, rec.Body)
	}
	// Deleting the global rule is tenant-wide authority only.
	if rec := callAs(t, h, http.MethodDelete, "/print/v1/output-rules/"+made.ID, "", "print.admin", "", leOne); rec.Code != http.StatusForbidden {
		t.Fatalf("entity admin deleted a global rule: %d", rec.Code)
	}
	if rec := callAs(t, h, http.MethodDelete, "/print/v1/output-rules/"+made.ID, "", tenantAdmin, "", ""); rec.Code != http.StatusNoContent {
		t.Fatalf("tenant admin delete: %d", rec.Code)
	}
	if rec := callAs(t, h, http.MethodDelete, "/print/v1/output-rules/"+made.ID, "", tenantAdmin, "", ""); rec.Code != http.StatusNotFound {
		t.Fatalf("second delete: %d", rec.Code)
	}
}

func TestDocgenRoutes_Answer503WithoutAStore(t *testing.T) {
	h := router(newMemStore(), &fakeSender{})
	if rec := call(t, h, http.MethodGet, "/print/v1/text-blocks", "", "print.job.submit", ""); rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("got %d", rec.Code)
	}
	// Rendering without templates still works.
	if rec := call(t, h, http.MethodPost, "/print/v1/documents:render", poDoc, "print.document.render", ""); rec.Code != http.StatusOK {
		t.Fatalf("render without docgen store: %d", rec.Code)
	}
}

func TestTemplates_SaveChecksItsTextBlocks(t *testing.T) {
	h := docRouter()
	save := func(code, cfg string, def bool) int {
		body := fmt.Sprintf(`{"name":"T","documentType":"CUSTOMER_INVOICE","default":%v,"config":%s}`, def, cfg)
		return call(t, h, http.MethodPut, "/print/v1/document-templates/"+code, body, "print.admin", "").Code
	}
	if got := save("INV-A", `{"noteBlocks":["TERMS"]}`, true); got != http.StatusBadRequest {
		t.Fatalf("missing block: %d", got)
	}
	call(t, h, http.MethodPut, "/print/v1/text-blocks/TERMS/de", `{"body":"Nur deutsch."}`, "print.admin", "")
	if got := save("INV-A", `{"noteBlocks":["TERMS"]}`, true); got != http.StatusBadRequest {
		t.Fatalf("block without English: %d", got)
	}
	call(t, h, http.MethodPut, "/print/v1/text-blocks/TERMS/en", `{"body":"Pay within {{days}} days."}`, "print.admin", "")
	rec := call(t, h, http.MethodPut, "/print/v1/document-templates/INV-A", `{"name":"T","documentType":"CUSTOMER_INVOICE","default":true,"config":{"noteBlocks":["TERMS"]}}`, "print.admin", "")
	if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), "days") {
		t.Fatalf("default template needing a value: %d %s", rec.Code, rec.Body)
	}
	// Not a default: it is only used when named, and the caller gives the values.
	if got := save("INV-A", `{"noteBlocks":["TERMS"]}`, false); got != http.StatusOK {
		t.Fatalf("non-default template: %d", got)
	}
	call(t, h, http.MethodPut, "/print/v1/text-blocks/NAME-ONLY/en", `{"body":"Thank you, {{counterpartyName}}."}`, "print.admin", "")
	if got := save("INV-B", `{"noteBlocks":["NAME-ONLY"]}`, true); got != http.StatusOK {
		t.Fatalf("default with built-in placeholders: %d", got)
	}
	// An existing default template's block cannot grow a placeholder nobody supplies.
	if rec := call(t, h, http.MethodPut, "/print/v1/text-blocks/NAME-ONLY/en", `{"body":"Due {{dueDate}}."}`, "print.admin", ""); rec.Code != http.StatusBadRequest {
		t.Fatalf("block of a default template given a new placeholder: %d %s", rec.Code, rec.Body)
	}
	// Form notes that cannot fit one document are refused.
	call(t, h, http.MethodPut, "/print/v1/text-blocks/LONG/en", `{"body":"`+strings.Repeat("x", 1500)+`"}`, "print.admin", "")
	if got := save("INV-C", `{"noteBlocks":["LONG","LONG"]}`, false); got != http.StatusBadRequest {
		t.Fatalf("notes too long: %d", got)
	}
	// The default template then renders a plain invoice without any values.
	doc := `{"documentType":"CUSTOMER_INVOICE","number":"INV-9","legalEntity":{"name":"Cedar Ridge Pumps"},"counterparty":{"name":"Bluewater"},"columns":[{"header":"Item"}],"lines":[["Casting"]]}`
	if rec := call(t, h, http.MethodPost, "/print/v1/documents:render", doc, "print.document.render", ""); rec.Code != http.StatusOK {
		t.Fatalf("render with the default template and no values: %d %s", rec.Code, rec.Body)
	}
}
