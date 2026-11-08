package store_test

import (
	"testing"

	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/services/scheduling/internal/domain"
	schedevents "github.com/BenjaminSRussell/passage/services/scheduling/internal/events"
	"github.com/BenjaminSRussell/passage/services/scheduling/internal/store"
)

// A planner resets the counter after a cleanout outside the plan (FR-23.15).
// The reset needs the counts the planner saw, is kept with its reason, leaves
// the campaigns sequenced from the old counter unable to start (SCHED-0123),
// writes one event, and is per tenant.
func TestResetCleanoutCounter(t *testing.T) {
	s, _, admin := setup(t)
	ctx := tenantCtx(1)
	r := mustResource(t, ctx, s, "FILL-9", true)
	mk := func(minutes int) store.Campaign {
		c, _, err := s.CreateCampaign(ctx, store.Campaign{ResourceID: r.ID, CampaignKey: "P", PlannedStart: at(0, 6, 0),
			SequenceRule: domain.SequenceManual, Orders: []store.CampaignOrder{campaignOrder("P", "1", minutes)}}, store.Idem{})
		if err != nil {
			t.Fatal(err)
		}
		c, _, err = s.SequenceCampaign(ctx, store.CampaignCommand{CampaignID: c.ID, Version: c.Version}, store.Idem{})
		if err != nil {
			t.Fatal(err)
		}
		return c
	}
	first := mk(60)
	if _, _, err := s.StartCampaign(ctx, store.CampaignCommand{CampaignID: first.ID, Version: first.Version}, store.Idem{}); err != nil {
		t.Fatal(err)
	}
	waiting := mk(30) // sequenced from the counter the first campaign left

	list, err := s.ListCleanoutCounters(ctx)
	if err != nil || len(list) != 1 || list[0].ResourceCode != "FILL-9" || list[0].BatchesSince != 1 || list[0].MinutesSince != 60 || list[0].LastResetAt != nil {
		t.Fatalf("list = %+v err=%v, want 1 batch and 60 minutes", list, err)
	}

	in := store.CleanoutResetInput{ResourceID: r.ID, SeenBatches: 1, SeenMinutes: 60, Reason: "unplanned wash-down"}
	if _, _, err := s.ResetCleanoutCounter(ctx, store.CleanoutResetInput{ResourceID: r.ID, SeenBatches: 0, SeenMinutes: 60, Reason: "x"}, store.Idem{}); problemCode(err) != store.CodeCleanoutCountSeen {
		t.Fatalf("reset from counts the planner did not see: err=%v", err)
	}
	if _, _, err := s.ResetCleanoutCounter(ctx, store.CleanoutResetInput{ResourceID: ids.New(), Reason: "x"}, store.Idem{}); problemCode(err) != store.CodeResourceNotFound {
		t.Fatalf("unknown resource: err=%v", err)
	}
	res, created, err := s.ResetCleanoutCounter(ctx, in, store.Idem{Key: "reset-1", Fingerprint: "fp"})
	if err != nil || !created || res.PreviousBatches != 1 || res.PreviousMinutes != 60 || res.Reason != in.Reason || res.ResetBy == "" {
		t.Fatalf("reset = %+v created=%v err=%v", res, created, err)
	}
	if res.Counter.BatchesSince != 0 || res.Counter.MinutesSince != 0 || res.Counter.LastCampaignID != nil || len(res.Counter.Resets) != 1 || res.Counter.LastResetAt == nil {
		t.Fatalf("counter after the reset = %+v", res.Counter)
	}
	if len(res.StaleCampaignIDs) != 1 || res.StaleCampaignIDs[0] != waiting.ID {
		t.Fatalf("stale campaigns = %v, want [%s]", res.StaleCampaignIDs, waiting.ID)
	}
	if b, m, _ := counterRow(t, admin, r.ID); b != 0 || m != 0 {
		t.Fatalf("stored counter = %d and %d, want zero", b, m)
	}

	// The same key replays the stored reset and writes nothing more; a reused
	// key with other content is refused.
	if again, created, err := s.ResetCleanoutCounter(ctx, in, store.Idem{Key: "reset-1", Fingerprint: "fp"}); err != nil || created || again.ID != res.ID ||
		len(again.StaleCampaignIDs) != 1 || again.StaleCampaignIDs[0] != waiting.ID {
		t.Fatalf("replay = %+v created=%v err=%v, want the stored reset and its stale campaigns", again, created, err)
	}
	if _, _, err := s.ResetCleanoutCounter(ctx, in, store.Idem{Key: "reset-1", Fingerprint: "other"}); problemCode(err) != store.CodeIdempotencyReuse {
		t.Fatalf("reused key: err=%v", err)
	}
	if n := outboxCount(t, admin, schedevents.CleanoutCounterReset); n != 1 {
		t.Fatalf("cleanoutcounter.reset events = %d, want 1 (a replay writes none)", n)
	}

	// The campaign sequenced from the old counter cannot start until it is
	// sequenced again from the clean resource.
	if _, _, err := s.StartCampaign(ctx, store.CampaignCommand{CampaignID: waiting.ID, Version: waiting.Version}, store.Idem{}); problemCode(err) != store.CodeCleanoutCounterMoved {
		t.Fatalf("start a campaign sequenced before the reset: err=%v", err)
	}
	// Cancelling the first campaign does not give back counts the reset erased.
	started, err := s.GetCampaign(ctx, first.ID)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := s.CancelCampaign(ctx, store.CampaignCommand{CampaignID: first.ID, Version: started.Version}, store.Idem{}); err != nil {
		t.Fatal(err)
	}
	if b, m, _ := counterRow(t, admin, r.ID); b != 0 || m != 0 {
		t.Fatalf("counter after cancelling the first campaign = %d and %d, want zero", b, m)
	}

	// Sequenced again from the clean resource, the stale campaign starts.
	current, err := s.GetCampaign(ctx, waiting.ID)
	if err != nil {
		t.Fatal(err)
	}
	resequenced, _, err := s.SequenceCampaign(ctx, store.CampaignCommand{CampaignID: waiting.ID, Version: current.Version}, store.Idem{})
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := s.StartCampaign(ctx, store.CampaignCommand{CampaignID: waiting.ID, Version: resequenced.Version}, store.Idem{}); err != nil {
		t.Fatalf("start after sequencing again: %v", err)
	}
	if b, m, _ := counterRow(t, admin, r.ID); b != 1 || m != 30 {
		t.Fatalf("counter after the resequenced campaign started = %d and %d, want 1 and 30", b, m)
	}

	d, err := s.GetCleanoutCounter(ctx, r.ID)
	if err != nil || len(d.Resets) != 1 || d.Resets[0].Reason != in.Reason {
		t.Fatalf("detail = %+v err=%v", d, err)
	}
	if _, err := s.GetCleanoutCounter(tenantCtx(2), r.ID); problemCode(err) != store.CodeResourceNotFound {
		t.Fatalf("tenant 2 reads tenant 1: err=%v", err)
	}
	if other, err := s.ListCleanoutCounters(tenantCtx(2)); err != nil || len(other) != 0 {
		t.Fatalf("tenant 2 list = %+v err=%v", other, err)
	}
	if _, _, err := s.ResetCleanoutCounter(tenantCtx(2), in, store.Idem{}); problemCode(err) != store.CodeResourceNotFound {
		t.Fatalf("tenant 2 resets tenant 1: err=%v", err)
	}
}

