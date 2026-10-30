package store

import (
	"context"

	"github.com/jackc/pgx/v5"

	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/platform/tenant"
)

const (
	CodeLedgerNotFound = "REPORT-0031"
	CodeLedgerEntity   = "REPORT-0032"
)

// ledgerEntityTx derives the legal entity that owns a ledger from the ledger's
// own read-model rows (reporting_hwm and reporting_balance), read under the
// caller's RLS scope. The requested entity is only an assertion: when present
// it must equal the derived one. found is false when the caller sees no row of
// the ledger, which is also what a ledger of another entity looks like. An empty
// owner with found true is a legacy ledger without an assigned entity.
func ledgerEntityTx(ctx context.Context, tx pgx.Tx, ledgerID, requested string) (owner string, found bool, err error) {
	rows, err := tx.Query(ctx, `select distinct legal_entity_id::text from (
			select legal_entity_id from reporting_hwm where ledger_id=$1::uuid
			union all
			select legal_entity_id from reporting_balance where ledger_id=$1::uuid) l`, ledgerID)
	if err != nil {
		return "", false, err
	}
	defer rows.Close()
	var entities []*string
	for rows.Next() {
		var e *string
		if err := rows.Scan(&e); err != nil {
			return "", false, err
		}
		entities = append(entities, e)
	}
	if err := rows.Err(); err != nil {
		return "", false, err
	}
	switch len(entities) {
	case 0:
		return "", false, nil
	case 1:
	default:
		return "", false, problem(422, CodeLedgerEntity, "ledger "+ledgerID+" spans more than one legal entity; narrow the caller's legal-entity scope")
	}
	if entities[0] != nil {
		owner = *entities[0]
	}
	if requested != "" {
		asserted, ok := ids.Canonical(requested)
		if !ok || asserted != owner {
			return "", false, problem(422, CodeLedgerEntity, "legalEntityId does not match the legal entity that owns ledger "+ledgerID)
		}
	}
	return owner, true, nil
}

func requireLedgerEntityTx(ctx context.Context, tx pgx.Tx, ledgerID, requested string) (string, error) {
	owner, found, err := ledgerEntityTx(ctx, tx, ledgerID, requested)
	if err != nil {
		return "", err
	}
	if !found {
		return "", problem(404, CodeLedgerNotFound, "ledger "+ledgerID+" has no reporting data visible to the caller")
	}
	return owner, nil
}

func auditEntityTx(ctx context.Context, tx pgx.Tx, ledgerID, requested string) (string, error) {
	owner, found, err := ledgerEntityTx(ctx, tx, ledgerID, requested)
	if err != nil || found {
		return owner, err
	}
	if p, _ := tenant.FromContext(ctx); p.LegalEntityID == "" && requested == "" {
		return "", nil
	}
	return httpx.OwnerLegalEntity(ctx, requested)
}
