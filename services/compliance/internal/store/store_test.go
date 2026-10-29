package store

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/BenjaminSRussell/passage/platform/dbx"
	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/platform/tenant"
	"github.com/BenjaminSRussell/passage/platform/testkit"
	"github.com/BenjaminSRussell/passage/services/compliance/internal/domain"
	cevents "github.com/BenjaminSRussell/passage/services/compliance/internal/events"
	"github.com/BenjaminSRussell/passage/services/compliance/migrations"
)

var t0 = time.Date(2026, 11, 2, 9, 0, 0, 0, time.UTC)

// appOf connects as the application role (passage_app, NOBYPASSRLS) when
// PASSAGE_TEST_APP_DATABASE_URL is set, so the store runs on the real grants.
func appOf(t *testing.T, admin *pgxpool.Pool) *pgxpool.Pool {
	t.Helper()
	url := os.Getenv(testkit.EnvAppURL)
	if url == "" {
		return admin
	}
	var schema string
	if err := admin.QueryRow(context.Background(), "select current_schema()").Scan(&schema); err != nil {
		t.Fatal(err)
	}
	cfg, err := pgxpool.ParseConfig(url)
	if err != nil {
		t.Fatal(err)
	}
	cfg.ConnConfig.RuntimeParams["search_path"] = schema + ",public"
	app, err := pgxpool.NewWithConfig(context.Background(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(app.Close)
	return app
}

func migrated(t *testing.T) (*Postgres, *pgxpool.Pool) {
	t.Helper()
	admin := testkit.DB(t)
	if err := dbx.Migrate(context.Background(), admin, migrations.FS); err != nil {
		t.Fatal(err)
	}
	return New(appOf(t, admin)), admin
}

func as(tenantID, userID string, duties ...string) context.Context {
	p := tenant.Principal{TenantID: tenantID, UserID: userID, Duties: map[string]struct{}{}}
	for _, d := range duties {
		p.Duties[d] = struct{}{}
	}
	return tenant.With(context.Background(), p)
}

func roles(pairs ...string) []domain.Role {
	out := []domain.Role{}
	for i := 0; i < len(pairs); i += 2 {
		out = append(out, domain.Role{Name: pairs[i], Duties: []string{pairs[i+1]}})
	}
	return out
}

func outboxTypes(t *testing.T, admin *pgxpool.Pool, aggregateID string) []string {
	t.Helper()
	rows, err := admin.Query(context.Background(), `select type from outbox where aggregate_id = $1 order by created_at, id`, aggregateID)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	out := []string{}
	for rows.Next() {
		var typ string
		if err := rows.Scan(&typ); err != nil {
			t.Fatal(err)
		}
		out = append(out, typ)
	}
	return out
}

func code(err error) string {
	var p *httpx.Problem
	if errors.As(err, &p) {
		return p.Code
	}
	var de *domain.Error
	if errors.As(err, &de) {
		return de.Code
	}
	return ""
}

func TestAppRole_CrossTenantRLS(t *testing.T) {
	admin, app := testkit.DBWithApp(t)
	if err := dbx.Migrate(context.Background(), admin, migrations.FS); err != nil {
		t.Fatal(err)
	}
	testkit.AssertTenantRLS(t, admin)
	conflict := `insert into sod_conflict (tenant_id, conflict_id, user_id, rule_id, risk_id, severity, duty_a, duty_b, detected_at, updated_at)
		values ($1, $2, $3, 'P2P-001', 'P2P-001', 'HIGH', 'MaintainVendors', 'ProcessVendorPayments', now(), now())`
	testkit.CrossTenant(t, app, []testkit.RLSCase{
		testkit.IdempotencyCase(),
		{
			Table:  "sod_conflict",
			Insert: conflict,
			Args:   func(tenantID, _ string) []any { return []any{tenantID, ids.New(), ids.New()} },
		},
		{
			Table:  "sod_mitigation",
			Before: conflict,
			Insert: `insert into sod_mitigation (tenant_id, mitigation_id, conflict_id, control_ref, reason, approver_id, approved_at, expires_at)
				values ($1, $3, $2, 'CTL', 'reason', 'approver', now(), now() + interval '30 days')`,
			Args:     func(tenantID, _ string) []any { return []any{tenantID, ids.New(), ids.New()} },
			NoUpdate: true,
		},
		{
			Table: "screening_hit",
			Insert: `insert into screening_hit (tenant_id, hit_id, subject_type, subject_id, subject_name, subject_fingerprint, list_code,
				list_entry_id, list_entry_version, list_entry_name, score, matched_fields, detected_at, updated_at)
				values ($1, $2, 'PARTY', $3, 'Volga Maritime', repeat('a', 64), 'OFAC_SDN', 'SDN-1', 1, 'Volga Maritime', 0.950, '{name}', now(), now())`,
			Args: func(tenantID, _ string) []any { return []any{tenantID, ids.New(), ids.New()} },
		},
		{
			Table: "subject_request",
			Insert: `insert into subject_request (tenant_id, request_id, type, regulation, subject_ref, received_at, deadline_at, created_by, created_at, updated_at)
				values ($1, $2, 'ACCESS', 'GDPR', 'party:' || $3::text, now(), now() + interval '30 days', 'intake', now(), now())`,
			Args: func(tenantID, _ string) []any { return []any{tenantID, ids.New(), ids.New()} },
		},
	})
}

func TestGrants_AppRoleCannotRewriteAppendOnlyTables(t *testing.T) {
	if os.Getenv(testkit.EnvAppURL) == "" {
		t.Skipf("%s is needed for the grants test", testkit.EnvAppURL)
	}
	s, _ := migrated(t)
	ctx := as(ids.New(), ids.New())
	for _, stmt := range []string{
		`update sod_mitigation set reason = reason`,
		`delete from sod_mitigation`,
		`delete from sod_conflict`,
		`select count(*) from outbox`,
		`update outbox set type = type`,
		`delete from idempotency_keys`,
		`delete from screening_hit`,
		`delete from subject_request`,
	} {
		err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
			_, err := tx.Exec(ctx, stmt)
			return err
		})
		var pgErr *pgconn.PgError
		if !errors.As(err, &pgErr) || pgErr.Code != "42501" {
			t.Errorf("%q = %v; want insufficient_privilege 42501", stmt, err)
		}
	}
}

