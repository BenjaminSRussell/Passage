package bankconnect

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rsa"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"golang.org/x/crypto/ssh"
)

const (
	defaultK8sMount   = "kubernetes"
	defaultK8sJWTPath = "/var/run/secrets/kubernetes.io/serviceaccount/token"
	defaultLease      = time.Hour
)

var vaultName = regexp.MustCompile(`^[A-Za-z0-9_-]{1,128}$`)

// VaultConfig says how the service reaches Vault and which transit keys it may
// use. Every key stays in Vault: signing, for bank files and for the SFTP
// client login, is done by the transit engine.
type VaultConfig struct {
	Addr            string
	Token           string
	K8sRole         string
	K8sMount        string
	K8sJWTPath      string
	SFTPKeyName     string
	SFTPKeyVersion  int
	SigningKeyNames []string
	BankChannels    []string
	Client          *http.Client
}

// LoadVaultConfig reads VAULT_ADDR, VAULT_TOKEN or VAULT_K8S_ROLE (with
// VAULT_K8S_AUTH_MOUNT and VAULT_K8S_JWT_PATH), TREASURY_SFTP_KEY_NAME and
// TREASURY_SFTP_KEY_VERSION (the pinned transit key behind the SFTP client
// login), TREASURY_SIGNING_KEY_NAMES (the allow-list of transit keys that may
// sign bank files; disjoint from the SFTP login key), TREASURY_BANK_CHANNELS
// (sftp or ebics, lowercased) and TREASURY_ENV. With no VAULT_ADDR bank
// connectivity is off, which is refused outside TREASURY_ENV=dev when a bank
// channel is enabled.
func LoadVaultConfig(getenv func(string) string) (VaultConfig, error) {
	c := VaultConfig{
		Addr: getenv("VAULT_ADDR"), Token: getenv("VAULT_TOKEN"), K8sRole: getenv("VAULT_K8S_ROLE"),
		K8sMount: orDefault(getenv("VAULT_K8S_AUTH_MOUNT"), defaultK8sMount), K8sJWTPath: orDefault(getenv("VAULT_K8S_JWT_PATH"), defaultK8sJWTPath),
		SFTPKeyName: getenv("TREASURY_SFTP_KEY_NAME"), SigningKeyNames: splitList(getenv("TREASURY_SIGNING_KEY_NAMES")),
		BankChannels: splitList(getenv("TREASURY_BANK_CHANNELS")),
	}
	channels, err := normalizeBankChannels(c.BankChannels)
	if err != nil {
		return VaultConfig{}, err
	}
	c.BankChannels = channels
	if c.Addr == "" {
		if c.Token != "" || c.K8sRole != "" || c.SFTPKeyName != "" || len(c.SigningKeyNames) > 0 {
			return VaultConfig{}, errors.New("vault settings given without VAULT_ADDR")
		}
		if len(c.BankChannels) > 0 && getenv("TREASURY_ENV") != "dev" {
			return VaultConfig{}, errors.New("TREASURY_BANK_CHANNELS needs VAULT_ADDR outside TREASURY_ENV=dev")
		}
		return VaultConfig{}, nil
	}
	if err := CheckURL(c.Addr); err != nil {
		return VaultConfig{}, fmt.Errorf("VAULT_ADDR: %w", err)
	}
	if (c.Token == "") == (c.K8sRole == "") {
		return VaultConfig{}, errors.New("set exactly one of VAULT_TOKEN and VAULT_K8S_ROLE")
	}
	names := append(append([]string{c.K8sMount}, c.SigningKeyNames...), c.SFTPKeyName)
	for _, n := range names {
		if n != "" && !vaultName.MatchString(n) {
			return VaultConfig{}, fmt.Errorf("vault name %q must be letters, digits, - or _", n)
		}
	}
	if c.SFTPKeyName != "" {
		n, err := strconv.Atoi(getenv("TREASURY_SFTP_KEY_VERSION"))
		if err != nil || n < 1 {
			return VaultConfig{}, errors.New("TREASURY_SFTP_KEY_VERSION must be a positive integer")
		}
		c.SFTPKeyVersion = n
	}
	for _, n := range c.SigningKeyNames {
		if n != "" && n == c.SFTPKeyName {
			return VaultConfig{}, errors.New("TREASURY_SFTP_KEY_NAME must not also be a signing key")
		}
	}
	if len(c.BankChannels) > 0 && len(c.SigningKeyNames) == 0 {
		return VaultConfig{}, errors.New("TREASURY_SIGNING_KEY_NAMES is required when a bank channel is enabled")
	}
	for _, ch := range c.BankChannels {
		if ch == ProtocolSFTP && c.SFTPKeyName == "" {
			return VaultConfig{}, errors.New("TREASURY_SFTP_KEY_NAME is required for the sftp channel")
		}
	}
	return c, nil
}

