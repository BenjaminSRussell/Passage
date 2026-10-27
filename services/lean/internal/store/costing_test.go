package store_test

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/services/lean/internal/domain"
	leanevents "github.com/BenjaminSRussell/passage/services/lean/internal/events"
	"github.com/BenjaminSRussell/passage/services/lean/internal/store"
)

func day(offset int) time.Time {
	return time.Now().UTC().Truncate(24*time.Hour).AddDate(0, 0, offset)
}

func openPeriod(t *testing.T, s *store.Postgres, ctx context.Context, flowID string, from, to int) store.Period {
	t.Helper()
	p, _, err := s.OpenPeriod(ctx, store.OpenPeriod{FlowID: flowID, PeriodStart: day(from), PeriodEnd: day(to), Currency: "USD"}, store.Idem{})
	if err != nil {
		t.Fatalf("open period %d..%d: %v", from, to, err)
	}
	return p
}

// runJob starts and completes one job of a card.
func runJob(t *testing.T, s *store.Postgres, ctx context.Context, j store.Job, complete bool) {
	t.Helper()
	started, _, err := s.StartJob(ctx, j.ID, j.Version, store.Idem{})
	if err != nil {
		t.Fatalf("start %s/%d: %v", j.CardNumber, j.Sequence, err)
	}
	if complete {
		if _, _, err := s.CompleteJob(ctx, started.ID, started.Version, done("20", "0", 1), store.Idem{}); err != nil {
			t.Fatalf("complete %s/%d: %v", j.CardNumber, j.Sequence, err)
		}
	}
}

// backdate moves a card's job times back by days, as if the work had happened then.
func backdate(t *testing.T, admin *pgxpool.Pool, kanbanID string, days int) {
	t.Helper()
	if _, err := admin.Exec(context.Background(), `update kanban_job set started_at = started_at - make_interval(days => $2),
		completed_at = completed_at - make_interval(days => $2) where kanban_id = $1`, kanbanID, days); err != nil {
		t.Fatal(err)
	}
}

func TestCosting_OpenOverlapReplayAndList(t *testing.T) {
	s, _ := setup(t)
	ctx := ctxFor(1)
	f := cellFlow(t, s, ctx, ids.New(), ids.New(), false)
	in := store.OpenPeriod{FlowID: f.ID, PeriodStart: day(-60), PeriodEnd: day(-31), Currency: "USD"}
	p, fresh, err := s.OpenPeriod(ctx, in, store.Idem{Key: "open-1", Fingerprint: "fp"})
	if err != nil || !fresh || p.Status != domain.PeriodOpen || p.Version != 1 || p.Currency != "USD" || p.Variance != nil ||
		p.PeriodStart != day(-60).Format(store.DateLayout) || p.PeriodEnd != day(-31).Format(store.DateLayout) {
		t.Fatalf("open fresh=%v %+v %v", fresh, p, err)
	}
	if again, fresh, err := s.OpenPeriod(ctx, in, store.Idem{Key: "open-1", Fingerprint: "fp"}); err != nil || fresh || again.ID != p.ID {
		t.Fatalf("replay fresh=%v %+v %v", fresh, again, err)
	}
	if _, _, err := s.OpenPeriod(ctx, in, store.Idem{Key: "open-1", Fingerprint: "other"}); problemCode(err) != store.CodeIdempotencyReuse {
		t.Fatalf("key reuse: %v", err)
	}
	// Periods of one flow never share a day, whichever end overlaps.
	for _, r := range [][2]int{{-45, -20}, {-70, -60}, {-31, -31}, {-90, -10}} {
		if _, _, err := s.OpenPeriod(ctx, store.OpenPeriod{FlowID: f.ID, PeriodStart: day(r[0]), PeriodEnd: day(r[1]), Currency: "USD"}, store.Idem{}); problemCode(err) != store.CodePeriodOverlap {
			t.Fatalf("overlap %v: %v", r, err)
		}
	}
	next := openPeriod(t, s, ctx, f.ID, -30, -1)
	// Another flow may cost the same days.
	other := cellFlow(t, s, ctx, ids.New(), ids.New(), false)
	openPeriod(t, s, ctx, other.ID, -60, -31)
	if _, _, err := s.OpenPeriod(ctx, store.OpenPeriod{FlowID: ids.New(), PeriodStart: day(-5), PeriodEnd: day(-1), Currency: "USD"}, store.Idem{}); problemCode(err) != store.CodeFlowNotFound {
		t.Fatalf("unknown flow: %v", err)
	}

	list, err := s.ListPeriods(ctx, f.ID, 50)
	if err != nil || len(list) != 2 || list[0].ID != next.ID || list[1].ID != p.ID {
		t.Fatalf("list %+v %v", list, err)
	}
	if one, err := s.ListPeriods(ctx, f.ID, 1); err != nil || len(one) != 1 {
		t.Fatalf("limit %d %v", len(one), err)
	}
	if _, err := s.ListPeriods(ctx, ids.New(), 50); problemCode(err) != store.CodeFlowNotFound {
		t.Fatalf("list unknown flow: %v", err)
	}
}

