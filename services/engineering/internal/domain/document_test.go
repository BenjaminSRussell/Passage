package domain

import (
	"strings"
	"testing"
	"time"
)

func TestDocumentRelease_ChangeControlledNeedsApprovedEcoListingTheDocument(t *testing.T) {
	typ := DocumentType{Code: "DRW", ChangeControlled: true, SignatureOnRelease: true, IndependentReview: true}
	prev := Document{ID: "d1", TypeCode: "DRW", Number: "DRW-1", Version: "01", Status: DocReleased, LastEditor: "rita"}
	next := Document{ID: "d2", TypeCode: "DRW", Number: "DRW-1", Version: "02", Status: DocInReview, LastEditor: "rita", Originals: []DocumentOriginal{{SHA256: "abc"}}}
	if err := next.CanRelease(typ, prev, nil); !HasCode(err, CodeDocEcoRequired) {
		t.Fatalf("release without ECO = %v; want %s", err, CodeDocEcoRequired)
	}
	next.EcoID = "eco-1"
	listing := []DocumentReplacement{{OldDocumentID: "d1", NewDocumentID: "d2"}}
	cases := []struct {
		name string
		eco  *Eco
		ok   bool
	}{
		{"unchecked id on a missing ECO", nil, false},
		{"submitted ECO", &Eco{ID: "eco-1", State: EcoSubmitted, Documents: listing}, false},
		{"other ECO", &Eco{ID: "eco-2", State: EcoApproved, Documents: listing}, false},
		{"ECO that does not list the document", &Eco{ID: "eco-1", State: EcoApproved}, false},
		{"approved ECO listing the pair", &Eco{ID: "eco-1", State: EcoApproved, Documents: listing}, true},
	}
	for _, tc := range cases {
		err := next.CanRelease(typ, prev, tc.eco)
		if tc.ok && err != nil || !tc.ok && !HasCode(err, CodeDocEcoRequired) {
			t.Fatalf("%s: %v", tc.name, err)
		}
	}
	if err := next.CanRelease(DocumentType{Code: "DRW"}, prev, nil); err != nil {
		t.Fatalf("type without change control = %v", err)
	}
}

func TestDocumentSign_Part11Manifestation(t *testing.T) {
	now := time.Date(2026, 7, 1, 12, 0, 0, 0, time.UTC)
	doc := Document{ID: "d2", TypeCode: "DRW", Number: "DRW-1", Version: "02", Status: DocInReview, LastEditor: "rita", Originals: []DocumentOriginal{{StorageKey: "k/a", SHA256: "aaa"}, {StorageKey: "k/b", SHA256: "bbb"}}}
	typ := DocumentType{Code: "DRW", SignatureOnRelease: true, IndependentReview: true, SignatureMeaning: "Approved for release", Network: DefaultStatusNetwork()}
	sig, err := SignDocument(typ, doc, DocReleased, AuthPassword, "carol", "Carol Kane", now)
	if err != nil {
		t.Fatal(err)
	}
	if sig.SignerID != "carol" || sig.PrintedName != "Carol Kane" || sig.Meaning != "Approved for release" || sig.SignedAt.IsZero() || sig.RecordID != "d2" || sig.Transition != DocReleased || sig.Method != AuthPassword {
		t.Fatalf("signature = %+v", sig)
	}
	if len(sig.OriginalsHash) != 64 || strings.Trim(sig.OriginalsHash, "0123456789abcdef") != "" {
		t.Fatalf("hash %q is not a sha256 hex digest", sig.OriginalsHash)
	}
	reordered := doc
	reordered.Originals = []DocumentOriginal{doc.Originals[1], doc.Originals[0]}
	if HashRecord(reordered) != sig.OriginalsHash {
		t.Fatal("file order must not change the hash")
	}
	mutations := map[string]func(*Document){
		"file hash": func(d *Document) {
			d.Originals = []DocumentOriginal{{StorageKey: "k/a", SHA256: "aab"}, doc.Originals[1]}
		},
		"extra file": func(d *Document) {
			d.Originals = append(append([]DocumentOriginal{}, d.Originals...), DocumentOriginal{StorageKey: "k/c", SHA256: "ccc"})
		},
		"document id":   func(d *Document) { d.ID = "d3" },
		"number":        func(d *Document) { d.Number = "DRW-2" },
		"part":          func(d *Document) { d.Part = "001" },
		"version":       func(d *Document) { d.Version = "03" },
		"document type": func(d *Document) { d.TypeCode = "SPC" },
	}
	for name, mutate := range mutations {
		changed := doc
		mutate(&changed)
		if HashRecord(changed) == sig.OriginalsHash {
			t.Fatalf("changing the %s must change the hash", name)
		}
	}
}

