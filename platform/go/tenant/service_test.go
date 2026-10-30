package tenant_test

import (
	"crypto/ed25519"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/BenjaminSRussell/passage/platform/tenant"
)

var epoch = time.Date(2026, 10, 29, 12, 0, 0, 0, time.UTC)

type fixture struct {
	signer    *tenant.ServiceSigner
	verifier  *tenant.ServiceVerifier
	now       *time.Time
	principal tenant.Principal
}

func newFixture(t *testing.T) *fixture {
	t.Helper()
	pub, priv, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatal(err)
	}
	signer, err := tenant.NewServiceSigner("receiving", priv)
	if err != nil {
		t.Fatal(err)
	}
	now := epoch
	clock := func() time.Time { return now }
	return &fixture{
		signer:    signer.WithClock(clock),
		verifier:  tenant.NewServiceVerifier("ledger", map[string]ed25519.PublicKey{"receiving": pub}).WithClock(clock),
		now:       &now,
		principal: tenant.Principal{TenantID: validTenant, LegalEntityID: validLE, UserID: "warehouse-user", Duties: map[string]struct{}{"receiving.post": {}}},
	}
}

func (f *fixture) mint(t *testing.T, aud string) string {
	t.Helper()
	token, err := f.signer.Mint(aud, f.principal, "")
	if err != nil {
		t.Fatal(err)
	}
	return token
}

func TestVerifyAcceptsFreshTokenAndMapsServicePrincipal(t *testing.T) {
	f := newFixture(t)
	claims, err := f.verifier.Verify(f.mint(t, "ledger"))
	if err != nil {
		t.Fatal(err)
	}
	p := claims.Principal()
	if p.TenantID != validTenant || p.LegalEntityID != validLE || p.UserID != "warehouse-user" || p.Service != "receiving" {
		t.Fatalf("principal %+v", p)
	}
	if !p.HasDuty("ledger.journal.post") || !p.HasDuty("ledger.journal.reverse") || p.HasDuty("receiving.post") || p.HasDuty("period.check") {
		t.Fatalf("duties must be the service's grant for ledger only, got %v", p.Duties)
	}
}

