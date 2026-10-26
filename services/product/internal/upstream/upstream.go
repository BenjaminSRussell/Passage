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
)

type authorizationKey struct{}

func WithAuthorization(ctx context.Context, value string) context.Context {
	return context.WithValue(ctx, authorizationKey{}, value)
}

func GetJSON(ctx context.Context, client *http.Client, url string, out any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return fmt.Errorf("%w: %v", ErrUnavailable, err)
	}
	if p, ok := tenant.FromContext(ctx); ok {
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
