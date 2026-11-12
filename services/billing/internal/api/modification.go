package api

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"sort"
	"strings"
	"time"

	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/platform/money"
	"github.com/BenjaminSRussell/passage/platform/tenant"
	"github.com/BenjaminSRussell/passage/services/billing/internal/domain"
	"github.com/BenjaminSRussell/passage/services/billing/internal/store"
)

const (
	// dutyModificationBill is the service-only duty of revrec's billing request for a contract modification.
	dutyModificationBill = "billing.contract-modification.bill"
	// CodeModificationNoInvoice is BIL-0140: the order has no posted invoice line for a product of the modification, so there is no
	// tax code, account and customer to bill the change with.
	CodeModificationNoInvoice = "BIL-0140"
	// CodeModificationLines is BIL-0141: the per-product changes do not add up to the price change.
	CodeModificationLines = "BIL-0141"
)

type modificationLineDTO struct {
	ProductID string `json:"productId"`
	Amount    string `json:"amount"`
}

// modificationRequest is POST /v1/billing/contract-modifications: revrec's request to bill the price change of a contract
// modification. Lines are the allocation change of each product (signed); they add up to priceChange.
type modificationRequest struct {
	ModificationID string                `json:"modificationId"`
	ContractID     string                `json:"contractId"`
	LegalEntityID  string                `json:"legalEntityId"`
	OrderID        string                `json:"orderId"`
	CustomerID     string                `json:"customerId"`
	Currency       string                `json:"currency"`
	PriceChange    string                `json:"priceChange"`
	EffectiveDate  string                `json:"effectiveDate"`
	Reason         string                `json:"reason,omitempty"`
	Lines          []modificationLineDTO `json:"lines"`
}

type modificationOut struct {
	ModificationID string       `json:"modificationId"`
	Documents      []invoiceDTO `json:"documents"`
}

// modificationKey keys the documents billing creates for one modification: the invoice of the increases and one credit note per
// original invoice for the decreases. creditnote.booked carries the modification id of such a credit note (store).
func modificationKey(modificationID, suffix string) string {
	return store.ModificationKeyPrefix + modificationID + ":" + suffix
}

// productLine is where the order last billed a product: the posted invoice and its line.
type productLine struct {
	invoice store.Invoice
	line    store.Line
}

