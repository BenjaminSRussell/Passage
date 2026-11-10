package devenv

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"sort"
	"strings"
)

const (
	WebClientID = "passage-web"
	RealmName   = "passage"
	devRPID     = "localhost"
	devFlow     = "passage-browser-dev"
)

type User struct {
	Username  string   `json:"username"`
	Password  string   `json:"password"`
	Sub       string   `json:"sub"`
	Email     string   `json:"email"`
	FirstName string   `json:"firstName"`
	LastName  string   `json:"lastName"`
	Tier      string   `json:"tier"`
	Duties    []string `json:"duties"`
}

type Seed struct {
	TenantID      string `json:"tenantId"`
	LegalEntityID string `json:"legalEntityId"`
	Users         []User `json:"users"`
}

func LoadSeed(path string) (Seed, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return Seed{}, err
	}
	var seed Seed
	if err := json.Unmarshal(raw, &seed); err != nil {
		return Seed{}, fmt.Errorf("%s: %w", path, err)
	}
	return seed, seed.validate()
}

func (s Seed) validate() error {
	if s.TenantID == "" || s.LegalEntityID == "" || len(s.Users) == 0 {
		return errors.New("seed needs tenantId, legalEntityId and users")
	}
	seen := map[string]bool{}
	for _, u := range s.Users {
		if u.Username == "" || u.Sub == "" || u.Password == "" || len(u.Duties) == 0 {
			return fmt.Errorf("seed user %q needs username, sub, password and duties", u.Username)
		}
		if seen[u.Sub] || seen[u.Username] {
			return fmt.Errorf("seed user %q repeats a username or sub", u.Username)
		}
		seen[u.Sub], seen[u.Username] = true, true
	}
	return nil
}

func (s Seed) User(username string) (User, bool) {
	for _, u := range s.Users {
		if u.Username == username {
			return u, true
		}
	}
	return User{}, false
}

func (u User) DutyClaim() string {
	duties := append([]string(nil), u.Duties...)
	sort.Strings(duties)
	return strings.Join(duties, ",")
}

// RenderAssignments is the OPA data document that grants each seeded user their duties: tenant -> sub -> duties.
func RenderAssignments(s Seed) ([]byte, error) {
	users := map[string][]string{}
	for _, u := range s.Users {
		duties := append([]string(nil), u.Duties...)
		sort.Strings(duties)
		users[u.Sub] = duties
	}
	return json.MarshalIndent(map[string]any{"assignments": map[string]any{s.TenantID: users}}, "", "  ")
}

// RenderRealm turns the production realm export into one a laptop can import: plain HTTP, no second factor, the web
// client redirecting to webOrigin, and the seeded users carrying the attributes the token mappers read.
func RenderRealm(base []byte, s Seed, webOrigin string) ([]byte, error) {
	var realm map[string]any
	if err := json.Unmarshal(base, &realm); err != nil {
		return nil, err
	}
	realm["sslRequired"] = "none"
	realm["browserFlow"] = devFlow
	flows, _ := realm["authenticationFlows"].([]any)
	realm["authenticationFlows"] = append(flows, devBrowserFlows()...)
	realm["bruteForceProtected"] = false
	realm["webAuthnPolicyRpId"] = devRPID
	realm["webAuthnPolicyPasswordlessRpId"] = devRPID
	if actions, ok := realm["requiredActions"].([]any); ok {
		for _, a := range actions {
			if action, ok := a.(map[string]any); ok {
				action["defaultAction"] = false
			}
		}
	}
	if err := pointWebClient(realm, webOrigin); err != nil {
		return nil, err
	}
	users, _ := realm["users"].([]any)
	for _, u := range s.Users {
		users = append(users, map[string]any{
			"id": u.Sub, "username": u.Username, "email": u.Email, "firstName": u.FirstName, "lastName": u.LastName,
			"enabled": true, "emailVerified": true, "requiredActions": []string{},
			"credentials": []map[string]any{{"type": "password", "value": u.Password, "temporary": false}},
			"attributes": map[string][]string{
				"tenant_id": {s.TenantID}, "legal_entity_id": {s.LegalEntityID}, "duties": {u.DutyClaim()}, "tier": {u.Tier},
			},
		})
	}
	realm["users"] = users
	return json.MarshalIndent(realm, "", "  ")
}

// devBrowserFlows is the production browser flow minus the second factor. The export defines only custom flows, so
// Keycloak creates no built-in "browser" flow on import and the dev realm must bring its own.
func devBrowserFlows() []any {
	execution := func(requirement string, priority int, extra map[string]any) map[string]any {
		e := map[string]any{"requirement": requirement, "priority": priority, "userSetupAllowed": false, "autheticatorFlow": false}
		for k, v := range extra {
			e[k] = v
		}
		return e
	}
	flow := func(alias string, topLevel bool, executions ...any) map[string]any {
		return map[string]any{
			"alias": alias, "description": "Dev browser login: password only", "providerId": "basic-flow",
			"topLevel": topLevel, "builtIn": false, "authenticationExecutions": executions,
		}
	}
	forms := devFlow + " forms"
	return []any{
		flow(devFlow, true,
			execution("ALTERNATIVE", 10, map[string]any{"authenticator": "auth-cookie"}),
			execution("ALTERNATIVE", 30, map[string]any{"autheticatorFlow": true, "flowAlias": forms}),
		),
		flow(forms, false, execution("REQUIRED", 10, map[string]any{"authenticator": "auth-username-password-form"})),
	}
}

func pointWebClient(realm map[string]any, webOrigin string) error {
	clients, _ := realm["clients"].([]any)
	for _, c := range clients {
		client, ok := c.(map[string]any)
		if !ok || client["clientId"] != WebClientID {
			continue
		}
		// The password grant lets `make e2e-local` get real Keycloak tokens for the seeded users without a browser.
		client["directAccessGrantsEnabled"] = true
		client["redirectUris"] = []string{webOrigin + "/auth/callback"}
		client["webOrigins"] = []string{webOrigin}
		attrs, _ := client["attributes"].(map[string]any)
		if attrs == nil {
			attrs = map[string]any{}
			client["attributes"] = attrs
		}
		attrs["post.logout.redirect.uris"] = webOrigin + "/"
		return nil
	}
	return fmt.Errorf("realm has no %s client", WebClientID)
}
