package store_test

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/platform/tenant"
	"github.com/BenjaminSRussell/passage/platform/testkit"
	"github.com/BenjaminSRussell/passage/services/processmfg/internal/domain"
	pmfevents "github.com/BenjaminSRussell/passage/services/processmfg/internal/events"
	"github.com/BenjaminSRussell/passage/services/processmfg/internal/store"
)

func num(s string) domain.Value {
	d := decimal.RequireFromString(s)
	return domain.Value{Num: &d}
}

func defineItem(t *testing.T, s *store.Postgres, item string) {
	t.Helper()
	ctx := ctxFor(1, "")
	lo, hi := dec("40"), dec("60")
	for i, d := range []store.AttributeDefinition{
		{ItemID: item, Name: "potency", Kind: "numeric", Min: &lo, Max: &hi},
		{ItemID: item, Name: "grade", Kind: "enum", Options: []string{"A", "B"}},
		{ItemID: item, Name: "expiry", Kind: "date"},
	} {
		if _, created, err := s.DefineAttribute(ctx, d, store.Idem{Key: "def-" + d.Name + item, Fingerprint: d.Name}); err != nil || !created {
			t.Fatalf("define %d: created=%v err=%v", i, created, err)
		}
	}
}

func register(t *testing.T, s *store.Postgres, item, number string) store.Batch {
	t.Helper()
	b, created, err := s.RegisterBatch(ctxFor(1, ""), store.NewBatch{LegalEntityID: entityA, ItemID: item, BatchNumber: number, Disposition: "available"},
		store.Idem{Key: "reg-" + number + item, Fingerprint: number})
	if err != nil || !created || b.Version != 1 {
		t.Fatalf("register: created=%v %+v err=%v", created, b, err)
	}
	return b
}

func TestDefineAttribute_ValidatesDuplicatesAndReplays(t *testing.T) {
	s, admin := setup(t)
	ctx := ctxFor(1, "")
	item := ids.New()
	lo, hi := dec("60"), dec("40")
	if _, _, err := s.DefineAttribute(ctx, store.AttributeDefinition{ItemID: item, Name: "p", Kind: "numeric", Min: &lo, Max: &hi}, store.Idem{}); problemCode(err) != domain.CodeInvalid {
		t.Fatalf("min above max: %v", err)
	}
	d := store.AttributeDefinition{ItemID: item, Name: "grade", Kind: "enum", Options: []string{"A"}}
	first, created, err := s.DefineAttribute(ctx, d, store.Idem{Key: "k", Fingerprint: "f"})
	if err != nil || !created {
		t.Fatal(err)
	}
	again, created, err := s.DefineAttribute(ctx, d, store.Idem{Key: "k", Fingerprint: "f"})
	if err != nil || created || again.ID != first.ID {
		t.Fatalf("replay created=%v %+v err=%v", created, again, err)
	}
	if _, _, err := s.DefineAttribute(ctx, d, store.Idem{Key: "k", Fingerprint: "other"}); problemCode(err) != store.CodeIdempotencyReuse {
		t.Fatalf("same key other body: %v", err)
	}
	if _, _, err := s.DefineAttribute(ctx, d, store.Idem{Key: "k2", Fingerprint: "f"}); problemCode(err) != store.CodeAttributeDuplicate {
		t.Fatalf("duplicate name: %v", err)
	}
	if n := len(outboxEvents(t, admin, pmfevents.BatchAttributeDefined)); n != 1 {
		t.Fatalf("%d defined events, want 1", n)
	}
	list, err := s.ListAttributeDefinitions(ctx, item, 10)
	if err != nil || len(list) != 1 || list[0].Options[0] != "A" {
		t.Fatalf("list %+v err=%v", list, err)
	}
	if other, _ := s.ListAttributeDefinitions(ctxFor(2, ""), item, 10); len(other) != 0 {
		t.Fatalf("tenant 2 sees %+v", other)
	}
}

