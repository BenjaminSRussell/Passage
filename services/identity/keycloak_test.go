package identity

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"html"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/BenjaminSRussell/passage/platform/cloudprofile"
	"github.com/BenjaminSRussell/passage/platform/tenant"
)

const (
	realmPath        = "../../deploy/keycloak/realm-passage.json"
	realmName        = "passage"
	realmAudience    = "passage-api"
	loginTenantID    = "6f1c2f3e-5a52-4f0e-9f3a-0d2f1b6a7c11"
	webCallbackURL   = "https://app.passage.example.invalid/auth/callback"
	loginUserName    = "ci-login-user"
	loginUserSecret  = "ci-Login-Passw0rd-1234"
	keycloakURLEnv   = "PASSAGE_TEST_KEYCLOAK_URL"
	keycloakAdminEnv = "PASSAGE_TEST_KEYCLOAK_ADMIN"
	keycloakPassEnv  = "PASSAGE_TEST_KEYCLOAK_ADMIN_PASSWORD"
	// loginDuties are seeded duties from policies/authz/data.json, comma-joined
	// the way the duties mapper emits them and apps/web parseDuties reads them.
	loginDuties = "party.vendor.view,workflow.workitem.approve"
)

type realmExport struct {
	Realm               string                      `json:"realm"`
	AccessTokenLifespan int                         `json:"accessTokenLifespan"`
	DefaultSignatureAlg string                      `json:"defaultSignatureAlgorithm"`
	BrowserFlow         string                      `json:"browserFlow"`
	Clients             []realmClient               `json:"clients"`
	IdentityProviders   []realmIdP                  `json:"identityProviders"`
	IdPMappers          []realmIdPMapper            `json:"identityProviderMappers"`
	PasswordPolicy      string                      `json:"passwordPolicy"`
	EventsEnabled       bool                        `json:"eventsEnabled"`
	AdminEventsEnabled  bool                        `json:"adminEventsEnabled"`
	AdminEventDetails   bool                        `json:"adminEventsDetailsEnabled"`
	EventsExpiration    int                         `json:"eventsExpiration"`
	EnabledEventTypes   []string                    `json:"enabledEventTypes"`
	OTPType             string                      `json:"otpPolicyType"`
	OTPAlgorithm        string                      `json:"otpPolicyAlgorithm"`
	OTPDigits           int                         `json:"otpPolicyDigits"`
	OTPPeriod           int                         `json:"otpPolicyPeriod"`
	OTPCodeReusable     bool                        `json:"otpPolicyCodeReusable"`
	AuthenticationFlows []realmFlow                 `json:"authenticationFlows"`
	RequiredActions     []realmAction               `json:"requiredActions"`
	Components          map[string][]realmComponent `json:"components"`
	Users               []realmUser                 `json:"users"`
	ClientScopeMappings map[string][]struct {
		Client string   `json:"client"`
		Roles  []string `json:"roles"`
	} `json:"clientScopeMappings"`
}

type realmUser struct {
	Username       string              `json:"username"`
	ServiceAccount string              `json:"serviceAccountClientId"`
	RealmRoles     []string            `json:"realmRoles"`
	ClientRoles    map[string][]string `json:"clientRoles"`
	Credentials    []json.RawMessage   `json:"credentials"`
}

type realmClient struct {
	ClientID       string            `json:"clientId"`
	PublicClient   bool              `json:"publicClient"`
	DirectGrants   bool              `json:"directAccessGrantsEnabled"`
	StandardFlow   bool              `json:"standardFlowEnabled"`
	ImplicitFlow   bool              `json:"implicitFlowEnabled"`
	ServiceAccount bool              `json:"serviceAccountsEnabled"`
	FullScope      *bool             `json:"fullScopeAllowed"`
	RedirectURIs   []string          `json:"redirectUris"`
	Attributes     map[string]string `json:"attributes"`
	ProtocolMapper []realmMapper     `json:"protocolMappers"`
}

