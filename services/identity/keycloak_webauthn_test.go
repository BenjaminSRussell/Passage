package identity

import (
	"bufio"
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"regexp"
	"slices"
	"strings"
	"testing"
	"time"
)

const (
	deploymentPath     = "../../deploy/keycloak/deployment.yaml"
	webAuthnDriverPath = "testdata/webauthn_browser.cjs"
)

// webAuthnPolicy is one of the realm's two WebAuthn policies: webAuthnPolicy* (second factor) or
// webAuthnPolicyPasswordless* (passkeys), read from the export with the prefix stripped.
type webAuthnPolicy struct {
	RpEntityName                    string   `json:"RpEntityName"`
	RpID                            string   `json:"RpId"`
	SignatureAlgorithms             []string `json:"SignatureAlgorithms"`
	AttestationConveyancePreference string   `json:"AttestationConveyancePreference"`
	UserVerificationRequirement     string   `json:"UserVerificationRequirement"`
	AvoidSameAuthenticatorRegister  bool     `json:"AvoidSameAuthenticatorRegister"`
	AcceptableAaguids               []string `json:"AcceptableAaguids"`
	ExtraOrigins                    []string `json:"ExtraOrigins"`
	set                             map[string]bool
}

func loadWebAuthnPolicy(t *testing.T, prefix string) webAuthnPolicy {
	t.Helper()
	raw, err := os.ReadFile(realmPath)
	if err != nil {
		t.Fatal(err)
	}
	var all map[string]json.RawMessage
	if err := json.Unmarshal(raw, &all); err != nil {
		t.Fatal(err)
	}
	fields := map[string]json.RawMessage{}
	set := map[string]bool{}
	for key, value := range all {
		name, ok := strings.CutPrefix(key, prefix)
		// webAuthnPolicyPasswordless* also starts with webAuthnPolicy.
		if !ok || (prefix == "webAuthnPolicy" && strings.HasPrefix(name, "Passwordless")) {
			continue
		}
		fields[name] = value
		set[name] = true
	}
	joined, _ := json.Marshal(fields)
	policy := webAuthnPolicy{set: set}
	if err := json.Unmarshal(joined, &policy); err != nil {
		t.Fatalf("%s policy: %v", prefix, err)
	}
	return policy
}

// keycloakHostname is the host of KC_HOSTNAME in the Keycloak deployment, which the WebAuthn RP ID
// must equal: a browser only lets a page use an RP ID that is its own host or a parent of it.
func keycloakHostname(t *testing.T) string {
	t.Helper()
	raw, err := os.ReadFile(deploymentPath)
	if err != nil {
		t.Fatal(err)
	}
	match := regexp.MustCompile(`name: KC_HOSTNAME, value: "([^"]+)"`).FindSubmatch(raw)
	if match == nil {
		t.Fatal("deployment.yaml sets no KC_HOSTNAME")
	}
	parsed, err := url.Parse(string(match[1]))
	if err != nil || parsed.Hostname() == "" {
		t.Fatalf("KC_HOSTNAME %q is not a URL", match[1])
	}
	return parsed.Hostname()
}

// TestKeycloakRealmPinsWebAuthnPolicy checks both WebAuthn policies against the values the issue
// fixes: the RP pinned to Keycloak's hostname, user verification required, no attestation, ES256
// and RS256 only, no extra origins, and every pinned key present so Keycloak's defaults never apply.
func TestKeycloakRealmPinsWebAuthnPolicy(t *testing.T) {
	host := keycloakHostname(t)
	for _, prefix := range []string{"webAuthnPolicy", "webAuthnPolicyPasswordless"} {
		policy := loadWebAuthnPolicy(t, prefix)
		for _, key := range []string{"RpEntityName", "RpId", "SignatureAlgorithms", "AttestationConveyancePreference", "UserVerificationRequirement",
			"AuthenticatorAttachment", "RequireResidentKey", "CreateTimeout", "AvoidSameAuthenticatorRegister", "AcceptableAaguids", "ExtraOrigins"} {
			if !policy.set[key] {
				t.Errorf("%s%s is not pinned in the export", prefix, key)
			}
		}
		if policy.RpEntityName != "Passage" || policy.RpID != host {
			t.Errorf("%s RP = %q/%q, want Passage/%s (the host of KC_HOSTNAME)", prefix, policy.RpEntityName, policy.RpID, host)
		}
		if policy.UserVerificationRequirement != "required" || policy.AttestationConveyancePreference != "none" {
			t.Errorf("%s user verification %q, attestation %q, want required and none", prefix, policy.UserVerificationRequirement, policy.AttestationConveyancePreference)
		}
		if !slices.Equal(policy.SignatureAlgorithms, []string{"ES256", "RS256"}) {
			t.Errorf("%s signature algorithms = %v, want [ES256 RS256]", prefix, policy.SignatureAlgorithms)
		}
		if len(policy.ExtraOrigins) != 0 || len(policy.AcceptableAaguids) != 0 {
			t.Errorf("%s extra origins %v / AAGUIDs %v, want none", prefix, policy.ExtraOrigins, policy.AcceptableAaguids)
		}
	}
	realm := loadRealm(t)
	if !slices.ContainsFunc(realm.RequiredActions, func(a realmAction) bool { return a.Alias == "webauthn-register" && !a.DefaultAction }) {
		t.Error("webauthn-register must be an enabled, non-default required action so an admin can ask a user to register a passkey")
	}
}

