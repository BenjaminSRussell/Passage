package identity_test

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/BenjaminSRussell/passage/platform/tenant"
	"github.com/BenjaminSRussell/passage/platform/testkit"
	"github.com/BenjaminSRussell/passage/services/identity"
)

const (
	dutyTenants       = "Maintain tenants"
	dutyLegalEntities = "Maintain legal entities"
	dutyRoles         = "Maintain security roles"
	dutyAllScope      = "org.switcher.all_scope"
)

type caller struct {
	tenantID, legalEntity, user string
	duties                      []string
}

type adminFixture struct {
	admin, app       *pgxpool.Pool
	tenantA, tenantB string
	operator         string
	router           http.Handler
}

func newAdminFixture(t *testing.T) *adminFixture {
	t.Helper()
	admin, app, tenantA, tenantB := setupWithAdmin(t)
	raw, err := os.ReadFile("../../policies/authz/data.json")
	if err != nil {
		t.Fatal(err)
	}
	catalogue, err := identity.LoadCatalogue(raw)
	if err != nil {
		t.Fatal(err)
	}
	f := &adminFixture{admin: admin, app: app, tenantA: tenantA, tenantB: tenantB, operator: testkit.TenantID(9)}
	ctx := context.Background()
	for _, stmt := range []string{
		`insert into identity_tenants (tenant_id, realm_name, display_name) values ($1, 'operator', 'Operator')`,
		`insert into identity_operator (tenant_id) values ($1)`,
		`insert into identity_users (tenant_id, user_id, idp_subject) values ($1, '` + userID + `', 'subject-operator')`,
	} {
		if _, err := admin.Exec(ctx, stmt, f.operator); err != nil {
			t.Fatal(err)
		}
	}
	r := chi.NewRouter()
	identity.NewTenantsAPI(app, f.operator).Register(r)
	identity.NewLegalEntitiesAPI(app).Register(r)
	identity.NewRoleAssignmentsAPI(app, catalogue, f.operator).Register(r)
	identity.NewEffectivePermissionsAPI(app, catalogue).Register(r)
	f.router = r
	return f
}

func (f *adminFixture) do(c caller, method, path string, body any) *httptest.ResponseRecorder {
	var reader *bytes.Reader
	if body == nil {
		reader = bytes.NewReader(nil)
	} else {
		raw, _ := json.Marshal(body)
		reader = bytes.NewReader(raw)
	}
	req := httptest.NewRequest(method, path, reader)
	if body == nil {
		req.ContentLength = -1
	}
	held := map[string]struct{}{}
	for _, d := range c.duties {
		held[d] = struct{}{}
	}
	req = req.WithContext(tenant.With(req.Context(), tenant.Principal{TenantID: c.tenantID, LegalEntityID: c.legalEntity, UserID: c.user, Duties: held}))
	rec := httptest.NewRecorder()
	f.router.ServeHTTP(rec, req)
	return rec
}

func (f *adminFixture) operatorCaller() caller {
	return caller{tenantID: f.operator, user: "operator-1", duties: []string{dutyTenants}}
}

func (f *adminFixture) adminOf(tenantID string, duties ...string) caller {
	return caller{tenantID: tenantID, user: "admin-1", duties: append([]string{dutyAllScope}, duties...)}
}

func decode[T any](t *testing.T, rec *httptest.ResponseRecorder, want int) T {
	t.Helper()
	var v T
	if rec.Code != want {
		t.Fatalf("status = %d, want %d, body = %s", rec.Code, want, rec.Body.String())
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &v); err != nil {
		t.Fatal(err)
	}
	return v
}

type problemBody struct {
	Code   string            `json:"code"`
	Errors map[string]string `json:"errors"`
}

func wantProblem(t *testing.T, rec *httptest.ResponseRecorder, status int, code string) problemBody {
	t.Helper()
	p := decode[problemBody](t, rec, status)
	if code != "" && p.Code != code {
		t.Fatalf("code = %q, want %q", p.Code, code)
	}
	if got := rec.Header().Get("Content-Type"); got != "application/problem+json" {
		t.Fatalf("content type = %q", got)
	}
	return p
}

type tenantRow struct {
	TenantID  string `json:"tenantId"`
	RealmName string `json:"realmName"`
	Status    string `json:"status"`
}

type legalEntityRow struct {
	LegalEntityID string `json:"legalEntityId"`
	Code          string `json:"code"`
}

type assignmentRow struct {
	AssignmentID  string     `json:"assignmentId"`
	UserID        string     `json:"userId"`
	Role          string     `json:"role"`
	LegalEntityID string     `json:"legalEntityId"`
	ValidFrom     time.Time  `json:"validFrom"`
	ValidTo       *time.Time `json:"validTo"`
	Reason        string     `json:"reason"`
	GrantedBy     string     `json:"grantedBy"`
}

type pageOf[T any] struct {
	Items []T    `json:"items"`
	Next  string `json:"next"`
}