type fixture struct {
	Roles map[string][]string `json:"roles"`
	Users []struct {
		UserID string   `json:"userId"`
		Roles  []string `json:"roles"`
		Expect []string `json:"expect"`
	} `json:"users"`
}

// AC2 / AC-48.2 end to end: the 50 planted users of fx-sod-50 are evaluated
// and stored; the conflict count reconciles with an independent recomputation
// from the role assignments (PRD §9.48.9), and every conflict was emitted.
func TestEvaluate_PlantedFiftyReconcileWithRecomputation(t *testing.T) {
	s, admin := migrated(t)
	raw, err := os.ReadFile("../../testdata/sod-50.json")
	if err != nil {
		t.Fatal(err)
	}
	var fx fixture
	if err := json.Unmarshal(raw, &fx); err != nil {
		t.Fatal(err)
	}
	tenantID := ids.New()
	ctx := as(tenantID, "sod-batch")
	expected := 0
	for i, u := range fx.Users {
		userID := fmt.Sprintf("00000000-0000-0000-0000-%012d", i+1)
		var rs []domain.Role
		for _, name := range u.Roles {
			rs = append(rs, domain.Role{Name: name, Duties: fx.Roles[name]})
		}
		got, err := s.Evaluate(ctx, userID, rs, t0)
		if err != nil {
			t.Fatal(err)
		}
		if len(got) != len(u.Expect) {
			t.Errorf("%s: stored %d conflicts, want %v", u.UserID, len(got), u.Expect)
		}
		for j, c := range got {
			if j < len(u.Expect) && (c.RuleID != u.Expect[j] || c.State != domain.StateOpen || c.Version != 1) {
				t.Errorf("%s: %+v, want OPEN %s", u.UserID, c, u.Expect[j])
			}
		}
		expected += len(u.Expect)
	}
	page, err := s.List(ctx, Filter{State: domain.StateOpen}, t0)
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Items) != expected || expected < 50 {
		t.Fatalf("OPEN conflicts %d, recomputed %d", len(page.Items), expected)
	}
	var events int
	if err := admin.QueryRow(context.Background(), `select count(*) from outbox where tenant_id = $1 and type = $2`, tenantID, cevents.ConflictDetected).Scan(&events); err != nil {
		t.Fatal(err)
	}
	if events != expected {
		t.Errorf("conflict.detected events %d, want %d", events, expected)
	}
	// Another tenant sees none of it.
	other, err := s.List(as(ids.New(), "x"), Filter{}, t0)
	if err != nil || len(other.Items) != 0 {
		t.Fatalf("other tenant sees %d conflicts (%v)", len(other.Items), err)
	}
}

