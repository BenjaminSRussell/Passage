package oauth_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/BenjaminSRussell/passage/services/migration/internal/oauth"
)

func TestClientCredentials(t *testing.T) {
	var gotForm url.Values
	var gotID, gotSecret string
	body := `{"access_token":"tok","token_type":"bearer","expires_in":120}`
	status := http.StatusOK
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = r.ParseForm()
		gotForm = r.PostForm
		gotID, gotSecret, _ = r.BasicAuth()
		w.WriteHeader(status)
		_, _ = w.Write([]byte(body))
	}))
	defer srv.Close()
	ctx := context.Background()

	tok, err := oauth.ClientCredentials(ctx, srv.Client(), srv.URL, "id 1", "sec/ret", "a b")
	if err != nil || tok.Value != "tok" || !tok.Valid() || tok.Expires.After(time.Now().Add(3*time.Minute)) {
		t.Fatalf("%+v %v", tok, err)
	}
	if gotForm.Get("grant_type") != "client_credentials" || gotForm.Get("scope") != "a b" || gotID != "id+1" || gotSecret != "sec%2Fret" {
		t.Fatalf("request: %v %q %q", gotForm, gotID, gotSecret)
	}
	if (oauth.Token{Value: "x", Expires: time.Now().Add(10 * time.Second)}).Valid() {
		t.Error("a token about to expire is valid")
	}

	for name, c := range map[string]struct {
		status int
		body   string
		want   string
	}{
		"401":      {401, `{"error":"invalid_client","error_description":"bad sec/ret"}`, "401"},
		"no token": {200, `{"token_type":"bearer"}`, "access_token"},
		"mac":      {200, `{"access_token":"t","token_type":"mac"}`, "bearer"},
		"not json": {200, `<html>`, "access_token"},
	} {
		status, body = c.status, c.body
		_, err := oauth.ClientCredentials(ctx, srv.Client(), srv.URL, "id", "sec/ret", "")
		if err == nil || !strings.Contains(err.Error(), c.want) || strings.Contains(err.Error(), "sec/ret") {
			t.Errorf("%s: %v", name, err)
		}
	}
	if _, err := oauth.ClientCredentials(ctx, srv.Client(), "http://\x7f", "id", "s", ""); err == nil {
		t.Error("bad URL accepted")
	}
	srv.Close()
	if _, err := oauth.ClientCredentials(ctx, http.DefaultClient, srv.URL+"/tok-url-secret", "id", "s", ""); err == nil || strings.Contains(err.Error(), "tok-url-secret") {
		t.Errorf("network error repeats the URL: %v", err)
	}
}
