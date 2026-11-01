package api

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/services/purchasing/internal/domain"
	"github.com/BenjaminSRussell/passage/services/purchasing/internal/store"
)

// catalogLog records what the catalog methods received and what they answer.
type catalogLog struct {
	created    *domain.Catalog
	entries    []domain.CatalogEntry
	ifMatch    int
	action     string
	removed    string
	search     store.CatalogSearch
	listFilter store.CatalogFilter
	catalog    domain.Catalog
	entry      domain.CatalogEntry
	err        error
}

func (f *fakeStore) CreateCatalog(_ context.Context, in domain.Catalog, _ store.Idem) (domain.Catalog, error) {
	f.cat.created = &in
	in.ID, in.Number, in.Status, in.Version = ids.New(), "CAT-00000001", domain.CatalogDraft, 1
	return in, f.cat.err
}

func (f *fakeStore) GetCatalog(context.Context, string) (domain.Catalog, error) {
	return f.cat.catalog, f.cat.err
}

func (f *fakeStore) ListCatalogs(_ context.Context, fl store.CatalogFilter, _ string, _ int) (store.Page[store.CatalogSummary], error) {
	f.cat.listFilter = fl
	return store.Page[store.CatalogSummary]{Items: []store.CatalogSummary{}}, f.cat.err
}

func (f *fakeStore) AddCatalogEntries(_ context.Context, _ string, ifMatch int, entries []domain.CatalogEntry, _ store.Idem) (domain.Catalog, error) {
	f.cat.entries, f.cat.ifMatch = entries, ifMatch
	c := f.cat.catalog
	c.Version = ifMatch + 1
	return c, f.cat.err
}

func (f *fakeStore) RemoveCatalogEntry(_ context.Context, _, entryID string, ifMatch int) (domain.Catalog, error) {
	f.cat.removed, f.cat.ifMatch = entryID, ifMatch
	c := f.cat.catalog
	c.Version = ifMatch + 1
	return c, f.cat.err
}

func (f *fakeStore) ActOnCatalog(_ context.Context, _ string, ifMatch int, action string, _ store.Idem) (domain.Catalog, error) {
	f.cat.action, f.cat.ifMatch = action, ifMatch
	c := f.cat.catalog
	c.Version = ifMatch + 1
	return c, f.cat.err
}

func (f *fakeStore) SearchCatalogEntries(_ context.Context, s store.CatalogSearch, _ string, _ int) (store.Page[store.CatalogHit], error) {
	f.cat.search = s
	return store.Page[store.CatalogHit]{Items: []store.CatalogHit{}}, f.cat.err
}

func (f *fakeStore) GetCatalogEntry(context.Context, string) (domain.Catalog, domain.CatalogEntry, error) {
	return f.cat.catalog, f.cat.entry, f.cat.err
}

const (
	catPath  = "/v1/procurement/catalogs"
	catEntry = `{"vendorId":"00000000-0000-7000-8000-0000000000a1","name":"Safety gloves","category":"MRO","unit":"PR","unitPrice":"4.250000","currency":"USD","leadTimeDays":3}`
)

func TestCatalog_CreateValidatesAndStoresDraft(t *testing.T) {
	h := newHarness(t)
	body := `{"legalEntityId":"` + leA + `","name":" Plant MRO ","validFrom":"2026-03-01","orgUnitIds":["` + leA + `","` + leA + `"],"entries":[` + catEntry + `]}`
	rec := h.do(call{method: "POST", path: catPath, body: body, duties: []string{dutyCatalogMaintain}, headers: idem("k1")})
	if rec.Code != http.StatusCreated || rec.Header().Get("ETag") != `"1"` {
		t.Fatalf("status %d ETag %q: %s", rec.Code, rec.Header().Get("ETag"), rec.Body)
	}
	got := h.fake.cat.created
	if got.Name != "Plant MRO" || len(got.OrgUnitIDs) != 1 || len(got.Entries) != 1 || got.Entries[0].UnitPrice.String() != "4.25" {
		t.Errorf("stored %+v", got)
	}
}

