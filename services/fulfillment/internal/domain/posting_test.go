package domain

import (
	"errors"
	"testing"

	"github.com/BenjaminSRussell/passage/platform/money"
)

// TestPackingSlipPosting_Matrix covers the BR-28.1 rows that the packing-slip
// post decides. Every non-empty result must balance.
func TestPackingSlipPosting_Matrix(t *testing.T) {
	tests := []struct {
		name          string
		lines         []PostingLine
		cogsAtShip    bool
		wantAccounts  []string // accounts in order, debit then credit per line
		wantAmounts   []string
		wantNoEntries bool
	}{
		{
			name:         "stocked physical posting (AC-28.1)",
			lines:        []PostingLine{{OrderLineID: "l1", Kind: KindStocked, Qty: d("10"), UnitCost: d("12.50")}},
			wantAccounts: []string{"1405", "1400"},
			wantAmounts:  []string{"125.00", "-125.00"},
		},
		{
			name:         "stocked with cogs_at_shipment (PGI style)",
			lines:        []PostingLine{{OrderLineID: "l1", Kind: KindStocked, Qty: d("10"), UnitCost: d("12.50")}},
			cogsAtShip:   true,
			wantAccounts: []string{"5000", "1400"},
			wantAmounts:  []string{"125.00", "-125.00"},
		},
		{
			name:         "standard cost item posts at standard",
			lines:        []PostingLine{{OrderLineID: "l1", Kind: KindStocked, Qty: d("4"), UnitCost: d("3.333")}},
			wantAccounts: []string{"1405", "1400"},
			wantAmounts:  []string{"13.33", "-13.33"},
		},
		{
			name:          "drop-ship line posts nothing at slip time",
			lines:         []PostingLine{{OrderLineID: "l1", Kind: KindDropShip, Qty: d("5"), UnitCost: d("9")}},
			wantNoEntries: true,
		},
		{
			name:          "non-stocked service line posts nothing",
			lines:         []PostingLine{{OrderLineID: "l1", Kind: KindNonStocked, Qty: d("1"), UnitCost: d("100")}},
			wantNoEntries: true,
		},
		{
			name:          "zero cost posts nothing",
			lines:         []PostingLine{{OrderLineID: "l1", Kind: KindStocked, Qty: d("3"), UnitCost: d("0")}},
			wantNoEntries: true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := PackingSlipPosting(tt.lines, tt.cogsAtShip, "USD")
			if err != nil {
				t.Fatal(err)
			}
			if tt.wantNoEntries {
				if len(got) != 0 {
					t.Fatalf("want no entries, got %+v", got)
				}
				return
			}
			if len(got) != len(tt.wantAccounts) {
				t.Fatalf("entries = %d, want %d", len(got), len(tt.wantAccounts))
			}
			for i, e := range got {
				if e.Account != tt.wantAccounts[i] || e.Amount.Value.StringFixed(2) != tt.wantAmounts[i] {
					t.Errorf("entry %d = %s %s, want %s %s", i, e.Account, e.Amount.Value, tt.wantAccounts[i], tt.wantAmounts[i])
				}
			}
		})
	}
}

// TestPackingSlipCancellation_AC28_2: cancelling the slip posts the exact
// mirror, so the net on 1405 returns to zero.
func TestPackingSlipCancellation_AC28_2(t *testing.T) {
	post, err := PackingSlipPosting([]PostingLine{{OrderLineID: "l1", Kind: KindStocked, Qty: d("10"), UnitCost: d("12.50")}}, false, "USD")
	if err != nil {
		t.Fatal(err)
	}
	mirror := Reverse(post)
	if len(mirror) != len(post) {
		t.Fatalf("mirror has %d entries, want %d", len(mirror), len(post))
	}
	for i := range post {
		if mirror[i].Account != post[i].Account || mirror[i].OrderLineID != post[i].OrderLineID {
			t.Fatalf("mirror dimensions differ at %d", i)
		}
		if !mirror[i].Amount.Value.Equal(post[i].Amount.Value.Neg()) {
			t.Fatalf("mirror amount %d not negated", i)
		}
	}
	both := append(append([]JournalLine{}, post...), mirror...)
	net, err := Net(both, AccountDeliveredNotInvoiced, "USD")
	if err != nil {
		t.Fatal(err)
	}
	if !net.IsZero() {
		t.Fatalf("GL 1405 net after cancel = %s, want 0", net)
	}
}

func TestCheckBalancedRejectsImbalance(t *testing.T) {
	err := checkBalanced([]JournalLine{{Account: "1405", Amount: money.Must("10", "USD")}}, "USD")
	if !errors.Is(err, ErrUnbalanced) {
		t.Fatalf("err = %v, want ErrUnbalanced", err)
	}
}
