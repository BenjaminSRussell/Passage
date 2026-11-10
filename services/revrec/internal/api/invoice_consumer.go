package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
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
	"github.com/BenjaminSRussell/passage/services/revrec/internal/domain"
	"github.com/BenjaminSRussell/passage/services/revrec/internal/store"
)

// The invoice consumer (29-4) opens the revenue contract of an order from billing's invoice.posted: one contract per order,
// one performance obligation per product on the order's invoice lines, each with its invoiced net as its stand-alone selling
// price (EXPLICIT), so the contract's allocation equals what the first invoice billed. The product's recognition template
// (PUT /v1/revrec/recognition-templates/{productId}) sets how it is recognized: RATABLE over the template's months from the
// posting date, or POINT_IN_TIME at the posting date when it has none. An order that already has a live contract (opened by an
// operator with real SSPs, or by an earlier invoice) is left alone, so the consumer never overwrites an allocation.
//
// Billing books an invoice with an order line only once the order has a contract (BIL-0207), so this consumer is what lets the
// invoice be booked without an operator creating the contract first. Lines without an order (free-text, subscription,
// milestone) have no contract here.
const (
	// InvoicePostedType is billing's invoice.posted.v1 on BillingInvoiceTopic.
	InvoicePostedType = "dev.passage.billing.invoice.posted.v1"
	// InvoiceContractKeyPrefix starts the idempotency key of a contract opened from an invoice:
	// invoice-posted:<legal entity>:<order>.
	InvoiceContractKeyPrefix = "invoice-posted:"

	dutyContractFromInvoice = "revrec.contract.from-invoice"
	maxContractsPerOrder    = 20
)

// InvoiceContractStore is what the consumer needs of the store.
type InvoiceContractStore interface {
	FindContractByKey(ctx context.Context, key string) (store.Contract, bool, error)
	ListContracts(ctx context.Context, f store.ContractFilter) (store.ContractPage, error)
	CreateContract(ctx context.Context, c store.Contract) (store.Contract, error)
	TemplatesFor(ctx context.Context, productIDs []string) (map[string]store.Template, error)
}

type invoicePostedData struct {
	InvoiceID     string `json:"invoice_id"`
	Number        string `json:"number"`
	LegalEntityID string `json:"legal_entity_id"`
	Customer      string `json:"customer"`
	Currency      string `json:"currency"`
	PostingDate   string `json:"posting_date"`
	Intercompany  bool   `json:"intercompany"`
	Lines         []struct {
		LineNo  int    `json:"line_no"`
		Net     string `json:"net"`
		OrderID string `json:"order_id"`
		Product string `json:"product"`
	} `json:"lines"`
}

// InvoiceConsumerHandlers handles invoice.posted. Register it with the consumer of CreditNoteConsumerHandlers (same topic,
// same principal) with Idempotent set: a contract is created under its own key, so a replay creates nothing twice.
func InvoiceConsumerHandlers(s InvoiceContractStore) map[string]eventing.Handler {
	return map[string]eventing.Handler{
		InvoicePostedType: eventing.HandlerFunc(func(ctx context.Context, _ pgx.Tx, evt events.Event) error {
			return openContracts(ctx, s, evt)
		}),
	}
}