func TestCatalog_CreateRefusals(t *testing.T) {
	h := newHarness(t)
	for _, c := range []struct {
		name, body, code string
		duties           []string
		status           int
		headers          map[string]string
	}{
		{"no duty", `{}`, "", []string{dutyPurchaseOrderInquire}, http.StatusForbidden, idem("k")},
		{"no key", `{}`, codeIdemRequired, []string{dutyCatalogMaintain}, http.StatusBadRequest, nil},
		{"no name", `{"legalEntityId":"` + leA + `","validFrom":"2026-03-01"}`, codeInvalid, []string{dutyCatalogMaintain}, http.StatusUnprocessableEntity, idem("k")},
		{"validity inverted", `{"legalEntityId":"` + leA + `","name":"x","validFrom":"2026-03-01","validTo":"2026-02-01"}`, codeInvalid, []string{dutyCatalogMaintain}, http.StatusUnprocessableEntity, idem("k")},
		{"entry without price", `{"legalEntityId":"` + leA + `","name":"x","validFrom":"2026-03-01","entries":[{"vendorId":"` + leA + `","name":"a","category":"c","unit":"EA","currency":"USD"}]}`, codeInvalid, []string{dutyCatalogMaintain}, http.StatusUnprocessableEntity, idem("k")},
		{"negative price", `{"legalEntityId":"` + leA + `","name":"x","validFrom":"2026-03-01","entries":[{"vendorId":"` + leA + `","name":"a","category":"c","unit":"EA","currency":"USD","unitPrice":"-1"}]}`, codeInvalid, []string{dutyCatalogMaintain}, http.StatusUnprocessableEntity, idem("k")},
		{"seven decimals", `{"legalEntityId":"` + leA + `","name":"x","validFrom":"2026-03-01","entries":[{"vendorId":"` + leA + `","name":"a","category":"c","unit":"EA","currency":"USD","unitPrice":"1.1234567"}]}`, codeInvalid, []string{dutyCatalogMaintain}, http.StatusUnprocessableEntity, idem("k")},
		{"bad image", `{"legalEntityId":"` + leA + `","name":"x","validFrom":"2026-03-01","entries":[{"vendorId":"` + leA + `","name":"a","category":"c","unit":"EA","currency":"USD","unitPrice":"1","imageUrl":"javascript:alert(1)"}]}`, codeInvalid, []string{dutyCatalogMaintain}, http.StatusUnprocessableEntity, idem("k")},
		{"outside scope", `{"legalEntityId":"00000000-0000-7000-8000-0000000001e9","name":"x","validFrom":"2026-03-01"}`, codeScope, []string{dutyCatalogMaintain}, http.StatusForbidden, idem("k")},
	} {
		rec := h.do(call{method: "POST", path: catPath, body: c.body, duties: c.duties, headers: c.headers})
		if rec.Code != c.status || (c.code != "" && problemCode(t, rec) != c.code) {
			t.Errorf("%s: status %d code %q: %s", c.name, rec.Code, problemCode(t, rec), rec.Body)
		}
	}
	if h.fake.cat.created != nil {
		t.Error("a refused create reached the store")
	}
}

func TestCatalog_ActionsAndEntries(t *testing.T) {
	h := newHarness(t)
	id := ids.New()
	for _, action := range []string{"publish", "retire"} {
		rec := h.do(call{method: "POST", path: catPath + "/" + id + ":" + action, duties: []string{dutyCatalogMaintain},
			headers: map[string]string{"Idempotency-Key": "k", "If-Match": `"3"`}})
		if rec.Code != http.StatusOK || h.fake.cat.action != action || h.fake.cat.ifMatch != 3 || rec.Header().Get("ETag") != `"4"` {
			t.Errorf("%s: status %d %+v", action, rec.Code, h.fake.cat)
		}
	}
	rec := h.do(call{method: "POST", path: catPath + "/" + id + ":delete", duties: []string{dutyCatalogMaintain},
		headers: map[string]string{"Idempotency-Key": "k", "If-Match": `"3"`}})
	if rec.Code != http.StatusNotFound || problemCode(t, rec) != codeUnknownAction {
		t.Errorf("unknown action: %d %s", rec.Code, rec.Body)
	}
	rec = h.do(call{method: "POST", path: catPath + "/" + id + ":publish", duties: []string{dutyCatalogMaintain}, headers: idem("k")})
	if rec.Code != http.StatusPreconditionRequired {
		t.Errorf("missing If-Match: %d", rec.Code)
	}
	rec = h.do(call{method: "POST", path: catPath + "/" + id + "/entries", body: `{"entries":[` + catEntry + `]}`, duties: []string{dutyCatalogMaintain},
		headers: map[string]string{"Idempotency-Key": "k", "If-Match": `"1"`}})
	if rec.Code != http.StatusOK || len(h.fake.cat.entries) != 1 {
		t.Errorf("add entries: %d %s", rec.Code, rec.Body)
	}
	rec = h.do(call{method: "POST", path: catPath + "/" + id + "/entries", body: `{"entries":[]}`, duties: []string{dutyCatalogMaintain},
		headers: map[string]string{"Idempotency-Key": "k", "If-Match": `"1"`}})
	if rec.Code != http.StatusUnprocessableEntity {
		t.Errorf("empty entries: %d", rec.Code)
	}
	entry := ids.New()
	rec = h.do(call{method: "DELETE", path: catPath + "/" + id + "/entries/" + entry, duties: []string{dutyCatalogMaintain},
		headers: map[string]string{"If-Match": `"2"`}})
	if rec.Code != http.StatusOK || h.fake.cat.removed != entry || h.fake.cat.ifMatch != 2 {
		t.Errorf("remove entry: %d %+v", rec.Code, h.fake.cat)
	}
}

