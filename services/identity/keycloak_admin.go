package identity

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

const (
	keycloakTokenSkew      = 30 * time.Second
	keycloakMaxBody        = 1 << 20
	keycloakErrorDetailMax = 200
)

// KeycloakOutcome is what a deprovisioning call did in Keycloak.
type KeycloakOutcome struct {
	// Result is "disabled" (the user is now disabled and its sessions are logged out) or "not_found" (no
	// Keycloak user matches, for example because it was deleted: nothing can log in as it).
	Result string
	// UserID is the Keycloak user id that was disabled.
	UserID string
}

// KeycloakTarget names the Keycloak user to deprovision in a tenant's realm.
type KeycloakTarget struct {
	TenantID string
	// Subject is identity_users.idp_subject: the Keycloak user id (the token sub) for users that have logged in,
	// or the scim:<id> placeholder for a SCIM-created user, which is then looked up by UserName.
	Subject  string
	UserName string
}

// KeycloakError is a Keycloak answer the worker could not use. Permanent errors are parked at once instead of
// being retried: retrying cannot change them.
type KeycloakError struct {
	Op        string
	Status    int
	Detail    string
	Permanent bool
}

func (e *KeycloakError) Error() string {
	msg := "keycloak " + e.Op
	if e.Status != 0 {
		msg += fmt.Sprintf(": status %d", e.Status)
	}
	if e.Detail != "" {
		msg += ": " + e.Detail
	}
	return msg
}

// KeycloakAdmin calls the Admin REST API of a tenant realm as that realm's service-account client (client
// credentials grant). The client is meant to hold realm-management manage-users and nothing else, in the tenant's
// own realm, so a leaked secret reaches one tenant's users only. Secrets are read per realm on each token fetch,
// so a rotated secret is picked up without a restart; tokens are cached per realm until shortly before expiry.
type KeycloakAdmin struct {
	base     string
	clientID string
	secret   func(realm string) (string, error)
	client   *http.Client
	now      func() time.Time

	mu     sync.Mutex
	tokens map[string]keycloakToken
}

type keycloakToken struct {
	value   string
	expires time.Time
}

// NewKeycloakAdmin creates the client. baseURL is Keycloak's root (https://keycloak.example.com, without
// /realms); like the token issuer it must be https except on loopback, because the client secret travels on it.
func NewKeycloakAdmin(baseURL, clientID string, secret func(realm string) (string, error), client *http.Client) (*KeycloakAdmin, error) {
	parsed, err := url.Parse(baseURL)
	if err != nil || !validIdentityURL(parsed) || (parsed.Path != "" && parsed.Path != "/") {
		return nil, errors.New("keycloak admin URL must be an https (or loopback http) root URL")
	}
	if clientID == "" || secret == nil {
		return nil, errors.New("keycloak admin client id and secret source are required")
	}
	if client == nil {
		client = &http.Client{Timeout: 15 * time.Second}
	}
	return &KeycloakAdmin{base: strings.TrimRight(baseURL, "/"), clientID: clientID, secret: secret, client: client, now: time.Now, tokens: map[string]keycloakToken{}}, nil
}

// SecretsDir reads a realm's client secret from dir/<realm>, the layout of a Kubernetes Secret volume whose keys
// are realm names. The realm name must match the tenant registry's pattern, so it cannot leave dir.
func SecretsDir(dir string) func(realm string) (string, error) {
	return func(realm string) (string, error) {
		if !realmNamePattern.MatchString(realm) {
			return "", &KeycloakError{Op: "secret", Detail: "invalid realm name", Permanent: true}
		}
		raw, err := os.ReadFile(filepath.Join(dir, realm))
		if errors.Is(err, os.ErrNotExist) {
			return "", &KeycloakError{Op: "secret", Detail: "no client secret for realm " + realm}
		}
		if err != nil {
			return "", err
		}
		secret := strings.TrimSpace(string(raw))
		if secret == "" {
			return "", &KeycloakError{Op: "secret", Detail: "empty client secret for realm " + realm}
		}
		return secret, nil
	}
}

