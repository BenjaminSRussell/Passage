package upstream

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"

	"github.com/go-chi/chi/v5/middleware"

	"github.com/BenjaminSRussell/passage/platform/tenant"
)

var (
	ErrNotFound    = errors.New("upstream resource not found")
	ErrUnavailable = errors.New("upstream unavailable")
	// ErrServiceCaller means the caller is a service (it arrived with a service
	// token) and this service cannot sign its own token for the upstream: no
	// signing key, or no service grant to that audience. Nothing is sent, since
	// the forwarded headers of a service actor are refused upstream.
	ErrServiceCaller = errors.New("service caller cannot be forwarded upstream")
)

// Signing is how a service caller's request is passed on: a token minted by
// this service for Audience, restricted to Duties.
type Signing struct {
	Signer   *tenant.ServiceSigner
	Audience string
	Duties   []string
}

type authorizationKey struct{}

func WithAuthorization(ctx context.Context, value string) context.Context {
	return context.WithValue(ctx, authorizationKey{}, value)
}

func GetJSON(ctx context.Context, client *http.Client, url string, out any) error {
	return GetJSONSigned(ctx, client, url, out, nil)
}

// GetJSONSigned is GetJSON that, for a service caller, sends a token this
// service signs (see Signing) instead of forwarding the caller's headers. An end
// user's call is forwarded exactly as GetJSON does.
func GetJSONSigned(ctx context.Context, client *http.Client, url string, out any, signing *Signing) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return fmt.Errorf("%w: %v", ErrUnavailable, err)
	}
	if p, ok := tenant.FromContext(ctx); ok && (p.Service != "" || tenant.IsServiceActor(p.UserID)) {
		if signing == nil || signing.Signer == nil {
			return fmt.Errorf("%w: no service signing key configured", ErrServiceCaller)
		}
		if _, err := signing.Signer.ApplyDuties(req, p, signing.Audience, p.LegalEntityID, signing.Duties); err != nil {
			return fmt.Errorf("%w: %w", ErrServiceCaller, err)
		}
	} else if ok {
		req.Header.Set(tenant.HeaderTenant, p.TenantID)
		req.Header.Set(tenant.HeaderLegalEntity, p.LegalEntityID)
		req.Header.Set(tenant.HeaderUser, p.UserID)
		duties := make([]string, 0, len(p.Duties))
		for d := range p.Duties {
			duties = append(duties, d)
		}
		req.Header.Set(tenant.HeaderDuties, strings.Join(duties, ","))
	}
	if v, _ := ctx.Value(authorizationKey{}).(string); v != "" {
		req.Header.Set("Authorization", v)
	}
	if id := middleware.GetReqID(ctx); id != "" {
		req.Header.Set(middleware.RequestIDHeader, id)
	}
	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("%w: %v", ErrUnavailable, err)
	}
	defer resp.Body.Close()
	switch {
	case resp.StatusCode == http.StatusNotFound:
		return ErrNotFound
	case resp.StatusCode != http.StatusOK:
		return fmt.Errorf("%w: status %d", ErrUnavailable, resp.StatusCode)
	}
	if err := json.NewDecoder(resp.Body).Decode(out); err != nil {
		return fmt.Errorf("%w: decode: %v", ErrUnavailable, err)
	}
	return nil
}
