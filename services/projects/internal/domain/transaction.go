package domain

import (
	"fmt"
	"regexp"
	"time"
	"unicode/utf8"

	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/platform/money"
)

// Service-specific codes (not in §8.41.18) raised by the transaction rules of slice 4.
const (
	// CodeTxnStage: transactions need a project in IN_PROCESS (§8.41.5 command validation).
	CodeTxnStage = "PROJ-TXN-001"
	// CodeTxnCurrency: a transaction is recorded in its project's currency.
	CodeTxnCurrency = "PROJ-TXN-002"
	// CodeTxnCategory: the category does not exist or is bound to another
	// transaction type (§8.41.5: "category type must match txn_type").
	CodeTxnCategory = "PROJ-TXN-003"
	// CodeCategoryType: a category's transaction type cannot change once set.
	CodeCategoryType = "PROJ-CAT-001"
)

// TxnType is the kind of project transaction (FR-41.4).
type TxnType string

// Transaction types (§8.41.4 ProjectTransaction.txn_type).
const (
	TxnHour      TxnType = "HOUR"
	TxnCost      TxnType = "COST"
	TxnItem      TxnType = "ITEM"
	TxnFee       TxnType = "FEE"
	TxnOnAccount TxnType = "ON_ACCOUNT"
)

// TxnTypes lists every transaction type in display order.
var TxnTypes = []TxnType{TxnHour, TxnCost, TxnItem, TxnFee, TxnOnAccount}

// Source document types a transaction can come from (§8.41.4). Slice 4 records
// MANUAL transactions; the integrated documents of other waves use the rest.
const (
	SourceTimesheet  = "TIMESHEET"
	SourceExpense    = "EXPENSE"
	SourcePOReceipt  = "PO_RECEIPT"
	SourceInvJournal = "INV_JOURNAL"
	SourceManual     = "MANUAL"
)

var categoryIDPattern = regexp.MustCompile(`^[A-Z0-9][A-Z0-9_-]{0,19}$`)

// ValidTxnType reports whether t is one of the five transaction types.
func ValidTxnType(t TxnType) bool {
	for _, v := range TxnTypes {
		if v == t {
			return true
		}
	}
	return false
}

func validSourceDocType(s string) bool {
	switch s {
	case SourceTimesheet, SourceExpense, SourcePOReceipt, SourceInvJournal, SourceManual:
		return true
	}
	return false
}

// Category is a project category (§8.41.4 ProjectCategory): the code a
// transaction is booked under, bound to one transaction type.
type Category struct {
	ID        string    `json:"id"`
	Name      string    `json:"name"`
	TxnType   TxnType   `json:"txnType"`
	Version   int       `json:"version"`
	CreatedAt time.Time `json:"createdAt"`
	UpdatedAt time.Time `json:"updatedAt"`
}

// ValidCategoryID reports whether id is a category code: 1 to 20 capitals,
// digits, hyphens or underscores.
func ValidCategoryID(id string) bool { return categoryIDPattern.MatchString(id) }

// ValidateCategory returns field-level errors for a category (empty when valid).
func ValidateCategory(c Category) map[string]string {
	fields := map[string]string{}
	if !ValidCategoryID(c.ID) {
		fields["id"] = "must be 1 to 20 characters of A-Z, 0-9, hyphen or underscore"
	}
	if n := utf8.RuneCountInString(c.Name); n == 0 || n > 120 {
		fields["name"] = "must be 1 to 120 characters"
	}
	if !ValidTxnType(c.TxnType) {
		fields["txnType"] = "must be HOUR, COST, ITEM, FEE or ON_ACCOUNT"
	}
	return fields
}

// CheckCategoryType refuses changing the transaction type of an existing
// category: transactions already booked under it would no longer match it.
func CheckCategoryType(stored, next TxnType, id string) error {
	if stored != next {
		return rule(CodeCategoryType, "Category %s is bound to %s; its transaction type cannot change to %s", id, stored, next)
	}
	return nil
}

// CheckCategory checks that a transaction's category exists (found) and is
// bound to the transaction's type.
func CheckCategory(found bool, category TxnType, t Transaction) error {
	switch {
	case !found:
		return rule(CodeTxnCategory, "Category %s does not exist", t.CategoryID)
	case category != t.TxnType:
		return rule(CodeTxnCategory, "Category %s is for %s transactions, not %s", t.CategoryID, category, t.TxnType)
	}
	return nil
}

// Transaction is one cost, sales and quantity fact on a project (FR-41.4).
// Quantity, prices and amounts are decimal strings: quantity to three places,
// amounts in the minor unit of the project currency. WBSCode is resolved from
// WBSNodeID when the transaction is recorded (node ids change when a working
// plan's WBS is replaced; the code does not).
type Transaction struct {
	ID            string  `json:"id"`
	ProjectID     string  `json:"projectId"`
	WBSNodeID     *string `json:"wbsNodeId,omitempty"`
	WBSCode       *string `json:"wbsCode,omitempty"`
	TxnType       TxnType `json:"txnType"`
	CategoryID    string  `json:"categoryId"`
	LineProperty  string  `json:"lineProperty"`
	TxnDate       string  `json:"txnDate"`
	Quantity      string  `json:"quantity"`
	CostPrice     string  `json:"costPrice"`
	SalesPrice    string  `json:"salesPrice"`
	Currency      string  `json:"currency"`
	CostAmount    string  `json:"costAmount"`
	SalesAmount   string  `json:"salesAmount"`
	WorkerID      *string `json:"workerId,omitempty"`
	ItemID        *string `json:"itemId,omitempty"`
	VendorPartyID *string `json:"vendorPartyId,omitempty"`
	SourceDocType string  `json:"sourceDocType"`
	SourceDocID   *string `json:"sourceDocId,omitempty"`
	Description   string  `json:"description"`
	// JournalID is the ledger entry that holds the line's cost once POST /gl-postings has booked it (D2b).
	JournalID  *string   `json:"journalId,omitempty"`
	ProposalID *string   `json:"proposalId,omitempty"`
	CreatedBy  string    `json:"createdBy"`
	CreatedAt  time.Time `json:"createdAt"`
}

