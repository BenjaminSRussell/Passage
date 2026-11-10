package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/platform/eventing"
	"github.com/BenjaminSRussell/passage/platform/events"
	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/platform/money"
	"github.com/BenjaminSRussell/passage/platform/tenant"
	"github.com/BenjaminSRussell/passage/services/revrec/internal/store"
)

// The credit-note consumer (BR-29.6, AC-29.6, #29 P3) reads billing's creditnote.booked, written when billing books a credit
// note's journal, and reverses the schedules of every credited line that carries a deferred amount (store.ApplyCreditLine,
// domain.PlanReversal): the unrecognized schedule of the line's obligations first, then recognized revenue, which the posting
// sweep books as Dr 4000 / Cr 2400. Each line is applied in its own transaction under its own key (credit note, line), so a
// retry or a replay applies only what is missing, and the inbox records the event once every line is applied. A line that
// cannot be applied (no contract for its order, no deferred obligation for its product, a credit above what the obligations
// hold) fails the event after the other lines: a rule failure is permanent and parks the event on the dead-letter topic,
// anything else is retried with back-off first.
const (
	// CreditNoteConsumerName is the consumer (and Kafka group) and the service principal it runs as,
	// service:revrec-creditnotes.
	CreditNoteConsumerName = "revrec-creditnotes"
	// CreditNoteBookedType is billing's creditnote.booked.v1 on BillingInvoiceTopic (billing routes its outbox by aggregate,
	// "invoice", so invoices and credit notes share the topic; the consumer skips the other types).
	CreditNoteBookedType = "dev.passage.billing.creditnote.booked.v1"
	BillingInvoiceTopic  = "passage.billing.invoice"
	BillingSource        = "billing"

	dutyCreditNoteReverse = "revrec.creditnote.reverse"
)

// CreditNoteStore is what the consumer needs of the store.
type CreditNoteStore interface {
	ApplyCreditLine(ctx context.Context, cn store.CreditNote, l store.CreditLine) (store.Reversal, bool, error)
}

type creditNoteBookedData struct {
	CreditNoteID  string `json:"credit_note_id"`
	Number        string `json:"number"`
	LegalEntityID string `json:"legal_entity_id"`
	Currency      string `json:"currency"`
	PostingDate   string `json:"posting_date"`
	Lines         []struct {
		LineNo         int    `json:"line_no"`
		OriginalLineNo int    `json:"original_line_no"`
		OrderID        string `json:"order_id"`
		Product        string `json:"product"`
		Deferred       string `json:"deferred"`
	} `json:"lines"`
}

// CreditNoteConsumerHandlers handles creditnote.booked. Register it with a consumer named CreditNoteConsumerName with
// Idempotent set: each line commits on its own under its own key, and the inbox records the event once every line is applied.
func CreditNoteConsumerHandlers(s CreditNoteStore) map[string]eventing.Handler {
	return map[string]eventing.Handler{
		CreditNoteBookedType: eventing.HandlerFunc(func(ctx context.Context, _ pgx.Tx, evt events.Event) error {
			return reverseCreditNote(ctx, s, evt)
		}),
	}
}

// creditNoteScope narrows the consumer's principal to the legal entity of the credit note, so the reversal reads and writes only
// under that legal entity's RLS scope.
func creditNoteScope(ctx context.Context, evt events.Event, legalEntityID string) (context.Context, error) {
	actor, ok := tenant.FromContext(ctx)
	if !ok || !actor.HasDuty(dutyCreditNoteReverse) {
		return nil, eventing.Permanent(errors.New("credit-note consumer has no principal with " + dutyCreditNoteReverse))
	}
	if evt.Source != BillingSource {
		return nil, eventing.Permanent(fmt.Errorf("%s from source %q, want %q", evt.Type, evt.Source, BillingSource))
	}
	if !ids.Valid(legalEntityID) {
		return nil, eventing.Permanent(fmt.Errorf("%s has no legal_entity_id", evt.Type))
	}
	scoped, err := tenant.ServicePrincipal(CreditNoteConsumerName, actor.TenantID, legalEntityID)
	if err != nil {
		return nil, eventing.Permanent(err)
	}
	return tenant.With(ctx, scoped), nil
}

func reverseCreditNote(ctx context.Context, s CreditNoteStore, evt events.Event) error {
	var d creditNoteBookedData
	if err := json.Unmarshal(evt.Data, &d); err != nil {
		return eventing.Permanent(fmt.Errorf("creditnote.booked data: %w", err))
	}
	ctx, err := creditNoteScope(ctx, evt, d.LegalEntityID)
	if err != nil {
		return err
	}
	date, err := time.Parse(dateLayout, d.PostingDate)
	if !ids.Valid(d.CreditNoteID) || err != nil || !currencyCode.MatchString(d.Currency) {
		return eventing.Permanent(errors.New("creditnote.booked needs credit_note_id, posting_date and currency"))
	}
	cn := store.CreditNote{ID: strings.ToLower(d.CreditNoteID), Number: d.Number, LegalEntityID: strings.ToLower(d.LegalEntityID),
		Currency: d.Currency, PostingDate: date, EventID: evt.ID}
	places := money.MinorUnits(d.Currency)
	var permanent []string
	var transient []error
	for _, l := range d.Lines {
		what := "line " + strconv.Itoa(l.LineNo)
		deferred, derr := decimal.NewFromString(l.Deferred)
		switch {
		case derr != nil || deferred.IsNegative() || deferred.Exponent() < -places || l.LineNo <= 0:
			permanent = append(permanent, what+": needs a positive line_no and a deferred amount at the currency's minor unit")
			continue
		case deferred.IsZero():
			// Nothing was debited to 2400 for the line (a point-in-time or free-text line): no schedule to reverse.
			continue
		case !ids.Valid(l.OrderID) || !ids.Valid(l.Product):
			permanent = append(permanent, what+": a deferred amount needs the order_id and product of the credited line")
			continue
		}
		_, _, err := s.ApplyCreditLine(ctx, cn, store.CreditLine{LineNo: l.LineNo, OriginalLineNo: l.OriginalLineNo,
			OrderID: strings.ToLower(l.OrderID), ProductID: strings.ToLower(l.Product), Deferred: deferred})
		var p *httpx.Problem
		switch {
		case err == nil:
		case errors.As(err, &p) && p.Status >= 400 && p.Status < 500 && p.Code != store.CodeKeyInFlight:
			permanent = append(permanent, fmt.Sprintf("%s: %s %s", what, p.Code, p.Detail))
		default:
			transient = append(transient, fmt.Errorf("%s: %w", what, err))
		}
	}
	if len(transient) > 0 {
		return fmt.Errorf("%s %s: %w", evt.Type, evt.ID, errors.Join(transient...))
	}
	if len(permanent) > 0 {
		return eventing.Permanent(fmt.Errorf("%s %s: lines not reversed: %s", evt.Type, evt.ID, strings.Join(permanent, "; ")))
	}
	return nil
}
