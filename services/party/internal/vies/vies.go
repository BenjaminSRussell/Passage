package vies

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"time"

	"github.com/BenjaminSRussell/passage/services/party/internal/domain"
)

const maxBody = 1 << 20

type Unavailable struct{}

func (Unavailable) Check(context.Context, string, string) (domain.Status, error) {
	return domain.StatusUnverified, nil
}

type Client struct {
	baseURL string
	http    *http.Client
}

func New(baseURL string, timeout time.Duration) *Client {
	return &Client{baseURL: baseURL, http: &http.Client{Timeout: timeout, CheckRedirect: sameHostOnly}}
}

func sameHostOnly(req *http.Request, via []*http.Request) error {
	if req.URL.Host != via[0].URL.Host {
		return fmt.Errorf("vies: refusing redirect to %s", req.URL.Host)
	}
	if len(via) >= 3 {
		return errors.New("vies: too many redirects")
	}
	return nil
}

type response struct {
	IsValid   bool   `json:"isValid"`
	UserError string `json:"userError"`
}

func (c *Client) Check(ctx context.Context, country, number string) (domain.Status, error) {
	endpoint := fmt.Sprintf("%s/ms/%s/vat/%s", c.baseURL, url.PathEscape(country), url.PathEscape(number))
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("Accept", "application/json")
	res, err := c.http.Do(req)
	if err != nil {
		return "", err
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		return "", fmt.Errorf("vies: status %d", res.StatusCode)
	}
	var body response
	if err := json.NewDecoder(io.LimitReader(res.Body, maxBody)).Decode(&body); err != nil {
		return "", fmt.Errorf("vies: decode response: %w", err)
	}
	switch {
	case body.UserError == "VALID" && body.IsValid:
		return domain.StatusValid, nil
	case body.UserError == "INVALID" && !body.IsValid:
		return domain.StatusInvalid, nil
	}
	return "", fmt.Errorf("vies: no verdict, userError %q isValid %t", body.UserError, body.IsValid)
}
