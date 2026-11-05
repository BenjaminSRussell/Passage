package identity_test

import (
	"context"
	"net/http"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5"

	"github.com/BenjaminSRussell/passage/services/identity"
)

type deprovisionRow struct {
	ID, Reason, Subject, UserName, RequestedBy string
	Attempts                                   int
	NextAttempt                                time.Time
	LastError, Outcome, KeycloakUser           string
	Completed, Parked                          bool
}

func deprovisionRows(t *testing.T, f *scimFixture, tenantID, userID string) []deprovisionRow {
	t.Helper()
	var out []deprovisionRow
	if err := asTenant(t, f.app, tenantID, "*", func(tx pgx.Tx) error {
		rows, err := tx.Query(context.Background(), `select deprovision_id::text, reason, idp_subject, coalesce(user_name, ''), requested_by, attempts, next_attempt_at,
  coalesce(last_error, ''), coalesce(outcome, ''), coalesce(keycloak_user_id, ''), completed_at is not null, parked_at is not null
from identity_keycloak_deprovisions where user_id = $1 order by requested_at, deprovision_id`, userID)
		if err != nil {
			return err
		}
		out, err = pgx.CollectRows(rows, func(row pgx.CollectableRow) (deprovisionRow, error) {
			var d deprovisionRow
			return d, row.Scan(&d.ID, &d.Reason, &d.Subject, &d.UserName, &d.RequestedBy, &d.Attempts, &d.NextAttempt, &d.LastError, &d.Outcome, &d.KeycloakUser, &d.Completed, &d.Parked)
		})
		return err
	}); err != nil {
		t.Fatal(err)
	}
	return out
}

type deprovisionFixture struct {
	*scimFixture
	kc     *fakeKeycloak
	worker *identity.KeycloakDeprovisioner
	clock  time.Time
	mu     sync.Mutex
}

func newDeprovisionFixture(t *testing.T) *deprovisionFixture {
	t.Helper()
	f := &deprovisionFixture{scimFixture: newSCIMFixture(t), kc: newFakeKeycloak(t), clock: time.Now().Add(time.Second)}
	f.worker = f.newWorker(t)
	r := chi.NewRouter()
	identity.NewKeycloakDeprovisionsAPI(f.app).Register(r)
	f.router = r
	return f
}

func (f *deprovisionFixture) newWorker(t *testing.T) *identity.KeycloakDeprovisioner {
	t.Helper()
	worker, err := identity.NewKeycloakDeprovisioner(f.app, f.kc.admin(t, "realm-"+f.tenantA, "realm-"+f.tenantB, "operator"), f.operator, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	worker.SetClock(func() time.Time {
		f.mu.Lock()
		defer f.mu.Unlock()
		return f.clock
	})
	return worker
}

func (f *deprovisionFixture) advance(d time.Duration) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.clock = f.clock.Add(d)
}

