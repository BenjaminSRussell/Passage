package identity_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/BenjaminSRussell/passage/services/identity"
)

const (
	fakeClientID = "passage-identity-deprovision"
	fakeSecret   = "fake-secret-value-for-tests"
	kcUserID     = "40000000-0000-0000-0000-000000000001"
	testTenant   = "00000000-0000-0000-0000-000000000001"
)

// fakeKeycloak is the slice of Keycloak's token endpoint and Admin REST API the deprovisioning worker uses:
// client-credentials tokens per realm, user lookup by id and by exact username, full-representation PUT, logout.
type fakeKeycloak struct {
	t   *testing.T
	srv *httptest.Server

	mu       sync.Mutex
	users    map[string]map[string]map[string]any // realm -> user id -> representation
	tokens   map[string]string                    // bearer -> realm
	issued   int
	calls    []string
	puts     []map[string]any
	logouts  []string
	status   int // when set, every admin call answers it
	failures int // how many admin calls answer status before it clears; 0 means all of them
	expire   bool
	gate     chan struct{} // when set, each admin call sends on it and then waits for a receive-side send
}

func newFakeKeycloak(t *testing.T) *fakeKeycloak {
	t.Helper()
	f := &fakeKeycloak{t: t, users: map[string]map[string]map[string]any{}, tokens: map[string]string{}}
	f.srv = httptest.NewServer(http.HandlerFunc(f.serve))
	t.Cleanup(f.srv.Close)
	return f
}

func (f *fakeKeycloak) addUser(realm, id, username, tenantID string, enabled bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.users[realm] == nil {
		f.users[realm] = map[string]map[string]any{}
	}
	attributes := map[string]any{"duties": []any{"party.vendor.view"}}
	if tenantID != "" {
		attributes["tenant_id"] = []any{tenantID}
	}
	f.users[realm][id] = map[string]any{"id": id, "username": username, "enabled": enabled, "firstName": "Kc", "attributes": attributes}
}

func (f *fakeKeycloak) user(realm, id string) map[string]any {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.users[realm][id]
}

func (f *fakeKeycloak) fail(status, times int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.status, f.failures = status, times
}

func (f *fakeKeycloak) callLog() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return slices.Clone(f.calls)
}

