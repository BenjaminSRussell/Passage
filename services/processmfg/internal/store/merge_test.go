package store_test

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/services/processmfg/internal/domain"
	pmfevents "github.com/BenjaminSRussell/passage/services/processmfg/internal/events"
	"github.com/BenjaminSRussell/passage/services/processmfg/internal/store"
)

// recordFor gives a registered batch a potency, grade and expiry and returns it
// at its new version.
func recordFor(t *testing.T, s *store.Postgres, b store.Batch, potency, grade, expiry string) store.Batch {
	t.Helper()
	exp, err := time.Parse("2006-01-02", expiry)
	if err != nil {
		t.Fatal(err)
	}
	res, _, err := s.RecordAttributes(ctxFor(1, ""), b.ID, map[string]domain.Value{"potency": num(potency), "grade": {Text: ptr(grade)}, "expiry": {Date: &exp}},
		b.Version, store.Idem{Key: "rec-" + b.ID, Fingerprint: b.ID})
	if err != nil {
		t.Fatal(err)
	}
	return res.Batch
}

func src(b store.Batch, qty, cost string) store.MergeSource {
	return store.MergeSource{BatchID: b.ID, Version: b.Version, WarehouseID: "W1", Qty: dec(qty), UnitCost: dec(cost)}
}

// AC-24.12: 200 at 40 and 300 at 45 merge into 500 at 43, the earliest expiry,
// the weighted cost, genealogy to both parents and one event.
func TestMergeBatches_AC2412(t *testing.T) {
	s, admin := setup(t)
	ctx := ctxFor(1, "")
	item := ids.New()
	defineItem(t, s, item)
	a := recordFor(t, s, register(t, s, item, "L-A"), "40", "A", "2027-06-01")
	b := recordFor(t, s, register(t, s, item, "L-B"), "45", "A", "2027-03-01")

	in := store.MergeInput{TargetNumber: "L-AB", Sources: []store.MergeSource{src(a, "200", "1.5"), src(b, "300", "2")}}
	res, created, err := s.MergeBatches(ctx, in, store.Idem{Key: "m1", Fingerprint: "1"})
	if err != nil || !created {
		t.Fatalf("merge created=%v err=%v", created, err)
	}
	if !res.Qty.Equal(dec("500")) || !res.UnitCost.Equal(dec("1.8")) || res.Batch.BatchNumber != "L-AB" || res.Batch.Version != 1 ||
		res.Batch.Disposition != "available" || res.Batch.ExpiryDate == nil || *res.Batch.ExpiryDate != "2027-03-01" || len(res.Parents) != 2 {
		t.Fatalf("result %+v", res)
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

	g, err := s.GetGenealogy(ctx, res.Batch.ID)
	if err != nil || len(g.Parents) != 2 || len(g.Children) != 0 {
		t.Fatalf("target genealogy %+v err=%v", g, err)
	}
	pg, err := s.GetGenealogy(ctx, a.ID)
	if err != nil || len(pg.Children) != 1 || pg.Children[0].BatchNumber != "L-AB" || !pg.Children[0].Qty.Equal(dec("200")) || pg.Children[0].Relation != "merge" {
		t.Fatalf("parent genealogy %+v err=%v", pg, err)
	}

	evts := outboxEvents(t, admin, pmfevents.BatchMerged)
	var ev pmfevents.BatchMergedData
	if len(evts) != 1 || json.Unmarshal(evts[0].Data, &ev) != nil || ev.Qty != "500" || ev.ExpiryDate != "2027-03-01" || len(ev.Parents) != 2 || ev.WarehouseID != "W1" {
		t.Fatalf("events %d %+v", len(evts), ev)
	}

	// A retry replays the first answer; nothing more is written.
	again, created, err := s.MergeBatches(ctx, in, store.Idem{Key: "m1", Fingerprint: "1"})
	if err != nil || created || again.Batch.ID != res.Batch.ID || len(outboxEvents(t, admin, pmfevents.BatchMerged)) != 1 {
		t.Fatalf("replay created=%v err=%v", created, err)
	}
	if _, _, err := s.MergeBatches(ctx, in, store.Idem{Key: "m1", Fingerprint: "other"}); problemCode(err) != store.CodeIdempotencyReuse {
		t.Fatalf("same key other body: %v", err)
	}

	// The merge moved each source to a new version: the same merge under a new
	// key (a second user working from the same read) is stale and writes nothing.
	in2 := in
	in2.TargetNumber = "L-AB2"
	if _, _, err := s.MergeBatches(ctx, in2, store.Idem{Key: "m2", Fingerprint: "2"}); problemCode(err) != store.CodeVersionConflict {
		t.Fatalf("second merge from the old versions: %v", err)
	}
	if _, _, err := s.ChangeDisposition(ctx, a.ID, "on-hold", "retest", a.Version, store.Idem{Key: "m-hold", Fingerprint: "h"}); problemCode(err) != store.CodeVersionConflict {
		t.Fatalf("disposition from the pre-merge version: %v", err)
	}
	// Re-read, the rest of a source can be merged again (a partial merge).
	fa, err := s.GetBatch(ctx, a.ID)
	if err != nil || fa.Version != a.Version+1 || fa.Disposition != "available" {
		t.Fatalf("source after merge %+v err=%v", fa, err)
	}
	fb, err := s.GetBatch(ctx, b.ID)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := s.MergeBatches(ctx, store.MergeInput{TargetNumber: "L-AB2", Sources: []store.MergeSource{src(fa, "5", "1.5"), src(fb, "5", "2")}},
		store.Idem{Key: "m3", Fingerprint: "3"}); err != nil {
		t.Fatalf("merge from the new versions: %v", err)
	}
	if pg, err := s.GetGenealogy(ctx, a.ID); err != nil || len(pg.Children) != 2 {
		t.Fatalf("parent genealogy after two merges %+v err=%v", pg, err)
	}
	if n := len(outboxEvents(t, admin, pmfevents.BatchMerged)); n != 2 {
		t.Fatalf("%d merged events, want 2", n)
	}
}

// The rule refusals and the guards write nothing: a hold on a source cannot be
// bypassed, a stale version is refused, the target number is unique.
func TestMergeBatches_RefusalsWriteNothing(t *testing.T) {
	s, admin := setup(t)
	ctx := ctxFor(1, "")
	item := ids.New()
	defineItem(t, s, item)
	a := recordFor(t, s, register(t, s, item, "R-A"), "40", "A", "2027-06-01")
	b := recordFor(t, s, register(t, s, item, "R-B"), "45", "B", "2027-03-01")
	otherItem := ids.New()
	defineItem(t, s, otherItem)
	other := recordFor(t, s, register(t, s, otherItem, "R-X"), "45", "A", "2027-03-01")

	// Different grade (text) is PMF-0006 unless overridden.
	if _, _, err := s.MergeBatches(ctx, store.MergeInput{TargetNumber: "T1", Sources: []store.MergeSource{src(a, "1", "1"), src(b, "1", "1")}},
		store.Idem{Key: "r1", Fingerprint: "1"}); problemCode(err) != domain.CodeMergeRefused {
		t.Fatalf("grade differs: %v", err)
	}
	// Another item.
	if _, _, err := s.MergeBatches(ctx, store.MergeInput{TargetNumber: "T2", Sources: []store.MergeSource{src(a, "1", "1"), src(other, "1", "1")}},
		store.Idem{Key: "r2", Fingerprint: "2"}); problemCode(err) != domain.CodeMergeRefused {
		t.Fatalf("other item: %v", err)
	}
	// A hold on one source is read from the register, so the dispositions differ.
	held, _, err := s.ChangeDisposition(ctx, b.ID, "on-hold", "retest", b.Version, store.Idem{Key: "hold", Fingerprint: "h"})
	if err != nil {
		t.Fatal(err)
	}
	over := map[string]bool{"grade": true}
	if _, _, err := s.MergeBatches(ctx, store.MergeInput{TargetNumber: "T3", Override: over, Sources: []store.MergeSource{src(a, "1", "1"), src(held, "1", "1")}},
		store.Idem{Key: "r3", Fingerprint: "3"}); problemCode(err) != domain.CodeMergeRefused {
		t.Fatalf("disposition differs: %v", err)
	}
	// Stale version (b moved to version 2 by the hold), unknown batch.
	if _, _, err := s.MergeBatches(ctx, store.MergeInput{TargetNumber: "T4", Override: over, Sources: []store.MergeSource{src(a, "1", "1"), src(b, "1", "1")}},
		store.Idem{Key: "r4", Fingerprint: "4"}); problemCode(err) != store.CodeVersionConflict {
		t.Fatalf("stale: %v", err)
	}
	ghost := store.MergeSource{BatchID: ids.New(), Version: 1, WarehouseID: "W1", Qty: dec("1")}
	if _, _, err := s.MergeBatches(ctx, store.MergeInput{TargetNumber: "T5", Sources: []store.MergeSource{src(a, "1", "1"), ghost}},
		store.Idem{Key: "r5", Fingerprint: "5"}); problemCode(err) != store.CodeBatchNotFound {
		t.Fatalf("unknown: %v", err)
	}
	// Released again, the override lets the grades differ; the target number must be new.
	rel, _, err := s.ChangeDisposition(ctx, b.ID, "available", "retest passed", held.Version, store.Idem{Key: "rel", Fingerprint: "r"})
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := s.MergeBatches(ctx, store.MergeInput{TargetNumber: "R-A", Override: over, Sources: []store.MergeSource{src(a, "1", "1"), src(rel, "1", "1")}},
		store.Idem{Key: "r6", Fingerprint: "6"}); problemCode(err) != store.CodeBatchDuplicate {
		t.Fatalf("duplicate target: %v", err)
	}
	ok, _, err := s.MergeBatches(ctx, store.MergeInput{TargetNumber: "T6", Override: over, Sources: []store.MergeSource{src(a, "10", "1"), src(rel, "30", "1")}},
		store.Idem{Key: "r7", Fingerprint: "7"})
	if err != nil {
		t.Fatalf("override merge: %v", err)
	}
	for _, v := range ok.Batch.Attributes {
		if v.Name == "grade" && (v.Text == nil || *v.Text != "B") {
			t.Fatalf("largest batch's grade should win: %+v", v)
		}
	}
	if n := len(outboxEvents(t, admin, pmfevents.BatchMerged)); n != 1 {
		t.Fatalf("%d merged events, want 1 (refusals write none)", n)
	}
}

// Genealogy and merge respect the tenant and the legal-entity scope.
func TestMergeBatches_ScopeIsolation(t *testing.T) {
	s, _ := setup(t)
	item := ids.New()
	defineItem(t, s, item)
	a := recordFor(t, s, register(t, s, item, "S-A"), "40", "A", "2027-06-01")
	b := recordFor(t, s, register(t, s, item, "S-B"), "45", "A", "2027-03-01")
	res, _, err := s.MergeBatches(ctxFor(1, ""), store.MergeInput{TargetNumber: "S-AB", Sources: []store.MergeSource{src(a, "1", "1"), src(b, "1", "1")}},
		store.Idem{Key: "s1", Fingerprint: "1"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.GetGenealogy(ctxFor(2, ""), res.Batch.ID); problemCode(err) != store.CodeBatchNotFound {
		t.Fatalf("tenant 2: %v", err)
	}
	if _, err := s.GetGenealogy(ctxFor(1, entityB), res.Batch.ID); problemCode(err) != store.CodeBatchNotFound {
		t.Fatalf("entity B: %v", err)
	}
	a, b = mustGet(t, s, a.ID), mustGet(t, s, b.ID)
	if _, _, err := s.MergeBatches(ctxFor(1, entityB), store.MergeInput{TargetNumber: "S-X", Sources: []store.MergeSource{src(a, "1", "1"), src(b, "1", "1")}},
		store.Idem{Key: "s2", Fingerprint: "2"}); problemCode(err) != store.CodeBatchNotFound {
		t.Fatalf("merge from entity B scope: %v", err)
	}

	// A caller who sees both legal entities still cannot merge across them.
	cross, _, err := s.RegisterBatch(ctxFor(1, ""), store.NewBatch{LegalEntityID: entityB, ItemID: item, BatchNumber: "S-C", Disposition: "available"},
		store.Idem{Key: "reg-S-C" + item, Fingerprint: "S-C"})
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := s.MergeBatches(ctxFor(1, ""), store.MergeInput{TargetNumber: "S-AC", Sources: []store.MergeSource{src(a, "1", "1"), src(cross, "1", "1")}},
		store.Idem{Key: "s3", Fingerprint: "3"}); problemCode(err) != domain.CodeMergeRefused {
		t.Fatalf("merge across legal entities: %v", err)
	}
	if got := mustGet(t, s, a.ID); got.Version != a.Version {
		t.Fatalf("refused merge moved the source version: %d -> %d", a.Version, got.Version)
	}
}

func mustGet(t *testing.T, s *store.Postgres, id string) store.Batch {
	t.Helper()
	b, err := s.GetBatch(ctxFor(1, ""), id)
	if err != nil {
		t.Fatal(err)
	}
	return b
}
