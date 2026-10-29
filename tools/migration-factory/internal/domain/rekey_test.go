package domain

import (
	"encoding/csv"
	"errors"
	"strings"
	"testing"
	"time"

	"pgregory.net/rapid"
)

var rekeyT0 = time.Date(2027, 1, 1, 0, 0, 0, 0, time.UTC)

func rekeyPlan() Plan {
	return Plan{PlanID: "p", T0: rekeyT0, Entities: []string{"USMF", "DEMF"}}
}

func jl(ref, account, amount string, at time.Time) RekeyPosting {
	return RekeyPosting{Kind: KindJournal, Entity: "USMF", DocumentRef: ref, PostedAt: at, Account: account, Currency: "USD", Amount: d(amount)}
}

func TestBuildRekeyExport_SplitsFilesAndExcludesPostingsBeforeT0(t *testing.T) {
	day1 := rekeyT0.Add(26 * time.Hour)
	postings := []RekeyPosting{
		jl("JV-2", "1100", "250.00", day1.Add(time.Hour)),
		jl("JV-2", "4000", "-250.00", day1.Add(time.Hour)),
		{Kind: KindDocument, Entity: "USMF", DocumentRef: "SI-9", PostedAt: day1, Account: "1200", Currency: "USD", Amount: d("125.50"), Text: "Invoice, Contoso"},
		jl("JV-1", "6100", "80.00", day1),
		jl("JV-1", "2000", "-80.00", day1),
		// Posted in legacy before T-0: not re-keyed.
		jl("JV-0", "6100", "10", rekeyT0.Add(-time.Minute)),
		jl("JV-0", "2000", "-10", rekeyT0.Add(-time.Minute)),
	}
	out, err := BuildRekeyExport(rekeyPlan(), postings)
	if err != nil {
		t.Fatal(err)
	}
	if got := out.Summary(); got != (RekeySummary{JournalLines: 4, DocumentLines: 1, Excluded: 2}) {
		t.Fatalf("summary %+v", got)
	}
	// JV-1 was posted first, so it comes first; lines keep their order and are numbered per document.
	var refs []string
	for _, l := range out.Journals {
		refs = append(refs, l.DocumentRef+"/"+l.Account+"/"+string(rune('0'+l.Line)))
	}
	if strings.Join(refs, " ") != "JV-1/6100/1 JV-1/2000/2 JV-2/1100/1 JV-2/4000/2" {
		t.Fatalf("journal order %v", refs)
	}
	if out.Documents[0].DocumentRef != "SI-9" || out.Documents[0].Line != 1 {
		t.Fatalf("documents %+v", out.Documents)
	}
}

func TestBuildRekeyExport_PostingAtT0IsExported(t *testing.T) {
	out, err := BuildRekeyExport(rekeyPlan(), []RekeyPosting{jl("JV-1", "1100", "1", rekeyT0), jl("JV-1", "2000", "-1", rekeyT0)})
	if err != nil {
		t.Fatal(err)
	}
	if out.Excluded != 0 || len(out.Journals) != 2 {
		t.Fatalf("a posting at exactly T-0 is made in Passage: %+v", out.Summary())
	}
}

func TestBuildRekeyExport_Rejects(t *testing.T) {
	after := rekeyT0.Add(time.Hour)
	ok := func() RekeyPosting {
		return RekeyPosting{Kind: KindDocument, Entity: "USMF", DocumentRef: "SI-1", PostedAt: after, Account: "1200", Currency: "USD", Amount: d("10")}
	}
	cases := []struct {
		name   string
		mutate func(*RekeyPosting)
		want   string
	}{
		{"unknown kind", func(p *RekeyPosting) { p.Kind = "TRANSFER" }, "JOURNAL or DOCUMENT"},
		{"blank entity", func(p *RekeyPosting) { p.Entity = " " }, "entity, documentRef and account"},
		{"blank document", func(p *RekeyPosting) { p.DocumentRef = "" }, "entity, documentRef and account"},
		{"blank account", func(p *RekeyPosting) { p.Account = "" }, "entity, documentRef and account"},
		{"entity outside the plan", func(p *RekeyPosting) { p.Entity = "GBSI" }, "does not cover"},
		{"lower-case currency", func(p *RekeyPosting) { p.Currency = "usd" }, "ISO 4217"},
		{"unknown currency", func(p *RekeyPosting) { p.Currency = "XYZ" }, "ISO 4217"},
		{"no posting time", func(p *RekeyPosting) { p.PostedAt = time.Time{} }, "postedAt"},
		{"seven decimal places", func(p *RekeyPosting) { p.Amount = d("0.0000001") }, "6 decimal places"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			p := ok()
			c.mutate(&p)
			_, err := BuildRekeyExport(rekeyPlan(), []RekeyPosting{ok(), p})
			if !errors.Is(err, ErrInvalid) || !strings.Contains(err.Error(), c.want) {
				t.Fatalf("err = %v, want ErrInvalid mentioning %q", err, c.want)
			}
		})
	}
}

func TestBuildRekeyExport_UnbalancedJournalIsRejected(t *testing.T) {
	after := rekeyT0.Add(time.Hour)
	_, err := BuildRekeyExport(rekeyPlan(), []RekeyPosting{jl("JV-1", "1100", "100.00", after), jl("JV-1", "4000", "-99.99", after)})
	if !errors.Is(err, ErrInvalid) || !strings.Contains(err.Error(), "JV-1") || !strings.Contains(err.Error(), "0.010000") {
		t.Fatalf("err = %v, want the unbalanced journal and its difference", err)
	}
	// The same lines as single-sided documents are accepted: documents do not balance.
	a, b := jl("SI-1", "1200", "100.00", after), jl("SI-1", "4000", "-99.99", after)
	a.Kind, b.Kind = KindDocument, KindDocument
	if _, err := BuildRekeyExport(rekeyPlan(), []RekeyPosting{a, b}); err != nil {
		t.Fatalf("documents: %v", err)
	}
	// An unbalanced journal dated before T-0 is not exported, so it is not checked either.
	if _, err := BuildRekeyExport(rekeyPlan(), []RekeyPosting{jl("JV-0", "1100", "1", rekeyT0.Add(-time.Hour))}); err != nil {
		t.Fatalf("excluded journal: %v", err)
	}
}

