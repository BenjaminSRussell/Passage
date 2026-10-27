package store

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/BenjaminSRussell/passage/platform/dbx"
	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/platform/tenant"
)

// Valuation setup problem codes (FR-20.15, FR-20.16).
const (
	CodeLedgerDuplicate      = "CST-VLG-002"
	CodePrimaryLedgerTaken   = "CST-VLG-003"
	CodeLedgerGroupTaken     = "CST-VLG-004"
	CodeLedgerNotFound       = "CST-VLG-005"
	CodeCategoryDuplicate    = "CST-VAL-004"
	constraintLedgerPrimary  = "valuation_ledger_one_primary"
	constraintLedgerGroup    = "valuation_ledger_group_unique"
	valuationLedgerColumns   = "id::text, legal_entity_id::text, ledger_group_id::text, code, method, currency, rate_type, is_primary, created_at"
	valuationCategoryColumns = "id::text, code, is_batch, types, created_at"
)

// ValuationLedger is a parallel inventory valuation ledger of a legal entity,
// bound to a wave 12 ledger group with its own costing method, currency and
// exchange rate type (FR-20.15). At most one ledger per legal entity is primary.
type ValuationLedger struct {
	ID            string    `json:"id"`
	LegalEntityID string    `json:"legalEntityId"`
	LedgerGroupID string    `json:"ledgerGroupId"`
	Code          string    `json:"code"`
	Method        string    `json:"method"`
	Currency      string    `json:"currency"`
	RateType      string    `json:"rateType"`
	Primary       bool      `json:"primary"`
	CreatedAt     time.Time `json:"createdAt"`
}

// ValuationType is one valuation type of a category and its inventory account.
type ValuationType struct {
	Code    string `json:"code"`
	Account string `json:"account"`
}

// ValuationCategory splits a product's stock into valuation types (FR-20.16).
// A batch category has no fixed types: each batch is its own type at receipt.
type ValuationCategory struct {
	ID        string          `json:"id"`
	Code      string          `json:"code"`
	Batch     bool            `json:"batch"`
	Types     []ValuationType `json:"types"`
	CreatedAt time.Time       `json:"createdAt"`
}

func scanLedger(row pgx.Row) (ValuationLedger, error) {
	var v ValuationLedger
	err := row.Scan(&v.ID, &v.LegalEntityID, &v.LedgerGroupID, &v.Code, &v.Method, &v.Currency, &v.RateType, &v.Primary, &v.CreatedAt)
	return v, err
}

// CreateValuationLedger stores a ledger. A code or ledger group already used in
// the legal entity is refused, and so is a second primary ledger.
func (s *Postgres) CreateValuationLedger(ctx context.Context, in ValuationLedger) (ValuationLedger, error) {
	p, _ := tenant.FromContext(ctx)
	var out ValuationLedger
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		var err error
		out, err = scanLedger(tx.QueryRow(ctx, `insert into valuation_ledger (tenant_id, id, legal_entity_id, ledger_group_id, code, method,
				currency, rate_type, is_primary)
			values ($1, $2, $3, $4, $5, $6, $7, $8, $9)
			returning `+valuationLedgerColumns,
			p.TenantID, ids.New(), in.LegalEntityID, in.LedgerGroupID, in.Code, in.Method, in.Currency, in.RateType, in.Primary))
		return err
	})
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) && pgErr.Code == uniqueViolation {
		switch pgErr.ConstraintName {
		case constraintLedgerPrimary:
			return ValuationLedger{}, httpx.NewProblem(http.StatusConflict, CodePrimaryLedgerTaken, "the legal entity already has a primary valuation ledger")
		case constraintLedgerGroup:
			return ValuationLedger{}, httpx.NewProblem(http.StatusConflict, CodeLedgerGroupTaken, "the ledger group already has a valuation ledger in this legal entity")
		default:
			return ValuationLedger{}, httpx.NewProblem(http.StatusConflict, CodeLedgerDuplicate, "a valuation ledger with this code already exists in the legal entity")
		}
	}
	if err != nil {
		return ValuationLedger{}, err
	}
	return out, nil
}

// ListValuationLedgers returns the valuation ledgers in the caller's scope,
// optionally of one legal entity, primary first and then by code.
func (s *Postgres) ListValuationLedgers(ctx context.Context, legalEntityID string) ([]ValuationLedger, error) {
	p, _ := tenant.FromContext(ctx)
	out := []ValuationLedger{}
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `select `+valuationLedgerColumns+` from valuation_ledger
			where tenant_id = $1 and ($2 = '' or legal_entity_id::text = $2)
			order by legal_entity_id, is_primary desc, code`, p.TenantID, legalEntityID)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			v, err := scanLedger(rows)
			if err != nil {
				return err
			}
			out = append(out, v)
		}
		return rows.Err()
	})
	return out, err
}

// GetValuationLedger returns one ledger.
func (s *Postgres) GetValuationLedger(ctx context.Context, id string) (ValuationLedger, error) {
	p, _ := tenant.FromContext(ctx)
	var out ValuationLedger
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		var err error
		out, err = scanLedger(tx.QueryRow(ctx, `select `+valuationLedgerColumns+` from valuation_ledger where tenant_id = $1 and id = $2`, p.TenantID, id))
		if errors.Is(err, pgx.ErrNoRows) {
			return httpx.NewProblem(http.StatusNotFound, CodeLedgerNotFound, "valuation ledger not found")
		}
		return err
	})
	return out, err
}

func scanCategory(row pgx.Row) (ValuationCategory, error) {
	var c ValuationCategory
	var raw []byte
	if err := row.Scan(&c.ID, &c.Code, &c.Batch, &raw, &c.CreatedAt); err != nil {
		return ValuationCategory{}, err
	}
	err := json.Unmarshal(raw, &c.Types)
	return c, err
}

// CreateValuationCategory stores a category; a code already used is refused.
func (s *Postgres) CreateValuationCategory(ctx context.Context, in ValuationCategory) (ValuationCategory, error) {
	p, _ := tenant.FromContext(ctx)
	types := in.Types
	if types == nil {
		types = []ValuationType{}
	}
	raw, err := json.Marshal(types)
	if err != nil {
		return ValuationCategory{}, err
	}
	var out ValuationCategory
	err = dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		var err error
		out, err = scanCategory(tx.QueryRow(ctx, `insert into valuation_category (tenant_id, id, code, is_batch, types)
			values ($1, $2, $3, $4, $5::jsonb)
			returning `+valuationCategoryColumns, p.TenantID, ids.New(), in.Code, in.Batch, string(raw)))
		return err
	})
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) && pgErr.Code == uniqueViolation {
		return ValuationCategory{}, httpx.NewProblem(http.StatusConflict, CodeCategoryDuplicate, "a valuation category with this code already exists")
	}
	if err != nil {
		return ValuationCategory{}, err
	}
	return out, nil
}

// ListValuationCategories returns the tenant's valuation categories by code.
func (s *Postgres) ListValuationCategories(ctx context.Context) ([]ValuationCategory, error) {
	p, _ := tenant.FromContext(ctx)
	out := []ValuationCategory{}
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `select `+valuationCategoryColumns+` from valuation_category where tenant_id = $1 order by code`, p.TenantID)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			c, err := scanCategory(rows)
			if err != nil {
				return err
			}
			out = append(out, c)
		}
		return rows.Err()
	})
	return out, err
}
