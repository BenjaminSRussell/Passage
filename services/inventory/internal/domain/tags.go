package domain

import (
	"fmt"
	"slices"
	"strings"

	"github.com/shopspring/decimal"
)

// Tag counting codes. INV-TAG-001 is from the message catalog (3.16.18); the
// others are service codes.
const (
	CodeTagUsed     = "INV-TAG-001" // the tag number has already been used
	CodeTagOpen     = "INV-TAG-002" // open tags block the transfer to a count sheet
	CodeTagState    = "INV-TAG-003" // the tag is VOID, MISSING or already on a count sheet
	CodeTagNoneUsed = "INV-TAG-004" // no used tag to transfer
	CodeTagNotFound = "INV-TAG-005" // unknown count tag
	CodeTagApproval = "INV-TAG-006" // marking open tags MISSING needs the supervisor (inventory.count.approve)
)

// MaxTagRange bounds one tag generation call (FR-16.18).
const MaxTagRange = 1000

// TagStatus is the state of one pre-numbered count tag.
type TagStatus string

const (
	TagOpen    TagStatus = "OPEN"
	TagUsed    TagStatus = "USED"
	TagVoid    TagStatus = "VOID"
	TagMissing TagStatus = "MISSING"
)

// TagStatuses lists every tag status.
var TagStatuses = []TagStatus{TagOpen, TagUsed, TagVoid, TagMissing}

// TagNumber formats a tag sequence as TAG-000001 (FR-16.18 TAG- sequence).
func TagNumber(seq int64) string { return fmt.Sprintf("TAG-%06d", seq) }

// CountTag is one printed count tag of a plan. A tag is used at most once
// (BR-16.15); once its quantity is transferred to a count sheet (SheetID set)
// it is final.
type CountTag struct {
	ID        string
	Seq       int64
	Number    string
	PlanID    string
	Status    TagStatus
	ProductID string
	Dims      Dimensions
	Qty       decimal.Decimal
	CountedBy string
	SheetID   string
	Version   int
}

// UseTag records a count on an OPEN tag: product, dimension set and counted
// quantity. A used tag is INV-TAG-001; a void, missing or transferred tag is
// INV-TAG-003. A zero count is a valid count (BR-16.15). The entry must be
// postable later as a COUNT journal line: site mandatory, no reserved
// dimension, a serial counts 0 or 1. When the plan names a warehouse the tag
// belongs to it: an empty warehouse takes the plan's, another one is refused.
func UseTag(t CountTag, planWarehouse, productID string, dims Dimensions, qty decimal.Decimal, maxScale int32) (CountTag, error) {
	switch {
	case t.SheetID != "":
		return t, fail(CodeTagState, "tag %s is already on a count sheet", t.Number)
	case t.Status == TagUsed:
		return t, fail(CodeTagUsed, "tag %s has already been used", t.Number)
	case t.Status != TagOpen:
		return t, fail(CodeTagState, "tag %s is %s and cannot be counted", t.Number, t.Status)
	}
	if productID == "" {
		return t, fail(CodeInvalid, "tag %s needs a product", t.Number)
	}
	if err := ValidateCounted(qty, maxScale); err != nil {
		return t, err
	}
	n, err := dims.Normalize()
	if err != nil {
		return t, fail(CodeInvalid, "tag %s: %v", t.Number, err)
	}
	for _, name := range reservedDimensions {
		if n[name] != "" {
			return t, fail(CodeInvalid, "tag %s: dimension %s is reserved and cannot be set yet", t.Number, name)
		}
	}
	if planWarehouse != "" {
		switch n[DimWarehouse] {
		case "":
			n[DimWarehouse] = planWarehouse
		case planWarehouse:
		default:
			return t, fail(CodeInvalid, "tag %s belongs to warehouse %s, not %s", t.Number, planWarehouse, n[DimWarehouse])
		}
	}
	if n[DimSite] == "" {
		return t, fail(CodeDimensionMandatory, "dimension %s is mandatory for product %s (tag %s)", DimSite, productID, t.Number)
	}
	if n[DimSerial] != "" && qty.GreaterThan(decimal.NewFromInt(1)) {
		return t, fail(CodeSerialQty, "a serial counts 0 or 1 (tag %s)", t.Number)
	}
	t.Status, t.ProductID, t.Dims, t.Qty = TagUsed, productID, n, qty
	return t, nil
}

// VoidTag voids an OPEN or USED tag that has not been transferred.
func VoidTag(t CountTag) (CountTag, error) {
	if t.SheetID != "" {
		return t, fail(CodeTagState, "tag %s is on a count sheet and cannot be voided", t.Number)
	}
	if t.Status != TagOpen && t.Status != TagUsed {
		return t, fail(CodeTagState, "tag %s is %s and cannot be voided", t.Number, t.Status)
	}
	t.Status = TagVoid
	return t, nil
}

// TagLine is one count sheet line of a tag transfer: the used tags of one
// product and dimension set, summed.
type TagLine struct {
	ProductID string
	Dims      Dimensions
	Qty       decimal.Decimal
	TagIDs    []string
}

// TagTransfer is the plan for moving used tags into a count sheet. MissingIDs
// are the open tags the transfer marks MISSING.
type TagTransfer struct {
	MissingIDs []string
	Lines      []TagLine
}

// PlanTagTransfer aggregates the untransferred USED tags by product and
// dimension set (FR-16.18). Open tags block the transfer with INV-TAG-002
// unless markOpenMissing is set, in which case they become MISSING (BR-16.15).
// Tags already on a sheet, void and missing tags are left out. Lines come out
// in (product, dimension hash) order, the projection lock order.
func PlanTagTransfer(tags []CountTag, markOpenMissing bool) (TagTransfer, error) {
	var out TagTransfer
	var used []CountTag
	for _, t := range tags {
		if t.SheetID != "" {
			continue
		}
		switch t.Status {
		case TagOpen:
			out.MissingIDs = append(out.MissingIDs, t.ID)
		case TagUsed:
			used = append(used, t)
		}
	}
	if len(out.MissingIDs) > 0 && !markOpenMissing {
		return out, fail(CodeTagOpen, "%d tag(s) are still open; enter them, void them or mark them missing before the transfer", len(out.MissingIDs))
	}
	if len(used) == 0 {
		return out, fail(CodeTagNoneUsed, "there is no used tag to transfer")
	}
	index := map[string]int{}
	for _, t := range used {
		dims, err := t.Dims.Normalize()
		if err != nil {
			return out, fail(CodeInvalid, "tag %s: %v", t.Number, err)
		}
		key := t.ProductID + "|" + string(dims.Hash())
		i, ok := index[key]
		if !ok {
			i = len(out.Lines)
			index[key] = i
			out.Lines = append(out.Lines, TagLine{ProductID: t.ProductID, Dims: dims, Qty: decimal.Zero})
		}
		out.Lines[i].Qty = out.Lines[i].Qty.Add(t.Qty)
		out.Lines[i].TagIDs = append(out.Lines[i].TagIDs, t.ID)
	}
	slices.SortFunc(out.Lines, func(a, b TagLine) int {
		return strings.Compare(a.ProductID+"|"+string(a.Dims.Hash()), b.ProductID+"|"+string(b.Dims.Hash()))
	})
	return out, nil
}