func TestRenderRekeyCSV_JournalsSplitDebitAndCreditAndQuoteText(t *testing.T) {
	at := time.Date(2027, 1, 2, 9, 30, 0, 0, time.FixedZone("EST", -5*3600))
	lines := []RekeyPosting{
		{Kind: KindJournal, Entity: "USMF", DocumentRef: "JV-1", Line: 1, PostedAt: at, Account: "6100", Dimensions: "DEPT=SALES", Currency: "USD", Amount: d("80"), Text: `Travel, "Q1"`},
		{Kind: KindJournal, Entity: "USMF", DocumentRef: "JV-1", Line: 2, PostedAt: at, Account: "2000", Currency: "USD", Amount: d("-80")},
	}
	body, err := RenderRekeyCSV(KindJournal, lines)
	if err != nil {
		t.Fatal(err)
	}
	rows, err := csv.NewReader(strings.NewReader(string(body))).ReadAll()
	if err != nil {
		t.Fatalf("the file must parse as CSV: %v\n%s", err, body)
	}
	if strings.Join(rows[0], ",") != strings.Join(JournalHeader, ",") {
		t.Fatalf("header %v", rows[0])
	}
	want := [][]string{
		{"USMF", "JV-1", "1", "2027-01-02T14:30:00Z", "6100", "DEPT=SALES", "USD", "80.000000", "", `Travel, "Q1"`},
		{"USMF", "JV-1", "2", "2027-01-02T14:30:00Z", "2000", "", "USD", "", "80.000000", ""},
	}
	for i, w := range want {
		if strings.Join(rows[i+1], "|") != strings.Join(w, "|") {
			t.Fatalf("row %d = %q, want %q", i+1, rows[i+1], w)
		}
	}
	doc, err := RenderRekeyCSV(KindDocument, []RekeyPosting{{Entity: "USMF", DocumentRef: "SI-9", Line: 1, PostedAt: at, Account: "1200", Currency: "USD", Amount: d("-12.5")}})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(string(doc), strings.Join(DocumentHeader, ",")+"\n") || !strings.Contains(string(doc), ",USD,-12.500000,") {
		t.Fatalf("document file %q", doc)
	}
	empty, err := RenderRekeyCSV(KindDocument, nil)
	if err != nil || string(empty) != strings.Join(DocumentHeader, ",")+"\n" {
		t.Fatalf("an empty file still has its header: %q %v", empty, err)
	}
}

// The export is complete: every posting since T-0 is in exactly one file, the journal file
// nets to zero per currency, and the input order of documents does not change the files.
func TestBuildRekeyExport_Properties(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		n := rapid.IntRange(0, 8).Draw(t, "journals")
		var ps []RekeyPosting
		before := 0
		for i := 0; i < n; i++ {
			ref := "JV-" + string(rune('A'+i))
			at := rekeyT0.Add(time.Duration(rapid.IntRange(-48, 48).Draw(t, "hour")) * time.Hour)
			amt := d(rapid.SampledFrom([]string{"0.01", "12.50", "1000"}).Draw(t, "amount"))
			ps = append(ps, jl(ref, "1100", amt.String(), at), jl(ref, "4000", amt.Neg().String(), at))
			if at.Before(rekeyT0) {
				before += 2
			}
		}
		m := rapid.IntRange(0, 6).Draw(t, "documents")
		for i := 0; i < m; i++ {
			at := rekeyT0.Add(time.Duration(rapid.IntRange(-48, 48).Draw(t, "dochour")) * time.Hour)
			ps = append(ps, RekeyPosting{Kind: KindDocument, Entity: rapid.SampledFrom([]string{"USMF", "DEMF"}).Draw(t, "entity"),
				DocumentRef: "SI-" + string(rune('A'+i)), PostedAt: at, Account: "1200", Currency: "EUR", Amount: d("5")})
			if at.Before(rekeyT0) {
				before++
			}
		}
		out, err := BuildRekeyExport(rekeyPlan(), ps)
		if err != nil {
			t.Fatal(err)
		}
		if got := len(out.Journals) + len(out.Documents) + out.Excluded; got != len(ps) || out.Excluded != before {
			t.Fatalf("exported %d + excluded %d of %d postings, %d before T-0", len(out.Journals)+len(out.Documents), out.Excluded, len(ps), before)
		}
		sum := d("0")
		for _, l := range out.Journals {
			sum = sum.Add(l.Amount)
		}
		if !sum.IsZero() {
			t.Fatalf("journal file nets to %s", sum)
		}
		reversed := append([]RekeyPosting(nil), ps...)
		docs := reversed[2*n:]
		for i, j := 0, len(docs)-1; i < j; i, j = i+1, j-1 {
			docs[i], docs[j] = docs[j], docs[i]
		}
		again, err := BuildRekeyExport(rekeyPlan(), reversed)
		if err != nil {
			t.Fatal(err)
		}
		a, _ := RenderRekeyCSV(KindDocument, out.Documents)
		b, _ := RenderRekeyCSV(KindDocument, again.Documents)
		if string(a) != string(b) {
			t.Fatalf("document file depends on input order:\n%s\n%s", a, b)
		}
	})
}