type realmMapper struct {
	Name           string            `json:"name"`
	ProtocolMapper string            `json:"protocolMapper"`
	Config         map[string]string `json:"config"`
}

type realmIdP struct {
	Alias          string            `json:"alias"`
	ProviderID     string            `json:"providerId"`
	Enabled        bool              `json:"enabled"`
	PostBrokerFlow string            `json:"postBrokerLoginFlowAlias"`
	Config         map[string]string `json:"config"`
}

type realmIdPMapper struct {
	Alias  string            `json:"identityProviderAlias"`
	Mapper string            `json:"identityProviderMapper"`
	Config map[string]string `json:"config"`
}

type realmFlow struct {
	Alias      string      `json:"alias"`
	Executions []realmExec `json:"authenticationExecutions"`
}

type realmExec struct {
	Authenticator string `json:"authenticator"`
	FlowAlias     string `json:"flowAlias"`
	Requirement   string `json:"requirement"`
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
	web := findClient(realm, "passage-web")
	if web == nil || !web.PublicClient || web.Attributes["pkce.code.challenge.method"] != "S256" {
		t.Fatal("passage-web must be a public client with PKCE S256")
	}
	if !slices.Equal(web.RedirectURIs, []string{webCallbackURL}) || web.Attributes["post.logout.redirect.uris"] == "" {
		t.Errorf("passage-web redirectUris = %v, want exactly %s plus explicit post.logout.redirect.uris", web.RedirectURIs, webCallbackURL)
	}
}

