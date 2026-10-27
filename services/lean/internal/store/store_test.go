package store_test

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/platform/dbx"
	"github.com/BenjaminSRussell/passage/platform/events"
	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/platform/tenant"
	"github.com/BenjaminSRussell/passage/platform/testkit"
	"github.com/BenjaminSRussell/passage/services/lean/internal/domain"
	leanevents "github.com/BenjaminSRussell/passage/services/lean/internal/events"
	"github.com/BenjaminSRussell/passage/services/lean/internal/store"
	"github.com/BenjaminSRussell/passage/services/lean/migrations"
)

// setup migrates a throwaway schema as the owner and returns a store on the
// application role (NOSUPERUSER, NOBYPASSRLS), so every query runs under RLS,
// plus the owner pool for assertions across tenants.
func setup(t *testing.T) (*store.Postgres, *pgxpool.Pool) {
	t.Helper()
	admin, app := testkit.DBWithApp(t)
	if err := dbx.Migrate(context.Background(), admin, migrations.FS); err != nil {
		t.Fatal(err)
	}
	return store.New(app), admin
}

func ctxFor(tenantN int) context.Context {
	return tenant.With(context.Background(), tenant.Principal{TenantID: testkit.TenantID(tenantN), UserID: "lean-1", Duties: map[string]struct{}{}})
}

func dec(s string) decimal.Decimal { return decimal.RequireFromString(s) }

func ptr[T any](v T) *T { return &v }

func problemCode(err error) string {
	var p *httpx.Problem
	if errors.As(err, &p) {
		return p.Code
	}
	var de *domain.Error
	if errors.As(err, &de) {
		return de.Code
	}
	return ""
}

func outboxEvents(t *testing.T, admin *pgxpool.Pool, typ string) []events.Event {
	t.Helper()
	rows, err := admin.Query(context.Background(), `select payload from outbox where type = $1 order by created_at, id`, typ)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var out []events.Event
	for rows.Next() {
		var raw []byte
		var evt events.Event
		if err := rows.Scan(&raw); err != nil || json.Unmarshal(raw, &evt) != nil {
			t.Fatalf("scan outbox: %v", err)
		}
		out = append(out, evt)
	}
	return out
}

// demoFlow is the lean-flow-demo fixture: three activities, the last one
// depending on the first two.
func demoFlow(name string, withOutput bool) store.Flow {
	var out *string
	if withOutput {
		out = ptr(ids.New())
	}
	return store.Flow{ValueStreamID: ids.New(), SiteID: ids.New(), Name: name, TaktTimeS: ptr(60), Activities: []store.Activity{
		{Sequence: 1, Name: "Mix", Kind: "process", CycleTimeS: 45, OutputLocationID: out, Predecessors: []int{}},
		{Sequence: 2, Name: "Move to filler", Kind: "transfer", Predecessors: []int{}},
		{Sequence: 3, Name: "Fill", Kind: "process", CycleTimeS: 30, OutputLocationID: ptr(ids.New()), Predecessors: []int{1, 2}},
	}}
}

func fixedRule(code string, kanbans int) store.Rule {
	return store.Rule{Code: code, ItemID: ids.New(), SiteID: ids.New(), RuleType: "manufacturing", Replenishment: "fixed", FixedQty: dec("25"),
		NumberOfKanbans: ptr(kanbans), LeadTimeH: 8, EffectiveFrom: time.Now().UTC().AddDate(0, 0, -1).Format(store.DateLayout)}
}

func eventRule(code string) store.Rule {
	return store.Rule{Code: code, ItemID: ids.New(), SiteID: ids.New(), RuleType: "manufacturing", Replenishment: "event", FixedQty: dec("20"),
		EventSource: ptr("sales"), LeadTimeH: 4, EffectiveFrom: time.Now().UTC().AddDate(0, 0, -1).Format(store.DateLayout)}
}

