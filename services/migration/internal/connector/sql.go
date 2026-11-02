package connector

import (
	"context"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/BenjaminSRussell/passage/services/migration/internal/domain"
	"github.com/BenjaminSRussell/passage/services/migration/internal/probe"
)

// pageTimeout bounds one page query, on top of the engine's own statement timeout where it has one.
const pageTimeout = 60 * time.Second

// SQL reads a table or one SELECT with keyset paging: WHERE (cursor, key) > (last cursor, last key) ORDER BY cursor,
// key, at most Limit rows a page, in a read-only transaction where the engine has one. Identifiers are validated
// (letters, digits, _ $ #) and quoted with the driver's quoting; values are always bound parameters.
type SQL struct {
	DB          *sql.DB
	Driver      probe.SQLDriver
	Src         domain.SQLSource
	Keys        []string
	CursorField string
	CursorType  string
}

// orderBy lists the keyset columns: the cursor first, then the key fields.
func (s *SQL) orderBy() []string {
	if s.CursorField != "" {
		return append([]string{s.CursorField}, s.Keys...)
	}
	return s.Keys
}

func (s *SQL) Read(ctx context.Context, q Query) (Page, error) {
	if q.Limit < 1 || q.Limit > domain.MaxBatch {
		return Page{}, fmt.Errorf("limit must be 1 to %d", domain.MaxBatch)
	}
	text, args, err := s.build(q)
	if err != nil {
		return Page{}, err
	}
	ctx, cancel := context.WithTimeout(ctx, pageTimeout)
	defer cancel()
	begin := s.Driver.Begin
	if begin == nil {
		return Page{}, errors.New("the driver has no read transaction")
	}
	tx, err := begin(ctx, s.DB)
	if err != nil {
		return Page{}, fmt.Errorf("begin read: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	rows, err := tx.QueryContext(ctx, text, args...)
	if err != nil {
		return Page{}, fmt.Errorf("read source: %w", err)
	}
	defer rows.Close()
	cols, err := rows.Columns()
	if err != nil {
		return Page{}, err
	}
	var page Page
	for rows.Next() {
		vals := make([]any, len(cols))
		ptrs := make([]any, len(cols))
		for i := range vals {
			ptrs[i] = &vals[i]
		}
		if err := rows.Scan(ptrs...); err != nil {
			return Page{}, fmt.Errorf("read source row: %w", err)
		}
		row := make(Row, len(cols))
		for i, c := range cols {
			row[c] = scalar(vals[i])
		}
		page.Rows = append(page.Rows, row)
	}
	if err := rows.Err(); err != nil {
		return Page{}, fmt.Errorf("read source: %w", err)
	}
	more := len(page.Rows) > q.Limit
	if more {
		page.Rows = page.Rows[:q.Limit]
	}
	// Keyset paging compares (cursor, keys) > (last cursor, last keys); a NULL in any of them compares as unknown, so
	// rows would be skipped or read again. A NULL fails the extraction instead of being passed over silently.
	for i, r := range page.Rows {
		for _, c := range s.orderBy() {
			if r[c] == nil {
				return Page{}, fmt.Errorf("row %d of the page: column %s is NULL or missing; keyset paging needs every key and cursor column set (make it NOT NULL, filter NULLs out in the query, or choose another column)", i+1, c)
			}
		}
		if s.CursorField != "" {
			c, ok := domain.NormalizeCursor(s.CursorType, r[s.CursorField])
			if !ok {
				return Page{}, fmt.Errorf("cursor field %s holds a value that is not a %s", s.CursorField, s.CursorType)
			}
			page.Watermark = domain.MaxCursor(s.CursorType, page.Watermark, c)
		}
	}
	if more {
		page.Next, err = s.position(page.Rows[len(page.Rows)-1])
		if err != nil {
			return Page{}, err
		}
	}
	return page, nil
}

// position encodes the last row's keyset columns.
func (s *SQL) position(last Row) (string, error) {
	vals := make([]any, 0, len(s.orderBy()))
	for i, c := range s.orderBy() {
		v := last[c]
		if v == nil {
			return "", fmt.Errorf("column %s is null in a row, so paging cannot continue after it; make it NOT NULL or choose another key", c)
		}
		if i == 0 && s.CursorField != "" {
			n, ok := domain.NormalizeCursor(s.CursorType, v)
			if !ok {
				return "", fmt.Errorf("cursor field %s holds a value that is not a %s", c, s.CursorType)
			}
			v = n
		}
		vals = append(vals, v)
	}
	raw, err := json.Marshal(vals)
	return string(raw), err
}

func (s *SQL) decodePosition(pos string) ([]any, error) {
	dec := json.NewDecoder(strings.NewReader(pos))
	dec.UseNumber()
	var vals []any
	if err := dec.Decode(&vals); err != nil || len(vals) != len(s.orderBy()) {
		return nil, errors.New("position is not one this binding returned")
	}
	for i, v := range vals {
		switch x := v.(type) {
		case json.Number:
			if n, err := x.Int64(); err == nil {
				vals[i] = n
			} else if f, err := x.Float64(); err == nil {
				vals[i] = f
			}
		case string, bool:
		default:
			return nil, errors.New("position is not one this binding returned")
		}
	}
	return vals, nil
}

// cursorArg converts a normalized cursor to the bind value its column type takes.
func (s *SQL) cursorArg(v any) (any, error) {
	text, ok := domain.NormalizeCursor(s.CursorType, v)
	if !ok {
		return nil, errors.New("cursor value is not a " + s.CursorType)
	}
	switch s.CursorType {
	case domain.CursorTimestamp:
		t, err := time.Parse(time.RFC3339Nano, text)
		if err != nil || !s.Driver.Dialect.TimestampText {
			return t, err
		}
		return timestampText(t), nil
	case domain.CursorInt:
		n, err := strconv.ParseInt(text, 10, 64)
		return n, err
	default:
		return hex.DecodeString(text)
	}
}

// timestampText renders a UTC time as ISO 8601 with no zone and at most 7 fractional digits (trailing zeros dropped),
// a form SQL Server converts to datetime, datetime2, smalldatetime, date or datetimeoffset regardless of language
// settings. A datetime column's values arrive rounded to whole milliseconds, so they never need more than 3 digits,
// which is all datetime accepts.
func timestampText(t time.Time) string {
	text := t.UTC().Format("2006-01-02T15:04:05.0000000")
	text = strings.TrimRight(text, "0")
	return strings.TrimSuffix(text, ".")
}

// minCursor is the value every real cursor is greater than, for a query's :watermark on a first extraction.
func (s *SQL) minCursor() any {
	switch s.CursorType {
	case domain.CursorInt:
		return int64(-1 << 62)
	case domain.CursorRowversion:
		return make([]byte, 8)
	}
	t := time.Date(1970, 1, 1, 0, 0, 0, 0, time.UTC)
	if s.Driver.Dialect.TimestampText {
		return timestampText(t)
	}
	return t
}

type builder struct {
	text strings.Builder
	args []any
	dial probe.Dialect
}

func (b *builder) param(v any) string {
	b.args = append(b.args, v)
	return b.dial.Param(len(b.args))
}

func (s *SQL) build(q Query) (string, []any, error) {
	dial := s.Driver.Dialect
	if dial.Param == nil || dial.Quote == nil {
		return "", nil, errors.New("the driver has no SQL dialect")
	}
	b := &builder{dial: dial}
	order := s.orderBy()
	quoted := make([]string, len(order))
	for i, c := range order {
		quoted[i] = dial.Quote(c)
	}
	// The source comes first in the text, so its :watermark parameters are numbered before the keyset ones.
	var prefix, from string
	if s.Src.Query != "" {
		sel, err := domain.ParseSelect(s.Src.Query)
		if err != nil {
			return "", nil, errors.New("query " + err.Error())
		}
		if sel.HasWatermark() && s.CursorField == "" {
			return "", nil, errors.New("query uses :watermark but the binding has no cursor field")
		}
		var since any
		if sel.HasWatermark() {
			since = s.minCursor()
			if q.Since != "" {
				v, err := s.cursorArg(q.Since)
				if err != nil {
					return "", nil, errors.New("since is not a " + s.CursorType + " watermark")
				}
				since = v
			}
		}
		// Comments were dropped by the guard, so nothing in the query can hide the clauses appended below.
		with, main := sel.Render(func() string { return b.param(since) })
		if with != "" {
			// SQL Server allows no WITH inside a derived table, so a WITH query becomes the last CTE of the statement.
			prefix = with + ", passage_src AS (" + main + ") "
			from = "passage_src"
		} else {
			from = "(" + main + ") src"
		}
	} else {
		from = dial.Quote(s.Src.Table)
		if s.Src.Schema != "" {
			from = dial.Quote(s.Src.Schema) + "." + from
		}
	}
	b.text.WriteString(prefix + "SELECT ")
	if dial.Limit == probe.LimitTop {
		fmt.Fprintf(&b.text, "TOP (%d) ", q.Limit+1)
	}
	if s.Src.Query != "" || len(s.Src.Columns) == 0 {
		b.text.WriteString("*")
	} else {
		cols := make([]string, len(s.Src.Columns))
		for i, c := range s.Src.Columns {
			cols[i] = dial.Quote(c)
		}
		b.text.WriteString(strings.Join(cols, ", "))
	}
	b.text.WriteString(" FROM " + from)
	switch {
	case q.Position != "":
		vals, err := s.decodePosition(q.Position)
		if err != nil {
			return "", nil, err
		}
		args := make([]any, len(vals))
		for i, v := range vals {
			args[i] = v
			if i == 0 && s.CursorField != "" {
				if args[i], err = s.cursorArg(v); err != nil {
					return "", nil, errors.New("position is not one this binding returned")
				}
			}
		}
		b.text.WriteString(" WHERE ")
		for i := range quoted {
			if i > 0 {
				b.text.WriteString(" OR ")
			}
			b.text.WriteString("(")
			for j := 0; j < i; j++ {
				b.text.WriteString(quoted[j] + " = " + b.param(args[j]) + " AND ")
			}
			b.text.WriteString(quoted[i] + " > " + b.param(args[i]) + ")")
		}
	case q.Since != "" && s.CursorField != "":
		v, err := s.cursorArg(q.Since)
		if err != nil {
			return "", nil, errors.New("since is not a " + s.CursorType + " watermark")
		}
		b.text.WriteString(" WHERE " + quoted[0] + " > " + b.param(v))
	}
	b.text.WriteString(" ORDER BY " + strings.Join(quoted, ", "))
	switch dial.Limit {
	case probe.LimitClause:
		fmt.Fprintf(&b.text, " LIMIT %d", q.Limit+1)
	case probe.LimitFetch:
		fmt.Fprintf(&b.text, " FETCH FIRST %d ROWS ONLY", q.Limit+1)
	}
	return b.text.String(), b.args, nil
}
