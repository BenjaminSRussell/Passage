package domain

import (
	"fmt"
	"slices"
	"time"

	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/platform/money"
)

// Journal and validation error codes (message catalog 3.16.18).
const (
	CodeInvalid            = "COM-0001"
	CodeJournalMax         = "INV-JNL-001"
	CodeOffsetRequired     = "INV-JNL-002"
	CodeJournalNotReady    = "INV-JNL-003"
	CodeUOMPrecision       = "INV-UOM-002"
	CodeDuplicateSerial    = "INV-SER-001"
	CodeSerialQty          = "INV-SER-002"
	CodeDimensionMandatory = "INV-DIM-001"
	CodeTransferLine       = "INV-JNL-006" // a transfer line breaks the transfer rules
)

// Error is a domain rule violation with a stable code.
type Error struct {
	Code   string
	Detail string
}

func (e *Error) Error() string { return e.Code + ": " + e.Detail }

func fail(code, format string, args ...any) *Error {
	return &Error{Code: code, Detail: fmt.Sprintf(format, args...)}
}

// JournalType is the kind of inventory journal. TRANSFER and BOM are not built
// yet (the BOM explosion is wave 9's).
type JournalType string

const (
	JournalMovement       JournalType = "MOVEMENT"
	JournalAdjustment     JournalType = "ADJUSTMENT"
	JournalOpeningBalance JournalType = "OPENING_BALANCE"
	// JournalCount carries the variances of a posted count sheet (FR-16.9). It
	// is created only by count-sheet posting and offsets to the adjustment
	// account like an ADJUSTMENT journal.
	JournalCount JournalType = "COUNT"
	// JournalOwnershipChange moves vendor-owned (consignment) stock to the
	// legal entity at the consignment price (FR-16.17, BR-16.14). Each line
	// names the vendor-owned source; it posts a paired issue and receipt that
	// differ only in the owner, and offsets to the consignment accrual account.
	JournalOwnershipChange JournalType = "OWNERSHIP_CHANGE"
	// JournalTransfer moves stock of one legal entity between two dimension
	// sets (location to location, warehouse to warehouse, segment to segment)
	// without a document in transit. Each line names the source (dimensions),
	// the destination (toDimensions) and the positive quantity moved; it posts
	// an issue at the source at its running average and a receipt of the same
	// value at the destination, so the inventory account does not move and no
	// offset account is needed.
	JournalTransfer JournalType = "TRANSFER"
)

// JournalTypes are the journal types stored and listed.
var JournalTypes = []JournalType{JournalMovement, JournalAdjustment, JournalOpeningBalance, JournalCount, JournalOwnershipChange, JournalTransfer}

// KeyedJournalTypes are the journal types a user may create directly; COUNT
// journals come from count sheets.
var KeyedJournalTypes = []JournalType{JournalMovement, JournalAdjustment, JournalOpeningBalance, JournalOwnershipChange, JournalTransfer}

// JournalStatus is the journal state machine of BR-16.11.
type JournalStatus string

const (
	JournalDraft     JournalStatus = "DRAFT"
	JournalSubmitted JournalStatus = "SUBMITTED"
	JournalApproved  JournalStatus = "APPROVED"
	JournalPosting   JournalStatus = "POSTING"
	JournalPosted    JournalStatus = "POSTED"
	JournalRejected  JournalStatus = "REJECTED"
	JournalCancelled JournalStatus = "CANCELLED"
)

// JournalStatuses lists every journal status.
var JournalStatuses = []JournalStatus{JournalDraft, JournalSubmitted, JournalApproved, JournalPosting, JournalPosted, JournalRejected, JournalCancelled}

var journalNext = map[JournalStatus][]JournalStatus{
	JournalDraft:     {JournalSubmitted, JournalCancelled},
	JournalSubmitted: {JournalApproved, JournalRejected},
	JournalRejected:  {JournalDraft, JournalCancelled},
	JournalApproved:  {JournalPosting},
	JournalPosting:   {JournalPosted},
}

// CanMoveJournal reports whether the journal state machine allows from -> to.
func CanMoveJournal(from, to JournalStatus) bool {
	return slices.Contains(journalNext[from], to)
}

