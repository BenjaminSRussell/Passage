// Package iccharge takes the borrower's vendor invoice of an intercompany charge in Invoice settlement mode into payables'
// vendor-invoice intake (#40): intercompany publishes dev.depot.interco.chargeinvoice.requested.v1 instead of posting the charge,
// and this consumer records the invoice in the borrower's legal entity for the lender (the pairing's vendor account), at the
// charge's price plus the cross-border tax intercompany worked out. Like any intake row it posts nothing: an AP clerk matches it
// and posts it.
//
// Review stays the default. A pairing that opts in (its autoPostInvoice action flag, carried on the event as autoPostInvoice) has the
// invoice posted by this consumer: one balanced ledger entry in the borrower's ledger (debit the cost account for the net and the
// input tax account for the tax, credit the AP control account for the gross, in the charge's currency), the open AP item for that
// gross, and the intake row marked POSTED. Each step is idempotent per charge version (the ledger entry by its key, the AP item by
// the intake row's id), so a redelivery after a failure between the steps finishes the rest and never posts twice.
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
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/depot/platform/eventing"
	"github.com/BenjaminSRussell/depot/platform/events"
	"github.com/BenjaminSRussell/depot/platform/ids"
	"github.com/BenjaminSRussell/depot/platform/ledgerclient"
	"github.com/BenjaminSRussell/depot/platform/money"
	"github.com/BenjaminSRussell/depot/platform/tenant"

	"github.com/BenjaminSRussell/depot/services/payables/internal/domain"
	"github.com/BenjaminSRussell/depot/services/payables/internal/ledger"
	"github.com/BenjaminSRussell/depot/services/payables/internal/store"
)

const (
	// ConsumerName is the consumer (and Kafka group) and the service principal it runs as, service:payables-iccharge.
	ConsumerName = "payables-iccharge"
	// Duty is the only duty of that principal.
	Duty                       = "payables.iccharge.from-event"
	ChargeInvoiceRequestedType = "dev.depot.interco.chargeinvoice.requested.v1"
	// Source is the CloudEvents source intercompany publishes with (services/intercompany/internal/events.Source).
	Source          = "/depot/intercompany"
	Topic           = "depot.interco.chargeinvoice"
	DeadLetterTopic = "depot.payables.iccharge.dlq"
	intakeSource    = "INTERCOMPANY"
	entrySourceType = "IC_CHARGE_INVOICE"
	// codePeriodClosed is the ledger problem code for a posting date in a closed period.
	codePeriodClosed = "LEDGER_PERIOD_CLOSED"
)

// Store records the intake row and, for a pairing that opted in, its posting; *store.Postgres implements it.
type Store interface {
	ReceiveVendorInvoice(ctx context.Context, in store.VendorInvoiceIntake) (store.VendorInvoiceIntake, bool, error)
	CreateFromInvoice(ctx context.Context, in store.OpenItem) (store.OpenItem, bool, error)
	MarkIntakePosted(ctx context.Context, id, entryID, actor string) (store.VendorInvoiceIntake, error)
	ParkIntakeAutoPost(ctx context.Context, id, reason string) error
	IntakePostedBefore(ctx context.Context, chargeID string, version int) (bool, error)
}

// Ledger posts the borrower's entry; internal/ledger.Client implements it.
type Ledger interface {
	Post(ctx context.Context, e ledger.Entry) (string, error)
}

// EntityProfile is what an entry of the borrower's legal entity posts with: its ledger and accounts, from the payables posting profile
// (the same profile payment runs read), never from one deployment-wide setting.
type EntityProfile struct {
	LedgerID        string
	ExpenseAccount  string
	InputTaxAccount string
	ControlAccount  string
}

// AutoPost is what posting without review needs: the ledger, which must sign, and the profile of each legal entity. A nil *AutoPost
// means the deployment has not configured it; a charge whose pairing opted in is then left RECEIVED with the reason for a clerk.
type AutoPost struct {
	Ledger          Ledger
	Profiles        func(legalEntityID string) (EntityProfile, bool)
	PaymentTermDays int
	// Now is the clock (default time.Now): a charge dated in a closed period is posted today.
	Now func() time.Time
}

// Signer is implemented by a ledger client that can sign its posts; auto-posting refuses a ledger that cannot.
type Signer interface{ Signed() bool }

