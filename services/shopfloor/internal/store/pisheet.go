package store

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/BenjaminSRussell/passage/platform/dbx"
	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/platform/tenant"
	"github.com/BenjaminSRussell/passage/services/shopfloor/internal/domain"
)

// PI sheet problem codes (see README).
const (
	CodePISheetNotFound = "SF-0180" // 404
	CodePISheetState    = "SF-0181" // 409: the sheet is not in a state the command needs
	CodePISheetRev      = "SF-0182" // 409: the sheet changed since it was read
	CodePISheetCode     = "SF-0183" // 409: the code has a draft already, or the version exists
	CodePISheetUnusable = "SF-0184" // 422: a recipe can be made only from a released sheet
)

// PISheet is one version of a PI sheet.
type PISheet struct {
	ID         string          `json:"id"`
	Code       string          `json:"code"`
	Version    int             `json:"version"`
	Name       string          `json:"name"`
	Status     string          `json:"status"`
	Doc        json.RawMessage `json:"recipe"`
	Rev        int             `json:"rev"`
	CreatedBy  string          `json:"createdBy"`
	CreatedAt  time.Time       `json:"createdAt"`
	UpdatedBy  string          `json:"updatedBy"`
	UpdatedAt  time.Time       `json:"updatedAt"`
	ReleasedBy *string         `json:"releasedBy"`
	ReleasedAt *time.Time      `json:"releasedAt"`
}

// PISheetFilter narrows the list.
type PISheetFilter struct {
	Status string
	Code   string
	Limit  int
}

const piCols = `id::text, code, version, name, status, doc, rev, created_by, created_at, updated_by, updated_at, released_by, released_at`

func scanPISheet(row pgx.Row) (PISheet, error) {
	var s PISheet
	err := row.Scan(&s.ID, &s.Code, &s.Version, &s.Name, &s.Status, &s.Doc, &s.Rev, &s.CreatedBy, &s.CreatedAt, &s.UpdatedBy, &s.UpdatedAt, &s.ReleasedBy, &s.ReleasedAt)
	return s, err
}

func piNotFound() error {
	return httpx.NewProblem(http.StatusNotFound, CodePISheetNotFound, "PI sheet not found")
}

func piCodeTaken(err error, detail string) error {
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) && pgErr.Code == uniqueViolation {
		return httpx.NewProblem(http.StatusConflict, CodePISheetCode, detail)
	}
	return err
}

// CreatePISheet stores a new sheet as version 1, a draft.
func (s *Postgres) CreatePISheet(ctx context.Context, code, name string, doc domain.RecipeDoc, by string) (PISheet, error) {
	p, _ := tenant.FromContext(ctx)
	raw, err := json.Marshal(doc)
	if err != nil {
		return PISheet{}, err
	}
	var out PISheet
	err = dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		var err error
		out, err = scanPISheet(tx.QueryRow(ctx, `insert into pi_sheet (tenant_id, id, code, version, name, status, doc, created_by, updated_by)
			values ($1, $2, $3, 1, $4, 'draft', $5, $6, $6) returning `+piCols, p.TenantID, ids.New(), code, name, raw, by))
		return piCodeTaken(err, "a PI sheet with the code "+code+" exists; make a new version of it instead")
	})
	return out, err
}

// ListPISheets returns the sheets by code and newest version first.
func (s *Postgres) ListPISheets(ctx context.Context, f PISheetFilter) ([]PISheet, error) {
	out := []PISheet{}
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `select `+piCols+` from pi_sheet where ($1 = '' or status = $1) and ($2 = '' or code = $2)
			order by code, version desc limit $3`, f.Status, f.Code, f.Limit)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			sh, err := scanPISheet(rows)
			if err != nil {
				return err
			}
			out = append(out, sh)
		}
		return rows.Err()
	})
	return out, err
}

// GetPISheet returns one sheet.
func (s *Postgres) GetPISheet(ctx context.Context, id string) (PISheet, error) {
	var out PISheet
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		var err error
		out, err = scanPISheet(tx.QueryRow(ctx, `select `+piCols+` from pi_sheet where id = $1`, id))
		if errors.Is(err, pgx.ErrNoRows) {
			return piNotFound()
		}
		return err
	})
	return out, err
}

func lockPISheet(ctx context.Context, tx pgx.Tx, id string) (PISheet, error) {
	sh, err := scanPISheet(tx.QueryRow(ctx, `select `+piCols+` from pi_sheet where id = $1 for update`, id))
	if errors.Is(err, pgx.ErrNoRows) {
		return sh, piNotFound()
	}
	return sh, err
}

func piStale(rev int, sh PISheet) error {
	if rev != sh.Rev {
		return httpx.NewProblem(http.StatusConflict, CodePISheetRev, "the PI sheet changed since you read it; reload it")
	}
	return nil
}

