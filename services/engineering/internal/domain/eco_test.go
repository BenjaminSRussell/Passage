package domain_test

import (
	"context"
	"errors"
	"strings"
	"testing"

	platformevents "github.com/BenjaminSRussell/passage/platform/events"
	"github.com/BenjaminSRussell/passage/services/engineering/internal/domain"
	"github.com/BenjaminSRussell/passage/services/engineering/internal/events"
)

type fakeApprover struct {
	decision domain.Decision
	err      error
}

func (f fakeApprover) Decide(context.Context, string) (domain.Decision, error) {
	return f.decision, f.err
}

func ecoIn(state domain.EcoState, releaseDate string) *domain.Eco {
	e := &domain.Eco{ID: "eco-1", Number: "ECO-0001", State: state, CreatedBy: "creator", Replaces: []domain.Replacement{{OldVersionID: "V1", NewVersionID: "V2"}}}
	if releaseDate != "" {
		e.ReleaseDate = date(releaseDate)
	}
	return e
}

func releaseFixture() []domain.BomVersion {
	v2 := version("V2", "P", "2026-01-01", "", line(10, "C2", 1))
	v2.Status = domain.StatusDraft
	return []domain.BomVersion{
		version("V1", "P", "2025-01-01", "", line(10, "C1", 1)),
		v2,
		version("U", "UNRELATED", "2025-01-01", "", line(10, "C1", 1)),
	}
}

func TestEventTypesFollowTheKernelNamingRule(t *testing.T) {
	for _, typ := range []string{
		events.BomVersionActivated, events.BomVersionExpired, events.EcoSubmitted, events.EcoApproved, events.EcoReleased,
	} {
		if !platformevents.ValidType(typ) {
			t.Errorf("%s is not a valid event type", typ)
		}
	}
}

func TestEcoTransitions(t *testing.T) {
	tests := []struct {
		from, to domain.EcoState
		ok       bool
	}{
		{domain.EcoDraft, domain.EcoSubmitted, true},
		{domain.EcoSubmitted, domain.EcoApproved, true},
		{domain.EcoSubmitted, domain.EcoRejected, true},
		{domain.EcoApproved, domain.EcoScheduled, true},
		{domain.EcoScheduled, domain.EcoReleased, true},
		{domain.EcoScheduled, domain.EcoCancelled, true},
		{domain.EcoDraft, domain.EcoReleased, false},
		{domain.EcoDraft, domain.EcoApproved, false},
		{domain.EcoReleased, domain.EcoCancelled, false},
		{domain.EcoRejected, domain.EcoApproved, false},
	}
	for _, tt := range tests {
		e := ecoIn(tt.from, "")
		err := e.Transition(tt.to)
		if tt.ok != (err == nil) || (!tt.ok && (!domain.HasCode(err, domain.CodeEcoState) || e.State != tt.from)) {
			t.Errorf("%s -> %s: err = %v, state = %s", tt.from, tt.to, err, e.State)
		}
	}
}

func TestEcoReview(t *testing.T) {
	tests := []struct {
		name      string
		state     domain.EcoState
		approver  fakeApprover
		wantState domain.EcoState
		wantErr   bool
	}{
		{"approved", domain.EcoSubmitted, fakeApprover{decision: domain.Decision{Approved: true, Actor: "ann"}}, domain.EcoApproved, false},
		{"rejected", domain.EcoSubmitted, fakeApprover{decision: domain.Decision{Actor: "bob"}}, domain.EcoRejected, false},
		{"draft cannot be reviewed", domain.EcoDraft, fakeApprover{decision: domain.Decision{Approved: true}}, domain.EcoDraft, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			e := ecoIn(tt.state, "")
			err := e.Review(context.Background(), tt.approver)
			if tt.wantErr != domain.HasCode(err, domain.CodeEcoState) || e.State != tt.wantState {
				t.Fatalf("err = %v, state = %s, want state %s", err, e.State, tt.wantState)
			}
		})
	}
}

func TestEcoReviewApproverUnavailableFailsClosed(t *testing.T) {
	cause := errors.New("temporal down: host-7.internal")
	e := ecoIn(domain.EcoSubmitted, "")
	err := e.Review(context.Background(), fakeApprover{err: cause})
	var de *domain.Error
	if !errors.As(err, &de) || de.Code != domain.CodeEcoApproverUnavailable || !errors.Is(err, cause) || strings.Contains(de.Detail, "host-7") || e.State != domain.EcoSubmitted {
		t.Fatalf("err = %v, state = %s; want %s wrapping the cause without leaking it, state unchanged", err, e.State, domain.CodeEcoApproverUnavailable)
	}
}

