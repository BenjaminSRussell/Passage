package identity

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"strconv"
	"strings"
)

// The customer portal's Keycloak role and the required actions every invited contact starts with. CONFIGURE_TOTP is the MFA
// enrolment: the contact cannot finish its first sign-in without registering an authenticator, whatever the realm's default.
const (
	CustomerUserRole     = "customer-user"
	invitationLifespan   = 7 * 24 * 3600
	maxContactEmailBytes = 254
)

var contactRequiredActions = []string{"VERIFY_EMAIL", "UPDATE_PASSWORD", "CONFIGURE_TOTP"}

// ContactSpec is what Keycloak needs to hold for one customer contact.
type ContactSpec struct {
	TenantID       string
	Email          string
	GivenName      string
	FamilyName     string
	CustomerID     string
	LegalEntityIDs []string
}

// Invitation says where the link in the invitation email leads after the contact has finished the required actions.
type Invitation struct {
	ClientID    string
	RedirectURI string
}

// ContactProvisioner creates and maintains the Keycloak accounts of customer contacts. KeycloakAdmin implements it; the tests use a
// fake of the Admin API.
type ContactProvisioner interface {
	// CreateContact makes (or, only for a provable leftover of this flow, adopts) the user for spec and returns its Keycloak id; ErrContactNotAdoptable for any other existing user. The
	// user is enabled, holds the customer-user role and the three required actions, and carries the portal's claims as attributes.
	CreateContact(ctx context.Context, realm string, spec ContactSpec) (userID string, adopted bool, err error)
	// SendInvitation emails the contact the link that completes the required actions.
	SendInvitation(ctx context.Context, realm, userID string, invitation Invitation) error
	// MapContact rewrites the contact's customer and legal entities and ends its sessions, so the next sign-in carries the new claims.
	MapContact(ctx context.Context, realm, tenantID, userID, customerID string, legalEntityIDs []string) error
	// DisableContact disables the account and ends its sessions.
	DisableContact(ctx context.Context, realm, tenantID, userID string) error
}

var _ ContactProvisioner = (*KeycloakAdmin)(nil)

func contactAttributes(spec ContactSpec) map[string][]string {
	return map[string][]string{
		"tenant_id":       {strings.ToLower(spec.TenantID)},
		"legal_entity_id": {strings.ToLower(strings.Join(spec.LegalEntityIDs, ","))},
		"customer_id":     {strings.ToLower(spec.CustomerID)},
		contactMarker:     {"customer"},
	}
}

// ErrContactNotAdoptable is returned when a Keycloak user with the address exists and is not provably a leftover of this flow.
var ErrContactNotAdoptable = errors.New("a Keycloak user with this address already exists and is not a portal contact created by this flow")

// contactMarker is set on every user this flow creates; only a user that carries it (and nothing staff-like) can be adopted.
const contactMarker = "portal_contact"

var contactAttributeNames = map[string]bool{"tenant_id": true, "legal_entity_id": true, "customer_id": true, contactMarker: true}

// adoptable reports whether an existing user is a leftover of this flow: it is enabled, carries the marker, only the portal's
// attributes, the same tenant, and (checked by the caller) no realm role beyond customer-user and Keycloak's defaults.
func adoptable(user map[string]any, tenantID string) bool {
	if enabled, _ := user["enabled"].(bool); !enabled {
		return false
	}
	attrs, _ := user["attributes"].(map[string]any)
	if len(attrs) == 0 {
		return false
	}
	for name := range attrs {
		if !contactAttributeNames[name] {
			return false
		}
	}
	if _, ok := attrs[contactMarker]; !ok {
		return false
	}
	return strings.EqualFold(userTenant(user), tenantID)
}

func (k *KeycloakAdmin) onlyPortalRoles(ctx context.Context, realm, userID string) (bool, error) {
	status, raw, err := k.call(ctx, realm, "get user roles", http.MethodGet, "/users/"+url.PathEscape(userID)+"/role-mappings/realm", nil)
	if err != nil {
		return false, err
	}
	if status != http.StatusOK {
		return false, statusError("get user roles", status, raw)
	}
	var roles []struct {
		Name string `json:"name"`
	}
	if err := json.Unmarshal(raw, &roles); err != nil {
		return false, &KeycloakError{Op: "get user roles", Detail: "unreadable role list"}
	}
	for _, r := range roles {
		if r.Name != CustomerUserRole && r.Name != "offline_access" && r.Name != "uma_authorization" && !strings.HasPrefix(r.Name, "default-roles-") {
			return false, nil
		}
	}
	return true, nil
}