func (f *adminFixture) auditAndEvents(t *testing.T, tenantID string) (audit []string, eventTypes []string) {
	t.Helper()
	ctx := context.Background()
	if err := asTenant(t, f.app, tenantID, "*", func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `select action || '|' || actor from identity_audit order by audit_id`)
		if err != nil {
			return err
		}
		if audit, err = pgx.CollectRows(rows, pgx.RowTo[string]); err != nil {
			return err
		}
		rows, err = tx.Query(ctx, `select type from outbox order by id`)
		if err != nil {
			return err
		}
		eventTypes, err = pgx.CollectRows(rows, pgx.RowTo[string])
		return err
	}); err != nil {
		t.Fatal(err)
	}
	return audit, eventTypes
}

func TestTenantsAreOperatorOnly(t *testing.T) {
	f := newAdminFixture(t)
	body := map[string]string{"realmName": "acme-co", "displayName": "Acme"}

	t.Run("operator creates, reads and lists", func(t *testing.T) {
		rec := f.do(f.operatorCaller(), http.MethodPost, "/v1/tenants", body)
		created := decode[tenantRow](t, rec, http.StatusCreated)
		if created.RealmName != "acme-co" || created.Status != "active" || rec.Header().Get("Location") != "/v1/tenants/"+created.TenantID {
			t.Fatalf("created = %+v location = %q", created, rec.Header().Get("Location"))
		}
		got := decode[tenantRow](t, f.do(f.operatorCaller(), http.MethodGet, "/v1/tenants/"+created.TenantID, nil), http.StatusOK)
		if got != created {
			t.Fatalf("get = %+v, want %+v", got, created)
		}
		first := decode[pageOf[tenantRow]](t, f.do(f.operatorCaller(), http.MethodGet, "/v1/tenants?limit=2", nil), http.StatusOK)
		if len(first.Items) != 2 || first.Next == "" {
			t.Fatalf("first page = %+v, want 2 items and a cursor (operator, A, B and the new tenant exist)", first)
		}
		second := decode[pageOf[tenantRow]](t, f.do(f.operatorCaller(), http.MethodGet, "/v1/tenants?limit=2&after="+first.Next, nil), http.StatusOK)
		if len(second.Items) != 2 || second.Next != "" || second.Items[0].TenantID == first.Items[0].TenantID {
			t.Fatalf("second page = %+v", second)
		}
	})

	t.Run("create is audited and emits an event for the new tenant", func(t *testing.T) {
		var tenantID string
		if err := f.admin.QueryRow(context.Background(), `select tenant_id::text from identity_tenants where realm_name = 'acme-co'`).Scan(&tenantID); err != nil {
			t.Fatal(err)
		}
		audit, events := f.auditAndEvents(t, tenantID)
		if len(audit) != 1 || audit[0] != "tenant.created|operator-1" || len(events) != 1 || events[0] != "dev.passage.identity.tenant.created.v1" {
			t.Fatalf("audit = %v events = %v", audit, events)
		}
		operatorAudit, operatorEvents := f.auditAndEvents(t, f.operator)
		if len(operatorAudit) != 1 || operatorAudit[0] != "tenant.created|operator-1" || len(operatorEvents) != 0 {
			t.Fatalf("operator audit = %v events = %v, want one tenant.created row and no event", operatorAudit, operatorEvents)
		}
	})

	t.Run("duplicate realm conflicts", func(t *testing.T) {
		wantProblem(t, f.do(f.operatorCaller(), http.MethodPost, "/v1/tenants", body), http.StatusConflict, "IDN-TENANT-EXISTS")
	})

	t.Run("validation carries field errors", func(t *testing.T) {
		p := wantProblem(t, f.do(f.operatorCaller(), http.MethodPost, "/v1/tenants", map[string]string{"realmName": "Bad Realm", "displayName": " "}), http.StatusBadRequest, "COM-0001")
		if p.Errors["realmName"] == "" || p.Errors["displayName"] == "" {
			t.Fatalf("errors = %v", p.Errors)
		}
		wantProblem(t, f.do(f.operatorCaller(), http.MethodPost, "/v1/tenants", map[string]string{"realm": "x"}), http.StatusBadRequest, "COM-0001")
		wantProblem(t, f.do(f.operatorCaller(), http.MethodGet, "/v1/tenants/nope", nil), http.StatusBadRequest, "COM-0001")
		wantProblem(t, f.do(f.operatorCaller(), http.MethodGet, "/v1/tenants?limit=0", nil), http.StatusBadRequest, "COM-0001")
	})

	t.Run("unknown tenant is 404", func(t *testing.T) {
		wantProblem(t, f.do(f.operatorCaller(), http.MethodGet, "/v1/tenants/"+testkit.TenantID(77), nil), http.StatusNotFound, "IDN-TENANT-NOT-FOUND")
	})

	t.Run("everyone else is refused", func(t *testing.T) {
		for name, c := range map[string]caller{
			"operator tenant without duty": {tenantID: f.operator, user: "u"},
			"tenant admin of a tenant":     f.adminOf(f.tenantA, dutyTenants),
			"wrong duty":                   {tenantID: f.operator, user: "u", duties: []string{dutyLegalEntities}},
			"no tenant":                    {user: "u", duties: []string{dutyTenants}},
		} {
			want := http.StatusForbidden
			if c.tenantID == "" {
				want = http.StatusUnauthorized
			}
			for _, call := range []struct{ method, path string }{{http.MethodPost, "/v1/tenants"}, {http.MethodGet, "/v1/tenants"}, {http.MethodGet, "/v1/tenants/" + f.tenantA}} {
				if rec := f.do(c, call.method, call.path, body); rec.Code != want {
					t.Errorf("%s %s %s: status = %d, want %d", name, call.method, call.path, rec.Code, want)
				}
			}
		}
	})

	t.Run("mutations need an authenticated actor", func(t *testing.T) {
		c := f.operatorCaller()
		c.user = ""
		wantProblem(t, f.do(c, http.MethodPost, "/v1/tenants", map[string]string{"realmName": "no-actor", "displayName": "x"}), http.StatusUnauthorized, "AUTH-0001")
	})

	t.Run("a failed create leaves no tenant or audit row", func(t *testing.T) {
		before, _ := f.auditAndEvents(t, f.operator)
		wantProblem(t, f.do(f.operatorCaller(), http.MethodPost, "/v1/tenants", body), http.StatusConflict, "IDN-TENANT-EXISTS")
		after, _ := f.auditAndEvents(t, f.operator)
		if len(before) != len(after) {
			t.Fatalf("operator audit %d -> %d", len(before), len(after))
		}
	})
}

