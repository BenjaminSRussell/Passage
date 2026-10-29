package jobs

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5/middleware"

	"github.com/BenjaminSRussell/passage/platform/tenant"
)

const productTimeout = 30 * time.Second

type HTTPProductClient struct {
	baseURL string
	client  *http.Client
}

func NewHTTPProductClient(baseURL string) *HTTPProductClient {
	return &HTTPProductClient{baseURL: strings.TrimRight(baseURL, "/"), client: &http.Client{Timeout: productTimeout}}
}

func (c *HTTPProductClient) ExplodePage(ctx context.Context, masterID string, selected map[string][]string, limit int) (ExplodePage, error) {
	var out struct{ Created, Remaining int }
	body := map[string]any{"selected": selected, "limit": limit}
	err := c.do(ctx, http.MethodPost, "/v1/product/products/"+url.PathEscape(masterID)+"/variants:explode", body, &out)
	return ExplodePage{Created: out.Created, Remaining: out.Remaining}, err
}

func (c *HTTPProductClient) ReleasedPage(ctx context.Context, after string, limit int) (ReleasedPage, error) {
	var out struct {
		Items []struct{ ID string } `json:"items"`
		Next  string                `json:"next"`
	}
	query := url.Values{"limit": {strconv.Itoa(limit)}}
	if after != "" {
		query.Set("after", after)
	}
	if err := c.do(ctx, http.MethodGet, "/v1/product/released-products?"+query.Encode(), nil, &out); err != nil {
		return ReleasedPage{}, err
	}
	page := ReleasedPage{Next: out.Next, IDs: make([]string, len(out.Items))}
	for i, item := range out.Items {
		page.IDs[i] = item.ID
	}
	return page, nil
}

func (c *HTTPProductClient) GroupChange(ctx context.Context, releasedIDs []string) ([]ItemResult, error) {
	items := make([]map[string]string, len(releasedIDs))
	for i, id := range releasedIDs {
		items[i] = map[string]string{"releasedProductId": id}
	}
	var out struct {
		Items []struct{ ID, Status, Code string } `json:"items"`
	}
	if err := c.do(ctx, http.MethodPost, "/v1/product/jobs/group-change", map[string]any{"items": items}, &out); err != nil {
		return nil, err
	}
	results := make([]ItemResult, len(out.Items))
	for i, item := range out.Items {
		results[i] = ItemResult{ID: item.ID, Status: item.Status, Code: item.Code}
	}
	return results, nil
}

func (c *HTTPProductClient) do(ctx context.Context, method, path string, in, out any) error {
	var body bytes.Buffer
	if in != nil {
		if err := json.NewEncoder(&body).Encode(in); err != nil {
			return fmt.Errorf("encode product request: %w", err)
		}
	}
	req, err := http.NewRequestWithContext(ctx, method, c.baseURL+path, &body)
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	if p, ok := tenant.FromContext(ctx); ok {
		duties := make([]string, 0, len(p.Duties))
		for d := range p.Duties {
			duties = append(duties, d)
		}
		req.Header.Set(tenant.HeaderTenant, p.TenantID)
		req.Header.Set(tenant.HeaderLegalEntity, p.LegalEntityID)
		req.Header.Set(tenant.HeaderUser, p.UserID)
		req.Header.Set(tenant.HeaderDuties, strings.Join(duties, ","))
	}
	if id := middleware.GetReqID(ctx); id != "" {
		req.Header.Set(middleware.RequestIDHeader, id)
	}
	resp, err := c.client.Do(req)
	if err != nil {
		return fmt.Errorf("product request: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 && resp.StatusCode < 500 {
		return fmt.Errorf("%w: status %d", ErrRejected, resp.StatusCode)
	}
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("product responded with status %d", resp.StatusCode)
	}
	if err := json.NewDecoder(resp.Body).Decode(out); err != nil {
		return fmt.Errorf("decode product response: %w", err)
	}
	return nil
}
