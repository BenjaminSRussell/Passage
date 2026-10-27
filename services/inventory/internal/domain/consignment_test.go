package domain

import (
	"testing"
	"time"
)

func consignment() ConsignmentOrder {
	return ConsignmentOrder{Number: "CRO-1", VendorID: "V1", Site: "S1", Warehouse: "W1", Currency: "USD", Status: ConsignmentCreated,
		RequestedDate: time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC),
		Lines: []ConsignmentLine{
			{ID: "l1", LineNo: 1, ProductID: "p1", Dims: Dimensions{DimBatch: "B1"}, Unit: "EA", Qty: d("500"), Price: d("4")},
			{ID: "l2", LineNo: 2, ProductID: "p2", Dims: Dimensions{}, Unit: "EA", Qty: d("10"), Price: d("0")},
		}}
}

func TestValidateNewConsignment(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*ConsignmentOrder)
		code   string
	}{
		{"valid", func(*ConsignmentOrder) {}, ""},
		{"vendor required", func(o *ConsignmentOrder) { o.VendorID = " " }, CodeInvalid},
		{"site required", func(o *ConsignmentOrder) { o.Site = "" }, CodeInvalid},
		{"warehouse required", func(o *ConsignmentOrder) { o.Warehouse = "" }, CodeInvalid},
		{"currency", func(o *ConsignmentOrder) { o.Currency = "US" }, CodeInvalid},
		{"no lines", func(o *ConsignmentOrder) { o.Lines = nil }, CodeInvalid},
		{"quantity positive", func(o *ConsignmentOrder) { o.Lines[0].Qty = d("0") }, CodeUOMPrecision},
		{"quantity precision", func(o *ConsignmentOrder) { o.Lines[0].Qty = d("1.0000001") }, CodeUOMPrecision},
		{"negative price", func(o *ConsignmentOrder) { o.Lines[0].Price = d("-1") }, CodeInvalid},
		{"owner comes from the vendor", func(o *ConsignmentOrder) { o.Lines[0].Dims = Dimensions{DimOwner: "V2"} }, CodeInvalid},
		{"warehouse comes from the order", func(o *ConsignmentOrder) { o.Lines[0].Dims = Dimensions{DimWarehouse: "W2"} }, CodeInvalid},
		{"no blocked receipts", func(o *ConsignmentOrder) { o.Lines[0].Dims = Dimensions{DimStatus: StockBlocked} }, CodeInvalid},
		{"no segment", func(o *ConsignmentOrder) { o.Lines[0].Dims = Dimensions{DimSegment: "PROJECT:P1"} }, CodeInvalid},
		{"serial takes quantity 1", func(o *ConsignmentOrder) { o.Lines[0].Dims = Dimensions{DimSerial: "SN1"} }, CodeSerialQty},
		{"serial with 1", func(o *ConsignmentOrder) { o.Lines[0].Dims, o.Lines[0].Qty = Dimensions{DimSerial: "SN1"}, d("1") }, ""},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			o := consignment()
			tc.mutate(&o)
			if got := codeOf(ValidateNewConsignment(o, 6)); got != tc.code {
				t.Fatalf("code %q, want %q", got, tc.code)
			}
		})
	}
}

func TestConsignmentLineDimsAreVendorOwnedAtTheOrderWarehouse(t *testing.T) {
	o := consignment()
	got, err := o.LineDims(o.Lines[0]).Normalize()
	if err != nil {
		t.Fatal(err)
	}
	if got[DimOwner] != "V1" || got[DimSite] != "S1" || got[DimWarehouse] != "W1" || got[DimBatch] != "B1" || len(got) != 4 {
		t.Fatalf("line dims %v", got)
	}
	if o.Lines[0].Dims[DimOwner] != "" {
		t.Fatal("LineDims must not modify the line")
	}
}

func TestApplyConsignmentReceiptsIsQuantityOnly(t *testing.T) {
	o := consignment()
	next, plan, err := ApplyConsignmentReceipts(o, []ConsignmentReceipt{{LineID: "l1", Qty: d("120")}})
	if err != nil {
		t.Fatal(err)
	}
	if next.Status != ConsignmentPartial || !next.Lines[0].ReceivedQty.Equal(d("120")) || !o.Lines[0].ReceivedQty.IsZero() {
		t.Fatalf("status %s received %s (input %s)", next.Status, next.Lines[0].ReceivedQty, o.Lines[0].ReceivedQty)
	}
	if len(plan) != 1 || plan[0].Status != StatusReceived || !plan[0].Value.IsZero() || !plan[0].Line.Qty.Equal(d("120")) ||
		plan[0].Line.Dims[DimOwner] != "V1" || plan[0].Line.EnteredUnit != "EA" {
		t.Fatalf("plan %+v, want one RECEIVED line of 120 at value 0 owned by V1", plan)
	}
	next, _, err = ApplyConsignmentReceipts(next, []ConsignmentReceipt{{LineID: "l1", Qty: d("380")}, {LineID: "l2", Qty: d("10")}})
	if err != nil || next.Status != ConsignmentReceived {
		t.Fatalf("status %s err %v, want RECEIVED", next.Status, err)
	}
	if _, _, err := ApplyConsignmentReceipts(next, []ConsignmentReceipt{{LineID: "l1", Qty: d("1")}}); codeOf(err) != CodeConsignmentState {
		t.Fatalf("receipt on a received order: %v, want %s", err, CodeConsignmentState)
	}
}