func TestOperatorTableIsOperatorReadOnly(t *testing.T) {
	f := newAdminFixture(t)
	for tenantID, want := range map[string]int{f.operator: 1, f.tenantA: 0} {
		if got := count(t, f.app, tenantID, "", `select count(*) from identity_operator`); got != want {
			t.Errorf("tenant %s sees %d identity_operator rows, want %d", tenantID, got, want)
		}
	}
	if got := count(t, f.app, f.tenantA, "", `select count(*) from identity_tenants`); got != 1 {
		t.Errorf("a non-operator tenant sees %d tenants, want only its own", got)
	}
	if got := count(t, f.app, f.operator, "", `select count(*) from identity_tenants`); got != 3 {
		t.Errorf("operator sees %d tenants, want all 3", got)
	}
	for _, stmt := range []string{`insert into identity_operator (tenant_id) values ($1)`, `delete from identity_operator where tenant_id = $1`, `update identity_tenants set status = 'suspended' where tenant_id = $1`} {
		err := asTenant(t, f.app, f.operator, "*", func(tx pgx.Tx) error {
			_, err := tx.Exec(context.Background(), stmt, f.tenantA)
			return err
		})
		requireDenied(t, err, "passage_app "+stmt)
	}
}

func TestLegalEntities(t *testing.T) {
	f := newAdminFixture(t)
	c := f.adminOf(f.tenantA, dutyLegalEntities)

	created := decode[legalEntityRow](t, f.do(c, http.MethodPost, "/v1/legal-entities", map[string]string{"code": "DE-01", "displayName": "Germany GmbH"}), http.StatusCreated)
	if created.Code != "DE-01" {
		t.Fatalf("created = %+v", created)
	}

	t.Run("create is audited and emits an event", func(t *testing.T) {
		audit, events := f.auditAndEvents(t, f.tenantA)
		if len(audit) != 1 || audit[0] != "legalentity.created|admin-1" || len(events) != 1 || events[0] != "dev.passage.identity.legalentity.created.v1" {
			t.Fatalf("audit = %v events = %v", audit, events)
		}
	})

	t.Run("duplicate code conflicts and validation reports fields", func(t *testing.T) {
		wantProblem(t, f.do(c, http.MethodPost, "/v1/legal-entities", map[string]string{"code": "DE-01", "displayName": "Again"}), http.StatusConflict, "IDN-LEGAL-ENTITY-EXISTS")
		p := wantProblem(t, f.do(c, http.MethodPost, "/v1/legal-entities", map[string]string{"code": "bad code!", "displayName": ""}), http.StatusBadRequest, "COM-0001")
		if p.Errors["code"] == "" || p.Errors["displayName"] == "" {
			t.Fatalf("errors = %v", p.Errors)
		}
	})

	t.Run("list pages and get returns the entity", func(t *testing.T) {
		first := decode[pageOf[legalEntityRow]](t, f.do(c, http.MethodGet, "/v1/legal-entities?limit=2", nil), http.StatusOK)
		if len(first.Items) != 2 || first.Next == "" {
			t.Fatalf("first page = %+v, want 2 of 3 with a cursor", first)
		}
		second := decode[pageOf[legalEntityRow]](t, f.do(c, http.MethodGet, "/v1/legal-entities?limit=2&after="+first.Next, nil), http.StatusOK)
		if len(second.Items) != 1 {
			t.Fatalf("second page = %+v", second)
		}
		got := decode[legalEntityRow](t, f.do(c, http.MethodGet, "/v1/legal-entities/"+created.LegalEntityID, nil), http.StatusOK)
		if got != created {
			t.Fatalf("get = %+v, want %+v", got, created)
		}
	})

	t.Run("a scoped caller sees only its entity and cannot create", func(t *testing.T) {
		scoped := caller{tenantID: f.tenantA, legalEntity: legalA, user: "scoped", duties: []string{dutyLegalEntities}}
		list := decode[pageOf[legalEntityRow]](t, f.do(scoped, http.MethodGet, "/v1/legal-entities", nil), http.StatusOK)
		if len(list.Items) != 1 || list.Items[0].LegalEntityID != legalA {
			t.Fatalf("scoped list = %+v", list)
		}
		wantProblem(t, f.do(scoped, http.MethodGet, "/v1/legal-entities/"+legalB, nil), http.StatusNotFound, "IDN-LEGAL-ENTITY-NOT-FOUND")
		wantProblem(t, f.do(scoped, http.MethodPost, "/v1/legal-entities", map[string]string{"code": "NEW", "displayName": "x"}), http.StatusForbidden, "AUTH-0003")
	})

	t.Run("tenants are isolated", func(t *testing.T) {
		other := f.adminOf(f.tenantB, dutyLegalEntities)
		list := decode[pageOf[legalEntityRow]](t, f.do(other, http.MethodGet, "/v1/legal-entities", nil), http.StatusOK)
		if len(list.Items) != 1 {
			t.Fatalf("tenant B list = %+v, want only its seeded entity", list)
		}
		wantProblem(t, f.do(other, http.MethodGet, "/v1/legal-entities/"+created.LegalEntityID, nil), http.StatusNotFound, "IDN-LEGAL-ENTITY-NOT-FOUND")
		wantProblem(t, f.do(c, http.MethodGet, "/v1/legal-entities/not-a-uuid", nil), http.StatusBadRequest, "COM-0001")
	})

	t.Run("create needs an authenticated actor", func(t *testing.T) {
		noActor := f.adminOf(f.tenantA, dutyLegalEntities)
		noActor.user = ""
		wantProblem(t, f.do(noActor, http.MethodPost, "/v1/legal-entities", map[string]string{"code": "NA", "displayName": "x"}), http.StatusUnauthorized, "AUTH-0001")
	})

	t.Run("duty is required", func(t *testing.T) {
		for _, duties := range [][]string{nil, {dutyRoles}} {
			if rec := f.do(f.adminOf(f.tenantA, duties...), http.MethodGet, "/v1/legal-entities", nil); rec.Code != http.StatusForbidden {
				t.Errorf("duties %v: status = %d, want 403", duties, rec.Code)
			}
		}
	})
}

