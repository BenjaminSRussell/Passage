package store

import (
	"testing"

	"github.com/shopspring/decimal"
)

func TestCreateRequestHash_CatchWeightOnlyWhenSet(t *testing.T) {
	in := CreateTransactionInput{LegalEntityID: "le", SourceType: "PURCH_LINE", SourceID: "s", ProductID: "p", Qty: decimal.NewFromInt(5),
		EnteredQty: decimal.NewFromInt(5), EnteredUnit: "EA", Status: "ORDERED", Currency: "USD", CostPrice: decimal.NewFromInt(2)}
	legacy := fingerprint("CREATE", "le", "PURCH_LINE", "s", "", "p", "null", "5", "5", "EA", "ORDERED", "USD", "2", requestedDate(in.Date))
	if got := createRequestHash(in); got != legacy {
		t.Fatalf("a non-catch-weight create must keep its pre-catch-weight fingerprint: %s vs %s", got, legacy)
	}
	in.CWQty = 3
	if createRequestHash(in) == legacy {
		t.Fatal("a catch-weight create must hash differently")
	}
}
