package domain

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/platform/money"
)

const invoiceKeyFormat = "post:receiving:vendorinvoice:%s:invoice"

type PostLine struct {
	Item         *GrirItem
	Qty          decimal.Decimal
	UnitPrice    decimal.Decimal
	StandardCost bool
}

type VendorInvoice struct {
	ID            string
	LegalEntityID string
	PostingDate   time.Time
	InvoiceDate   time.Time
	Currency      string
	Lines         []PostLine
}

func clearingFor(item GrirItem, qty decimal.Decimal, exhausts bool, clearedInInvoice decimal.Decimal) money.Amount {
	if exhausts {
		return money.Amount{Value: item.OpenAmount().Sub(clearedInInvoice), Currency: item.ReceivedAmount.Currency}
	}
	return roundedMoney(item.ReceivedAmount.Value.Mul(qty).Div(item.ReceivedQty), item.ReceivedAmount.Currency)
}

func PostInvoice(ctx context.Context, in VendorInvoice, periods PeriodControl, ledger LedgerPoster, profile PostingProfile) (JournalEntry, error) {
	if in.PostingDate.IsZero() || len(in.Lines) == 0 {
		return JournalEntry{}, fail(CodeInvalidReceipt, "invoice %s needs a posting date and at least one line", in.ID)
	}
	status, err := periods.AP(ctx, in.PostingDate)
	if err != nil {
		slog.ErrorContext(ctx, "period control unavailable", "err", err, "invoiceId", in.ID)
		return JournalEntry{}, fmt.Errorf("check AP period for invoice %s: %w", in.ID, err)
	}
	if status != PeriodOpen {
		return JournalEntry{}, fail(CodePeriodClosed, "AP period for %s is %s", in.PostingDate.Format(time.DateOnly), status)
	}
	entry := JournalEntry{
		IdempotencyKey: fmt.Sprintf(invoiceKeyFormat, in.ID),
		LegalEntityID:  in.LegalEntityID,
		PostingDate:    in.PostingDate,
		DocumentDate:   in.InvoiceDate,
		Source:         DocumentSource{Context: "receiving", DocumentType: "vendorinvoice", DocumentID: in.ID, Purpose: "invoice"},
		RateType:       "spot",
	}
	claimed := map[*GrirItem]decimal.Decimal{}
	cleared := map[*GrirItem]decimal.Decimal{}
	clearings := make([]money.Amount, len(in.Lines))
	payable := money.Amount{Value: decimal.Zero, Currency: in.Currency}
	for i, l := range in.Lines {
		if l.Qty.Sign() <= 0 {
			return JournalEntry{}, fail(CodeInvalidReceipt, "invoice line %d quantity must be positive", i+1)
		}
		if l.Item.ReceivedAmount.Currency != in.Currency {
			return JournalEntry{}, fail(CodeInvalidReceipt, "invoice line %d currency %s differs from receipt currency %s", i+1, in.Currency, l.Item.ReceivedAmount.Currency)
		}
		claimed[l.Item] = claimed[l.Item].Add(l.Qty)
		if claimed[l.Item].GreaterThan(l.Item.ReceivedQty.Sub(l.Item.InvoicedQty)) {
			return JournalEntry{}, fail(CodeOverInvoice, "invoice line %d exceeds received uninvoiced quantity", i+1)
		}
		exhausts := claimed[l.Item].Equal(l.Item.ReceivedQty.Sub(l.Item.InvoicedQty))
		clearings[i] = clearingFor(*l.Item, l.Qty, exhausts, cleared[l.Item])
		cleared[l.Item] = cleared[l.Item].Add(clearings[i].Value)
		amount := roundedMoney(l.Qty.Mul(l.UnitPrice), in.Currency)
		payable.Value = payable.Value.Add(amount.Value)
		entry.Lines = append(entry.Lines, JournalLine{Account: profile.GRIR, Amount: clearings[i]})
		if diff := amount.Value.Sub(clearings[i].Value); !diff.IsZero() {
			account := profile.Inventory
			if l.StandardCost {
				account = profile.PPV
			}
			entry.Lines = append(entry.Lines, JournalLine{Account: account, Amount: money.Amount{Value: diff, Currency: in.Currency}})
		}
	}
	entry.Lines = append(entry.Lines, JournalLine{Account: profile.VendorControl, Amount: payable.Neg()})
	if err := ledger.Post(ctx, entry); err != nil {
		slog.ErrorContext(ctx, "invoice ledger post failed", "err", err, "invoiceId", in.ID)
		return JournalEntry{}, fmt.Errorf("post invoice %s: %w", in.ID, err)
	}
	for i, l := range in.Lines {
		l.Item.InvoicedQty = l.Item.InvoicedQty.Add(l.Qty)
		l.Item.ClearedAmount.Value = l.Item.ClearedAmount.Value.Add(clearings[i].Value)
	}
	return entry, nil
}
