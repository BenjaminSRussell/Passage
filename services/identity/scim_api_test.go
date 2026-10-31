package identity_test

import (
	"bytes"
	"context"
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"math/big"
	"net/http"
	"net/http/httptest"
	"net/url"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/BenjaminSRussell/passage/services/identity"
)

const (
	scimClient    = "scim-client"
	scimMedia     = "application/scim+json"
	schemaPatchOp = "urn:ietf:params:scim:api:messages:2.0:PatchOp"
)

type scimFixture struct {
	*adminFixture
	scim       http.Handler
	key        *rsa.PrivateKey
	issuerBase string
	opaSeen    []opaCall
}

type opaCall struct{ User, Action, Resource string }

func newSCIMFixture(t *testing.T) *scimFixture {
	t.Helper()
	f := &scimFixture{adminFixture: newAdminFixture(t)}
	var err error
	if f.key, err = rsa.GenerateKey(rand.Reader, 2048); err != nil {
		t.Fatal(err)
	}
	jwks := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{"keys": []map[string]string{{
			"kty": "RSA", "kid": "key-1", "use": "sig", "alg": "RS256",
			"n": base64.RawURLEncoding.EncodeToString(f.key.N.Bytes()), "e": base64.RawURLEncoding.EncodeToString(big.NewInt(int64(f.key.E)).Bytes()),
		}}})
	}))
	t.Cleanup(jwks.Close)
	f.issuerBase = jwks.URL + "/realms"
	opa := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var in struct {
			Input struct {
				Subject struct {
					UserID string `json:"user_id"`
				} `json:"subject"`
				Action, Resource string
			} `json:"input"`
		}
		_ = json.NewDecoder(r.Body).Decode(&in)
		f.opaSeen = append(f.opaSeen, opaCall{in.Input.Subject.UserID, in.Input.Action, in.Input.Resource})
		_ = json.NewEncoder(w).Encode(map[string]any{"result": map[string]any{"allow": in.Input.Subject.UserID == scimClient, "duty_missing": "Provision users via SCIM"}})
	}))
	t.Cleanup(opa.Close)
	verifiers, err := identity.NewTenantVerifiers(f.app, f.issuerBase, "passage-api", jwks.Client())
	if err != nil {
		t.Fatal(err)
	}
	client, err := identity.NewOPAClient(opa.URL, opa.Client())
	if err != nil {
		t.Fatal(err)
	}
	router := chi.NewRouter()
	identity.NewSCIMAPI(f.app, verifiers, client).Register(router)
	f.scim = router
	return f
}

func (f *scimFixture) token(tenantID, sub string) string {
	now := time.Now()
	return f.signed(map[string]any{"iss": f.issuerBase + "/realm-" + tenantID, "sub": sub, "tid": tenantID, "aud": "passage-api", "iat": now.Unix(), "exp": now.Add(5 * time.Minute).Unix()}, f.key)
}

func (f *scimFixture) signed(claims map[string]any, key *rsa.PrivateKey) string {
	header, _ := json.Marshal(map[string]string{"alg": "RS256", "kid": "key-1"})
	payload, _ := json.Marshal(claims)
	signing := base64.RawURLEncoding.EncodeToString(header) + "." + base64.RawURLEncoding.EncodeToString(payload)
	digest := sha256.Sum256([]byte(signing))
	signature, _ := rsa.SignPKCS1v15(rand.Reader, key, crypto.SHA256, digest[:])
	return signing + "." + base64.RawURLEncoding.EncodeToString(signature)
}

func (f *scimFixture) call(token, method, path string, body any) *httptest.ResponseRecorder {
	var reader *bytes.Reader
	switch b := body.(type) {
	case nil:
		reader = bytes.NewReader(nil)
	case string:
		reader = bytes.NewReader([]byte(b))
	default:
		raw, _ := json.Marshal(b)
		reader = bytes.NewReader(raw)
	}
	req := httptest.NewRequest(method, path, reader)
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	rec := httptest.NewRecorder()
	f.scim.ServeHTTP(rec, req)
	return rec
}

