package store_test

import (
	"encoding/json"
	"testing"

	"github.com/BenjaminSRussell/passage/platform/ids"
	pmfevents "github.com/BenjaminSRussell/passage/services/processmfg/internal/events"
	"github.com/BenjaminSRussell/passage/services/processmfg/internal/store"
)

func defineRule(t *testing.T, s *store.Postgres, item, attr, agg string, decimals *int) store.InheritanceRule {
	t.Helper()
	r, created, err := s.CreateInheritanceRule(ctxFor(1, ""), store.InheritanceRule{LegalEntityID: entityA, ItemID: item, Attribute: attr, Aggregation: agg, Decimals: decimals},
		store.Idem{Key: "rule-" + attr + item, Fingerprint: attr})
	if err != nil || !created || r.Version != 1 {
		t.Fatalf("define rule %s: created=%v %+v err=%v", attr, created, r, err)
	}
	return r
}

func TestInheritanceRule_ValidatesDuplicatesRetireAndEvents(t *testing.T) {
	s, admin := setup(t)
	ctx := ctxFor(1, "")
	item := ids.New()
	defineItem(t, s, item)

	two := 2
	rule := defineRule(t, s, item, "potency", "weighted-average", &two)
	if rule.Decimals == nil || *rule.Decimals != 2 {
		t.Fatalf("decimals %+v", rule)
	}
	// Replay returns the first answer, no second event.
	if again, created, err := s.CreateInheritanceRule(ctx, store.InheritanceRule{LegalEntityID: entityA, ItemID: item, Attribute: "potency", Aggregation: "weighted-average", Decimals: &two},
		store.Idem{Key: "rule-potency" + item, Fingerprint: "potency"}); err != nil || created || again.ID != rule.ID {
		t.Fatalf("replay created=%v err=%v", created, err)
	}
	// One active rule per item and attribute.
	if _, _, err := s.CreateInheritanceRule(ctx, store.InheritanceRule{LegalEntityID: entityA, ItemID: item, Attribute: "potency", Aggregation: "min"},
		store.Idem{Key: "dup", Fingerprint: "dup"}); problemCode(err) != store.CodeRuleDuplicate {
		t.Fatalf("duplicate: %v", err)
	}
	// The item must define the attribute and the aggregation must fit its kind.
	if _, _, err := s.CreateInheritanceRule(ctx, store.InheritanceRule{LegalEntityID: entityA, ItemID: item, Attribute: "colour", Aggregation: "copy"},
		store.Idem{Key: "nodef", Fingerprint: "nodef"}); problemCode(err) != "COM-0001" {
		t.Fatalf("undefined attribute: %v", err)
	}
	if _, _, err := s.CreateInheritanceRule(ctx, store.InheritanceRule{LegalEntityID: entityA, ItemID: item, Attribute: "grade", Aggregation: "max"},
		store.Idem{Key: "kind", Fingerprint: "kind"}); problemCode(err) != "COM-0001" {
		t.Fatalf("kind mismatch: %v", err)
	}
	fixed := "B"
	if r, _, err := s.CreateInheritanceRule(ctx, store.InheritanceRule{LegalEntityID: entityA, ItemID: item, Attribute: "grade", Aggregation: "fixed", FixedText: &fixed},
		store.Idem{Key: "fixed", Fingerprint: "fixed"}); err != nil || r.FixedText == nil || *r.FixedText != "B" {
		t.Fatalf("fixed rule %+v err=%v", r, err)
	}

	list, err := s.ListInheritanceRules(ctx, store.RuleFilter{ItemID: item, Limit: 10})
	if err != nil || len(list) != 2 || list[0].Attribute != "grade" || list[1].Attribute != "potency" {
		t.Fatalf("list %+v err=%v", list, err)
	}

	if _, _, err := s.RetireInheritanceRule(ctx, rule.ID, 5, store.Idem{Key: "ret0", Fingerprint: "a"}); problemCode(err) != store.CodeVersionConflict {
		t.Fatalf("stale retire: %v", err)
	}
	ret, changed, err := s.RetireInheritanceRule(ctx, rule.ID, 1, store.Idem{Key: "ret1", Fingerprint: "b"})
	if err != nil || !changed || ret.RetiredAt == nil || ret.Version != 2 {
		t.Fatalf("retire %+v changed=%v err=%v", ret, changed, err)
	}
	if _, _, err := s.RetireInheritanceRule(ctx, rule.ID, 2, store.Idem{Key: "ret2", Fingerprint: "c"}); problemCode(err) != store.CodeRuleRetired {
		t.Fatalf("second retire: %v", err)
	}
	// A retired rule can be replaced.
	defineRule2 := store.InheritanceRule{LegalEntityID: entityA, ItemID: item, Attribute: "potency", Aggregation: "min"}
	if _, created, err := s.CreateInheritanceRule(ctx, defineRule2, store.Idem{Key: "again", Fingerprint: "again"}); err != nil || !created {
		t.Fatalf("replace: created=%v err=%v", created, err)
	}
	if active, _ := s.ListInheritanceRules(ctx, store.RuleFilter{ItemID: item, Active: true, Limit: 10}); len(active) != 2 {
		t.Fatalf("active %+v", active)
	}
	if _, err := s.GetInheritanceRule(ctxFor(2, ""), rule.ID); problemCode(err) != store.CodeRuleNotFound {
		t.Fatalf("other tenant: %v", err)
	}
	if n := len(outboxEvents(t, admin, pmfevents.InheritanceRuleDefined)); n != 3 {
		t.Fatalf("defined events %d", n)
	}
	if n := len(outboxEvents(t, admin, pmfevents.InheritanceRuleRetired)); n != 1 {
		t.Fatalf("retired events %d", n)
	}
}

