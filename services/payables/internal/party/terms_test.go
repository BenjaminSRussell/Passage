package party_test

import (
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/BenjaminSRussell/depot/platform/tenant"
	"github.com/BenjaminSRussell/depot/services/payables/internal/party"
)

const term = "00000000-0000-0000-0000-0000000000d1"

func TestTermDates_PostsTheDocumentDateWithOnlyTheLookupDuty(t *testing.T) {
	signer, verifier := signed(t)
	var gotPath, gotMethod, gotBody string
	var gotToken string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		gotPath, gotMethod, gotBody, gotToken = r.URL.Path, r.Method, string(raw), r.Header.Get(tenant.HeaderServiceToken)
		_, _ = w.Write([]byte(`{"dueDate":"2027-03-31","discounts":[{"percent":2.5,"date":"2027-03-11"}],"ignored":true}`))
	}))
	defer srv.Close()

	got, err := party.New(srv.URL+"/", nil).WithSigner(signer).TermDates(ctx(), entity, term, "2027-03-01")

	if err != nil || got.DueDate != "2027-03-31" || len(got.Discounts) != 1 || got.Discounts[0].Percent.String() != "2.5" || got.Discounts[0].Date != "2027-03-11" {
		t.Fatalf("dates = %+v err = %v", got, err)
	}
	if gotMethod != http.MethodPost || gotPath != "/v1/party/payment-terms/"+term+":due-dates" || gotBody != `{"documentDate":"2027-03-01"}` {
		t.Fatalf("request = %s %s %s", gotMethod, gotPath, gotBody)
	}
	claims, err := verifier.Verify(gotToken)
	if err != nil || claims.Aud != "party" || claims.Le != entity || len(claims.Duties) != 1 || claims.Duties[0] != "party.vendor.lookup" {
		t.Fatalf("claims %+v err %v", claims, err)
	}
}

func TestTermDates_Failures(t *testing.T) {
	signer, _ := signed(t)
	tests := map[string]struct {
		status int
		body   string
		check  func(error) bool
	}{
		"not found":    {http.StatusNotFound, "", func(err error) bool { return errors.Is(err, party.ErrTermNotFound) }},
		"server error": {http.StatusInternalServerError, "", func(err error) bool { var se *party.StatusError; return errors.As(err, &se) && se.Status == 500 }},
		"bad body":     {http.StatusOK, "not json", func(err error) bool { return err != nil && !errors.Is(err, party.ErrTermNotFound) }},
	}
	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(tc.status)
				_, _ = w.Write([]byte(tc.body))
			}))
			defer srv.Close()
			if _, err := party.New(srv.URL, nil).WithSigner(signer).TermDates(ctx(), entity, term, "2027-03-01"); !tc.check(err) {
				t.Fatalf("err = %v", err)
			}
		})
	}
}

func TestTermDates_NeedsASigner(t *testing.T) {
	called := false
	srv := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { called = true }))
	defer srv.Close()
	if _, err := party.New(srv.URL, nil).TermDates(ctx(), entity, term, "2027-03-01"); !errors.Is(err, party.ErrUnsigned) || called {
		t.Fatalf("err = %v called = %v", err, called)
	}
}
