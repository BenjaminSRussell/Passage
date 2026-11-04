package domain

import (
	"fmt"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/shopspring/decimal"
)

// Service line problem codes (FR-17.23, BR-17.19, listed in the service README).
const (
	// CodeServiceTarget is PRC-SRV-001: the PO or line cannot take this service specification change.
	CodeServiceTarget = "PRC-SRV-001"
	// CodeServicePlanned is PRC-SRV-002: the planned outline is worth more than the line, or the
	// expected value is above the value limit, or the contract limit names a line that cannot back it.
	CodeServicePlanned = "PRC-SRV-002"
	// CodeServiceLimit is PRC-SRV-003: unplanned services exceed the value limit or the contract limit.
	CodeServiceLimit = "PRC-SRV-003"
	// CodeServiceOutline is PRC-SRV-004: outline items do not sum to the outline level's total.
	CodeServiceOutline = "PRC-SRV-004"
	// CodeServiceModel is PRC-SRV-005: the model service specification cannot be applied to the line.
	CodeServiceModel = "PRC-SRV-005"
)

// Outline node kinds.
const (
	OutlineLevel = "LEVEL"
	OutlineItem  = "ITEM"
)

// Outline bounds: the outline of one line or model holds at most MaxOutlineNodes nodes in at most
// MaxOutlineDepth levels, so a spec stays readable and the events small.
const (
	MaxOutlineNodes = 200
	MaxOutlineDepth = 5
)

// Model service specification statuses.
const (
	ModelActive   = "ACTIVE"
	ModelInactive = "INACTIVE"
)

// OutlineNode is a requested node of a service outline (the tree form of the API). A LEVEL groups
// children and carries only a description and, optionally, the total the caller expects them to
// reach; an ITEM is a service with a product or free text, quantity, unit and unit price.
type OutlineNode struct {
	Kind        string
	Description string
	ProductID   string
	Qty         decimal.Decimal
	Unit        string
	UnitPrice   decimal.Decimal
	// Total is the amount the caller states for a LEVEL; nil means none stated.
	Total    *decimal.Decimal
	Children []OutlineNode
}

// OutlineRow is a stored node in depth-first order. ParentSeq is 0 for a top-level node. A LEVEL's
// Amount is the sum of its children's amounts (BR-17.19).
type OutlineRow struct {
	Seq         int             `json:"seq"`
	ParentSeq   int             `json:"parentSeq,omitempty"`
	Depth       int             `json:"depth"`
	Kind        string          `json:"kind"`
	Description string          `json:"description,omitempty"`
	ProductID   string          `json:"productId,omitempty"`
	Qty         decimal.Decimal `json:"qty"`
	Unit        string          `json:"unit,omitempty"`
	UnitPrice   decimal.Decimal `json:"unitPrice"`
	Amount      decimal.Decimal `json:"amount"`
}

// Outline is a built outline: its rows and the planned total, the sum of the top-level amounts.
type Outline struct {
	Rows         []OutlineRow
	PlannedTotal decimal.Decimal
}

// BuildOutline validates a tree and flattens it depth first. Item amounts are quantity × unit
// price rounded to the currency's minor units; a level's amount is the sum of its children, and a
// stated Total that differs is PRC-SRV-004. An empty tree is a valid, empty outline.
func BuildOutline(nodes []OutlineNode, minor int32) (Outline, error) {
	b := outlineBuilder{minor: minor}
	total := decimal.Zero
	for i, n := range nodes {
		amount, err := b.add(n, 0, 1, "outline["+strconv.Itoa(i)+"]")
		if err != nil {
			return Outline{}, err
		}
		total = total.Add(amount)
	}
	return Outline{Rows: b.rows, PlannedTotal: total}, nil
}

type outlineBuilder struct {
	rows  []OutlineRow
	minor int32
}

