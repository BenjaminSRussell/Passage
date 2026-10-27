package domain

import (
	"fmt"
	"regexp"
	"strings"
	"time"

	"github.com/shopspring/decimal"
)

// Regulation sets for the transport DG check (FR-34.15, dg_regulation_set). ADR
// covers EU road and 49CFR US road; RID, IMDG and IATA DGR rules arrive as
// reference data with the wave-48 DgRegulationPack.
const (
	RegADR   = "ADR"
	Reg49CFR = "49CFR"
)

// DG check results (BR-34.15).
const (
	DGPass = "PASS"
	DGWarn = "WARN"
	DGFail = "FAIL"
)

// ADRPointsThreshold is the ADR 1.1.3.6 limit above which full ADR requirements
// apply (TMS-0131).
const ADRPointsThreshold = 1000

// MaxDGLines bounds one DG check request.
const MaxDGLines = 100

// Codes raised by the DG check (§6.34.18).
const (
	CodeDGSegregation = "TMS-0130"
	CodeADRPoints     = "TMS-0131"
)

// Rule ids reported with the findings; they are stable so the DG load report can
// group by them.
const (
	RuleSeg49CFR        = "49CFR-177.848"
	RuleSegADR          = "ADR-7.5.2"
	RuleADRPoints       = "ADR-1.1.3.6"
	RuleADRCategory0    = "ADR-1.1.3.6-CAT0"
	RuleLimitedQuantity = "LQ-MARKING"
	RuleEmergencyPhone  = "DG-EMERGENCY-PHONE"
)

var (
	unNumberRe = regexp.MustCompile(`^UN[0-9]{4}$`)
	// dgClassRe is a hazard class or division with an optional class 1
	// compatibility group letter: 3, 2.1, 1.1D.
	dgClassRe = regexp.MustCompile(`^([1-9])(\.[1-6])?([A-HJ-LNS])?$`)
	// e164Re is an E.164 telephone number (emergency_response_phone, §6.34.13).
	e164Re = regexp.MustCompile(`^\+[1-9][0-9]{6,14}$`)
	// prohibited is the mixed-loading reference subset used until the wave-48
	// DgRegulationPack supplies the edition tables: divisions 1.1 and 1.2 may not
	// share a vehicle with any other hazard class (49 CFR 177.848 table, "X"; ADR
	// 7.5.2.1 for labels 1 against 2 to 9). Pairs within class 1 depend on
	// compatibility groups and are not checked here.
	prohibited = prohibitedPairs([]string{"1.1", "1.2"},
		[]string{"2.1", "2.2", "2.3", "3", "4.1", "4.2", "4.3", "5.1", "5.2", "6.1", "7", "8"})
)

func prohibitedPairs(explosives, others []string) map[string]bool {
	out := map[string]bool{}
	for _, e := range explosives {
		for _, o := range others {
			out[pairKey(e, o)] = true
		}
	}
	return out
}

// DGLine is one dangerous-goods line of a load: the HazmatProfile fields of
// wave 31 and the quantity in litres or kilograms as ADR 1.1.3.6 counts it.
type DGLine struct {
	UNNumber           string          `json:"unNumber"`
	ProperShippingName string          `json:"properShippingName"`
	Class              string          `json:"class"`
	PackingGroup       string          `json:"packingGroup,omitempty"`
	Quantity           decimal.Decimal `json:"quantity"`
	TransportCategory  int             `json:"transportCategory"`
	LimitedQuantity    bool            `json:"limitedQuantity"`
}

// DGRequest is the input of a DG check for one load.
type DGRequest struct {
	Regulation             string   `json:"regulation"`
	EmergencyResponsePhone string   `json:"emergencyResponsePhone,omitempty"`
	Lines                  []DGLine `json:"lines"`
}

// DGFinding is one rule that fired, with its rule id and catalog code.
type DGFinding struct {
	RuleID   string `json:"ruleId"`
	Severity string `json:"severity"`
	Code     string `json:"code,omitempty"`
	Detail   string `json:"detail"`
}

// PaperLine is one dangerous-goods line for the BOL and the DG transport
// document (ADR 5.4.1, 49 CFR 172.202): UN number, proper shipping name, class,
// packing group and quantity, with the 24-hour emergency response phone.
type PaperLine struct {
	UNNumber               string          `json:"unNumber"`
	ProperShippingName     string          `json:"properShippingName"`
	Class                  string          `json:"class"`
	PackingGroup           string          `json:"packingGroup,omitempty"`
	Quantity               decimal.Decimal `json:"quantity"`
	LimitedQuantity        bool            `json:"limitedQuantity"`
	EmergencyResponsePhone string          `json:"emergencyResponsePhone,omitempty"`
}

// DGCheck is a stored DG check of a load (the DG load report row, §6.34.14).
type DGCheck struct {
	ID             string          `json:"id"`
	LoadID         string          `json:"loadId"`
	Regulation     string          `json:"regulation"`
	Result         string          `json:"result"`
	ADRPoints      decimal.Decimal `json:"adrPoints"`
	Findings       []DGFinding     `json:"findings"`
	ShippingPapers []PaperLine     `json:"shippingPapers"`
	CheckedBy      string          `json:"checkedBy"`
	CheckedAt      time.Time       `json:"checkedAt"`
}

