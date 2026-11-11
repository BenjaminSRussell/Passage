package replay

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/platform/ledgerclient"

	"github.com/BenjaminSRussell/passage/tools/migration-factory/internal/domain"
)

func sdoc(seq int, id string, area domain.Area, kind, date, key, amount string) domain.SubledgerDocument {
	return domain.SubledgerDocument{Seq: seq, DocumentID: id, Area: area, Kind: kind, PostingDate: date, Key: key, Currency: "USD", Amount: decimal.RequireFromString(amount)}
}

func ports() (Ports, map[domain.Area]*MemoryPort) {
	m := map[domain.Area]*MemoryPort{}
	p := Ports{}
	for _, a := range domain.SubledgerAreas {
		m[a] = NewMemoryPort()
		p[a] = m[a]
	}
	return p, m
}

func variances(in domain.ParallelInput) []domain.Variance {
	return domain.Diff(in.Legacy, in.Passage, domain.DefaultRules(), nil)
}

func unexplained(vs []domain.Variance) []domain.Variance {
	var out []domain.Variance
	for _, v := range vs {
		if v.Class == domain.ClassUnexplained {
			out = append(out, v)
		}
	}
	return out
}

func TestRunSubledger_PostsEveryAreaAndDiffsTheMovement(t *testing.T) {
	ctx := context.Background()
	p, mem := ports()
	in := Request{Period: "2026-05", System: domain.SourceAX2012, LegalEntityID: "le", Ports: p, Subledger: []domain.SubledgerDocument{
		sdoc(3, "P1", domain.AreaAR, "PAYMENT", "2026-05-09", "C1", "-40"),
		sdoc(1, "I1", domain.AreaAR, "INVOICE", "2026-05-02", "C1", "100.50"),
		sdoc(2, "V1", domain.AreaAP, "INVOICE", "2026-05-03", "V1", "250"),
		sdoc(4, "R1", domain.AreaINV, "RECEIPT", "2026-05-04", "ITEM@WH1", "80"),
		sdoc(5, "F1", domain.AreaFA, "ACQUISITION", "2026-05-05", "FA-9", "9000"),
	}}
	res, err := RunSubledger(ctx, in.Ports, in)
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Posted) != 5 || len(res.Rejected) != 0 {
		t.Fatalf("posted %v rejected %v", res.Posted, res.Rejected)
	}
	for a, m := range mem {
		if m.Posts() == 0 {
			t.Fatalf("%s port was not used", a)
		}
	}
	// A replay run twice posts each document once (the fixed idempotency key).
	if _, err := RunSubledger(ctx, in.Ports, in); err != nil {
		t.Fatal(err)
	}
	if mem[domain.AreaAR].Posts() != 2 {
		t.Fatalf("AR posted %d, want 2", mem[domain.AreaAR].Posts())
	}
	// Through Run: the tuples of every area are in the parallel-run input and nothing is unexplained.
	f := ledgerclient.NewFake()
	f.AddLedger("L", "le")
	full := in
	full.LedgerID = "L"
	full.Documents = []domain.ReplayDocument{doc(1, "GJ-1", "2026-05-02", "10")}
	got, err := Run(ctx, f, full)
	if err != nil {
		t.Fatal(err)
	}
	areas := map[domain.Area]bool{}
	for _, l := range got.Input.Legacy {
		areas[l.Area] = true
	}
	if len(areas) != 5 || got.Input.DocumentsReplayed != 6 {
		t.Fatalf("areas %v, documents replayed %d", areas, got.Input.DocumentsReplayed)
	}
	if u := unexplained(variances(got.Input)); len(u) != 0 {
		t.Fatalf("unexplained %+v", u)
	}
}

func TestRunSubledger_RoundingWithinTheDocumentCountIsExpected(t *testing.T) {
	// 40 invoices of 1.004 round to 1.00 each: the AR tuple differs by 0.16 over 40 documents, inside 0.01 per document.
	p, _ := ports()
	var docs []domain.SubledgerDocument
	for i := 1; i <= 40; i++ {
		docs = append(docs, sdoc(i, fmt.Sprintf("I%d", i), domain.AreaAR, "INVOICE", "2026-05-02", "C1", "1.004"))
	}
	res, err := Run(context.Background(), nil, Request{Period: "2026-05", System: domain.SourceSAPECC, LegalEntityID: "le", Ports: p, Subledger: docs})
	if err != nil {
		t.Fatal(err)
	}
	vs := variances(res.Input)
	if len(vs) != 1 || vs[0].Class == domain.ClassUnexplained || vs[0].Documents != 40 || !vs[0].Delta.Equal(decimal.RequireFromString("-0.16")) {
		t.Fatalf("variances %+v", vs)
	}
	// A subledger that books tenths loses 0.04 on one document, more than 0.01 per document allows: unexplained.
	p2, m2 := ports()
	m2[domain.AreaAR].Scale = 1
	docs = []domain.SubledgerDocument{sdoc(1, "I1", domain.AreaAR, "INVOICE", "2026-05-02", "C1", "1.04")}
	res, err = Run(context.Background(), nil, Request{Period: "2026-05", System: domain.SourceSAPECC, LegalEntityID: "le", Ports: p2, Subledger: docs})
	if err != nil {
		t.Fatal(err)
	}
	if u := unexplained(variances(res.Input)); len(u) != 1 {
		t.Fatalf("a 0.04 difference on one document is unexplained: %+v", variances(res.Input))
	}
}

