package ledgerbalances

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/BenjaminSRussell/passage/platform/tenant"
)

const (
	tid      = "11111111-1111-4111-8111-111111111111"
	entity   = "22222222-2222-4222-8222-222222222222"
	ledgerID = "33333333-3333-4333-8333-333333333333"
)

func ctx() context.Context {
	return tenant.With(context.Background(), tenant.Principal{TenantID: tid, UserID: "u1"})
}

func newClient(t *testing.T, h http.HandlerFunc) *Client {
	t.Helper()
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	_, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	s, err := tenant.NewServiceSigner("finreport", priv)
	if err != nil {
		t.Fatal(err)
	}
	c, err := New(srv.URL+"/", s, nil)
	if err != nil {
		t.Fatal(err)
	}
	c.sign = func(req *http.Request, _ tenant.Principal, le string) error {
		if le != entity {
			t.Errorf("signed for entity %q", le)
		}
		req.Header.Set("X-Test-Signed", "yes")
		return nil
	}
	return c
}

func TestNetsReadsRowsAsDecimals(t *testing.T) {
	c := newClient(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/ledger/balances" || r.URL.Query().Get("ledger") != ledgerID || len(r.URL.Query()) != 1 {
			t.Errorf("request %s", r.URL)
		}
		if r.Header.Get("X-Test-Signed") != "yes" {
			t.Error("request is not signed")
		}
		_, _ = w.Write([]byte(`{"ledgerId":"` + ledgerID + `","rows":[{"account":"1000","net":"10.50"},{"account":"4000","net":"-10.5"}],"balanced":true}`))
	})
	got, err := c.Nets(ctx(), ledgerID, entity)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got["1000"].String() != "10.5" || got["4000"].String() != "-10.5" {
		t.Fatalf("got %v", got)
	}
}

func TestNetsRefusals(t *testing.T) {
	reply := func(status int, body string) *Client {
		return newClient(t, func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(status)
			_, _ = w.Write([]byte(body))
		})
	}
	if _, err := reply(200, `{}`).Nets(context.Background(), ledgerID, entity); !errors.Is(err, ErrNoPrincipal) {
		t.Errorf("no principal: %v", err)
	}
	var le *Error
	for _, status := range []int{403, 404, 503} {
		if _, err := reply(status, `{}`).Nets(ctx(), ledgerID, entity); !errors.As(err, &le) || le.Status != status {
			t.Errorf("%d: %v", status, err)
		}
	}
	if _, err := reply(200, `{"rows":[],"filtered":true}`).Nets(ctx(), ledgerID, entity); !errors.Is(err, ErrFiltered) {
		t.Errorf("filtered: %v", err)
	}
	if _, err := reply(200, `{"ledgerId":"`+ledgerID+`","rows":[{"account":"1","net":"1"},{"account":"1","net":"2"}]}`).Nets(ctx(), ledgerID, entity); !errors.Is(err, ErrDuplicateAccount) {
		t.Errorf("duplicate: %v", err)
	}
	if _, err := reply(200, `{"ledgerId":"`+ledgerID+`","rows":[{"account":"1","net":"abc"}]}`).Nets(ctx(), ledgerID, entity); err == nil {
		t.Error("bad net accepted")
	}
	// An answer without rows, or for another ledger, is not this ledger's trial balance.
	for _, body := range []string{`{"ledgerId":"` + ledgerID + `"}`, `{"ledgerId":"` + entity + `","rows":[]}`} {
		if _, err := reply(200, body).Nets(ctx(), ledgerID, entity); err == nil {
			t.Errorf("%s accepted", body)
		}
	}
	if _, err := reply(200, `not json`).Nets(ctx(), ledgerID, entity); err == nil {
		t.Error("bad json accepted")
	}
	if _, err := New("", nil, nil); err == nil {
		t.Error("empty URL accepted")
	}
	if _, err := New("http://x", nil, nil); err == nil {
		t.Error("nil signer accepted")
	}
}

// Without the finreport to ledger grant for ledger.balance.read, the platform
// signer refuses to mint the token: the call never leaves the service and the
// error is ErrSign (the API answers 503, not 502).
func TestNetsWithoutAServiceGrantIsASignError(t *testing.T) {
	called := false
	srv := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { called = true }))
	t.Cleanup(srv.Close)
	_, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	s, err := tenant.NewServiceSigner("not-a-granted-service", priv)
	if err != nil {
		t.Fatal(err)
	}
	c, err := New(srv.URL, s, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := c.Nets(ctx(), ledgerID, entity); !errors.Is(err, ErrSign) || called {
		t.Fatalf("err %v, ledger called %v", err, called)
	}
}