func TestCatalog_ReadDutiesAndFilters(t *testing.T) {
	h := newHarness(t)
	for _, duty := range []string{dutyCatalogMaintain, dutyPurchaseOrderInquire} {
		if rec := h.do(call{method: "GET", path: catPath + "?status=PUBLISHED", duties: []string{duty}}); rec.Code != http.StatusOK {
			t.Errorf("%s list: %d", duty, rec.Code)
		}
	}
	if h.fake.cat.listFilter.Status != "PUBLISHED" {
		t.Errorf("filter = %+v", h.fake.cat.listFilter)
	}
	if rec := h.do(call{method: "GET", path: catPath, duties: []string{dutyRequisitionCreate}}); rec.Code != http.StatusForbidden {
		t.Errorf("a requester must not list catalogs: %d", rec.Code)
	}
	if rec := h.do(call{method: "GET", path: catPath + "?status=NOPE", duties: []string{dutyCatalogMaintain}}); rec.Code != http.StatusUnprocessableEntity {
		t.Errorf("bad status: %d", rec.Code)
	}
	h.fake.cat.err = httpx.NewProblem(http.StatusNotFound, store.CodeCatalogNotFound, "procurement catalog not found")
	if rec := h.do(call{method: "GET", path: catPath + "/" + ids.New(), duties: []string{dutyCatalogMaintain}}); rec.Code != http.StatusNotFound {
		t.Errorf("get missing: %d", rec.Code)
	}
}

func TestCatalog_SearchForRequesters(t *testing.T) {
	h := newHarness(t)
	unit := ids.New()
	rec := h.do(call{method: "GET", path: "/v1/procurement/catalog-entries?legalEntityId=" + leA + "&operatingUnitId=" + unit + "&q=glove&category=MRO",
		duties: []string{dutyRequisitionCreate}})
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d: %s", rec.Code, rec.Body)
	}
	s := h.fake.cat.search
	if s.LegalEntityID != leA || s.OrgUnitID != unit || s.Text != "glove" || s.Category != "MRO" || s.Date.String() != "2026-03-01" {
		t.Errorf("search = %+v", s)
	}
	for _, c := range []struct{ name, query, code string }{
		{"no legal entity", "", codeInvalid},
		{"bad date", "?legalEntityId=" + leA + "&date=1-2-3", codeInvalid},
		{"outside scope", "?legalEntityId=00000000-0000-7000-8000-0000000001e9", codeScope},
	} {
		rec := h.do(call{method: "GET", path: "/v1/procurement/catalog-entries" + c.query, duties: []string{dutyRequisitionCreate}})
		if problemCode(t, rec) != c.code {
			t.Errorf("%s: %d %s", c.name, rec.Code, rec.Body)
		}
	}
	if rec := h.do(call{method: "GET", path: "/v1/procurement/catalog-entries?legalEntityId=" + leA, duties: []string{dutyPurchaseOrderInquire}}); rec.Code != http.StatusForbidden {
		t.Errorf("inquire alone cannot search: %d", rec.Code)
	}
}

func pickedEntry() (domain.Catalog, domain.CatalogEntry) {
	from, _ := domain.ParseDay("2026-01-01")
	cat := domain.Catalog{ID: ids.New(), Number: "CAT-1", LegalEntityID: leA, Status: domain.CatalogPublished, ValidFrom: from}
	e := domain.CatalogEntry{ID: ids.New(), VendorID: ids.New(), ProductID: ids.New(), Name: "Safety gloves", Category: "MRO", Unit: "PR",
		UnitPrice: decimal.RequireFromString("4.25"), Currency: "USD"}
	return cat, e
}

func TestRequisition_CatalogLineTakesTheEntryAndKeepsTheLink(t *testing.T) {
	h := newHarness(t)
	h.fake.cat.catalog, h.fake.cat.entry = pickedEntry()
	e := h.fake.cat.entry
	body := `{"legalEntityId":"` + leA + `","currency":"USD","lines":[{"catalogEntryId":"` + e.ID + `","qty":"10","requestedDate":"2026-03-10"}]}`
	rec := h.do(call{method: "POST", path: "/v1/procurement/requisitions", body: body, user: userA, duties: []string{dutyRequisitionCreate}, headers: idem("k")})
	if rec.Code != http.StatusCreated {
		t.Fatalf("status %d: %s", rec.Code, rec.Body)
	}
	var got store.Requisition
	_ = json.Unmarshal(rec.Body.Bytes(), &got)
	l := got.Lines[0]
	if l.CatalogEntryID != e.ID || l.ProductID != e.ProductID || l.Unit != "PR" || l.Category != "MRO" || l.Description != "Safety gloves" ||
		l.PreferredVendorID != e.VendorID || l.EstimatedUnitPrice.String() != "4.25" || got.Total.String() != "42.5" || len(got.Warnings) != 0 {
		t.Errorf("line = %+v total %s warnings %v", l, got.Total, got.Warnings)
	}
}