func TestVerifyRejections(t *testing.T) {
	f := newFixture(t)
	good := f.mint(t, "ledger")
	body, sig, _ := strings.Cut(good, ".")
	forged := func(mutate func(c *tenant.ServiceClaims)) string {
		raw, _ := base64.RawURLEncoding.DecodeString(body)
		var c tenant.ServiceClaims
		_ = json.Unmarshal(raw, &c)
		mutate(&c)
		out, _ := json.Marshal(c)
		return base64.RawURLEncoding.EncodeToString(out) + "." + sig
	}
	_, otherPriv, _ := ed25519.GenerateKey(nil)
	stranger, _ := tenant.NewServiceSigner("receiving", otherPriv)
	unknownIssuer, _ := tenant.NewServiceSigner("mystery", otherPriv)
	wrongAudience := f.mint(t, "close")

	cases := map[string]string{
		"empty":                 "",
		"garbage":               "not-a-token",
		"tampered duties":       forged(func(c *tenant.ServiceClaims) { c.Duties = append(c.Duties, "ledger.ledger.write") }),
		"tampered legal entity": forged(func(c *tenant.ServiceClaims) { c.Le = "0190a5f2-7b1c-7d3e-8a4f-1234567890ad" }),
		"tampered signature":    body + "." + base64.RawURLEncoding.EncodeToString(make([]byte, 64)),
		"wrong audience":        wrongAudience,
		"signed by another key": mustMint(t, stranger, "ledger", f.principal),
		"unknown issuer":        mustMint(t, unknownIssuer, "ledger", f.principal),
		"oversized":             strings.Repeat("a", 5000),
	}
	for name, token := range cases {
		if _, err := f.verifier.Verify(token); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
}

func mustMint(t *testing.T, s *tenant.ServiceSigner, aud string, p tenant.Principal) string {
	t.Helper()
	token, err := s.Mint(aud, p, "")
	if err != nil {
		return "unmintable"
	}
	return token
}

func TestVerifyExpiryAndFutureIssue(t *testing.T) {
	f := newFixture(t)
	token := f.mint(t, "ledger")
	*f.now = epoch.Add(tenant.ServiceTokenMaxTTL - time.Second)
	if _, err := f.verifier.Verify(token); err != nil {
		t.Fatalf("token inside its ttl: %v", err)
	}
	expired := f.mint(t, "ledger")
	*f.now = epoch.Add(2 * tenant.ServiceTokenMaxTTL)
	if _, err := f.verifier.Verify(expired); err == nil {
		t.Fatal("expired token accepted")
	}
	*f.now = epoch.Add(-time.Minute)
	if _, err := f.verifier.Verify(token); err == nil {
		t.Fatal("token issued in the future accepted")
	}
}

func TestVerifyRejectsReplay(t *testing.T) {
	f := newFixture(t)
	token := f.mint(t, "ledger")
	if _, err := f.verifier.Verify(token); err != nil {
		t.Fatal(err)
	}
	if _, err := f.verifier.Verify(token); err == nil {
		t.Fatal("replayed token accepted")
	}
	if _, err := f.verifier.Verify(f.mint(t, "ledger")); err != nil {
		t.Fatalf("a fresh token must still verify: %v", err)
	}
}

func TestMintFailsClosed(t *testing.T) {
	f := newFixture(t)
	for name, tc := range map[string]struct {
		aud string
		p   tenant.Principal
		le  string
	}{
		"no grant for audience": {"warehouse", f.principal, ""},
		"no legal entity":       {"ledger", tenant.Principal{TenantID: validTenant, UserID: "u"}, ""},
		"multiple entities":     {"ledger", tenant.Principal{TenantID: validTenant, LegalEntityID: validLE + "," + validTenant, UserID: "u"}, ""},
		"nil legal entity":      {"ledger", f.principal, nilUUID},
		"no tenant":             {"ledger", tenant.Principal{LegalEntityID: validLE, UserID: "u"}, ""},
		"no end user":           {"ledger", tenant.Principal{TenantID: validTenant, LegalEntityID: validLE}, ""},
	} {
		if token, err := f.signer.Mint(tc.aud, tc.p, tc.le); err == nil {
			t.Errorf("%s: minted %q", name, token)
		}
	}
}

func TestApplyNilSignerLeavesRequestUntouched(t *testing.T) {
	var s *tenant.ServiceSigner
	req := httptest.NewRequest(http.MethodPost, "/x", nil)
	signed, err := s.Apply(req, tenant.Principal{}, "ledger", "")
	if signed || err != nil || req.Header.Get(tenant.HeaderServiceToken) != "" {
		t.Fatalf("signed=%v err=%v headers=%v", signed, err, req.Header)
	}
}

func serveWithVerifier(t *testing.T, f *fixture, mutate func(*http.Request)) (*httptest.ResponseRecorder, *tenant.Principal) {
	t.Helper()
	tenant.SetServiceVerifier(f.verifier)
	t.Cleanup(func() { tenant.SetServiceVerifier(nil) })
	var got *tenant.Principal
	h := tenant.Middleware(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		p, _ := tenant.FromContext(r.Context())
		got = &p
	}))
	req := httptest.NewRequest(http.MethodPost, "/v1/ledger/journal-entries", nil)
	mutate(req)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec, got
}

func TestMiddlewareServiceTokenReplacesUserHeaders(t *testing.T) {
	f := newFixture(t)
	rec, got := serveWithVerifier(t, f, func(r *http.Request) {
		r.Header.Set(tenant.HeaderServiceToken, f.mint(t, "ledger"))
		r.Header.Set(tenant.HeaderTenant, "0190a5f2-7b1c-7d3e-8a4f-ffffffffffff")
		r.Header.Set(tenant.HeaderLegalEntity, "0190a5f2-7b1c-7d3e-8a4f-eeeeeeeeeeee")
		r.Header.Set(tenant.HeaderUser, "someone-else")
		r.Header.Set(tenant.HeaderDuties, tenant.DutyAllScope+",ledger.ledger.write")
	})
	if rec.Code != http.StatusOK || got == nil {
		t.Fatalf("status %d", rec.Code)
	}
	if got.TenantID != validTenant || got.LegalEntityID != validLE || got.UserID != "warehouse-user" || got.HasDuty("ledger.ledger.write") || got.HasDuty(tenant.DutyAllScope) {
		t.Fatalf("user headers leaked into the service principal: %+v", got)
	}
}

