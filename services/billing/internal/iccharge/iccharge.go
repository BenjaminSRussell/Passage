// Package iccharge raises the lender's sales invoice of an intercompany charge in Invoice settlement mode (#40): intercompany
// publishes dev.passage.interco.chargeinvoice.requested.v1 instead of posting the charge, and this consumer creates a FREE_TEXT
// invoice proposal in the lender's legal entity for the borrower (the pairing's customer account): one line at the charge's price,
// on the configured revenue account and tax code. A person posts it from the billing workspace, as every proposal.
//
// The consumer runs as the service principal billing-iccharge, scoped to the lender's legal entity, with the one flag duty
// billing.iccharge.from-event. The proposal is keyed by the charge and its version, so a redelivered event creates nothing new.
package iccharge

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strconv"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/platform/eventing"
	"github.com/BenjaminSRussell/passage/platform/events"
	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/platform/money"
	"github.com/BenjaminSRussell/passage/platform/tenant"

	"github.com/BenjaminSRussell/passage/services/billing/internal/domain"
	"github.com/BenjaminSRussell/passage/services/billing/internal/store"
)

const (
	// ConsumerName is the consumer (and Kafka group) and the service principal it runs as, service:billing-iccharge.
	ConsumerName = "billing-iccharge"
	// Duty is the only duty of that principal.
	Duty = "billing.iccharge.from-event"
	// ChargeInvoiceRequestedType is the event this consumer handles; Topic is where intercompany's outbox routes it.
	ChargeInvoiceRequestedType = "dev.passage.interco.chargeinvoice.requested.v1"
	Source                     = "intercompany"
	Topic                      = "passage.interco.chargeinvoice"
	DeadLetterTopic            = "passage.billing.iccharge.dlq"
	maxDescription             = domain.MaxDescription
)

// Store creates the proposal; *store.Postgres implements it.
type Store interface {
	FindByIdempotencyKey(ctx context.Context, key string) (store.Invoice, bool, error)
	CreateProposal(ctx context.Context, inv store.Invoice) (store.Invoice, error)
}

// Tax quotes the line's tax; the API's TaxEngine implements it.
type Tax interface {
	Quote(ctx context.Context, currency string, lines []domain.Line) ([]decimal.Decimal, error)
}

// Config is what an intercompany invoice is booked with: the revenue account of the line, the tax code when the charge names none,
// and the payment term in days.
type Config struct {
	RevenueAccount  string
	TaxCode         string
	PaymentTermDays int
}

// Data is the part of the event this consumer reads.
type Data struct {
	ID                  string `json:"id"`
	SourceID            string `json:"sourceId"`
	Version             int    `json:"version"`
	Lender              string `json:"lender"`
	Borrower            string `json:"borrower"`
	Category            string `json:"category"`
	Date                string `json:"transactionDate"`
	Currency            string `json:"currency"`
	Price               string `json:"price"`
	TaxCode             string `json:"taxCode"`
	LenderLegalEntityID string `json:"lenderLegalEntityId"`
	CustomerAccount     string `json:"customerAccount"`
}

// Key is the Idempotency-Key of the proposal of a charge version.
func Key(d Data) string { return store.ICChargeKeyPrefix + d.ID + ":" + strconv.Itoa(d.Version) }

