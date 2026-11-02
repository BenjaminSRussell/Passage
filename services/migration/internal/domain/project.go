package domain

import (
	"strconv"
	"strings"
	"time"
	"unicode/utf8"
)

// Project types and statuses (EN-064, FR-10.14).
const (
	ProjectMigrate       = "migrate"
	ProjectCopy          = "copy"
	ProjectConfigPackage = "config_package"
	ProjectActive        = "active"
	ProjectArchived      = "archived"

	maxProjectName  = 80
	maxRewriteRules = 200
	maxRewriteValue = 100
)

// Project is the unit a copy between legal entities (or a migration, or a package apply) belongs to. Its type,
// source system and legal entities are fixed at creation: the xref lineage of a copy is keyed by the project.
type Project struct {
	Name                string
	Type                string
	SourceSystem        string
	SourceLegalEntityID string
	TargetLegalEntityID string
	// ConnectionProfileID is the connection profile a migrate project reads from (optional).
	ConnectionProfileID string
	Status              string
	CutoverAt           *time.Time
	ScheduleCron        string
}

// ValidateProject returns a message per invalid field; an empty map means the project is valid. isID tells whether
// a legal entity or connection profile id is a well-formed Passage id.
func ValidateProject(p Project, isID func(string) bool) map[string]string {
	bad := map[string]string{}
	if n := strings.TrimSpace(p.Name); n == "" || utf8.RuneCountInString(n) > maxProjectName || n != p.Name {
		bad["name"] = "is required, at most 80 characters, without leading or trailing spaces"
	}
	switch p.Type {
	case ProjectMigrate:
		if p.SourceSystem == "" {
			bad["sourceSystem"] = "is required for a migrate project"
		}
		if p.SourceLegalEntityID != "" {
			bad["sourceLegalEntityId"] = "a migrate project has a target legal entity only; the source legal entity belongs to copy projects"
		}
		if p.TargetLegalEntityID != "" && !isID(p.TargetLegalEntityID) {
			bad["targetLegalEntityId"] = "must be a UUID"
		}
	case ProjectCopy:
		switch {
		case p.SourceLegalEntityID == "" || !isID(p.SourceLegalEntityID):
			bad["sourceLegalEntityId"] = "is required and must be a UUID"
		case p.TargetLegalEntityID == "" || !isID(p.TargetLegalEntityID):
			bad["targetLegalEntityId"] = "is required and must be a UUID"
		case p.SourceLegalEntityID == p.TargetLegalEntityID:
			bad["targetLegalEntityId"] = "must differ from the source legal entity"
		}
		if p.SourceSystem != "" {
			bad["sourceSystem"] = "a copy reads Passage, not a legacy system"
		}
	case ProjectConfigPackage:
		if p.SourceSystem != "" || p.SourceLegalEntityID != "" || p.TargetLegalEntityID != "" {
			bad["projectType"] = "a config_package project has no source system or legal entities"
		}
	default:
		bad["projectType"] = "must be one of migrate, copy, config_package"
	}
	if p.SourceSystem != "" && !ValidSourceSystem(p.SourceSystem) {
		bad["sourceSystem"] = SourceSystemHint
	}
	if p.ConnectionProfileID != "" {
		switch {
		case p.Type != ProjectMigrate:
			bad["connectionProfileId"] = "only a migrate project reads from a connection profile"
		case !isID(p.ConnectionProfileID):
			bad["connectionProfileId"] = "must be a UUID"
		}
	}
	if p.Status != ProjectActive && p.Status != ProjectArchived {
		bad["status"] = "must be active or archived"
	}
	if p.ScheduleCron != "" {
		if gap, ok := CronMinInterval(p.ScheduleCron); !ok {
			bad["scheduleCron"] = "must be a five-field cron expression (minute hour day month weekday) of at most 100 characters"
		} else if gap < MinScheduleMinutes {
			bad["scheduleCron"] = "must leave at least 15 minutes between runs"
		}
	}
	return bad
}

var cronBounds = [5][2]int{{0, 59}, {0, 23}, {1, 31}, {1, 12}, {0, 7}}

