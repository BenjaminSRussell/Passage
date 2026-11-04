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
	CodePeriodUnresolved  = "PER-AP-002"
	CodeOverInvoice       = "AP-QTY-001"
	CodeDuplicateInvoice  = "AP-DUP-001"
	CodeCorrectionBelow   = "RCV-COR-001"
	CodePostingPending    = "RCV-IDM-001"
	CodeLedgerRejected    = "RCV-LDG-001"

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
	ClearedAmount    decimal.Decimal
	OrderedRemaining decimal.Decimal
	OverDeliveryPct  decimal.Decimal
	UnitPrice        money.Amount
	BatchTracked     bool
	SerialTracked    bool
	Tracking         Tracking
	// CWQty is the catch-weight count (cases). Qty is the actual weight.
	CWQty       int
	CatchWeight CatchWeight
}

// SerialUnits is the number of serials a line needs: cases when the line is
// catch-weight (Qty is then the weight), otherwise Qty.
func (l ReceiptLine) SerialUnits() decimal.Decimal {
	if l.CWQty > 0 {
		return decimal.NewFromInt(int64(l.CWQty))
	}
	return l.Qty
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
		if l.CWQty > 0 || l.CatchWeight.Set() {
			if err := ValidateCatchWeight(l.CatchWeight, l.CWQty, l.Qty); err != nil {
				return r, err
			}
		}
		allowance := l.OrderedRemaining.Mul(decimal.NewFromInt(1).Add(l.OverDeliveryPct.Div(decimal.NewFromInt(100))))
		if l.Qty.GreaterThan(allowance) {
			return r, fail(CodeOverDelivery, "line %d quantity %s exceeds allowance %s", l.LineNo, l.Qty, allowance)
		}
		units := l.SerialUnits()
		if l.BatchTracked && l.Tracking.Batch == "" {
			return r, fail(CodeInvalidReceipt, "line %d requires a batch", l.LineNo)
		}
		if !l.SerialTracked {
			continue
		}
		if !units.IsInteger() || int64(len(l.Tracking.Serials)) != units.IntPart() {
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
		LedgerID:       profile.LedgerID,
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
	if errors.Is(err, ErrLedgerConflict) {
		return "", fail(CodePostingPending, "ledger holds a different posting under the key for receipt %s", receiptID)
	}
	if err != nil {
		slog.ErrorContext(ctx, "receipt ledger post failed", "err", err, "receiptId", receiptID)
		return "", fmt.Errorf("post receipt %s: %w", receiptID, err)
	}
	return entryID, nil
}

// LineChange is a line's quantity (and catch-weight count) after an
// adjustment, with the amount it now carries.
type LineChange struct {
	LineNo int
	Qty    decimal.Decimal
	CWQty  int
	Amount money.Amount
}

// WithCaseDeltas applies catch-weight count corrections (cases, FR-24.7) before
// Correct re-checks BR-24.5 against the corrected weight and count. Only a
// catch-weight line takes one, and the count stays at least 1.
func (r Receipt) WithCaseDeltas(deltas map[int]int) (Receipt, error) {
	if len(deltas) == 0 {
		return r, nil
	}
	out := r
	out.Lines = slices.Clone(r.Lines)
	seen := 0
	for i, l := range out.Lines {
		d, ok := deltas[l.LineNo]
		if !ok {
			continue
		}
		seen++
		switch next := l.CWQty + d; {
		case l.CWQty < 1:
			return r, fail(CodeInvalidReceipt, "line %d is not a catch-weight line and has no case count to correct", l.LineNo)
		case d == 0:
			return r, fail(CodeInvalidReceipt, "line %d case delta must not be zero", l.LineNo)
		case next < 1:
			return r, fail(CodeInvalidReceipt, "line %d corrected case count must stay at least 1, cancel the receipt instead", l.LineNo)
		default:
			out.Lines[i].CWQty = next
		}
	}
	if seen != len(deltas) {
		return r, fail(CodeInvalidReceipt, "correction names lines the receipt does not have")
	}
	return out, nil
}

func (r Receipt) adjustmentEntry(key string, postingDate time.Time, changes []LineChange, profile PostingProfile) JournalEntry {
	entry := JournalEntry{
		IdempotencyKey: key, LegalEntityID: r.LegalEntityID, LedgerID: profile.LedgerID, PostingDate: postingDate, DocumentDate: r.Date,
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

func (r Receipt) adjustmentDate(postingDate time.Time) (time.Time, error) {
	if postingDate.IsZero() {
		return r.Date, nil
	}
	if postingDate.Before(r.Date) {
		return time.Time{}, fail(CodeInvalidReceipt, "posting date %s is before the receipt date %s", postingDate.Format(time.DateOnly), r.Date.Format(time.DateOnly))
	}
	return postingDate, nil
}

func (r Receipt) Correct(ctx context.Context, ledger LedgerPoster, profile PostingProfile, seq int, postingDate time.Time, deltas map[int]decimal.Decimal) (string, []LineChange, error) {
	date, err := r.adjustmentDate(postingDate)
	if err != nil {
		return "", nil, err
	}
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
			return "", nil, fail(CodeCorrectionBelow, "line %d has %s invoiced and cannot go below it", l.LineNo, l.InvoicedQty)
		case delta.Sign() > 0 && next.GreaterThan(allowance):
			return "", nil, fail(CodeOverDelivery, "line %d quantity %s exceeds allowance %s", l.LineNo, next, allowance)
		case l.SerialTracked:
			return "", nil, fail(CodeInvalidReceipt, "line %d is serial tracked and cannot be corrected by quantity", l.LineNo)
		}
		if l.CWQty > 0 {
			if err := ValidateCatchWeight(l.CatchWeight, l.CWQty, next); err != nil {
				return "", nil, err
			}
		}
		amount := amountOf(l.UnitPrice, next)
		if l.InvoicedQty.Sign() > 0 && next.Equal(l.InvoicedQty) {
			amount = money.Amount{Value: l.ClearedAmount, Currency: l.UnitPrice.Currency}
		}
		changes = append(changes, LineChange{LineNo: l.LineNo, Qty: next, CWQty: l.CWQty, Amount: amount})
	}
	if len(changes) != len(deltas) {
		return "", nil, fail(CodeInvalidReceipt, "correction names lines the receipt does not have")
	}
	entryID, err := postReceiptEntry(ctx, ledger, r.adjustmentEntry(fmt.Sprintf(correctionKeyFormat, r.ID, seq), date, changes, profile), r.ID)
	return entryID, changes, err
}

func (r Receipt) Cancel(ctx context.Context, ledger LedgerPoster, profile PostingProfile, postingDate time.Time) (string, error) {
	invoiced := slices.ContainsFunc(r.Lines, func(l ReceiptLine) bool { return l.InvoicedQty.Sign() > 0 })
	if _, err := r.Transition(Cancelled, invoiced); err != nil {
		return "", err
	}
	if r.State != Posted {
		return "", nil
	}
	date, err := r.adjustmentDate(postingDate)
	if err != nil {
		return "", err
	}
	changes := make([]LineChange, len(r.Lines))
	for i, l := range r.Lines {
		changes[i] = LineChange{LineNo: l.LineNo, Qty: decimal.Zero, Amount: money.Amount{Value: decimal.Zero, Currency: l.UnitPrice.Currency}}
	}
	return postReceiptEntry(ctx, ledger, r.adjustmentEntry(fmt.Sprintf(cancelKeyFormat, r.ID), date, changes, profile), r.ID)
}
