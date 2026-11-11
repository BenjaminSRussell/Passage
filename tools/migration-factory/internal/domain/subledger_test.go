package domain

import (
	"strings"
	"testing"

	"github.com/shopspring/decimal"
)

func sl(seq int, id string, area Area, kind, date, key, amount string) SubledgerDocument {
	return SubledgerDocument{Seq: seq, DocumentID: id, Area: area, Kind: kind, PostingDate: date, Key: key, Currency: "USD", Amount: decimal.RequireFromString(amount)}
}

func TestValidateSubledger(t *testing.T) {
	good := []SubledgerDocument{
		sl(1, "I1", AreaAR, "INVOICE", "2026-05-02", "C1", "100.50"),
		sl(2, "P1", AreaAR, "PAYMENT", "2026-05-03", "C1", "-40"),
		sl(3, "R1", AreaINV, "RECEIPT", "2026-05-04", "ITEM@WH1", "12.345678"),
		sl(4, "D1", AreaFA, "DEPRECIATION", "2026-05-31", "FA-9", "-5"),
		sl(5, "A1", AreaAP, "ADJUSTMENT", "2026-05-31", "V1", "-1"),
	}
	if err := ValidateSubledger("2026-05", good); err != nil {
		t.Fatal(err)
	}
	bad := map[string]SubledgerDocument{
		"blank id":       sl(9, " ", AreaAR, "INVOICE", "2026-05-02", "C1", "1"),
		"repeated id":    sl(9, "I1", AreaAR, "INVOICE", "2026-05-02", "C1", "1"),
		"repeated seq":   sl(1, "X", AreaAR, "INVOICE", "2026-05-02", "C1", "1"),
		"unknown area":   sl(9, "X", "GL", "INVOICE", "2026-05-02", "C1", "1"),
		"kind of area":   sl(9, "X", AreaINV, "INVOICE", "2026-05-02", "C1", "1"),
		"other period":   sl(9, "X", AreaAR, "INVOICE", "2026-06-02", "C1", "1"),
		"blank key":      sl(9, "X", AreaAR, "INVOICE", "2026-05-02", " ", "1"),
		"key with bar":   sl(9, "X", AreaAR, "INVOICE", "2026-05-02", "C|1", "1"),
		"zero amount":    sl(9, "X", AreaAR, "INVOICE", "2026-05-02", "C1", "0"),
		"7 decimals":     sl(9, "X", AreaAR, "INVOICE", "2026-05-02", "C1", "1.1234567"),
		"invoice sign":   sl(9, "X", AreaAR, "INVOICE", "2026-05-02", "C1", "-1"),
		"payment sign":   sl(9, "X", AreaAP, "PAYMENT", "2026-05-02", "V1", "1"),
		"currency":       {Seq: 9, DocumentID: "X", Area: AreaAR, Kind: "INVOICE", PostingDate: "2026-05-02", Key: "C1", Currency: "usd", Amount: decimal.NewFromInt(1)},
		"issue positive": sl(9, "X", AreaINV, "ISSUE", "2026-05-02", "I@W", "1"),
	}
	for name, d := range bad {
		if err := ValidateSubledger("2026-05", append(append([]SubledgerDocument(nil), good...), d)); err == nil {
			t.Errorf("%s: accepted", name)
		} else if !strings.Contains(err.Error(), "invalid input") {
			t.Errorf("%s: %v", name, err)
		}
	}
}

func TestOrderSubledgerAndKeys(t *testing.T) {
	docs := []SubledgerDocument{sl(2, "B", AreaAR, "INVOICE", "2026-05-02", "C1", "1"), sl(1, "A", AreaAR, "INVOICE", "2026-05-02", "C1", "1"), sl(3, "C", AreaAR, "INVOICE", "2026-05-01", "C1", "1")}
	got := OrderSubledger(docs)
	if got[0].DocumentID != "C" || got[1].DocumentID != "A" || got[2].DocumentID != "B" || docs[0].DocumentID != "B" {
		t.Fatalf("order %v", got)
	}
	// The same document id in two areas is two documents with two keys.
	if SubledgerReplayKey(SourceAX2012, "2026-05", AreaAR, "X") == SubledgerReplayKey(SourceAX2012, "2026-05", AreaAP, "X") {
		t.Fatal("keys must differ per area")
	}
	if SubledgerReplayKey(SourceAX2012, "2026-05", AreaAR, "X") == ReplayKey(SourceAX2012, "2026-05", "X") {
		t.Fatal("a subledger key must not collide with a journal key")
	}
}

func TestSubledgerLinesNormalizeLegacyAndPassageSides(t *testing.T) {
	docs := []SubledgerDocument{
		sl(1, "I1", AreaAR, "INVOICE", "2026-05-02", "C1", "100"),
		sl(2, "P1", AreaAR, "PAYMENT", "2026-05-03", "C1", "-40"),
		sl(3, "I2", AreaAP, "INVOICE", "2026-05-03", "V1", "7.5"),
	}
	legacy := SubledgerLines(SubledgerFromDocuments(docs), "2026-05", "legacy", SubledgerDocumentCounts(docs, "2026-05"))
	if len(legacy) != 2 || legacy[0].Area != AreaAP || legacy[1].Area != AreaAR {
		t.Fatalf("lines %+v", legacy)
	}
	ar := legacy[1]
	if ar.Key != "C1|USD|2026-05" || !ar.Legacy.Equal(decimal.NewFromInt(60)) || ar.Documents != 2 {
		t.Fatalf("AR line %+v", ar)
	}
	passage := SubledgerLines([]SubledgerBalance{{Area: AreaAR, Key: "C1", Currency: "USD", Net: decimal.NewFromInt(60)}}, "2026-05", "passage", nil)
	vs := Diff(legacy, passage, DefaultRules(), nil)
	// AR matches; AP is missing at Passage and is unexplained.
	unexplained := 0
	for _, v := range vs {
		if v.Class == ClassUnexplained {
			unexplained++
			if v.Area != AreaAP {
				t.Fatalf("unexplained %+v", v)
			}
		}
	}
	if unexplained != 1 {
		t.Fatalf("variances %+v", vs)
	}
}
