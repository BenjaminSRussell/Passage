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
	GetContract(ctx context.Context, id string) (store.Contract, error)
	AddObligations(ctx context.Context, contractID string, increase decimal.Decimal, add []store.Obligation, expectedVersion int) (store.Contract, error)
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

func billableOf(lines []orderProduct) []orderProduct {
	var out []orderProduct
	for _, l := range lines {
		if l.net.IsPositive() {
			out = append(out, l)
		}
	}
	return out
}

func templatesOf(ctx context.Context, s InvoiceContractStore, billable []orderProduct) (map[string]store.Template, error) {
	products := make([]string, len(billable))
	for i, l := range billable {
		products[i] = l.product
	}
	return s.TemplatesFor(ctx, products)
}

// modifyForLater handles an invoice of an order whose contract this consumer already opened: a cancelled contract takes nothing,
// otherwise the products the contract lacks are added.
func modifyForLater(ctx context.Context, s InvoiceContractStore, d invoicePostedData, legalEntityID, order string, date time.Time, lines []orderProduct) error {
	live, err := s.ListContracts(ctx, store.ContractFilter{LegalEntityID: legalEntityID, OrderID: order, Limit: maxContractsPerOrder})
	if err != nil {
		return err
	}
	var open []store.Contract
	for _, c := range live.Items {
		if c.Status != domain.ContractCancelled {
			open = append(open, c)
		}
	}
	billable := billableOf(lines)
	if len(open) != 1 || len(billable) == 0 {
		return nil
	}
	templates, err := templatesOf(ctx, s, billable)
	if err != nil {
		return err
	}
	return addLaterProducts(ctx, s, open[0], d, date, billable, templates)
}

// templateObligation is the obligation an invoiced product becomes under its recognition template: its invoiced net as an EXPLICIT
// SSP, recognized RATABLE over the template's term, as a MILESTONE awaiting satisfaction, as USAGE, or (no template) at the posting
// date.
func templateObligation(l orderProduct, t store.Template, postingDate string, date time.Time) contractObligationDTO {
	o := contractObligationDTO{ProductID: l.product, SSP: l.net.String(), SSPSource: "EXPLICIT", Method: domain.MethodPointInTime}
	switch {
	case t.Method == domain.MethodRatable && t.TermMonths != nil:
		start, end := date.Format(dateLayout), date.AddDate(0, *t.TermMonths, 0).AddDate(0, 0, -1).Format(dateLayout)
		o.Method, o.TermStart, o.TermEnd = domain.MethodRatable, &start, &end
	case t.Method == domain.MethodMilestone:
		o.Method = domain.MethodMilestone
	case t.Method == domain.MethodUsage:
		o.Method = domain.MethodUsage
	default:
		on := postingDate
		o.SatisfiedOn = &on
	}
	return o
}

func openContract(ctx context.Context, s InvoiceContractStore, d invoicePostedData, legalEntityID, order string, date time.Time, lines []orderProduct) error {
	key := InvoiceContractKeyPrefix + legalEntityID + ":" + order
	// A contract this consumer opened earlier (an earlier invoice of the order) is modified, not opened again.
	if _, found, err := s.FindContractByKey(ctx, key); err != nil {
		return err
	} else if found {
		return modifyForLater(ctx, s, d, strings.ToLower(d.LegalEntityID), order, date, lines)
	}
	live, err := s.ListContracts(ctx, store.ContractFilter{LegalEntityID: legalEntityID, OrderID: order, Limit: maxContractsPerOrder})
	if err != nil {
		return err
	}
	var existing *store.Contract
	for _, c := range live.Items {
		if c.Status != domain.ContractCancelled {
			if existing != nil {
				return nil // several live contracts: which one an invoice belongs to is for an operator to say
			}
			c := c
			existing = &c
		}
	}
	billable := billableOf(lines)
	if len(billable) == 0 {
		return nil
	}
	templates, err := templatesOf(ctx, s, billable)
	if err != nil {
		return err
	}
	if existing != nil {
		return addLaterProducts(ctx, s, *existing, d, date, billable, templates)
	}
	price := decimal.Zero
	for _, l := range billable {
		price = price.Add(l.net)
	}
	req := contractRequest{LegalEntityID: legalEntityID, OrderID: order, CustomerID: strings.ToLower(d.Customer), Currency: d.Currency,
		TransactionPrice: price.String(), ContractDate: d.PostingDate}
	for _, l := range billable {
		req.Obligations = append(req.Obligations, templateObligation(l, templates[l.product], d.PostingDate, date))
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

// addLaterProducts is the contract modification for an invoice of an order that already has a contract: the products the contract
// has no obligation for yet (first invoiced now) are added as obligations at their invoiced net, a separate contract in the sense of
// ASC 606-10-25-12, and the allocation and schedules already there are left alone. Products the contract has, another currency or a
// contract with nothing to add change nothing, so a replayed event adds nothing twice.
func addLaterProducts(ctx context.Context, s InvoiceContractStore, existing store.Contract, d invoicePostedData, date time.Time, billable []orderProduct, templates map[string]store.Template) error {
	if !strings.EqualFold(existing.Currency, d.Currency) {
		return nil
	}
	c, err := s.GetContract(ctx, existing.ID)
	if err != nil {
		return err
	}
	have := map[string]bool{}
	for _, o := range c.Obligations {
		have[strings.ToLower(o.ProductID)] = true
	}
	var obligations []contractObligationDTO
	increase := decimal.Zero
	for _, l := range billable {
		if have[l.product] {
			continue
		}
		obligations = append(obligations, templateObligation(l, templates[l.product], d.PostingDate, date))
		increase = increase.Add(l.net)
	}
	if len(obligations) == 0 {
		return nil
	}
	fields := map[string]string{}
	add, price, err := buildObligations(c.Currency, c.RatableBasis, c.ResidualRule, increase.String(), obligations, 0, fields)
	if len(fields) > 0 {
		return httpx.NewProblem(422, "REV-0310", fmt.Sprintf("the invoice lines do not make a contract modification: %v", fields))
	}
	if err != nil {
		return err
	}
	_, err = s.AddObligations(ctx, c.ID, price, add, 0)
	return err
}
