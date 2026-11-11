package store

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/platform/testkit"
	"github.com/BenjaminSRussell/passage/services/shopfloor/internal/domain"
	sfevents "github.com/BenjaminSRussell/passage/services/shopfloor/internal/events"
)

// newEdge registers a node bound to the connector identity "conn-<key>".
func newEdge(t *testing.T, s *Postgres, ctx context.Context, name, key string) EdgeNode {
	t.Helper()
	sub := "conn-" + key
	n, err := s.CreateEdgeNode(ctx, EdgeNodeInput{Name: name, Platform: domain.PlatformK3s, Ring: domain.RingCanary, ConnectorSubject: &sub, Active: true}, "admin-1", idem(key))
	if err != nil {
		t.Fatal(err)
	}
	return n
}

func sub(n EdgeNode) string { return *n.ConnectorSubject }

func edgeDoc(machine string) json.RawMessage {
	return json.RawMessage(`{"machines":[{"machineId":"` + machine + `","resourceId":"` + ids.New() + `"}],"tags":[{"address":"ns=2;s=C","machineId":"` + machine + `","kind":"count"}]}`)
}

func TestEdgeNode_CreateUpdateListAndEvents(t *testing.T) {
	s, app, _ := setup(t)
	tenantID := testkit.TenantID(51)
	ctx := testkit.Ctx(tenantID)
	n := newEdge(t, s, ctx, "Plant 1", "e-1")
	if n.Version != 1 || n.Status != domain.EdgePending || n.Ring != "canary" || len(n.Machines) != 0 || n.LatestConfigVersion != nil {
		t.Fatalf("created = %+v", n)
	}
	if _, err := s.CreateEdgeNode(ctx, EdgeNodeInput{Name: "Plant 1", Platform: "linux", Ring: "all", Active: true}, "admin-1", idem("e-2")); problemCode(err) != CodeEdgeNameTaken {
		t.Fatalf("duplicate name: %v", err)
	}
	up, err := s.UpdateEdgeNode(ctx, n.ID, 1, EdgeNodeInput{Name: "Plant 1 gateway", Ring: domain.RingOne, Active: true}, "admin-2")
	if err != nil || up.Version != 2 || up.Ring != "ring1" || up.Platform != "k3s" {
		t.Fatalf("update = %+v, %v", up, err)
	}
	if _, err := s.UpdateEdgeNode(ctx, n.ID, 1, EdgeNodeInput{Name: "Stale", Ring: "all", Active: true}, "admin-2"); problemCode(err) != CodeVersionMismatch {
		t.Fatalf("stale: %v", err)
	}
	if _, err := s.UpdateEdgeNode(ctx, ids.New(), 1, EdgeNodeInput{Name: "x", Ring: "all"}, "admin-2"); problemCode(err) != CodeEdgeNotFound {
		t.Fatalf("unknown: %v", err)
	}
	newEdge(t, s, ctx, "Plant 2", "e-3")
	if l, err := s.ListEdgeNodes(ctx, "", "", 10); err != nil || len(l) != 2 || l[0].Name != "Plant 1 gateway" {
		t.Fatalf("list = %+v, %v", l, err)
	}
	if l, _ := s.ListEdgeNodes(ctx, "ring1", "", 10); len(l) != 1 {
		t.Fatalf("ring filter = %+v", l)
	}
	if l, _ := s.ListEdgeNodes(ctx, "", "pending", 10); len(l) != 2 {
		t.Fatalf("pending filter = %+v", l)
	}
	if n := countEvents(t, app, tenantID, sfevents.EdgeNodeSaved); n != 3 {
		t.Fatalf("edgenode.saved events = %d, want 3", n)
	}
	if _, err := s.GetEdgeNode(testkit.Ctx(testkit.TenantID(52)), n.ID); problemCode(err) != CodeEdgeNotFound {
		t.Fatalf("other tenant: %v", err)
	}
}

