package store

import (
	"context"
	"encoding/json"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/platform/dbx"
	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/platform/testkit"
	"github.com/BenjaminSRussell/passage/services/costing/internal/domain"
)

func d(s string) decimal.Decimal { return decimal.RequireFromString(s) }

func qty(s string) OnHandQty { return OnHandQty{Qty: d(s)} }

func valued(q, book string) OnHandQty {
	b := d(book)
	return OnHandQty{Qty: d(q), BookValue: &b}
}

func domainCode(err error) string {
	if de, ok := err.(*domain.Error); ok {
		return de.Code
	}
	return problemCode(err)
}

func mustVersion(t *testing.T, s *Postgres, ctx context.Context, in CostingVersion) CostingVersion {
	t.Helper()
	v, _, err := s.CreateCostingVersion(ctx, in, ids.New())
	if err != nil {
		t.Fatal(err)
	}
	return v
}

type outboxRow struct {
	Type    string
	AggID   string
	Payload map[string]any
}

func outboxRows(t *testing.T, ctx context.Context, app *pgxpool.Pool) []outboxRow {
	t.Helper()
	var out []outboxRow
	err := dbx.WithTenantTx(ctx, app, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `select type, aggregate_id, payload from outbox order by aggregate_id`)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var r outboxRow
			var raw []byte
			if err := rows.Scan(&r.Type, &r.AggID, &raw); err != nil {
				return err
			}
			var env struct {
				Data map[string]any `json:"data"`
			}
			if err := json.Unmarshal(raw, &env); err != nil {
				return err
			}
			r.Payload = env.Data
			out = append(out, r)
		}
		return rows.Err()
	})
	if err != nil {
		t.Fatal(err)
	}
	return out
}

func statusOf(t *testing.T, ctx context.Context, app *pgxpool.Pool, versionID, productID string) string {
	t.Helper()
	var status string
	err := dbx.WithTenantTx(ctx, app, func(tx pgx.Tx) error {
		return tx.QueryRow(ctx, `select status from item_cost where version_id = $1 and product_id = $2`, versionID, productID).Scan(&status)
	})
	if err != nil {
		t.Fatal(err)
	}
	return status
}