// CreateContact implements ContactProvisioner. The registry is checked by the caller first; here an existing user is adopted only
// when it is provably a leftover of an earlier attempt of this flow (marker attribute, only portal attributes, same tenant, enabled,
// no other realm role). It is never enabled, never stripped of attributes it has, and anything else is ErrContactNotAdoptable with
// Keycloak untouched.
func (k *KeycloakAdmin) CreateContact(ctx context.Context, realm string, spec ContactSpec) (string, bool, error) {
	username := strings.ToLower(spec.Email)
	existing, err := k.findUser(ctx, realm, KeycloakTarget{TenantID: spec.TenantID, UserName: username})
	if err != nil {
		return "", false, err
	}
	var id string
	adopted := false
	if existing != nil {
		id, _ = existing["id"].(string)
		if _, ok := canonicalUUID(id); !ok {
			return "", false, &KeycloakError{Op: "get user", Detail: "the user representation has no id"}
		}
		if !adoptable(existing, spec.TenantID) {
			return "", false, ErrContactNotAdoptable
		}
		if ok, err := k.onlyPortalRoles(ctx, realm, id); err != nil {
			return "", false, err
		} else if !ok {
			return "", false, ErrContactNotAdoptable
		}
		attrs, _ := existing["attributes"].(map[string]any)
		for name, values := range contactAttributes(spec) {
			attrs[name] = values
		}
		existing["attributes"] = attrs
		if status, raw, err := k.call(ctx, realm, "update user", http.MethodPut, "/users/"+url.PathEscape(id), existing); err != nil {
			return "", false, err
		} else if status != http.StatusNoContent && status != http.StatusOK {
			return "", false, statusError("update user", status, raw)
		}
		adopted = true
	} else {
		rep := map[string]any{
			"username": username, "email": username, "firstName": spec.GivenName, "lastName": spec.FamilyName,
			"enabled": true, "emailVerified": false, "requiredActions": contactRequiredActions, "attributes": contactAttributes(spec),
		}
		status, raw, err := k.call(ctx, realm, "create user", http.MethodPost, "/users", rep)
		if err != nil {
			return "", false, err
		}
		if status != http.StatusCreated && status != http.StatusOK {
			return "", false, statusError("create user", status, raw)
		}
		created, err := k.findUser(ctx, realm, KeycloakTarget{TenantID: spec.TenantID, UserName: username})
		if err != nil {
			return "", false, err
		}
		if created == nil {
			return "", false, &KeycloakError{Op: "create user", Detail: "the created user cannot be found"}
		}
		id, _ = created["id"].(string)
		if _, ok := canonicalUUID(id); !ok {
			return "", false, &KeycloakError{Op: "create user", Detail: "the created user has no id"}
		}
	}
	return id, adopted, k.grantCustomerRole(ctx, realm, id)
}

func (k *KeycloakAdmin) grantCustomerRole(ctx context.Context, realm, userID string) error {
	status, raw, err := k.call(ctx, realm, "get role", http.MethodGet, "/roles/"+url.PathEscape(CustomerUserRole), nil)
	if err != nil {
		return err
	}
	if status == http.StatusNotFound {
		return &KeycloakError{Op: "get role", Status: status, Detail: "the realm has no " + CustomerUserRole + " role; import the portal realm", Permanent: true}
	}
	if status != http.StatusOK {
		return statusError("get role", status, raw)
	}
	var role map[string]any
	if err := json.Unmarshal(raw, &role); err != nil {
		return &KeycloakError{Op: "get role", Detail: "unreadable role representation"}
	}
	status, raw, err = k.call(ctx, realm, "assign role", http.MethodPost, "/users/"+url.PathEscape(userID)+"/role-mappings/realm", []any{role})
	if err != nil {
		return err
	}
	if status != http.StatusNoContent && status != http.StatusOK {
		return statusError("assign role", status, raw)
	}
	return nil
}

// SendInvitation implements ContactProvisioner.
func (k *KeycloakAdmin) SendInvitation(ctx context.Context, realm, userID string, inv Invitation) error {
	query := url.Values{"lifespan": {strconv.Itoa(invitationLifespan)}}
	if inv.RedirectURI != "" {
		query.Set("client_id", inv.ClientID)
		query.Set("redirect_uri", inv.RedirectURI)
	}
	status, raw, err := k.call(ctx, realm, "send invitation", http.MethodPut, "/users/"+url.PathEscape(userID)+"/execute-actions-email?"+query.Encode(), contactRequiredActions)
	if err != nil {
		return err
	}
	if status != http.StatusNoContent && status != http.StatusOK {
		return statusError("send invitation", status, raw)
	}
	return nil
}

// MapContact implements ContactProvisioner.
func (k *KeycloakAdmin) MapContact(ctx context.Context, realm, tenantID, userID, customerID string, legalEntityIDs []string) error {
	user, err := k.findUser(ctx, realm, KeycloakTarget{TenantID: tenantID, Subject: userID})
	if err != nil {
		return err
	}
	if user == nil {
		return &KeycloakError{Op: "get user", Status: http.StatusNotFound, Detail: "the Keycloak user no longer exists", Permanent: true}
	}
	if existing := userTenant(user); existing != "" && !strings.EqualFold(existing, tenantID) {
		return &KeycloakError{Op: "get user", Detail: "the Keycloak user belongs to another tenant", Permanent: true}
	}
	user["attributes"] = contactAttributes(ContactSpec{TenantID: tenantID, CustomerID: customerID, LegalEntityIDs: legalEntityIDs})
	path := "/users/" + url.PathEscape(userID)
	if status, raw, err := k.call(ctx, realm, "map user", http.MethodPut, path, user); err != nil {
		return err
	} else if status != http.StatusNoContent && status != http.StatusOK {
		return statusError("map user", status, raw)
	}
	return k.logout(ctx, realm, path)
}

// DisableContact implements ContactProvisioner.
func (k *KeycloakAdmin) DisableContact(ctx context.Context, realm, tenantID, userID string) error {
	outcome, err := k.DeprovisionUser(ctx, realm, KeycloakTarget{TenantID: tenantID, Subject: userID})
	if err != nil {
		return err
	}
	if outcome.Result == "not_found" {
		return errors.New("the Keycloak user no longer exists")
	}
	return nil
}

func (k *KeycloakAdmin) logout(ctx context.Context, realm, userPath string) error {
	status, raw, err := k.call(ctx, realm, "logout user", http.MethodPost, userPath+"/logout", nil)
	if err != nil {
		return err
	}
	if status != http.StatusNoContent && status != http.StatusOK && status != http.StatusNotFound {
		return statusError("logout user", status, raw)
	}
	return nil
}
