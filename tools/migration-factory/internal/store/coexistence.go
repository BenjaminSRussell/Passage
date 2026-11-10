package store

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/BenjaminSRussell/passage/tools/migration-factory/internal/domain"
	mfevents "github.com/BenjaminSRussell/passage/tools/migration-factory/internal/events"
)

// CoexistChange sets one entity kind's coexistence mode as the signed-in user By, against the
// flag's current version (0 for a kind never set).
type CoexistChange struct {
	System          domain.SourceSystem
	Company         string
	Entity          string
	Mode            domain.CoexistMode
	ExpectedVersion int
	By              string
	Now             time.Time
}

func scanFlag(row pgx.Row) (domain.CoexistFlag, error) {
	var f domain.CoexistFlag
	var sys, mode string
	err := row.Scan(&sys, &f.Company, &f.Entity, &mode, &f.CutoverAt, &f.Version, &f.ChangedBy, &f.ChangedAt)
	f.SourceSystem, f.Mode = domain.SourceSystem(sys), domain.CoexistMode(mode)
	return f, err
}

const flagColumns = `source_system, source_company, entity, mode, cutover_at, version, changed_by, changed_at`

// ListCoexistence returns every entity kind's flag for a legacy company; a kind never set is LEGACY
// at version 0.
func (s *Postgres) ListCoexistence(ctx context.Context, system domain.SourceSystem, company string) ([]domain.CoexistFlag, error) {
	stored := map[string]domain.CoexistFlag{}
	err := s.tx(ctx, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `select `+flagColumns+` from coexistence_entity
			where tenant_id = current_setting('passage.tenant_id', true)::uuid and source_system = $1 and source_company = $2`, string(system), company)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			f, err := scanFlag(rows)
			if err != nil {
				return err
			}
			stored[f.Entity] = f
		}
		return rows.Err()
	})
	if err != nil {
		return nil, err
	}
	out := make([]domain.CoexistFlag, 0, len(domain.CoexistEntities))
	for _, e := range domain.CoexistEntities {
		f, ok := stored[e]
		if !ok {
			f = domain.CoexistFlag{SourceSystem: system, Company: company, Entity: e, Mode: domain.CoexistLegacy}
		}
		out = append(out, f)
	}
	return out, nil
}

// GetCoexistence returns one entity kind's flag (LEGACY at version 0 when never set).
func (s *Postgres) GetCoexistence(ctx context.Context, system domain.SourceSystem, company, entity string) (domain.CoexistFlag, error) {
	var out domain.CoexistFlag
	err := s.tx(ctx, func(tx pgx.Tx) error {
		var err error
		out, err = lockFlag(ctx, tx, system, company, entity, false)
		return err
	})
	return out, err
}

func lockFlag(ctx context.Context, tx pgx.Tx, system domain.SourceSystem, company, entity string, forUpdate bool) (domain.CoexistFlag, error) {
	q := `select ` + flagColumns + ` from coexistence_entity
		where tenant_id = current_setting('passage.tenant_id', true)::uuid and source_system = $1 and source_company = $2 and entity = $3`
	if forUpdate {
		q += ` for update`
	}
	f, err := scanFlag(tx.QueryRow(ctx, q, string(system), company, entity))
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.CoexistFlag{SourceSystem: system, Company: company, Entity: entity, Mode: domain.CoexistLegacy}, nil
	}
	return f, err
}

// SetCoexistence changes an entity kind's mode under its row lock, against the expected version.
func (s *Postgres) SetCoexistence(ctx context.Context, idem Idem, c CoexistChange) (domain.CoexistFlag, bool, error) {
	var out domain.CoexistFlag
	var replayed bool
	err := s.tx(ctx, func(tx pgx.Tx) error {
		var err error
		out, replayed, err = idempotent(ctx, tx, idem, http.StatusOK, func() (domain.CoexistFlag, error) {
			// Serialize the first write of a kind too: there is no row to lock yet.
			if _, err := tx.Exec(ctx, `select pg_advisory_xact_lock(hashtextextended('coexist|' || current_setting('passage.tenant_id', true) || '|' || $1 || '|' || $2 || '|' || $3, 0))`,
				string(c.System), c.Company, c.Entity); err != nil {
				return domain.CoexistFlag{}, err
			}
			f, err := lockFlag(ctx, tx, c.System, c.Company, c.Entity, true)
			if err != nil {
				return domain.CoexistFlag{}, err
			}
			if f.Version != c.ExpectedVersion {
				return domain.CoexistFlag{}, fmt.Errorf("%w: flag is at version %d, request expected %d", ErrVersion, f.Version, c.ExpectedVersion)
			}
			if err := f.SetMode(c.Mode, c.By, c.Now); err != nil {
				return domain.CoexistFlag{}, err
			}
			if err := saveFlag(ctx, tx, f, ""); err != nil {
				return domain.CoexistFlag{}, err
			}
			return f, nil
		})
		return err
	})
	return out, replayed, err
}