// AC-24.11: the receiver gets the weighted potency, the earliest expiry and the
// agreed grade from its senders; the derivation is logged and re-run moves the version.
func TestDeriveBatch_AppliesRulesLogsAndEvents(t *testing.T) {
	s, admin := setup(t)
	ctx := ctxFor(1, "")
	item := ids.New()
	defineItem(t, s, item)
	two := 2
	defineRule(t, s, item, "potency", "weighted-average", &two)
	defineRule(t, s, item, "expiry", "earliest-date", nil)
	defineRule(t, s, item, "grade", "copy", nil)
	a := recordFor(t, s, register(t, s, item, "S-A"), "40", "A", "2027-06-01")
	b := recordFor(t, s, register(t, s, item, "S-B"), "45", "A", "2027-03-01")
	recv := register(t, s, item, "R-1")

	in := store.DeriveInput{BatchID: recv.ID, Version: recv.Version, Senders: []store.DeriveSender{{BatchID: a.ID, Qty: dec("200")}, {BatchID: b.ID, Qty: dec("300")}}}
	res, changed, err := s.DeriveBatch(ctx, in, store.Idem{Key: "dv1", Fingerprint: "1"})
	if err != nil || !changed {
		t.Fatalf("derive changed=%v err=%v", changed, err)
	}
	values := map[string]store.BatchAttribute{}
	for _, v := range res.Batch.Attributes {
		values[v.Name] = v
	}
	if p := values["potency"]; p.Num == nil || !p.Num.Equal(dec("43")) || !p.Pass {
		t.Fatalf("potency %+v", p)
	}
	if g := values["grade"]; g.Text == nil || *g.Text != "A" {
		t.Fatalf("grade %+v", g)
	}
	if res.Batch.ExpiryDate == nil || *res.Batch.ExpiryDate != "2027-03-01" || res.Batch.Version != 2 || len(res.Derived) != 3 || len(res.Warnings) != 0 {
		t.Fatalf("result %+v", res)
	}

	// Replay: same answer, nothing more written.
	if again, changed, err := s.DeriveBatch(ctx, in, store.Idem{Key: "dv1", Fingerprint: "1"}); err != nil || changed || again.Batch.Version != 2 {
		t.Fatalf("replay changed=%v err=%v", changed, err)
	}
	// A stale version, a self sender and a sender from another legal entity are refused.
	if _, _, err := s.DeriveBatch(ctx, in, store.Idem{Key: "dv2", Fingerprint: "2"}); problemCode(err) != store.CodeVersionConflict {
		t.Fatalf("stale: %v", err)
	}
	self := store.DeriveInput{BatchID: recv.ID, Version: 2, Senders: []store.DeriveSender{{BatchID: recv.ID, Qty: dec("1")}}}
	if _, _, err := s.DeriveBatch(ctx, self, store.Idem{Key: "dv3", Fingerprint: "3"}); problemCode(err) != "COM-0001" {
		t.Fatalf("self sender: %v", err)
	}
	other, _, err := s.RegisterBatch(ctx, store.NewBatch{LegalEntityID: entityB, ItemID: item, BatchNumber: "X-1", Disposition: "available"}, store.Idem{Key: "regx", Fingerprint: "x"})
	if err != nil {
		t.Fatal(err)
	}
	cross := store.DeriveInput{BatchID: recv.ID, Version: 2, Senders: []store.DeriveSender{{BatchID: other.ID, Qty: dec("1")}}}
	if _, _, err := s.DeriveBatch(ctx, cross, store.Idem{Key: "dv4", Fingerprint: "4"}); problemCode(err) != "COM-0001" {
		t.Fatalf("cross legal entity: %v", err)
	}
	if _, _, err := s.DeriveBatch(ctx, store.DeriveInput{BatchID: recv.ID, Version: 2, Senders: []store.DeriveSender{{BatchID: ids.New(), Qty: dec("1")}}}, store.Idem{Key: "dv5", Fingerprint: "5"}); problemCode(err) != store.CodeBatchNotFound {
		t.Fatalf("unknown sender: %v", err)
	}

	// Re-derive from one sender that lacks the grade's agreement partner: a sender
	// without values is reported as missing (PMF-0007) and its attributes left alone.
	bare := register(t, s, item, "S-C")
	re := store.DeriveInput{BatchID: recv.ID, Version: 2, Senders: []store.DeriveSender{{BatchID: a.ID, Qty: dec("10")}, {BatchID: bare.ID, Qty: dec("10")}}}
	res2, changed, err := s.DeriveBatch(ctx, re, store.Idem{Key: "dv6", Fingerprint: "6"})
	if err != nil || !changed || res2.Batch.Version != 3 || len(res2.Warnings) != 3 {
		t.Fatalf("re-derive changed=%v %+v err=%v", changed, res2, err)
	}
	for _, d := range res2.Derived {
		if len(d.Missing) != 1 || d.Missing[0] != bare.ID || !d.Recorded {
			t.Fatalf("derived %+v", d)
		}
	}

	// No sender has a value: nothing is recorded, the version stays, the log says so.
	bare2 := register(t, s, item, "S-D")
	none := store.DeriveInput{BatchID: recv.ID, Version: 3, Senders: []store.DeriveSender{{BatchID: bare.ID, Qty: dec("1")}, {BatchID: bare2.ID, Qty: dec("1")}}}
	res3, changed, err := s.DeriveBatch(ctx, none, store.Idem{Key: "dv7", Fingerprint: "7"})
	if err != nil || !changed || res3.Batch.Version != 3 {
		t.Fatalf("empty derive changed=%v %+v err=%v", changed, res3, err)
	}
	for _, d := range res3.Derived {
		if d.Recorded || d.Value != "" || len(d.Missing) != 2 {
			t.Fatalf("empty derived %+v", d)
		}
	}

	// The monitor: 9 log rows for the receiver, newest first; 6 of them with missing senders.
	log, err := s.ListDerivations(ctx, store.DerivationFilter{BatchID: recv.ID, Limit: 50})
	if err != nil || len(log) != 9 || log[0].BatchNumber != "R-1" {
		t.Fatalf("log %d err=%v", len(log), err)
	}
	withMissing, err := s.ListDerivations(ctx, store.DerivationFilter{ItemID: item, WithMissing: true, Limit: 50})
	if err != nil || len(withMissing) != 6 {
		t.Fatalf("with missing %d err=%v", len(withMissing), err)
	}
	var first *store.Derivation
	for i := range log {
		if log[i].Attribute == "potency" && log[i].ReceiverVersion == 2 {
			first = &log[i]
		}
	}
	var senders []struct{ BatchID, BatchNumber, Qty string }
	if first == nil || first.Value != "43" || first.ValueBefore != "" || !first.Recorded || json.Unmarshal(first.Senders, &senders) != nil || len(senders) != 2 || senders[0].BatchNumber != "S-A" || senders[1].Qty != "300" {
		t.Fatalf("first potency row %+v senders %+v", first, senders)
	}
	// The log keeps the receiver value before each derivation: the re-derive from
	// S-A alone replaced the potency 43 by 40 and the grade stayed A.
	for _, d := range log {
		if !d.Recorded || d.ReceiverVersion != 3 {
			continue
		}
		if d.Attribute == "potency" && (d.ValueBefore != "43" || d.Value != "40") {
			t.Fatalf("re-derived potency row %+v", d)
		}
		if d.Attribute == "expiry" && (d.ValueBefore != "2027-03-01" || d.Value != "2027-06-01") {
			t.Fatalf("re-derived expiry row %+v", d)
		}
	}
	// A user scoped to another legal entity sees neither the log nor the rules.
	if scoped, err := s.ListDerivations(ctxFor(1, entityB), store.DerivationFilter{Limit: 50}); err != nil || len(scoped) != 0 {
		t.Fatalf("legal entity B sees %d err=%v", len(scoped), err)
	}
	if rules, err := s.ListInheritanceRules(ctxFor(1, entityB), store.RuleFilter{Limit: 50}); err != nil || len(rules) != 0 {
		t.Fatalf("legal entity B sees %d rules err=%v", len(rules), err)
	}
	if other, _ := s.ListDerivations(ctxFor(2, ""), store.DerivationFilter{Limit: 50}); len(other) != 0 {
		t.Fatalf("other tenant sees %d", len(other))
	}

	evts := outboxEvents(t, admin, pmfevents.BatchDerived)
	var ev pmfevents.BatchDerivedData
	if len(evts) != 3 || json.Unmarshal(evts[0].Data, &ev) != nil || ev.Version != 2 || len(ev.Senders) != 2 || len(ev.Derived) != 3 {
		t.Fatalf("events %d %+v", len(evts), ev)
	}
}

