package domain

import (
	"fmt"
	"sort"
	"time"
)

// MaxSkillScale bounds the rating scale a skill may declare (FR-42.10).
const MaxSkillScale = 10

// DefaultSkillScale is the scale used when a skill does not state one.
const DefaultSkillScale = 5

// CodeSkillRating is raised when a rating or requirement lies outside the
// skill's scale (FR-42.10). It is not in the 8.42.18 catalog; the service
// README lists it.
const CodeSkillRating = "HCM-SKL-RATING"

// Requirement is a skill with the minimum rating a position or search needs
// (FR-42.18).
type Requirement struct {
	Skill     string
	MinRating int
}

// GapRow compares one requirement with a worker's rating (FR-42.18): the PRD's
// (skill, required, actual, delta). Delta is Actual minus Required, so a
// negative delta is a shortfall.
type GapRow struct {
	Skill    string `json:"skill"`
	Required int    `json:"required"`
	Actual   int    `json:"actual"`
	Delta    int    `json:"delta"`
}

// ValidateScale checks a skill's declared scale.
func ValidateScale(scaleMax int) error {
	if scaleMax < 1 || scaleMax > MaxSkillScale {
		return &Error{Code: CodeSkillRating, Detail: fmt.Sprintf("the scale must be between 1 and %d", MaxSkillScale)}
	}
	return nil
}

// ValidateRating checks a rating against the skill scale: ratings run from 1 up
// to scaleMax. A worker without the skill has no row, which reads as 0.
func ValidateRating(rating, scaleMax int) error {
	if err := ValidateScale(scaleMax); err != nil {
		return err
	}
	if rating < 1 || rating > scaleMax {
		return &Error{Code: CodeSkillRating, Detail: fmt.Sprintf("the rating must be between 1 and %d on this skill's scale", scaleMax)}
	}
	return nil
}

// Normalize collapses duplicate requirements to the highest minimum per skill
// and sorts them by skill code, so rows come out in a stable order.
func Normalize(reqs []Requirement) []Requirement {
	best := map[string]int{}
	for _, r := range reqs {
		if cur, ok := best[r.Skill]; !ok || r.MinRating > cur {
			best[r.Skill] = r.MinRating
		}
	}
	out := make([]Requirement, 0, len(best))
	for skill, min := range best {
		out = append(out, Requirement{Skill: skill, MinRating: min})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Skill < out[j].Skill })
	return out
}

// Compare lists every requirement next to the worker's rating, met or not. A
// skill missing from actual counts as rating 0.
func Compare(required []Requirement, actual map[string]int) []GapRow {
	rows := []GapRow{}
	for _, r := range Normalize(required) {
		have := actual[r.Skill]
		rows = append(rows, GapRow{Skill: r.Skill, Required: r.MinRating, Actual: have, Delta: have - r.MinRating})
	}
	return rows
}

// Gap lists only the requirements the worker does not meet (AC-42.16).
func Gap(required []Requirement, actual map[string]int) []GapRow {
	rows := []GapRow{}
	for _, row := range Compare(required, actual) {
		if row.Delta < 0 {
			rows = append(rows, row)
		}
	}
	return rows
}

// CertificationExpiry returns when a certificate issued on completed expires
// (BR-42.9): completion plus validityMonths. A completion on a day the target
// month does not have (31 January + 1 month) expires on that month's last day,
// never in the month after. Zero or negative validity means the certificate
// does not expire and nil is returned.
func CertificationExpiry(completed time.Time, validityMonths int) *time.Time {
	if validityMonths <= 0 {
		return nil
	}
	d := AsOfDay(completed)
	firstOfTarget := time.Date(d.Year(), d.Month(), 1, 0, 0, 0, 0, time.UTC).AddDate(0, validityMonths, 0)
	lastDay := firstOfTarget.AddDate(0, 1, -1).Day()
	day := d.Day()
	if day > lastDay {
		day = lastDay
	}
	t := time.Date(firstOfTarget.Year(), firstOfTarget.Month(), day, 0, 0, 0, 0, time.UTC)
	return &t
}

// CertificationValid reports whether a certificate is valid on asOf: issued on
// or before that day and not yet expired. The expiry day is exclusive, so a
// certificate expiring on the 10th is no longer valid on it (BR-42.8).
func CertificationValid(issued time.Time, expires *time.Time, asOf time.Time) bool {
	day := AsOfDay(asOf)
	if day.Before(AsOfDay(issued)) {
		return false
	}
	return expires == nil || day.Before(AsOfDay(*expires))
}