func TestRegisterBatch_DuplicateReplayAndIsolation(t *testing.T) {
	s, admin := setup(t)
	item := ids.New()
	b := register(t, s, item, "B-1")
	again, created, err := s.RegisterBatch(ctxFor(1, ""), store.NewBatch{LegalEntityID: entityA, ItemID: item, BatchNumber: "B-1", Disposition: "available"}, store.Idem{Key: "reg-B-1" + item, Fingerprint: "B-1"})
	if err != nil || created || again.ID != b.ID {
		t.Fatalf("replay created=%v err=%v", created, err)
	}
	if _, _, err := s.RegisterBatch(ctxFor(1, ""), store.NewBatch{LegalEntityID: entityA, ItemID: item, BatchNumber: "B-1", Disposition: "available"}, store.Idem{Key: "new", Fingerprint: "n"}); problemCode(err) != store.CodeBatchDuplicate {
		t.Fatalf("duplicate number: %v", err)
	}
	if n := len(outboxEvents(t, admin, pmfevents.BatchRegistered)); n != 1 {
		t.Fatalf("%d registered events, want 1", n)
	}
	// Another tenant cannot read it; another legal entity's scope cannot either.
	if _, err := s.GetBatch(ctxFor(2, ""), b.ID); problemCode(err) != store.CodeBatchNotFound {
		t.Fatalf("tenant 2: %v", err)
	}
	if _, err := s.GetBatch(ctxFor(1, entityB), b.ID); problemCode(err) != store.CodeBatchNotFound {
		t.Fatalf("entity B scope: %v", err)
	}
	if list, err := s.ListBatches(ctxFor(1, entityB), store.BatchFilter{Limit: 10}); err != nil || len(list) != 0 {
		t.Fatalf("entity B list %+v err=%v", list, err)
	}
	if list, err := s.ListBatches(ctxFor(1, ""), store.BatchFilter{ItemID: item, Disposition: "available", Limit: 10}); err != nil || len(list) != 1 {
		t.Fatalf("list %+v err=%v", list, err)
	}
	// q finds the batch by part of its number, ignoring case.
	if list, err := s.ListBatches(ctxFor(1, ""), store.BatchFilter{Q: strings.ToLower(b.BatchNumber), Limit: 10}); err != nil || len(list) != 1 || list[0].ID != b.ID {
		t.Fatalf("search %q: %+v err=%v", b.BatchNumber, list, err)
	}
	if list, err := s.ListBatches(ctxFor(1, ""), store.BatchFilter{Q: "no-such-batch", Limit: 10}); err != nil || len(list) != 0 {
		t.Fatalf("search for nothing: %+v err=%v", list, err)
	}
}