func TestEdgeConfig_PublishVersionsAndPull(t *testing.T) {
	s, app, _ := setup(t)
	tenantID := testkit.TenantID(53)
	ctx := testkit.Ctx(tenantID)
	n := newEdge(t, s, ctx, "Plant 1", "c-1")
	if _, err := s.CurrentEdgeConfig(ctx, n.ID, sub(n)); problemCode(err) != CodeEdgeConfigNotFound {
		t.Fatalf("pull before a publish: %v", err)
	}
	v1, err := s.PublishEdgeConfig(ctx, n.ID, edgeDoc("M1"), "admin-1", idem("p-1"))
	if err != nil || v1.Version != 1 || v1.AppliedAt != nil {
		t.Fatalf("v1 = %+v, %v", v1, err)
	}
	var doc map[string]json.RawMessage
	if err := json.Unmarshal(v1.Document, &doc); err != nil || string(doc["version"]) != "1" {
		t.Fatalf("document = %s", v1.Document)
	}
	v2, err := s.PublishEdgeConfig(ctx, n.ID, edgeDoc("M1"), "admin-1", idem("p-2"))
	var doc2 map[string]json.RawMessage
	if err != nil || v2.Version != 2 || json.Unmarshal(v2.Document, &doc2) != nil || string(doc2["version"]) != "2" {
		t.Fatalf("v2 = %+v, %v", v2, err)
	}
	if _, err := s.PublishEdgeConfig(ctx, n.ID, json.RawMessage(`{"tags":[{"address":"a","machineId":"zz","kind":"count"}]}`), "admin-1", idem("p-3")); err == nil {
		t.Fatal("an invalid document was stored")
	}
	if _, err := s.PublishEdgeConfig(ctx, ids.New(), edgeDoc("M1"), "admin-1", idem("p-4")); problemCode(err) != CodeEdgeNotFound {
		t.Fatalf("unknown node: %v", err)
	}
	cur, err := s.CurrentEdgeConfig(ctx, n.ID, sub(n))
	if err != nil || cur.Version != 2 {
		t.Fatalf("current = %+v, %v", cur, err)
	}
	list, err := s.ListEdgeConfigs(ctx, n.ID, 10)
	if err != nil || len(list) != 2 || list[0].Version != 2 {
		t.Fatalf("list = %+v, %v", list, err)
	}
	if _, err := s.ListEdgeConfigs(ctx, ids.New(), 10); problemCode(err) != CodeEdgeNotFound {
		t.Fatalf("list of unknown node: %v", err)
	}
	got, _ := s.GetEdgeNode(ctx, n.ID)
	if got.LatestConfigVersion == nil || *got.LatestConfigVersion != 2 || got.AppliedConfigVersion != nil {
		t.Fatalf("node = %+v", got)
	}
	if n := countEvents(t, app, tenantID, sfevents.EdgeConfigPublished); n != 2 {
		t.Fatalf("edgeconfig.published events = %d, want 2", n)
	}
	// Another tenant's node has none of these versions.
	if _, err := s.CurrentEdgeConfig(testkit.Ctx(testkit.TenantID(54)), n.ID, sub(n)); problemCode(err) != CodeEdgeNotFound {
		t.Fatalf("other tenant: %v", err)
	}
}

