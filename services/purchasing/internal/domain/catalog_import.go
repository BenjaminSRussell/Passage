package domain

import (
	"encoding/csv"
	"errors"
	"io"
	"strconv"
	"strings"
	"time"
	"unicode"
)

// Vendor catalog import (FR-17.18, AC-17.13). A vendor file is staged row by row:
// a row that passes validation is ACCEPTABLE, one that does not is ERROR with its
// reason. The buyer accepts or rejects ACCEPTABLE rows, and the commit adds the
// ACCEPTED ones to the catalog as entries.
const (
	ImportStaged    = "STAGED"
	ImportCommitted = "COMMITTED"
	ImportCancelled = "CANCELLED"

	RowAcceptable = "ACCEPTABLE"
	RowError      = "ERROR"
	RowAccepted   = "ACCEPTED"
	RowRejected   = "REJECTED"

	ImportCommit = "commit"
	ImportCancel = "cancel"

	ImportFormatCSV  = "CSV"
	ImportFormatJSON = "JSON"
)

// Vendor import message codes (PRD 3.17.18; -008 and -009 are service codes).
const (
	CodeImportRow     = "PRC-CAT-005"
	CodeImportTooBig  = "PRC-CAT-008"
	CodeImportNothing = "PRC-CAT-009"
	CodeImportState   = "PRC-CAT-IMPORT-STATE"
)

// MaxImportRows is the row limit of one vendor import (vendor_catalog_import_params).
const MaxImportRows = 50000

// Import file columns, by their canonical name.
const (
	ColVendorPartNo = "vendorPartNo"
	ColProductID    = "productId"
	ColName         = "name"
	ColDescription  = "description"
	ColCategory     = "category"
	ColUnit         = "unit"
	ColUnitPrice    = "unitPrice"
	ColCurrency     = "currency"
	ColLeadTime     = "leadTimeDays"
	ColImageURL     = "imageUrl"
)

// ImportColumns lists the canonical columns in display order.
var ImportColumns = []string{ColVendorPartNo, ColName, ColDescription, ColCategory, ColUnit, ColUnitPrice, ColCurrency,
	ColLeadTime, ColImageURL, ColProductID}

// RequiredImportColumns must be present in a file's header; a row may still leave
// one blank, which makes that row an ERROR.
var RequiredImportColumns = []string{ColName, ColCategory, ColUnit, ColUnitPrice, ColCurrency}

var columnAliases = map[string]string{
	"vendorpartno": ColVendorPartNo, "vendorpartnumber": ColVendorPartNo, "vendorproductnumber": ColVendorPartNo,
	"vendorproductno": ColVendorPartNo, "partno": ColVendorPartNo, "partnumber": ColVendorPartNo, "sku": ColVendorPartNo,
	"productid": ColProductID,
	"name":      ColName, "productname": ColName, "itemname": ColName,
	"description": ColDescription,
	"category":    ColCategory,
	"unit":        ColUnit, "uom": ColUnit, "unitofmeasure": ColUnit,
	"unitprice": ColUnitPrice, "price": ColUnitPrice,
	"currency": ColCurrency,
	"leadtime": ColLeadTime, "leadtimedays": ColLeadTime,
	"imageurl": ColImageURL, "image": ColImageURL,
}

// CanonicalColumn maps a file header ("Vendor part number", "unit_price") to a
// canonical column; ok is false for a column the import does not use.
func CanonicalColumn(header string) (string, bool) {
	var b strings.Builder
	for _, r := range header {
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			b.WriteRune(unicode.ToLower(r))
		}
	}
	c, ok := columnAliases[b.String()]
	return c, ok
}

// ImportRow is one staged file row. Raw holds what the file said under canonical
// column names; Entry is set when Status is ACCEPTABLE, ACCEPTED or REJECTED.
type ImportRow struct {
	RowNo  int               `json:"rowNo"`
	Status string            `json:"status"`
	Reason string            `json:"reason,omitempty"`
	Raw    map[string]string `json:"raw"`
	Entry  *CatalogEntry     `json:"entry,omitempty"`
}

// CatalogImport is the header of a staged vendor file with its row counts.
type CatalogImport struct {
	ID             string    `json:"id"`
	CatalogID      string    `json:"catalogId"`
	LegalEntityID  string    `json:"legalEntityId"`
	VendorID       string    `json:"vendorId"`
	Format         string    `json:"format"`
	FileName       string    `json:"fileName,omitempty"`
	Status         string    `json:"status"`
	TotalRows      int       `json:"totalRows"`
	AcceptableRows int       `json:"acceptableRows"`
	ErrorRows      int       `json:"errorRows"`
	AcceptedRows   int       `json:"acceptedRows"`
	RejectedRows   int       `json:"rejectedRows"`
	EntriesAdded   int       `json:"entriesAdded"`
	Version        int       `json:"version"`
	CreatedAt      time.Time `json:"createdAt"`
	UpdatedAt      time.Time `json:"updatedAt"`
}

