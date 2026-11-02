package domain

import (
	"strings"
	"testing"
)

// The read-only query guard refuses every construct below. Each vector holds only the one construct under test.
func TestValidateSelect_RejectsAmbiguousOrWritingConstructs(t *testing.T) {
	cases := []struct{ name, q, want string }{
		{"empty", "  ", "empty"},
		{"too long", "select " + strings.Repeat("a,", 4000) + "a", "longer"},
		{"not a select", "update t set a = 1", "SELECT"},
		{"a second statement", "select 1; select 2", "one statement"},
		{"a parenthesized start", "(select 1)", "SELECT"},
		{"unterminated quote", "select 'x", "unterminated"},
		{"unterminated comment", "select 1 /* x", "unterminated"},
		{"unbalanced parentheses", "select (1", "("},
		{"backslash in a string", `select 'a\' from t`, "backslash"},
		{"backslash in a quoted name", `select "a\" from t`, "backslash"},
		{"E'' string", "select E'x'", "prefixed"},
		{"Oracle q'' quoting", "select q'[x]' from dual", "prefixed"},
		{"charset introducer", "select _utf8'x'", "prefixed"},
		{"U&'' escape", "select U&'x'", "U&"},
		{"# comment", "select 1 # x", "#"},
		{"MySQL /*! */ comment", "select 1 /*! x */", "/*!"},
		{"MariaDB /*M! */ comment", "select 1 /*M! x */", "/*!"},
		{"nested comment", "select 1 /* a /* b */", "nest"},
		{"-- without whitespace", "select 1 --x", "space after --"},
		{"lone carriage return", "select 1 -- x\rfrom t", "carriage return"},
		{"control character", "select 1\x0bfrom t", "control"},
		{"non-ASCII outside quotes", "select ä from t", "non-ASCII"},
		{"]] inside brackets", "select [a]]b] from t", "]]"},
		{"bracket subscript hiding a call", "select a[b(1)] from t", "inside [name]"},
		{"bracket hiding a refused word", "select a[pg_sleep] from t", "PG_SLEEP"},
		{"-- inside brackets", "select a[x -- y] from t", "-- inside"},
		{"quote inside backticks", "select `a'b` from t", "inside `name`"},
		{"refused word inside backticks", "select `sleep` from t", "SLEEP"},
		{"stray ]", "select a] from t", "]"},
		{"ODBC escape", "select {fn now()}", "{ }"},
		{"dollar quoting", "select $$x$$", "$"},
		{"positional parameter", "select $1", "$"},
		{"question-mark parameter", "select ?", "?"},
		{"variable", "select @x", "@"},
		{"other : parameter", "select :other", ":watermark"},
		{"number glued to a word", "select 1from t", "separate"},
		{"call through a quoted name", `select "f"(1)`, "quoted name"},
		{"WITH without a SELECT", "with x as (select 1)", "SELECT"},
		{"WITH followed by a non-CTE", "with x select 1", "WITH"},
		{"INTO", "select a into b from t", "INTO"},
		{"OUTFILE", "select a from t outfile", "OUTFILE"},
		{"DUMPFILE", "select a from t dumpfile", "DUMPFILE"},
		{"FOR UPDATE", "select a from t for update", "UPDATE"},
		{"FOR SHARE", "select a from t for share", "row locks"},
		{"FOR KEY SHARE", "select a from t for key share", "row locks"},
		{"FOR NO KEY UPDATE", "select a from t for no key update", "row locks"},
		{"lock hint", "select a from t with (updlock)", "UPDLOCK"},
		{"NEXT VALUE FOR", "select next value for s", "NEXT VALUE"},
		{"OPENQUERY", "select * from openquery(s, 'x')", "OPENQUERY"},
		{"OPENROWSET", "select * from openrowset('x')", "OPENROWSET"},
		{"OPENDATASOURCE", "select * from opendatasource('x')", "OPENDATASOURCE"},
		{"xp_ prefix", "select xp_x()", "XP_"},
		{"sp_ prefix", "select sp_x()", "SP_"},
		{"pg_ prefix", "select pg_x()", "PG_"},
		{"lo_ prefix", "select lo_x()", "LO_"},
		{"dbms_ prefix", "select dbms_x.y()", "DBMS_"},
		{"utl_ prefix", "select utl_x.y()", "UTL_"},
		{"dblink prefix", "select dblink_exec()", "DBLINK"},
		{"sleep", "select sleep(1)", "SLEEP"},
		{"benchmark", "select benchmark(1, 1)", "BENCHMARK"},
		{"get_lock", "select get_lock('a', 1)", "GET_LOCK"},
		{"load_file", "select load_file('a')", "LOAD_FILE"},
		{"waitfor", "select 1 waitfor", "WAITFOR"},
		{"set_config", "select set_config('a', 'b', true)", "SET_CONFIG"},
		{"nextval", "select nextval('s')", "NEXTVAL"},
		{"DML in a CTE", "with x as (delete from t) select 1", "DELETE"},
		{"EXEC", "exec x", "SELECT"},
		{"refused word in another case", "select a from t FoR uPdAtE", "UPDATE"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			err := ValidateSelect(c.q)
			if err == nil {
				t.Fatalf("accepted %q", c.q)
			}
			if !strings.Contains(err.Error(), c.want) {
				t.Fatalf("%q refused with %q, want it to mention %q", c.q, err, c.want)
			}
		})
	}
}

func TestValidateSelect_AcceptsPlainReadOnlyQueries(t *testing.T) {
	for _, q := range []string{
		"SELECT 1",
		"select a, b from erp.cust where c > 1;",
		"select a from t where upd > :watermark",
		`select "Customer Name", "update" from "Cust"`,
		"select [Customer Name], [Order#] from [dbo].[Cust]",
		"select `Customer Name` from `cust`",
		"select 'it''s; -- not a comment /* nor this */' as note from t",
		"select N'Grüße' from t",
		`select "Straße" from t`,
		"select a::date, 1.5e-3, 0x1F, .5 from t where a <> b and c >= d or e != f",
		"select a from t -- trailing note",
		"select a /* inline note */ from t",
		"select a from t for xml path",
		"with x as (select a from t), y (a) as (select a from x) select a from y",
		"WITH RECURSIVE r AS (select 1 as n union all select n + 1 from r where n < 5) SELECT n FROM r",
		"select replace(name, 'a', 'b'), coalesce(a, b) from t",
		"select a from t\r\nwhere b = 1",
	} {
		if err := ValidateSelect(q); err != nil {
			t.Errorf("%q refused: %v", q, err)
		}
	}
}

func TestReadOnlySelect_Render_DropsCommentsAndSplitsWith(t *testing.T) {
	n := 0
	param := func() string { n++; return "$" + string(rune('0'+n)) }

	s, err := ParseSelect("  select a from t where u > :watermark -- note\n and v = ':watermark';  ")
	if err != nil {
		t.Fatal(err)
	}
	with, main := s.Render(param)
	if with != "" || main != "select a from t where u > $1  \n and v = ':watermark'" || !s.HasWatermark() {
		t.Fatalf("with %q main %q", with, main)
	}

	n = 0
	s, err = ParseSelect("with x as (select a from t where u > :watermark /* c */) select a from x where u > :watermark -- trailing")
	if err != nil {
		t.Fatal(err)
	}
	with, main = s.Render(param)
	if with != "with x as (select a from t where u > $1  )" || main != "select a from x where u > $2" {
		t.Fatalf("with %q main %q", with, main)
	}

	s, _ = ParseSelect("select 1")
	if s.HasWatermark() {
		t.Fatal("watermark reported for a query without one")
	}
}
