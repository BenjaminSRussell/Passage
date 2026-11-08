package store

import (
	"context"
	"sort"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/BenjaminSRussell/passage/platform/dbx"
)

// MaxBlockReportResources bounds how many resources one block utilization
// report reads; more sets Truncated.
const MaxBlockReportResources = 200

// BlockUsage is the period total of one capacity block on one resource: the
// minutes its windows hold in the range, how many of them jobs matching its
// condition use, and the share used (PRD 4.23.14 Block utilization).
type BlockUsage struct {
	ResourceID   string `json:"resourceId"`
	ResourceCode string `json:"resourceCode"`
	ResourceName string `json:"resourceName"`
	BlockID      string `json:"blockId"`
	Code         string `json:"code"`
	Priority     int    `json:"priority"`
	Windows      int    `json:"windows"`
	// BlockMinutes, MatchedMinutes and UnusedMinutes add up the windows of the
	// block in the range as BlockUtilization counts them.
	BlockMinutes   int `json:"blockMinutes"`
	MatchedMinutes int `json:"matchedMinutes"`
	UnusedMinutes  int `json:"unusedMinutes"`
	// UtilizationPct is MatchedMinutes over BlockMinutes in whole percent,
	// rounded half up; nil when the block holds no minute in the range.
	UtilizationPct *int `json:"utilizationPct"`
}

// BlockReport is the block utilization report across resources. The totals add
// up the listed rows.
type BlockReport struct {
	Items          []BlockUsage `json:"items"`
	Truncated      bool         `json:"truncated"`
	BlockMinutes   int          `json:"blockMinutes"`
	MatchedMinutes int          `json:"matchedMinutes"`
	UnusedMinutes  int          `json:"unusedMinutes"`
	UtilizationPct *int         `json:"utilizationPct"`
}

// pct returns matched over held in whole percent, rounded half up, or nil
// when nothing is held.
func pct(matched, held int) *int {
	if held <= 0 {
		return nil
	}
	v := (matched*200 + held) / (2 * held)
	return &v
}

// BlockReport totals the active capacity blocks of every resource that has
// one (or of one resource) over [from, to): one row per block, sorted by
// resource code, then priority (highest first), then block code. A resource
// that does not exist is SCHED-0102. At most MaxBlockReportResources resources
// are read, in code order; Truncated is set when more have blocks.
func (s *Postgres) BlockReport(ctx context.Context, resourceID string, from, to time.Time) (BlockReport, error) {
	out := BlockReport{Items: []BlockUsage{}}
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `select r.id, r.code, r.name from scheduling_resource r
			where ($1::uuid is null or r.id = $1)
			and ($1::uuid is not null or exists (select 1 from scheduling_capacity_block b where b.tenant_id = r.tenant_id and b.resource_id = r.id and b.active))
			order by r.code, r.id limit $2`, nullable(resourceID), MaxBlockReportResources+1)
		if err != nil {
			return err
		}
		type res struct{ id, code, name string }
		var rs []res
		for rows.Next() {
			var r res
			if err := rows.Scan(&r.id, &r.code, &r.name); err != nil {
				rows.Close()
				return err
			}
			rs = append(rs, r)
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			return err
		}
		if len(rs) > MaxBlockReportResources {
			rs, out.Truncated = rs[:MaxBlockReportResources], true
		}
		if resourceID != "" && len(rs) == 0 {
			// Reuse the single-resource lookup for its not-found problem.
			_, err := blockUtilizationTx(ctx, tx, resourceID, from, to)
			return err
		}
		for _, r := range rs {
			windows, err := blockUtilizationTx(ctx, tx, r.id, from, to)
			if err != nil {
				return err
			}
			byBlock := map[string]*BlockUsage{}
			var order []*BlockUsage
			for _, w := range windows {
				u := byBlock[w.BlockID]
				if u == nil {
					u = &BlockUsage{ResourceID: r.id, ResourceCode: r.code, ResourceName: r.name, BlockID: w.BlockID, Code: w.Code, Priority: w.Priority}
					byBlock[w.BlockID] = u
					order = append(order, u)
				}
				u.Windows++
				u.BlockMinutes += w.BlockMinutes
				u.MatchedMinutes += w.MatchedMinutes
				u.UnusedMinutes += w.UnusedMinutes
			}
			sort.SliceStable(order, func(i, j int) bool {
				if order[i].Priority != order[j].Priority {
					return order[i].Priority > order[j].Priority
				}
				return order[i].Code < order[j].Code
			})
			for _, u := range order {
				u.UtilizationPct = pct(u.MatchedMinutes, u.BlockMinutes)
				out.BlockMinutes += u.BlockMinutes
				out.MatchedMinutes += u.MatchedMinutes
				out.UnusedMinutes += u.UnusedMinutes
				out.Items = append(out.Items, *u)
			}
		}
		out.UtilizationPct = pct(out.MatchedMinutes, out.BlockMinutes)
		return nil
	})
	return out, err
}
