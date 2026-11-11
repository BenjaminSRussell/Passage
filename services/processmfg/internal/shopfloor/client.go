// Package shopfloor is the signed client of the shopfloor service's control recipe API (#25, 25-5). Releasing a
// batch order calls it as the releasing user, with a service token limited to the one duty it needs.
package shopfloor

import (
	"bytes"
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
	// Audience is the service the token is addressed to.
	Audience = "shopfloor"
	// DutyCreate is the shopfloor duty the call carries.
	DutyCreate = "shopfloor.controlrecipe.create"

	timeout = 10 * time.Second
	maxBody = 1 << 20
)

// RecipeRequest is POST /v1/shopfloor/control-recipes.
type RecipeRequest struct {
	OrderID     string         `json:"orderId"`
	ResourceID  string         `json:"resourceId"`
	Destination string         `json:"destination"`
	EdgeID      *string        `json:"edgeId,omitempty"`
	Topic       *string        `json:"topic,omitempty"`
	Recipe      map[string]any `json:"recipe"`
}

// Sender creates control recipes. The API depends on it, so tests can stand in for the service.
type Sender interface {
	CreateRecipe(ctx context.Context, legalEntityID, key string, req RecipeRequest) (recipeID string, err error)
}

// Client is the HTTP Sender.
type Client struct {
	baseURL string
	http    *http.Client
	signer  *tenant.ServiceSigner
}

// New returns the client of the shopfloor service at baseURL.
func New(baseURL string, signer *tenant.ServiceSigner, httpClient *http.Client) (*Client, error) {
	if baseURL == "" {
		return nil, errors.New("the shopfloor client needs SHOPFLOOR_URL")
	}
	if signer == nil {
		return nil, errors.New("the shopfloor client needs a service signing key")
	}
	if httpClient == nil {
		httpClient = &http.Client{Timeout: timeout, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	}
	return &Client{baseURL: strings.TrimRight(baseURL, "/"), http: httpClient, signer: signer}, nil
}

// CreateRecipe sends the recipe as the principal in ctx. key is the Idempotency-Key, so sending the same
// release twice yields the one recipe (a replay answers 200 with it).
func (c *Client) CreateRecipe(ctx context.Context, legalEntityID, key string, in RecipeRequest) (string, error) {
	p, ok := tenant.FromContext(ctx)
	if !ok {
		return "", errors.New("shopfloor call: no principal in context")
	}
	body, err := json.Marshal(in)
	if err != nil {
		return "", err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+"/v1/shopfloor/control-recipes", bytes.NewReader(body))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Idempotency-Key", key)
	if _, err := c.signer.ApplyDuties(req, p, Audience, legalEntityID, []string{DutyCreate}); err != nil {
		return "", fmt.Errorf("shopfloor call: %w", err)
	}
	res, err := c.http.Do(req)
	if err != nil {
		return "", fmt.Errorf("shopfloor call: %w", err)
	}
	defer res.Body.Close()
	b, err := io.ReadAll(io.LimitReader(res.Body, maxBody))
	if err != nil {
		return "", fmt.Errorf("shopfloor call: %w", err)
	}
	if res.StatusCode != http.StatusCreated && res.StatusCode != http.StatusOK {
		var problem struct {
			Code   string `json:"code"`
			Detail string `json:"detail"`
		}
		_ = json.Unmarshal(b, &problem)
		if problem.Detail != "" {
			return "", fmt.Errorf("the shopfloor service refused the control recipe (%d %s): %s", res.StatusCode, problem.Code, problem.Detail)
		}
		return "", fmt.Errorf("the shopfloor service answered %d", res.StatusCode)
	}
	var out struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(b, &out); err != nil || out.ID == "" {
		return "", errors.New("the shopfloor service answered without a recipe id")
	}
	return out.ID, nil
}
