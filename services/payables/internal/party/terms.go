package party

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"

	"github.com/go-chi/chi/v5/middleware"
	"github.com/shopspring/decimal"
)

const (
	termDatesPath  = "/v1/party/payment-terms/%s:due-dates"
	dutyTermLookup = "party.vendor.lookup"
)

var ErrTermNotFound = errors.New("party: payment term not found")

type DiscountDate struct {
	Percent decimal.Decimal `json:"percent"`
	Date    string          `json:"date"`
}

type TermDates struct {
	DueDate   string         `json:"dueDate"`
	Discounts []DiscountDate `json:"discounts"`
}

func (c *Client) TermDates(ctx context.Context, legalEntityID, termID, documentDate string) (TermDates, error) {
	payload, err := json.Marshal(map[string]string{"documentDate": documentDate})
	if err != nil {
		return TermDates{}, fmt.Errorf("party: %w", err)
	}
	raw, err := c.call(ctx, legalEntityID, http.MethodPost, fmt.Sprintf(termDatesPath, url.PathEscape(termID)), payload, dutyTermLookup, ErrTermNotFound)
	if err != nil {
		return TermDates{}, err
	}
	var dates TermDates
	if err := json.Unmarshal(raw, &dates); err != nil {
		slog.ErrorContext(ctx, "party term dates response unusable", "err", err, "termId", termID, "correlationId", middleware.GetReqID(ctx))
		return TermDates{}, errors.New("party: response is not a due date result")
	}
	return dates, nil
}