// saveFlag writes a changed flag and dev.passage.migration.coexistence.changed.v1 in the caller's
// transaction; planID names the rollback that flipped it back, if any.
func saveFlag(ctx context.Context, tx pgx.Tx, f domain.CoexistFlag, planID string) error {
	if _, err := tx.Exec(ctx, `insert into coexistence_entity (tenant_id, source_system, source_company, entity, mode, cutover_at, version, changed_by, changed_at)
		values (current_setting('passage.tenant_id', true)::uuid, $1, $2, $3, $4, $5, $6, $7, $8)
		on conflict (tenant_id, source_system, source_company, entity) do update
		set mode = excluded.mode, cutover_at = excluded.cutover_at, version = excluded.version, changed_by = excluded.changed_by, changed_at = excluded.changed_at`,
		string(f.SourceSystem), f.Company, f.Entity, string(f.Mode), f.CutoverAt, f.Version, f.ChangedBy, f.ChangedAt); err != nil {
		return fmt.Errorf("upsert coexistence_entity: %w", err)
	}
	if err := enqueue(ctx, tx, mfevents.CoexistenceChanged, "coexistence_entity", string(f.SourceSystem)+"/"+f.Company+"/"+f.Entity, mfevents.CoexistenceChangedData{
		SourceSystem: string(f.SourceSystem), SourceCompany: f.Company, Entity: f.Entity, Mode: string(f.Mode), Version: f.Version,
		CutoverAt: f.CutoverAt, ChangedBy: f.ChangedBy, PlanID: planID,
	}); err != nil {
		return fmt.Errorf("enqueue coexistence.changed: %w", err)
	}
	return nil
}

// resumeLegacy flips every entity kind of the plan's legacy companies that Passage masters back to
// LEGACY, as the rollback's executing approver, under the flags' row locks (FR-49.7, AC-49.7: the
// rollback restores legacy coexistence). A plan names its legacy companies, not their system, so a
// company is handed back in every system it is cut over in. It returns the flags it changed.
func resumeLegacy(ctx context.Context, tx pgx.Tx, plan domain.Plan, by string, now time.Time) ([]domain.CoexistFlag, error) {
	rows, err := tx.Query(ctx, `select `+flagColumns+` from coexistence_entity
		where tenant_id = current_setting('passage.tenant_id', true)::uuid and source_company = any($1) and mode = 'PASSAGE'
		order by source_system, source_company, entity for update`, plan.Entities)
	if err != nil {
		return nil, fmt.Errorf("read coexistence_entity: %w", err)
	}
	flags, err := pgx.CollectRows(rows, func(r pgx.CollectableRow) (domain.CoexistFlag, error) { return scanFlag(r) })
	if err != nil {
		return nil, fmt.Errorf("read coexistence_entity: %w", err)
	}
	for i := range flags {
		if err := flags[i].SetMode(domain.CoexistLegacy, by, now); err != nil {
			return nil, err
		}
		if err := saveFlag(ctx, tx, flags[i], plan.PlanID); err != nil {
			return nil, err
		}
	}
	return flags, nil
}

// KnownXrefs returns the Passage ids legacy_xref holds for legacy ids of one source table, keyed
// "table|legacy id" as domain.RouteInbound reads them.
func (s *Postgres) KnownXrefs(ctx context.Context, system domain.SourceSystem, company, table string, legacyIDs []string) (map[string]string, error) {
	out := map[string]string{}
	err := s.tx(ctx, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `select source_id, passage_id::text from legacy_xref
			where tenant_id = current_setting('passage.tenant_id', true)::uuid and source_system = $1 and source_company = $2 and source_table = $3 and source_id = any($4)`,
			string(system), company, table, legacyIDs)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var id, passage string
			if err := rows.Scan(&id, &passage); err != nil {
				return err
			}
			out[table+"|"+id] = passage
		}
		return rows.Err()
	})
	return out, err
}
