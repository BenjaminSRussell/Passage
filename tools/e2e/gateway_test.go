package e2e_test

import (
	"context"
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"math/big"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/BenjaminSRussell/passage/tools/e2e/devenv"
	"github.com/BenjaminSRussell/passage/tools/e2e/stack"
)

const gatewayIssuer = "http://127.0.0.1:1/realms/passage"

type tokenSigner struct{ key *rsa.PrivateKey }

func (s tokenSigner) jwks() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{"keys": []map[string]string{{
			"kty": "RSA", "kid": "e2e", "use": "sig", "alg": "RS256",
			"n": base64.RawURLEncoding.EncodeToString(s.key.N.Bytes()),
			"e": base64.RawURLEncoding.EncodeToString(big.NewInt(int64(s.key.E)).Bytes()),
		}}})
	})
}

func (s tokenSigner) token(t *testing.T, seed devenv.Seed, u devenv.User) string {
	t.Helper()
	now := time.Now()
	header, _ := json.Marshal(map[string]string{"alg": "RS256", "kid": "e2e"})
	claims, _ := json.Marshal(map[string]any{
		"iss": gatewayIssuer, "sub": u.Sub, "tid": seed.TenantID, "le": seed.LegalEntityID, "duties": u.DutyClaim(), "tier": u.Tier,
		"aud": []string{"passage-api"}, "iat": now.Unix(), "exp": now.Add(5 * time.Minute).Unix(),
	})
	signed := base64.RawURLEncoding.EncodeToString(header) + "." + base64.RawURLEncoding.EncodeToString(claims)
	digest := sha256.Sum256([]byte(signed))
	sig, err := rsa.SignPKCS1v15(rand.Reader, s.key, crypto.SHA256, digest[:])
	if err != nil {
		t.Fatal(err)
	}
	return signed + "." + base64.RawURLEncoding.EncodeToString(sig)
}

func freeAddr(t *testing.T) string {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = l.Close() }()
	return l.Addr().String()
}

func waitFor(t *testing.T, url string) {
	t.Helper()
	for deadline := time.Now().Add(30 * time.Second); time.Now().Before(deadline); time.Sleep(200 * time.Millisecond) {
		if resp, err := http.Get(url); err == nil {
			_ = resp.Body.Close()
			return
		}
	}
	t.Fatalf("%s did not answer", url)
}

