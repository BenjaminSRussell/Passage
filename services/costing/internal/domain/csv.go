package domain

import (
	"encoding/csv"
	"errors"
	"fmt"
	"io"
	"strings"

	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/platform/ids"
)

// CodeImportInvalid is raised when an item-cost CSV cannot be imported at all.
const CodeImportInvalid = "CST-IMP-001"

// CostRow is one parsed line of an item-cost import file.
type CostRow struct {
	Line      int
	ProductID string
	SiteID    string
	UnitCost  decimal.Decimal
}

// ValidUnitCost reports whether d is a storable unit cost: non-negative, at
// most 14 integer digits and 6 decimal places (numeric(20,6)).
func ValidUnitCost(d decimal.Decimal) bool {
	return !d.IsNegative() && d.Equal(d.Truncate(6)) && d.LessThan(MaxUnitCost)
}

// ParseItemCostCSV reads an item-cost file (FR-20.5 import) with the header
// `productId,siteId,unitCost` (any order, case-insensitive, extra columns
// ignored). It rejects the whole file on the first bad row, so an import is all
// or nothing; the error detail names the line. At most maxRows data rows are
// accepted. Blank lines are skipped.
func ParseItemCostCSV(r io.Reader, maxRows int) ([]CostRow, error) {
	cr := csv.NewReader(r)
	cr.TrimLeadingSpace = true
	cr.FieldsPerRecord = -1
	header, err := cr.Read()
	if err != nil {
		return nil, &Error{Code: CodeImportInvalid, Detail: "file must start with a header row"}
	}
	col := map[string]int{}
	for i, h := range header {
		col[strings.ToLower(strings.TrimSpace(strings.TrimPrefix(h, "\ufeff")))] = i
	}
	pi, okP := col["productid"]
	si, okS := col["siteid"]
	ui, okU := col["unitcost"]
	if !okP || !okS || !okU {
		return nil, &Error{Code: CodeImportInvalid, Detail: "header must contain productId, siteId and unitCost"}
	}
	var rows []CostRow
	seen := map[string]bool{}
	for {
		rec, err := cr.Read()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return nil, &Error{Code: CodeImportInvalid, Detail: "file is not valid CSV: " + err.Error()}
		}
		line, _ := cr.FieldPos(0)
		if len(rec) <= max(pi, si, ui) {
			return nil, &Error{Code: CodeImportInvalid, Detail: fmt.Sprintf("line %d: too few columns", line)}
		}
		if len(rows) >= maxRows {
			return nil, &Error{Code: CodeImportInvalid, Detail: fmt.Sprintf("file has more than %d rows", maxRows)}
		}
		p := strings.ToLower(strings.TrimSpace(rec[pi]))
		s := strings.ToLower(strings.TrimSpace(rec[si]))
		if !ids.Valid(p) || !ids.Valid(s) {
			return nil, &Error{Code: CodeImportInvalid, Detail: fmt.Sprintf("line %d: productId and siteId must be UUIDs", line)}
		}
		cost, err := decimal.NewFromString(strings.TrimSpace(rec[ui]))
		if err != nil || !ValidUnitCost(cost) {
			return nil, &Error{Code: CodeImportInvalid, Detail: fmt.Sprintf(
				"line %d: unitCost must be a non-negative decimal with at most 14 integer digits and 6 decimal places", line)}
		}
		key := p + "|" + s
		if seen[key] {
			return nil, &Error{Code: CodeImportInvalid, Detail: fmt.Sprintf("line %d: duplicates an earlier product and site", line)}
		}
		seen[key] = true
		rows = append(rows, CostRow{Line: line, ProductID: p, SiteID: s, UnitCost: cost})
	}
	if len(rows) == 0 {
		return nil, &Error{Code: CodeImportInvalid, Detail: "file has no data rows"}
	}
	return rows, nil
}
