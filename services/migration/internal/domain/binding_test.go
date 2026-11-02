package domain

import (
	"testing"
	"time"
)

func sqlBinding(src map[string]any) EntityBinding {
	return EntityBinding{Entity: "customer", Source: src, KeyFields: []string{"id"}}
}

func TestValidateBinding_PerKind(t *testing.T) {
	cases := []struct {
		name string
		kind string
		b    EntityBinding
		bad  string // a key expected in the result; "" means valid
	}{
		{"table", ConnSQL, sqlBinding(map[string]any{"schema": "dbo", "table": "CustTable"}), ""},
		{"table with columns", ConnSQL, sqlBinding(map[string]any{"table": "t", "columns": []any{"id", "name"}}), ""},
		{"columns missing the key", ConnSQL, sqlBinding(map[string]any{"table": "t", "columns": []any{"name"}}), "source.columns"},
		{"table and query", ConnSQL, sqlBinding(map[string]any{"table": "t", "query": "select 1"}), "source"},
		{"neither", ConnSQL, sqlBinding(map[string]any{}), "source"},
		{"quoted table", ConnSQL, sqlBinding(map[string]any{"table": `t"; drop table x; --`}), "source.table"},
		{"schema dot", ConnSQL, sqlBinding(map[string]any{"table": "a.b"}), "source.table"},
		{"query", ConnSQL, sqlBinding(map[string]any{"query": "select id from t where u > :watermark"}), ""},
		{"insert", ConnSQL, sqlBinding(map[string]any{"query": "insert into t values (1)"}), "source.query"},
		{"two statements", ConnSQL, sqlBinding(map[string]any{"query": "select 1; select 2"}), "source.query"},
		{"select into", ConnSQL, sqlBinding(map[string]any{"query": "select * into x from t"}), "source.query"},
		{"cte write", ConnSQL, sqlBinding(map[string]any{"query": "with d as (delete from t returning *) select * from d"}), "source.query"},
		{"unknown source key", ConnSQL, sqlBinding(map[string]any{"table": "t", "where": "1=1"}), "source"},
		{"key with space", ConnSQL, EntityBinding{Entity: "customer", Source: map[string]any{"table": "t"}, KeyFields: []string{"a b"}}, "keyFields"},
		{"rest", ConnREST, sqlBinding(map[string]any{"path": "/api/customers", "recordsPath": "data.items", "paging": map[string]any{"style": "page", "pageSize": 100}}), ""},
		{"rest cursor paging needs a path", ConnREST, sqlBinding(map[string]any{"path": "/c", "paging": map[string]any{"style": "cursor"}}), "source.paging.cursorPath"},
		{"rest relative path", ConnREST, sqlBinding(map[string]any{"path": "api/c"}), "source.path"},
		{"rest query in path", ConnREST, sqlBinding(map[string]any{"path": "/c?key=1"}), "source.path"},
		{"rest dotdot", ConnREST, sqlBinding(map[string]any{"path": "/a/../c"}), "source.path"},
		{"rest secret param", ConnREST, sqlBinding(map[string]any{"path": "/c", "params": map[string]any{"api_key": "abc"}}), "source.params"},
		{"rest page size", ConnREST, sqlBinding(map[string]any{"path": "/c", "paging": map[string]any{"style": "page", "pageSize": 5000}}), "source.paging.pageSize"},
		{"rest paging style", ConnREST, sqlBinding(map[string]any{"path": "/c", "paging": map[string]any{"style": "graphql"}}), "source.paging.style"},
		{"file", ConnFile, sqlBinding(map[string]any{"file": "customers.csv"}), ""},
		{"file pattern", ConnFile, sqlBinding(map[string]any{"filePattern": "cust_*.csv"}), ""},
		{"file both", ConnFile, sqlBinding(map[string]any{"file": "a.csv", "filePattern": "*.csv"}), "source"},
		{"file path", ConnFile, sqlBinding(map[string]any{"file": "../a.csv"}), "source.file"},
		{"file glob in file", ConnFile, sqlBinding(map[string]any{"file": "*.csv"}), "source.file"},
		{"unknown kind", "ftp", sqlBinding(map[string]any{}), "connectionProfileId"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			bad := ValidateBinding(c.b, c.kind)
			if c.bad == "" && len(bad) > 0 {
				t.Fatalf("valid binding refused: %v", bad)
			}
			if c.bad != "" {
				if _, ok := bad[c.bad]; !ok {
					t.Fatalf("want a problem at %q, got %v", c.bad, bad)
				}
			}
		})
	}
}

