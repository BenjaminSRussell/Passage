package e2e_test

import (
	"encoding/json"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/BenjaminSRussell/passage/tools/e2e/devenv"
	"github.com/BenjaminSRussell/passage/tools/e2e/stack"
)

// passwordToken signs the user in against the running Keycloak with the dev realm's password grant.
func passwordToken(t *testing.T, issuer string, u devenv.User) string {
	t.Helper()
	form := url.Values{"grant_type": {"password"}, "client_id": {devenv.WebClientID}, "username": {u.Username}, "password": {u.Password}, "scope": {"openid"}}
	resp, err := http.Post(issuer+"/protocol/openid-connect/token", "application/x-www-form-urlencoded", strings.NewReader(form.Encode()))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	var out struct {
		AccessToken string `json:"access_token"`
		Error       string `json:"error_description"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil || out.AccessToken == "" {
		t.Fatalf("keycloak token for %s: status %d %q %v", u.Username, resp.StatusCode, out.Error, err)
	}
	return out.AccessToken
}

func getenv(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

// TestLiveStackFlow runs the vendor to ledger flow against the stack `make dev` started: real Keycloak tokens for the
// seeded users, the dev gateway, OPA and the real services. `make e2e-local` runs it.
func TestLiveStackFlow(t *testing.T) {
	if os.Getenv("PASSAGE_E2E_LIVE") == "" {
		t.Skip("set PASSAGE_E2E_LIVE=1 with `make dev` running (or use `make e2e-local`)")
	}
	root := repoRoot(t)
	seed, err := devenv.LoadSeed(filepath.Join(root, "deploy", "local", "seed.json"))
	if err != nil {
		t.Fatal(err)
	}
	gateway := getenv("PASSAGE_E2E_GATEWAY_URL", "http://127.0.0.1:8088")
	issuer := getenv("PASSAGE_E2E_ISSUER", "http://localhost:8080/realms/"+devenv.RealmName)
	ledgerID := os.Getenv("PASSAGE_E2E_LEDGER_ID")
	if ledgerID == "" {
		raw, err := os.ReadFile(filepath.Join(root, ".run", "dev", "ledger-id"))
		if err != nil {
			t.Fatalf("no ledger id: is `make dev` running? %v", err)
		}
		ledgerID = strings.TrimSpace(string(raw))
	}
	admin, _ := seed.User("admin")
	clerk, _ := seed.User("clerk")
	api := func(u devenv.User) stack.Client {
		return stack.Client{Base: gateway + "/api", Headers: map[string]string{"Authorization": "Bearer " + passwordToken(t, issuer, u)}}
	}
	a, c := api(admin), api(clerk)
	runFlow(t, flowClients{party: a, purchasing: a, approving: a, receiving: a, clerkReceiving: c, ledger: a, approverID: admin.Sub, ledgerID: ledgerID}, "")
}
