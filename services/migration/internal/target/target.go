// Package target loads mapped rows into the owning services through their HTTP APIs, acting as the
// service:migration-loader principal (integration track S3). Party, vendor and customer rows go to the party service,
// products to the product service and opening AR items to receivables. Every call carries a service token this
// service signs for the target, a stable Idempotency-Key and the legal entity of the row. A refusal (4xx) is the row's
// reject with the target's own code; a 5xx, a 429 or a timeout is retried with backoff and, once the attempts are
// used up, rejects the row with MIG-TARGET-UNAVAILABLE. An entity with no target API is rejected as
// MIG-TARGET-NOT-AVAILABLE, never skipped.
package target

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/BenjaminSRussell/passage/platform/tenant"
	"github.com/BenjaminSRussell/passage/services/migration/internal/domain"
)

// Reject codes the loader raises itself; a target's own refusal keeps the target's code.
const (
	CodeNotAvailable      = "MIG-TARGET-NOT-AVAILABLE"
	CodeUnavailable       = "MIG-TARGET-UNAVAILABLE"
	CodeUpdateUnavailable = "MIG-TARGET-UPDATE-UNAVAILABLE"
	CodeVersionUnknown    = "MIG-TARGET-VERSION-UNKNOWN"
	// CodeDrift is BR-10.8: the record changed in Passage since the last load, so the changed legacy row is not
	// written over it.
	CodeDrift         = "MIG-TARGET-DRIFT"
	CodeRefused       = "MIG-TARGET-REFUSED"
	CodeFieldRequired = "MIG-LOAD-FIELD-REQUIRED"
	CodeLegalEntity   = "MIG-LOAD-LEGAL-ENTITY"
	CodeRefUnresolved = "MIG-REF-UNRESOLVED"

	// Principal is the service principal every call runs as; Caller is the service name the tokens are signed under.
	Principal = "migration-loader"
	Caller    = "migration"

	audParty       = "party"
	audProduct     = "product"
	audReceivables = "receivables"

	// BR-10.1: five tries with exponential backoff from 1 s, at most 60 s between tries.
	defaultAttempts    = 5
	defaultBackoff     = time.Second
	maxBackoff         = 60 * time.Second
	defaultConcurrency = 8
	defaultTimeout     = 30 * time.Second
	maxDetail          = 300
	maxBody            = 4 << 20
	tripAfter          = 3
)

// Config says where the owning services are and how to sign for them. A base URL left empty makes that service's
// entities "target not available".
type Config struct {
	PartyURL, ProductURL, ReceivablesURL string
	Signer                               *tenant.ServiceSigner
	Client                               *http.Client
	// Attempts is the most tries per call (default 5); Backoff the wait after the first (default 1s, doubling, at
	// most 60s); Concurrency the calls in flight for one batch of single-row endpoints (default 8).
	Attempts    int
	Backoff     time.Duration
	Concurrency int
	// Sleep waits between attempts; tests replace it.
	Sleep func(ctx context.Context, d time.Duration) error
}

// Configured reports whether a run can load anything: a signing key and at least one target.
func (c Config) Configured() bool {
	return c.Signer != nil && (c.PartyURL != "" || c.ProductURL != "" || c.ReceivablesURL != "")
}

// Run is what one migration run fixes for the loader.
type Run struct {
	SourceSystem string
	// LegalEntityID is the legal entity rows load into unless a row names its own in legalEntityId.
	LegalEntityID string
	Xref          domain.XrefStore
}

// Loader implements domain.TargetLoader for one run.
type Loader struct {
	cfg    Config
	run    Run
	client *http.Client

	mu     sync.Mutex
	failed map[string]int // consecutive exhausted calls per target service
}

var _ domain.TargetLoader = (*Loader)(nil)

// New builds the loader of one run.
func New(cfg Config, run Run) *Loader {
	if cfg.Attempts < 1 {
		cfg.Attempts = defaultAttempts
	}
	if cfg.Backoff <= 0 {
		cfg.Backoff = defaultBackoff
	}
	if cfg.Concurrency < 1 {
		cfg.Concurrency = defaultConcurrency
	}
	if cfg.Sleep == nil {
		cfg.Sleep = sleep
	}
	client := cfg.Client
	if client == nil {
		client = &http.Client{Timeout: defaultTimeout}
	}
	cfg.PartyURL, cfg.ProductURL, cfg.ReceivablesURL = strings.TrimRight(cfg.PartyURL, "/"), strings.TrimRight(cfg.ProductURL, "/"), strings.TrimRight(cfg.ReceivablesURL, "/")
	return &Loader{cfg: cfg, run: run, client: client, failed: map[string]int{}}
}