// FR-24.5: values are checked against the definitions; an out-of-limit value is
// recorded and flagged; the expiry date lands on the batch; history is kept.
func TestRecordAttributes_ChecksRecordsAndKeepsHistory(t *testing.T) {
	s, admin := setup(t)
	ctx := ctxFor(1, "")
	item := ids.New()
	defineItem(t, s, item)
	b := register(t, s, item, "B-2")
	exp := time.Date(2027, 3, 1, 0, 0, 0, 0, time.UTC)

	res, changed, err := s.RecordAttributes(ctx, b.ID, map[string]domain.Value{"potency": num("38.5"), "grade": {Text: ptr("A")}, "expiry": {Date: &exp}},
		1, store.Idem{Key: "rec1", Fingerprint: "1"})
	if err != nil || !changed || res.Batch.Version != 2 {
		t.Fatalf("record changed=%v %+v err=%v", changed, res.Batch.Version, err)
	}
	if len(res.Checks) != 3 || res.Checks[2].Name != "potency" || res.Checks[2].Pass {
		t.Fatalf("checks %+v", res.Checks)
	}
	if res.Batch.ExpiryDate == nil || *res.Batch.ExpiryDate != "2027-03-01" || len(res.Batch.Attributes) != 3 {
		t.Fatalf("batch %+v", res.Batch)
	}

	// A retry replays the first answer and writes nothing more.
	rep, changed, err := s.RecordAttributes(ctx, b.ID, map[string]domain.Value{"potency": num("38.5"), "grade": {Text: ptr("A")}, "expiry": {Date: &exp}},
		1, store.Idem{Key: "rec1", Fingerprint: "1"})
	if err != nil || changed || rep.Batch.Version != 2 {
		t.Fatalf("replay changed=%v v%d err=%v", changed, rep.Batch.Version, err)
	}
	// Stale version, undefined attribute, wrong type and a non-option are refused and write nothing.
	if _, _, err := s.RecordAttributes(ctx, b.ID, map[string]domain.Value{"potency": num("50")}, 1, store.Idem{Key: "stale", Fingerprint: "s"}); problemCode(err) != store.CodeVersionConflict {
		t.Fatalf("stale: %v", err)
	}
	for name, v := range map[string]map[string]domain.Value{
		"undefined": {"colour": {Text: ptr("red")}},
		"wrong":     {"potency": {Text: ptr("high")}},
		"option":    {"grade": {Text: ptr("Z")}},
	} {
		if _, _, err := s.RecordAttributes(ctx, b.ID, v, 2, store.Idem{Key: "bad-" + name, Fingerprint: name}); problemCode(err) != domain.CodeInvalid {
			t.Fatalf("%s: %v", name, err)
		}
	}
	// A second recording corrects the potency: the latest value is current, both stay in the history.
	res2, _, err := s.RecordAttributes(ctx, b.ID, map[string]domain.Value{"potency": num("49.9")}, 2, store.Idem{Key: "rec2", Fingerprint: "2"})
	if err != nil || res2.Batch.Version != 3 || !res2.Checks[0].Pass {
		t.Fatalf("second record %+v err=%v", res2, err)
	}
	got, err := s.GetBatch(ctx, b.ID)
	if err != nil || len(got.Attributes) != 3 || len(got.AttributeHistory) != 4 {
		t.Fatalf("detail attrs=%d history=%d err=%v", len(got.Attributes), len(got.AttributeHistory), err)
	}
	for _, a := range got.Attributes {
		if a.Name == "potency" && (a.Num == nil || !a.Num.Equal(dec("49.9")) || !a.Pass) {
			t.Fatalf("current potency %+v", a)
		}
	}
	evts := outboxEvents(t, admin, pmfevents.BatchAttributesRecorded)
	if len(evts) != 2 {
		t.Fatalf("%d recorded events, want 2", len(evts))
	}
	var first pmfevents.BatchAttributesRecordedData
	if err := json.Unmarshal(evts[0].Data, &first); err != nil || first.Failures != 1 || first.ExpiryDate != "2027-03-01" || len(first.Attributes) != 3 {
		t.Fatalf("event %+v err=%v", first, err)
	}
}

func ptr(s string) *string { return &s }

func TestChangeDisposition_HistoryVersionAndReplay(t *testing.T) {
	s, admin := setup(t)
	ctx := ctxFor(1, "")
	b := register(t, s, ids.New(), "B-3")
	held, changed, err := s.ChangeDisposition(ctx, b.ID, "on-hold", "potency below minimum", 1, store.Idem{Key: "d1", Fingerprint: "1"})
	if err != nil || !changed || held.Disposition != "on-hold" || held.Version != 2 || len(held.Dispositions) != 1 {
		t.Fatalf("hold changed=%v %+v err=%v", changed, held, err)
	}
	if c := held.Dispositions[0]; c.From != "available" || c.To != "on-hold" || c.Reason != "potency below minimum" || c.ChangedBy != "planner-1" {
		t.Fatalf("history %+v", c)
	}
	if _, changed, err := s.ChangeDisposition(ctx, b.ID, "on-hold", "potency below minimum", 1, store.Idem{Key: "d1", Fingerprint: "1"}); err != nil || changed {
		t.Fatalf("replay changed=%v err=%v", changed, err)
	}
	if _, _, err := s.ChangeDisposition(ctx, b.ID, "rejected", "x", 1, store.Idem{Key: "d2", Fingerprint: "2"}); problemCode(err) != store.CodeVersionConflict {
		t.Fatalf("stale: %v", err)
	}
	if _, _, err := s.ChangeDisposition(ctx, b.ID, "on-hold", "again", 2, store.Idem{Key: "d3", Fingerprint: "3"}); problemCode(err) != store.CodeDispositionUnchanged {
		t.Fatalf("unchanged: %v", err)
	}
	rel, _, err := s.ChangeDisposition(ctx, b.ID, "available", "retest passed", 2, store.Idem{Key: "d4", Fingerprint: "4"})
	if err != nil || rel.Version != 3 || len(rel.Dispositions) != 2 || rel.Dispositions[0].To != "available" {
		t.Fatalf("release %+v err=%v", rel, err)
	}
	if n := len(outboxEvents(t, admin, pmfevents.BatchDispositionChanged)); n != 2 {
		t.Fatalf("%d disposition events, want 2", n)
	}
	if _, _, err := s.ChangeDisposition(ctxFor(2, ""), b.ID, "rejected", "x", 3, store.Idem{Key: "d5", Fingerprint: "5"}); problemCode(err) != store.CodeBatchNotFound {
		t.Fatalf("other tenant: %v", err)
	}
}

