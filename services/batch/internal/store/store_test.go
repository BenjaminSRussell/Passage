package store_test

import (
	"context"
	"encoding/json"
	"reflect"
	"testing"

	"github.com/BenjaminSRussell/passage/platform/dbx"
	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/platform/tenant"
	"github.com/BenjaminSRussell/passage/platform/testkit"
	"github.com/BenjaminSRussell/passage/services/batch/internal/domain"
	"github.com/BenjaminSRussell/passage/services/batch/internal/schema"
	"github.com/BenjaminSRussell/passage/services/batch/internal/store"
	"github.com/BenjaminSRussell/passage/services/batch/migrations"
)

func principal(n int) tenant.Principal {
	return tenant.Principal{
		TenantID:      testkit.TenantID(n),
		LegalEntityID: testkit.TenantID(9),
		UserID:        testkit.TenantID(8),
	}
}

func migrated(t *testing.T) (*store.Postgres, context.Context) {
	t.Helper()
	pool := testkit.DB(t)
	ctx := tenant.With(context.Background(), principal(1))
	if err := dbx.Migrate(ctx, pool, migrations.FS); err != nil {
		t.Fatal(err)
	}
	return store.New(pool), ctx
}

func seedDefinition(t *testing.T, s *store.Postgres, ctx context.Context) {
	t.Helper()
	_, err := s.SaveDefinition(ctx, store.Definition{
		Entity:        "purchaserequisition",
		LegalEntityID: testkit.TenantID(9),
		Version:       1,
		Status:        "active",
		AdminUserID:   testkit.TenantID(7),
		Steps: []domain.Step{{
			Index: 1, AssigneeType: domain.AssigneeUser, Assignee: testkit.TenantID(6),
			EscalationAction: domain.EscalateDelegate, EscalationTarget: testkit.TenantID(7),
		}},
	})
	if err != nil {
		t.Fatal(err)
	}
}

func TestSubmitActRecallAndTenantIsolation(t *testing.T) {
	s, ctx := migrated(t)
	seedDefinition(t, s, ctx)
	inst, err := s.Submit(ctx, store.Submit{
		Entity: "purchaserequisition", RecordID: testkit.TenantID(11), LegalEntityID: testkit.TenantID(9),
		Assignees: []string{testkit.TenantID(6)},
	})
	if err != nil || inst.Status != domain.InstanceSubmitted {
		t.Fatalf("%+v %v", inst, err)
	}
	items, err := s.ListWorkItems(ctx, testkit.TenantID(6), domain.ItemPending)
	if err != nil || len(items) != 1 {
		t.Fatalf("%v %v", items, err)
	}
	_, err = s.Act(ctx, items[0].ID, domain.Action{Kind: "recall", Actor: testkit.TenantID(8)})
	if err != nil {
		t.Fatal(err)
	}
	got, err := s.GetInstance(ctx, inst.ID)
	if err != nil || got.Status != domain.InstanceRecalled {
		t.Fatalf("%+v %v", got, err)
	}

	inst2, err := s.Submit(ctx, store.Submit{
		Entity: "purchaserequisition", RecordID: testkit.TenantID(12), LegalEntityID: testkit.TenantID(9),
		Assignees: []string{testkit.TenantID(6)},
	})
	if err != nil {
		t.Fatal(err)
	}
	items, err = s.ListWorkItems(ctx, testkit.TenantID(6), domain.ItemPending)
	if err != nil || len(items) == 0 {
		t.Fatal(err)
	}
	if _, err := s.Act(ctx, items[0].ID, domain.Action{Kind: "approve", Actor: testkit.TenantID(6)}); err != nil {
		t.Fatal(err)
	}
	_, err = s.Act(ctx, items[0].ID, domain.Action{Kind: "recall", Actor: testkit.TenantID(8)})
	if err == nil {
		t.Fatal("recall after approval must fail")
	}
	var p *httpx.Problem
	if !asProblem(err, &p) || p.Code != domain.CodeRecallNotAllowed {
		t.Fatalf("err = %v", err)
	}
	_ = inst2

	other := tenant.With(context.Background(), principal(2))
	if _, err := s.GetInstance(other, inst.ID); err == nil {
		t.Fatal("tenant 2 must not see tenant 1 instance")
	}
}

