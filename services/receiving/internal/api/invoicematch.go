package api

import (
	"context"
	"errors"

	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/services/receiving/internal/domain"
	"github.com/BenjaminSRussell/passage/services/receiving/internal/store"
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

func (a *API) invoiceMatch(ctx context.Context, inv store.Invoice) (*matchBody, error) {
	receipts := map[string]store.Receipt{}
	for _, l := range inv.Lines {
		if _, seen := receipts[l.ReceiptID]; seen {
			continue
		}
		rec, err := a.store.GetReceipt(ctx, l.ReceiptID)
		if err != nil && !errors.Is(err, store.ErrNotFound) {
			return nil, err
		}
		receipts[l.ReceiptID] = rec
	}
	matched := domain.Invoice{Currency: inv.Currency, Lines: make([]domain.InvoiceLine, len(inv.Lines))}
	received := make([]string, len(inv.Lines))
	sum := decimal.Zero
	for i, l := range inv.Lines {
		line := domain.InvoiceLine{Qty: l.Qty, UnitPrice: l.UnitPrice}
		for _, rl := range receipts[l.ReceiptID].Lines {
			if rl.LineNo == l.ReceiptLineNo {
				line.POUnitPrice, line.POCurrency, line.ReceivedUninvoiced = rl.UnitPrice, rl.Currency, rl.Qty
				line.ReceiptState = receipts[l.ReceiptID].State
				received[i] = rl.Qty.String()
			}
		}
		matched.Lines[i] = line
		sum = sum.Add(l.Qty.Mul(l.UnitPrice))
	}
	matched.HeaderTotal = sum
	res := domain.Evaluate(domain.DefaultPolicy(), matched)
	out := &matchBody{Status: string(res.Status), WithinTolerance: res.Status == domain.Matched, Lines: make([]matchLineBody, len(res.Lines))}
	for i, lr := range res.Lines {
		out.Lines[i] = matchLineBody{LineNo: i + 1, Status: string(lr.Status), ReceivedQty: received[i], PriceVariance: toMoney(lr.PriceVariance), CurrencyMismatch: lr.CurrencyMismatch}
	}
	return out, nil
}