func (k *KeycloakAdmin) token(ctx context.Context, realm string, fresh bool) (string, error) {
	now := k.now()
	k.mu.Lock()
	cached, ok := k.tokens[realm]
	k.mu.Unlock()
	if ok && !fresh && now.Before(cached.expires) {
		return cached.value, nil
	}
	secret, err := k.secret(realm)
	if err != nil {
		return "", err
	}
	form := url.Values{"grant_type": {"client_credentials"}}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, k.base+"/realms/"+url.PathEscape(realm)+"/protocol/openid-connect/token", strings.NewReader(form.Encode()))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	// RFC 6749 2.3.1: client_secret_basic form-encodes the id and secret before base64.
	req.SetBasicAuth(url.QueryEscape(k.clientID), url.QueryEscape(secret))
	resp, err := k.client.Do(req)
	if err != nil {
		return "", &KeycloakError{Op: "token", Detail: transportDetail(err)}
	}
	defer func() { _ = resp.Body.Close() }()
	var body struct {
		AccessToken string `json:"access_token"`
		ExpiresIn   int    `json:"expires_in"`
		Error       string `json:"error"`
	}
	_ = json.NewDecoder(io.LimitReader(resp.Body, keycloakMaxBody)).Decode(&body)
	if resp.StatusCode != http.StatusOK || body.AccessToken == "" {
		return "", &KeycloakError{Op: "token", Status: resp.StatusCode, Detail: limitDetail(body.Error)}
	}
	expires := now.Add(time.Duration(body.ExpiresIn)*time.Second - keycloakTokenSkew)
	k.mu.Lock()
	k.tokens[realm] = keycloakToken{value: body.AccessToken, expires: expires}
	k.mu.Unlock()
	return body.AccessToken, nil
}

// call sends one admin API request for realm. A 401 drops the cached token and retries once with a new one.
func (k *KeycloakAdmin) call(ctx context.Context, realm, op, method, path string, body any) (int, []byte, error) {
	var payload []byte
	if body != nil {
		var err error
		if payload, err = json.Marshal(body); err != nil {
			return 0, nil, err
		}
	}
	for attempt := 0; ; attempt++ {
		token, err := k.token(ctx, realm, attempt > 0)
		if err != nil {
			return 0, nil, err
		}
		req, err := http.NewRequestWithContext(ctx, method, k.base+"/admin/realms/"+url.PathEscape(realm)+path, bytes.NewReader(payload))
		if err != nil {
			return 0, nil, err
		}
		req.Header.Set("Authorization", "Bearer "+token)
		if body != nil {
			req.Header.Set("Content-Type", "application/json")
		}
		resp, err := k.client.Do(req)
		if err != nil {
			return 0, nil, &KeycloakError{Op: op, Detail: transportDetail(err)}
		}
		raw, readErr := io.ReadAll(io.LimitReader(resp.Body, keycloakMaxBody))
		_ = resp.Body.Close()
		if resp.StatusCode == http.StatusUnauthorized && attempt == 0 {
			continue
		}
		if readErr != nil {
			return 0, nil, &KeycloakError{Op: op, Detail: transportDetail(readErr)}
		}
		return resp.StatusCode, raw, nil
	}
}

func statusError(op string, status int, raw []byte) error {
	var body struct {
		Error        string `json:"error"`
		ErrorMessage string `json:"errorMessage"`
	}
	_ = json.Unmarshal(raw, &body)
	// 400 (the representation was refused) and 403 (the client lacks manage-users) need an operator; a 5xx or 429
	// may clear by itself.
	return &KeycloakError{Op: op, Status: status, Detail: limitDetail(firstNonEmpty(body.ErrorMessage, body.Error)), Permanent: status == http.StatusBadRequest || status == http.StatusForbidden}
}

func limitDetail(s string) string {
	s = strings.Join(strings.Fields(s), " ")
	if len(s) > keycloakErrorDetailMax {
		s = s[:keycloakErrorDetailMax]
	}
	return s
}

// transportDetail keeps the error class of a failed request, not the URL (which could carry a query).
func transportDetail(err error) string {
	var urlErr *url.Error
	if errors.As(err, &urlErr) {
		err = urlErr.Err
	}
	return limitDetail(err.Error())
}

