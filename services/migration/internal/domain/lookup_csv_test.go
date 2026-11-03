package domain

import (
	"errors"
	"fmt"
	"reflect"
	"strings"
	"testing"
)

func csvErr(t *testing.T, kind, in string, header bool) string {
	t.Helper()
	_, err := ParseLookupCSV(kind, []byte(in), header)
	var de *Error
	if !errors.As(err, &de) || de.Code != CodeLookupInvalid {
		t.Fatalf("%q: want MIG-LOOKUP-INVALID, got %v", in, err)
	}
	return de.Detail
}

func TestParseLookupCSV_KeyPartsInCellsOrJoined(t *testing.T) {
	in := "from,to,factor\r\nLB,KG,0.45359237\nKG|LB,2.20462262185\n\n"
	res, err := ParseLookupCSV(LookupUoM, []byte("\xef\xbb\xbf"+in), true)
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]string{"LB|KG": "0.45359237", "KG|LB": "2.20462262185"}
	if !reflect.DeepEqual(res.Entries, want) || res.Rows != 2 {
		t.Fatalf("entries = %v rows %d", res.Entries, res.Rows)
	}
	price, err := ParseLookupCSV(LookupPrice, []byte("SKU-1, EUR ,12.50\nSKU-2,EUR,\"1,5\"\n"), false)
	if err == nil || !strings.Contains(err.Error(), "line 2") {
		t.Fatalf("a comma in a price must be refused with its line: %v %v", price, err)
	}
}

func TestParseLookupCSV_ReportsEveryProblemWithItsLine(t *testing.T) {
	detail := csvErr(t, LookupPrice, "SKU-1,EUR,12.50\nSKU-1,EUR,13\nSKU-2,-4\nonlyone\nSKU-3,EUR,USD,GBP,9\n", false)
	for _, want := range []string{`line 2: key "SKU-1|EUR" already appears on line 1`, "line 3:", "line 4: needs 2 to 4 cells", "line 5: needs 2 to 4 cells"} {
		if !strings.Contains(detail, want) {
			t.Errorf("detail %q lacks %q", detail, want)
		}
	}
	var b strings.Builder
	for i := 0; i < 30; i++ {
		fmt.Fprintf(&b, "K%d,-1\n", i)
	}
	if d := csvErr(t, LookupPrice, b.String(), false); !strings.HasSuffix(d, "and 20 more") {
		t.Errorf("issue cap = %q", d)
	}
}

func TestParseLookupCSV_RefusesEmptyBadKindAndBadText(t *testing.T) {
	csvErr(t, LookupEnum, "key,value\n", true)
	csvErr(t, LookupEnum, "", false)
	if d := csvErr(t, LookupEnum, "a,b\nc,\"unterminated\n", false); !strings.HasPrefix(d, "line 2: column") || strings.Contains(d, "parse error on line") {
		t.Errorf("malformed quote = %q", d)
	}
	csvErr(t, LookupEnum, "a,\xff\n", false)
	csvErr(t, LookupUoM, "KG,KG,1\n", false)
	csvErr(t, LookupUoM, "KG,LB,0\n", false)
	if _, err := ParseLookupCSV("map", []byte("a,b"), false); err == nil {
		t.Error("unknown kind accepted")
	}
	csvErr(t, LookupEnum, strings.Repeat("a,b\n", 1)+strings.Repeat("x", maxLookupCSVBytes), false)
}

func TestApplyLookupImport_MergeAndReplace(t *testing.T) {
	cur := map[string]string{"a": "1", "b": "2", "c": "3"}
	file := map[string]string{"b": "2", "c": "30", "d": "4"}
	merged, sum := ApplyLookupImport(cur, file, false)
	if len(merged) != 4 || merged["a"] != "1" || merged["c"] != "30" || sum != (LookupImportSummary{Rows: 3, Added: 1, Changed: 1, Unchanged: 1}) {
		t.Fatalf("merge = %v %+v", merged, sum)
	}
	replaced, sum := ApplyLookupImport(cur, file, true)
	if len(replaced) != 3 || replaced["a"] != "" || sum != (LookupImportSummary{Rows: 3, Added: 1, Changed: 1, Unchanged: 1, Removed: 1}) {
		t.Fatalf("replace = %v %+v", replaced, sum)
	}
	if cur["c"] != "3" {
		t.Error("the current entries were modified")
	}
}

func TestLookupCSV_RoundTripsAndNeutralisesFormulas(t *testing.T) {
	entries := map[string]string{"SKU-1|EUR": "12.50", "=cmd|x": "+5", "plain": "@home", "q,uote": "he said \"hi\"", "apos": "'=SUM(A1)", "apos2": "''-1", "note": "'quoted"}
	out := LookupCSV(entries)
	if !strings.Contains(string(out), "'=cmd,x,'+5") || !strings.Contains(string(out), "plain,'@home") || !strings.Contains(string(out), "apos,''=SUM(A1)") || !strings.Contains(string(out), "note,'quoted\n") {
		t.Fatalf("formula cells are not neutralised:\n%s", out)
	}
	back, err := ParseLookupCSV(LookupEnum, out, false)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(back.Entries, entries) {
		t.Fatalf("round trip = %v", back.Entries)
	}
	if string(LookupCSV(nil)) != "" {
		t.Error("empty table must export nothing")
	}
}
