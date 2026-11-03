package domain

import (
	"bytes"
	"encoding/csv"
	"errors"
	"fmt"
	"io"
	"sort"
	"strings"
	"unicode/utf8"
)

// CSV import and export of lookup table entries. A legacy price list or unit table lives in a spreadsheet; the file has
// one entry per line: the key parts in the leading cells and the value in the last cell (item,currency,price or
// from,to,factor). A key that is already joined with | ("LB|KG,0.45359237") reads the same as two cells.

const (
	// maxLookupCSVBytes bounds an import: 5000 entries of the longest key and value fit well inside it.
	maxLookupCSVBytes = 2 << 20
	maxCSVIssues      = 10
)

// LookupCSVResult is what a file holds: the entries by key and the number of data lines read.
type LookupCSVResult struct {
	Entries map[string]string
	Rows    int
}

// ParseLookupCSV reads the entries of a table of the given kind from CSV text. hasHeader skips the first line. Each
// line needs two to four cells (up to three key parts and the value). Every problem found is reported with its line
// (at most ten), so a file is corrected in one pass; a key that appears twice is a problem rather than a silent win.
func ParseLookupCSV(kind string, raw []byte, hasHeader bool) (LookupCSVResult, error) {
	if !ValidLookupKind(kind) {
		return LookupCSVResult{}, &Error{CodeLookupInvalid, "unknown lookup table kind " + kind}
	}
	if len(raw) > maxLookupCSVBytes {
		return LookupCSVResult{}, &Error{CodeLookupInvalid, fmt.Sprintf("the CSV is larger than %d bytes", maxLookupCSVBytes)}
	}
	raw = bytes.TrimPrefix(raw, []byte("\xef\xbb\xbf"))
	if !utf8.Valid(raw) {
		return LookupCSVResult{}, &Error{CodeLookupInvalid, "the CSV is not valid UTF-8 text"}
	}
	rd := csv.NewReader(bytes.NewReader(raw))
	rd.FieldsPerRecord = -1
	rd.TrimLeadingSpace = true
	rd.ReuseRecord = false

	var issues []string
	more := 0
	note := func(line int, format string, a ...any) {
		if len(issues) >= maxCSVIssues {
			more++
			return
		}
		issues = append(issues, fmt.Sprintf("line %d: %s", line, fmt.Sprintf(format, a...)))
	}
	res := LookupCSVResult{Entries: map[string]string{}}
	firstLine := map[string]int{}
	first := true
	for {
		rec, err := rd.Read()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			var pe *csv.ParseError
			if errors.As(err, &pe) {
				note(pe.Line, "column %d: %s", pe.Column, pe.Err)
			} else {
				note(0, "%s", err)
			}
			break // a malformed quote leaves the reader unreliable for the rest of the file
		}
		line, _ := rd.FieldPos(0)
		if first {
			first = false
			if hasHeader {
				continue
			}
		}
		if len(rec) == 1 && strings.TrimSpace(rec[0]) == "" {
			continue // blank line
		}
		if len(rec) < 2 || len(rec) > maxLookupKeyParts+1 {
			note(line, "needs 2 to %d cells (key parts, then the value), found %d", maxLookupKeyParts+1, len(rec))
			continue
		}
		parts := make([]string, len(rec))
		for i, c := range rec {
			parts[i] = unquoteSheetCell(strings.TrimSpace(c))
		}
		key := strings.Join(parts[:len(parts)-1], KeySeparator)
		value := parts[len(parts)-1]
		if prev, dup := firstLine[key]; dup {
			note(line, "key %q already appears on line %d", key, prev)
			continue
		}
		one := LookupDefinition{Kind: kind, Entries: map[string]string{key: value}}
		if err := one.check(); err != nil {
			var de *Error
			if errors.As(err, &de) {
				note(line, "%s", de.Detail)
			} else {
				note(line, "%s", err.Error())
			}
			continue
		}
		firstLine[key] = line
		res.Entries[key] = value
		res.Rows++
	}
	if len(issues) > 0 {
		msg := strings.Join(issues, "; ")
		if more > 0 {
			msg += fmt.Sprintf("; and %d more", more)
		}
		return LookupCSVResult{}, &Error{CodeLookupInvalid, msg}
	}
	if res.Rows == 0 {
		return LookupCSVResult{}, &Error{CodeLookupInvalid, "the CSV has no entries"}
	}
	return res, nil
}

// LookupImportSummary counts what an import did to the table: entries new to it, entries whose value changed, entries
// the file repeated unchanged, and (replace mode) entries the file left out.
type LookupImportSummary struct {
	Rows      int `json:"rows"`
	Added     int `json:"added"`
	Changed   int `json:"changed"`
	Unchanged int `json:"unchanged"`
	Removed   int `json:"removed"`
}

// ApplyLookupImport combines the entries of a table with the entries of a file: merge keeps the entries the file does
// not mention, replace keeps only the file's.
func ApplyLookupImport(current, file map[string]string, replace bool) (map[string]string, LookupImportSummary) {
	sum := LookupImportSummary{Rows: len(file)}
	out := make(map[string]string, len(current)+len(file))
	if !replace {
		for k, v := range current {
			out[k] = v
		}
	}
	for k, v := range file {
		old, had := current[k]
		switch {
		case !had:
			sum.Added++
		case old != v:
			sum.Changed++
		default:
			sum.Unchanged++
		}
		out[k] = v
	}
	if replace {
		for k := range current {
			if _, kept := file[k]; !kept {
				sum.Removed++
			}
		}
	}
	return out, sum
}

// LookupCSV writes the entries as CSV in key order, one line each: the key parts in separate cells, then the value.
// Cells that a spreadsheet would run as a formula start with an apostrophe, which ParseLookupCSV removes again.
func LookupCSV(entries map[string]string) []byte {
	keys := make([]string, 0, len(entries))
	for k := range entries {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var buf bytes.Buffer
	w := csv.NewWriter(&buf)
	for _, k := range keys {
		parts := strings.Split(k, KeySeparator)
		cells := make([]string, 0, len(parts)+1)
		for _, p := range parts {
			cells = append(cells, quoteSheetCell(p))
		}
		cells = append(cells, quoteSheetCell(entries[k]))
		_ = w.Write(cells)
	}
	w.Flush()
	return buf.Bytes()
}

// formulaStart reports whether a cell, after any leading apostrophes, starts like a spreadsheet formula. Counting the
// apostrophes keeps a value that itself starts with an apostrophe ('=x) exact through an export and an import.
func formulaStart(c string) bool {
	c = strings.TrimLeft(c, "'")
	return c != "" && strings.ContainsRune("=+-@\t\r", rune(c[0]))
}

// quoteSheetCell neutralises a cell that a spreadsheet would read as a formula with one more leading apostrophe.
func quoteSheetCell(c string) string {
	if formulaStart(c) {
		return "'" + c
	}
	return c
}

// unquoteSheetCell reverses quoteSheetCell.
func unquoteSheetCell(c string) string {
	if strings.HasPrefix(c, "'") && formulaStart(c) {
		return c[1:]
	}
	return c
}
