package domain

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"slices"
	"time"

	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/platform/money"
)

const (
	CodeOverDelivery      = "RCV-OVR-001"
	CodeDuplicateSerial   = "INV-SER-001"
	CodeInvalidReceipt    = "RCV-VAL-001"
	CodeInvalidTransition = "RCV-STA-001"
	CodePeriodClosed      = "PER-AP-001"
	CodeOverInvoice       = "AP-QTY-001"
	CodeDuplicateInvoice  = "AP-DUP-001"

	receiptKeyFormat    = "post:receiving:productreceipt:%s:receipt"
	correctionKeyFormat = "post:receiving:productreceipt:%s:correction:%d"
	cancelKeyFormat     = "post:receiving:productreceipt:%s:cancel"
)

type Error struct{ Code, Detail string }

func (e *Error) Error() string { return e.Code + ": " + e.Detail }

func fail(code, format string, args ...any) *Error {
	return &Error{Code: code, Detail: fmt.Sprintf(format, args...)}
}

type ReceiptState string

const (
	Arrived    ReceiptState = "ARRIVED"
	Registered ReceiptState = "REGISTERED"
	Posted     ReceiptState = "POSTED"
	Corrected  ReceiptState = "CORRECTED"
	Cancelled  ReceiptState = "CANCELLED"
)

var transitions = map[ReceiptState][]ReceiptState{
	Arrived:    {Registered, Cancelled},
	Registered: {Posted, Cancelled},
	Posted:     {Corrected, Cancelled},
}

type Tracking struct {
	Batch   string
	Serials []string
}

type SerialKey struct{ ProductID, Serial string }

type ReceiptLine struct {
	LineNo           int
	ProductID        string
	Qty              decimal.Decimal
	InvoicedQty      decimal.Decimal
	OrderedRemaining decimal.Decimal
	OverDeliveryPct  decimal.Decimal
	UnitPrice        money.Amount
	BatchTracked     bool
	SerialTracked    bool
	Tracking         Tracking
}

type Receipt struct {
	ID            string
	LegalEntityID string
	Date          time.Time
	State         ReceiptState
	Lines         []ReceiptLine
	LedgerEntryID string
}

type GrirItem struct {
	ReceiptID      string
	LineNo         int
	ReceivedQty    decimal.Decimal
	InvoicedQty    decimal.Decimal
	ReceivedAmount money.Amount
	ClearedAmount  money.Amount
}

func (g GrirItem) OpenAmount() decimal.Decimal {
	return g.ReceivedAmount.Value.Sub(g.ClearedAmount.Value)
}

func amountOf(price money.Amount, qty decimal.Decimal) money.Amount {
	return money.Amount{Value: price.Value.Mul(qty).RoundBank(money.MinorUnits(price.Currency)), Currency: price.Currency}
}

func (r Receipt) Transition(to ReceiptState, invoiced bool) (Receipt, error) {
	if !slices.Contains(transitions[r.State], to) {
		return r, fail(CodeInvalidTransition, "receipt %s cannot go from %s to %s", r.ID, r.State, to)
	}
	if r.State == Posted && to == Cancelled && invoiced {
		return r, fail(CodeInvalidTransition, "receipt %s has invoiced quantity and cannot be cancelled", r.ID)
	}
	r.State = to
	return r, nil
}

func (r Receipt) Register(recorded map[SerialKey]struct{}) (Receipt, error) {
	next, err := r.Transition(Registered, false)
	if err != nil {
		return r, err
	}
	seen := map[SerialKey]struct{}{}
	for _, l := range r.Lines {
		if l.Qty.Sign() <= 0 {
			return r, fail(CodeInvalidReceipt, "line %d quantity must be positive", l.LineNo)
		}
		allowance := l.OrderedRemaining.Mul(decimal.NewFromInt(1).Add(l.OverDeliveryPct.Div(decimal.NewFromInt(100))))
		if l.Qty.GreaterThan(allowance) {
			return r, fail(CodeOverDelivery, "line %d quantity %s exceeds allowance %s", l.LineNo, l.Qty, allowance)
		}
		if l.BatchTracked && l.Tracking.Batch == "" {
			return r, fail(CodeInvalidReceipt, "line %d requires a batch", l.LineNo)
		}
		if !l.SerialTracked {
			continue
		}
		if !l.Qty.IsInteger() || int64(len(l.Tracking.Serials)) != l.Qty.IntPart() {
			return r, fail(CodeInvalidReceipt, "line %d needs one serial per unit", l.LineNo)
		}
		for _, s := range l.Tracking.Serials {
			key := SerialKey{l.ProductID, s}
			_, dupInReceipt := seen[key]
			_, dupRecorded := recorded[key]
			if s == "" || dupInReceipt || dupRecorded {
				return r, fail(CodeDuplicateSerial, "line %d serial %q is empty or already recorded", l.LineNo, s)
			}
			seen[key] = struct{}{}
		}
	}
	return next, nil
}

