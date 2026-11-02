package store

import (
	"context"
	"net/http"

	"github.com/jackc/pgx/v5"

	"github.com/BenjaminSRussell/passage/platform/dbx"
	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/platform/tenant"
)

// ProjectionSource is what the current version of a plan holds for a set of
// items: the version and its as-of date, and per item the net requirement days
// from the as-of date to the window the caller asked for.
type ProjectionSource struct {
	Version int
	AsOf    string
	SiteID  string
	Rows    map[string][]NetRequirement
}

// ReadProjectionSource reads the net requirements of the given items in the
// current version of a plan for the days up to windowDays after its as-of date
// (FR-22.21, FR-22.22), and always the item's first stored day, which carries
// its opening on-hand even when its first movement is past the window. A plan
// without a current version (or one outside the caller's legal entities, by
// row-level security) is 404 PLAN-VERSION-NOT-FOUND; an item the plan did not
// plan has no entry.
func (s *Postgres) ReadProjectionSource(ctx context.Context, planCode string, itemIDs []string, windowDays int) (ProjectionSource, error) {
	p, _ := tenant.FromContext(ctx)
	out := ProjectionSource{Rows: map[string][]NetRequirement{}}
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		var versionID string
		err := tx.QueryRow(ctx, `select v.id::text, v.number, to_char(v.as_of, 'YYYY-MM-DD'), v.site_id::text
			from plan_version v join plan p on p.tenant_id = v.tenant_id and p.id = v.plan_id
			where v.tenant_id = $1 and p.code = $2 and v.status = 'current'`, p.TenantID, planCode).Scan(&versionID, &out.Version, &out.AsOf, &out.SiteID)
		if err == pgx.ErrNoRows {
			return httpx.NewProblem(http.StatusNotFound, CodeVersionNotFound, "plan "+planCode+" has no current version")
		}
		if err != nil {
			return err
		}
		rows, err := tx.Query(ctx, `select n.item_id::text, to_char(n.day, 'YYYY-MM-DD'), n.gross_requirements::text,
			n.scheduled_receipts::text, n.planned_receipts::text, n.pab::text, n.net_requirement::text
			from net_requirement n join plan_version v on v.tenant_id = n.tenant_id and v.id = n.plan_version_id
			where n.tenant_id = $1 and n.plan_version_id = $2::uuid and n.item_id = any($3::uuid[])
			  and (n.day <= v.as_of + $4::int or n.day = (select min(m.day) from net_requirement m
			    where m.tenant_id = n.tenant_id and m.plan_version_id = n.plan_version_id and m.item_id = n.item_id))
			order by n.item_id, n.day`, p.TenantID, versionID, itemIDs, windowDays)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var n NetRequirement
			if err := rows.Scan(&n.ItemID, &n.Date, &n.GrossRequirements, &n.ScheduledReceipts, &n.PlannedReceipts, &n.PAB, &n.NetRequirement); err != nil {
				return err
			}
			out.Rows[n.ItemID] = append(out.Rows[n.ItemID], n)
		}
		return rows.Err()
	})
	return out, err
}
