package domain

import (
	"strings"
	"testing"
	"time"
)

func TestDocumentRelease_ChangeControlledNeedsECO(t *testing.T) {
	typ := DocumentType{Code: "DRW", ChangeControlled: true, SignatureOnRelease: true, IndependentReview: true}
	prev := Document{ID: "d1", TypeCode: "DRW", Number: "DRW-1", Version: "01", Status: DocReleased, LastEditor: "rita"}
	next := Document{ID: "d2", TypeCode: "DRW", Number: "DRW-1", Version: "02", Status: DocInReview, LastEditor: "rita", Originals: []DocumentOriginal{{SHA256: "abc"}}}
	if err := next.CanRelease(typ, prev, ""); !HasCode(err, CodeDocEcoRequired) {
		t.Fatalf("release without ECO = %v; want %s", err, CodeDocEcoRequired)
	}
	if err := next.CanRelease(typ, prev, "eco-1"); err != nil {
		t.Fatalf("release through ECO = %v", err)
	}
}

func TestDocumentSign_Part11Manifestation(t *testing.T) {
	now := time.Date(2026, 7, 1, 12, 0, 0, 0, time.UTC)
	doc := Document{ID: "d2", TypeCode: "DRW", Number: "DRW-1", Version: "02", Status: DocInReview, LastEditor: "rita", Originals: []DocumentOriginal{{SHA256: "aaa"}, {SHA256: "bbb"}}}
	typ := DocumentType{Code: "DRW", SignatureOnRelease: true, IndependentReview: true, SignatureMeaning: "Approved for release"}
	auth := Reauth{UserID: "carol", Assertion: "step-up", Method: AuthPassword}
	sig, err := SignDocument(typ, doc, "released", auth, "carol", "Carol Kane", now)
	if err != nil {
		t.Fatal(err)
	}
	if sig.SignerID != "carol" || sig.PrintedName != "Carol Kane" || sig.Meaning != "Approved for release" || sig.SignedAt.IsZero() || sig.RecordID != "d2" || sig.OriginalsHash == "" || sig.Transition != "released" {
		t.Fatalf("signature = %+v", sig)
	}
	if !strings.Contains(sig.OriginalsHash, "aaa") && len(sig.OriginalsHash) != 64 {
		t.Fatalf("originals hash should bind the files: %s", sig.OriginalsHash)
	}
}

func TestDocumentSign_RequiresReauthAndDistinctSigner(t *testing.T) {
	now := time.Now().UTC()
	doc := Document{ID: "d2", LastEditor: "rita", Status: DocInReview, Originals: []DocumentOriginal{{SHA256: "x"}}}
	typ := DocumentType{SignatureOnRelease: true, IndependentReview: true, SignatureMeaning: "Approved for release"}
	if _, err := SignDocument(typ, doc, "released", Reauth{UserID: "carol"}, "carol", "C", now); !HasCode(err, CodeDocReauth) {
		t.Fatalf("missing assertion = %v; want %s", err, CodeDocReauth)
	}
	if _, err := SignDocument(typ, doc, "released", Reauth{UserID: "rita", Assertion: "ok"}, "rita", "R", now); !HasCode(err, CodeDocSignerSame) {
		t.Fatalf("editor signing = %v; want %s", err, CodeDocSignerSame)
	}
	if _, err := SignDocument(typ, doc, "released", Reauth{UserID: "carol", Assertion: "ok"}, "mallory", "M", now); !HasCode(err, CodeDocReauth) {
		t.Fatalf("assertion user != actor = %v", err)
	}
}

func TestDocumentSignedOriginalsAreLocked(t *testing.T) {
	doc := Document{ID: "d2", Status: DocReleased, SignedAt: time.Now().UTC()}
	if err := doc.AssertUnlocked(); !HasCode(err, CodeDocSigned) {
		t.Fatalf("got %v; want %s", err, CodeDocSigned)
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
