package azurekv

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/BenjaminSRussell/passage/platform/cloudprofile"
)

// fakeAzure serves a Key Vault key (real RSA-OAEP-256 wrapping) and an Entra token endpoint over TLS, and returns an
// http.Client that sends every request, whatever its host, to it, so the production URLs are exercised.
type fakeAzure struct {
	server    *httptest.Server
	client    *http.Client
	priv      *rsa.PrivateKey
	tokens    atomic.Int32
	wantScope string
	paths     []string
}

func newFakeAzure(t *testing.T) *fakeAzure {
	t.Helper()
	priv, err := rsa.GenerateKey(rand.Reader, 3072)
	if err != nil {
		t.Fatal(err)
	}
	f := &fakeAzure{priv: priv}
	// A plain handler, not a ServeMux: a mux would clean "//" (the empty key-version segment) and redirect.
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.paths = append(f.paths, r.Method+" "+r.Host+r.URL.Path)
		switch {
		case strings.HasSuffix(r.URL.Path, "/oauth2/v2.0/token"):
			_ = r.ParseForm()
			f.tokens.Add(1)
			f.wantScope = r.Form.Get("scope")
			if r.Form.Get("client_assertion") != "federated-jwt" || r.Form.Get("client_id") != "client-1" {
				http.Error(w, `{"error":"invalid_client"}`, http.StatusBadRequest)
				return
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"access_token": "tok-1", "expires_in": 3600})
		case strings.HasSuffix(r.URL.Path, "/wrapkey") || strings.HasSuffix(r.URL.Path, "/unwrapkey"):
			if r.Header.Get("Authorization") != "Bearer tok-1" || r.URL.Query().Get("api-version") == "" {
				http.Error(w, `{"error":{"code":"Unauthorized"}}`, http.StatusUnauthorized)
				return
			}
			var in operation
			_ = json.NewDecoder(r.Body).Decode(&in)
			if in.Alg != "RSA-OAEP-256" {
				http.Error(w, `{"error":{"code":"BadParameter"}}`, http.StatusBadRequest)
				return
			}
			data, _ := base64.RawURLEncoding.DecodeString(in.Value)
			var out []byte
			if strings.HasSuffix(r.URL.Path, "/wrapkey") {
				out, err = rsa.EncryptOAEP(sha256.New(), rand.Reader, &priv.PublicKey, data, nil)
			} else {
				out, err = rsa.DecryptOAEP(sha256.New(), nil, priv, data, nil)
			}
			if err != nil {
				http.Error(w, `{"error":{"code":"BadParameter"}}`, http.StatusBadRequest)
				return
			}
			_ = json.NewEncoder(w).Encode(map[string]string{"kid": "k", "value": base64.RawURLEncoding.EncodeToString(out)})
		default:
			http.NotFound(w, r)
		}
	})
	f.server = httptest.NewTLSServer(handler)
	t.Cleanup(f.server.Close)
	pool := x509.NewCertPool()
	pool.AddCert(f.server.Certificate())
	addr := f.server.Listener.Addr().String()
	f.client = &http.Client{Transport: &http.Transport{
		TLSClientConfig: &tls.Config{RootCAs: pool, InsecureSkipVerify: true}, //nolint:gosec // test: hostnames differ from the cert
		DialContext: func(ctx context.Context, network, _ string) (net.Conn, error) {
			return (&net.Dialer{}).DialContext(ctx, network, addr)
		},
	}}
	return f
}