func (f *scimFixture) as(tenantID string) func(method, path string, body any) *httptest.ResponseRecorder {
	token := f.token(tenantID, scimClient)
	return func(method, path string, body any) *httptest.ResponseRecorder {
		return f.call(token, method, path, body)
	}
}

type scimErrorBody struct {
	Schemas  []string `json:"schemas"`
	Status   string   `json:"status"`
	ScimType string   `json:"scimType"`
	Detail   string   `json:"detail"`
}

func wantSCIMError(t *testing.T, rec *httptest.ResponseRecorder, status, scimType string) {
	t.Helper()
	body := decode[scimErrorBody](t, rec, atoi(status))
	if body.Status != status || body.ScimType != scimType || len(body.Schemas) != 1 || !strings.HasSuffix(body.Schemas[0], "Error") {
		t.Fatalf("error body = %+v, want %s/%s", body, status, scimType)
	}
	if got := rec.Header().Get("Content-Type"); got != scimMedia {
		t.Fatalf("content type = %q", got)
	}
}

func atoi(s string) int {
	n := 0
	for _, c := range s {
		n = n*10 + int(c-'0')
	}
	return n
}

type scimUserBody struct {
	ID          string `json:"id"`
	ExternalID  string `json:"externalId"`
	UserName    string `json:"userName"`
	DisplayName string `json:"displayName"`
	Active      bool   `json:"active"`
	Emails      []struct {
		Value   string `json:"value"`
		Primary bool   `json:"primary"`
	} `json:"emails"`
	Groups []struct {
		Value   string `json:"value"`
		Display string `json:"display"`
	} `json:"groups"`
	Meta struct {
		Location string `json:"location"`
	} `json:"meta"`
}

type scimGroupBody struct {
	ID          string `json:"id"`
	DisplayName string `json:"displayName"`
	Members     []struct {
		Value string `json:"value"`
	} `json:"members"`
}

type scimList[T any] struct {
	TotalResults int `json:"totalResults"`
	StartIndex   int `json:"startIndex"`
	ItemsPerPage int `json:"itemsPerPage"`
	Resources    []T `json:"Resources"`
}

func patchBody(ops ...map[string]any) map[string]any {
	return map[string]any{"schemas": []string{schemaPatchOp}, "Operations": ops}
}

func (f *scimFixture) createUser(t *testing.T, do func(string, string, any) *httptest.ResponseRecorder, name string) scimUserBody {
	t.Helper()
	return decode[scimUserBody](t, do(http.MethodPost, "/scim/v2/Users", map[string]any{
		"schemas": []string{"urn:ietf:params:scim:schemas:core:2.0:User"}, "userName": name, "externalId": "ext-" + name,
		"displayName": "Display " + name, "emails": []map[string]any{{"value": name + "@example.test", "primary": true}}, "active": true,
	}), http.StatusCreated)
}