// JournalLine is one keyed line. Qty is in the inventory unit; EnteredQty and
// EnteredUnit record what the user typed (equal to Qty until wave 6/8 unit
// conversion is wired in). ClientKey is the optional per-line idempotency key
// of lines:bulk. ReversesLineID links a reversal line to the posted line it
// negates (BR-16.12).
type JournalLine struct {
	ID             string
	LineNo         int
	ClientKey      string
	ReversesLineID string
	ProductID      string
	Dims           Dimensions
	Qty            decimal.Decimal
	EnteredQty     decimal.Decimal
	EnteredUnit    string
	CostPrice      decimal.Decimal
	// ToDims is the destination dimension set of a TRANSFER line; nil on any
	// other line.
	ToDims Dimensions
}

// SameContent reports whether two lines carry the same keyed values; it is the
// replay check for a repeated per-line client key.
func (l JournalLine) SameContent(o JournalLine) bool {
	a, errA := l.Dims.Normalize()
	b, errB := o.Dims.Normalize()
	return errA == nil && errB == nil && l.ProductID == o.ProductID && a.Canonical() == b.Canonical() &&
		l.Qty.Equal(o.Qty) && l.EnteredQty.Equal(o.EnteredQty) && l.EnteredUnit == o.EnteredUnit && l.CostPrice.Equal(o.CostPrice) &&
		l.sameDestination(o)
}

func (l JournalLine) sameDestination(o JournalLine) bool {
	a, errA := l.ToDims.Normalize()
	b, errB := o.ToDims.Normalize()
	return errA == nil && errB == nil && a.Canonical() == b.Canonical()
}

// Journal is an inventory journal with its lines. ReversesJournalID is set on
// a reversal journal; ReversedByJournalID on a journal that has one (BR-16.12).
type Journal struct {
	ID                  string
	LegalEntityID       string
	ReversesJournalID   string
	ReversedByJournalID string
	JournalNameID       string
	Type                JournalType
	Status              JournalStatus
	PostingDate         time.Time
	OffsetAccountID     string
	Currency            string
	Lines               []JournalLine
	LineCount           int
	Version             int
	GLEntry             *JournalEntry
	CreatedAt           time.Time
	PostedAt            *time.Time
}

// JournalRules are the tenant limits applied during validation.
type JournalRules struct {
	MaxLines int
	MaxScale int32
}

// DefaultJournalRules are the configured defaults (INV_JOURNAL_MAX_LINES=10000).
// MaxScale is the largest inventory unit precision (FR-16.10: max 6); the
// product's own unit decimals arrive with the wave 6/8 UoM lookup.
var DefaultJournalRules = JournalRules{MaxLines: 10000, MaxScale: 6}

// maxCostScale is the precision of cost_price (numeric(20,6)).
const maxCostScale = 6

// reservedDimensions are hashed but not accepted on keyed documents:
// license_plate belongs to wave 31, and owner is set only by a consignment
// replenishment receipt and read only by an ownership change (FR-16.17), which
// keeps vendor-owned stock free of value (BR-16.14).
var reservedDimensions = []string{DimLicensePlate, DimOwner}

// ValidateHeader checks the journal header: a supported type, a currency, and an
// offset account exactly when the journal is a MOVEMENT (FR-16.6: ADJUSTMENT
// and OPENING_BALANCE take their offset from the posting profile).
func ValidateHeader(j Journal) error {
	if !slices.Contains(JournalTypes, j.Type) {
		return fail(CodeInvalid, "journal type %q is not supported", j.Type)
	}
	if len(j.Currency) != 3 {
		return fail(CodeInvalid, "currency %q is not an ISO 4217 code", j.Currency)
	}
	if j.Type == JournalMovement && j.OffsetAccountID == "" {
		return fail(CodeOffsetRequired, "movement journal requires an offset account")
	}
	if j.Type != JournalMovement && j.OffsetAccountID != "" {
		return fail(CodeInvalid, "%s journals post to the posting profile account; leave the offset account empty", j.Type)
	}
	return nil
}

// ValidateLine checks a line of a valued document (any journal type but
// OWNERSHIP_CHANGE, and transfer lines); see ValidateLineFor.
func ValidateLine(l JournalLine, maxScale int32) error {
	return ValidateLineFor(JournalMovement, l, maxScale)
}