func TestEvaluate_IsARecomputation(t *testing.T) {
	s, admin := migrated(t)
	ctx := as(ids.New(), "role-service")
	user := ids.New()
	both := roles("Maint", "MaintainVendors", "Pay", "ProcessVendorPayments")

	first, err := s.Evaluate(ctx, user, both, t0)
	if err != nil || len(first) != 1 {
		t.Fatalf("first: %+v %v", first, err)
	}
	if first[0].RolesA[0] != "Maint" || first[0].RolesB[0] != "Pay" {
		t.Errorf("roles not recorded: %+v", first[0])
	}
	again, err := s.Evaluate(ctx, user, both, t0.Add(time.Hour))
	if err != nil || len(again) != 1 || again[0].Version != 1 || !again[0].DetectedAt.Equal(t0) {
		t.Fatalf("a repeat changed the conflict: %+v %v", again, err)
	}
	id := first[0].ConflictID

	// Removing a role resolves the conflict (BR-48.1: RESOLVED, role removed).
	gone, err := s.Evaluate(ctx, user, roles("Maint", "MaintainVendors"), t0.Add(2*time.Hour))
	if err != nil || len(gone) != 1 || gone[0].State != domain.StateResolved || gone[0].ResolvedAt == nil {
		t.Fatalf("not resolved: %+v %v", gone, err)
	}
	// Adding it back reopens the same conflict and emits again.
	back, err := s.Evaluate(ctx, user, both, t0.Add(3*time.Hour))
	if err != nil || len(back) != 1 || back[0].ConflictID != id || back[0].State != domain.StateOpen || back[0].ResolvedAt != nil {
		t.Fatalf("not reopened: %+v %v", back, err)
	}
	if got := outboxTypes(t, admin, id); len(got) != 2 || got[0] != cevents.ConflictDetected || got[1] != cevents.ConflictDetected {
		t.Errorf("outbox %v, want two conflict.detected", got)
	}
}

