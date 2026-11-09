package api_test

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"testing"

	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/services/party/internal/api"
	"github.com/BenjaminSRussell/passage/services/party/internal/store"
)

type fakeSearch struct {
	kind  store.SearchKind
	query string
	after store.SearchKey
	limit int
	page  store.SearchPage
	err   error
}

func (f *fakeSearch) Search(_ context.Context, kind store.SearchKind, query string, after store.SearchKey, limit int) (store.SearchPage, error) {
	f.kind, f.query, f.after, f.limit = kind, query, after, limit
	return f.page, f.err
}

func searchRouter(f *fakeSearch) http.Handler {
	r := httpx.NewRouter()
	api.New(&stubChecker{}, stubPinger{}, &fakeParties{}, &fakeSequencer{}).WithSearch(f).Register(r)
	return r
}

func TestSearchRoutes_PassTheKindQueryAndLimit(t *testing.T) {
	for path, kind := range map[string]store.SearchKind{
		"/v1/party/parties:search":   store.SearchParties,
		"/v1/party/vendors:search":   store.SearchVendors,
		"/v1/party/customers:search": store.SearchCustomers,
	} {
		f := &fakeSearch{page: store.SearchPage{Items: []store.SearchHit{{ID: knownID, PartyID: knownID, PartyNumber: "P-1", Name: "Contoso Ltd"}}}}
		rec := do(searchRouter(f), http.MethodGet, path+"?q=%20contoso%20&limit=5", "", "party.read", true)
		if rec.Code != http.StatusOK || f.kind != kind || f.query != "contoso" || f.limit != 5 || !strings.Contains(rec.Body.String(), `"name":"Contoso Ltd"`) || strings.Contains(rec.Body.String(), "nextCursor") {
			t.Fatalf("%s = %d %s with %+v", path, rec.Code, rec.Body, f)
		}
	}
}

func TestSearchRoutes_CursorRoundTrips(t *testing.T) {
	f := &fakeSearch{page: store.SearchPage{Items: []store.SearchHit{{ID: knownID, Name: "Contoso Ltd"}}, Next: &store.SearchKey{Name: "Contoso Ltd", ID: knownID}}}
	r := searchRouter(f)
	first := do(r, http.MethodGet, "/v1/party/parties:search", "", "party.read", true)
	cursor := between(first.Body.String(), `"nextCursor":"`, `"`)
	if first.Code != http.StatusOK || cursor == "" || f.limit != store.MaxSearchPage {
		t.Fatalf("first page = %d %s limit %d", first.Code, first.Body, f.limit)
	}
	do(r, http.MethodGet, "/v1/party/parties:search?cursor="+cursor, "", "party.read", true)
	if f.after != (store.SearchKey{Name: "Contoso Ltd", ID: knownID}) {
		t.Fatalf("the cursor must resume after the last hit; got %+v", f.after)
	}
}

func between(s, open, close string) string {
	_, rest, ok := strings.Cut(s, open)
	if !ok {
		return ""
	}
	value, _, _ := strings.Cut(rest, close)
	return value
}

func TestSearchRoutes_Refusals(t *testing.T) {
	r := searchRouter(&fakeSearch{})
	for name, tc := range map[string]struct {
		query, duties string
		tenant        bool
		want          int
	}{
		"limit zero":    {"?limit=0", "party.read", true, http.StatusBadRequest},
		"limit too big": {"?limit=51", "party.read", true, http.StatusBadRequest},
		"limit not int": {"?limit=x", "party.read", true, http.StatusBadRequest},
		"bad cursor":    {"?cursor=!!", "party.read", true, http.StatusBadRequest},
		"cursor no id":  {"?cursor=Q29udG9zbw", "party.read", true, http.StatusBadRequest},
		"text too long": {"?q=" + strings.Repeat("a", 101), "party.read", true, http.StatusBadRequest},
		"write only":    {"", "party.write", true, http.StatusForbidden},
		"no tenant":     {"", "party.read", false, http.StatusUnauthorized},
	} {
		if rec := do(r, http.MethodGet, "/v1/party/vendors:search"+tc.query, "", tc.duties, tc.tenant); rec.Code != tc.want {
			t.Fatalf("%s = %d %s; want %d", name, rec.Code, rec.Body, tc.want)
		}
	}
}

func TestSearchRoutes_StoreFailureIsAProblem(t *testing.T) {
	rec := do(searchRouter(&fakeSearch{err: errors.New("boom")}), http.MethodGet, "/v1/party/parties:search?q=a", "", "party.read", true)
	if rec.Code != http.StatusInternalServerError || strings.Contains(rec.Body.String(), "boom") {
		t.Fatalf("got %d %s; want a 500 problem that hides the cause", rec.Code, rec.Body)
	}
}
