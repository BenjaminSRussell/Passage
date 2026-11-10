// Package warehouse is the signed client of the wave-31 work API. The mobile
// service calls it as the signed-in user with a service token limited to the
// duties each operation needs, so the warehouse still checks who is acting.
package warehouse

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/BenjaminSRussell/passage/platform/tenant"
)

const (
	Audience        = "warehouse"
	Timeout         = 5 * time.Second
	maxResponseSize = 1 << 20
	CodeMissingDuty = "AUTH-0003"

	dutyExecute = "warehouse.work.execute"
	dutyInquiry = "warehouse.inquiry.view"
	dutyReceive = "warehouse.receipt.register"
)

var ErrUnknownOp = errors.New("warehouse: unknown operation")

// Outcome is the warehouse's answer. Conflict is a 409: the target changed
// since the device captured the scan (a cancelled or already confirmed line).
// Rejected is any other 4xx. A transport failure or 5xx is an error, not an
// Outcome, because the caller must retry it.
type Outcome struct {
	Status   int             `json:"status"`
	Body     json.RawMessage `json:"body,omitempty"`
	Code     string          `json:"code,omitempty"`
	Detail   string          `json:"detail,omitempty"`
	Replayed bool            `json:"replayed,omitempty"`
}

func (o Outcome) OK() bool       { return o.Status >= 200 && o.Status < 300 }
func (o Outcome) Conflict() bool { return o.Status == http.StatusConflict }

// Caller is what the mobile API needs from the warehouse.
type Caller interface {
	Do(ctx context.Context, op, key string, params map[string]string) (Outcome, error)
}

type Client struct {
	baseURL string
	http    *http.Client
	signer  *tenant.ServiceSigner
}