// AC-48.4 and AC2: a non-conflicted approver mitigates with a reason and a
// 180-day expiry; the approval is recorded, mitigation.approved is in the
// outbox, the Idempotency-Key replays, and the expiry reopens the conflict.
func TestMitigate_RecordsApprovalAndExpires(t *testing.T) {
	s, admin := migrated(t)
	tenantID, user, approver := ids.New(), ids.New(), ids.New()
	cs, err := s.Evaluate(as(tenantID, "role-service"), user, roles("Maint", "MaintainVendors", "Pay", "ProcessVendorPayments"), t0)
	if err != nil {
		t.Fatal(err)
	}
	id := cs[0].ConflictID
	in := MitigationInput{ControlRef: "CTL-DUAL-PAY", Reason: "every payment needs a second approver", ExpiresAt: t0.AddDate(0, 0, 180)}

	if _, err := s.Mitigate(as(tenantID, user), id, 1, in, 365, "k-self", t0); code(err) != domain.CodeSelfApproval {
		t.Fatalf("self approval: %v", err)
	}
	if _, err := s.Mitigate(as(tenantID, approver), id, 9, in, 365, "k-stale", t0); code(err) != CodeVersionConflict {
		t.Fatalf("stale version: %v", err)
	}
	if _, err := s.Mitigate(as(tenantID, approver), id, 1, in, 90, "k-cap", t0); code(err) != domain.CodeExpiryTooLong {
		t.Fatalf("tenant cap: %v", err)
	}
	if _, err := s.Mitigate(as(tenantID, approver), ids.New(), 1, in, 365, "k-missing", t0); code(err) != CodeNotFound {
		t.Fatalf("unknown conflict: %v", err)
	}

	got, err := s.Mitigate(as(tenantID, approver), id, 1, in, 365, "k-1", t0)
	if err != nil {
		t.Fatal(err)
	}
	if got.State != domain.StateMitigated || got.Version != 2 || got.ExpiresAt == nil || !got.ExpiresAt.Equal(in.ExpiresAt) || got.DecidedBy != approver {
		t.Fatalf("not mitigated: %+v", got.Conflict)
	}
	if len(got.Mitigations) != 1 || got.Mitigations[0].ApproverID != approver || got.Mitigations[0].ControlRef != in.ControlRef || got.MitigationID != got.Mitigations[0].MitigationID {
		t.Fatalf("approval not recorded: %+v", got.Mitigations)
	}
	replay, err := s.Mitigate(as(tenantID, approver), id, 1, in, 365, "k-1", t0)
	if err != nil || replay.Version != 2 || replay.MitigationID != got.MitigationID {
		t.Fatalf("replay: %+v %v", replay, err)
	}
	other := in
	other.Reason = "different"
	if _, err := s.Mitigate(as(tenantID, approver), id, 1, other, 365, "k-1", t0); code(err) != CodeIdempotencyReuse {
		t.Fatalf("key reuse: %v", err)
	}
	if got := outboxTypes(t, admin, id); len(got) != 2 || got[1] != cevents.MitigationApproved {
		t.Fatalf("outbox %v, want detected then mitigation.approved", got)
	}
	var payload []byte
	if err := admin.QueryRow(context.Background(), `select payload from outbox where aggregate_id = $1 and type = $2`, id, cevents.MitigationApproved).Scan(&payload); err != nil {
		t.Fatal(err)
	}
	var evt struct {
		Actor string                         `json:"actor"`
		Data  cevents.MitigationApprovedData `json:"data"`
	}
	if err := json.Unmarshal(payload, &evt); err != nil || evt.Data.Approver != approver || evt.Data.ConflictID != id || !evt.Data.ExpiresAt.Equal(in.ExpiresAt) || evt.Actor != approver {
		t.Fatalf("event payload %+v %v", evt, err)
	}

	// While mitigated, simulation does not block; after expiry it does again.
	known, err := s.KnownStates(as(tenantID, "x"), user, t0.AddDate(0, 0, 179))
	if err != nil || known["P2P-001"] != domain.StateMitigated {
		t.Fatalf("known before expiry: %v %v", known, err)
	}
	after, err := s.Get(as(tenantID, "x"), id, t0.AddDate(0, 0, 180))
	if err != nil || after.State != domain.StateOpen || after.ExpiresAt != nil || after.MitigationID != "" || after.Version != 3 {
		t.Fatalf("expiry must reopen: %+v %v", after.Conflict, err)
	}
	if len(after.Mitigations) != 1 {
		t.Errorf("the approval history must survive expiry: %+v", after.Mitigations)
	}
}

