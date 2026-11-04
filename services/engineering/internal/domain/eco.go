package domain

import (
	"context"
	"slices"
	"time"

	"github.com/BenjaminSRussell/passage/services/engineering/internal/events"
)

const dateLayout = "2006-01-02"

type EcoState string

const (
	EcoDraft     EcoState = "draft"
	EcoSubmitted EcoState = "submitted"
	EcoApproved  EcoState = "approved"
	EcoScheduled EcoState = "scheduled"
	EcoReleased  EcoState = "released"
	EcoRejected  EcoState = "rejected"
	EcoCancelled EcoState = "cancelled"
)

var ecoTransitions = map[EcoState][]EcoState{
	EcoDraft:     {EcoSubmitted, EcoCancelled},
	EcoSubmitted: {EcoApproved, EcoRejected, EcoCancelled},
	EcoApproved:  {EcoScheduled, EcoReleased, EcoRejected, EcoCancelled},
	EcoScheduled: {EcoReleased, EcoCancelled},
}

type Replacement struct {
	OldVersionID string
	NewVersionID string
}

// DocumentReplacement is a change-controlled DIR pair an ECO releases (FR-9.25).
type DocumentReplacement struct {
	OldDocumentID string
	NewDocumentID string
}

type Eco struct {
	ID             string
	Number         string
	State          EcoState
	ReleaseDate    time.Time
	Replaces       []Replacement
	Documents      []DocumentReplacement
	DecidedBy      string
	CreatedBy      string
	DecisionReason string
	// EcrID is the approved change request this ECO carries out, empty for an ECO raised directly.
	EcrID string
}

type Decision struct {
	Approved bool
	Actor    string
}

type Approver interface {
	Decide(ctx context.Context, ecoID string) (Decision, error)
}

type ReleasePlan struct {
	Expire   []BomVersion
	Activate []BomVersion
}

func day(t time.Time) time.Time {
	y, m, d := t.Date()
	return time.Date(y, m, d, 0, 0, 0, 0, time.UTC)
}

func (e *Eco) Transition(to EcoState) error {
	if !slices.Contains(ecoTransitions[e.State], to) {
		return errf(CodeEcoState, "cannot move ECO %s from %s to %s", e.ID, e.State, to)
	}
	e.State = to
	return nil
}

func (e *Eco) Review(ctx context.Context, ap Approver) error {
	if e.State != EcoSubmitted {
		return errf(CodeEcoState, "ECO %s is %s, only a submitted ECO can be reviewed", e.ID, e.State)
	}
	d, err := ap.Decide(ctx, e.ID)
	if err != nil {
		unavailable := errf(CodeEcoApproverUnavailable, "approval service unavailable, release blocked")
		unavailable.Cause = err
		return unavailable
	}
	if err := e.checkSeparation(d.Actor); err != nil {
		return err
	}
	e.DecidedBy = d.Actor
	if !d.Approved {
		return e.Transition(EcoRejected)
	}
	return e.Transition(EcoApproved)
}

func (e *Eco) checkSeparation(actor string) error {
	if actor == "" {
		return errf(CodeEcoActorRequired, "an ECO decision needs the deciding user")
	}
	if e.CreatedBy == "" {
		return errf(CodeEcoCreatorUnknown, "ECO %s has no recorded creator, so separation of duties cannot be shown", e.Number)
	}
	if actor == e.CreatedBy {
		return errf(CodeEcoSelfApproval, "the user who created ECO %s cannot decide it", e.Number)
	}
	return nil
}

func (e *Eco) Decide(to EcoState, actor, reason string) error {
	if to != EcoApproved && to != EcoRejected && to != EcoCancelled {
		return errf(CodeEcoState, "an ECO is decided as approved, rejected or cancelled, not %s", to)
	}
	if e.State != EcoSubmitted {
		return errf(CodeEcoState, "ECO %s is %s, only a submitted ECO can be decided", e.ID, e.State)
	}
	if err := e.checkSeparation(actor); err != nil {
		return err
	}
	e.DecidedBy, e.DecisionReason = actor, reason
	return e.Transition(to)
}

// MaxEcoHorizonDays is how far ahead an ECO may be scheduled (BR-9.10 default for eng.eco.max_horizon_days).
const MaxEcoHorizonDays = 365

// CheckReleaseDate applies BR-9.10 to a requested release date. Both dates are compared as calendar
// days in their own location; the API passes today as the current UTC date, the same day Release uses.
// An empty date means release on the day it is called and always passes. A date before today needs the
// "Backdate engineering changes" duty, which is not built, so it is refused (ENG-ECO-BACKDATE-DUTY), and a
// date more than MaxEcoHorizonDays after today is refused (ENG-ECO-HORIZON).
func CheckReleaseDate(releaseDate, today time.Time) error {
	if releaseDate.IsZero() {
		return nil
	}
	date, today := day(releaseDate), day(today)
	if date.Before(today) {
		return errf(CodeEcoBackdateDuty, "Backdated release requires the Backdate duty.")
	}
	if date.After(today.AddDate(0, 0, MaxEcoHorizonDays)) {
		return errf(CodeEcoHorizon, "Release date is more than %d days ahead.", MaxEcoHorizonDays)
	}
	return nil
}

