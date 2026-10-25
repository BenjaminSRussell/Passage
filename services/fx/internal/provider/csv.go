package provider

import (
	"context"
	"encoding/csv"
	"fmt"
	"io"
	"slices"
	"strings"
	"time"
)

var csvColumns = []string{"type", "from", "to", "valid_from", "rate"}

type Row struct {
	RateType string
	RatePoint
}

type CSV struct {
	rows []Row
}

func NewCSV(r io.Reader) (*CSV, error) {
	rows, err := ParseCSV(r)
	if err != nil {
		return nil, err
	}
	return &CSV{rows: rows}, nil
}

func (*CSV) Name() string { return "csv" }

func (c *CSV) Fetch(_ context.Context, rateType string, _ time.Time) ([]RatePoint, error) {
	points := []RatePoint{}
	for _, row := range c.rows {
		if row.RateType == rateType {
			points = append(points, row.RatePoint)
		}
	}
	return points, nil
}

func csvFields(line string) ([]string, error) {
	r := csv.NewReader(strings.NewReader(line))
	r.TrimLeadingSpace = true
	return r.Read()
}

func csvHeader(line string) (map[string]int, error) {
	fields, err := csvFields(line)
	if err != nil {
		return nil, malformed("line 1: %v", err)
	}
	index := map[string]int{}
	for i, name := range fields {
		name = strings.TrimSpace(name)
		if !slices.Contains(csvColumns, name) {
			return nil, malformed("line 1: unknown column %q", name)
		}
		if _, dup := index[name]; dup {
			return nil, malformed("line 1: duplicate column %q", name)
		}
		index[name] = i
	}
	for _, name := range csvColumns {
		if _, ok := index[name]; !ok {
			return nil, malformed("line 1: missing column %q", name)
		}
	}
	return index, nil
}

func ParseCSV(r io.Reader) ([]Row, error) {
	raw, err := io.ReadAll(io.LimitReader(r, maxBody))
	if err != nil {
		return nil, fmt.Errorf("read csv: %w", err)
	}
	lines := strings.Split(string(raw), "\n")
	var index map[string]int
	rows := []Row{}
	for i, line := range lines {
		line = strings.TrimRight(line, "\r")
		if strings.TrimSpace(line) == "" {
			continue
		}
		if index == nil {
			if i != 0 {
				return nil, malformed("line %d: header must be the first line", i+1)
			}
			if index, err = csvHeader(line); err != nil {
				return nil, err
			}
			continue
		}
		row, err := parseCSVRow(i+1, line, index)
		if err != nil {
			return nil, err
		}
		rows = append(rows, row)
	}
	if index == nil {
		return nil, malformed("line 1: header %s is required", strings.Join(csvColumns, ","))
	}
	if len(rows) == 0 {
		return nil, malformed("line 1: no data rows")
	}
	return rows, nil
}

func parseCSVRow(number int, line string, index map[string]int) (Row, error) {
	fields, err := csvFields(line)
	if err != nil {
		return Row{}, malformed("line %d: %v", number, err)
	}
	if len(fields) != len(index) {
		return Row{}, malformed("line %d: %d fields, want %d", number, len(fields), len(index))
	}
	field := func(name string) string { return strings.TrimSpace(fields[index[name]]) }
	row := Row{RateType: field("type")}
	if row.RateType == "" {
		return Row{}, malformed("line %d: type is required", number)
	}
	row.From, row.To = field("from"), field("to")
	if !validCurrency(row.From) || !validCurrency(row.To) {
		return Row{}, malformed("line %d: currencies must be three upper-case letters, got %q and %q", number, row.From, row.To)
	}
	if row.ValidFrom, err = time.Parse(time.DateOnly, field("valid_from")); err != nil {
		return Row{}, malformed("line %d: valid_from %q is not a date", number, field("valid_from"))
	}
	if row.Rate, err = parseRate(field("rate"), true); err != nil {
		return Row{}, malformed("line %d: %v", number, err)
	}
	row.PayloadHash = hashOf(line)
	return row, nil
}
