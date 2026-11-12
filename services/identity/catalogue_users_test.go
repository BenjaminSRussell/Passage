package identity_test

import (
	"context"
	"net/http"
	"net/url"
	"strings"
	"testing"
)

type userRow struct {
	UserID      string `json:"userId"`
	IdpSubject  string `json:"idpSubject"`
	Email       string `json:"email"`
	DisplayName string `json:"displayName"`
	Status      string `json:"status"`
}

type dutySummaryRow struct {
	Name            string `json:"name"`
	ServiceOnly     bool   `json:"serviceOnly"`
	PermissionCount int    `json:"permissionCount"`
}

type dutyDetailRow struct {
	Name        string `json:"name"`
	ServiceOnly bool   `json:"serviceOnly"`
	Permissions []struct {
		Resource string   `json:"resource"`
		Methods  []string `json:"methods"`
	} `json:"permissions"`
}

type roleSummaryRow struct {
	Name   string   `json:"name"`
	Duties []string `json:"duties"`
}

type roleDetailRow struct {
	Name   string          `json:"name"`
	Duties []dutyDetailRow `json:"duties"`
}

func TestUsersList(t *testing.T) {
	f := newAdminFixture(t)
	ctx := context.Background()
	extra := []string{"20000000-0000-0000-0000-000000000002", "20000000-0000-0000-0000-000000000003"}
	for i, id := range extra {
		if _, err := f.admin.Exec(ctx, `insert into identity_users (tenant_id, user_id, idp_subject, email, display_name, status) values ($1, $2, $3, $4, $5, $6)`,
			f.tenantA, id, "subject-extra-"+id[len(id)-1:], []string{"ada@example.test", "bob@example.test"}[i], []string{"Ada Lovelace", "Bob Builder"}[i], []string{"active", "deprovisioned"}[i]); err != nil {
			t.Fatal(err)
		}
	}
	c := f.adminOf(f.tenantA, dutyUsers)

	t.Run("lists the tenant's users in id order and pages", func(t *testing.T) {
		first := decode[pageOf[userRow]](t, f.do(c, http.MethodGet, "/v1/identity/users?limit=2", nil), http.StatusOK)
		if len(first.Items) != 2 || first.Next == "" || first.Items[0].UserID != userID {
			t.Fatalf("first = %+v", first)
		}
		second := decode[pageOf[userRow]](t, f.do(c, http.MethodGet, "/v1/identity/users?limit=2&after="+first.Next, nil), http.StatusOK)
		if len(second.Items) != 1 || second.Items[0].UserID != extra[1] || second.Next != "" {
			t.Fatalf("second = %+v", second)
		}
	})

	t.Run("filters by status and text", func(t *testing.T) {
		gone := decode[pageOf[userRow]](t, f.do(c, http.MethodGet, "/v1/identity/users?status=deprovisioned", nil), http.StatusOK)
		if len(gone.Items) != 1 || gone.Items[0].DisplayName != "Bob Builder" {
			t.Fatalf("deprovisioned = %+v", gone)
		}
		found := decode[pageOf[userRow]](t, f.do(c, http.MethodGet, "/v1/identity/users?q=LOVELACE", nil), http.StatusOK)
		if len(found.Items) != 1 || found.Items[0].Email != "ada@example.test" {
			t.Fatalf("q = %+v", found)
		}
		none := decode[pageOf[userRow]](t, f.do(c, http.MethodGet, "/v1/identity/users?q=%25", nil), http.StatusOK)
		if len(none.Items) != 0 {
			t.Fatalf("a wildcard character matched %+v", none)
		}
	})

	t.Run("filters by legal entity through visible assignments", func(t *testing.T) {
		inA := decode[pageOf[userRow]](t, f.do(c, http.MethodGet, "/v1/identity/users?legalEntityId="+legalA, nil), http.StatusOK)
		if len(inA.Items) != 1 || inA.Items[0].UserID != userID {
			t.Fatalf("legal entity A = %+v", inA)
		}
		scoped := caller{tenantID: f.tenantA, legalEntity: legalA, user: "scoped", duties: []string{dutyUsers}}
		hidden := decode[pageOf[userRow]](t, f.do(scoped, http.MethodGet, "/v1/identity/users?legalEntityId="+legalB, nil), http.StatusOK)
		if len(hidden.Items) != 0 {
			t.Fatalf("a caller scoped to A saw users assigned in B: %+v", hidden)
		}
	})

	t.Run("get returns the user and 404s across tenants", func(t *testing.T) {
		got := decode[userRow](t, f.do(c, http.MethodGet, "/v1/identity/users/"+userID, nil), http.StatusOK)
		if got.IdpSubject != "subject-"+f.tenantA || got.Status != "active" {
			t.Fatalf("get = %+v", got)
		}
		wantProblem(t, f.do(f.adminOf(f.tenantB, dutyUsers), http.MethodGet, "/v1/identity/users/"+extra[0], nil), http.StatusNotFound, "IDN-USER-NOT-FOUND")
	})

	t.Run("tenants are isolated", func(t *testing.T) {
		other := decode[pageOf[userRow]](t, f.do(f.adminOf(f.tenantB, dutyUsers), http.MethodGet, "/v1/identity/users", nil), http.StatusOK)
		if len(other.Items) != 1 || other.Items[0].IdpSubject != "subject-"+f.tenantB {
			t.Fatalf("tenant B = %+v", other)
		}
	})

	t.Run("validation, duty and tenant are enforced", func(t *testing.T) {
		for _, path := range []string{"/v1/identity/users?status=gone", "/v1/identity/users?legalEntityId=urn:uuid:" + legalA, "/v1/identity/users?limit=0", "/v1/identity/users?q=" + strings.Repeat("x", 101), "/v1/identity/users/not-a-uuid"} {
			wantProblem(t, f.do(c, http.MethodGet, path, nil), http.StatusBadRequest, "COM-0001")
		}
		wantProblem(t, f.do(f.adminOf(f.tenantA, dutyRoles), http.MethodGet, "/v1/identity/users", nil), http.StatusForbidden, "AUTH-0003")
		wantProblem(t, f.do(caller{user: "u", duties: []string{dutyUsers}}, http.MethodGet, "/v1/identity/users", nil), http.StatusUnauthorized, "AUTH-0001")
	})
}