func (f *deprovisionFixture) round(t *testing.T) identity.DeprovisionResult {
	t.Helper()
	res, err := f.worker.RunOnce(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	return res
}

func (f *deprovisionFixture) deactivate(t *testing.T, tenantID, id string) {
	t.Helper()
	got := decode[scimUserBody](t, f.as(tenantID)(http.MethodPatch, "/scim/v2/Users/"+id, patchBody(map[string]any{"op": "replace", "path": "active", "value": false})), http.StatusOK)
	if got.Active {
		t.Fatal("still active")
	}
}

func TestSCIMDeprovisioningReachesKeycloakThroughTheQueue(t *testing.T) {
	f := newDeprovisionFixture(t)
	do := f.as(f.tenantA)
	alice := f.createUser(t, do, "alice")
	f.kc.addUser("realm-"+f.tenantA, kcUserID, "alice", f.tenantA, true)

	if res := f.round(t); res != (identity.DeprovisionResult{}) || len(f.kc.callLog()) != 0 {
		t.Fatalf("nothing deprovisioned yet, round = %+v calls %v", res, f.kc.callLog())
	}
	f.deactivate(t, f.tenantA, alice.ID)
	rows := deprovisionRows(t, f.scimFixture, f.tenantA, alice.ID)
	if len(rows) != 1 || rows[0].Reason != "scim_deactivated" || rows[0].Subject != "scim:"+alice.ID || rows[0].UserName != "alice" || rows[0].RequestedBy != scimClient || rows[0].Completed {
		t.Fatalf("queued = %+v, want one pending scim_deactivated row for scim:%s/alice by %s", rows, alice.ID, scimClient)
	}

	if res := f.round(t); res != (identity.DeprovisionResult{Done: 1}) {
		t.Fatalf("round = %+v, want one done", res)
	}
	rows = deprovisionRows(t, f.scimFixture, f.tenantA, alice.ID)
	if !rows[0].Completed || rows[0].Outcome != "disabled" || rows[0].KeycloakUser != kcUserID || rows[0].Attempts != 0 {
		t.Fatalf("after the round = %+v", rows[0])
	}
	if f.kc.user("realm-"+f.tenantA, kcUserID)["enabled"] != false || !slices.Equal(f.kc.logouts, []string{"realm-" + f.tenantA + "/" + kcUserID}) {
		t.Fatalf("keycloak user %v, logouts %v", f.kc.user("realm-"+f.tenantA, kcUserID), f.kc.logouts)
	}
	calls := len(f.kc.callLog())
	if res := f.round(t); res != (identity.DeprovisionResult{}) || len(f.kc.callLog()) != calls {
		t.Fatalf("a finished row was worked again: %+v", res)
	}

	// DELETE of an already deprovisioned user changes nothing and queues nothing.
	if rec := do(http.MethodDelete, "/scim/v2/Users/"+alice.ID, nil); rec.Code != http.StatusNoContent {
		t.Fatalf("delete = %d", rec.Code)
	}
	if rows := deprovisionRows(t, f.scimFixture, f.tenantA, alice.ID); len(rows) != 1 {
		t.Fatalf("a repeated deprovisioning queued another call: %+v", rows)
	}

	// DELETE of an active user is queued as scim_deleted; with no Keycloak user it is done as not_found.
	carol := f.createUser(t, do, "carol")
	if rec := do(http.MethodDelete, "/scim/v2/Users/"+carol.ID, nil); rec.Code != http.StatusNoContent {
		t.Fatalf("delete = %d", rec.Code)
	}
	// Tenant B's users are worked in tenant B's realm.
	dave := f.createUser(t, f.as(f.tenantB), "dave")
	f.kc.addUser("realm-"+f.tenantB, "40000000-0000-0000-0000-0000000000b1", "dave", f.tenantB, true)
	f.deactivate(t, f.tenantB, dave.ID)
	if res := f.round(t); res != (identity.DeprovisionResult{Done: 2}) {
		t.Fatalf("round = %+v, want two done", res)
	}
	if rows := deprovisionRows(t, f.scimFixture, f.tenantA, carol.ID); len(rows) != 1 || rows[0].Reason != "scim_deleted" || rows[0].Outcome != "not_found" {
		t.Fatalf("carol = %+v, want scim_deleted done as not_found", rows)
	}
	if rows := deprovisionRows(t, f.scimFixture, f.tenantB, dave.ID); len(rows) != 1 || rows[0].Outcome != "disabled" {
		t.Fatalf("dave = %+v", rows)
	}
	if f.kc.user("realm-"+f.tenantB, "40000000-0000-0000-0000-0000000000b1")["enabled"] != false {
		t.Fatal("tenant B's Keycloak user was not disabled in tenant B's realm")
	}
	if rows := deprovisionRows(t, f.scimFixture, f.tenantA, dave.ID); len(rows) != 0 {
		t.Fatalf("tenant A sees tenant B's queue: %+v", rows)
	}
}

func TestKeycloakDeprovisionRetriesWithBackoffThenParksAndAnAdminRetries(t *testing.T) {
	f := newDeprovisionFixture(t)
	do := f.as(f.tenantA)
	alice := f.createUser(t, do, "alice")
	f.kc.addUser("realm-"+f.tenantA, kcUserID, "alice", f.tenantA, true)
	f.kc.fail(http.StatusServiceUnavailable, 0)
	f.deactivate(t, f.tenantA, alice.ID)

	for attempt := 1; attempt <= identity.DeprovisionMaxAttempts; attempt++ {
		res := f.round(t)
		row := deprovisionRows(t, f.scimFixture, f.tenantA, alice.ID)[0]
		if row.Attempts != attempt || !strings.Contains(row.LastError, "status 503") || row.Completed {
			t.Fatalf("attempt %d: row %+v", attempt, row)
		}
		if strings.Contains(row.LastError, fakeSecret) || strings.Contains(row.LastError, "token-") {
			t.Fatalf("last_error %q carries a secret or token", row.LastError)
		}
		if attempt == identity.DeprovisionMaxAttempts {
			if res != (identity.DeprovisionResult{Parked: 1}) || !row.Parked {
				t.Fatalf("attempt %d: round %+v parked %v, want parked", attempt, res, row.Parked)
			}
			break
		}
		if res != (identity.DeprovisionResult{Retried: 1}) || row.Parked {
			t.Fatalf("attempt %d: round %+v parked %v, want a retry", attempt, res, row.Parked)
		}
		backoff := min(30*time.Second<<(attempt-1), time.Hour)
		if got := row.NextAttempt.Sub(f.clock); got < backoff-time.Second || got > backoff+time.Second {
			t.Fatalf("attempt %d: next attempt in %v, want %v", attempt, got, backoff)
		}
		if res := f.round(t); res != (identity.DeprovisionResult{}) {
			t.Fatalf("attempt %d: worked again before the backoff ran out: %+v", attempt, res)
		}
		f.advance(backoff)
	}
	f.advance(24 * time.Hour)
	if res := f.round(t); res != (identity.DeprovisionResult{}) {
		t.Fatalf("a parked row was worked: %+v", res)
	}

	list := decode[struct {
		Items []struct {
			DeprovisionID, State, LastError string
			Attempts                        int
		} `json:"items"`
	}](t, f.do(f.adminOf(f.tenantA, dutyUsers), http.MethodGet, "/v1/identity/users/"+alice.ID+"/keycloak-deprovisions", nil), http.StatusOK)
	if len(list.Items) != 1 || list.Items[0].State != "parked" || list.Items[0].Attempts != identity.DeprovisionMaxAttempts || list.Items[0].LastError == "" {
		t.Fatalf("list = %+v", list)
	}
	retryPath := "/v1/identity/users/" + alice.ID + "/keycloak-deprovisions/" + list.Items[0].DeprovisionID + ":retry"
	scoped := caller{tenantID: f.tenantA, legalEntity: legalA, user: "clerk-1", duties: []string{dutyUsers}}
	wantProblem(t, f.do(scoped, http.MethodPost, retryPath, nil), http.StatusForbidden, "AUTH-0003")
	wantProblem(t, f.do(f.adminOf(f.tenantB, dutyUsers), http.MethodPost, retryPath, nil), http.StatusNotFound, "IDN-KEYCLOAK-DEPROVISION-NOT-FOUND")
	wantProblem(t, f.do(f.adminOf(f.tenantA, dutyUsers), http.MethodPost, "/v1/identity/users/"+alice.ID+"/keycloak-deprovisions/"+list.Items[0].DeprovisionID+":bogus", nil), http.StatusNotFound, "")

	retried := decode[struct {
		State    string `json:"state"`
		Attempts int    `json:"attempts"`
	}](t, f.do(f.adminOf(f.tenantA, dutyUsers), http.MethodPost, retryPath, nil), http.StatusOK)
	if retried.State != "pending" || retried.Attempts != 0 {
		t.Fatalf("retried = %+v", retried)
	}
	if audit, events := f.auditAndEvents(t, f.tenantA); !slices.Contains(audit, "keycloakdeprovision.retried|admin-1") || !slices.Contains(events, "dev.passage.identity.keycloakdeprovision.retried.v1") {
		t.Fatalf("retry not audited: %v %v", audit, events)
	}
	f.kc.fail(0, 0)
	if res := f.round(t); res != (identity.DeprovisionResult{Done: 1}) {
		t.Fatalf("round after the retry = %+v", res)
	}
	wantProblem(t, f.do(f.adminOf(f.tenantA, dutyUsers), http.MethodPost, retryPath, nil), http.StatusConflict, "IDN-KEYCLOAK-DEPROVISION-DONE")
}

func TestKeycloakDeprovisionParksAPermanentErrorAtOnce(t *testing.T) {
	f := newDeprovisionFixture(t)
	alice := f.createUser(t, f.as(f.tenantA), "alice")
	f.kc.addUser("realm-"+f.tenantA, kcUserID, "alice", f.tenantA, true)
	f.kc.fail(http.StatusForbidden, 0)
	f.deactivate(t, f.tenantA, alice.ID)
	if res := f.round(t); res != (identity.DeprovisionResult{Parked: 1}) {
		t.Fatalf("round = %+v, want parked", res)
	}
	if row := deprovisionRows(t, f.scimFixture, f.tenantA, alice.ID)[0]; !row.Parked || row.Attempts != 1 || !strings.Contains(row.LastError, "status 403") {
		t.Fatalf("row = %+v", row)
	}
}

func TestSCIMReactivationSupersedesAQueuedKeycloakDeprovision(t *testing.T) {
	f := newDeprovisionFixture(t)
	do := f.as(f.tenantA)
	alice := f.createUser(t, do, "alice")
	f.kc.addUser("realm-"+f.tenantA, kcUserID, "alice", f.tenantA, true)
	f.kc.fail(http.StatusServiceUnavailable, 0)
	f.deactivate(t, f.tenantA, alice.ID)
	if res := f.round(t); res != (identity.DeprovisionResult{Retried: 1}) {
		t.Fatalf("round = %+v", res)
	}
	if got := decode[scimUserBody](t, do(http.MethodPatch, "/scim/v2/Users/"+alice.ID, patchBody(map[string]any{"op": "replace", "path": "active", "value": true})), http.StatusOK); !got.Active {
		t.Fatal("not reactivated")
	}
	if row := deprovisionRows(t, f.scimFixture, f.tenantA, alice.ID)[0]; !row.Completed || row.Outcome != "superseded" {
		t.Fatalf("row = %+v, want superseded", row)
	}
	f.kc.fail(0, 0)
	f.advance(time.Hour)
	calls := len(f.kc.callLog())
	if res := f.round(t); res != (identity.DeprovisionResult{}) || len(f.kc.callLog()) != calls || f.kc.user("realm-"+f.tenantA, kcUserID)["enabled"] != true {
		t.Fatalf("a superseded row reached Keycloak: %+v", res)
	}
}

// A row a worker has claimed stays invisible to another worker until its lease runs out, so two replicas never
// call Keycloak for the same row at once.
func TestKeycloakDeprovisionClaimHidesTheRowFromAConcurrentWorker(t *testing.T) {
	f := newDeprovisionFixture(t)
	alice := f.createUser(t, f.as(f.tenantA), "alice")
	f.kc.addUser("realm-"+f.tenantA, kcUserID, "alice", f.tenantA, true)
	f.deactivate(t, f.tenantA, alice.ID)
	gate := make(chan struct{})
	f.kc.mu.Lock()
	f.kc.gate = gate
	f.kc.mu.Unlock()

	first := make(chan identity.DeprovisionResult, 1)
	go func() {
		res, err := f.worker.RunOnce(context.Background())
		if err != nil {
			t.Error(err)
		}
		first <- res
	}()
	<-gate // the first worker is inside its Keycloak call
	if res, err := f.newWorker(t).RunOnce(context.Background()); err != nil || res != (identity.DeprovisionResult{}) {
		t.Fatalf("second worker = %+v, %v; want nothing to claim", res, err)
	}
	f.kc.mu.Lock()
	f.kc.gate = nil
	f.kc.mu.Unlock()
	gate <- struct{}{} // the PUT and logout that follow read the cleared gate and pass straight through
	if res := <-first; res != (identity.DeprovisionResult{Done: 1}) {
		t.Fatalf("first worker = %+v", res)
	}
}

func TestKeycloakDeprovisionsAreTenantIsolatedAndNeverDeleted(t *testing.T) {
	admin, app, tenantA, tenantB := setupWithAdmin(t)
	ctx := context.Background()
	const insert = `insert into identity_keycloak_deprovisions (tenant_id, deprovision_id, user_id, idp_subject, reason, requested_by)
values ($1, gen_random_uuid(), '` + userID + `', 'subject', 'scim_deactivated', 'test')`
	if _, err := admin.Exec(ctx, insert, tenantB); err != nil {
		t.Fatal(err)
	}
	if got := count(t, app, tenantA, "*", `select count(*) from identity_keycloak_deprovisions`); got != 0 {
		t.Fatalf("tenant A sees %d tenant B rows", got)
	}
	var updated int64
	if err := asTenant(t, app, tenantA, "*", func(tx pgx.Tx) error {
		tag, err := tx.Exec(ctx, `update identity_keycloak_deprovisions set parked_at = now() where tenant_id = $1`, tenantB)
		updated = tag.RowsAffected()
		return err
	}); err != nil || updated != 0 {
		t.Fatalf("tenant A updated %d tenant B rows (%v)", updated, err)
	}
	requireDenied(t, asTenant(t, app, tenantA, "*", func(tx pgx.Tx) error {
		_, err := tx.Exec(ctx, insert, tenantB)
		return err
	}), "tenant A queueing a tenant B deprovisioning")
	requireDenied(t, asTenant(t, app, tenantB, "*", func(tx pgx.Tx) error {
		_, err := tx.Exec(ctx, `delete from identity_keycloak_deprovisions`)
		return err
	}), "deleting a queued deprovisioning")
}
