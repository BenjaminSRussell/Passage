package consumer

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/platform/events"
	"github.com/BenjaminSRussell/passage/platform/tenant"
	"github.com/BenjaminSRussell/passage/services/payables/internal/store"
)

var (
	InvoicePostedType = events.Type("payables", "invoice", "posted", 1)
	ErrInvalidEvent   = errors.New("invalid invoice.posted event")
)

type invoicePosted struct {
	InvoiceID     string  `json:"invoice_id"`
	LegalEntityID string  `json:"legal_entity_id"`
	VendorID      string  `json:"vendor_id"`
	InvoiceNumber string  `json:"invoice_number"`
	InvoiceDate   string  `json:"invoice_date"`
	DueDate       string  `json:"due_date"`
	DiscountDate  *string `json:"discount_date"`
	DiscountAmt   string  `json:"discount_amount"`
	Currency      string  `json:"currency"`
	Total         string  `json:"total"`
}

func HandleInvoicePosted(ctx context.Context, items *store.Postgres, evt events.Event) error {
	if evt.Type != InvoicePostedType {
		return fmt.Errorf("%w: type %q", ErrInvalidEvent, evt.Type)
	}
	var d invoicePosted
	if err := json.Unmarshal(evt.Data, &d); err != nil {
		return fmt.Errorf("%w: %v", ErrInvalidEvent, err)
	}
	total, err := decimal.NewFromString(d.Total)
	if err != nil || !total.IsPositive() {
		return fmt.Errorf("%w: total %q", ErrInvalidEvent, d.Total)
	}
	discount := decimal.Zero
	if d.DiscountAmt != "" {
		if discount, err = decimal.NewFromString(d.DiscountAmt); err != nil || discount.IsNegative() || discount.GreaterThan(total) {
			return fmt.Errorf("%w: discount_amount %q", ErrInvalidEvent, d.DiscountAmt)
		}
	}
	due := d.DueDate
	if due == "" {
		due = d.InvoiceDate
	}
	for _, day := range []string{d.InvoiceDate, due} {
		if _, err := time.Parse(time.DateOnly, day); err != nil {
			return fmt.Errorf("%w: date %q", ErrInvalidEvent, day)
		}
	}
	if evt.TenantID == "" || d.InvoiceID == "" || d.LegalEntityID == "" || d.VendorID == "" || d.InvoiceNumber == "" || len(d.Currency) != 3 {
		return fmt.Errorf("%w: missing identity fields", ErrInvalidEvent)
	}
	ctx = tenant.With(ctx, tenant.Principal{TenantID: evt.TenantID, LegalEntityID: d.LegalEntityID, UserID: "system:invoice.posted"})
	_, _, err = items.CreateFromInvoice(ctx, store.OpenItem{
		LegalEntityID: d.LegalEntityID, VendorID: d.VendorID, SourceInvoiceID: d.InvoiceID, InvoiceNumber: d.InvoiceNumber,
		InvoiceDate: d.InvoiceDate, DueDate: due, Currency: d.Currency, Amount: total, DiscountDate: d.DiscountDate, DiscountAmount: discount,
	})
	return err
}
