package identity_test

import (
	"context"
	"errors"
	"net/http"
	"slices"
	"testing"

	"github.com/BenjaminSRussell/passage/services/identity"
)

func contactSpec() identity.ContactSpec {
	return identity.ContactSpec{TenantID: testTenant, Email: "Pat@Example.com", GivenName: "Pat", FamilyName: "Lee", CustomerID: custOne, LegalEntityIDs: []string{legalA, legalB}}
}

func attributeOf(user map[string]any, name string) string {
	attrs, _ := user["attributes"].(map[string][]string)
	if values := attrs[name]; len(values) == 1 {
		return values[0]
	}
	// A user stored by the fake from JSON has []any values.
	generic, _ := user["attributes"].(map[string]any)
	if values, _ := generic[name].([]any); len(values) == 1 {
		s, _ := values[0].(string)
		return s
	}
	return ""
}

func TestKeycloakAdminCreatesAContactWithTheClaimsRoleAndMFAEnrolment(t *testing.T) {
	kc := newFakeKeycloak(t)
	k := kc.admin(t, "acme")
	id, _, err := k.CreateContact(context.Background(), "acme", contactSpec())
	if err != nil || id == "" {
		t.Fatalf("create = %q, %v", id, err)
	}
	user := kc.user("acme", id)
	if user["username"] != "pat@example.com" || user["enabled"] != true || user["emailVerified"] != false {
		t.Fatalf("user = %v", user)
	}
	if attributeOf(user, "tenant_id") != testTenant || attributeOf(user, "customer_id") != custOne || attributeOf(user, "legal_entity_id") != legalA+","+legalB {
		t.Fatalf("attributes = %v: the portal reads tenant_id, customer_id and a comma separated legal_entity_id", user["attributes"])
	}
	actions, _ := user["requiredActions"].([]any)
	var got []string
	for _, a := range actions {
		got = append(got, a.(string))
	}
	if !slices.Contains(got, "CONFIGURE_TOTP") || !slices.Contains(got, "UPDATE_PASSWORD") || !slices.Contains(got, "VERIFY_EMAIL") {
		t.Fatalf("required actions %v must include the MFA enrolment", got)
	}
	if want := "acme/" + id + "/customer-user"; len(kc.roleMaps) != 1 || kc.roleMaps[0] != want {
		t.Fatalf("role maps %v, want %s", kc.roleMaps, want)
	}

	// A retry after a partial failure adopts the same user instead of creating another.
	again, adopted, err := k.CreateContact(context.Background(), "acme", contactSpec())
	if err != nil || again != id || kc.created != 1 || !adopted {
		t.Fatalf("retry = %q (%d created), %v; want the same user", again, kc.created, err)
	}
}

func TestKeycloakAdminNeverAdoptsAnotherTenantsUser(t *testing.T) {
	kc := newFakeKeycloak(t)
	kc.addUser("acme", kcUserID, "pat@example.com", "00000000-0000-0000-0000-000000000777", true)
	_, _, err := kc.admin(t, "acme").CreateContact(context.Background(), "acme", contactSpec())
	if !errors.Is(err, identity.ErrContactNotAdoptable) || len(kc.puts) != 0 || len(kc.roleMaps) != 0 || kc.created != 0 {
		t.Fatalf("err = %v puts %d roles %v created %d; want a permanent refusal and no change", err, len(kc.puts), kc.roleMaps, kc.created)
	}
}

func TestKeycloakAdminNeedsTheCustomerRoleInTheRealm(t *testing.T) {
	kc := newFakeKeycloak(t)
	kc.noRole = true
	if _, _, err := kc.admin(t, "acme").CreateContact(context.Background(), "acme", contactSpec()); !permanent(err) {
		t.Fatalf("a realm without customer-user: %v, want a permanent error", err)
	}
}

func TestKeycloakAdminSendsTheInvitationWithTheRequiredActions(t *testing.T) {
	kc := newFakeKeycloak(t)
	k := kc.admin(t, "acme")
	if err := k.SendInvitation(context.Background(), "acme", kcUserID, identity.Invitation{ClientID: "passage-portal-customer", RedirectURI: "https://customer.example/"}); err != nil {
		t.Fatal(err)
	}
	if err := k.SendInvitation(context.Background(), "acme", kcUserID, identity.Invitation{ClientID: "passage-portal-customer"}); err != nil {
		t.Fatal(err)
	}
	if len(kc.emails) != 2 {
		t.Fatalf("emails %v", kc.emails)
	}
	with, without := kc.emails[0], kc.emails[1]
	if with.query.Get("client_id") != "passage-portal-customer" || with.query.Get("redirect_uri") != "https://customer.example/" || with.query.Get("lifespan") != "604800" || !slices.Contains(with.actions, "CONFIGURE_TOTP") {
		t.Fatalf("with a redirect: %+v", with)
	}
	if without.query.Has("client_id") || without.query.Has("redirect_uri") {
		t.Fatalf("without a redirect Keycloak must not be told a client: %+v", without)
	}
	kc.emailStatus = http.StatusInternalServerError
	if err := k.SendInvitation(context.Background(), "acme", kcUserID, identity.Invitation{}); err == nil || permanent(err) {
		t.Fatalf("a 500 from Keycloak: %v, want a retryable error", err)
	}
}