func ctxAs(user string) context.Context {
	return tenant.With(context.Background(), tenant.Principal{TenantID: testkit.TenantID(1), UserID: user, Duties: testkit.ScopeDuties("")})
}

// PRD 4.24.4 role: one potency attribute per item, numeric only; its recorded
// value travels on the attributes-recorded event (PRD 4.24.5 potency).
func TestDefineAttribute_PotencyRoleAndEvent(t *testing.T) {
	s, admin := setup(t)
	ctx := ctxFor(1, "")
	item := ids.New()
	lo := dec("40")
	assay, created, err := s.DefineAttribute(ctx, store.AttributeDefinition{ItemID: item, Name: "assay", Kind: "numeric", Role: "potency", Min: &lo}, store.Idem{})
	if err != nil || !created || assay.Role != "potency" {
		t.Fatalf("potency def %+v err=%v", assay, err)
	}
	note, _, err := s.DefineAttribute(ctx, store.AttributeDefinition{ItemID: item, Name: "note", Kind: "text"}, store.Idem{})
	if err != nil || note.Role != "generic" {
		t.Fatalf("role default %+v err=%v", note, err)
	}
	if _, _, err := s.DefineAttribute(ctx, store.AttributeDefinition{ItemID: item, Name: "assay2", Kind: "numeric", Role: "potency"}, store.Idem{}); problemCode(err) != store.CodeAttributeDuplicate {
		t.Fatalf("second potency attribute: %v", err)
	}
	if _, _, err := s.DefineAttribute(ctx, store.AttributeDefinition{ItemID: item, Name: "grade", Kind: "enum", Role: "potency", Options: []string{"A"}}, store.Idem{}); problemCode(err) != domain.CodeInvalid {
		t.Fatalf("potency on an enum: %v", err)
	}
	if list, _ := s.ListAttributeDefinitions(ctx, item, 10); len(list) != 2 || list[0].Role != "potency" || list[1].Role != "generic" {
		t.Fatalf("list %+v", list)
	}
	b := register(t, s, item, "B-P")
	if _, _, err := s.RecordAttributes(ctx, b.ID, map[string]domain.Value{"assay": num("38.5")}, 1, store.Idem{}); err != nil {
		t.Fatal(err)
	}
	if _, _, err := s.RecordAttributes(ctx, b.ID, map[string]domain.Value{"note": {Text: ptr("retest")}}, 2, store.Idem{}); err != nil {
		t.Fatal(err)
	}
	evts := outboxEvents(t, admin, pmfevents.BatchAttributesRecorded)
	var first, second pmfevents.BatchAttributesRecordedData
	if len(evts) != 2 || json.Unmarshal(evts[0].Data, &first) != nil || json.Unmarshal(evts[1].Data, &second) != nil {
		t.Fatalf("%d events", len(evts))
	}
	if first.Potency != "38.5" || first.Failures != 1 || second.Potency != "" {
		t.Fatalf("potency on events: %q (failures %d), %q", first.Potency, first.Failures, second.Potency)
	}
}

