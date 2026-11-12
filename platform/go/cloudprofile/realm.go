package cloudprofile

import (
	"fmt"
	"regexp"
	"slices"
	"strconv"
)

const (
	// TrustIdPMFAFlow is the realm flow that skips Passage's second factor when the identity provider already
	// proved a phishing-resistant one (#602 #603), instead of asking again.
	TrustIdPMFAFlow = "passage-post-broker-trust-idp-mfa"
	// AttrIdPAMR and AttrIdPACR are the user attributes the IdP's amr and acr claims are copied into at every login.
	AttrIdPAMR = "idp_amr"
	AttrIdPACR = "idp_acr"
	// DefaultAMRRegex matches the amr values of Entra ID that mean a second factor, or a certificate, was used at the IdP:
	// "mfa" is what Entra emits when the user completed MFA, including certificate-based authentication with a CAC or PIV
	// card (multi-factor certificate); "hwk" is a hardware-bound key. Passwordless-only "pwd" and "otp" alone do not match.
	// Override it with the values the customer's Conditional Access policy produces.
	DefaultAMRRegex = `(^|[ ,\[\]"])(mfa|hwk)($|[ ,\[\]"])`
	trustFlowGate   = "passage-post-broker-trust-idp-mfa gate"
)

// RealmOptions chooses realm settings that depend on the customer rather than the profile.
type RealmOptions struct {
	// TrustIdPMFA adds the flow that trusts the IdP's amr claim, and points the IdP at it.
	TrustIdPMFA bool
	// AMRRegex is matched against the idp_amr attribute; empty means DefaultAMRRegex.
	AMRRegex string
}

// RealmPatch is what a profile changes in realm-passage.json: values to set at the top level, plus authentication flows and
// authenticator configs to add. ApplyRealmPatch merges it into a realm export.
type RealmPatch struct {
	Set                  map[string]any   `json:"set"`
	AuthenticationFlows  []map[string]any `json:"authenticationFlows,omitempty"`
	AuthenticatorConfig  []map[string]any `json:"authenticatorConfig,omitempty"`
	IdentityProviderFlow string           `json:"identityProviderFlow,omitempty"`
}

// RenderRealmPatch derives the realm settings the profile requires: password hash and minimum length, the event
// retention (six years under the hipaa overlay), and the idle session timeout of the hipaa overlay. With
// TrustIdPMFA it also renders the post-broker flow that trusts the identity provider's phishing-resistant MFA.
func RenderRealmPatch(p *Profile, opts RealmOptions) (*RealmPatch, error) {
	k := p.Keycloak
	if k.PasswordHash == "" || k.PasswordMinLength < 1 {
		return nil, fmt.Errorf("cloudprofile: profile %s has no Keycloak password settings", p.Selector())
	}
	seconds := k.EventRetentionDays * 24 * 60 * 60
	patch := &RealmPatch{Set: map[string]any{
		"passwordPolicy":     fmt.Sprintf("hashAlgorithm(%s) and length(%d) and notUsername", k.PasswordHash, k.PasswordMinLength),
		"eventsEnabled":      true,
		"adminEventsEnabled": true,
		"eventsExpiration":   seconds,
		// Admin event retention is not a RealmRepresentation property: Keycloak stores it as the realm attribute
		// adminEventsExpiration (RealmAttributes.ADMIN_EVENTS_EXPIRATION), a string, and refuses the import otherwise.
		"attributes": map[string]any{"adminEventsExpiration": strconv.Itoa(seconds)},
	}}
	if p.IdleTimeoutMinutes > 0 {
		// The browser signs out on its own idle timer (apps/web); the SSO session must not outlive it, or the next
		// page load would sign the user straight back in.
		patch.Set["ssoSessionIdleTimeout"] = p.IdleTimeoutMinutes * 60
	}
	if !opts.TrustIdPMFA {
		return patch, nil
	}
	expr := opts.AMRRegex
	if expr == "" {
		expr = DefaultAMRRegex
	}
	if _, err := regexp.Compile(expr); err != nil {
		return nil, fmt.Errorf("cloudprofile: amr regex: %w", err)
	}
	patch.IdentityProviderFlow = TrustIdPMFAFlow
	patch.AuthenticatorConfig = []map[string]any{{
		"alias": "passage-idp-mfa-not-proven",
		"config": map[string]string{
			// True (so the second factor runs) when the user's idp_amr does NOT match: negate is on.
			"attribute_name": AttrIdPAMR, "attribute_expected_value": expr, "regex": "true", "not": "true",
		},
	}}
	exec := func(requirement string, priority int, extra map[string]any) map[string]any {
		m := map[string]any{"requirement": requirement, "priority": priority, "userSetupAllowed": false}
		for key, v := range extra {
			m[key] = v
		}
		return m
	}
	patch.AuthenticationFlows = []map[string]any{
		{
			"alias": TrustIdPMFAFlow, "description": "Second factor after federated login, unless the IdP proved MFA", "providerId": "basic-flow", "topLevel": true, "builtIn": false,
			"authenticationExecutions": []map[string]any{exec("REQUIRED", 10, map[string]any{"autheticatorFlow": true, "flowAlias": trustFlowGate})},
		},
		{
			"alias": trustFlowGate, "description": "Runs the second factor only when the IdP's amr claim does not show MFA", "providerId": "basic-flow", "topLevel": false, "builtIn": false,
			"authenticationExecutions": []map[string]any{
				exec("REQUIRED", 10, map[string]any{"autheticatorFlow": false, "authenticator": "conditional-user-attribute", "authenticatorConfig": "passage-idp-mfa-not-proven"}),
				exec("REQUIRED", 20, map[string]any{"autheticatorFlow": true, "flowAlias": PostBrokerFlow + " second factor"}),
			},
		},
	}
	return patch, nil
}

