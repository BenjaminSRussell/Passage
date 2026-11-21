package ledger

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"slices"
	"time"

	"github.com/BenjaminSRussell/depot/platform/ledgerclient"
	"github.com/BenjaminSRussell/depot/platform/tenant"
	"github.com/BenjaminSRussell/depot/services/receiving/internal/domain"
)

const (
	Timeout             = 3 * time.Second
	idempotencyConflict = "LEDGER_IDEMPOTENCY_CONFLICT"
	dateFormat          = "2006-01-02"
	sourceSystem        = "receiving"
	reversalDescription = "reversal of a receiving posting that was never confirmed locally"
)

var periodClosedCodes = []string{"period_closed", "LEDGER_PERIOD_CLOSED"}

type Client struct {
	baseURL string
	http    *http.Client
	poster  *ledgerclient.Client
	signErr error
}

func New(baseURL string, httpClient *http.Client) *Client {
	if httpClient == nil {
		httpClient = &http.Client{Timeout: Timeout}
	}
	return &Client{baseURL: baseURL, http: httpClient, signErr: ledgerclient.ErrUnsigned}
}

func (c *Client) WithSigner(s *tenant.ServiceSigner) *Client {
	c.poster, c.signErr = ledgerclient.New(c.baseURL, sourceSystem, s, c.http)
	return c
}

func periodClosed(failure *ledgerclient.Error) bool {
	return slices.Contains(periodClosedCodes, failure.Code) || slices.ContainsFunc(failure.ViolationCodes, func(code string) bool {
		return slices.Contains(periodClosedCodes, code)
	})
}

func classify(err error) error {
	var failure *ledgerclient.Error
	if !errors.As(err, &failure) {
		return err
	}
	switch {
	case failure.Status == http.StatusUnprocessableEntity && periodClosed(failure):
		return fmt.Errorf("%w: %w", domain.ErrPeriodClosed, err)
	case failure.Status == http.StatusConflict && failure.Code == idempotencyConflict:
		return fmt.Errorf("%w: %w", domain.ErrLedgerConflict, err)
	case ledgerclient.Rejected(err):
		return fmt.Errorf("%w: %w", domain.ErrLedgerRejected, err)
	}
	return err
}

func toEntry(entry domain.JournalEntry) (ledgerclient.Entry, error) {
	if len(entry.Lines) == 0 {
		return ledgerclient.Entry{}, errors.New("journal entry has no lines")
	}
	currency := entry.Lines[0].Amount.Currency
	out := ledgerclient.Entry{
		LegalEntityID: entry.LegalEntityID, LedgerID: entry.LedgerID, DocumentID: entry.Source.DocumentID, PostingDate: entry.PostingDate.Format(dateFormat),
		Currency: currency, IdempotencyKey: entry.IdempotencyKey, SourceType: entry.Source.DocumentType, OptionalDimensions: entry.OptionalDimensions,
	}
	if !entry.DocumentDate.IsZero() {
		out.DocumentDate = entry.DocumentDate.Format(dateFormat)
	}
	for i, l := range entry.Lines {
		if l.Account == "" || l.Amount.Currency != currency {
			return ledgerclient.Entry{}, fmt.Errorf("journal line %d needs an account and currency %s", i+1, currency)
		}
		if l.Amount.Value.IsZero() {
			continue
		}
		out.Lines = append(out.Lines, ledgerclient.Line{Account: l.Account, Credit: l.Amount.Value.IsNegative(), Amount: l.Amount.Value.Abs(), Dimensions: l.Dimensions})
	}
	return out, nil
}

func (c *Client) Post(ctx context.Context, entry domain.JournalEntry) (string, error) {
	if c.signErr != nil {
		return "", fmt.Errorf("ledger post %s: %w", entry.IdempotencyKey, c.signErr)
	}
	body, err := toEntry(entry)
	if err != nil {
		return "", fmt.Errorf("ledger post %s: %w", entry.IdempotencyKey, err)
	}
	id, err := c.poster.Post(ctx, body)
	return id, classify(err)
}

func (c *Client) Reverse(ctx context.Context, entryID, idempotencyKey, legalEntityID string, postingDate time.Time) error {
	if c.signErr != nil {
		return fmt.Errorf("ledger reverse %s: %w", entryID, c.signErr)
	}
	_, err := c.poster.Reverse(ctx, ledgerclient.Reversal{LegalEntityID: legalEntityID, EntryID: entryID, PostingDate: postingDate.Format(dateFormat),
		Description: reversalDescription, IdempotencyKey: idempotencyKey})
	return classify(err)
}
