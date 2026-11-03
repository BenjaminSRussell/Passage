package bankconnect

import (
	"context"
	"crypto"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"golang.org/x/crypto/ssh"
)

type transitServer struct {
	srv                     *httptest.Server
	mu                      sync.Mutex
	logins, renews, revokes int
	renewFails              bool
	ed                      ed25519.PrivateKey
	rsaKey                  *rsa.PrivateKey
	signedWith              []map[string]any
	lease                   int
}

func newTransitServer(t *testing.T) *transitServer {
	t.Helper()
	_, ed, _ := ed25519.GenerateKey(rand.Reader)
	rk, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	k := &transitServer{ed: ed, rsaKey: rk, lease: 900}
	k.srv = httptest.NewServer(http.HandlerFunc(k.serve))
	t.Cleanup(k.srv.Close)
	return k
}

func (k *transitServer) serve(w http.ResponseWriter, r *http.Request) {
	k.mu.Lock()
	defer k.mu.Unlock()
	reply := func(v any) { _ = json.NewEncoder(w).Encode(v) }
	auth := map[string]any{"auth": map[string]any{"client_token": "s.k8s", "lease_duration": k.lease}}
	switch {
	case r.URL.Path == "/v1/auth/kubernetes/login":
		var in map[string]string
		_ = json.NewDecoder(r.Body).Decode(&in)
		if in["role"] != "treasury" || in["jwt"] != "sa-jwt" {
			w.WriteHeader(http.StatusForbidden)
			return
		}
		k.logins++
		reply(auth)
		return
	case r.URL.Path == "/v1/auth/token/renew-self":
		if k.renewFails {
			w.WriteHeader(http.StatusForbidden)
			return
		}
		k.renews++
		reply(map[string]any{"auth": map[string]any{"lease_duration": k.lease}})
		return
	case r.URL.Path == "/v1/auth/token/revoke-self":
		k.revokes++
		w.WriteHeader(http.StatusNoContent)
		return
	}
	if tok := r.Header.Get("X-Vault-Token"); tok != "s.k8s" && tok != "s.static" {
		w.WriteHeader(http.StatusForbidden)
		return
	}
	switch r.URL.Path {
	case "/v1/transit/keys/sftp-ed":
		reply(map[string]any{"data": map[string]any{"type": "ed25519", "latest_version": 2, "keys": map[string]any{"2": map[string]string{"public_key": base64.StdEncoding.EncodeToString(k.ed.Public().(ed25519.PublicKey))}}}})
	case "/v1/transit/keys/sftp-rsa":
		der, _ := x509.MarshalPKIXPublicKey(&k.rsaKey.PublicKey)
		reply(map[string]any{"data": map[string]any{"type": "rsa-2048", "latest_version": 1, "keys": map[string]any{"1": map[string]string{"public_key": string(pem.EncodeToMemory(&pem.Block{Type: "PUBLIC KEY", Bytes: der}))}}}})
	case "/v1/transit/sign/sftp-ed", "/v1/transit/sign/sftp-rsa":
		var in map[string]any
		_ = json.NewDecoder(r.Body).Decode(&in)
		k.signedWith = append(k.signedWith, in)
		data, _ := base64.StdEncoding.DecodeString(in["input"].(string))
		var sig []byte
		if strings.HasSuffix(r.URL.Path, "ed") {
			sig = ed25519.Sign(k.ed, data)
		} else {
			h := sha256.Sum256(data)
			if in["hash_algorithm"] != "sha2-256" || in["signature_algorithm"] != "pkcs1v15" {
				w.WriteHeader(http.StatusBadRequest)
				return
			}
			sig, _ = rsa.SignPKCS1v15(rand.Reader, k.rsaKey, crypto.SHA256, h[:])
		}
		reply(map[string]any{"data": map[string]any{"signature": "vault:v2:" + base64.StdEncoding.EncodeToString(sig)}})
	default:
		w.WriteHeader(http.StatusNotFound)
	}
}

func env(m map[string]string) func(string) string { return func(k string) string { return m[k] } }

