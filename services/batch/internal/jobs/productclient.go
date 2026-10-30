package jobs

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/BenjaminSRussell/passage/platform/tenant"
)

const (
	productTimeout  = 30 * time.Second
	productAttempts = 3
	productBackoff  = 200 * time.Millisecond
	maxRetryAfter   = 5 * time.Second
	maxErrorDetail  = 512

	productAudience = "product"

	dutyProductExplode      = "product.variants.explode"
	dutyProductReleasedRead = "product.released.read"
	dutyProductJobsWrite    = "product.jobs.write"
)

type HTTPProductClient struct {
	baseURL string
	signer  *tenant.ServiceSigner
	client  *http.Client
}

func NewHTTPProductClient(baseURL string, signer *tenant.ServiceSigner) *HTTPProductClient {
	return &HTTPProductClient{baseURL: strings.TrimRight(baseURL, "/"), signer: signer, client: &http.Client{
		Timeout:       productTimeout,
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}}
}

func (c *HTTPProductClient) ExplodePage(ctx context.Context, masterID string, selected map[string][]string, limit int) (ExplodePage, error) {
	var out struct{ Created, Remaining int }
	body := map[string]any{"selected": selected, "limit": limit}
	err := c.do(ctx, http.MethodPost, "/v1/product/products/"+url.PathEscape(masterID)+"/variants:explode", dutyProductExplode, body, &out)
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
	if err := c.do(ctx, http.MethodGet, "/v1/product/released-products?"+query.Encode(), dutyProductReleasedRead, nil, &out); err != nil {
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
	if err := c.do(ctx, http.MethodPost, "/v1/product/jobs/group-change", dutyProductJobsWrite, map[string]any{"items": items}, &out); err != nil {
		return nil, err
	}
	results := make([]ItemResult, len(out.Items))
	for i, item := range out.Items {
		results[i] = ItemResult{ID: item.ID, Status: item.Status, Code: item.Code}
	}
	return results, nil
}

func (c *HTTPProductClient) do(ctx context.Context, method, path, duty string, in, out any) error {
	var payload []byte
	if in != nil {
		var err error
		if payload, err = json.Marshal(in); err != nil {
			return fmt.Errorf("encode product request: %w", err)
		}
	}
	var err error
	for attempt := 0; attempt < productAttempts; attempt++ {
		var wait time.Duration
		if wait, err = c.once(ctx, method, path, duty, payload, out); err == nil || errors.Is(err, ErrRejected) || attempt == productAttempts-1 {
			return err
		}
		select {
		case <-ctx.Done():
			return errors.Join(err, ctx.Err())
		case <-time.After(max(wait, productBackoff<<attempt)):
		}
	}
	return err
}

func (c *HTTPProductClient) once(ctx context.Context, method, path, duty string, payload []byte, out any) (time.Duration, error) {
	req, err := http.NewRequestWithContext(ctx, method, c.baseURL+path, bytes.NewReader(payload))
	if err != nil {
		return 0, fmt.Errorf("%w: %v", ErrRejected, err)
	}
	req.Header.Set("Content-Type", "application/json")
	p, ok := tenant.FromContext(ctx)
	if !ok {
		return 0, fmt.Errorf("%w: no principal in context", ErrRejected)
	}
	if !p.HasDuty(duty) {
		return 0, fmt.Errorf("%w: the run holds no %s duty", ErrRejected, duty)
	}
	signed, err := c.signer.ApplyDuties(req, p, productAudience, p.LegalEntityID, []string{duty})
	if err == nil && !signed {
		err = errors.New("no service signer configured")
	}
	if err != nil {
		return 0, fmt.Errorf("%w: sign product request: %w", ErrRejected, err)
	}
	resp, err := c.client.Do(req)
	if err != nil {
		return 0, fmt.Errorf("product request: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		detail, _ := io.ReadAll(io.LimitReader(resp.Body, maxErrorDetail))
		err := fmt.Errorf("product responded with status %d: %s", resp.StatusCode, strings.TrimSpace(string(detail)))
		if resp.StatusCode < 500 && resp.StatusCode != http.StatusUnauthorized && resp.StatusCode != http.StatusRequestTimeout && resp.StatusCode != http.StatusTooManyRequests {
			return 0, fmt.Errorf("%w: %w", ErrRejected, err)
		}
		return retryAfter(resp.Header.Get("Retry-After")), err
	}
	if err := json.NewDecoder(resp.Body).Decode(out); err != nil {
		return 0, fmt.Errorf("decode product response: %w", err)
	}
	return 0, nil
}

func retryAfter(header string) time.Duration {
	secs, err := strconv.Atoi(header)
	if err != nil || secs < 0 {
		return 0
	}
	return min(time.Duration(secs)*time.Second, maxRetryAfter)
}