func TestDocumentSign_Guards(t *testing.T) {
	now := time.Now().UTC()
	doc := Document{ID: "d2", LastEditor: "rita", Status: DocInReview, Originals: []DocumentOriginal{{StorageKey: "k/a", SHA256: "x"}}}
	typ := DocumentType{SignatureOnRelease: true, IndependentReview: true, SignatureMeaning: "Approved for release", Network: DefaultStatusNetwork()}
	if _, err := SignDocument(typ, doc, DocReleased, "", "rita", "R", now); !HasCode(err, CodeDocSignerSame) {
		t.Fatalf("editor signing = %v; want %s", err, CodeDocSignerSame)
	}
	if _, err := SignDocument(typ, doc, DocCancelled, "", "carol", "C", now); !HasCode(err, CodeDocNotSignable) {
		t.Fatalf("signing a cancel = %v; want %s", err, CodeDocNotSignable)
	}
	inWork := doc
	inWork.Status = DocInWork
	if _, err := SignDocument(typ, inWork, DocInReview, "", "carol", "C", now); !HasCode(err, CodeDocNotSignable) {
		t.Fatalf("signing a move that needs no signature = %v; want %s", err, CodeDocNotSignable)
	}
	empty := doc
	empty.Originals = nil
	if _, err := SignDocument(typ, empty, DocReleased, "", "carol", "C", now); !HasCode(err, CodeDocNoOriginals) {
		t.Fatalf("no originals = %v; want %s", err, CodeDocNoOriginals)
	}
	unsigned := typ
	unsigned.SignatureOnRelease = false
	if _, err := SignDocument(unsigned, empty, DocReleased, "", "carol", "C", now); err != nil {
		t.Fatalf("type that is not signed on release needs no file: %v", err)
	}
}

func TestDocumentLockedBySignatureOrRelease(t *testing.T) {
	for _, doc := range []Document{{ID: "d2", Status: DocInReview, SignedAt: time.Now().UTC()}, {ID: "d3", Status: DocReleased}} {
		if err := doc.AssertUnlocked(); !HasCode(err, CodeDocSigned) {
			t.Fatalf("%+v = %v; want %s", doc, err, CodeDocSigned)
		}
	}
	if err := (Document{ID: "d4", Status: DocInWork}).AssertUnlocked(); err != nil {
		t.Fatalf("draft = %v", err)
	}
}

func TestValidateReplacement(t *testing.T) {
	prev := Document{ID: "d1", TypeCode: "DRW", Number: "N", Status: DocReleased}
	cur := Document{ID: "d2", TypeCode: "DRW", Number: "N", Version: "02", Status: DocInReview}
	if err := ValidateReplacement(prev, cur, true); err != nil {
		t.Fatalf("valid pair = %v", err)
	}
	other := cur
	other.Number = "M"
	notReleased := prev
	notReleased.Status = DocObsolete
	released := cur
	released.Status = DocReleased
	for name, tc := range map[string]struct {
		prev, cur Document
		signed    bool
		code      string
	}{
		"different number":       {prev, other, true, CodeDocState},
		"old version not active": {notReleased, cur, true, CodeDocState},
		"new version released":   {prev, released, true, CodeDocState},
		"no signature":           {prev, cur, false, CodeDocUnsigned},
	} {
		if err := ValidateReplacement(tc.prev, tc.cur, tc.signed); !HasCode(err, tc.code) {
			t.Fatalf("%s = %v; want %s", name, err, tc.code)
		}
	}
}

func TestDocumentStructure_RejectsCycle(t *testing.T) {
	// parent A -> child B -> parent A
	if err := CheckDocumentCycle("A", []string{"B"}, map[string][]string{"B": {"A"}}); !HasCode(err, CodeDocCycle) {
		t.Fatalf("cycle = %v; want %s", err, CodeDocCycle)
	}
	if err := CheckDocumentCycle("A", []string{"B"}, map[string][]string{"B": {"C"}}); err != nil {
		t.Fatalf("tree = %v", err)
	}
}

func TestDefaultNetwork_AllowsInWorkToReleasedViaReview(t *testing.T) {
	net := DefaultStatusNetwork()
	if err := net.CanTransition(DocInWork, DocInReview, ""); err != nil {
		t.Fatal(err)
	}
	if err := net.CanTransition(DocInReview, DocReleased, "engineering.doc.sign"); err != nil {
		t.Fatal(err)
	}
	if err := net.CanTransition(DocInWork, DocReleased, ""); err == nil {
		t.Fatal("in_work cannot skip to released")
	}
}

func TestObsoletePreviousOnRelease(t *testing.T) {
	prev := Document{ID: "d1", Status: DocReleased}
	next := Document{ID: "d2", Status: DocInReview}
	got := ReleasePair(prev, next)
	if got.Previous.Status != DocObsolete || got.Current.Status != DocReleased {
		t.Fatalf("pair = %+v", got)
	}
}
