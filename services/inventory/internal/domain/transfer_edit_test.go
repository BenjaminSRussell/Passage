package domain

import (
	"testing"
	"time"

	"github.com/shopspring/decimal"
)

func editOrder() TransferOrder {
	d := time.Date(2026, 11, 2, 0, 0, 0, 0, time.UTC)
	return TransferOrder{ID: "o1", Number: "TO-1", Status: TransferCreated, FromWarehouse: "MAIN", ToWarehouse: "EAST", UseTransit: true,
		TransitWarehouse: "TRANSIT", ShipDate: d, PlannedReceipt: d.AddDate(0, 0, 3), Lines: []TransferLine{
			{ID: "l1", LineNo: 1, ProductID: "p1", Dims: Dimensions{DimSite: "S1"}, Unit: "EA", Qty: decimal.NewFromInt(5)},
			{ID: "l2", LineNo: 2, ProductID: "p2", Dims: Dimensions{DimSite: "S1"}, Unit: "EA", Qty: decimal.NewFromInt(7)},
		}}
}

func TestApplyTransferEdit(t *testing.T) {
	q := decimal.NewFromInt(9)
	pct := decimal.NewFromInt(10)
	newShip := time.Date(2026, 11, 5, 0, 0, 0, 0, time.UTC)
	site := "S9"
	added := TransferLine{ProductID: "p3", Dims: Dimensions{DimSite: "S1"}, Unit: "EA", Qty: decimal.NewFromInt(2)}

	o, err := ApplyTransferEdit(editOrder(), TransferEdit{ShipDate: &newShip, ToSite: &site, Remove: []string{"l1"},
		Update: []LineEdit{{LineID: "l2", Qty: &q, OverPct: &pct}}, Add: []TransferLine{added}}, 6)
	if err != nil {
		t.Fatal(err)
	}
	if len(o.Lines) != 2 || o.Lines[0].ID != "l2" || !o.Lines[0].Qty.Equal(q) || !o.Lines[0].OverPct.Equal(pct) {
		t.Fatalf("lines %+v", o.Lines)
	}
	if o.Lines[1].ID != "" || o.Lines[1].LineNo != 3 || !o.ShipDate.Equal(newShip) || o.ToSite != "S9" {
		t.Fatalf("added line or header wrong: %+v", o)
	}
	if !o.PlannedReceipt.Equal(editOrder().PlannedReceipt) {
		t.Fatal("planned receipt must stay")
	}
}

func TestApplyTransferEditRefusals(t *testing.T) {
	zero := decimal.Zero
	before := time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC)
	shipped := editOrder()
	shipped.Status = TransferShipped
	partlyShipped := editOrder()
	partlyShipped.Lines[0].ShippedQty = decimal.NewFromInt(1)
	cases := []struct {
		name string
		o    TransferOrder
		e    TransferEdit
		code string
	}{
		{"shipped order", shipped, TransferEdit{Remove: []string{"l1"}}, CodeTransferState},
		{"shipped quantity", partlyShipped, TransferEdit{Remove: []string{"l1"}}, CodeTransferState},
		{"empty edit", editOrder(), TransferEdit{}, CodeInvalid},
		{"unknown line", editOrder(), TransferEdit{Remove: []string{"zz"}}, CodeTransferNotFound},
		{"unknown update", editOrder(), TransferEdit{Update: []LineEdit{{LineID: "zz", Qty: &zero}}}, CodeTransferNotFound},
		{"line named twice", editOrder(), TransferEdit{Remove: []string{"l1"}, Update: []LineEdit{{LineID: "l1", Qty: &zero}}}, CodeInvalid},
		{"last line removed", editOrder(), TransferEdit{Remove: []string{"l1", "l2"}}, CodeInvalid},
		{"zero quantity", editOrder(), TransferEdit{Update: []LineEdit{{LineID: "l1", Qty: &zero}}}, CodeInvalid},
		{"ship date after planned receipt moved before", editOrder(), TransferEdit{ShipDate: &before, PlannedReceipt: &before}, ""},
	}
	for _, c := range cases {
		_, err := ApplyTransferEdit(c.o, c.e, 6)
		if c.code == "" {
			// A planned receipt equal to the ship date is allowed.
			if err != nil {
				t.Errorf("%s: %v", c.name, err)
			}
			continue
		}
		de, ok := err.(*Error)
		if !ok || de.Code != c.code {
			t.Errorf("%s: got %v, want code %s", c.name, err, c.code)
		}
	}
}

func TestApplyTransferEditPlannedReceipt(t *testing.T) {
	late := time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC)
	if _, err := ApplyTransferEdit(editOrder(), TransferEdit{PlannedReceipt: &late}, 6); err == nil {
		t.Fatal("planned receipt before the ship date must be refused")
	}
	o, err := ApplyTransferEdit(editOrder(), TransferEdit{ClearPlannedReceipt: true}, 6)
	if err != nil || !o.PlannedReceipt.IsZero() {
		t.Fatalf("clear: %v %v", err, o.PlannedReceipt)
	}
}

func TestApplyTransferEditDoesNotReuseRemovedLineNumber(t *testing.T) {
	added := TransferLine{ProductID: "p3", Dims: Dimensions{DimSite: "S1"}, Unit: "EA", Qty: decimal.NewFromInt(2)}
	o, err := ApplyTransferEdit(editOrder(), TransferEdit{Remove: []string{"l2"}, Add: []TransferLine{added}}, 6)
	if err != nil {
		t.Fatal(err)
	}
	if len(o.Lines) != 2 || o.Lines[0].LineNo != 1 || o.Lines[1].LineNo != 3 {
		t.Fatalf("removing line 2 and adding one must number the new line 3: %+v", o.Lines)
	}
}
