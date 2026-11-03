package bankconnect

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"os"
	"strings"

	"golang.org/x/crypto/ssh"
)

const (
	defaultK8sMount   = "kubernetes"
	defaultK8sJWTPath = "/var/run/secrets/kubernetes.io/serviceaccount/token"
	defaultKeyField   = "private_key"
)

// VaultConfig says where the bank keys live and how the service authenticates
// to Vault. It holds the address, a token or a Kubernetes auth role, and key
// names and paths; it never holds key material.
type VaultConfig struct {
	Addr           string
	Token          string
	K8sRole        string
	K8sMount       string
	K8sJWTPath     string
	SFTPKeyPath    string
	SFTPKeyField   string
	SigningKeyName string
	Client         *http.Client
}

// LoadVaultConfig reads VAULT_ADDR, VAULT_TOKEN or VAULT_K8S_ROLE (with
// VAULT_K8S_AUTH_MOUNT and VAULT_K8S_JWT_PATH), TREASURY_SFTP_KEY_PATH
// (KV v2, "<mount>/<path>"), TREASURY_SFTP_KEY_FIELD and
// TREASURY_SIGNING_KEY_NAME. With no VAULT_ADDR bank connectivity is off and
// the zero config is returned.
func LoadVaultConfig(getenv func(string) string) (VaultConfig, error) {
	c := VaultConfig{
		Addr: getenv("VAULT_ADDR"), Token: getenv("VAULT_TOKEN"), K8sRole: getenv("VAULT_K8S_ROLE"),
		K8sMount: orDefault(getenv("VAULT_K8S_AUTH_MOUNT"), defaultK8sMount), K8sJWTPath: orDefault(getenv("VAULT_K8S_JWT_PATH"), defaultK8sJWTPath),
		SFTPKeyPath: strings.Trim(getenv("TREASURY_SFTP_KEY_PATH"), "/"), SFTPKeyField: orDefault(getenv("TREASURY_SFTP_KEY_FIELD"), defaultKeyField),
		SigningKeyName: getenv("TREASURY_SIGNING_KEY_NAME"),
	}
	if c.Addr == "" {
		if c.Token != "" || c.K8sRole != "" || c.SFTPKeyPath != "" || c.SigningKeyName != "" {
			return VaultConfig{}, errors.New("vault settings given without VAULT_ADDR")
		}
		return VaultConfig{}, nil
	}
	if err := checkURL(c.Addr); err != nil {
		return VaultConfig{}, fmt.Errorf("VAULT_ADDR: %w", err)
	}
	if (c.Token == "") == (c.K8sRole == "") {
		return VaultConfig{}, errors.New("set exactly one of VAULT_TOKEN and VAULT_K8S_ROLE")
	}
	if c.SFTPKeyPath != "" && !strings.Contains(c.SFTPKeyPath, "/") {
		return VaultConfig{}, errors.New("TREASURY_SFTP_KEY_PATH must be <mount>/<path>")
	}
	return c, nil
}

func (c VaultConfig) Enabled() bool { return c.Addr != "" }

func orDefault(v, def string) string {
	if v == "" {
		return def
	}
	return v
}

func (c VaultConfig) token(ctx context.Context) (string, error) {
	if c.Token != "" {
		return c.Token, nil
	}
	jwt, err := os.ReadFile(c.K8sJWTPath)
	if err != nil {
		return "", fmt.Errorf("vault login: read service account token: %w", err)
	}
	body, err := json.Marshal(map[string]string{"role": c.K8sRole, "jwt": strings.TrimSpace(string(jwt))})
	if err != nil {
		return "", err
	}
	var out struct {
		Auth struct {
			ClientToken string `json:"client_token"`
		} `json:"auth"`
	}
	if err := c.call(ctx, http.MethodPost, "/v1/auth/"+c.K8sMount+"/login", "", body, &out); err != nil {
		return "", fmt.Errorf("vault login: %w", err)
	}
	if out.Auth.ClientToken == "" {
		return "", errors.New("vault login: no client token in reply")
	}
	return out.Auth.ClientToken, nil
}

// Transit returns a signer that signs in Vault's transit engine with a token
// obtained now, so a short-lived Kubernetes login never outlives its caller.
func (c VaultConfig) Transit(ctx context.Context) (VaultTransit, error) {
	if !c.Enabled() {
		return VaultTransit{}, errors.New("vault is not configured")
	}
	tok, err := c.token(ctx)
	if err != nil {
		return VaultTransit{}, err
	}
	return VaultTransit{Addr: c.Addr, Token: tok, Client: c.Client}, nil
}

// SFTPSigner reads the SFTP client private key from Vault KV v2 and returns it
// as an in-memory ssh.Signer. The PEM is never written to disk, the database
// or a log; the buffers it passes through are zeroed once parsed.
func (c VaultConfig) SFTPSigner(ctx context.Context) (ssh.Signer, error) {
	if !c.Enabled() || c.SFTPKeyPath == "" {
		return nil, errors.New("sftp client key is not configured")
	}
	tok, err := c.token(ctx)
	if err != nil {
		return nil, err
	}
	mount, rest, _ := strings.Cut(c.SFTPKeyPath, "/")
	var out struct {
		Data struct {
			Data map[string]string `json:"data"`
		} `json:"data"`
	}
	if err := c.call(ctx, http.MethodGet, "/v1/"+url.PathEscape(mount)+"/data/"+rest, tok, nil, &out); err != nil {
		return nil, fmt.Errorf("vault read sftp client key: %w", err)
	}
	pem := []byte(out.Data.Data[c.SFTPKeyField])
	defer clear(pem)
	if len(pem) == 0 {
		return nil, fmt.Errorf("vault read sftp client key: field %q is empty or missing", c.SFTPKeyField)
	}
	signer, err := ssh.ParsePrivateKey(pem)
	if err != nil {
		return nil, errors.New("vault read sftp client key: not an unencrypted private key")
	}
	slog.InfoContext(ctx, "sftp client key loaded from vault", "path", c.SFTPKeyPath, "fingerprint", ssh.FingerprintSHA256(signer.PublicKey()))
	return signer, nil
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
	defer clear(raw)
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("status %d", resp.StatusCode)
	}
	if err := json.Unmarshal(raw, out); err != nil {
		return errors.New("malformed reply")
	}
	return nil
}