func TestSCIMAuthentication(t *testing.T) {
	f := newSCIMFixture(t)
	other, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	claims := func(tid, realmOf, sub string) map[string]any {
		return map[string]any{"iss": f.issuerBase + "/realm-" + realmOf, "sub": sub, "tid": tid, "aud": "passage-api", "iat": now.Unix(), "exp": now.Add(5 * time.Minute).Unix()}
	}

	for name, tc := range map[string]string{
		"no token":                 "",
		"garbage":                  "not.a.jwt",
		"unknown tenant":           f.signed(claims("00000000-0000-0000-0000-0000000000aa", f.tenantA, scimClient), f.key),
		"wrong signing key":        f.signed(claims(f.tenantA, f.tenantA, scimClient), other),
		"tid of B with realm of A": f.signed(claims(f.tenantB, f.tenantA, scimClient), f.key),
		"tid not a uuid":           f.signed(claims("tenant-a", f.tenantA, scimClient), f.key),
	} {
		t.Run(name, func(t *testing.T) {
			wantSCIMError(t, f.call(tc, http.MethodGet, "/scim/v2/Users", nil), "401", "")
		})
	}

	t.Run("a suspended tenant is refused", func(t *testing.T) {
		if _, err := f.admin.Exec(context.Background(), `update identity_tenants set status = 'suspended' where tenant_id = $1`, f.tenantB); err != nil {
			t.Fatal(err)
		}
		wantSCIMError(t, f.call(f.token(f.tenantB, scimClient), http.MethodGet, "/scim/v2/Users", nil), "401", "")
	})

	t.Run("policy decides on the request method and path", func(t *testing.T) {
		f.opaSeen = nil
		if rec := f.call(f.token(f.tenantA, "intruder"), http.MethodGet, "/scim/v2/Users", nil); rec.Code != http.StatusForbidden {
			t.Fatalf("intruder = %d %s", rec.Code, rec.Body.String())
		}
		if rec := f.as(f.tenantA)(http.MethodDelete, "/scim/v2/Users/"+userID, nil); rec.Code != http.StatusNotFound {
			t.Fatalf("delete = %d", rec.Code)
		}
		want := []opaCall{{"intruder", "GET", "/scim/v2/Users"}, {scimClient, "DELETE", "/scim/v2/Users/" + userID}}
		if !slices.Equal(f.opaSeen, want) {
			t.Fatalf("policy saw %+v, want %+v", f.opaSeen, want)
		}
	})
}

