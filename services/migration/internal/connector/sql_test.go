package connector_test

import (
	"context"
	"database/sql"
	"fmt"
	"net"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgconn"

	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/services/migration/internal/connector"
	"github.com/BenjaminSRussell/passage/services/migration/internal/domain"
	"github.com/BenjaminSRussell/passage/services/migration/internal/probe"
)

// openLocal opens the real postgres registry entry against the test database. The egress guard is covered by the probe
// tests; the extractor's own logic (keyset paging, delta, read-only) is what this exercises.
func openLocal(t *testing.T) (*sql.DB, probe.SQLDriver, string) {
	t.Helper()
	url := os.Getenv("PASSAGE_TEST_DATABASE_URL")
	if url == "" {
		t.Skip("PASSAGE_TEST_DATABASE_URL not set; skipping database test")
	}
	cc, err := pgconn.ParseConfig(url)
	if err != nil {
		t.Fatal(err)
	}
	drv, _ := probe.LookupSQLDriver("postgres")
	conn, err := drv.Connector(probe.SQLSettings{Host: cc.Host, Port: cc.Port, Database: cc.Database, User: cc.User, Password: cc.Password, SSLMode: "disable"},
		func(ctx context.Context, network, address string) (net.Conn, error) {
			return (&net.Dialer{}).DialContext(ctx, network, address)
		}, nil)
	if err != nil {
		t.Fatal(err)
	}
	db := sql.OpenDB(conn)
	db.SetMaxOpenConns(1)
	t.Cleanup(func() { _ = db.Close() })
	return db, drv, "extract_" + strings.ReplaceAll(ids.New(), "-", "")[:12]
}

func seedLegacy(t *testing.T, db *sql.DB, table string) {
	t.Helper()
	ctx := context.Background()
	stmts := []string{
		fmt.Sprintf(`create table public.%s (id integer primary key, company text not null, name text, upd timestamptz not null, ver bigint not null, note bytea)`, table),
	}
	for i := 1; i <= 25; i++ {
		// Rows 1..25 change on days 1..25 except that five rows share one timestamp, to prove ties do not skip or repeat.
		day := i
		if i >= 10 && i < 15 {
			day = 10
		}
		stmts = append(stmts, fmt.Sprintf(`insert into public.%s values (%d, 'C%d', 'N%d', '2026-03-%02dT10:00:00Z', %d, '\x%02x%02x')`, table, i, i%3, i, day, i*10, i, i))
	}
	for _, s := range stmts {
		if _, err := db.ExecContext(ctx, s); err != nil {
			t.Fatal(err)
		}
	}
	t.Cleanup(func() { _, _ = db.ExecContext(context.Background(), `drop table public.`+table) })
}

func sqlReader(db *sql.DB, drv probe.SQLDriver, src domain.SQLSource, keys []string, cursor, typ string) *connector.SQL {
	return &connector.SQL{DB: db, Driver: drv, Src: src, Keys: keys, CursorField: cursor, CursorType: typ}
}

func readAll(t *testing.T, r connector.Reader, since string, limit int) ([]connector.Row, string) {
	t.Helper()
	var rows []connector.Row
	wm := ""
	q := connector.Query{Limit: limit, Since: since}
	for pages := 0; pages < 100; pages++ {
		p, err := r.Read(context.Background(), q)
		if err != nil {
			t.Fatal(err)
		}
		if len(p.Rows) > limit {
			t.Fatalf("page of %d rows for limit %d", len(p.Rows), limit)
		}
		rows = append(rows, p.Rows...)
		wm = domain.MaxCursor(domain.CursorTimestamp, wm, p.Watermark)
		if p.Next == "" {
			return rows, wm
		}
		q.Position = p.Next
	}
	t.Fatal("paging did not end")
	return nil, ""
}