func TestValidateBinding_EntityKeysAndCursor(t *testing.T) {
	base := func() EntityBinding { return sqlBinding(map[string]any{"table": "t"}) }
	check := func(name string, edit func(*EntityBinding), field string) {
		t.Helper()
		b := base()
		edit(&b)
		if _, ok := ValidateBinding(b, ConnSQL)[field]; !ok {
			t.Errorf("%s: no problem at %s", name, field)
		}
	}
	check("entity case", func(b *EntityBinding) { b.Entity = "Customer" }, "entity")
	check("entity empty", func(b *EntityBinding) { b.Entity = "" }, "entity")
	check("no keys", func(b *EntityBinding) { b.KeyFields = nil }, "keyFields")
	check("six keys", func(b *EntityBinding) { b.KeyFields = []string{"a", "b", "c", "d", "e", "f"} }, "keyFields")
	check("repeated key", func(b *EntityBinding) { b.KeyFields = []string{"a", "a"} }, "keyFields")
	check("cursor type without field", func(b *EntityBinding) { b.CursorType = CursorInt }, "cursorField")
	check("cursor field without type", func(b *EntityBinding) { b.CursorField = "upd" }, "cursorType")
	check("bad cursor type", func(b *EntityBinding) { b.CursorField, b.CursorType = "upd", "float" }, "cursorType")
	check("cursor not a column", func(b *EntityBinding) { b.CursorField, b.CursorType = "u pd", CursorInt }, "source")
	b := base()
	b.CursorField, b.CursorType = "upd", CursorTimestamp
	if bad := ValidateBinding(b, ConnSQL); len(bad) > 0 {
		t.Fatalf("valid cursor refused: %v", bad)
	}
}

func TestSourceKey(t *testing.T) {
	row := map[string]any{"a": "x", "n": 42, "obj": map[string]any{"id": "7"}, "empty": " ", "arr": []any{1}}
	if k, err := SourceKey(row, []string{"a", "n", "obj.id"}); err != nil || k != "x|42|7" {
		t.Fatalf("%q %v", k, err)
	}
	for _, f := range []string{"missing", "empty", "arr"} {
		if _, err := SourceKey(row, []string{"a", f}); err == nil {
			t.Errorf("key field %s accepted", f)
		}
	}
}

func TestCursors_NormalizeCompareAndMax(t *testing.T) {
	ts := time.Date(2026, 3, 1, 12, 0, 0, 500, time.FixedZone("x", 3600))
	for _, c := range []struct {
		typ  string
		in   any
		want string
		ok   bool
	}{
		{CursorTimestamp, ts, "2026-03-01T11:00:00.000000500Z", true},
		{CursorTimestamp, "2026-03-01 10:00:00", "2026-03-01T10:00:00.000000000Z", true},
		{CursorTimestamp, "2026-03-01T10:00:00+02:00", "2026-03-01T08:00:00.000000000Z", true},
		{CursorTimestamp, "yesterday", "", false},
		{CursorInt, int64(5), "5", true},
		{CursorInt, 5.0, "5", true},
		{CursorInt, 5.5, "", false},
		{CursorInt, " 12 ", "12", true},
		{CursorRowversion, []byte{0, 0, 0, 0, 0, 1, 0xa0, 0x0f}, "000000000001a00f", true},
		{CursorRowversion, "0x00AB", "00ab", true},
		{CursorRowversion, "xyz", "", false},
	} {
		got, ok := NormalizeCursor(c.typ, c.in)
		if got != c.want || ok != c.ok {
			t.Errorf("%s %v = %q %v", c.typ, c.in, got, ok)
		}
	}
	if CompareCursor(CursorInt, "9", "10") >= 0 || CompareCursor(CursorTimestamp, "2026-03-01T00:00:00.000000000Z", "2026-03-01T00:00:00.000000001Z") >= 0 {
		t.Error("compare order")
	}
	if MaxCursor(CursorInt, "9", "10") != "10" || MaxCursor(CursorInt, "", "3") != "3" || MaxCursor(CursorInt, "3", "") != "3" {
		t.Error("max")
	}
}

func TestSameOrigin(t *testing.T) {
	for next, want := range map[string]bool{
		"https://erp.example/api?page=2":    true,
		"https://erp.example:443/api":       false, // an explicit port is another origin string; the service follows only exact matches
		"http://erp.example/api":            false,
		"https://evil.example/api":          false,
		"https://user@erp.example/api":      false,
		"https://erp.example.evil.test/api": false,
		"//erp.example/api":                 false,
	} {
		if got := SameOrigin("https://erp.example", next); got != want {
			t.Errorf("%s = %v", next, got)
		}
	}
}
