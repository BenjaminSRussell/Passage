package domain

import (
	"maps"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/BenjaminSRussell/passage/platform/ids"
)

// Stability study statuses (FR-33.16).
const (
	StudyPlanned    = "PLANNED"
	StudyActive     = "ACTIVE"
	StudyCompleted  = "COMPLETED"
	StudyTerminated = "TERMINATED"
)

// Pull point statuses. Every pull starts PLANNED; the scheduling workflow moves it
// on. Terminating a study cancels the pulls still PLANNED.
const (
	PullPlanned   = "PLANNED"
	PullOrdered   = "ORDERED"
	PullPulled    = "PULLED"
	PullMissed    = "MISSED"
	PullCancelled = "CANCELLED"
)

// MaxStudyReason bounds the reason recorded with a study status change.
const MaxStudyReason = 500

// Defaults for the study inputs the API fills in when the caller omits them
// (parameters stability_lead_days and stability_pull_window_days).
const (
	DefaultStudyLeadDays   = 7
	DefaultStudyWindowDays = 3
)

const (
	maxPullMonths = 120
	maxPullPoints = 40
	maxLeadDays   = 30
	maxWindowDays = 14
)

// ValidStudyStatus reports whether s is a study status.
func ValidStudyStatus(s string) bool {
	return s == StudyPlanned || s == StudyActive || s == StudyCompleted || s == StudyTerminated
}

// CanTransitionStudy reports whether a study may move from -> to:
// PLANNED -> ACTIVE | TERMINATED and ACTIVE -> COMPLETED | TERMINATED.
func CanTransitionStudy(from, to string) bool {
	switch from {
	case StudyPlanned:
		return to == StudyActive || to == StudyTerminated
	case StudyActive:
		return to == StudyCompleted || to == StudyTerminated
	}
	return false
}

// Pull is one scheduled pull point: the calendar date of the pull, the date its
// quality order is due to be created (stability_lead_days before, BR-33.14) and
// the test group the pulled samples are tested with.
type Pull struct {
	Months      int       `json:"months"`
	PullDate    time.Time `json:"pullDate"`
	OrderDate   time.Time `json:"orderDate"`
	TestGroupID string    `json:"testGroupId"`
	Status      string    `json:"status"`
}

// AddMonthsClamped adds whole months to a date keeping the day of month, clamped
// to the last day of the target month (31 Jan + 3 months = 30 Apr).
func AddMonthsClamped(day time.Time, months int) time.Time {
	y, m, d := day.UTC().Date()
	first := time.Date(y, m+time.Month(months), 1, 0, 0, 0, 0, time.UTC)
	last := first.AddDate(0, 1, -1).Day()
	if d > last {
		d = last
	}
	return time.Date(first.Year(), first.Month(), d, 0, 0, 0, 0, time.UTC)
}

// PullSchedule generates the pull dates for a study starting on start. Every pull
// counts from the start date, not from the previous pull, so clamping never drifts.
// months must be strictly ascending, each between 0 and 120.
func PullSchedule(start time.Time, months []int, leadDays int) ([]Pull, error) {
	if len(months) == 0 || len(months) > maxPullPoints {
		return nil, invalid("a study needs 1 to 40 pull points")
	}
	if leadDays < 0 || leadDays > maxLeadDays {
		return nil, invalid("stability_lead_days must be 0 to 30")
	}
	prev := -1
	for _, m := range months {
		if m <= prev || m < 0 || m > maxPullMonths {
			return nil, invalid("pull points must be strictly ascending months from 0 to 120")
		}
		prev = m
	}
	base := dateOnly(start)
	out := make([]Pull, 0, len(months))
	for _, m := range months {
		pd := AddMonthsClamped(base, m)
		out = append(out, Pull{Months: m, PullDate: pd, OrderDate: pd.AddDate(0, 0, -leadDays), Status: PullPlanned})
	}
	return out, nil
}

// LatePull reports the days a pull was recorded after its date and whether that
// exceeds the window (deviation, QM-0126). Days are calendar days; a pull recorded
// early returns a negative number.
func LatePull(pull, recorded time.Time, windowDays int) (days int, deviation bool, err error) {
	if windowDays < 0 || windowDays > maxWindowDays {
		return 0, false, invalid("stability_pull_window_days must be 0 to 14")
	}
	days = dayNumber(recorded) - dayNumber(pull)
	return days, days > windowDays, nil
}

// StudySpec holds the input of a stability study (FR-33.16). TestGroupID tests
// every pull point unless PullTestGroups names another group for that month.
type StudySpec struct {
	ItemID         string         `json:"itemId"`
	BatchID        string         `json:"batchId"`
	Protocol       string         `json:"protocol"`
	Condition      string         `json:"condition"`
	StartDate      time.Time      `json:"startDate"`
	TestGroupID    string         `json:"testGroupId"`
	PullMonths     []int          `json:"pullMonths"`
	PullTestGroups map[int]string `json:"pullTestGroups,omitempty"`
	LeadDays       int            `json:"leadDays"`
	WindowDays     int            `json:"windowDays"`
}

// TestGroupIDs returns the distinct test groups a study uses, sorted.
func (s StudySpec) TestGroupIDs() []string {
	set := map[string]bool{s.TestGroupID: true}
	for _, id := range s.PullTestGroups {
		set[id] = true
	}
	return slices.Sorted(maps.Keys(set))
}

