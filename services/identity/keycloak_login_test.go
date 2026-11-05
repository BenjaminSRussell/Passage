package identity

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha1" //nolint:gosec // RFC 6238 TOTP with the realm's HmacSHA1 policy, not a security hash here.
	"crypto/sha256"
	"encoding/base64"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"html"
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"regexp"
	"slices"
	"strings"
	"testing"
	"time"
)

// These tests drive real Keycloak logins. They run only when
// PASSAGE_TEST_KEYCLOAK_URL is set (the keycloak job in .github/workflows/ci.yml).

const (
	samlEntityID = "https://auth.passage.example.invalid/realms/passage"
	otpPeriod    = 30
)

var (
	htmlForm   = regexp.MustCompile(`(?is)<form\b([^>]*)>(.*?)</form>`)
	htmlInput  = regexp.MustCompile(`(?is)<input\b[^>]*>`)
	htmlAttr   = func(name string) *regexp.Regexp { return regexp.MustCompile(`(?is)\b` + name + `\s*=\s*"([^"]*)"`) }
	htmlName   = htmlAttr("name")
	htmlValue  = htmlAttr("value")
	htmlAction = htmlAttr("action")
)

type pageForm struct {
	action string
	fields url.Values
}

func pageForms(body string) []pageForm {
	var forms []pageForm
	for _, form := range htmlForm.FindAllStringSubmatch(body, -1) {
		action := htmlAction.FindStringSubmatch(form[1])
		if action == nil {
			continue
		}
		fields := url.Values{}
		for _, input := range htmlInput.FindAllString(form[2], -1) {
			name := htmlName.FindStringSubmatch(input)
			if name == nil {
				continue
			}
			value := ""
			if v := htmlValue.FindStringSubmatch(input); v != nil {
				value = html.UnescapeString(v[1])
			}
			fields.Set(html.UnescapeString(name[1]), value)
		}
		forms = append(forms, pageForm{action: html.UnescapeString(action[1]), fields: fields})
	}
	return forms
}

// totp holds one user's TOTP secret (the raw totpSecret Keycloak shows at
// enrolment) and the last time step used, because the realm rejects reused codes.
type totp struct {
	secret []byte
	last   int64
}

func totpCode(secret []byte, step int64) string {
	var msg [8]byte
	binary.BigEndian.PutUint64(msg[:], uint64(step))
	mac := hmac.New(sha1.New, secret)
	mac.Write(msg[:])
	sum := mac.Sum(nil)
	offset := sum[len(sum)-1] & 0x0f
	return fmt.Sprintf("%06d", (binary.BigEndian.Uint32(sum[offset:offset+4])&0x7fffffff)%1000000)
}

// next returns a code for a step not used before, within the realm's one-step look-ahead.
func (o *totp) next() string {
	for {
		now := time.Now().Unix() / otpPeriod
		step := max(now, o.last+1)
		if step <= now+1 {
			o.last = step
			return totpCode(o.secret, step)
		}
		time.Sleep(time.Until(time.Unix((now+1)*otpPeriod, 0)) + 100*time.Millisecond)
	}
}

type loginUser struct {
	username, password string
	otp                totp
}

type browserLogin struct {
	t      *testing.T
	base   string
	client *http.Client
	steps  []string // the pages answered, in order
}

func newBrowserLogin(t *testing.T, base string) *browserLogin {
	t.Helper()
	jar, err := cookiejar.New(nil)
	if err != nil {
		t.Fatal(err)
	}
	return &browserLogin{t: t, base: base, client: &http.Client{
		Timeout: 15 * time.Second, Jar: httpLocalJar{jar},
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}}
}