// webAuthnBrowser is the Playwright driver in testdata/webauthn_browser.cjs: one Chromium with one
// virtual authenticator (CTAP2, internal transport, user verification) for the whole test.
type webAuthnBrowser struct {
	t   *testing.T
	in  io.WriteCloser
	out *bufio.Scanner
	cmd *exec.Cmd
}

type browserResult struct {
	Callback string   `json:"callback"`
	Steps    []string `json:"steps"`
	Refused  bool     `json:"refused"`
	Error    string   `json:"error"`
	Page     string   `json:"page"`
	URL      string   `json:"url"`
	Count    int      `json:"count"`
	RpIDs    []string `json:"rpIds"`
}

func startWebAuthnBrowser(t *testing.T, browserOrigin, mapTo string) *webAuthnBrowser {
	t.Helper()
	node, err := exec.LookPath("node")
	if err != nil {
		t.Fatalf("%s is set but node is not on PATH: the WebAuthn login needs node with the playwright package and Chromium", keycloakURLEnv)
	}
	if out, err := exec.Command(node, "-e", "require.resolve('playwright')").CombinedOutput(); err != nil {
		t.Fatalf("%s is set but node cannot load playwright (install it and Chromium, as the keycloak CI job does): %v %s", keycloakURLEnv, err, out)
	}
	cmd := exec.Command(node, webAuthnDriverPath)
	cmd.Stderr = os.Stderr
	in, err := cmd.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	b := &webAuthnBrowser{t: t, in: in, out: bufio.NewScanner(stdout), cmd: cmd}
	b.out.Buffer(make([]byte, 0, 64<<10), 4<<20)
	t.Cleanup(func() {
		_ = in.Close()
		done := make(chan struct{})
		go func() { _ = cmd.Wait(); close(done) }()
		select {
		case <-done:
		case <-time.After(15 * time.Second):
			_ = cmd.Process.Kill()
		}
	})
	if res := b.send(map[string]any{"cmd": "start", "origin": browserOrigin, "mapTo": mapTo, "callbackPrefix": webCallbackURL}); res.Error != "" {
		t.Fatalf("start Chromium: %s", res.Error)
	}
	return b
}

func (b *webAuthnBrowser) send(cmd map[string]any) browserResult {
	b.t.Helper()
	line, _ := json.Marshal(cmd)
	if _, err := b.in.Write(append(line, '\n')); err != nil {
		b.t.Fatalf("browser driver: %v", err)
	}
	if !b.out.Scan() {
		b.t.Fatalf("browser driver exited: %v", b.out.Err())
	}
	var res browserResult
	if err := json.Unmarshal(b.out.Bytes(), &res); err != nil {
		b.t.Fatalf("browser driver answered %q: %v", b.out.Text(), err)
	}
	return res
}

// login runs one authorization-code login with PKCE through the browser, from a fresh Keycloak
// session, and returns the tokens, or nil and the steps when Keycloak refused it.
func (b *webAuthnBrowser) login(browserBase, keycloakBase string, user *loginUser, prefer string) (*tokenSet, []string) {
	b.t.Helper()
	b.send(map[string]any{"cmd": "forget-session"})
	authURL, verifier, state := authorizationRequest(browserBase, "")
	res := b.send(map[string]any{"cmd": "login", "url": authURL, "username": user.username, "password": user.password, "prefer": prefer})
	if res.Error != "" {
		b.t.Fatalf("browser login: %s (steps %v, at %s): %.600s", res.Error, res.Steps, res.URL, res.Page)
	}
	if res.Refused {
		b.t.Logf("login refused after %v: %.300s", res.Steps, res.Page)
		return nil, res.Steps
	}
	exchange := newBrowserLogin(b.t, keycloakBase)
	tokens := exchange.exchange(callbackQuery(b.t, res.Callback, state), verifier)
	return &tokens, res.Steps
}

func userCredentialTypes(t *testing.T, admin *keycloakAdmin, userID string) map[string]string {
	t.Helper()
	var credentials []struct {
		ID   string `json:"id"`
		Type string `json:"type"`
	}
	if err := json.Unmarshal(admin.do(http.MethodGet, "/admin/realms/"+realmName+"/users/"+userID+"/credentials", nil, http.StatusOK), &credentials); err != nil {
		t.Fatal(err)
	}
	types := map[string]string{}
	for _, c := range credentials {
		types[c.Type] = c.ID
	}
	return types
}