func TestExclusiveJobOverlapAndSchemaGate(t *testing.T) {
	s, ctx := migrated(t)
	job, err := s.SaveJob(ctx, store.Job{Code: "rows-1m", Handler: "synthetic.rows", PartitionCount: 16, Concurrency: 4, Exclusive: true})
	if err != nil || job.Code != "rows-1m" {
		t.Fatalf("%+v %v", job, err)
	}
	if _, err := s.StartRun(ctx, "rows-1m", 1_000_000, testkit.TenantID(8), nil); err != nil {
		t.Fatal(err)
	}
	_, err = s.StartRun(ctx, "rows-1m", 1_000_000, testkit.TenantID(8), nil)
	if err == nil {
		t.Fatal("expected overlap")
	}
	var p *httpx.Problem
	if !asProblem(err, &p) || p.Code != domain.CodeRunOverlap {
		t.Fatalf("err = %v", err)
	}

	current := schema.JSONSchema{Type: "object", Required: []string{"id"}, Properties: map[string]schema.JSONSchema{"id": {Type: "string"}}}
	if err := s.RegisterSchema(ctx, "dev.passage.workflow.instance.submitted.v1", current); err != nil {
		t.Fatal(err)
	}
	breaking := current
	breaking.Required = []string{"id", "amount"}
	if err := s.RegisterSchema(ctx, "dev.passage.workflow.instance.submitted.v1", breaking); err == nil {
		t.Fatal("breaking schema must be rejected")
	}
}

func TestNotifyTemplateAndMessages(t *testing.T) {
	s, ctx := migrated(t)
	allowed := map[string]bool{"record.number": true, "link": true}
	if _, err := s.PublishTemplate(ctx, store.Template{
		Code: "workflow.workitem.assigned", Channel: domain.ChannelEmail, Language: "en",
		Subject: "Approve {{record.number}}", Body: "Open {{link}}",
	}, allowed); err != nil {
		t.Fatal(err)
	}
	_, err := s.PublishTemplate(ctx, store.Template{
		Code: "workflow.workitem.assigned", Channel: domain.ChannelEmail, Language: "de-DE",
		Subject: "x", Body: "{{record.nonexistent}}",
	}, allowed)
	if err == nil {
		t.Fatal("unknown placeholder must fail")
	}
	msg, err := s.EnqueueMessage(ctx, store.Message{Channel: domain.ChannelInApp, RecipientUserID: testkit.TenantID(8), Subject: "Hi", Body: "body"})
	if err != nil || msg.Status != domain.MsgQueued {
		t.Fatalf("%+v %v", msg, err)
	}
	if _, err := s.PutPreference(ctx, store.Preference{EventClass: "workflow_assigned", Channels: []string{domain.ChannelInApp, domain.ChannelEmail}, Digest: domain.DigestDaily}); err != nil {
		t.Fatal(err)
	}
}

func TestMigrationsApplyWithRLS(t *testing.T) {
	admin, pool := testkit.DBWithApp(t)
	ctx := tenant.With(context.Background(), principal(1))
	if err := dbx.Migrate(ctx, admin, migrations.FS); err != nil {
		t.Fatal(err)
	}
	var currentUser string
	var roleSuper, roleBypass bool
	if err := pool.QueryRow(ctx, `select current_user, r.rolsuper, r.rolbypassrls from pg_roles r where r.rolname = current_user`).Scan(&currentUser, &roleSuper, &roleBypass); err != nil || currentUser != "passage_app" || roleSuper || roleBypass {
		t.Fatalf("RLS test must use passage_app: user=%q super=%v bypass=%v", currentUser, roleSuper, roleBypass)
	}
	for _, table := range []string{"workflow_instance", "batch_job", "notify_outbound_message", "outbox"} {
		var enabled, forced bool
		err := pool.QueryRow(ctx, `select relrowsecurity, relforcerowsecurity from pg_class where relname = $1 and relnamespace = current_schema()::regnamespace`, table).Scan(&enabled, &forced)
		if err != nil || !enabled || !forced {
			t.Errorf("%s: rls enabled=%v forced=%v err=%v", table, enabled, forced, err)
		}
	}
	app := store.New(pool)
	seedDefinition(t, app, ctx)
	if _, err := app.Submit(ctx, store.Submit{Entity: "purchaserequisition", RecordID: testkit.TenantID(21), LegalEntityID: testkit.TenantID(9), Assignees: []string{testkit.TenantID(6)}}); err != nil {
		t.Fatal(err)
	}
	other := tenant.With(context.Background(), principal(2))
	items, err := app.ListWorkItems(other, "", "")
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 0 {
		t.Fatalf("tenant 2 saw %d items", len(items))
	}
}

