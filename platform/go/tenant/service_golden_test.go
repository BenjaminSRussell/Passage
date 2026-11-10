package tenant_test

import (
	"crypto/ed25519"
	"encoding/base64"
	"encoding/json"
	"os"
	"slices"
	"testing"
	"time"

	"github.com/BenjaminSRussell/passage/platform/tenant"
)

type goldenFixture struct {
	Seed        string `json:"seed"`
	PublicKey   string `json:"publicKey"`
	Now         int64  `json:"now"`
	Tenant      string `json:"tenant"`
	LegalEntity string `json:"legalEntity"`
	Actor       string `json:"actor"`
	GoToken     string `json:"goToken"`
	PyToken     string `json:"pyToken"`
}

func TestServiceToken_GoldenFixtureCrossesLanguages(t *testing.T) {
	raw, err := os.ReadFile("../../../services/dispatch-py/tests/fixtures/service_token.json")
	if err != nil {
		t.Fatal(err)
	}
	var f goldenFixture
	if err := json.Unmarshal(raw, &f); err != nil {
		t.Fatal(err)
	}
	seed, _ := base64.StdEncoding.DecodeString(f.Seed)
	key := ed25519.NewKeyFromSeed(seed)
	if got := base64.StdEncoding.EncodeToString(key.Public().(ed25519.PublicKey)); got != f.PublicKey {
		t.Fatalf("public key %s, fixture %s", got, f.PublicKey)
	}
	at := time.Unix(f.Now+1, 0)
	verifier := func() *tenant.ServiceVerifier {
		return tenant.NewServiceVerifier("dispatch", map[string]ed25519.PublicKey{"fieldservice": key.Public().(ed25519.PublicKey)}).WithClock(func() time.Time { return at })
	}

	goClaims, err := verifier().Verify(f.GoToken)
	if err != nil || goClaims.Tid != f.Tenant || goClaims.Le != f.LegalEntity || goClaims.Act != f.Actor || !slices.Equal(goClaims.Duties, []string{"dispatch.run.create"}) {
		t.Fatalf("go token: %+v %v", goClaims, err)
	}
	pyClaims, err := verifier().Verify(f.PyToken)
	if err != nil || pyClaims.Tid != f.Tenant || pyClaims.Le != f.LegalEntity || !slices.Equal(pyClaims.Duties, []string{"dispatch.run.read"}) {
		t.Fatalf("python token: %+v %v", pyClaims, err)
	}

	signer, _ := tenant.NewServiceSigner("fieldservice", key)
	signer = signer.WithClock(func() time.Time { return time.Unix(f.Now, 0) })
	p := tenant.Principal{TenantID: f.Tenant, UserID: f.Actor, LegalEntityID: f.LegalEntity}
	minted, err := signer.MintDuties("dispatch", p, "", []string{"dispatch.run.create"})
	if err != nil {
		t.Fatal(err)
	}
	reminted, err := verifier().Verify(minted)
	if err != nil || reminted.Iat != goClaims.Iat || reminted.Exp != goClaims.Exp {
		t.Fatalf("fresh mint differs from the fixture: %+v %v", reminted, err)
	}
}
