package upstream

import (
	"crypto/ed25519"
	"errors"
	"net/http"
	"testing"

	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/platform/tenant"
)

// partyIssuer is the service the round trip signs as. sales -> party
// party.read is a pending authz seed (NOT-IMPLEMENTED.md, slice 34); until it
// is in the grant table the test signs as receiving, which already holds the
// same party.read grant, so the signed call is still proven end to end. Once
// the seed lands it signs as sales with no change here.
func partyIssuer() string {
	if len(tenant.ServiceGrantTable()["sales"]["party"]) > 0 {
		return "sales"
	}
	return "receiving"
}

func TestCustomerGroup(t *testing.T) {
	group := ids.New()
	pub, priv, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatal(err)
	}
	issuer := partyIssuer()
	verifier := tenant.NewServiceVerifier(partyAudience, map[string]ed25519.PublicKey{issuer: pub})
	var h *harness
	h = newHarness(t, func(w http.ResponseWriter, r *http.Request, _ *harness) {
		c, err := verifier.Verify(r.Header.Get(tenant.HeaderServiceToken))
		if err != nil || c.Aud != partyAudience || c.Le != h.le || len(c.Duties) != 1 || c.Duties[0] != dutyPartyRead || r.Method != http.MethodGet {
			t.Errorf("party call: %s %s claims %+v err %v", r.Method, r.URL.Path, c, err)
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		switch {
		case r.URL.Path == "/v1/party/customers/known":
			_, _ = w.Write([]byte(`{"legalEntityId":"` + h.le + `","customerGroupId":"` + group + `"}`))
		case r.URL.Path == "/v1/party/customers/other-le":
			_, _ = w.Write([]byte(`{"legalEntityId":"` + ids.New() + `","customerGroupId":"` + group + `"}`))
		case r.URL.Path == "/v1/party/customers/broken":
			w.WriteHeader(http.StatusInternalServerError)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	})
	if h.client.signer, err = tenant.NewServiceSigner(issuer, priv); err != nil {
		t.Fatal(err)
	}
	h.client.WithParty(h.client.productURL + "/")
	if got, err := h.client.CustomerGroup(h.ctx, h.le, "known"); err != nil || got != group {
		t.Fatalf("got %q %v", got, err)
	}
	for id, want := range map[string]error{"missing": ErrCustomerUnknown, "other-le": ErrCustomerUnknown} {
		if _, err := h.client.CustomerGroup(h.ctx, h.le, id); !errors.Is(err, want) {
			t.Fatalf("%s: %v", id, err)
		}
	}
	var se *StatusError
	if _, err := h.client.CustomerGroup(h.ctx, h.le, "broken"); !errors.As(err, &se) || se.Status != 500 {
		t.Fatalf("broken: %v", err)
	}
	if _, err := New("", "", nil).CustomerGroup(h.ctx, h.le, "known"); !errors.Is(err, ErrNotConfigured) {
		t.Fatalf("unconfigured: %v", err)
	}
}

func TestDecodeCustomerGroup(t *testing.T) {
	le, group := ids.New(), ids.New()
	body := func(l, g string) []byte { return []byte(`{"legalEntityId":"` + l + `","customerGroupId":"` + g + `"}`) }
	if got, err := decodeCustomerGroup(200, body(le, group), le); err != nil || got != group {
		t.Fatalf("got %q %v", got, err)
	}
	if _, err := decodeCustomerGroup(404, nil, le); !errors.Is(err, ErrCustomerUnknown) {
		t.Fatalf("404: %v", err)
	}
	if _, err := decodeCustomerGroup(200, body(ids.New(), group), le); !errors.Is(err, ErrCustomerUnknown) {
		t.Fatalf("other legal entity: %v", err)
	}
	var se *StatusError
	if _, err := decodeCustomerGroup(500, nil, le); !errors.As(err, &se) || se.Status != 500 {
		t.Fatalf("500: %v", err)
	}
	for _, raw := range [][]byte{body(le, "not-a-uuid"), []byte(`{`), []byte(`{}`)} {
		if got, err := decodeCustomerGroup(200, raw, le); err == nil || got != "" {
			t.Fatalf("%s must not yield a group: %q", raw, got)
		}
	}
}