func TestKeycloakRealmRequiresMFAAndHoldsNoSecrets(t *testing.T) {
	realm := loadRealm(t)
	var bound *realmFlow
	for i := range realm.AuthenticationFlows {
		if realm.AuthenticationFlows[i].Alias == realm.BrowserFlow {
			bound = &realm.AuthenticationFlows[i]
		}
	}
	if bound == nil {
		t.Fatalf("browserFlow %q is not defined in the export", realm.BrowserFlow)
	}
	if !secondFactorRequired(realm, bound) {
		t.Error("bound browser flow must require a second-factor subflow: WebAuthn or a required OTP form, as alternatives")
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
	for _, name := range []string{"tenant_id", "legal_entity_id", "customer_id", "vendor_id", "duties", "tier"} {
		if !locked[name] {
			t.Errorf("user attribute %s feeds a token claim and must be admin-edit only", name)
		}
	}
}

// TestKeycloakRealmFederationKeepsMFAAndTokenClaimsLocal checks that a federated
// login still needs the second factor, that the tenant comes from the realm (a
// hardcoded mapper) and that no upstream IdP claim can write a token attribute.
func TestKeycloakRealmFederationKeepsMFAAndTokenClaimsLocal(t *testing.T) {
	realm := loadRealm(t)
	if realm.OTPType != "totp" || realm.OTPAlgorithm != "HmacSHA1" || realm.OTPDigits != 6 || realm.OTPPeriod != 30 || realm.OTPCodeReusable {
		t.Errorf("OTP policy %s/%s/%d/%ds reusable=%v, want totp/HmacSHA1/6/30s single-use (the CI login computes codes with it)",
			realm.OTPType, realm.OTPAlgorithm, realm.OTPDigits, realm.OTPPeriod, realm.OTPCodeReusable)
	}
	tokenAttributes := map[string]bool{"tenant_id": true, "legal_entity_id": true, "duties": true, "tier": true}
	for _, idp := range realm.IdentityProviders {
		post := findFlow(realm, idp.PostBrokerFlow)
		if post == nil || !secondFactorRequired(realm, post) {
			t.Errorf("IdP %s postBrokerLoginFlowAlias %q must require the second factor", idp.Alias, idp.PostBrokerFlow)
		}
		var tenantMapped bool
		for _, mapper := range realm.IdPMappers {
			if mapper.Alias != idp.Alias {
				continue
			}
			attribute := mapper.Config["attribute"] + mapper.Config["user.attribute"]
			switch {
			case mapper.Mapper == "hardcoded-attribute-idp-mapper" && attribute == "tenant_id":
				tenantMapped = mapper.Config["syncMode"] == "FORCE"
			case tokenAttributes[attribute]:
				t.Errorf("IdP %s mapper %s lets the upstream IdP set token attribute %s", idp.Alias, mapper.Mapper, attribute)
			}
		}
		if !tenantMapped {
			t.Errorf("IdP %s needs a FORCE hardcoded tenant_id mapper, or federated users get no tid", idp.Alias)
		}
	}
}

// TestKeycloakRealmDeprovisionClientHoldsManageUsersOnly checks the service account identity's deprovisioning
// worker uses: confidential, client credentials only, no secret in the export, and realm-management manage-users
// as its only role and its only scope (fullScopeAllowed off), so its tokens can manage this realm's users and
// nothing else.
func TestKeycloakRealmDeprovisionClientHoldsManageUsersOnly(t *testing.T) {
	realm := loadRealm(t)
	client := findClient(realm, deprovisionClientID)
	if client == nil {
		t.Fatalf("client %s missing", deprovisionClientID)
	}
	if client.PublicClient || !client.ServiceAccount || client.StandardFlow || client.ImplicitFlow || client.DirectGrants || client.FullScope == nil || *client.FullScope {
		t.Errorf("%s must be confidential with only the service account grant and fullScopeAllowed false: %+v", deprovisionClientID, client)
	}
	if len(client.RedirectURIs) != 0 || len(client.ProtocolMapper) != 0 {
		t.Errorf("%s needs no redirect URIs or token mappers: %v %v", deprovisionClientID, client.RedirectURIs, client.ProtocolMapper)
	}
	var account *realmUser
	for i := range realm.Users {
		switch realm.Users[i].ServiceAccount {
		case deprovisionClientID:
			account = &realm.Users[i]
		case "":
			t.Errorf("the export must not create person accounts, found %s", realm.Users[i].Username)
		}
	}
	if account == nil || len(account.RealmRoles) != 0 || len(account.Credentials) != 0 || len(account.ClientRoles) != 1 || !slices.Equal(account.ClientRoles["realm-management"], []string{"manage-users"}) {
		t.Fatalf("service account of %s = %+v, want realm-management manage-users only", deprovisionClientID, account)
	}
	scoped := map[string][]string{}
	for owner, mappings := range realm.ClientScopeMappings {
		for _, m := range mappings {
			if m.Client == deprovisionClientID {
				scoped[owner] = m.Roles
			}
		}
	}
	if len(scoped) != 1 || !slices.Equal(scoped["realm-management"], []string{"manage-users"}) {
		t.Errorf("scope mappings of %s = %v, want realm-management manage-users only", deprovisionClientID, scoped)
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

func findFlow(realm realmExport, alias string) *realmFlow {
	i := slices.IndexFunc(realm.AuthenticationFlows, func(f realmFlow) bool { return f.Alias == alias })
	if i < 0 {
		return nil
	}
	return &realm.AuthenticationFlows[i]
}

// secondFactorFlow reports whether flow offers exactly the two second factors as alternatives:
// webauthn-authenticator, and a subflow that requires auth-otp-form. Keycloak runs the alternative
// the user has a credential for; a user with neither falls through to the OTP form, which sends
// them to CONFIGURE_TOTP, so no alternative lets a password alone through.
func secondFactorFlow(realm realmExport, flow *realmFlow) bool {
	var webauthn, otp bool
	for _, exec := range flow.Executions {
		if exec.Requirement != "ALTERNATIVE" {
			return false
		}
		sub := findFlow(realm, exec.FlowAlias)
		switch {
		case exec.Authenticator == "webauthn-authenticator":
			webauthn = true
		case sub != nil && len(sub.Executions) == 1 && sub.Executions[0].Authenticator == "auth-otp-form" && sub.Executions[0].Requirement == "REQUIRED":
			otp = true
		default:
			return false
		}
	}
	return webauthn && otp
}

func secondFactorRequired(realm realmExport, flow *realmFlow) bool {
	for _, exec := range flow.Executions {
		sub := findFlow(realm, exec.FlowAlias)
		if sub == nil {
			continue
		}
		if exec.Requirement == "REQUIRED" && secondFactorFlow(realm, sub) {
			return true
		}
		if secondFactorRequired(realm, sub) {
			return true
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

func keycloakAdminOrSkip(t *testing.T) (*keycloakAdmin, string, *http.Client) {
	t.Helper()
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
	return admin, base, client
}

func TestKeycloakLoginIsAcceptedByIdentityJWTVerifier(t *testing.T) {
	admin, base, client := keycloakAdminOrSkip(t)

	var clients []map[string]any
	if err := json.Unmarshal(admin.do(http.MethodGet, "/admin/realms/"+realmName+"/clients?clientId=passage-web", nil, http.StatusOK), &clients); err != nil || len(clients) != 1 {
		t.Fatalf("passage-web not imported: %v %v", err, clients)
	}
	clients[0]["directAccessGrantsEnabled"] = true
	admin.do(http.MethodPut, "/admin/realms/"+realmName+"/clients/"+clients[0]["id"].(string), clients[0], http.StatusNoContent)

	admin.do(http.MethodPost, "/admin/realms/"+realmName+"/users", map[string]any{
		"username": loginUserName, "email": loginUserName + "@example.test", "emailVerified": true, "enabled": true,
		"firstName": "Ci", "lastName": "Login", "requiredActions": []string{},
		"attributes":  map[string][]string{"tenant_id": {loginTenantID}, "duties": {loginDuties}, "tier": {"starter"}},
		"credentials": []map[string]any{{"type": "password", "value": loginUserSecret, "temporary": false}},
	}, http.StatusCreated)

	var created []map[string]any
	if err := json.Unmarshal(admin.do(http.MethodGet, "/admin/realms/"+realmName+"/users?username="+loginUserName+"&exact=true", nil, http.StatusOK), &created); err != nil || len(created) != 1 {
		t.Fatalf("created user not found: %v %v", err, created)
	}
	admin.do(http.MethodPut, "/admin/realms/"+realmName+"/users/"+created[0]["id"].(string), map[string]any{"requiredActions": []string{}}, http.StatusNoContent)

	accessToken := tokenRequest(t, client, base, realmName, url.Values{
		"grant_type": {"password"}, "client_id": {"passage-web"}, "username": {loginUserName}, "password": {loginUserSecret},
	})
	verifyWithIdentity(t, base, client, accessToken)
	claims := tokenClaims(t, accessToken)
	if claims["duties"] != loginDuties || claims["tier"] != "starter" {
		t.Fatalf("claims duties/tier = %v/%v, want the single-string values", claims["duties"], claims["tier"])
	}
}

// verifyWithIdentity passes a Keycloak access token through identity's
// JWTVerifier middleware and returns the principal it derives.
func verifyWithIdentity(t *testing.T, base string, client *http.Client, accessToken string) tenant.Principal {
	t.Helper()
	return verifyWithIdentityIssuer(t, base, base, client, accessToken)
}

// verifyWithIdentityIssuer is verifyWithIdentity for a token whose issuer is the Keycloak host the
// browser used (issuerBase), with the keys fetched from base. NewJWTVerifier refuses a plain-http
// issuer on a non-loopback host, so the test sets that issuer on the verifier after creating it.
func verifyWithIdentityIssuer(t *testing.T, base, issuerBase string, client *http.Client, accessToken string) tenant.Principal {
	t.Helper()
	issuer := base + "/realms/" + realmName
	verifier, err := NewJWTVerifier(issuer, realmAudience, loginTenantID, issuer+"/protocol/openid-connect/certs", client)
	if err != nil {
		t.Fatal(err)
	}
	verifier.issuer = issuerBase + "/realms/" + realmName
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
	return principal
}

func tokenClaims(t *testing.T, accessToken string) map[string]any {
	t.Helper()
	parts := strings.Split(accessToken, ".")
	if len(parts) != 3 {
		t.Fatalf("access token has %d parts", len(parts))
	}
	payload, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		t.Fatal(err)
	}
	var claims map[string]any
	if err := json.Unmarshal(payload, &claims); err != nil {
		t.Fatal(err)
	}
	return claims
}

var formAction = regexp.MustCompile(`action="([^"]*)"`)

// httpLocalJar stores cookies that Keycloak marks Secure even when the test
// talks to start-dev over http://localhost, so AUTH_SESSION survives the login POST.
type httpLocalJar struct{ http.CookieJar }

func (j httpLocalJar) SetCookies(u *url.URL, cookies []*http.Cookie) {
	plain := make([]*http.Cookie, len(cookies))
	for i, c := range cookies {
		cp := *c
		if u.Scheme == "http" {
			cp.Secure = false
			if cp.SameSite == http.SameSiteNoneMode {
				cp.SameSite = http.SameSiteLaxMode
			}
		}
		plain[i] = &cp
	}
	j.CookieJar.SetCookies(u, plain)
}

func browserPasswordLogin(t *testing.T, base, username, password string) *http.Response {
	t.Helper()
	inner, err := cookiejar.New(nil)
	if err != nil {
		t.Fatal(err)
	}
	noRedirect := &http.Client{Timeout: 15 * time.Second, Jar: httpLocalJar{inner}, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	query := url.Values{
		"client_id": {"passage-web"}, "response_type": {"code"}, "scope": {"openid"}, "redirect_uri": {webCallbackURL},
		"code_challenge": {"E9Melhoa2OwvFrEMTJguCHaoeK1t8URWbuGJSstw-cM"}, "code_challenge_method": {"S256"}, "state": {"s1"},
	}
	page, err := noRedirect.Get(base + "/realms/" + realmName + "/protocol/openid-connect/auth?" + query.Encode())
	if err != nil {
		t.Fatal(err)
	}
	var body bytes.Buffer
	_, _ = body.ReadFrom(page.Body)
	_ = page.Body.Close()
	match := formAction.FindSubmatch(body.Bytes())
	if page.StatusCode != http.StatusOK || match == nil {
		t.Fatalf("login page: status %d, no form action", page.StatusCode)
	}
	resp, err := noRedirect.PostForm(html.UnescapeString(string(match[1])), url.Values{"username": {username}, "password": {password}})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = resp.Body.Close() })
	return resp
}

func createLoginUser(t *testing.T, admin *keycloakAdmin, username string, clearRequiredActions bool) {
	t.Helper()
	admin.do(http.MethodPost, "/admin/realms/"+realmName+"/users", map[string]any{
		"username": username, "email": username + "@example.test", "emailVerified": true, "enabled": true,
		"firstName": "Ci", "lastName": "Flow",
		"attributes":  map[string][]string{"tenant_id": {loginTenantID}},
		"credentials": []map[string]any{{"type": "password", "value": loginUserSecret, "temporary": false}},
	}, http.StatusCreated)
	if !clearRequiredActions {
		return
	}
	var found []map[string]any
	if err := json.Unmarshal(admin.do(http.MethodGet, "/admin/realms/"+realmName+"/users?username="+username+"&exact=true", nil, http.StatusOK), &found); err != nil || len(found) != 1 {
		t.Fatalf("user %s not found: %v", username, err)
	}
	admin.do(http.MethodPut, "/admin/realms/"+realmName+"/users/"+found[0]["id"].(string), map[string]any{"requiredActions": []string{}}, http.StatusNoContent)
}

func TestKeycloakBrowserLoginEnrolsOTPForUserWithoutFactor(t *testing.T) {
	admin, base, _ := keycloakAdminOrSkip(t)
	createLoginUser(t, admin, "ci-new-user", false)
	resp := browserPasswordLogin(t, base, "ci-new-user", loginUserSecret)
	location := resp.Header.Get("Location")
	if resp.StatusCode != http.StatusFound || !strings.Contains(location, "required-action") || !strings.Contains(location, "execution=CONFIGURE_TOTP") {
		var got bytes.Buffer
		_, _ = got.ReadFrom(resp.Body)
		t.Fatalf("new user after password: status %d location %q body %q, want 302 to required-action CONFIGURE_TOTP", resp.StatusCode, location, got.String())
	}
}

func TestKeycloakBrowserLoginNeverCompletesOnPasswordAlone(t *testing.T) {
	admin, base, _ := keycloakAdminOrSkip(t)
	createLoginUser(t, admin, "ci-no-factor-user", true)
	resp := browserPasswordLogin(t, base, "ci-no-factor-user", loginUserSecret)
	if location := resp.Header.Get("Location"); strings.HasPrefix(location, webCallbackURL) || strings.Contains(location, "code=") {
		t.Fatalf("password-only login completed: status %d location %q", resp.StatusCode, location)
	}
}

// TestKeycloakRealmPasswordPolicyIsFIPSCompatible: Keycloak 26 hashes passwords with argon2 unless told otherwise,
// and argon2 is not supported in FIPS mode; strict FIPS mode also needs passwords of 14 characters or more
// (https://www.keycloak.org/server/fips). The realm pins pbkdf2-sha512 and a length that meets every cloud profile.
func TestKeycloakRealmPasswordPolicyIsFIPSCompatible(t *testing.T) {
	realm := loadRealm(t)
	policy := realm.PasswordPolicy
	if !strings.Contains(policy, "hashAlgorithm(pbkdf2-sha512)") || strings.Contains(strings.ToLower(policy), "argon2") {
		t.Errorf("passwordPolicy %q must pin hashAlgorithm(pbkdf2-sha512)", policy)
	}
	match := regexp.MustCompile(`length\((\d+)\)`).FindStringSubmatch(policy)
	if match == nil {
		t.Fatalf("passwordPolicy %q has no length()", policy)
	}
	length, _ := strconv.Atoi(match[1])
	for _, name := range cloudprofile.ProfileNames() {
		p, err := cloudprofile.Resolve(name)
		if err != nil {
			t.Fatal(err)
		}
		if p.Keycloak.PasswordHash != "pbkdf2-sha512" || length < p.Keycloak.PasswordMinLength {
			t.Errorf("profile %s needs %s and length >= %d, realm has %q", name, p.Keycloak.PasswordHash, p.Keycloak.PasswordMinLength, policy)
		}
		if p.FIPS && length < 14 {
			t.Errorf("profile %s runs Keycloak in strict FIPS mode: passwords need 14 characters, realm has %d", name, length)
		}
	}
	if !strings.Contains(policy, "notUsername") {
		t.Errorf("passwordPolicy %q lost notUsername", policy)
	}
}

// TestKeycloakRealmKeepsLoginAndAdminEvents: FedRAMP AU controls need the login and admin audit trail on, with a set
// retention that meets every cloud profile (#601).
func TestKeycloakRealmKeepsLoginAndAdminEvents(t *testing.T) {
	realm := loadRealm(t)
	if !realm.EventsEnabled || !realm.AdminEventsEnabled || !realm.AdminEventDetails {
		t.Errorf("login events %v, admin events %v, admin event details %v: all three must be on", realm.EventsEnabled, realm.AdminEventsEnabled, realm.AdminEventDetails)
	}
	if realm.EventsExpiration <= 0 {
		t.Fatal("eventsExpiration must set a retention in seconds")
	}
	days := realm.EventsExpiration / 86400
	for _, name := range cloudprofile.ProfileNames() {
		p, err := cloudprofile.Resolve(name)
		if err != nil {
			t.Fatal(err)
		}
		if days < p.Keycloak.EventRetentionDays {
			t.Errorf("profile %s keeps login events %d days, realm keeps %d", name, p.Keycloak.EventRetentionDays, days)
		}
	}
	for _, want := range []string{"LOGIN", "LOGIN_ERROR", "LOGOUT", "UPDATE_PASSWORD", "UPDATE_TOTP", "IDENTITY_PROVIDER_LOGIN", "USER_DISABLED_BY_PERMANENT_LOCKOUT"} {
		if !slices.Contains(realm.EnabledEventTypes, want) {
			t.Errorf("enabledEventTypes lacks %s", want)
		}
	}
}

// TestEntraIdPTemplatesKeepMFAAndTokenClaimsLocal applies the federation rules of
// TestKeycloakRealmFederationKeepsMFAAndTokenClaimsLocal to the Entra ID provider rendered from each cloud profile
// (deploy/keycloak/idp): the second factor still runs after the broker login, the tenant comes from the realm through
// a FORCE hardcoded mapper, and no mapper lets an Entra claim write tenant_id, legal_entity_id, duties or tier.
func TestEntraIdPTemplatesKeepMFAAndTokenClaimsLocal(t *testing.T) {
	realm := loadRealm(t)
	tokenAttributes := map[string]bool{"tenant_id": true, "legal_entity_id": true, "duties": true, "tier": true}
	files, err := filepath.Glob("../../deploy/keycloak/idp/entra-oidc.*.json")
	if err != nil || len(files) != len(cloudprofile.ProfileNames()) {
		t.Fatalf("want one rendered Entra IdP per profile (%v), got %v %v", cloudprofile.ProfileNames(), files, err)
	}
	for _, file := range files {
		raw, err := os.ReadFile(file)
		if err != nil {
			t.Fatal(err)
		}
		var export struct {
			Provider realmIdP         `json:"identityProvider"`
			Mappers  []realmIdPMapper `json:"identityProviderMappers"`
		}
		if err := json.Unmarshal(raw, &export); err != nil {
			t.Fatalf("%s: %v", file, err)
		}
		idp := export.Provider
		if idp.Enabled {
			t.Errorf("%s: the shipped example must be disabled until a tenant and client are set", file)
		}
		if post := findFlow(realm, idp.PostBrokerFlow); post == nil || !secondFactorRequired(realm, post) {
			t.Errorf("%s: postBrokerLoginFlowAlias %q must require the second factor", file, idp.PostBrokerFlow)
		}
		if idp.Config["pkceEnabled"] != "true" || idp.Config["pkceMethod"] != "S256" {
			t.Errorf("%s: authorization code with PKCE S256 required", file)
		}
		for key := range idp.Config {
			if strings.Contains(strings.ToLower(key), "secret") {
				t.Errorf("%s: config %s must not hold a secret", file, key)
			}
		}
		var tenantMapped bool
		for _, mapper := range export.Mappers {
			attribute := mapper.Config["attribute"] + mapper.Config["user.attribute"]
			switch {
			case mapper.Mapper == "hardcoded-attribute-idp-mapper" && attribute == "tenant_id":
				tenantMapped = mapper.Config["syncMode"] == "FORCE"
			case tokenAttributes[attribute]:
				t.Errorf("%s: mapper %s lets Entra set token attribute %s", file, mapper.Mapper, attribute)
			case mapper.Config["claim"] == "tid":
				t.Errorf("%s: a mapper copies the Entra tid claim; the Passage tid stays bound to the realm", file)
			}
		}
		if !tenantMapped {
			t.Errorf("%s: needs a FORCE hardcoded tenant_id mapper", file)
		}
	}
}
