package identity

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/BenjaminSRussell/passage/platform/tenant"
)

const (
	realmPath        = "../../deploy/keycloak/realm-passage.json"
	realmName        = "passage"
	realmAudience    = "passage-api"
	loginTenantID    = "6f1c2f3e-5a52-4f0e-9f3a-0d2f1b6a7c11"
	loginUserName    = "ci-login-user"
	loginUserSecret  = "ci-Login-Passw0rd-1234"
	keycloakURLEnv   = "PASSAGE_TEST_KEYCLOAK_URL"
	keycloakAdminEnv = "PASSAGE_TEST_KEYCLOAK_ADMIN"
	keycloakPassEnv  = "PASSAGE_TEST_KEYCLOAK_ADMIN_PASSWORD"
)

type realmExport struct {
	Realm               string                      `json:"realm"`
	AccessTokenLifespan int                         `json:"accessTokenLifespan"`
	DefaultSignatureAlg string                      `json:"defaultSignatureAlgorithm"`
	BrowserFlow         string                      `json:"browserFlow"`
	Clients             []realmClient               `json:"clients"`
	IdentityProviders   []realmIdP                  `json:"identityProviders"`
	AuthenticationFlows []realmFlow                 `json:"authenticationFlows"`
	RequiredActions     []realmAction               `json:"requiredActions"`
	Components          map[string][]realmComponent `json:"components"`
}

type realmClient struct {
	ClientID       string            `json:"clientId"`
	PublicClient   bool              `json:"publicClient"`
	DirectGrants   bool              `json:"directAccessGrantsEnabled"`
	Attributes     map[string]string `json:"attributes"`
	ProtocolMapper []realmMapper     `json:"protocolMappers"`
}

type realmMapper struct {
	Name           string            `json:"name"`
	ProtocolMapper string            `json:"protocolMapper"`
	Config         map[string]string `json:"config"`
}

type realmIdP struct {
	Alias      string            `json:"alias"`
	ProviderID string            `json:"providerId"`
	Enabled    bool              `json:"enabled"`
	Config     map[string]string `json:"config"`
}

type realmFlow struct {
	Alias      string `json:"alias"`
	Executions []struct {
		Authenticator string `json:"authenticator"`
		FlowAlias     string `json:"flowAlias"`
		Requirement   string `json:"requirement"`
	} `json:"authenticationExecutions"`
}

type realmAction struct {
	Alias         string `json:"alias"`
	DefaultAction bool   `json:"defaultAction"`
}

type realmComponent struct {
	Config map[string][]string `json:"config"`
}

func loadRealm(t *testing.T) realmExport {
	t.Helper()
	raw, err := os.ReadFile(realmPath)
	if err != nil {
		t.Fatal(err)
	}
	var realm realmExport
	if err := json.Unmarshal(raw, &realm); err != nil {
		t.Fatalf("realm export is not valid JSON: %v", err)
	}
	return realm
}

func TestKeycloakRealmMatchesIdentityExpectations(t *testing.T) {
	realm := loadRealm(t)
	if realm.Realm != realmName || realm.DefaultSignatureAlg != "RS256" {
		t.Fatalf("realm %q signs with %q, want %q with RS256", realm.Realm, realm.DefaultSignatureAlg, realmName)
	}
	if realm.AccessTokenLifespan <= 0 || time.Duration(realm.AccessTokenLifespan)*time.Second > 10*time.Minute {
		t.Fatalf("accessTokenLifespan %d must be within identity's 10 minute token limit", realm.AccessTokenLifespan)
	}
	wantClaims := map[string]string{"tid": "tenant_id", "le": "legal_entity_id", "duties": "duties", "tier": "tier"}
	for _, name := range []string{"passage-web", "passage-services"} {
		client := findClient(realm, name)
		if client == nil {
			t.Fatalf("client %s missing", name)
		}
		if client.DirectGrants {
			t.Errorf("client %s must not enable direct access grants in the exported realm", name)
		}
		var audience bool
		gotClaims := map[string]string{}
		for _, mapper := range client.ProtocolMapper {
			switch mapper.ProtocolMapper {
			case "oidc-audience-mapper":
				audience = mapper.Config["included.custom.audience"] == realmAudience && mapper.Config["access.token.claim"] == "true"
			case "oidc-usermodel-attribute-mapper":
				if mapper.Config["jsonType.label"] != "String" || mapper.Config["multivalued"] != "false" || mapper.Config["access.token.claim"] != "true" {
					t.Errorf("client %s mapper %s must emit a single string access-token claim", name, mapper.Name)
				}
				gotClaims[mapper.Config["claim.name"]] = mapper.Config["user.attribute"]
			}
		}
		if !audience {
			t.Errorf("client %s does not add audience %s to the access token", name, realmAudience)
		}
		for claim, attribute := range wantClaims {
			if gotClaims[claim] != attribute {
				t.Errorf("client %s maps claim %s from %q, want %q", name, claim, gotClaims[claim], attribute)
			}
		}
	}
	if web := findClient(realm, "passage-web"); web == nil || !web.PublicClient || web.Attributes["pkce.code.challenge.method"] != "S256" {
		t.Error("passage-web must be a public client with PKCE S256")
	}
}

