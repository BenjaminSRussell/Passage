package consumer

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
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

var (
	currencyPattern = regexp.MustCompile(`^[A-Z]{3}$`)
	uuidPattern     = regexp.MustCompile(`^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$`)
)

const amountScale = 6

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
	for field, id := range map[string]string{"tenant_id": evt.TenantID, "invoice_id": d.InvoiceID, "legal_entity_id": d.LegalEntityID, "vendor_id": d.VendorID} {
		if !uuidPattern.MatchString(id) {
			return fmt.Errorf("%w: %s %q is not a uuid", ErrInvalidEvent, field, id)
		}
	}
	if d.InvoiceNumber == "" || !currencyPattern.MatchString(d.Currency) {
		return fmt.Errorf("%w: invoice_number or currency %q", ErrInvalidEvent, d.Currency)
	}
	total, err := decimal.NewFromString(d.Total)
	if err != nil || !total.IsPositive() || !total.Equal(total.Round(amountScale)) {
		return fmt.Errorf("%w: total %q", ErrInvalidEvent, d.Total)
	}
	discount := decimal.Zero
	if d.DiscountAmt != "" {
		if discount, err = decimal.NewFromString(d.DiscountAmt); err != nil || discount.IsNegative() || discount.GreaterThan(total) || !discount.Equal(discount.Round(amountScale)) {
			return fmt.Errorf("%w: discount_amount %q", ErrInvalidEvent, d.DiscountAmt)
		}
	}
	if d.DiscountDate != nil && *d.DiscountDate == "" {
		d.DiscountDate = nil
	}
	if (d.DiscountDate != nil) != (d.DiscountAmt != "") {
		return fmt.Errorf("%w: discount_date and discount_amount must be given together", ErrInvalidEvent)
	}
	if d.DiscountDate != nil {
		if _, err := time.Parse(time.DateOnly, *d.DiscountDate); err != nil {
			return fmt.Errorf("%w: discount_date %q", ErrInvalidEvent, *d.DiscountDate)
		}
	}
	due, dueSource := d.DueDate, store.DueDateEvent
	if due == "" {
		due, dueSource = d.InvoiceDate, store.DueDateDefaulted
	}
	for _, day := range []string{d.InvoiceDate, due} {
		if _, err := time.Parse(time.DateOnly, day); err != nil {
			return fmt.Errorf("%w: date %q", ErrInvalidEvent, day)
		}
	}
	ctx = tenant.With(ctx, tenant.Principal{TenantID: evt.TenantID, LegalEntityID: d.LegalEntityID, UserID: "system:invoice.posted"})
	_, _, err = items.CreateFromInvoice(ctx, store.OpenItem{
		LegalEntityID: d.LegalEntityID, VendorID: d.VendorID, SourceInvoiceID: d.InvoiceID, InvoiceNumber: d.InvoiceNumber,
		InvoiceDate: d.InvoiceDate, DueDate: due, DueDateSource: dueSource, Currency: d.Currency, Amount: total, DiscountDate: d.DiscountDate, DiscountAmount: discount,
	})
	return err
}
