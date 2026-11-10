package api_test

import (
	"crypto/ed25519"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/platform/tenant"
	"github.com/BenjaminSRussell/passage/platform/testkit"
	"github.com/BenjaminSRussell/passage/services/product/internal/api"
	"github.com/BenjaminSRussell/passage/services/product/internal/domain"
	"github.com/BenjaminSRussell/passage/services/product/internal/orgclient"
)

const importDuty = "product.import.write"

// migrationCall sends a request with a token minted by a signer named
// "migration" (the ERP integration loader) through the real grant table
// (serviceGrants["migration"]["product"] = product.import.write) and verified by
// a real tenant.ServiceVerifier.
func migrationCall(t *testing.T, f *fakeStore, legalEntity, method, path, body string) *httptest.ResponseRecorder {
	t.Helper()
	return migrationCallH(t, f, legalEntity, method, path, body, nil)
}

func migrationCallH(t *testing.T, f *fakeStore, legalEntity, method, path, body string, headers map[string]string) *httptest.ResponseRecorder {
	t.Helper()
	return migrationCallTo(t, newRouter(f), legalEntity, method, path, body, headers)
}

func migrationCallTo(t *testing.T, h http.Handler, legalEntity, method, path, body string, headers map[string]string) *httptest.ResponseRecorder {
	t.Helper()
	pub, priv, _ := ed25519.GenerateKey(nil)
	tenant.SetServiceVerifier(tenant.NewServiceVerifier("product", map[string]ed25519.PublicKey{"migration": pub}))
	t.Cleanup(func() { tenant.SetServiceVerifier(nil) })
	signer, err := tenant.NewServiceSigner("migration", priv)
	if err != nil {
		t.Fatal(err)
	}
	token, err := signer.Mint("product", tenant.Principal{TenantID: testkit.TenantID(1), LegalEntityID: legalEntity, UserID: tenant.ServiceActor("migration-loader")}, "")
	if err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set(tenant.HeaderServiceToken, token)
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

func TestMigrationLoaderCreatesAndReleasesInItsLegalEntity(t *testing.T) {
	f := newFake()
	le, other := testkit.TenantID(9), testkit.TenantID(7)
	id := importUUID(60)

	rec := migrationCall(t, f, le, http.MethodPost, "/v1/product/products",
		fmt.Sprintf(`{"id":%q,"productNumber":"LEG-1","name":"Legacy","baseUom":"EA","lifecycleState":"active"}`, id))
	if rec.Code != http.StatusCreated {
		t.Fatalf("create %d %s", rec.Code, rec.Body)
	}
	if _, ok := f.products[id]; !ok {
		t.Fatalf("product must be stored under the supplied id %s: %v", id, f.products)
	}

	release := func(entity string) *httptest.ResponseRecorder {
		return migrationCall(t, f, le, http.MethodPost, "/v1/product/releases",
			fmt.Sprintf(`{"productId":%q,"legalEntityId":%q,"itemGroupId":"FG","itemModelGroupId":"STD","defaultSiteId":"S1","defaultWarehouseId":"W1"}`, id, entity))
	}
	if rec := release(le); rec.Code != http.StatusCreated {
		t.Fatalf("release in own legal entity %d %s", rec.Code, rec.Body)
	}
	if rec := release(other); rec.Code != http.StatusForbidden {
		t.Fatalf("release to another legal entity = %d, want 403: %s", rec.Code, rec.Body)
	}
	if len(f.released) != 1 {
		t.Fatalf("only the in-scope release may be written, got %d", len(f.released))
	}

	// attributes, identifiers, relations and the product update route are open too.
	for _, c := range []struct{ method, path, body string }{
		{http.MethodPost, "/v1/product/products/" + id + "/attributes", `{"color":"red"}`},
		{http.MethodPost, "/v1/product/products/" + id + "/identifiers", `{"value":"06141411234579","uom":"EA"}`},
		{http.MethodPost, "/v1/product/products/" + id + "/relations", `{"toProductId":"` + testkit.TenantID(61) + `","typeCode":"substitute"}`},
	} {
		if rec := migrationCall(t, f, le, c.method, c.path, c.body); rec.Code == http.StatusForbidden || rec.Code == http.StatusUnauthorized {
			t.Errorf("%s %s must accept %s, got %d %s", c.method, c.path, importDuty, rec.Code, rec.Body)
		}
	}
	upd := func(version, body string) *httptest.ResponseRecorder {
		return migrationCallH(t, f, le, http.MethodPatch, "/v1/product/products/"+id, body, map[string]string{"If-Match": version})
	}
	if rec := upd(`"1"`, `{"name":"Renamed"}`); rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "Renamed") {
		t.Fatalf("update %d %s", rec.Code, rec.Body)
	}
	if rec := upd(`"1"`, `{"name":"Again"}`); rec.Code != http.StatusPreconditionFailed {
		t.Fatalf("stale version = %d, want 412", rec.Code)
	}
	if rec := migrationCall(t, f, le, http.MethodPatch, "/v1/product/products/"+id, `{"name":"x"}`); rec.Code != http.StatusPreconditionRequired {
		t.Fatalf("missing If-Match = %d, want 428", rec.Code)
	}
	if rec := upd(`"2"`, `{"lifecycleState":"bogus"}`); rec.Code != http.StatusBadRequest {
		t.Fatalf("bad lifecycle = %d, want 400", rec.Code)
	}

	patch := func(rpID string) *httptest.ResponseRecorder {
		return migrationCall(t, f, le, http.MethodPatch, "/v1/product/released-products/"+rpID, `{}`)
	}
	if rec := patch("rp-1"); rec.Code != http.StatusOK {
		t.Fatalf("patch own released product %d %s", rec.Code, rec.Body)
	}
	f.released["rp-2"] = f.released["rp-1"]
	rp := f.released["rp-2"]
	rp.LegalEntityID = other
	f.released["rp-2"] = rp
	if rec := patch("rp-2"); rec.Code != http.StatusForbidden {
		t.Fatalf("patch in another legal entity = %d, want 403", rec.Code)
	}
}

