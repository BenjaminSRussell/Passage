package store

import (
	"context"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/BenjaminSRussell/passage/services/scheduling/internal/domain"
)

// loadConstraints gives each resource what the scheduler needs for the
// slice 3 constraints over the window [from, from+days): the setup matrix in
// force on the day of on (FR-23.15), the setup group it is in at from, and its
// active capacity blocks with their windows expanded in the resource's time
// zone (FR-23.17). It returns the resource networks active on the day of on
// (FR-23.13). on is the run start, or the start a move asks for.
func loadConstraints(ctx context.Context, tx pgx.Tx, resources []domain.Resource, from time.Time, days int, on time.Time) ([]domain.ResourceNetwork, error) {
	day := on.UTC().Format(time.DateOnly)
	matrices, err := activeMatrices(ctx, tx, day)
	if err != nil {
		return nil, err
	}
	groups, err := currentGroups(ctx, tx, from)
	if err != nil {
		return nil, err
	}
	blocks, err := activeBlocks(ctx, tx)
	if err != nil {
		return nil, err
	}
	for i := range resources {
		r := &resources[i]
		r.Matrix = matrices[r.ID]
		r.CurrentGroup = groups[r.ID]
		r.Blocks = nil
		for _, b := range blocks[r.ID] {
			db := domain.CapacityBlock{ID: b.ID, ResourceID: b.ResourceID, Priority: b.Priority, Condition: b.Condition}
			if b.StartAt != nil && b.EndAt != nil {
				db.Windows = []domain.Interval{{Start: b.StartAt.UTC(), End: b.EndAt.UTC()}}
			} else {
				// Expand from the day before so a window that started before
				// from in the site's time zone still covers its tail.
				db.Windows = domain.OpenIntervals(r.Location, b.Windows, from.Add(-24*time.Hour), days+2)
			}
			r.Blocks = append(r.Blocks, db)
		}
	}
	return activeNetworks(ctx, tx, day)
}

// activeMatrices returns, per resource, the setup matrix in force on day: the
// highest version whose active_from is on or before it.
func activeMatrices(ctx context.Context, tx pgx.Tx, day string) (map[string]domain.SetupMatrix, error) {
	rows, err := tx.Query(ctx, `select m.resource_id::text, e.from_setup_group, e.to_setup_group, e.changeover_minutes, e.changeover_cost::text,
			coalesce(e.cleaning_operation_set_id, '')
		from (select distinct on (resource_id) id, resource_id from scheduling_setup_matrix
			where active_from <= $1::date order by resource_id, version desc) m
		join scheduling_setup_matrix_entry e on e.matrix_id = m.id`, day)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	entries := map[string][]domain.SetupEntry{}
	for rows.Next() {
		var rid, cost string
		var e domain.SetupEntry
		if err := rows.Scan(&rid, &e.FromGroup, &e.ToGroup, &e.Minutes, &cost, &e.CleaningSetID); err != nil {
			return nil, err
		}
		e.Cost = dec(cost)
		entries[rid] = append(entries[rid], e)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	out := make(map[string]domain.SetupMatrix, len(entries))
	for rid, es := range entries {
		m, err := domain.NewSetupMatrix(es)
		if err != nil {
			return nil, fmt.Errorf("setup matrix of resource %s: %w", rid, err)
		}
		out[rid] = m
	}
	return out, nil
}

// currentGroups returns the setup group each finite resource is in at t: the
// group of its last job with a setup group that ends at or before t.
func currentGroups(ctx context.Context, tx pgx.Tx, t time.Time) (map[string]string, error) {
	rows, err := tx.Query(ctx, `select distinct on (j.resource_id) j.resource_id::text, o.setup_group
		from scheduled_job j join scheduling_operation o on o.tenant_id = j.tenant_id and o.id = j.job_id
		where j.finite and j.end_at <= $1 and o.setup_group is not null
		order by j.resource_id, j.end_at desc`, t)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]string{}
	for rows.Next() {
		var rid, group string
		if err := rows.Scan(&rid, &group); err != nil {
			return nil, err
		}
		out[rid] = group
	}
	return out, rows.Err()
}

// activeBlocks returns the active capacity blocks by resource.
func activeBlocks(ctx context.Context, tx pgx.Tx) (map[string][]CapacityBlock, error) {
	rows, err := tx.Query(ctx, `select `+blockCols+` from scheduling_capacity_block where active order by resource_id, priority desc, code`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string][]CapacityBlock{}
	for rows.Next() {
		b, err := scanBlock(rows)
		if err != nil {
			return nil, err
		}
		out[b.ResourceID] = append(out[b.ResourceID], b)
	}
	return out, rows.Err()
}

// activeNetworks returns the networks whose active dates include day, keyed
// for the scheduler by code.
func activeNetworks(ctx context.Context, tx pgx.Tx, day string) ([]domain.ResourceNetwork, error) {
	nets, err := loadNetworks(ctx, tx, "")
	if err != nil {
		return nil, err
	}
	out := make([]domain.ResourceNetwork, 0, len(nets))
	for _, n := range nets {
		if n.ActiveFrom > day || (n.ActiveTo != nil && *n.ActiveTo < day) {
			continue
		}
		dn, err := domain.NewResourceNetwork(n.Code, n.Edges)
		if err != nil {
			// A network stored before a rule it breaks was added (a maximum
			// wait shorter than the transfer) is left out; operations naming
			// it are unplaced as naming no active network.
			continue
		}
		out = append(out, dn)
	}
	return out, nil
}