// billModification bills a contract modification's price change (#29): the products whose allocation rose are invoiced on one
// invoice proposal (one line per product with the order and product of the order's invoice line, so the journal books it on the
// order's revrec allocation like the first invoice), and the products whose allocation fell are credited by amount on the order's
// invoice lines of each, newest first and each up to what it has left to credit, one credit note proposal per invoice, all made in one
// transaction. Both stay proposals for a person to post. Each document
// is keyed by the modification, so a retry of the request returns the documents already made and creates only the missing ones.
func (a *API) billModification(w http.ResponseWriter, r *http.Request) {
	var req modificationRequest
	if err := httpx.Decode(r, &req); err != nil {
		httpx.Fail(w, r, err)
		return
	}
	fields := map[string]string{}
	for name, v := range map[string]string{"modificationId": req.ModificationID, "contractId": req.ContractID, "legalEntityId": req.LegalEntityID,
		"orderId": req.OrderID, "customerId": req.CustomerID} {
		if !ids.Valid(v) {
			fields[name] = "must be a UUID"
		}
	}
	if !currencyCode.MatchString(req.Currency) {
		fields["currency"] = "must be an ISO 4217 alpha-3 code"
	}
	effective, err := time.Parse(dateLayout, req.EffectiveDate)
	if err != nil {
		fields["effectiveDate"] = "must be a YYYY-MM-DD date"
	}
	change, err := decimal.NewFromString(req.PriceChange)
	if err != nil || change.IsZero() {
		fields["priceChange"] = "must be a non-zero decimal"
	}
	if len(req.Lines) == 0 || len(req.Lines) > a.maxLines {
		fields["lines"] = "must hold the allocation change of each product"
	}
	amounts := map[string]decimal.Decimal{}
	total := decimal.Zero
	for i, l := range req.Lines {
		amt, err := decimal.NewFromString(l.Amount)
		if !ids.Valid(l.ProductID) || err != nil {
			fields["lines"] = "line " + itoa(i) + " needs a productId and a decimal amount"
			continue
		}
		amounts[strings.ToLower(l.ProductID)] = amounts[strings.ToLower(l.ProductID)].Add(amt)
		total = total.Add(amt)
	}
	if len(fields) > 0 {
		writeFields(w, r, fields)
		return
	}
	if !total.Equal(change) {
		httpx.Fail(w, r, httpx.NewProblem(http.StatusUnprocessableEntity, CodeModificationLines, "the product changes add up to "+total.String()+", not the price change "+change.String()))
		return
	}
	if p, _ := tenant.FromContext(r.Context()); !tenant.InLegalEntityScope(p, req.LegalEntityID) {
		httpx.Fail(w, r, httpx.NewProblem(http.StatusForbidden, "AUTH-0003", "the caller is scoped to another legal entity"))
		return
	}
	exp := money.MinorUnits(req.Currency)
	for _, amt := range amounts {
		if amt.Exponent() < -exp {
			writeFields(w, r, map[string]string{"lines": "amounts must be at the currency's minor unit"})
			return
		}
	}
	raw, _ := json.Marshal(req)
	sum := sha256.Sum256(raw)
	hash := hex.EncodeToString(sum[:])
	billed, err := a.billedLines(r, req.OrderID, req.Currency)
	if err != nil {
		httpx.Fail(w, r, err)
		return
	}
	products := make([]string, 0, len(amounts))
	for p := range amounts {
		if !amounts[p].IsZero() {
			products = append(products, p)
		}
	}
	sort.Strings(products)
	var up, down []string
	where := map[string]productLine{}
	decreases := map[string]decimal.Decimal{}
	for _, p := range products {
		lines := billed[p]
		if len(lines) == 0 {
			httpx.Fail(w, r, httpx.NewProblem(http.StatusUnprocessableEntity, CodeModificationNoInvoice, "the order has no posted invoice line for product "+p+" to bill the change with"))
			return
		}
		where[p] = lines[0]
		if amounts[p].IsPositive() {
			up = append(up, p)
			continue
		}
		down = append(down, p)
		decreases[p] = amounts[p].Neg()
	}
	out := modificationOut{ModificationID: req.ModificationID, Documents: []invoiceDTO{}}
	created := false
	reason := "Contract modification of " + req.EffectiveDate
	if strings.TrimSpace(req.Reason) != "" {
		reason += ": " + strings.TrimSpace(req.Reason)
	}
	if len(up) > 0 {
		key := modificationKey(req.ModificationID, "invoice")
		doc, made, err := a.findOr(r, key, hash, func() (store.Invoice, error) {
			first := where[up[0]].invoice
			inv := store.Invoice{ID: ids.New(), LegalEntityID: req.LegalEntityID, CustomerID: first.CustomerID, InvoiceAccountID: first.InvoiceAccountID,
				Type: domain.TypeFreeText, Status: domain.StatusProposal, Currency: req.Currency, InvoiceDate: effective,
				DueDate: effective.AddDate(0, 0, first.PaymentTermDays), PaymentTermDays: first.PaymentTermDays, IdempotencyKey: key, RequestHash: hash,
				CounterpartyCountry: first.CounterpartyCountry, CounterpartyVATID: first.CounterpartyVATID, SupplyKind: first.SupplyKind}
			lines := make([]domain.Line, 0, len(up))
			for i, p := range up {
				src := where[p].line
				desc := truncateRunes(reason+" - "+src.Description, domain.MaxDescription)
				lines = append(lines, domain.Line{No: i + 1, Description: desc, Qty: decimal.NewFromInt(1), UnitPrice: amounts[p], DiscountPct: decimal.Zero,
					RevenueAccount: src.RevenueAccount, TaxCode: src.TaxCode})
			}
			taxes, err := a.tax.Quote(r.Context(), req.Currency, lines)
			if err != nil {
				return store.Invoice{}, taxProblem(err)
			}
			nets, totals, err := domain.Compute(req.Currency, lines, taxes, decimal.Zero)
			if err != nil {
				return store.Invoice{}, problemFrom(err)
			}
			for i, p := range up {
				src := where[p].line.Source
				inv.Lines = append(inv.Lines, store.Line{Line: lines[i], Net: nets[i], Tax: taxes[i].RoundBank(exp),
					Source: domain.Source{OrderID: strings.ToLower(req.OrderID), OrderLineID: src.OrderLineID, ProductID: p}})
			}
			inv.Totals = totals
			return a.store.CreateProposal(r.Context(), inv)
		})
		if err != nil {
			httpx.Fail(w, r, err)
			return
		}
		if made {
			a.proposalsCreated.Add(1)
		}
		created = created || made
		out.Documents = append(out.Documents, toDTO(doc))
	}
	if len(down) > 0 {
		// The credit notes of a modification are made together, once: a retry finds them by the modification's key prefix and returns
		// them, whatever invoices posted since, so the decrease is never credited twice.
		prefix := modificationKey(req.ModificationID, "credit:")
		existing, err := a.store.FindByIdempotencyPrefix(r.Context(), prefix)
		if err != nil {
			httpx.Fail(w, r, err)
			return
		}
		docs := existing
		for _, d := range existing {
			if d.RequestHash != hash {
				httpx.Fail(w, r, httpx.NewProblem(http.StatusUnprocessableEntity, "idempotency_key_reuse", "this modification was billed with a different request"))
				return
			}
		}
		if len(existing) == 0 {
			plan, invoiceOrder, err := creditPlan(billed, decreases, down)
			if err != nil {
				httpx.Fail(w, r, err)
				return
			}
			var cns []store.Invoice
			for _, invoiceID := range invoiceOrder {
				var orig store.Invoice
				for _, p := range down {
					for _, pl := range billed[p] {
						if pl.invoice.ID == invoiceID {
							orig = pl.invoice
						}
					}
				}
				original := make([]domain.PostedLine, len(orig.Lines))
				for i, l := range orig.Lines {
					original[i] = domain.PostedLine{Line: l.Line, Net: l.Net, Tax: l.Tax}
				}
				planned, totals, err := domain.PlanAmountCredit(orig.Currency, original, orig.Totals, orig.Credited, plan[invoiceID])
				if err != nil {
					httpx.Fail(w, r, problemFrom(err))
					return
				}
				date := effective
				if date.Before(orig.InvoiceDate) {
					date = orig.InvoiceDate
				}
				cn := store.Invoice{ID: ids.New(), LegalEntityID: orig.LegalEntityID, CustomerID: orig.CustomerID, InvoiceAccountID: orig.InvoiceAccountID,
					Type: domain.TypeCreditNote, Status: domain.StatusProposal, Currency: orig.Currency, InvoiceDate: date, DueDate: date,
					Totals: totals, OriginalInvoiceID: orig.ID, OriginalNumber: orig.Number, Reason: truncateRunes(reason, 500),
					IdempotencyKey: prefix + invoiceID, RequestHash: hash}
				for _, l := range planned {
					cn.Lines = append(cn.Lines, store.Line{Line: l.Line, Net: l.Net, Tax: l.Tax, OriginalLineNo: l.OriginalLineNo})
				}
				cns = append(cns, cn)
			}
			if docs, err = a.store.CreateCreditNotes(r.Context(), cns); err != nil {
				httpx.Fail(w, r, err)
				return
			}
			a.creditNotesCreated.Add(int64(len(docs)))
			created = created || len(docs) > 0
		}
		for _, d := range docs {
			out.Documents = append(out.Documents, toDTO(d))
		}
	}
	status := http.StatusOK
	if created {
		status = http.StatusCreated
	} else {
		w.Header().Set("Idempotent-Replayed", "true")
	}
	httpx.JSON(w, status, out)
}