func (b *outlineBuilder) add(n OutlineNode, parent, depth int, path string) (decimal.Decimal, error) {
	if depth > MaxOutlineDepth {
		return decimal.Zero, fail(CodeValidation, "%s: an outline holds at most %d levels", path, MaxOutlineDepth)
	}
	if len(b.rows) >= MaxOutlineNodes {
		return decimal.Zero, fail(CodeValidation, "an outline holds at most %d nodes", MaxOutlineNodes)
	}
	n.Description = strings.TrimSpace(n.Description)
	if utf8.RuneCountInString(n.Description) > 500 {
		return decimal.Zero, fail(CodeValidation, "%s: description is at most 500 characters", path)
	}
	row := OutlineRow{Seq: len(b.rows) + 1, ParentSeq: parent, Depth: depth, Kind: n.Kind, Description: n.Description}
	idx := len(b.rows)
	switch n.Kind {
	case OutlineItem:
		if len(n.Children) > 0 {
			return decimal.Zero, fail(CodeValidation, "%s: a service item has no children", path)
		}
		if n.Total != nil {
			return decimal.Zero, fail(CodeValidation, "%s: a service item states no total; its amount is quantity × unit price", path)
		}
		if n.Description == "" && n.ProductID == "" {
			return decimal.Zero, fail(CodeValidation, "%s: a service item needs a product or a description", path)
		}
		if !n.Qty.IsPositive() || !n.Qty.Equal(n.Qty.Round(qtyScale)) {
			return decimal.Zero, fail(CodeValidation, "%s: qty must be positive with at most %d decimals", path, qtyScale)
		}
		if strings.TrimSpace(n.Unit) == "" || utf8.RuneCountInString(strings.TrimSpace(n.Unit)) > 20 {
			return decimal.Zero, fail(CodeValidation, "%s: unit is required (at most 20 characters)", path)
		}
		if n.UnitPrice.IsNegative() || !n.UnitPrice.Equal(n.UnitPrice.Round(qtyScale)) {
			return decimal.Zero, fail(CodeValidation, "%s: unitPrice must not be negative and has at most %d decimals", path, qtyScale)
		}
		row.ProductID, row.Qty, row.Unit, row.UnitPrice = n.ProductID, n.Qty, strings.TrimSpace(n.Unit), n.UnitPrice
		row.Amount = n.Qty.Mul(n.UnitPrice).RoundBank(b.minor)
		b.rows = append(b.rows, row)
		return row.Amount, nil
	case OutlineLevel:
		if n.Description == "" {
			return decimal.Zero, fail(CodeValidation, "%s: an outline level needs a description", path)
		}
		if len(n.Children) == 0 {
			return decimal.Zero, fail(CodeValidation, "%s: an outline level needs at least one child", path)
		}
		b.rows = append(b.rows, row)
		sum := decimal.Zero
		for i, c := range n.Children {
			amount, err := b.add(c, row.Seq, depth+1, path+".children["+strconv.Itoa(i)+"]")
			if err != nil {
				return decimal.Zero, err
			}
			sum = sum.Add(amount)
		}
		if n.Total != nil && !n.Total.Equal(sum) {
			return decimal.Zero, fail(CodeServiceOutline, "Service outline items (%s) do not sum to outline total (%s) of %q", sum, *n.Total, n.Description)
		}
		b.rows[idx].Amount = sum
		return sum, nil
	}
	return decimal.Zero, fail(CodeValidation, "%s: kind must be LEVEL or ITEM", path)
}

// ServiceTerms are the limits of a service line (FR-17.23): the value limit for unplanned
// services, the value expected to be used, and the agreement line whose remaining value also
// bounds them.
type ServiceTerms struct {
	ValueLimit              decimal.Decimal
	ExpectedValue           decimal.Decimal
	ContractAgreementLineID string
}

// CheckServiceTarget refuses a service specification change unless the PO is still being prepared
// (DRAFT or REJECTED) and the line is live; a confirmed PO changes through change management.
func CheckServiceTarget(poStatus, lineStatus string) error {
	if poStatus != POStatusDraft && poStatus != POStatusRejected {
		return fail(CodeServiceTarget, "the service specification of a purchase order in %s cannot be changed; only a DRAFT or REJECTED order can", poStatus)
	}
	if lineStatus == POLineCancelled {
		return fail(CodeServiceTarget, "a cancelled line cannot take a service specification")
	}
	return nil
}

// ValidateServiceTerms checks the limits against each other and the outline against the line:
// limits are not negative with at most qtyScale decimals, the expected value is within the value
// limit and the planned outline within the line amount (PRC-SRV-002).
func ValidateServiceTerms(t ServiceTerms, planned, lineAmount decimal.Decimal) error {
	for name, v := range map[string]decimal.Decimal{"valueLimit": t.ValueLimit, "expectedValue": t.ExpectedValue} {
		if v.IsNegative() || !v.Equal(v.Round(qtyScale)) {
			return fail(CodeValidation, "%s must not be negative and has at most %d decimals", name, qtyScale)
		}
	}
	if t.ExpectedValue.GreaterThan(t.ValueLimit) {
		return fail(CodeServicePlanned, "the expected value (%s) is above the value limit (%s)", t.ExpectedValue, t.ValueLimit)
	}
	return CheckPlannedWithinLine(planned, lineAmount)
}

