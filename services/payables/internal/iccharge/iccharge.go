// Package iccharge takes the borrower's vendor invoice of an intercompany charge in Invoice settlement mode into payables'
// vendor-invoice intake (#40): intercompany publishes dev.passage.interco.chargeinvoice.requested.v1 instead of posting the charge,
// and this consumer records the invoice in the borrower's legal entity for the lender (the pairing's vendor account), at the
// charge's price plus the cross-border tax intercompany worked out. Like any intake row it posts nothing: an AP clerk matches it
// and posts it.
//
// The consumer runs as the service principal payables-iccharge, scoped to the borrower's legal entity, with the one flag duty
// payables.iccharge.from-event. The row is keyed by the charge and its version, so a redelivered event creates nothing new.
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

	"github.com/BenjaminSRussell/passage/services/payables/internal/store"
)

const (
	// ConsumerName is the consumer (and Kafka group) and the service principal it runs as, service:payables-iccharge.
	ConsumerName = "payables-iccharge"
	// Duty is the only duty of that principal.
	Duty                       = "payables.iccharge.from-event"
	ChargeInvoiceRequestedType = "dev.passage.interco.chargeinvoice.requested.v1"
	Source                     = "intercompany"
	Topic                      = "passage.interco.chargeinvoice"
	DeadLetterTopic            = "passage.payables.iccharge.dlq"
	intakeSource               = "INTERCOMPANY"
)

// Store records the intake row; *store.Postgres implements it.
type Store interface {
	ReceiveVendorInvoice(ctx context.Context, in store.VendorInvoiceIntake) (store.VendorInvoiceIntake, bool, error)
}

// Data is the part of the event this consumer reads.
type Data struct {
	ID                    string `json:"id"`
	SourceID              string `json:"sourceId"`
	Version               int    `json:"version"`
	Lender                string `json:"lender"`
	Borrower              string `json:"borrower"`
	Category              string `json:"category"`
	Date                  string `json:"transactionDate"`
	Currency              string `json:"currency"`
	Price                 string `json:"price"`
	Tax                   string `json:"tax"`
	BorrowerLegalEntityID string `json:"borrowerLegalEntityId"`
	VendorAccount         string `json:"vendorAccount"`
}

// Intake builds the intake row of a charge, or the reason it cannot be taken in (no retry changes it).
func Intake(d Data) (store.VendorInvoiceIntake, error) {
	switch {
	case !ids.Valid(d.ID) || d.Version < 1:
		return store.VendorInvoiceIntake{}, errors.New("the charge has no id or version")
	case !ids.Valid(d.BorrowerLegalEntityID):
		return store.VendorInvoiceIntake{}, fmt.Errorf("borrower %q has no legal entity in the intercompany registry", d.Borrower)
	case !ids.Valid(d.VendorAccount):
		return store.VendorInvoiceIntake{}, fmt.Errorf("the pairing %s -> %s names no vendor account that is a vendor id", d.Lender, d.Borrower)
	case len(d.Currency) != 3:
		return store.VendorInvoiceIntake{}, errors.New("the charge has no currency")
	}
	if _, err := time.Parse(time.DateOnly, d.Date); err != nil {
		return store.VendorInvoiceIntake{}, errors.New("the charge has no transaction date")
	}
	price, err := decimal.NewFromString(d.Price)
	if err != nil || price.IsNegative() {
		return store.VendorInvoiceIntake{}, errors.New("the charge has no price")
	}
	tax := decimal.Zero
	if d.Tax != "" {
		if tax, err = decimal.NewFromString(d.Tax); err != nil || tax.IsNegative() {
			return store.VendorInvoiceIntake{}, errors.New("the charge's tax is not an amount")
		}
	}
	ref := d.ID + ":" + strconv.Itoa(d.Version)
	number := "IC-" + d.SourceID + "-v" + strconv.Itoa(d.Version)
	if d.SourceID == "" || len(number) > 100 {
		number = "IC-" + ref
	}
	net := price.Round(money.MinorUnits(d.Currency))
	line := store.IntakeLine{LineNo: "1", Item: d.Category, Quantity: decimal.NewFromInt(1), UnitPrice: net, Amount: net}
	in := store.VendorInvoiceIntake{LegalEntityID: d.BorrowerLegalEntityID, Source: intakeSource, SourceRef: ref, VendorID: d.VendorAccount,
		InvoiceNumber: number, InvoiceDate: d.Date, Purpose: "standard", Currency: d.Currency, Net: net, Gross: net.Add(tax),
		Lines: []store.IntakeLine{line}, ReceivedBy: "service:" + ConsumerName}
	return in.WithContentHash("intercompany:" + ref + ":" + net.String() + ":" + tax.String()), nil
}

// Handlers returns the consumer's handler. Register it with a consumer named ConsumerName with Idempotent set.
func Handlers(s Store) map[string]eventing.Handler {
	return map[string]eventing.Handler{
		ChargeInvoiceRequestedType: eventing.HandlerFunc(func(ctx context.Context, _ pgx.Tx, evt events.Event) error {
			_, err := Handle(ctx, s, evt)
			return err
		}),
	}
}

// Handle takes the vendor invoice of one event into the intake and returns the row (the one already there on a redelivery).
func Handle(ctx context.Context, s Store, evt events.Event) (store.VendorInvoiceIntake, error) {
	actor, ok := tenant.FromContext(ctx)
	if !ok || !actor.HasDuty(Duty) {
		return store.VendorInvoiceIntake{}, eventing.Permanent(errors.New("ic charge consumer has no principal with " + Duty))
	}
	if evt.Source != Source {
		return store.VendorInvoiceIntake{}, eventing.Permanent(fmt.Errorf("%s from source %q, want %q", evt.Type, evt.Source, Source))
	}
	var d Data
	if err := json.Unmarshal(evt.Data, &d); err != nil {
		return store.VendorInvoiceIntake{}, eventing.Permanent(fmt.Errorf("chargeinvoice.requested data: %w", err))
	}
	in, err := Intake(d)
	if err != nil {
		return store.VendorInvoiceIntake{}, eventing.Permanent(err)
	}
	scoped, err := tenant.ServicePrincipal(ConsumerName, actor.TenantID, d.BorrowerLegalEntityID)
	if err != nil {
		return store.VendorInvoiceIntake{}, eventing.Permanent(err)
	}
	out, created, err := s.ReceiveVendorInvoice(tenant.With(ctx, scoped), in)
	switch {
	case errors.Is(err, store.ErrIntakeReplayConflict), errors.Is(err, store.ErrIntakeDuplicateInvoice):
		return out, eventing.Permanent(err)
	case err != nil:
		return out, err
	}
	if created {
		slog.InfoContext(ctx, "intercompany vendor invoice taken in", "charge", d.ID, "version", d.Version, "intake", out.ID, "legalEntity", d.BorrowerLegalEntityID)
	}
	return out, nil
}