func TestMiddlewareRejectsBadServiceTokenWithoutFallingBack(t *testing.T) {
	f := newFixture(t)
	userHeaders := func(r *http.Request) {
		r.Header.Set(tenant.HeaderTenant, validTenant)
		r.Header.Set(tenant.HeaderLegalEntity, validLE)
		r.Header.Set(tenant.HeaderDuties, "ledger.journal.post")
	}
	token := f.mint(t, "ledger")
	for name, mutate := range map[string]func(*http.Request){
		"forged": func(r *http.Request) {
			userHeaders(r)
			r.Header.Set(tenant.HeaderServiceToken, token[:len(token)-4]+"AAAA")
		},
		"expired": func(r *http.Request) {
			userHeaders(r)
			r.Header.Set(tenant.HeaderServiceToken, token)
			*f.now = epoch.Add(time.Hour)
		},
		"two tokens": func(r *http.Request) {
			r.Header.Add(tenant.HeaderServiceToken, f.mint(t, "ledger"))
			r.Header.Add(tenant.HeaderServiceToken, f.mint(t, "ledger"))
		},
	} {
		*f.now = epoch
		rec, got := serveWithVerifier(t, f, mutate)
		if rec.Code != http.StatusUnauthorized || got != nil {
			t.Errorf("%s: status %d handler reached=%v", name, rec.Code, got != nil)
		}
	}
}

func TestMiddlewareRejectsServiceTokenWhenNoVerifierConfigured(t *testing.T) {
	f := newFixture(t)
	token := f.mint(t, "ledger")
	tenant.SetServiceVerifier(nil)
	req := httptest.NewRequest(http.MethodPost, "/v1/x", nil)
	req.Header.Set(tenant.HeaderServiceToken, token)
	rec := httptest.NewRecorder()
	tenant.Middleware(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { t.Error("handler reached") })).ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status %d", rec.Code)
	}
}