func TestRoleAssignmentLifecycle(t *testing.T) {
	f := newAdminFixture(t)
	c := f.adminOf(f.tenantA, dutyRoles)
	assign := func(body map[string]any) *httptest.ResponseRecorder {
		return f.do(c, http.MethodPost, "/v1/authz/assignments", body)
	}
	day := 24 * time.Hour
	now := time.Now().UTC()

	first := decode[assignmentRow](t, assign(map[string]any{"userId": userID, "role": "View vendors"}), http.StatusCreated)
	if first.Role != "View vendors" || first.ValidTo != nil || first.LegalEntityID != "" || first.ValidFrom.IsZero() {
		t.Fatalf("first = %+v", first)
	}

	t.Run("overlapping assignment of the same role, user and entity conflicts", func(t *testing.T) {
		wantProblem(t, assign(map[string]any{"userId": userID, "role": "View vendors"}), http.StatusConflict, "IDN-ASSIGNMENT-OVERLAP")
		wantProblem(t, assign(map[string]any{"userId": userID, "role": "View vendors", "validFrom": now.Add(-time.Minute), "validTo": now.Add(day)}), http.StatusConflict, "IDN-ASSIGNMENT-OVERLAP")
	})

	t.Run("same role on another legal entity or another role is allowed", func(t *testing.T) {
		decode[assignmentRow](t, assign(map[string]any{"userId": userID, "role": "View vendors", "legalEntityId": legalA}), http.StatusCreated)
		decode[assignmentRow](t, assign(map[string]any{"userId": userID, "role": "Maintain vendors"}), http.StatusCreated)
	})

	t.Run("end-dating frees the role for a later period", func(t *testing.T) {
		end := now.Add(2 * day)
		ended := decode[assignmentRow](t, f.do(c, http.MethodPost, "/v1/authz/assignments/"+first.AssignmentID+"/end", map[string]any{"validTo": end}), http.StatusOK)
		if ended.ValidTo == nil || !ended.ValidTo.Equal(end) {
			t.Fatalf("ended = %+v", ended)
		}
		wantProblem(t, assign(map[string]any{"userId": userID, "role": "View vendors", "validFrom": end.Add(-day)}), http.StatusConflict, "IDN-ASSIGNMENT-OVERLAP")
		decode[assignmentRow](t, assign(map[string]any{"userId": userID, "role": "View vendors", "validFrom": end}), http.StatusCreated)
	})

	t.Run("ending at exactly the current end is a no-op without an audit row", func(t *testing.T) {
		before, _ := f.auditAndEvents(t, f.tenantA)
		again := decode[assignmentRow](t, f.do(c, http.MethodPost, "/v1/authz/assignments/"+first.AssignmentID+"/end", map[string]any{"validTo": now.Add(2 * day)}), http.StatusOK)
		after, _ := f.auditAndEvents(t, f.tenantA)
		if again.ValidTo == nil || len(before) != len(after) {
			t.Fatalf("again = %+v audit %d -> %d", again, len(before), len(after))
		}
	})

	t.Run("end date cannot extend, precede the start or be in the past", func(t *testing.T) {
		path := "/v1/authz/assignments/" + first.AssignmentID + "/end"
		for name, validTo := range map[string]time.Time{"extend": now.Add(30 * day), "past": now.Add(-day), "before start": first.ValidFrom.Add(-day)} {
			p := wantProblem(t, f.do(c, http.MethodPost, path, map[string]any{"validTo": validTo}), http.StatusBadRequest, "COM-0001")
			if p.Errors["validTo"] == "" {
				t.Errorf("%s: errors = %v", name, p.Errors)
			}
		}
	})

	t.Run("ending with no body ends now and is final", func(t *testing.T) {
		other := decode[assignmentRow](t, assign(map[string]any{"userId": userID, "role": "View audit log"}), http.StatusCreated)
		ended := decode[assignmentRow](t, f.do(c, http.MethodPost, "/v1/authz/assignments/"+other.AssignmentID+"/end", nil), http.StatusOK)
		if ended.ValidTo == nil || ended.ValidTo.After(time.Now()) {
			t.Fatalf("ended = %+v", ended)
		}
		wantProblem(t, f.do(c, http.MethodPost, "/v1/authz/assignments/"+other.AssignmentID+"/end", nil), http.StatusConflict, "IDN-ASSIGNMENT-ENDED")
	})

	t.Run("list by user and by role, active filter and paging", func(t *testing.T) {
		byUser := decode[pageOf[assignmentRow]](t, f.do(c, http.MethodGet, "/v1/authz/assignments?userId="+userID+"&limit=3", nil), http.StatusOK)
		if len(byUser.Items) != 3 || byUser.Next == "" {
			t.Fatalf("by user first page = %+v", byUser)
		}
		rest := decode[pageOf[assignmentRow]](t, f.do(c, http.MethodGet, "/v1/authz/assignments?userId="+userID+"&limit=100&after="+byUser.Next, nil), http.StatusOK)
		if len(byUser.Items)+len(rest.Items) != 8 {
			t.Fatalf("user has %d assignments, want 8 (3 seeded + 5 created)", len(byUser.Items)+len(rest.Items))
		}
		byRole := decode[pageOf[assignmentRow]](t, f.do(c, http.MethodGet, "/v1/authz/assignments?role=View+vendors", nil), http.StatusOK)
		if len(byRole.Items) != 3 {
			t.Fatalf("by role = %+v, want 3 View vendors assignments", byRole.Items)
		}
		active := decode[pageOf[assignmentRow]](t, f.do(c, http.MethodGet, "/v1/authz/assignments?role=View+vendors&active=true", nil), http.StatusOK)
		if len(active.Items) != 2 {
			t.Fatalf("active by role = %+v, want 2 (the later period has not started)", active.Items)
		}
		got := decode[assignmentRow](t, f.do(c, http.MethodGet, "/v1/authz/assignments/"+first.AssignmentID, nil), http.StatusOK)
		if got.AssignmentID != first.AssignmentID || got.ValidTo == nil {
			t.Fatalf("get = %+v", got)
		}
	})

	t.Run("assignments feed the effective-permission report", func(t *testing.T) {
		rec := f.do(f.adminOf(f.tenantA, dutyRoles, "View effective permissions"), http.MethodGet, "/v1/identity/users/"+userID+"/effective-permissions", nil)
		report := decode[struct {
			Duties []string `json:"duties"`
		}](t, rec, http.StatusOK)
		if strings.Join(report.Duties, ",") != "Maintain vendors,View vendors" {
			t.Fatalf("duties = %v", report.Duties)
		}
	})

	t.Run("every change is audited with its actor and emitted", func(t *testing.T) {
		audit, events := f.auditAndEvents(t, f.tenantA)
		assigned, ended := 0, 0
		for _, a := range audit {
			switch a {
			case "roleassignment.assigned|admin-1":
				assigned++
			case "roleassignment.ended|admin-1":
				ended++
			}
		}
		eventCount := map[string]int{}
		for _, e := range events {
			eventCount[e]++
		}
		if assigned != 5 || ended != 2 || eventCount["dev.passage.identity.roleassignment.assigned.v1"] != 5 || eventCount["dev.passage.identity.roleassignment.ended.v1"] != 2 {
			t.Fatalf("audit = %v events = %v", audit, eventCount)
		}
	})

	t.Run("a failed write leaves no audit or event behind", func(t *testing.T) {
		before, beforeEvents := f.auditAndEvents(t, f.tenantA)
		wantProblem(t, assign(map[string]any{"userId": userID, "role": "Maintain vendors"}), http.StatusConflict, "IDN-ASSIGNMENT-OVERLAP")
		after, afterEvents := f.auditAndEvents(t, f.tenantA)
		if len(before) != len(after) || len(beforeEvents) != len(afterEvents) {
			t.Fatalf("audit %d -> %d, events %d -> %d", len(before), len(after), len(beforeEvents), len(afterEvents))
		}
	})
}

