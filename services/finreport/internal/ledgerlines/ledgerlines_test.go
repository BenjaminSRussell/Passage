package ledgerlines

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/BenjaminSRussell/passage/platform/eventing"
	"github.com/BenjaminSRussell/passage/platform/tenant"
)

const (
	tid      = "11111111-1111-4111-8111-111111111111"
	entity   = "22222222-2222-4222-8222-222222222222"
	ledgerID = "33333333-3333-4333-8333-333333333333"
	docID    = "44444444-4444-4444-8444-444444444444"
	entryID  = "55555555-5555-4555-8555-555555555555"
	otherID  = "66666666-6666-4666-8666-666666666666"
)

func consumerCtx(entityID string) context.Context {
	return tenant.With(context.Background(), tenant.Principal{
		TenantID: tid, LegalEntityID: entityID, UserID: tenant.ServiceActor("finreport-ledger"), Duties: map[string]struct{}{Duty: {}},
	})
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
	c.sign = func(req *http.Request, p tenant.Principal) error {
		if p.LegalEntityID != entity {
			t.Errorf("signed for entity %q", p.LegalEntityID)
		}
		req.Header.Set("X-Test-Signed", "yes")
		return nil
	}
	return c
}

func line(entry string, n int, side, amount string) string {
	return fmt.Sprintf(`{"entryId":%q,"lineNumber":%d,"account":"4000","dimensions":{"DEPT":"D1"},"debitCredit":%q,"amountAcct":%q,"negativePosting":%t,"sourceUri":"/payables/invoices/1","traceId":"t1"}`,
		entry, n, side, amount, n == 3)
}

func TestEntryLinesPagesFiltersAndMaps(t *testing.T) {
	var calls []string
	c := newClient(t, func(w http.ResponseWriter, r *http.Request) {
		calls = append(calls, r.URL.RawQuery)
		q := r.URL.Query()
		if r.URL.Path != "/v1/ledger/journal-lines" || q.Get("ledger") != ledgerID || q.Get("documentId") != docID || q.Get("limit") != "1000" {
			t.Errorf("request %s", r.URL)
		}
		if r.Header.Get("X-Test-Signed") != "yes" {
			t.Errorf("request is not signed: %v", r.Header)
		}
		w.Header().Set("Content-Type", "application/json")
		if q.Get("cursor") == "" {
			_, _ = fmt.Fprintf(w, `{"items":[%s,%s],"nextCursor":"7.1"}`, line(entryID, 1, "D", "10.00"), line(otherID, 1, "C", "99.00"))
			return
		}
		if q.Get("cursor") != "7.1" {
			t.Errorf("cursor %q", q.Get("cursor"))
		}
		_, _ = fmt.Fprintf(w, `{"items":[%s,%s]}`, line(entryID, 2, "C", "6.00"), line(entryID, 3, "C", "4.00"))
	})
	got, err := c.EntryLines(consumerCtx(entity), ledgerID, docID, entryID)
	if err != nil {
		t.Fatal(err)
	}
	if len(calls) != 2 || len(got) != 3 {
		t.Fatalf("%d calls, %d lines", len(calls), len(got))
	}
	if got[0].DebitCredit != "D" || got[0].AmountAcct.String() != "10" || got[0].Account != "4000" || got[0].Dimensions["DEPT"] != "D1" ||
		got[0].SourceURI != "/payables/invoices/1" || got[0].TraceID != "t1" || got[0].Negative {
		t.Errorf("line 1 = %+v", got[0])
	}
	if !got[2].Negative || got[2].LineNumber != 3 {
		t.Errorf("line 3 = %+v", got[2])
	}
}

func TestEntryLinesRefusals(t *testing.T) {
	ok := func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte(`{"items":[]}`)) }
	c := newClient(t, ok)
	if _, err := c.EntryLines(context.Background(), ledgerID, docID, entryID); !errors.Is(err, ErrNoPrincipal) {
		t.Errorf("no principal: %v", err)
	}
	if _, err := c.EntryLines(consumerCtx(""), ledgerID, docID, entryID); !errors.Is(err, ErrNoEntity) {
		t.Errorf("no entity: %v", err)
	}

	denied := newClient(t, func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusForbidden) })
	_, err := denied.EntryLines(consumerCtx(entity), ledgerID, docID, entryID)
	var le *Error
	if !errors.As(err, &le) || le.Status != http.StatusForbidden || eventing.IsPermanent(err) {
		t.Errorf("403 must be retried: %v", err)
	}
	for _, status := range []int{http.StatusBadRequest, http.StatusNotFound} {
		refused := newClient(t, func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(status) })
		_, err := refused.EntryLines(consumerCtx(entity), ledgerID, docID, entryID)
		if !errors.As(err, &le) || le.Status != status || !eventing.IsPermanent(err) {
			t.Errorf("%d must be permanent: %v", status, err)
		}
	}
	busy := newClient(t, func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusServiceUnavailable) })
	if _, err := busy.EntryLines(consumerCtx(entity), ledgerID, docID, entryID); err == nil || eventing.IsPermanent(err) {
		t.Errorf("503 must be retried: %v", err)
	}

	bad := newClient(t, func(w http.ResponseWriter, _ *http.Request) {
		_, _ = fmt.Fprintf(w, `{"items":[%s]}`, strings.Replace(line(entryID, 1, "D", "x"), `"x"`, `"abc"`, 1))
	})
	if _, err := bad.EntryLines(consumerCtx(entity), ledgerID, docID, entryID); err == nil {
		t.Error("bad amount accepted")
	}

	loop := newClient(t, func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"items":[],"nextCursor":"1.1"}`))
	})
	if _, err := loop.EntryLines(consumerCtx(entity), ledgerID, docID, entryID); !errors.Is(err, ErrTooManyPages) {
		t.Errorf("endless cursor: %v", err)
	}

	if _, err := New("", nil, nil); err == nil {
		t.Error("empty URL accepted")
	}
	if _, err := New("http://x", nil, nil); err == nil {
		t.Error("nil signer accepted")
	}
}
