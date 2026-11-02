// Package oauth fetches OAuth 2.0 client-credentials access tokens (RFC 6749 section 4.4) for rest connection
// profiles. The caller passes an HTTP client that connects through the egress guard; the client secret and the token
// are never part of an error.
package oauth

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// Token is an access token and when to stop using it.
type Token struct {
	Value   string
	Expires time.Time
}

// Valid reports whether the token can still be used, with a margin for clock and network delay.
func (t Token) Valid() bool { return t.Value != "" && time.Now().Add(30*time.Second).Before(t.Expires) }

const maxTokenBody = 64 << 10

// ClientCredentials asks tokenURL for a token with the client id and secret sent as HTTP Basic (client_secret_basic).
func ClientCredentials(ctx context.Context, client *http.Client, tokenURL, clientID, clientSecret, scope string) (Token, error) {
	form := url.Values{"grant_type": {"client_credentials"}}
	if scope != "" {
		form.Set("scope", scope)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, tokenURL, strings.NewReader(form.Encode()))
	if err != nil {
		return Token{}, errors.New("invalid token URL")
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", "application/json")
	req.SetBasicAuth(url.QueryEscape(clientID), url.QueryEscape(clientSecret))
	resp, err := client.Do(req)
	if err != nil {
		var ue *url.Error
		if errors.As(err, &ue) {
			err = ue.Err // the URL error would repeat the request URL
		}
		return Token{}, fmt.Errorf("token request: %w", err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxTokenBody))
	if err != nil {
		return Token{}, errors.New("token response could not be read")
	}
	if resp.StatusCode != http.StatusOK {
		return Token{}, fmt.Errorf("the token endpoint answered %d; check the client id and secret", resp.StatusCode)
	}
	var out struct {
		AccessToken string      `json:"access_token"`
		TokenType   string      `json:"token_type"`
		ExpiresIn   json.Number `json:"expires_in"`
	}
	if err := json.Unmarshal(body, &out); err != nil || out.AccessToken == "" {
		return Token{}, errors.New("the token endpoint answered without an access_token")
	}
	if out.TokenType != "" && !strings.EqualFold(out.TokenType, "bearer") {
		return Token{}, fmt.Errorf("the token endpoint issued a %s token; only bearer tokens are supported", out.TokenType)
	}
	ttl := time.Hour
	if n, err := out.ExpiresIn.Int64(); err == nil && n > 0 {
		ttl = time.Duration(n) * time.Second
	}
	return Token{Value: out.AccessToken, Expires: time.Now().Add(ttl)}, nil
}
