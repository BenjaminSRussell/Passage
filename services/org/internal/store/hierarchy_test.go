package store_test

import (
	"context"
	"errors"
	"reflect"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/BenjaminSRussell/passage/platform/dbx"
	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/services/org/internal/domain"
	"github.com/BenjaminSRussell/passage/services/org/internal/store"
)

func hierarchyType(code string) store.HierarchyType {
	return store.HierarchyType{Code: code, Name: "Type " + code, Purposes: []string{"reporting", "security"}}
}

func mustCreateHierarchyType(t *testing.T, s *store.Postgres, ctx context.Context, code string) store.HierarchyType {
	t.Helper()
	created, err := s.CreateHierarchyType(ctx, hierarchyType(code), "")
	if err != nil {
		t.Fatal(err)
	}
	return created
}

func mustCreateDraft(t *testing.T, s *store.Postgres, ctx context.Context, typeID string) store.HierarchyVersion {
	t.Helper()
	draft, err := s.CreateHierarchyDraft(ctx, typeID, "")
	if err != nil {
		t.Fatal(err)
	}
	return draft
}

type hierarchyUnits struct{ legalEntity, first, second string }

func createHierarchyUnits(t *testing.T, s *store.Postgres, ctx context.Context) hierarchyUnits {
	t.Helper()
	return hierarchyUnits{
		legalEntity: mustCreate(t, s, ctx, newEntity("US01")).ID,
		first:       mustCreateUnit(t, s, ctx, unit("department", "SALES")).ID,
		second:      mustCreateUnit(t, s, ctx, unit("cost_center", "CC100")).ID,
	}
}

func (u hierarchyUnits) nodes() []store.HierarchyNode {
	return []store.HierarchyNode{
		{OrgUnitID: u.legalEntity},
		{OrgUnitID: u.first, ParentOrgUnitID: &u.legalEntity, SortOrder: 1},
		{OrgUnitID: u.second, ParentOrgUnitID: &u.legalEntity, SortOrder: 2},
	}
}

func nodeProjection(nodes []store.HierarchyNode) []store.HierarchyNode {
	projected := make([]store.HierarchyNode, len(nodes))
	for i, n := range nodes {
		projected[i] = store.HierarchyNode{OrgUnitID: n.OrgUnitID, ParentOrgUnitID: n.ParentOrgUnitID, SortOrder: n.SortOrder}
	}
	return projected
}

