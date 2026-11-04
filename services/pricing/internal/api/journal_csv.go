package api

import (
	"encoding/csv"
	"errors"
	"fmt"
	"io"
	"net/http"
	"slices"
	"strconv"
	"strings"

	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/services/pricing/internal/domain"
)

// journalsImportPath accepts a price journal as CSV (FR-26.9, §5.26.6 "import CSV (≤ 100k rows)").
const (
	journalsImportPath = journalsPath + ":import-csv"
	// maxJournalCSVBytes bounds the upload; 100k rows of the fixed columns fit well inside it.
	maxJournalCSVBytes = 32 << 20
	// maxCSVRowFaults caps the row faults one response lists.
	maxCSVRowFaults = 100
)

// journalCSVColumns is every header the import understands. Unknown headers are refused so a
// misspelt column can never be silently ignored.
var journalCSVColumns = []string{"op", "recordId", "expectedVersion", "adjustPct", "type",
	"customerKind", "customerKey", "productKind", "productKey", "variant", "currency", "unit",
	"fromQuantity", "amount", "discPct1", "discPct2", "validFrom", "validTo", "searchNext",
	"exclusive", "campaignId"}

// importJournalCSV turns a CSV upload into the same journal the JSON route creates (same
// validation, same Idempotency-Key handling); the journal is OPEN and is validated and posted
// like any other. Query parameters carry the journal header: legalEntityId, name and
// smartRound=true. Data row n of the file becomes journal line n.
func (a *API) importJournalCSV(w http.ResponseWriter, r *http.Request) {
	if !validTenant(w, r) {
		return
	}
	q := r.URL.Query()
	req := journalRequest{LegalEntityID: q.Get("legalEntityId"), Name: q.Get("name")}
	if v := q.Get("smartRound"); v != "" {
		b, err := strconv.ParseBool(v)
		if err != nil {
			validationFailed(w, r, map[string]string{"smartRound": "must be true or false"})
			return
		}
		req.SmartRound = b
	}
	lines, fields, err := parseJournalCSV(http.MaxBytesReader(w, r.Body, maxJournalCSVBytes), req.LegalEntityID)
	if err != nil {
		httpx.Fail(w, r, err)
		return
	}
	if len(fields) > 0 {
		validationFailed(w, r, fields)
		return
	}
	req.Lines = lines
	a.submitJournal(w, r, req)
}

// parseJournalCSV reads a price journal CSV into operations. Row faults are keyed rows[n].<column>,
// n counting data rows from 1; at most maxCSVRowFaults rows are reported. Record columns are
// read only for CREATE and for UPDATE without adjustPct, mirroring the JSON contract in
// toJournalLine. The error is set only when the upload exceeds maxJournalCSVBytes.
func parseJournalCSV(src io.Reader, legalEntityID string) ([]journalLineDTO, map[string]string, error) {
	fields := map[string]string{}
	cr := csv.NewReader(src)
	cr.TrimLeadingSpace = true
	header, err := cr.Read()
	if tooLarge(err) {
		return nil, nil, errTooLarge()
	}
	if err != nil {
		fields["csv"] = "a header row is required"
		return nil, fields, nil
	}
	idx := map[string]int{}
	for i, h := range header {
		h = strings.TrimSpace(h)
		if i == 0 {
			h = strings.TrimPrefix(h, "\ufeff") // spreadsheet exports start with a byte-order mark
		}
		if !slices.Contains(journalCSVColumns, h) {
			fields["csv.header"] = fmt.Sprintf("unknown column %q", h)
		}
		if _, dup := idx[h]; dup {
			fields["csv.header"] = fmt.Sprintf("column %q appears twice", h)
		}
		idx[h] = i
	}
	if _, ok := idx["op"]; !ok {
		fields["csv.header"] = "the op column is required"
	}
	var lines []journalLineDTO
	faultyRows := 0
	for row := 1; ; row++ {
		rec, err := cr.Read()
		if err == io.EOF {
			break
		}
		if tooLarge(err) {
			return nil, nil, errTooLarge()
		}
		if err != nil {
			fields["csv"] = fmt.Sprintf("row %d: %v", row, err)
			break
		}
		if row > domain.MaxJournalLines {
			fields["lines"] = fmt.Sprintf("a journal holds at most %d lines", domain.MaxJournalLines)
			break
		}
		cell := func(name string) string {
			i, ok := idx[name]
			if !ok || i >= len(rec) {
				return ""
			}
			return strings.TrimSpace(rec[i])
		}
		dto, rowFields := journalRow(cell, legalEntityID)
		if len(rowFields) > 0 {
			faultyRows++
		}
		if faultyRows <= maxCSVRowFaults {
			for k, v := range rowFields {
				fields[fmt.Sprintf("rows[%d].%s", row, k)] = v
			}
		}
		lines = append(lines, dto)
	}
	if faultyRows > maxCSVRowFaults {
		fields["csv"] = fmt.Sprintf("%d rows have errors; the first %d are listed", faultyRows, maxCSVRowFaults)
	}
	return lines, fields, nil
}

func tooLarge(err error) bool {
	var mbe *http.MaxBytesError
	return errors.As(err, &mbe)
}

func errTooLarge() error {
	return httpx.NewProblem(http.StatusRequestEntityTooLarge, "COM-0001",
		fmt.Sprintf("the CSV file is larger than %d MB", maxJournalCSVBytes>>20))
}

// journalRow maps one CSV row. cell reads a named column and returns "" when the column is absent.
func journalRow(cell func(string) string, legalEntityID string) (journalLineDTO, map[string]string) {
	fields := map[string]string{}
	dto := journalLineDTO{Op: strings.ToUpper(cell("op")), RecordID: cell("recordId"), AdjustPct: cell("adjustPct")}
	if v := cell("expectedVersion"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil {
			fields["expectedVersion"] = "must be a whole number"
		}
		dto.ExpectedVersion = n
	}
	withRecord := dto.Op == string(domain.OpCreate) || (dto.Op == string(domain.OpUpdate) && dto.AdjustPct == "")
	if !withRecord {
		return dto, fields
	}
	rec := &recordDTO{LegalEntityID: legalEntityID, Type: cell("type"), CustomerKind: cell("customerKind"),
		CustomerKey: cell("customerKey"), ProductKind: cell("productKind"), ProductKey: cell("productKey"),
		Variant: cell("variant"), Currency: cell("currency"), Unit: cell("unit"), FromQuantity: cell("fromQuantity"),
		Amount: cell("amount"), DiscPct1: cell("discPct1"), DiscPct2: cell("discPct2"), ValidFrom: cell("validFrom"),
		ValidTo: cell("validTo"), CampaignID: cell("campaignId")}
	if cell("searchNext") != "" { // empty takes the legal entity's price_search_mode
		next := boolCell(cell("searchNext"), "searchNext", fields)
		rec.SearchNext = &next
	}
	rec.Exclusive = boolCell(cell("exclusive"), "exclusive", fields)
	dto.Record = rec
	return dto, fields
}

// boolCell reads an optional true/false column; empty means false.
func boolCell(v, column string, fields map[string]string) bool {
	if v == "" {
		return false
	}
	b, err := strconv.ParseBool(v)
	if err != nil {
		fields[column] = "must be true or false"
	}
	return b
}
