// Package azurekv wraps and unwraps fieldcrypt master keys with an Azure Key Vault (or Managed HSM) key (#600).
//
// A master key is generated once, wrapped by the vault key (RSA-OAEP-256 by default; the vault key never leaves the
// vault or HSM), and stored as `id=wrapped:<base64>` in FIELDCRYPT_KEYS. At start the service unwraps each entry
// through the vault and holds the plain master key in memory only. The sealed-value envelope, which names its master
// key id, is unchanged, so values sealed before wrapping still open and rotation is "wrap a new key under a new id".
//
// The vault host must be a Key Vault host of the cloud profile (PASSAGE_CLOUD_PROFILE): a vault URL from another
// cloud is refused, since tokens are not accepted across clouds. Authentication is AKS workload identity
// (AZURE_CLIENT_ID, AZURE_TENANT_ID, AZURE_FEDERATED_TOKEN_FILE), exchanged at the profile's Entra authority.
package azurekv

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/BenjaminSRussell/passage/platform/cloudprofile"
)

const (
	// EnvKeyID is the Key Vault key that wraps the master keys: https://<vault>/keys/<name>[/<version>].
	EnvKeyID = "FIELDCRYPT_AZURE_KEY_ID"
	// EnvAlgorithm overrides the wrapping algorithm (RSA-OAEP-256, RSA-OAEP, RSA1_5, A256KW for a symmetric Managed HSM key).
	EnvAlgorithm = "FIELDCRYPT_AZURE_KEY_ALGORITHM"

	apiVersion       = "7.4"
	defaultAlgorithm = "RSA-OAEP-256"
	maxBody          = 1 << 20
)

// TokenSource returns a bearer token for scope.
type TokenSource interface {
	Token(ctx context.Context, scope string) (string, error)
}

// Client wraps and unwraps keys with one vault key.
type Client struct {
	keyURL    *url.URL // https://<vault>/keys/<name>[/<version>]
	algorithm string
	scope     string
	tokens    TokenSource
	http      *http.Client
}

// Config builds a Client. Zero values take the defaults noted.
type Config struct {
	KeyID     string // required, https://<vault>/keys/<name>[/<version>]
	Algorithm string // default RSA-OAEP-256
	Profile   *cloudprofile.Profile
	Tokens    TokenSource  // default: workload identity from the environment
	HTTP      *http.Client // default: 15 s timeout
}

// New validates the key id against the cloud profile and returns a Client.
func New(cfg Config) (*Client, error) {
	if cfg.Profile == nil {
		return nil, errors.New("azurekv: a cloud profile is required")
	}
	u, err := url.Parse(strings.TrimRight(cfg.KeyID, "/"))
	if err != nil || u.Scheme != "https" || u.Hostname() == "" {
		return nil, fmt.Errorf("azurekv: %s must be an https Key Vault key URL, got %q", EnvKeyID, cfg.KeyID)
	}
	// A Key Vault key, or a Managed HSM key (#602: the wrapping key may sit in a Managed HSM). Each has its own
	// host suffix and its own token audience.
	suffix := cfg.Profile.Endpoint("keyVaultSuffix")
	host := strings.ToLower(u.Hostname())
	if hsm := cfg.Profile.Endpoint("managedHsmSuffix"); hsm != "" && strings.HasSuffix(host, "."+hsm) {
		suffix = hsm
	} else if !strings.HasSuffix(host, "."+suffix) {
		return nil, fmt.Errorf("azurekv: vault host %s is not a *.%s or *.%s host of the %s cloud profile", u.Hostname(), suffix, hsm, cfg.Profile.Selector())
	}
	parts := strings.Split(strings.Trim(u.Path, "/"), "/")
	if len(parts) < 2 || len(parts) > 3 || parts[0] != "keys" || parts[1] == "" {
		return nil, fmt.Errorf("azurekv: key URL path %q must be /keys/<name>[/<version>]", u.Path)
	}
	c := &Client{keyURL: u, algorithm: cfg.Algorithm, scope: "https://" + suffix + "/.default", tokens: cfg.Tokens, http: cfg.HTTP}
	if c.algorithm == "" {
		c.algorithm = defaultAlgorithm
	}
	if c.http == nil {
		c.http = &http.Client{Timeout: 15 * time.Second}
	}
	if c.tokens == nil {
		c.tokens = &WorkloadIdentity{Authority: cfg.Profile.Endpoint("entraAuthority"), HTTP: c.http}
	}
	return c, nil
}

