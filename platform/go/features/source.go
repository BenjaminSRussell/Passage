package features

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/go-chi/chi/v5/middleware"

	"github.com/BenjaminSRussell/passage/platform/tenant"
)

const (
	httpSourceTimeout = 2 * time.Second
	maxResponseBytes  = 1 << 20
	featuresPath      = "/v1/tenant/features"
)

type HTTPSource struct {
	baseURL string
	client  *http.Client
}

func NewHTTPSource(baseURL string, client *http.Client) *HTTPSource {
	if client == nil {
		client = &http.Client{Timeout: httpSourceTimeout}
	}
	return &HTTPSource{baseURL: strings.TrimRight(baseURL, "/"), client: client}
}

func (s *HTTPSource) Enabled(ctx context.Context, tenantID string) (map[string]bool, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, s.baseURL+featuresPath, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set(tenant.HeaderTenant, tenantID)
	if p, ok := tenant.FromContext(ctx); ok && p.UserID != "" {
		req.Header.Set(tenant.HeaderUser, p.UserID)
	}
	if id := middleware.GetReqID(ctx); id != "" {
		req.Header.Set(middleware.RequestIDHeader, id)
	}
	resp, err := s.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
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