// PRD 4.24.17: recording attributes and changing the disposition of the same
// batch as the same user is flagged on the change and its event, not refused.
func TestChangeDisposition_FlagsSameUserSegregationConflict(t *testing.T) {
	s, admin := setup(t)
	item := ids.New()
	defineItem(t, s, item)
	b := register(t, s, item, "B-SOD")
	if _, _, err := s.RecordAttributes(ctxAs("tech-1"), b.ID, map[string]domain.Value{"potency": num("50")}, 1, store.Idem{}); err != nil {
		t.Fatal(err)
	}
	other, _, err := s.ChangeDisposition(ctxAs("qa-1"), b.ID, "on-hold", "awaiting retest", 2, store.Idem{})
	if err != nil || other.Dispositions[0].SegregationConflict {
		t.Fatalf("another user is flagged: %+v err=%v", other.Dispositions, err)
	}
	same, _, err := s.ChangeDisposition(ctxAs("tech-1"), b.ID, "available", "my own retest passed", 3, store.Idem{})
	if err != nil || !same.Dispositions[0].SegregationConflict || same.Disposition != "available" {
		t.Fatalf("same user not flagged: %+v err=%v", same.Dispositions, err)
	}
	evts := outboxEvents(t, admin, pmfevents.BatchDispositionChanged)
	var last pmfevents.BatchDispositionChangedData
	if len(evts) != 2 || json.Unmarshal(evts[1].Data, &last) != nil || !last.SegregationConflict {
		t.Fatalf("event %+v", last)
	}
}

// A batch links only to a batch order of its own legal entity that the caller
// can see, and a caller scoped to one legal entity cannot register into another.
func TestRegisterBatch_OrderLinkAndLegalEntityWrites(t *testing.T) {
	s, admin := setup(t)
	ctx := ctxFor(1, "")
	f, _, err := s.CreateFormula(ctx, ac241("DOUGH-B"), store.Idem{})
	if err != nil {
		t.Fatal(err)
	}
	o, _, err := s.CreateBatchOrder(ctx, store.NewBatchOrder{LegalEntityID: entityA, FormulaID: f.ID, BatchSize: dec("1000")}, store.Idem{})
	if err != nil {
		t.Fatal(err)
	}
	missing := ids.New()
	if _, _, err := s.RegisterBatch(ctx, store.NewBatch{LegalEntityID: entityA, ItemID: f.ItemID, BatchNumber: "L-0", BatchOrderID: &missing, Disposition: "available"}, store.Idem{}); problemCode(err) != store.CodeOrderNotFound {
		t.Fatalf("unknown order: %v", err)
	}
	if _, _, err := s.RegisterBatch(ctx, store.NewBatch{LegalEntityID: entityB, ItemID: f.ItemID, BatchNumber: "L-1", BatchOrderID: &o.ID, Disposition: "available"}, store.Idem{}); problemCode(err) != domain.CodeInvalid {
		t.Fatalf("order of another entity: %v", err)
	}
	linked, _, err := s.RegisterBatch(ctx, store.NewBatch{LegalEntityID: entityA, ItemID: f.ItemID, BatchNumber: "L-2", BatchOrderID: &o.ID, Disposition: "quarantine"}, store.Idem{})
	if err != nil || linked.BatchOrderID == nil || *linked.BatchOrderID != o.ID || linked.Disposition != "quarantine" {
		t.Fatalf("linked %+v err=%v", linked, err)
	}
	if _, _, err := s.RegisterBatch(ctxFor(1, entityA), store.NewBatch{LegalEntityID: entityB, ItemID: f.ItemID, BatchNumber: "L-3", Disposition: "available"}, store.Idem{}); err == nil {
		t.Fatal("a caller scoped to entity A registered a batch in entity B")
	}
	var n int
	if err := admin.QueryRow(context.Background(), `select count(*) from batch_record`).Scan(&n); err != nil || n != 1 {
		t.Fatalf("%d batches stored, want 1 (err %v)", n, err)
	}
}