func (c VaultConfig) Enabled() bool { return c.Addr != "" }

func splitList(raw string) []string {
	var out []string
	for _, p := range strings.Split(raw, ",") {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}

func normalizeBankChannels(raw []string) ([]string, error) {
	out := make([]string, 0, len(raw))
	for _, ch := range raw {
		ch = strings.ToLower(ch)
		if ch != ProtocolSFTP && ch != ProtocolEBICS {
			return nil, fmt.Errorf("unknown bank channel %q", ch)
		}
		out = append(out, ch)
	}
	return out, nil
}

func orDefault(v, def string) string {
	if v == "" {
		return def
	}
	return v
}

// Vault authenticates to Vault and hands out transit signers. A Kubernetes
// login is cached until two thirds of its lease has passed, renewed after that
// and revoked by Close; a static VAULT_TOKEN is used as given and never revoked.
type Vault struct {
	cfg       VaultConfig
	now       func() time.Time
	mu        sync.Mutex
	closed    bool
	token     string
	renewAt   time.Time
	expiresAt time.Time
}

func NewVault(cfg VaultConfig) *Vault { return &Vault{cfg: cfg, now: time.Now} }

func (v *Vault) Token(ctx context.Context) (string, error) {
	if !v.cfg.Enabled() {
		return "", errors.New("vault is not configured")
	}
	v.mu.Lock()
	defer v.mu.Unlock()
	if v.closed {
		return "", errors.New("vault client is closed")
	}
	if v.cfg.Token != "" {
		return v.cfg.Token, nil
	}
	now := v.now()
	if v.token != "" && now.Before(v.renewAt) {
		return v.token, nil
	}
	if v.token != "" && now.Before(v.expiresAt) {
		if err := v.renew(ctx); err == nil {
			return v.token, nil
		}
		slog.WarnContext(ctx, "vault token renewal failed, logging in again")
		v.revokeLocked(ctx)
	}
	if err := v.login(ctx); err != nil {
		return "", err
	}
	return v.token, nil
}

type authReply struct {
	Auth struct {
		ClientToken   string `json:"client_token"`
		LeaseDuration int    `json:"lease_duration"`
	} `json:"auth"`
}

func (v *Vault) adopt(r authReply, fallback time.Duration) {
	lease := time.Duration(r.Auth.LeaseDuration) * time.Second
	if lease <= 0 {
		lease = fallback
	}
	if r.Auth.ClientToken != "" {
		v.token = r.Auth.ClientToken
	}
	v.renewAt = v.now().Add(lease * 2 / 3)
	v.expiresAt = v.now().Add(lease)
}

func (v *Vault) login(ctx context.Context) error {
	jwt, err := os.ReadFile(v.cfg.K8sJWTPath)
	if err != nil {
		return fmt.Errorf("vault login: read service account token: %w", err)
	}
	body, err := json.Marshal(map[string]string{"role": v.cfg.K8sRole, "jwt": strings.TrimSpace(string(jwt))})
	if err != nil {
		return err
	}
	var out authReply
	if err := v.cfg.call(ctx, http.MethodPost, "/v1/auth/"+v.cfg.K8sMount+"/login", "", body, &out); err != nil {
		return fmt.Errorf("vault login: %w", err)
	}
	if out.Auth.ClientToken == "" {
		return errors.New("vault login: no client token in reply")
	}
	v.token = ""
	v.adopt(out, defaultLease)
	return nil
}

func (v *Vault) renew(ctx context.Context) error {
	var out authReply
	if err := v.cfg.call(ctx, http.MethodPost, "/v1/auth/token/renew-self", v.token, []byte("{}"), &out); err != nil {
		return err
	}
	v.adopt(out, 0)
	return nil
}

func (v *Vault) revokeLocked(ctx context.Context) {
	if v.token == "" || v.cfg.Token != "" {
		return
	}
	tok := v.token
	v.token = ""
	var out json.RawMessage
	if err := v.cfg.call(ctx, http.MethodPost, "/v1/auth/token/revoke-self", tok, []byte("{}"), &out); err != nil {
		slog.WarnContext(ctx, "vault token revoke failed", "err", err)
	}
}

func (v *Vault) invalidate() {
	v.mu.Lock()
	defer v.mu.Unlock()
	v.token = ""
}

func (v *Vault) dropOnForbidden(err error) {
	if err != nil && strings.Contains(err.Error(), "status 403") {
		v.invalidate()
	}
}

// Close revokes the Kubernetes-login token so it does not outlive the process.
func (v *Vault) Close(ctx context.Context) error {
	v.mu.Lock()
	defer v.mu.Unlock()
	v.closed = true
	if v.token == "" || v.cfg.Token != "" {
		return nil
	}
	var out json.RawMessage
	err := v.cfg.call(ctx, http.MethodPost, "/v1/auth/token/revoke-self", v.token, []byte("{}"), &out)
	v.token = ""
	return err
}

type allowListSigner struct {
	inner   Signer
	allowed map[string]bool
}

func (a allowListSigner) Sign(ctx context.Context, keyName string, payload []byte) (Signature, error) {
	if !a.allowed[keyName] {
		return Signature{}, fmt.Errorf("vault sign: key %q is not an allowed signing key", keyName)
	}
	return a.inner.Sign(ctx, keyName, payload)
}

type clearingSigner struct {
	inner Signer
	vault *Vault
}

func (c clearingSigner) Sign(ctx context.Context, keyName string, payload []byte) (Signature, error) {
	sig, err := c.inner.Sign(ctx, keyName, payload)
	c.vault.dropOnForbidden(err)
	return sig, err
}

// Signer signs bank files in transit, only with the configured key names.
func (v *Vault) Signer() Signer {
	allowed := map[string]bool{}
	for _, n := range v.cfg.SigningKeyNames {
		allowed[n] = true
	}
	return allowListSigner{inner: clearingSigner{inner: VaultTransit{Addr: v.cfg.Addr, TokenFunc: v.Token, Client: v.cfg.Client}, vault: v}, allowed: allowed}
}

// SFTPSigner returns the SFTP client login key as an ssh.Signer whose
// signatures are made by Vault transit; the private key is not exportable and
// never enters this process. Its public key is the transit key's, and is what
// the bank is onboarded with.
func (v *Vault) SFTPSigner(ctx context.Context) (ssh.Signer, error) {
	name := v.cfg.SFTPKeyName
	if !v.cfg.Enabled() || name == "" {
		return nil, errors.New("sftp client key is not configured")
	}
	tok, err := v.Token(ctx)
	if err != nil {
		return nil, err
	}
	var out struct {
		Data struct {
			Type                 string `json:"type"`
			Exportable           bool   `json:"exportable"`
			AllowPlaintextBackup bool   `json:"allow_plaintext_backup"`
			Keys                 map[string]struct {
				PublicKey string `json:"public_key"`
			} `json:"keys"`
		} `json:"data"`
	}
	if err := v.cfg.call(ctx, http.MethodGet, "/v1/transit/keys/"+name, tok, nil, &out); err != nil {
		v.dropOnForbidden(err)
		return nil, fmt.Errorf("vault read sftp public key: %w", err)
	}
	if out.Data.Exportable || out.Data.AllowPlaintextBackup {
		return nil, errors.New("sftp transit key must not be exportable or allow plaintext backup")
	}
	version := v.cfg.SFTPKeyVersion
	if version < 1 {
		return nil, errors.New("sftp client key version is not configured")
	}
	material, ok := out.Data.Keys[strconv.Itoa(version)]
	if !ok || material.PublicKey == "" {
		return nil, fmt.Errorf("vault read sftp public key: version %d is not present", version)
	}
	pub, err := transitPublicKey(out.Data.Type, material.PublicKey)
	if err != nil {
		return nil, fmt.Errorf("vault read sftp public key: %w", err)
	}
	slog.InfoContext(ctx, "sftp client key bound to vault transit", "key", name, "version", version, "fingerprint", ssh.FingerprintSHA256(pub))
	return &transitSSHSigner{vault: v, name: name, version: version, pub: pub, rsa: strings.HasPrefix(out.Data.Type, "rsa-")}, nil
}

func transitPublicKey(keyType, material string) (ssh.PublicKey, error) {
	switch {
	case keyType == "ed25519":
		raw, err := base64.StdEncoding.DecodeString(material)
		if err != nil || len(raw) != ed25519.PublicKeySize {
			return nil, errors.New("malformed ed25519 public key")
		}
		return ssh.NewPublicKey(ed25519.PublicKey(raw))
	case keyType == "rsa-2048" || keyType == "rsa-3072" || keyType == "rsa-4096":
		block, _ := pem.Decode([]byte(material))
		if block == nil {
			return nil, errors.New("malformed rsa public key")
		}
		parsed, err := x509.ParsePKIXPublicKey(block.Bytes)
		if err != nil {
			return nil, errors.New("malformed rsa public key")
		}
		key, ok := parsed.(*rsa.PublicKey)
		if !ok {
			return nil, errors.New("malformed rsa public key")
		}
		return ssh.NewPublicKey(key)
	}
	return nil, fmt.Errorf("transit key type %q cannot sign for SSH", keyType)
}

type transitSSHSigner struct {
	vault   *Vault
	name    string
	version int
	pub     ssh.PublicKey
	rsa     bool
}

func (s *transitSSHSigner) PublicKey() ssh.PublicKey { return s.pub }

func (s *transitSSHSigner) Sign(rand io.Reader, data []byte) (*ssh.Signature, error) {
	return s.SignWithAlgorithm(rand, data, "")
}

func (s *transitSSHSigner) SignWithAlgorithm(_ io.Reader, data []byte, algorithm string) (*ssh.Signature, error) {
	req := map[string]any{"input": base64.StdEncoding.EncodeToString(data), "key_version": s.version}
	format := ssh.KeyAlgoED25519
	if s.rsa {
		hash := map[string]string{"": "sha2-256", ssh.KeyAlgoRSASHA256: "sha2-256", ssh.KeyAlgoRSASHA512: "sha2-512"}[algorithm]
		if hash == "" {
			return nil, fmt.Errorf("sftp key: algorithm %q is not allowed", algorithm)
		}
		req["hash_algorithm"], req["signature_algorithm"] = hash, "pkcs1v15"
		format = ssh.KeyAlgoRSASHA256
		if hash == "sha2-512" {
			format = ssh.KeyAlgoRSASHA512
		}
	} else if algorithm != "" && algorithm != ssh.KeyAlgoED25519 {
		return nil, fmt.Errorf("sftp key: algorithm %q is not allowed", algorithm)
	}
	body, err := json.Marshal(req)
	if err != nil {
		return nil, err
	}
	ctx, cancel := context.WithTimeout(context.Background(), defaultSFTPTimeout)
	defer cancel()
	tok, err := s.vault.Token(ctx)
	if err != nil {
		return nil, err
	}
	var out struct {
		Data struct {
			Signature string `json:"signature"`
		} `json:"data"`
	}
	if err := s.vault.cfg.call(ctx, http.MethodPost, "/v1/transit/sign/"+s.name, tok, body, &out); err != nil {
		s.vault.dropOnForbidden(err)
		return nil, fmt.Errorf("vault sign: %w", err)
	}
	blob, err := parseVaultSignature(out.Data.Signature)
	if err != nil {
		return nil, err
	}
	return &ssh.Signature{Format: format, Blob: blob}, nil
}

func parseVaultSignature(s string) ([]byte, error) {
	parts := strings.SplitN(s, ":", 3)
	if len(parts) != 3 || parts[0] != "vault" || !strings.HasPrefix(parts[1], "v") {
		return nil, errors.New("vault sign: malformed signature")
	}
	sig, err := base64.StdEncoding.DecodeString(parts[2])
	if err != nil || len(sig) == 0 {
		return nil, errors.New("vault sign: malformed signature")
	}
	return sig, nil
}

func (c VaultConfig) call(ctx context.Context, method, path, token string, body []byte, out any) error {
	req, err := http.NewRequestWithContext(ctx, method, strings.TrimRight(c.Addr, "/")+path, bytes.NewReader(body))
	if err != nil {
		return err
	}
	if token != "" {
		req.Header.Set("X-Vault-Token", token)
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := httpClient(c.Client).Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, maxReplyBytes))
	if err != nil {
		return err
	}
	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusNoContent {
		return fmt.Errorf("status %d", resp.StatusCode)
	}
	if len(raw) == 0 {
		return nil
	}
	if err := json.Unmarshal(raw, out); err != nil {
		return errors.New("malformed reply")
	}
	return nil
}
