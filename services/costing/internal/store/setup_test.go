package store

import (
	"context"
	"testing"

	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/platform/tenant"
	"github.com/BenjaminSRussell/passage/platform/testkit"
)

// narrowedCtx is a principal limited to one legal entity, so the restrictive
// legal-entity policy applies.
func narrowedCtx(tenantID, legalEntityID string) context.Context {
	return tenant.With(context.Background(), tenant.Principal{TenantID: tenantID, LegalEntityID: legalEntityID, Duties: testkit.ScopeDuties(legalEntityID)})
}

func TestPostgres_ValuationLedgers(t *testing.T) {
	s, _ := setup(t)
	ctx := scopedCtx(testkit.TenantID(1))
	le, ifrs, gaap := ids.New(), ids.New(), ids.New()
	ledger := func(group, code string, primary bool) ValuationLedger {
		return ValuationLedger{LegalEntityID: le, LedgerGroupID: group, Code: code, Method: "FIFO", Currency: "EUR", RateType: "AVERAGE", Primary: primary}
	}
	primary, err := s.CreateValuationLedger(ctx, ledger(ifrs, "IFRS", true))
	if err != nil || primary.ID == "" || !primary.Primary || primary.Currency != "EUR" {
		t.Fatalf("primary = %+v, %v", primary, err)
	}
	if _, err := s.CreateValuationLedger(ctx, ledger(gaap, "USGAAP", false)); err != nil {
		t.Fatal(err)
	}
	refusals := []struct {
		name string
		in   ValuationLedger
		want string
	}{
		{"second primary", ledger(ids.New(), "TAX", true), CodePrimaryLedgerTaken},
		{"ledger group already bound", ledger(gaap, "TAX", false), CodeLedgerGroupTaken},
		{"code reused", ledger(ids.New(), "IFRS", false), CodeLedgerDuplicate},
	}
	for _, tc := range refusals {
		if _, err := s.CreateValuationLedger(ctx, tc.in); problemCode(err) != tc.want {
			t.Errorf("%s: %v, want %s", tc.name, err, tc.want)
		}
	}
	list, err := s.ListValuationLedgers(ctx, le)
	if err != nil || len(list) != 2 || list[0].Code != "IFRS" {
		t.Fatalf("list = %+v, %v (want IFRS, primary, first)", list, err)
	}
	if got, err := s.GetValuationLedger(ctx, primary.ID); err != nil || got.Code != "IFRS" {
		t.Errorf("get = %+v, %v", got, err)
	}
	// Row-level security: another tenant and another legal entity see nothing.
	if _, err := s.GetValuationLedger(scopedCtx(testkit.TenantID(2)), primary.ID); problemCode(err) != CodeLedgerNotFound {
		t.Errorf("other tenant get: %v", err)
	}
	if got, _ := s.ListValuationLedgers(narrowedCtx(testkit.TenantID(1), ids.New()), ""); len(got) != 0 {
		t.Errorf("other legal entity sees %d ledgers", len(got))
	}
	if got, _ := s.ListValuationLedgers(narrowedCtx(testkit.TenantID(1), le), ""); len(got) != 2 {
		t.Errorf("own legal entity sees %d ledgers, want 2", len(got))
	}
}

func TestPostgres_ValuationCategories(t *testing.T) {
	s, _ := setup(t)
	ctx := scopedCtx(testkit.TenantID(1))
	origin, err := s.CreateValuationCategory(ctx, ValuationCategory{Code: "ORIGIN", Types: []ValuationType{{Code: "DOMESTIC", Account: "1300"}, {Code: "IMPORT", Account: "1301"}}})
	if err != nil || len(origin.Types) != 2 || origin.Types[1].Account != "1301" {
		t.Fatalf("origin = %+v, %v", origin, err)
	}
	batch, err := s.CreateValuationCategory(ctx, ValuationCategory{Code: "BATCH", Batch: true})
	if err != nil || !batch.Batch || batch.Types == nil || len(batch.Types) != 0 {
		t.Fatalf("batch = %+v, %v", batch, err)
	}
	if _, err := s.CreateValuationCategory(ctx, ValuationCategory{Code: "ORIGIN", Types: []ValuationType{{Code: "X1", Account: "1"}}}); problemCode(err) != CodeCategoryDuplicate {
		t.Errorf("duplicate: %v", err)
	}
	// The table refuses a batch category with types even past the API.
	if _, err := s.CreateValuationCategory(ctx, ValuationCategory{Code: "LOT", Batch: true, Types: []ValuationType{{Code: "L1", Account: "1"}}}); err == nil {
		t.Error("batch category with types was stored")
	}
	list, err := s.ListValuationCategories(ctx)
	if err != nil || len(list) != 2 || list[0].Code != "BATCH" {
		t.Errorf("list = %+v, %v", list, err)
	}
	if other, _ := s.ListValuationCategories(scopedCtx(testkit.TenantID(2))); len(other) != 0 {
		t.Errorf("other tenant sees %d categories", len(other))
	}
}

