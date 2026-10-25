package connectors

import (
	"context"
	"encoding/csv"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"strings"
)

const keySeparator = "|"

type Entity struct {
	Name         string
	System       string
	Table        string
	KeyColumns   []string
	Columns      []string
	CursorColumn string
	Unverified   bool
}

type Record struct {
	Key    string
	Fields map[string]any
}

type RowSource interface {
	Rows(ctx context.Context, e Entity, since string) ([]Record, error)
}

type FlatFiles struct{ FS fs.FS }

func (f FlatFiles) Rows(ctx context.Context, e Entity, since string) ([]Record, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	file, err := f.FS.Open(e.Table + ".csv")
	if err != nil {
		return nil, fmt.Errorf("open %s extract: %w", e.Table, err)
	}
	defer file.Close()
	return ReadCSV(file, e, since)
}

func ReadCSV(r io.Reader, e Entity, since string) ([]Record, error) {
	reader := csv.NewReader(r)
	header, err := reader.Read()
	if err != nil {
		return nil, fmt.Errorf("%s: read header: %w", e.Table, err)
	}
	index := make(map[string]int, len(header))
	for i, name := range header {
		index[strings.TrimSpace(name)] = i
	}
	for _, column := range e.Columns {
		if _, ok := index[column]; !ok {
			return nil, fmt.Errorf("%s: header is missing column %s", e.Table, column)
		}
	}
	var records []Record
	for {
		row, err := reader.Read()
		if errors.Is(err, io.EOF) {
			return records, nil
		}
		if err != nil {
			return nil, fmt.Errorf("%s: %w", e.Table, err)
		}
		line, _ := reader.FieldPos(0)
		fields := make(map[string]any, len(e.Columns))
		for _, column := range e.Columns {
			fields[column] = row[index[column]]
		}
		key, err := e.rowKey(fields)
		if err != nil {
			return nil, fmt.Errorf("%s: line %d: %w", e.Table, line, err)
		}
		if since != "" && e.CursorColumn != "" && fields[e.CursorColumn].(string) <= since {
			continue
		}
		records = append(records, Record{Key: key, Fields: fields})
	}
}

func (e Entity) rowKey(fields map[string]any) (string, error) {
	parts := make([]string, len(e.KeyColumns))
	for i, column := range e.KeyColumns {
		parts[i] = strings.TrimSpace(fields[column].(string))
		if parts[i] == "" {
			return "", fmt.Errorf("key column %s is empty", column)
		}
	}
	return strings.Join(parts, keySeparator), nil
}