func TestRequisition_CatalogLineRefusals(t *testing.T) {
	line := func(entryID, extra string) string {
		return `{"legalEntityId":"` + leA + `","currency":"USD","lines":[{"catalogEntryId":"` + entryID + `","qty":"1","requestedDate":"2026-03-10"` + extra + `}]}`
	}
	for _, c := range []struct {
		name   string
		mutate func(*domain.Catalog, *domain.CatalogEntry)
		extra  string
		code   string
		status int
	}{
		{"retired catalog", func(c *domain.Catalog, _ *domain.CatalogEntry) { c.Status = domain.CatalogRetired }, "", domain.CodeCatalogUnavailable, 422},
		{"draft catalog", func(c *domain.Catalog, _ *domain.CatalogEntry) { c.Status = domain.CatalogDraft }, "", domain.CodeCatalogUnavailable, 422},
		{"not yet valid", func(c *domain.Catalog, _ *domain.CatalogEntry) { c.ValidFrom, _ = domain.ParseDay("2026-04-01") }, "", domain.CodeCatalogUnavailable, 422},
		{"other unit", func(c *domain.Catalog, _ *domain.CatalogEntry) { c.OrgUnitIDs = []string{ids.New()} }, "", domain.CodeCatalogUnavailable, 422},
		{"other currency", func(_ *domain.Catalog, e *domain.CatalogEntry) { e.Currency = "EUR" }, "", domain.CodeCatalogCurrency, 422},
		{"other unit of measure", nil, `,"unit":"BOX"`, codeInvalid, 422},
		{"other product", nil, `,"productId":"00000000-0000-7000-8000-0000000000b7"`, codeInvalid, 422},
	} {
		h := newHarness(t)
		h.fake.cat.catalog, h.fake.cat.entry = pickedEntry()
		if c.mutate != nil {
			c.mutate(&h.fake.cat.catalog, &h.fake.cat.entry)
		}
		rec := h.do(call{method: "POST", path: "/v1/procurement/requisitions", body: line(h.fake.cat.entry.ID, c.extra), user: userA,
			duties: []string{dutyRequisitionCreate}, headers: idem("k")})
		if rec.Code != c.status || problemCode(t, rec) != c.code {
			t.Errorf("%s: status %d code %s: %s", c.name, rec.Code, problemCode(t, rec), rec.Body)
		}
		if len(h.fake.requisitions) != 0 {
			t.Errorf("%s: a refused requisition was stored", c.name)
		}
	}
	// An entry the store cannot find (unknown, or hidden by legal-entity RLS) is a field
	// error on the line, not a 404 for the requisition.
	h := newHarness(t)
	h.fake.cat.err = httpx.NewProblem(http.StatusNotFound, store.CodeCatalogEntryNotFound, "catalog entry not found")
	rec := h.do(call{method: "POST", path: "/v1/procurement/requisitions", body: line(ids.New(), ""), user: userA,
		duties: []string{dutyRequisitionCreate}, headers: idem("k")})
	if rec.Code != http.StatusUnprocessableEntity || problemCode(t, rec) != codeInvalid || !strings.Contains(rec.Body.String(), "lines[0].catalogEntryId") {
		t.Errorf("unknown entry: %d %s", rec.Code, rec.Body)
	}
	// Outside the caller's legal entities the requisition is refused for scope before any entry lookup.
	h = newHarness(t)
	h.fake.cat.catalog, h.fake.cat.entry = pickedEntry()
	outside := strings.Replace(line(h.fake.cat.entry.ID, ""), leA, "00000000-0000-7000-8000-0000000001e9", 1)
	rec = h.do(call{method: "POST", path: "/v1/procurement/requisitions", body: outside, user: userA,
		duties: []string{dutyRequisitionCreate}, headers: idem("k")})
	if rec.Code != http.StatusForbidden || problemCode(t, rec) != codeScope {
		t.Errorf("outside scope: %d %s", rec.Code, rec.Body)
	}
	h = newHarness(t)
	rec = h.do(call{method: "POST", path: "/v1/procurement/requisitions", body: line("nope", ""), user: userA,
		duties: []string{dutyRequisitionCreate}, headers: idem("k")})
	if rec.Code != http.StatusUnprocessableEntity || !strings.Contains(rec.Body.String(), "catalogEntryId") {
		t.Errorf("bad entry id: %d %s", rec.Code, rec.Body)
	}
}
