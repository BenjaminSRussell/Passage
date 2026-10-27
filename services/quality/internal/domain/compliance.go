package domain

import (
	"regexp"
	"slices"
	"strconv"
	"strings"

	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/platform/ids"
)

// Compliance list kinds and block modes (FR-33.13, parameter compliance_block_mode).
const (
	ListRestricted = "RESTRICTED"
	ListReporting  = "REPORTING"
	BlockModeBlock = "BLOCK"
	BlockModeWarn  = "WARN"
)

// Result severities of a compliance evaluation.
const (
	SeverityError   = "error"
	SeverityWarning = "warning"
)

const (
	maxBOMDepth   = 20
	maxListLimits = 2000
	maxRegions    = 60
	maxListName   = 120
	// MaxCompositionEntries bounds the substances and parts declared on one item.
	MaxCompositionEntries = 500
)

var (
	listCode   = regexp.MustCompile(`^[A-Z0-9][A-Z0-9_-]{1,31}$`)
	regionCode = regexp.MustCompile(`^[A-Z][A-Z0-9-]{1,9}$`)
	casFormat  = regexp.MustCompile(`^(\d{2,7})-(\d{2})-(\d)$`)
)

// ValidCAS reports whether s is a CAS registry number with a correct check digit:
// the digits before the check digit, weighted 1, 2, 3, ... from the right, sum to
// the check digit modulo 10 (7439-92-1 is lead).
func ValidCAS(s string) bool {
	m := casFormat.FindStringSubmatch(s)
	if m == nil {
		return false
	}
	body := m[1] + m[2]
	sum := 0
	for i := 0; i < len(body); i++ {
		sum += int(body[len(body)-1-i]-'0') * (i + 1)
	}
	return sum%10 == int(m[3][0]-'0')
}

// ValidRegion reports whether r is a region code: a country (DE), a subdivision
// (US-CA) or a region group (EU), in capitals.
func ValidRegion(r string) bool { return regionCode.MatchString(r) }

// LimitLine is one substance on a list with its threshold in percent w/w. A
// substance is over the limit when its concentration is strictly greater (BR-33.11).
type LimitLine struct {
	CAS          string          `json:"cas"`
	Name         string          `json:"name,omitempty"`
	ThresholdPct decimal.Decimal `json:"thresholdPct"`
}

// ComplianceList is one immutable version of a regulated, restricted or reporting
// list (REACH SVHC, RoHS Annex II, Proposition 65, TSCA). The store numbers the
// versions of a code 1, 2, 3, ...; the highest version is the one evaluated.
type ComplianceList struct {
	ID        string      `json:"id"`
	Code      string      `json:"code"`
	Version   int         `json:"version"`
	Name      string      `json:"name"`
	Kind      string      `json:"kind"`
	BlockMode string      `json:"blockMode"`
	Regions   []string    `json:"regions"`
	Limits    []LimitLine `json:"limits"`
}

// ValidateComplianceList returns field errors, empty when valid. The version is
// assigned by the store and is not checked.
func ValidateComplianceList(l ComplianceList) map[string]string {
	fields := map[string]string{}
	if !listCode.MatchString(l.Code) {
		fields["code"] = "must be 2 to 32 capital letters, digits, _ or -"
	}
	if len(l.Name) > maxListName {
		fields["name"] = "must be at most 120 characters"
	}
	if l.Kind != ListRestricted && l.Kind != ListReporting {
		fields["kind"] = "must be RESTRICTED or REPORTING"
	}
	if l.BlockMode != BlockModeBlock && l.BlockMode != BlockModeWarn {
		fields["blockMode"] = "must be BLOCK or WARN"
	}
	if len(l.Regions) == 0 || len(l.Regions) > maxRegions {
		fields["regions"] = "must name 1 to 60 regions"
	}
	seenRegion := map[string]bool{}
	for i, r := range l.Regions {
		key := "regions[" + strconv.Itoa(i) + "]"
		switch {
		case !ValidRegion(r):
			fields[key] = "must be a region code such as EU, DE or US-CA"
		case seenRegion[r]:
			fields[key] = "is listed twice"
		}
		seenRegion[r] = true
	}
	if len(l.Limits) == 0 || len(l.Limits) > maxListLimits {
		fields["limits"] = "must list 1 to 2000 substances"
	}
	seenCAS := map[string]bool{}
	hundred := decimal.NewFromInt(100)
	for i, lim := range l.Limits {
		key := "limits[" + strconv.Itoa(i) + "]"
		switch {
		case !ValidCAS(lim.CAS):
			fields[key+".cas"] = "must be a CAS number with a valid check digit, such as 7439-92-1"
		case seenCAS[lim.CAS]:
			fields[key+".cas"] = "is listed twice"
		}
		seenCAS[lim.CAS] = true
		if len(lim.Name) > maxListName {
			fields[key+".name"] = "must be at most 120 characters"
		}
		if !lim.ThresholdPct.IsPositive() || lim.ThresholdPct.GreaterThan(hundred) {
			fields[key+".thresholdPct"] = "must be above 0 and at most 100"
		}
	}
	return fields
}

