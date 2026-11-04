package store_test

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/platform/testkit"
	"github.com/BenjaminSRussell/passage/services/planning/internal/domain"
	planevents "github.com/BenjaminSRussell/passage/services/planning/internal/events"
	"github.com/BenjaminSRussell/passage/services/planning/internal/store"
)

// surplusItem is the item of every surplus run, so later runs raise the same messages.
var surplusItem = ids.New()

// surplusRun stores a run whose messages are Decrease (PO-1) and Cancel (PO-2).
func surplusRun(t *testing.T, st *store.Postgres, ctx context.Context, key string) {
	t.Helper()
	surplusRunPO1(t, st, ctx, key, 300)
}

// surplusRunPO1 is surplusRun with PO-1 receiving po1 (Decrease by po1 - 100).
func surplusRunPO1(t *testing.T, st *store.Postgres, ctx context.Context, key string, po1 int64) {
	t.Helper()
	res, err := domain.Run(0, []domain.Item{{
		ID: surplusItem, Coverage: domain.CoverageRequirement, OrderType: domain.OrderPurchase, CoverageDays: 30,
		Demand: []domain.Requirement{{Ref: "SO-1", RefType: domain.RefSales, Date: 40, Qty: decimal.NewFromInt(100), Priority: domain.PrioritySales}},
		Receipts: []domain.Requirement{
			{Ref: "PO-1", RefType: "purchase-line", Date: 40, Qty: decimal.NewFromInt(po1)},
			{Ref: "PO-2", RefType: "purchase-line", Date: 50, Qty: decimal.NewFromInt(80)},
		},
	}})
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := st.RunPlan(ctx, runRequest(res, key, "in-"+key)); err != nil {
		t.Fatal(err)
	}
}

func messagesByAction(t *testing.T, st *store.Postgres, ctx context.Context) map[string]store.Message {
	t.Helper()
	msgs, err := st.ListMessages(ctx, "MAIN", "", store.StatusAll, "", 10)
	if err != nil {
		t.Fatal(err)
	}
	out := map[string]store.Message{}
	for _, m := range msgs {
		out[m.Action] = m
	}
	return out
}