func (e *Eco) Release(versions []BomVersion, today time.Time) (ReleasePlan, error) {
	if e.State != EcoApproved && e.State != EcoScheduled {
		return ReleasePlan{}, errf(CodeEcoNotApproved, "ECO %s must be approved before release.", e.Number)
	}
	today = day(today)
	date := day(e.ReleaseDate)
	if e.ReleaseDate.IsZero() || (date.Before(today) && e.State == EcoScheduled) {
		date = today
	}
	switch {
	case date.Before(today):
		return ReleasePlan{}, errf(CodeEcoDate, "release date %s is in the past", date.Format(dateLayout))
	case date.After(today):
		if e.State == EcoApproved {
			return ReleasePlan{}, e.Transition(EcoScheduled)
		}
		return ReleasePlan{}, nil
	}
	plan, err := e.dryRun(versions, date)
	if err != nil {
		return ReleasePlan{}, err
	}
	return plan, e.Transition(EcoReleased)
}

func (e *Eco) dryRun(versions []BomVersion, date time.Time) (ReleasePlan, error) {
	work := slices.Clone(versions)
	index := make(map[string]int, len(work))
	for i, v := range work {
		index[v.ID] = i
	}
	var plan ReleasePlan
	var activated []int
	for _, r := range e.Replaces {
		oi, okOld := index[r.OldVersionID]
		ni, okNew := index[r.NewVersionID]
		if !okOld || !okNew {
			return ReleasePlan{}, errf(CodeEcoDryRun, "ECO %s references an unknown version", e.Number)
		}
		old, nw := &work[oi], &work[ni]
		if old.Status != StatusActive || nw.Status != StatusDraft || old.ProductID != nw.ProductID {
			return ReleasePlan{}, errf(CodeEcoDryRun, "replace %s with %s needs an active version and a draft of the same product", old.ID, nw.ID)
		}
		// FR-9.19: an ECO replaces a BOM with one of the same usage and category, so releasing a
		// costing draft cannot expire the production BOM it sits beside.
		if !sameStructure(*old, *nw) {
			return ReleasePlan{}, errf(CodeEcoDryRun, "replace %s (%s %s) with %s (%s %s) needs the same BOM usage and category", old.ID, usageOf(*old), categoryOf(*old), nw.ID, usageOf(*nw), categoryOf(*nw))
		}
		if date.AddDate(0, 0, -1).Before(old.EffectiveFrom) {
			return ReleasePlan{}, errf(CodeEcoDryRun, "version %s cannot expire before it starts", old.ID)
		}
		if !nw.EffectiveTo.IsZero() && nw.EffectiveTo.Before(date) {
			return ReleasePlan{}, errf(CodeEcoDryRun, "version %s ends on %s, before the release date %s", nw.ID, nw.EffectiveTo.Format(dateLayout), date.Format(dateLayout))
		}
		old.Status = StatusExpired
		if expiry := date.AddDate(0, 0, -1); old.EffectiveTo.IsZero() || old.EffectiveTo.After(expiry) {
			old.EffectiveTo = expiry
		}
		nw.Status, nw.EffectiveFrom = StatusActive, date
		activated = append(activated, ni)
		plan.Expire = append(plan.Expire, *old)
		plan.Activate = append(plan.Activate, *nw)
	}
	for _, ni := range activated {
		if err := CheckNoOverlap(work, work[ni]); err != nil {
			return ReleasePlan{}, errf(CodeEcoDryRun, "%s", err.Error())
		}
	}
	return plan, nil
}

func (p ReleasePlan) ActivatedComponents() []string {
	var out []string
	for _, v := range p.Activate {
		for _, l := range v.Lines {
			out = append(out, l.ComponentProductID)
		}
	}
	return out
}

func (p ReleasePlan) CheckCycles(cat Catalog) error {
	for _, v := range p.Activate {
		for _, l := range v.Lines {
			if err := CheckCycle(cat, v.ProductID, l.ComponentProductID, v.EffectiveFrom); err != nil {
				return err
			}
		}
	}
	return nil
}

func (p ReleasePlan) ActivatedEvents() []events.BomVersionActivatedData {
	out := make([]events.BomVersionActivatedData, len(p.Activate))
	for i, v := range p.Activate {
		out[i] = v.ActivatedEvent(p.Expire[i].ID)
	}
	return out
}

func (p ReleasePlan) ExpiredEvents(ecoID string) []events.BomVersionExpiredData {
	out := make([]events.BomVersionExpiredData, len(p.Expire))
	for i, v := range p.Expire {
		out[i] = events.BomVersionExpiredData{BomVersionID: v.ID, EffectiveTo: v.EffectiveTo.Format(dateLayout), EcoID: ecoID}
	}
	return out
}

func (e Eco) EventData() events.EcoData {
	data := events.EcoData{EcoID: e.ID, EcoNumber: e.Number, ApproverID: e.DecidedBy, EcrID: e.EcrID, AffectedVersions: make([]string, len(e.Replaces))}
	for i, r := range e.Replaces {
		data.AffectedVersions[i] = r.NewVersionID
	}
	if !e.ReleaseDate.IsZero() {
		data.ReleaseDate = e.ReleaseDate.Format(dateLayout)
	}
	return data
}