// LatestLists keeps the highest version of each list code, ordered by code: only
// current versions are evaluated.
func LatestLists(all []ComplianceList) []ComplianceList {
	latest := map[string]ComplianceList{}
	for _, l := range all {
		if cur, ok := latest[l.Code]; !ok || l.Version > cur.Version {
			latest[l.Code] = l
		}
	}
	out := make([]ComplianceList, 0, len(latest))
	for _, l := range latest {
		out = append(out, l)
	}
	slices.SortFunc(out, func(a, b ComplianceList) int { return strings.Compare(a.Code, b.Code) })
	return out
}

// MassPart is a BOM component and its mass fraction of the parent item (0 to 1).
type MassPart struct {
	ItemID       string          `json:"itemId"`
	MassFraction decimal.Decimal `json:"massFraction"`
}

// Constituent is one item for composition roll-up: substances declared directly on
// it by CAS number (mass fraction w/w, 0 to 1) and its BOM parts with their mass
// fractions. Until the wave-9 BOM feeds it, the parts are declared here.
type Constituent struct {
	ItemID string                     `json:"itemId"`
	Direct map[string]decimal.Decimal `json:"direct"`
	Parts  []MassPart                 `json:"parts"`
}

// Composition is one stored, immutable version of an item's declared composition
// (FR-33.13). Versions are appended; the highest version is the current one.
type Composition struct {
	Version int `json:"version"`
	Constituent
}

// ValidateConstituent returns field errors for an item's declared composition,
// empty when valid. Substances are CAS numbers with a fraction w/w in [0, 1]; parts
// are other items (not the item itself, each once) whose fractions sum to at most 1.
func ValidateConstituent(c Constituent) map[string]string {
	fields := map[string]string{}
	if len(c.Direct)+len(c.Parts) == 0 {
		fields["direct"] = "declare at least one substance or part"
	}
	if len(c.Direct)+len(c.Parts) > MaxCompositionEntries {
		fields["parts"] = "an item declares at most 500 substances and parts"
	}
	one := decimal.NewFromInt(1)
	direct := decimal.Zero
	for cas, v := range c.Direct {
		if !ValidCAS(cas) {
			fields["direct."+cas] = "must be keyed by a CAS number with a valid check digit"
			continue
		}
		if v.IsNegative() || v.GreaterThan(one) {
			fields["direct."+cas] = "must be a fraction from 0 to 1"
		}
		direct = direct.Add(v)
	}
	if direct.GreaterThan(one) {
		fields["direct"] = "substance fractions must sum to 1 or less"
	}
	sum := decimal.Zero
	seen := map[string]bool{}
	for i, p := range c.Parts {
		key := "parts[" + strconv.Itoa(i) + "]"
		switch {
		case !ids.Valid(p.ItemID):
			fields[key+".itemId"] = "must be a UUID"
		case p.ItemID == c.ItemID:
			fields[key+".itemId"] = "an item cannot be a part of itself"
		case seen[p.ItemID]:
			fields[key+".itemId"] = "is listed twice"
		}
		seen[p.ItemID] = true
		if !p.MassFraction.IsPositive() || p.MassFraction.GreaterThan(one) {
			fields[key+".massFraction"] = "must be above 0 and at most 1"
		}
		sum = sum.Add(p.MassFraction)
	}
	if sum.GreaterThan(one) {
		fields["parts"] = "mass fractions must sum to 1 or less"
	}
	return fields
}

// Rollup computes the concentration of every substance in root (BR-33.11):
// c_s(i) = declared(i, s) + Σ mass_fraction(k) × c_s(k), applied recursively. A
// cycle, a missing item or a BOM deeper than 20 levels is rejected.
func Rollup(root string, nodes map[string]Constituent) (map[string]decimal.Decimal, error) {
	return rollup(root, nodes, map[string]bool{}, 0)
}