func publishVersion(t *testing.T, ctx context.Context, pool *pgxpool.Pool, versionID, effectiveFrom string) {
	t.Helper()
	err := dbx.WithTenantTx(ctx, pool, func(tx pgx.Tx) error {
		_, err := tx.Exec(ctx, `update hierarchy_version set status = 'published', effective_from = $2 where id = $1`, versionID, effectiveFrom)
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
}

func storedNodeCount(t *testing.T, ctx context.Context, pool *pgxpool.Pool, versionID string) int {
	t.Helper()
	var n int
	err := dbx.WithTenantTx(ctx, pool, func(tx pgx.Tx) error {
		return tx.QueryRow(ctx, `select count(*) from hierarchy_node where version_id = $1`, versionID).Scan(&n)
	})
	if err != nil {
		t.Fatal(err)
	}
	return n
}

func TestPostgres_HierarchyTypeCreateReplayReuseAndDuplicate(t *testing.T) {
	s, ctx, _ := lifecycleStore(t)
	first, err := s.CreateHierarchyType(ctx, hierarchyType("REP"), "key-1")
	if err != nil {
		t.Fatal(err)
	}
	if first.BackdateAllowed || first.Status != "active" || first.Version != 1 || !reflect.DeepEqual(first.Purposes, []string{"reporting", "security"}) {
		t.Fatalf("created = %+v; want defaults and purposes read back", first)
	}
	replay, err := s.CreateHierarchyType(ctx, hierarchyType("REP"), "key-1")
	if err != nil || replay.ID != first.ID {
		t.Fatalf("replay = %+v, %v; want the original type", replay, err)
	}
	changed := hierarchyType("REP")
	changed.Purposes = []string{"procurement"}
	if _, err := s.CreateHierarchyType(ctx, changed, "key-1"); problemCode(t, err) != store.CodeIdempotencyReuse {
		t.Fatalf("same key with a different body error = %v", err)
	}
	if _, err := s.CreateHierarchyType(ctx, hierarchyType("REP"), "key-2"); problemCode(t, err) != store.CodeHierarchyTypeCode {
		t.Fatalf("duplicate code error = %v", err)
	}
}

func TestPostgres_HierarchyDraftNumberingReplayAndOneOpenDraft(t *testing.T) {
	s, ctx, _ := lifecycleStore(t)
	ht := mustCreateHierarchyType(t, s, ctx, "REP")
	first, err := s.CreateHierarchyDraft(ctx, ht.ID, "draft-key")
	if err != nil {
		t.Fatal(err)
	}
	if first.VersionNo != 1 || first.Status != "draft" || first.NodeCount != 0 || first.CopiedFromVersionID != nil || first.EffectiveFrom != nil || first.Nodes == nil || len(first.Nodes) != 0 || first.Version != 1 {
		t.Fatalf("first draft = %+v", first)
	}
	if _, err := s.CreateHierarchyDraft(ctx, ht.ID, "other-key"); problemCode(t, err) != store.CodeHierarchyDraftExists {
		t.Fatalf("second draft error = %v", err)
	}
	replay, err := s.CreateHierarchyDraft(ctx, ht.ID, "draft-key")
	if err != nil || replay.ID != first.ID {
		t.Fatalf("replay = %+v, %v; want the first draft", replay, err)
	}
	other := mustCreateHierarchyType(t, s, ctx, "PROC")
	if _, err := s.CreateHierarchyDraft(ctx, other.ID, "draft-key"); problemCode(t, err) != store.CodeIdempotencyReuse {
		t.Fatalf("same key for another type error = %v", err)
	}
	if _, err := s.CreateHierarchyDraft(ctx, ids.New(), ""); problemCode(t, err) != store.CodeHierarchyTypeNotFound {
		t.Fatalf("unknown type error = %v", err)
	}
}

func TestPostgres_HierarchyReplaceResolvesKindsBumpsVersionAndReconciles(t *testing.T) {
	s, ctx, pool := lifecycleStore(t)
	units := createHierarchyUnits(t, s, ctx)
	draft := mustCreateDraft(t, s, ctx, mustCreateHierarchyType(t, s, ctx, "REP").ID)
	replaced, err := s.ReplaceHierarchyNodes(ctx, draft.ID, 1, units.nodes())
	if err != nil {
		t.Fatal(err)
	}
	wantKinds := []string{"legal_entity", "operating_unit", "operating_unit"}
	if replaced.NodeCount != 3 || replaced.Version != 2 || len(replaced.Nodes) != 3 {
		t.Fatalf("replaced = %+v", replaced)
	}
	for i, n := range replaced.Nodes {
		if n.Kind != wantKinds[i] || n.OrgUnitID != units.nodes()[i].OrgUnitID {
			t.Fatalf("node %d = %+v; want kind %s in sortOrder", i, n, wantKinds[i])
		}
	}
	if stored := storedNodeCount(t, ctx, pool, draft.ID); stored != replaced.NodeCount {
		t.Fatalf("node_count %d != stored nodes %d", replaced.NodeCount, stored)
	}
	again, err := s.ReplaceHierarchyNodes(ctx, draft.ID, 2, units.nodes())
	if err != nil || again.Version != 3 || !reflect.DeepEqual(nodeProjection(again.Nodes), nodeProjection(replaced.Nodes)) {
		t.Fatalf("replay = %+v, %v; want the same node set at version 3", again, err)
	}
	if _, err := s.ReplaceHierarchyNodes(ctx, draft.ID, 1, units.nodes()); problemCode(t, err) != store.CodeVersionConflict {
		t.Fatalf("stale version error = %v", err)
	}
}

func TestPostgres_HierarchyReplaceRejectsAndKeepsNodes(t *testing.T) {
	s, ctx, pool := lifecycleStore(t)
	units := createHierarchyUnits(t, s, ctx)
	draft := mustCreateDraft(t, s, ctx, mustCreateHierarchyType(t, s, ctx, "REP").ID)
	good, err := s.ReplaceHierarchyNodes(ctx, draft.ID, 1, units.nodes())
	if err != nil {
		t.Fatal(err)
	}
	stranger := ids.New()
	cycle := units.nodes()
	cycle[0].ParentOrgUnitID = &units.first
	tests := []struct {
		name  string
		nodes []store.HierarchyNode
		code  string
	}{
		{"unknown org unit", []store.HierarchyNode{{OrgUnitID: units.legalEntity}, {OrgUnitID: stranger, ParentOrgUnitID: &units.legalEntity}}, store.CodeHierarchyNodeUnknown},
		{"cycle", cycle, domain.CodeHierarchyCycle},
		{"two roots", []store.HierarchyNode{{OrgUnitID: units.legalEntity}, {OrgUnitID: units.first}}, domain.CodeHierarchyMultiRoot},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			_, err := s.ReplaceHierarchyNodes(ctx, draft.ID, good.Version, tc.nodes)
			var de *domain.Error
			var code string
			if errors.As(err, &de) {
				code = de.Code
			} else {
				code = problemCode(t, err)
			}
			if code != tc.code {
				t.Fatalf("error = %v; want %s", err, tc.code)
			}
			if stored := storedNodeCount(t, ctx, pool, draft.ID); stored != 3 {
				t.Fatalf("stored nodes = %d after a rejected request; want 3", stored)
			}
		})
	}
	reloaded, err := s.ReplaceHierarchyNodes(ctx, draft.ID, good.Version, units.nodes())
	if err != nil || reloaded.Version != good.Version+1 {
		t.Fatalf("version after rejections = %+v, %v; want only the accepted replace to bump it", reloaded, err)
	}
}

