package api

import (
	"bytes"
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"sync/atomic"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"
	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/platform/authz"
	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/platform/money"
	"github.com/BenjaminSRussell/passage/platform/tenant"
	"github.com/BenjaminSRussell/passage/services/receiving/internal/domain"
	"github.com/BenjaminSRussell/passage/services/receiving/internal/store"
)

const (
	readyTimeout       = 500 * time.Millisecond
	dutyMatchingPolicy = "MatchingPolicyMaintain"
	dutyReceiving      = "ReceivingMaintain"
	dutyReceivingPost  = "ReceivingPost"
	dutyInvoicePost    = "VendorInvoicePost"
	dutyAPInquire      = "APInquire"
	maxLines           = 500
	maxMagnitude       = 1e12
	maxExponent        = 12
	minExponent        = -18
)

// Ports are required; New panics when either is nil.
type Ports struct {
	Periods domain.PeriodControl
	Ledger  domain.LedgerPoster
	Profile domain.PostingProfile
}

type API struct {
	store      store.Store
	ports      Ports
	evaluated  atomic.Int64
	rejected   atomic.Int64
	arrived    atomic.Int64
	registered atomic.Int64
	posted     atomic.Int64
	adjusted   atomic.Int64
	invoiced   atomic.Int64
	http       requestMetrics
}

func New(s store.Store, ports Ports) *API {
	if ports.Periods == nil || ports.Ledger == nil {
		panic("api.New: Ports.Periods and Ports.Ledger are required")
	}
	return &API{store: s, ports: ports, http: newRequestMetrics()}
}

func (a *API) PostInvoice(ctx context.Context, in domain.VendorInvoice, profile domain.PostingProfile) (domain.JournalEntry, error) {
	return domain.PostInvoice(ctx, in, a.ports.Periods, a.ports.Ledger, profile)
}

func (a *API) Register(r chi.Router) {
	r.Get("/readyz", a.ready)
	r.With(authz.RequireDuty(dutyMatchingPolicy)).Post("/v1/receiving/matching:evaluate", a.evaluate)
	r.With(authz.RequireDuty(dutyReceiving)).Post("/v1/receiving/arrivals", a.idempotent(a.arrive))
	r.With(authz.RequireDuty(dutyReceiving)).Post("/v1/receiving/receipts", a.idempotent(a.directReceipt))
	r.With(authz.RequireDuty(dutyReceiving)).Get("/v1/receiving/receipts", a.listReceipts)
	r.With(authz.RequireDuty(dutyReceiving)).Get("/v1/receiving/receipts/{id}", a.getReceipt)
	r.With(authz.RequireDuty(dutyReceiving)).Post("/v1/receiving/receipts/{id}:register", a.idempotent(a.registerReceipt))
	r.With(authz.RequireDuty(dutyReceivingPost)).Post("/v1/receiving/receipts/{id}:post", a.idempotent(a.postReceipt))
	r.With(authz.RequireDuty(dutyReceivingPost)).Post("/v1/receiving/receipts/{id}:correct", a.idempotent(a.correctReceipt))
	r.With(authz.RequireDuty(dutyReceivingPost)).Post("/v1/receiving/receipts/{id}:cancel", a.idempotent(a.cancelReceipt))
	r.With(authz.RequireDuty(dutyAPInquire)).Get("/v1/receiving/grir", a.listGrir)
	r.With(authz.RequireDuty(dutyInvoicePost)).Post("/v1/receiving/invoices:post", a.idempotent(a.postVendorInvoice))
}

func (a *API) Metrics() http.Handler {
	counters := []struct {
		name, help string
		value      *atomic.Int64
	}{
		{"receiving_matches_evaluated_total", "Invoices evaluated by the matching engine.", &a.evaluated},
		{"receiving_evaluate_rejected_total", "Evaluate requests rejected by validation.", &a.rejected},
		{"receiving_receipts_arrived_total", "Receipts recorded at arrival.", &a.arrived},
		{"receiving_receipts_registered_total", "Receipts registered, including direct receipts.", &a.registered},
		{"receiving_receipts_posted_total", "Receipts posted to the ledger.", &a.posted},
		{"receiving_receipts_adjusted_total", "Posted receipts corrected or cancelled.", &a.adjusted},
		{"receiving_invoices_posted_total", "Vendor invoices posted against GR/IR items.", &a.invoiced},
	}
	return http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/plain; version=0.0.4")
		for _, c := range counters {
			_, _ = fmt.Fprintf(w, "# HELP %s %s\n# TYPE %s counter\n%s %d\n", c.name, c.help, c.name, c.name, c.value.Load())
		}
		a.http.write(w)
	})
}