func TestRoleAndDutyCatalogue(t *testing.T) {
	f := newAdminFixture(t)
	c := f.adminOf(f.tenantA, dutyRoles)

	t.Run("duties list is sorted and filterable", func(t *testing.T) {
		all := decode[pageOf[dutySummaryRow]](t, f.do(c, http.MethodGet, "/v1/authz/duties", nil), http.StatusOK)
		if len(all.Items) < 100 || all.Items[0].Name > all.Items[1].Name {
			t.Fatalf("got %d duties, first two %+v", len(all.Items), all.Items[:2])
		}
		serviceOnly := decode[pageOf[dutySummaryRow]](t, f.do(c, http.MethodGet, "/v1/authz/duties?serviceOnly=true", nil), http.StatusOK)
		if len(serviceOnly.Items) == 0 {
			t.Fatal("no service-only duties listed")
		}
		for _, d := range serviceOnly.Items {
			if !d.ServiceOnly || d.PermissionCount == 0 {
				t.Fatalf("service-only filter returned %+v", d)
			}
		}
		matched := decode[pageOf[dutySummaryRow]](t, f.do(c, http.MethodGet, "/v1/authz/duties?q=party.vendor", nil), http.StatusOK)
		if len(matched.Items) < 2 {
			t.Fatalf("q=party.vendor matched %+v", matched.Items)
		}
	})

	t.Run("duty detail lists granted routes", func(t *testing.T) {
		d := decode[dutyDetailRow](t, f.do(c, http.MethodGet, "/v1/authz/duties/"+url.PathEscape("identity.security-role.maintain"), nil), http.StatusOK)
		if d.Name != "identity.security-role.maintain" || len(d.Permissions) != 4 || d.Permissions[0].Resource != "/v1/authz/roles{,/**}" {
			t.Fatalf("detail = %+v", d)
		}
		all := decode[pageOf[dutySummaryRow]](t, f.do(c, http.MethodGet, "/v1/authz/duties", nil), http.StatusOK)
		for _, summary := range all.Items {
			if summary.PermissionCount != 0 {
				continue
			}
			body := f.do(c, http.MethodGet, "/v1/authz/duties/"+url.PathEscape(summary.Name), nil).Body.String()
			if !strings.Contains(body, `"permissions":[]`) {
				t.Fatalf("route-less duty %q must emit an empty permissions array, got %s", summary.Name, body)
			}
			break
		}
		wantProblem(t, f.do(c, http.MethodGet, "/v1/authz/duties/nope", nil), http.StatusNotFound, "IDN-DUTY-NOT-FOUND")
	})

	t.Run("roles are the assignable duties", func(t *testing.T) {
		roles := decode[pageOf[roleSummaryRow]](t, f.do(c, http.MethodGet, "/v1/authz/roles", nil), http.StatusOK)
		all := decode[pageOf[dutySummaryRow]](t, f.do(c, http.MethodGet, "/v1/authz/duties", nil), http.StatusOK)
		assignable := 0
		for _, d := range all.Items {
			if !d.ServiceOnly {
				assignable++
			}
		}
		if len(roles.Items) != assignable {
			t.Fatalf("roles = %d, assignable duties = %d", len(roles.Items), assignable)
		}
		for _, r := range roles.Items {
			if len(r.Duties) != 1 || r.Duties[0] != r.Name || r.Name == "fx.rate.lookup" {
				t.Fatalf("role = %+v", r)
			}
		}
		role := decode[roleDetailRow](t, f.do(c, http.MethodGet, "/v1/authz/roles/"+url.PathEscape("party.vendor.view"), nil), http.StatusOK)
		if role.Name != "party.vendor.view" || len(role.Duties) != 1 || len(role.Duties[0].Permissions) == 0 {
			t.Fatalf("role detail = %+v", role)
		}
		wantProblem(t, f.do(c, http.MethodGet, "/v1/authz/roles/fx.rate.lookup", nil), http.StatusNotFound, "IDN-ROLE-NOT-FOUND")
		wantProblem(t, f.do(c, http.MethodGet, "/v1/authz/roles/nope", nil), http.StatusNotFound, "IDN-ROLE-NOT-FOUND")
	})

	t.Run("the q filter narrows roles", func(t *testing.T) {
		roles := decode[pageOf[roleSummaryRow]](t, f.do(c, http.MethodGet, "/v1/authz/roles?q=party.vendor.view", nil), http.StatusOK)
		if len(roles.Items) != 1 || roles.Items[0].Name != "party.vendor.view" {
			t.Fatalf("q filter = %+v", roles.Items)
		}
	})

	t.Run("filters validate and the duty is required", func(t *testing.T) {
		wantProblem(t, f.do(c, http.MethodGet, "/v1/authz/duties?serviceOnly=maybe", nil), http.StatusBadRequest, "COM-0001")
		wantProblem(t, f.do(c, http.MethodGet, "/v1/authz/roles?q="+strings.Repeat("x", 101), nil), http.StatusBadRequest, "COM-0001")
		for _, path := range []string{"/v1/authz/roles", "/v1/authz/roles/party.vendor.view", "/v1/authz/duties", "/v1/authz/duties/party.vendor.view"} {
			wantProblem(t, f.do(f.adminOf(f.tenantA, dutyUsers), http.MethodGet, path, nil), http.StatusForbidden, "AUTH-0003")
			wantProblem(t, f.do(caller{user: "u", duties: []string{dutyRoles}}, http.MethodGet, path, nil), http.StatusUnauthorized, "AUTH-0001")
		}
	})
}