func TestDeriveBatch_NoRulesAndCopyDisagreement(t *testing.T) {
	s, _ := setup(t)
	ctx := ctxFor(1, "")
	item := ids.New()
	defineItem(t, s, item)
	a := recordFor(t, s, register(t, s, item, "S-A"), "40", "A", "2027-06-01")
	b := recordFor(t, s, register(t, s, item, "S-B"), "45", "B", "2027-03-01")
	recv := register(t, s, item, "R-1")
	in := store.DeriveInput{BatchID: recv.ID, Version: 1, Senders: []store.DeriveSender{{BatchID: a.ID, Qty: dec("1")}, {BatchID: b.ID, Qty: dec("1")}}}
	if _, _, err := s.DeriveBatch(ctx, in, store.Idem{Key: "n1", Fingerprint: "1"}); problemCode(err) != store.CodeNoRules {
		t.Fatalf("no rules: %v", err)
	}
	defineRule(t, s, item, "grade", "copy", nil)
	if _, _, err := s.DeriveBatch(ctx, in, store.Idem{Key: "n2", Fingerprint: "2"}); problemCode(err) != "COM-0001" {
		t.Fatalf("copy disagreement: %v", err)
	}
	// The failed derivation wrote nothing: the receiver is unchanged and the log empty.
	got, err := s.GetBatch(ctx, recv.ID)
	if err != nil || got.Version != 1 || len(got.Attributes) != 0 {
		t.Fatalf("receiver %+v err=%v", got, err)
	}
	if log, _ := s.ListDerivations(ctx, store.DerivationFilter{Limit: 10}); len(log) != 0 {
		t.Fatalf("log %d", len(log))
	}
	// Weighted average without a consumed quantity is refused.
	defineRule(t, s, item, "potency", "weighted-average", nil)
	w := store.DeriveInput{BatchID: recv.ID, Version: 1, Senders: []store.DeriveSender{{BatchID: a.ID, Qty: dec("0")}}}
	if _, _, err := s.DeriveBatch(ctx, store.DeriveInput{BatchID: recv.ID, Version: 1, Senders: w.Senders}, store.Idem{Key: "n3", Fingerprint: "3"}); problemCode(err) != "COM-0001" {
		t.Fatalf("zero qty: %v", err)
	}
}