func TestPostgres_HierarchyDraftCopiesLatestPublishedAndPublishedIsImmutable(t *testing.T) {
	s, ctx, pool := lifecycleStore(t)
	units := createHierarchyUnits(t, s, ctx)
	ht := mustCreateHierarchyType(t, s, ctx, "REP")
	first := mustCreateDraft(t, s, ctx, ht.ID)
	if _, err := s.ReplaceHierarchyNodes(ctx, first.ID, 1, units.nodes()); err != nil {
		t.Fatal(err)
	}
	publishVersion(t, ctx, pool, first.ID, "2026-01-01")
	if _, err := s.ReplaceHierarchyNodes(ctx, first.ID, 2, units.nodes()); problemCode(t, err) != store.CodeHierarchyVersionImmutable {
		t.Fatalf("replace on a published version error = %v", err)
	}
	next := mustCreateDraft(t, s, ctx, ht.ID)
	if next.VersionNo != 2 || next.CopiedFromVersionID == nil || *next.CopiedFromVersionID != first.ID || next.NodeCount != 3 {
		t.Fatalf("next draft = %+v", next)
	}
	if !reflect.DeepEqual(nodeProjection(next.Nodes), nodeProjection(units.nodes())) {
		t.Fatalf("copied projection %+v != source %+v", nodeProjection(next.Nodes), nodeProjection(units.nodes()))
	}
	if stored := storedNodeCount(t, ctx, pool, next.ID); stored != next.NodeCount {
		t.Fatalf("node_count %d != stored nodes %d", next.NodeCount, stored)
	}
}

func TestPostgres_HierarchyIsTenantIsolated(t *testing.T) {
	s, ctx, pool := lifecycleStore(t)
	units := createHierarchyUnits(t, s, ctx)
	ht := mustCreateHierarchyType(t, s, ctx, "REP")
	draft := mustCreateDraft(t, s, ctx, ht.ID)
	if _, err := s.ReplaceHierarchyNodes(ctx, draft.ID, 1, units.nodes()); err != nil {
		t.Fatal(err)
	}
	other := principalContext(2)
	if _, err := s.ReplaceHierarchyNodes(other, draft.ID, 2, units.nodes()); problemCode(t, err) != store.CodeHierarchyVersionNotFound {
		t.Fatalf("other tenant replace error = %v", err)
	}
	if _, err := s.CreateHierarchyDraft(other, ht.ID, ""); problemCode(t, err) != store.CodeHierarchyTypeNotFound {
		t.Fatalf("other tenant draft error = %v", err)
	}
	for _, table := range []string{"hierarchy_type", "hierarchy_version", "hierarchy_node"} {
		if n := fiscalRows(t, other, pool, table); n != 0 {
			t.Fatalf("other tenant reads %d rows of %s", n, table)
		}
		if n := fiscalRows(t, ctx, pool, table); n == 0 {
			t.Fatalf("owner tenant reads no rows of %s", table)
		}
	}
}

func TestPostgres_HierarchyNodeRejectsDuplicateOrgUnitPerVersion(t *testing.T) {
	s, ctx, pool := lifecycleStore(t)
	units := createHierarchyUnits(t, s, ctx)
	draft := mustCreateDraft(t, s, ctx, mustCreateHierarchyType(t, s, ctx, "REP").ID)
	err := dbx.WithTenantTx(ctx, pool, func(tx pgx.Tx) error {
		_, err := tx.Exec(ctx, `insert into hierarchy_node (tenant_id, version_id, id, org_unit_kind, org_unit_id)
			select current_setting('passage.tenant_id')::uuid, $1, gen_random_uuid(), 'legal_entity', $2 from generate_series(1, 2)`, draft.ID, units.legalEntity)
		return err
	})
	var pgErr *pgconn.PgError
	if !errors.As(err, &pgErr) || pgErr.Code != "23505" {
		t.Fatalf("duplicate org_unit_id insert error = %v; want unique violation", err)
	}
}