// settle follows redirects until Keycloak answers with a page, or redirects to
// the passage-web callback (returned as callback).
func (b *browserLogin) settle(resp *http.Response) (callback, page string) {
	b.t.Helper()
	for range 20 {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
		_ = resp.Body.Close()
		if resp.StatusCode < 300 || resp.StatusCode >= 400 {
			if resp.StatusCode != http.StatusOK {
				b.t.Fatalf("%s %s: status %d: %.400s", resp.Request.Method, resp.Request.URL, resp.StatusCode, body)
			}
			return "", string(body)
		}
		next, err := resp.Request.URL.Parse(resp.Header.Get("Location"))
		if err != nil {
			b.t.Fatal(err)
		}
		if strings.HasPrefix(next.String(), webCallbackURL) {
			return next.String(), ""
		}
		if resp, err = b.client.Get(next.String()); err != nil {
			b.t.Fatal(err)
		}
	}
	b.t.Fatal("more than 20 redirects")
	return "", ""
}

// login runs the authorization-code flow with PKCE for passage-web, answering
// the password form, TOTP enrolment, the OTP form, SAML POST bindings and the
// first-broker-login profile review. It returns the callback query, or nil when
// Keycloak keeps showing the password form (the login was refused).
func (b *browserLogin) login(user *loginUser, idpHint string) (url.Values, string) {
	b.t.Helper()
	authURL, verifier, state := authorizationRequest(b.base, idpHint)
	resp, err := b.client.Get(authURL)
	if err != nil {
		b.t.Fatal(err)
	}
	callback, page := b.settle(resp)
	passwordForms := 0
	for step := 0; callback == "" && step < 12; step++ {
		form, answer := b.answer(page, user)
		if form == nil {
			b.t.Fatalf("login step %d: no form this test knows how to answer: %.600s", step, page)
		}
		b.steps = append(b.steps, answer)
		if answer == "password" {
			if passwordForms++; passwordForms > 1 {
				return nil, page
			}
		}
		if resp, err = b.client.PostForm(form.action, form.fields); err != nil {
			b.t.Fatal(err)
		}
		callback, page = b.settle(resp)
	}
	if callback == "" {
		b.t.Fatalf("login did not reach %s: %.600s", webCallbackURL, page)
	}
	return callbackQuery(b.t, callback, state), verifier
}

// authorizationRequest builds passage-web's authorization-code request with PKCE S256 against
// base and returns its URL, the code verifier and the state.
func authorizationRequest(base, idpHint string) (authURL, verifier, state string) {
	verifierBytes := make([]byte, 32)
	_, _ = rand.Read(verifierBytes)
	verifier = base64.RawURLEncoding.EncodeToString(verifierBytes)
	challenge := sha256.Sum256([]byte(verifier))
	state = "s-" + verifier[:8]
	query := url.Values{
		"client_id": {"passage-web"}, "response_type": {"code"}, "scope": {"openid"}, "redirect_uri": {webCallbackURL},
		"code_challenge": {base64.RawURLEncoding.EncodeToString(challenge[:])}, "code_challenge_method": {"S256"}, "state": {state},
	}
	if idpHint != "" {
		query.Set("kc_idp_hint", idpHint)
	}
	return base + "/realms/" + realmName + "/protocol/openid-connect/auth?" + query.Encode(), verifier, state
}

// callbackQuery checks that the passage-web callback carries the request's state and a code.
func callbackQuery(t *testing.T, callback, state string) url.Values {
	t.Helper()
	parsed, err := url.Parse(callback)
	if err != nil {
		t.Fatal(err)
	}
	if parsed.Query().Get("state") != state || parsed.Query().Get("code") == "" {
		t.Fatalf("callback %s: want state %s and a code", callback, state)
	}
	return parsed.Query()
}

func (b *browserLogin) answer(page string, user *loginUser) (*pageForm, string) {
	for _, form := range pageForms(page) {
		f := form.fields
		switch {
		case f.Has("SAMLResponse") || f.Has("SAMLRequest"):
			return &form, "saml"
		case f.Has("totpSecret"):
			user.otp = totp{secret: []byte(f.Get("totpSecret"))}
			f.Set("totp", user.otp.next())
			f.Set("userLabel", "ci")
			return &form, "enrol-totp"
		case f.Has("otp"):
			if len(user.otp.secret) == 0 {
				b.t.Fatal("OTP form shown for a user with no enrolled secret")
			}
			f.Set("otp", user.otp.next())
			return &form, "otp"
		case f.Has("password"):
			f.Set("username", user.username)
			f.Set("password", user.password)
			return &form, "password"
		case f.Has("email") || f.Has("firstName"):
			for key, value := range map[string]string{"email": user.username + "@example.test", "firstName": "Ci", "lastName": "Federated"} {
				if f.Get(key) == "" {
					f.Set(key, value)
				}
			}
			return &form, "review-profile"
		}
	}
	return nil, ""
}