func sleep(ctx context.Context, d time.Duration) error {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}

// Entities lists the entity names the loader has a target API for.
func Entities() []string {
	return []string{"party", "customer", "vendor", "product", "item", "ar_open_item"}
}

// LoadBatch loads items of one entity. The result has one entry per item, in order. An error means the batch could
// not be attempted at all (no tenant in the context, a missing service grant).
func (l *Loader) LoadBatch(ctx context.Context, entity string, items []domain.LoadItem) ([]domain.LoadResult, error) {
	p, ok := tenant.FromContext(ctx)
	if !ok || p.TenantID == "" {
		return nil, errors.New("loader needs the tenant in the context")
	}
	switch entity {
	case "party":
		return l.each(ctx, p.TenantID, audParty, l.cfg.PartyURL, items, l.loadParty)
	case "customer":
		return l.each(ctx, p.TenantID, audParty, l.cfg.PartyURL, items, l.loadCustomer)
	case "vendor":
		return l.each(ctx, p.TenantID, audParty, l.cfg.PartyURL, items, l.loadVendor)
	case "product", "item":
		return l.each(ctx, p.TenantID, audProduct, l.cfg.ProductURL, items, l.loadProduct)
	case "ar_open_item":
		return l.openItems(ctx, p.TenantID, items)
	}
	return all(items, domain.LoadResult{Code: CodeNotAvailable, Detail: fmt.Sprintf("no target API loads %s yet; it is a handoff (services/migration/NOT-IMPLEMENTED.md)", entity)}), nil
}

func all(items []domain.LoadItem, r domain.LoadResult) []domain.LoadResult {
	out := make([]domain.LoadResult, len(items))
	for i := range out {
		out[i] = r
	}
	return out
}

// rowLoader loads one row; a returned error aborts the batch.
type rowLoader func(ctx context.Context, c *call, it domain.LoadItem) (domain.LoadResult, error)

// each runs load for every item, a few at a time. Once a target has exhausted its retries on several rows in a row
// it is treated as down for the rest of the batch: the other rows are rejected as unavailable without more calls, so
// an outage costs one backoff cycle, not one per row.
func (l *Loader) each(ctx context.Context, tenantID, aud, base string, items []domain.LoadItem, load rowLoader) ([]domain.LoadResult, error) {
	if base == "" {
		return all(items, domain.LoadResult{Code: CodeNotAvailable, Detail: "the " + aud + " service address is not configured for the migration service"}), nil
	}
	results := make([]domain.LoadResult, len(items))
	var wg sync.WaitGroup
	var firstErr error
	var errOnce sync.Once
	sem := make(chan struct{}, l.cfg.Concurrency)
	for i := range items {
		if err := ctx.Err(); err != nil {
			errOnce.Do(func() { firstErr = err })
			break
		}
		sem <- struct{}{}
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			defer func() { <-sem }()
			if l.down(aud) {
				results[i] = domain.LoadResult{Code: CodeUnavailable, Detail: "the " + aud + " service stopped answering earlier in this batch"}
				return
			}
			res, err := load(ctx, &call{l: l, tenantID: tenantID, aud: aud, base: base}, items[i])
			if err != nil {
				errOnce.Do(func() { firstErr = err })
				return
			}
			results[i] = res
		}(i)
	}
	wg.Wait()
	if firstErr != nil {
		return nil, firstErr
	}
	return results, nil
}

func (l *Loader) down(aud string) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.failed[aud] >= tripAfter
}

func (l *Loader) note(aud string, exhausted bool) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if exhausted {
		l.failed[aud]++
	} else {
		l.failed[aud] = 0
	}
}

// call is one row's access to a target.
type call struct {
	l        *Loader
	tenantID string
	aud      string
	base     string
}

// reply is a target's final answer to a call: a 2xx, or a 4xx refusal; exhausted is a 5xx/timeout that outlived the retries.
type reply struct {
	status    int
	body      []byte
	exhausted bool
	detail    string
}

func (r reply) ok() bool { return !r.exhausted && r.status >= 200 && r.status < 300 }

// refusal turns a failed reply into the row's result.
func (r reply) refusal() domain.LoadResult {
	if r.exhausted {
		return domain.LoadResult{Code: CodeUnavailable, Detail: r.detail}
	}
	code, detail := problem(r.status, r.body)
	return domain.LoadResult{Code: code, Detail: detail}
}

