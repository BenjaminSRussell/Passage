package api

import (
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/platform/money"
	"github.com/BenjaminSRussell/passage/services/intercompany/internal/domain"
)

// dateLayout is the ISO 8601 calendar date the API accepts and returns.
const dateLayout = "2006-01-02"

// chargeCategories are the IcChargeRequest categories (FR-40.13).
var chargeCategories = map[string]bool{"Expense": true, "Hour": true, "Item": true, "Fee": true}

// fieldParser collects one field error per bad value while it converts a request.
type fieldParser map[string]string

func (f fieldParser) text(name, v string) string {
	v = strings.TrimSpace(v)
	if v == "" {
		f[name] = "is required"
	}
	return v
}

// amount parses a non-negative decimal with at most 6 decimals. Optional empty
// values return zero.
func (f fieldParser) amount(name, raw string, required bool) decimal.Decimal {
	if raw == "" && !required {
		return decimal.Zero
	}
	if !amountText.MatchString(raw) {
		f[name] = "must be a non-negative decimal with at most 6 decimals"
		return decimal.Zero
	}
	return decimal.RequireFromString(raw)
}

func (f fieldParser) positive(name, raw string) decimal.Decimal {
	v := f.amount(name, raw, true)
	if _, bad := f[name]; !bad && !v.IsPositive() {
		f[name] = "must be above 0"
	}
	return v
}

func (f fieldParser) currency(name, v string) string {
	if !currencyCode.MatchString(v) {
		f[name] = "must be an ISO 4217 code"
	}
	return v
}

func (f fieldParser) date(name, raw string) time.Time {
	t, err := time.Parse(dateLayout, raw)
	if err != nil {
		f[name] = "must be a date such as 2026-12-10"
	}
	return t
}

// ---- IC charge preview (FR-40.13, BR-40.12, AC-40.11) ----

type transferPriceRow struct {
	Category      string `json:"category"`
	ResourceRole  string `json:"resourceRole"`
	Method        string `json:"method"`
	MarkupPct     string `json:"markupPct"`
	UnitPrice     string `json:"unitPrice"`
	EffectiveFrom string `json:"effectiveFrom"`
}

type chargeRequest struct {
	LendingEntity   string             `json:"lendingEntity"`
	BorrowingEntity string             `json:"borrowingEntity"`
	Category        string             `json:"category"`
	ResourceRole    string             `json:"resourceRole"`
	TransactionDate string             `json:"transactionDate"`
	Quantity        string             `json:"quantity"`
	UnitCost        string             `json:"unitCost"`
	Currency        string             `json:"currency"`
	TransferPrices  []transferPriceRow `json:"transferPrices"`
	CostAccount     string             `json:"costAccount"`
	RecoveryAccount string             `json:"recoveryAccount"`
	MarginAccount   string             `json:"marginAccount"`
	Matrix          []matrixRow        `json:"matrix"`
}

type appliedPrice struct {
	Method        string `json:"method"`
	MarkupPct     string `json:"markupPct,omitempty"`
	UnitPrice     string `json:"unitPrice,omitempty"`
	EffectiveFrom string `json:"effectiveFrom"`
}

type chargeResponse struct {
	PairID        string       `json:"pairId"`
	Currency      string       `json:"currency"`
	TransferPrice appliedPrice `json:"transferPrice"`
	Cost          string       `json:"cost"`
	Price         string       `json:"price"`
	Margin        string       `json:"margin"`
	Balanced      bool         `json:"balanced"`
	Legs          []leg        `json:"legs"`
}

type chargeInput struct {
	lender, borrower, category, role string
	on                               time.Time
	quantity, unitCost               decimal.Decimal
	prices                           []domain.TransferPrice
	matrix                           domain.Matrix
}

func (a *API) previewCharge(w http.ResponseWriter, r *http.Request) {
	if !guard(w, r) {
		return
	}
	var req chargeRequest
	if err := httpx.Decode(r, &req); err != nil {
		httpx.Fail(w, r, err)
		return
	}
	in, fields := parseCharge(req)
	if !validation(w, r, fields) {
		return
	}
	tp, err := domain.ResolveTransferPrice(in.prices, in.lender, in.borrower, in.category, in.role, in.on)
	if err != nil {
		httpx.Fail(w, r, problemFrom(err))
		return
	}
	pricing, err := domain.PriceWithTransfer(tp, in.quantity, in.unitCost)
	if err != nil {
		httpx.Fail(w, r, problemFrom(err))
		return
	}
	pairID := ids.New()
	j, err := domain.ChargeJournal(in.matrix, pairID, in.lender, in.borrower, req.CostAccount, req.RecoveryAccount, req.MarginAccount, pricing)
	if err != nil {
		httpx.Fail(w, r, problemFrom(err))
		return
	}
	a.chargePreviews.Add(1)
	places := money.MinorUnits(req.Currency)
	applied := appliedPrice{Method: tp.Method, EffectiveFrom: tp.EffectiveFrom.Format(dateLayout)}
	switch tp.Method {
	case domain.TPCostPlus:
		applied.MarkupPct = tp.MarkupPct.String()
	case domain.TPTable:
		applied.UnitPrice = tp.UnitPrice.String()
	}
	httpx.JSON(w, http.StatusOK, chargeResponse{
		PairID: pairID, Currency: req.Currency, TransferPrice: applied,
		Cost: pricing.Cost.Value.StringFixed(places), Price: pricing.Price.Value.StringFixed(places), Margin: pricing.Margin.Value.StringFixed(places),
		Balanced: j.Balanced() == nil, Legs: legsOf(j),
	})
}