// UpdatePISheet changes the name and document of a draft.
func (s *Postgres) UpdatePISheet(ctx context.Context, id string, rev int, name string, doc domain.RecipeDoc, by string) (PISheet, error) {
	raw, err := json.Marshal(doc)
	if err != nil {
		return PISheet{}, err
	}
	var out PISheet
	err = dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		sh, err := lockPISheet(ctx, tx, id)
		if err != nil {
			return err
		}
		if sh.Status != "draft" {
			return httpx.NewProblem(http.StatusConflict, CodePISheetState, "only a draft can be edited; make a new version of a released sheet")
		}
		if err := piStale(rev, sh); err != nil {
			return err
		}
		out, err = scanPISheet(tx.QueryRow(ctx, `update pi_sheet set name = $2, doc = $3, rev = rev + 1, updated_by = $4, updated_at = now() where id = $1 returning `+piCols, id, name, raw, by))
		return err
	})
	return out, err
}

// ReleasePISheet makes a draft immutable and the sheet recipes are made from. Releasing a version makes the
// earlier released versions of the code obsolete, so there is one released version at a time.
func (s *Postgres) ReleasePISheet(ctx context.Context, id string, rev int, by string) (PISheet, error) {
	var out PISheet
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		sh, err := lockPISheet(ctx, tx, id)
		if err != nil {
			return err
		}
		if sh.Status != "draft" {
			return httpx.NewProblem(http.StatusConflict, CodePISheetState, "only a draft can be released")
		}
		if err := piStale(rev, sh); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `update pi_sheet set status = 'obsolete', rev = rev + 1, updated_by = $3, updated_at = now() where code = $1 and status = 'released' and id <> $2`, sh.Code, id, by); err != nil {
			return err
		}
		out, err = scanPISheet(tx.QueryRow(ctx, `update pi_sheet set status = 'released', rev = rev + 1, updated_by = $2, updated_at = now(), released_by = $2, released_at = now() where id = $1 returning `+piCols, id, by))
		return err
	})
	return out, err
}

// NewPISheetVersion copies a released sheet into the next version of its code, a draft. A code has at most one
// draft.
func (s *Postgres) NewPISheetVersion(ctx context.Context, id string, by string) (PISheet, error) {
	p, _ := tenant.FromContext(ctx)
	var out PISheet
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		sh, err := lockPISheet(ctx, tx, id)
		if err != nil {
			return err
		}
		if sh.Status != "released" {
			return httpx.NewProblem(http.StatusConflict, CodePISheetState, "a new version is made from a released sheet")
		}
		out, err = scanPISheet(tx.QueryRow(ctx, `insert into pi_sheet (tenant_id, id, code, version, name, status, doc, created_by, updated_by)
			values ($1, $2, $3, (select max(version) + 1 from pi_sheet where code = $3), $4, 'draft', $5, $6, $6) returning `+piCols,
			p.TenantID, ids.New(), sh.Code, sh.Name, []byte(sh.Doc), by))
		return piCodeTaken(err, "the PI sheet "+sh.Code+" has a draft already; finish or release it first")
	})
	return out, err
}

// ObsoletePISheet retires a released sheet: recipes can no longer be made from it, those already made are unchanged.
func (s *Postgres) ObsoletePISheet(ctx context.Context, id string, rev int, by string) (PISheet, error) {
	var out PISheet
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		sh, err := lockPISheet(ctx, tx, id)
		if err != nil {
			return err
		}
		if sh.Status != "released" {
			return httpx.NewProblem(http.StatusConflict, CodePISheetState, "only a released sheet can be made obsolete")
		}
		if err := piStale(rev, sh); err != nil {
			return err
		}
		out, err = scanPISheet(tx.QueryRow(ctx, `update pi_sheet set status = 'obsolete', rev = rev + 1, updated_by = $2, updated_at = now() where id = $1 returning `+piCols, id, by))
		return err
	})
	return out, err
}

// CreateControlRecipeFromPISheet makes a kiosk or process control recipe from the document of a released
// sheet, for an order and resource; the sheet's id is kept on the recipe. The sheet is read under a share lock in
// the transaction that stores the recipe, so it cannot be made obsolete or changed in between. Idempotent by the key.
func (s *Postgres) CreateControlRecipeFromPISheet(ctx context.Context, piSheetID string, in ControlRecipeInput, by string, idem Idem) (ControlRecipe, error) {
	var out ControlRecipe
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		sh, err := scanPISheet(tx.QueryRow(ctx, `select `+piCols+` from pi_sheet where id = $1 for share`, piSheetID))
		if errors.Is(err, pgx.ErrNoRows) {
			return piNotFound()
		}
		if err != nil {
			return err
		}
		if sh.Status != "released" {
			return httpx.NewProblem(http.StatusUnprocessableEntity, CodePISheetUnusable, "a control recipe is made from a released PI sheet; this one is "+sh.Status)
		}
		var doc domain.RecipeDoc
		if err := json.Unmarshal(sh.Doc, &doc); err != nil {
			return err
		}
		if f := doc.FieldErrors(in.Destination); len(f) > 0 {
			for k, v := range f {
				return httpx.NewProblem(http.StatusUnprocessableEntity, CodePISheetUnusable, "the sheet does not fit this destination: "+strings.TrimPrefix(k, "recipe.")+" "+v)
			}
		}
		in.Recipe = doc
		out, err = createControlRecipeTx(ctx, tx, in, &sh.ID, by, idem)
		return err
	})
	return out, err
}