func TestKeycloakRealmRequiresMFAAndHoldsNoSecrets(t *testing.T) {
	realm := loadRealm(t)
	secondFactor := map[string]bool{}
	var bound *realmFlow
	for i := range realm.AuthenticationFlows {
		flow := &realm.AuthenticationFlows[i]
		if flow.Alias == realm.BrowserFlow {
			bound = flow
		}
		for _, exec := range flow.Executions {
			if exec.Requirement == "ALTERNATIVE" && (exec.Authenticator == "auth-otp-form" || exec.Authenticator == "webauthn-authenticator") {
				secondFactor[exec.Authenticator] = true
			}
		}
	}
	if bound == nil {
		t.Fatalf("browserFlow %q is not defined in the export", realm.BrowserFlow)
	}
	if len(secondFactor) != 2 {
		t.Errorf("second-factor flow must offer both OTP and WebAuthn, got %v", secondFactor)
	}
	if !flowReachesRequired(realm, bound, "auth-username-password-form") {
		t.Error("bound browser flow never requires the username/password form")
	}
	if !slices.ContainsFunc(realm.RequiredActions, func(a realmAction) bool { return a.Alias == "CONFIGURE_TOTP" && a.DefaultAction }) {
		t.Error("CONFIGURE_TOTP must be a default required action so new users enrol a factor")
	}
	if len(realm.IdentityProviders) < 2 {
		t.Fatalf("want OIDC and SAML federation placeholders, got %d", len(realm.IdentityProviders))
	}
	kinds := map[string]bool{}
	for _, idp := range realm.IdentityProviders {
		kinds[idp.ProviderID] = true
		if idp.Enabled {
			t.Errorf("placeholder IdP %s must ship disabled", idp.Alias)
		}
		for key, value := range idp.Config {
			if strings.Contains(strings.ToLower(key), "secret") || (strings.Contains(value, "://") && !strings.Contains(value, ".invalid")) {
				t.Errorf("IdP %s config %s must not hold a secret or a real endpoint", idp.Alias, key)
			}
		}
	}
	if !kinds["oidc"] || !kinds["saml"] {
		t.Errorf("want oidc and saml placeholders, got %v", kinds)
	}
	raw, err := os.ReadFile(realmPath)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(bytes.ToLower(raw), []byte(`"secret"`)) || bytes.Contains(bytes.ToLower(raw), []byte(`"clientsecret"`)) {
		t.Error("realm export must not embed a secret")
	}
}

func TestKeycloakRealmUserProfileLocksTokenAttributesToAdmins(t *testing.T) {
	realm := loadRealm(t)
	var config string
	for _, components := range realm.Components {
		for _, component := range components {
			if values := component.Config["kc.user.profile.config"]; len(values) == 1 {
				config = values[0]
			}
		}
	}
	var profile struct {
		Attributes []struct {
			Name        string `json:"name"`
			Permissions struct {
				Edit []string `json:"edit"`
			} `json:"permissions"`
		} `json:"attributes"`
	}
	if err := json.Unmarshal([]byte(config), &profile); err != nil {
		t.Fatalf("user profile config: %v", err)
	}
	locked := map[string]bool{}
	for _, attribute := range profile.Attributes {
		if slices.Equal(attribute.Permissions.Edit, []string{"admin"}) {
			locked[attribute.Name] = true
		}
	}
	for _, name := range []string{"tenant_id", "legal_entity_id", "duties", "tier"} {
		if !locked[name] {
			t.Errorf("user attribute %s feeds a token claim and must be admin-edit only", name)
		}
	}
}

func findClient(realm realmExport, id string) *realmClient {
	for i := range realm.Clients {
		if realm.Clients[i].ClientID == id {
			return &realm.Clients[i]
		}
	}
	return nil
}

func flowReachesRequired(realm realmExport, flow *realmFlow, authenticator string) bool {
	for _, exec := range flow.Executions {
		if exec.Authenticator == authenticator && exec.Requirement == "REQUIRED" {
			return true
		}
		if exec.FlowAlias == "" {
			continue
		}
		for i := range realm.AuthenticationFlows {
			if realm.AuthenticationFlows[i].Alias == exec.FlowAlias && flowReachesRequired(realm, &realm.AuthenticationFlows[i], authenticator) {
				return true
			}
		}
	}
	return false
}

type keycloakAdmin struct {
	t      *testing.T
	base   string
	client *http.Client
	token  string
}