// A batch cannot be derived from its own descendants (PMF-0130): the senders of
// each batch's latest derivation are its links, followed transitively; a
// re-derivation from other senders replaces the links.
func TestDeriveBatch_RefusesCycles(t *testing.T) {
	s, _ := setup(t)
	ctx := ctxFor(1, "")
	item := ids.New()
	defineItem(t, s, item)
	defineRule(t, s, item, "potency", "max", nil)
	src := recordFor(t, s, register(t, s, item, "SRC"), "40", "A", "2027-06-01")
	mid := register(t, s, item, "MID")
	end := register(t, s, item, "END")
	other := recordFor(t, s, register(t, s, item, "OTH"), "50", "A", "2027-06-01")
	sib := register(t, s, item, "SIB")
	derive := func(key string, recv store.Batch, version int, senders ...store.Batch) error {
		in := store.DeriveInput{BatchID: recv.ID, Version: version}
		for _, b := range senders {
			in.Senders = append(in.Senders, store.DeriveSender{BatchID: b.ID, Qty: dec("1")})
		}
		_, _, err := s.DeriveBatch(ctx, in, store.Idem{Key: key, Fingerprint: key})
		return err
	}
	// SRC -> MID -> END.
	if err := derive("c1", mid, 1, src); err != nil {
		t.Fatal(err)
	}
	if err := derive("c2", end, 1, mid); err != nil {
		t.Fatal(err)
	}
	// Deriving SRC from END (two steps) or from MID (one step) closes a cycle.
	if err := derive("c3", src, src.Version, end); problemCode(err) != store.CodeDeriveCycle {
		t.Fatalf("two-step cycle: %v", err)
	}
	if err := derive("c4", src, src.Version, other, mid); problemCode(err) != store.CodeDeriveCycle {
		t.Fatalf("one-step cycle: %v", err)
	}
	// A sibling of SRC is no cycle.
	if err := derive("c5", sib, 1, src); err != nil {
		t.Fatalf("sibling: %v", err)
	}
	// MID re-derived from OTH alone: SRC is no longer an ancestor of END.
	if err := derive("c6", mid, 2, other); err != nil {
		t.Fatal(err)
	}
	if err := derive("c7", src, src.Version, end); err != nil {
		t.Fatalf("after re-derivation: %v", err)
	}
}
