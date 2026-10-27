package domain

import (
	"testing"

	"github.com/shopspring/decimal"
	"pgregory.net/rapid"
)

func TestValidateJournal_Table(t *testing.T) {
	site := Dimensions{DimSite: "S1"}
	ok := JournalLine{LineNo: 1, ProductID: "p1", Dims: site, Qty: d("2"), EnteredQty: d("2"), CostPrice: d("4")}
	with := func(mod func(*JournalLine)) []JournalLine {
		l := ok
		mod(&l)
		return []JournalLine{l}
	}
	cases := []struct {
		name string
		j    Journal
		code string
	}{
		{"movement ok", Journal{Type: JournalMovement, Currency: "USD", OffsetAccountID: "500100", Lines: []JournalLine{ok}}, ""},
		{"unsupported type", Journal{Type: "TRANSFER", Currency: "USD", Lines: []JournalLine{ok}}, CodeInvalid},
		{"bad currency", Journal{Type: JournalAdjustment, Currency: "US", Lines: []JournalLine{ok}}, CodeInvalid},
		{"no lines", Journal{Type: JournalAdjustment, Currency: "USD"}, CodeInvalid},
		{"movement without offset", Journal{Type: JournalMovement, Currency: "USD", Lines: []JournalLine{ok}}, CodeOffsetRequired},
		{"adjustment needs no offset", Journal{Type: JournalAdjustment, Currency: "USD", Lines: []JournalLine{ok}}, ""},
		{"adjustment rejects an offset", Journal{Type: JournalAdjustment, Currency: "USD", OffsetAccountID: "500100", Lines: []JournalLine{ok}}, CodeInvalid},
		{"zero qty", Journal{Type: JournalAdjustment, Currency: "USD", Lines: with(func(l *JournalLine) { l.Qty, l.EnteredQty = d("0"), d("0") })}, CodeInvalid},
		{"too many decimals", Journal{Type: JournalAdjustment, Currency: "USD", Lines: with(func(l *JournalLine) { l.Qty, l.EnteredQty = d("1.0000001"), d("1") })}, CodeUOMPrecision},
		{"entered qty on the other side", Journal{Type: JournalAdjustment, Currency: "USD", Lines: with(func(l *JournalLine) { l.EnteredQty = d("-2") })}, CodeInvalid},
		{"negative cost", Journal{Type: JournalAdjustment, Currency: "USD", Lines: with(func(l *JournalLine) { l.CostPrice = d("-1") })}, CodeInvalid},
		{"cost beyond six decimals", Journal{Type: JournalAdjustment, Currency: "USD", Lines: with(func(l *JournalLine) { l.CostPrice = d("0.0000001") })}, CodeInvalid},
		{"site is mandatory", Journal{Type: JournalAdjustment, Currency: "USD", Lines: with(func(l *JournalLine) { l.Dims = Dimensions{DimWarehouse: "W1"} })}, CodeDimensionMandatory},
		{"unknown dimension", Journal{Type: JournalAdjustment, Currency: "USD", Lines: with(func(l *JournalLine) { l.Dims = Dimensions{DimSite: "S1", "colour": "RED"} })}, CodeInvalid},
		{"license plate reserved", Journal{Type: JournalAdjustment, Currency: "USD", Lines: with(func(l *JournalLine) { l.Dims = Dimensions{DimSite: "S1", DimLicensePlate: "LP1"} })}, CodeInvalid},
		{"vendor-owned stock is never valued", Journal{Type: JournalAdjustment, Currency: "USD", Lines: with(func(l *JournalLine) { l.Dims = Dimensions{DimSite: "S1", DimOwner: "V1"} })}, CodeOwnershipValue},
		{"serial qty must be one", Journal{Type: JournalAdjustment, Currency: "USD", Lines: with(func(l *JournalLine) { l.Dims = Dimensions{DimSite: "S1", DimSerial: "S-1"} })}, CodeSerialQty},
		{"serial qty one ok", Journal{Type: JournalAdjustment, Currency: "USD", Lines: with(func(l *JournalLine) {
			l.Dims, l.Qty, l.EnteredQty = Dimensions{DimSite: "S1", DimSerial: "S"}, d("-1"), d("-1")
		})}, ""},
		{"no product", Journal{Type: JournalAdjustment, Currency: "USD", Lines: with(func(l *JournalLine) { l.ProductID = "" })}, CodeInvalid},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := ValidateJournal(tc.j, DefaultJournalRules)
			if tc.code == "" {
				if err != nil {
					t.Fatalf("want ok, got %v", err)
				}
				return
			}
			de, isDomain := err.(*Error)
			if !isDomain || de.Code != tc.code {
				t.Fatalf("want code %s, got %v", tc.code, err)
			}
		})
	}
}

func TestValidateJournal_MaxLines(t *testing.T) {
	lines := make([]JournalLine, 3)
	for i := range lines {
		lines[i] = JournalLine{LineNo: i + 1, ProductID: "p", Dims: Dimensions{DimSite: "S1"}, Qty: d("1"), EnteredQty: d("1")}
	}
	err := ValidateJournal(Journal{Type: JournalAdjustment, Currency: "USD", Lines: lines}, JournalRules{MaxLines: 2, MaxScale: 6})
	de, _ := err.(*Error)
	if de == nil || de.Code != CodeJournalMax {
		t.Fatalf("want %s, got %v", CodeJournalMax, err)
	}
}