// walk moves a card through the given states, returning it at the last one.
func walk(t *testing.T, s *store.Postgres, ctx context.Context, k store.Kanban, states ...domain.State) store.Kanban {
	t.Helper()
	for _, to := range states {
		moved, _, err := s.MoveKanban(ctx, k.ID, to, k.Version, store.Idem{})
		if err != nil {
			t.Fatalf("move %s from %s to %s: %v", k.CardNumber, k.State, to, err)
		}
		k = moved
	}
	return k
}

func TestFlows_CreateActivateAndConnectivity(t *testing.T) {
	s, admin := setup(t)
	ctx := ctxFor(1)
	f, fresh, err := s.CreateFlow(ctx, demoFlow("Line 1", true), store.Idem{Key: "flow-1", Fingerprint: "fp"})
	if err != nil || !fresh || f.Status != "draft" || f.ActivityCount != 3 {
		t.Fatalf("create fresh=%v flow=%+v err=%v", fresh, f, err)
	}
	got, err := s.GetFlow(ctx, f.ID)
	if err != nil || len(got.Activities) != 3 || len(got.Activities[2].Predecessors) != 2 || got.Activities[2].Predecessors[1] != 2 {
		t.Fatalf("stored flow %+v %v", got, err)
	}
	again, fresh, err := s.CreateFlow(ctx, demoFlow("Line 1", true), store.Idem{Key: "flow-1", Fingerprint: "fp"})
	if err != nil || fresh || again.ID != f.ID {
		t.Fatalf("replay fresh=%v id=%s err=%v", fresh, again.ID, err)
	}
	if _, _, err := s.CreateFlow(ctx, demoFlow("Line 1", true), store.Idem{Key: "flow-1", Fingerprint: "other"}); problemCode(err) != store.CodeIdempotencyReuse {
		t.Fatalf("key reuse: %v", err)
	}

	from := time.Date(2026, 11, 2, 0, 0, 0, 0, time.UTC)
	if _, _, err := s.ActivateFlow(ctx, f.ID, 9, from, nil, store.Idem{}); problemCode(err) != store.CodeVersionConflict {
		t.Fatalf("stale version: %v", err)
	}
	act, changed, err := s.ActivateFlow(ctx, f.ID, 1, from, nil, store.Idem{Key: "act-1", Fingerprint: "fp"})
	if err != nil || !changed || act.Status != "active" || act.Version != 2 || act.ActiveFrom == nil || *act.ActiveFrom != "2026-11-02" {
		t.Fatalf("activate changed=%v flow=%+v err=%v", changed, act, err)
	}
	if _, changed, err := s.ActivateFlow(ctx, f.ID, 1, from, nil, store.Idem{Key: "act-1", Fingerprint: "fp"}); err != nil || changed {
		t.Fatalf("activate replay changed=%v err=%v", changed, err)
	}
	if _, _, err := s.ActivateFlow(ctx, f.ID, 2, from, nil, store.Idem{}); problemCode(err) != store.CodeFlowNotDraft {
		t.Fatalf("second activation: %v", err)
	}
	if evts := outboxEvents(t, admin, leanevents.FlowActivated); len(evts) != 1 {
		t.Fatalf("flow activated events = %d, want 1", len(evts))
	}

	// FR-24.8: a process activity without an output location cannot go live.
	broken, _, err := s.CreateFlow(ctx, demoFlow("Line 2", false), store.Idem{})
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := s.ActivateFlow(ctx, broken.ID, 1, from, nil, store.Idem{}); problemCode(err) != domain.CodeNoOutput {
		t.Fatalf("unconnected flow activated: %v", err)
	}
	if still, _ := s.GetFlow(ctx, broken.ID); still.Status != "draft" || still.Version != 1 {
		t.Fatalf("refused activation changed the flow: %+v", still)
	}
	dup := demoFlow("Line 1", true)
	dup.ValueStreamID = f.ValueStreamID
	if _, _, err := s.CreateFlow(ctx, dup, store.Idem{}); problemCode(err) != store.CodeDuplicate {
		t.Fatalf("duplicate name in value stream: %v", err)
	}
	if list, err := s.ListFlows(ctx, "active", 10); err != nil || len(list) != 1 || list[0].ID != f.ID {
		t.Fatalf("active flows %+v %v", list, err)
	}
}

