// Package forecastsrc feeds the cash forecast from the subledgers (FR-36.9): payables open items are cash going
// out on their due date, receivables open items cash coming in. Each read is live, so the forecast follows the
// subledger and nothing is stored twice.
package forecastsrc

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/platform/tenant"
	"github.com/BenjaminSRussell/passage/services/treasury/internal/domain"
)

const (
	TypeAP = "AP"
	TypeAR = "AR"

	maxBody    = 8 << 20
	maxPages   = 25
	pageSize   = 200
	arPageSize = 500 // receivables answers one page of at most 500 open items by due date
)

// Result is what one source contributed and how complete it is.
type Result struct {
	Contributions []domain.Contribution
	// Truncated reports that the subledger holds more items than were read; the forecast is then understated.
	Truncated bool
}

// Source is one live feed of the cash forecast.
type Source interface {
	// Type is the forecast source type the contributions carry (AP or AR).
	Type() string
	// Contribute reads the legal entity's open items due from `from` to `to`, both inclusive.
	Contribute(ctx context.Context, legalEntityID string, from, to time.Time) (Result, error)
}

type base struct {
	baseURL  string
	audience string
	duty     string
	http     *http.Client
	signer   *tenant.ServiceSigner
}

func newBase(baseURL, audience, duty string, signer *tenant.ServiceSigner, c *http.Client) base {
	if c == nil {
		c = &http.Client{Timeout: 10 * time.Second}
	}
	return base{baseURL: strings.TrimRight(baseURL, "/"), audience: audience, duty: duty, http: c, signer: signer}
}

func (b base) get(ctx context.Context, legalEntityID, path string, q url.Values, into any) error {
	p, ok := tenant.FromContext(ctx)
	if !ok {
		return fmt.Errorf("%s forecast read: no principal in context", b.audience)
	}
	if b.signer == nil {
		return fmt.Errorf("%s forecast read: no service signing key configured", b.audience)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, b.baseURL+path+"?"+q.Encode(), nil)
	if err != nil {
		return err
	}
	req.Header.Set("Accept", "application/json")
	if _, err := b.signer.ApplyDuties(req, p, b.audience, legalEntityID, []string{b.duty}); err != nil {
		return fmt.Errorf("%s forecast read: %w", b.audience, err)
	}
	resp, err := b.http.Do(req)
	if err != nil {
		return fmt.Errorf("%s forecast read: %w", b.audience, err)
	}
	defer func() { _ = resp.Body.Close() }()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, maxBody))
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("%s forecast read: status %d", b.audience, resp.StatusCode)
	}
	return json.Unmarshal(raw, into)
}

func day(s string) (time.Time, bool) {
	t, err := time.Parse("2006-01-02", s)
	return t, err == nil
}

// Payables reads open vendor items.
type Payables struct{ base }

// NewPayables returns the AP source over the payables service at baseURL.
func NewPayables(baseURL string, signer *tenant.ServiceSigner, c *http.Client) *Payables {
	return &Payables{newBase(baseURL, "payables", "payables.forecast.read", signer, c)}
}

func (*Payables) Type() string { return TypeAP }

type apItem struct {
	ID         string `json:"id"`
	DueDate    string `json:"dueDate"`
	Currency   string `json:"currency"`
	OpenAmount string `json:"openAmount"`
	Status     string `json:"status"`
}

// Contribute reads the OPEN and PARTIALLY_SETTLED items due in the window, a page of 200 at a time (at most 25
// pages per status), as outflows of their open amount in the item's currency on the due date.
func (p *Payables) Contribute(ctx context.Context, legalEntityID string, from, to time.Time) (Result, error) {
	var res Result
	for _, status := range []string{"OPEN", "PARTIALLY_SETTLED"} {
		cursor := ""
		for page := 0; ; page++ {
			if page == maxPages {
				res.Truncated = true
				break
			}
			q := url.Values{"legal_entity": {legalEntityID}, "status": {status}, "due_from": {from.Format("2006-01-02")}, "due_to": {to.Format("2006-01-02")}, "limit": {fmt.Sprint(pageSize)}}
			if cursor != "" {
				q.Set("cursor", cursor)
			}
			var body struct {
				Items      []apItem `json:"items"`
				NextCursor string   `json:"nextCursor"`
			}
			if err := p.get(ctx, legalEntityID, "/v1/payables/open-items", q, &body); err != nil {
				return Result{}, err
			}
			for _, it := range body.Items {
				amount, err := decimal.NewFromString(it.OpenAmount)
				due, ok := day(it.DueDate)
				if err != nil || !ok || !amount.IsPositive() || it.Currency == "" {
					continue
				}
				res.Contributions = append(res.Contributions, domain.Contribution{SourceType: TypeAP, SourceID: it.ID, ExpectedDate: due, Amount: amount.Neg(), Currency: it.Currency})
			}
			if body.NextCursor == "" {
				break
			}
			cursor = body.NextCursor
		}
	}
	return res, nil
}

// Receivables reads open customer items.
type Receivables struct{ base }

// NewReceivables returns the AR source over the receivables service at baseURL.
func NewReceivables(baseURL string, signer *tenant.ServiceSigner, c *http.Client) *Receivables {
	return &Receivables{newBase(baseURL, "receivables", "receivables.forecast.read", signer, c)}
}

func (*Receivables) Type() string { return TypeAR }

type arItem struct {
	ID         string `json:"id"`
	DueDate    string `json:"dueDate"`
	Currency   string `json:"currency"`
	OpenAmount string `json:"openAmount"`
}

// Contribute reads the open items (receivables answers one page of at most 500 by due date and has no date filter, so
// the window is applied here) as inflows of their signed open amount: a credit note or unapplied payment is negative.
func (r *Receivables) Contribute(ctx context.Context, legalEntityID string, from, to time.Time) (Result, error) {
	var body struct {
		Items []arItem `json:"items"`
	}
	q := url.Values{"status": {"open"}, "limit": {fmt.Sprint(arPageSize)}}
	if err := r.get(ctx, legalEntityID, "/v1/receivables/open-items", q, &body); err != nil {
		return Result{}, err
	}
	res := Result{Truncated: len(body.Items) >= arPageSize}
	for _, it := range body.Items {
		amount, err := decimal.NewFromString(it.OpenAmount)
		due, ok := day(it.DueDate)
		if err != nil || !ok || amount.IsZero() || it.Currency == "" || due.Before(from) || due.After(to) {
			continue
		}
		res.Contributions = append(res.Contributions, domain.Contribution{SourceType: TypeAR, SourceID: it.ID, ExpectedDate: due, Amount: amount, Currency: it.Currency})
	}
	return res, nil
}
