package domain

import "unicode/utf8"

const (
	IncidentReported    = "REPORTED"
	IncidentUnderReview = "UNDER_REVIEW"
	IncidentClosed      = "CLOSED"

	OutcomeDeath      = "DEATH"
	OutcomeDaysAway   = "DAYS_AWAY"
	OutcomeRestricted = "RESTRICTED"
	OutcomeOther      = "OTHER_RECORDABLE"
	OutcomeNone       = "NONE"

	CodeIncidentRecordable = "HCM-INC-001"
	CodeIncidentState      = "HCM-INC-STATE"
	CodeIncidentSelfClose  = "HCM-INC-SELF-CLOSE"

	MinOverrideReason = 10
)

var BodyParts = []string{"HEAD", "EYE", "EAR", "FACE", "NECK", "SHOULDER", "ARM", "ELBOW", "WRIST", "HAND", "FINGER", "CHEST", "BACK", "ABDOMEN",
	"HIP", "LEG", "KNEE", "ANKLE", "FOOT", "TOE", "INTERNAL", "MULTIPLE", "OTHER"}

var IncidentTypes = []string{"INJURY", "SKIN_DISORDER", "RESPIRATORY", "POISONING", "HEARING_LOSS", "OTHER_ILLNESS"}

// IncidentFacts are the facts BR-42.11 reads (29 CFR 1904.7).
type IncidentFacts struct {
	Death, LossOfConsciousness, MedicalTreatment bool
	DaysAway, DaysRestricted                     int
}

// Outcome is the most severe outcome the facts show, in OSHA 300 order.
func (f IncidentFacts) Outcome() string {
	switch {
	case f.Death:
		return OutcomeDeath
	case f.DaysAway > 0:
		return OutcomeDaysAway
	case f.DaysRestricted > 0:
		return OutcomeRestricted
	case f.MedicalTreatment || f.LossOfConsciousness:
		return OutcomeOther
	default:
		return OutcomeNone
	}
}

// Recordable is the BR-42.11 default for osha_recordable.
func (f IncidentFacts) Recordable() bool { return f.Outcome() != OutcomeNone }

// ResolveRecordable applies the default, or the caller's override. Overriding
// the default either way needs a justification (HCM-INC-001).
func ResolveRecordable(f IncidentFacts, override *bool, reason string) (bool, *Error) {
	computed := f.Recordable()
	if override == nil || *override == computed {
		return computed, nil
	}
	if utf8.RuneCountInString(reason) < MinOverrideReason {
		return false, &Error{Code: CodeIncidentRecordable, Detail: "the facts make this incident recordable=" + boolText(computed) +
			"; overriding it needs a justification of at least 10 characters"}
	}
	return *override, nil
}

func boolText(b bool) string {
	if b {
		return "true"
	}
	return "false"
}

// NextIncidentStatus allows REPORTED to UNDER_REVIEW to CLOSED. The person who
// reported an incident cannot close it (8.42.17).
func NextIncidentStatus(from, to, reportedBy, actor string) *Error {
	switch {
	case from == IncidentReported && to == IncidentUnderReview, from == IncidentUnderReview && to == IncidentClosed:
	default:
		return &Error{Code: CodeIncidentState, Detail: "an incident goes REPORTED, UNDER_REVIEW, CLOSED in that order; this one is " + from}
	}
	if to == IncidentClosed && reportedBy == actor {
		return &Error{Code: CodeIncidentSelfClose, Detail: "an administrator cannot close an incident they reported"}
	}
	return nil
}

// OSHARow is the part of an incident the 300A summary counts.
type OSHARow struct {
	Outcome, IncidentType    string
	DaysAway, DaysRestricted int
}

// OSHASummary is the 300A totals (29 CFR 1904.32): cases by outcome, days, and
// cases by injury or illness type.
type OSHASummary struct {
	Deaths              int            `json:"deaths"`
	DaysAwayCases       int            `json:"daysAwayCases"`
	RestrictedCases     int            `json:"restrictedCases"`
	OtherRecordable     int            `json:"otherRecordableCases"`
	TotalDaysAway       int            `json:"totalDaysAway"`
	TotalDaysRestricted int            `json:"totalDaysRestricted"`
	ByType              map[string]int `json:"byType"`
}

func Summarize(rows []OSHARow) OSHASummary {
	s := OSHASummary{ByType: map[string]int{}}
	for _, t := range IncidentTypes {
		s.ByType[t] = 0
	}
	for _, r := range rows {
		switch r.Outcome {
		case OutcomeDeath:
			s.Deaths++
		case OutcomeDaysAway:
			s.DaysAwayCases++
		case OutcomeRestricted:
			s.RestrictedCases++
		default:
			s.OtherRecordable++
		}
		s.TotalDaysAway += r.DaysAway
		s.TotalDaysRestricted += r.DaysRestricted
		s.ByType[r.IncidentType]++
	}
	return s
}