func parseCharge(req chargeRequest) (chargeInput, map[string]string) {
	f := fieldParser{}
	in := chargeInput{
		lender:   f.text("lendingEntity", req.LendingEntity),
		borrower: f.text("borrowingEntity", req.BorrowingEntity),
		category: req.Category,
		role:     strings.TrimSpace(req.ResourceRole),
		on:       f.date("transactionDate", req.TransactionDate),
		quantity: f.positive("quantity", req.Quantity),
		unitCost: f.amount("unitCost", req.UnitCost, true),
	}
	if in.lender != "" && in.lender == in.borrower {
		f["borrowingEntity"] = "must differ from lendingEntity"
	}
	if !chargeCategories[req.Category] {
		f["category"] = "must be Expense, Hour, Item or Fee"
	}
	f.currency("currency", req.Currency)
	f.text("costAccount", req.CostAccount)
	f.text("recoveryAccount", req.RecoveryAccount)
	f.text("marginAccount", req.MarginAccount)
	switch {
	case len(req.TransferPrices) == 0:
		f["transferPrices"] = "must contain at least one transfer price"
	case len(req.TransferPrices) > MaxTransferPrices:
		f["transferPrices"] = fmt.Sprintf("must contain at most %d rows", MaxTransferPrices)
	}
	if _, bad := f["transferPrices"]; !bad {
		for i, row := range req.TransferPrices {
			at := func(name string) string { return fmt.Sprintf("transferPrices[%d].%s", i, name) }
			tp := domain.TransferPrice{
				Lender: in.lender, Borrower: in.borrower, Category: row.Category, Role: strings.TrimSpace(row.ResourceRole),
				Method: row.Method, Currency: req.Currency, EffectiveFrom: f.date(at("effectiveFrom"), row.EffectiveFrom),
			}
			if !chargeCategories[row.Category] {
				f[at("category")] = "must be Expense, Hour, Item or Fee"
			}
			switch row.Method {
			case domain.TPCost:
			case domain.TPCostPlus:
				tp.MarkupPct = f.amount(at("markupPct"), row.MarkupPct, true)
			case domain.TPTable:
				tp.UnitPrice = f.amount(at("unitPrice"), row.UnitPrice, true)
			default:
				f[at("method")] = "must be Cost, CostPlus or Table"
			}
			in.prices = append(in.prices, tp)
		}
	}
	in.matrix = parseMatrix(req.Matrix, f)
	return in, f
}

// ---- centralized payment preview (FR-40.12, AC-40.10) ----

type centralPaymentRequest struct {
	PayingEntity  string      `json:"payingEntity"`
	OwningEntity  string      `json:"owningEntity"`
	PaymentMethod string      `json:"paymentMethod"`
	Direction     string      `json:"direction"`
	Amount        string      `json:"amount"`
	Currency      string      `json:"currency"`
	BankAccount   string      `json:"bankAccount"`
	OwningAccount string      `json:"owningAccount"`
	Matrix        []matrixRow `json:"matrix"`
}

func (a *API) previewCentralPayment(w http.ResponseWriter, r *http.Request) {
	if !guard(w, r) {
		return
	}
	var req centralPaymentRequest
	if err := httpx.Decode(r, &req); err != nil {
		httpx.Fail(w, r, err)
		return
	}
	f := fieldParser{}
	setup := domain.CentralizedPaymentSetup{
		PayingEntity:  f.text("payingEntity", req.PayingEntity),
		OwningEntity:  f.text("owningEntity", req.OwningEntity),
		PaymentMethod: f.text("paymentMethod", req.PaymentMethod),
		Direction:     domain.PaymentDirection(req.Direction),
	}
	if setup.PayingEntity != "" && setup.PayingEntity == setup.OwningEntity {
		f["owningEntity"] = "must differ from payingEntity"
	}
	if setup.Direction != domain.DirectionPay && setup.Direction != domain.DirectionReceive {
		f["direction"] = "must be Pay or Receive"
	}
	amount := f.positive("amount", req.Amount)
	f.currency("currency", req.Currency)
	bank := f.text("bankAccount", req.BankAccount)
	owning := f.text("owningAccount", req.OwningAccount)
	matrix := parseMatrix(req.Matrix, f)
	if !validation(w, r, f) {
		return
	}
	pairID := ids.New()
	j, err := domain.CentralPaymentJournal(matrix, pairID, setup, money.Amount{Value: amount, Currency: req.Currency}, bank, owning)
	if err != nil {
		httpx.Fail(w, r, problemFrom(err))
		return
	}
	a.centralPaymentPreviews.Add(1)
	httpx.JSON(w, http.StatusOK, previewResponse{PairID: pairID, Balanced: j.Balanced() == nil, Legs: legsOf(j)})
}