// CheckPlannedWithinLine is PRC-SRV-002 when the planned outline is worth more than the line.
func CheckPlannedWithinLine(planned, lineAmount decimal.Decimal) error {
	if planned.GreaterThan(lineAmount) {
		return fail(CodeServicePlanned, "the planned services (%s) are worth more than the line (%s); raise the line amount or trim the outline", planned, lineAmount)
	}
	return nil
}

// LimitCheck is the answer of the unplanned services check (BR-17.19) wave 18 makes for a
// service entry sheet: the limits, what is already entered, what remains and whether the entry
// fits. A refused entry is an answer, not an error: Allowed is false and Code and Detail carry
// PRC-SRV-003 and its message, so the caller keeps the figures (the sheet footer shows the limit
// remaining) and raises the code on its own sheet.
type LimitCheck struct {
	LineNo            int              `json:"lineNo"`
	Currency          string           `json:"currency"`
	ValueLimit        decimal.Decimal  `json:"valueLimit"`
	ContractRemaining *decimal.Decimal `json:"contractRemaining,omitempty"`
	UnplannedEntered  decimal.Decimal  `json:"unplannedEntered"`
	Entry             decimal.Decimal  `json:"entry"`
	// ValueRemaining is the value limit less what is entered, never negative.
	ValueRemaining decimal.Decimal `json:"valueRemaining"`
	// Remaining is what unplanned services may still add before this entry: the smaller of
	// ValueRemaining and the contract remaining less what is entered, never negative.
	Remaining decimal.Decimal `json:"remaining"`
	Allowed   bool            `json:"allowed"`
	Code      string          `json:"code,omitempty"`
	Detail    string          `json:"detail,omitempty"`
}

// CheckUnplanned applies BR-17.19: the unplanned services already entered plus this entry may not
// exceed the value limit, nor the remaining value of the contract line when there is one
// (contractRemaining non-nil). Exceeding either answers Allowed false with PRC-SRV-003; only a
// negative amount is an error.
func CheckUnplanned(valueLimit, entered, entry decimal.Decimal, contractRemaining *decimal.Decimal, lineNo int) (LimitCheck, error) {
	if entered.IsNegative() || entry.IsNegative() {
		return LimitCheck{}, fail(CodeValidation, "unplanned and entry must not be negative")
	}
	total := entered.Add(entry)
	out := LimitCheck{LineNo: lineNo, ValueLimit: valueLimit, UnplannedEntered: entered, Entry: entry,
		ValueRemaining: nonNegative(valueLimit.Sub(entered)), ContractRemaining: contractRemaining}
	out.Remaining = out.ValueRemaining
	if contractRemaining != nil {
		out.Remaining = decimal.Min(out.Remaining, nonNegative(contractRemaining.Sub(entered)))
	}
	switch {
	case total.GreaterThan(valueLimit):
		out.Code, out.Detail = CodeServiceLimit, fmt.Sprintf("Unplanned services %s exceed limit %s on PO line %d", total, valueLimit, lineNo)
	case contractRemaining != nil && total.GreaterThan(*contractRemaining):
		out.Code, out.Detail = CodeServiceLimit, fmt.Sprintf("Unplanned services %s exceed the remaining %s of the contract line on PO line %d", total, *contractRemaining, lineNo)
	default:
		out.Allowed = true
	}
	return out, nil
}

func nonNegative(d decimal.Decimal) decimal.Decimal {
	if d.IsNegative() {
		return decimal.Zero
	}
	return d
}

// ContractRemaining is the remaining value of an agreement line as a service contract limit: the
// remaining amount of a value commitment, or the remaining quantity at the line's net unit price
// for a quantity commitment. It is never negative.
func ContractRemaining(l AgreementLine, minor int32) decimal.Decimal {
	rem := l.Remaining()
	if l.CommitmentType == CommitQuantity {
		net := l.UnitPrice.Mul(decimal.NewFromInt(100).Sub(l.DiscountPct)).Shift(-2)
		rem = rem.Mul(net).RoundBank(minor)
	}
	if rem.IsNegative() {
		return decimal.Zero
	}
	return rem
}

