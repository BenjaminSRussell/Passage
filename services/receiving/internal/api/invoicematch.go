package api

import (
	"context"
	"errors"

	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/depot/services/receiving/internal/domain"
	"github.com/BenjaminSRussell/depot/services/receiving/internal/store"
)

type matchLineBody struct {
	LineNo           int       `json:"lineNo"`
	Status           string    `json:"status"`
	ReceivedQty      string    `json:"receivedQty,omitempty"`
	PriceVariance    moneyBody `json:"priceVariance"`
	CurrencyMismatch bool      `json:"currencyMismatch,omitempty"`
}

type matchBody struct {
	Status          string          `json:"status"`
	WithinTolerance bool            `json:"withinTolerance"`
	Lines           []matchLineBody `json:"lines"`
}

type matchOutcome struct {
	result   domain.Result
	received []string
	owner    string
}

func (a *API) invoiceMatch(ctx context.Context, inv store.Invoice) (*matchBody, error) {
	policies, err := a.store.ListMatchingPolicies(ctx, inv.LegalEntityID)
	if err != nil {
		return nil, err
	}
	m, err := a.matchInvoice(ctx, inv, policies)
	if err != nil {
		return nil, err
	}
	res := m.result
	out := &matchBody{Status: string(res.Status), WithinTolerance: res.Status == domain.Matched, Lines: make([]matchLineBody, len(res.Lines))}
	for i, lr := range res.Lines {
		out.Lines[i] = matchLineBody{LineNo: i + 1, Status: string(lr.Status), ReceivedQty: m.received[i], PriceVariance: toMoney(lr.PriceVariance), CurrencyMismatch: lr.CurrencyMismatch}
	}
	return out, nil
}

func (a *API) matchInvoice(ctx context.Context, inv store.Invoice, policies []domain.MatchingPolicy) (matchOutcome, error) {
	receipts := map[string]store.Receipt{}
	for _, l := range inv.Lines {
		if _, seen := receipts[l.ReceiptID]; seen {
			continue
		}
		rec, err := a.store.GetReceipt(ctx, l.ReceiptID)
		if err != nil && !errors.Is(err, store.ErrNotFound) {
			return matchOutcome{}, err
		}
		receipts[l.ReceiptID] = rec
	}
	matched := domain.Invoice{Currency: inv.Currency, Lines: make([]domain.InvoiceLine, len(inv.Lines))}
	received := make([]string, len(inv.Lines))
	lineRules := make([]domain.MatchingPolicy, len(inv.Lines))
	sum := decimal.Zero
	for i, l := range inv.Lines {
		line := domain.InvoiceLine{Qty: l.Qty, UnitPrice: l.UnitPrice}
		for _, rl := range receipts[l.ReceiptID].Lines {
			if rl.LineNo == l.ReceiptLineNo {
				line.POUnitPrice, line.POCurrency, line.ReceivedUninvoiced = rl.UnitPrice, rl.Currency, rl.Qty
				line.ReceiptState = receipts[l.ReceiptID].State
				received[i] = rl.Qty.String()
				lineRules[i], _ = domain.ResolvePolicy(policies, inv.VendorID, rl.ProductID)
			}
		}
		matched.Lines[i] = line
		sum = sum.Add(l.Qty.Mul(l.UnitPrice))
	}
	matched.HeaderTotal = sum
	headerRule, _ := domain.ResolvePolicy(policies, inv.VendorID, "")
	res := domain.EvaluateWith(withDefault(headerRule), func(i int) domain.Policy { return withDefault(lineRules[i]) }, matched)
	owner := ""
	for i, lr := range res.Lines {
		if domain.IsVariance(lr.Status) && lineRules[i].OwnerUserID != "" {
			owner = lineRules[i].OwnerUserID
			break
		}
	}
	if owner == "" && res.Status == domain.TotalVariance {
		owner = headerRule.OwnerUserID
	}
	return matchOutcome{result: res, received: received, owner: owner}, nil
}

func withDefault(mp domain.MatchingPolicy) domain.Policy {
	if mp.OwnerUserID == "" {
		return domain.DefaultPolicy()
	}
	return mp.Policy
}