func rollup(id string, nodes map[string]Constituent, onPath map[string]bool, depth int) (map[string]decimal.Decimal, error) {
	n, ok := nodes[id]
	if !ok {
		return nil, invalid("composition: unknown item " + id)
	}
	if onPath[id] {
		return nil, fail(CodeCompositionCycle, "the bill of materials has a cycle through item "+id)
	}
	if depth > maxBOMDepth {
		return nil, fail(CodeCompositionTooLarge, "the bill of materials is deeper than "+strconv.Itoa(maxBOMDepth)+" levels")
	}
	onPath[id] = true
	defer delete(onPath, id)

	one := decimal.NewFromInt(1)
	out := map[string]decimal.Decimal{}
	for cas, c := range n.Direct {
		if c.IsNegative() || c.GreaterThan(one) {
			return nil, invalid("composition: concentration of " + cas + " on " + id + " must be 0 to 1")
		}
		out[cas] = c
	}
	sum := decimal.Zero
	for _, p := range n.Parts {
		if p.MassFraction.IsNegative() || p.MassFraction.GreaterThan(one) {
			return nil, invalid("composition: mass fraction of " + p.ItemID + " must be 0 to 1")
		}
		sum = sum.Add(p.MassFraction)
		sub, err := rollup(p.ItemID, nodes, onPath, depth+1)
		if err != nil {
			return nil, err
		}
		for cas, c := range sub {
			out[cas] = out[cas].Add(c.Mul(p.MassFraction))
		}
	}
	if sum.GreaterThan(one) {
		return nil, invalid("composition: BOM mass fractions of " + id + " exceed 1")
	}
	return out, nil
}

// Hit is one substance over its threshold.
type Hit struct {
	CAS          string          `json:"cas"`
	Name         string          `json:"name,omitempty"`
	PercentWW    decimal.Decimal `json:"percentWw"`
	ThresholdPct decimal.Decimal `json:"thresholdPct"`
}

// ComplianceResult is the evaluation of one item against one list version for one
// region. Code is QM-0121 (reporting hit, warning) or QM-0122 (restricted hit,
// error under BLOCK, warning under WARN); empty when nothing is over its threshold.
// Version numbers the stored results of (item, list version, region); the store
// adds a version only when the outcome changes.
type ComplianceResult struct {
	ListCode    string `json:"listCode"`
	ListVersion int    `json:"listVersion"`
	ListKind    string `json:"listKind"`
	Region      string `json:"region"`
	Applies     bool   `json:"applies"`
	Hits        []Hit  `json:"hits"`
	Restricted  bool   `json:"restricted"`
	Code        string `json:"code,omitempty"`
	Severity    string `json:"severity,omitempty"`
	Version     int    `json:"version,omitempty"`
}

// SameOutcome reports whether two results of the same list and region carry the
// same verdict and hits, so a re-evaluation need not store a new version.
func (r ComplianceResult) SameOutcome(o ComplianceResult) bool {
	if r.Restricted != o.Restricted || r.Code != o.Code || r.Severity != o.Severity || len(r.Hits) != len(o.Hits) {
		return false
	}
	for i := range r.Hits {
		a, b := r.Hits[i], o.Hits[i]
		if a.CAS != b.CAS || !a.PercentWW.Equal(b.PercentWW) || !a.ThresholdPct.Equal(b.ThresholdPct) {
			return false
		}
	}
	return true
}

// EvaluateList checks an item's composition (conc, fractions w/w) against a list
// for a destination region. The list applies only to the regions it names; the
// caller expands a country to its region groups.
func EvaluateList(l ComplianceList, region string, conc map[string]decimal.Decimal) ComplianceResult {
	res := ComplianceResult{ListCode: l.Code, ListVersion: l.Version, ListKind: l.Kind, Region: region, Hits: []Hit{}}
	res.Applies = slices.Contains(l.Regions, region)
	if !res.Applies {
		return res
	}
	hundred := decimal.NewFromInt(100)
	for _, lim := range l.Limits {
		c, ok := conc[lim.CAS]
		if !ok {
			continue
		}
		pct := c.Mul(hundred)
		if pct.GreaterThan(lim.ThresholdPct) {
			res.Hits = append(res.Hits, Hit{CAS: lim.CAS, Name: lim.Name, PercentWW: pct, ThresholdPct: lim.ThresholdPct})
		}
	}
	if len(res.Hits) == 0 {
		return res
	}
	if l.Kind == ListReporting {
		res.Code, res.Severity = CodeReportingHit, SeverityWarning
		return res
	}
	res.Restricted = true
	res.Code = CodeRestricted
	res.Severity = SeverityError
	if l.BlockMode == BlockModeWarn {
		res.Severity = SeverityWarning
	}
	return res
}

// EvaluateItem evaluates the composition of root against every list that applies
// to region (BR-33.11). nodes must hold root and every item reachable through its
// BOM. Lists that do not apply to region are omitted from the result.
func EvaluateItem(root string, nodes map[string]Constituent, lists []ComplianceList, region string) ([]ComplianceResult, error) {
	if !ValidRegion(region) {
		return nil, invalid("region must be a region code such as EU, DE or US-CA")
	}
	conc, err := Rollup(root, nodes)
	if err != nil {
		return nil, err
	}
	out := []ComplianceResult{}
	for _, l := range lists {
		if res := EvaluateList(l, region, conc); res.Applies {
			out = append(out, res)
		}
	}
	return out, nil
}
