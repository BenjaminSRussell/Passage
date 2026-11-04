package store_test

import (
	"net/http"
	"testing"

	"github.com/jackc/pgx/v5"

	"github.com/BenjaminSRussell/passage/platform/dbx"
	"github.com/BenjaminSRussell/passage/services/pricing/internal/domain"
	"github.com/BenjaminSRussell/passage/services/pricing/internal/store"
)

func TestPostgres_TenantParametersVersionedIsolatedAndAnnounced(t *testing.T) {
	pool := appPool(t)
	ctx := asUser("admin-1")
	s := store.New(pool)
	def, err := s.GetTenantParameters(ctx)
	if err != nil || def.Version != 0 || def.CacheTTLSeconds != 0 || def.CampaignKillSwitchEffect != domain.KillEffectNextResolve || def.UpdatedAt != nil {
		t.Fatalf("defaults = %+v, %v", def, err)
	}
	in := store.TenantParameters{CacheTTLSeconds: 30, CampaignKillSwitchEffect: domain.KillEffectFlush}
	set, err := s.SetTenantParameters(ctx, in, 0, "tp-1")
	if err != nil || set.Version != 1 || set.CacheTTLSeconds != 30 || set.CampaignKillSwitchEffect != domain.KillEffectFlush || set.UpdatedBy != "admin-1" || set.UpdatedAt == nil {
		t.Fatalf("set = %+v, %v", set, err)
	}
	if replay, err := s.SetTenantParameters(ctx, in, 0, "tp-1"); err != nil || replay.Version != 1 {
		t.Fatalf("replay = %+v, %v; want the first response", replay, err)
	}
	if _, err := s.SetTenantParameters(ctx, store.TenantParameters{CacheTTLSeconds: 40}, 1, "tp-1"); problemCode(err) != store.CodeIdempotencyUse {
		t.Fatalf("same key, other request = %v; want %s", err, store.CodeIdempotencyUse)
	}
	if _, err := s.SetTenantParameters(ctx, in, 0, ""); problemCode(err) != store.CodeVersionConflict || problemStatus(err) != http.StatusPreconditionFailed {
		t.Fatalf("stale insert = %v; want 412 %s", err, store.CodeVersionConflict)
	}
	if _, err := s.SetTenantParameters(ctx, in, 7, ""); problemCode(err) != store.CodeVersionConflict {
		t.Fatalf("stale update = %v; want %s", err, store.CodeVersionConflict)
	}
	for _, bad := range []store.TenantParameters{{CacheTTLSeconds: 9}, {CacheTTLSeconds: 601}, {CampaignKillSwitchEffect: "NOW"}} {
		if _, err := s.SetTenantParameters(ctx, bad, 1, ""); problemStatus(err) != http.StatusUnprocessableEntity {
			t.Fatalf("%+v = %v; want 422", bad, err)
		}
	}
	// 0 clears the tenant's own lifetime (stored NULL) and the effect falls back to the default.
	back, err := s.SetTenantParameters(asUser("admin-2"), store.TenantParameters{}, 1, "")
	if err != nil || back.Version != 2 || back.CacheTTLSeconds != 0 || back.CampaignKillSwitchEffect != domain.KillEffectNextResolve || back.UpdatedBy != "admin-2" {
		t.Fatalf("clear = %+v, %v", back, err)
	}
	var isNull bool
	if err := dbx.WithTenantTx(ctx, pool, func(tx pgx.Tx) error {
		return tx.QueryRow(ctx, `select cache_ttl_seconds is null from pricing_tenant_parameter`).Scan(&isNull)
	}); err != nil || !isNull {
		t.Fatalf("cleared lifetime stored as NULL = %v, %v", isNull, err)
	}
	// Another tenant sees the defaults, and writes its own row.
	other := principal(2, "")
	if got, err := s.GetTenantParameters(other); err != nil || got.Version != 0 {
		t.Fatalf("other tenant = %+v, %v; want its own defaults", got, err)
	}
	if got, err := s.SetTenantParameters(other, store.TenantParameters{CacheTTLSeconds: 600}, 0, ""); err != nil || got.Version != 1 {
		t.Fatalf("other tenant set = %+v, %v", got, err)
	}
	if got, _ := s.GetTenantParameters(ctx); got.CacheTTLSeconds != 0 {
		t.Fatalf("first tenant = %+v; want it unchanged by the other tenant's write", got)
	}
	// Each change is audited through the outbox; the replay, the conflicts and the refusals add none.
	var changes []map[string]any
	for _, r := range outboxRows(t, ctx, pool) {
		if r.Type == "dev.passage.pricing.tenantparameter.changed.v1" {
			changes = append(changes, r.Data)
		}
	}
	// The outbox listing spans tenants: this tenant's two changes, then the other tenant's.
	n := len(changes) - 1
	var last map[string]any
	if n > 0 {
		last = changes[n-1]
	}
	if n != 2 || last["cache_ttl_seconds"] != float64(0) || last["campaign_kill_switch_effect"] != "NEXT_RESOLVE" || last["updated_by"] != "admin-2" || last["version"] != float64(2) {
		t.Fatalf("%d events, last %v; want two, the last with the cleared values by admin-2", n, last)
	}
}