// ValidateLineFor checks the rules of one line of a journal of type t that
// need no database: product, non-zero quantity within precision, entered
// quantity on the same side, a non-negative cost, known dimensions with a site
// (FR-16.5), ±1 for a serial (BR-16.9), and the owner rule: an
// OWNERSHIP_CHANGE line names vendor-owned stock and a positive quantity (the
// quantity moved to the legal entity); any other line is refused on
// vendor-owned stock, which never carries value (INV-OWN-001, BR-16.14).
func ValidateLineFor(t JournalType, l JournalLine, maxScale int32) error {
	if l.ProductID == "" {
		return fail(CodeInvalid, "line %d has no product", l.LineNo)
	}
	if l.Qty.IsZero() {
		return fail(CodeInvalid, "line %d has zero quantity", l.LineNo)
	}
	if !l.Qty.Equal(l.Qty.Truncate(maxScale)) {
		return fail(CodeUOMPrecision, "line %d quantity %s has more than %d decimals", l.LineNo, l.Qty, maxScale)
	}
	if l.EnteredQty.Sign() != l.Qty.Sign() {
		return fail(CodeInvalid, "line %d entered quantity %s must have the sign of quantity %s", l.LineNo, l.EnteredQty, l.Qty)
	}
	if l.CostPrice.Sign() < 0 || !l.CostPrice.Equal(l.CostPrice.Truncate(maxCostScale)) {
		return fail(CodeInvalid, "line %d cost price must be non-negative with at most %d decimals", l.LineNo, maxCostScale)
	}
	dims, err := l.Dims.Normalize()
	if err != nil {
		return fail(CodeInvalid, "line %d: %v", l.LineNo, err)
	}
	switch {
	case t == JournalOwnershipChange && !VendorOwned(dims):
		return fail(CodeInvalid, "line %d: an ownership change line names the vendor-owned stock it moves (dimension owner)", l.LineNo)
	case t == JournalOwnershipChange && l.Qty.Sign() < 0:
		return fail(CodeInvalid, "line %d: an ownership change quantity is the positive quantity moved to the legal entity", l.LineNo)
	case t != JournalOwnershipChange && VendorOwned(dims):
		return fail(CodeOwnershipValue, "line %d: vendor-owned stock cannot be valued or financially posted; post an ownership change first", l.LineNo)
	}
	if dims[DimLicensePlate] != "" {
		return fail(CodeInvalid, "line %d: dimension %s is reserved and cannot be set yet", l.LineNo, DimLicensePlate)
	}
	if t == JournalTransfer {
		if err := validateTransferLine(l, dims); err != nil {
			return err
		}
	} else if len(l.ToDims) > 0 {
		return fail(CodeInvalid, "line %d: toDimensions apply only to TRANSFER journals", l.LineNo)
	}
	if dims[DimSite] == "" {
		return fail(CodeDimensionMandatory, "dimension %s is mandatory for product %s (line %d)", DimSite, l.ProductID, l.LineNo)
	}
	if dims[DimSerial] != "" && !l.Qty.Abs().Equal(decimal.NewFromInt(1)) {
		return fail(CodeSerialQty, "serial-tracked product %s requires quantity ±1 per transaction (line %d)", l.ProductID, l.LineNo)
	}
	return nil
}

// ValidateJournal checks the rules that do not need the database: header,
// line count and every line.
func ValidateJournal(j Journal, r JournalRules) error {
	if err := ValidateHeader(j); err != nil {
		return err
	}
	if len(j.Lines) == 0 {
		return fail(CodeInvalid, "journal has no lines")
	}
	if len(j.Lines) > r.MaxLines {
		return fail(CodeJournalMax, "journal exceeds %d lines", r.MaxLines)
	}
	for _, l := range j.Lines {
		if err := ValidateLineFor(j.Type, l, r.MaxScale); err != nil {
			return err
		}
	}
	if j.Type == JournalOwnershipChange {
		return CheckOwnershipLines(j.Lines)
	}
	if j.Type == JournalTransfer {
		return CheckTransferLines(j.Lines)
	}
	return nil
}

// transferFixedDimensions are the dimensions a TRANSFER line keeps from source
// to destination: the tracking dimensions (batch, serial) and the product
// dimensions (config, size, color, style), which identify what the stock is
// rather than where it sits.
var transferFixedDimensions = []string{DimBatch, DimSerial, DimConfig, DimSize, DimColor, DimStyle}

