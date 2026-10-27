package store

import (
	"context"
	"errors"
	"net/http"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/BenjaminSRussell/passage/platform/dbx"
	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/services/ledger/internal/domain"
)

// ReasonCode is a shared financial reason code (FR-11.15). Codes are deactivated,
// never deleted, because journal entries reference them.
type ReasonCode struct {
	Code                          string    `json:"code"`
	Name                          string    `json:"name"`
	AppliesTo                     []string  `json:"appliesTo"`
	RequiresComment               bool      `json:"requiresComment"`
	NegativePostingAllowed        bool      `json:"negativePostingAllowed"`
	AlternativePostingDateAllowed bool      `json:"alternativePostingDateAllowed"`
	Active                        bool      `json:"active"`
	UpdatedAt                     time.Time `json:"updatedAt"`
}

const reasonColumns = `code, name, applies_to, requires_comment, negative_posting_allowed, alternative_posting_date_allowed, active, updated_at`

func scanReason(row pgx.Row, extra ...any) (ReasonCode, error) {
	var r ReasonCode
	err := row.Scan(append([]any{&r.Code, &r.Name, &r.AppliesTo, &r.RequiresComment, &r.NegativePostingAllowed,
		&r.AlternativePostingDateAllowed, &r.Active, &r.UpdatedAt}, extra...)...)
	if r.AppliesTo == nil {
		r.AppliesTo = []string{}
	}
	return r, err
}

// ListReasonCodes returns every reason code of the tenant ordered by code.
func (s *Postgres) ListReasonCodes(ctx context.Context) ([]ReasonCode, error) {
	p, err := principalOf(ctx)
	if err != nil {
		return nil, err
	}
	out := []ReasonCode{}
	err = dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `select `+reasonColumns+` from reason_code where tenant_id = $1 order by code`, p.TenantID)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			r, err := scanReason(rows)
			if err != nil {
				return err
			}
			out = append(out, r)
		}
		return rows.Err()
	})
	return out, err
}

// GetReasonCode returns one reason code or 404 LEDGER_REASON_NOT_FOUND.
func (s *Postgres) GetReasonCode(ctx context.Context, code string) (ReasonCode, error) {
	p, err := principalOf(ctx)
	if err != nil {
		return ReasonCode{}, err
	}
	var out ReasonCode
	err = dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		var err error
		out, err = loadReasonRow(ctx, tx, p.TenantID, code)
		return err
	})
	return out, err
}

// PutReasonCode creates or replaces a reason code. created reports whether the
// code is new, so the API can answer 201 or 200.
func (s *Postgres) PutReasonCode(ctx context.Context, code string, in ReasonCode) (ReasonCode, bool, error) {
	p, err := principalOf(ctx)
	if err != nil {
		return ReasonCode{}, false, err
	}
	if in.AppliesTo == nil {
		in.AppliesTo = []string{}
	}
	var out ReasonCode
	var created bool
	err = dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		row := tx.QueryRow(ctx, `insert into reason_code (tenant_id, code, name, applies_to, requires_comment, negative_posting_allowed,
			alternative_posting_date_allowed, active)
			values ($1, $2, $3, $4, $5, $6, $7, $8)
			on conflict (tenant_id, code) do update set name = excluded.name, applies_to = excluded.applies_to,
				requires_comment = excluded.requires_comment, negative_posting_allowed = excluded.negative_posting_allowed,
				alternative_posting_date_allowed = excluded.alternative_posting_date_allowed, active = excluded.active,
				updated_at = now()
			returning `+reasonColumns+`, (xmax = 0)`, p.TenantID, code, in.Name, in.AppliesTo, in.RequiresComment,
			in.NegativePostingAllowed, in.AlternativePostingDateAllowed, in.Active)
		var scanErr error
		out, scanErr = scanReason(row, &created)
		return scanErr
	})
	if err != nil {
		return ReasonCode{}, false, err
	}
	return out, created, nil
}

// loadReasonRow returns the reason code or 404 LEDGER_REASON_NOT_FOUND.
func loadReasonRow(ctx context.Context, tx pgx.Tx, tenantID, code string) (ReasonCode, error) {
	r, err := scanReason(tx.QueryRow(ctx, `select `+reasonColumns+` from reason_code where tenant_id = $1 and code = $2`, tenantID, code))
	if errors.Is(err, pgx.ErrNoRows) {
		return ReasonCode{}, httpx.NewProblem(http.StatusNotFound, "LEDGER_REASON_NOT_FOUND", "reason code not found")
	}
	return r, err
}

// loadReason returns the domain view of a reason code, or nil when code is empty
// or no such code exists.
func loadReason(ctx context.Context, tx pgx.Tx, tenantID, code string) (*domain.ReasonInfo, error) {
	if code == "" {
		return nil, nil
	}
	r, err := scanReason(tx.QueryRow(ctx, `select `+reasonColumns+` from reason_code where tenant_id = $1 and code = $2`, tenantID, code))
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &domain.ReasonInfo{Code: r.Code, AppliesTo: r.AppliesTo, RequiresComment: r.RequiresComment,
		NegativePostingAllowed: r.NegativePostingAllowed, AlternativePostingDateAllowed: r.AlternativePostingDateAllowed,
		Active: r.Active}, nil
}
