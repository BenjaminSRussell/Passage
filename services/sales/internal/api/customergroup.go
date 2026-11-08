package api

import (
	"context"
	"errors"
	"log/slog"
	"net/http"

	"github.com/go-chi/chi/v5/middleware"

	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/services/sales/internal/upstream"
)

const (
	codeCustomerGroupMismatch    = "SLS-0151"
	codeCustomerGroupUnavailable = "SLS-0152"
	codeCustomerUnknown          = "SLS-0153"
)

// CustomerGroupSource reads a customer's group from the party service, which
// owns it. The group on an order or quotation always comes from here, never
// from the request, so a rep cannot name another group to dodge a group-level
// listing DENY or meet a group ALLOW.
type CustomerGroupSource interface {
	CustomerGroup(ctx context.Context, legalEntityID, customerID string) (string, error)
}

// WithCustomerGroups sets the party-backed customer group source.
func (a *API) WithCustomerGroups(s CustomerGroupSource) *API {
	a.groups = s
	return a
}

// resolveGroup returns the customer's group from the party service. A client
// supplied group is refused with 422 SLS-0151 when it differs from the party
// group id (an equal one is accepted; a free-text code never is). When the party service is not
// configured or fails, the request is refused with 503 SLS-0152: the typed
// group is never used as a fallback.
func (a *API) resolveGroup(ctx context.Context, legalEntityID, customerID string, supplied *string) (*string, error) {
	if a.groups == nil {
		return nil, httpx.NewProblem(http.StatusServiceUnavailable, codeCustomerGroupUnavailable, "the customer group cannot be read: the party service is not configured")
	}
	group, err := a.groups.CustomerGroup(ctx, legalEntityID, customerID)
	switch {
	case errors.Is(err, upstream.ErrCustomerUnknown):
		return nil, httpx.NewProblem(http.StatusUnprocessableEntity, codeCustomerUnknown, "the customer is not known to the party service in this legal entity")
	case err != nil:
		slog.ErrorContext(ctx, "customer group lookup failed", "err", err, "customerId", customerID, "correlationId", middleware.GetReqID(ctx))
		return nil, httpx.NewProblem(http.StatusServiceUnavailable, codeCustomerGroupUnavailable, "the party service could not confirm the customer group; retry")
	}
	if supplied != nil && *supplied != group {
		return nil, httpx.NewProblem(http.StatusUnprocessableEntity, codeCustomerGroupMismatch, "customerGroup differs from the customer's group in the party service; omit it")
	}
	return &group, nil
}