func (f *fakeKeycloak) serve(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	gate := f.gate
	f.mu.Unlock()
	if gate != nil && strings.HasPrefix(r.URL.Path, "/admin/") {
		gate <- struct{}{} // announce the call, then wait to be let through
		<-gate
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	path := r.URL.Path
	if realm, ok := strings.CutSuffix(strings.TrimPrefix(path, "/realms/"), "/protocol/openid-connect/token"); ok && r.Method == http.MethodPost {
		id, secret, basic := r.BasicAuth()
		if err := r.ParseForm(); err != nil || !basic || id != fakeClientID || secret != fakeSecret || r.PostForm.Get("grant_type") != "client_credentials" {
			w.WriteHeader(http.StatusUnauthorized)
			_, _ = w.Write([]byte(`{"error":"invalid_client"}`))
			return
		}
		f.issued++
		token := fmt.Sprintf("token-%s-%d", realm, f.issued)
		f.tokens[token] = realm
		_ = json.NewEncoder(w).Encode(map[string]any{"access_token": token, "expires_in": 300, "token_type": "Bearer"})
		return
	}
	rest, ok := strings.CutPrefix(path, "/admin/realms/")
	if !ok {
		http.NotFound(w, r)
		return
	}
	realm, rest, _ := strings.Cut(rest, "/")
	f.calls = append(f.calls, r.Method+" "+realm+"/"+rest)
	bearer := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
	if f.tokens[bearer] != realm || f.expire {
		f.expire = false
		w.WriteHeader(http.StatusUnauthorized)
		return
	}
	if f.status != 0 {
		status := f.status
		if f.failures > 0 {
			if f.failures--; f.failures == 0 {
				f.status = 0
			}
		}
		w.WriteHeader(status)
		_, _ = w.Write([]byte(`{"error":"unknown_error","errorMessage":"Keycloak could not do it"}`))
		return
	}
	users := f.users[realm]
	parts := strings.Split(rest, "/")
	switch {
	case r.Method == http.MethodGet && rest == "users":
		var found []map[string]any
		for _, u := range users {
			if r.URL.Query().Get("exact") == "true" && strings.EqualFold(u["username"].(string), r.URL.Query().Get("username")) {
				found = append(found, u)
			}
		}
		_ = json.NewEncoder(w).Encode(append([]map[string]any{}, found...))
	case len(parts) == 2 && parts[0] == "users" && r.Method == http.MethodGet:
		if u, ok := users[parts[1]]; ok {
			_ = json.NewEncoder(w).Encode(u)
			return
		}
		w.WriteHeader(http.StatusNotFound)
	case len(parts) == 2 && parts[0] == "users" && r.Method == http.MethodPut:
		if _, ok := users[parts[1]]; !ok {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		var rep map[string]any
		if err := json.NewDecoder(r.Body).Decode(&rep); err != nil {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		f.puts = append(f.puts, rep)
		users[parts[1]] = rep
		w.WriteHeader(http.StatusNoContent)
	case len(parts) == 3 && parts[0] == "users" && parts[2] == "logout" && r.Method == http.MethodPost:
		if _, ok := users[parts[1]]; !ok {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		f.logouts = append(f.logouts, realm+"/"+parts[1])
		w.WriteHeader(http.StatusNoContent)
	default:
		http.NotFound(w, r)
	}
}

func secretsDir(t *testing.T, realms ...string) string {
	t.Helper()
	dir := t.TempDir()
	for _, realm := range realms {
		if err := os.WriteFile(filepath.Join(dir, realm), []byte(fakeSecret+"\n"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

func (f *fakeKeycloak) admin(t *testing.T, realms ...string) *identity.KeycloakAdmin {
	t.Helper()
	k, err := identity.NewKeycloakAdmin(f.srv.URL, fakeClientID, identity.SecretsDir(secretsDir(t, realms...)), f.srv.Client())
	if err != nil {
		t.Fatal(err)
	}
	return k
}

func TestKeycloakAdminDisablesUserKeepsItsRepresentationAndLogsItOut(t *testing.T) {
	kc := newFakeKeycloak(t)
	kc.addUser("acme", kcUserID, "alice", testTenant, true)
	k := kc.admin(t, "acme")
	outcome, err := k.DeprovisionUser(context.Background(), "acme", identity.KeycloakTarget{TenantID: testTenant, Subject: kcUserID, UserName: "alice"})
	if err != nil || outcome != (identity.KeycloakOutcome{Result: "disabled", UserID: kcUserID}) {
		t.Fatalf("outcome = %+v, %v", outcome, err)
	}
	if want := []string{"GET acme/users/" + kcUserID, "PUT acme/users/" + kcUserID, "POST acme/users/" + kcUserID + "/logout"}; !slices.Equal(kc.callLog(), want) {
		t.Fatalf("calls = %v, want %v", kc.callLog(), want)
	}
	user := kc.user("acme", kcUserID)
	attributes, _ := user["attributes"].(map[string]any)
	if user["enabled"] != false || user["firstName"] != "Kc" || attributes["tenant_id"] == nil || attributes["duties"] == nil {
		t.Fatalf("PUT must send the whole representation with enabled=false, Keycloak now holds %v", user)
	}

	// Idempotent: a repeat finds the user disabled, skips the PUT and logs out again.
	if again, err := k.DeprovisionUser(context.Background(), "acme", identity.KeycloakTarget{TenantID: testTenant, Subject: kcUserID}); err != nil || again.Result != "disabled" {
		t.Fatalf("repeat = %+v, %v", again, err)
	}
	if len(kc.puts) != 1 || len(kc.logouts) != 2 || kc.issued != 1 {
		t.Fatalf("puts %d logouts %d tokens %d, want 1, 2 and one cached token", len(kc.puts), len(kc.logouts), kc.issued)
	}
}

func TestKeycloakAdminFindsSCIMPlaceholderUsersByUsername(t *testing.T) {
	kc := newFakeKeycloak(t)
	kc.addUser("acme", kcUserID, "bob", "", true)
	k := kc.admin(t, "acme")
	outcome, err := k.DeprovisionUser(context.Background(), "acme", identity.KeycloakTarget{TenantID: testTenant, Subject: "scim:" + userID, UserName: "Bob"})
	if err != nil || outcome.Result != "disabled" || outcome.UserID != kcUserID {
		t.Fatalf("outcome = %+v, %v", outcome, err)
	}
	if calls := kc.callLog(); calls[0] != "GET acme/users" || calls[1] != "PUT acme/users/"+kcUserID {
		t.Fatalf("calls = %v, want the username search then the PUT of the match", calls)
	}
	if none, err := k.DeprovisionUser(context.Background(), "acme", identity.KeycloakTarget{TenantID: testTenant, Subject: "scim:" + userID, UserName: "nobody"}); err != nil || none.Result != "not_found" {
		t.Fatalf("unknown username = %+v, %v; want not_found", none, err)
	}
	if _, err := k.DeprovisionUser(context.Background(), "acme", identity.KeycloakTarget{TenantID: testTenant, Subject: "scim:" + userID}); !permanent(err) {
		t.Fatalf("no subject and no username = %v, want a permanent error", err)
	}
	kc.addUser("acme", "40000000-0000-0000-0000-000000000002", "BOB", "", true)
	if _, err := k.DeprovisionUser(context.Background(), "acme", identity.KeycloakTarget{TenantID: testTenant, Subject: "scim:" + userID, UserName: "bob"}); !permanent(err) {
		t.Fatalf("two users answering the username = %v, want a permanent error", err)
	}
}

func permanent(err error) bool {
	var kcErr *identity.KeycloakError
	return errors.As(err, &kcErr) && kcErr.Permanent
}

func TestKeycloakAdminTreatsAMissingUserAsDone(t *testing.T) {
	kc := newFakeKeycloak(t)
	k := kc.admin(t, "acme")
	outcome, err := k.DeprovisionUser(context.Background(), "acme", identity.KeycloakTarget{TenantID: testTenant, Subject: kcUserID})
	if err != nil || outcome.Result != "not_found" {
		t.Fatalf("outcome = %+v, %v; want not_found", outcome, err)
	}
}

func TestKeycloakAdminRefusesAUserOfAnotherTenant(t *testing.T) {
	kc := newFakeKeycloak(t)
	kc.addUser("acme", kcUserID, "alice", "00000000-0000-0000-0000-000000000777", true)
	_, err := kc.admin(t, "acme").DeprovisionUser(context.Background(), "acme", identity.KeycloakTarget{TenantID: testTenant, Subject: kcUserID})
	if !permanent(err) || len(kc.puts) != 0 || len(kc.logouts) != 0 {
		t.Fatalf("err = %v, puts %d, logouts %d; want a permanent refusal and no change", err, len(kc.puts), len(kc.logouts))
	}
}

func TestKeycloakAdminRefreshesARejectedTokenOnce(t *testing.T) {
	kc := newFakeKeycloak(t)
	kc.addUser("acme", kcUserID, "alice", testTenant, true)
	k := kc.admin(t, "acme")
	if _, err := k.DeprovisionUser(context.Background(), "acme", identity.KeycloakTarget{TenantID: testTenant, Subject: kcUserID}); err != nil {
		t.Fatal(err)
	}
	kc.expire = true
	if _, err := k.DeprovisionUser(context.Background(), "acme", identity.KeycloakTarget{TenantID: testTenant, Subject: kcUserID}); err != nil {
		t.Fatalf("after a revoked token: %v", err)
	}
	if kc.issued != 2 {
		t.Fatalf("tokens issued = %d, want 2 (one refresh after the 401)", kc.issued)
	}
}

func TestKeycloakAdminClassifiesFailuresAndKeepsSecretsOutOfErrors(t *testing.T) {
	for status, wantPermanent := range map[int]bool{http.StatusForbidden: true, http.StatusBadRequest: true, http.StatusServiceUnavailable: false, http.StatusInternalServerError: false, http.StatusTooManyRequests: false} {
		kc := newFakeKeycloak(t)
		kc.addUser("acme", kcUserID, "alice", testTenant, true)
		kc.fail(status, 0)
		_, err := kc.admin(t, "acme").DeprovisionUser(context.Background(), "acme", identity.KeycloakTarget{TenantID: testTenant, Subject: kcUserID})
		var kcErr *identity.KeycloakError
		if !errors.As(err, &kcErr) || kcErr.Status != status || kcErr.Permanent != wantPermanent {
			t.Errorf("status %d: err = %#v, want permanent=%v", status, err, wantPermanent)
		}
		if err != nil && strings.Contains(err.Error(), fakeSecret) {
			t.Errorf("status %d: error %q carries the client secret", status, err)
		}
	}

	kc := newFakeKeycloak(t)
	k, err := identity.NewKeycloakAdmin(kc.srv.URL, fakeClientID, identity.SecretsDir(t.TempDir()), kc.srv.Client())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := k.DeprovisionUser(context.Background(), "acme", identity.KeycloakTarget{TenantID: testTenant, Subject: kcUserID}); err == nil || permanent(err) || kc.issued != 0 {
		t.Fatalf("missing secret file = %v (tokens %d), want a retryable error before any token request", err, kc.issued)
	}
	wrong := t.TempDir()
	if err := os.WriteFile(filepath.Join(wrong, "acme"), []byte("not-the-secret"), 0o600); err != nil {
		t.Fatal(err)
	}
	k, _ = identity.NewKeycloakAdmin(kc.srv.URL, fakeClientID, identity.SecretsDir(wrong), kc.srv.Client())
	if _, err := k.DeprovisionUser(context.Background(), "acme", identity.KeycloakTarget{TenantID: testTenant, Subject: kcUserID}); err == nil || strings.Contains(err.Error(), "not-the-secret") {
		t.Fatalf("rejected secret = %v, want an error without the secret", err)
	}
}

func TestSecretsDirOnlyReadsRealmFiles(t *testing.T) {
	dir := secretsDir(t, "acme")
	read := identity.SecretsDir(dir)
	if secret, err := read("acme"); err != nil || secret != fakeSecret {
		t.Fatalf("acme = %q, %v", secret, err)
	}
	for _, realm := range []string{"../acme", "acme/../acme", "", "Acme", ".."} {
		if _, err := read(realm); err == nil {
			t.Errorf("realm %q was read", realm)
		}
	}
	if err := os.WriteFile(filepath.Join(dir, "empty"), []byte(" \n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := read("empty"); err == nil {
		t.Error("an empty secret file was accepted")
	}
}

func TestNewKeycloakAdminRequiresTLSAndARootURL(t *testing.T) {
	secret := identity.SecretsDir(t.TempDir())
	for _, raw := range []string{"http://keycloak.example.com", "https://keycloak.example.com/admin", "ftp://x", "https://user:pw@keycloak.example.com", "https://keycloak.example.com?x=1"} {
		if _, err := identity.NewKeycloakAdmin(raw, fakeClientID, secret, nil); err == nil {
			t.Errorf("%s accepted", raw)
		}
	}
	for _, raw := range []string{"https://keycloak.example.com", "https://keycloak.example.com/", "http://127.0.0.1:8080", "http://localhost:8080"} {
		if _, err := identity.NewKeycloakAdmin(raw, fakeClientID, secret, nil); err != nil {
			t.Errorf("%s refused: %v", raw, err)
		}
	}
	if _, err := identity.NewKeycloakAdmin("https://keycloak.example.com", "", secret, nil); err == nil {
		t.Error("empty client id accepted")
	}
}