// findUser returns the Keycloak user's full representation, or nil when no user matches.
func (k *KeycloakAdmin) findUser(ctx context.Context, realm string, target KeycloakTarget) (map[string]any, error) {
	if id, ok := canonicalUUID(target.Subject); ok {
		status, raw, err := k.call(ctx, realm, "get user", http.MethodGet, "/users/"+url.PathEscape(id), nil)
		switch {
		case err != nil:
			return nil, err
		case status == http.StatusNotFound:
			return nil, nil
		case status != http.StatusOK:
			return nil, statusError("get user", status, raw)
		}
		var user map[string]any
		if err := json.Unmarshal(raw, &user); err != nil {
			return nil, &KeycloakError{Op: "get user", Detail: "unreadable user representation"}
		}
		return user, nil
	}
	if target.UserName == "" {
		return nil, &KeycloakError{Op: "find user", Detail: "the user has neither a Keycloak subject nor a userName", Permanent: true}
	}
	query := url.Values{"username": {target.UserName}, "exact": {"true"}, "briefRepresentation": {"false"}}
	status, raw, err := k.call(ctx, realm, "find user", http.MethodGet, "/users?"+query.Encode(), nil)
	if err != nil {
		return nil, err
	}
	if status != http.StatusOK {
		return nil, statusError("find user", status, raw)
	}
	var users []map[string]any
	if err := json.Unmarshal(raw, &users); err != nil {
		return nil, &KeycloakError{Op: "find user", Detail: "unreadable user list"}
	}
	switch len(users) {
	case 0:
		return nil, nil
	case 1:
		return users[0], nil
	}
	return nil, &KeycloakError{Op: "find user", Detail: "more than one Keycloak user has this username", Permanent: true}
}

// userTenant reads the single tenant_id attribute of a user representation.
func userTenant(user map[string]any) string {
	attributes, _ := user["attributes"].(map[string]any)
	values, _ := attributes["tenant_id"].([]any)
	if len(values) == 0 {
		return ""
	}
	tenantID, _ := values[0].(string)
	return tenantID
}

// DeprovisionUser disables the target's Keycloak user and logs out all of its sessions, which also sets the user's
// not-before so tokens issued earlier stop refreshing. It is idempotent: an already disabled user is logged out
// again, and a user that does not exist (or disappears midway) is reported as not_found, which is done. A user
// whose tenant_id attribute names another tenant is refused, never touched.
func (k *KeycloakAdmin) DeprovisionUser(ctx context.Context, realm string, target KeycloakTarget) (KeycloakOutcome, error) {
	user, err := k.findUser(ctx, realm, target)
	if err != nil || user == nil {
		return KeycloakOutcome{Result: "not_found"}, err
	}
	id, _ := user["id"].(string)
	if _, ok := canonicalUUID(id); !ok {
		return KeycloakOutcome{}, &KeycloakError{Op: "get user", Detail: "the user representation has no id"}
	}
	if tenantID := userTenant(user); tenantID != "" && !strings.EqualFold(tenantID, target.TenantID) {
		return KeycloakOutcome{}, &KeycloakError{Op: "get user", Detail: "the Keycloak user belongs to another tenant", Permanent: true}
	}
	path := "/users/" + url.PathEscape(id)
	if enabled, _ := user["enabled"].(bool); enabled {
		// The whole representation goes back with enabled=false, as the admin console does, so no attribute is
		// dropped by a partial update.
		user["enabled"] = false
		status, raw, err := k.call(ctx, realm, "disable user", http.MethodPut, path, user)
		switch {
		case err != nil:
			return KeycloakOutcome{}, err
		case status == http.StatusNotFound:
			return KeycloakOutcome{Result: "not_found"}, nil
		case status != http.StatusNoContent && status != http.StatusOK:
			return KeycloakOutcome{}, statusError("disable user", status, raw)
		}
	}
	status, raw, err := k.call(ctx, realm, "logout user", http.MethodPost, path+"/logout", nil)
	switch {
	case err != nil:
		return KeycloakOutcome{}, err
	case status == http.StatusNotFound:
		return KeycloakOutcome{Result: "not_found"}, nil
	case status != http.StatusNoContent && status != http.StatusOK:
		return KeycloakOutcome{}, statusError("logout user", status, raw)
	}
	return KeycloakOutcome{Result: "disabled", UserID: id}, nil
}