func (k *keycloakAdmin) do(method, path string, body any, wantStatus ...int) []byte {
	k.t.Helper()
	var reader *bytes.Reader
	if body != nil {
		raw, err := json.Marshal(body)
		if err != nil {
			k.t.Fatal(err)
		}
		reader = bytes.NewReader(raw)
	} else {
		reader = bytes.NewReader(nil)
	}
	req, err := http.NewRequestWithContext(context.Background(), method, k.base+path, reader)
	if err != nil {
		k.t.Fatal(err)
	}
	req.Header.Set("Authorization", "Bearer "+k.token)
	req.Header.Set("Content-Type", "application/json")
	resp, err := k.client.Do(req)
	if err != nil {
		k.t.Fatalf("%s %s: %v", method, path, err)
	}
	defer func() { _ = resp.Body.Close() }()
	var buf bytes.Buffer
	_, _ = buf.ReadFrom(resp.Body)
	if !slices.Contains(wantStatus, resp.StatusCode) {
		k.t.Fatalf("%s %s: status %d, want %v: %s", method, path, resp.StatusCode, wantStatus, buf.String())
	}
	return buf.Bytes()
}

func tokenRequest(t *testing.T, client *http.Client, base, realm string, form url.Values) string {
	t.Helper()
	resp, err := client.PostForm(base+"/realms/"+realm+"/protocol/openid-connect/token", form)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	var body struct {
		AccessToken string `json:"access_token"`
		Error       string `json:"error_description"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil || resp.StatusCode != http.StatusOK {
		t.Fatalf("token request for realm %s: status %d, error %v, detail %q", realm, resp.StatusCode, err, body.Error)
	}
	return body.AccessToken
}

func TestKeycloakLoginIsAcceptedByIdentityJWTVerifier(t *testing.T) {
	base := strings.TrimRight(os.Getenv(keycloakURLEnv), "/")
	if base == "" {
		t.Skipf("%s is not set; the keycloak CI job runs this test against a container", keycloakURLEnv)
	}
	client := &http.Client{Timeout: 15 * time.Second}
	admin := &keycloakAdmin{t: t, base: base, client: client}
	admin.token = tokenRequest(t, client, base, "master", url.Values{
		"grant_type": {"password"}, "client_id": {"admin-cli"},
		"username": {os.Getenv(keycloakAdminEnv)}, "password": {os.Getenv(keycloakPassEnv)},
	})

	var clients []map[string]any
	if err := json.Unmarshal(admin.do(http.MethodGet, "/admin/realms/"+realmName+"/clients?clientId=passage-web", nil, http.StatusOK), &clients); err != nil || len(clients) != 1 {
		t.Fatalf("passage-web not imported: %v %v", err, clients)
	}
	clients[0]["directAccessGrantsEnabled"] = true
	admin.do(http.MethodPut, "/admin/realms/"+realmName+"/clients/"+clients[0]["id"].(string), clients[0], http.StatusNoContent)

	admin.do(http.MethodPost, "/admin/realms/"+realmName+"/users", map[string]any{
		"username": loginUserName, "email": loginUserName + "@example.test", "emailVerified": true, "enabled": true,
		"firstName": "Ci", "lastName": "Login", "requiredActions": []string{},
		"attributes":  map[string][]string{"tenant_id": {loginTenantID}, "duties": {"ap_clerk,approver"}, "tier": {"starter"}},
		"credentials": []map[string]any{{"type": "password", "value": loginUserSecret, "temporary": false}},
	}, http.StatusCreated)

	issuer := base + "/realms/" + realmName
	accessToken := tokenRequest(t, client, base, realmName, url.Values{
		"grant_type": {"password"}, "client_id": {"passage-web"}, "username": {loginUserName}, "password": {loginUserSecret},
	})

	verifier, err := NewJWTVerifier(issuer, realmAudience, loginTenantID, issuer+"/protocol/openid-connect/certs", client)
	if err != nil {
		t.Fatal(err)
	}
	var principal tenant.Principal
	handler := verifier.Middleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		principal, _ = tenant.FromContext(r.Context())
		w.WriteHeader(http.StatusNoContent)
	}))
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.Header.Set("Authorization", "Bearer "+accessToken)
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusNoContent {
		t.Fatalf("identity rejected the Keycloak token: %d %s", rec.Code, rec.Body.String())
	}
	if principal.TenantID != loginTenantID || principal.UserID == "" {
		t.Fatalf("verified principal = %+v, want tenant %s and a subject", principal, loginTenantID)
	}

	parts := strings.Split(accessToken, ".")
	payload, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		t.Fatal(err)
	}
	var claims map[string]any
	if err := json.Unmarshal(payload, &claims); err != nil {
		t.Fatal(err)
	}
	if claims["duties"] != "ap_clerk,approver" || claims["tier"] != "starter" {
		t.Fatalf("claims duties/tier = %v/%v, want the single-string values", claims["duties"], claims["tier"])
	}
}
