package connectors_test

import (
	"context"
	"strings"
	"testing"
	"testing/fstest"

	"github.com/BenjaminSRussell/passage/tools/connectors"
)

func TestSpecs(t *testing.T) {
	want := map[string][]string{
		"ax2012":  {"DirParty", "CustTable", "VendTable", "InventTable", "EcoResProduct", "BOM", "Route"},
		"sap_ecc": {"KNA1", "LFA1", "MARA", "MAST", "STKO", "PLKO"},
	}
	got := map[string][]connectors.Entity{"ax2012": connectors.AX(), "sap_ecc": connectors.SAP()}
	for system, tables := range want {
		if len(got[system]) != len(tables) {
			t.Fatalf("%s: %d entities, want %d", system, len(got[system]), len(tables))
		}
		for i, e := range got[system] {
			columns := map[string]bool{}
			for _, c := range e.Columns {
				columns[c] = true
			}
			if e.Table != tables[i] || e.System != system || !e.Unverified || !columns[e.CursorColumn] || len(e.KeyColumns) == 0 {
				t.Errorf("%s %s: spec %+v", system, tables[i], e)
			}
			for _, k := range e.KeyColumns {
				if !columns[k] {
					t.Errorf("%s: key column %s is not extracted", e.Table, k)
				}
			}
		}
	}
}

func custTable(t *testing.T) connectors.Entity {
	for _, e := range connectors.AX() {
		if e.Table == "CustTable" {
			return e
		}
	}
	t.Fatal("CustTable spec missing")
	return connectors.Entity{}
}

const custHeader = "DataAreaId,AccountNum,Party,CustGroup,Currency,CreditMax,Blocked,ModifiedDateTime\n"

func TestReadCSV(t *testing.T) {
	tests := []struct {
		name      string
		input     string
		since     string
		wantKeys  []string
		wantError string
	}{
		{"two rows keyed by company and account", custHeader + "usmf,C1,1,10,USD,100.50,0,2024-01-01T00:00:00\nusmf,C2,2,20,USD,0,1,2024-02-01T00:00:00\n", "", []string{"usmf|C1", "usmf|C2"}, ""},
		{"extra columns ignored", "Extra," + custHeader + "x,usmf,C1,1,10,USD,1,0,2024-01-01T00:00:00\n", "", []string{"usmf|C1"}, ""},
		{"delta keeps only rows after the cursor", custHeader + "usmf,C1,1,10,USD,1,0,2024-01-01T00:00:00\nusmf,C2,2,10,USD,1,0,2024-03-01T00:00:00\n", "2024-02-01T00:00:00", []string{"usmf|C2"}, ""},
		{"header only is empty", custHeader, "", nil, ""},
		{"missing column", "DataAreaId,AccountNum\nusmf,C1\n", "", nil, "missing column"},
		{"short row names the line", custHeader + "usmf,C1,1,10,USD,1,0,2024-01-01T00:00:00\nusmf,C2\n", "", nil, "line 3"},
		{"blank key names the line", custHeader + "usmf,  ,1,10,USD,1,0,2024-01-01T00:00:00\n", "", nil, "line 2"},
		{"empty file", "", "", nil, "read header"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := connectors.ReadCSV(strings.NewReader(tc.input), custTable(t), tc.since)
			if tc.wantError != "" {
				if err == nil || !strings.Contains(err.Error(), tc.wantError) || got != nil {
					t.Fatalf("got %v, %v; want error containing %q and no partial rows", got, err, tc.wantError)
				}
				return
			}
			if err != nil || len(got) != len(tc.wantKeys) {
				t.Fatalf("got %v, %v", got, err)
			}
			for i, r := range got {
				if r.Key != tc.wantKeys[i] {
					t.Errorf("row %d key %q, want %q", i, r.Key, tc.wantKeys[i])
				}
			}
		})
	}
}

func TestFlatFiles_ReadsExtractNamedAfterTheTable(t *testing.T) {
	src := connectors.FlatFiles{FS: fstest.MapFS{"CustTable.csv": {Data: []byte(custHeader + "usmf,C1,1,10,USD,100.50,0,2024-01-01T00:00:00\n")}}}
	var source connectors.RowSource = src
	rows, err := source.Rows(context.Background(), custTable(t), "")
	if err != nil || len(rows) != 1 || rows[0].Fields["CreditMax"] != "100.50" {
		t.Fatalf("got %+v, %v", rows, err)
	}
	if _, err := source.Rows(context.Background(), connectors.AX()[2], ""); err == nil || !strings.Contains(err.Error(), "VendTable") {
		t.Fatalf("missing extract must name the table, got %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := source.Rows(ctx, custTable(t), ""); err == nil {
		t.Fatal("cancelled context must stop the read")
	}
}