func (a *API) ready(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), readyTimeout)
	defer cancel()
	if err := a.store.Ping(ctx); err != nil {
		slog.Error("readiness check failed", "err", err, "correlationId", middleware.GetReqID(r.Context()))
		httpx.JSON(w, http.StatusServiceUnavailable, map[string]string{"status": "unavailable"})
		return
	}
	httpx.JSON(w, http.StatusOK, map[string]string{"status": "ready"})
}

type wireDecimal struct{ decimal.Decimal }

func (d *wireDecimal) UnmarshalJSON(b []byte) error {
	if len(b) == 0 || (b[0] != '-' && (b[0] < '0' || b[0] > '9')) {
		return fmt.Errorf("must be a JSON number")
	}
	v, err := decimal.NewFromString(string(b))
	if err != nil {
		return err
	}
	if v.Exponent() > maxExponent || v.Exponent() < minExponent || v.Abs().GreaterThan(decimal.NewFromFloat(maxMagnitude)) {
		return fmt.Errorf("out of range")
	}
	d.Decimal = v
	return nil
}

type policyRequest struct {
	MatchLevel   string       `json:"matchLevel"`
	PriceTolPct  *wireDecimal `json:"priceTolPct"`
	PriceTolAbs  *wireDecimal `json:"priceTolAbs"`
	QtyTolPct    *wireDecimal `json:"qtyTolPct"`
	TotalTolPct  *wireDecimal `json:"totalTolPct"`
	TotalTolAbs  *wireDecimal `json:"totalTolAbs"`
	SmallDiffAbs *wireDecimal `json:"smallDiffAbs"`
	PriceTolMode string       `json:"priceTolMode"`
}

type lineRequest struct {
	Qty                   *wireDecimal `json:"qty"`
	Unit                  string       `json:"unit"`
	UnitPrice             *wireDecimal `json:"unitPrice"`
	POUnitPrice           *wireDecimal `json:"poUnitPrice"`
	POCurrency            string       `json:"poCurrency"`
	ReceiptState          string       `json:"receiptState"`
	ReceivedUninvoicedQty *wireDecimal `json:"receivedUninvoicedQty"`
	POQtyRemaining        *wireDecimal `json:"poQtyRemaining"`
}

type evaluateRequest struct {
	Policy      *policyRequest `json:"policy"`
	Currency    string         `json:"currency"`
	HeaderTotal *wireDecimal   `json:"headerTotal"`
	Charges     *wireDecimal   `json:"charges"`
	Tax         *wireDecimal   `json:"tax"`
	Lines       []lineRequest  `json:"lines"`
}

type moneyBody struct {
	Amount   string `json:"amount"`
	Currency string `json:"currency"`
}

type lineBody struct {
	Unit             string    `json:"unit,omitempty"`
	Status           string    `json:"status"`
	PriceVariance    moneyBody `json:"priceVariance"`
	CurrencyMismatch bool      `json:"currencyMismatch"`
}

type postingBody struct {
	Kind   string    `json:"kind"`
	Amount moneyBody `json:"amount"`
}

type evaluateResponse struct {
	Status        string        `json:"status"`
	Lines         []lineBody    `json:"lines"`
	TotalVariance moneyBody     `json:"totalVariance"`
	Postings      []postingBody `json:"postings"`
}

func (a *API) evaluate(w http.ResponseWriter, r *http.Request) {
	p, ok := tenant.FromContext(r.Context())
	if !ok || !ids.Valid(p.TenantID) {
		httpx.Fail(w, r, httpx.NewProblem(http.StatusUnauthorized, "AUTH-0001", "missing or malformed tenant"))
		return
	}
	var req evaluateRequest
	if err := httpx.Decode(r, &req); err != nil {
		a.rejected.Add(1)
		httpx.Fail(w, r, err)
		return
	}
	policy, invoice, fields := buildInvoice(req)
	if len(fields) > 0 {
		a.rejected.Add(1)
		problem := httpx.NewProblem(http.StatusBadRequest, "COM-0001", "request failed validation")
		problem.Errors = fields
		httpx.Fail(w, r, problem)
		return
	}
	result := domain.Evaluate(policy, invoice)
	a.evaluated.Add(1)
	httpx.JSON(w, http.StatusOK, toResponse(result, req.Lines))
}

func toMoney(a money.Amount) moneyBody {
	return moneyBody{Amount: a.Value.StringFixed(money.MinorUnits(a.Currency)), Currency: a.Currency}
}

func toResponse(res domain.Result, lines []lineRequest) evaluateResponse {
	out := evaluateResponse{Status: string(res.Status), TotalVariance: toMoney(res.TotalVariance), Lines: make([]lineBody, 0, len(res.Lines)), Postings: []postingBody{}}
	for i, l := range res.Lines {
		out.Lines = append(out.Lines, lineBody{Unit: lines[i].Unit, Status: string(l.Status), PriceVariance: toMoney(l.PriceVariance), CurrencyMismatch: l.CurrencyMismatch})
	}
	for _, p := range res.Postings {
		out.Postings = append(out.Postings, postingBody{Kind: p.Kind, Amount: toMoney(p.Amount)})
	}
	return out
}