func TestSQL_KeysetPagingDeltaAndQueryAgainstPostgres(t *testing.T) {
	db, drv, table := openLocal(t)
	seedLegacy(t, db, table)

	// Table: pages of 4 over 25 rows with five tied change stamps, no row skipped or repeated.
	r := sqlReader(db, drv, domain.SQLSource{Table: table, Schema: "public", Columns: []string{"id", "company", "name", "upd"}}, []string{"id"}, "upd", domain.CursorTimestamp)
	rows, wm := readAll(t, r, "", 4)
	if len(rows) != 25 || wm != "2026-03-25T10:00:00.000000000Z" {
		t.Fatalf("rows %d watermark %q", len(rows), wm)
	}
	seen := map[any]bool{}
	for _, row := range rows {
		if seen[row["id"]] {
			t.Fatalf("row %v repeated", row["id"])
		}
		seen[row["id"]] = true
	}
	if rows[0]["upd"] != "2026-03-01T10:00:00.000000000Z" {
		t.Fatalf("timestamp not normalized: %v", rows[0]["upd"])
	}

	// Delta: rows changed after day 20.
	rows, wm = readAll(t, r, "2026-03-20T10:00:00.000000000Z", 4)
	if len(rows) != 5 || wm != "2026-03-25T10:00:00.000000000Z" {
		t.Fatalf("delta rows %d watermark %q", len(rows), wm)
	}

	// Query with :watermark and a composite key, and bytea read as hex.
	q := sqlReader(db, drv, domain.SQLSource{Query: fmt.Sprintf(`SELECT id, company, upd, note FROM public.%s WHERE upd > :watermark`, table)}, []string{"company", "id"}, "upd", domain.CursorTimestamp)
	rows, _ = readAll(t, q, "2026-03-22T10:00:00.000000000Z", 2)
	if len(rows) != 3 || rows[0]["note"] != "1717" {
		t.Fatalf("query rows %d: %+v", len(rows), rows)
	}

	// A WITH query with :watermark in the CTE and a trailing comment pages the same way.
	w := sqlReader(db, drv, domain.SQLSource{Query: fmt.Sprintf("WITH c AS (SELECT id, company, upd FROM public.%s WHERE upd > :watermark) SELECT id, company, upd FROM c -- recent rows", table)}, []string{"id"}, "upd", domain.CursorTimestamp)
	rows, wm = readAll(t, w, "2026-03-05T10:00:00.000000000Z", 4)
	if len(rows) != 20 || wm != "2026-03-25T10:00:00.000000000Z" {
		t.Fatalf("with query rows %d watermark %q", len(rows), wm)
	}

	// A NULL key fails the extraction instead of being skipped.
	nk := sqlReader(db, drv, domain.SQLSource{Query: fmt.Sprintf("SELECT id, CASE WHEN id = 3 THEN NULL ELSE company END AS company FROM public.%s", table)}, []string{"company", "id"}, "", "")
	if _, err := nk.Read(context.Background(), connector.Query{Limit: 50}); err == nil || !strings.Contains(err.Error(), "column company is NULL") {
		t.Fatalf("null key: %v", err)
	}

	// Integer cursor.
	iv := sqlReader(db, drv, domain.SQLSource{Table: table, Schema: "public"}, []string{"id"}, "ver", domain.CursorInt)
	p, err := iv.Read(context.Background(), connector.Query{Limit: 5, Since: "200"})
	if err != nil || len(p.Rows) != 5 || p.Watermark != "250" || p.Next != "" {
		t.Fatalf("int cursor: %v %+v", err, p)
	}

	// Whole table without a cursor, ordered by key.
	plain := sqlReader(db, drv, domain.SQLSource{Table: table}, []string{"id"}, "", "")
	rows, wm = readAll(t, plain, "", 10)
	if len(rows) != 25 || wm != "" || rows[24]["id"] != int64(25) {
		t.Fatalf("plain: %d %q %v", len(rows), wm, rows[len(rows)-1]["id"])
	}
}

func TestSQL_ReadsAreReadOnlyAndBounded(t *testing.T) {
	db, drv, table := openLocal(t)
	seedLegacy(t, db, table)
	// A write that slipped past validation still fails: the extraction transaction is read-only.
	tx, err := drv.Begin(context.Background(), db)
	if err != nil {
		t.Fatal(err)
	}
	_, err = tx.ExecContext(context.Background(), fmt.Sprintf(`delete from public.%s`, table))
	_ = tx.Rollback()
	if err == nil {
		t.Fatal("delete succeeded inside the extraction transaction")
	}
	var n int
	if err := db.QueryRow(fmt.Sprintf(`select count(*) from public.%s`, table)).Scan(&n); err != nil || n != 25 {
		t.Fatalf("rows left %d %v", n, err)
	}
	// Limits and unknown columns fail cleanly.
	r := sqlReader(db, drv, domain.SQLSource{Table: table}, []string{"id"}, "", "")
	if _, err := r.Read(context.Background(), connector.Query{Limit: 0}); err == nil {
		t.Fatal("limit 0 read")
	}
	if _, err := sqlReader(db, drv, domain.SQLSource{Table: table}, []string{"nope"}, "", "").Read(context.Background(), connector.Query{Limit: 5}); err == nil {
		t.Fatal("unknown key column read")
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Nanosecond)
	defer cancel()
	if _, err := r.Read(ctx, connector.Query{Limit: 5}); err == nil {
		t.Fatal("expired context read")
	}
}
