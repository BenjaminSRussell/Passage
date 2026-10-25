package store_test

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/BenjaminSRussell/passage/platform/dbx"
	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/services/org/internal/domain"
	orgevents "github.com/BenjaminSRussell/passage/services/org/internal/events"
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

func TestPostgres_HierarchyTypeConcurrentSameKeyReturnsOneType(t *testing.T) {
	s, ctx, _ := lifecycleStore(t)
	const callers = 8
	typeIDs := make([]string, callers)
	errs := make([]error, callers)
	var wg sync.WaitGroup
	for i := range callers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			created, err := s.CreateHierarchyType(ctx, hierarchyType("RACE"), "race-key")
			typeIDs[i], errs[i] = created.ID, err
		}()
	}
	wg.Wait()
	for i := range callers {
		if errs[i] != nil || typeIDs[i] != typeIDs[0] {
			t.Fatalf("caller %d = %q, %v; want the same type id %q", i, typeIDs[i], errs[i], typeIDs[0])
		}
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

const publisher = "user-1"

func (u hierarchyUnits) deepNodes() []store.HierarchyNode {
	nodes := u.nodes()
	nodes[2].ParentOrgUnitID = &u.first
	return nodes
}

func draftWithNodes(t *testing.T, s *store.Postgres, ctx context.Context, typeID string, nodes []store.HierarchyNode) store.HierarchyVersion {
	t.Helper()
	draft := mustCreateDraft(t, s, ctx, typeID)
	replaced, err := s.ReplaceHierarchyNodes(ctx, draft.ID, draft.Version, nodes)
	if err != nil {
		t.Fatal(err)
	}
	return replaced
}

func publish(s *store.Postgres, ctx context.Context, v store.HierarchyVersion, from time.Time) (store.HierarchyVersion, error) {
	return s.PublishHierarchyVersion(ctx, v.ID, v.Version, from, "", "", publisher, false)
}

func mustPublish(t *testing.T, s *store.Postgres, ctx context.Context, v store.HierarchyVersion, from time.Time) store.HierarchyVersion {
	t.Helper()
	published, err := publish(s, ctx, v, from)
	if err != nil {
		t.Fatal(err)
	}
	return published
}

func versionState(t *testing.T, ctx context.Context, pool *pgxpool.Pool, id string) (status string, effectiveTo *time.Time, version int) {
	t.Helper()
	err := dbx.WithTenantTx(ctx, pool, func(tx pgx.Tx) error {
		return tx.QueryRow(ctx, `select status, effective_to, version from hierarchy_version where id = $1`, id).Scan(&status, &effectiveTo, &version)
	})
	if err != nil {
		t.Fatal(err)
	}
	return status, effectiveTo, version
}

func TestPostgres_HierarchyPublishWritesClosureEventAndReplaysIdempotently(t *testing.T) {
	s, ctx, pool := lifecycleStore(t)
	units := createHierarchyUnits(t, s, ctx)
	draft := draftWithNodes(t, s, ctx, mustCreateHierarchyType(t, s, ctx, "REP").ID, units.deepNodes())
	from := day(2026, 1, 1)
	published, err := s.PublishHierarchyVersion(ctx, draft.ID, draft.Version, from, "", "pub-key", publisher, false)
	if err != nil {
		t.Fatal(err)
	}
	if published.Status != "published" || *published.EffectiveFrom != "2026-01-01" || published.EffectiveTo != nil || published.Version != draft.Version+1 ||
		published.PublishedAt == nil || *published.PublishedBy != publisher || published.PublishReason != nil || len(published.Nodes) != 3 {
		t.Fatalf("published = %+v", published)
	}
	if rows := fiscalRows(t, ctx, pool, "hierarchy_closure"); rows != 3 {
		t.Fatalf("closure rows = %d; want 3 (sum of depths of a chain of three)", rows)
	}
	replay, err := s.PublishHierarchyVersion(ctx, draft.ID, draft.Version, from, "", "pub-key", publisher, false)
	if err != nil || replay.Version != published.Version || !reflect.DeepEqual(replay.PublishedAt, published.PublishedAt) {
		t.Fatalf("replay = %+v, %v; want the published representation unchanged", replay, err)
	}
	if _, err := s.PublishHierarchyVersion(ctx, draft.ID, draft.Version, day(2026, 2, 1), "", "pub-key", publisher, false); problemCode(t, err) != store.CodeIdempotencyReuse {
		t.Fatalf("same key with another date error = %v", err)
	}
	next := mustCreateDraft(t, s, ctx, published.HierarchyTypeID)
	if _, err := s.PublishHierarchyVersion(ctx, next.ID, next.Version, day(2026, 3, 1), "", "pub-key", publisher, false); problemCode(t, err) != store.CodeIdempotencyReuse {
		t.Fatalf("same key on another version error = %v", err)
	}
	events := unitEvents(t, ctx, pool, orgevents.HierarchyVersionActivated)
	if len(events) != 1 {
		t.Fatalf("activated events = %d; want exactly one", len(events))
	}
	e := events[0]
	if e["version_id"] != draft.ID || e["hierarchy_type_id"] != draft.HierarchyTypeID || e["version_no"] != float64(1) || e["effective_from"] != "2026-01-01" ||
		e["node_count"] != float64(3) || e["superseded_version_id"] != nil || e["backdate_reason"] != nil || !reflect.DeepEqual(e["purposes"], []any{"reporting", "security"}) {
		t.Fatalf("event data = %+v", e)
	}
}

func TestPostgres_HierarchyPublishSupersedesAndRejectsOverlap(t *testing.T) {
	s, ctx, pool := lifecycleStore(t)
	units := createHierarchyUnits(t, s, ctx)
	typeID := mustCreateHierarchyType(t, s, ctx, "REP").ID
	first := mustPublish(t, s, ctx, draftWithNodes(t, s, ctx, typeID, units.nodes()), day(2026, 1, 1))
	second := mustPublish(t, s, ctx, mustCreateDraft(t, s, ctx, typeID), day(2026, 6, 1))
	status, to, version := versionState(t, ctx, pool, first.ID)
	if status != "superseded" || to == nil || !to.Equal(day(2026, 6, 1)) || version != first.Version+1 {
		t.Fatalf("predecessor = %s %v v%d; want superseded until 2026-06-01 with a bumped version", status, to, version)
	}
	if second.Status != "published" || second.EffectiveTo != nil {
		t.Fatalf("successor = %+v", second)
	}
	if events := unitEvents(t, ctx, pool, orgevents.HierarchyVersionActivated); events[1]["superseded_version_id"] != first.ID {
		t.Fatalf("second event = %+v; want superseded_version_id %s", events[1], first.ID)
	}
	third := mustCreateDraft(t, s, ctx, typeID)
	for _, from := range []time.Time{day(2026, 6, 1), day(2026, 5, 1), day(2025, 1, 1)} {
		_, err := publish(s, ctx, third, from)
		if problemCode(t, err) != store.CodeHierarchyOverlap {
			t.Fatalf("publish from %s error = %v; want overlap", from.Format("2006-01-02"), err)
		}
	}
	if status, _, v := versionState(t, ctx, pool, third.ID); status != "draft" || v != third.Version {
		t.Fatalf("rejected draft = %s v%d; want unchanged", status, v)
	}
	if status, _, v := versionState(t, ctx, pool, second.ID); status != "published" || v != second.Version {
		t.Fatalf("latest = %s v%d; want unchanged", status, v)
	}
	if _, err := publish(s, ctx, third, day(2026, 5, 1)); err == nil || !strings.Contains(err.Error(), "overlaps version 2") {
		t.Fatalf("overlap detail = %v; want the overlapped version number", err)
	}
}

func TestPostgres_HierarchyPublishRejections(t *testing.T) {
	s, ctx, pool := lifecycleStore(t)
	units := createHierarchyUnits(t, s, ctx)
	typeID := mustCreateHierarchyType(t, s, ctx, "REP").ID
	draft := draftWithNodes(t, s, ctx, typeID, units.nodes())
	stale := draft
	stale.Version--
	if _, err := publish(s, ctx, stale, day(2026, 1, 1)); problemCode(t, err) != store.CodeVersionConflict {
		t.Fatalf("stale version error = %v", err)
	}
	published := mustPublish(t, s, ctx, draft, day(2026, 1, 1))
	if _, err := publish(s, ctx, published, day(2026, 2, 1)); problemCode(t, err) != store.CodeHierarchyVersionImmutable {
		t.Fatalf("republish error = %v", err)
	}
	if _, err := s.PublishHierarchyVersion(ctx, ids.New(), 1, day(2026, 2, 1), "", "", publisher, false); problemCode(t, err) != store.CodeHierarchyVersionNotFound {
		t.Fatalf("unknown version error = %v", err)
	}
	if _, err := s.PublishHierarchyVersion(principalContext(2), draft.ID, 1, day(2026, 2, 1), "", "", publisher, false); problemCode(t, err) != store.CodeHierarchyVersionNotFound {
		t.Fatalf("other tenant error = %v", err)
	}
	empty := mustCreateDraft(t, s, ctx, mustCreateHierarchyType(t, s, ctx, "EMPTY").ID)
	_, err := publish(s, ctx, empty, day(2026, 1, 1))
	var de *domain.Error
	if !errors.As(err, &de) || de.Code != domain.CodeHierarchyMultiRoot {
		t.Fatalf("empty draft error = %v; want %s", err, domain.CodeHierarchyMultiRoot)
	}
	if status, _, _ := versionState(t, ctx, pool, empty.ID); status != "draft" {
		t.Fatalf("empty draft status = %s", status)
	}
}

func TestPostgres_HierarchyPublishBackdateChecks(t *testing.T) {
	s, ctx, pool := lifecycleStore(t)
	units := createHierarchyUnits(t, s, ctx)
	strict := mustCreateHierarchyType(t, s, ctx, "STRICT")
	open := hierarchyType("OPEN")
	open.BackdateAllowed = true
	openType, err := s.CreateHierarchyType(ctx, open, "")
	if err != nil {
		t.Fatal(err)
	}
	const reason = "Correcting the reporting structure"
	strictDraft := draftWithNodes(t, s, ctx, strict.ID, units.nodes())
	first := mustPublish(t, s, ctx, draftWithNodes(t, s, ctx, openType.ID, units.nodes()), day(2026, 1, 1))
	draft := mustCreateDraft(t, s, ctx, openType.ID)
	tests := []struct {
		name    string
		version store.HierarchyVersion
		from    time.Time
		reason  string
		code    string
	}{
		{"type forbids backdating", strictDraft, day(2025, 12, 1), reason, store.CodeHierarchyBackdateDuty},
		{"reason of nine characters", draft, day(2026, 2, 1), "123456789", store.CodeHierarchyBackdateDuty},
		{"no reason", draft, day(2026, 2, 1), "", store.CodeHierarchyBackdateDuty},
		{"reason over 500 characters", draft, day(2026, 2, 1), string(make([]rune, 501)), store.CodeHierarchyBackdateDuty},
		{"allowed but overlapping", draft, day(2025, 12, 1), reason, store.CodeHierarchyOverlap},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			_, err := s.PublishHierarchyVersion(ctx, tc.version.ID, tc.version.Version, tc.from, tc.reason, "", publisher, true)
			if problemCode(t, err) != tc.code {
				t.Fatalf("error = %v; want %s", err, tc.code)
			}
			if status, _, v := versionState(t, ctx, pool, tc.version.ID); status != "draft" || v != tc.version.Version {
				t.Fatalf("version = %s v%d after a refusal; want the draft unchanged", status, v)
			}
		})
	}
	if status, _, _ := versionState(t, ctx, pool, first.ID); status != "published" {
		t.Fatalf("predecessor status = %s after refusals", status)
	}
	published, err := s.PublishHierarchyVersion(ctx, draft.ID, draft.Version, day(2026, 2, 1), "  "+reason+"  ", "", publisher, true)
	if err != nil || published.PublishReason == nil || *published.PublishReason != reason {
		t.Fatalf("backdated publish = %+v, %v; want the trimmed reason stored", published, err)
	}
	events := unitEvents(t, ctx, pool, orgevents.HierarchyVersionActivated)
	if len(events) != 2 || events[1]["backdate_reason"] != reason {
		t.Fatalf("events = %+v; want the backdate reason on the second", events)
	}
}