// AC-24.8 and BR-24.9: a period settles after its last day only when every
// card with jobs in it is Completed or Emptied; the variance is material +
// conversion − output and the settled event carries it.
func TestCosting_SettleGateVarianceAndEvent(t *testing.T) {
	s, admin := setup(t)
	ctx := ctxFor(1)
	f := cellFlow(t, s, ctx, ids.New(), ids.New(), false)
	rule := eventRule("COST-1")
	rule.FlowID = &f.ID
	r, _, err := s.CreateRule(ctx, rule, store.Idem{})
	if err != nil {
		t.Fatal(err)
	}
	cards, _, err := s.GenerateKanbans(ctx, store.Generate{RuleID: r.ID, DemandQty: dec("60"), EventRefType: ptr("sales-line"), EventRefID: ptr("SO-9/1")}, store.Idem{})
	if err != nil || len(cards) != 3 {
		t.Fatalf("generate %d %v", len(cards), err)
	}
	// Card 1 runs through both jobs; card 2 finishes Mix only. Both worked
	// 20 days ago, inside the period. Card 3 starts today, after the period.
	_, jobs1 := jobsOf(t, s, ctx, cards[0].ID)
	runJob(t, s, ctx, jobs1[0], true)
	_, jobs1 = jobsOf(t, s, ctx, cards[0].ID)
	runJob(t, s, ctx, jobs1[1], true)
	_, jobs2 := jobsOf(t, s, ctx, cards[1].ID)
	runJob(t, s, ctx, jobs2[0], true)
	_, jobs3 := jobsOf(t, s, ctx, cards[2].ID)
	runJob(t, s, ctx, jobs3[0], false)
	backdate(t, admin, cards[0].ID, 20)
	backdate(t, admin, cards[1].ID, 20)

	p := openPeriod(t, s, ctx, f.ID, -30, -1)
	cmd := store.SettlePeriod{FlowID: f.ID, PeriodID: p.ID, Version: p.Version, MaterialCost: dec("900.10"), ConversionCost: dec("300"), OutputValue: dec("1150.25")}
	_, _, err = s.SettlePeriod(ctx, cmd, store.Idem{Key: "settle-blocked", Fingerprint: "fp"})
	if problemCode(err) != domain.CodeOpenKanbans || !strings.Contains(err.Error(), "1 kanbans open") || !strings.Contains(err.Error(), cards[1].CardNumber) {
		t.Fatalf("open card in the period: %v", err)
	}
	if again, err := s.ListPeriods(ctx, f.ID, 50); err != nil || again[0].Status != domain.PeriodOpen || again[0].Version != 1 {
		t.Fatalf("refused settlement changed the period: %+v %v", again, err)
	}

	// Amounts finer than the currency allows are refused, not rounded.
	bad := cmd
	bad.ConversionCost = dec("300.005")
	if _, _, err := s.SettlePeriod(ctx, bad, store.Idem{}); problemCode(err) != domain.CodeInvalid {
		t.Fatalf("three decimals in USD: %v", err)
	}
	stale := cmd
	stale.Version = 7
	if _, _, err := s.SettlePeriod(ctx, stale, store.Idem{}); problemCode(err) != store.CodeVersionConflict {
		t.Fatalf("stale version: %v", err)
	}

	// Card 2 finishes Fill 20 days ago too; now every card of the period is done.
	_, jobs2 = jobsOf(t, s, ctx, cards[1].ID)
	runJob(t, s, ctx, jobs2[1], true)
	backdate(t, admin, cards[1].ID, 20)
	settled, fresh, err := s.SettlePeriod(ctx, cmd, store.Idem{Key: "settle-1", Fingerprint: "fp"})
	if err != nil || !fresh || settled.Status != domain.PeriodSettled || settled.Version != 2 || settled.SettledAt == nil || *settled.SettledBy != "lean-1" ||
		*settled.MaterialCost != "900.10" || *settled.ConversionCost != "300.00" || *settled.OutputValue != "1150.25" || *settled.Variance != "49.85" ||
		*settled.Kanbans != 2 || settled.JournalEntryID != nil {
		t.Fatalf("settle fresh=%v %+v %v", fresh, settled, err)
	}
	if again, fresh, err := s.SettlePeriod(ctx, cmd, store.Idem{Key: "settle-1", Fingerprint: "fp"}); err != nil || fresh || again.Version != 2 || *again.Variance != "49.85" {
		t.Fatalf("settle replay fresh=%v %+v %v", fresh, again, err)
	}
	cmd.Version = settled.Version
	if _, _, err := s.SettlePeriod(ctx, cmd, store.Idem{Key: "settle-2", Fingerprint: "fp"}); problemCode(err) != store.CodePeriodSettled {
		t.Fatalf("settled twice: %v", err)
	}

	evts := outboxEvents(t, admin, leanevents.FlowPeriodSettled)
	if len(evts) != 1 || evts[0].Subject != p.ID {
		t.Fatalf("settled events = %d", len(evts))
	}
	var d leanevents.FlowPeriodSettledData
	if err := json.Unmarshal(evts[0].Data, &d); err != nil || d.FlowID != f.ID || d.PeriodID != p.ID || d.Currency != "USD" ||
		d.MaterialCost != "900.10" || d.ConversionCost != "300.00" || d.OutputValue != "1150.25" || d.Variance != "49.85" || d.Kanbans != 2 ||
		d.JournalEntryID != nil || d.PeriodEnd != day(-1).Format(store.DateLayout) {
		t.Fatalf("settled event %+v %v", d, err)
	}

	// A period that includes today cannot settle yet.
	current := openPeriod(t, s, ctx, f.ID, 0, 6)
	if _, _, err := s.SettlePeriod(ctx, store.SettlePeriod{FlowID: f.ID, PeriodID: current.ID, Version: 1, MaterialCost: dec("0"), ConversionCost: dec("0"),
		OutputValue: dec("0")}, store.Idem{}); problemCode(err) != store.CodePeriodNotEnded {
		t.Fatalf("current period settled: %v", err)
	}
	// A period is settled only under its own flow.
	other := cellFlow(t, s, ctx, ids.New(), ids.New(), false)
	if _, _, err := s.SettlePeriod(ctx, store.SettlePeriod{FlowID: other.ID, PeriodID: current.ID, Version: 1, MaterialCost: dec("0"), ConversionCost: dec("0"),
		OutputValue: dec("0")}, store.Idem{}); problemCode(err) != store.CodePeriodNotFound {
		t.Fatalf("period under another flow: %v", err)
	}
}