// ParseImportCSV reads a vendor CSV into rows of canonical columns. The first record
// is the header; columns the import does not use are ignored, a required column that
// is missing is PRC-CAT-005, and a file over MaxImportRows is PRC-CAT-008. Blank
// lines are skipped.
func ParseImportCSV(text string) ([]map[string]string, error) {
	r := csv.NewReader(strings.NewReader(strings.TrimPrefix(text, "\xef\xbb\xbf")))
	r.FieldsPerRecord = -1
	r.TrimLeadingSpace = true
	head, err := r.Read()
	if errors.Is(err, io.EOF) {
		return nil, fail(CodeImportRow, "the file has no header row")
	}
	if err != nil {
		return nil, fail(CodeImportRow, "the file is not valid CSV: %v", err)
	}
	cols := make([]string, len(head))
	have := map[string]bool{}
	for i, h := range head {
		if c, ok := CanonicalColumn(h); ok && !have[c] {
			cols[i], have[c] = c, true
		}
	}
	for _, c := range RequiredImportColumns {
		if !have[c] {
			return nil, fail(CodeImportRow, "the header has no %s column", c)
		}
	}
	var rows []map[string]string
	for {
		rec, err := r.Read()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return nil, fail(CodeImportRow, "line %d of the file is not valid CSV: %v", len(rows)+2, err)
		}
		row, blank := map[string]string{}, true
		for i, v := range rec {
			if i < len(cols) && cols[i] != "" {
				row[cols[i]] = strings.TrimSpace(v)
				blank = blank && strings.TrimSpace(v) == ""
			} else if strings.TrimSpace(v) != "" {
				blank = false
			}
		}
		if blank {
			continue
		}
		if len(rows) >= MaxImportRows {
			return nil, fail(CodeImportTooBig, "a vendor import holds at most %d rows", MaxImportRows)
		}
		rows = append(rows, row)
	}
	if len(rows) == 0 {
		return nil, fail(CodeImportRow, "the file has no data rows")
	}
	return rows, nil
}

// LeadTime reads the lead-time column: blank is zero days.
func LeadTime(raw string) (int, error) {
	if raw == "" {
		return 0, nil
	}
	return strconv.Atoi(raw)
}

// FlagDuplicates marks a staged row ERROR when an earlier ACCEPTABLE row of the file
// carries the same vendor part number, or when existing holds it already (the part
// numbers of the vendor's entries in the catalog). Rows without a part number are
// not compared.
func FlagDuplicates(rows []ImportRow, existing map[string]bool) {
	seen := map[string]int{}
	for i := range rows {
		r := &rows[i]
		part := strings.ToLower(strings.TrimSpace(r.Raw[ColVendorPartNo]))
		if part == "" || r.Status != RowAcceptable {
			continue
		}
		switch first, dup := seen[part]; {
		case existing[part]:
			r.Status, r.Reason = RowError, "vendorPartNo "+r.Raw[ColVendorPartNo]+" is already an entry of this catalog"
		case dup:
			r.Status, r.Reason = RowError, "vendorPartNo "+r.Raw[ColVendorPartNo]+" repeats row "+strconv.Itoa(first)
		default:
			seen[part] = r.RowNo
		}
	}
}

// NextImportStatus applies commit or cancel to a STAGED import; anything else is
// PRC-CAT-IMPORT-STATE.
func NextImportStatus(status, action string) (string, error) {
	if status == ImportStaged {
		switch action {
		case ImportCommit:
			return ImportCommitted, nil
		case ImportCancel:
			return ImportCancelled, nil
		}
	}
	return "", fail(CodeImportState, "cannot %s a %s import", action, strings.ToLower(status))
}

// Decide applies a buyer decision (ACCEPT or REJECT) to a row status. An ERROR row
// cannot be accepted (PRC-CAT-005); a committed or cancelled import takes none.
func Decide(rowNo int, status, decision string) (string, error) {
	switch {
	case decision == "REJECT" && status != RowError:
		return RowRejected, nil
	case decision == "REJECT":
		return RowError, nil
	case decision == "ACCEPT" && status == RowError:
		return "", fail(CodeImportRow, "row %d has an error and cannot be accepted", rowNo)
	case decision == "ACCEPT":
		return RowAccepted, nil
	}
	return "", fail(CodeValidation, "decision must be ACCEPT or REJECT")
}