func workloadEnv(t *testing.T) func(string) string {
	t.Helper()
	file := filepath.Join(t.TempDir(), "token")
	if err := os.WriteFile(file, []byte("federated-jwt\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	env := map[string]string{"AZURE_CLIENT_ID": "client-1", "AZURE_TENANT_ID": "tenant-1", "AZURE_FEDERATED_TOKEN_FILE": file}
	return func(k string) string { return env[k] }
}

func newClient(t *testing.T, f *fakeAzure, profile, keyID string) *Client {
	t.Helper()
	base, overlays := cloudprofile.ParseSelector(profile)
	p, err := cloudprofile.Resolve(base, overlays...)
	if err != nil {
		t.Fatal(err)
	}
	c, err := New(Config{KeyID: keyID, Profile: p, HTTP: f.client, Tokens: &WorkloadIdentity{Authority: p.Endpoint("entraAuthority"), HTTP: f.client, Getenv: workloadEnv(t)}})
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func TestWrapUnwrapRoundTripThroughKeyVaultWithWorkloadIdentity(t *testing.T) {
	f := newFakeAzure(t)
	c := newClient(t, f, "commercial", "https://kv-passage.vault.azure.net/keys/fieldcrypt-kek/0123abcd")
	master := make([]byte, 32)
	_, _ = rand.Read(master)
	wrapped, err := c.Wrap(context.Background(), master)
	if err != nil {
		t.Fatal(err)
	}
	if string(wrapped) == string(master) || len(wrapped) != 384 { // RSA-3072 OAEP block
		t.Fatalf("wrapped blob looks wrong (%d bytes)", len(wrapped))
	}
	got, err := c.Unwrap(context.Background(), wrapped)
	if err != nil || string(got) != string(master) {
		t.Fatalf("unwrap = %x, %v", got, err)
	}
	if f.wantScope != "https://vault.azure.net/.default" {
		t.Errorf("token scope %q must come from the profile's Key Vault suffix", f.wantScope)
	}
	if f.tokens.Load() != 1 {
		t.Errorf("token fetched %d times, want one (cached)", f.tokens.Load())
	}
	for _, want := range []string{"POST login.microsoftonline.com/tenant-1/oauth2/v2.0/token", "POST kv-passage.vault.azure.net/keys/fieldcrypt-kek/0123abcd/wrapkey", "POST kv-passage.vault.azure.net/keys/fieldcrypt-kek/0123abcd/unwrapkey"} {
		if !slices.Contains(f.paths, want) {
			t.Errorf("no request %q in %v", want, f.paths)
		}
	}
}

func TestGovCloudUsesTheGovAuthorityAndScope(t *testing.T) {
	f := newFakeAzure(t)
	c := newClient(t, f, "gcc-high", "https://kv.vault.usgovcloudapi.net/keys/kek")
	if _, err := c.Wrap(context.Background(), make([]byte, 32)); err != nil {
		t.Fatal(err)
	}
	if f.wantScope != "https://vault.usgovcloudapi.net/.default" {
		t.Errorf("scope = %q", f.wantScope)
	}
	if !strings.Contains(strings.Join(f.paths, " "), "login.microsoftonline.us/tenant-1/") {
		t.Errorf("token must come from the .us authority: %v", f.paths)
	}
	// Without a version the request carries an empty version segment: the latest key version.
	if !strings.Contains(strings.Join(f.paths, " "), "/keys/kek//wrapkey") {
		t.Errorf("paths = %v", f.paths)
	}
}

func TestVaultFromAnotherCloudOrANonVaultHostIsRefused(t *testing.T) {
	p, _ := cloudprofile.Resolve("gcc-high")
	for _, id := range []string{
		"https://kv.vault.azure.net/keys/kek",             // commercial vault under a gov profile
		"https://evil.example.com/keys/kek",               //
		"https://vault.usgovcloudapi.net/keys/kek",        // the bare suffix is not a vault
		"http://kv.vault.usgovcloudapi.net/keys/kek",      // not https
		"https://kv.vault.usgovcloudapi.net/secrets/x",    // not a key
		"https://kv.vault.usgovcloudapi.net/keys/a/b/c/d", // too deep
		"",
	} {
		if _, err := New(Config{KeyID: id, Profile: p}); err == nil {
			t.Errorf("New(%q) must fail", id)
		}
	}
	if _, err := New(Config{KeyID: "https://kv.vault.usgovcloudapi.net/keys/kek"}); err == nil {
		t.Error("a profile is required")
	}
}

func TestVaultRefusalAndBadTokenNeverLeakKeyMaterial(t *testing.T) {
	f := newFakeAzure(t)
	c := newClient(t, f, "commercial", "https://kv.vault.azure.net/keys/kek/v1")
	master := []byte("0123456789abcdef0123456789abcdef")
	if _, err := c.Unwrap(context.Background(), []byte("not a valid wrapped blob")); err == nil || strings.Contains(err.Error(), "0123456789") {
		t.Errorf("err = %v", err)
	}
	wrapped, _ := c.Wrap(context.Background(), master)
	c.algorithm = "RSA1_5"
	if _, err := c.Unwrap(context.Background(), wrapped); err == nil || !strings.Contains(err.Error(), "BadParameter") {
		t.Errorf("a vault refusal must surface its code: %v", err)
	}
	bad := &WorkloadIdentity{Authority: "https://login.microsoftonline.com", HTTP: f.client, Getenv: func(k string) string {
		return map[string]string{"AZURE_CLIENT_ID": "wrong", "AZURE_TENANT_ID": "t", "AZURE_FEDERATED_TOKEN_FILE": filepath.Join(t.TempDir(), "missing")}[k]
	}}
	if _, err := bad.Token(context.Background(), "s"); err == nil {
		t.Error("a missing federated token file must fail")
	}
}

func TestWorkloadIdentityRefusesAnAuthorityOfAnotherCloudAndMissingSetup(t *testing.T) {
	w := &WorkloadIdentity{Authority: "https://login.microsoftonline.us", Getenv: func(k string) string {
		return map[string]string{"AZURE_CLIENT_ID": "c", "AZURE_TENANT_ID": "t", "AZURE_FEDERATED_TOKEN_FILE": "/x", "AZURE_AUTHORITY_HOST": "https://login.microsoftonline.com/"}[k]
	}}
	if _, err := w.Token(context.Background(), "s"); err == nil || !strings.Contains(err.Error(), "not the cloud profile's authority") {
		t.Errorf("err = %v", err)
	}
	empty := &WorkloadIdentity{Authority: "https://login.microsoftonline.com", Getenv: func(string) string { return "" }}
	if _, err := empty.Token(context.Background(), "s"); err == nil || !strings.Contains(err.Error(), "workload identity") {
		t.Errorf("err = %v", err)
	}
}

func TestFromEnvNeedsAKeyInTheProfilesVault(t *testing.T) {
	env := func(m map[string]string) func(string) string { return func(k string) string { return m[k] } }
	if _, err := FromEnv(env(map[string]string{})); err == nil {
		t.Error("no key id must fail")
	}
	if _, err := FromEnv(env(map[string]string{EnvKeyID: "https://kv.vault.azure.net/keys/kek", "PASSAGE_CLOUD_PROFILE": "gcc-high"})); err == nil {
		t.Error("a commercial vault under gcc-high must fail")
	}
	if _, err := FromEnv(env(map[string]string{EnvKeyID: "https://kv.vault.azure.net/keys/kek"})); err != nil {
		t.Errorf("commercial default: %v", err)
	}
}