func TestSCIMUserLifecycle(t *testing.T) {
	f := newSCIMFixture(t)
	do := f.as(f.tenantA)
	ctx := context.Background()

	created := f.createUser(t, do, "Ada.Lovelace")
	if created.ID == "" || created.UserName != "Ada.Lovelace" || !created.Active || created.DisplayName != "Display Ada.Lovelace" || len(created.Emails) != 1 || created.Emails[0].Value != "Ada.Lovelace@example.test" {
		t.Fatalf("created = %+v", created)
	}
	rec := do(http.MethodGet, "/scim/v2/Users/"+created.ID, nil)
	if got := decode[scimUserBody](t, rec, http.StatusOK); got.Meta.Location != "/scim/v2/Users/"+created.ID || rec.Header().Get("Content-Type") != scimMedia {
		t.Fatalf("get = %+v %q", got, rec.Header().Get("Content-Type"))
	}

	t.Run("placeholder idp subject is bound later by the IdP push", func(t *testing.T) {
		var subject string
		if err := f.admin.QueryRow(ctx, `select idp_subject from identity_users where user_id = $1`, created.ID).Scan(&subject); err != nil || subject != "scim:"+created.ID {
			t.Fatalf("idp_subject = %q err = %v", subject, err)
		}
	})

	t.Run("userName is unique per tenant, ignoring case, and not across tenants", func(t *testing.T) {
		wantSCIMError(t, do(http.MethodPost, "/scim/v2/Users", map[string]any{"userName": "ADA.LOVELACE"}), "409", "uniqueness")
		f.createUser(t, f.as(f.tenantB), "Ada.Lovelace")
	})

	t.Run("lenient bodies: unknown attributes ignored, name defaults displayName, string booleans accepted", func(t *testing.T) {
		got := decode[scimUserBody](t, do(http.MethodPost, "/scim/v2/Users", `{"userName":"bob","name":{"givenName":"Bob","familyName":"Builder"},"x-unknown":{"a":1},"active":"False"}`), http.StatusCreated)
		if got.DisplayName != "Bob Builder" || got.Active {
			t.Fatalf("got = %+v", got)
		}
	})

	t.Run("validation", func(t *testing.T) {
		wantSCIMError(t, do(http.MethodPost, "/scim/v2/Users", map[string]any{"displayName": "no name"}), "400", "invalidValue")
		wantSCIMError(t, do(http.MethodPost, "/scim/v2/Users", map[string]any{"userName": strings.Repeat("x", 256)}), "400", "invalidValue")
		wantSCIMError(t, do(http.MethodPost, "/scim/v2/Users", map[string]any{"userName": "z", "active": "maybe"}), "400", "invalidValue")
		wantSCIMError(t, do(http.MethodPost, "/scim/v2/Users", "[1]"), "400", "invalidSyntax")
		wantSCIMError(t, do(http.MethodGet, "/scim/v2/Users/not-a-uuid", nil), "404", "")
		wantSCIMError(t, do(http.MethodGet, "/scim/v2/Users?filter="+url.QueryEscape(`userName co "a"`), nil), "400", "invalidFilter")
		wantSCIMError(t, do(http.MethodGet, "/scim/v2/Users?filter="+url.QueryEscape(`active eq "x"`), nil), "400", "invalidFilter")
		wantSCIMError(t, do(http.MethodPatch, "/scim/v2/Users/"+created.ID, patchBody(map[string]any{"op": "move", "path": "active"})), "400", "invalidValue")
		wantSCIMError(t, do(http.MethodPatch, "/scim/v2/Users/"+created.ID, map[string]any{"Operations": []any{}}), "400", "invalidValue")
	})

	t.Run("list filters, pages and hides users SCIM did not create", func(t *testing.T) {
		all := decode[scimList[scimUserBody]](t, do(http.MethodGet, "/scim/v2/Users", nil), http.StatusOK)
		if all.TotalResults != 2 || len(all.Resources) != 2 || all.StartIndex != 1 || all.ItemsPerPage != 2 {
			t.Fatalf("all = %+v (the user seeded without a userName must stay hidden)", all)
		}
		byName := decode[scimList[scimUserBody]](t, do(http.MethodGet, "/scim/v2/Users?filter="+url.QueryEscape(`USERNAME EQ "ada.LOVELACE"`), nil), http.StatusOK)
		if byName.TotalResults != 1 || byName.Resources[0].ID != created.ID {
			t.Fatalf("byName = %+v", byName)
		}
		byExternal := decode[scimList[scimUserBody]](t, do(http.MethodGet, "/scim/v2/Users?filter="+url.QueryEscape(`externalId eq "ext-Ada.Lovelace"`), nil), http.StatusOK)
		if byExternal.TotalResults != 1 || byExternal.Resources[0].ID != created.ID {
			t.Fatalf("byExternal = %+v", byExternal)
		}
		none := decode[scimList[scimUserBody]](t, do(http.MethodGet, "/scim/v2/Users?filter="+url.QueryEscape(`userName eq ""`), nil), http.StatusOK)
		if none.TotalResults != 0 || len(none.Resources) != 0 {
			t.Fatalf("empty value matched %+v", none)
		}
		second := decode[scimList[scimUserBody]](t, do(http.MethodGet, "/scim/v2/Users?startIndex=2&count=1", nil), http.StatusOK)
		if second.TotalResults != 2 || second.StartIndex != 2 || second.ItemsPerPage != 1 || len(second.Resources) != 1 || second.Resources[0].ID == all.Resources[0].ID {
			t.Fatalf("second = %+v", second)
		}
		countOnly := decode[scimList[scimUserBody]](t, do(http.MethodGet, "/scim/v2/Users?count=0", nil), http.StatusOK)
		if countOnly.TotalResults != 2 || len(countOnly.Resources) != 0 {
			t.Fatalf("countOnly = %+v", countOnly)
		}
		wantSCIMError(t, do(http.MethodGet, "/scim/v2/Users/"+userID, nil), "404", "")
	})

	t.Run("tenants are isolated", func(t *testing.T) {
		wantSCIMError(t, f.as(f.tenantB)(http.MethodGet, "/scim/v2/Users/"+created.ID, nil), "404", "")
		wantSCIMError(t, f.as(f.tenantB)(http.MethodDelete, "/scim/v2/Users/"+created.ID, nil), "404", "")
	})

	t.Run("PUT replaces: omitted attributes are cleared", func(t *testing.T) {
		got := decode[scimUserBody](t, do(http.MethodPut, "/scim/v2/Users/"+created.ID, map[string]any{"userName": "ada.l", "displayName": "Ada L"}), http.StatusOK)
		if got.UserName != "ada.l" || got.ExternalID != "" || len(got.Emails) != 0 || !got.Active || got.DisplayName != "Ada L" {
			t.Fatalf("put = %+v", got)
		}
	})

	t.Run("PATCH handles Entra and RFC shapes", func(t *testing.T) {
		got := decode[scimUserBody](t, do(http.MethodPatch, "/scim/v2/Users/"+created.ID, patchBody(
			map[string]any{"op": "Replace", "path": "displayName", "value": "Ada Lovelace"},
			map[string]any{"op": "Add", "value": map[string]any{"externalId": "ext-2", "emails[type eq \"work\"].value": "ada@example.test", "name.givenName": "ignored"}},
		)), http.StatusOK)
		if got.DisplayName != "Ada Lovelace" || got.ExternalID != "ext-2" || len(got.Emails) != 1 || got.Emails[0].Value != "ada@example.test" {
			t.Fatalf("patch = %+v", got)
		}
		cleared := decode[scimUserBody](t, do(http.MethodPatch, "/scim/v2/Users/"+created.ID, patchBody(map[string]any{"op": "remove", "path": "externalId"})), http.StatusOK)
		if cleared.ExternalID != "" {
			t.Fatalf("externalId not removed: %+v", cleared)
		}
		wantSCIMError(t, do(http.MethodPatch, "/scim/v2/Users/"+created.ID, patchBody(map[string]any{"op": "remove", "path": "userName"})), "400", "invalidValue")
		wantSCIMError(t, do(http.MethodPatch, "/scim/v2/Users/"+created.ID, patchBody(map[string]any{"op": "replace", "path": "active", "value": "nope"})), "400", "invalidValue")
	})

	t.Run("deprovisioning ends active and future assignments in one step and is audited", func(t *testing.T) {
		for _, stmt := range []string{
			`insert into identity_role_assignments (tenant_id, assignment_id, user_id, role, valid_from, valid_to) values ($1, gen_random_uuid(), $2, 'role-active', now() - interval '1 day', null)`,
			`insert into identity_role_assignments (tenant_id, assignment_id, user_id, role, legal_entity_id, valid_from, valid_to) values ($1, gen_random_uuid(), $2, 'role-active', '` + legalA + `', now() - interval '1 day', now() + interval '30 days')`,
			`insert into identity_role_assignments (tenant_id, assignment_id, user_id, role, valid_from, valid_to) values ($1, gen_random_uuid(), $2, 'role-future', now() + interval '1 day', now() + interval '2 days')`,
			`insert into identity_role_assignments (tenant_id, assignment_id, user_id, role, valid_from, valid_to) values ($1, gen_random_uuid(), $2, 'role-past', now() - interval '3 days', now() - interval '2 days')`,
		} {
			if _, err := f.admin.Exec(ctx, stmt, f.tenantA, created.ID); err != nil {
				t.Fatal(err)
			}
		}
		past := func() string {
			var v string
			if err := f.admin.QueryRow(ctx, `select valid_to::text from identity_role_assignments where user_id = $1 and role = 'role-past'`, created.ID).Scan(&v); err != nil {
				t.Fatal(err)
			}
			return v
		}
		pastBefore := past()

		got := decode[scimUserBody](t, do(http.MethodPatch, "/scim/v2/Users/"+created.ID, patchBody(map[string]any{"op": "replace", "path": "active", "value": "False"})), http.StatusOK)
		if got.Active {
			t.Fatalf("still active: %+v", got)
		}
		var status string
		var openOrFuture int
		if err := f.admin.QueryRow(ctx, `select (select status from identity_users where user_id = $1),
  (select count(*) from identity_role_assignments where user_id = $1 and (valid_to is null or valid_to > greatest(now(), valid_from)))`, created.ID).Scan(&status, &openOrFuture); err != nil {
			t.Fatal(err)
		}
		if status != "deprovisioned" || openOrFuture != 0 {
			t.Fatalf("status = %s, assignments still open or future = %d", status, openOrFuture)
		}
		var futureCancelled bool
		if err := f.admin.QueryRow(ctx, `select valid_to = valid_from from identity_role_assignments where user_id = $1 and role = 'role-future'`, created.ID).Scan(&futureCancelled); err != nil || !futureCancelled {
			t.Fatalf("future assignment not cancelled to an empty range: %v %v", futureCancelled, err)
		}
		if past() != pastBefore {
			t.Fatal("an already-ended assignment was rewritten")
		}

		audit, events := f.auditAndEvents(t, f.tenantA)
		for _, want := range []string{"user.deprovisioned|" + scimClient, "roleassignment.ended|" + scimClient} {
			if !slices.Contains(audit, want) {
				t.Errorf("audit %v lacks %q", audit, want)
			}
		}
		if n := countOf(audit, "roleassignment.ended|"+scimClient); n != 3 {
			t.Errorf("ended audit rows = %d, want 3 (active tenant-wide, active entity-scoped, future)", n)
		}
		if !slices.Contains(events, "dev.passage.identity.user.deprovisioned.v1") || !slices.Contains(events, "dev.passage.identity.user.provisioned.v1") {
			t.Errorf("events = %v", events)
		}

		again := len(audit)
		if rec := do(http.MethodDelete, "/scim/v2/Users/"+created.ID, nil); rec.Code != http.StatusNoContent || rec.Body.Len() != 0 {
			t.Fatalf("repeat delete = %d %s", rec.Code, rec.Body.String())
		}
		audit, _ = f.auditAndEvents(t, f.tenantA)
		if len(audit) != again {
			t.Errorf("a repeated delete wrote %d more audit rows", len(audit)-again)
		}

		react := decode[scimUserBody](t, do(http.MethodPatch, "/scim/v2/Users/"+created.ID, patchBody(map[string]any{"op": "replace", "value": map[string]any{"active": true}})), http.StatusOK)
		if !react.Active {
			t.Fatal("not reactivated")
		}
		audit, _ = f.auditAndEvents(t, f.tenantA)
		if !slices.Contains(audit, "user.reactivated|"+scimClient) {
			t.Errorf("audit %v lacks the reactivation", audit)
		}
		var reopened int
		if err := f.admin.QueryRow(ctx, `select count(*) from identity_role_assignments where user_id = $1 and valid_to is null`, created.ID).Scan(&reopened); err != nil || reopened != 0 {
			t.Fatalf("reactivation must not restore access: %d %v", reopened, err)
		}
	})

	t.Run("DELETE deprovisions and answers 204", func(t *testing.T) {
		u := f.createUser(t, do, "carol")
		if rec := do(http.MethodDelete, "/scim/v2/Users/"+u.ID, nil); rec.Code != http.StatusNoContent {
			t.Fatalf("delete = %d", rec.Code)
		}
		if got := decode[scimUserBody](t, do(http.MethodGet, "/scim/v2/Users/"+u.ID, nil), http.StatusOK); got.Active {
			t.Fatal("deleted user is still active")
		}
	})
}

