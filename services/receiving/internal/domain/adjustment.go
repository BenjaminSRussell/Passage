package domain

import (
	"context"
	"fmt"
	"time"

	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/platform/money"
)

const (
	AdjustmentCredit = "CREDIT"
	AdjustmentDebit  = "DEBIT"

	adjustmentKeyFormat = "post:receiving:invoiceadjustment:%s:%s"
)

type InvoiceLineState struct {
	LineNo       int
	Amount       decimal.Decimal
	StandardCost bool
	Credited     decimal.Decimal
	Debited      decimal.Decimal
}

type AdjustmentLine struct {
	InvoiceLineNo int
	Amount        decimal.Decimal
}

type InvoiceAdjustment struct {
	ID             string
	Kind           string
	LegalEntityID  string
	LedgerID       string
	FiscalPeriodID string
	PostingDate    time.Time
	DocumentDate   time.Time
	Currency       string
	Original       []InvoiceLineState
	Lines          []AdjustmentLine
}

func PostInvoiceAdjustment(ctx context.Context, in InvoiceAdjustment, periods PeriodControl, ledger LedgerPoster, profile PostingProfile) (JournalEntry, error) {
	if in.Kind != AdjustmentCredit && in.Kind != AdjustmentDebit {
		return JournalEntry{}, fail(CodeInvalidReceipt, "adjustment %s kind %q must be %s or %s", in.ID, in.Kind, AdjustmentCredit, AdjustmentDebit)
	}
	if in.PostingDate.IsZero() || len(in.Lines) == 0 || in.LedgerID == "" || in.FiscalPeriodID == "" {
		return JournalEntry{}, fail(CodeInvalidReceipt, "adjustment %s needs a posting date, a ledger, a fiscal period and at least one line", in.ID)
	}
	byNo := make(map[int]InvoiceLineState, len(in.Original))
	for _, l := range in.Original {
		byNo[l.LineNo] = l
	}
	claimed := map[int]decimal.Decimal{}
	sign := decimal.NewFromInt(1)
	if in.Kind == AdjustmentCredit {
		sign = sign.Neg()
	}
	entry := JournalEntry{
		IdempotencyKey: fmt.Sprintf(adjustmentKeyFormat, in.ID, in.Kind),
		LegalEntityID:  in.LegalEntityID,
		LedgerID:       in.LedgerID,
		PostingDate:    in.PostingDate,
		DocumentDate:   in.DocumentDate,
		Source:         DocumentSource{Context: "receiving", DocumentType: "invoiceadjustment", DocumentID: in.ID, Purpose: in.Kind},
		RateType:       "spot",
	}
	total := decimal.Zero
	for i, l := range in.Lines {
		orig, ok := byNo[l.InvoiceLineNo]
		switch {
		case !ok:
			return JournalEntry{}, fail(CodeInvalidReceipt, "adjustment line %d references invoice line %d, which the invoice does not have", i+1, l.InvoiceLineNo)
		case !l.Amount.IsPositive() || !l.Amount.Equal(roundedMoney(l.Amount, in.Currency).Value):
			return JournalEntry{}, fail(CodeInvalidReceipt, "adjustment line %d amount %s must be positive with at most the currency's decimals", i+1, l.Amount)
		}
		claimed[l.InvoiceLineNo] = claimed[l.InvoiceLineNo].Add(l.Amount)
		if in.Kind == AdjustmentCredit {
			if ceiling := orig.Amount.Add(orig.Debited).Sub(orig.Credited); claimed[l.InvoiceLineNo].GreaterThan(ceiling) {
				return JournalEntry{}, fail(CodeOverCredit, "adjustment line %d credits %s against invoice line %d, which has %s left to credit", i+1, claimed[l.InvoiceLineNo], l.InvoiceLineNo, ceiling)
			}
		}
		account := profile.Inventory
		if orig.StandardCost {
			account = profile.PPV
		}
		entry.Lines = append(entry.Lines, JournalLine{Account: account, Amount: money.Amount{Value: l.Amount.Mul(sign), Currency: in.Currency}})
		total = total.Add(l.Amount)
	}
	entry.Lines = append(entry.Lines, JournalLine{Account: profile.VendorControl, Amount: money.Amount{Value: total.Mul(sign.Neg()), Currency: in.Currency}})
	if err := checkAPPeriod(ctx, periods, in.LedgerID, in.FiscalPeriodID, in.PostingDate, in.ID); err != nil {
		return JournalEntry{}, err
	}
	entryID, err := postEntry(ctx, ledger, entry, in.ID)
	if err != nil {
		return JournalEntry{}, err
	}
	entry.LedgerEntryID = entryID
	return entry, nil
}
