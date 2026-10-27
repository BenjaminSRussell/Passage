package domain

import (
	"fmt"
	"strings"
	"unicode/utf8"
)

// CodeSegment is a demand that would take stock of another segment
// (INV-SEG-001, BR-16.17).
const CodeSegment = "INV-SEG-001"

// SegmentType is the kind of segregated stock (FR-16.20; SAP special stock E / Q).
type SegmentType string

const (
	SegmentNone       SegmentType = "NONE"
	SegmentSalesOrder SegmentType = "SALES_ORDER"
	SegmentProject    SegmentType = "PROJECT"
)

// SegmentValue is the value the segment dimension holds for a type and
// reference: "SALES_ORDER:<ref>" or "PROJECT:<ref>". NONE (or an empty type) is
// unsegmented stock, the empty value, and takes no reference.
func SegmentValue(t SegmentType, ref string) (string, error) {
	ref = strings.TrimSpace(ref)
	switch t {
	case "", SegmentNone:
		if ref != "" {
			return "", fail(CodeInvalid, "segment type NONE takes no reference")
		}
		return "", nil
	case SegmentSalesOrder, SegmentProject:
		v := string(t) + ":" + ref
		if _, _, err := ParseSegment(v); err != nil {
			return "", fail(CodeInvalid, "%v", err)
		}
		return v, nil
	default:
		return "", fail(CodeInvalid, "segment type must be NONE, SALES_ORDER or PROJECT")
	}
}

// ParseSegment splits a segment dimension value into its type and reference.
// The reference is required and the whole value fits a dimension value.
func ParseSegment(v string) (SegmentType, string, error) {
	t, ref, ok := strings.Cut(v, ":")
	if !ok || (SegmentType(t) != SegmentSalesOrder && SegmentType(t) != SegmentProject) {
		return "", "", fmt.Errorf("segment %q must be SALES_ORDER:<reference> or PROJECT:<reference>", v)
	}
	if strings.TrimSpace(ref) == "" || ref != strings.TrimSpace(ref) {
		return "", "", fmt.Errorf("segment %q needs a reference without surrounding spaces", v)
	}
	if utf8.RuneCountInString(v) > maxDimensionValue {
		return "", "", fmt.Errorf("segment %q is longer than %d characters", v, maxDimensionValue)
	}
	return SegmentType(t), ref, nil
}

// SegmentEligible reports whether stock in segment stock may serve a demand in
// segment demand (BR-16.17): its own segment always; unsegmented stock for a
// segmented demand only with fallback; never another segment, and never
// segmented stock for an unsegmented demand.
func SegmentEligible(stock, demand string, fallback bool) bool {
	if stock == demand {
		return true
	}
	return demand != "" && stock == "" && fallback
}

// CheckSegment returns INV-SEG-001 when stock in segment stock may not serve
// the demand (BR-16.17).
func CheckSegment(stock, demand string, fallback bool) error {
	if SegmentEligible(stock, demand, fallback) {
		return nil
	}
	return fail(CodeSegment, "stock with segment %s cannot be issued to demand with segment %s", segmentLabel(stock), segmentLabel(demand))
}

func segmentLabel(v string) string {
	if v == "" {
		return string(SegmentNone)
	}
	return v
}