// TestGatewayEnforcesSeededDutiesInFrontOfTheRealServices proves the login-to-API chain without Keycloak: signed
// tokens for the seeded users, the dev gateway, a real OPA evaluating policies/authz, and the real services behind it.
func TestGatewayEnforcesSeededDutiesInFrontOfTheRealServices(t *testing.T) {
	opa, err := exec.LookPath("opa")
	if err != nil {
		if os.Getenv("CI") != "" {
			t.Fatal("opa is required in CI")
		}
		t.Skip("opa is not installed")
	}
	root := repoRoot(t)
	seed, err := devenv.LoadSeed(filepath.Join(root, "deploy", "local", "seed.json"))
	if err != nil {
		t.Fatal(err)
	}
	s := startStack(t)

	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	signer := tokenSigner{key: key}
	jwks := httptest.NewServer(signer.jwks())
	t.Cleanup(jwks.Close)

	dir := t.TempDir()
	assignments, err := devenv.RenderAssignments(seed)
	if err != nil {
		t.Fatal(err)
	}
	assignmentsPath := filepath.Join(dir, "assignments.json")
	if err := os.WriteFile(assignmentsPath, assignments, 0o600); err != nil {
		t.Fatal(err)
	}
	opaAddr := freeAddr(t)
	if err := s.Spawn("opa", root, nil, opa, "run", "--server", "--addr", opaAddr,
		filepath.Join(root, "policies", "authz", "authz.rego"), filepath.Join(root, "policies", "authz", "data.json"), assignmentsPath); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "http://"+opaAddr+"/health")

	binary := filepath.Join(dir, "devgateway")
	build := exec.Command("go", "build", "-o", binary, "./cmd/devgateway")
	build.Dir = filepath.Join(root, "services", "identity")
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build devgateway: %v\n%s", err, out)
	}
	gatewayAddr := freeAddr(t)
	env := map[string]string{
		"DEVGATEWAY_ADDR": gatewayAddr, "DEVGATEWAY_ISSUER": gatewayIssuer, "DEVGATEWAY_JWKS_URL": jwks.URL,
		"DEVGATEWAY_TENANT_ID": seed.TenantID, "DEVGATEWAY_OPA_URL": "http://" + opaAddr,
		"PASSAGE_SVC_LEDGER_URL": s.URL("ledger"), "PASSAGE_SVC_NUMBERING_URL": s.URL("numbering"),
		"PASSAGE_SVC_PARTY_URL": s.URL("party"), "PASSAGE_SVC_PROCUREMENT_URL": s.URL("purchasing"), "PASSAGE_SVC_RECEIVING_URL": s.URL("receiving"),
	}
	if err := s.Spawn("devgateway", "", env, binary); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "http://"+gatewayAddr+"/healthz")

	adminUser, _ := seed.User("admin")
	clerkUser, _ := seed.User("clerk")
	api := func(u devenv.User, forged map[string]string) stack.Client {
		headers := map[string]string{}
		for k, v := range forged {
			headers[k] = v
		}
		if u.Sub != "" {
			headers["Authorization"] = "Bearer " + signer.token(t, seed, u)
		}
		return stack.Client{Base: "http://" + gatewayAddr + "/api", Headers: headers}
	}
	ctx := context.Background()

	t.Run("no token is 401 even with forged identity headers", func(t *testing.T) {
		resp, err := api(devenv.User{}, map[string]string{"X-Passage-Tenant": seed.TenantID, "X-Passage-User": adminUser.Sub, "X-Passage-Duties": adminUser.DutyClaim()}).
			Do(ctx, http.MethodGet, "/v1/numbering/sequences", nil, nil)
		if err != nil || resp.Status != http.StatusUnauthorized {
			t.Fatalf("status %d err %v, want 401", resp.Status, err)
		}
	})

	t.Run("a clerk is refused admin routes by OPA and told which duty is missing", func(t *testing.T) {
		resp, err := api(clerkUser, map[string]string{"X-Passage-Duties": adminUser.DutyClaim()}).Do(ctx, http.MethodGet, "/v1/numbering/sequences", nil, nil)
		if err != nil || resp.Status != http.StatusForbidden || resp.String("code") != "AUTH-0003" {
			t.Fatalf("status %d code %q err %v, want 403 AUTH-0003: %s", resp.Status, resp.String("code"), err, resp.Body)
		}
		if !strings.Contains(string(resp.Body), "duty_missing") {
			t.Errorf("denial names no missing duty: %s", resp.Body)
		}
		create, err := api(clerkUser, nil).Do(ctx, http.MethodPost, "/v1/numbering/sequences", map[string]any{"code": "X"}, nil)
		if err != nil || create.Status != http.StatusForbidden {
			t.Fatalf("clerk creating a sequence: status %d err %v, want 403", create.Status, err)
		}
	})

	t.Run("a clerk may still use what their duties allow", func(t *testing.T) {
		resp, err := api(clerkUser, nil).Do(ctx, http.MethodGet, "/v1/ledger/journal-lines", nil, nil)
		if err != nil || resp.Status/100 != 2 {
			t.Fatalf("status %d err %v: %s", resp.Status, err, resp.Body)
		}
	})

	t.Run("an admin reads a numbering sequence", func(t *testing.T) {
		admin := api(adminUser, nil)
		if resp, err := admin.Do(ctx, http.MethodGet, "/v1/numbering/sequences/PARTY", nil, nil); err != nil || resp.Status != http.StatusOK {
			t.Fatalf("admin listing sequences: status %d err %v: %s", resp.Status, err, resp.Body)
		}
	})

	t.Run("the seeded admin completes vendor to ledger entry through the gateway", func(t *testing.T) {
		admin, clerk := api(adminUser, nil), api(clerkUser, nil)
		runFlow(t, flowClients{
			party: admin, purchasing: admin, approving: admin, receiving: admin, clerkReceiving: clerk, ledger: admin,
			approverID: adminUser.Sub, ledgerID: s.LedgerID,
		}, s.URL("purchasing"))
	})
}