// A resource that never ran a campaign reads zero and resetting zero is a
// recorded no-op, not an error. A planned campaign sequenced from that zero
// counter is still valid after the reset: it is not listed as stale and
// starts.
func TestResetCleanoutCounter_NeverRun(t *testing.T) {
	s, _, _ := setup(t)
	ctx := tenantCtx(1)
	r := mustResource(t, ctx, s, "MIX-9", true)
	d, err := s.GetCleanoutCounter(ctx, r.ID)
	if err != nil || d.BatchesSince != 0 || d.UpdatedAt != nil || len(d.Resets) != 0 {
		t.Fatalf("detail = %+v err=%v", d, err)
	}
	c, _, err := s.CreateCampaign(ctx, store.Campaign{ResourceID: r.ID, CampaignKey: "M", PlannedStart: at(0, 6, 0),
		SequenceRule: domain.SequenceManual, Orders: []store.CampaignOrder{campaignOrder("M", "1", 45)}}, store.Idem{})
	if err != nil {
		t.Fatal(err)
	}
	if c, _, err = s.SequenceCampaign(ctx, store.CampaignCommand{CampaignID: c.ID, Version: c.Version}, store.Idem{}); err != nil {
		t.Fatal(err)
	}
	res, created, err := s.ResetCleanoutCounter(ctx, store.CleanoutResetInput{ResourceID: r.ID, Reason: "changeover to a new product"}, store.Idem{})
	if err != nil || !created || res.StaleCampaignIDs == nil || len(res.StaleCampaignIDs) != 0 || len(res.Counter.Resets) != 1 {
		t.Fatalf("reset = %+v created=%v err=%v, want no stale campaign", res, created, err)
	}
	if _, _, err := s.StartCampaign(ctx, store.CampaignCommand{CampaignID: c.ID, Version: c.Version}, store.Idem{}); err != nil {
		t.Fatalf("start a campaign sequenced from zero after a reset: %v", err)
	}
}

// Two requests with one Idempotency-Key at the same time: the counter lock is
// taken before the key is looked up, so one resets and the other replays it
// instead of failing on the moved counter or the key.
func TestResetCleanoutCounter_ConcurrentSameKey(t *testing.T) {
	s, _, admin := setup(t)
	ctx := tenantCtx(1)
	r := mustResource(t, ctx, s, "WASH-9", true)
	in := store.CleanoutResetInput{ResourceID: r.ID, Reason: "unplanned wash-down"}
	idem := store.Idem{Key: "same", Fingerprint: "fp"}
	type answer struct {
		id      string
		created bool
		err     error
	}
	answers := make(chan answer, 2)
	for range 2 {
		go func() {
			res, created, err := s.ResetCleanoutCounter(ctx, in, idem)
			answers <- answer{res.ID, created, err}
		}()
	}
	a, b := <-answers, <-answers
	if a.err != nil || b.err != nil || a.id != b.id || a.created == b.created {
		t.Fatalf("answers %+v and %+v, want one reset and one replay of it", a, b)
	}
	if n := outboxCount(t, admin, schedevents.CleanoutCounterReset); n != 1 {
		t.Fatalf("cleanoutcounter.reset events = %d, want 1", n)
	}
}
