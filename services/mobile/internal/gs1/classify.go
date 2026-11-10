package gs1

import (
	"regexp"
	"slices"
)

// ScanClass is what a flow step expects the worker to scan.
type ScanClass string

const (
	ClassLP       ScanClass = "LP"
	ClassGTIN     ScanClass = "GTIN"
	ClassLot      ScanClass = "LOT"
	ClassLocation ScanClass = "LOCATION"
	ClassAny      ScanClass = "ANY"
)

// ValidClass reports whether c is a known scan class.
func ValidClass(c ScanClass) bool {
	return slices.Contains([]ScanClass{ClassLP, ClassGTIN, ClassLot, ClassLocation, ClassAny}, c)
}

var (
	lpFormat       = regexp.MustCompile(`^LP[0-9A-Z-]{3,20}$`)
	locationFormat = regexp.MustCompile(`^[A-Z0-9]{1,4}(-[A-Z0-9]{1,4}){1,3}$`)
	plainGTIN      = regexp.MustCompile(`^\d{8}$|^\d{12,14}$`)
)

// Classified is a scan decided to be GS1 or plain.
type Classified struct {
	Kind  string  `json:"kind"`
	Value string  `json:"value"`
	GS1   *Result `json:"gs1,omitempty"`
}

// ClassifyOptions extend Options with the tenant's scan policy
// (gs1_require_symbology_id) and the step's expectation.
type ClassifyOptions struct {
	Options
	RequireSymbologyID bool
	ExpectGS1          bool
}

// Classify decides whether a raw scan is a GS1 element string or a plain
// value (BR-32.1 last paragraph).
func Classify(raw string, opts ClassifyOptions) Classified {
	if hasSymbologyID(raw) {
		if slices.Contains(GS1Symbologies, raw[1:3]) {
			r := Parse(raw, opts.Options)
			return Classified{Kind: "gs1", Value: raw, GS1: &r}
		}
		return Classified{Kind: "plain", Value: raw[3:]}
	}
	if opts.RequireSymbologyID {
		return Classified{Kind: "plain", Value: raw}
	}
	if opts.ExpectGS1 && StartsWithAI(raw) {
		r := Parse(raw, opts.Options)
		return Classified{Kind: "gs1", Value: raw, GS1: &r}
	}
	return Classified{Kind: "plain", Value: raw}
}

// ClassifyFor classifies a scan for a step that expects one of the classes. A
// step that expects only a location never reads unprefixed text as GS1. An
// unprefixed scan that parses as GS1 falls back to a plain value when the parse
// fails or the plain value already fits one of the specific classes, so an
// EAN-13, a UPC-A or the location 10-01-02 is not taken for AIs.
func ClassifyFor(raw string, expected []ScanClass, opts ClassifyOptions) Classified {
	opts.ExpectGS1 = slices.ContainsFunc(expected, func(c ScanClass) bool { return c != ClassLocation })
	c := Classify(raw, opts)
	if c.GS1 == nil || hasSymbologyID(raw) {
		return c
	}
	plain := Classified{Kind: "plain", Value: raw}
	fits := slices.ContainsFunc(expected, func(e ScanClass) bool { return e != ClassAny && matches(e, plain) })
	if !c.GS1.OK || fits {
		return plain
	}
	return c
}

var classLabel = map[ScanClass]string{ClassLP: "LP", ClassGTIN: "GTIN", ClassLot: "lot", ClassLocation: "location", ClassAny: "any barcode"}

// Describe names what the worker scanned, in the words of MOB-0101.
func Describe(c Classified) string {
	if c.GS1 != nil {
		switch {
		case c.GS1.Fields["00"] != "":
			return "LP"
		case c.GS1.Fields["01"] != "":
			return "GTIN"
		case c.GS1.Fields["10"] != "":
			return "lot"
		case len(c.GS1.Order) > 0:
			return "AI " + c.GS1.Order[0]
		}
		return "AI ?"
	}
	if lpFormat.MatchString(c.Value) {
		return "LP"
	}
	if locationFormat.MatchString(c.Value) {
		return "location"
	}
	return c.Value
}

// Verdict is the answer to one scan; a nil Verdict means the step may advance.
type Verdict struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

func matches(expected ScanClass, c Classified) bool {
	var fields map[string]string
	if c.GS1 != nil {
		fields = c.GS1.Fields
	}
	plain := c.Kind == "plain"
	switch expected {
	case ClassAny:
		return true
	case ClassLP:
		return fields["00"] != "" || (plain && lpFormat.MatchString(c.Value))
	case ClassGTIN:
		return fields["01"] != "" || (plain && plainGTIN.MatchString(c.Value) && Mod10Valid(c.Value))
	case ClassLot:
		return fields["10"] != ""
	case ClassLocation:
		return plain && locationFormat.MatchString(c.Value)
	}
	return false
}

// Check judges a scan against the expected class. A GS1 parse failure is
// reported before a class mismatch, because the label itself is wrong.
func Check(expected ScanClass, c Classified) *Verdict {
	if c.GS1 != nil && len(c.GS1.Errors) > 0 {
		e := c.GS1.Errors[0]
		return &Verdict{Code: e.Code, Message: e.Message}
	}
	if matches(expected, c) {
		return nil
	}
	return &Verdict{Code: CodeNoMatch, Message: "Expected " + classLabel[expected] + ", scanned " + Describe(c) + "."}
}