func TestUsersListIsTenantWideAndFiltersOnActiveAssignments(t *testing.T) {
	f := newAdminFixture(t)
	ctx := context.Background()
	const unassigned = "20000000-0000-0000-0000-000000000002"
	if _, err := f.admin.Exec(ctx, `insert into identity_users (tenant_id, user_id, idp_subject) values ($1, $2, 'subject-unassigned')`, f.tenantA, unassigned); err != nil {
		t.Fatal(err)
	}
	scoped := caller{tenantID: f.tenantA, legalEntity: legalA, user: "scoped", duties: []string{dutyUsers}}

	t.Run("a legal-entity-scoped caller without the filter sees every user of the tenant", func(t *testing.T) {
		all := decode[pageOf[userRow]](t, f.do(scoped, http.MethodGet, "/v1/identity/users", nil), http.StatusOK)
		if len(all.Items) != 2 {
			t.Fatalf("scoped listing = %+v, want both tenant users (the accepted tenant-wide decision)", all)
		}
	})

	t.Run("the legal-entity filter drops ended and cancelled assignments", func(t *testing.T) {
		list := func() int {
			return len(decode[pageOf[userRow]](t, f.do(f.adminOf(f.tenantA, dutyUsers), http.MethodGet, "/v1/identity/users?legalEntityId="+legalB, nil), http.StatusOK).Items)
		}
		if list() != 1 {
			t.Fatal("user with a current legal entity B assignment is not listed")
		}
		if _, err := f.admin.Exec(ctx, `update identity_role_assignments set valid_to = valid_from where tenant_id = $1 and legal_entity_id = $2`, f.tenantA, legalB); err != nil {
			t.Fatal(err)
		}
		if list() != 0 {
			t.Fatal("a cancelled assignment still lists the user under legal entity B")
		}
		if _, err := f.admin.Exec(ctx, `update identity_role_assignments set valid_from = now() + interval '1 day', valid_to = null where tenant_id = $1 and legal_entity_id = $2`, f.tenantA, legalB); err != nil {
			t.Fatal(err)
		}
		if list() != 0 {
			t.Fatal("a future assignment already lists the user under legal entity B")
		}
	})

	t.Run("q is limited in characters, not bytes", func(t *testing.T) {
		c := f.adminOf(f.tenantA, dutyUsers, dutyRoles)
		for _, path := range []string{"/v1/identity/users?q=", "/v1/authz/duties?q=", "/v1/authz/roles?q="} {
			if rec := f.do(c, http.MethodGet, path+url.QueryEscape(strings.Repeat("é", 100)), nil); rec.Code != http.StatusOK {
				t.Errorf("%s with 100 two-byte characters = %d", path, rec.Code)
			}
			wantProblem(t, f.do(c, http.MethodGet, path+url.QueryEscape(strings.Repeat("é", 101)), nil), http.StatusBadRequest, "COM-0001")
		}
	})
}