// MinScheduleMinutes is the shortest gap FR-10.16 allows between two scheduled runs (`mig.recurring.min_interval_min`
// defaults to 15; the per-tenant setting is not read yet).
const MinScheduleMinutes = 15

const maxCronLength = 100

// CronMinInterval accepts a five-field cron expression of `*`, numbers, ranges, lists and steps within each field's
// bounds (names such as JAN or MON, macros such as @daily and signed numbers are refused) and returns the shortest
// gap in minutes between two firings, measured over minute and hour as if every day ran; false means not valid.
func CronMinInterval(expr string) (int, bool) {
	sets, ok := cronFields(expr)
	if !ok {
		return 0, false
	}
	var times []int
	for h, hOn := range sets[1] {
		for m, mOn := range sets[0] {
			if hOn && mOn {
				times = append(times, h*60+m)
			}
		}
	}
	gap := 24*60 - times[len(times)-1] + times[0]
	for i := 1; i < len(times); i++ {
		gap = min(gap, times[i]-times[i-1])
	}
	return gap, true
}

func cronFields(expr string) ([5][]bool, bool) {
	var sets [5][]bool
	fields := strings.Fields(expr)
	if len(expr) > maxCronLength || len(fields) != 5 {
		return sets, false
	}
	for i, f := range fields {
		set := make([]bool, cronBounds[i][1]+1)
		for _, item := range strings.Split(f, ",") {
			if !cronItem(item, cronBounds[i][0], cronBounds[i][1], set) {
				return sets, false
			}
		}
		sets[i] = set
	}
	return sets, true
}

// cronNumber reads an unsigned decimal of at most two digits.
func cronNumber(s string) (int, bool) {
	if s == "" || len(s) > 2 || strings.Trim(s, "0123456789") != "" {
		return 0, false
	}
	n, err := strconv.Atoi(s)
	return n, err == nil
}

// cronItem marks the values one list item allows in set, or answers false when the item is malformed.
func cronItem(item string, lo, hi int, set []bool) bool {
	base, stepRaw, hasStep := strings.Cut(item, "/")
	step := 1
	if hasStep {
		n, ok := cronNumber(stepRaw)
		if !ok || n < 1 || n > hi {
			return false
		}
		step = n
	}
	from, to := lo, hi
	if base != "*" {
		fromRaw, toRaw, isRange := strings.Cut(base, "-")
		a, ok := cronNumber(fromRaw)
		if !ok || a < lo || a > hi || (!isRange && hasStep) {
			return false
		}
		from, to = a, a
		if isRange {
			b, ok := cronNumber(toRaw)
			if !ok || b < a || b > hi {
				return false
			}
			to = b
		}
	}
	for v := from; v <= to; v += step {
		set[v] = true
	}
	return true
}

// ValidateRewriteRules checks the rules of one copy project: the first rule matching a field wins, so a later rule
// whose prefix starts with an earlier rule's prefix for the same field (USMF after US, or a repeat) could never
// apply and is refused rather than silently ignored.
func ValidateRewriteRules(rules []KeyRewrite) map[string]string {
	bad := map[string]string{}
	if len(rules) > maxRewriteRules {
		bad["rules"] = "at most 200 rules"
		return bad
	}
	for i, r := range rules {
		at := "rules[" + strconv.Itoa(i) + "]"
		switch {
		case strings.TrimSpace(r.Field) == "" || len(r.Field) > maxRewriteValue:
			bad[at+".field"] = "is required (a field name, or " + KeyField + " for the record key), at most 100 characters"
		case r.From == "" || len(r.From) > maxRewriteValue || len(r.To) > maxRewriteValue:
			bad[at+".from"] = "from is required; from and to are at most 100 characters"
		case r.From == r.To:
			bad[at+".to"] = "must differ from the value it replaces"
		default:
			for j, earlier := range rules[:i] {
				if earlier.Field == r.Field && earlier.From != "" && strings.HasPrefix(r.From, earlier.From) {
					bad[at+".from"] = "rule " + strconv.Itoa(j+1) + " already matches this prefix for this field, so this rule would never apply"
					break
				}
			}
		}
	}
	return bad
}