func TestPostgres_SearchModeAndSmallOrderThresholds(t *testing.T) {
	pool := appPool(t)
	ctx := principal(1, "")
	s := store.New(pool)
	def, err := s.GetParameters(ctx, legalEntity)
	if err != nil || def.PriceSearchMode != domain.SearchModeFirstMatch || len(def.SmallOrderThresholds) != 0 {
		t.Fatalf("defaults = %+v, %v", def, err)
	}
	set, err := s.SetParameters(ctx, store.Parameters{LegalEntityID: legalEntity, PriceSearchMode: domain.SearchModeNext,
		SmallOrderThresholds: map[string]string{"USD": "100.0", "GBP": "0"}}, 0, "")
	if err != nil || set.PriceSearchMode != domain.SearchModeNext || set.SmallOrderThresholds["USD"] != "100" || set.SmallOrderThresholds["GBP"] != "0" {
		t.Fatalf("set = %+v, %v", set, err)
	}
	got, err := s.GetParameters(ctx, legalEntity)
	if err != nil || got.PriceSearchMode != domain.SearchModeNext || got.SmallOrderThresholds["USD"] != "100" || len(got.SmallOrderThresholds) != 2 {
		t.Fatalf("read = %+v, %v", got, err)
	}
	for _, bad := range []store.Parameters{
		{LegalEntityID: legalEntity, PriceSearchMode: "ANY"},
		{LegalEntityID: legalEntity, SmallOrderThresholds: map[string]string{"usd": "1"}},
		{LegalEntityID: legalEntity, SmallOrderThresholds: map[string]string{"USD": "-1"}},
	} {
		if _, err := s.SetParameters(ctx, bad, 1, ""); problemStatus(err) != http.StatusUnprocessableEntity {
			t.Fatalf("%+v = %v; want 422", bad, err)
		}
	}
	ev := outboxRows(t, ctx, pool)
	last := ev[len(ev)-1]
	th, _ := last.Data["small_order_thresholds"].(map[string]any)
	if last.Type != "dev.passage.pricing.parameters.changed.v1" || last.Data["price_search_mode"] != "SEARCH_NEXT" || th["USD"] != "100" {
		t.Fatalf("event = %+v", last)
	}
	// An empty table is stored as NULL: the entity follows the reference thresholds again.
	back, err := s.SetParameters(ctx, store.Parameters{LegalEntityID: legalEntity}, 1, "")
	if err != nil || back.PriceSearchMode != domain.SearchModeFirstMatch || len(back.SmallOrderThresholds) != 0 {
		t.Fatalf("clear = %+v, %v", back, err)
	}
	var isNull bool
	if err := dbx.WithTenantTx(ctx, pool, func(tx pgx.Tx) error {
		return tx.QueryRow(ctx, `select small_order_thresholds is null from pricing_parameter where legal_entity_id = $1`, legalEntity).Scan(&isNull)
	}); err != nil || !isNull {
		t.Fatalf("empty table stored as NULL = %v, %v", isNull, err)
	}
}
