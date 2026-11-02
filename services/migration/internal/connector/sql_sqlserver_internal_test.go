package connector

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"fmt"
	"io"
	"math"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/BenjaminSRussell/passage/services/migration/internal/domain"
	"github.com/BenjaminSRussell/passage/services/migration/internal/probe"
)

// fakeDatetime stands in for a SQL Server table with an int key and a datetime change stamp, which SQL Server keeps
// in 1/300 s ticks. It answers only the keyset queries SQL.build writes and follows SQL Server's comparison rules:
// a text parameter is converted to the column's type (datetime, rounded to the nearest tick, and only up to 3
// fractional digits accepted), a time parameter is a datetimeoffset, so the column is converted to it exactly. Values
// are returned the way go-mssqldb decodes a datetime: rounded to whole milliseconds.
type fakeDatetime struct {
	rows []fakeDatetimeRow
}

type fakeDatetimeRow struct {
	id    int64
	ticks *int64 // since fakeBase; nil is NULL
}

var fakeBase = time.Date(2026, 3, 1, 0, 0, 0, 0, time.UTC)

func (f *fakeDatetime) Connect(context.Context) (driver.Conn, error) { return fakeConn{f}, nil }
func (f *fakeDatetime) Driver() driver.Driver                        { return nil }

type fakeConn struct{ f *fakeDatetime }

func (fakeConn) Prepare(string) (driver.Stmt, error) { return nil, errors.New("not supported") }
func (fakeConn) Close() error                        { return nil }
func (fakeConn) Begin() (driver.Tx, error)           { return fakeTx{}, nil }

type fakeTx struct{}

func (fakeTx) Commit() error   { return nil }
func (fakeTx) Rollback() error { return nil }

var topRe = regexp.MustCompile(`^SELECT TOP \((\d+)\) \* FROM \[t\]`)

// compare orders a row's stamp against a bound parameter, the way SQL Server would.
func compare(ticks int64, arg any) (int, error) {
	switch v := arg.(type) {
	case string:
		if dot := strings.IndexByte(v, '.'); dot >= 0 && len(v)-dot-1 > 3 {
			return 0, errors.New("conversion failed when converting date and/or time from character string")
		}
		t, err := time.Parse("2006-01-02T15:04:05.999", v)
		if err != nil {
			return 0, err
		}
		p := int64(math.Round(float64(t.Sub(fakeBase)) / (float64(time.Second) / 300)))
		return cmpInt(ticks, p), nil
	case time.Time:
		col := int64(math.Round(float64(ticks) * 1e7 / 300)) // the column as datetimeoffset(7), in 100 ns units
		return cmpInt(col, int64(v.Sub(fakeBase)/100)), nil
	}
	return 0, fmt.Errorf("unexpected parameter %T", arg)
}

func cmpInt(a, b int64) int {
	switch {
	case a < b:
		return -1
	case a > b:
		return 1
	}
	return 0
}

func (c fakeConn) QueryContext(_ context.Context, query string, args []driver.NamedValue) (driver.Rows, error) {
	m := topRe.FindStringSubmatch(query)
	if m == nil {
		return nil, fmt.Errorf("unexpected query %q", query)
	}
	top, _ := strconv.Atoi(m[1])
	rows := slices.Clone(c.f.rows)
	slices.SortFunc(rows, func(a, b fakeDatetimeRow) int { // NULLs sort first, as in SQL Server
		switch {
		case a.ticks == nil && b.ticks != nil:
			return -1
		case a.ticks != nil && b.ticks == nil:
			return 1
		case a.ticks != nil && *a.ticks != *b.ticks:
			return cmpInt(*a.ticks, *b.ticks)
		}
		return cmpInt(a.id, b.id)
	})
	var out []fakeDatetimeRow
	for _, r := range rows {
		keep := true
		switch len(args) {
		case 1: // [changed] > @p1
			if r.ticks == nil {
				keep = false
				break
			}
			c, err := compare(*r.ticks, args[0].Value)
			if err != nil {
				return nil, err
			}
			keep = c > 0
		case 3: // ([changed] > @p1) OR ([changed] = @p2 AND [id] > @p3)
			if r.ticks == nil {
				keep = false
				break
			}
			c1, err := compare(*r.ticks, args[0].Value)
			if err != nil {
				return nil, err
			}
			c2, err := compare(*r.ticks, args[1].Value)
			if err != nil {
				return nil, err
			}
			keep = c1 > 0 || c2 == 0 && r.id > args[2].Value.(int64)
		}
		if keep {
			out = append(out, r)
		}
	}
	return &fakeRows{rows: out[:min(top, len(out))]}, nil
}

type fakeRows struct {
	rows []fakeDatetimeRow
	i    int
}