// BR-24.6 and BR-24.7 on a fixed rule: cards fill the rule, a card walks the
// states one step at a time, and reuse of an emptied card respects the count.
func TestFixedRule_GenerateMoveAndCapacity(t *testing.T) {
	s, admin := setup(t)
	ctx := ctxFor(1)
	r, _, err := s.CreateRule(ctx, fixedRule("MIX-A", 3), store.Idem{})
	if err != nil {
		t.Fatal(err)
	}
	cards, fresh, err := s.GenerateKanbans(ctx, store.Generate{RuleID: r.ID}, store.Idem{Key: "gen-1", Fingerprint: "fp"})
	if err != nil || !fresh || len(cards) != 3 || cards[0].CardNumber != "MIX-A-0001" || cards[2].CardNumber != "MIX-A-0003" ||
		cards[0].State != domain.NotPlanned || !cards[1].Qty.Equal(dec("25")) || cards[0].Due != nil {
		t.Fatalf("generate fresh=%v cards=%+v err=%v", fresh, cards, err)
	}
	replayed, fresh, err := s.GenerateKanbans(ctx, store.Generate{RuleID: r.ID}, store.Idem{Key: "gen-1", Fingerprint: "fp"})
	if err != nil || fresh || len(replayed) != 3 || replayed[0].ID != cards[0].ID {
		t.Fatalf("replay fresh=%v cards=%d err=%v", fresh, len(replayed), err)
	}
	if _, _, err := s.GenerateKanbans(ctx, store.Generate{RuleID: r.ID, Count: 1}, store.Idem{}); problemCode(err) != domain.CodeCapacity {
		t.Fatalf("over capacity: %v", err)
	}

	k := walk(t, s, ctx, cards[0], domain.Planned)
	if k.Due == nil || k.Version != 2 {
		t.Fatalf("planned card has no due date: %+v", k)
	}
	// AC-24.7: Planned → Completed is refused and the card stays put.
	if _, _, err := s.MoveKanban(ctx, k.ID, domain.Completed, k.Version, store.Idem{}); problemCode(err) != domain.CodeMove {
		t.Fatalf("planned → completed: %v", err)
	}
	if _, _, err := s.MoveKanban(ctx, k.ID, domain.Prepared, 1, store.Idem{}); problemCode(err) != store.CodeVersionConflict {
		t.Fatalf("stale move: %v", err)
	}
	k = walk(t, s, ctx, k, domain.NotPlanned)
	if k.Due != nil {
		t.Fatalf("back to not planned keeps due %v", k.Due)
	}
	k = walk(t, s, ctx, k, domain.Planned, domain.Prepared, domain.InProgress, domain.Completed, domain.Emptied)
	if rule, _ := s.GetRule(ctx, r.ID); rule.ActiveKanbans != 2 {
		t.Fatalf("active kanbans after emptying = %d, want 2", rule.ActiveKanbans)
	}
	// The emptied card's slot is taken by a new card, so reusing it would exceed the rule.
	if _, _, err := s.GenerateKanbans(ctx, store.Generate{RuleID: r.ID, Count: 1}, store.Idem{}); err != nil {
		t.Fatal(err)
	}
	if _, _, err := s.MoveKanban(ctx, k.ID, domain.NotPlanned, k.Version, store.Idem{}); problemCode(err) != domain.CodeCapacity {
		t.Fatalf("reuse over capacity: %v", err)
	}
	board, err := s.Board(ctx, store.BoardFilter{RuleID: r.ID})
	if err != nil || len(board) != 4 {
		t.Fatalf("board shows %d cards (want 3 active + 1 emptied fixed card) %v", len(board), err)
	}
	if n := len(outboxEvents(t, admin, leanevents.KanbanCreated)); n != 4 {
		t.Fatalf("created events = %d, want 4 (the replay writes none)", n)
	}
	if n := len(outboxEvents(t, admin, leanevents.KanbanStateChanged)); n != 7 {
		t.Fatalf("state-changed events = %d, want 7", n)
	}
}

