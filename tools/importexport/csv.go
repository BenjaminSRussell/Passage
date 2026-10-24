// Package importexport converts any metadata-shaped entity to and from CSV.
// Wave 3 owns the live catalog; callers pass Field/Entity values.
package importexport

import (
	"bytes"
	"encoding/csv"
	"fmt"
	"io"
	"strings"
)

// Field is the wave-3 metadata seam.
type Field struct {
	Name     string
	Required bool
}

// Entity is the imported or exported shape.
type Entity struct {
	ID     string
	Fields []Field
}

// Result is one import run.
type Result struct {
	Rows   []map[string]string
	Errors []string
}

// ExportCSV writes rows in field order.
func ExportCSV(entity Entity, rows []map[string]string) ([]byte, error) {
	if len(entity.Fields) == 0 {
		return nil, fmt.Errorf("entity has no fields")
	}
	var buf bytes.Buffer
	w := csv.NewWriter(&buf)
	header := make([]string, len(entity.Fields))
	for i, f := range entity.Fields {
		header[i] = f.Name
	}
	if err := w.Write(header); err != nil {
		return nil, err
	}
	for _, row := range rows {
		record := make([]string, len(entity.Fields))
		for i, f := range entity.Fields {
			record[i] = row[f.Name]
		}
		if err := w.Write(record); err != nil {
			return nil, err
		}
	}
	w.Flush()
	return buf.Bytes(), w.Error()
}

// ImportCSV reads a headered CSV against entity fields.
func ImportCSV(entity Entity, data []byte) (Result, error) {
	r := csv.NewReader(bytes.NewReader(data))
	r.TrimLeadingSpace = true
	header, err := r.Read()
	if err != nil {
		return Result{}, err
	}
	index := map[string]int{}
	for i, name := range header {
		index[name] = i
	}
	for _, f := range entity.Fields {
		if _, ok := index[f.Name]; !ok {
			return Result{}, fmt.Errorf("missing column %s", f.Name)
		}
	}
	out := Result{Rows: []map[string]string{}}
	line := 1
	for {
		line++
		record, err := r.Read()
		if err == io.EOF {
			break
		}
		if err != nil {
			return Result{}, err
		}
		row := map[string]string{}
		for _, f := range entity.Fields {
			value := strings.TrimSpace(record[index[f.Name]])
			row[f.Name] = value
			if f.Required && value == "" {
				out.Errors = append(out.Errors, fmt.Sprintf("line %d: %s is required", line, f.Name))
			}
		}
		out.Rows = append(out.Rows, row)
	}
	return out, nil
}