// validateTransferLine checks one TRANSFER line: a positive quantity, a
// destination with a site that differs from the source, no restricted status
// on either side (blocking and release own those moves), and unchanged
// tracking and product dimensions (transferFixedDimensions).
func validateTransferLine(l JournalLine, from Dimensions) error {
	if l.Qty.Sign() < 0 {
		return fail(CodeTransferLine, "line %d: a transfer quantity is the positive quantity moved from the source to the destination", l.LineNo)
	}
	to, err := l.ToDims.Normalize()
	if err != nil {
		return fail(CodeTransferLine, "line %d destination: %v", l.LineNo, err)
	}
	if to[DimSite] == "" {
		return fail(CodeDimensionMandatory, "dimension %s is mandatory on the destination of line %d", DimSite, l.LineNo)
	}
	if VendorOwned(to) || to[DimLicensePlate] != "" {
		return fail(CodeTransferLine, "line %d: the destination cannot be vendor-owned or carry a license plate", l.LineNo)
	}
	if string(to.Hash()) == string(from.Hash()) {
		return fail(CodeTransferLine, "line %d: the destination equals the source", l.LineNo)
	}
	for _, name := range transferFixedDimensions {
		if to[name] != from[name] {
			return fail(CodeTransferLine, "line %d: a transfer cannot change the %s (it moves stock, it does not relabel it)", l.LineNo, name)
		}
	}
	for _, s := range []string{from[DimStatus], to[DimStatus]} {
		if s == StockBlocked || s == StockQuarantine {
			return fail(CodeTransferLine, "line %d: stock is blocked or quarantined through a blocking, not a transfer", l.LineNo)
		}
	}
	return nil
}

// CheckTransferLines rejects a TRANSFER journal in which one projection row
// (product and dimension set) is both a source and a destination: the
// receipts take the value of the issues, so a row must be one or the other
// within a journal. Chain the moves in two journals.
func CheckTransferLines(lines []JournalLine) error {
	from, to := map[string]int{}, map[string]int{}
	for _, l := range lines {
		src, err := l.Dims.Normalize()
		if err != nil {
			return fail(CodeInvalid, "line %d: %v", l.LineNo, err)
		}
		dst, err := l.ToDims.Normalize()
		if err != nil {
			return fail(CodeTransferLine, "line %d destination: %v", l.LineNo, err)
		}
		from[l.ProductID+"|"+string(src.Hash())] = l.LineNo
		to[l.ProductID+"|"+string(dst.Hash())] = l.LineNo
	}
	for k, n := range from {
		if m, ok := to[k]; ok {
			return fail(CodeTransferLine, "line %d is the source of one transfer line and line %d its destination; split the chain into two journals", n, m)
		}
	}
	return nil
}

// PostedLine is a journal line planned as one inventory transaction. Value is
// the signed cost in the accounting currency, rounded to its minor unit; it is
// set by the posting once the on-hand row is locked (issues are valued at the
// row's running average). Corrects is the transaction a reversal line
// negates (corrects_transaction_id, FR-16.1). Paired marks the receipt leg of
// an ownership change, which shares its journal line with the issue leg.
type PostedLine struct {
	Line     JournalLine
	Status   Status
	Value    decimal.Decimal
	Corrects string
	Paired   bool
}

