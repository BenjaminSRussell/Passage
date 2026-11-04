package store

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/tools/migration-factory/internal/domain"
	mfevents "github.com/BenjaminSRussell/passage/tools/migration-factory/internal/events"
)

// ToleranceChange sets the tolerance of one (pack, total). ExpectedVersion is 0 for a total that has
// no configured tolerance yet, and the stored version for one that has.
type ToleranceChange struct {
	Rule            domain.ToleranceRule
	ExpectedVersion int
	By              string
}

// ListTolerances returns the configured tolerances, ordered by pack and total.
func (s *Postgres) ListTolerances(ctx context.Context) ([]domain.ToleranceRule, error) {
	var out []domain.ToleranceRule
	err := s.tx(ctx, func(tx pgx.Tx) error {
		var err error
		out, err = readTolerances(ctx, tx)
		return err
	})
	return out, err
}

func readTolerances(ctx context.Context, tx pgx.Tx) ([]domain.ToleranceRule, error) {
	rows, err := tx.Query(ctx, `select pack, total_name, amount::text, reason, version, updated_by, updated_at from pack_tolerance
		where tenant_id = current_setting('passage.tenant_id', true)::uuid order by pack, total_name`)
	if err != nil {
		return nil, fmt.Errorf("read pack_tolerance: %w", err)
	}
	defer rows.Close()
	out := []domain.ToleranceRule{}
	for rows.Next() {
		var r domain.ToleranceRule
		var pack, amount string
		if err := rows.Scan(&pack, &r.Total, &amount, &r.Reason, &r.Version, &r.UpdatedBy, &r.UpdatedAt); err != nil {
			return nil, err
		}
		if r.Amount, err = decimal.NewFromString(amount); err != nil {
			return nil, err
		}
		r.Pack = domain.Pack(pack)
		out = append(out, r)
	}
	return out, rows.Err()
}

// loadTolerances reads the configured tolerances a run reconciles with.
func loadTolerances(ctx context.Context, tx pgx.Tx) (domain.ToleranceSet, error) {
	rules, err := readTolerances(ctx, tx)
	if err != nil {
		return nil, err
	}
	return domain.NewToleranceSet(rules), nil
}

// PutTolerance sets a tolerance and writes dev.passage.migration.tolerance.configured.v1 in the same
// transaction. A stale ExpectedVersion fails with ErrVersion.
func (s *Postgres) PutTolerance(ctx context.Context, idem Idem, c ToleranceChange) (domain.ToleranceRule, bool, error) {
	var out domain.ToleranceRule
	var replayed bool
	err := s.tx(ctx, func(tx pgx.Tx) error {
		var err error
		out, replayed, err = idempotent(ctx, tx, idem, 200, func() (domain.ToleranceRule, error) { return putTolerance(ctx, tx, c) })
		return err
	})
	return out, replayed, err
}

func putTolerance(ctx context.Context, tx pgx.Tx, c ToleranceChange) (domain.ToleranceRule, error) {
	r := c.Rule
	var prevAmount string
	var version int
	err := tx.QueryRow(ctx, `select amount::text, version from pack_tolerance
		where tenant_id = current_setting('passage.tenant_id', true)::uuid and pack = $1 and total_name = $2 for update`,
		string(r.Pack), r.Total).Scan(&prevAmount, &version)
	switch {
	case errors.Is(err, pgx.ErrNoRows):
		if c.ExpectedVersion != 0 {
			return domain.ToleranceRule{}, fmt.Errorf("%w: no tolerance is configured for %s, request expected version %d", ErrVersion, r.Total, c.ExpectedVersion)
		}
		prevAmount, r.Version = "0", 1
		// Two first configurations of one total can race past the read; the loser is a stale version.
		tag, err := tx.Exec(ctx, `insert into pack_tolerance (tenant_id, pack, total_name, amount, reason, version, updated_by)
			values (current_setting('passage.tenant_id', true)::uuid, $1, $2, $3, $4, 1, $5) on conflict do nothing`,
			string(r.Pack), r.Total, r.Amount.String(), r.Reason, c.By)
		if err != nil {
			return domain.ToleranceRule{}, fmt.Errorf("insert pack_tolerance: %w", err)
		}
		if tag.RowsAffected() == 0 {
			return domain.ToleranceRule{}, fmt.Errorf("%w: a tolerance for %s was configured meanwhile; read it and pass its version", ErrVersion, r.Total)
		}
	case err != nil:
		return domain.ToleranceRule{}, fmt.Errorf("read pack_tolerance: %w", err)
	default:
		if prev, perr := decimal.NewFromString(prevAmount); perr == nil {
			prevAmount = prev.String()
		}
		if c.ExpectedVersion != version {
			return domain.ToleranceRule{}, fmt.Errorf("%w: tolerance for %s is at version %d, request expected %d", ErrVersion, r.Total, version, c.ExpectedVersion)
		}
		r.Version = version + 1
		if _, err := tx.Exec(ctx, `update pack_tolerance set amount = $3, reason = $4, version = $5, updated_by = $6, updated_at = now()
			where tenant_id = current_setting('passage.tenant_id', true)::uuid and pack = $1 and total_name = $2`,
			string(r.Pack), r.Total, r.Amount.String(), r.Reason, r.Version, c.By); err != nil {
			return domain.ToleranceRule{}, fmt.Errorf("update pack_tolerance: %w", err)
		}
	}
	if err := tx.QueryRow(ctx, `select updated_at from pack_tolerance where tenant_id = current_setting('passage.tenant_id', true)::uuid and pack = $1 and total_name = $2`,
		string(r.Pack), r.Total).Scan(&r.UpdatedAt); err != nil {
		return domain.ToleranceRule{}, err
	}
	r.UpdatedBy = c.By
	if err := enqueue(ctx, tx, mfevents.ToleranceConfigured, "pack_tolerance", string(r.Pack)+"|"+r.Total, mfevents.ToleranceConfiguredData{
		Pack: string(r.Pack), Total: r.Total, Amount: r.Amount.String(), PreviousAmount: prevAmount, Reason: r.Reason, Version: r.Version, ConfiguredBy: c.By,
	}); err != nil {
		return domain.ToleranceRule{}, fmt.Errorf("enqueue tolerance.configured: %w", err)
	}
	return r, nil
}