// findOr returns the document stored under key (made false), or creates it (made true). A key stored with another request body
// is 422 idempotency_key_reuse.
func (a *API) findOr(r *http.Request, key, hash string, create func() (store.Invoice, error)) (store.Invoice, bool, error) {
	existing, found, err := a.store.FindByIdempotencyKey(r.Context(), key)
	if err != nil {
		return store.Invoice{}, false, err
	}
	if found {
		if existing.RequestHash != hash {
			return store.Invoice{}, false, httpx.NewProblem(http.StatusUnprocessableEntity, "idempotency_key_reuse", "this modification was billed with a different request")
		}
		return existing, false, nil
	}
	doc, err := create()
	return doc, err == nil, err
}

// billedLines finds, per product, the order's posted invoice lines in the currency, newest invoice first.
func (a *API) billedLines(r *http.Request, orderID, currency string) (map[string][]productLine, error) {
	out := map[string][]productLine{}
	after := ""
	for range 20 {
		page, err := a.store.ListInvoices(r.Context(), store.ListFilter{OrderID: strings.ToLower(orderID), PostedOnly: true, After: after, Limit: 200})
		if err != nil {
			return nil, err
		}
		for _, head := range page.Items {
			if head.Type == domain.TypeCreditNote || head.Currency != currency || head.Status == domain.StatusProposal || head.Status == domain.StatusReversed {
				continue
			}
			inv, err := a.store.GetInvoice(r.Context(), head.ID)
			if err != nil {
				return nil, err
			}
			for _, l := range inv.Lines {
				p := strings.ToLower(l.Source.ProductID)
				if p == "" || !strings.EqualFold(l.Source.OrderID, orderID) {
					continue
				}
				out[p] = append(out[p], productLine{invoice: inv, line: l})
			}
		}
		if page.NextAfter == "" {
			break
		}
		after = page.NextAfter
	}
	return out, nil
}

// creditPlan spreads the decreases of the products over the order's invoice lines, newest invoice first, each line up to what it
// still has to credit (its net less what posted credit notes took), and groups them per original invoice (in the order first used).
// A product whose lines together cannot take its decrease is BIL-0005.
func creditPlan(lines map[string][]productLine, decreases map[string]decimal.Decimal, products []string) (map[string][]domain.AmountCredit, []string, error) {
	plan := map[string][]domain.AmountCredit{}
	var order []string
	for _, p := range products {
		left := decreases[p]
		for _, pl := range lines[p] {
			if !left.IsPositive() {
				break
			}
			room := pl.line.Net.Sub(pl.invoice.Credited.Lines[pl.line.No].Net)
			if !room.IsPositive() {
				continue
			}
			take := decimal.Min(room, left)
			if _, seen := plan[pl.invoice.ID]; !seen {
				order = append(order, pl.invoice.ID)
			}
			plan[pl.invoice.ID] = append(plan[pl.invoice.ID], domain.AmountCredit{LineNo: pl.line.No, Net: take})
			left = left.Sub(take)
		}
		if left.IsPositive() {
			return nil, nil, httpx.NewProblem(http.StatusUnprocessableEntity, domain.CodeCreditExceeded,
				"the order's invoice lines for product "+p+" have less left to credit than its decrease of "+decreases[p].String())
		}
	}
	return plan, order, nil
}

func truncateRunes(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n])
}

func itoa(i int) string { return decimal.NewFromInt(int64(i)).String() }
