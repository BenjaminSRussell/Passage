// Package optimizer calls the OR-Tools route solver and the load-plan stacker in
// services/routing-py over HTTP.
package optimizer

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

	"github.com/BenjaminSRussell/passage/services/transport/internal/domain"
)

// ErrUnavailable means the optimizer could not be reached, failed or answered
// with something that is not JSON. The API maps it to 503 TMS-0123 rather than
// returning an empty plan.
var ErrUnavailable = errors.New("optimizer unavailable")

// ErrRejected means the optimizer refused the request as invalid (HTTP 422).
// The API validates first, so this signals a contract drift between the services.
var ErrRejected = errors.New("optimizer rejected the request")

// clientTimeout is the ceiling for one call; the caller's context, bounded by the
// solver time limit, normally ends it first.
const clientTimeout = 60 * time.Second

// maxResponseBytes bounds the optimizer reply.
const maxResponseBytes = 4 << 20

// Client is an HTTP client for the optimizer.
type Client struct {
	base string
	hc   *http.Client
}

// New returns a client for the optimizer at baseURL, for example
// http://passage-tms-optimizer:8080. A nil http.Client gets a default one.
func New(baseURL string, hc *http.Client) *Client {
	if hc == nil {
		hc = &http.Client{Timeout: clientTimeout}
	}
	return &Client{base: strings.TrimRight(baseURL, "/"), hc: hc}
}

// Solve posts a normalized VRP request to /v1/routes:solve.
func (c *Client) Solve(ctx context.Context, req domain.RouteRequest) (domain.RouteSolution, error) {
	var sol domain.RouteSolution
	req.LegalEntityID = ""
	if err := c.post(ctx, "/v1/routes:solve", req, &sol); err != nil {
		return domain.RouteSolution{}, err
	}
	if sol.Routes == nil {
		sol.Routes = []domain.RouteSolutionRoute{}
	}
	if sol.Dropped == nil {
		sol.Dropped = []string{}
	}
	return sol, nil
}

// Stack posts a load plan request to /v1/load-plans:stack.
func (c *Client) Stack(ctx context.Context, req domain.LoadPlanRequest) (domain.LoadPlan, error) {
	var plan domain.LoadPlan
	if err := c.post(ctx, "/v1/load-plans:stack", req, &plan); err != nil {
		return domain.LoadPlan{}, err
	}
	if plan.Placements == nil {
		plan.Placements = []domain.Placement{}
	}
	if plan.Unplaced == nil {
		plan.Unplaced = []string{}
	}
	if plan.LoadSequence == nil {
		plan.LoadSequence = []string{}
	}
	return plan, nil
}

func (c *Client) post(ctx context.Context, path string, in, out any) error {
	body, err := json.Marshal(in)
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.base+path, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	resp, err := c.hc.Do(req)
	if err != nil {
		return fmt.Errorf("%w: %v", ErrUnavailable, err)
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, maxResponseBytes))
	if err != nil {
		return fmt.Errorf("%w: %v", ErrUnavailable, err)
	}
	switch {
	case resp.StatusCode == http.StatusUnprocessableEntity:
		return fmt.Errorf("%w: %s", ErrRejected, truncate(raw))
	case resp.StatusCode/100 != 2:
		return fmt.Errorf("%w: HTTP %d", ErrUnavailable, resp.StatusCode)
	}
	if err := json.Unmarshal(raw, out); err != nil {
		return fmt.Errorf("%w: malformed reply", ErrUnavailable)
	}
	return nil
}

func truncate(b []byte) string {
	const max = 300
	if len(b) > max {
		return string(b[:max]) + "..."
	}
	return string(b)
}
