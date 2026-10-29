package store

import (
	"context"

	"github.com/jackc/pgx/v5"

	"github.com/BenjaminSRussell/passage/platform/dbx"
	"github.com/BenjaminSRussell/passage/platform/tenant"
	"github.com/BenjaminSRussell/passage/services/analytics/internal/domain"
)

// Lineage is an asset's upstream and downstream assets within a depth.
// Distance is the fewest hops from the asset, so the nearest assets come first.
type Lineage struct {
	Asset      string         `json:"asset"`
	Depth      int            `json:"depth"`
	Upstream   []LineageAsset `json:"upstream"`
	Downstream []LineageAsset `json:"downstream"`
}

// LineageAsset is one asset reached by a lineage walk.
type LineageAsset struct {
	FQN      string `json:"fqn"`
	Distance int    `json:"distance"`
}

// walkSQL walks lineage_edge breadth first from $1 for at most $2 hops. from
// and to name the edge columns for the direction: upstream follows target to
// source. UNION keeps (asset, hop) pairs distinct, so a cycle ends at the depth.
func walkSQL(from, to string) string {
	return `with recursive walk(fqn, hop) as (
		select ` + from + `, 1 from lineage_edge where ` + ownTenant + ` and ` + to + ` = $1
		union
		select e.` + from + `, w.hop + 1 from lineage_edge e join walk w on e.` + to + ` = w.fqn
		where e.` + ownTenant + ` and w.hop < $2)
	select fqn, min(hop) from walk where fqn <> $1 group by fqn order by min(hop), fqn`
}

// GetLineage returns the assets upstream (feeding) and downstream (fed by)
// asset within depth hops (GET /catalog/lineage/{asset_fqn}). An asset with no
// edges has empty lists.
func (s *Postgres) GetLineage(ctx context.Context, asset string, depth int) (Lineage, error) {
	out := Lineage{Asset: asset, Depth: depth, Upstream: []LineageAsset{}, Downstream: []LineageAsset{}}
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		for _, dir := range []struct {
			sql string
			dst *[]LineageAsset
		}{{walkSQL("source_fqn", "target_fqn"), &out.Upstream}, {walkSQL("target_fqn", "source_fqn"), &out.Downstream}} {
			rows, err := tx.Query(ctx, dir.sql, asset, depth)
			if err != nil {
				return err
			}
			for rows.Next() {
				var a LineageAsset
				if err := rows.Scan(&a.FQN, &a.Distance); err != nil {
					rows.Close()
					return err
				}
				*dir.dst = append(*dir.dst, a)
			}
			rows.Close()
			if err := rows.Err(); err != nil {
				return err
			}
		}
		return nil
	})
	return out, err
}

// AddLineageEdges records a validated batch of edges. Existing edges are kept,
// so re-sending a batch is harmless: the call is idempotent by edge. It
// returns how many edges were new.
func (s *Postgres) AddLineageEdges(ctx context.Context, edges []domain.Edge) (int64, error) {
	if err := domain.ValidateEdges(edges); err != nil {
		return 0, err
	}
	p, _ := tenant.FromContext(ctx)
	var added int64
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		added = 0
		for _, e := range edges {
			tag, err := tx.Exec(ctx, `insert into lineage_edge (tenant_id, source_fqn, target_fqn)
				values ($1, $2, $3) on conflict (tenant_id, source_fqn, target_fqn) do nothing`,
				p.TenantID, e.Source, e.Target)
			if err != nil {
				return err
			}
			added += tag.RowsAffected()
		}
		return nil
	})
	return added, err
}