func TestRunSubledger_RefusedDocumentShowsAsAVarianceAndMissingDocumentsAgainstTheLegacyExtract(t *testing.T) {
	p, mem := ports()
	mem[domain.AreaAP].Refuse("V2", "PAYABLES-VENDOR-BLOCKED")
	in := Request{Period: "2026-05", System: domain.SourceAX2012, LegalEntityID: "le", Ports: p, Subledger: []domain.SubledgerDocument{
		sdoc(1, "V1", domain.AreaAP, "INVOICE", "2026-05-02", "V1", "100"),
		sdoc(2, "V2", domain.AreaAP, "INVOICE", "2026-05-03", "V1", "60"),
	}}
	res, err := Run(context.Background(), nil, in)
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Rejected) != 1 || res.Rejected[0].DocumentID != "V2" || res.Rejected[0].Code != "PAYABLES-VENDOR-BLOCKED" || res.Rejected[0].Area != "AP" {
		t.Fatalf("rejected %+v", res.Rejected)
	}
	u := unexplained(variances(res.Input))
	if len(u) != 1 || u[0].Area != domain.AreaAP || !u[0].Legacy.Equal(decimal.NewFromInt(160)) || !u[0].Passage.Equal(decimal.NewFromInt(100)) {
		t.Fatalf("unexplained %+v", u)
	}
	// The legacy extract is the legacy side when given: a document the legacy ledger holds and the replay file
	// lacks shows up as a difference even though every replayed document posted.
	p2, _ := ports()
	in2 := Request{Period: "2026-05", System: domain.SourceAX2012, LegalEntityID: "le", Ports: p2,
		Subledger:       []domain.SubledgerDocument{sdoc(1, "I1", domain.AreaAR, "INVOICE", "2026-05-02", "C1", "100")},
		LegacySubledger: []domain.SubledgerBalance{{Area: domain.AreaAR, Key: "C1", Currency: "USD", Net: decimal.NewFromInt(130)}}}
	res, err = Run(context.Background(), nil, in2)
	if err != nil {
		t.Fatal(err)
	}
	if u := unexplained(variances(res.Input)); len(u) != 1 || !u[0].Delta.Equal(decimal.NewFromInt(-30)) {
		t.Fatalf("legacy extract difference %+v", variances(res.Input))
	}
}

func TestRunSubledger_MissingPortAndOutage(t *testing.T) {
	ctx := context.Background()
	p, mem := ports()
	delete(p, domain.AreaFA)
	in := Request{Period: "2026-05", System: domain.SourceAX2012, LegalEntityID: "le", Ports: p, Subledger: []domain.SubledgerDocument{
		sdoc(1, "I1", domain.AreaAR, "INVOICE", "2026-05-02", "C1", "1"),
		sdoc(2, "F1", domain.AreaFA, "ACQUISITION", "2026-05-03", "FA-1", "1"),
	}}
	if _, err := Run(ctx, nil, in); !errors.Is(err, ErrNoSubledgerPort) {
		t.Fatalf("missing port: %v", err)
	}
	if mem[domain.AreaAR].Posts() != 0 {
		t.Fatal("nothing may be posted before every area has a port")
	}
	// With journals too, the ports are checked before the first journal is posted.
	f := ledgerclient.NewFake()
	f.AddLedger("L", "le")
	withJournal := in
	withJournal.LedgerID, withJournal.Documents = "L", []domain.ReplayDocument{doc(1, "GJ", "2026-05-02", "1")}
	if _, err := Run(ctx, f, withJournal); !errors.Is(err, ErrNoSubledgerPort) || len(f.Entries()) != 0 {
		t.Fatalf("journal replay with a missing port: %v, %d entries", err, len(f.Entries()))
	}
	p[domain.AreaFA] = NewMemoryPort()
	mem[domain.AreaAR].FailPosts(errors.New("connection reset"))
	if _, err := Run(ctx, nil, in); !errors.Is(err, ErrSubledgerUnavailable) {
		t.Fatalf("outage: %v", err)
	}
	// The outage cleared, the replay is repeated and completes.
	mem[domain.AreaAR].FailPosts(nil)
	res, err := Run(ctx, nil, in)
	if err != nil || len(res.Posted) != 2 {
		t.Fatalf("repeat: %v %v", err, res.Posted)
	}
	// An invalid document is refused before any port is called.
	bad := in
	bad.Subledger = []domain.SubledgerDocument{sdoc(1, "I1", domain.AreaAR, "INVOICE", "2026-06-02", "C1", "1")}
	if _, err := Run(ctx, nil, bad); !errors.Is(err, domain.ErrInvalid) {
		t.Fatalf("invalid document: %v", err)
	}
}
