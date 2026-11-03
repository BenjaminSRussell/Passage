package bankconnect

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/json"
	"encoding/pem"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"golang.org/x/crypto/ssh"
)

func marshalClientKey(t *testing.T) (string, ssh.Signer) {
	t.Helper()
	_, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	block, err := ssh.MarshalPrivateKey(priv, "")
	if err != nil {
		t.Fatal(err)
	}
	signer, err := ssh.NewSignerFromKey(priv)
	if err != nil {
		t.Fatal(err)
	}
	return string(pem.EncodeToMemory(block)), signer
}

type kvServer struct {
	srv        *httptest.Server
	loginCalls int
}

func newKVServer(t *testing.T, field, value string) *kvServer {
	t.Helper()
	k := &kvServer{}
	k.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/v1/auth/kubernetes/login":
			var in map[string]string
			_ = json.NewDecoder(r.Body).Decode(&in)
			if in["role"] != "treasury" || in["jwt"] != "sa-jwt" {
				w.WriteHeader(http.StatusForbidden)
				return
			}
			k.loginCalls++
			_ = json.NewEncoder(w).Encode(map[string]any{"auth": map[string]any{"client_token": "s.k8s"}})
		case r.Method == http.MethodGet && r.URL.Path == "/v1/secret/data/treasury/sftp":
			if tok := r.Header.Get("X-Vault-Token"); tok != "s.k8s" && tok != "s.static" {
				w.WriteHeader(http.StatusForbidden)
				return
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"data": map[string]any{"data": map[string]string{field: value}}})
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(k.srv.Close)
	return k
}

func env(m map[string]string) func(string) string { return func(k string) string { return m[k] } }

func TestLoadVaultConfig(t *testing.T) {
	off, err := LoadVaultConfig(env(nil))
	if err != nil || off.Enabled() {
		t.Fatalf("no VAULT_ADDR must switch it off: %+v, %v", off, err)
	}
	ok, err := LoadVaultConfig(env(map[string]string{"VAULT_ADDR": "https://vault.internal:8200", "VAULT_K8S_ROLE": "treasury", "TREASURY_SFTP_KEY_PATH": "/secret/treasury/sftp", "TREASURY_SIGNING_KEY_NAME": "bank-a"}))
	if err != nil || ok.K8sMount != "kubernetes" || ok.SFTPKeyField != "private_key" || ok.SFTPKeyPath != "secret/treasury/sftp" || ok.SigningKeyName != "bank-a" {
		t.Fatalf("defaults: %+v, %v", ok, err)
	}
	for name, m := range map[string]map[string]string{
		"settings without an address": {"VAULT_TOKEN": "t"},
		"no credential":               {"VAULT_ADDR": "https://v"},
		"both credentials":            {"VAULT_ADDR": "https://v", "VAULT_TOKEN": "t", "VAULT_K8S_ROLE": "r"},
		"plain http to a remote host": {"VAULT_ADDR": "http://vault.internal", "VAULT_TOKEN": "t"},
		"key path without a mount":    {"VAULT_ADDR": "https://v", "VAULT_TOKEN": "t", "TREASURY_SFTP_KEY_PATH": "sftp"},
	} {
		if _, err := LoadVaultConfig(env(m)); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
}

func TestSFTPSigner_LoadsTheKeyFromVaultKVIntoMemory(t *testing.T) {
	pemKey, want := marshalClientKey(t)
	kv := newKVServer(t, "private_key", pemKey)
	jwt := filepath.Join(t.TempDir(), "jwt")
	if err := os.WriteFile(jwt, []byte("sa-jwt\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	cases := map[string]VaultConfig{
		"static token": {Addr: kv.srv.URL, Token: "s.static", SFTPKeyPath: "secret/treasury/sftp", SFTPKeyField: "private_key"},
		"k8s role":     {Addr: kv.srv.URL, K8sRole: "treasury", K8sMount: "kubernetes", K8sJWTPath: jwt, SFTPKeyPath: "secret/treasury/sftp", SFTPKeyField: "private_key"},
	}
	for name, cfg := range cases {
		got, err := cfg.SFTPSigner(context.Background())
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if string(got.PublicKey().Marshal()) != string(want.PublicKey().Marshal()) {
			t.Fatalf("%s: loaded a different key", name)
		}
	}
	if kv.loginCalls != 1 {
		t.Fatalf("k8s logins = %d; want 1", kv.loginCalls)
	}
}

func TestSFTPSigner_FailuresNameNoKeyMaterial(t *testing.T) {
	pemKey, _ := marshalClientKey(t)
	kv := newKVServer(t, "private_key", pemKey)
	base := VaultConfig{Addr: kv.srv.URL, Token: "s.static", SFTPKeyPath: "secret/treasury/sftp", SFTPKeyField: "private_key"}
	wrongToken, wrongField, wrongPath := base, base, base
	wrongToken.Token, wrongField.SFTPKeyField, wrongPath.SFTPKeyPath = "s.bad", "other", "secret/treasury/none"
	for name, cfg := range map[string]VaultConfig{"token refused": wrongToken, "missing field": wrongField, "missing path": wrongPath, "unconfigured": {}} {
		if _, err := cfg.SFTPSigner(context.Background()); err == nil {
			t.Errorf("%s: no error", name)
		} else if strings.Contains(err.Error(), "PRIVATE KEY") {
			t.Errorf("%s: error carries key material", name)
		}
	}
	garbage := newKVServer(t, "private_key", "-----BEGIN OPENSSH PRIVATE KEY-----\nnot-a-key\n")
	cfg := VaultConfig{Addr: garbage.srv.URL, Token: "s.static", SFTPKeyPath: "secret/treasury/sftp", SFTPKeyField: "private_key"}
	if _, err := cfg.SFTPSigner(context.Background()); err == nil || strings.Contains(err.Error(), "not-a-key") {
		t.Fatalf("unparseable key: %v", err)
	}
}