func TestEcoReleaseExpiresOldActivatesNew(t *testing.T) {
	e := ecoIn(domain.EcoSubmitted, "2026-07-01")
	if err := e.Review(context.Background(), fakeApprover{decision: domain.Decision{Approved: true, Actor: "ann"}}); err != nil {
		t.Fatal(err)
	}
	versions := releaseFixture()

	held, err := e.Release(versions, date("2026-06-15"))
	if err != nil || e.State != domain.EcoScheduled || len(held.Activate) != 0 {
		t.Fatalf("future release: err %v state %s plan %+v, want scheduled and empty", err, e.State, held)
	}
	plan, err := e.Release(versions, date("2026-07-01"))
	if err != nil || e.State != domain.EcoReleased {
		t.Fatalf("release on the day: err %v state %s", err, e.State)
	}
	if versions[0].Status != domain.StatusActive || versions[1].Status != domain.StatusDraft {
		t.Fatal("release must not mutate the input versions")
	}
	if len(plan.Expire) != 1 || plan.Expire[0].Status != domain.StatusExpired || plan.Expire[0].EffectiveTo != date("2026-06-30") {
		t.Fatalf("expired = %+v, want V1 expired with effective_to 2026-06-30", plan.Expire)
	}
	if len(plan.Activate) != 1 || plan.Activate[0].Status != domain.StatusActive || plan.Activate[0].EffectiveFrom != date("2026-07-01") {
		t.Fatalf("activated = %+v, want V2 active from 2026-07-01", plan.Activate)
	}

	after := []domain.BomVersion{plan.Expire[0], plan.Activate[0], versions[2]}
	for asOf, want := range map[string]string{"2026-06-30": "V1", "2026-07-01": "V2"} {
		got, err := domain.ResolveVersion(after, "P", "", date(asOf), dec("1"))
		if err != nil || got.ID != want {
			t.Errorf("as of %s resolved %q (err %v), want %s", asOf, got.ID, err, want)
		}
	}
	activated, expired := plan.ActivatedEvents(), plan.ExpiredEvents(e.ID)
	if len(activated) != 1 || activated[0].SupersedesVersionID != "V1" || activated[0].EffectiveFrom != "2026-07-01" || activated[0].LineCount != 1 {
		t.Errorf("activated event = %+v", activated)
	}
	if len(expired) != 1 || expired[0].EffectiveTo != "2026-06-30" || expired[0].EcoID != "eco-1" {
		t.Errorf("expired event = %+v", expired)
	}
	if data := e.EventData(); data.ApproverID != "ann" || data.ReleaseDate != "2026-07-01" || len(data.AffectedVersions) != 1 {
		t.Errorf("eco event data = %+v", data)
	}
}

func TestEcoReleaseRefusals(t *testing.T) {
	overlapping := releaseFixture()
	overlapping = append(overlapping, version("V9", "P", "2026-06-01", ""))
	tests := []struct {
		name     string
		eco      *domain.Eco
		versions []domain.BomVersion
		today    string
		want     string
		state    domain.EcoState
	}{
		{"not approved", ecoIn(domain.EcoSubmitted, ""), releaseFixture(), "2026-07-01", domain.CodeEcoNotApproved, domain.EcoSubmitted},
		{"past release date", ecoIn(domain.EcoApproved, "2026-06-01"), releaseFixture(), "2026-07-01", domain.CodeEcoDate, domain.EcoApproved},
		{"unknown version", &domain.Eco{ID: "e", State: domain.EcoApproved, Replaces: []domain.Replacement{{OldVersionID: "nope", NewVersionID: "V2"}}}, releaseFixture(), "2026-07-01", domain.CodeEcoDryRun, domain.EcoApproved},
		{"overlap with another active version", ecoIn(domain.EcoApproved, "2026-07-01"), overlapping, "2026-07-01", domain.CodeEcoDryRun, domain.EcoApproved},
	}
	endedDraft := releaseFixture()
	endedDraft[1].EffectiveTo = date("2026-03-01")
	for _, tt := range []struct {
		name     string
		eco      *domain.Eco
		versions []domain.BomVersion
	}{{"draft ends before the release date", ecoIn(domain.EcoApproved, "2026-07-01"), endedDraft}} {
		tests = append(tests, struct {
			name     string
			eco      *domain.Eco
			versions []domain.BomVersion
			today    string
			want     string
			state    domain.EcoState
		}{tt.name, tt.eco, tt.versions, "2026-07-01", domain.CodeEcoDryRun, domain.EcoApproved})
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			plan, err := tt.eco.Release(tt.versions, date(tt.today))
			if !domain.HasCode(err, tt.want) || tt.eco.State != tt.state || len(plan.Activate)+len(plan.Expire) != 0 {
				t.Fatalf("err = %v, state = %s, plan = %+v", err, tt.eco.State, plan)
			}
		})
	}
}

func TestEcoReleaseNeverLengthensAnEndedVersion(t *testing.T) {
	versions := releaseFixture()
	versions[0].EffectiveTo = date("2026-03-31")
	e := ecoIn(domain.EcoApproved, "2026-07-01")
	plan, err := e.Release(versions, date("2026-07-01"))
	if err != nil || plan.Expire[0].EffectiveTo != date("2026-03-31") {
		t.Fatalf("err %v expired %+v, want effective_to kept at 2026-03-31", err, plan.Expire)
	}
}

func TestEcoScheduledReleasesOnALaterDay(t *testing.T) {
	e := ecoIn(domain.EcoScheduled, "2026-06-20")
	plan, err := e.Release(releaseFixture(), date("2026-07-01"))
	if err != nil || e.State != domain.EcoReleased || plan.Activate[0].EffectiveFrom != date("2026-07-01") {
		t.Fatalf("err %v state %s plan %+v, want released with the late day as effective date", err, e.State, plan)
	}
}

func TestEcoDecisionsNeedAKnownCreator(t *testing.T) {
	e := &domain.Eco{ID: "e", Number: "ECO-9", State: domain.EcoSubmitted}
	var de *domain.Error
	if err := e.Decide(domain.EcoApproved, "alice", ""); !errors.As(err, &de) || de.Code != domain.CodeEcoCreatorUnknown || e.State != domain.EcoSubmitted || e.DecidedBy != "" {
		t.Fatalf("decide with no creator = %v, state %s", err, e.State)
	}
}
