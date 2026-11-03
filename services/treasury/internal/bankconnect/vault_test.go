package bankconnect

import (
	"context"
	"crypto"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/sha512"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
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
	signForbidden           bool
	exportable              bool
	plaintextBackup         bool
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
	if k.signForbidden && strings.HasPrefix(r.URL.Path, "/v1/transit/sign/") {
		w.WriteHeader(http.StatusForbidden)
		return
	}
	keyData := func(typ string, version int, pub string) map[string]any {
		return map[string]any{"data": map[string]any{
			"type": typ, "latest_version": version, "exportable": k.exportable, "allow_plaintext_backup": k.plaintextBackup,
			"keys": map[string]any{strconv.Itoa(version): map[string]string{"public_key": pub}},
		}}
	}
	switch r.URL.Path {
	case "/v1/transit/keys/sftp-ed":
		reply(keyData("ed25519", 2, base64.StdEncoding.EncodeToString(k.ed.Public().(ed25519.PublicKey))))
	case "/v1/transit/keys/sftp-rsa":
		der, _ := x509.MarshalPKIXPublicKey(&k.rsaKey.PublicKey)
		reply(keyData("rsa-2048", 1, string(pem.EncodeToMemory(&pem.Block{Type: "PUBLIC KEY", Bytes: der}))))
	case "/v1/transit/sign/sftp-ed", "/v1/transit/sign/sftp-rsa":
		var in map[string]any
		_ = json.NewDecoder(r.Body).Decode(&in)
		k.signedWith = append(k.signedWith, in)
		data, _ := base64.StdEncoding.DecodeString(in["input"].(string))
		var sig []byte
		if strings.HasSuffix(r.URL.Path, "ed") {
			sig = ed25519.Sign(k.ed, data)
		} else {
			if in["signature_algorithm"] != "pkcs1v15" {
				w.WriteHeader(http.StatusBadRequest)
				return
			}
			switch in["hash_algorithm"] {
			case "sha2-256":
				h := sha256.Sum256(data)
				sig, _ = rsa.SignPKCS1v15(rand.Reader, k.rsaKey, crypto.SHA256, h[:])
			case "sha2-512":
				h := sha512.Sum512(data)
				sig, _ = rsa.SignPKCS1v15(rand.Reader, k.rsaKey, crypto.SHA512, h[:])
			default:
				w.WriteHeader(http.StatusBadRequest)
				return
			}
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
	cfg := VaultConfig{Addr: k.srv.URL, K8sRole: "treasury", K8sMount: "kubernetes", K8sJWTPath: jwt, SFTPKeyName: "sftp-ed", SFTPKeyVersion: 2, SigningKeyNames: []string{"bank-a"}}
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
	good := map[string]string{"VAULT_ADDR": "https://vault.internal:8200", "VAULT_K8S_ROLE": "treasury", "TREASURY_SFTP_KEY_NAME": "sftp-ed", "TREASURY_SFTP_KEY_VERSION": "2", "TREASURY_SIGNING_KEY_NAMES": "bank-a, bank-b", "TREASURY_BANK_CHANNELS": "sftp,ebics"}
	with := func(k, v string) map[string]string {
		m := map[string]string{}
		for a, b := range good {
			m[a] = b
		}
		m[k] = v
		return m
	}
	ok, err := LoadVaultConfig(env(good))
	if err != nil || ok.K8sMount != "kubernetes" || len(ok.SigningKeyNames) != 2 || ok.SigningKeyNames[1] != "bank-b" || ok.SFTPKeyVersion != 2 {
		t.Fatalf("config: %+v, %v", ok, err)
	}
	caps, err := LoadVaultConfig(env(with("TREASURY_BANK_CHANNELS", "SFTP")))
	if err != nil || len(caps.BankChannels) != 1 || caps.BankChannels[0] != ProtocolSFTP {
		t.Fatalf("SFTP must be lowercased to sftp: %+v, %v", caps, err)
	}
	for name, m := range map[string]map[string]string{
		"settings without an address":       {"VAULT_TOKEN": "t"},
		"channel without vault outside dev": {"TREASURY_BANK_CHANNELS": "sftp"},
		"channel without vault in staging":  {"TREASURY_BANK_CHANNELS": "ebics", "TREASURY_ENV": "staging"},
		"unknown channel":                   with("TREASURY_BANK_CHANNELS", "ftp"),
		"unknown channel in dev":            {"TREASURY_BANK_CHANNELS": "ftp", "TREASURY_ENV": "dev"},
		"no credential":                     {"VAULT_ADDR": "https://v"},
		"both credentials":                  with("VAULT_TOKEN", "t"),
		"plain http to a remote host":       with("VAULT_ADDR", "http://vault.internal"),
		"dotdot key name":                   with("TREASURY_SIGNING_KEY_NAMES", "../sys"),
		"slash in sftp key name":            with("TREASURY_SFTP_KEY_NAME", "a/b"),
		"escaped mount":                     with("VAULT_K8S_AUTH_MOUNT", "k8s%2f.."),
		"channel without signing keys":      with("TREASURY_SIGNING_KEY_NAMES", ""),
		"sftp channel without an sftp key":  with("TREASURY_SFTP_KEY_NAME", ""),
		"SFTP capitals without an sftp key": func() map[string]string {
			m := with("TREASURY_BANK_CHANNELS", "SFTP")
			m["TREASURY_SFTP_KEY_NAME"] = ""
			return m
		}(),
		"sftp key also a signing key": with("TREASURY_SIGNING_KEY_NAMES", "sftp-ed, bank-a"),
		"sftp key without a version":  with("TREASURY_SFTP_KEY_VERSION", ""),
		"zero sftp key version":       with("TREASURY_SFTP_KEY_VERSION", "0"),
		"non-numeric sftp version":    with("TREASURY_SFTP_KEY_VERSION", "latest"),
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
	v, _ := k8sVault(t, k, func(c *VaultConfig) { c.SFTPKeyName = "sftp-rsa"; c.SFTPKeyVersion = 1 })
	signer, err := v.SFTPSigner(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	as := signer.(ssh.AlgorithmSigner)
	data := []byte("session-hash")
	for _, algo := range []string{ssh.KeyAlgoRSASHA256, ssh.KeyAlgoRSASHA512} {
		sig, err := as.SignWithAlgorithm(rand.Reader, data, algo)
		if err != nil || sig.Format != algo {
			t.Fatalf("%s: sig = %+v, %v", algo, sig, err)
		}
		if err := signer.PublicKey().Verify(data, sig); err != nil {
			t.Fatalf("%s signature does not verify: %v", algo, err)
		}
	}
	if _, err := as.SignWithAlgorithm(rand.Reader, data, ssh.KeyAlgoRSA); err == nil {
		t.Fatal("sha1 ssh-rsa must be refused")
	}
}

func TestSFTPSigner_RSALogsInOverDialSSH(t *testing.T) {
	k := newTransitServer(t)
	v, _ := k8sVault(t, k, func(c *VaultConfig) { c.SFTPKeyName = "sftp-rsa"; c.SFTPKeyVersion = 1 })
	signer, err := v.SFTPSigner(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	fx := newSSHFixtureForKey(t, signer.PublicKey())
	client, err := DialSSH(context.Background(), SSHConfig{Addr: fx.addr, User: "bank", HostKeyFingerprint: fx.hostPrint, Signer: signer, Timeout: 5 * time.Second})
	if err != nil {
		t.Fatalf("RSA transit login: %v", err)
	}
	defer func() { _ = client.Close() }()
	path := filepath.Join(fx.rootDir, "hello.txt")
	if err := client.Put(context.Background(), path, []byte("ok")); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(path)
	if err != nil || string(got) != "ok" {
		t.Fatalf("stored = %q, %v", got, err)
	}
}

func TestSFTPSigner_RefusesUnconfiguredAndUnsupportedKeys(t *testing.T) {
	k := newTransitServer(t)
	v, _ := k8sVault(t, k, func(c *VaultConfig) { c.SFTPKeyName = "missing" })
	if _, err := v.SFTPSigner(context.Background()); err == nil {
		t.Fatal("a missing transit key was accepted")
	}
	v, _ = k8sVault(t, k, func(c *VaultConfig) { c.SFTPKeyName = ""; c.SFTPKeyVersion = 0 })
	if _, err := v.SFTPSigner(context.Background()); err == nil {
		t.Fatal("no key name was accepted")
	}
	v, _ = k8sVault(t, k, func(c *VaultConfig) { c.SFTPKeyVersion = 0 })
	if _, err := v.SFTPSigner(context.Background()); err == nil {
		t.Fatal("an unpinned key version was accepted")
	}
	v, _ = k8sVault(t, k, func(c *VaultConfig) { c.SFTPKeyVersion = 9 })
	if _, err := v.SFTPSigner(context.Background()); err == nil {
		t.Fatal("a missing key version was accepted")
	}
	if _, err := transitPublicKey("aes256-gcm96", "x"); err == nil {
		t.Fatal("a symmetric key was accepted")
	}
}

func TestSFTPSigner_RefusesExportableAndPlaintextBackupKeys(t *testing.T) {
	k := newTransitServer(t)
	k.exportable = true
	v, _ := k8sVault(t, k, nil)
	if _, err := v.SFTPSigner(context.Background()); err == nil || !strings.Contains(err.Error(), "exportable") {
		t.Fatalf("exportable key: %v", err)
	}
	k.exportable, k.plaintextBackup = false, true
	if _, err := v.SFTPSigner(context.Background()); err == nil || !strings.Contains(err.Error(), "plaintext") {
		t.Fatalf("plaintext backup key: %v", err)
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
	if _, err := v.Token(ctx); err != nil || k.logins != 2 || k.revokes != 1 {
		t.Fatalf("a failed renewal must revoke then log in again: logins %d revokes %d, %v", k.logins, k.revokes, err)
	}
	if err := v.Close(ctx); err != nil || k.revokes != 2 {
		t.Fatalf("close: revokes %d, %v", k.revokes, err)
	}
	if _, err := v.Token(ctx); err == nil {
		t.Fatal("Token after Close must refuse")
	}
	if k.logins != 2 {
		t.Fatalf("Token after Close minted a token: logins %d", k.logins)
	}
	static := NewVault(VaultConfig{Addr: k.srv.URL, Token: "s.static"})
	_, _ = static.Token(ctx)
	if err := static.Close(ctx); err != nil || k.revokes != 2 {
		t.Fatalf("a static token must not be revoked: revokes %d, %v", k.revokes, err)
	}
	if _, err := static.Token(ctx); err == nil {
		t.Fatal("Token after Close must refuse a static token too")
	}
}

func TestVault_RenewalDoesNotApplyTheLoginLeaseFallback(t *testing.T) {
	k := newTransitServer(t)
	v, clock := k8sVault(t, k, nil)
	ctx := context.Background()
	if _, err := v.Token(ctx); err != nil {
		t.Fatal(err)
	}
	k.lease = 0
	*clock = clock.Add(700 * time.Second)
	if _, err := v.Token(ctx); err != nil || k.renews != 1 {
		t.Fatalf("renew: renews %d, %v", k.renews, err)
	}
	*clock = clock.Add(time.Second)
	if _, err := v.Token(ctx); err != nil || k.logins != 2 {
		t.Fatalf("a zero renewal lease must not be treated as 1h: logins %d, %v", k.logins, err)
	}
}

func TestVault_ForbiddenSignDropsTheCachedToken(t *testing.T) {
	k := newTransitServer(t)
	v, _ := k8sVault(t, k, nil)
	ctx := context.Background()
	signer, err := v.SFTPSigner(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if k.logins != 1 {
		t.Fatalf("logins = %d", k.logins)
	}
	k.signForbidden = true
	if _, err := signer.Sign(rand.Reader, []byte("session-hash")); err == nil {
		t.Fatal("forbidden sign must fail")
	}
	k.signForbidden = false
	if _, err := v.Token(ctx); err != nil || k.logins != 2 {
		t.Fatalf("a 403 from sign must drop the cached token: logins %d, %v", k.logins, err)
	}
	k.signForbidden = true
	if _, err := v.Signer().Sign(ctx, "bank-a", []byte("x")); err == nil {
		t.Fatal("forbidden file sign must fail")
	}
	k.signForbidden = false
	if _, err := v.Token(ctx); err != nil || k.logins != 3 {
		t.Fatalf("a 403 from file sign must drop the cached token: logins %d, %v", k.logins, err)
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
