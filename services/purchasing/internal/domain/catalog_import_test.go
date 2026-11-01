package domain

import (
	"strings"
	"testing"
)

func TestParseImportCSV_HeadersAliasesAndBlankLines(t *testing.T) {
	text := "\xef\xbb\xbfVendor Part Number,Product name,Category,UOM,Price,Currency,Lead time,Ignored\n" +
		"G-1,Gloves,MRO,PR,4.25,USD,3,x\n\n" +
		"T-1,Tape,MRO,RL,1.10,usd,,\n"
	rows, err := ParseImportCSV(text)
	if err != nil || len(rows) != 2 {
		t.Fatalf("rows = %v, %v", rows, err)
	}
	if rows[0][ColVendorPartNo] != "G-1" || rows[0][ColUnit] != "PR" || rows[0][ColUnitPrice] != "4.25" || rows[0][ColLeadTime] != "3" {
		t.Errorf("row 1 = %v", rows[0])
	}
	if _, ok := rows[0]["Ignored"]; ok || rows[1][ColLeadTime] != "" {
		t.Errorf("unused column kept or blank not blank: %v", rows)
	}
}

func TestParseImportCSV_FileLevelRefusals(t *testing.T) {
	var big strings.Builder
	big.WriteString("name,category,unit,price,currency\n")
	for i := 0; i < MaxImportRows+1; i++ {
		big.WriteString("a,b,c,1,USD\n")
	}
	for _, c := range []struct{ name, text, code string }{
		{"empty", "", CodeImportRow},
		{"no unit column", "name,category,price,currency\na,b,1,USD\n", CodeImportRow},
		{"header only", "name,category,unit,price,currency\n", CodeImportRow},
		{"too many rows", big.String(), CodeImportTooBig},
	} {
		_, err := ParseImportCSV(c.text)
		if e, ok := err.(*Error); !ok || e.Code != c.code {
			t.Errorf("%s: %v, want %s", c.name, err, c.code)
		}
	}
	atLimit := strings.Replace(big.String(), "a,b,c,1,USD\n", "", 1)
	if rows, err := ParseImportCSV(atLimit); err != nil || len(rows) != MaxImportRows {
		t.Errorf("exactly %d rows: %d, %v", MaxImportRows, len(rows), err)
	}
}

func TestFlagDuplicates_FileAndCatalog(t *testing.T) {
	rows := []ImportRow{
		{RowNo: 1, Status: RowAcceptable, Raw: map[string]string{ColVendorPartNo: "A-1"}},
		{RowNo: 2, Status: RowAcceptable, Raw: map[string]string{ColVendorPartNo: "a-1"}},
		{RowNo: 3, Status: RowAcceptable, Raw: map[string]string{ColVendorPartNo: "B-2"}},
		{RowNo: 4, Status: RowAcceptable, Raw: map[string]string{}},
		{RowNo: 5, Status: RowAcceptable, Raw: map[string]string{}},
		{RowNo: 6, Status: RowError, Raw: map[string]string{ColVendorPartNo: "C-3"}},
		{RowNo: 7, Status: RowAcceptable, Raw: map[string]string{ColVendorPartNo: "C-3"}},
	}
	FlagDuplicates(rows, map[string]bool{"b-2": true})
	want := []string{RowAcceptable, RowError, RowError, RowAcceptable, RowAcceptable, RowError, RowAcceptable}
	for i, r := range rows {
		if r.Status != want[i] {
			t.Errorf("row %d = %s (%s), want %s", r.RowNo, r.Status, r.Reason, want[i])
		}
	}
	if !strings.Contains(rows[1].Reason, "repeats row 1") || !strings.Contains(rows[2].Reason, "already an entry") {
		t.Errorf("reasons: %q, %q", rows[1].Reason, rows[2].Reason)
	}
}

func TestDecideAndImportStatus(t *testing.T) {
	for _, c := range []struct{ status, decision, want, code string }{
		{RowAcceptable, "ACCEPT", RowAccepted, ""},
		{RowRejected, "ACCEPT", RowAccepted, ""},
		{RowAccepted, "REJECT", RowRejected, ""},
		{RowError, "ACCEPT", "", CodeImportRow},
		{RowError, "REJECT", RowError, ""},
		{RowAcceptable, "MAYBE", "", CodeValidation},
	} {
		got, err := Decide(3, c.status, c.decision)
		code := ""
		if e, ok := err.(*Error); ok {
			code = e.Code
		}
		if got != c.want || code != c.code {
			t.Errorf("%s %s = %q %v, want %q %s", c.status, c.decision, got, err, c.want, c.code)
		}
	}
	if s, err := NextImportStatus(ImportStaged, ImportCommit); s != ImportCommitted || err != nil {
		t.Errorf("commit = %s, %v", s, err)
	}
	if s, err := NextImportStatus(ImportStaged, ImportCancel); s != ImportCancelled || err != nil {
		t.Errorf("cancel = %s, %v", s, err)
	}
	for _, st := range []string{ImportCommitted, ImportCancelled} {
		if _, err := NextImportStatus(st, ImportCommit); err == nil || err.(*Error).Code != CodeImportState {
			t.Errorf("commit a %s import: %v", st, err)
		}
	}
}
