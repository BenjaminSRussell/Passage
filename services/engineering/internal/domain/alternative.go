package domain

import (
	"sort"
	"time"

	"github.com/shopspring/decimal"
)

const (
	maxGroupLength = 2
	// probabilityScale is the scale of bom_line.usage_probability_pct (numeric(7,4)): a finer value
	// would be rounded on save and the stored group would no longer sum to exactly 100.
	probabilityScale = 4
)

// Alternative is the FR-9.20 alternative item group on a BOM line. A line with an empty Group is not
// in a group and carries none of the other fields.
type Alternative struct {
	Group    string
	Priority int
	Strategy string
	// ProbabilityPct is set only with the usage_probability strategy.
	ProbabilityPct *decimal.Decimal
}

// Discontinuation is the FR-9.20 discontinuation data on a BOM line: the line stops being used on
// DiscontinueOn, alone (single) or together with the rest of its group (simultaneous), and the
// follow-up component takes over.
type Discontinuation struct {
	Group             string
	Type              string
	FollowUpGroup     string
	FollowUpComponent string
	DiscontinueOn     time.Time
}

func (a Alternative) empty() bool {
	return a.Group == "" && a.Priority == 0 && a.Strategy == "" && a.ProbabilityPct == nil
}

func (d Discontinuation) empty() bool {
	return d.Group == "" && d.Type == "" && d.FollowUpGroup == "" && d.FollowUpComponent == "" && d.DiscontinueOn.IsZero()
}

// ValidateLineMeta checks the shape of one line's FR-9.20 fields; the rules that need the other lines
// of the version are in CheckAlternatives.
func ValidateLineMeta(l BomLine) error {
	a, d := l.Alt, l.Discontinuation
	if !a.empty() {
		switch {
		case a.Group == "" || len(a.Group) > maxGroupLength:
			return errf(CodeAltConflict, "Alternative group %q on line %d is inconsistent: the group code is 1 or 2 characters.", a.Group, l.LineNo)
		case a.Strategy != AltPriority && a.Strategy != AltProbability:
			return errf(CodeAltConflict, "Alternative group %s on line %d is inconsistent: the strategy is priority or usage_probability.", a.Group, l.LineNo)
		case a.Priority < 0 || a.Priority > 99:
			return errf(CodeAltConflict, "Alternative group %s on line %d is inconsistent: priority is 1 to 99.", a.Group, l.LineNo)
		case a.Strategy == AltPriority && (a.Priority < 1 || a.ProbabilityPct != nil):
			return errf(CodeAltConflict, "Alternative group %s on line %d is inconsistent: a priority group takes a priority and no probability.", a.Group, l.LineNo)
		case a.Strategy == AltProbability && (a.Priority != 0 || a.ProbabilityPct == nil ||
			!a.ProbabilityPct.IsPositive() || a.ProbabilityPct.GreaterThan(hundred)):
			return errf(CodeAltConflict, "Alternative group %s on line %d is inconsistent: a usage_probability group takes a probability above 0 and up to 100 and no priority.", a.Group, l.LineNo)
		case a.Strategy == AltProbability && !a.ProbabilityPct.Equal(a.ProbabilityPct.Round(probabilityScale)):
			return errf(CodeAltConflict, "Alternative group %s on line %d is inconsistent: a probability has at most %d decimal places.", a.Group, l.LineNo, probabilityScale)
		}
	}
	if !d.empty() {
		switch {
		case d.Type != DiscSingle && d.Type != DiscSimul:
			return errf(CodeBomLine, "line %d: discontinueType must be single or simultaneous", l.LineNo)
		case len(d.Group) > maxGroupLength || len(d.FollowUpGroup) > maxGroupLength:
			return errf(CodeBomLine, "line %d: discontinuation and follow-up groups are at most %d characters", l.LineNo, maxGroupLength)
		case d.Type == DiscSimul && d.Group == "":
			return errf(CodeBomLine, "line %d: a simultaneous discontinuation needs a discontinuationGroup", l.LineNo)
		case d.FollowUpGroup != "" && d.FollowUpComponent == "":
			return errf(CodeBomLine, "line %d: followUpGroup needs a followUpComponentId", l.LineNo)
		}
	}
	return nil
}

// CheckAlternatives applies BR-9.16 across the lines of one version: a usage_probability group sums to
// exactly 100, a priority group has unique priorities, every group has at least two members and one
// strategy, and a line may not be both a member of a group and the follow-up component into that same
// group (a line's followUpGroup and followUpComponentId name the line that joins the group).
func CheckAlternatives(lines []BomLine) error {
	for _, l := range lines {
		if err := ValidateLineMeta(l); err != nil {
			return err
		}
	}
	groups := map[string][]BomLine{}
	for _, l := range lines {
		if l.Alt.Group != "" {
			groups[l.Alt.Group] = append(groups[l.Alt.Group], l)
		}
	}
	names := make([]string, 0, len(groups))
	for g := range groups {
		names = append(names, g)
	}
	sort.Strings(names)
	for _, g := range names {
		members := groups[g]
		if len(members) < 2 {
			return errf(CodeAltConflict, "Alternative group %s is inconsistent: it needs at least two lines.", g)
		}
		sum := decimal.Zero
		priorities := map[int]bool{}
		for _, m := range members {
			switch {
			case m.Alt.Strategy != members[0].Alt.Strategy:
				return errf(CodeAltConflict, "Alternative group %s is inconsistent: lines use different strategies.", g)
			case m.Alt.Strategy == AltProbability:
				sum = sum.Add(*m.Alt.ProbabilityPct)
			case priorities[m.Alt.Priority]:
				return errf(CodeAltConflict, "Alternative group %s is inconsistent: priority %d is used twice.", g, m.Alt.Priority)
			}
			priorities[m.Alt.Priority] = true
		}
		if members[0].Alt.Strategy == AltProbability && !sum.Equal(hundred) {
			return errf(CodeAltConflict, "Alternative group %s is inconsistent: probabilities sum to %s, not 100.", g, sum.String())
		}
	}
	for _, l := range lines {
		d := l.Discontinuation
		if d.FollowUpGroup == "" {
			continue
		}
		for _, m := range groups[d.FollowUpGroup] {
			if m.ComponentProductID == d.FollowUpComponent {
				return errf(CodeAltConflict, "Alternative group %s is inconsistent: %s is both a member and the follow-up component of line %d.", d.FollowUpGroup, d.FollowUpComponent, l.LineNo)
			}
		}
	}
	return nil
}