// TestPostgres_ActivationLifecycle covers FR-20.5 and BR-20.7 on the database:
// the preview writes nothing; activation needs the current optimistic version,
// makes the pending costs ACTIVE with their revaluation recorded, writes one
// cost.changed per product x site to the outbox in the same transaction and is
// idempotent; a second version copied +10 % supersedes the first; the as-of
// lookup reads the cost in force on each date.
func TestPostgres_ActivationLifecycle(t *testing.T) {
	s, app := setup(t)
	ctx := scopedCtx(testkit.TenantID(11))
	le, p1, p2, site := ids.New(), ids.New(), ids.New(), ids.New()
	today := time.Now().UTC().Format(time.DateOnly)

	v1 := mustVersion(t, s, ctx, CostingVersion{LegalEntityID: le, Code: "STD-A", Type: "STANDARD", Currency: "EUR", EffectiveFrom: "2026-01-01"})
	if _, err := s.ImportItemCosts(ctx, v1.ID, []ItemCost{{ProductID: p1, SiteID: site, UnitCost: "10"}, {ProductID: p2, SiteID: site, UnitCost: "5"}}); err != nil {
		t.Fatal(err)
	}
	// p1 has 100 on hand carried at 950.00 and no standard yet: 100 x 10 − 950 = 50.00.
	// p2 has no stock, so it needs no book value.
	if _, err := s.PreviewActivation(ctx, v1.ID, OnHand{OnHandKey(p1, site): qty("100")}); domainCode(err) != domain.CodeBookValueMissing {
		t.Fatalf("preview without book value: %v, want %s", err, domain.CodeBookValueMissing)
	}
	onHand := OnHand{OnHandKey(p1, site): valued("100", "950")}
	prev, err := s.PreviewActivation(ctx, v1.ID, onHand)
	if err != nil {
		t.Fatal(err)
	}
	if !prev.Plan.Total.Equal(d("50")) || prev.AlreadyActive || prev.Version.Version != 2 {
		t.Fatalf("preview total %s already=%v version %d, want 50, false, 2", prev.Plan.Total, prev.AlreadyActive, prev.Version.Version)
	}
	if _, err := s.CurrentCost(ctx, p1, site, today); problemCode(err) != CodeCostNotFound {
		t.Fatalf("preview must not activate: lookup err %v", err)
	}
	if got := outboxRows(t, ctx, app); len(got) != 0 {
		t.Fatalf("preview wrote %d outbox rows", len(got))
	}

	if _, err := s.ActivateVersion(ctx, v1.ID, 1, onHand); problemCode(err) != CodeConflictVersion {
		t.Fatalf("stale If-Match: %v, want %s", err, CodeConflictVersion)
	}
	act, err := s.ActivateVersion(ctx, v1.ID, prev.Version.Version, onHand)
	if err != nil {
		t.Fatal(err)
	}
	if act.AlreadyActive || !act.Plan.Total.Equal(d("50")) || act.Version.Status != "ACTIVE" || act.Version.Version != 3 || act.Version.ActivatedAt == nil {
		t.Fatalf("activation result %+v", act)
	}
	events := outboxRows(t, ctx, app)
	if len(events) != 2 {
		t.Fatalf("outbox rows = %d, want one cost.changed per product x site", len(events))
	}
	for _, e := range events {
		if e.Type != "dev.passage.costing.cost.changed.v1" || e.Payload["versionId"] != v1.ID || e.Payload["currency"] != "EUR" {
			t.Errorf("event %+v", e)
		}
		if _, has := e.Payload["oldCost"]; has {
			t.Errorf("first standard must carry no oldCost: %v", e.Payload)
		}
		if e.Payload["productId"] == p1 && (e.Payload["newCost"] != "10" || e.Payload["revaluationAmount"] != "50.00" || e.Payload["onHandQty"] != "100") {
			t.Errorf("p1 event payload %v", e.Payload)
		}
	}

	again, err := s.ActivateVersion(ctx, v1.ID, 1, onHand)
	if err != nil || !again.AlreadyActive || !again.Plan.Total.Equal(d("50")) || len(again.Plan.Lines) != 2 {
		t.Fatalf("replay = %+v, %v; want ALREADY_ACTIVE with the recorded 50.00", again, err)
	}
	if got := outboxRows(t, ctx, app); len(got) != 2 {
		t.Fatalf("replay wrote events: %d rows", len(got))
	}
	if _, err := s.ImportItemCosts(ctx, v1.ID, []ItemCost{{ProductID: p1, SiteID: site, UnitCost: "1"}}); problemCode(err) != CodeVersionNotDraft {
		t.Errorf("import into ACTIVE version: %v, want %s", err, CodeVersionNotDraft)
	}

	// Version 2 copies version 1 up 10 % and is activated: (11 − 10) × 100 + (5.5 − 5) × 4 = 102.00.
	v2 := mustVersion(t, s, ctx, CostingVersion{LegalEntityID: le, Code: "STD-B", Type: "STANDARD", Currency: "EUR", EffectiveFrom: today})
	if n, err := s.CopyItemCosts(ctx, v2.ID, v1.ID, d("10")); err != nil || n != 2 {
		t.Fatalf("copy n=%d err=%v", n, err)
	}
	page, err := s.ListItemCosts(ctx, v2.ID, "", 10)
	if err != nil || len(page.Items) != 2 {
		t.Fatalf("list item costs %+v, %v", page, err)
	}
	for _, r := range page.Items {
		if r.Source != "COPY" || r.Status != "PENDING" || r.ActiveCost == nil {
			t.Errorf("copied row %+v, want COPY, PENDING and the active cost to compare", r)
		}
		if r.ProductID == p1 && (r.UnitCost != "11" || *r.ActiveCost != "10") {
			t.Errorf("p1 row %+v, want 11 against active 10", r)
		}
	}
	v2Now, err := s.GetVersion(ctx, v2.ID)
	if err != nil || v2Now.ItemCount != 2 || v2Now.Version != 2 {
		t.Fatalf("v2 after copy = %+v, %v; want 2 items at version 2", v2Now, err)
	}
	second, err := s.ActivateVersion(ctx, v2.ID, v2Now.Version, OnHand{OnHandKey(p1, site): qty("100"), OnHandKey(p2, site): qty("4")})
	if err != nil || !second.Plan.Total.Equal(d("102")) {
		t.Fatalf("second activation total %s, %v; want 102", second.Plan.Total, err)
	}
	if got := statusOf(t, ctx, app, v1.ID, p1); got != "SUPERSEDED" {
		t.Errorf("v1 p1 status %s, want SUPERSEDED", got)
	}
	cur, err := s.CurrentCost(ctx, p1, site, today)
	if err != nil || cur.VersionID != v2.ID || cur.UnitCost != "11" || cur.VersionCode != "STD-B" || cur.Currency != "EUR" {
		t.Fatalf("current cost %+v, %v; want STD-B at 11", cur, err)
	}
	ev := outboxRows(t, ctx, app)
	var p1Change map[string]any
	for _, e := range ev {
		if e.Payload["productId"] == p1 && e.Payload["versionId"] == v2.ID {
			p1Change = e.Payload
		}
	}
	if p1Change == nil || p1Change["oldCost"] != "10" || p1Change["newCost"] != "11" || p1Change["revaluationAmount"] != "100.00" {
		t.Errorf("v2 p1 cost.changed = %v", p1Change)
	}

	// History: move v1's activation into February; the lookup for 1 March reads the superseded v1 cost.
	err = dbx.WithTenantTx(ctx, app, func(tx pgx.Tx) error {
		_, err := tx.Exec(ctx, `update item_cost set activated_at = '2026-02-01T10:00:00Z' where version_id = $1`, v1.ID)
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	old, err := s.CurrentCost(ctx, p1, site, "2026-03-01")
	if err != nil || old.VersionID != v1.ID || old.UnitCost != "10" {
		t.Errorf("as of 2026-03-01 = %+v, %v; want v1 at 10", old, err)
	}
	if _, err := s.CurrentCost(ctx, p1, site, "2026-01-31"); problemCode(err) != CodeCostNotFound {
		t.Errorf("before any activation: %v, want %s", err, CodeCostNotFound)
	}
}

func TestPostgres_ActivationRefusals(t *testing.T) {
	s, _ := setup(t)
	ctx := scopedCtx(testkit.TenantID(12))
	le, p, site := ids.New(), ids.New(), ids.New()
	cases := []struct {
		name string
		in   CostingVersion
		want string
	}{
		{"planned", CostingVersion{Type: "PLANNED", EffectiveFrom: "2026-01-01"}, domain.CodeNotStandard},
		{"future", CostingVersion{Type: "STANDARD", EffectiveFrom: "2099-01-01"}, domain.CodeFutureEffective},
		{"blocked", CostingVersion{Type: "STANDARD", EffectiveFrom: "2026-01-01", BlockActivation: true}, domain.CodeActivationBlocked},
		{"empty", CostingVersion{Type: "STANDARD", EffectiveFrom: "2026-01-01"}, domain.CodeNoPending},
	}
	for i, tc := range cases {
		tc.in.LegalEntityID, tc.in.Currency, tc.in.Code = le, "EUR", "REF-"+string(rune('A'+i))
		v := mustVersion(t, s, ctx, tc.in)
		if tc.name != "empty" {
			if _, err := s.ImportItemCosts(ctx, v.ID, []ItemCost{{ProductID: p, SiteID: site, UnitCost: "1"}}); err != nil {
				t.Fatal(err)
			}
			v.Version++
		}
		if _, err := s.ActivateVersion(ctx, v.ID, v.Version, OnHand{}); domainCode(err) != tc.want {
			t.Errorf("%s: %v, want %s", tc.name, err, tc.want)
		}
	}
	if _, err := s.ActivateVersion(ctx, ids.New(), 1, OnHand{}); problemCode(err) != CodeVersionNotFound {
		t.Errorf("unknown version: %v", err)
	}
}

func TestPostgres_CopyRefusals(t *testing.T) {
	s, _ := setup(t)
	ctx := scopedCtx(testkit.TenantID(13))
	le := ids.New()
	src := mustVersion(t, s, ctx, CostingVersion{LegalEntityID: le, Code: "SRC", Type: "STANDARD", Currency: "EUR", EffectiveFrom: "2026-01-01"})
	dst := mustVersion(t, s, ctx, CostingVersion{LegalEntityID: le, Code: "DST", Type: "STANDARD", Currency: "EUR", EffectiveFrom: "2026-01-01"})
	usd := mustVersion(t, s, ctx, CostingVersion{LegalEntityID: le, Code: "USD", Type: "STANDARD", Currency: "USD", EffectiveFrom: "2026-01-01"})
	other := mustVersion(t, s, ctx, CostingVersion{LegalEntityID: ids.New(), Code: "OTH", Type: "STANDARD", Currency: "EUR", EffectiveFrom: "2026-01-01"})
	if _, err := s.CopyItemCosts(ctx, dst.ID, src.ID, d("5")); problemCode(err) != CodeCopySourceEmpty {
		t.Errorf("empty source: %v", err)
	}
	if _, err := s.ImportItemCosts(ctx, src.ID, []ItemCost{{ProductID: ids.New(), SiteID: ids.New(), UnitCost: "99999999999999"}}); err != nil {
		t.Fatal(err)
	}
	for name, tc := range map[string]struct {
		target, source string
		want           string
	}{
		"same version":     {dst.ID, dst.ID, CodeCopySameVersion},
		"other currency":   {usd.ID, src.ID, CodeCopyIncompatible},
		"other entity":     {other.ID, src.ID, CodeCopyIncompatible},
		"unknown source":   {dst.ID, ids.New(), CodeVersionNotFound},
		"unknown target":   {ids.New(), src.ID, CodeVersionNotFound},
		"overflowing cost": {dst.ID, src.ID, domain.CodeBadPercent},
	} {
		if _, err := s.CopyItemCosts(ctx, tc.target, tc.source, d("5")); domainCode(err) != tc.want {
			t.Errorf("%s: %v, want %s", name, err, tc.want)
		}
	}
}

// TestPostgres_ConcurrentActivationsChain: two versions activated at once for
// the same product x site are serialised, so the second revalues from the
// first's cost and exactly one cost stays ACTIVE.
func TestPostgres_ConcurrentActivationsChain(t *testing.T) {
	s, app := setup(t)
	ctx := scopedCtx(testkit.TenantID(14))
	le, p, site := ids.New(), ids.New(), ids.New()
	base := mustVersion(t, s, ctx, CostingVersion{LegalEntityID: le, Code: "BASE", Type: "STANDARD", Currency: "EUR", EffectiveFrom: "2026-01-01"})
	if _, err := s.ImportItemCosts(ctx, base.ID, []ItemCost{{ProductID: p, SiteID: site, UnitCost: "10"}}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.ActivateVersion(ctx, base.ID, 2, OnHand{}); err != nil {
		t.Fatal(err)
	}
	var vs []CostingVersion
	for i, c := range []string{"12", "15"} {
		v := mustVersion(t, s, ctx, CostingVersion{LegalEntityID: le, Code: "NEXT-" + string(rune('A'+i)), Type: "STANDARD", Currency: "EUR", EffectiveFrom: "2026-01-01"})
		if _, err := s.ImportItemCosts(ctx, v.ID, []ItemCost{{ProductID: p, SiteID: site, UnitCost: c}}); err != nil {
			t.Fatal(err)
		}
		vs = append(vs, v)
	}
	onHand := OnHand{OnHandKey(p, site): qty("10")}
	results := make([]ActivationResult, 2)
	errs := make([]error, 2)
	var wg sync.WaitGroup
	for i := range vs {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			results[i], errs[i] = s.ActivateVersion(ctx, vs[i].ID, 2, onHand)
		}(i)
	}
	wg.Wait()
	for i, err := range errs {
		if err != nil {
			t.Fatalf("activation %d: %v", i, err)
		}
	}
	var active int
	var final string
	err := dbx.WithTenantTx(ctx, app, func(tx pgx.Tx) error {
		return tx.QueryRow(ctx, `select count(*), max(unit_cost)::text from item_cost where product_id = $1 and status = 'ACTIVE'`, p).Scan(&active, &final)
	})
	if err != nil {
		t.Fatal(err)
	}
	// Whatever the order, the revaluations chain from 10 to the final cost.
	sum := results[0].Plan.Total.Add(results[1].Plan.Total)
	want := d(final).Sub(d("10")).Mul(d("10"))
	if active != 1 || !sum.Equal(want) {
		t.Errorf("active rows %d, revaluation sum %s, want 1 and %s (final cost %s)", active, sum, want, final)
	}
}

func TestPostgres_ListVersionsPagesAndFilters(t *testing.T) {
	s, _ := setup(t)
	ctx := scopedCtx(testkit.TenantID(15))
	le := ids.New()
	for _, code := range []string{"STD-C", "STD-A", "PLAN-B"} {
		typ := "STANDARD"
		if code == "PLAN-B" {
			typ = "PLANNED"
		}
		mustVersion(t, s, ctx, CostingVersion{LegalEntityID: le, Code: code, Type: typ, Currency: "EUR", EffectiveFrom: "2026-01-01"})
	}
	mustVersion(t, s, ctx, CostingVersion{LegalEntityID: ids.New(), Code: "STD-Z", Type: "STANDARD", Currency: "EUR", EffectiveFrom: "2026-01-01"})

	first, err := s.ListVersions(ctx, VersionFilter{LegalEntityID: le}, "", 2)
	if err != nil || len(first.Items) != 2 || first.Items[0].Code != "PLAN-B" || first.Items[1].Code != "STD-A" || first.Next == "" {
		t.Fatalf("first page %+v, %v", first, err)
	}
	rest, err := s.ListVersions(ctx, VersionFilter{LegalEntityID: le}, first.Next, 2)
	if err != nil || len(rest.Items) != 1 || rest.Items[0].Code != "STD-C" || rest.Next != "" {
		t.Fatalf("second page %+v, %v", rest, err)
	}
	std, err := s.ListVersions(ctx, VersionFilter{Type: "STANDARD", Status: "DRAFT"}, "", 50)
	if err != nil || len(std.Items) != 3 {
		t.Fatalf("standard drafts across entities = %d, %v; want 3", len(std.Items), err)
	}
	// Another tenant sees none of them (RLS).
	if other, err := s.ListVersions(scopedCtx(testkit.TenantID(16)), VersionFilter{}, "", 50); err != nil || len(other.Items) != 0 {
		t.Errorf("other tenant sees %d versions, %v", len(other.Items), err)
	}
	if _, err := s.GetVersion(scopedCtx(testkit.TenantID(16)), first.Items[0].ID); problemCode(err) != CodeVersionNotFound {
		t.Errorf("cross-tenant get: %v", err)
	}
	if _, err := s.ListItemCosts(ctx, ids.New(), "", 10); problemCode(err) != CodeVersionNotFound {
		t.Errorf("item costs of unknown version: %v", err)
	}
}