// FromEnv builds a Client from FIELDCRYPT_AZURE_KEY_ID and PASSAGE_CLOUD_PROFILE.
func FromEnv(getenv func(string) string) (*Client, error) {
	profile, err := cloudprofile.FromEnv(getenv)
	if err != nil {
		return nil, err
	}
	return New(Config{KeyID: getenv(EnvKeyID), Algorithm: getenv(EnvAlgorithm), Profile: profile})
}

type operation struct {
	Alg   string `json:"alg"`
	Value string `json:"value"`
}

type result struct {
	Kid   string `json:"kid"`
	Value string `json:"value"`
	Error *struct {
		Code    string `json:"code"`
		Message string `json:"message"`
	} `json:"error"`
}

// do runs a key operation. version, when set, names the key version explicitly (an unwrap of a key wrapped under an
// older version); otherwise the configured key URL decides, and a versionless URL means the latest version. It returns
// the result and the version the vault used (the last segment of the answer's kid).
func (c *Client) do(ctx context.Context, op, version string, in []byte) ([]byte, string, error) {
	token, err := c.tokens.Token(ctx, c.scope)
	if err != nil {
		return nil, "", fmt.Errorf("azurekv: token: %w", err)
	}
	u := *c.keyURL
	parts := strings.Split(strings.Trim(c.keyURL.Path, "/"), "/")
	switch {
	case version != "":
		u.Path = "/" + parts[0] + "/" + parts[1] + "/" + version + "/" + op
	case len(parts) == 2:
		// No version given: Key Vault takes an empty version segment to mean the latest.
		u.Path = strings.TrimRight(c.keyURL.Path, "/") + "//" + op
	default:
		u.Path = strings.TrimRight(u.Path, "/") + "/" + op
	}
	u.RawQuery = url.Values{"api-version": {apiVersion}}.Encode()
	body, _ := json.Marshal(operation{Alg: c.algorithm, Value: base64.RawURLEncoding.EncodeToString(in)})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, u.String(), strings.NewReader(string(body)))
	if err != nil {
		return nil, "", err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")
	res, err := c.http.Do(req)
	if err != nil {
		return nil, "", fmt.Errorf("azurekv: %s: %w", op, err)
	}
	defer res.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(res.Body, maxBody))
	if err != nil {
		return nil, "", fmt.Errorf("azurekv: %s: read: %w", op, err)
	}
	var out result
	_ = json.Unmarshal(raw, &out)
	if res.StatusCode != http.StatusOK {
		code := res.Status
		if out.Error != nil {
			code = out.Error.Code
		}
		// The key material and the request never appear in the error; the vault's own error code does.
		return nil, "", fmt.Errorf("azurekv: %s refused: %s", op, code)
	}
	plain, err := base64.RawURLEncoding.DecodeString(out.Value)
	if err != nil {
		return nil, "", fmt.Errorf("azurekv: %s: response is not base64url: %w", op, err)
	}
	used := version
	if kid, perr := url.Parse(out.Kid); perr == nil {
		if segs := strings.Split(strings.Trim(kid.Path, "/"), "/"); len(segs) == 3 && segs[0] == "keys" && ValidVersion(segs[2]) {
			used = segs[2]
		}
	}
	return plain, used, nil
}

// ValidVersion reports whether v can be a Key Vault key version (32 hex characters in practice).
func ValidVersion(v string) bool {
	if v == "" || len(v) > 64 {
		return false
	}
	for _, r := range v {
		if !(r >= '0' && r <= '9' || r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z') {
			return false
		}
	}
	return true
}

// Wrap encrypts a master key under the vault key.
func (c *Client) Wrap(ctx context.Context, key []byte) ([]byte, error) {
	out, _, err := c.do(ctx, "wrapkey", "", key)
	return out, err
}

