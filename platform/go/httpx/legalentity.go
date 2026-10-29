package httpx

import (
	"context"
	"net/http"
	"strings"

	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/platform/tenant"
)

// OwnerLegalEntity returns the legal entity a new row belongs to, for services
// whose parent tables carry legal_entity_id. A requested entity must be a UUID
// inside the caller's scope. Without one, a caller scoped to exactly one entity
// owns the row; an all-entity caller leaves it unassigned (empty string, stored
// as NULL and invisible to scoped callers); a caller scoped to several must
// name the entity.
func OwnerLegalEntity(ctx context.Context, requested string) (string, error) {
	p, ok := tenant.FromContext(ctx)
	if !ok {
		return "", NewProblem(http.StatusUnauthorized, "AUTH-0001", "no principal")
	}
	var scopes []string
	if p.LegalEntityID != "" && p.LegalEntityID != "*" {
		scopes = strings.Split(p.LegalEntityID, ",")
	}
	if requested == "" {
		switch len(scopes) {
		case 0:
			return "", nil
		case 1:
			return scopes[0], nil
		}
		return "", NewProblem(http.StatusUnprocessableEntity, "COM-0001", "legalEntityId is required for a caller scoped to several legal entities")
	}
	entity, ok := ids.Canonical(requested)
	if !ok || entity == ids.Nil {
		return "", NewProblem(http.StatusUnprocessableEntity, "COM-0001", "legalEntityId must be a UUID")
	}
	if len(scopes) == 0 {
		return entity, nil
	}
	for _, s := range scopes {
		if s == entity {
			return entity, nil
		}
	}
	return "", NewProblem(http.StatusForbidden, "AUTH-0003", "legalEntityId is outside the caller's legal-entity scope")
}