func asProblem(err error, dest **httpx.Problem) bool {
	var p *httpx.Problem
	ok := err != nil
	if ok {
		p, ok = err.(*httpx.Problem)
		*dest = p
	}
	return ok
}

func TestStartRunPersistsValidatedParamsAndTheCreatorsScope(t *testing.T) {
	admin, app := testkit.DBWithApp(t)
	ctx := tenant.With(context.Background(), principal(1))
	if err := dbx.Migrate(ctx, admin, migrations.FS); err != nil {
		t.Fatal(err)
	}
	s := store.New(app)
	for code, handler := range map[string]string{"explode": domain.HandlerProductExplode, "group": domain.HandlerProductGroupChange, "synthetic": "synthetic.rows"} {
		if _, err := s.SaveJob(ctx, store.Job{Code: code, Handler: handler}); err != nil {
			t.Fatal(err)
		}
	}
	master := testkit.TenantID(50)
	good := json.RawMessage(`{"masterId":"` + master + `","selected":{"size":["S","M"]}}`)

	run, err := s.StartRun(ctx, "explode", 0, testkit.TenantID(8), good)
	if err != nil {
		t.Fatal(err)
	}
	var params, le, by string
	if err := admin.QueryRow(context.Background(), `select params::text, legal_entity_id::text, triggered_by::text from batch_run where id = $1`, run.ID).Scan(&params, &le, &by); err != nil {
		t.Fatal(err)
	}
	var stored, wanted any
	if err := json.Unmarshal([]byte(params), &stored); err != nil {
		t.Fatal(err)
	}
	_ = json.Unmarshal(good, &wanted)
	if !reflect.DeepEqual(stored, wanted) || le != testkit.TenantID(9) || by != testkit.TenantID(8) {
		t.Fatalf("stored params=%s le=%s by=%s", params, le, by)
	}

	for _, ok := range []struct {
		code   string
		params json.RawMessage
	}{{"group", nil}, {"group", json.RawMessage(`{}`)}, {"group", json.RawMessage(`null`)}, {"synthetic", nil}} {
		if _, err := s.StartRun(ctx, ok.code, 0, testkit.TenantID(8), ok.params); err != nil {
			t.Errorf("%s with %s: %v", ok.code, ok.params, err)
		}
	}

	for name, bad := range map[string]struct {
		code   string
		params json.RawMessage
		field  string
	}{
		"explode without params":   {"explode", nil, "params"},
		"explode bad master":       {"explode", json.RawMessage(`{"masterId":"nope"}`), "params.masterId"},
		"explode unknown key":      {"explode", json.RawMessage(`{"masterId":"` + master + `","extra":1}`), "params"},
		"explode empty axis":       {"explode", json.RawMessage(`{"masterId":"` + master + `","selected":{"size":[]}}`), "params.selected"},
		"explode not an object":    {"explode", json.RawMessage(`[1]`), "params"},
		"group-change with params": {"group", json.RawMessage(`{"masterId":"` + master + `"}`), "params"},
		"handler without a schema": {"synthetic", json.RawMessage(`{"a":1}`), "params"},
	} {
		_, err := s.StartRun(ctx, bad.code, 0, testkit.TenantID(8), bad.params)
		var p *httpx.Problem
		if !asProblem(err, &p) || p.Status != 400 || p.Code != domain.CodeInvalid || p.Errors[bad.field] == "" {
			t.Errorf("%s: err = %v", name, err)
		}
	}

	multi := principal(1)
	multi.LegalEntityID = testkit.TenantID(9) + "," + testkit.TenantID(10)
	noLE := principal(1)
	noLE.LegalEntityID = ""
	for name, caller := range map[string]tenant.Principal{"several entities": multi, "no entity": noLE} {
		_, err := s.StartRun(tenant.With(context.Background(), caller), "group", 0, testkit.TenantID(8), nil)
		var p *httpx.Problem
		if !asProblem(err, &p) || p.Status != 400 || p.Errors["legalEntity"] == "" {
			t.Errorf("%s: a product run must be bound to one legal entity, err = %v", name, err)
		}
	}
	if _, err := s.StartRun(ctx, "group", 0, "", nil); err == nil {
		t.Error("a product run needs the creating user")
	}
	if _, err := s.StartRun(tenant.With(context.Background(), multi), "synthetic", 0, testkit.TenantID(8), nil); err != nil {
		t.Errorf("a non-product run needs no single legal entity: %v", err)
	}
}