func countOf(values []string, want string) int {
	n := 0
	for _, v := range values {
		if v == want {
			n++
		}
	}
	return n
}

func TestSCIMGroups(t *testing.T) {
	f := newSCIMFixture(t)
	do := f.as(f.tenantA)
	ctx := context.Background()
	ada := f.createUser(t, do, "ada")
	bob := f.createUser(t, do, "bob")
	cy := f.createUser(t, do, "cy")
	var rolesBefore int
	if err := f.admin.QueryRow(ctx, `select count(*) from identity_role_assignments`).Scan(&rolesBefore); err != nil {
		t.Fatal(err)
	}
	member := func(ids ...string) []map[string]string {
		out := []map[string]string{}
		for _, id := range ids {
			out = append(out, map[string]string{"value": id})
		}
		return out
	}
	ids := func(g scimGroupBody) []string {
		out := []string{}
		for _, m := range g.Members {
			out = append(out, m.Value)
		}
		slices.Sort(out)
		return out
	}

	g := decode[scimGroupBody](t, do(http.MethodPost, "/scim/v2/Groups", map[string]any{"displayName": "Engineering", "members": member(ada.ID, bob.ID)}), http.StatusCreated)
	if want := []string{ada.ID, bob.ID}; !slices.Equal(ids(g), slices.Sorted(slices.Values(want))) {
		t.Fatalf("members = %v", ids(g))
	}

	t.Run("uniqueness and member validation", func(t *testing.T) {
		wantSCIMError(t, do(http.MethodPost, "/scim/v2/Groups", map[string]any{"displayName": "engineering"}), "409", "uniqueness")
		wantSCIMError(t, do(http.MethodPost, "/scim/v2/Groups", map[string]any{"displayName": "Ghosts", "members": member(userID)}), "400", "invalidValue")
		wantSCIMError(t, do(http.MethodPost, "/scim/v2/Groups", map[string]any{"displayName": "Bad", "members": member("nope")}), "400", "invalidValue")
		wantSCIMError(t, do(http.MethodPost, "/scim/v2/Groups", map[string]any{"members": member(ada.ID)}), "400", "invalidValue")
		wantSCIMError(t, do(http.MethodPatch, "/scim/v2/Groups/"+g.ID, patchBody(map[string]any{"op": "replace", "path": "owner", "value": "x"})), "400", "invalidPath")
		wantSCIMError(t, do(http.MethodGet, "/scim/v2/Groups/"+userID, nil), "404", "")
		var n int
		if err := f.admin.QueryRow(ctx, `select count(*) from identity_scim_groups where display_name = 'Ghosts'`).Scan(&n); err != nil || n != 0 {
			t.Fatalf("a rejected create left %d rows: %v", n, err)
		}
	})

	t.Run("users report their groups", func(t *testing.T) {
		got := decode[scimUserBody](t, do(http.MethodGet, "/scim/v2/Users/"+ada.ID, nil), http.StatusOK)
		if len(got.Groups) != 1 || got.Groups[0].Value != g.ID || got.Groups[0].Display != "Engineering" {
			t.Fatalf("groups = %+v", got.Groups)
		}
	})

	t.Run("PATCH adds, removes by filter path and renames", func(t *testing.T) {
		got := decode[scimGroupBody](t, do(http.MethodPatch, "/scim/v2/Groups/"+g.ID, patchBody(
			map[string]any{"op": "add", "path": "members", "value": member(cy.ID)},
			map[string]any{"op": "remove", "path": `members[value eq "` + bob.ID + `"]`},
			map[string]any{"op": "replace", "path": "displayName", "value": "Platform"},
		)), http.StatusOK)
		if got.DisplayName != "Platform" || !slices.Equal(ids(got), slices.Sorted(slices.Values([]string{ada.ID, cy.ID}))) {
			t.Fatalf("patched = %+v", got)
		}
		cleared := decode[scimGroupBody](t, do(http.MethodPatch, "/scim/v2/Groups/"+g.ID, patchBody(map[string]any{"op": "remove", "path": "members"})), http.StatusOK)
		if len(cleared.Members) != 0 {
			t.Fatalf("members = %+v", cleared.Members)
		}
	})

	t.Run("PUT replaces members and an unchanged PUT writes no audit row", func(t *testing.T) {
		put := func() scimGroupBody {
			return decode[scimGroupBody](t, do(http.MethodPut, "/scim/v2/Groups/"+g.ID, map[string]any{"displayName": "Platform", "members": member(bob.ID)}), http.StatusOK)
		}
		if got := put(); !slices.Equal(ids(got), []string{bob.ID}) {
			t.Fatalf("put = %+v", got)
		}
		audit, _ := f.auditAndEvents(t, f.tenantA)
		put()
		after, _ := f.auditAndEvents(t, f.tenantA)
		if len(after) != len(audit) {
			t.Errorf("an unchanged PUT wrote %d audit rows", len(after)-len(audit))
		}
		if !slices.Contains(audit, "scimgroup.created|"+scimClient) || !slices.Contains(audit, "scimgroup.updated|"+scimClient) {
			t.Errorf("audit = %v", audit)
		}
	})

	t.Run("list, filter and tenant isolation", func(t *testing.T) {
		list := decode[scimList[scimGroupBody]](t, do(http.MethodGet, "/scim/v2/Groups?filter="+url.QueryEscape(`displayName eq "PLATFORM"`), nil), http.StatusOK)
		if list.TotalResults != 1 || list.Resources[0].ID != g.ID {
			t.Fatalf("list = %+v", list)
		}
		other := decode[scimList[scimGroupBody]](t, f.as(f.tenantB)(http.MethodGet, "/scim/v2/Groups", nil), http.StatusOK)
		if other.TotalResults != 0 {
			t.Fatalf("tenant B sees %+v", other)
		}
		wantSCIMError(t, f.as(f.tenantB)(http.MethodDelete, "/scim/v2/Groups/"+g.ID, nil), "404", "")
	})

	t.Run("DELETE removes the group and its memberships but not the users", func(t *testing.T) {
		if rec := do(http.MethodDelete, "/scim/v2/Groups/"+g.ID, nil); rec.Code != http.StatusNoContent {
			t.Fatalf("delete = %d", rec.Code)
		}
		wantSCIMError(t, do(http.MethodGet, "/scim/v2/Groups/"+g.ID, nil), "404", "")
		var members int
		if err := f.admin.QueryRow(ctx, `select count(*) from identity_scim_group_members`).Scan(&members); err != nil || members != 0 {
			t.Fatalf("members left = %d %v", members, err)
		}
		if got := decode[scimUserBody](t, do(http.MethodGet, "/scim/v2/Users/"+ada.ID, nil), http.StatusOK); len(got.Groups) != 0 {
			t.Fatalf("groups = %+v", got.Groups)
		}
	})

	t.Run("groups never grant roles", func(t *testing.T) {
		var rolesAfter int
		if err := f.admin.QueryRow(ctx, `select count(*) from identity_role_assignments`).Scan(&rolesAfter); err != nil || rolesAfter != rolesBefore {
			t.Fatalf("role assignments %d -> %d (%v)", rolesBefore, rolesAfter, err)
		}
	})
}

