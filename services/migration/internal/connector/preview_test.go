package connector_test

import (
	"context"
	"strings"
	"testing"

	"github.com/BenjaminSRussell/passage/services/migration/internal/connector"
	"github.com/BenjaminSRussell/passage/services/migration/internal/domain"
)

const customerMapping = `{"entity":"customer","version":1,"fields":[
 {"source":"KUNNR","target":"customerNumber","type":"string","required":true},
 {"source":"NAME1","target":"name","type":"string","required":true},
 {"source":"CREDIT","target":"creditLimit","type":"decimal","scale":2}],
 "tests":[{"name":"ok","input":{"KUNNR":" 1 ","NAME1":"A","CREDIT":"1"},"expect":{"customerNumber":"1"}}]}`

func TestPreview_ShowsTargetRecordsRejectsWarningsAndKeyProblems(t *testing.T) {
	m, err := domain.ParseMapping([]byte(customerMapping))
	if err != nil {
		t.Fatal(err)
	}
	csv := "KUNNR,NAME1,CREDIT\n 1001 ,Acme,1500.50\n1002,,10\n1003,Beta,abc\n1001,Dup,5\n,NoKey,1\n" + "1006,Long," + "7\n"
	spec := fileSpec(map[string]any{"dropDir": "x", "format": "csv"}, map[string]any{"file": "a.csv"})
	spec.KeyFields = []string{"KUNNR"}
	spec.Upload = &connector.Upload{Name: "cust.csv", Content: []byte(csv + "1007," + strings.Repeat("x", 800) + ",1\n")}

	res, err := connector.Preview(context.Background(), connector.Env{}, spec, &m, 5)
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Rows) != 5 || !res.More || res.Mapped != 2 || res.Rejected != 3 {
		t.Fatalf("counts: rows=%d more=%v mapped=%d rejected=%d", len(res.Rows), res.More, res.Mapped, res.Rejected)
	}
	first := res.Rows[0]
	if first.SourceKey != "1001" || first.Target["customerNumber"] != "1001" || first.Target["name"] != "Acme" || len(first.Rejects) != 0 {
		t.Fatalf("first row: %+v", first)
	}
	if r := res.Rows[1]; len(r.Rejects) == 0 || r.Target != nil || r.Rejects[0].Field != "name" {
		t.Fatalf("missing required name: %+v", r)
	}
	if r := res.Rows[2]; len(r.Rejects) == 0 || r.Rejects[0].Field != "creditLimit" {
		t.Fatalf("bad decimal: %+v", r)
	}
	if r := res.Rows[4]; len(r.Rejects) != 1 || r.Rejects[0].Code != "MIG-ROW-NO-KEY" || r.SourceKey != "" {
		t.Fatalf("row without a key: %+v", r)
	}
	if len(res.DuplicateKeys) != 1 || res.DuplicateKeys[0] != "1001" {
		t.Fatalf("duplicates: %v", res.DuplicateKeys)
	}
	if strings.Join(res.Columns, ",") != "CREDIT,KUNNR,NAME1" {
		t.Fatalf("columns: %v", res.Columns)
	}

	// Without a mapping only the source shows; long text is clipped; the limit is honoured.
	spec.Upload.Content = []byte(csv + "1007," + strings.Repeat("x", 800) + ",1\n")
	res, err = connector.Preview(context.Background(), connector.Env{}, spec, nil, 100)
	if err != nil || len(res.Rows) != 7 || res.More || res.Mapped != 0 || res.Rows[0].Target != nil {
		t.Fatalf("no mapping: %v rows=%d more=%v", err, len(res.Rows), res.More)
	}
	if got := res.Rows[6].Source["NAME1"].(string); len(got) > 600 || !strings.Contains(got, "800 bytes") {
		t.Fatalf("not clipped: %d", len(got))
	}
	if _, err := connector.Preview(context.Background(), connector.Env{}, fileSpec(nil, map[string]any{"file": "a.csv"}), nil, 5); err == nil {
		t.Fatal("a file profile without drop root or upload previewed")
	}
}
