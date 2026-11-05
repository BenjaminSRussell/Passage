package store

import (
	"testing"

	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/services/purchasing/internal/domain"
)

// Only a change that comes from inbound planning may move the quantity of a line ordered from a requisition line.
func TestCheckLineChanges_RequisitionLinkedQuantityIsForPlanningOnly(t *testing.T) {
	cur := []PurchaseOrderLine{{LineNo: 1, Qty: decimal.NewFromInt(100), Unit: "EA", RequisitionLineID: "r1", Status: domain.POLineOpen}}
	next := []PurchaseOrderLine{{LineNo: 1, Qty: decimal.NewFromInt(80), Unit: "EA", RequisitionLineID: "r1"}}
	if _, err := checkLineChanges(cur, next, false, false); err == nil {
		t.Fatal("a buyer's quantity change on a requisition-linked line was accepted")
	}
	if _, err := checkLineChanges(cur, next, false, true); err != nil {
		t.Fatalf("a planning change = %v", err)
	}
	next[0].Unit = "BOX"
	if _, err := checkLineChanges(cur, next, false, false); err == nil {
		t.Fatal("a unit change on a requisition-linked line was accepted")
	}
}