func outboxCount(t *testing.T, admin *pgxpool.Pool, typ string) int {
	t.Helper()
	var n int
	if err := admin.QueryRow(context.Background(), `select count(*) from outbox where type = $1`, typ).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

// TestRunPlan_CarriesMessageDecisions: a run that raises a decided message again unchanged carries
// the decision onto the new version's message (not open again, not raised again, decided once), and
// a message whose quantity changed is a new one that is open. carriedFrom keeps naming the message
// the planner decided across runs.
func TestRunPlan_CarriesMessageDecisions(t *testing.T) {
	st, admin, _ := setup(t)
	ctx := testkit.Ctx(testkit.TenantID(1), "planning.run", "planning.read", "planning.order.maintain")
	surplusRun(t, st, ctx, "run-1")
	first := messagesByAction(t, st, ctx)
	dec, cancel := first[domain.ActionDecrease], first[domain.ActionCancel]
	if _, _, err := st.DecideMessage(ctx, dec.ID, 1, store.MessageDecision{Note: "keep the stock"}); err != nil {
		t.Fatal(err)
	}
	if _, _, err := st.DecideMessage(ctx, cancel.ID, 1, store.MessageDecision{Apply: true, Note: "vendor agreed"}); err != nil {
		t.Fatal(err)
	}

	surplusRun(t, st, ctx, "run-2")
	if open, err := st.ListMessages(ctx, "MAIN", "", "", "", 10); err != nil || len(open) != 0 {
		t.Fatalf("open messages after an unchanged run = %+v, %v; want none", open, err)
	}
	second := messagesByAction(t, st, ctx)
	d2, c2 := second[domain.ActionDecrease], second[domain.ActionCancel]
	if d2.ID == dec.ID || d2.Status != "dismissed" || d2.CarriedFrom == nil || *d2.CarriedFrom != dec.ID || d2.DecisionNote != "keep the stock" ||
		d2.DecidedBy == nil || *d2.DecidedBy != "user-test" || d2.DecidedAt == nil {
		t.Fatalf("carried dismissal = %+v", d2)
	}
	if c2.Status != "applied" || c2.CarriedFrom == nil || *c2.CarriedFrom != cancel.ID || c2.DecisionNote != "vendor agreed" {
		t.Fatalf("carried apply = %+v", c2)
	}
	if _, _, err := st.DecideMessage(ctx, c2.ID, c2.Version, store.MessageDecision{Apply: true}); problemCode(err) != store.CodeMessageDecided {
		t.Fatalf("apply a carried applied message again: %v", err)
	}
	if got := outboxCount(t, admin, planevents.MessageRaised); got != 2 {
		t.Fatalf("message.raised.v1 = %d; want 2 (run 1 only)", got)
	}

	// PO-1 now receives 250: the decrease is 150, a different message, so it is open again.
	surplusRunPO1(t, st, ctx, "run-3", 250)
	third := messagesByAction(t, st, ctx)
	d3, c3 := third[domain.ActionDecrease], third[domain.ActionCancel]
	if d3.Status != "open" || d3.CarriedFrom != nil || !decimal.RequireFromString(d3.Qty).Equal(decimal.NewFromInt(150)) {
		t.Fatalf("changed decrease = %+v; want open", d3)
	}
	if c3.Status != "applied" || c3.CarriedFrom == nil || *c3.CarriedFrom != cancel.ID {
		t.Fatalf("cancel carried over two runs = %+v", c3)
	}
	if got := outboxCount(t, admin, planevents.MessageRaised); got != 3 {
		t.Fatalf("message.raised.v1 = %d; want 3", got)
	}
	if applied, dismissed := outboxCount(t, admin, planevents.MessageApplied), outboxCount(t, admin, planevents.MessageDismissed); applied != 1 || dismissed != 1 {
		t.Fatalf("decision events applied %d dismissed %d; want one each", applied, dismissed)
	}
}

// TestDecideMessage_TenantAndLegalEntityIsolation: another tenant, or a caller scoped to another
// legal entity, neither reads nor decides the message (404), and it stays open.
func TestDecideMessage_TenantAndLegalEntityIsolation(t *testing.T) {
	st, _, _ := setup(t)
	entityA := ids.New()
	ctxA := scopedCtx(testkit.TenantID(1), entityA)
	surplusRun(t, st, ctxA, "run-1")
	msg := firstMessage(t, st, ctxA, domain.ActionCancel)
	for name, ctx := range map[string]context.Context{
		"other tenant": testkit.Ctx(testkit.TenantID(2), "planning.read", "planning.order.maintain"),
		"other entity": scopedCtx(testkit.TenantID(1), ids.New()),
	} {
		if _, err := st.GetMessage(ctx, msg.ID); problemCode(err) != store.CodeMessageNotFound {
			t.Fatalf("%s reads the message: %v", name, err)
		}
		if _, _, err := st.DecideMessage(ctx, msg.ID, 1, store.MessageDecision{}); problemCode(err) != store.CodeMessageNotFound {
			t.Fatalf("%s dismisses the message: %v", name, err)
		}
	}
	if got, err := st.GetMessage(ctxA, msg.ID); err != nil || got.Status != "open" {
		t.Fatalf("message after refused decisions = %+v, %v", got, err)
	}
}

func firstMessage(t *testing.T, st *store.Postgres, ctx context.Context, action string) store.Message {
	t.Helper()
	msgs, err := st.ListMessages(ctx, "MAIN", action, "", "", 10)
	if err != nil || len(msgs) != 1 {
		t.Fatalf("%s messages = %+v, %v", action, msgs, err)
	}
	return msgs[0]
}

// TestDecideMessage_ApplyRecordsDecisionAndEvent: applying a Cancel message marks it applied
// under If-Match, bumps its version, leaves it out of the default (open) list, keeps it in the
// applied list and writes message.applied.v1 with the planner and the note.
func TestDecideMessage_ApplyRecordsDecisionAndEvent(t *testing.T) {
	st, admin, _ := setup(t)
	ctx := testkit.Ctx(testkit.TenantID(1), "planning.run", "planning.read", "planning.order.maintain")
	surplusRun(t, st, ctx, "run-1")
	msg := firstMessage(t, st, ctx, domain.ActionCancel)
	if msg.Status != "open" || msg.Version != 1 || msg.ID == "" || msg.DecidedBy != nil {
		t.Fatalf("new message = %+v", msg)
	}
	got, replayed, err := st.DecideMessage(ctx, msg.ID, 1, store.MessageDecision{Apply: true, Note: "vendor agreed", IdempotencyKey: "k1"})
	if err != nil || replayed {
		t.Fatalf("apply: %+v replayed %v err %v", got, replayed, err)
	}
	if got.Status != "applied" || got.Version != 2 || got.DecidedBy == nil || *got.DecidedBy != "user-test" || got.DecidedAt == nil || got.DecisionNote != "vendor agreed" {
		t.Fatalf("applied message = %+v", got)
	}
	if open, err := st.ListMessages(ctx, "MAIN", domain.ActionCancel, "", "", 10); err != nil || len(open) != 0 {
		t.Fatalf("open list after apply = %+v, %v", open, err)
	}
	if all, err := st.ListMessages(ctx, "MAIN", "", store.StatusAll, "", 10); err != nil || len(all) != 2 {
		t.Fatalf("all list = %+v, %v", all, err)
	}
	if done, err := st.ListMessages(ctx, "MAIN", "", "applied", "", 10); err != nil || len(done) != 1 || done[0].ID != msg.ID {
		t.Fatalf("applied list = %+v, %v", done, err)
	}
	var payload []byte
	if err := admin.QueryRow(context.Background(), `select payload from outbox where type = $1`, planevents.MessageApplied).Scan(&payload); err != nil {
		t.Fatal(err)
	}
	var evt struct {
		Data planevents.MessageDecidedData `json:"data"`
	}
	if err := json.Unmarshal(payload, &evt); err != nil {
		t.Fatal(err)
	}
	d := evt.Data
	if d.MessageID != msg.ID || d.Action != "cancel" || d.SupplyRef != "PO-2" || d.DecidedBy != "user-test" || d.Note != "vendor agreed" || d.Version != 2 || d.PlanCode != "MAIN" {
		t.Fatalf("event data = %+v", d)
	}
}

// TestDecideMessage_Rules: a stale If-Match is 412, a decided message is 409 unless the same
// Idempotency-Key replays it, an information message cannot be applied but can be dismissed,
// and an unknown id is 404. Nothing is written by a refusal.
func TestDecideMessage_Rules(t *testing.T) {
	st, admin, _ := setup(t)
	ctx := testkit.Ctx(testkit.TenantID(1), "planning.run", "planning.read", "planning.order.maintain")
	surplusRun(t, st, ctx, "run-1")
	dec := firstMessage(t, st, ctx, domain.ActionDecrease)

	if _, _, err := st.DecideMessage(ctx, dec.ID, 7, store.MessageDecision{Apply: true}); problemCode(err) != store.CodeVersionConflict {
		t.Fatalf("stale If-Match: %v", err)
	}
	if _, _, err := st.DecideMessage(ctx, ids.New(), 1, store.MessageDecision{}); problemCode(err) != store.CodeMessageNotFound {
		t.Fatalf("unknown id: %v", err)
	}
	if _, _, err := st.DecideMessage(ctx, dec.ID, 1, store.MessageDecision{Apply: false, IdempotencyKey: "same"}); err != nil {
		t.Fatalf("dismiss: %v", err)
	}
	again, replayed, err := st.DecideMessage(ctx, dec.ID, 1, store.MessageDecision{Apply: false, IdempotencyKey: "same"})
	if err != nil || !replayed || again.Status != "dismissed" || again.Version != 2 {
		t.Fatalf("replay: %+v replayed %v err %v", again, replayed, err)
	}
	if _, _, err := st.DecideMessage(ctx, dec.ID, 2, store.MessageDecision{Apply: true}); problemCode(err) != store.CodeMessageDecided {
		t.Fatalf("apply after dismiss: %v", err)
	}
	if _, _, err := st.DecideMessage(ctx, dec.ID, 2, store.MessageDecision{Apply: false, IdempotencyKey: "other"}); problemCode(err) != store.CodeMessageDecided {
		t.Fatalf("other key after dismiss: %v", err)
	}
	var dismissed, applied int
	if err := admin.QueryRow(context.Background(), `select count(*) filter (where type = $1), count(*) filter (where type = $2) from outbox`,
		planevents.MessageDismissed, planevents.MessageApplied).Scan(&dismissed, &applied); err != nil || dismissed != 1 || applied != 0 {
		t.Fatalf("events dismissed %d applied %d, %v", dismissed, applied, err)
	}
}

// TestDecideMessage_InformationAndStaleVersion: a sales-on-top message is dismissed but not
// applied (422), and after a new run the old version's message is 409 PLAN-MESSAGE-STALE.
func TestDecideMessage_InformationAndStaleVersion(t *testing.T) {
	st, _, _ := setup(t)
	ctx := testkit.Ctx(testkit.TenantID(1), "planning.run", "planning.read", "planning.order.maintain")
	res, err := domain.Run(0, []domain.Item{{ID: ids.New(), Coverage: domain.CoverageRequirement, OrderType: domain.OrderPurchase, LeadTimeDays: 2,
		SalesOnTop: []domain.Requirement{{Ref: "SO-1", RefType: domain.RefSales, Date: 4, Qty: decimal.NewFromInt(30)}}}})
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := st.RunPlan(ctx, runRequest(res, "run-1", "in-1")); err != nil {
		t.Fatal(err)
	}
	msg := firstMessage(t, st, ctx, domain.ActionSalesOnTop)
	_, _, err = st.DecideMessage(ctx, msg.ID, 1, store.MessageDecision{Apply: true})
	if problemCode(err) != store.CodeMessageNotActionable {
		t.Fatalf("apply information: %v", err)
	}
	if _, _, err := st.RunPlan(ctx, runRequest(res, "run-2", "in-2")); err != nil {
		t.Fatal(err)
	}
	if _, _, err := st.DecideMessage(ctx, msg.ID, 1, store.MessageDecision{Apply: false}); problemCode(err) != store.CodeMessageStale {
		t.Fatalf("dismiss message of an old version: %v", err)
	}
	if got, err := st.GetMessage(ctx, msg.ID); err != nil || got.Status != "open" {
		t.Fatalf("get old message = %+v, %v", got, err)
	}
}
