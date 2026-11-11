// Package registry reads the list of active tenants from the identity service, so a scheduled job needs no
// configured tenant list (#25). The call is signed with the shopfloor service key and carries one read-only
// duty, identity.tenant-registry.read, which identity accepts only from the platform operator tenant.
package registry

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/platform/tenant"
)

const (
	// Duty is the identity duty the registry read carries.
	Duty = "identity.tenant-registry.read"
	// Actor is the end user named in the service token.
	Actor = "shopfloor-sweeper"

	audience  = "identity"
	pageSize  = 200
	maxPages  = 500 // 100,000 tenants; more is a runaway cursor
	maxBody   = 4 << 20
	reqTimout = 10 * time.Second
)

// Identity reads GET /v1/tenants?status=active from the identity service as the operator tenant.
type Identity struct {
	BaseURL          string
	OperatorTenantID string
	Signer           *tenant.ServiceSigner
	Client           *http.Client
}

// New checks its settings and returns the registry.
func New(baseURL, operatorTenantID string, signer *tenant.ServiceSigner, client *http.Client) (*Identity, error) {
	switch {
	case baseURL == "":
		return nil, errors.New("the tenant registry needs IDENTITY_URL")
	case !ids.Valid(operatorTenantID):
		return nil, errors.New("the tenant registry needs SWEEP_OPERATOR_TENANT_ID, the platform operator tenant's UUID")
	case signer == nil:
		return nil, errors.New("the tenant registry needs a service signing key (PASSAGE_SERVICE_NAME and PASSAGE_SERVICE_SIGNING_KEY or PASSAGE_SERVICE_SIGNING_KEY_FILE)")
	}
	if client == nil {
		client = &http.Client{Timeout: reqTimout}
	}
	return &Identity{BaseURL: strings.TrimRight(baseURL, "/"), OperatorTenantID: strings.ToLower(operatorTenantID), Signer: signer, Client: client}, nil
}

// Tenants returns the ids of the active tenants, following the cursor to the end.
func (i *Identity) Tenants(ctx context.Context) ([]string, error) {
	var out []string
	after := ""
	for n := 0; n < maxPages; n++ {
		q := url.Values{"status": {"active"}, "limit": {fmt.Sprint(pageSize)}}
		if after != "" {
			q.Set("after", after)
		}
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, i.BaseURL+"/v1/tenants?"+q.Encode(), nil)
		if err != nil {
			return nil, err
		}
		p := tenant.Principal{TenantID: i.OperatorTenantID, UserID: Actor}
		if _, err := i.Signer.ApplyDuties(req, p, audience, "", []string{Duty}); err != nil {
			return nil, fmt.Errorf("sign the registry read: %w", err)
		}
		res, err := i.Client.Do(req)
		if err != nil {
			return nil, fmt.Errorf("read the tenant registry: %w", err)
		}
		body, err := io.ReadAll(io.LimitReader(res.Body, maxBody))
		res.Body.Close()
		if err != nil {
			return nil, fmt.Errorf("read the tenant registry: %w", err)
		}
		if res.StatusCode != http.StatusOK {
			return nil, fmt.Errorf("identity answered %d to the tenant registry read", res.StatusCode)
		}
		var page struct {
			Items []struct {
				TenantID string `json:"tenantId"`
			} `json:"items"`
			Next string `json:"next"`
		}
		if err := json.Unmarshal(body, &page); err != nil {
			return nil, fmt.Errorf("tenant registry answer: %w", err)
		}
		for _, it := range page.Items {
			id, ok := ids.Canonical(it.TenantID)
			if !ok || id == ids.Nil {
				return nil, fmt.Errorf("the tenant registry returned %q, which is not a tenant id", it.TenantID)
			}
			out = append(out, id)
		}
		if page.Next == "" {
			return out, nil
		}
		after = page.Next
	}
	return nil, errors.New("the tenant registry did not end")
}