func TestJournalLine_SameContent(t *testing.T) {
	a := JournalLine{ProductID: "p", Dims: Dimensions{DimSite: "S1", DimBatch: " "}, Qty: d("2"), EnteredQty: d("2"), EnteredUnit: "EA", CostPrice: d("1.50")}
	b := JournalLine{ProductID: "p", Dims: Dimensions{DimSite: "S1"}, Qty: d("2.0"), EnteredQty: d("2"), EnteredUnit: "EA", CostPrice: d("1.5")}
	if !a.SameContent(b) {
		t.Fatal("equal values with blank dimensions and trailing zeros must match")
	}
	b.Qty = d("3")
	if a.SameContent(b) {
		t.Fatal("different quantity must not match")
	}
}

func TestCanMoveJournal(t *testing.T) {
	cases := []struct {
		from, to JournalStatus
		want     bool
	}{
		{JournalDraft, JournalSubmitted, true},
		{JournalSubmitted, JournalApproved, true},
		{JournalApproved, JournalPosting, true},
		{JournalPosting, JournalPosted, true},
		{JournalSubmitted, JournalRejected, true},
		{JournalRejected, JournalDraft, true},
		{JournalDraft, JournalCancelled, true},
		{JournalPosted, JournalDraft, false},
		{JournalDraft, JournalPosted, false},
		{JournalApproved, JournalCancelled, false},
	}
	for _, c := range cases {
		if got := CanMoveJournal(c.from, c.to); got != c.want {
			t.Errorf("%s -> %s: got %v want %v", c.from, c.to, got, c.want)
		}
	}
}

func TestPlanPosting_SignDeterminesStatus(t *testing.T) {
	plan := PlanPosting(Journal{Lines: []JournalLine{
		{ProductID: "p", Qty: d("5"), CostPrice: d("2")},
		{ProductID: "p", Qty: d("-3"), CostPrice: d("2")},
	}})
	if plan[0].Status != StatusPurchased || plan[1].Status != StatusSold {
		t.Errorf("statuses %s, %s; want PURCHASED, SOLD", plan[0].Status, plan[1].Status)
	}
}

func TestReceiptValue_RoundsToMinorUnit(t *testing.T) {
	if got := ReceiptValue(d("3"), d("0.333"), "USD"); !got.Equal(d("1")) {
		t.Errorf("3 x 0.333 USD = %s, want 1.00", got)
	}
	if got := ReceiptValue(d("1"), d("0.0005"), "KWD"); !got.Equal(d("0.001")) {
		t.Errorf("KWD keeps three decimals: %s", got)
	}
}

func TestBuildEntry_SplitsReceiptsAndIssues(t *testing.T) {
	plan := []PostedLine{{Value: d("10")}, {Value: d("-4")}, {Value: d("0")}}
	e, err := BuildEntry(plan, "1300", "9000", "USD")
	if err != nil {
		t.Fatal(err)
	}
	if len(e.Lines) != 4 || !e.Balanced() {
		t.Fatalf("entry %+v", e)
	}
	for _, l := range e.Lines {
		if l.Debit.Sign() != 0 && l.Credit.Sign() != 0 {
			t.Fatalf("line carries both a debit and a credit: %+v", l)
		}
	}
	if !e.Net("1300").Equal(d("6")) || !e.Net("9000").Equal(d("-6")) {
		t.Fatalf("nets %s / %s", e.Net("1300"), e.Net("9000"))
	}
	empty, _ := BuildEntry([]PostedLine{{Value: d("0")}}, "1300", "9000", "USD")
	if len(empty.Lines) != 0 {
		t.Fatalf("zero-value journal must produce no GL lines: %+v", empty)
	}
}

// TestBuildEntry_BalancedPerJournalType is AC3: every journal type produces a
// balanced GL entry, whatever mix of receipts and issues it carries, and the
// inventory account moves by exactly the change in posted value.
func TestBuildEntry_BalancedPerJournalType(t *testing.T) {
	for _, jt := range JournalTypes {
		t.Run(string(jt), func(t *testing.T) {
			rapid.Check(t, func(t *rapid.T) {
				n := rapid.IntRange(1, 50).Draw(t, "lines")
				lines := make([]JournalLine, n)
				for i := range lines {
					q := rapid.Int64Range(-500, 500).Draw(t, "qty")
					if q == 0 {
						q = 1
					}
					c := rapid.Int64Range(0, 1_000_000).Draw(t, "cost")
					lines[i] = JournalLine{LineNo: i + 1, ProductID: "p", Qty: decimal.New(q, -1), CostPrice: decimal.New(c, -4)}
				}
				plan := PlanPosting(Journal{Type: jt, Lines: lines})
				row := OnHand{PostedQty: d("100"), PostedValue: d("333.33")}
				net := decimal.Zero
				for i := range plan {
					l := plan[i].Line
					if l.Qty.Sign() > 0 {
						plan[i].Value = ReceiptValue(l.Qty, l.CostPrice, "USD")
					} else {
						plan[i].Value = row.IssueValue(l.Qty, l.CostPrice, "USD")
					}
					row.PostedQty = row.PostedQty.Add(l.Qty)
					row.PostedValue = row.PostedValue.Add(plan[i].Value)
					net = net.Add(plan[i].Value)
				}
				entry, err := BuildEntry(plan, "1300", "9000", "USD")
				if err != nil {
					t.Fatal(err)
				}
				if !entry.Balanced() {
					t.Fatalf("unbalanced entry: %+v", entry)
				}
				if !entry.Net("1300").Equal(net) {
					t.Fatalf("inventory net %s != posted value change %s", entry.Net("1300"), net)
				}
			})
		})
	}
}

func TestBuildEntry_RejectsBadInput(t *testing.T) {
	if _, err := BuildEntry(nil, "1300", "9000", "US"); err == nil {
		t.Fatal("want error for malformed currency")
	}
	if _, err := BuildEntry(nil, "1300", "", "USD"); err == nil {
		t.Fatal("want error for a missing offset account")
	}
}