// Validate reports a configuration that cannot post an entry.
func (a *AutoPost) Validate() error {
	switch {
	case a == nil || a.Ledger == nil || a.Profiles == nil:
		return errors.New("auto-posting needs the ledger (LEDGER_URL and the service signing key) and the payables posting profiles")
	}
	if sg, ok := a.Ledger.(Signer); ok && !sg.Signed() {
		return errors.New("auto-posting needs the service signing key: the ledger refuses an unsigned journal")
	}
	return nil
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
	// LenderLegalEntityID is the other side of the vendor invoice; the entry names it as the trading partner on the cost and control
	// lines (#40).
	LenderLegalEntityID string `json:"lenderLegalEntityId"`
	VendorAccount       string `json:"vendorAccount"`
	// AutoPostInvoice is the pairing's opt-in to post the vendor invoice without review.
	AutoPostInvoice bool `json:"autoPostInvoice"`
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
func Handlers(s Store, auto *AutoPost) map[string]eventing.Handler {
	return map[string]eventing.Handler{
		ChargeInvoiceRequestedType: eventing.HandlerFunc(func(ctx context.Context, _ pgx.Tx, evt events.Event) error {
			_, err := Handle(ctx, s, auto, evt)
			return err
		}),
	}
}

// Handle takes the vendor invoice of one event into the intake and returns the row (the one already there on a redelivery).
func Handle(ctx context.Context, s Store, auto *AutoPost, evt events.Event) (store.VendorInvoiceIntake, error) {
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
	if d.AutoPostInvoice {
		return autoPost(tenant.With(ctx, scoped), s, auto, d, out)
	}
	return out, nil
}

// EntryLines are the lines of the borrower's entry for an intake row: debit the cost account for the net, debit the input tax account
// for the tax, credit the control account for the gross. They balance by construction (gross is net plus tax), and the tax
// account is required only when there is tax.
func EntryLines(p EntityProfile, in store.VendorInvoiceIntake, text string) ([]domain.RunJournalLine, error) {
	tax := in.Gross.Sub(in.Net)
	lines := []domain.RunJournalLine{{Account: p.ExpenseAccount, Amount: in.Net, Text: text}}
	if tax.IsPositive() {
		if p.InputTaxAccount == "" {
			return nil, errors.New("the charge carries tax and the posting profile has no intercompany input tax account")
		}
		lines = append(lines, domain.RunJournalLine{Account: p.InputTaxAccount, Amount: tax, Text: text})
	}
	lines = append(lines, domain.RunJournalLine{Account: p.ControlAccount, Credit: true, Amount: in.Gross, Text: text})
	return lines, nil
}

// PartnerOf is the trading partner an auto-posted entry names on its cost and control lines: the lender's legal entity, or "" when the
// event carries none or it is the borrower's own legal entity (the ledger refuses a partner equal to its own entity).
func PartnerOf(d Data) string {
	if ids.Valid(d.LenderLegalEntityID) && d.LenderLegalEntityID != d.BorrowerLegalEntityID {
		return d.LenderLegalEntityID
	}
	return ""
}

// park leaves the intake row RECEIVED with the reason an auto-post was refused, so a clerk sees it on the worklist and posts it by
// hand. Nothing is dead-lettered: the event is done, and the row is the one recovery path.
func park(ctx context.Context, s Store, in store.VendorInvoiceIntake, reason string) (store.VendorInvoiceIntake, error) {
	if err := s.ParkIntakeAutoPost(ctx, in.ID, reason); err != nil {
		return in, err
	}
	in.AutoPostNote = reason
	slog.WarnContext(ctx, "intercompany vendor invoice left for a clerk: auto-post refused", "intake", in.ID, "reason", reason)
	return in, nil
}

// autoPost posts the intake row of a charge whose pairing opted in. Only a row that is plainly RECEIVED is posted: a row a clerk
// posted or rejected, or an earlier auto-post, is never posted again, and a later version of a charge whose earlier version is
// posted is left for a clerk (who reverses version 1 first). Whatever is refused is parked on the row. Each step is idempotent: the
// ledger entry by its key, the AP item by the row's id, so a redelivery after a failure finishes the rest.
func autoPost(ctx context.Context, s Store, auto *AutoPost, d Data, in store.VendorInvoiceIntake) (store.VendorInvoiceIntake, error) {
	if in.Status != "RECEIVED" {
		return in, nil
	}
	if err := auto.Validate(); err != nil {
		return park(ctx, s, in, err.Error())
	}
	if d.Version > 1 {
		earlier, err := s.IntakePostedBefore(ctx, d.ID, d.Version)
		if err != nil {
			return in, err
		}
		if earlier {
			return park(ctx, s, in, "an earlier version of this charge is already posted; reverse it, then post this version by hand")
		}
	}
	profile, ok := auto.Profiles(in.LegalEntityID)
	if !ok || profile.LedgerID == "" || profile.ExpenseAccount == "" || profile.ControlAccount == "" {
		return park(ctx, s, in, "the legal entity's posting profile has no ledger, intercompany cost account or control account")
	}
	price, err := decimal.NewFromString(d.Price)
	if err != nil {
		return park(ctx, s, in, "the charge has no price")
	}
	tax := decimal.Zero
	if d.Tax != "" {
		if tax, err = decimal.NewFromString(d.Tax); err != nil {
			return park(ctx, s, in, "the charge's tax is not an amount")
		}
	}
	places := money.MinorUnits(d.Currency)
	want := price.Round(places).Add(tax.RoundBank(places))
	if in.Currency != d.Currency || !in.Gross.Equal(want) || !in.Net.Equal(price.Round(places)) {
		return park(ctx, s, in, fmt.Sprintf("the row totals %s %s, not the charge's %s %s", in.Gross, in.Currency, want, d.Currency))
	}
	text := "Intercompany charge " + d.SourceID + " from " + d.Lender
	lines, err := EntryLines(profile, in, text)
	if err != nil {
		return park(ctx, s, in, err.Error())
	}
	if partner := PartnerOf(d); partner != "" {
		for i := range lines {
			if lines[i].Account == profile.ExpenseAccount || lines[i].Account == profile.ControlAccount {
				lines[i].TradingPartnerID = partner
			}
		}
	}
	now := time.Now
	if auto.Now != nil {
		now = auto.Now
	}
	today := now().UTC().Format(time.DateOnly)
	date := in.InvoiceDate
	if date > today {
		date = today
	}
	entry := ledger.Entry{LegalEntityID: in.LegalEntityID, LedgerID: profile.LedgerID, DocumentID: in.ID, PostingDate: date, Currency: in.Currency,
		IdempotencyKey: ledgerclient.MustPostingKey("payables", "iccharge", strings.ReplaceAll(in.SourceRef, ":", "-"), "post"), SourceType: entrySourceType, Lines: lines}
	entryID, err := auto.Ledger.Post(ctx, entry)
	var status *ledger.StatusError
	if errors.As(err, &status) && status.Code == codePeriodClosed && date != today {
		// The period of the charge's date is closed: post on the first open date, today, the charge date kept in the line text.
		entry.PostingDate = today
		for i := range entry.Lines {
			entry.Lines[i].Text = text + " (dated " + in.InvoiceDate + ")"
		}
		entryID, err = auto.Ledger.Post(ctx, entry)
		errors.As(err, &status)
	}
	if err != nil {
		switch {
		case errors.As(err, &status) && status.Code == codePeriodClosed:
			return park(ctx, s, in, "the ledger period is closed for the charge date and today; post it by hand when a period is open")
		case errors.As(err, &status) && status.Status >= 400 && status.Status < 500 && status.Status != 408 && status.Status != 429:
			return park(ctx, s, in, fmt.Sprintf("the ledger refused the entry (status %d %s)", status.Status, status.Code))
		}
		return in, err
	}
	days := max(auto.PaymentTermDays, 0)
	invoiceDate, err := time.Parse(time.DateOnly, in.InvoiceDate)
	if err != nil {
		return park(ctx, s, in, "the row has no invoice date")
	}
	if _, _, err := s.CreateFromInvoice(ctx, store.OpenItem{LegalEntityID: in.LegalEntityID, VendorID: in.VendorID, SourceInvoiceID: in.ID, InvoiceNumber: in.InvoiceNumber,
		InvoiceDate: in.InvoiceDate, DueDate: invoiceDate.AddDate(0, 0, days).Format(time.DateOnly), DueDateSource: store.DueDateDefaulted, Currency: in.Currency, Amount: in.Gross, TradingPartnerID: PartnerOf(d)}); err != nil {
		if errors.Is(err, store.ErrConflictingReplay) {
			return in, eventing.Permanent(err)
		}
		return in, err
	}
	posted, err := s.MarkIntakePosted(ctx, in.ID, entryID, "service:"+ConsumerName)
	if err != nil {
		return in, err
	}
	slog.InfoContext(ctx, "intercompany vendor invoice posted", "charge", d.ID, "version", d.Version, "intake", in.ID, "entry", entryID, "legalEntity", in.LegalEntityID)
	return posted, nil
}
