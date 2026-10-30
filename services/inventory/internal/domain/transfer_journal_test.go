package domain

import "testing"

func TestTransferJournalValidation(t *testing.T) {
	a := Dimensions{DimSite: "S1", DimLocation: "A1"}
	b := Dimensions{DimSite: "S1", DimLocation: "B7"}
	mk := func(from, to Dimensions, qty string) JournalLine {
		return JournalLine{LineNo: 1, ProductID: "p1", Dims: from, ToDims: to, Qty: d(qty), EnteredQty: d(qty)}
	}
	cases := []struct {
		name  string
		lines []JournalLine
		code  string
	}{
		{"ok", []JournalLine{mk(a, b, "2")}, ""},
		{"same set", []JournalLine{mk(a, a, "2")}, CodeTransferLine},
		{"negative", []JournalLine{mk(a, b, "-2")}, CodeTransferLine},
		{"no destination site", []JournalLine{mk(a, Dimensions{DimLocation: "B"}, "2")}, CodeDimensionMandatory},
		{"batch changes", []JournalLine{mk(Dimensions{DimSite: "S1", DimBatch: "1"}, Dimensions{DimSite: "S1", DimBatch: "2"}, "2")}, CodeTransferLine},
		{"serial changes", []JournalLine{mk(Dimensions{DimSite: "S1", DimSerial: "SN1"}, Dimensions{DimSite: "S1", DimLocation: "B", DimSerial: "SN2"}, "1")}, CodeTransferLine},
		{"serial moves", []JournalLine{mk(Dimensions{DimSite: "S1", DimSerial: "SN1"}, Dimensions{DimSite: "S1", DimLocation: "B", DimSerial: "SN1"}, "1")}, ""},
		{"product dimension changes", []JournalLine{mk(Dimensions{DimSite: "S1", DimColor: "RED"}, Dimensions{DimSite: "S1", DimLocation: "B", DimColor: "BLUE"}, "2")}, CodeTransferLine},
		{"product dimension dropped", []JournalLine{mk(Dimensions{DimSite: "S1", DimConfig: "C1"}, Dimensions{DimSite: "S1", DimLocation: "B"}, "2")}, CodeTransferLine},
		{"segment changes", []JournalLine{mk(a, Dimensions{DimSite: "S1", DimLocation: "A1", DimSegment: "PROJECT:P1"}, "2")}, ""},
		{"quarantined source", []JournalLine{mk(Dimensions{DimSite: "S1", DimStatus: StockQuarantine}, b, "2")}, CodeTransferLine},
		{"blocked status", []JournalLine{mk(a, Dimensions{DimSite: "S1", DimStatus: StockBlocked}, "2")}, CodeTransferLine},
		{"vendor-owned destination", []JournalLine{mk(a, Dimensions{DimSite: "S1", DimOwner: "V"}, "2")}, CodeTransferLine},
		{"row is source and destination", []JournalLine{mk(a, b, "1"), mk(b, Dimensions{DimSite: "S1", DimLocation: "C"}, "1")}, CodeTransferLine},
		{"offset refused", nil, CodeInvalid},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			j := Journal{Type: JournalTransfer, Currency: "USD", Lines: tc.lines}
			if tc.name == "offset refused" {
				j.OffsetAccountID, j.Lines = "5001", []JournalLine{mk(a, b, "1")}
			}
			err := ValidateJournal(j, DefaultJournalRules)
			if tc.code == "" {
				if err != nil {
					t.Fatal(err)
				}
				return
			}
			if de, ok := err.(*Error); !ok || de.Code != tc.code {
				t.Fatalf("want %s, got %v", tc.code, err)
			}
		})
	}
}

func TestTransferPlanPairsIssueAndReceipt(t *testing.T) {
	a := Dimensions{DimSite: "S1", DimLocation: "A1"}
	b := Dimensions{DimSite: "S1", DimLocation: "B7"}
	j := Journal{Type: JournalTransfer, Lines: []JournalLine{{ProductID: "p", Dims: a, ToDims: b, Qty: d("3"), EnteredQty: d("3")}}}
	plan := PlanPosting(j)
	if len(plan) != 2 || plan[0].Status != StatusSold || !plan[0].Line.Qty.Equal(d("-3")) || plan[0].Line.Dims[DimLocation] != "A1" ||
		plan[1].Status != StatusPurchased || !plan[1].Line.Qty.Equal(d("3")) || plan[1].Line.Dims[DimLocation] != "B7" || !plan[1].Paired {
		t.Fatalf("plan %+v", plan)
	}
	if _, err := ReverseJournal(Journal{Type: JournalTransfer, Status: JournalPosted}, "x", j.PostingDate); err == nil {
		t.Fatal("a transfer was reversed")
	}
}