func invoiceScope(ctx context.Context, evt events.Event, legalEntityID string) (context.Context, error) {
	actor, ok := tenant.FromContext(ctx)
	if !ok || !actor.HasDuty(dutyContractFromInvoice) {
		return nil, eventing.Permanent(errors.New("invoice consumer has no principal with " + dutyContractFromInvoice))
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

type orderProduct struct {
	product string
	net     decimal.Decimal
}

func openContracts(ctx context.Context, s InvoiceContractStore, evt events.Event) error {
	var d invoicePostedData
	if err := json.Unmarshal(evt.Data, &d); err != nil {
		return eventing.Permanent(fmt.Errorf("invoice.posted data: %w", err))
	}
	ctx, err := invoiceScope(ctx, evt, d.LegalEntityID)
	if err != nil {
		return err
	}
	if d.Intercompany {
		return nil
	}
	date, derr := time.Parse(dateLayout, d.PostingDate)
	if !ids.Valid(d.InvoiceID) || !ids.Valid(d.Customer) || derr != nil || !currencyCode.MatchString(d.Currency) {
		return eventing.Permanent(errors.New("invoice.posted needs invoice_id, customer, posting_date and currency"))
	}
	places := money.MinorUnits(d.Currency)
	byOrder := map[string][]orderProduct{}
	var orders []string
	for _, l := range d.Lines {
		if l.OrderID == "" || l.Product == "" {
			continue
		}
		net, nerr := decimal.NewFromString(l.Net)
		if nerr != nil || net.Exponent() < -places || !ids.Valid(l.OrderID) || !ids.Valid(l.Product) {
			return eventing.Permanent(fmt.Errorf("invoice.posted line %d needs an order_id, a product and a net at the currency's minor unit", l.LineNo))
		}
		order, product := strings.ToLower(l.OrderID), strings.ToLower(l.Product)
		if _, seen := byOrder[order]; !seen {
			orders = append(orders, order)
		}
		merged := false
		for i := range byOrder[order] {
			if byOrder[order][i].product == product {
				byOrder[order][i].net = byOrder[order][i].net.Add(net)
				merged = true
			}
		}
		if !merged {
			byOrder[order] = append(byOrder[order], orderProduct{product: product, net: net})
		}
	}
	sort.Strings(orders)
	var permanent []string
	var transient []error
	for _, order := range orders {
		err := openContract(ctx, s, d, strings.ToLower(d.LegalEntityID), order, date, byOrder[order])
		var p *httpx.Problem
		switch {
		case err == nil:
		case errors.As(err, &p) && p.Status >= 400 && p.Status < 500:
			permanent = append(permanent, fmt.Sprintf("order %s: %s %s", order, p.Code, p.Detail))
		default:
			transient = append(transient, fmt.Errorf("order %s: %w", order, err))
		}
	}
	if len(transient) > 0 {
		return fmt.Errorf("%s %s: %w", evt.Type, evt.ID, errors.Join(transient...))
	}
	if len(permanent) > 0 {
		return eventing.Permanent(fmt.Errorf("%s %s: %s", evt.Type, evt.ID, strings.Join(permanent, "; ")))
	}
	return nil
}

func openContract(ctx context.Context, s InvoiceContractStore, d invoicePostedData, legalEntityID, order string, date time.Time, lines []orderProduct) error {
	key := InvoiceContractKeyPrefix + legalEntityID + ":" + order
	if _, found, err := s.FindContractByKey(ctx, key); err != nil || found {
		return err
	}
	live, err := s.ListContracts(ctx, store.ContractFilter{LegalEntityID: legalEntityID, OrderID: order, Limit: maxContractsPerOrder})
	if err != nil {
		return err
	}
	for _, c := range live.Items {
		if c.Status != domain.ContractCancelled {
			return nil
		}
	}
	var billable []orderProduct
	price := decimal.Zero
	for _, l := range lines {
		if l.net.IsPositive() {
			billable = append(billable, l)
			price = price.Add(l.net)
		}
	}
	if len(billable) == 0 {
		return nil
	}
	products := make([]string, len(billable))
	for i, l := range billable {
		products[i] = l.product
	}
	templates, err := s.TemplatesFor(ctx, products)
	if err != nil {
		return err
	}
	req := contractRequest{LegalEntityID: legalEntityID, OrderID: order, CustomerID: strings.ToLower(d.Customer), Currency: d.Currency,
		TransactionPrice: price.String(), ContractDate: d.PostingDate}
	for _, l := range billable {
		o := contractObligationDTO{ProductID: l.product, SSP: l.net.String(), SSPSource: "EXPLICIT", Method: domain.MethodPointInTime}
		if t, ok := templates[l.product]; ok && t.Method == domain.MethodRatable && t.TermMonths != nil {
			start, end := date.Format(dateLayout), date.AddDate(0, *t.TermMonths, 0).AddDate(0, 0, -1).Format(dateLayout)
			o.Method, o.TermStart, o.TermEnd = domain.MethodRatable, &start, &end
		} else {
			on := d.PostingDate
			o.SatisfiedOn = &on
		}
		req.Obligations = append(req.Obligations, o)
	}
	fields := map[string]string{}
	c, err := buildContract(req, fields)
	if len(fields) > 0 {
		return httpx.NewProblem(422, "REV-0310", fmt.Sprintf("the invoice lines do not make a contract: %v", fields))
	}
	if err != nil {
		return err
	}
	c.IdempotencyKey, c.RequestHash = key, requestHash(req)
	_, err = s.CreateContract(ctx, c)
	return err
}
