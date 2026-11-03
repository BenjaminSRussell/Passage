package domain

import (
	"strings"
	"testing"

	"github.com/shopspring/decimal"
)

// ProblemCode is the code of a domain error, empty for nil or another error.
func problemCodeOf(err error) string {
	if de, ok := err.(*Error); ok {
		return de.Code
	}
	return ""
}

func TestDraftIsEditableConfirmableAndDeletable(t *testing.T) {
	o := editOrder()
	o.Status = TransferDraft
	if err := CanConfirm(o); err != nil {
		t.Fatal(err)
	}
	if err := CanDelete(o); err != nil {
		t.Fatal(err)
	}
	terms, mode := "FCA", "ROAD"
	next, err := ApplyTransferEdit(o, TransferEdit{DeliveryTerms: &terms, ModeOfDelivery: &mode}, 6)
	if err != nil || next.Status != TransferDraft || next.DeliveryTerms != "FCA" || next.ModeOfDelivery != "ROAD" {
		t.Fatalf("edit a draft: %v %+v", err, next)
	}
	clear := ""
	next, err = ApplyTransferEdit(next, TransferEdit{DeliveryTerms: &clear}, 6)
	if err != nil || next.DeliveryTerms != "" || next.ModeOfDelivery != "ROAD" {
		t.Fatalf("clear terms: %v %+v", err, next)
	}
	accented := strings.Repeat("É", MaxDeliveryCode) // 20 characters, 40 bytes: allowed
	if _, err := ApplyTransferEdit(o, TransferEdit{DeliveryTerms: &accented}, 6); err != nil {
		t.Fatalf("20 accented characters: %v", err)
	}
	long := strings.Repeat("X", MaxDeliveryCode+1)
	if _, err := ApplyTransferEdit(o, TransferEdit{ModeOfDelivery: &long}, 6); problemCodeOf(err) != CodeInvalid {
		t.Fatalf("long mode: %v", err)
	}
	if err := CanShip(o); problemCodeOf(err) != CodeTransferState {
		t.Fatalf("ship a draft: %v", err)
	}
	if err := CanCancel(o); problemCodeOf(err) != CodeTransferState {
		t.Fatalf("cancel a draft: %v", err)
	}
}

func TestDeleteAndConfirmRefusals(t *testing.T) {
	created := editOrder()
	if err := CanConfirm(created); problemCodeOf(err) != CodeTransferState {
		t.Fatalf("confirm a CREATED order: %v", err)
	}
	if err := CanDelete(created); err != nil {
		t.Fatal(err)
	}
	shipped := editOrder()
	shipped.Status = TransferShipped
	shipped.Lines[0].ShippedQty = decimal.NewFromInt(5)
	if err := CanDelete(shipped); problemCodeOf(err) != CodeTransferState {
		t.Fatalf("delete in transit: %v, want %s", err, CodeTransferState)
	}
	for _, st := range []string{TransferPartial, TransferReceived, TransferCancelled} {
		o := editOrder()
		o.Status = st
		if err := CanDelete(o); problemCodeOf(err) != CodeTransferState {
			t.Fatalf("delete %s: %v", st, err)
		}
	}
	odd := editOrder()
	odd.Lines[0].ShippedQty = decimal.NewFromInt(1)
	if err := CanDelete(odd); problemCodeOf(err) != CodeTransferState {
		t.Fatalf("delete with shipped quantity: %v", err)
	}
}