// ValidateDGRequest reports field errors for a DG check request.
func ValidateDGRequest(req DGRequest) map[string]string {
	f := map[string]string{}
	if req.Regulation != RegADR && req.Regulation != Reg49CFR {
		f["regulation"] = "must be ADR or 49CFR"
	}
	if len(req.Lines) == 0 || len(req.Lines) > MaxDGLines {
		f["lines"] = fmt.Sprintf("must hold 1 to %d dangerous-goods lines", MaxDGLines)
	}
	if req.EmergencyResponsePhone != "" && !e164Re.MatchString(req.EmergencyResponsePhone) {
		f["emergencyResponsePhone"] = "must be an E.164 number such as +18005551234"
	}
	for i, l := range req.Lines {
		p := fmt.Sprintf("lines[%d]", i)
		if !unNumberRe.MatchString(l.UNNumber) {
			f[p+".unNumber"] = "must be UN followed by four digits"
		}
		if n := len(strings.TrimSpace(l.ProperShippingName)); n == 0 || n > 200 {
			f[p+".properShippingName"] = "must be 1 to 200 characters"
		}
		if m := dgClassRe.FindStringSubmatch(l.Class); m == nil || (m[3] != "" && m[1] != "1") {
			f[p+".class"] = "must be a hazard class or division such as 3, 2.1 or 1.1D"
		}
		if !contains([]string{"", "I", "II", "III"}, l.PackingGroup) {
			f[p+".packingGroup"] = "must be I, II, III or empty"
		}
		if !l.Quantity.IsPositive() || l.Quantity.Exponent() < -maxScale {
			f[p+".quantity"] = "must be greater than zero with at most 6 decimals"
		}
		if l.TransportCategory < 0 || l.TransportCategory > 4 {
			f[p+".transportCategory"] = "must be 0 to 4"
		}
	}
	return f
}

// ADRMultiplier is the ADR 1.1.3.6 factor per transport category: category 1
// ×50, 2 ×3, 3 ×1, 4 ×0. Category 0 has no factor; it always needs full ADR.
func ADRMultiplier(category int) int64 {
	switch category {
	case 1:
		return 50
	case 2:
		return 3
	case 3:
		return 1
	}
	return 0
}

// DivisionOf strips a class 1 compatibility group, so 1.1D is division 1.1.
func DivisionOf(class string) string {
	m := dgClassRe.FindStringSubmatch(class)
	if m == nil {
		return class
	}
	return m[1] + m[2]
}

// CheckDG evaluates a load's dangerous goods (BR-34.15): FAIL for a prohibited
// mixed load or a regulated load without a 24-hour emergency phone; WARN for ADR
// 1.1.3.6 points above 1,000, transport category 0 and limited-quantity marking.
// Limited-quantity lines are left out of segregation and points. Findings follow
// the input order, so the same request gives the same result.
func CheckDG(req DGRequest) (result string, points decimal.Decimal, findings []DGFinding) {
	findings = []DGFinding{}
	points = decimal.Zero
	regulated := false
	for _, l := range req.Lines {
		if l.LimitedQuantity {
			findings = append(findings, DGFinding{RuleID: RuleLimitedQuantity, Severity: DGWarn,
				Detail: l.UNNumber + " is a limited quantity: mark the packages; no further DG requirements"})
			continue
		}
		regulated = true
		if req.Regulation != RegADR {
			continue
		}
		if l.TransportCategory == 0 {
			findings = append(findings, DGFinding{RuleID: RuleADRCategory0, Severity: DGWarn,
				Detail: l.UNNumber + " is transport category 0: full ADR requirements apply whatever the quantity"})
			continue
		}
		points = points.Add(l.Quantity.Mul(decimal.NewFromInt(ADRMultiplier(l.TransportCategory))))
	}
	segRule := RuleSegADR
	if req.Regulation == Reg49CFR {
		segRule = RuleSeg49CFR
	}
	for i := 0; i < len(req.Lines); i++ {
		for j := i + 1; j < len(req.Lines); j++ {
			a, b := req.Lines[i], req.Lines[j]
			if a.LimitedQuantity || b.LimitedQuantity {
				continue
			}
			if prohibited[pairKey(DivisionOf(a.Class), DivisionOf(b.Class))] {
				findings = append(findings, DGFinding{RuleID: segRule, Severity: DGFail, Code: CodeDGSegregation,
					Detail: fmt.Sprintf("Dangerous goods check failed: %s (%s with %s)", segRule, a.Class, b.Class)})
			}
		}
	}
	if regulated && req.EmergencyResponsePhone == "" {
		findings = append(findings, DGFinding{RuleID: RuleEmergencyPhone, Severity: DGFail,
			Detail: "a 24-hour emergency response phone is required before a DG shipment"})
	}
	if req.Regulation == RegADR && points.GreaterThan(decimal.NewFromInt(ADRPointsThreshold)) {
		findings = append(findings, DGFinding{RuleID: RuleADRPoints, Severity: DGWarn, Code: CodeADRPoints,
			Detail: fmt.Sprintf("ADR 1.1.3.6 points %s exceed 1,000; full ADR requirements apply", points.String())})
	}
	result = DGPass
	for _, f := range findings {
		if f.Severity == DGFail {
			result = DGFail
			break
		}
		result = DGWarn
	}
	return result, points, findings
}

// ShippingPapers returns the DG lines for the BOL in input order, each with the
// emergency response phone (FR-34.15).
func ShippingPapers(req DGRequest) []PaperLine {
	out := make([]PaperLine, 0, len(req.Lines))
	for _, l := range req.Lines {
		out = append(out, PaperLine{
			UNNumber:               l.UNNumber,
			ProperShippingName:     strings.TrimSpace(l.ProperShippingName),
			Class:                  l.Class,
			PackingGroup:           l.PackingGroup,
			Quantity:               l.Quantity,
			LimitedQuantity:        l.LimitedQuantity,
			EmergencyResponsePhone: req.EmergencyResponsePhone,
		})
	}
	return out
}

func pairKey(a, b string) string {
	if a > b {
		a, b = b, a
	}
	return a + "|" + b
}
