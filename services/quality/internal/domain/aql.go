package domain

import (
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

// InspectionLevelII is the default general inspection level (FR-33.2).
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

// storedAQLs are the AQL columns a plan stored on an association or a manual
// order may name: the quality_association.aql column is numeric(4,2) and the
// web plan form offers these three. Sizing itself (DrawSample) has every column.
var storedAQLs = []string{"1.0", "2.5", "4.0"}

// AQLSupported reports whether a plan stored on an association or a manual order
// may use the AQL value (see storedAQLs).
func AQLSupported(aql decimal.Decimal) bool {
	for _, s := range storedAQLs {
		if aql.Equal(decimal.RequireFromString(s)) {
			return true
		}
	}
	return false
}

func validLevel(level string) bool {
	_, ok := tableI[level]
	return ok
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
	if !validLevel(level) {
		return SampleDraw{}, invalid("inspectionLevel must be S-1, S-2, S-3, S-4, I, II or III")
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
	if _, ok := masterTables[sev]; !ok {
		return SampleDraw{}, invalid("severity must be NORMAL, TIGHTENED or REDUCED")
	}
	if !isWhole(lot) || lot.LessThan(decimal.NewFromInt(2)) {
		return SampleDraw{}, invalid("AQL sizing needs a whole lot of at least 2")
	}
	if _, ok := aqlColumn(aql); !ok {
		return SampleDraw{}, fail(CodeTableNotLoaded, "AQL is not a Z1.4 column (0.010 to 1000)")
	}
	n := lot.IntPart()
	if !lot.Equal(decimal.NewFromInt(n)) { // beyond int64
		n = 1<<63 - 1
	}
	letter, _ := Z14CodeLetter(level, n)
	plan, ok := Z14Resolve(sev, letter, aql)
	if !ok {
		return SampleDraw{}, fail(CodeTableNotLoaded, "no Z1.4 plan for code letter "+letter)
	}
	return SampleDraw{CodeLetter: letter, SampleQty: decimal.Min(decimal.NewFromInt(plan.SampleSize), lot), AcceptNo: plan.AcceptNo, RejectNo: plan.RejectNo, Severity: sev, InspectionLevel: level}, nil
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
// size a sample is refused when it is saved rather than on every order. Sizing
// has every Z1.4 level, AQL and severity, but a stored plan is still level II,
// NORMAL and AQL 1.0, 2.5 or 4.0: the association columns hold only those,
// switching (BR-33.2) is not applied, and evaluating a reduced lot between Ac
// and Re (accept, reinstate normal) is not built.
func ValidatePlan(prefix string, p SamplingPlan) map[string]string {
	fields := map[string]string{}
	if p.InspectionLevel != "" && p.InspectionLevel != InspectionLevelII {
		fields[prefix+".inspectionLevel"] = "only level II is stored on associations and orders yet"
	}
	switch p.Severity {
	case "", SeverityNormal:
	case SeverityTightened, SeverityReduced:
		if p.Method == MethodAQL {
			fields[prefix+".severity"] = "stored AQL plans are NORMAL until switching is applied"
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
			fields[prefix+".aql"] = "must be 1.0, 2.5 or 4.0 on a stored plan"
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