func TestPostgres_HierarchyAsOfAnswersFromTheVersionInForce(t *testing.T) {
	s, ctx, _ := lifecycleStore(t)
	units := createHierarchyUnits(t, s, ctx)
	typeID := mustCreateHierarchyType(t, s, ctx, "REP").ID
	first := mustPublish(t, s, ctx, draftWithNodes(t, s, ctx, typeID, units.deepNodes()), day(2026, 1, 1))
	second := mustPublish(t, s, ctx, draftWithNodes(t, s, ctx, typeID, units.nodes()), day(2026, 6, 1))
	pending := mustCreateDraft(t, s, ctx, typeID)
	if pending.Status != "draft" {
		t.Fatal("expected an open draft")
	}
	root, err := s.HierarchyAsOf(ctx, typeID, day(2026, 3, 1), units.legalEntity)
	if err != nil {
		t.Fatal(err)
	}
	wantDown := []store.AsOfRelative{{OrgUnitID: units.first, Depth: 1}, {OrgUnitID: units.second, Depth: 2}}
	if root.VersionID != first.ID || root.VersionNo != 1 || root.EffectiveFrom != "2026-01-01" || *root.EffectiveTo != "2026-06-01" || root.Kind != "legal_entity" ||
		root.ParentOrgUnitID != nil || len(root.Ancestors) != 0 || root.Ancestors == nil || !reflect.DeepEqual(root.Descendants, wantDown) {
		t.Fatalf("root in the superseded range = %+v", root)
	}
	leaf, err := s.HierarchyAsOf(ctx, typeID, day(2026, 1, 1), units.second)
	wantUp := []store.AsOfRelative{{OrgUnitID: units.first, Depth: 1}, {OrgUnitID: units.legalEntity, Depth: 2}}
	if err != nil || leaf.Kind != "operating_unit" || *leaf.ParentOrgUnitID != units.first || !reflect.DeepEqual(leaf.Ancestors, wantUp) || len(leaf.Descendants) != 0 {
		t.Fatalf("leaf on the first day = %+v, %v", leaf, err)
	}
	later, err := s.HierarchyAsOf(ctx, typeID, day(2026, 6, 1), units.second)
	if err != nil || later.VersionID != second.ID || later.EffectiveTo != nil || *later.ParentOrgUnitID != units.legalEntity || len(later.Ancestors) != 1 {
		t.Fatalf("leaf on the supersede day = %+v, %v; want the successor", later, err)
	}
	if _, err := s.HierarchyAsOf(ctx, typeID, day(2025, 12, 31), units.second); problemCode(t, err) != store.CodeHierarchyNoVersion {
		t.Fatalf("date before the first version error = %v", err)
	}
	if _, err := s.HierarchyAsOf(ctx, typeID, day(2026, 3, 1), ids.New()); problemCode(t, err) != store.CodeHierarchyNodeAbsent {
		t.Fatalf("absent node error = %v", err)
	}
	if _, err := s.HierarchyAsOf(ctx, ids.New(), day(2026, 3, 1), units.second); problemCode(t, err) != store.CodeHierarchyTypeNotFound {
		t.Fatalf("unknown type error = %v", err)
	}
	if _, err := s.HierarchyAsOf(principalContext(2), typeID, day(2026, 3, 1), units.second); problemCode(t, err) != store.CodeHierarchyTypeNotFound {
		t.Fatalf("other tenant error = %v", err)
	}
	draftOnly := mustCreateHierarchyType(t, s, ctx, "DRAFT")
	mustCreateDraft(t, s, ctx, draftOnly.ID)
	if _, err := s.HierarchyAsOf(ctx, draftOnly.ID, day(2026, 3, 1), units.second); problemCode(t, err) != store.CodeHierarchyNoVersion {
		t.Fatalf("draft-only type error = %v; drafts are never in force", err)
	}
}

func TestPostgres_HierarchyClosureIsTenantIsolated(t *testing.T) {
	s, ctx, pool := lifecycleStore(t)
	units := createHierarchyUnits(t, s, ctx)
	mustPublish(t, s, ctx, draftWithNodes(t, s, ctx, mustCreateHierarchyType(t, s, ctx, "REP").ID, units.nodes()), day(2026, 1, 1))
	if n := fiscalRows(t, principalContext(2), pool, "hierarchy_closure"); n != 0 {
		t.Fatalf("other tenant reads %d closure rows", n)
	}
	if n := fiscalRows(t, ctx, pool, "hierarchy_closure"); n != 2 {
		t.Fatalf("owner reads %d closure rows; want 2", n)
	}
}
