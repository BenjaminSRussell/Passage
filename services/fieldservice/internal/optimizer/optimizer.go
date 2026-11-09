// Package optimizer calls the dispatch service (CP-SAT optimizer) with a signed service
// token that carries only the duty each call needs.
package optimizer

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/BenjaminSRussell/passage/platform/tenant"
)

const (
	timeout         = 10 * time.Second
	maxResponseSize = 8 << 20

	audience   = "dispatch"
	dutyCreate = "dispatch.run.create"
	dutyRead   = "dispatch.run.read"

	Queued    = "QUEUED"
	Running   = "RUNNING"
	Succeeded = "SUCCEEDED"
	Failed    = "FAILED"
)

var (
	ErrNotConfigured = errors.New("dispatch service is not configured")
	ErrRunLost       = errors.New("dispatch service does not know the run")
)

// Error is a refusal of the dispatch service. Permanent ones (400, 422) repeat with every
// retry, so the caller parks instead of backing off.
type Error struct {
	Status int
	Detail string
}

func (e *Error) Error() string {
	return fmt.Sprintf("dispatch responded with status %d: %s", e.Status, e.Detail)
}

func (e *Error) Permanent() bool {
	return e.Status == http.StatusBadRequest || e.Status == http.StatusUnprocessableEntity
}

type Technician struct {
	ID              string   `json:"id"`
	Skills          []string `json:"skills"`
	Territories     []string `json:"territories"`
	ShiftStart      int      `json:"shiftStart"`
	ShiftEnd        int      `json:"shiftEnd"`
	Home            int      `json:"home"`
	MaxDailyMinutes int      `json:"maxDailyMinutes"`
	OvertimeMinutes int      `json:"overtimeMinutes"`
}

type WorkOrder struct {
	ID          string   `json:"id"`
	Territory   string   `json:"territory"`
	Skills      []string `json:"skills"`
	Duration    int      `json:"duration"`
	Location    int      `json:"location"`
	Priority    int      `json:"priority"`
	WindowStart *int     `json:"windowStart,omitempty"`
	WindowEnd   *int     `json:"windowEnd,omitempty"`
	Due         *int     `json:"due,omitempty"`
}

// Assignment places a work order on a technician; times are minutes from the start of the
// local day of the run.
type Assignment struct {
	WorkOrderID  string `json:"workOrderId"`
	TechnicianID string `json:"technicianId"`
	Start        int    `json:"start"`
	End          int    `json:"end"`
	Sequence     int    `json:"sequence"`
}

type Request struct {
	Technicians      []Technician `json:"technicians"`
	WorkOrders       []WorkOrder  `json:"workOrders"`
	Travel           [][]int      `json:"travel"`
	BufferMinutes    int          `json:"bufferMinutes"`
	Pinned           []Assignment `json:"pinned"`
	TimeLimitSeconds int          `json:"timeLimitSeconds"`
}

type Cost struct {
	UnassignedPriority int `json:"unassignedPriority"`
	LatenessMinutes    int `json:"latenessMinutes"`
	TravelMinutes      int `json:"travelMinutes"`
	OvertimeMinutes    int `json:"overtimeMinutes"`
	Technicians        int `json:"technicians"`
	Total              int `json:"total"`
}

type Result struct {
	Assignments    []Assignment `json:"assignments"`
	Unassigned     []string     `json:"unassigned"`
	Solver         string       `json:"solver"`
	CPSatStatus    string       `json:"cpsatStatus"`
	FallbackReason *string      `json:"fallbackReason"`
	ElapsedSeconds float64      `json:"elapsedSeconds"`
	Cost           Cost         `json:"cost"`
}

// Run is the state of one run at the dispatch service.
type Run struct {
	ID     string  `json:"id"`
	Status string  `json:"status"`
	Result *Result `json:"result"`
	Error  *string `json:"error"`
}

type Client struct {
	baseURL string
	signer  *tenant.ServiceSigner
	http    *http.Client
}

func New(baseURL string, signer *tenant.ServiceSigner) *Client {
	return &Client{baseURL: strings.TrimRight(baseURL, "/"), signer: signer,
		http: &http.Client{Timeout: timeout, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}}
}

// Configured reports whether calls can be made.
func (c *Client) Configured() bool { return c != nil && c.baseURL != "" && c.signer != nil }

// Submit starts a run; the same key returns the run started earlier.
func (c *Client) Submit(ctx context.Context, legalEntityID, key string, req Request) (Run, error) {
	payload, err := json.Marshal(req)
	if err != nil {
		return Run{}, err
	}
	status, raw, err := c.do(ctx, http.MethodPost, c.baseURL+"/v1/dispatch/runs", dutyCreate, legalEntityID, key, payload)
	if err != nil {
		return Run{}, err
	}
	if status != http.StatusAccepted && status != http.StatusOK {
		return Run{}, refusal(status, raw)
	}
	return decodeRun(raw)
}

// Get reads a run; ErrRunLost means the service has no such run (it restarted).
func (c *Client) Get(ctx context.Context, legalEntityID, id string) (Run, error) {
	status, raw, err := c.do(ctx, http.MethodGet, c.baseURL+"/v1/dispatch/runs/"+url.PathEscape(id), dutyRead, legalEntityID, "", nil)
	if err != nil {
		return Run{}, err
	}
	switch status {
	case http.StatusOK:
		return decodeRun(raw)
	case http.StatusNotFound:
		return Run{}, ErrRunLost
	}
	return Run{}, refusal(status, raw)
}

func decodeRun(raw []byte) (Run, error) {
	var run Run
	if err := json.Unmarshal(raw, &run); err != nil || run.ID == "" {
		return Run{}, fmt.Errorf("decode dispatch response: %v", err)
	}
	return run, nil
}

func refusal(status int, raw []byte) error {
	var body struct {
		Detail string `json:"detail"`
		Code   string `json:"code"`
	}
	_ = json.Unmarshal(raw, &body)
	return &Error{Status: status, Detail: strings.TrimSpace(body.Code + " " + body.Detail)}
}

func (c *Client) do(ctx context.Context, method, target, duty, legalEntityID, idempotencyKey string, payload []byte) (int, []byte, error) {
	if !c.Configured() {
		return 0, nil, ErrNotConfigured
	}
	p, ok := tenant.FromContext(ctx)
	if !ok {
		return 0, nil, errors.New("no principal in context")
	}
	req, err := http.NewRequestWithContext(ctx, method, target, bytes.NewReader(payload))
	if err != nil {
		return 0, nil, err
	}
	if payload != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if idempotencyKey != "" {
		req.Header.Set("Idempotency-Key", idempotencyKey)
	}
	if _, err := c.signer.ApplyDuties(req, p, audience, legalEntityID, []string{duty}); err != nil {
		return 0, nil, fmt.Errorf("sign dispatch request: %w", err)
	}
	resp, err := c.http.Do(req)
	if err != nil {
		slog.ErrorContext(ctx, "dispatch request failed", "err", err)
		return 0, nil, fmt.Errorf("dispatch request: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, maxResponseSize))
	if err != nil {
		return 0, nil, fmt.Errorf("read dispatch response: %w", err)
	}
	return resp.StatusCode, raw, nil
}
