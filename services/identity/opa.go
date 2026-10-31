// Package identity contains Wave 2 identity and authorization integrations.
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
	"strings"
	"time"

	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/platform/tenant"
)

var ErrInvalidOPAResponse = errors.New("invalid OPA decision response")

// OPAClient asks a local OPA sidecar for each authorization decision. A failed
// request is an error so callers can fail closed with a service-unavailable
// response instead of treating a policy outage as an authorization denial.
type OPAClient struct {
	endpoint string
	client   *http.Client
}

// NewOPAClient creates a client for an OPA base URL such as
// http://127.0.0.1:8181. The sidecar endpoint must not include credentials,
// query parameters, or a fragment.
func NewOPAClient(baseURL string, client *http.Client) (*OPAClient, error) {
	u, err := url.Parse(strings.TrimSpace(baseURL))
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" {
		return nil, fmt.Errorf("invalid OPA base URL")
	}
	u.Path = strings.TrimRight(u.Path, "/") + "/v1/data/authz/decision"
	u.RawPath = ""
	if client == nil {
		client = &http.Client{Timeout: 250 * time.Millisecond}
	}
	return &OPAClient{endpoint: u.String(), client: client}, nil
}

type opaInput struct {
	Subject  opaSubject `json:"subject"`
	Action   string     `json:"action"`
	Resource string     `json:"resource"`
}

type opaSubject struct {
	TenantID      string `json:"tenant_id"`
	LegalEntityID string `json:"legal_entity_id,omitempty"`
	UserID        string `json:"user_id"`
}

type opaResponse struct {
	Result *struct {
		Allow       bool   `json:"allow"`
		DutyMissing string `json:"duty_missing"`
	} `json:"result"`
}

// Decision is the fail-closed result returned by the policy bundle.
type Decision struct {
	Allow       bool
	DutyMissing string
}

// Allow implements the platform authorization policy interface. The supplied
// action is an HTTP method and resource is the request path; OPA maps those to
// duties and applies tenant isolation before granting access.
func (c *OPAClient) Allow(ctx context.Context, p tenant.Principal, action, resource string) (bool, error) {
	decision, err := c.Decide(ctx, p, action, resource)
	return decision.Allow, err
}

// Decide queries OPA and returns the allow result and, when available, the
// missing duty for a denied method and resource.
func (c *OPAClient) Decide(ctx context.Context, p tenant.Principal, action, resource string) (Decision, error) {
	if c == nil || c.client == nil || p.TenantID == "" || p.UserID == "" || action == "" || resource == "" {
		return Decision{}, errors.New("incomplete OPA authorization input")
	}
	body, err := json.Marshal(struct {
		Input opaInput `json:"input"`
	}{Input: opaInput{
		Subject: opaSubject{TenantID: p.TenantID, LegalEntityID: p.LegalEntityID, UserID: p.UserID},
		Action:  action, Resource: resource,
	}})
	if err != nil {
		return Decision{}, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.endpoint, bytes.NewReader(body))
	if err != nil {
		return Decision{}, err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := c.client.Do(req)
	if err != nil {
		return Decision{}, fmt.Errorf("query OPA: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return Decision{}, fmt.Errorf("query OPA: status %d", resp.StatusCode)
	}
	var decoded opaResponse
	dec := json.NewDecoder(io.LimitReader(resp.Body, 64<<10))
	if err := dec.Decode(&decoded); err != nil {
		return Decision{}, fmt.Errorf("decode OPA response: %w", err)
	}
	if err := dec.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return Decision{}, ErrInvalidOPAResponse
	}
	if decoded.Result == nil {
		return Decision{}, ErrInvalidOPAResponse
	}
	return Decision{Allow: decoded.Result.Allow, DutyMissing: decoded.Result.DutyMissing}, nil
}

// RequireOPA enforces an OPA decision on a route. Policy outages fail with
// 503, missing principals with 401, and denied decisions with 403.
func RequireOPA(client *OPAClient, action, resource string) func(http.Handler) http.Handler {
	return requireOPA(client, func(*http.Request) (string, string) { return action, resource })
}

// RequireOPAForRequest is RequireOPA for a route family: the action is the request method and the resource is
// the request path, so one middleware guards every route beneath a prefix.
func RequireOPAForRequest(client *OPAClient) func(http.Handler) http.Handler {
	return requireOPA(client, func(r *http.Request) (string, string) { return r.Method, r.URL.Path })
}

func requireOPA(client *OPAClient, resolve func(*http.Request) (string, string)) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if verified, _ := r.Context().Value(verifiedPrincipalKey{}).(bool); !verified {
				httpx.Fail(w, r, httpx.NewProblem(http.StatusUnauthorized, "AUTH-0001", "no verified principal"))
				return
			}
			principal, ok := tenant.FromContext(r.Context())
			if !ok {
				httpx.Fail(w, r, httpx.NewProblem(http.StatusUnauthorized, "AUTH-0001", "no principal"))
				return
			}
			action, resource := resolve(r)
			decision, err := client.Decide(r.Context(), principal, action, resource)
			if err != nil {
				httpx.Fail(w, r, httpx.NewProblem(http.StatusServiceUnavailable, "AUTH-0503", "authorization policy is unavailable"))
				return
			}
			if !decision.Allow {
				problem := httpx.NewProblem(http.StatusForbidden, "AUTH-0003", "request is not authorized")
				if decision.DutyMissing != "" {
					problem.Errors = map[string]string{"duty_missing": decision.DutyMissing}
				}
				httpx.Fail(w, r, problem)
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}
