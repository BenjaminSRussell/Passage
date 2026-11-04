package domain

import (
	"context"
	"fmt"
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
	ID             string
	LegalEntityID  string
	LedgerID       string
	FiscalPeriodID string
	PostingDate    time.Time
	InvoiceDate    time.Time
	Currency       string
	TaxAmount      decimal.Decimal
	TaxCode        string
	Lines          []PostLine
	// VendorGroupCode is the short code of the vendor's group in the party
	// master; empty when unknown. It rides on the vendor-control line.
	VendorGroupCode string
}

type HeldLine struct {
	Qty          decimal.Decimal
	UnitPrice    decimal.Decimal
	StandardCost bool
	Service      bool
	Clearing     decimal.Decimal
}

func clearingFor(item GrirItem, qty decimal.Decimal, exhausts bool, clearedInInvoice decimal.Decimal) money.Amount {
	if exhausts {
		return money.Amount{Value: item.OpenAmount().Sub(clearedInInvoice), Currency: item.ReceivedAmount.Currency}
	}
	return roundedMoney(item.ReceivedAmount.Value.Mul(qty).Div(item.ReceivedQty), item.ReceivedAmount.Currency)
}

func checkInvoiceHeader(in VendorInvoice, lines int, profile PostingProfile) error {
	if in.PostingDate.IsZero() || lines == 0 {
		return fail(CodeInvalidReceipt, "invoice %s needs a posting date and at least one line", in.ID)
	}
	if in.LedgerID == "" || in.FiscalPeriodID == "" {
		return fail(CodeInvalidReceipt, "invoice %s needs a ledger and a fiscal period", in.ID)
	}
	if in.TaxAmount.IsNegative() {
		return fail(CodeInvalidReceipt, "invoice %s tax amount must not be negative", in.ID)
	}
	if in.TaxAmount.IsPositive() && profile.InputTax == "" {
		return fail(CodeInvalidReceipt, "invoice %s has input tax but the posting profile has no input-tax account", in.ID)
	}
	return nil
}

func buildInvoiceEntry(in VendorInvoice, lines []HeldLine, profile PostingProfile) JournalEntry {
	entry := JournalEntry{
		IdempotencyKey: fmt.Sprintf(invoiceKeyFormat, in.ID),
		LegalEntityID:  in.LegalEntityID,
		LedgerID:       in.LedgerID,
		PostingDate:    in.PostingDate,
		DocumentDate:   in.InvoiceDate,
		Source:         DocumentSource{Context: "receiving", DocumentType: "vendorinvoice", DocumentID: in.ID, Purpose: "invoice"},
		RateType:       "spot",
	}
	payable := money.Amount{Value: decimal.Zero, Currency: in.Currency}
	for _, l := range lines {
		amount := roundedMoney(l.Qty.Mul(l.UnitPrice), in.Currency)
		payable.Value = payable.Value.Add(amount.Value)
		entry.Lines = append(entry.Lines, JournalLine{Account: profile.GRIR, Amount: money.Amount{Value: l.Clearing, Currency: in.Currency}})
		if diff := amount.Value.Sub(l.Clearing); !diff.IsZero() {
			account := profile.Inventory
			switch {
			case l.Service:
				account = profile.Expense
			case l.StandardCost:
				account = profile.PPV
			}
			entry.Lines = append(entry.Lines, JournalLine{Account: account, Amount: money.Amount{Value: diff, Currency: in.Currency}})
		}
		entry.GrirCleared = append(entry.GrirCleared, l.Clearing)
	}
	if in.TaxAmount.IsPositive() {
		entry.Lines = append(entry.Lines, JournalLine{Account: profile.InputTax, Amount: money.Amount{Value: in.TaxAmount, Currency: in.Currency}})
		payable.Value = payable.Value.Add(in.TaxAmount)
	}
	profile.addVendorControlLine(&entry, payable.Neg(), in.VendorGroupCode)
	return entry
}

func assessInvoice(in VendorInvoice, profile PostingProfile) (JournalEntry, error) {
	claimed := map[*GrirItem]decimal.Decimal{}
	cleared := map[*GrirItem]decimal.Decimal{}
	held := make([]HeldLine, len(in.Lines))
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
		clearing := clearingFor(*l.Item, l.Qty, exhausts, cleared[l.Item])
		cleared[l.Item] = cleared[l.Item].Add(clearing.Value)
		held[i] = HeldLine{Qty: l.Qty, UnitPrice: l.UnitPrice, StandardCost: l.StandardCost, Service: l.Item.Service, Clearing: clearing.Value}
	}
	return buildInvoiceEntry(in, held, profile), nil
}

func applyInvoice(in VendorInvoice, entry JournalEntry) {
	for i, l := range in.Lines {
		l.Item.InvoicedQty = l.Item.InvoicedQty.Add(l.Qty)
		l.Item.ClearedAmount.Value = l.Item.ClearedAmount.Value.Add(entry.GrirCleared[i])
	}
}

func PostInvoice(ctx context.Context, in VendorInvoice, periods PeriodControl, ledger LedgerPoster, profile PostingProfile) (JournalEntry, error) {
	if err := checkInvoiceHeader(in, len(in.Lines), profile); err != nil {
		return JournalEntry{}, err
	}
	if err := checkAPPeriod(ctx, periods, in.LegalEntityID, in.LedgerID, in.FiscalPeriodID, in.PostingDate, in.ID); err != nil {
		return JournalEntry{}, err
	}
	entry, err := assessInvoice(in, profile)
	if err != nil {
		return JournalEntry{}, err
	}
	if entry.LedgerEntryID, err = postEntry(ctx, ledger, entry, in.ID); err != nil {
		return JournalEntry{}, err
	}
	applyInvoice(in, entry)
	return entry, nil
}

func HoldInvoice(ctx context.Context, in VendorInvoice, periods PeriodControl, profile PostingProfile) (JournalEntry, error) {
	if err := checkInvoiceHeader(in, len(in.Lines), profile); err != nil {
		return JournalEntry{}, err
	}
	if err := checkAPPeriod(ctx, periods, in.LegalEntityID, in.LedgerID, in.FiscalPeriodID, in.PostingDate, in.ID); err != nil {
		return JournalEntry{}, err
	}
	entry, err := assessInvoice(in, profile)
	if err != nil {
		return JournalEntry{}, err
	}
	applyInvoice(in, entry)
	return entry, nil
}

func PostHeldInvoice(ctx context.Context, in VendorInvoice, lines []HeldLine, periods PeriodControl, ledger LedgerPoster, profile PostingProfile) (JournalEntry, error) {
	if err := checkInvoiceHeader(in, len(lines), profile); err != nil {
		return JournalEntry{}, err
	}
	if err := checkAPPeriod(ctx, periods, in.LegalEntityID, in.LedgerID, in.FiscalPeriodID, in.PostingDate, in.ID); err != nil {
		return JournalEntry{}, err
	}
	entry := buildInvoiceEntry(in, lines, profile)
	var err error
	if entry.LedgerEntryID, err = postEntry(ctx, ledger, entry, in.ID); err != nil {
		return JournalEntry{}, err
	}
	return entry, nil
}
