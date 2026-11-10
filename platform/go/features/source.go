package features

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/BenjaminSRussell/passage/platform/tenant"
)

const (
	httpSourceTimeout = 2 * time.Second
	maxResponseBytes  = 1 << 20
	featuresPath      = "/v1/tenant/features"
)

const (
	identityAudience = "identity"
	DutyView         = "identity.feature.view"
)

var ErrUnsigned = errors.New("features: reading a tenant's features needs a service signing key")

type HTTPSource struct {
	baseURL string
	client  *http.Client
	signer  *tenant.ServiceSigner
}

func NewHTTPSource(baseURL string, client *http.Client, signer *tenant.ServiceSigner) *HTTPSource {
	if client == nil {
		client = &http.Client{Timeout: httpSourceTimeout}
	}
	return &HTTPSource{baseURL: strings.TrimRight(baseURL, "/"), client: client, signer: signer}
}

func (s *HTTPSource) Enabled(ctx context.Context, tenantID string) (map[string]bool, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, s.baseURL+featuresPath, nil)
	if err != nil {
		return nil, err
	}
	p, ok := tenant.FromContext(ctx)
	if !ok || p.TenantID != tenantID {
		return nil, errors.New("features: no principal for the tenant being read")
	}
	signed, err := s.signer.ApplyDuties(req, p, identityAudience, "", []string{DutyView})
	if err != nil {
		return nil, err
	}
	if !signed {
		return nil, ErrUnsigned
	}
	resp, err := s.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("features: identity answered %d", resp.StatusCode)
	}
	var body struct {
		Features []struct {
			Key     string `json:"key"`
			Enabled bool   `json:"enabled"`
		} `json:"features"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, maxResponseBytes)).Decode(&body); err != nil {
		return nil, fmt.Errorf("features: unreadable answer: %w", err)
	}
	state := make(map[string]bool, len(body.Features))
	for _, f := range body.Features {
		state[f.Key] = f.Enabled
	}
	return state, nil
}