// ModelService is a reusable service outline (SAP ML10–ML12): copied into a service line, it fills
// the outline. Prices are in Currency, which must be the PO's.
type ModelService struct {
	ID            string          `json:"id"`
	LegalEntityID string          `json:"legalEntityId"`
	Code          string          `json:"code"`
	Name          string          `json:"name"`
	Currency      string          `json:"currency"`
	Status        string          `json:"status"`
	PlannedTotal  decimal.Decimal `json:"plannedTotal"`
	Outline       []OutlineRow    `json:"outline"`
	Version       int             `json:"version"`
	CreatedBy     string          `json:"createdBy,omitempty"`
	CreatedAt     time.Time       `json:"createdAt"`
	UpdatedBy     string          `json:"updatedBy,omitempty"`
	UpdatedAt     time.Time       `json:"updatedAt"`
}

// CheckModelApplies is PRC-SRV-005 when the model cannot fill a line of this PO: it is inactive,
// of another legal entity or priced in another currency.
func CheckModelApplies(m ModelService, legalEntityID, currency string) error {
	switch {
	case m.Status != ModelActive:
		return fail(CodeServiceModel, "model service specification %s is %s", m.Code, m.Status)
	case m.LegalEntityID != legalEntityID:
		return fail(CodeServiceModel, "model service specification %s belongs to another legal entity", m.Code)
	case m.Currency != currency:
		return fail(CodeServiceModel, "model service specification %s is priced in %s and the purchase order in %s", m.Code, m.Currency, currency)
	}
	return nil
}

// ValidModelCode reports whether code is 1 to 20 of A-Z, 0-9, underscore and hyphen.
func ValidModelCode(code string) bool {
	if len(code) < 1 || len(code) > 20 {
		return false
	}
	for _, r := range code {
		if !(r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '_' || r == '-') {
			return false
		}
	}
	return true
}

// ToTree turns stored rows back into the nested form BuildOutline takes, so a model's outline can
// be rebuilt (and re-priced) for a line.
func ToTree(rows []OutlineRow) []OutlineNode {
	children := map[int][]OutlineRow{}
	for _, r := range rows {
		children[r.ParentSeq] = append(children[r.ParentSeq], r)
	}
	var build func(parent int) []OutlineNode
	build = func(parent int) []OutlineNode {
		var out []OutlineNode
		for _, r := range children[parent] {
			out = append(out, OutlineNode{Kind: r.Kind, Description: r.Description, ProductID: r.ProductID,
				Qty: r.Qty, Unit: r.Unit, UnitPrice: r.UnitPrice, Children: build(r.Seq)})
		}
		return out
	}
	return build(0)
}

// ServiceSpec is the specification of one SERVICE PO line (FR-17.23): its limits and outline.
// ContractRemaining is read from the agreement line when the specification names one.
type ServiceSpec struct {
	PurchaseOrderID         string           `json:"purchaseOrderId"`
	LineNo                  int              `json:"lineNo"`
	Currency                string           `json:"currency"`
	LineAmount              decimal.Decimal  `json:"lineAmount"`
	ValueLimit              decimal.Decimal  `json:"valueLimit"`
	ExpectedValue           decimal.Decimal  `json:"expectedValue"`
	PlannedTotal            decimal.Decimal  `json:"plannedTotal"`
	ContractAgreementLineID string           `json:"contractAgreementLineId,omitempty"`
	ContractRemaining       *decimal.Decimal `json:"contractRemaining,omitempty"`
	ModelID                 string           `json:"modelId,omitempty"`
	Outline                 []OutlineRow     `json:"outline"`
	UpdatedBy               string           `json:"updatedBy,omitempty"`
	UpdatedAt               time.Time        `json:"updatedAt"`
}

// CheckContractLine is PRC-SRV-002 when the agreement line named as the contract limit cannot back
// the PO: its agreement is not EFFECTIVE or belongs to another legal entity, vendor or currency.
func CheckContractLine(a Agreement, legalEntityID, vendorID, currency string) error {
	switch {
	case a.Status != AgreementEffective:
		return fail(CodeServicePlanned, "agreement %s is %s; a contract limit needs an EFFECTIVE agreement", a.Number, a.Status)
	case a.LegalEntityID != legalEntityID || a.VendorID != vendorID || a.Currency != currency:
		return fail(CodeServicePlanned, "agreement %s is of another legal entity, vendor or currency than the purchase order", a.Number)
	}
	return nil
}