// PlanPosting turns journal lines into transactions. Each line is posted in one
// step at its financial status: receipts become PURCHASED, issues become SOLD,
// so posted quantity and value move with the journal's GL entry. Physical
// statuses other than these are reached by later slices (receiving, shipping).
//
// An OWNERSHIP_CHANGE line becomes a pair (FR-16.17, BR-16.14): a DEDUCTED
// issue of the vendor-owned source, which stays quantity-only and is valued at
// zero, and a PURCHASED receipt of the same quantity at the same dimensions
// owned by the legal entity, valued at the consignment price. Physical stock
// summed across owners is unchanged.
func PlanPosting(j Journal) []PostedLine {
	if j.Type == JournalTransfer {
		// Issue at the source and receipt at the destination, adjacent: the
		// receipt of plan[i] is plan[i+1]. The receipt takes the issue's value.
		out := make([]PostedLine, 0, 2*len(j.Lines))
		for _, l := range j.Lines {
			issue := l
			issue.Qty, issue.EnteredQty = l.Qty.Neg(), l.EnteredQty.Neg()
			receipt := l
			receipt.Dims = l.ToDims
			out = append(out, PostedLine{Line: issue, Status: StatusSold}, PostedLine{Line: receipt, Status: StatusPurchased, Paired: true})
		}
		return out
	}
	if j.Type == JournalOwnershipChange {
		out := make([]PostedLine, 0, 2*len(j.Lines))
		for _, l := range j.Lines {
			issue := l
			issue.Qty, issue.EnteredQty = l.Qty.Neg(), l.EnteredQty.Neg()
			receipt := l
			receipt.Dims = WithOwner(l.Dims, "")
			out = append(out, PostedLine{Line: issue, Status: StatusDeducted, Value: decimal.Zero},
				PostedLine{Line: receipt, Status: StatusPurchased, Paired: true})
		}
		return out
	}
	out := make([]PostedLine, 0, len(j.Lines))
	for _, l := range j.Lines {
		st := StatusSold
		if l.Qty.Sign() > 0 {
			st = StatusPurchased
		}
		out = append(out, PostedLine{Line: l, Status: st})
	}
	return out
}

// ReverseJournal builds the DRAFT reversal of a POSTED journal
// (BR-16.12): same legal entity, type, offset account and currency, every line
// negated and linked to the line it reverses. The original is not changed; the
// caller assigns the ids. A reversal is posted like any journal, but each line
// is valued at the negated value of the transaction it corrects, so the GL
// entry is the exact mirror of the original.
func ReverseJournal(orig Journal, newID string, postingDate time.Time) (Journal, error) {
	if orig.Status != JournalPosted {
		return Journal{}, fail(CodeJournalNotReady, "only a POSTED journal can be reversed; this one is %s", orig.Status)
	}
	if orig.Type == JournalOwnershipChange {
		return Journal{}, fail(CodeJournalNotReady, "an ownership change cannot be reversed: returning stock to the vendor is a later slice")
	}
	if orig.Type == JournalTransfer {
		return Journal{}, fail(CodeJournalNotReady, "a transfer cannot be reversed: post a transfer back from the destination")
	}
	if orig.ReversedByJournalID != "" {
		return Journal{}, fail(CodeJournalNotReady, "journal is already reversed by %s", orig.ReversedByJournalID)
	}
	out := Journal{ID: newID, LegalEntityID: orig.LegalEntityID, ReversesJournalID: orig.ID, Type: orig.Type, Status: JournalDraft,
		PostingDate: postingDate, OffsetAccountID: orig.OffsetAccountID, Currency: orig.Currency, Version: 1}
	for _, l := range orig.Lines {
		out.Lines = append(out.Lines, JournalLine{LineNo: l.LineNo, ReversesLineID: l.ID, ProductID: l.ProductID, Dims: l.Dims,
			Qty: l.Qty.Neg(), EnteredQty: l.EnteredQty.Neg(), EnteredUnit: l.EnteredUnit, CostPrice: l.CostPrice})
	}
	return out, nil
}

// CopyJournal builds a new DRAFT journal with the header and the lines of src
// (AX copy journal): same legal entity, type, offset account, journal name and
// currency, the given posting date, every line copied with its quantities,
// unit, cost price and dimensions, without client keys or reversal links. Any
// status can be copied, so a posted journal is a template for the next one. A
// COUNT journal belongs to its sheet and a reversal to the journal it
// reverses (a copy would lose the link that values it at the original), so
// neither can be copied.
func CopyJournal(src Journal, newID string, postingDate time.Time) (Journal, error) {
	if !slices.Contains(KeyedJournalTypes, src.Type) {
		return Journal{}, fail(CodeJournalNotReady, "%s journals are created by their source document and cannot be copied", src.Type)
	}
	if src.ReversesJournalID != "" {
		return Journal{}, fail(CodeJournalNotReady, "a reversal cannot be copied: it only makes sense against the journal it reverses")
	}
	out := Journal{ID: newID, LegalEntityID: src.LegalEntityID, Type: src.Type, Status: JournalDraft, PostingDate: postingDate,
		OffsetAccountID: src.OffsetAccountID, Currency: src.Currency, JournalNameID: src.JournalNameID, Version: 1}
	for _, l := range src.Lines {
		out.Lines = append(out.Lines, JournalLine{LineNo: l.LineNo, ProductID: l.ProductID, Dims: l.Dims, Qty: l.Qty, EnteredQty: l.EnteredQty,
			EnteredUnit: l.EnteredUnit, CostPrice: l.CostPrice, ToDims: l.ToDims})
	}
	return out, nil
}