type tokenSet struct {
	AccessToken  string `json:"access_token"`
	RefreshToken string `json:"refresh_token"`
	Error        string `json:"error"`
	Description  string `json:"error_description"`
}

func (b *browserLogin) token(form url.Values) (int, tokenSet) {
	b.t.Helper()
	form.Set("client_id", "passage-web")
	resp, err := b.client.PostForm(b.base+"/realms/"+realmName+"/protocol/openid-connect/token", form)
	if err != nil {
		b.t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	var tokens tokenSet
	if err := json.NewDecoder(resp.Body).Decode(&tokens); err != nil {
		b.t.Fatalf("token response: %v", err)
	}
	return resp.StatusCode, tokens
}

func (b *browserLogin) exchange(callback url.Values, verifier string) tokenSet {
	b.t.Helper()
	status, tokens := b.token(url.Values{
		"grant_type": {"authorization_code"}, "code": {callback.Get("code")}, "redirect_uri": {webCallbackURL}, "code_verifier": {verifier},
	})
	if status != http.StatusOK || tokens.AccessToken == "" || tokens.RefreshToken == "" {
		b.t.Fatalf("code exchange: status %d %s %s", status, tokens.Error, tokens.Description)
	}
	return tokens
}

func randomSuffix(t *testing.T) string {
	t.Helper()
	raw := make([]byte, 4)
	if _, err := rand.Read(raw); err != nil {
		t.Fatal(err)
	}
	return hex.EncodeToString(raw)
}

// TestKeycloakOIDCLoginWithOTPThenDeprovision is the AC3 local-account path:
// authorization code with PKCE, TOTP enrolment, a second login through the OTP
// form, identity accepting the token with the duty claims, and then the user
// disabled in Keycloak losing refresh and fresh login.
func TestKeycloakOIDCLoginWithOTPThenDeprovision(t *testing.T) {
	admin, base, client := keycloakAdminOrSkip(t)
	user := &loginUser{username: "ci-otp-" + randomSuffix(t), password: loginUserSecret}
	admin.do(http.MethodPost, "/admin/realms/"+realmName+"/users", map[string]any{
		"username": user.username, "email": user.username + "@example.test", "emailVerified": true, "enabled": true,
		"firstName": "Ci", "lastName": "Otp",
		"attributes":  map[string][]string{"tenant_id": {loginTenantID}, "duties": {loginDuties}, "tier": {"starter"}},
		"credentials": []map[string]any{{"type": "password", "value": user.password, "temporary": false}},
	}, http.StatusCreated)

	enrol := newBrowserLogin(t, base)
	callback, verifier := enrol.login(user, "")
	if !slices.Equal(enrol.steps, []string{"password", "enrol-totp"}) {
		t.Fatalf("first login answered %v, want password then TOTP enrolment", enrol.steps)
	}
	first := enrol.exchange(callback, verifier)
	verifyWithIdentity(t, base, client, first.AccessToken)

	again := newBrowserLogin(t, base)
	callback, verifier = again.login(user, "")
	if callback == nil || !slices.Equal(again.steps, []string{"password", "otp"}) {
		t.Fatalf("second login answered %v (completed %v), want password then the OTP form", again.steps, callback != nil)
	}
	tokens := again.exchange(callback, verifier)
	principal := verifyWithIdentity(t, base, client, tokens.AccessToken)
	claims := tokenClaims(t, tokens.AccessToken)
	if claims["duties"] != loginDuties || claims["tier"] != "starter" || claims["acr"] == nil {
		t.Fatalf("claims duties/tier/acr = %v/%v/%v, want %q/starter and an acr", claims["duties"], claims["tier"], claims["acr"], loginDuties)
	}
	if status, refreshed := again.token(url.Values{"grant_type": {"refresh_token"}, "refresh_token": {tokens.RefreshToken}}); status != http.StatusOK {
		t.Fatalf("refresh before deprovisioning: status %d %s", status, refreshed.Description)
	} else {
		tokens = refreshed
	}

	admin.do(http.MethodPut, "/admin/realms/"+realmName+"/users/"+principal.UserID, map[string]any{"enabled": false}, http.StatusNoContent)
	if status, refreshed := again.token(url.Values{"grant_type": {"refresh_token"}, "refresh_token": {tokens.RefreshToken}}); status != http.StatusBadRequest || refreshed.Error != "invalid_grant" {
		t.Fatalf("refresh after disabling the user: status %d error %q, want 400 invalid_grant", status, refreshed.Error)
	}
	if callback, _ := newBrowserLogin(t, base).login(user, ""); callback != nil {
		t.Fatal("disabled user completed a fresh login")
	}
}

// TestKeycloakFederatedLoginThroughOIDCAndSAMLIdPs is the AC3 two-IdP login
// with Keycloak-to-Keycloak stand-ins: two upstream realms in the same server,
// one wired to corporate-oidc and one to corporate-saml. Each federated user
// must enrol TOTP through passage-post-broker-mfa and get a token identity
// accepts, with tid from the realm's hardcoded mapper and no duties.
func TestKeycloakFederatedLoginThroughOIDCAndSAMLIdPs(t *testing.T) {
	admin, base, client := keycloakAdminOrSkip(t)
	for _, alias := range []string{"corporate-oidc", "corporate-saml"} {
		t.Run(alias, func(t *testing.T) {
			upstream := "ci-upstream-" + strings.TrimPrefix(alias, "corporate-")
			admin.do(http.MethodDelete, "/admin/realms/"+upstream, nil, http.StatusNoContent, http.StatusNotFound)
			admin.do(http.MethodPost, "/admin/realms", map[string]any{"realm": upstream, "enabled": true}, http.StatusCreated)
			t.Cleanup(func() { admin.do(http.MethodDelete, "/admin/realms/"+upstream, nil, http.StatusNoContent) })
			user := &loginUser{username: "ci-fed-" + randomSuffix(t), password: loginUserSecret}
			admin.do(http.MethodPost, "/admin/realms/"+upstream+"/users", map[string]any{
				"username": user.username, "email": user.username + "@upstream.example.test", "emailVerified": true, "enabled": true,
				"firstName": "Ci", "lastName": "Upstream", "requiredActions": []string{},
				"credentials": []map[string]any{{"type": "password", "value": user.password, "temporary": false}},
			}, http.StatusCreated)

			issuer := base + "/realms/" + upstream
			endpoint := base + "/realms/" + realmName + "/broker/" + alias + "/endpoint"
			config := map[string]string{}
			if alias == "corporate-oidc" {
				secret := "ci-" + randomSuffix(t) + randomSuffix(t)
				admin.do(http.MethodPost, "/admin/realms/"+upstream+"/clients", map[string]any{
					"clientId": "passage-broker", "protocol": "openid-connect", "publicClient": false, "secret": secret,
					"standardFlowEnabled": true, "directAccessGrantsEnabled": false, "redirectUris": []string{endpoint},
				}, http.StatusCreated)
				config = map[string]string{
					"authorizationUrl": issuer + "/protocol/openid-connect/auth", "tokenUrl": issuer + "/protocol/openid-connect/token",
					"jwksUrl": issuer + "/protocol/openid-connect/certs", "issuer": issuer, "clientId": "passage-broker", "clientSecret": secret,
				}
			} else {
				admin.do(http.MethodPost, "/admin/realms/"+upstream+"/clients", map[string]any{
					"clientId": samlEntityID, "protocol": "saml", "redirectUris": []string{endpoint},
					"attributes": map[string]string{
						"saml_assertion_consumer_url_post": endpoint, "saml.assertion.signature": "true", "saml.server.signature": "true",
						"saml.client.signature": "false", "saml_name_id_format": "persistent", "saml_force_name_id_format": "true",
					},
				}, http.StatusCreated)
				var keys struct {
					Keys []struct {
						Algorithm   string `json:"algorithm"`
						Use         string `json:"use"`
						Certificate string `json:"certificate"`
					} `json:"keys"`
				}
				if err := json.Unmarshal(admin.do(http.MethodGet, "/admin/realms/"+upstream+"/keys", nil, http.StatusOK), &keys); err != nil {
					t.Fatal(err)
				}
				for _, key := range keys.Keys {
					if key.Algorithm == "RS256" && key.Use == "SIG" && key.Certificate != "" {
						config["signingCertificate"] = key.Certificate
					}
				}
				if config["signingCertificate"] == "" {
					t.Fatalf("upstream realm %s has no RS256 signing certificate", upstream)
				}
				config["singleSignOnServiceUrl"] = issuer + "/protocol/saml"
				config["idpEntityId"] = issuer
			}
			configureFederation(t, admin, alias, config)

			login := newBrowserLogin(t, base)
			callback, verifier := login.login(user, alias)
			if callback == nil {
				t.Fatal("federated login refused")
			}
			if !slices.Contains(login.steps, "enrol-totp") || (alias == "corporate-saml") != slices.Contains(login.steps, "saml") {
				t.Fatalf("federated login answered %v, want the upstream login, the SAML POST binding for SAML only, and the post-broker TOTP enrolment", login.steps)
			}
			t.Logf("%s login answered %v", alias, login.steps)
			tokens := login.exchange(callback, verifier)
			principal := verifyWithIdentity(t, base, client, tokens.AccessToken)
			if claims := tokenClaims(t, tokens.AccessToken); claims["duties"] != nil || claims["tier"] != nil {
				t.Fatalf("federated token carries duties/tier %v/%v the upstream IdP must not set", claims["duties"], claims["tier"])
			}
			var links []struct {
				IdentityProvider string `json:"identityProvider"`
			}
			if err := json.Unmarshal(admin.do(http.MethodGet, "/admin/realms/"+realmName+"/users/"+principal.UserID+"/federated-identity", nil, http.StatusOK), &links); err != nil || len(links) != 1 || links[0].IdentityProvider != alias {
				t.Fatalf("passage user %s federated links = %v (%v), want one link to %s", principal.UserID, links, err, alias)
			}
		})
	}
}

// configureFederation points a shipped, disabled IdP placeholder at a stand-in
// upstream, sets the tenant mapper to the test tenant and enables it; cleanup
// disables it again so the realm is back to its exported state.
func configureFederation(t *testing.T, admin *keycloakAdmin, alias string, config map[string]string) {
	t.Helper()
	path := "/admin/realms/" + realmName + "/identity-provider/instances/" + alias
	var idp map[string]any
	if err := json.Unmarshal(admin.do(http.MethodGet, path, nil, http.StatusOK), &idp); err != nil {
		t.Fatal(err)
	}
	current, _ := idp["config"].(map[string]any)
	for key, value := range config {
		current[key] = value
	}
	idp["enabled"] = true
	admin.do(http.MethodPut, path, idp, http.StatusNoContent)
	t.Cleanup(func() {
		idp["enabled"] = false
		admin.do(http.MethodPut, path, idp, http.StatusNoContent)
	})

	var mappers []map[string]any
	if err := json.Unmarshal(admin.do(http.MethodGet, path+"/mappers", nil, http.StatusOK), &mappers); err != nil {
		t.Fatal(err)
	}
	for _, mapper := range mappers {
		if mapper["name"] == "tenant" {
			mapper["config"].(map[string]any)["attribute.value"] = loginTenantID
			admin.do(http.MethodPut, path+"/mappers/"+mapper["id"].(string), mapper, http.StatusNoContent)
			return
		}
	}
	t.Fatalf("IdP %s has no tenant mapper", alias)
}
