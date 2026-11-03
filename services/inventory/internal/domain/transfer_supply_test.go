package domain

import (
	"testing"

	"github.com/shopspring/decimal"
)

func TestTransferSupplyFollowsShipAndReceive(t *testing.T) {
	d := decimal.RequireFromString
	o := TransferOrder{Status: TransferCreated}
	l := TransferLine{Qty: d("10")}
	if s := o.Supply(l); !s.OnOrder.Equal(d("10")) || !s.Ordered.Equal(d("10")) {
		t.Fatalf("created: %+v", s)
	}
	o.Status = TransferPartial
	l.ShippedQty, l.ReceivedQty = d("10"), d("4")
	if s := o.Supply(l); !s.OnOrder.IsZero() || !s.Ordered.Equal(d("6")) {
		t.Fatalf("shipped 10 received 4: %+v", s)
	}
	l.ShippedQty, l.ReceivedQty = d("11"), d("12") // within tolerance
	if s := o.Supply(l); !s.OnOrder.IsZero() || !s.Ordered.IsZero() {
		t.Fatalf("over: %+v", s)
	}
	l = TransferLine{Qty: d("10"), ShippedQty: d("10"), ReceivedQty: d("7"), Closed: true}
	if s := o.Supply(l); !s.Ordered.IsZero() {
		t.Fatalf("closed short expects nothing: %+v", s)
	}
}

func TestTransferSupplyNoneOutsideActiveStatuses(t *testing.T) {
	l := TransferLine{Qty: decimal.NewFromInt(5)}
	for _, st := range []string{TransferDraft, TransferCancelled, TransferReceived, TransferDeleted} {
		if s := (TransferOrder{Status: st}).Supply(l); !s.OnOrder.IsZero() || !s.Ordered.IsZero() {
			t.Fatalf("%s holds supply: %+v", st, s)
		}
	}
}