// PickCandidates reads the disposition, expiry and current attribute values of
// the stored batches, in the order asked, and the item's potency attribute.
func TestPickCandidates_ReadsRegisterState(t *testing.T) {
	s, _ := setup(t)
	ctx := ctxFor(1, "")
	item, other := ids.New(), ids.New()
	lo, hi := dec("40"), dec("60")
	if _, _, err := s.DefineAttribute(ctx, store.AttributeDefinition{ItemID: item, Name: "assay", Kind: "numeric", Role: "potency", Min: &lo, Max: &hi}, store.Idem{Key: "pc-def", Fingerprint: "1"}); err != nil {
		t.Fatal(err)
	}
	if _, _, err := s.DefineAttribute(ctx, store.AttributeDefinition{ItemID: item, Name: "expiry", Kind: "date"}, store.Idem{Key: "pc-def2", Fingerprint: "2"}); err != nil {
		t.Fatal(err)
	}
	a, b := register(t, s, item, "PC-1"), register(t, s, item, "PC-2")
	foreign := register(t, s, other, "PC-9")
	exp := time.Date(2027, 3, 1, 0, 0, 0, 0, time.UTC)
	if _, _, err := s.RecordAttributes(ctx, a.ID, map[string]domain.Value{"assay": num("52"), "expiry": {Date: &exp}}, 1, store.Idem{Key: "pc-rec", Fingerprint: "r"}); err != nil {
		t.Fatal(err)
	}
	if _, _, err := s.ChangeDisposition(ctx, b.ID, "on-hold", "awaiting retest", 1, store.Idem{Key: "pc-hold", Fingerprint: "h"}); err != nil {
		t.Fatal(err)
	}
	got, potency, err := s.PickCandidates(ctx, item, []store.OnHand{{BatchID: b.ID, Qty: dec("7")}, {BatchID: a.ID, Qty: dec("40")}})
	if err != nil || potency != "assay" || len(got) != 2 {
		t.Fatalf("got %+v potency %q err=%v", got, potency, err)
	}
	if got[0].Batch.ID != b.ID || got[0].BatchNumber != "PC-2" || got[0].Batch.Disposition != "on-hold" || got[0].Batch.Qty.String() != "7" {
		t.Fatalf("first %+v", got[0])
	}
	v := got[1].Batch.Values
	if got[1].Batch.Disposition != "available" || v["assay"].Num == nil || v["assay"].Num.String() != "52" || v["expiry"].Date == nil || !v["expiry"].Date.Equal(exp) {
		t.Fatalf("second %+v", got[1])
	}
	if _, _, err := s.PickCandidates(ctx, item, []store.OnHand{{BatchID: foreign.ID, Qty: dec("1")}}); problemCode(err) != domain.CodeInvalid {
		t.Fatalf("batch of another item: %v", err)
	}
	if _, _, err := s.PickCandidates(ctx, item, []store.OnHand{{BatchID: ids.New(), Qty: dec("1")}}); problemCode(err) != store.CodeBatchNotFound {
		t.Fatalf("unknown batch: %v", err)
	}
	if _, _, err := s.PickCandidates(ctxFor(2, ""), item, []store.OnHand{{BatchID: a.ID, Qty: dec("1")}}); problemCode(err) != store.CodeBatchNotFound {
		t.Fatalf("tenant 2: %v", err)
	}
	if _, _, err := s.PickCandidates(ctxFor(1, entityB), item, []store.OnHand{{BatchID: a.ID, Qty: dec("1")}}); problemCode(err) != store.CodeBatchNotFound {
		t.Fatalf("other legal entity: %v", err)
	}
	if _, p, err := s.PickCandidates(ctx, other, nil); err != nil || p != "" {
		t.Fatalf("item without potency attribute: %q %v", p, err)
	}
	// Only registered batches in the caller's scope are reported.
	if got, err := s.RegisteredBatches(ctx, []string{b.ID, ids.New()}); err != nil || len(got) != 1 || got[0] != b.ID {
		t.Fatalf("registered: %v %v", got, err)
	}
	if got, err := s.RegisteredBatches(ctxFor(1, entityB), []string{b.ID}); err != nil || len(got) != 0 {
		t.Fatalf("registered, other legal entity: %v %v", got, err)
	}
}
