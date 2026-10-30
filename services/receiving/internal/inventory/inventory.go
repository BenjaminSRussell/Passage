package inventory

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5/middleware"
	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/platform/tenant"
	"github.com/BenjaminSRussell/passage/services/receiving/internal/domain"
	"github.com/BenjaminSRussell/passage/services/receiving/internal/identity"
)

const (
	Timeout         = 2 * time.Second
	pageSize        = 200
	maxPages        = 50
	maxResponseSize = 1 << 20
	ownershipType   = "OWNERSHIP_CHANGE"
	postedStatus    = "POSTED"
	ownerDimension  = "owner"
	dateLayout      = "2006-01-02"
)

type Client struct {
	baseURL string
	http    *http.Client
}

func New(baseURL string, httpClient *http.Client) *Client {
	if httpClient == nil {
		httpClient = &http.Client{Timeout: Timeout, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	}
	return &Client{baseURL: strings.TrimRight(baseURL, "/"), http: httpClient}
}

type summaryBody struct {
	ID            string `json:"id"`
	LegalEntityID string `json:"legalEntityId"`
}

type journalBody struct {
	ID            string `json:"id"`
	LegalEntityID string `json:"legalEntityId"`
	JournalType   string `json:"journalType"`
	Status        string `json:"status"`
	PostingDate   string `json:"postingDate"`
	Currency      string `json:"currency"`
	Lines         []struct {
		Dimensions map[string]string `json:"dimensions"`
	} `json:"lines"`
	GLEntry *struct {
		Currency string `json:"currency"`
		Lines    []struct {
			Account string          `json:"account"`
			Debit   decimal.Decimal `json:"debit"`
			Credit  decimal.Decimal `json:"credit"`
		} `json:"lines"`
	} `json:"glEntry"`
}

func (c *Client) get(ctx context.Context, path string) (int, []byte, error) {
	principal, ok := tenant.FromContext(ctx)
	if !ok {
		return 0, nil, errors.New("inventory get: no principal in context")
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.baseURL+path, nil)
	if err != nil {
		return 0, nil, fmt.Errorf("inventory get %s: %w", path, err)
	}
	identity.Forward(req, principal)
	resp, err := c.http.Do(req)
	if err != nil {
		slog.ErrorContext(ctx, "inventory get transport failed", "err", err, "path", path, "correlationId", middleware.GetReqID(ctx))
		return 0, nil, fmt.Errorf("inventory get %s: %w", path, err)
	}
	defer func() { _ = resp.Body.Close() }()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, maxResponseSize))
	if err != nil {
		slog.ErrorContext(ctx, "inventory get read failed", "err", err, "path", path, "correlationId", middleware.GetReqID(ctx))
		return 0, nil, fmt.Errorf("inventory get %s: %w", path, err)
	}
	switch resp.StatusCode {
	case http.StatusOK:
		return resp.StatusCode, raw, nil
	case http.StatusNotFound:
		return resp.StatusCode, nil, fmt.Errorf("inventory get %s: %w", path, domain.ErrOwnershipChangeNotFound)
	case http.StatusForbidden:
		return resp.StatusCode, nil, fmt.Errorf("inventory get %s: %w", path, domain.ErrInventoryForbidden)
	}
	err = fmt.Errorf("inventory get %s: unexpected status %d", path, resp.StatusCode)
	slog.ErrorContext(ctx, "inventory get rejected", "err", err, "status", resp.StatusCode, "path", path, "correlationId", middleware.GetReqID(ctx))
	return resp.StatusCode, nil, err
}

func (c *Client) List(ctx context.Context, legalEntityID string) ([]string, bool, error) {
	var out []string
	cursor := ""
	for range maxPages {
		query := url.Values{"type": {ownershipType}, "status": {postedStatus}, "limit": {strconv.Itoa(pageSize)}}
		if cursor != "" {
			query.Set("cursor", cursor)
		}
		_, raw, err := c.get(ctx, domain.InventoryJournalsPath+"?"+query.Encode())
		if err != nil {
			return nil, false, err
		}
		var page struct {
			Items      []summaryBody `json:"items"`
			NextCursor string        `json:"nextCursor"`
		}
		if err := json.Unmarshal(raw, &page); err != nil {
			slog.ErrorContext(ctx, "inventory journal list unusable", "err", err, "correlationId", middleware.GetReqID(ctx))
			return nil, false, errors.New("inventory journal list: response is not a journal page")
		}
		for _, item := range page.Items {
			if item.LegalEntityID == legalEntityID && item.ID != "" {
				out = append(out, item.ID)
			}
		}
		if page.NextCursor == "" {
			return out, false, nil
		}
		cursor = page.NextCursor
	}
	slog.WarnContext(ctx, "inventory journal list truncated", "pages", maxPages, "correlationId", middleware.GetReqID(ctx))
	return out, true, nil
}

func (c *Client) Get(ctx context.Context, journalID string) (domain.OwnershipChange, error) {
	_, raw, err := c.get(ctx, fmt.Sprintf(domain.InventoryJournalPathTmpl, url.PathEscape(journalID)))
	if err != nil {
		return domain.OwnershipChange{}, err
	}
	var j journalBody
	if err := json.Unmarshal(raw, &j); err != nil {
		slog.ErrorContext(ctx, "inventory journal unusable", "err", err, "journalId", journalID, "correlationId", middleware.GetReqID(ctx))
		return domain.OwnershipChange{}, fmt.Errorf("inventory journal %s: response is not a journal", journalID)
	}
	change, err := j.toDomain()
	if err != nil {
		slog.ErrorContext(ctx, "inventory journal unusable", "err", err, "journalId", journalID, "correlationId", middleware.GetReqID(ctx))
		return domain.OwnershipChange{}, fmt.Errorf("inventory journal %s: %w", journalID, err)
	}
	return change, nil
}

func (j journalBody) toDomain() (domain.OwnershipChange, error) {
	if j.JournalType != ownershipType || j.Status != postedStatus || j.ID == "" {
		return domain.OwnershipChange{}, errors.New("not a posted ownership change")
	}
	posted, err := time.Parse(dateLayout, j.PostingDate)
	if err != nil || len(j.Currency) != 3 {
		return domain.OwnershipChange{}, errors.New("no usable posting date or currency")
	}
	vendor := ""
	for _, l := range j.Lines {
		owner := strings.TrimSpace(l.Dimensions[ownerDimension])
		if owner == "" || (vendor != "" && owner != vendor) {
			return domain.OwnershipChange{}, errors.New("lines do not name exactly one vendor")
		}
		vendor = owner
	}
	if vendor == "" || j.GLEntry == nil {
		return domain.OwnershipChange{}, errors.New("no vendor or no GL entry")
	}
	accrual, debits, credits := "", decimal.Zero, decimal.Zero
	for _, l := range j.GLEntry.Lines {
		debits = debits.Add(l.Debit)
		if l.Credit.IsPositive() {
			if accrual != "" && accrual != l.Account {
				return domain.OwnershipChange{}, errors.New("GL entry credits more than one account")
			}
			accrual = l.Account
			credits = credits.Add(l.Credit)
		}
	}
	if accrual == "" || !credits.Equal(debits) {
		return domain.OwnershipChange{}, errors.New("GL entry has no accrual credit or does not balance")
	}
	return domain.OwnershipChange{
		JournalID: j.ID, LegalEntityID: j.LegalEntityID, VendorID: vendor, Currency: j.Currency,
		AccrualAccount: accrual, PostingDate: posted, Amount: credits,
	}, nil
}