// TrustIdPMFAMappers are the identity provider mappers that copy the IdP's amr and acr claims into the user
// attributes the trust flow reads. They use sync mode FORCE: every login rewrites them, so an old value cannot
// carry over to a login where the IdP did not prove MFA.
func TrustIdPMFAMappers() []map[string]any {
	m := func(name, claim, attr string) map[string]any {
		return map[string]any{
			"name": name, "identityProviderAlias": EntraAlias, "identityProviderMapper": "oidc-user-attribute-idp-mapper",
			"config": map[string]string{"syncMode": "FORCE", "claim": claim, "user.attribute": attr},
		}
	}
	return []map[string]any{m("idp-amr", "amr", AttrIdPAMR), m("idp-acr", "acr", AttrIdPACR)}
}

// ApplyRealmPatch merges patch into a decoded realm export. Existing flows with the same alias are replaced; the identity
// providers named EntraAlias (and the placeholder providers) are pointed at the patch's post-broker flow when it has one.
func ApplyRealmPatch(realm map[string]any, patch *RealmPatch) {
	for key, v := range patch.Set {
		if attrs, ok := v.(map[string]any); ok && key == "attributes" {
			// Realm attributes are merged, so the patch never drops one the export already has.
			merged, _ := realm["attributes"].(map[string]any)
			if merged == nil {
				merged = map[string]any{}
			}
			for k, a := range attrs {
				merged[k] = a
			}
			realm[key] = merged
			continue
		}
		realm[key] = v
	}
	flows, _ := realm["authenticationFlows"].([]any)
	flows = slices.DeleteFunc(flows, func(f any) bool {
		m, ok := f.(map[string]any)
		return ok && slices.ContainsFunc(patch.AuthenticationFlows, func(n map[string]any) bool { return n["alias"] == m["alias"] })
	})
	for _, f := range patch.AuthenticationFlows {
		flows = append(flows, f)
	}
	if len(flows) > 0 {
		realm["authenticationFlows"] = flows
	}
	if len(patch.AuthenticatorConfig) > 0 {
		cfgs, _ := realm["authenticatorConfig"].([]any)
		for _, c := range patch.AuthenticatorConfig {
			cfgs = append(cfgs, c)
		}
		realm["authenticatorConfig"] = cfgs
	}
	if patch.IdentityProviderFlow != "" {
		idps, _ := realm["identityProviders"].([]any)
		for _, i := range idps {
			if m, ok := i.(map[string]any); ok {
				m["postBrokerLoginFlowAlias"] = patch.IdentityProviderFlow
			}
		}
	}
}