// TestKeycloakBrowserLoginWithWebAuthnSecondFactor is the WebAuthn half of the MFA flow, in Chromium
// with a virtual authenticator, against the realm's pinned RP ID (the browser reaches Keycloak under
// that host). A user asked to register a passkey enrols it next to TOTP; a user holding both can
// finish the second factor with either; with the OTP credential removed the passkey alone completes
// the login; and an authenticator that cannot verify the user fails the ceremony, because the realm
// asks for user verification (with "preferred" Chromium would send an unverified assertion).
func TestKeycloakBrowserLoginWithWebAuthnSecondFactor(t *testing.T) {
	admin, base, client := keycloakAdminOrSkip(t)
	keycloakURL, err := url.Parse(base)
	if err != nil {
		t.Fatal(err)
	}
	rpID := loadWebAuthnPolicy(t, "webAuthnPolicy").RpID
	browserBase := keycloakURL.Scheme + "://" + rpID
	if keycloakURL.Port() != "" {
		browserBase += ":" + keycloakURL.Port()
	}
	browser := startWebAuthnBrowser(t, browserBase, keycloakURL.Hostname())

	user := &loginUser{username: "ci-webauthn-" + randomSuffix(t), password: loginUserSecret}
	admin.do(http.MethodPost, "/admin/realms/"+realmName+"/users", map[string]any{
		"username": user.username, "email": user.username + "@example.test", "emailVerified": true, "enabled": true,
		"firstName": "Ci", "lastName": "WebAuthn", "requiredActions": []string{"CONFIGURE_TOTP", "webauthn-register"},
		"attributes":  map[string][]string{"tenant_id": {loginTenantID}, "duties": {loginDuties}, "tier": {"starter"}},
		"credentials": []map[string]any{{"type": "password", "value": user.password, "temporary": false}},
	}, http.StatusCreated)

	tokens, steps := browser.login(browserBase, base, user, "webauthn")
	if tokens == nil || !slices.Equal(steps, []string{"password", "enrol-totp", "register-webauthn"}) {
		t.Fatalf("first login answered %v (completed %v), want password, TOTP enrolment and passkey registration", steps, tokens != nil)
	}
	principal := verifyWithIdentityIssuer(t, base, browserBase, client, tokens.AccessToken)
	if types := userCredentialTypes(t, admin, principal.UserID); types["webauthn"] == "" || types["otp"] == "" {
		t.Fatalf("credentials after enrolment = %v, want webauthn and otp", types)
	}
	if held := browser.send(map[string]any{"cmd": "credentials"}); held.Count != 1 || !slices.Equal(held.RpIDs, []string{rpID}) {
		t.Fatalf("virtual authenticator holds %d credentials for %v, want one for %s", held.Count, held.RpIDs, rpID)
	}

	for _, prefer := range []string{"webauthn", "otp"} {
		tokens, steps = browser.login(browserBase, base, user, prefer)
		other := map[string]string{"webauthn": "otp", "otp": "webauthn"}[prefer]
		if tokens == nil || !slices.Contains(steps, prefer) || slices.Contains(steps, other) {
			t.Fatalf("login preferring %s answered %v (completed %v), want the %s step and not %s", prefer, steps, tokens != nil, prefer, other)
		}
		t.Logf("second factor by %s answered %v", prefer, steps)
		verifyWithIdentityIssuer(t, base, browserBase, client, tokens.AccessToken)
	}

	otpID := userCredentialTypes(t, admin, principal.UserID)["otp"]
	admin.do(http.MethodDelete, "/admin/realms/"+realmName+"/users/"+principal.UserID+"/credentials/"+otpID, nil, http.StatusNoContent)
	tokens, steps = browser.login(browserBase, base, user, "webauthn")
	if tokens == nil || !slices.Equal(steps, []string{"password", "webauthn"}) {
		t.Fatalf("passkey-only login answered %v (completed %v), want password then the passkey", steps, tokens != nil)
	}
	verifyWithIdentityIssuer(t, base, browserBase, client, tokens.AccessToken)
	if claims := tokenClaims(t, tokens.AccessToken); claims["duties"] != loginDuties {
		t.Fatalf("passkey login duties = %v, want %q", claims["duties"], loginDuties)
	}

	browser.send(map[string]any{"cmd": "user-verified", "value": false})
	if tokens, steps = browser.login(browserBase, base, user, "webauthn"); tokens != nil || !slices.Equal(steps, []string{"password", "webauthn", "webauthn-error"}) {
		t.Fatalf("login without user verification answered %v (completed %v), want the passkey error page", steps, tokens != nil)
	}
}
