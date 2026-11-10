package domain

import "testing"

// BR-49.7: RND-001 allows 0.01 per document, so a trial-balance tuple summing n documents is
// EXPECTED up to n x 0.01; beyond that, or without a document count, it is UNEXPLAINED.
func TestRND001_ScalesWithTheTuplesDocuments(t *testing.T) {
	docs := []ReplayDocument{
		{Seq: 1, DocumentID: "D1", PostingDate: "2026-03-02", Currency: "USD", Lines: []ReplayLine{{Account: "6000", Amount: d("10.004")}, {Account: "2000", Amount: d("-10.004")}}},
		{Seq: 2, DocumentID: "D2", PostingDate: "2026-03-03", Currency: "USD", Lines: []ReplayLine{{Account: "6000", Amount: d("20.004")}, {Account: "2000", Amount: d("-20.004")}}},
		{Seq: 3, DocumentID: "D3", PostingDate: "2026-03-04", Currency: "USD", Lines: []ReplayLine{{Account: "6000", Amount: d("30.004")}, {Account: "6000", Amount: d("1")}, {Account: "2000", Amount: d("-31.004")}}},
	}
	counts := DocumentCounts(docs, "2026-03")
	if counts["6000|USD|2026-03"] != 3 || counts["2000|USD|2026-03"] != 3 {
		t.Fatalf("a document counts once per account, however many lines it has there: %v", counts)
	}
	legacy := TBLines(TrialBalanceFromDocuments(docs), "2026-03", "legacy", counts)
	// The ledger rounds every line to cents: 6000 holds 61.00 and 2000 holds -61.00, legacy 61.012.
	passage := []Line{{Area: "TB", Key: "6000|USD|2026-03", Passage: d("61")}, {Area: "TB", Key: "2000|USD|2026-03", Passage: d("-61")}}
	vs := Diff(legacy, passage, DefaultRules(), Explanations{})
	if len(vs) != 2 {
		t.Fatalf("variances %+v", vs)
	}
	for _, v := range vs {
		if v.Class != ClassExpected || v.RuleID != RuleRND001 || v.Documents != 3 || !v.Delta.Abs().Equal(d("0.012")) {
			t.Fatalf("0.012 over 3 documents must be EXPECTED by RND-001: %+v", v)
		}
	}
	// The same delta on a tuple without a document count is one document's worth: UNEXPLAINED.
	bare := TBLines(TrialBalanceFromDocuments(docs), "2026-03", "legacy", nil)
	for _, v := range Diff(bare, passage, DefaultRules(), Explanations{}) {
		if v.Class != ClassUnexplained {
			t.Fatalf("without a document count the tuple is one document: %+v", v)
		}
	}
	// More than 0.01 per document stays UNEXPLAINED (0.032 over 3 documents).
	var legacy6000 []Line
	for _, l := range legacy {
		if l.Key == "6000|USD|2026-03" {
			legacy6000 = append(legacy6000, l)
		}
	}
	far := []Line{{Area: "TB", Key: "6000|USD|2026-03", Passage: d("60.98")}}
	if v := Diff(legacy6000, far, DefaultRules(), Explanations{}); len(v) != 1 || v[0].Class != ClassUnexplained {
		t.Fatalf("0.032 over 3 documents: %+v", v)
	}
	// A rule that is not per document keeps its flat limit whatever the count.
	flat := []Rule{{ID: "FLAT", MaxAbs: d("0.01")}}
	if v := Diff(legacy, passage, flat, Explanations{}); v[0].Class != ClassUnexplained {
		t.Fatalf("flat rule: %+v", v[0])
	}
	// The count is part of the input hash only when given.
	in := ParallelInput{Period: "2026-03", SourceSystem: SourceAX2012, Legacy: bare, Passage: passage}
	with := in
	with.Legacy = legacy
	if in.Hash() == with.Hash() {
		t.Fatal("the document count must change the input hash")
	}
	if err := (ParallelInput{Period: "2026-03", Legacy: []Line{{Area: "TB", Key: "k", Documents: -1}}}).Validate(); err == nil {
		t.Fatal("a negative document count must be refused")
	}
}