func TestApplyConsignmentReceiptsRejects(t *testing.T) {
	tests := []struct {
		name     string
		receipts []ConsignmentReceipt
		code     string
	}{
		{"none", nil, CodeInvalid},
		{"unknown line", []ConsignmentReceipt{{LineID: "nope", Qty: d("1")}}, CodeConsignmentNotFound},
		{"zero", []ConsignmentReceipt{{LineID: "l1", Qty: d("0")}}, CodeInvalid},
		{"over the ordered quantity", []ConsignmentReceipt{{LineID: "l2", Qty: d("10.5")}}, CodeConsignmentOver},
		{"over across two receipts of one line", []ConsignmentReceipt{{LineID: "l2", Qty: d("6")}, {LineID: "l2", Qty: d("5")}}, CodeConsignmentOver},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			o := consignment()
			got, plan, err := ApplyConsignmentReceipts(o, tc.receipts)
			if codeOf(err) != tc.code || plan != nil || got.Status != ConsignmentCreated {
				t.Fatalf("err %v plan %v status %s; want %s, nothing planned, order unchanged", err, plan, got.Status, tc.code)
			}
		})
	}
}

func ownershipJournal(lines ...JournalLine) Journal {
	return Journal{Type: JournalOwnershipChange, Currency: "USD", Lines: lines}
}

func ownLine(owner, qty, price string) JournalLine {
	return JournalLine{LineNo: 1, ProductID: "p1", Dims: Dimensions{DimSite: "S1", DimWarehouse: "W1", DimOwner: owner},
		Qty: d(qty), EnteredQty: d(qty), EnteredUnit: "EA", CostPrice: d(price)}
}

func TestValidateOwnershipChangeLines(t *testing.T) {
	tests := []struct {
		name string
		j    Journal
		code string
	}{
		{"valid", ownershipJournal(ownLine("V1", "120", "4")), ""},
		{"source must be vendor-owned", ownershipJournal(ownLine("", "120", "4")), CodeInvalid},
		{"quantity is positive", ownershipJournal(ownLine("V1", "-120", "4")), CodeInvalid},
		{"one vendor per journal", ownershipJournal(ownLine("V1", "1", "4"), ownLine("V2", "1", "4")), CodeInvalid},
		{"offset comes from the posting profile", Journal{Type: JournalOwnershipChange, Currency: "USD", OffsetAccountID: "2190",
			Lines: []JournalLine{ownLine("V1", "1", "4")}}, CodeInvalid},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := codeOf(ValidateJournal(tc.j, DefaultJournalRules)); got != tc.code {
				t.Fatalf("code %q, want %q", got, tc.code)
			}
		})
	}
}

// AC-16.12: 120 EA at consignment price 4.00 leave the vendor at no value and
// arrive at the legal entity at 480.00, Dr inventory / Cr consignment accrual.
func TestOwnershipChangePlansAPairAndABalancedAccrualEntry(t *testing.T) {
	j := ownershipJournal(ownLine("V1", "120", "4.00"))
	plan := PlanPosting(j)
	if len(plan) != 2 {
		t.Fatalf("plan has %d lines, want an issue and a receipt", len(plan))
	}
	issue, receipt := plan[0], plan[1]
	if issue.Status != StatusDeducted || !issue.Line.Qty.Equal(d("-120")) || issue.Line.Dims[DimOwner] != "V1" || !issue.Value.IsZero() || issue.Paired {
		t.Fatalf("issue leg %+v, want DEDUCTED -120 from V1 at 0", issue)
	}
	if receipt.Status != StatusPurchased || !receipt.Line.Qty.Equal(d("120")) || VendorOwned(receipt.Line.Dims) || !receipt.Paired ||
		receipt.Line.Dims[DimWarehouse] != "W1" {
		t.Fatalf("receipt leg %+v, want PURCHASED 120 at W1 without owner", receipt)
	}
	if j.Lines[0].Dims[DimOwner] != "V1" {
		t.Fatal("planning must not modify the journal line")
	}
	// The posting values the receipt at the consignment price.
	receipt.Value = ReceiptValue(receipt.Line.Qty, receipt.Line.CostPrice, "USD")
	entry, err := BuildEntry([]PostedLine{issue, receipt}, "1300", "2190", "USD")
	if err != nil {
		t.Fatal(err)
	}
	if !entry.Balanced() || !entry.Net("1300").Equal(d("480")) || !entry.Net("2190").Equal(d("-480")) || len(entry.Lines) != 2 {
		t.Fatalf("entry %+v, want Dr 1300 480.00 / Cr 2190 480.00", entry)
	}
	if OwnershipVendor(j) != "V1" {
		t.Fatalf("vendor %q, want V1", OwnershipVendor(j))
	}
}

func TestOwnershipChangeCannotBeReversed(t *testing.T) {
	j := ownershipJournal(ownLine("V1", "1", "4"))
	j.Status = JournalPosted
	if _, err := ReverseJournal(j, "rev", time.Now()); codeOf(err) != CodeJournalNotReady {
		t.Fatalf("err %v, want %s", err, CodeJournalNotReady)
	}
}

func TestWithOwner(t *testing.T) {
	base := Dimensions{DimSite: "S1", DimOwner: "V1"}
	if got := WithOwner(base, ""); VendorOwned(got) || got[DimSite] != "S1" || base[DimOwner] != "V1" {
		t.Fatalf("clearing the owner: %v (input %v)", got, base)
	}
	if got := WithOwner(Dimensions{DimSite: "S1"}, " V2 "); got[DimOwner] != "V2" {
		t.Fatalf("setting the owner: %v", got)
	}
	// Company-owned stock keeps the hash it had before owners existed.
	a, _ := WithOwner(base, "").Normalize()
	b, _ := Dimensions{DimSite: "S1"}.Normalize()
	if a.Canonical() != b.Canonical() {
		t.Fatalf("legal-entity stock %q must hash like %q", a.Canonical(), b.Canonical())
	}
}
