package store

import (
	"context"
	"net/http"
	"strings"

	"github.com/jackc/pgx/v5"

	"github.com/BenjaminSRussell/passage/platform/httpx"
)

// CodePlacementVersion is SCHED-0154: the order's placement changed since the
// planner loaded the Gantt (another move, a run, a firm or an unschedule), so
// a move or firm built on the old picture is refused instead of overwriting it.
const CodePlacementVersion = "SCHED-0154"

// placementVersionSQL hashes every job segment of the orders (resource, span,
// firmed flag) into one token per order. It changes whenever any writer
// places, moves, firms or removes a job of the order, and needs no stored
// counter. Epoch seconds keep it independent of the session time zone.
const placementVersionSQL = `select order_id::text,
		md5(string_agg(job_id::text || '|' || segment_no::text || '|' || resource_id::text || '|' ||
			extract(epoch from start_at)::text || '|' || extract(epoch from end_at)::text || '|' || firmed::text,
			';' order by job_id, segment_no))
	from scheduled_job where order_id = any($1::uuid[]) group by order_id`

// placementVersions returns the current placement version of each order that
// has jobs.
func placementVersions(ctx context.Context, tx pgx.Tx, orderIDs []string) (map[string]string, error) {
	rows, err := tx.Query(ctx, placementVersionSQL, orderIDs)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]string{}
	for rows.Next() {
		var order, v string
		if err := rows.Scan(&order, &v); err != nil {
			return nil, err
		}
		out[order] = v
	}
	return out, rows.Err()
}

// checkPlacementVersion refuses a command whose If-Match (quotes and a weak
// prefix are ignored) is not the order's current placement version. An empty
// ifMatch skips the check.
func checkPlacementVersion(ctx context.Context, tx pgx.Tx, orderID, ifMatch string) error {
	want := strings.Trim(strings.TrimPrefix(strings.TrimSpace(ifMatch), "W/"), `"`)
	if want == "" {
		return nil
	}
	cur, err := placementVersions(ctx, tx, []string{orderID})
	if err != nil {
		return err
	}
	if cur[orderID] != want {
		return httpx.NewProblem(http.StatusPreconditionFailed, CodePlacementVersion,
			"the order's placement changed since you loaded it; reload the Gantt and try again")
	}
	return nil
}

// fillPlacementVersions sets PlacementVersion on every job bar.
func fillPlacementVersions(ctx context.Context, tx pgx.Tx, bars []Bar) error {
	seen := map[string]bool{}
	orders := []string{}
	for _, b := range bars {
		if !b.Cleanout && !seen[b.OrderID] {
			seen[b.OrderID] = true
			orders = append(orders, b.OrderID)
		}
	}
	if len(orders) == 0 {
		return nil
	}
	versions, err := placementVersions(ctx, tx, orders)
	if err != nil {
		return err
	}
	for i := range bars {
		if !bars[i].Cleanout {
			bars[i].PlacementVersion = versions[bars[i].OrderID]
		}
	}
	return nil
}