// WrapVersioned encrypts a master key under the vault key and returns the key version that wrapped it, so the entry
// can name it and still unwrap after the vault key is rotated to a new version.
func (c *Client) WrapVersioned(ctx context.Context, key []byte) ([]byte, string, error) {
	return c.do(ctx, "wrapkey", "", key)
}

// Unwrap decrypts a wrapped master key with the configured key (the latest version when the URL names none). It
// implements fieldcrypt.Unwrapper.
func (c *Client) Unwrap(ctx context.Context, wrapped []byte) ([]byte, error) {
	out, _, err := c.do(ctx, "unwrapkey", "", wrapped)
	return out, err
}

// UnwrapVersion decrypts a key wrapped under the named version of the vault key.
func (c *Client) UnwrapVersion(ctx context.Context, version string, wrapped []byte) ([]byte, error) {
	if !ValidVersion(version) {
		return nil, fmt.Errorf("azurekv: %q is not a key version", version)
	}
	out, _, err := c.do(ctx, "unwrapkey", version, wrapped)
	return out, err
}

// WorkloadIdentity exchanges the AKS workload identity federated token for an Entra access token.
type WorkloadIdentity struct {
	Authority string // https://login... from the cloud profile; AZURE_AUTHORITY_HOST, set by the webhook, must agree
	HTTP      *http.Client
	Getenv    func(string) string // default os.Getenv

	mu     sync.Mutex
	cached map[string]cachedToken
}

type cachedToken struct {
	token   string
	expires time.Time
}

// Token implements TokenSource.
func (w *WorkloadIdentity) Token(ctx context.Context, scope string) (string, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if t, ok := w.cached[scope]; ok && time.Until(t.expires) > 2*time.Minute {
		return t.token, nil
	}
	getenv := w.Getenv
	if getenv == nil {
		getenv = os.Getenv
	}
	client, tenant, file := getenv("AZURE_CLIENT_ID"), getenv("AZURE_TENANT_ID"), getenv("AZURE_FEDERATED_TOKEN_FILE")
	if client == "" || tenant == "" || file == "" {
		return "", errors.New("workload identity is not configured: AZURE_CLIENT_ID, AZURE_TENANT_ID and AZURE_FEDERATED_TOKEN_FILE are set by the AKS workload identity webhook")
	}
	if host := getenv("AZURE_AUTHORITY_HOST"); host != "" && cloudprofile.HostOf(host) != cloudprofile.HostOf(w.Authority) {
		return "", fmt.Errorf("AZURE_AUTHORITY_HOST %s is not the cloud profile's authority %s", host, w.Authority)
	}
	assertion, err := os.ReadFile(file)
	if err != nil {
		return "", fmt.Errorf("read federated token: %w", err)
	}
	form := url.Values{
		"grant_type":            {"client_credentials"},
		"client_id":             {client},
		"scope":                 {scope},
		"client_assertion_type": {"urn:ietf:params:oauth:client-assertion-type:jwt-bearer"},
		"client_assertion":      {strings.TrimSpace(string(assertion))},
	}
	endpoint := strings.TrimRight(w.Authority, "/") + "/" + url.PathEscape(tenant) + "/oauth2/v2.0/token"
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, strings.NewReader(form.Encode()))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	httpc := w.HTTP
	if httpc == nil {
		httpc = http.DefaultClient
	}
	res, err := httpc.Do(req)
	if err != nil {
		return "", err
	}
	defer res.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(res.Body, maxBody))
	var out struct {
		AccessToken string `json:"access_token"`
		ExpiresIn   int    `json:"expires_in"`
		Error       string `json:"error"`
	}
	_ = json.Unmarshal(raw, &out)
	if res.StatusCode != http.StatusOK || out.AccessToken == "" {
		return "", fmt.Errorf("token endpoint refused: %s %s", res.Status, out.Error)
	}
	if w.cached == nil {
		w.cached = map[string]cachedToken{}
	}
	w.cached[scope] = cachedToken{token: out.AccessToken, expires: time.Now().Add(time.Duration(out.ExpiresIn) * time.Second)}
	return out.AccessToken, nil
}
