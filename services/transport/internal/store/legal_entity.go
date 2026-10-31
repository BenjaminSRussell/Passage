package store

import (
	"context"
	"errors"
	"net/http"

	"github.com/jackc/pgx/v5"

	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/platform/ids"
)

func ownerOrParentTx(ctx context.Context, tx pgx.Tx, table, tenantID, parentID, requested string) (string, error) {
	if parentID == "" {
		return httpx.OwnerLegalEntity(ctx, requested)
	}
	return parentEntityTx(ctx, tx, table, tenantID, parentID, requested)
}

const (
	parentLoad        = "load"
	parentFreightBill = "freight_bill"
	parentVoyage      = "voyage"
)

func parentEntityTx(ctx context.Context, tx pgx.Tx, table, tenantID, id, requested string) (string, error) {
	var entity string
	err := tx.QueryRow(ctx, `select coalesce(legal_entity_id::text, '') from `+table+` where tenant_id = $1 and id = $2`, tenantID, id).Scan(&entity)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", NotFound(map[string]string{parentLoad: "load", parentFreightBill: "freight bill"}[table])
	}
	if err != nil {
		return "", err
	}
	return entity, assertParentEntity(table, requested, entity)
}

func assertParentEntity(table, requested, entity string) error {
	if requested == "" {
		return nil
	}
	if asserted, ok := ids.Canonical(requested); !ok || asserted != entity {
		bad := httpx.NewProblem(http.StatusUnprocessableEntity, "COM-0001", "request failed validation")
		bad.Errors = map[string]string{"legalEntityId": "does not match the legal entity of the referenced " + table}
		return bad
	}
	return nil
}