func buildInvoice(req evaluateRequest) (domain.Policy, domain.Invoice, map[string]string) {
	fields := map[string]string{}
	policy := buildPolicy(req.Policy, fields)
	if len(req.Currency) != 3 || bytes.ContainsFunc([]byte(req.Currency), func(c rune) bool { return c < 'A' || c > 'Z' }) {
		fields["currency"] = "must be a 3-letter upper-case ISO 4217 code"
	}
	if len(req.Lines) == 0 || len(req.Lines) > maxLines {
		fields["lines"] = fmt.Sprintf("must hold 1 to %d lines", maxLines)
	}
	invoice := domain.Invoice{Currency: req.Currency}
	invoice.HeaderTotal = required(fields, "headerTotal", req.HeaderTotal)
	invoice.Charges = optionalNonNegative(fields, "charges", req.Charges)
	invoice.Tax = optionalNonNegative(fields, "tax", req.Tax)
	for i, l := range req.Lines {
		key := func(name string) string { return fmt.Sprintf("lines[%d].%s", i, name) }
		line := domain.InvoiceLine{
			POCurrency:         l.POCurrency,
			ReceiptState:       domain.ReceiptState(l.ReceiptState),
			Qty:                required(fields, key("qty"), l.Qty),
			UnitPrice:          required(fields, key("unitPrice"), l.UnitPrice),
			POUnitPrice:        required(fields, key("poUnitPrice"), l.POUnitPrice),
			ReceivedUninvoiced: optionalNonNegative(fields, key("receivedUninvoicedQty"), l.ReceivedUninvoicedQty),
			POQtyRemaining:     optionalNonNegative(fields, key("poQtyRemaining"), l.POQtyRemaining),
		}
		if l.Qty != nil && line.Qty.Sign() <= 0 {
			fields[key("qty")] = "must be greater than zero"
		}
		if l.UnitPrice != nil && line.UnitPrice.Sign() < 0 {
			fields[key("unitPrice")] = "must not be negative"
		}
		if l.POUnitPrice != nil && line.POUnitPrice.Sign() < 0 {
			fields[key("poUnitPrice")] = "must not be negative"
		}
		if l.POCurrency != "" && len(l.POCurrency) != 3 {
			fields[key("poCurrency")] = "must be a 3-letter ISO 4217 code"
		}
		if s := line.ReceiptState; s != "" && s != domain.Registered && s != domain.Posted {
			fields[key("receiptState")] = "must be REGISTERED or POSTED when present"
		}
		invoice.Lines = append(invoice.Lines, line)
	}
	return policy, invoice, fields
}

func buildPolicy(in *policyRequest, fields map[string]string) domain.Policy {
	p := domain.DefaultPolicy()
	if in == nil {
		return p
	}
	if in.MatchLevel != "" {
		p.MatchLevel = domain.MatchLevel(in.MatchLevel)
		if p.MatchLevel != domain.MatchNone && p.MatchLevel != domain.MatchTwoWay && p.MatchLevel != domain.MatchThreeWay {
			fields["policy.matchLevel"] = "must be NONE, TWO_WAY or THREE_WAY"
		}
	}
	if in.PriceTolMode != "" {
		p.PriceTolMode = domain.PriceMode(in.PriceTolMode)
		if p.PriceTolMode != domain.UpperOnly && p.PriceTolMode != domain.Both {
			fields["policy.priceTolMode"] = "must be UPPER_ONLY or BOTH"
		}
	}
	for name, target := range map[string]struct {
		in  *wireDecimal
		out *decimal.Decimal
	}{
		"priceTolPct": {in.PriceTolPct, &p.PriceTolPct}, "priceTolAbs": {in.PriceTolAbs, &p.PriceTolAbs},
		"qtyTolPct": {in.QtyTolPct, &p.QtyTolPct}, "totalTolPct": {in.TotalTolPct, &p.TotalTolPct},
		"totalTolAbs": {in.TotalTolAbs, &p.TotalTolAbs}, "smallDiffAbs": {in.SmallDiffAbs, &p.SmallDiffAbs},
	} {
		if target.in == nil {
			continue
		}
		if target.in.Sign() < 0 {
			fields["policy."+name] = "must not be negative"
			continue
		}
		*target.out = target.in.Decimal
	}
	return p
}

func required(fields map[string]string, name string, v *wireDecimal) decimal.Decimal {
	if v == nil {
		fields[name] = "is required"
		return decimal.Zero
	}
	return v.Decimal
}

func optionalNonNegative(fields map[string]string, name string, v *wireDecimal) decimal.Decimal {
	if v == nil {
		return decimal.Zero
	}
	if v.Sign() < 0 {
		fields[name] = "must not be negative"
	}
	return v.Decimal
}
