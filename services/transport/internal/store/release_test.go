package store_test

import (
	"context"
	"encoding/json"
	"slices"
	"testing"
	"time"

	"github.com/BenjaminSRussell/passage/platform/dbx"
	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/platform/testkit"
	"github.com/BenjaminSRussell/passage/services/transport/internal/domain"
	tmsevents "github.com/BenjaminSRussell/passage/services/transport/internal/events"
	"github.com/BenjaminSRussell/passage/services/transport/internal/store"
	"github.com/BenjaminSRussell/passage/services/transport/migrations"
)

func releaseOf(load string, carriers ...string) domain.LoadRelease {
	t, _ := domain.NewTender(load, carriers, 45*time.Minute)
	return domain.ReleaseOf(t)
}

type releasedEvent struct {
	AggregateType string
	Source        string                        `json:"source"`
	Subject       string                        `json:"subject"`
	TenantID      string                        `json:"tenantid"`
	Actor         string                        `json:"actor"`
	Data          tmsevents.LoadReleasedPayload `json:"data"`
}

// TestReleaseLoad_PublishesTheVerifiedReleaser is #426 item 7 on the producer
// side: the release writes one load.released event whose releasedBy and
// legalEntityScope are the caller's, refuses a load outside the caller's scope
// and a load transport would refuse to tender, and replays by Idempotency-Key.
func TestReleaseLoad_PublishesTheVerifiedReleaser(t *testing.T) {
	admin, app := testkit.DBWithApp(t)
	if err := dbx.Migrate(context.Background(), admin, migrations.FS); err != nil {
		t.Fatal(err)
	}
	testkit.AssertTenantRLS(t, admin)
	s := store.New(app)
	entityA, entityB := ids.New(), ids.New()
	ctxA, ctxB := scopedCtx(testkit.TenantID(1), entityA), scopedCtx(testkit.TenantID(1), entityB)
	ctxBoth := scopedCtx(testkit.TenantID(1), entityA+","+entityB)
	loadA, loadB := seedLoad(t, s, ctxA), seedLoad(t, s, ctxB)
	carrierA, carrierB := ids.New(), ids.New()
	events := func() []releasedEvent {
		t.Helper()
		rows, err := admin.Query(context.Background(), `select aggregate_type, payload from outbox where type = $1 order by created_at, id`, tmsevents.LoadReleased)
		if err != nil {
			t.Fatal(err)
		}
		defer rows.Close()
		var out []releasedEvent
		for rows.Next() {
			var e releasedEvent
			var raw []byte
			if err := rows.Scan(&e.AggregateType, &raw); err != nil {
				t.Fatal(err)
			}
			if err := json.Unmarshal(raw, &e); err != nil {
				t.Fatal(err)
			}
			out = append(out, e)
		}
		return out
	}

	in := releaseOf(loadA, carrierA, carrierB)
	in.Carriers[0].RateQuoted, in.Currency = "120.00", "USD"
	// Fields the store must not take from its input.
	in.ReleasedBy, in.LegalEntityScope, in.LegalEntityID = "someone-else", []string{entityB}, entityB
	idem := store.Idem{Key: "release-1", Fingerprint: "f1"}
	got, err := s.ReleaseLoad(ctxBoth, in, idem, t0)
	if err != nil {
		t.Fatal(err)
	}
	if got.ReleasedBy != "u" || got.LegalEntityID != entityA || !slices.Equal(got.LegalEntityScope, []string{entityA, entityB}) || !got.ReleasedAt.Equal(t0) {
		t.Fatalf("release = %+v", got)
	}
	if len(got.Carriers) != 2 || got.Carriers[0] != (domain.ReleasedCarrier{CarrierID: carrierA, RateQuoted: "120.00"}) || got.Carriers[1].CarrierID != carrierB || got.TimeoutMin != 45 {
		t.Fatalf("release cascade = %+v", got)
	}
	evs := events()
	if len(evs) != 1 {
		t.Fatalf("load.released events = %d, want 1", len(evs))
	}
	e := evs[0]
	want := tmsevents.LoadReleasedPayload{ReleaseID: got.ID, LoadID: loadA, LegalEntityID: entityA, ReleasedBy: "u", LegalEntityScope: []string{entityA, entityB},
		Carriers: []tmsevents.ReleasedCarrier{{CarrierID: carrierA, RateQuoted: "120.00"}, {CarrierID: carrierB}}, Currency: "USD", TimeoutMin: 45}
	wantJSON, _ := json.Marshal(want)
	gotJSON, _ := json.Marshal(e.Data)
	if string(gotJSON) != string(wantJSON) {
		t.Fatalf("event data = %s, want %s", gotJSON, wantJSON)
	}
	if e.AggregateType != "load" || e.Source != "/passage/tms/transport" || e.Subject != loadA || e.TenantID != testkit.TenantID(1) || e.Actor != "u" {
		t.Fatalf("event envelope = %+v", e)
	}

	// The same key and request replays the release and publishes nothing; another request under the key is refused.
	again, err := s.ReleaseLoad(ctxBoth, in, idem, t0.Add(time.Minute))
	if err != nil || again.ID != got.ID || !again.ReleasedAt.Equal(t0) {
		t.Fatalf("replay = %+v, %v", again, err)
	}
	if _, err := s.ReleaseLoad(ctxBoth, releaseOf(loadA, carrierB), store.Idem{Key: "release-1", Fingerprint: "f2"}, t0); problemCode(t, err) != store.CodeIdempotencyReuse {
		t.Fatalf("reused key = %v", err)
	}
	if n := len(events()); n != 1 {
		t.Fatalf("load.released events after replays = %d, want 1", n)
	}

	// A caller scoped to another legal entity does not see the load, and nothing is published.
	if _, err := s.ReleaseLoad(ctxB, releaseOf(loadA, carrierA), store.Idem{}, t0); problemCode(t, err) != domain.CodeNotFound {
		t.Fatalf("release outside the caller's scope = %v, want TMS-0100", err)
	}
	// The release record follows the load's legal entity.
	if n := countAs(t, app, ctxB, `select count(*) from load_release`); n != 0 {
		t.Fatalf("entity B sees %d releases of entity A", n)
	}
	if n := countAs(t, app, ctxA, `select count(*) from load_release`); n != 1 {
		t.Fatalf("entity A sees %d releases, want 1", n)
	}

	// A load with an open tender, or one the tender gates refuse, is not released.
	tender := sentTender(loadA, time.Hour, carrierA)
	if _, err := s.CreateTender(ctxA, tender, store.Idem{}, t0); err != nil {
		t.Fatal(err)
	}
	if _, err := s.ReleaseLoad(ctxA, releaseOf(loadA, carrierA), store.Idem{}, t0.Add(time.Minute)); problemCode(t, err) != domain.CodeTenderOpen {
		t.Fatalf("release with a pending tender = %v, want TMS-0114", err)
	}
	if _, err := s.RecordDGCheck(ctxB, domain.DGCheck{LoadID: loadB, Regulation: domain.RegADR, Result: domain.DGFail, ADRPoints: dec("0"), Findings: []domain.DGFinding{}, ShippingPapers: domain.ShippingPapers(domain.DGRequest{}), CheckedAt: t0}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.ReleaseLoad(ctxB, releaseOf(loadB, carrierA), store.Idem{}, t0); problemCode(t, err) != domain.CodeDGSegregation {
		t.Fatalf("release after a failed DG check = %v, want TMS-0130", err)
	}
	if n := len(events()); n != 1 {
		t.Fatalf("load.released events after refusals = %d, want 1", n)
	}
	// A pending tender past its deadline is cascaded first, and once it has failed the load can be released again.
	if _, err := s.ReleaseLoad(ctxA, releaseOf(loadA, carrierB), store.Idem{}, t0.Add(2*time.Hour)); err != nil {
		t.Fatalf("release after the tender failed by time = %v", err)
	}
	if n := len(events()); n != 2 {
		t.Fatalf("load.released events = %d, want 2", n)
	}
}