func TestKeycloakAdminRemapsAContactAndEndsItsSessions(t *testing.T) {
	kc := newFakeKeycloak(t)
	k := kc.admin(t, "acme")
	id, _, err := k.CreateContact(context.Background(), "acme", contactSpec())
	if err != nil {
		t.Fatal(err)
	}
	if err := k.MapContact(context.Background(), "acme", testTenant, id, custTwo, []string{legalB}); err != nil {
		t.Fatal(err)
	}
	user := kc.user("acme", id)
	if attributeOf(user, "customer_id") != custTwo || attributeOf(user, "legal_entity_id") != legalB || attributeOf(user, "tenant_id") != testTenant {
		t.Fatalf("attributes = %v", user["attributes"])
	}
	if user["username"] != "pat@example.com" || len(kc.logouts) != 1 {
		t.Fatalf("the user must keep its other fields and be logged out: %v logouts %v", user, kc.logouts)
	}
	kc.addUser("acme", kcUserID, "other", "00000000-0000-0000-0000-000000000777", true)
	if err := k.MapContact(context.Background(), "acme", testTenant, kcUserID, custTwo, []string{legalB}); !permanent(err) {
		t.Fatalf("another tenant's user: %v", err)
	}
	if err := k.MapContact(context.Background(), "acme", testTenant, "40000000-0000-0000-0000-0000000009ff", custTwo, []string{legalB}); !permanent(err) {
		t.Fatalf("a missing user: %v", err)
	}
}

func TestKeycloakAdminDisablesAContact(t *testing.T) {
	kc := newFakeKeycloak(t)
	kc.addUser("acme", kcUserID, "pat@example.com", testTenant, true)
	k := kc.admin(t, "acme")
	if err := k.DisableContact(context.Background(), "acme", testTenant, kcUserID); err != nil {
		t.Fatal(err)
	}
	if kc.user("acme", kcUserID)["enabled"] != false || len(kc.logouts) != 1 {
		t.Fatalf("user = %v logouts %v", kc.user("acme", kcUserID), kc.logouts)
	}
	if err := k.DisableContact(context.Background(), "acme", testTenant, "40000000-0000-0000-0000-0000000009ff"); err == nil {
		t.Fatal("a missing user was reported disabled")
	}
}

// Existing Keycloak users that are not leftovers of this flow are never touched: a staff user, a disabled user, a user with another
// role, a user with staff attributes. Nothing is written and no role is added.
func TestKeycloakAdminNeverAdoptsAStaffOrDisabledUser(t *testing.T) {
	staffAttrs := func(extra map[string]any) map[string]any {
		a := map[string]any{"tenant_id": []any{testTenant}, "duties": []any{"ap.pay"}, "tier": []any{"gold"}}
		for k, v := range extra {
			a[k] = v
		}
		return a
	}
	cases := map[string]struct {
		attrs   map[string]any
		enabled bool
		roles   []string
	}{
		"staff attributes":      {staffAttrs(nil), true, nil},
		"marker plus staff":     {staffAttrs(map[string]any{"portal_contact": []any{"customer"}}), true, nil},
		"no attributes":         {map[string]any{}, true, nil},
		"disabled leftover":     {map[string]any{"tenant_id": []any{testTenant}, "portal_contact": []any{"customer"}}, false, nil},
		"marker but staff role": {map[string]any{"tenant_id": []any{testTenant}, "portal_contact": []any{"customer"}}, true, []string{"tenant-admin"}},
	}
	for name, c := range cases {
		kc := newFakeKeycloak(t)
		kc.addUser("acme", kcUserID, "pat@example.com", testTenant, c.enabled)
		kc.users["acme"][kcUserID]["attributes"] = c.attrs
		kc.extraRoles = map[string][]string{kcUserID: c.roles}
		_, _, err := kc.admin(t, "acme").CreateContact(context.Background(), "acme", contactSpec())
		if !errors.Is(err, identity.ErrContactNotAdoptable) || len(kc.puts) != 0 || len(kc.roleMaps) != 0 || kc.created != 0 {
			t.Errorf("%s: err %v puts %d roles %v created %d; want a refusal and no change", name, err, len(kc.puts), kc.roleMaps, kc.created)
		}
		if user := kc.user("acme", kcUserID); user["enabled"] != c.enabled {
			t.Errorf("%s: enabled flipped to %v", name, user["enabled"])
		}
	}
}

// A leftover is adopted by merging: the attributes it has stay, the portal's are set, and it is not enabled by the adoption.
func TestKeycloakAdminAdoptsALeftoverByMerging(t *testing.T) {
	kc := newFakeKeycloak(t)
	kc.addUser("acme", kcUserID, "pat@example.com", testTenant, true)
	kc.users["acme"][kcUserID]["attributes"] = map[string]any{"tenant_id": []any{testTenant}, "portal_contact": []any{"customer"}, "customer_id": []any{custTwo}}
	kc.extraRoles = map[string][]string{kcUserID: {"customer-user", "default-roles-acme"}}
	id, adopted, err := kc.admin(t, "acme").CreateContact(context.Background(), "acme", contactSpec())
	if err != nil || !adopted || id != kcUserID {
		t.Fatalf("adopt = %q %v %v", id, adopted, err)
	}
	user := kc.user("acme", kcUserID)
	if attributeOf(user, "customer_id") != custOne || attributeOf(user, "portal_contact") != "customer" {
		t.Fatalf("attributes = %v", user["attributes"])
	}
}
