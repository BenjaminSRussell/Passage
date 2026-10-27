// Package optimizer calls scheduler-opt, the Python CP-SAT service (FR-23.14).
// The Go scheduler owns the decision: a failed or UNKNOWN answer is never an
// error for the planner, the caller keeps its heuristic sequence (BR-23.9).
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
)

// CampaignPath is scheduler-opt's campaign sequencing endpoint.
const CampaignPath = "/v1/optimize/campaign-sequence"

// maxResponseBytes bounds what a misbehaving solver can make us read.
const maxResponseBytes = 1 << 20

// ErrNotConfigured is returned when no optimizer URL was set.
var ErrNotConfigured = errors.New("optimizer: no scheduler-opt URL configured")

// CampaignResult is scheduler-opt's answer. Sequence holds zero-based order
// indices in run order; it is empty unless Status is OPTIMAL or FEASIBLE.
type CampaignResult struct {
	Status            string `json:"status"`
	Sequence          []int  `json:"sequence"`
	ChangeoverMinutes int    `json:"changeoverMinutes"`
}

// Client posts to scheduler-opt over HTTP.
type Client struct {
	base string
	http *http.Client
}

// NewClient returns a client for the scheduler-opt base URL, or nil when the
// URL is empty so that callers can treat "not configured" as no optimizer.
func NewClient(baseURL string) *Client {
	base := strings.TrimRight(strings.TrimSpace(baseURL), "/")
	if base == "" {
		return nil
	}
	return &Client{base: base, http: &http.Client{}}
}

// CampaignSequence asks the solver for the minimum-changeover order. The
// request timeout is the solver time limit plus a margin for transport.
func (c *Client) CampaignSequence(ctx context.Context, changeover [][]int, timeLimit time.Duration) (CampaignResult, error) {
	if c == nil {
		return CampaignResult{}, ErrNotConfigured
	}
	body, err := json.Marshal(map[string]any{
		"changeover":       changeover,
		"timeLimitSeconds": timeLimit.Seconds(),
	})
	if err != nil {
		return CampaignResult{}, fmt.Errorf("optimizer: encode request: %w", err)
	}
	ctx, cancel := context.WithTimeout(ctx, timeLimit+5*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.base+CampaignPath, bytes.NewReader(body))
	if err != nil {
		return CampaignResult{}, fmt.Errorf("optimizer: build request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := c.http.Do(req)
	if err != nil {
		return CampaignResult{}, fmt.Errorf("optimizer: call scheduler-opt: %w", err)
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, maxResponseBytes))
	if err != nil {
		return CampaignResult{}, fmt.Errorf("optimizer: read response: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		return CampaignResult{}, fmt.Errorf("optimizer: scheduler-opt returned HTTP %d", resp.StatusCode)
	}
	var out CampaignResult
	if err := json.Unmarshal(raw, &out); err != nil {
		return CampaignResult{}, fmt.Errorf("optimizer: decode response: %w", err)
	}
	return out, nil
}