func TestMigrationLoaderReachesNoOtherRoute(t *testing.T) {
	f := newFake()
	le, pid := testkit.TenantID(9), importUUID(60)
	for _, c := range []struct{ method, path string }{
		{http.MethodGet, "/v1/product/products"},
		{http.MethodGet, "/v1/product/products:search"},
		{http.MethodGet, "/v1/product/products/" + pid + "/variants:lookup"},
		{http.MethodPost, "/v1/product/products/" + pid + "/variants:explode"},
		{http.MethodGet, "/v1/product/released-products"},
		{http.MethodPut, "/v1/product/released-products/rp-1/site-defaults/S1"},
		{http.MethodPost, "/v1/product/gtins"},
		{http.MethodPost, "/v1/product/setup/item-groups"},
		{http.MethodPost, "/v1/product/jobs/group-change"},
	} {
		if rec := migrationCall(t, f, le, c.method, c.path, `{}`); rec.Code != http.StatusForbidden {
			t.Errorf("%s %s with only %s = %d, want 403", c.method, c.path, importDuty, rec.Code)
		}
	}
}

// product.import.read opens the product detail read, so the loader can tie out what it loaded.
func TestMigrationLoaderReadsBackAProductByID(t *testing.T) {
	f := newFake()
	le, pid := testkit.TenantID(9), importUUID(61)
	body := fmt.Sprintf(`{"id":%q,"productNumber":"P-61","name":"Bolt","productType":"item","subtype":"product","baseUom":"ea","dimensionGroupId":"dg-1"}`, pid)
	if rec := migrationCall(t, f, le, http.MethodPost, "/v1/product/products", body); rec.Code != http.StatusCreated {
		t.Fatalf("create = %d %s", rec.Code, rec.Body)
	}
	rec := migrationCall(t, f, le, http.MethodGet, "/v1/product/products/"+pid, "")
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"productNumber":"P-61"`) {
		t.Fatalf("read = %d %s", rec.Code, rec.Body)
	}
}

func TestImportAnswersExistingProductWithItsID(t *testing.T) {
	f := newFake()
	le := testkit.TenantID(9)
	first := importUUID(61)
	body := func(id string) string {
		return fmt.Sprintf(`{"id":%q,"productNumber":"LEG-2","name":"Legacy","baseUom":"EA"}`, id)
	}
	if rec := migrationCall(t, f, le, http.MethodPost, "/v1/product/products", body(first)); rec.Code != http.StatusCreated {
		t.Fatalf("first create %d %s", rec.Code, rec.Body)
	}
	rec := migrationCall(t, f, le, http.MethodPost, "/v1/product/products", body(importUUID(62)))
	if rec.Code != http.StatusConflict {
		t.Fatalf("duplicate number = %d, want 409: %s", rec.Code, rec.Body)
	}
	var p struct {
		Code   string            `json:"code"`
		Errors map[string]string `json:"errors"`
	}
	_ = json.Unmarshal(rec.Body.Bytes(), &p)
	if p.Errors["existingId"] != first {
		t.Fatalf("409 must carry the existing id %s: %s", first, rec.Body)
	}
	taken := migrationCall(t, f, le, http.MethodPost, "/v1/product/products",
		fmt.Sprintf(`{"id":%q,"productNumber":"LEG-3","name":"Other","baseUom":"EA"}`, first))
	if taken.Code != http.StatusConflict || !strings.Contains(taken.Body.String(), "PRD-ID-TAKEN") {
		t.Fatalf("taken id = %d %s", taken.Code, taken.Body)
	}
	if rec := migrationCall(t, f, le, http.MethodPost, "/v1/product/products", `{"id":"nope","productNumber":"LEG-4","name":"X","baseUom":"EA"}`); rec.Code != http.StatusBadRequest {
		t.Fatalf("malformed id = %d, want 400", rec.Code)
	}
}

func TestHumanPathsUnchangedByImportDuty(t *testing.T) {
	r := newRouter(newFake())
	// product.write alone cannot choose ids.
	rec := do(r, http.MethodPost, "/v1/product/products", fmt.Sprintf(`{"id":%q,"productNumber":"H1","name":"H","baseUom":"EA"}`, importUUID(70)), "product.write")
	if rec.Code != http.StatusForbidden {
		t.Fatalf("supplied id without the import duty = %d, want 403", rec.Code)
	}
	if rec := do(r, http.MethodPost, "/v1/product/products", `{"productNumber":"H1","name":"H","baseUom":"EA"}`, "product.write"); rec.Code != http.StatusCreated {
		t.Fatalf("human create %d %s", rec.Code, rec.Body)
	}
	if rec := do(r, http.MethodPost, "/v1/product/products", `{"productNumber":"H2","name":"H","baseUom":"EA"}`, "product.read"); rec.Code != http.StatusForbidden {
		t.Fatalf("create without duty = %d", rec.Code)
	}
	if rec := do(r, http.MethodPost, "/v1/product/releases", `{}`, "product.write"); rec.Code != http.StatusForbidden {
		t.Fatalf("release without duty = %d", rec.Code)
	}
}

// importUUID is a version-7, RFC 9562-variant UUID such as a loader generates.
func importUUID(n int) string { return fmt.Sprintf("0190f5a0-0000-7000-8000-%012d", n) }

func TestImportIDMustBeAVersionedUUIDAndIsStoredCanonically(t *testing.T) {
	f := newFake()
	le := testkit.TenantID(9)
	create := func(id, number string) *httptest.ResponseRecorder {
		return migrationCall(t, f, le, http.MethodPost, "/v1/product/products",
			fmt.Sprintf(`{"id":%q,"productNumber":%q,"name":"Legacy","baseUom":"EA"}`, id, number))
	}
	for _, bad := range []string{
		"00000000-0000-0000-0000-000000000000",          // nil
		"ffffffff-ffff-ffff-ffff-ffffffffffff",          // max
		"00000000-0000-0000-0000-000000000060",          // version 0, NCS variant
		"0190f5a0-0000-7000-c000-000000000060",          // Microsoft variant
		"{0190f5a0-0000-7000-8000-000000000060}",        // braces
		"urn:uuid:0190f5a0-0000-7000-8000-000000000060", // URN
		"0190f5a000007000800000000000060",               // no hyphens (and short)
	} {
		if rec := create(bad, "BAD-1"); rec.Code != http.StatusBadRequest {
			t.Errorf("id %q = %d, want 400: %s", bad, rec.Code, rec.Body)
		}
	}
	if len(f.created) != 0 {
		t.Fatalf("a refused id must write nothing, got %d", len(f.created))
	}
	upper := strings.ToUpper(importUUID(90))
	if rec := create(upper, "OK-1"); rec.Code != http.StatusCreated {
		t.Fatalf("upper-case v7 id = %d %s", rec.Code, rec.Body)
	}
	if _, ok := f.products[importUUID(90)]; !ok {
		t.Fatalf("id must be stored lower-cased as Postgres returns it: %v", f.products)
	}
	if rec := create("6ba7b810-9dad-41d1-80b4-00c04fd430c8", "OK-2"); rec.Code != http.StatusCreated {
		t.Fatalf("v4 id = %d %s", rec.Code, rec.Body)
	}
}

func TestPatchProductFollowsTheLifecycleMachine(t *testing.T) {
	f := newFake()
	r := newRouter(f)
	id := importUUID(91)
	f.products[id] = domain.Product{ID: id, ProductNumber: "LC-1", Name: "LC", LifecycleState: domain.StateObsolete, Version: 3}
	patch := func(version, body, duties string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodPatch, "/v1/product/products/"+id, strings.NewReader(body))
		req.Header.Set("X-Passage-Tenant", testkit.TenantID(1))
		req.Header.Set("X-Passage-Duties", duties)
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("If-Match", version)
		rec := httptest.NewRecorder()
		r.ServeHTTP(rec, req)
		return rec
	}
	if rec := patch("3", `{"name":"x"}`, "product.read"); rec.Code != http.StatusForbidden {
		t.Fatalf("PATCH with only product.read = %d, want 403", rec.Code)
	}
	rec := patch("3", `{"lifecycleState":"active"}`, "product.write")
	if code, _ := problemCode(t, rec.Body.String()); rec.Code != http.StatusUnprocessableEntity || code != domain.CodeLifecycleIllegal {
		t.Fatalf("obsolete -> active = %d %s, want 422 %s", rec.Code, rec.Body, domain.CodeLifecycleIllegal)
	}
	if got := f.products[id]; got.LifecycleState != domain.StateObsolete || got.Version != 3 {
		t.Fatalf("refused move must write nothing: %+v", got)
	}
	if rec := patch("3", `{"name":"Renamed"}`, "product.write"); rec.Code != http.StatusOK {
		t.Fatalf("human rename with product.write = %d %s", rec.Code, rec.Body)
	}
	if rec := migrationCallH(t, f, testkit.TenantID(9), http.MethodPatch, "/v1/product/products/not-a-uuid", `{"name":"x"}`, map[string]string{"If-Match": "1"}); rec.Code != http.StatusNotFound {
		t.Fatalf("malformed id = %d, want 404", rec.Code)
	}
}

// orgServer counts lookups and records the headers of the last one.
func orgServer(t *testing.T) (*httptest.Server, *int, *http.Header) {
	t.Helper()
	hits, last := 0, http.Header{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits++
		last = r.Header.Clone()
		le := testkit.TenantID(9)
		_, _ = fmt.Fprintf(w, `{"id":%q,"legalEntityId":%q,"siteId":"S1"}`, strings.TrimPrefix(strings.TrimPrefix(r.URL.Path, "/v1/org/sites/"), "/v1/org/warehouses/"), le)
	}))
	t.Cleanup(srv.Close)
	return srv, &hits, &last
}

func TestLoaderReleaseWithPlacementsIsExplicitUntilProductCanSignForOrg(t *testing.T) {
	_, priv, _ := ed25519.GenerateKey(nil)
	productSigner, err := tenant.NewServiceSigner("product", priv)
	if err != nil {
		t.Fatal(err)
	}
	granted := len(tenant.ServiceGrantTable()["product"]["org"]) > 0
	for _, tc := range []struct {
		name   string
		signer *tenant.ServiceSigner
	}{{"no signing key", nil}, {"signing key", productSigner}} {
		t.Run(tc.name, func(t *testing.T) {
			srv, hits, last := orgServer(t)
			f := newFake()
			pid := importUUID(92)
			f.products[pid] = domain.Product{ID: pid, ProductNumber: "REL-1", LifecycleState: domain.StateActive}
			r := httpx.NewRouter()
			api.New(f).WithOrg(orgclient.NewSigned(srv.URL, tc.signer)).Register(r)
			le := testkit.TenantID(9)
			rec := migrationCallTo(t, r, le, http.MethodPost, "/v1/product/releases",
				fmt.Sprintf(`{"productId":%q,"legalEntityId":%q,"itemGroupId":"FG","itemModelGroupId":"STD","defaultSiteId":"S1","defaultWarehouseId":"W1"}`, pid, le), nil)
			if granted && tc.signer != nil {
				// Once platform grants product -> org, the lookup goes out under product's own token.
				if rec.Code != http.StatusCreated || last.Get(tenant.HeaderServiceToken) == "" || last.Get(tenant.HeaderUser) != "" {
					t.Fatalf("signed release = %d %s, token sent %v", rec.Code, rec.Body, last.Get(tenant.HeaderServiceToken) != "")
				}
				return
			}
			if code, detail := problemCode(t, rec.Body.String()); rec.Code != http.StatusServiceUnavailable || code != "PRD-ORG-SERVICE-CALLER" || !strings.Contains(detail, "org.read") {
				t.Fatalf("loader release = %d %s, want 503 PRD-ORG-SERVICE-CALLER", rec.Code, rec.Body)
			}
			if *hits != 0 || len(f.released) != 0 {
				t.Fatalf("nothing may be sent to org or written: hits=%d released=%d", *hits, len(f.released))
			}
		})
	}
	// A person releasing through the same client is still checked with their own headers.
	srv, hits, last := orgServer(t)
	f := newFake()
	f.products["p-1"] = domain.Product{ID: "p-1", ProductNumber: "P1", LifecycleState: domain.StateActive}
	r := httpx.NewRouter()
	api.New(f).WithOrg(orgclient.NewSigned(srv.URL, productSigner)).Register(r)
	le := testkit.TenantID(9)
	req := httptest.NewRequest(http.MethodPost, "/v1/product/releases", strings.NewReader(
		fmt.Sprintf(`{"productId":"p-1","legalEntityId":%q,"itemGroupId":"FG","itemModelGroupId":"STD","defaultSiteId":"S1","defaultWarehouseId":"W1"}`, le)))
	req.Header.Set("X-Passage-Tenant", testkit.TenantID(1))
	req.Header.Set("X-Passage-Legal-Entity", le)
	req.Header.Set("X-Passage-User", "u-1")
	req.Header.Set("X-Passage-Duties", "product.release")
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)
	if rec.Code != http.StatusCreated || *hits != 2 || last.Get(tenant.HeaderUser) != "u-1" || last.Get(tenant.HeaderServiceToken) != "" {
		t.Fatalf("human release = %d %s hits=%d user=%q", rec.Code, rec.Body, *hits, last.Get(tenant.HeaderUser))
	}
}