func TestRoleAssignmentRejections(t *testing.T) {
	f := newAdminFixture(t)
	c := f.adminOf(f.tenantA, dutyRoles)
	assign := func(c caller, body map[string]any) *httptest.ResponseRecorder {
		return f.do(c, http.MethodPost, "/v1/authz/assignments", body)
	}

	t.Run("validation carries field errors", func(t *testing.T) {
		p := wantProblem(t, assign(c, map[string]any{"userId": "x", "role": "tenant_admin", "legalEntityId": "y"}), http.StatusBadRequest, "COM-0001")
		if p.Errors["userId"] == "" || p.Errors["role"] == "" || p.Errors["legalEntityId"] == "" {
			t.Fatalf("errors = %v", p.Errors)
		}
		now := time.Now().UTC()
		p = wantProblem(t, assign(c, map[string]any{"userId": userID, "role": "View vendors", "validFrom": now, "validTo": now.Add(-time.Hour)}), http.StatusBadRequest, "COM-0001")
		if p.Errors["validTo"] == "" {
			t.Fatalf("errors = %v", p.Errors)
		}
		p = wantProblem(t, assign(c, map[string]any{"userId": userID, "role": "View vendors", "validTo": now.Add(-time.Hour)}), http.StatusBadRequest, "COM-0001")
		if p.Errors["validTo"] == "" {
			t.Fatalf("errors = %v", p.Errors)
		}
		wantProblem(t, f.do(c, http.MethodGet, "/v1/authz/assignments", nil), http.StatusBadRequest, "COM-0001")
		wantProblem(t, f.do(c, http.MethodGet, "/v1/authz/assignments?userId=x", nil), http.StatusBadRequest, "COM-0001")
		wantProblem(t, f.do(c, http.MethodGet, "/v1/authz/assignments/x", nil), http.StatusBadRequest, "COM-0001")
		wantProblem(t, f.do(c, http.MethodPost, "/v1/authz/assignments/x/end", nil), http.StatusBadRequest, "COM-0001")
	})

	t.Run("unknown user, legal entity and assignment are 404", func(t *testing.T) {
		wantProblem(t, assign(c, map[string]any{"userId": "20000000-0000-0000-0000-0000000000ff", "role": "View vendors"}), http.StatusNotFound, "IDN-USER-NOT-FOUND")
		wantProblem(t, assign(c, map[string]any{"userId": userID, "role": "View vendors", "legalEntityId": legalNone}), http.StatusNotFound, "IDN-LEGAL-ENTITY-NOT-FOUND")
		missing := "30000000-0000-0000-0000-0000000000ff"
		wantProblem(t, f.do(c, http.MethodGet, "/v1/authz/assignments/"+missing, nil), http.StatusNotFound, "IDN-ASSIGNMENT-NOT-FOUND")
		wantProblem(t, f.do(c, http.MethodPost, "/v1/authz/assignments/"+missing+"/end", nil), http.StatusNotFound, "IDN-ASSIGNMENT-NOT-FOUND")
	})

	t.Run("a deprovisioned user cannot receive a role", func(t *testing.T) {
		if _, err := f.admin.Exec(context.Background(), `update identity_users set status = 'deprovisioned' where tenant_id = $1`, f.tenantB); err != nil {
			t.Fatal(err)
		}
		wantProblem(t, assign(f.adminOf(f.tenantB, dutyRoles), map[string]any{"userId": userID, "role": "View vendors"}), http.StatusConflict, "IDN-USER-DEPROVISIONED")
	})

	t.Run("tenants are isolated", func(t *testing.T) {
		mine := decode[assignmentRow](t, assign(c, map[string]any{"userId": userID, "role": "View vendors"}), http.StatusCreated)
		other := f.adminOf(f.tenantB, dutyRoles)
		wantProblem(t, f.do(other, http.MethodGet, "/v1/authz/assignments/"+mine.AssignmentID, nil), http.StatusNotFound, "IDN-ASSIGNMENT-NOT-FOUND")
		wantProblem(t, f.do(other, http.MethodPost, "/v1/authz/assignments/"+mine.AssignmentID+"/end", nil), http.StatusNotFound, "IDN-ASSIGNMENT-NOT-FOUND")
		list := decode[pageOf[assignmentRow]](t, f.do(other, http.MethodGet, "/v1/authz/assignments?role=View+vendors", nil), http.StatusOK)
		if len(list.Items) != 0 {
			t.Fatalf("tenant B sees tenant A assignments: %+v", list.Items)
		}
	})

	t.Run("a legal-entity-scoped caller is confined to its entities", func(t *testing.T) {
		scoped := caller{tenantID: f.tenantA, legalEntity: legalA, user: "scoped", duties: []string{dutyRoles}}
		wantProblem(t, assign(scoped, map[string]any{"userId": userID, "role": "Maintain vendors"}), http.StatusForbidden, "AUTH-0003")
		wantProblem(t, assign(scoped, map[string]any{"userId": userID, "role": "Maintain vendors", "legalEntityId": legalB}), http.StatusNotFound, "IDN-LEGAL-ENTITY-NOT-FOUND")
		created := decode[assignmentRow](t, assign(scoped, map[string]any{"userId": userID, "role": "Maintain vendors", "legalEntityId": legalA}), http.StatusCreated)
		list := decode[pageOf[assignmentRow]](t, f.do(scoped, http.MethodGet, "/v1/authz/assignments?userId="+userID, nil), http.StatusOK)
		for _, a := range list.Items {
			if a.LegalEntityID != legalA {
				t.Fatalf("scoped caller saw %+v", a)
			}
		}
		if len(list.Items) != 2 {
			t.Fatalf("scoped list = %+v, want the seeded ap_clerk and the new assignment", list.Items)
		}
		decode[assignmentRow](t, f.do(scoped, http.MethodPost, "/v1/authz/assignments/"+created.AssignmentID+"/end", nil), http.StatusOK)
	})

	t.Run("duty, tenant and actor are required", func(t *testing.T) {
		body := map[string]any{"userId": userID, "role": "View vendors"}
		for name, tc := range map[string]struct {
			c    caller
			want int
		}{
			"no duty":    {f.adminOf(f.tenantA), http.StatusForbidden},
			"wrong duty": {f.adminOf(f.tenantA, dutyLegalEntities), http.StatusForbidden},
			"no tenant":  {caller{user: "u", duties: []string{dutyRoles}}, http.StatusUnauthorized},
			"no actor":   {caller{tenantID: f.tenantA, legalEntity: "*", duties: []string{dutyRoles}}, http.StatusUnauthorized},
		} {
			if rec := assign(tc.c, body); rec.Code != tc.want {
				t.Errorf("%s: status = %d, want %d", name, rec.Code, tc.want)
			}
		}
	})
}