func k8sVault(t *testing.T, k *transitServer, mutate func(*VaultConfig)) (*Vault, *time.Time) {
	t.Helper()
	jwt := filepath.Join(t.TempDir(), "jwt")
	if err := os.WriteFile(jwt, []byte("sa-jwt\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg := VaultConfig{Addr: k.srv.URL, K8sRole: "treasury", K8sMount: "kubernetes", K8sJWTPath: jwt, SFTPKeyName: "sftp-ed", SigningKeyNames: []string{"bank-a"}}
	if mutate != nil {
		mutate(&cfg)
	}
	clock := time.Unix(1_000_000, 0)
	v := NewVault(cfg)
	v.now = func() time.Time { return clock }
	return v, &clock
}

func TestLoadVaultConfig(t *testing.T) {
	off, err := LoadVaultConfig(env(nil))
	if err != nil || off.Enabled() {
		t.Fatalf("no VAULT_ADDR must switch it off: %+v, %v", off, err)
	}
	if _, err := LoadVaultConfig(env(map[string]string{"TREASURY_BANK_CHANNELS": "sftp", "TREASURY_ENV": "dev"})); err != nil {
		t.Fatalf("dev may run a channel without vault: %v", err)
	}
	good := map[string]string{"VAULT_ADDR": "https://vault.internal:8200", "VAULT_K8S_ROLE": "treasury", "TREASURY_SFTP_KEY_NAME": "sftp-ed", "TREASURY_SIGNING_KEY_NAMES": "bank-a, bank-b", "TREASURY_BANK_CHANNELS": "sftp,ebics"}
	ok, err := LoadVaultConfig(env(good))
	if err != nil || ok.K8sMount != "kubernetes" || len(ok.SigningKeyNames) != 2 || ok.SigningKeyNames[1] != "bank-b" {
		t.Fatalf("config: %+v, %v", ok, err)
	}
	with := func(k, v string) map[string]string {
		m := map[string]string{}
		for a, b := range good {
			m[a] = b
		}
		m[k] = v
		return m
	}
	for name, m := range map[string]map[string]string{
		"settings without an address":       {"VAULT_TOKEN": "t"},
		"channel without vault outside dev": {"TREASURY_BANK_CHANNELS": "sftp"},
		"channel without vault in staging":  {"TREASURY_BANK_CHANNELS": "ebics", "TREASURY_ENV": "staging"},
		"no credential":                     {"VAULT_ADDR": "https://v"},
		"both credentials":                  with("VAULT_TOKEN", "t"),
		"plain http to a remote host":       with("VAULT_ADDR", "http://vault.internal"),
		"dotdot key name":                   with("TREASURY_SIGNING_KEY_NAMES", "../sys"),
		"slash in sftp key name":            with("TREASURY_SFTP_KEY_NAME", "a/b"),
		"escaped mount":                     with("VAULT_K8S_AUTH_MOUNT", "k8s%2f.."),
		"channel without signing keys":      with("TREASURY_SIGNING_KEY_NAMES", ""),
		"sftp channel without an sftp key":  with("TREASURY_SFTP_KEY_NAME", ""),
	} {
		if _, err := LoadVaultConfig(env(m)); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
}

func TestSFTPSigner_Ed25519SignaturesAreMadeByTransitAndVerify(t *testing.T) {
	k := newTransitServer(t)
	v, _ := k8sVault(t, k, nil)
	signer, err := v.SFTPSigner(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	data := []byte("session-hash")
	sig, err := signer.Sign(rand.Reader, data)
	if err != nil {
		t.Fatal(err)
	}
	if err := signer.PublicKey().Verify(data, sig); err != nil {
		t.Fatalf("signature does not verify against the transit public key: %v", err)
	}
	if got := k.signedWith[0]["key_version"]; got != float64(2) {
		t.Fatalf("signed with key_version %v; want the version whose public key was read", got)
	}
	if _, err := signer.(ssh.AlgorithmSigner).SignWithAlgorithm(rand.Reader, data, ssh.KeyAlgoRSASHA256); err == nil {
		t.Fatal("an ed25519 key must refuse an rsa algorithm")
	}
}

func TestSFTPSigner_RSAUsesPKCS1v15WithSHA256OrSHA512Only(t *testing.T) {
	k := newTransitServer(t)
	v, _ := k8sVault(t, k, func(c *VaultConfig) { c.SFTPKeyName = "sftp-rsa" })
	signer, err := v.SFTPSigner(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	as := signer.(ssh.AlgorithmSigner)
	data := []byte("session-hash")
	sig, err := as.SignWithAlgorithm(rand.Reader, data, ssh.KeyAlgoRSASHA256)
	if err != nil || sig.Format != ssh.KeyAlgoRSASHA256 {
		t.Fatalf("sig = %+v, %v", sig, err)
	}
	if err := signer.PublicKey().Verify(data, sig); err != nil {
		t.Fatalf("rsa-sha2-256 signature does not verify: %v", err)
	}
	if _, err := as.SignWithAlgorithm(rand.Reader, data, ssh.KeyAlgoRSA); err == nil {
		t.Fatal("sha1 ssh-rsa must be refused")
	}
}

func TestSFTPSigner_RefusesUnconfiguredAndUnsupportedKeys(t *testing.T) {
	k := newTransitServer(t)
	v, _ := k8sVault(t, k, func(c *VaultConfig) { c.SFTPKeyName = "missing" })
	if _, err := v.SFTPSigner(context.Background()); err == nil {
		t.Fatal("a missing transit key was accepted")
	}
	v, _ = k8sVault(t, k, func(c *VaultConfig) { c.SFTPKeyName = "" })
	if _, err := v.SFTPSigner(context.Background()); err == nil {
		t.Fatal("no key name was accepted")
	}
	if _, err := transitPublicKey("aes256-gcm96", "x"); err == nil {
		t.Fatal("a symmetric key was accepted")
	}
}

func TestVault_CachesRenewsAndRevokesTheKubernetesToken(t *testing.T) {
	k := newTransitServer(t)
	v, clock := k8sVault(t, k, nil)
	ctx := context.Background()
	for range 5 {
		if tok, err := v.Token(ctx); err != nil || tok != "s.k8s" {
			t.Fatalf("token = %q, %v", tok, err)
		}
	}
	if k.logins != 1 {
		t.Fatalf("logins = %d; want one for five calls", k.logins)
	}
	*clock = clock.Add(700 * time.Second)
	if _, err := v.Token(ctx); err != nil || k.renews != 1 || k.logins != 1 {
		t.Fatalf("after two thirds of the lease: renews %d logins %d, %v", k.renews, k.logins, err)
	}
	k.renewFails = true
	*clock = clock.Add(700 * time.Second)
	if _, err := v.Token(ctx); err != nil || k.logins != 2 {
		t.Fatalf("a failed renewal must log in again: logins %d, %v", k.logins, err)
	}
	if err := v.Close(ctx); err != nil || k.revokes != 1 {
		t.Fatalf("close: revokes %d, %v", k.revokes, err)
	}
	static := NewVault(VaultConfig{Addr: k.srv.URL, Token: "s.static"})
	_, _ = static.Token(ctx)
	if err := static.Close(ctx); err != nil || k.revokes != 1 {
		t.Fatalf("a static token must not be revoked: revokes %d, %v", k.revokes, err)
	}
}

func TestVault_SignerRefusesKeyNamesOffTheAllowList(t *testing.T) {
	k := newTransitServer(t)
	v, _ := k8sVault(t, k, nil)
	if _, err := v.Signer().Sign(context.Background(), "other-key", []byte("x")); err == nil || !strings.Contains(err.Error(), "not an allowed") {
		t.Fatalf("err = %v", err)
	}
	if k.logins != 0 {
		t.Fatal("a refused key name must not reach Vault")
	}
	for _, bad := range []string{"../sys", "a/b", "a%2fb", ""} {
		if _, err := (VaultTransit{Addr: k.srv.URL, Token: "s.static"}).Sign(context.Background(), bad, []byte("x")); err == nil {
			t.Errorf("key name %q accepted", bad)
		}
	}
}
