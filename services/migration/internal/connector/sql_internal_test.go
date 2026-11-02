package connector

import (
	"strings"
	"testing"

	"github.com/BenjaminSRussell/passage/services/migration/internal/domain"
	"github.com/BenjaminSRussell/passage/services/migration/internal/probe"
)

func TestSQLBuild_KeysetDialectsAndInjection(t *testing.T) {
	pg, _ := probe.LookupSQLDriver("postgres")
	ms, _ := probe.LookupSQLDriver("sqlserver")
	table := domain.SQLSource{Schema: "dbo", Table: "Cust Tab]le", Columns: []string{"id", "name", "changed"}}

	s := &SQL{Driver: pg, Src: table, Keys: []string{"id"}, CursorField: "changed", CursorType: domain.CursorTimestamp}
	text, args, err := s.build(Query{Limit: 50})
	if err != nil || text != `SELECT "id", "name", "changed" FROM "dbo"."Cust Tab]le" ORDER BY "changed", "id" LIMIT 51` || len(args) != 0 {
		t.Fatalf("first page: %q %v %v", text, args, err)
	}
	text, args, err = s.build(Query{Limit: 50, Since: "2026-03-01T00:00:00.000000000Z"})
	if err != nil || !strings.Contains(text, `WHERE "changed" > $1 ORDER BY`) || len(args) != 1 {
		t.Fatalf("delta: %q %v %v", text, args, err)
	}
	pos := `["2026-03-01T00:00:00.000000000Z",42]`
	text, args, err = s.build(Query{Limit: 50, Position: pos})
	want := `WHERE ("changed" > $1) OR ("changed" = $2 AND "id" > $3) ORDER BY`
	if err != nil || !strings.Contains(text, want) || len(args) != 3 {
		t.Fatalf("keyset: %q %v %v", text, args, err)
	}

	m := &SQL{Driver: ms, Src: domain.SQLSource{Table: "Customers"}, Keys: []string{"id", "company"}}
	text, args, err = m.build(Query{Limit: 10, Position: `[7,"C1"]`})
	if err != nil || text != `SELECT TOP (11) * FROM [Customers] WHERE ([id] > @p1) OR ([id] = @p2 AND [company] > @p3) ORDER BY [id], [company]` || len(args) != 3 {
		t.Fatalf("sqlserver: %q %v %v", text, args, err)
	}

	my, _ := probe.LookupSQLDriver("mysql")
	text, args, err = (&SQL{Driver: my, Src: domain.SQLSource{Table: "cust"}, Keys: []string{"id"}, CursorField: "upd", CursorType: domain.CursorInt}).build(Query{Limit: 9, Position: `[5,7]`})
	if err != nil || text != "SELECT * FROM `cust` WHERE (`upd` > ?) OR (`upd` = ? AND `id` > ?) ORDER BY `upd`, `id` LIMIT 10" || len(args) != 3 {
		t.Fatalf("mysql: %q %v %v", text, args, err)
	}
	or, _ := probe.LookupSQLDriver("oracle")
	text, args, err = (&SQL{Driver: or, Src: domain.SQLSource{Schema: "ERP", Table: "KNA1"}, Keys: []string{"KUNNR"}}).build(Query{Limit: 9, Position: `["0000100"]`})
	if err != nil || text != `SELECT * FROM "ERP"."KNA1" WHERE ("KUNNR" > :1) ORDER BY "KUNNR" FETCH FIRST 10 ROWS ONLY` || len(args) != 1 {
		t.Fatalf("oracle: %q %v %v", text, args, err)
	}

	q := &SQL{Driver: pg, Src: domain.SQLSource{Query: "SELECT id, name, upd FROM erp.cust WHERE upd > :watermark AND note <> ':watermark'"}, Keys: []string{"id"}, CursorField: "upd", CursorType: domain.CursorTimestamp}
	text, args, err = q.build(Query{Limit: 5, Since: "2026-03-01T00:00:00.000000000Z"})
	if err != nil || !strings.HasPrefix(text, `SELECT * FROM (SELECT id, name, upd FROM erp.cust WHERE upd > $1 AND note <> ':watermark') src WHERE "upd" > $2`) || len(args) != 2 {
		t.Fatalf("query: %q %v %v", text, args, err)
	}
	// A WITH query becomes the leading CTEs plus passage_src, and its comments are dropped so a trailing -- cannot
	// swallow the generated WHERE, ORDER BY and row limit.
	w := &SQL{Driver: ms, Src: domain.SQLSource{Query: "WITH c AS (SELECT id, upd FROM cust WHERE upd > :watermark) SELECT id, upd FROM c -- note"}, Keys: []string{"id"}, CursorField: "upd", CursorType: domain.CursorTimestamp}
	text, args, err = w.build(Query{Limit: 5, Since: "2026-03-01T00:00:00.003000000Z", Position: `["2026-03-01T00:00:00.003000000Z",4]`})
	want = `WITH c AS (SELECT id, upd FROM cust WHERE upd > @p1), passage_src AS (SELECT id, upd FROM c) SELECT TOP (6) * FROM passage_src WHERE ([upd] > @p2) OR ([upd] = @p3 AND [id] > @p4) ORDER BY [upd], [id]`
	if err != nil || text != want || len(args) != 4 {
		t.Fatalf("with query: %q %v %v", text, args, err)
	}
	// SQL Server timestamp cursors are bound as zoneless ISO text so the server converts them to the column's type.
	for i, a := range args[:3] {
		if a != "2026-03-01T00:00:00.003" {
			t.Fatalf("sqlserver timestamp arg %d: %#v", i, a)
		}
	}
	text, _, err = (&SQL{Driver: pg, Src: domain.SQLSource{Query: "select id from t -- note"}, Keys: []string{"id"}}).build(Query{Limit: 5})
	if err != nil || text != `SELECT * FROM (select id from t) src ORDER BY "id" LIMIT 6` {
		t.Fatalf("trailing comment: %q %v", text, err)
	}

	// A query the guard refuses is refused at build time too, even if it reached the store.
	for _, bad := range []string{"SELECT 1; DROP TABLE x", "DELETE FROM cust", "SELECT * INTO t FROM cust", "WITH x AS (DELETE FROM c RETURNING *) SELECT * FROM x"} {
		b := &SQL{Driver: pg, Src: domain.SQLSource{Query: bad}, Keys: []string{"id"}}
		if _, _, err := b.build(Query{Limit: 5}); err == nil {
			t.Errorf("%q built", bad)
		}
	}
	if _, _, err := (&SQL{Driver: pg, Src: domain.SQLSource{Query: "SELECT * FROM c WHERE u > :watermark"}, Keys: []string{"id"}}).build(Query{Limit: 5}); err == nil {
		t.Error(":watermark without a cursor built")
	}
	if _, _, err := s.build(Query{Limit: 5, Position: `["x"]`}); err == nil {
		t.Error("a position of the wrong shape built")
	}
}