// A card that started before the period and is still open had work in it,
// so it blocks; once emptied and reused its old cycle no longer counts.
func TestCosting_WorkInProgressFromAnEarlierPeriodBlocks(t *testing.T) {
	s, admin := setup(t)
	ctx := ctxFor(1)
	f := cellFlow(t, s, ctx, ids.New(), ids.New(), false)
	rule := fixedRule("COST-FIX", 1)
	rule.FlowID = &f.ID
	r, _, err := s.CreateRule(ctx, rule, store.Idem{})
	if err != nil {
		t.Fatal(err)
	}
	cards, _, err := s.GenerateKanbans(ctx, store.Generate{RuleID: r.ID}, store.Idem{})
	if err != nil || len(cards) != 1 {
		t.Fatalf("generate %d %v", len(cards), err)
	}
	k := walk(t, s, ctx, cards[0], domain.Planned)
	_, jobs := jobsOf(t, s, ctx, k.ID)
	runJob(t, s, ctx, jobs[0], true)
	backdate(t, admin, k.ID, 40)
	p := openPeriod(t, s, ctx, f.ID, -30, -1)
	cmd := store.SettlePeriod{FlowID: f.ID, PeriodID: p.ID, Version: 1, MaterialCost: dec("10"), ConversionCost: dec("5"), OutputValue: dec("20")}
	if _, _, err := s.SettlePeriod(ctx, cmd, store.Idem{}); problemCode(err) != domain.CodeOpenKanbans {
		t.Fatalf("card in progress since an earlier period: %v", err)
	}
	// Finish, empty and reuse the card: its jobs start afresh and no longer
	// belong to the period.
	_, jobs = jobsOf(t, s, ctx, k.ID)
	runJob(t, s, ctx, jobs[1], true)
	k, _ = jobsOf(t, s, ctx, k.ID)
	walk(t, s, ctx, k, domain.Emptied, domain.NotPlanned)
	settled, _, err := s.SettlePeriod(ctx, cmd, store.Idem{})
	if err != nil || *settled.Variance != "-5.00" || *settled.Kanbans != 0 {
		t.Fatalf("settle after reuse %+v %v", settled, err)
	}
}

func TestCosting_TenantIsolation(t *testing.T) {
	s, _ := setup(t)
	one, two := ctxFor(1), ctxFor(2)
	f := cellFlow(t, s, one, ids.New(), ids.New(), false)
	p := openPeriod(t, s, one, f.ID, -30, -1)
	if _, err := s.ListPeriods(two, f.ID, 50); problemCode(err) != store.CodeFlowNotFound {
		t.Fatalf("tenant 2 listed tenant 1's periods: %v", err)
	}
	if _, _, err := s.OpenPeriod(two, store.OpenPeriod{FlowID: f.ID, PeriodStart: day(-10), PeriodEnd: day(-5), Currency: "USD"}, store.Idem{}); problemCode(err) != store.CodeFlowNotFound {
		t.Fatalf("tenant 2 opened a period on tenant 1's flow: %v", err)
	}
	if _, _, err := s.SettlePeriod(two, store.SettlePeriod{FlowID: f.ID, PeriodID: p.ID, Version: 1, MaterialCost: dec("1"), ConversionCost: dec("1"),
		OutputValue: dec("1")}, store.Idem{}); problemCode(err) != store.CodePeriodNotFound {
		t.Fatalf("tenant 2 settled tenant 1's period: %v", err)
	}
}