// AC-24.6 (lean-kanban-sim): an event rule with fixed qty 20 and a sales line
// of 50 creates three cards that complete through the board for 60 in total.
func TestEventRule_SalesLineToCompletion(t *testing.T) {
	s, admin := setup(t)
	ctx := ctxFor(1)
	r, _, err := s.CreateRule(ctx, eventRule("FILL-EV"), store.Idem{})
	if err != nil {
		t.Fatal(err)
	}
	cards, _, err := s.GenerateKanbans(ctx, store.Generate{RuleID: r.ID, DemandQty: dec("50"), EventRefType: ptr("sales-line"), EventRefID: ptr("SO-1/10")}, store.Idem{})
	if err != nil || len(cards) != 3 {
		t.Fatalf("event kanbans %+v %v", cards, err)
	}
	total := decimal.Zero
	for i, c := range cards {
		if c.State != domain.Planned || c.Due == nil || *c.EventRefID != "SO-1/10" {
			t.Fatalf("event card %+v", c)
		}
		cards[i] = walk(t, s, ctx, c, domain.Prepared, domain.InProgress, domain.Completed)
		total = total.Add(cards[i].Qty)
	}
	if !total.Equal(dec("60")) {
		t.Fatalf("completed quantity %s, want 60", total)
	}
	evts := outboxEvents(t, admin, leanevents.KanbanCreated)
	var data leanevents.KanbanCreatedData
	if len(evts) != 3 || json.Unmarshal(evts[0].Data, &data) != nil || data.EventRefID == nil || *data.EventRefID != "SO-1/10" || data.Qty != "20" {
		t.Fatalf("created events %d %+v", len(evts), data)
	}
	// BR-24.6: an emptied event card is spent and leaves the board.
	spent := walk(t, s, ctx, cards[0], domain.Emptied)
	if _, _, err := s.MoveKanban(ctx, spent.ID, domain.NotPlanned, spent.Version, store.Idem{}); problemCode(err) != domain.CodeMove {
		t.Fatalf("event card reused: %v", err)
	}
	if board, _ := s.Board(ctx, store.BoardFilter{RuleID: r.ID}); len(board) != 2 {
		t.Fatalf("board shows %d cards, want 2", len(board))
	}
}

func TestGenerate_RefusedForScheduledAndNotEffectiveRules(t *testing.T) {
	s, _ := setup(t)
	ctx := ctxFor(1)
	sched := fixedRule("PLAN-1", 1)
	sched.Replenishment, sched.NumberOfKanbans = "scheduled", nil
	r, _, err := s.CreateRule(ctx, sched, store.Idem{})
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := s.GenerateKanbans(ctx, store.Generate{RuleID: r.ID}, store.Idem{}); problemCode(err) != store.CodeRuleNotGenerable {
		t.Fatalf("scheduled rule generated: %v", err)
	}
	future := fixedRule("LATER-1", 2)
	future.EffectiveFrom = time.Now().UTC().AddDate(0, 1, 0).Format(store.DateLayout)
	r, _, err = s.CreateRule(ctx, future, store.Idem{})
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := s.GenerateKanbans(ctx, store.Generate{RuleID: r.ID}, store.Idem{}); problemCode(err) != store.CodeRuleNotGenerable {
		t.Fatalf("future rule generated: %v", err)
	}
	if _, _, err := s.CreateRule(ctx, fixedRule("LATER-1", 2), store.Idem{}); problemCode(err) != store.CodeDuplicate {
		t.Fatalf("duplicate code: %v", err)
	}
	bad := fixedRule("BAD-1", 2)
	bad.FlowID = ptr(ids.New())
	if _, _, err := s.CreateRule(ctx, bad, store.Idem{}); problemCode(err) != store.CodeFlowNotFound {
		t.Fatalf("unknown flow: %v", err)
	}
	if _, _, err := s.GenerateKanbans(ctx, store.Generate{RuleID: ids.New()}, store.Idem{}); problemCode(err) != store.CodeRuleNotFound {
		t.Fatalf("unknown rule: %v", err)
	}
}

