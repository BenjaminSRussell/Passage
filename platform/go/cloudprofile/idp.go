package cloudprofile

import (
	"fmt"
	"regexp"
	"strings"
)

const (
	// EntraAlias is the Keycloak alias of the Entra ID identity provider this package renders.
	EntraAlias = "entra-oidc"
	// PlaceholderEntraTenant and PlaceholderClientID mark values an operator fills in; the rendered provider
	// stays disabled while either is a placeholder.
	PlaceholderEntraTenant = "REPLACE_WITH_ENTRA_TENANT_ID"
	PlaceholderClientID    = "REPLACE_WITH_CLIENT_ID"
	PlaceholderPassageTID  = "REPLACE_WITH_TENANT_ID"
	// PostBrokerFlow is the realm flow that applies Passage's second factor after a federated login.
	PostBrokerFlow = "passage-post-broker-mfa"
)

var guid = regexp.MustCompile(`^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$`)

// IdPExport is a Keycloak identity provider with its mappers, ready for the admin API
// (POST /admin/realms/{realm}/identity-provider/instances, then one POST per mapper to
// .../instances/{alias}/mappers).
type IdPExport struct {
	Provider map[string]any   `json:"identityProvider"`
	Mappers  []map[string]any `json:"identityProviderMappers"`
}

// EntraOIDC renders the Entra ID OIDC identity provider (authorization code with PKCE) for a customer
// tenant from the profile's authority and Graph root. entraTenantID must be a tenant GUID (or the placeholder):
// "common" and "organizations" would accept any tenant. clientID is the app registration's application
// (client) ID; the client secret is never rendered, set it through the admin API or Key Vault.
// passageTenantID is the realm's Passage tenant, written by a hardcoded mapper: the tid comes from the realm,
// never from the Entra tid claim, and no upstream claim may write tenant_id, legal_entity_id, duties or tier.
func EntraOIDC(p *Profile, entraTenantID, clientID, passageTenantID string) (*IdPExport, error) {
	if entraTenantID != PlaceholderEntraTenant && !guid.MatchString(entraTenantID) {
		return nil, fmt.Errorf("cloudprofile: Entra tenant %q is not a tenant GUID", entraTenantID)
	}
	authority := strings.TrimRight(p.Endpoint("entraAuthority"), "/") + "/" + entraTenantID
	graph := strings.TrimRight(p.Endpoint("graphRoot"), "/")
	placeholder := entraTenantID == PlaceholderEntraTenant || clientID == PlaceholderClientID || passageTenantID == PlaceholderPassageTID
	provider := map[string]any{
		"alias":                    EntraAlias,
		"displayName":              "Microsoft Entra ID",
		"providerId":               "oidc",
		"enabled":                  !placeholder,
		"trustEmail":               false,
		"storeToken":               false,
		"linkOnly":                 false,
		"postBrokerLoginFlowAlias": PostBrokerFlow,
		"config": map[string]string{
			"authorizationUrl":  authority + "/oauth2/v2.0/authorize",
			"tokenUrl":          authority + "/oauth2/v2.0/token",
			"jwksUrl":           authority + "/discovery/v2.0/keys",
			"logoutUrl":         authority + "/oauth2/v2.0/logout",
			"userInfoUrl":       graph + "/oidc/userinfo",
			"issuer":            authority + "/v2.0",
			"clientId":          clientID,
			"clientAuthMethod":  "client_secret_post",
			"pkceEnabled":       "true",
			"pkceMethod":        "S256",
			"validateSignature": "true",
			"useJwksUrl":        "true",
			"defaultScope":      "openid email profile",
			"syncMode":          "FORCE",
		},
	}
	mapper := func(name, kind string, config map[string]string) map[string]any {
		return map[string]any{"name": name, "identityProviderAlias": EntraAlias, "identityProviderMapper": kind, "config": config}
	}
	return &IdPExport{
		Provider: provider,
		Mappers: []map[string]any{
			mapper("tenant", "hardcoded-attribute-idp-mapper", map[string]string{"syncMode": "FORCE", "attribute": "tenant_id", "attribute.value": passageTenantID}),
			mapper("given-name", "oidc-user-attribute-idp-mapper", map[string]string{"syncMode": "INHERIT", "claim": "given_name", "user.attribute": "firstName"}),
			mapper("family-name", "oidc-user-attribute-idp-mapper", map[string]string{"syncMode": "INHERIT", "claim": "family_name", "user.attribute": "lastName"}),
		},
	}, nil
}
