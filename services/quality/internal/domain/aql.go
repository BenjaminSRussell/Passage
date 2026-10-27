package domain

import (
	"math"

	"github.com/shopspring/decimal"
)

// Severity is the ANSI/ASQ Z1.4 switching severity (BR-33.2).
type Severity string

const (
	SeverityNormal    Severity = "NORMAL"
	SeverityTightened Severity = "TIGHTENED"
	SeverityReduced   Severity = "REDUCED"
)

// Method is how a sample size is derived.
type Method string

const (
	MethodFixed   Method = "FIXED"
	MethodPercent Method = "PERCENT"
	MethodAQL     Method = "AQL"
)

// InspectionLevelII is the default general inspection level (FR-33.2). Other
// levels need Table I rows that are not loaded in this slice.
const InspectionLevelII = "II"

// SamplingPlan is the sampling rule of a test group association or a manual order.
type SamplingPlan struct {
	Method          Method          `json:"method"`
	FixedQty        decimal.Decimal `json:"fixedQty"`
	Percent         decimal.Decimal `json:"percent"`
	AQL             decimal.Decimal `json:"aql"`
	Severity        Severity        `json:"severity"`
	InspectionLevel string          `json:"inspectionLevel"`
}

// SampleDraw is the outcome of sizing a sample for one lot.
type SampleDraw struct {
	CodeLetter      string          `json:"codeLetter"`
	SampleQty       decimal.Decimal `json:"sampleQty"`
	AcceptNo        int             `json:"acceptNo"`
	RejectNo        int             `json:"rejectNo"`
	Severity        Severity        `json:"severity"`
	InspectionLevel string          `json:"inspectionLevel"`
}

type codeBand struct {
	upTo   int64
	letter string
}

// codeLetterLevelII is Z1.4 Table I for general inspection level II.
var codeLetterLevelII = []codeBand{
	{8, "A"}, {15, "B"}, {25, "C"}, {50, "D"}, {90, "E"}, {150, "F"}, {280, "G"}, {500, "H"},
	{1200, "J"}, {3200, "K"}, {10000, "L"}, {35000, "M"}, {150000, "N"}, {500000, "P"}, {math.MaxInt64, "Q"},
}

// sampleSize is the sample size n for each code letter (Table I).
var sampleSize = map[string]int64{
	"A": 2, "B": 3, "C": 5, "D": 8, "E": 13, "F": 20, "G": 32, "H": 50, "J": 80,
	"K": 125, "L": 200, "M": 315, "N": 500, "P": 800, "Q": 1250,
}

// cell is one Table II-A entry after arrow resolution: the code letter whose
// sample size applies and its acceptance and rejection numbers.
type cell struct {
	letter string
	ac, re int
}

// aqlColumns maps the AQL values loaded in this slice to their column index.
var aqlColumns = map[string]int{"1.0": 0, "2.5": 1, "4.0": 2}

// normalSingle is the excerpt of Table II-A (normal, single sampling) used by
// BR-33.1, columns AQL 1.0, 2.5, 4.0. Arrow cells are stored resolved.
var normalSingle = map[string][3]cell{
	"A": {{"E", 0, 1}, {"D", 0, 1}, {"C", 0, 1}},
	"B": {{"E", 0, 1}, {"D", 0, 1}, {"C", 0, 1}},
	"C": {{"E", 0, 1}, {"D", 0, 1}, {"C", 0, 1}},
	"D": {{"E", 0, 1}, {"D", 0, 1}, {"E", 1, 2}},
	"E": {{"E", 0, 1}, {"F", 1, 2}, {"E", 1, 2}},
	"F": {{"G", 1, 2}, {"F", 1, 2}, {"F", 2, 3}},
	"G": {{"G", 1, 2}, {"G", 2, 3}, {"G", 3, 4}},
	"H": {{"H", 2, 3}, {"H", 3, 4}, {"H", 5, 6}},
	"J": {{"J", 3, 4}, {"J", 5, 6}, {"J", 7, 8}},
	"K": {{"K", 5, 6}, {"K", 7, 8}, {"K", 10, 11}},
	"L": {{"L", 7, 8}, {"L", 10, 11}, {"L", 14, 15}},
	"M": {{"M", 10, 11}, {"M", 14, 15}, {"M", 21, 22}},
	"N": {{"N", 14, 15}, {"N", 21, 22}, {"N", 21, 22}},
	"P": {{"P", 21, 22}, {"P", 21, 22}, {"P", 21, 22}},
	"Q": {{"Q", 21, 22}, {"Q", 21, 22}, {"Q", 21, 22}},
}

// AQLSupported reports whether the Z1.4 excerpt loaded in this slice has the AQL column.
func AQLSupported(aql decimal.Decimal) bool {
	_, ok := aqlColumns[aql.StringFixed(1)]
	return ok && aql.Equal(decimal.RequireFromString(aql.StringFixed(1)))
}

// CodeLetter returns the Z1.4 code letter for a lot of n units at level II.
func CodeLetter(lot int64) string {
	for _, b := range codeLetterLevelII {
		if lot <= b.upTo {
			return b.letter
		}
	}
	return "Q"
}