func TestSCIMDiscovery(t *testing.T) {
	f := newSCIMFixture(t)
	do := f.as(f.tenantA)
	cfg := decode[map[string]any](t, do(http.MethodGet, "/scim/v2/ServiceProviderConfig", nil), http.StatusOK)
	if cfg["patch"].(map[string]any)["supported"] != true || cfg["bulk"].(map[string]any)["supported"] != false {
		t.Fatalf("config = %v", cfg)
	}
	schemas := decode[scimList[struct {
		ID string `json:"id"`
	}]](t, do(http.MethodGet, "/scim/v2/Schemas", nil), http.StatusOK)
	if schemas.TotalResults != 2 {
		t.Fatalf("schemas = %+v", schemas)
	}
	one := decode[struct{ ID, Name string }](t, do(http.MethodGet, "/scim/v2/Schemas/urn:ietf:params:scim:schemas:core:2.0:Group", nil), http.StatusOK)
	if one.Name != "Group" {
		t.Fatalf("schema = %+v", one)
	}
	types := decode[scimList[struct {
		ID, Endpoint string
	}]](t, do(http.MethodGet, "/scim/v2/ResourceTypes", nil), http.StatusOK)
	if types.TotalResults != 2 || types.Resources[0].Endpoint != "/Users" {
		t.Fatalf("types = %+v", types)
	}
	wantSCIMError(t, do(http.MethodGet, "/scim/v2/ResourceTypes/Nope", nil), "404", "")
}
