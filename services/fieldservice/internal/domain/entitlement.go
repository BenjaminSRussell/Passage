package domain

import (
	"fmt"
	"time"

	"github.com/shopspring/decimal"
)

// Agreement status that makes an agreement eligible for entitlement.
const AgreementActive = "ACTIVE"

// Specificity of a matching agreement line (BR-45.1: exact object > object
// category > any).
const (
	SpecNone     = 0
	SpecAny      = 1
	SpecCategory = 2
	SpecObject   = 3
)

// AgreementLine covers one service object, one category, or (neither set) any object.
type AgreementLine struct {
	ServiceObjectID string          `json:"serviceObjectId,omitempty"`
	CategoryID      string          `json:"categoryId,omitempty"`
	IncludedVisits  int             `json:"includedVisits"`
	IncludedHours   decimal.Decimal `json:"includedHours"`
	PartsCovered    bool            `json:"partsCovered"`
	LaborCovered    bool            `json:"laborCovered"`
	TravelCovered   bool            `json:"travelCovered"`
}

func (l AgreementLine) specificity(objectID, categoryID string) int {
	switch {
	case l.ServiceObjectID != "":
		if l.ServiceObjectID == objectID {
			return SpecObject
		}
		return SpecNone
	case l.CategoryID != "":
		if categoryID != "" && l.CategoryID == categoryID {
			return SpecCategory
		}
		return SpecNone
	default:
		return SpecAny
	}
}

// BestLine returns the most specific line that matches the object or category.
// ok is false when no line matches.
func BestLine(lines []AgreementLine, objectID, categoryID string) (line AgreementLine, specificity int, ok bool) {
	for _, l := range lines {
		if s := l.specificity(objectID, categoryID); s > specificity {
			line, specificity = l, s
		}
	}
	return line, specificity, specificity > SpecNone
}

// Agreement is the entitlement view of a service agreement.
type Agreement struct {
	ID           string
	SLAProfileID string
	Status       string
	ValidFrom    time.Time
	ValidTo      *time.Time
	PriorityRank int
	Lines        []AgreementLine
}

// Entitlement is the outcome of resolving a case to an agreement (BR-45.1).
type Entitlement struct {
	AgreementID  string   `json:"agreementId"`
	SLAProfileID string   `json:"slaProfileId"`
	Specificity  int      `json:"specificity"`
	Trace        []string `json:"trace"`
}

func dayOf(t time.Time) time.Time {
	y, m, d := t.Date()
	return time.Date(y, m, d, 0, 0, 0, 0, time.UTC)
}

func validOn(a Agreement, asOf time.Time) bool {
	day := dayOf(asOf)
	if day.Before(dayOf(a.ValidFrom)) {
		return false
	}
	return a.ValidTo == nil || !day.After(dayOf(*a.ValidTo))
}

// beats reports whether candidate a (matching at spec) outranks b (matching at bspec).
// Ranking: specificity, then priority rank (higher wins), then the latest
// validity start, then id so the choice is deterministic.
func beats(a Agreement, spec int, b Agreement, bspec int) bool {
	if spec != bspec {
		return spec > bspec
	}
	if a.PriorityRank != b.PriorityRank {
		return a.PriorityRank > b.PriorityRank
	}
	if da, db := dayOf(a.ValidFrom), dayOf(b.ValidFrom); !da.Equal(db) {
		return da.After(db)
	}
	return a.ID < b.ID
}

// Resolve picks the agreement that governs a case opened at asOf for the given
// service object and category. Candidates must be the customer's agreements
// (the caller loads them). ok is false when nothing matches; the caller then
// applies the fallback SLA profile and bills every line (SVC-ENT-001).
func Resolve(candidates []Agreement, asOf time.Time, objectID, categoryID string) (Entitlement, bool) {
	var best Agreement
	bestSpec := SpecNone
	for _, a := range candidates {
		if a.Status != AgreementActive || !validOn(a, asOf) {
			continue
		}
		_, spec, ok := BestLine(a.Lines, objectID, categoryID)
		if !ok {
			continue
		}
		if bestSpec == SpecNone || beats(a, spec, best, bestSpec) {
			best, bestSpec = a, spec
		}
	}
	if bestSpec == SpecNone {
		return Entitlement{}, false
	}
	return Entitlement{
		AgreementID:  best.ID,
		SLAProfileID: best.SLAProfileID,
		Specificity:  bestSpec,
		Trace: []string{
			fmt.Sprintf("agreement %s matched at specificity %d", best.ID, bestSpec),
			fmt.Sprintf("priority rank %d", best.PriorityRank),
		},
	}, true
}