func TestPostgres_BucketSets(t *testing.T) {
	s, _ := setup(t)
	ctx := scopedCtx(testkit.TenantID(1))
	le := ids.New()
	set, err := s.CreateBucketSet(ctx, BucketSet{LegalEntityID: le, Name: "Monthly", Bounds: []int{30, 60, 90}})
	if err != nil || len(set.Bounds) != 3 || set.Bounds[2] != 90 {
		t.Fatalf("create = %+v, %v", set, err)
	}
	if _, err := s.CreateBucketSet(ctx, BucketSet{LegalEntityID: le, Name: "Monthly", Bounds: []int{10}}); problemCode(err) != CodeBucketSetDuplicate {
		t.Errorf("duplicate: %v", err)
	}
	if _, err := s.CreateBucketSet(ctx, BucketSet{LegalEntityID: ids.New(), Name: "Monthly", Bounds: []int{10}}); err != nil {
		t.Errorf("same name in another legal entity: %v", err)
	}
	if got, err := s.GetBucketSet(ctx, set.ID); err != nil || got.Name != "Monthly" {
		t.Errorf("get = %+v, %v", got, err)
	}
	if list, _ := s.ListBucketSets(ctx, le); len(list) != 1 {
		t.Errorf("list = %d, want 1", len(list))
	}
	if _, err := s.GetBucketSet(scopedCtx(testkit.TenantID(2)), set.ID); problemCode(err) != CodeBucketSetNotFound {
		t.Errorf("other tenant get: %v", err)
	}
}

func snapshotFixture(le, asOf, total string) AgingSnapshot {
	return AgingSnapshot{LegalEntityID: le, AsOf: asOf, Method: "FIFO", Currency: "EUR",
		Buckets:  []SnapshotBucket{{Label: "0-30", Qty: "100", Value: total}, {Label: "> 30", Qty: "0", Value: "0.00"}},
		TotalQty: "100", TotalValue: total, RemainingValue: total, TiesToRemaining: true,
		Lines: []SnapshotLineOut{{ProductID: ids.New(), SiteID: ids.New(), TotalQty: "100", TotalValue: total, RemainingValue: total,
			TiesToRemaining: true, Buckets: []SnapshotBucket{{Label: "0-30", Qty: "100", Value: total}}}}}
}

// A stored snapshot writes one agingsnapshot.created event in its transaction;
// a replay returns it and writes nothing; the key with other content is refused.
func TestPostgres_AgingSnapshotIdempotentWithEvent(t *testing.T) {
	s, app := setup(t)
	ctx := scopedCtx(testkit.TenantID(1))
	le := ids.New()
	in := snapshotFixture(le, "2026-01-31", "500.00")
	first, created, err := s.CreateAgingSnapshot(ctx, in, "snap-jan")
	if err != nil || !created || first.ID == "" || first.LineCount != 1 || len(first.Lines) != 1 {
		t.Fatalf("create = %+v created=%v, %v", first, created, err)
	}
	replay, created, err := s.CreateAgingSnapshot(ctx, in, "snap-jan")
	if err != nil || created || replay.ID != first.ID {
		t.Fatalf("replay = %s created=%v, %v; want %s", replay.ID, created, err, first.ID)
	}
	if _, _, err := s.CreateAgingSnapshot(ctx, snapshotFixture(le, "2026-01-31", "501.00"), "snap-jan"); problemCode(err) != CodeIdempotencyReuse {
		t.Errorf("key reuse: %v, want %s", err, CodeIdempotencyReuse)
	}
	rows := outboxRows(t, ctx, app)
	if len(rows) != 1 {
		t.Fatalf("outbox rows = %d, want 1", len(rows))
	}
	ev := rows[0]
	if ev.Type != AgingSnapshotEventType || ev.AggID != le || ev.Payload["snapshotId"] != first.ID || ev.Payload["totalValue"] != "500.00" ||
		ev.Payload["asOf"] != "2026-01-31" || len(ev.Payload["buckets"].([]any)) != 2 {
		t.Errorf("event = %+v", ev)
	}
	got, err := s.GetAgingSnapshot(ctx, first.ID)
	if err != nil || len(got.Lines) != 1 || got.TotalValue != "500.00" {
		t.Errorf("get = %+v, %v", got, err)
	}
	if _, err := s.GetAgingSnapshot(narrowedCtx(testkit.TenantID(1), ids.New()), first.ID); problemCode(err) != CodeSnapshotNotFound {
		t.Errorf("other legal entity get: %v", err)
	}
}

func TestPostgres_AgingSnapshotListPages(t *testing.T) {
	s, _ := setup(t)
	ctx := scopedCtx(testkit.TenantID(1))
	le := ids.New()
	for i, asOf := range []string{"2026-01-31", "2026-03-31", "2026-02-28"} {
		if _, _, err := s.CreateAgingSnapshot(ctx, snapshotFixture(le, asOf, "1.00"), "k"+asOf); err != nil {
			t.Fatalf("snapshot %d: %v", i, err)
		}
	}
	if _, _, err := s.CreateAgingSnapshot(ctx, snapshotFixture(ids.New(), "2026-04-30", "1.00"), "other"); err != nil {
		t.Fatal(err)
	}
	var seen []string
	after := ""
	for range 4 {
		page, err := s.ListAgingSnapshots(ctx, le, after, 2)
		if err != nil {
			t.Fatal(err)
		}
		for _, snap := range page.Items {
			if snap.Lines != nil || snap.LineCount != 1 {
				t.Errorf("summary carries lines %v or lineCount %d", snap.Lines, snap.LineCount)
			}
			seen = append(seen, snap.AsOf)
		}
		if after = page.Next; after == "" {
			break
		}
	}
	if len(seen) != 3 || seen[0] != "2026-03-31" || seen[1] != "2026-02-28" || seen[2] != "2026-01-31" {
		t.Errorf("as-of order = %v, want newest first across pages", seen)
	}
	if all, _ := s.ListAgingSnapshots(ctx, "", "", 50); len(all.Items) != 4 {
		t.Errorf("unfiltered list = %d, want 4", len(all.Items))
	}
}
