package connector_test

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/BenjaminSRussell/passage/services/migration/internal/connector"
	"github.com/BenjaminSRussell/passage/services/migration/internal/domain"
)

func fileSpec(profile map[string]any, source map[string]any) connector.Spec {
	return connector.Spec{Kind: domain.ConnFile, Profile: profile, Source: source, KeyFields: []string{"id"}}
}

func TestFile_DropDirectoryFormatsCodepageAndPaging(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "acme")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	write := func(name string, b []byte) {
		if err := os.WriteFile(filepath.Join(dir, name), b, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	// Windows-1252 bytes: "Müller" is 4D FC 6C 6C 65 72; the older drop must lose against the newer by name.
	write("cust_20260101.csv", []byte("id;name\n1;old\n"))
	write("cust_20260102.csv", append([]byte("id;name\n1;M"), append([]byte{0xFC}, []byte("ller\n2;Meyer\n\n3;\"Sch\xe4fer; Co\"\n")...)...))
	write("cust.json", []byte(`{"result":[{"id":7,"name":"Seven","changed":"2026-03-01T00:00:00Z"},{"id":8,"name":"Eight","changed":"2026-04-01T00:00:00Z"}]}`))
	write("cust.jsonl", []byte("{\"id\":1,\"name\":\"a\"}\n\n{\"id\":2,\"name\":\"b\"}\n"))
	write("bad.csv", []byte("id,name\n1,a,extra\n"))
	env := connector.Env{DropRoot: root}

	recs := extractAll(t, env, fileSpec(map[string]any{"dropDir": "acme", "format": "csv", "delimiter": ";", "codepage": "windows-1252"}, map[string]any{"filePattern": "cust_*.csv"}))
	if len(recs) != 3 || recs[0].Fields["name"] != "Müller" || recs[2].Fields["name"] != "Schäfer; Co" {
		t.Fatalf("csv: %+v", recs)
	}
	recs = extractAll(t, env, fileSpec(map[string]any{"dropDir": "acme", "format": "json"}, map[string]any{"file": "cust.json", "recordsPath": "result"}))
	if len(recs) != 2 || recs[0].Key != "7" {
		t.Fatalf("json: %+v", recs)
	}
	recs = extractAll(t, env, fileSpec(map[string]any{"dropDir": "acme", "format": "jsonl"}, map[string]any{"file": "cust.jsonl"}))
	if len(recs) != 2 {
		t.Fatalf("jsonl: %+v", recs)
	}

	o, err := env.Open(fileSpec(map[string]any{"dropDir": "acme", "format": "csv"}, map[string]any{"file": "bad.csv"}))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := o.Read(context.Background(), connector.Query{Limit: 10}); err == nil || !strings.Contains(err.Error(), "line 2 has 3 fields") {
		t.Fatalf("ragged csv: %v", err)
	}

	// Delta by cursor over a re-dropped file: only the newer row comes back and the watermark covers the file.
	spec := fileSpec(map[string]any{"dropDir": "acme", "format": "json"}, map[string]any{"file": "cust.json", "recordsPath": "result"})
	spec.CursorField, spec.CursorType = "changed", domain.CursorTimestamp
	o, _ = env.Open(spec)
	p, err := o.Read(context.Background(), connector.Query{Limit: 10, Since: "2026-03-01T00:00:00.000000000Z"})
	if err != nil || len(p.Rows) != 1 || p.Rows[0]["id"].(interface{ String() string }).String() != "8" || p.Watermark != "2026-04-01T00:00:00.000000000Z" {
		t.Fatalf("delta: %v %+v", err, p)
	}
}

func TestFile_PathsStayInsideTheDropRootAndNeedOne(t *testing.T) {
	root := t.TempDir()
	outside := t.TempDir()
	if err := os.WriteFile(filepath.Join(outside, "secret.csv"), []byte("id\n1\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(root, "d"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(outside, "secret.csv"), filepath.Join(root, "d", "link.csv")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(root, "esc")); err != nil {
		t.Fatal(err)
	}
	read := func(env connector.Env, profile, source map[string]any) error {
		o, err := env.Open(fileSpec(profile, source))
		if err != nil {
			return err
		}
		_, err = o.Read(context.Background(), connector.Query{Limit: 10})
		return err
	}
	env := connector.Env{DropRoot: root}
	for name, c := range map[string]struct{ profile, source map[string]any }{
		"symlinked file": {map[string]any{"dropDir": "d", "format": "csv"}, map[string]any{"file": "link.csv"}},
		"symlinked dir":  {map[string]any{"dropDir": "esc", "format": "csv"}, map[string]any{"filePattern": "*.csv"}},
		"dotdot dir":     {map[string]any{"dropDir": "../" + filepath.Base(outside), "format": "csv"}, map[string]any{"filePattern": "*.csv"}},
	} {
		if err := read(env, c.profile, c.source); err == nil {
			t.Errorf("%s was read", name)
		}
	}
	if _, err := (connector.Env{}).Open(fileSpec(map[string]any{"dropDir": "d", "format": "csv"}, map[string]any{"file": "a.csv"})); err == nil || !strings.Contains(err.Error(), "MIG_DROP_ROOT") {
		t.Fatalf("no drop root: %v", err)
	}
	if _, err := env.Open(fileSpec(map[string]any{"dropDir": "d", "format": "csv"}, map[string]any{"file": "../x.csv"})); err == nil {
		t.Fatal("a file name with a path opened")
	}
}

func TestFile_UploadAndKindMismatch(t *testing.T) {
	env := connector.Env{}
	spec := fileSpec(map[string]any{"dropDir": "x"}, map[string]any{"file": "a.csv"})
	spec.Upload = &connector.Upload{Name: "people.csv", Content: []byte("\xef\xbb\xbfid,name\n1,Ann\n2,Bob\n")}
	recs := extractAll(t, env, spec)
	if len(recs) != 2 || recs[1].Fields["name"] != "Bob" {
		t.Fatalf("upload: %+v", recs)
	}
	spec.Upload = &connector.Upload{Name: "big.csv", Content: make([]byte, connector.MaxUploadBytes+1)}
	if _, err := env.Open(spec); err == nil {
		t.Fatal("oversized upload opened")
	}
	rest := restSpec("https://a.example", map[string]any{"path": "/x"}, "", nil)
	rest.Upload = &connector.Upload{Name: "a.csv", Content: []byte("id\n1\n")}
	if _, err := env.Open(rest); err != connector.ErrNoUpload {
		t.Fatalf("upload to rest: %v", err)
	}
}
