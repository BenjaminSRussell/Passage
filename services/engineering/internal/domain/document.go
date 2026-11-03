package domain

import (
	"crypto/sha256"
	"encoding/hex"
	"slices"
	"strings"
	"time"
)

const (
	DocInWork    = "in_work"
	DocInReview  = "in_review"
	DocReleased  = "released"
	DocObsolete  = "obsolete"
	DocCancelled = "cancelled"

	AuthPassword = "password"
)

// DocumentType is FR-8.20 settings used by engineering (FR-9.24/9.25).
type DocumentType struct {
	ID                 string
	Code               string
	Name               string
	ChangeControlled   bool
	SignatureOnRelease bool
	IndependentReview  bool
	SignatureMeaning   string
	Network            StatusNetwork
}

// StatusNetwork is the allowed document status graph (FR-9.24).
type StatusNetwork struct {
	Transitions []StatusTransition
}

// StatusTransition is one allowed edge.
type StatusTransition struct {
	From         string
	To           string
	Signature    bool
	RequiredDuty string
	Meaning      string
}

// Document is a DIR version stored by engineering.
type Document struct {
	ID          string
	TypeCode    string
	Number      string
	Part        string
	Version     string
	Description string
	Status      string
	LastEditor  string
	EcoID       string
	Originals   []DocumentOriginal
	Links       []DocumentLink
	Children    []string
	SignedAt    time.Time
}

// DocumentOriginal is one file on a DIR version.
type DocumentOriginal struct {
	StorageKey string
	SHA256     string
	SizeBytes  int64
}

// DocumentLink points a DIR at a product, BOM version, route version or ECO.
type DocumentLink struct {
	TargetType string
	TargetID   string
}

// Reauth is the step-up proof the signer just completed (21 CFR 11.200).
type Reauth struct {
	UserID    string
	Assertion string
	Method    string
}

// Signature is the 21 CFR 11.50/11.70 manifestation: signer, meaning, timestamp, record link, originals hash.
type Signature struct {
	ID              string
	RecordID        string
	DocumentNumber  string
	DocumentVersion string
	Transition      string
	SignerID        string
	PrintedName     string
	Meaning         string
	SignedAt        time.Time
	OriginalsHash   string
	Method          string
}

// DocumentRelease is the pair ECO release writes: new version released, previous obsolete.
type DocumentRelease struct {
	Previous Document
	Current  Document
}

// DefaultStatusNetwork is in_work → in_review → released → obsolete plus in_work → cancelled.
func DefaultStatusNetwork() StatusNetwork {
	return StatusNetwork{Transitions: []StatusTransition{
		{From: DocInWork, To: DocInReview},
		{From: DocInWork, To: DocCancelled},
		{From: DocInReview, To: DocReleased, Signature: true, RequiredDuty: "engineering.doc.sign"},
		{From: DocInReview, To: DocInWork},
		{From: DocReleased, To: DocObsolete},
	}}
}

func (n StatusNetwork) CanTransition(from, to, duty string) error {
	for _, t := range n.Transitions {
		if t.From == from && t.To == to {
			if t.RequiredDuty != "" && duty != "" && duty != t.RequiredDuty {
				return errf(CodeDocState, "transition %s → %s needs duty %s", from, to, t.RequiredDuty)
			}
			return nil
		}
	}
	return errf(CodeDocState, "cannot move a document from %s to %s", from, to)
}

func (n StatusNetwork) RequiresSignature(from, to string) bool {
	for _, t := range n.Transitions {
		if t.From == from && t.To == to {
			return t.Signature
		}
	}
	return false
}

// CanRelease enforces FR-9.25: a change-controlled type cannot release a successor outside an ECO.
func (d Document) CanRelease(typ DocumentType, previous Document, ecoID string) error {
	if !typ.ChangeControlled {
		return nil
	}
	if previous.ID != "" && previous.Status == DocReleased && ecoID == "" {
		return errf(CodeDocEcoRequired, "Document type %s is change-controlled; release through an ECO.", typ.Code)
	}
	return nil
}

// SignDocument records a Part 11 signature after re-authentication (FR-9.24, BR-9.18).
func SignDocument(typ DocumentType, doc Document, to string, auth Reauth, actor, printedName string, now time.Time) (Signature, error) {
	if auth.Assertion == "" || auth.UserID == "" || auth.UserID != actor {
		return Signature{}, errf(CodeDocReauth, "signature rejected: re-authentication failed")
	}
	if typ.IndependentReview && doc.LastEditor != "" && actor == doc.LastEditor {
		return Signature{}, errf(CodeDocSignerSame, "signer must differ from the last editor")
	}
	meaning := typ.SignatureMeaning
	if meaning == "" {
		meaning = "Approved for release"
	}
	if printedName == "" {
		printedName = actor
	}
	return Signature{
		RecordID:        doc.ID,
		DocumentNumber:  doc.Number,
		DocumentVersion: doc.Version,
		Transition:      to,
		SignerID:        actor,
		PrintedName:     printedName,
		Meaning:         meaning,
		SignedAt:        now.UTC(),
		OriginalsHash:   HashOriginals(doc.Originals),
		Method:          auth.Method,
	}, nil
}

// HashOriginals is the sha256 of the sorted original hashes, binding the signature to the files (11.70).
func HashOriginals(files []DocumentOriginal) string {
	hashes := make([]string, 0, len(files))
	for _, f := range files {
		hashes = append(hashes, f.SHA256)
	}
	slices.Sort(hashes)
	sum := sha256.Sum256([]byte(strings.Join(hashes, ",")))
	return hex.EncodeToString(sum[:])
}

func (d Document) AssertUnlocked() error {
	if !d.SignedAt.IsZero() {
		return errf(CodeDocSigned, "Originals are locked by signature on %s.", d.SignedAt.UTC().Format(time.RFC3339))
	}
	return nil
}

func ReleasePair(previous, current Document) DocumentRelease {
	previous.Status = DocObsolete
	current.Status = DocReleased
	return DocumentRelease{Previous: previous, Current: current}
}

// CheckDocumentCycle walks parent → children and refuses a document that would contain itself (FR-9.26).
func CheckDocumentCycle(parent string, children []string, existing map[string][]string) error {
	graph := make(map[string][]string, len(existing)+1)
	for k, v := range existing {
		graph[k] = append([]string{}, v...)
	}
	graph[parent] = append([]string{}, children...)
	seen := map[string]bool{}
	var walk func(string) error
	walk = func(id string) error {
		if id == parent && seen[id] {
			return errf(CodeDocCycle, "document %s would contain itself", parent)
		}
		if seen[id] {
			return nil
		}
		seen[id] = true
		for _, c := range graph[id] {
			if c == parent {
				return errf(CodeDocCycle, "document %s would contain itself", parent)
			}
			if err := walk(c); err != nil {
				return err
			}
		}
		return nil
	}
	for _, c := range children {
		if err := walk(c); err != nil {
			return err
		}
	}
	return nil
}

// DocumentWhereUsed is one parent of a document: another DIR or a BOM document line (FR-9.26).
type DocumentWhereUsed struct {
	Kind     string
	ParentID string
	Number   string
}