// ReceiptValue is a receipt's value: quantity times the entered cost price,
// rounded to the currency's minor unit (BR-16.5).
func ReceiptValue(qty, costPrice decimal.Decimal, currency string) decimal.Decimal {
	return money.Amount{Value: qty.Mul(costPrice), Currency: currency}.Round().Value
}

// IssueValue values an issue of qty (negative) against the row's running
// average, posted_value / posted_qty (PRD 3.16.1: issues at the running
// average until wave 20). An issue that empties the row takes its whole posted
// value, so no rounding residue stays behind. Quantity beyond the posted
// quantity (negative stock where the policy allows it), or an issue from a row
// with no positive posted quantity, is valued at fallbackCost. The result is
// negative or zero, rounded to the currency's minor unit.
func (o OnHand) IssueValue(qty, fallbackCost decimal.Decimal, currency string) decimal.Decimal {
	out := qty.Abs()
	if o.PostedQty.Sign() <= 0 {
		return ReceiptValue(out, fallbackCost, currency).Neg()
	}
	if out.LessThan(o.PostedQty) {
		avg := o.PostedValue.Div(o.PostedQty)
		return ReceiptValue(out, avg, currency).Neg()
	}
	beyond := ReceiptValue(out.Sub(o.PostedQty), fallbackCost, currency)
	return o.PostedValue.Add(beyond).Neg()
}

// GLLine is one ledger line of a journal entry, in journal currency.
type GLLine struct {
	Account string          `json:"account"`
	Debit   decimal.Decimal `json:"debit"`
	Credit  decimal.Decimal `json:"credit"`
}

// JournalEntry is the balanced GL entry a posted inventory journal produces.
type JournalEntry struct {
	Currency string   `json:"currency"`
	Lines    []GLLine `json:"lines"`
}

// Balanced reports whether total debits equal total credits.
func (e JournalEntry) Balanced() bool {
	dr, cr := decimal.Zero, decimal.Zero
	for _, l := range e.Lines {
		dr = dr.Add(l.Debit)
		cr = cr.Add(l.Credit)
	}
	return dr.Equal(cr)
}

// BuildEntry builds the GL entry for a valued plan: receipts debit the
// inventory account and credit the offset, issues debit the offset and credit
// inventory. Each side is one line per account, zero lines are left out, and
// every amount is already rounded, so the entry balances exactly and the net
// on the inventory account equals the change in posted value.
func BuildEntry(plan []PostedLine, inventoryAccount, offsetAccount, currency string) (JournalEntry, error) {
	if len(currency) != 3 {
		return JournalEntry{}, fmt.Errorf("invalid currency %q", currency)
	}
	if inventoryAccount == "" || offsetAccount == "" {
		return JournalEntry{}, fmt.Errorf("inventory and offset accounts are required")
	}
	receipts, issues := decimal.Zero, decimal.Zero
	for _, p := range plan {
		if p.Value.Sign() >= 0 {
			receipts = receipts.Add(p.Value)
		} else {
			issues = issues.Add(p.Value.Neg())
		}
	}
	entry := JournalEntry{Currency: currency, Lines: []GLLine{}}
	if receipts.Sign() > 0 {
		entry.Lines = append(entry.Lines,
			GLLine{Account: inventoryAccount, Debit: receipts, Credit: decimal.Zero},
			GLLine{Account: offsetAccount, Debit: decimal.Zero, Credit: receipts})
	}
	if issues.Sign() > 0 {
		entry.Lines = append(entry.Lines,
			GLLine{Account: offsetAccount, Debit: issues, Credit: decimal.Zero},
			GLLine{Account: inventoryAccount, Debit: decimal.Zero, Credit: issues})
	}
	return entry, nil
}

// Net returns debits minus credits on account.
func (e JournalEntry) Net(account string) decimal.Decimal {
	net := decimal.Zero
	for _, l := range e.Lines {
		if l.Account == account {
			net = net.Add(l.Debit).Sub(l.Credit)
		}
	}
	return net
}