func TestRoleAssignmentFutureDatedRevoke(t *testing.T) {
	f := newAdminFixture(t)
	c := f.adminOf(f.tenantA, dutyRoles, "View effective permissions")
	day := 24 * time.Hour
	start := time.Now().UTC().Add(day)
	path := func(id string) string { return "/v1/authz/assignments/" + id }
	report := func() string {
		rec := f.do(c, http.MethodGet, "/v1/identity/users/"+userID+"/effective-permissions", nil)
		return strings.Join(decode[struct {
			Duties []string `json:"duties"`
		}](t, rec, http.StatusOK).Duties, ",")
	}

	future := decode[assignmentRow](t, f.do(c, http.MethodPost, "/v1/authz/assignments", map[string]any{"userId": userID, "role": "View vendors", "validFrom": start}), http.StatusCreated)

	t.Run("ending a future-dated assignment cancels it", func(t *testing.T) {
		ended := decode[assignmentRow](t, f.do(c, http.MethodPost, path(future.AssignmentID)+"/end", nil), http.StatusOK)
		if ended.ValidTo == nil || !ended.ValidTo.Equal(future.ValidFrom) {
			t.Fatalf("ended = %+v, want validTo = validFrom", ended)
		}
		active := decode[pageOf[assignmentRow]](t, f.do(c, http.MethodGet, "/v1/authz/assignments?role=View+vendors&active=true", nil), http.StatusOK)
		if len(active.Items) != 0 {
			t.Fatalf("active = %+v", active.Items)
		}
	})

	t.Run("ending again is a no-op without a new audit row", func(t *testing.T) {
		before, _ := f.auditAndEvents(t, f.tenantA)
		decode[assignmentRow](t, f.do(c, http.MethodDelete, path(future.AssignmentID), nil), http.StatusOK)
		after, _ := f.auditAndEvents(t, f.tenantA)
		if len(before) != len(after) {
			t.Fatalf("audit %d -> %d", len(before), len(after))
		}
	})

	t.Run("the empty range does not block a real assignment of the same period", func(t *testing.T) {
		decode[assignmentRow](t, f.do(c, http.MethodPost, "/v1/authz/assignments", map[string]any{"userId": userID, "role": "View vendors", "validFrom": start}), http.StatusCreated)
	})

	t.Run("an empty range grants nothing", func(t *testing.T) {
		if _, err := f.admin.Exec(context.Background(), `insert into identity_role_assignments (tenant_id, assignment_id, user_id, role, valid_from, valid_to)
values ($1, gen_random_uuid(), $2, 'Maintain vendors', now() - interval '1 hour', now() - interval '1 hour')`, f.tenantA, userID); err != nil {
			t.Fatal(err)
		}
		if got := report(); got != "" {
			t.Fatalf("duties = %q, want none", got)
		}
	})

	t.Run("end dates before the start are still refused", func(t *testing.T) {
		other := decode[assignmentRow](t, f.do(c, http.MethodPost, "/v1/authz/assignments", map[string]any{"userId": userID, "role": "View audit log", "validFrom": start}), http.StatusCreated)
		wantProblem(t, f.do(c, http.MethodPost, path(other.AssignmentID)+"/end", map[string]any{"validTo": start.Add(-time.Hour)}), http.StatusBadRequest, "COM-0001")
	})
}

