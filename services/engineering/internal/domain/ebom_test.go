package domain

import (
	"strconv"
	"testing"

	"github.com/shopspring/decimal"
)

func TestImportEbom_UnknownPartsBecomeExceptionsAndKnownLinesDraftABom(t *testing.T) {
	payload := EbomPayload{PartNumber: "P", Revision: "A", Lines: make([]EbomLine, 0, 120)}
	known := map[string]string{"P": "P"}
	for i := 1; i <= 120; i++ {
		part := "C" + strconv.Itoa(i)
		payload.Lines = append(payload.Lines, EbomLine{FindNumber: i * 10, PartNumber: part, Quantity: decimal.NewFromInt(1), Unit: "EA"})
		if i <= 115 {
			known[part] = part
		}
	}
	result := ImportEbom(payload, known, nil)
	if result.Draft == nil || len(result.Draft.Lines) != 115 {
		t.Fatalf("draft lines = %d; want 115", len(result.Draft.Lines))
	}
	if result.ECR == nil || result.ECR.State != EcrDraft {
		t.Fatalf("ECR = %+v", result.ECR)
	}
	if len(result.Exceptions) != 5 {
		t.Fatalf("exceptions = %d; want 5", len(result.Exceptions))
	}
	for _, ex := range result.Exceptions {
		if ex.Code != CodeEbomPartUnknown {
			t.Fatalf("exception = %+v", ex)
		}
	}
}

func TestImportEbom_ComparisonCounts(t *testing.T) {
	payload := EbomPayload{PartNumber: "P", Revision: "B", Lines: []EbomLine{
		{FindNumber: 10, PartNumber: "A", Quantity: decimal.NewFromInt(2)},
		{FindNumber: 20, PartNumber: "B", Quantity: decimal.NewFromInt(1)},
		{FindNumber: 30, PartNumber: "C", Quantity: decimal.NewFromInt(1)},
	}}
	known := map[string]string{"P": "P", "A": "A", "B": "B", "C": "C"}
	active := &BomVersion{ProductID: "P", Status: StatusActive, Lines: []BomLine{
		{LineNo: 10, ComponentProductID: "A", Quantity: decimal.NewFromInt(1)},
		{LineNo: 20, ComponentProductID: "B", Quantity: decimal.NewFromInt(1)},
		{LineNo: 40, ComponentProductID: "D", Quantity: decimal.NewFromInt(1)},
	}}
	result := ImportEbom(payload, known, active)
	if result.Comparison.Added != 1 || result.Comparison.Removed != 1 || result.Comparison.Changed != 1 {
		t.Fatalf("comparison = %+v; want added 1 (C), removed 1 (D), changed 1 (A qty)", result.Comparison)
	}
}

func TestImportEbom_ReleasedRevisionIsRejected(t *testing.T) {
	existing := EbomImport{Source: "windchill", PartNumber: "P", Revision: "A", Status: EbomReleased}
	if err := existing.CanReimport(); !HasCode(err, CodeEbomRevisionReleased) {
		t.Fatalf("got %v; want %s", err, CodeEbomRevisionReleased)
	}
	draft := EbomImport{Status: EbomDraft}
	if err := draft.CanReimport(); err != nil {
		t.Fatalf("draft reimport = %v", err)
	}
}