// updateRefusal is refusal for a PATCH under the version recorded at the last load: a version conflict means the
// record changed in Passage since then (or the answer to an earlier PATCH was lost), which BR-10.8 reports as drift.
func (r reply) updateRefusal() domain.LoadResult {
	res := r.refusal()
	if !r.exhausted && (r.status == http.StatusConflict || r.status == http.StatusPreconditionFailed) && strings.HasSuffix(res.Code, "-VERSION-CONFLICT") {
		return domain.LoadResult{Code: CodeDrift, Detail: clip("the record changed in Passage since it was loaded and is not overwritten (" + res.Code + ": " + res.Detail + ")")}
	}
	return res
}

type problemBody struct {
	Code        string            `json:"code"`
	ServiceCode string            `json:"serviceCode"`
	Detail      string            `json:"detail"`
	Title       string            `json:"title"`
	Errors      map[string]string `json:"errors"`
}

// problem extracts the target's error code and a short reason from a problem+json body.
func problem(status int, body []byte) (string, string) {
	var p problemBody
	if json.Unmarshal(body, &p) != nil || (p.Code == "" && p.ServiceCode == "") {
		return CodeRefused, clip(fmt.Sprintf("target answered %d", status))
	}
	code := p.ServiceCode
	if code == "" {
		code = p.Code
	}
	detail := p.Detail
	if detail == "" {
		detail = p.Title
	}
	if len(p.Errors) > 0 {
		keys := make([]string, 0, len(p.Errors))
		for k := range p.Errors {
			keys = append(keys, k)
		}
		slices.Sort(keys)
		parts := make([]string, len(keys))
		for i, k := range keys {
			parts[i] = k + ": " + p.Errors[k]
		}
		detail += " (" + strings.Join(parts, "; ") + ")"
	}
	return code, clip(detail)
}

func clip(s string) string {
	if len(s) > maxDetail {
		return s[:maxDetail] + "..."
	}
	return s
}

// do sends one request with retries. Each attempt gets a fresh service token (a token id is single use). A 5xx, a
// 429, a timeout or a connection failure is retried with backoff; every other answer is final. Retrying a POST is
// safe because every call that creates something carries a stable Idempotency-Key.
func (c *call) do(ctx context.Context, method, path, legalEntityID string, headers map[string]string, payload any) (reply, error) {
	var raw []byte
	if payload != nil {
		var err error
		if raw, err = json.Marshal(payload); err != nil {
			return reply{}, err
		}
	}
	if c.l.cfg.Signer == nil {
		return reply{}, errors.New("no service signing key is configured")
	}
	principal, err := tenant.ServicePrincipal(Principal, c.tenantID, legalEntityID)
	if err != nil {
		return reply{}, err
	}
	var last string
	for attempt := 1; attempt <= c.l.cfg.Attempts; attempt++ {
		req, err := http.NewRequestWithContext(ctx, method, c.base+path, bytes.NewReader(raw))
		if err != nil {
			return reply{}, err
		}
		if payload != nil {
			req.Header.Set("Content-Type", "application/json")
		}
		for k, v := range headers {
			req.Header.Set(k, v)
		}
		if _, err := c.l.cfg.Signer.Apply(req, principal, c.aud, legalEntityID); err != nil {
			return reply{}, fmt.Errorf("sign for %s: %w", c.aud, err)
		}
		resp, err := c.l.client.Do(req)
		switch {
		case err != nil:
			if ctx.Err() != nil {
				return reply{}, ctx.Err()
			}
			last = "no answer from the " + c.aud + " service"
		default:
			body, rerr := io.ReadAll(io.LimitReader(resp.Body, maxBody))
			_ = resp.Body.Close()
			if rerr == nil && resp.StatusCode < 500 && resp.StatusCode != http.StatusTooManyRequests {
				c.l.note(c.aud, false)
				return reply{status: resp.StatusCode, body: body}, nil
			}
			last = fmt.Sprintf("the %s service answered %d", c.aud, resp.StatusCode)
			if rerr != nil {
				last = "the " + c.aud + " service answer was cut off"
			}
		}
		if attempt < c.l.cfg.Attempts {
			wait := min(c.l.cfg.Backoff<<(attempt-1), maxBackoff)
			if err := c.l.cfg.Sleep(ctx, wait); err != nil {
				return reply{}, err
			}
		}
	}
	c.l.note(c.aud, true)
	return reply{exhausted: true, detail: clip(fmt.Sprintf("%s; gave up after %d attempts", last, c.l.cfg.Attempts))}, nil
}

// receipt is the part of a create or update answer the loader keeps.
type receipt struct {
	ID      string `json:"id"`
	Version int    `json:"version"`
}

func parseReceipt(body []byte) receipt {
	var r receipt
	_ = json.Unmarshal(body, &r)
	return r
}

func etag(version int) string { return `"` + strconv.Itoa(version) + `"` }
