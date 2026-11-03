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
// Unknown parts become ENG-EBOM-PART-UNKNOWN exceptions and are left out of the draft (AC-9.17).
func ImportEbom(payload EbomPayload, knownProducts map[string]string, active *BomVersion) EbomResult {
	result := EbomResult{}
	draft := BomVersion{
		ProductID:     payload.PartNumber,
		Status:        StatusDraft,
		EffectiveFrom: time.Now().UTC(),
		Usage:         UsageEngineering,
		Category:      CategoryMaterial,
	}
	if id, ok := knownProducts[payload.PartNumber]; ok {
		draft.ProductID = id
	}
	for _, line := range payload.Lines {
		productID, ok := knownProducts[line.PartNumber]
		if !ok {
			result.Exceptions = append(result.Exceptions, EbomException{PartNumber: line.PartNumber, Code: CodeEbomPartUnknown})
			continue
		}
		qty := line.Quantity
		if qty.IsZero() {
			qty = decimal.NewFromInt(1)
		}
		no := line.FindNumber
		if no == 0 {
			no = (len(draft.Lines) + 1) * 10
		}
		draft.Lines = append(draft.Lines, BomLine{
			LineNo: no, ComponentProductID: productID, Quantity: qty, Series: decimal.NewFromInt(1), LineType: LineItem,
		})
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
	return result
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