func TestEdgeHeartbeat_AppliesOneVersionAtATime(t *testing.T) {
	s, app, _ := setup(t)
	tenantID := testkit.TenantID(55)
	ctx := testkit.Ctx(tenantID)
	n := newEdge(t, s, ctx, "Plant 1", "h-1")
	if _, err := s.Heartbeat(ctx, n.ID, sub(n), HeartbeatInput{BufferFillPct: decimal.Zero}); err != nil {
		t.Fatalf("first heartbeat without a config: %v", err)
	}
	got, _ := s.GetEdgeNode(ctx, n.ID)
	if got.Status != domain.EdgeOnline || got.LastHeartbeatAt == nil || got.BufferFillPct == nil {
		t.Fatalf("after heartbeat = %+v", got)
	}
	three := 3
	if _, err := s.Heartbeat(ctx, n.ID, sub(n), HeartbeatInput{BufferFillPct: decimal.Zero, AppliedConfigVersion: &three}); problemCode(err) != CodeEdgeConfigNotFound {
		t.Fatalf("unpublished version: %v", err)
	}
	for _, k := range []string{"a", "b"} {
		if _, err := s.PublishEdgeConfig(ctx, n.ID, edgeDoc("M1"), "admin-1", idem("hp-"+k)); err != nil {
			t.Fatal(err)
		}
	}
	one, two := 1, 2
	cv := "1.4.2"
	exp := time.Now().Add(90 * 24 * time.Hour).UTC().Truncate(time.Second)
	serial := "ab:cd"
	res, err := s.Heartbeat(ctx, n.ID, sub(n), HeartbeatInput{ConnectorVersion: &cv, BufferFillPct: decimal.RequireFromString("12.5"),
		Machines: []domain.MachineStatus{{MachineCode: "M1", State: "RUNNING"}}, AppliedConfigVersion: &one, CertSerial: &serial, CertExpiresAt: &exp})
	if err != nil || res.AppliedConfigVersion == nil || *res.AppliedConfigVersion != 1 || *res.LatestConfigVersion != 2 || res.HeartbeatIntervalSeconds != 30 {
		t.Fatalf("heartbeat = %+v, %v", res, err)
	}
	// A repeated report of the same version applies nothing new.
	if _, err := s.Heartbeat(ctx, n.ID, sub(n), HeartbeatInput{BufferFillPct: decimal.RequireFromString("13"), AppliedConfigVersion: &one}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Heartbeat(ctx, n.ID, sub(n), HeartbeatInput{BufferFillPct: decimal.RequireFromString("14"), AppliedConfigVersion: &two}); err != nil {
		t.Fatal(err)
	}
	// A heartbeat without a version keeps the applied one, the certificate and the connector version.
	if _, err := s.Heartbeat(ctx, n.ID, sub(n), HeartbeatInput{BufferFillPct: decimal.RequireFromString("15")}); err != nil {
		t.Fatal(err)
	}
	got, _ = s.GetEdgeNode(ctx, n.ID)
	if got.AppliedConfigVersion == nil || *got.AppliedConfigVersion != 2 || got.ConnectorVersion == nil || *got.ConnectorVersion != "1.4.2" ||
		got.CertSerial == nil || *got.CertSerial != serial || got.CertExpiresAt == nil || !got.CertExpiresAt.Equal(exp) || got.BufferFillPct.String() != "15" || got.Version != 1 {
		t.Fatalf("node = %+v", got)
	}
	list, _ := s.ListEdgeConfigs(ctx, n.ID, 10)
	if list[0].AppliedAt == nil || list[1].AppliedAt == nil {
		t.Fatalf("applied_at missing: %+v", list)
	}
	// 1 -> 2 are the only changes; the repeat and the version-less heartbeat publish nothing.
	if n := countEvents(t, app, tenantID, sfevents.EdgeConfigApplied); n != 2 {
		t.Fatalf("edgeconfig.applied events = %d, want 2", n)
	}
	// Going back to an older published version is a change too (a rolled-back connector).
	if _, err := s.Heartbeat(ctx, n.ID, sub(n), HeartbeatInput{BufferFillPct: decimal.Zero, AppliedConfigVersion: &one}); err != nil {
		t.Fatal(err)
	}
	if n := countEvents(t, app, tenantID, sfevents.EdgeConfigApplied); n != 3 {
		t.Fatalf("after rollback, edgeconfig.applied events = %d, want 3", n)
	}
}

func TestEdgeHeartbeat_InactiveAndUnknown(t *testing.T) {
	s, _, _ := setup(t)
	ctx := testkit.Ctx(testkit.TenantID(56))
	n := newEdge(t, s, ctx, "Plant 1", "i-1")
	if _, err := s.PublishEdgeConfig(ctx, n.ID, edgeDoc("M1"), "admin-1", idem("i-p")); err != nil {
		t.Fatal(err)
	}
	if _, err := s.UpdateEdgeNode(ctx, n.ID, 1, EdgeNodeInput{Name: "Plant 1", Ring: "canary", ConnectorSubject: n.ConnectorSubject, Active: false}, "admin-1"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Heartbeat(ctx, n.ID, sub(n), HeartbeatInput{BufferFillPct: decimal.Zero}); problemCode(err) != CodeEdgeInactive {
		t.Fatalf("heartbeat of a switched-off node: %v", err)
	}
	if _, err := s.CurrentEdgeConfig(ctx, n.ID, sub(n)); problemCode(err) != CodeEdgeInactive {
		t.Fatalf("pull of a switched-off node: %v", err)
	}
	if l, _ := s.ListEdgeNodes(ctx, "", "inactive", 10); len(l) != 1 || l[0].Status != domain.EdgeInactive {
		t.Fatalf("inactive filter = %+v", l)
	}
	if _, err := s.Heartbeat(ctx, ids.New(), "conn-i-1", HeartbeatInput{BufferFillPct: decimal.Zero}); problemCode(err) != CodeEdgeNotFound {
		t.Fatalf("unknown node: %v", err)
	}
}

func TestEdgeStatus_OfflineAfterTheWindow(t *testing.T) {
	s, app, _ := setup(t)
	tenantID := testkit.TenantID(57)
	ctx := testkit.Ctx(tenantID)
	n := newEdge(t, s, ctx, "Plant 1", "o-1")
	if _, err := s.Heartbeat(ctx, n.ID, sub(n), HeartbeatInput{BufferFillPct: decimal.Zero}); err != nil {
		t.Fatal(err)
	}
	inTenant(t, app, tenantID, func(tx pgx.Tx) error {
		_, err := tx.Exec(context.Background(), `update edge_node set last_heartbeat_at = now() - interval '91 seconds' where id = $1`, n.ID)
		return err
	})
	got, _ := s.GetEdgeNode(ctx, n.ID)
	if got.Status != domain.EdgeOffline {
		t.Fatalf("status = %s, want offline", got.Status)
	}
	if l, _ := s.ListEdgeNodes(ctx, "", "offline", 10); len(l) != 1 {
		t.Fatalf("offline filter = %+v", l)
	}
	if l, _ := s.ListEdgeNodes(ctx, "", "online", 10); len(l) != 0 {
		t.Fatalf("online filter = %+v", l)
	}
}

// Only the connector bound to a node may pull its configuration (with the write allowlist) or
// report for it: an unbound node, another node's connector and an anonymous caller are refused
// with SF-0154 before anything is read or written, and one identity serves one node.
func TestEdgeConnector_BoundIdentityOnly(t *testing.T) {
	s, app, _ := setup(t)
	tenantID := testkit.TenantID(58)
	ctx := testkit.Ctx(tenantID)
	a := newEdge(t, s, ctx, "Plant A", "a")
	b := newEdge(t, s, ctx, "Plant B", "b")
	unbound, err := s.CreateEdgeNode(ctx, EdgeNodeInput{Name: "Plant C", Platform: domain.PlatformLinux, Ring: domain.RingAll, Active: true}, "admin-1", idem("c"))
	if err != nil || unbound.ConnectorSubject != nil {
		t.Fatalf("unbound = %+v, %v", unbound, err)
	}
	if _, err := s.PublishEdgeConfig(ctx, a.ID, json.RawMessage(`{"writeRules":[{"node":"ns=2;s=SP","min":0,"max":90}]}`), "admin-1", idem("a-p")); err != nil {
		t.Fatal(err)
	}
	one := 1
	for name, c := range map[string]struct{ node, subject string }{
		"another node's connector": {a.ID, sub(b)},
		"no identity":              {a.ID, ""},
		"unbound node":             {unbound.ID, sub(a)},
	} {
		if _, err := s.CurrentEdgeConfig(ctx, c.node, c.subject); problemCode(err) != CodeEdgeNotConnector {
			t.Errorf("pull, %s: %v", name, err)
		}
		if _, err := s.Heartbeat(ctx, c.node, c.subject, HeartbeatInput{BufferFillPct: decimal.Zero, AppliedConfigVersion: &one}); problemCode(err) != CodeEdgeNotConnector {
			t.Errorf("heartbeat, %s: %v", name, err)
		}
	}
	got, _ := s.GetEdgeNode(ctx, a.ID)
	if got.LastHeartbeatAt != nil || got.AppliedConfigVersion != nil || countEvents(t, app, tenantID, sfevents.EdgeConfigApplied) != 0 {
		t.Fatalf("a refused heartbeat changed the node: %+v", got)
	}
	if _, err := s.CreateEdgeNode(ctx, EdgeNodeInput{Name: "Plant D", Platform: domain.PlatformLinux, Ring: domain.RingAll, ConnectorSubject: a.ConnectorSubject, Active: true}, "admin-1", idem("d")); problemCode(err) != CodeEdgeNameTaken {
		t.Fatalf("one identity bound to two nodes: %v", err)
	}
	// Rebinding moves the right: the old identity is refused, the new one is served.
	moved := "conn-a2"
	if _, err := s.UpdateEdgeNode(ctx, a.ID, 1, EdgeNodeInput{Name: "Plant A", Ring: domain.RingCanary, ConnectorSubject: &moved, Active: true}, "admin-2"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.CurrentEdgeConfig(ctx, a.ID, sub(a)); problemCode(err) != CodeEdgeNotConnector {
		t.Fatalf("old identity after rebinding: %v", err)
	}
	if c, err := s.CurrentEdgeConfig(ctx, a.ID, moved); err != nil || c.Version != 1 {
		t.Fatalf("new identity: %+v, %v", c, err)
	}
}

// A published version cannot be changed or removed by the application role: only the first
// applied_at stamp is allowed, and the publish event carries the document hash and allowlist.
func TestEdgeConfig_PublishedVersionIsImmutable(t *testing.T) {
	s, app, admin := setup(t)
	tenantID := testkit.TenantID(59)
	ctx := testkit.Ctx(tenantID)
	n := newEdge(t, s, ctx, "Plant 1", "m")
	v, err := s.PublishEdgeConfig(ctx, n.ID, json.RawMessage(`{"writeRules":[{"node":"ns=2;s=SP","min":0,"max":90},{"node":"ns=2;s=Valve","min":0,"max":1}]}`), "admin-1", idem("m-p"))
	if err != nil || len(v.DocumentSHA256) != 64 {
		t.Fatalf("publish = %+v, %v", v, err)
	}
	var data sfevents.EdgeConfigPublishedData
	inTenant(t, app, tenantID, func(tx pgx.Tx) error {
		var raw []byte
		if err := tx.QueryRow(context.Background(), `select payload from outbox where type = $1`, sfevents.EdgeConfigPublished).Scan(&raw); err != nil {
			return err
		}
		var env struct {
			Data sfevents.EdgeConfigPublishedData `json:"data"`
		}
		err := json.Unmarshal(raw, &env)
		data = env.Data
		return err
	})
	if data.DocumentSHA256 != v.DocumentSHA256 || data.PublishedBy != "admin-1" || len(data.WriteRuleNodes) != 2 || data.WriteRuleNodes[1] != "ns=2;s=Valve" {
		t.Fatalf("published event = %+v", data)
	}
	ctxBG := context.Background()
	for name, stmt := range map[string]string{
		"document":     `update edge_config_version set document = '{"writeRules":[{"node":"ns=2;s=Any","min":-1e9,"max":1e9}]}' where id = $1`,
		"version":      `update edge_config_version set version = 9 where id = $1`,
		"published by": `update edge_config_version set published_by = 'someone' where id = $1`,
		"delete":       `delete from edge_config_version where id = $1`,
	} {
		tx, err := app.Begin(ctxBG)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := tx.Exec(ctxBG, `select set_config('passage.tenant_id', $1, true)`, tenantID); err != nil {
			t.Fatal(err)
		}
		if _, err := tx.Exec(ctxBG, stmt, v.ID); err == nil {
			t.Errorf("%s: the application role changed a published version", name)
		}
		_ = tx.Rollback(ctxBG)
	}
	// The trigger holds even for the table owner, which column grants do not restrict.
	if _, err := admin.Exec(ctxBG, `update edge_config_version set document = '{}' where id = $1`, v.ID); err == nil {
		t.Fatal("the owner changed a published document")
	}
	one := 1
	if _, err := s.Heartbeat(ctx, n.ID, sub(n), HeartbeatInput{BufferFillPct: decimal.Zero, AppliedConfigVersion: &one}); err != nil {
		t.Fatal(err)
	}
	tx, err := app.Begin(ctxBG)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback(ctxBG) }()
	if _, err := tx.Exec(ctxBG, `select set_config('passage.tenant_id', $1, true)`, tenantID); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(ctxBG, `update edge_config_version set applied_at = now() + interval '1 day' where id = $1`, v.ID); err == nil {
		t.Fatal("applied_at was stamped twice")
	}
}

// The gateway forwards a connector certificate that names a node and no tenant; the service finds the node's
// tenant with the definer function, for the one node asked about and for any caller, and finds nothing for an
// id that is no node.
func TestEdgeNodeTenantFindsTheTenantOfANode(t *testing.T) {
	s, _, _ := setup(t)
	a, b := testkit.TenantID(61), testkit.TenantID(62)
	na := newEdge(t, s, testkit.Ctx(a), "Plant A", "a-1")
	nb := newEdge(t, s, testkit.Ctx(b), "Plant B", "b-1")
	for id, want := range map[string]string{na.ID: a, nb.ID: b} {
		got, err := s.EdgeNodeTenant(context.Background(), id)
		if err != nil || got != want {
			t.Fatalf("node %s: tenant %q err %v, want %q", id, got, err, want)
		}
	}
	if got, err := s.EdgeNodeTenant(context.Background(), ids.New()); err != nil || got != "" {
		t.Fatalf("unknown node: %q %v", got, err)
	}
}
