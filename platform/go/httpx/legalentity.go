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
// owns the row; a caller scoped to several and a caller with no legal entity
// (all-entity) must name it (422 with a legalEntityId field error), because a
// row stored without an entity is invisible to every scoped caller. Callers
// creating a child of a row that already has an entity derive it from that
// parent instead and treat the request value as an assertion. A legal-entity
// claim with no valid UUID in it fails closed (403), as the scope GUC does.
func OwnerLegalEntity(ctx context.Context, requested string) (string, error) {
	p, ok := tenant.FromContext(ctx)
	if !ok {
		return "", NewProblem(http.StatusUnauthorized, "AUTH-0001", "no principal")
	}
	var scopes []string
	if p.LegalEntityID != "" {
		for _, part := range strings.Split(p.LegalEntityID, ",") {
			if id, ok := ids.Canonical(strings.TrimSpace(part)); ok && id != ids.Nil {
				scopes = append(scopes, id)
			}
		}
		if len(scopes) == 0 {
			return "", NewProblem(http.StatusForbidden, "AUTH-0003", "the caller's legal-entity scope holds no valid legal entity")
		}
	}
	if requested == "" {
		if len(scopes) == 1 {
			return scopes[0], nil
		}
		return "", fieldProblem("legalEntityId is required for a caller scoped to several legal entities or to all of them")
	}
	entity, ok := ids.Canonical(requested)
	if !ok || entity == ids.Nil {
		return "", fieldProblem("legalEntityId must be a UUID")
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

func fieldProblem(message string) *Problem {
	p := NewProblem(http.StatusUnprocessableEntity, "COM-0001", "request failed validation")
	p.Errors = map[string]string{"legalEntityId": message}
	return p
}
