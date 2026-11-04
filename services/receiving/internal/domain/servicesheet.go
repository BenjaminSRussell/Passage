package domain

import (
	"context"
	"fmt"
	"time"

	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/platform/money"
)

const (
	CodeSheetOverLine   = "AP-SES-001"
	CodeSheetSelfAccept = "AP-SES-002"
	CodeSheetNotPending = "AP-SES-003"

	SheetPending  = "PENDING"
	SheetAccepted = "ACCEPTED"
	SheetRejected = "REJECTED"

	MaxSheetLines = 200

	ReceiptSourceProduct = "PRODUCT"
	ReceiptSourceService = "SERVICE_ENTRY_SHEET"

	serviceSheetKeyFormat = "post:receiving:serviceentrysheet:%s:accrual"
)

type SheetLine struct {
	POLineNo    int             `json:"poLineNo"`
	Description string          `json:"description"`
	Unit        string          `json:"unit"`
	Qty         decimal.Decimal `json:"qty"`
	UnitPrice   decimal.Decimal `json:"unitPrice"`
	Amount      decimal.Decimal `json:"amount"`
}

func SheetAmount(qty, unitPrice decimal.Decimal, currency string) decimal.Decimal {
	return qty.Mul(unitPrice).RoundBank(money.MinorUnits(currency))
}

func (l PurchaseOrderLine) Amount(currency string) decimal.Decimal {
	return SheetAmount(l.Qty, l.NetUnitPrice(), currency)
}

type ServiceAccrualLine struct {
	LineNo    int
	Qty       decimal.Decimal
	UnitPrice money.Amount
}

type ServiceAccrual struct {
	ID             string
	LegalEntityID  string
	LedgerID       string
	FiscalPeriodID string
	PostingDate    time.Time
	Currency       string
	Lines          []ServiceAccrualLine
}

func PostServiceSheet(ctx context.Context, in ServiceAccrual, periods PeriodControl, ledger LedgerPoster, profile PostingProfile) (JournalEntry, []GrirItem, error) {
	if in.ID == "" || in.PostingDate.IsZero() || len(in.Lines) == 0 {
		return JournalEntry{}, nil, fail(CodeInvalidReceipt, "service entry sheet %s needs a posting date and at least one line", in.ID)
	}
	if in.LedgerID == "" || in.FiscalPeriodID == "" {
		return JournalEntry{}, nil, fail(CodeInvalidReceipt, "service entry sheet %s needs a ledger and a fiscal period", in.ID)
	}
	if profile.Expense == "" || profile.GRIR == "" {
		return JournalEntry{}, nil, fail(CodeInvalidReceipt, "service entry sheet %s needs expense and GR/IR accounts", in.ID)
	}
	if err := checkAPPeriod(ctx, periods, in.LegalEntityID, in.LedgerID, in.FiscalPeriodID, in.PostingDate, in.ID); err != nil {
		return JournalEntry{}, nil, err
	}
	entry := JournalEntry{
		IdempotencyKey: fmt.Sprintf(serviceSheetKeyFormat, in.ID),
		LegalEntityID:  in.LegalEntityID,
		LedgerID:       in.LedgerID,
		PostingDate:    in.PostingDate,
		DocumentDate:   in.PostingDate,
		Source:         DocumentSource{Context: "receiving", DocumentType: "serviceentrysheet", DocumentID: in.ID, Purpose: "accrual"},
		RateType:       "spot",
	}
	items := make([]GrirItem, 0, len(in.Lines))
	for i, l := range in.Lines {
		if l.Qty.Sign() <= 0 {
			return JournalEntry{}, nil, fail(CodeInvalidReceipt, "service entry sheet %s line %d quantity must be positive", in.ID, i+1)
		}
		amount := amountOf(l.UnitPrice, l.Qty)
		entry.Lines = append(entry.Lines,
			JournalLine{Account: profile.Expense, Amount: amount},
			JournalLine{Account: profile.GRIR, Amount: amount.Neg()})
		items = append(items, GrirItem{
			ReceiptID: in.ID, LineNo: l.LineNo, ReceivedQty: l.Qty,
			ReceivedAmount: amount, ClearedAmount: money.Amount{Value: decimal.Zero, Currency: amount.Currency},
		})
	}
	var err error
	if entry.LedgerEntryID, err = postEntry(ctx, ledger, entry, in.ID); err != nil {
		return JournalEntry{}, nil, err
	}
	return entry, items, nil
}