func New(baseURL string, signer *tenant.ServiceSigner, httpClient *http.Client) (*Client, error) {
	if baseURL == "" {
		return nil, errors.New("warehouse client needs a base URL")
	}
	if signer == nil {
		return nil, errors.New("warehouse client needs a service signing key")
	}
	if httpClient == nil {
		httpClient = &http.Client{Timeout: Timeout, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	}
	return &Client{baseURL: strings.TrimRight(baseURL, "/"), http: httpClient, signer: signer}, nil
}

type route struct {
	method string
	path   string
	duty   string
	body   map[string]any
}

func plan(op string, p map[string]string) (route, error) {
	seg := url.PathEscape
	switch op {
	case "assign":
		return route{http.MethodPost, "/v1/warehouse/work:assign", dutyExecute, map[string]any{"warehouseId": p["warehouseId"]}}, nil
	case "confirm":
		return route{http.MethodPost, fmt.Sprintf("/v1/warehouse/work/%s/lines/%s:confirm", seg(p["workId"]), seg(p["lineNo"])), dutyExecute,
			map[string]any{"qty": json.Number(numberOrZero(p["qty"]))}}, nil
	case "exception":
		return route{http.MethodPost, fmt.Sprintf("/v1/warehouse/work/%s:exception", seg(p["workId"])), dutyExecute,
			map[string]any{"action": p["action"], "lineNo": lineNumber(p["lineNo"]), "confirmedQty": json.Number(numberOrZero(p["qty"])), "reason": p["reason"]}}, nil
	case "receive":
		return route{http.MethodPost, fmt.Sprintf("/v1/warehouse/receipts/%s/lines/%s:receive", seg(p["receiptId"]), seg(p["lineNo"])), dutyReceive,
			map[string]any{"qty": json.Number(numberOrZero(p["qty"]))}}, nil
	case "get-work":
		return route{http.MethodGet, "/v1/warehouse/work/" + seg(p["workId"]), dutyInquiry, nil}, nil
	}
	return route{}, ErrUnknownOp
}

func numberOrZero(s string) string {
	n := json.Number(strings.TrimSpace(s))
	if _, err := n.Float64(); err != nil || s == "" {
		return "0"
	}
	return string(n)
}

func lineNumber(s string) int {
	var n int
	_, _ = fmt.Sscanf(s, "%d", &n)
	return n
}

// DutyFor is the duty a user must hold for op to be called on their behalf.
func DutyFor(op string) (string, bool) {
	r, err := plan(op, nil)
	return r.duty, err == nil
}

// WorkReader is the one internal read that skips the user's duty: a work read
// the service makes for its own bookkeeping, whose body never reaches the user.
type WorkReader interface {
	ReadWork(ctx context.Context, workID string) (Outcome, error)
}

var errNoPrincipal = errors.New("warehouse call: no principal in context")

// checkDuty refuses a call the signed-in user lacks the duty for. The service
// token carries the service grant's duties, never the user's, and the warehouse
// trusts them, so the user's own duty is checked before every call. A context
// without a principal is an error, never a pass. The refusal is a 403 outcome
// and nothing is sent.
func checkDuty(ctx context.Context, duty string) (Outcome, bool, error) {
	p, ok := tenant.FromContext(ctx)
	switch {
	case !ok:
		return Outcome{}, false, errNoPrincipal
	case !p.HasDuty(duty):
		return Outcome{Status: http.StatusForbidden, Code: CodeMissingDuty, Detail: "missing duty " + duty}, true, nil
	}
	return Outcome{}, false, nil
}

// Guard applies the same duty check to any Caller.
func Guard(next Caller) Caller { return guard{next} }

type guard struct{ next Caller }

func (g guard) Do(ctx context.Context, op, key string, params map[string]string) (Outcome, error) {
	duty, known := DutyFor(op)
	if !known {
		return Outcome{}, ErrUnknownOp
	}
	if out, refused, err := checkDuty(ctx, duty); err != nil || refused {
		return out, err
	}
	return g.next.Do(ctx, op, key, params)
}

// Do runs one operation as the signed-in user, who must hold the operation's
// duty. key is sent as the Idempotency-Key, so repeating a call with the same
// key never repeats its effect in the warehouse.
func (c *Client) Do(ctx context.Context, op, key string, params map[string]string) (Outcome, error) {
	r, err := plan(op, params)
	if err != nil {
		return Outcome{}, err
	}
	if out, refused, err := checkDuty(ctx, r.duty); err != nil || refused {
		return out, err
	}
	return c.send(ctx, r, key)
}

func (c *Client) ReadWork(ctx context.Context, workID string) (Outcome, error) {
	r, err := plan("get-work", map[string]string{"workId": workID})
	if err != nil {
		return Outcome{}, err
	}
	return c.send(ctx, r, "")
}

func (c *Client) send(ctx context.Context, r route, key string) (Outcome, error) {
	principal, ok := tenant.FromContext(ctx)
	if !ok {
		return Outcome{}, errNoPrincipal
	}
	var reader io.Reader
	if r.body != nil {
		raw, err := json.Marshal(r.body)
		if err != nil {
			return Outcome{}, err
		}
		reader = bytes.NewReader(raw)
	}
	req, err := http.NewRequestWithContext(ctx, r.method, c.baseURL+r.path, reader)
	if err != nil {
		return Outcome{}, fmt.Errorf("warehouse call: %w", err)
	}
	if r.body != nil {
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Idempotency-Key", key)
	}
	if _, err := c.signer.ApplyDuties(req, principal, Audience, "", []string{r.duty}); err != nil {
		return Outcome{}, fmt.Errorf("warehouse call: %w", err)
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return Outcome{}, fmt.Errorf("warehouse call: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxResponseSize))
	if err != nil {
		return Outcome{}, fmt.Errorf("warehouse call: %w", err)
	}
	if resp.StatusCode >= 500 || resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusTooManyRequests || resp.StatusCode == http.StatusRequestTimeout {
		return Outcome{}, fmt.Errorf("warehouse call: status %d", resp.StatusCode)
	}
	out := Outcome{Status: resp.StatusCode, Body: body, Replayed: resp.Header.Get("Idempotent-Replayed") == "true"}
	if !out.OK() {
		var problem struct {
			Code   string `json:"code"`
			Detail string `json:"detail"`
		}
		_ = json.Unmarshal(body, &problem)
		out.Code, out.Detail = problem.Code, problem.Detail
	}
	return out, nil
}
