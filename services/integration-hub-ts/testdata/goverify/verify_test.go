// Package goverify runs the real platform/go/tenant service-token verifier against tokens the integration hub
// minted (../service-token-vectors.json, re-minted byte for byte by test/serviceTokenVectors.test.ts), so the
// TypeScript signer and the Go verifier cannot drift apart unnoticed. Run: cd testdata/goverify && go test ./...
package goverify

import (
	"context"
	"crypto/ed25519"
	"encoding/base64"
	"encoding/json"
	"os"
	"slices"
	"testing"
	"time"

	"github.com/BenjaminSRussell/passage/platform/tenant"
)

type vectors struct {
	SeedBase64     string   `json:"seedBase64"`
	NowUnixSeconds int64    `json:"nowUnixSeconds"`
	TenantID       string   `json:"tenantId"`
	LegalEntityID  string   `json:"legalEntityId"`
	Duties         []string `json:"duties"`
	Vectors        []struct {
		Name  string `json:"name"`
		Jti   string `json:"jti"`
		Token string `json:"token"`
		Act   string `json:"act"`
	} `json:"vectors"`
}

func load(t *testing.T) (vectors, ed25519.PrivateKey) {
	t.Helper()
	raw, err := os.ReadFile("../service-token-vectors.json")
	if err != nil {
		t.Fatal(err)
	}
	var doc vectors
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatal(err)
	}
	seed, err := base64.StdEncoding.DecodeString(doc.SeedBase64)
	if err != nil || len(seed) != ed25519.SeedSize {
		t.Fatalf("seed: %v", err)
	}
	return doc, ed25519.NewKeyFromSeed(seed)
}

// The migration service's verifier: audience migration, trusting the hub's public key, at the vectors' instant.
func verifier(doc vectors, priv ed25519.PrivateKey) *tenant.ServiceVerifier {
	now := time.Unix(doc.NowUnixSeconds, 0)
	return tenant.NewServiceVerifier("migration", map[string]ed25519.PublicKey{"integration-hub": priv.Public().(ed25519.PublicKey)}).
		WithClock(func() time.Time { return now })
}

func TestHubTokensAreAcceptedWithOnlyTheGrant(t *testing.T) {
	doc, priv := load(t)
	grant := tenant.ServiceGrantTable()["integration-hub"]["migration"]
	if !slices.Equal(grant, doc.Duties) {
		t.Fatalf("the hub's MIGRATION_GRANT %v is not the platform grant %v", doc.Duties, grant)
	}
	if _, ok := tenant.ServicePrincipalTable()["integration-hub"]; !ok {
		t.Fatal("integration-hub is not a registered service principal: act service:integration-hub would be refused")
	}
	v := verifier(doc, priv)
	for _, vec := range doc.Vectors {
		t.Run(vec.Name, func(t *testing.T) {
			c, err := v.VerifyContext(context.Background(), vec.Token)
			if err != nil {
				t.Fatalf("refused: %v", err)
			}
			p := c.Principal()
			if p.Service != "integration-hub" || p.UserID != vec.Act || p.TenantID != doc.TenantID || p.LegalEntityID != doc.LegalEntityID {
				t.Fatalf("principal %+v", p)
			}
			// The actor never adds duties: an operator's test send holds exactly the hub's grant, not the operator's.
			if len(p.Duties) != len(grant) || p.HasDuty("migration.project.write") || p.HasDuty("migration.run") || p.HasDuty(tenant.DutyAllScope) {
				t.Fatalf("duties %v", p.Duties)
			}
			for _, d := range grant {
				if !p.HasDuty(d) {
					t.Fatalf("missing %s", d)
				}
			}
			if _, err := v.VerifyContext(context.Background(), vec.Token); err == nil {
				t.Fatal("a replayed token was accepted")
			}
		})
	}
}

// Tokens the hub never mints, signed with its key: the verifier refuses each, so a forged actor or a widened duty
// list cannot escalate even if the signing key were misused.
func TestForgedClaimsAreRefused(t *testing.T) {
	doc, priv := load(t)
	base := map[string]any{
		"iss": "integration-hub", "aud": "migration", "tid": doc.TenantID, "le": doc.LegalEntityID,
		"act": "service:integration-hub", "duties": doc.Duties, "iat": doc.NowUnixSeconds, "exp": doc.NowUnixSeconds + 60,
	}
	cases := map[string]map[string]any{
		"an unregistered service actor":         {"act": "service:sales"},
		"an upper-case unregistered actor":      {"act": "SERVICE:billing"},
		"no actor":                              {"act": ""},
		"a duty outside the grant":              {"duties": append(slices.Clone(doc.Duties), "migration.project.write")},
		"the all-entity duty":                   {"duties": []string{tenant.DutyAllScope}},
		"no duties":                             {"duties": []string{}},
		"the nil legal entity":                  {"le": "00000000-0000-0000-0000-000000000000"},
		"an upper-case legal entity":            {"le": "AAAAAAAA-4444-4555-8666-777777777777"},
		"another audience":                      {"aud": "receivables"},
		"a lifetime over 60 seconds":            {"exp": doc.NowUnixSeconds + 61},
		"expired":                               {"iat": doc.NowUnixSeconds - 61, "exp": doc.NowUnixSeconds - 1},
		"an issuer whose key is someone else's": {"iss": "migration-loader"},
	}
	v := verifier(doc, priv)
	for name, over := range cases {
		t.Run(name, func(t *testing.T) {
			claims := map[string]any{}
			for k, val := range base {
				claims[k] = val
			}
			for k, val := range over {
				claims[k] = val
			}
			claims["jti"] = "forged " + name
			payload, err := json.Marshal(claims)
			if err != nil {
				t.Fatal(err)
			}
			body := base64.RawURLEncoding.EncodeToString(payload)
			token := body + "." + base64.RawURLEncoding.EncodeToString(ed25519.Sign(priv, []byte("passage.service-token.v1."+body)))
			if _, err := v.VerifyContext(context.Background(), token); err == nil {
				t.Fatalf("accepted %v", claims)
			}
		})
	}
}
