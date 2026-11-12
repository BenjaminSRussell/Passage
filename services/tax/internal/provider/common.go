// Package provider holds the external tax provider adapters (13 deliverable 4): Avalara AvaTax and Vertex O Series. Each
// implements domain.TaxProvider over HTTPS with credentials from the environment. A 4xx answer is a *domain.ProviderError the
// failover does not mask (the provider refused the document); a 5xx, a timeout or a transport error is not, so the internal
// engine takes over and the recommit queue keeps the document.
package provider

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/services/tax/internal/domain"
)

const (
	maxResponseSize = 4 << 20
	maxErrorDetail  = 300
	defaultTimeout  = 10 * time.Second
)

var hundred = decimal.NewFromInt(100)

type client struct {
	base string
	http *http.Client
}

// requireHTTPS refuses a provider URL that would send credentials and tax data in the clear: it must be an absolute
// https URL with a host. http is accepted only for a loopback host (a local stub or a test server), never for a name.
func requireHTTPS(what, raw string) error {
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" {
		return fmt.Errorf("%s must be an absolute https URL", what)
	}
	switch u.Scheme {
	case "https":
		return nil
	case "http":
		if ip := net.ParseIP(u.Hostname()); u.Hostname() == "localhost" || (ip != nil && ip.IsLoopback()) {
			return nil
		}
	}
	return fmt.Errorf("%s must use https (got %q)", what, u.Scheme)
}

func newClient(base string, hc *http.Client) (client, error) {
	if base == "" {
		return client{}, errors.New("provider needs a base URL")
	}
	if err := requireHTTPS("the provider base URL", base); err != nil {
		return client{}, err
	}
	if hc == nil {
		hc = &http.Client{Timeout: defaultTimeout, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	}
	return client{base: strings.TrimRight(base, "/"), http: hc}, nil
}

// do sends one JSON request and decodes a 2xx answer into out (when not nil). Any other status becomes a ProviderError whose
// message holds at most maxErrorDetail characters of the answer and never the request.
func (c client) do(ctx context.Context, method, path string, auth func(*http.Request), in, out any) error {
	var body io.Reader
	if in != nil {
		raw, err := json.Marshal(in)
		if err != nil {
			return err
		}
		body = bytes.NewReader(raw)
	}
	req, err := http.NewRequestWithContext(ctx, method, c.base+path, body)
	if err != nil {
		return err
	}
	req.Header.Set("Accept", "application/json")
	if in != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if auth != nil {
		auth(req)
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("provider request: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, maxResponseSize))
	if err != nil {
		return fmt.Errorf("read provider response: %w", err)
	}
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		detail := strings.TrimSpace(string(raw))
		return &domain.ProviderError{Status: resp.StatusCode, Err: errors.New(detail[:min(len(detail), maxErrorDetail)])}
	}
	if out == nil {
		return nil
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	if err := dec.Decode(out); err != nil {
		return fmt.Errorf("decode provider response: %w", err)
	}
	return nil
}

func num(n json.Number) decimal.Decimal {
	d, err := decimal.NewFromString(n.String())
	if err != nil {
		return decimal.Zero
	}
	return d
}

func amount(d decimal.Decimal, exp int32) domain.Dec { return domain.Dec{Decimal: d.Round(exp)} }

// assemble totals a provider's per-line taxes into the engine's Result shape: the document totals per code and direction are the
// sum of the line legs.
func assemble(engine string, lines []domain.LineResult) domain.Result {
	res := domain.Result{Engine: engine, Lines: lines, Warnings: []domain.Warning{}, DocumentTaxesByCode: []domain.CodeTotal{},
		DeterminationTrace: []string{"calculated by " + engine}}
	index := map[string]int{}
	for _, l := range lines {
		for _, t := range l.Taxes {
			key := t.Code + "|" + t.Direction
			i, ok := index[key]
			if !ok {
				i = len(res.DocumentTaxesByCode)
				index[key] = i
				res.DocumentTaxesByCode = append(res.DocumentTaxesByCode, domain.CodeTotal{Code: t.Code, Direction: t.Direction})
			}
			total := &res.DocumentTaxesByCode[i]
			total.Base = domain.Dec{Decimal: total.Base.Add(l.Base.Decimal)}
			total.TaxAmount = domain.Dec{Decimal: total.TaxAmount.Add(t.TaxAmount.Decimal)}
		}
	}
	return res
}