func TestEnvLoaders(t *testing.T) {
	pub, priv, _ := ed25519.GenerateKey(nil)
	env := map[string]string{
		tenant.EnvServiceName:       "receiving",
		tenant.EnvServiceSigningKey: base64.StdEncoding.EncodeToString(priv.Seed()),
	}
	signer, err := tenant.ServiceSignerFromEnv(func(k string) string { return env[k] })
	if err != nil || signer == nil {
		t.Fatalf("signer %v err %v", signer, err)
	}
	file := filepath.Join(t.TempDir(), "key")
	if err := os.WriteFile(file, []byte(env[tenant.EnvServiceSigningKey]+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	fromFile, err := tenant.ServiceSignerFromEnv(func(k string) string {
		return map[string]string{tenant.EnvServiceName: "receiving", tenant.EnvServiceSigningFile: file}[k]
	})
	if err != nil || fromFile == nil {
		t.Fatalf("file signer %v err %v", fromFile, err)
	}
	none, err := tenant.ServiceSignerFromEnv(func(string) string { return "" })
	if none != nil || err != nil {
		t.Fatalf("unset must be nil,nil, got %v %v", none, err)
	}
	if _, err := tenant.ServiceSignerFromEnv(func(k string) string { return map[string]string{tenant.EnvServiceSigningKey: "bad"}[k] }); err == nil {
		t.Fatal("malformed seed accepted")
	}

	verifier, err := tenant.ServiceVerifierFromEnv(func(k string) string {
		return map[string]string{tenant.EnvServiceName: "ledger", tenant.EnvServicePublicKeys: "receiving=" + base64.StdEncoding.EncodeToString(pub)}[k]
	})
	if err != nil || verifier == nil {
		t.Fatalf("verifier %v err %v", verifier, err)
	}
	p := tenant.Principal{TenantID: validTenant, LegalEntityID: validLE, UserID: "u"}
	token, err := signer.Mint("ledger", p, "")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := verifier.Verify(token); err != nil {
		t.Fatalf("env-built pair must interoperate: %v", err)
	}
	if _, err := tenant.ServiceVerifierFromEnv(func(k string) string {
		return map[string]string{tenant.EnvServiceName: "ledger", tenant.EnvServicePublicKeys: "receiving=zzz"}[k]
	}); err == nil {
		t.Fatal("malformed public key accepted")
	}
}

func TestGrantTableMatchesPolicyData(t *testing.T) {
	raw, err := os.ReadFile("../../../policies/authz/data.json")
	if err != nil {
		t.Fatal(err)
	}
	var data struct {
		Duties        map[string]json.RawMessage     `json:"duties"`
		ServiceGrants map[string]map[string][]string `json:"service_grants"`
	}
	if err := json.Unmarshal(raw, &data); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(data.ServiceGrants, tenant.ServiceGrantTable()) {
		t.Fatalf("policies/authz/data.json service_grants %v != Go table %v", data.ServiceGrants, tenant.ServiceGrantTable())
	}
	for iss, targets := range data.ServiceGrants {
		for aud, duties := range targets {
			for _, d := range duties {
				if _, ok := data.Duties[d]; !ok {
					t.Errorf("%s -> %s grants %q which is not a seeded duty", iss, aud, d)
				}
			}
		}
	}
}

func TestMintRequiresExplicitEntityInTheUsersScope(t *testing.T) {
	f := newFixture(t)
	const other = "0190a5f2-7b1c-7d3e-8a4f-1234567890ad"
	multi := f.principal
	multi.LegalEntityID = validLE + "," + other
	if _, err := f.signer.Mint("ledger", f.principal, other); err == nil {
		t.Fatal("an entity outside the user's scope was signed")
	}
	if _, err := f.signer.Mint("ledger", multi, ""); err == nil {
		t.Fatal("a multi-entity user needs an explicit entity")
	}
	token, err := f.signer.Mint("ledger", multi, other)
	if err != nil {
		t.Fatal(err)
	}
	if claims, err := f.verifier.Verify(token); err != nil || claims.Le != other {
		t.Fatalf("claims %+v err %v", claims, err)
	}
	allScope := tenant.Principal{TenantID: validTenant, UserID: "u", Duties: map[string]struct{}{tenant.DutyAllScope: {}}}
	if _, err := f.signer.Mint("ledger", allScope, other); err == nil {
		t.Fatal("the all-scope exception must not apply to a grant that can post")
	}
	_, priv, _ := ed25519.GenerateKey(nil)
	payables, _ := tenant.NewServiceSigner("payables", priv)
	if _, err := payables.Mint("ledger", allScope, other); err != nil {
		t.Fatalf("an ALL-scope read-only grant may name any entity: %v", err)
	}
	allScope.LegalEntityID = validLE
	if _, err := f.signer.Mint("ledger", allScope, other); err == nil {
		t.Fatal("the all-scope duty must not widen a user who is scoped to an entity")
	}
}

func TestKeyRotationSeveralKeysPerIssuerSelectedByKid(t *testing.T) {
	oldPub, oldPriv, _ := ed25519.GenerateKey(nil)
	newPub, newPriv, _ := ed25519.GenerateKey(nil)
	oldSigner, _ := tenant.NewServiceSigner("receiving", oldPriv)
	newSigner, _ := tenant.NewServiceSigner("receiving", newPriv)
	oldSigner, newSigner = oldSigner.WithKeyID("k1"), newSigner.WithKeyID("k2")
	verifier := tenant.NewServiceVerifierKids("ledger", map[string]map[string]ed25519.PublicKey{"receiving": {"k1": oldPub, "k2": newPub}})
	p := tenant.Principal{TenantID: validTenant, LegalEntityID: validLE, UserID: "u"}
	for name, s := range map[string]*tenant.ServiceSigner{"k1": oldSigner, "k2": newSigner} {
		token, _ := s.Mint("ledger", p, "")
		if claims, err := verifier.Verify(token); err != nil || claims.Kid != name {
			t.Fatalf("%s: %+v %v", name, claims, err)
		}
	}
	mislabelled, _ := newSigner.WithKeyID("k1").Mint("ledger", p, "")
	if _, err := verifier.Verify(mislabelled); err == nil {
		t.Fatal("a token labelled with another key's kid verified")
	}
	unknown, _ := newSigner.WithKeyID("k9").Mint("ledger", p, "")
	if _, err := verifier.Verify(unknown); err == nil {
		t.Fatal("unknown kid verified")
	}
}

func TestVerifierEnvKeysAndDuplicates(t *testing.T) {
	pubA, _, _ := ed25519.GenerateKey(nil)
	pubB, _, _ := ed25519.GenerateKey(nil)
	a, b := base64.StdEncoding.EncodeToString(pubA), base64.StdEncoding.EncodeToString(pubB)
	env := func(keys, size string) func(string) string {
		return func(k string) string {
			return map[string]string{tenant.EnvServiceName: "ledger", tenant.EnvServicePublicKeys: keys, tenant.EnvServiceReplaySize: size}[k]
		}
	}
	if v, err := tenant.ServiceVerifierFromEnv(env("receiving:k1="+a+",receiving:k2="+b, "")); err != nil || v == nil {
		t.Fatalf("rotation keys: %v", err)
	}
	for name, keys := range map[string]string{
		"duplicate issuer and kid": "receiving:k1=" + a + ",receiving:k1=" + b,
		"duplicate bare issuer":    "receiving=" + a + ",receiving=" + b,
		"short key":                "receiving=AAAA",
	} {
		if _, err := tenant.ServiceVerifierFromEnv(env(keys, "")); err == nil {
			t.Errorf("%s accepted", name)
		}
	}
	if _, err := tenant.ServiceVerifierFromEnv(env("receiving="+a, "0")); err == nil {
		t.Error("non-positive replay cache size accepted")
	}
	if err := tenant.ConfigureServiceVerifier(env("receiving=zzz", "")); err == nil {
		t.Error("ConfigureServiceVerifier accepted malformed keys")
	}
	t.Cleanup(func() { tenant.SetServiceVerifier(nil) })
	if err := tenant.ConfigureServiceVerifier(env("receiving="+a, "")); err != nil {
		t.Fatal(err)
	}
}

func TestReplayCacheEvictsExpiredAndFailsClosedWhenFull(t *testing.T) {
	f := newFixture(t)
	small := f.verifier.WithReplayCapacity(3)
	mint := func() string { return f.mint(t, "ledger") }
	for i := 0; i < 3; i++ {
		if _, err := small.Verify(mint()); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := small.Verify(mint()); err == nil {
		t.Fatal("a full cache must reject rather than forget")
	}
	*f.now = epoch.Add(tenant.ServiceTokenMaxTTL + time.Second)
	if _, err := small.Verify(mint()); err != nil {
		t.Fatalf("expired entries must be evicted to make room: %v", err)
	}
}

func TestMintDutiesCarriesOnlyTheIntersectionWithTheGrant(t *testing.T) {
	f := newFixture(t)
	token, err := f.signer.MintDuties("ledger", f.principal, "", []string{"ledger.journal.post", "ledger.account.write", "org.switcher.all_scope"})
	if err != nil {
		t.Fatal(err)
	}
	claims, err := f.verifier.Verify(token)
	if err != nil || len(claims.Duties) != 1 || claims.Duties[0] != "ledger.journal.post" {
		t.Fatalf("claims = %+v, %v", claims, err)
	}
	if _, err := f.signer.MintDuties("ledger", f.principal, "", []string{"ledger.account.write"}); !errors.Is(err, tenant.ErrNoServiceGrant) {
		t.Fatalf("a duty outside the grant must not be signed: %v", err)
	}
	if _, err := f.signer.MintDuties("ledger", f.principal, "", []string{}); err == nil {
		t.Fatal("an empty duty list must not fall back to the whole grant")
	}
}