func (r Receipt) Post(ctx context.Context, ledger LedgerPoster, profile PostingProfile) (Receipt, []GrirItem, error) {
	next, err := r.Transition(Posted, false)
	if err != nil {
		return r, nil, err
	}
	entry := JournalEntry{
		IdempotencyKey: fmt.Sprintf(receiptKeyFormat, r.ID),
		LegalEntityID:  r.LegalEntityID,
		PostingDate:    r.Date,
		DocumentDate:   r.Date,
		Source:         DocumentSource{Context: "receiving", DocumentType: "productreceipt", DocumentID: r.ID, Purpose: "receipt"},
		RateType:       "spot",
	}
	items := make([]GrirItem, 0, len(r.Lines))
	for _, l := range r.Lines {
		amount := amountOf(l.UnitPrice, l.Qty)
		entry.Lines = append(entry.Lines,
			JournalLine{Account: profile.Inventory, Amount: amount},
			JournalLine{Account: profile.GRIR, Amount: amount.Neg()})
		items = append(items, GrirItem{
			ReceiptID: r.ID, LineNo: l.LineNo, ReceivedQty: l.Qty,
			ReceivedAmount: amount, ClearedAmount: money.Amount{Value: decimal.Zero, Currency: amount.Currency},
		})
	}
	entryID, err := postReceiptEntry(ctx, ledger, entry, r.ID)
	if err != nil {
		return r, nil, err
	}
	next.LedgerEntryID = entryID
	return next, items, nil
}

func postReceiptEntry(ctx context.Context, ledger LedgerPoster, entry JournalEntry, receiptID string) (string, error) {
	entryID, err := ledger.Post(ctx, entry)
	if errors.Is(err, ErrPeriodClosed) {
		return "", fail(CodePeriodClosed, "ledger rejected receipt %s: period closed", receiptID)
	}
	if err != nil {
		slog.ErrorContext(ctx, "receipt ledger post failed", "err", err, "receiptId", receiptID)
		return "", fmt.Errorf("post receipt %s: %w", receiptID, err)
	}
	return entryID, nil
}

type LineChange struct {
	LineNo int
	Qty    decimal.Decimal
	Amount money.Amount
}

func (r Receipt) adjustmentEntry(key string, changes []LineChange, profile PostingProfile) JournalEntry {
	entry := JournalEntry{
		IdempotencyKey: key, LegalEntityID: r.LegalEntityID, PostingDate: r.Date, DocumentDate: r.Date,
		Source:   DocumentSource{Context: "receiving", DocumentType: "productreceipt", DocumentID: r.ID, Purpose: "receipt"},
		RateType: "spot",
	}
	byLine := map[int]ReceiptLine{}
	for _, l := range r.Lines {
		byLine[l.LineNo] = l
	}
	for _, c := range changes {
		delta := money.Amount{Value: c.Amount.Value.Sub(amountOf(byLine[c.LineNo].UnitPrice, byLine[c.LineNo].Qty).Value), Currency: c.Amount.Currency}
		if delta.Value.IsZero() {
			continue
		}
		entry.Lines = append(entry.Lines, JournalLine{Account: profile.Inventory, Amount: delta}, JournalLine{Account: profile.GRIR, Amount: delta.Neg()})
	}
	return entry
}

func (r Receipt) Correct(ctx context.Context, ledger LedgerPoster, profile PostingProfile, seq int, deltas map[int]decimal.Decimal) (string, []LineChange, error) {
	if r.State != Posted {
		return "", nil, fail(CodeInvalidTransition, "receipt %s is %s, only a POSTED receipt can be corrected", r.ID, r.State)
	}
	if len(deltas) == 0 {
		return "", nil, fail(CodeInvalidReceipt, "a correction needs at least one line delta")
	}
	var changes []LineChange
	for _, l := range r.Lines {
		delta, ok := deltas[l.LineNo]
		if !ok {
			continue
		}
		next := l.Qty.Add(delta)
		allowance := l.OrderedRemaining.Mul(decimal.NewFromInt(1).Add(l.OverDeliveryPct.Div(decimal.NewFromInt(100))))
		switch {
		case delta.IsZero():
			return "", nil, fail(CodeInvalidReceipt, "line %d delta must not be zero", l.LineNo)
		case next.Sign() <= 0:
			return "", nil, fail(CodeInvalidReceipt, "line %d corrected quantity must stay positive, cancel the receipt instead", l.LineNo)
		case next.LessThan(l.InvoicedQty):
			return "", nil, fail(CodeInvalidTransition, "line %d has %s invoiced and cannot go below it", l.LineNo, l.InvoicedQty)
		case delta.Sign() > 0 && next.GreaterThan(allowance):
			return "", nil, fail(CodeOverDelivery, "line %d quantity %s exceeds allowance %s", l.LineNo, next, allowance)
		case l.SerialTracked:
			return "", nil, fail(CodeInvalidReceipt, "line %d is serial tracked and cannot be corrected by quantity", l.LineNo)
		}
		changes = append(changes, LineChange{LineNo: l.LineNo, Qty: next, Amount: amountOf(l.UnitPrice, next)})
	}
	if len(changes) != len(deltas) {
		return "", nil, fail(CodeInvalidReceipt, "correction names lines the receipt does not have")
	}
	entryID, err := postReceiptEntry(ctx, ledger, r.adjustmentEntry(fmt.Sprintf(correctionKeyFormat, r.ID, seq), changes, profile), r.ID)
	return entryID, changes, err
}

func (r Receipt) Cancel(ctx context.Context, ledger LedgerPoster, profile PostingProfile) (string, error) {
	invoiced := slices.ContainsFunc(r.Lines, func(l ReceiptLine) bool { return l.InvoicedQty.Sign() > 0 })
	if _, err := r.Transition(Cancelled, invoiced); err != nil {
		return "", err
	}
	if r.State != Posted {
		return "", nil
	}
	changes := make([]LineChange, len(r.Lines))
	for i, l := range r.Lines {
		changes[i] = LineChange{LineNo: l.LineNo, Qty: decimal.Zero, Amount: money.Amount{Value: decimal.Zero, Currency: l.UnitPrice.Currency}}
	}
	return postReceiptEntry(ctx, ledger, r.adjustmentEntry(fmt.Sprintf(cancelKeyFormat, r.ID), changes, profile), r.ID)
}