// ValidateStudy returns field errors for a study, empty when valid.
func ValidateStudy(s StudySpec) map[string]string {
	fields := map[string]string{}
	if !ids.Valid(s.ItemID) {
		fields["itemId"] = "must be a UUID"
	}
	if b := strings.TrimSpace(s.BatchID); b == "" || len(b) > 60 {
		fields["batchId"] = "must be 1 to 60 characters"
	}
	if p := strings.TrimSpace(s.Protocol); p == "" || len(p) > 120 {
		fields["protocol"] = "must be 1 to 120 characters"
	}
	if c := strings.TrimSpace(s.Condition); c == "" || len(c) > 60 {
		fields["condition"] = "must be 1 to 60 characters, such as 25C/60RH"
	}
	if s.StartDate.IsZero() {
		fields["startDate"] = "is required"
	}
	if !ids.Valid(s.TestGroupID) {
		fields["testGroupId"] = "must be a UUID"
	}
	if len(s.PullMonths) == 0 || len(s.PullMonths) > maxPullPoints {
		fields["pullMonths"] = "must hold 1 to 40 pull points"
	} else {
		prev := -1
		for _, m := range s.PullMonths {
			if m <= prev || m < 0 || m > maxPullMonths {
				fields["pullMonths"] = "must be strictly ascending months from 0 to 120"
				break
			}
			prev = m
		}
	}
	for m, id := range s.PullTestGroups {
		key := "pullTestGroups." + strconv.Itoa(m)
		switch {
		case !slices.Contains(s.PullMonths, m):
			fields[key] = "is not one of the pull points"
		case !ids.Valid(id):
			fields[key] = "must be a UUID"
		}
	}
	if s.LeadDays < 0 || s.LeadDays > maxLeadDays {
		fields["leadDays"] = "must be 0 to 30"
	}
	if s.WindowDays < 0 || s.WindowDays > maxWindowDays {
		fields["windowDays"] = "must be 0 to 14"
	}
	return fields
}

// Study is a stability study with its generated pull schedule (FR-33.16). A new
// study starts PLANNED at version 1; its pulls are fixed when it is created (BR-33.14).
// StatusReason, StatusChangedBy and StatusChangedAt describe the last status change.
type Study struct {
	ID              string     `json:"id"`
	Status          string     `json:"status"`
	Version         int        `json:"version"`
	CreatedAt       time.Time  `json:"createdAt"`
	StatusReason    string     `json:"statusReason,omitempty"`
	StatusChangedBy string     `json:"statusChangedBy,omitempty"`
	StatusChangedAt *time.Time `json:"statusChangedAt,omitempty"`
	StudySpec
	Pulls []Pull `json:"pulls"`
}

// Transition moves the study to status to (FR-33.16: PLANNED -> ACTIVE ->
// COMPLETED, or TERMINATED from PLANNED or ACTIVE). TERMINATED needs a reason and
// cancels the pulls still PLANNED, so the scheduling workflow creates no more
// orders; ORDERED pulls keep their quality orders. COMPLETED and TERMINATED are final.
func (s Study) Transition(to, reason, user string, now time.Time) (Study, error) {
	if !ValidStudyStatus(to) || to == StudyPlanned {
		return s, invalid("to must be ACTIVE, COMPLETED or TERMINATED")
	}
	reason = strings.TrimSpace(reason)
	if len(reason) > MaxStudyReason {
		return s, invalid("a status reason must be 500 characters or fewer")
	}
	if !CanTransitionStudy(s.Status, to) {
		return s, fail(CodeStudyStatus, "a stability study cannot move from "+s.Status+" to "+to)
	}
	if to == StudyTerminated && reason == "" {
		return s, invalid("terminating a stability study needs a reason")
	}
	at := now.UTC()
	s.Status, s.StatusReason, s.StatusChangedBy, s.StatusChangedAt = to, reason, user, &at
	if to == StudyTerminated {
		pulls := make([]Pull, len(s.Pulls))
		for i, p := range s.Pulls {
			if p.Status == PullPlanned {
				p.Status = PullCancelled
			}
			pulls[i] = p
		}
		s.Pulls = pulls
	}
	return s, nil
}

// NewStudy validates a study request and generates its pull schedule, each pull
// carrying its test group.
func NewStudy(spec StudySpec) (Study, error) {
	if fields := ValidateStudy(spec); len(fields) > 0 {
		return Study{}, invalid("stability study is invalid: " + strings.Join(slices.Sorted(maps.Keys(fields)), ", "))
	}
	spec.BatchID, spec.Protocol, spec.Condition = strings.TrimSpace(spec.BatchID), strings.TrimSpace(spec.Protocol), strings.TrimSpace(spec.Condition)
	spec.StartDate = dateOnly(spec.StartDate)
	pulls, err := PullSchedule(spec.StartDate, spec.PullMonths, spec.LeadDays)
	if err != nil {
		return Study{}, err
	}
	for i := range pulls {
		pulls[i].TestGroupID = spec.TestGroupID
		if id, ok := spec.PullTestGroups[pulls[i].Months]; ok {
			pulls[i].TestGroupID = id
		}
	}
	return Study{Status: StudyPlanned, Version: 1, StudySpec: spec, Pulls: pulls}, nil
}

func dateOnly(t time.Time) time.Time {
	y, m, d := t.UTC().Date()
	return time.Date(y, m, d, 0, 0, 0, 0, time.UTC)
}

func dayNumber(t time.Time) int {
	return int(dateOnly(t).Unix() / 86400)
}
