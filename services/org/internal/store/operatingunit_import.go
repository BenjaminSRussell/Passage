package store

import (
	"context"
	"errors"
	"net/http"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/BenjaminSRussell/passage/platform/dbx"
	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/platform/tenant"
	orgevents "github.com/BenjaminSRussell/passage/services/org/internal/events"
)

// Row outcomes of an operating-unit import.
const (
	ImportCreated   = "created"
	ImportUnchanged = "unchanged"
	ImportFailed    = "failed"
)

// OperatingUnitImportRow is one validated CSV row; Line is its 1-based position in the
// file's data rows. LegalEntityCode is the code of the default legal entity, or empty.
type OperatingUnitImportRow struct {
	Line            int
	Type            string
	Code            string
	Name            string
	LegalEntityCode string
}

// OperatingUnitImportResult is the outcome for one row. Errors maps a field to its problem.
type OperatingUnitImportResult struct {
	Line   int
	Type   string
	Code   string
	Status string
	ID     string
	Errors map[string]string
}

// ImportOperatingUnits checks every row against the tenant's data and, unless dryRun is
// set and only when no row failed, creates the missing units in one transaction with one
// operatingunit.created event each. A row that matches an existing unit exactly (name and
// default legal entity) is unchanged, so running the same file twice writes nothing;
// a row that differs from an existing unit fails, since an import never updates.
func (s *Postgres) ImportOperatingUnits(ctx context.Context, rows []OperatingUnitImportRow, dryRun bool) ([]OperatingUnitImportResult, error) {
	p, _ := tenant.FromContext(ctx)
	var results []OperatingUnitImportResult
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		results = make([]OperatingUnitImportResult, 0, len(rows))
		entities, err := entitiesByCode(ctx, tx, p.TenantID, rows)
		if err != nil {
			return err
		}
		existing, err := unitsByKey(ctx, tx, p.TenantID, rows)
		if err != nil {
			return err
		}
		seen := map[string]int{}
		failed := false
		var pending []int
		entityIDs := make([]*string, len(rows))
		for i, row := range rows {
			res := OperatingUnitImportResult{Line: row.Line, Type: row.Type, Code: row.Code, Status: ImportCreated, Errors: map[string]string{}}
			key := row.Type + "/" + row.Code
			var entityID *string
			if row.LegalEntityCode != "" {
				e, ok := entities[row.LegalEntityCode]
				if !ok || e.status != "active" {
					res.Errors["legalEntity"] = "is not an active legal entity visible to the caller"
				} else {
					id := e.id
					entityID = &id
				}
			}
			if _, dup := seen[key]; dup {
				res.Errors["code"] = "repeats the type and code of an earlier row"
			}
			seen[key] = row.Line
			if cur, ok := existing[key]; ok && len(res.Errors) == 0 {
				if cur.Name == row.Name && deref(cur.DefaultLegalEntityID) == deref(entityID) {
					res.Status, res.ID = ImportUnchanged, cur.ID
				} else {
					res.Errors["code"] = "exists with a different name or default legal entity; an import never updates"
				}
			}
			if len(res.Errors) > 0 {
				res.Status, failed = ImportFailed, true
			} else if res.Status == ImportCreated {
				pending = append(pending, i)
				res.ID, entityIDs[i] = ids.New(), entityID
			}
			results = append(results, res)
		}
		if dryRun || failed {
			return nil
		}
		for _, i := range pending {
			row, res := rows[i], results[i]
			if _, err := tx.Exec(ctx, `insert into operating_unit (tenant_id, id, type, code, name, default_legal_entity_id)
				values ($1, $2, $3, $4, $5, $6)`, p.TenantID, res.ID, row.Type, row.Code, row.Name, entityIDs[i]); err != nil {
				return err
			}
			if err := enqueue(ctx, tx, "operatingunit", res.ID, orgevents.OperatingUnitCreated, p.TenantID,
				orgevents.OperatingUnitCreatedData{ID: res.ID, Type: row.Type, Code: row.Code, Name: row.Name}); err != nil {
				return err
			}
		}
		return nil
	})
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) && pgErr.Code == uniqueViolation {
		// Another request created one of the units after this file was checked; nothing was written.
		return nil, httpx.NewProblem(http.StatusConflict, CodeOUCodeDuplicate, "an operating unit in the file was created by another request; run the file again")
	}
	return results, err
}

type importEntity struct{ id, status string }

func entitiesByCode(ctx context.Context, tx pgx.Tx, tenantID string, rows []OperatingUnitImportRow) (map[string]importEntity, error) {
	var codes []string
	for _, r := range rows {
		if r.LegalEntityCode != "" {
			codes = append(codes, r.LegalEntityCode)
		}
	}
	out := map[string]importEntity{}
	if len(codes) == 0 {
		return out, nil
	}
	found, err := tx.Query(ctx, `select code, id, status from legal_entity where tenant_id = $1 and code = any($2)`, tenantID, codes)
	if err != nil {
		return nil, err
	}
	defer found.Close()
	for found.Next() {
		var code string
		var e importEntity
		if err := found.Scan(&code, &e.id, &e.status); err != nil {
			return nil, err
		}
		out[code] = e
	}
	return out, found.Err()
}

func unitsByKey(ctx context.Context, tx pgx.Tx, tenantID string, rows []OperatingUnitImportRow) (map[string]OperatingUnit, error) {
	codes := make([]string, len(rows))
	for i, r := range rows {
		codes[i] = r.Code
	}
	found, err := tx.Query(ctx, `select `+operatingUnitColumns+` from operating_unit where tenant_id = $1 and code = any($2)`, tenantID, codes)
	if err != nil {
		return nil, err
	}
	defer found.Close()
	out := map[string]OperatingUnit{}
	for found.Next() {
		u, err := scanOperatingUnit(found)
		if err != nil {
			return nil, err
		}
		out[u.Type+"/"+u.Code] = u
	}
	return out, found.Err()
}
