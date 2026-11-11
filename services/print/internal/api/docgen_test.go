package api_test

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"sort"
	"strings"
	"sync"
	"testing"

	"github.com/BenjaminSRussell/passage/platform/httpx"
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
}

func newMemDocs() *memDocs {
	return &memDocs{templates: map[string]domain.DocumentTemplate{}, blocks: map[string]domain.TextBlock{}}
}

func (m *memDocs) PutTemplate(_ context.Context, t domain.DocumentTemplate) (domain.DocumentTemplate, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	t.Version = m.templates[t.Code].Version + 1
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

func (m *memDocs) DeleteOutputRule(_ context.Context, id string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	for i, r := range m.rules {
		if r.ID == id {
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
	if rec := call(t, h, http.MethodPut, "/print/v1/document-templates/INV-STD", body, "print.admin", ""); !strings.Contains(rec.Body.String(), `"version":2`) {
		t.Fatalf("second save: %s", rec.Body)
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

func TestRenderDocument_UsesDefaultTemplateAndTextBlocksInTheRequestedLanguage(t *testing.T) {
	h := docRouter()
	call(t, h, http.MethodPut, "/print/v1/text-blocks/PAY-TERMS/en", `{"body":"Pay within {{days}} days."}`, "print.admin", "")
	call(t, h, http.MethodPut, "/print/v1/text-blocks/PAY-TERMS/de", `{"body":"Zahlbar innerhalb von {{days}} Tagen."}`, "print.admin", "")
	call(t, h, http.MethodPut, "/print/v1/document-templates/INV-STD",
		`{"name":"Standard","documentType":"CUSTOMER_INVOICE","default":true,"config":{"title":"Tax invoice","noteBlocks":["PAY-TERMS"]}}`, "print.admin", "")
	doc := func(extra string) string {
		return `{"documentType":"CUSTOMER_INVOICE","number":"INV-9","legalEntity":{"name":"Cedar Ridge Pumps"},"columns":[{"header":"Item"}],"lines":[["Casting"]]` + extra + `}`
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
	if rec := call(t, h, http.MethodPost, "/print/v1/documents:render", doc(`,"templateCode":"GONE-1"`), "print.document.render", ""); rec.Code != http.StatusNotFound {
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

func TestOutputRules_PlanFollowsTheMostSpecificScope(t *testing.T) {
	h := docRouter()
	post := func(key, body string) {
		t.Helper()
		if rec := call(t, h, http.MethodPost, "/print/v1/output-rules", body, "print.admin", key); rec.Code != http.StatusCreated {
			t.Fatalf("%s: %d %s", key, rec.Code, rec.Body)
		}
	}
	post("o1", `{"documentType":"CUSTOMER_INVOICE","copyKind":"ORIGINAL","channel":"PRINTER","destination":"OFFICE","sequence":1}`)
	post("o2", `{"documentType":"CUSTOMER_INVOICE","copyKind":"COPY","channel":"ARCHIVE","sequence":1}`)
	post("o3", `{"documentType":"CUSTOMER_INVOICE","accountId":"cust-1","copyKind":"ORIGINAL","channel":"EMAIL","destination":"ap@contoso.example","sequence":1}`)
	post("o4", `{"documentType":"CUSTOMER_INVOICE","accountId":"cust-1","copyKind":"ORIGINAL","channel":"PRINTER","destination":"VIP","copies":2,"sequence":1}`)
	if rec := call(t, h, http.MethodPost, "/print/v1/output-rules", `{"documentType":"CUSTOMER_INVOICE","copyKind":"ORIGINAL","channel":"EMAIL","destination":"nope","sequence":1}`, "print.admin", "o5"); rec.Code != http.StatusBadRequest {
		t.Fatalf("bad address: %d", rec.Code)
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
	if got := strings.Join(plan("&accountId=cust-1"), "|"); got != "ORIGINAL PRINTER VIP|ORIGINAL EMAIL ap@contoso.example|COPY ARCHIVE " {
		t.Errorf("account plan = %s", got)
	}
	if rec := call(t, h, http.MethodGet, "/print/v1/output-plan?documentType=INVOICE", "", "print.job.submit", ""); rec.Code != http.StatusBadRequest {
		t.Errorf("unknown type: %d", rec.Code)
	}
	listed := call(t, h, http.MethodGet, "/print/v1/output-rules", "", "print.job.submit", "")
	var l struct{ Items []domain.OutputRule }
	_ = json.Unmarshal(listed.Body.Bytes(), &l)
	if len(l.Items) != 4 {
		t.Fatalf("listed %d rules", len(l.Items))
	}
	if rec := call(t, h, http.MethodDelete, "/print/v1/output-rules/"+l.Items[0].ID, "", "print.admin", ""); rec.Code != http.StatusNoContent {
		t.Fatalf("delete: %d", rec.Code)
	}
	if rec := call(t, h, http.MethodDelete, "/print/v1/output-rules/"+l.Items[0].ID, "", "print.admin", ""); rec.Code != http.StatusNotFound {
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