// Bounds of the §8.41.4 columns: quantity numeric(14,3), prices numeric(20,6).
var (
	maxQuantity = decimal.RequireFromString("99999999999.999")
	maxPrice    = decimal.RequireFromString("99999999999999.999999")
)

// ValidateTransaction returns field-level errors for a transaction request
// (empty when valid). Quantity is non-negative with at most three decimals;
// prices are non-negative with at most six, except the sales price of an
// ON_ACCOUNT transaction, which is negative when a retainer is applied (BR-41.4).
// An HOUR transaction names its worker and an ITEM transaction its item
// (§8.41.4 "cond by txn_type").
func ValidateTransaction(t Transaction) map[string]string {
	f := map[string]string{}
	if !ValidTxnType(t.TxnType) {
		f["txnType"] = "must be HOUR, COST, ITEM, FEE or ON_ACCOUNT"
	}
	if t.LineProperty != LineChargeable && t.LineProperty != LineNonChargeable {
		f["lineProperty"] = "must be CHARGEABLE or NON_CHARGEABLE"
	}
	if !ValidCategoryID(t.CategoryID) {
		f["categoryId"] = "must be a category code (1 to 20 characters of A-Z, 0-9, hyphen or underscore)"
	}
	if _, err := time.Parse(time.DateOnly, t.TxnDate); err != nil {
		f["txnDate"] = "must be a date (YYYY-MM-DD)"
	}
	if !currencyPattern.MatchString(t.Currency) {
		f["currency"] = "must be an ISO 4217 alpha-3 code in capitals"
	}
	if q, err := decimal.NewFromString(t.Quantity); err != nil || q.IsNegative() || q.Exponent() < -3 || q.GreaterThan(maxQuantity) {
		f["quantity"] = "must be a non-negative decimal with at most 3 places"
	}
	checkPrice := func(field, raw string, allowNegative bool) {
		p, err := decimal.NewFromString(raw)
		switch {
		case err != nil || p.Exponent() < -6 || p.Abs().GreaterThan(maxPrice):
			f[field] = "must be a decimal amount with at most 6 places"
		case p.IsNegative() && !allowNegative:
			f[field] = "must not be negative"
		}
	}
	checkPrice("costPrice", t.CostPrice, false)
	checkPrice("salesPrice", t.SalesPrice, t.TxnType == TxnOnAccount)
	if t.TxnType == TxnHour && t.WorkerID == nil {
		f["workerId"] = "is required for an HOUR transaction"
	}
	if t.TxnType == TxnItem && t.ItemID == nil {
		f["itemId"] = "is required for an ITEM transaction"
	}
	if !validSourceDocType(t.SourceDocType) {
		f["sourceDocType"] = "must be TIMESHEET, EXPENSE, PO_RECEIPT, INV_JOURNAL or MANUAL"
	}
	if utf8.RuneCountInString(t.Description) > 500 {
		f["description"] = "must be at most 500 characters"
	}
	return f
}

// LineAmount is qty × price rounded half-even to the currency's minor unit, per
// line and never per total (BR-41.5).
func LineAmount(qty, price decimal.Decimal, currency string) decimal.Decimal {
	return qty.Mul(price).RoundBank(money.MinorUnits(currency))
}

// PriceLine sets the cost and sales amounts of a validated transaction from its
// quantity and unit prices (BR-41.5) and normalises the decimal strings.
func PriceLine(t *Transaction) error {
	qty, err := decimal.NewFromString(t.Quantity)
	if err != nil {
		return fmt.Errorf("%w: quantity", ErrInvalidInput)
	}
	cost, err := decimal.NewFromString(t.CostPrice)
	if err != nil {
		return fmt.Errorf("%w: cost price", ErrInvalidInput)
	}
	sales, err := decimal.NewFromString(t.SalesPrice)
	if err != nil {
		return fmt.Errorf("%w: sales price", ErrInvalidInput)
	}
	minor := money.MinorUnits(t.Currency)
	t.Quantity = qty.StringFixed(3)
	t.CostPrice = cost.StringFixed(6)
	t.SalesPrice = sales.StringFixed(6)
	t.CostAmount = LineAmount(qty, cost, t.Currency).StringFixed(minor)
	t.SalesAmount = LineAmount(qty, sales, t.Currency).StringFixed(minor)
	return nil
}

// CheckTransactionProject refuses a transaction on a project that is not
// IN_PROCESS (§8.41.5: journal lines must reference an IN_PROCESS project) or
// in a currency other than the project's.
func CheckTransactionProject(stage Stage, currency string, t Transaction) error {
	if stage != StageInProcess {
		return rule(CodeTxnStage, "Project %s is %s; transactions need an IN_PROCESS project", t.ProjectID, stage)
	}
	if currency != t.Currency {
		return rule(CodeTxnCurrency, "Transaction currency %s differs from project currency %s", t.Currency, currency)
	}
	return nil
}

// ParseDate parses a date (YYYY-MM-DD) at UTC midnight, as Candidate and
// Criteria compare them.
func ParseDate(s string) (time.Time, error) { return time.Parse(time.DateOnly, s) }