func TestTenantIsolation(t *testing.T) {
	s, _ := setup(t)
	ctx := ctxFor(1)
	f, _, err := s.CreateFlow(ctx, demoFlow("Line 1", true), store.Idem{})
	if err != nil {
		t.Fatal(err)
	}
	rule := fixedRule("MIX-A", 2)
	rule.FlowID = &f.ID
	r, _, err := s.CreateRule(ctx, rule, store.Idem{})
	if err != nil {
		t.Fatal(err)
	}
	cards, _, err := s.GenerateKanbans(ctx, store.Generate{RuleID: r.ID}, store.Idem{})
	if err != nil {
		t.Fatal(err)
	}
	other := ctxFor(2)
	if _, err := s.GetFlow(other, f.ID); problemCode(err) != store.CodeFlowNotFound {
		t.Fatalf("other tenant read the flow: %v", err)
	}
	if _, err := s.GetRule(other, r.ID); problemCode(err) != store.CodeRuleNotFound {
		t.Fatalf("other tenant read the rule: %v", err)
	}
	if _, _, err := s.MoveKanban(other, cards[0].ID, domain.Planned, 1, store.Idem{}); problemCode(err) != store.CodeKanbanNotFound {
		t.Fatalf("other tenant moved a card: %v", err)
	}
	if board, err := s.Board(other, store.BoardFilter{}); err != nil || len(board) != 0 {
		t.Fatalf("other tenant board %d %v", len(board), err)
	}
	if list, err := s.ListRules(ctx, f.ID, 10); err != nil || len(list) != 1 || list[0].ActiveKanbans != 2 {
		t.Fatalf("rules of flow %+v %v", list, err)
	}
	if board, err := s.Board(ctx, store.BoardFilter{FlowID: f.ID}); err != nil || len(board) != 2 {
		t.Fatalf("flow board %d %v", len(board), err)
	}
}

// Two concurrent calls with one Idempotency-Key create the cards once: the
// second waits on the rule lock and then replays the first answer.
func TestGenerate_ConcurrentRetryReplays(t *testing.T) {
	s, _ := setup(t)
	ctx := ctxFor(1)
	r, _, err := s.CreateRule(ctx, fixedRule("RACE-1", 4), store.Idem{})
	if err != nil {
		t.Fatal(err)
	}
	type result struct {
		cards []store.Kanban
		fresh bool
		err   error
	}
	results := make(chan result, 2)
	for range 2 {
		go func() {
			cards, fresh, err := s.GenerateKanbans(ctx, store.Generate{RuleID: r.ID, Count: 2}, store.Idem{Key: "race", Fingerprint: "fp"})
			results <- result{cards, fresh, err}
		}()
	}
	a, b := <-results, <-results
	if a.err != nil || b.err != nil || a.fresh == b.fresh || len(a.cards) != 2 || a.cards[0].ID != b.cards[0].ID {
		t.Fatalf("concurrent generate: %+v / %+v", a, b)
	}
	if rule, _ := s.GetRule(ctx, r.ID); rule.ActiveKanbans != 2 {
		t.Fatalf("active kanbans = %d, want 2", rule.ActiveKanbans)
	}
}