func TestUserNames(t *testing.T) {
	f := newAdminFixture(t)
	ctx := context.Background()
	other := "20000000-0000-0000-0000-000000000009"
	if _, err := f.admin.Exec(ctx, `insert into identity_users (tenant_id, user_id, idp_subject, email, display_name, status) values ($1, $2, 'subject-gone', 'gone@example.test', 'Gina Gone', 'deprovisioned')`, f.tenantA, other); err != nil {
		t.Fatal(err)
	}
	reader := caller{tenantID: f.tenantA, user: "plain-user", duties: []string{"identity.user-name.read"}}
	type names struct {
		Items []struct {
			UserID      string `json:"userId"`
			DisplayName string `json:"displayName"`
			Email       string `json:"email"`
		} `json:"items"`
	}
	got := decode[names](t, f.do(reader, http.MethodGet, "/v1/identity/user-names?ids="+userID+","+other+",30000000-0000-0000-0000-000000000001", nil), http.StatusOK)
	if len(got.Items) != 2 {
		t.Fatalf("an unknown id is left out, a deprovisioned user still resolves: %+v", got)
	}
	if got.Items[0].Email != "" || got.Items[0].UserID != userID || got.Items[1].DisplayName != "Gina Gone" {
		t.Fatalf("names only: %+v", got)
	}
	if rec := f.do(caller{tenantID: f.tenantB, user: "x", duties: []string{"identity.user-name.read"}}, http.MethodGet, "/v1/identity/user-names?ids="+other, nil); rec.Code != http.StatusOK || len(decode[names](t, rec, http.StatusOK).Items) != 0 {
		t.Fatalf("another tenant sees nothing: %s", rec.Body.String())
	}
	for _, q := range []string{"", "?ids=", "?ids=nope"} {
		if rec := f.do(reader, http.MethodGet, "/v1/identity/user-names"+q, nil); rec.Code != http.StatusBadRequest {
			t.Fatalf("%q = %d", q, rec.Code)
		}
	}
	if rec := f.do(caller{tenantID: f.tenantA, user: "nobody"}, http.MethodGet, "/v1/identity/user-names?ids="+userID, nil); rec.Code != http.StatusForbidden {
		t.Fatalf("no duty = %d", rec.Code)
	}
}
