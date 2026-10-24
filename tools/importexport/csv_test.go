package importexport

import (
	"strings"
	"testing"
)

func note() Entity {
	return Entity{ID: "platform.note", Fields: []Field{{Name: "title", Required: true}, {Name: "body"}}}
}

func TestExportImportRoundTrip(t *testing.T) {
	rows := []map[string]string{{"title": "Hello", "body": "World"}}
	raw, err := ExportCSV(note(), rows)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(raw), "title,body") {
		t.Fatalf("header: %s", raw)
	}
	got, err := ImportCSV(note(), raw)
	if err != nil || len(got.Rows) != 1 || got.Rows[0]["title"] != "Hello" || len(got.Errors) != 0 {
		t.Fatalf("import %+v %v", got, err)
	}
}

func TestImportMissingRequiredAndColumn(t *testing.T) {
	got, err := ImportCSV(note(), []byte("title,body\n,x\n"))
	if err != nil || len(got.Errors) != 1 {
		t.Fatalf("required: %+v %v", got, err)
	}
	if _, err := ImportCSV(note(), []byte("body\nonly\n")); err == nil {
		t.Fatal("missing title column")
	}
}
