package domain

import (
	"sort"
	"time"

	"github.com/shopspring/decimal"
)

// Job dispatch rules (FR-25.3, BR-25.1, BR-25.7). A worker has at most one open
// unit of work: one job, or the jobs of one bundle started together. Starting
// another unit auto-stops the open one (site setting "Auto-stop previous job",
// default true).

// Catalog warnings returned with a registration result.
const (
	// CodeJobAutoStopped is SF-0002: the previous job was stopped automatically.
	CodeJobAutoStopped = "SF-0002"
	// CodeJobFinishedOffline is SF-0004: a job-start arrived for a job the server
	// had already finished (BR-25.7, flag ConflictJobFinished).
	CodeJobFinishedOffline = "SF-0004"
)

// Flags on server-written job-stop registrations.
const (
	// FlagAutoStopped marks a job-stop the server wrote because the worker started
	// another job (FR-25.3).
	FlagAutoStopped = "auto-stopped"
	// FlagAutoStopCapped marks an auto-stop of a job open for more than 24 hours:
	// the stop is put at 24 hours after the start (the registration limit) and a
	// supervisor reviews it.
	FlagAutoStopCapped = "auto-stop-capped"
	// FlagProductionFinished marks a job-stop the server wrote because production (w21)
	// finished the operation while the worker's start was still open (FR-25.3).
	FlagProductionFinished = "production-finished"
)

// MaxJobHours is the longest a single registration may run (hours ≤ 24.00).
const MaxJobHours = 24 * time.Hour

// OpenStart is a job-start with no stoppedAt and no later job-stop for its job.
// BundleID is empty when the start is not part of a bundle. QtyGood is the good
// quantity carried on the start registration (BR-25.1 split basis).
type OpenStart struct {
	RegistrationID string
	JobID          string
	BundleID       string
	StartedAt      time.Time
	QtyGood        decimal.Decimal
}

// StopUnit is one unit of work an incoming job-start closes: a single job, or
// the open members of one bundle, stopped together at StoppedAt.
type StopUnit struct {
	BundleID string
	// Members are in start order; each member is closed from its own start.
	Members   []OpenStart
	StoppedAt time.Time
	// Capped is set when the unit was open for more than MaxJobHours.
	Capped bool
}

// StartedAt is the earliest start of the unit's members.
func (u StopUnit) StartedAt() time.Time {
	first := u.Members[0].StartedAt
	for _, m := range u.Members[1:] {
		if m.StartedAt.Before(first) {
			first = m.StartedAt
		}
	}
	return first
}

// Hours returns each member's hours in member order (BR-25.1). A single job gets
// its elapsed time from its start to the stop. A bundle's elapsed time runs from
// its earliest start and is split by the good quantity on the members' start
// registrations, or equally when none is reported, in 0.01 h shares.
func (u StopUnit) Hours() ([]decimal.Decimal, error) {
	if u.BundleID == "" {
		out := make([]decimal.Decimal, len(u.Members))
		for i, m := range u.Members {
			out[i] = ElapsedHours(m.StartedAt, u.StoppedAt)
		}
		return out, nil
	}
	members := make([]BundleMember, len(u.Members))
	for i, m := range u.Members {
		members[i] = BundleMember{JobID: m.JobID, QtyGood: m.QtyGood}
	}
	return SplitBundleHours(ElapsedHours(u.StartedAt(), u.StoppedAt), members, SplitByQuantity)
}

// PlanAutoStop returns the units a new open job-start closes (FR-25.3). open is
// the worker's open starts before the incoming one was stored; the incoming
// start itself and the open members of its own bundle are never stopped, so the
// jobs of one bundle start together.
//
// Every other open unit whose members all started at or before the incoming
// start is stopped at the incoming start. When a unit has a member that started
// after the incoming start (an older action replayed late, BR-25.7), the
// incoming start is the stale one: it is closed when that unit began (never
// before its own start) together with the open members of its bundle, and the
// later unit stays open. Either way at most one unit stays open.
// A unit open for more than MaxJobHours is stopped at its start plus MaxJobHours
// and marked Capped. Units come back in start order.
func PlanAutoStop(open []OpenStart, incoming OpenStart) []StopUnit {
	units := map[string]*StopUnit{}
	var keys []string
	var own []OpenStart
	for _, s := range open {
		if s.RegistrationID == incoming.RegistrationID {
			continue
		}
		if incoming.BundleID != "" && s.BundleID == incoming.BundleID {
			own = append(own, s)
			continue
		}
		key := "r:" + s.RegistrationID
		if s.BundleID != "" {
			key = "b:" + s.BundleID
		}
		u, ok := units[key]
		if !ok {
			u = &StopUnit{BundleID: s.BundleID}
			units[key] = u
			keys = append(keys, key)
		}
		u.Members = append(u.Members, s)
	}
	var stops []StopUnit
	var later *time.Time
	for _, k := range keys {
		u := *units[k]
		sortStarts(u.Members)
		if last := u.Members[len(u.Members)-1].StartedAt; last.After(incoming.StartedAt) {
			at := u.StartedAt()
			if at.Before(incoming.StartedAt) {
				at = incoming.StartedAt
			}
			if later == nil || at.Before(*later) {
				later = &at
			}
			continue
		}
		u.StoppedAt = incoming.StartedAt
		stops = append(stops, capped(u))
	}
	if later != nil {
		self := StopUnit{BundleID: incoming.BundleID, Members: []OpenStart{incoming}, StoppedAt: *later}
		for _, s := range own {
			if !s.StartedAt.After(*later) {
				self.Members = append(self.Members, s)
			}
		}
		sortStarts(self.Members)
		stops = append(stops, capped(self))
	}
	sort.SliceStable(stops, func(i, j int) bool { return stops[i].StartedAt().Before(stops[j].StartedAt()) })
	return stops
}

func sortStarts(m []OpenStart) {
	sort.SliceStable(m, func(i, j int) bool {
		if !m[i].StartedAt.Equal(m[j].StartedAt) {
			return m[i].StartedAt.Before(m[j].StartedAt)
		}
		return m[i].RegistrationID < m[j].RegistrationID
	})
}

// capped limits a unit to MaxJobHours from its earliest start.
func capped(u StopUnit) StopUnit {
	if limit := u.StartedAt().Add(MaxJobHours); u.StoppedAt.After(limit) {
		u.StoppedAt, u.Capped = limit, true
	}
	return u
}

// PlanProductionStop returns the open starts production's finish of an operation closes
// (FR-25.3): each start on the finished job is stopped at the finish, or at its own start when it
// began after the finish (an offline replay or a start accepted while the finish was on its way),
// so work after the finish adds no hours. A start open for more than MaxJobHours is stopped at its
// start plus MaxJobHours and marked Capped. Starts that belong to a bundle are returned in
// bundled, untouched: closing one member would change the split of the others (BR-25.1), so they
// stay open for the worker's stop or a supervisor. Units come back in start order.
func PlanProductionStop(open []OpenStart, finishedAt time.Time) (stops []StopUnit, bundled []OpenStart) {
	for _, s := range open {
		if s.BundleID != "" {
			bundled = append(bundled, s)
			continue
		}
		at := finishedAt
		if s.StartedAt.After(at) {
			at = s.StartedAt
		}
		stops = append(stops, capped(StopUnit{Members: []OpenStart{s}, StoppedAt: at}))
	}
	sort.SliceStable(stops, func(i, j int) bool { return stops[i].StartedAt().Before(stops[j].StartedAt()) })
	return stops, bundled
}
