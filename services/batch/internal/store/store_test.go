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
	"github.com/jackc/pgx/v5"
)

func principal(n int) tenant.Principal {
	return tenant.Principal{
		TenantID:      testkit.TenantID(n),
		LegalEntityID: testkit.TenantID(9),
		UserID:        testkit.TenantID(8),
		Duties:        map[string]struct{}{"product.write": {}, "product.read": {}, "product.jobs.write": {}},
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
	if _, err := s.StartRun(ctx, "rows-1m", store.RunInput{Rows: 1_000_000, TriggeredBy: testkit.TenantID(8)}); err != nil {
		t.Fatal(err)
	}
	_, err = s.StartRun(ctx, "rows-1m", store.RunInput{Rows: 1_000_000, TriggeredBy: testkit.TenantID(8)})
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
	by := testkit.TenantID(8)

	run, err := s.StartRun(ctx, "explode", store.RunInput{TriggeredBy: by, Params: good})
	if err != nil {
		t.Fatal(err)
	}
	var params, le, creator string
	var duties []string
	if err := admin.QueryRow(context.Background(), `select params::text, legal_entity_id::text, triggered_by::text, service_duties from batch_run where id = $1`, run.ID).Scan(&params, &le, &creator, &duties); err != nil {
		t.Fatal(err)
	}
	var stored, wanted any
	if err := json.Unmarshal([]byte(params), &stored); err != nil {
		t.Fatal(err)
	}
	_ = json.Unmarshal(good, &wanted)
	if !reflect.DeepEqual(stored, wanted) || le != testkit.TenantID(9) || creator != by || !reflect.DeepEqual(duties, []string{"product.variants.explode"}) {
		t.Fatalf("stored params=%s le=%s by=%s duties=%v", params, le, creator, duties)
	}

	for _, ok := range []struct {
		code   string
		params json.RawMessage
	}{{"group", nil}, {"group", json.RawMessage(`{}`)}, {"group", json.RawMessage(`null`)}, {"synthetic", nil}} {
		if _, err := s.StartRun(ctx, ok.code, store.RunInput{TriggeredBy: by, Params: ok.params}); err != nil {
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
		"explode trailing json":    {"explode", json.RawMessage(`{"masterId":"` + master + `"} {"x":1}`), "params"},
		"explode empty axis":       {"explode", json.RawMessage(`{"masterId":"` + master + `","selected":{"size":[]}}`), "params.selected"},
		"explode not an object":    {"explode", json.RawMessage(`[1]`), "params"},
		"group-change with params": {"group", json.RawMessage(`{"masterId":"` + master + `"}`), "params"},
		"handler without a schema": {"synthetic", json.RawMessage(`{"a":1}`), "params"},
	} {
		_, err := s.StartRun(ctx, bad.code, store.RunInput{TriggeredBy: by, Params: bad.params})
		var p *httpx.Problem
		if !asProblem(err, &p) || p.Status != 400 || p.Code != domain.CodeInvalid || p.Errors[bad.field] == "" {
			t.Errorf("%s: err = %v", name, err)
		}
	}
}

func TestProductRunsRequireTheCreatorToHoldTheProductDuties(t *testing.T) {
	admin, app := testkit.DBWithApp(t)
	ctx := tenant.With(context.Background(), principal(1))
	if err := dbx.Migrate(ctx, admin, migrations.FS); err != nil {
		t.Fatal(err)
	}
	s := store.New(app)
	for code, handler := range map[string]string{"explode": domain.HandlerProductExplode, "group": domain.HandlerProductGroupChange} {
		if _, err := s.SaveJob(ctx, store.Job{Code: code, Handler: handler}); err != nil {
			t.Fatal(err)
		}
	}
	with := func(duties ...string) context.Context {
		p := principal(1)
		p.Duties = map[string]struct{}{}
		for _, d := range duties {
			p.Duties[d] = struct{}{}
		}
		return tenant.With(context.Background(), p)
	}
	params := json.RawMessage(`{"masterId":"` + testkit.TenantID(50) + `"}`)
	by := testkit.TenantID(8)
	cases := map[string]struct {
		ctx  context.Context
		code string
		ok   bool
		want []string
	}{
		"batch.write only, explode":      {with("batch.write"), "explode", false, nil},
		"batch.write only, group-change": {with("batch.write"), "group", false, nil},
		"read without jobs.write":        {with("product.read"), "group", false, nil},
		"jobs.write without read":        {with("product.jobs.write"), "group", false, nil},
		"scoped explode duty":            {with("product.variants.explode"), "explode", true, []string{"product.variants.explode"}},
		"broad product.write":            {with("product.write"), "explode", true, []string{"product.variants.explode"}},
		"scoped group duties":            {with("product.released.read", "product.jobs.write"), "group", true, []string{"product.released.read", "product.jobs.write"}},
	}
	for name, c := range cases {
		in := store.RunInput{TriggeredBy: by}
		if c.code == "explode" {
			in.Params = params
		}
		run, err := s.StartRun(c.ctx, c.code, in)
		var p *httpx.Problem
		if !c.ok {
			if !asProblem(err, &p) || p.Status != 403 {
				t.Errorf("%s: err = %v; want 403", name, err)
			}
			continue
		}
		if err != nil {
			t.Errorf("%s: %v", name, err)
			continue
		}
		var duties []string
		if err := admin.QueryRow(context.Background(), `select service_duties from batch_run where id = $1`, run.ID).Scan(&duties); err != nil || !reflect.DeepEqual(duties, c.want) {
			t.Errorf("%s: stored duties = %v, %v; want %v", name, duties, err, c.want)
		}
	}
	if _, err := s.SaveJob(with("batch.write"), store.Job{Code: "sneaky", Handler: domain.HandlerProductGroupChange}); err == nil {
		t.Error("saving a product job needs the product duties too")
	}
	if _, err := s.SaveJob(with("batch.write"), store.Job{Code: "plain", Handler: "synthetic.rows"}); err != nil {
		t.Errorf("a plain job needs no product duties: %v", err)
	}
}

func TestRunsAreAlwaysBoundToOneLegalEntity(t *testing.T) {
	admin, app := testkit.DBWithApp(t)
	ctx := tenant.With(context.Background(), principal(1))
	if err := dbx.Migrate(ctx, admin, migrations.FS); err != nil {
		t.Fatal(err)
	}
	s := store.New(app)
	if _, err := s.SaveJob(ctx, store.Job{Code: "synthetic", Handler: "synthetic.rows"}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.SaveJob(ctx, store.Job{Code: "group", Handler: domain.HandlerProductGroupChange}); err != nil {
		t.Fatal(err)
	}
	other := testkit.TenantID(10)
	caller := func(entities string, duties ...string) context.Context {
		p := principal(1)
		p.LegalEntityID = entities
		p.Duties = map[string]struct{}{"product.read": {}, "product.jobs.write": {}}
		for _, d := range duties {
			p.Duties[d] = struct{}{}
		}
		return tenant.With(context.Background(), p)
	}
	by := testkit.TenantID(8)
	multi := testkit.TenantID(9) + "," + other
	cases := map[string]struct {
		ctx      context.Context
		code     string
		explicit string
		wantLE   string
	}{
		"several, none named":      {caller(multi), "synthetic", "", ""},
		"several, product, none":   {caller(multi), "group", "", ""},
		"no entity, none named":    {caller(""), "synthetic", "", ""},
		"all scope, none named":    {caller("", tenant.DutyAllScope), "synthetic", "", ""},
		"named outside the scope":  {caller(testkit.TenantID(9)), "synthetic", other, ""},
		"malformed":                {caller(multi), "synthetic", "nope", ""},
		"several, one named":       {caller(multi), "synthetic", other, other},
		"several, product, named":  {caller(multi), "group", other, other},
		"all scope, one named":     {caller("", tenant.DutyAllScope), "synthetic", other, other},
		"one entity, none named":   {caller(testkit.TenantID(9)), "synthetic", "", testkit.TenantID(9)},
		"one entity, itself named": {caller(testkit.TenantID(9)), "group", testkit.TenantID(9), testkit.TenantID(9)},
	}
	for name, c := range cases {
		run, err := s.StartRun(c.ctx, c.code, store.RunInput{TriggeredBy: by, LegalEntityID: c.explicit})
		if c.wantLE == "" {
			var p *httpx.Problem
			if !asProblem(err, &p) || p.Status != 422 || p.Errors["legalEntityId"] == "" {
				t.Errorf("%s: err = %v; want 422 on legalEntityId", name, err)
			}
			continue
		}
		var got *string
		if err != nil {
			t.Errorf("%s: %v", name, err)
			continue
		}
		if err := admin.QueryRow(context.Background(), `select legal_entity_id::text from batch_run where id = $1`, run.ID).Scan(&got); err != nil || got == nil || *got != c.wantLE {
			t.Errorf("%s: stored legal entity %v, %v; want %s", name, got, err, c.wantLE)
		}
	}
}

func TestBatchRunRowSecurityTreatsNullLegalEntityAsAllScopeOnly(t *testing.T) {
	admin, app := testkit.DBWithApp(t)
	ctx := tenant.With(context.Background(), principal(1))
	if err := dbx.Migrate(ctx, admin, migrations.FS); err != nil {
		t.Fatal(err)
	}
	s := store.New(app)
	if _, err := s.SaveJob(ctx, store.Job{Code: "synthetic", Handler: "synthetic.rows"}); err != nil {
		t.Fatal(err)
	}
	var jobID string
	if err := admin.QueryRow(context.Background(), `select id::text from batch_job`).Scan(&jobID); err != nil {
		t.Fatal(err)
	}
	legacy := testkit.TenantID(77)
	if _, err := admin.Exec(context.Background(), `insert into batch_run (tenant_id, id, job_id, status) values ($1, $2, $3, 'ended')`, testkit.TenantID(1), legacy, jobID); err != nil {
		t.Fatal(err)
	}
	if _, err := s.GetRun(ctx, legacy); err == nil {
		t.Fatal("a run without a legal entity must not be visible to a legal-entity-scoped caller")
	}
	all := principal(1)
	all.LegalEntityID = ""
	all.Duties = map[string]struct{}{tenant.DutyAllScope: {}}
	if _, err := s.GetRun(tenant.With(context.Background(), all), legacy); err != nil {
		t.Fatalf("all-scope caller must still see it: %v", err)
	}
	err := dbx.WithTenantTx(tenant.With(context.Background(), all), app, func(tx pgx.Tx) error {
		_, err := tx.Exec(context.Background(), `insert into batch_run (tenant_id, id, job_id, status) values ($1, $2, $3, 'executing')`, testkit.TenantID(1), testkit.TenantID(78), jobID)
		return err
	})
	if err == nil {
		t.Fatal("inserting a run without a legal entity must be refused even at all scope")
	}
}