// Proposal builds the invoice proposal of a charge, or the reason it cannot be raised (no retry changes it).
func Proposal(d Data, cfg Config) (store.Invoice, domain.Line, error) {
	switch {
	case !ids.Valid(d.ID) || d.Version < 1:
		return store.Invoice{}, domain.Line{}, errors.New("the charge has no id or version")
	case !ids.Valid(d.LenderLegalEntityID):
		return store.Invoice{}, domain.Line{}, fmt.Errorf("lender %q has no legal entity in the intercompany registry", d.Lender)
	case !ids.Valid(d.CustomerAccount):
		return store.Invoice{}, domain.Line{}, fmt.Errorf("the pairing %s -> %s names no customer account that is a customer id", d.Lender, d.Borrower)
	case len(d.Currency) != 3:
		return store.Invoice{}, domain.Line{}, errors.New("the charge has no currency")
	}
	date, err := time.Parse(time.DateOnly, d.Date)
	if err != nil {
		return store.Invoice{}, domain.Line{}, errors.New("the charge has no transaction date")
	}
	price, err := decimal.NewFromString(d.Price)
	if err != nil || price.IsNegative() {
		return store.Invoice{}, domain.Line{}, errors.New("the charge has no price")
	}
	taxCode := d.TaxCode
	if taxCode == "" {
		taxCode = cfg.TaxCode
	}
	desc := fmt.Sprintf("Intercompany %s charge %s from %s to %s", d.Category, d.SourceID, d.Lender, d.Borrower)
	if r := []rune(desc); len(r) > maxDescription {
		desc = string(r[:maxDescription])
	}
	line := domain.Line{No: 1, Description: desc, Qty: decimal.NewFromInt(1), UnitPrice: price.Round(money.MinorUnits(d.Currency)),
		RevenueAccount: cfg.RevenueAccount, TaxCode: taxCode}
	if err := line.Validate(); err != nil {
		return store.Invoice{}, domain.Line{}, err
	}
	inv := store.Invoice{ID: ids.New(), LegalEntityID: d.LenderLegalEntityID, CustomerID: d.CustomerAccount, InvoiceAccountID: d.CustomerAccount,
		Type: domain.TypeFreeText, Status: domain.StatusProposal, Currency: d.Currency, InvoiceDate: date,
		DueDate: date.AddDate(0, 0, cfg.PaymentTermDays), PaymentTermDays: cfg.PaymentTermDays, IdempotencyKey: Key(d), RequestHash: Key(d)}
	return inv, line, nil
}

// Handlers returns the consumer's handler. Register it with a consumer named ConsumerName with Idempotent set.
func Handlers(s Store, tax Tax, cfg Config) map[string]eventing.Handler {
	return map[string]eventing.Handler{
		ChargeInvoiceRequestedType: eventing.HandlerFunc(func(ctx context.Context, _ pgx.Tx, evt events.Event) error {
			_, err := Handle(ctx, s, tax, cfg, evt)
			return err
		}),
	}
}

// Handle raises the proposal of one event and returns it (the one already raised on a redelivery).
func Handle(ctx context.Context, s Store, tax Tax, cfg Config, evt events.Event) (store.Invoice, error) {
	actor, ok := tenant.FromContext(ctx)
	if !ok || !actor.HasDuty(Duty) {
		return store.Invoice{}, eventing.Permanent(errors.New("ic charge consumer has no principal with " + Duty))
	}
	if evt.Source != Source {
		return store.Invoice{}, eventing.Permanent(fmt.Errorf("%s from source %q, want %q", evt.Type, evt.Source, Source))
	}
	var d Data
	if err := json.Unmarshal(evt.Data, &d); err != nil {
		return store.Invoice{}, eventing.Permanent(fmt.Errorf("chargeinvoice.requested data: %w", err))
	}
	inv, line, err := Proposal(d, cfg)
	if err != nil {
		return store.Invoice{}, eventing.Permanent(err)
	}
	scoped, err := tenant.ServicePrincipal(ConsumerName, actor.TenantID, d.LenderLegalEntityID)
	if err != nil {
		return store.Invoice{}, eventing.Permanent(err)
	}
	ctx = tenant.With(ctx, scoped)
	if existing, found, err := s.FindByIdempotencyKey(ctx, inv.IdempotencyKey); err != nil || found {
		return existing, err
	}
	taxes, err := tax.Quote(ctx, inv.Currency, []domain.Line{line})
	if err != nil {
		return store.Invoice{}, err
	}
	nets, totals, err := domain.Compute(inv.Currency, []domain.Line{line}, taxes, decimal.Zero)
	if err != nil {
		return store.Invoice{}, eventing.Permanent(err)
	}
	inv.Lines = []store.Line{{Line: line, Net: nets[0], Tax: taxes[0].RoundBank(money.MinorUnits(inv.Currency))}}
	inv.Totals = totals
	created, err := s.CreateProposal(ctx, inv)
	if err != nil {
		return store.Invoice{}, err
	}
	slog.InfoContext(ctx, "intercompany invoice proposal raised", "charge", d.ID, "version", d.Version, "invoice", created.ID, "legalEntity", d.LenderLegalEntityID)
	return created, nil
}