func (*fakeRows) Columns() []string { return []string{"id", "changed"} }
func (*fakeRows) Close() error      { return nil }
func (r *fakeRows) Next(dest []driver.Value) error {
	if r.i >= len(r.rows) {
		return io.EOF
	}
	row := r.rows[r.i]
	r.i++
	dest[0] = row.id
	dest[1] = nil
	if row.ticks != nil {
		ms := int64(math.Trunc(float64(*row.ticks%300)/0.3 + 0.5))
		dest[1] = fakeBase.Add(time.Duration(*row.ticks/300)*time.Second + time.Duration(ms)*time.Millisecond)
	}
	return nil
}

func fakeSQLServer(t *testing.T, rows []fakeDatetimeRow, timestampText bool) *Extractor {
	t.Helper()
	db := sql.OpenDB(&fakeDatetime{rows: rows})
	t.Cleanup(func() { _ = db.Close() })
	drv, _ := probe.LookupSQLDriver("sqlserver")
	drv.Dialect.TimestampText = timestampText
	reader := &SQL{DB: db, Driver: drv, Src: domain.SQLSource{Table: "t"}, Keys: []string{"id"}, CursorField: "changed", CursorType: domain.CursorTimestamp}
	return &Extractor{PageSize: 4, Bindings: map[string]Binding{"customer": {
		KeyFields: []string{"id"}, CursorField: "changed", CursorType: domain.CursorTimestamp,
		Open: func(context.Context) (Opened, error) { return Opened{Reader: reader}, nil },
	}}}
}

func tick(n int64) *int64 { return &n }

// Ten rows share the stamp .00333 s and ten share .00667 s, so page boundaries (4 rows a page) fall inside ties on
// values a datetime rounds; every row must come back exactly once and the extraction must end.
func TestSQLServerDatetimeCursor_PageBoundaryOnARoundedValueNeitherRepeatsNorLoops(t *testing.T) {
	var rows []fakeDatetimeRow
	for i := int64(1); i <= 25; i++ {
		switch {
		case i <= 10:
			rows = append(rows, fakeDatetimeRow{id: i, ticks: tick(1)})
		case i <= 20:
			rows = append(rows, fakeDatetimeRow{id: i, ticks: tick(2)})
		default:
			rows = append(rows, fakeDatetimeRow{id: i, ticks: tick(300 + i)})
		}
	}
	recs, wm, err := fakeSQLServer(t, rows, true).ExtractSince(context.Background(), "customer", "")
	if err != nil {
		t.Fatal(err)
	}
	seen := map[string]int{}
	for _, r := range recs {
		seen[r.Key]++
	}
	if len(recs) != 25 || len(seen) != 25 {
		t.Fatalf("got %d records, %d distinct", len(recs), len(seen))
	}
	if want := "2026-03-01T00:00:01.083000000Z"; wm != want {
		t.Fatalf("watermark %s, want %s", wm, want)
	}
	// A delta from a watermark on a rounded value reads only what is newer.
	recs, _, err = fakeSQLServer(t, rows, true).ExtractSince(context.Background(), "customer", "2026-03-01T00:00:00.003000000Z")
	if err != nil || len(recs) != 15 {
		t.Fatalf("delta after the first tie: %d records, %v", len(recs), err)
	}

	// Bound as a time value (a datetimeoffset) the same boundary reads the tie again, which the extractor stops.
	if _, _, err := fakeSQLServer(t, rows, false).ExtractSince(context.Background(), "customer", ""); err == nil || !strings.Contains(err.Error(), "same next page position") {
		t.Fatalf("time-bound cursor: %v", err)
	}
}

func TestSQLKeyset_FailsTheExtractionOnANullKeyOrCursor(t *testing.T) {
	rows := []fakeDatetimeRow{{id: 1, ticks: nil}, {id: 2, ticks: tick(5)}}
	if _, _, err := fakeSQLServer(t, rows, true).ExtractSince(context.Background(), "customer", ""); err == nil || !strings.Contains(err.Error(), "column changed is NULL") {
		t.Fatalf("null cursor: %v", err)
	}
}

func TestTimestampText_IsZonelessISOWithAtMostSevenDigits(t *testing.T) {
	for in, want := range map[string]string{
		"2026-03-01T10:00:00.000000000Z": "2026-03-01T10:00:00",
		"2026-03-01T10:00:00.003000000Z": "2026-03-01T10:00:00.003",
		"2026-03-01T10:00:00.123456789Z": "2026-03-01T10:00:00.1234567",
	} {
		tm, _ := time.Parse(time.RFC3339Nano, in)
		if got := timestampText(tm); got != want {
			t.Errorf("%s: %s, want %s", in, got, want)
		}
	}
}