// DrawSample sizes the sample for a lot. An AQL sample larger than the lot is
// replaced by a 100 % inspection of the lot (Z1.4 rule); the Ac/Re numbers stay.
func DrawSample(p SamplingPlan, lot decimal.Decimal) (SampleDraw, error) {
	if !lot.IsPositive() {
		return SampleDraw{}, invalid("lot quantity must be positive")
	}
	level := p.InspectionLevel
	if level == "" {
		level = InspectionLevelII
	}
	if level != InspectionLevelII {
		return SampleDraw{}, fail(CodeTableNotLoaded, "inspection level "+level+" is not loaded yet")
	}
	sev := p.Severity
	if sev == "" {
		sev = SeverityNormal
	}
	switch p.Method {
	case MethodFixed:
		if !p.FixedQty.IsPositive() || !isWhole(p.FixedQty) {
			return SampleDraw{}, invalid("fixedQty must be a positive whole number")
		}
		return SampleDraw{SampleQty: decimal.Min(p.FixedQty, lot), AcceptNo: 0, RejectNo: 1, Severity: SeverityNormal, InspectionLevel: level}, nil
	case MethodPercent:
		if !p.Percent.IsPositive() || p.Percent.GreaterThan(decimal.NewFromInt(100)) {
			return SampleDraw{}, invalid("percent must be above 0 and at most 100")
		}
		n := lot.Mul(p.Percent).Div(decimal.NewFromInt(100)).Ceil()
		if n.LessThan(decimal.NewFromInt(1)) {
			n = decimal.NewFromInt(1)
		}
		return SampleDraw{SampleQty: decimal.Min(n, lot), AcceptNo: 0, RejectNo: 1, Severity: SeverityNormal, InspectionLevel: level}, nil
	case MethodAQL:
		return drawAQL(p.AQL, lot, sev, level)
	}
	return SampleDraw{}, invalid("method must be FIXED, PERCENT or AQL")
}

func drawAQL(aql, lot decimal.Decimal, sev Severity, level string) (SampleDraw, error) {
	if sev != SeverityNormal {
		return SampleDraw{}, fail(CodeTableNotLoaded, "severity "+string(sev)+" needs Table II-B or II-C, not loaded yet")
	}
	if !isWhole(lot) || lot.LessThan(decimal.NewFromInt(2)) {
		return SampleDraw{}, invalid("AQL sizing needs a whole lot of at least 2")
	}
	if !AQLSupported(aql) {
		return SampleDraw{}, fail(CodeTableNotLoaded, "AQL column is not loaded; this slice has 1.0, 2.5 and 4.0")
	}
	col := aqlColumns[aql.StringFixed(1)]
	letter := CodeLetter(lot.IntPart())
	c := normalSingle[letter][col]
	n := decimal.NewFromInt(sampleSize[c.letter])
	return SampleDraw{CodeLetter: letter, SampleQty: decimal.Min(n, lot), AcceptNo: c.ac, RejectNo: c.re, Severity: sev, InspectionLevel: level}, nil
}

func isWhole(d decimal.Decimal) bool { return d.Equal(d.Floor()) }

// SampleCount is the number of samples results are recorded for: the sample
// quantity rounded up, and at least one. A fractional sample (a percentage of
// a lot measured in kg) is still one physical sample.
func SampleCount(sampleQty decimal.Decimal) int {
	n := sampleQty.Ceil().IntPart()
	if n < 1 {
		return 1
	}
	return int(n)
}

// ValidatePlan returns field errors (keyed under prefix) for a sampling plan
// stored on an association or sent with a manual order, so a plan that cannot
// size a sample is refused when it is saved rather than on every order.
func ValidatePlan(prefix string, p SamplingPlan) map[string]string {
	fields := map[string]string{}
	if p.InspectionLevel != "" && p.InspectionLevel != InspectionLevelII {
		fields[prefix+".inspectionLevel"] = "only level II is loaded in this slice"
	}
	switch p.Severity {
	case "", SeverityNormal:
	case SeverityTightened, SeverityReduced:
		if p.Method == MethodAQL {
			fields[prefix+".severity"] = "Tables II-B and II-C are not loaded yet; use NORMAL"
		}
	default:
		fields[prefix+".severity"] = "must be NORMAL, TIGHTENED or REDUCED"
	}
	switch p.Method {
	case MethodFixed:
		if !p.FixedQty.IsPositive() || !isWhole(p.FixedQty) {
			fields[prefix+".fixedQty"] = "must be a positive whole number"
		}
	case MethodPercent:
		if !p.Percent.IsPositive() || p.Percent.GreaterThan(decimal.NewFromInt(100)) {
			fields[prefix+".percent"] = "must be above 0 and at most 100"
		}
	case MethodAQL:
		if !AQLSupported(p.AQL) {
			fields[prefix+".aql"] = "must be 1.0, 2.5 or 4.0 in this slice"
		}
	default:
		fields[prefix+".method"] = "must be FIXED, PERCENT or AQL"
	}
	return fields
}

// Normalized fills the defaults of a plan: severity NORMAL and level II.
func (p SamplingPlan) Normalized() SamplingPlan {
	if p.Severity == "" {
		p.Severity = SeverityNormal
	}
	if p.InspectionLevel == "" {
		p.InspectionLevel = InspectionLevelII
	}
	return p
}