func TestAcceptRisk_OnlyLowAndMedium(t *testing.T) {
	s, _ := migrated(t)
	tenantID, user, admin := ids.New(), ids.New(), ids.New()
	cs, err := s.Evaluate(as(tenantID, "role-service"), user, roles("GR", "ReceiveGoods", "AP", "PostVendorInvoices", "Maint", "MaintainVendors", "Pay", "ProcessVendorPayments"), t0)
	if err != nil || len(cs) != 2 {
		t.Fatalf("want P2P-001 and P2P-004: %+v %v", cs, err)
	}
	high, medium := cs[0], cs[1]
	if high.RuleID != "P2P-001" || medium.RuleID != "P2P-004" {
		t.Fatalf("order: %+v", cs)
	}
	if _, err := s.AcceptRisk(as(tenantID, admin), high.ConflictID, 1, "x", t0); code(err) != domain.CodeAcceptRisk {
		t.Fatalf("HIGH accepted: %v", err)
	}
	got, err := s.AcceptRisk(as(tenantID, admin), medium.ConflictID, 1, "  monthly 3-way match review  ", t0)
	if err != nil || got.State != domain.StateAcceptedRisk || got.DecidedBy != admin || got.DecisionReason != "monthly 3-way match review" || got.Version != 2 {
		t.Fatalf("MEDIUM: %+v %v", got.Conflict, err)
	}
	if _, err := s.AcceptRisk(as(tenantID, admin), medium.ConflictID, 2, "again", t0); code(err) != domain.CodeNotOpen {
		t.Fatalf("second accept: %v", err)
	}
	// An accepted risk survives a re-evaluation that still detects it.
	again, err := s.Evaluate(as(tenantID, "role-service"), user, roles("GR", "ReceiveGoods", "AP", "PostVendorInvoices"), t0.Add(time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range again {
		want := map[string]string{"P2P-004": domain.StateAcceptedRisk, "P2P-001": domain.StateResolved}[c.RuleID]
		if c.State != want {
			t.Errorf("%s is %s, want %s", c.RuleID, c.State, want)
		}
	}
}

func TestList_FiltersAndPages(t *testing.T) {
	s, _ := migrated(t)
	ctx := as(ids.New(), "role-service")
	for i := 0; i < 5; i++ {
		if _, err := s.Evaluate(ctx, ids.New(), roles("GL", "PostGeneralJournals", "GLA", "ApproveGeneralJournals", "COA", "MaintainChartOfAccounts"), t0.Add(time.Duration(i)*time.Minute)); err != nil {
			t.Fatal(err)
		}
	}
	all, err := s.List(ctx, Filter{}, t0)
	if err != nil || len(all.Items) != 10 {
		t.Fatalf("want 10 conflicts (GL-001 and GL-002 per user): %d %v", len(all.Items), err)
	}
	medium, err := s.List(ctx, Filter{Severity: domain.SeverityMedium}, t0)
	if err != nil || len(medium.Items) != 5 {
		t.Fatalf("medium: %d %v", len(medium.Items), err)
	}
	seen := map[string]bool{}
	cursor := ""
	for pages := 0; pages < 10; pages++ {
		page, err := s.List(ctx, Filter{Limit: 3, Cursor: cursor}, t0)
		if err != nil {
			t.Fatal(err)
		}
		for _, c := range page.Items {
			if seen[c.ConflictID] {
				t.Fatalf("%s returned twice", c.ConflictID)
			}
			seen[c.ConflictID] = true
		}
		if page.NextCursor == "" {
			break
		}
		cursor = page.NextCursor
	}
	if len(seen) != 10 {
		t.Fatalf("paging returned %d of 10", len(seen))
	}
	if _, err := s.List(ctx, Filter{Cursor: "bad"}, t0); code(err) != "COM-0002" {
		t.Fatalf("bad cursor: %v", err)
	}
	var p *httpx.Problem
	if _, err := s.Get(ctx, ids.New(), t0); !errors.As(err, &p) || p.Status != http.StatusNotFound {
		t.Fatalf("missing conflict: %v", err)
	}
}

// Two first evaluations of one user racing each other both succeed, store
// one conflict and emit one conflict.detected.
func TestEvaluate_ConcurrentFirstEvaluationsStoreOneConflict(t *testing.T) {
	s, admin := migrated(t)
	tenantID, user := ids.New(), ids.New()
	both := roles("Maint", "MaintainVendors", "Pay", "ProcessVendorPayments")
	errs := make(chan error, 4)
	for i := 0; i < 4; i++ {
		go func() {
			_, err := s.Evaluate(as(tenantID, "role-service"), user, both, t0)
			errs <- err
		}()
	}
	for i := 0; i < 4; i++ {
		if err := <-errs; err != nil {
			t.Fatalf("concurrent evaluation: %v", err)
		}
	}
	var conflicts, events int
	if err := admin.QueryRow(context.Background(), `select count(*) from sod_conflict where tenant_id = $1`, tenantID).Scan(&conflicts); err != nil {
		t.Fatal(err)
	}
	if err := admin.QueryRow(context.Background(), `select count(*) from outbox where tenant_id = $1`, tenantID).Scan(&events); err != nil {
		t.Fatal(err)
	}
	if conflicts != 1 || events != 1 {
		t.Fatalf("conflicts %d events %d, want 1 and 1", conflicts, events)
	}
}