func TestRoleAssignmentGuards(t *testing.T) {
	f := newAdminFixture(t)
	c := f.adminOf(f.tenantA, dutyRoles)
	assign := func(c caller, body map[string]any) *httptest.ResponseRecorder {
		return f.do(c, http.MethodPost, "/v1/authz/assignments", body)
	}

	t.Run("a caller cannot grant a role to themselves", func(t *testing.T) {
		byUserID := c
		byUserID.user = userID
		wantProblem(t, assign(byUserID, map[string]any{"userId": userID, "role": "View vendors"}), http.StatusForbidden, "IAM-0416")
		bySubject := c
		bySubject.user = "subject-" + f.tenantA
		wantProblem(t, assign(bySubject, map[string]any{"userId": userID, "role": "View vendors"}), http.StatusForbidden, "IAM-0416")
		if got := count(t, f.app, f.tenantA, "", `select count(*) from identity_role_assignments where role = 'View vendors'`); got != 0 {
			t.Fatalf("a refused self-grant left %d rows", got)
		}
	})

	t.Run("Maintain tenants is grantable only in the operator tenant", func(t *testing.T) {
		wantProblem(t, assign(c, map[string]any{"userId": userID, "role": dutyTenants}), http.StatusForbidden, "AUTH-0003")
		operator := caller{tenantID: f.operator, user: "operator-1", duties: []string{dutyRoles, dutyAllScope}}
		decode[assignmentRow](t, assign(operator, map[string]any{"userId": userID, "role": dutyTenants}), http.StatusCreated)
	})

	t.Run("past validity is refused beyond the clock-skew allowance", func(t *testing.T) {
		now := time.Now().UTC()
		p := wantProblem(t, assign(c, map[string]any{"userId": userID, "role": "View vendors", "validFrom": now.Add(-time.Hour)}), http.StatusBadRequest, "COM-0001")
		if p.Errors["validFrom"] == "" {
			t.Fatalf("errors = %v", p.Errors)
		}
		decode[assignmentRow](t, assign(c, map[string]any{"userId": userID, "role": "View vendors", "validFrom": now.Add(-time.Minute)}), http.StatusCreated)
	})

	t.Run("reason and granted-by are stored and returned", func(t *testing.T) {
		created := decode[assignmentRow](t, assign(c, map[string]any{"userId": userID, "role": "Maintain vendors", "reason": " covering leave "}), http.StatusCreated)
		if created.Reason != "covering leave" || created.GrantedBy != "admin-1" {
			t.Fatalf("created = %+v", created)
		}
		got := decode[assignmentRow](t, f.do(c, http.MethodGet, "/v1/authz/assignments/"+created.AssignmentID, nil), http.StatusOK)
		if got.Reason != created.Reason || got.GrantedBy != created.GrantedBy {
			t.Fatalf("get = %+v", got)
		}
		p := wantProblem(t, assign(c, map[string]any{"userId": userID, "role": "View audit log", "reason": strings.Repeat("x", 501)}), http.StatusBadRequest, "COM-0001")
		if p.Errors["reason"] == "" {
			t.Fatalf("errors = %v", p.Errors)
		}
	})

	t.Run("an inactive legal entity cannot receive an assignment", func(t *testing.T) {
		const inactive = "10000000-0000-0000-0000-000000000077"
		if _, err := f.admin.Exec(context.Background(), `insert into identity_legal_entities (tenant_id, legal_entity_id, code, display_name, status) values ($1, $2, 'OLD', 'Old', 'inactive')`, f.tenantA, inactive); err != nil {
			t.Fatal(err)
		}
		wantProblem(t, assign(c, map[string]any{"userId": userID, "role": "View vendors", "legalEntityId": inactive}), http.StatusUnprocessableEntity, "IDN-LEGAL-ENTITY-INACTIVE")
	})

	t.Run("an end date slightly in the past is accepted as now", func(t *testing.T) {
		created := decode[assignmentRow](t, assign(c, map[string]any{"userId": userID, "role": "View audit log"}), http.StatusCreated)
		ended := decode[assignmentRow](t, f.do(c, http.MethodPost, "/v1/authz/assignments/"+created.AssignmentID+"/end", map[string]any{"validTo": time.Now().Add(-time.Minute)}), http.StatusOK)
		if ended.ValidTo == nil || ended.ValidTo.Before(created.ValidFrom) {
			t.Fatalf("ended = %+v", ended)
		}
	})

	t.Run("DELETE ends an assignment like POST end", func(t *testing.T) {
		created := decode[assignmentRow](t, assign(c, map[string]any{"userId": userID, "role": "View number sequences"}), http.StatusCreated)
		ended := decode[assignmentRow](t, f.do(c, http.MethodDelete, "/v1/authz/assignments/"+created.AssignmentID, nil), http.StatusOK)
		if ended.ValidTo == nil {
			t.Fatalf("ended = %+v", ended)
		}
		wantProblem(t, f.do(c, http.MethodDelete, "/v1/authz/assignments/"+created.AssignmentID, nil), http.StatusConflict, "IDN-ASSIGNMENT-ENDED")
	})
}
