package domain

import (
	"time"

	"github.com/shopspring/decimal"
)

const (
	EbomDraft    = "draft"
	EbomReleased = "released"
)

// EbomPayload is the CAD/PLM contract (FR-9.27): header part + revision and find-numbered lines.
type EbomPayload struct {
	PartNumber string
	Revision   string
	Lines      []EbomLine
}

// EbomLine is one CAD BOM row.
type EbomLine struct {
	FindNumber           int
	PartNumber           string
	Quantity             decimal.Decimal
	Unit                 string
	ReferenceDesignators []string
}

// EbomImport is one idempotent (source, part, revision) row (BR-9.19).
type EbomImport struct {
	ID           string
	Source       string
	PartNumber   string
	Revision     string
	Status       string
	BOMVersionID string
	EcrID        string
	Payload      EbomPayload
}

// EbomException is a line that could not be resolved.
type EbomException struct {
	PartNumber string
	Code       string
}

// EbomComparison is eBOM versus the active mBOM (FR-9.27).
type EbomComparison struct {
	Added   int
	Removed int
	Changed int
}

// EbomResult is the draft BOM, ECR, exceptions and comparison produced by an import.
type EbomResult struct {
	Draft      *BomVersion
	ECR        *Ecr
	Exceptions []EbomException
	Comparison EbomComparison
}

func (e EbomImport) CanReimport() error {
	if e.Status == EbomReleased {
		return errf(CodeEbomRevisionReleased, "Revision %s of %s is already released.", e.Revision, e.PartNumber)
	}
	return nil
}

// ImportEbom resolves part numbers through known product ids (product number or cad_part identifier).
// Unknown parts, the header included, become ENG-EBOM-PART-UNKNOWN exceptions (one per part number); unknown
// lines are left out of the draft (AC-9.17). Find numbers are kept as given; a line without one takes the next
// free multiple of ten. A line that does not validate refuses the whole import.
func ImportEbom(payload EbomPayload, knownProducts map[string]string, active *BomVersion) (EbomResult, error) {
	result := EbomResult{}
	draft := BomVersion{
		ProductID:     payload.PartNumber,
		Status:        StatusDraft,
		EffectiveFrom: time.Now().UTC(),
		Usage:         UsageEngineering,
		Category:      CategoryMaterial,
	}
	flagged := map[string]bool{}
	flag := func(part string) {
		if !flagged[part] {
			flagged[part] = true
			result.Exceptions = append(result.Exceptions, EbomException{PartNumber: part, Code: CodeEbomPartUnknown})
		}
	}
	if id, ok := knownProducts[payload.PartNumber]; ok {
		draft.ProductID = id
	} else {
		flag(payload.PartNumber)
	}
	taken := map[int]bool{}
	for _, line := range payload.Lines {
		if line.FindNumber != 0 {
			taken[line.FindNumber] = true
		}
	}
	next := 0
	for _, line := range payload.Lines {
		productID, ok := knownProducts[line.PartNumber]
		if !ok {
			flag(line.PartNumber)
			continue
		}
		no := line.FindNumber
		if no == 0 {
			for next += 10; taken[next]; next += 10 {
			}
			no = next
		}
		bomLine := BomLine{LineNo: no, ComponentProductID: productID, Quantity: line.Quantity, Series: decimal.NewFromInt(1), LineType: LineItem}
		if err := ValidateLine(bomLine); err != nil {
			return result, err
		}
		draft.Lines = append(draft.Lines, bomLine)
	}
	result.Draft = &draft
	result.ECR = &Ecr{
		Number:   "ECR-" + payload.PartNumber + "-" + payload.Revision,
		Title:    "eBOM " + payload.PartNumber + " " + payload.Revision,
		Priority: "medium",
		State:    EcrDraft,
		Products: []string{draft.ProductID},
	}
	result.Comparison = CompareEbom(draft, active)
	return result, nil
}

// CompareEbom counts added, removed and quantity-changed component rows against the active mBOM.
func CompareEbom(draft BomVersion, active *BomVersion) EbomComparison {
	if active == nil {
		return EbomComparison{Added: len(draft.Lines)}
	}
	oldQty := map[string]decimal.Decimal{}
	for _, l := range active.Lines {
		oldQty[l.ComponentProductID] = oldQty[l.ComponentProductID].Add(l.Quantity)
	}
	newQty := map[string]decimal.Decimal{}
	for _, l := range draft.Lines {
		newQty[l.ComponentProductID] = newQty[l.ComponentProductID].Add(l.Quantity)
	}
	var c EbomComparison
	for id, qty := range newQty {
		prev, ok := oldQty[id]
		if !ok {
			c.Added++
		} else if !prev.Equal(qty) {
			c.Changed++
		}
	}
	for id := range oldQty {
		if _, ok := newQty[id]; !ok {
			c.Removed++
		}
	}
	return c
}
