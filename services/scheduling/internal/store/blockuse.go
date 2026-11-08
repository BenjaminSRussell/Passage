package store

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/BenjaminSRussell/passage/platform/dbx"
	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/services/scheduling/internal/domain"
)

// BlockWindow is one occurrence of an active capacity block window on a
// resource within an inquiry range, with the minutes it holds and how many of
// them jobs matching its condition use (FR-23.17, PRD 4.23.14 Block
// utilization). See domain.BlockWindow for how the minutes are counted.
type BlockWindow struct {
	BlockID        string             `json:"blockId"`
	Code           string             `json:"code"`
	Priority       int                `json:"priority"`
	Condition      []domain.Predicate `json:"condition"`
	Start          time.Time          `json:"start"`
	End            time.Time          `json:"end"`
	BlockMinutes   int                `json:"blockMinutes"`
	MatchedMinutes int                `json:"matchedMinutes"`
	UnusedMinutes  int                `json:"unusedMinutes"`
}

// BlockUtilization lists the windows of the resource's active capacity blocks
// that overlap [from, to), expanded in the resource's time zone and clipped to
// the range, with the finite booked minutes of matching jobs in each. Inactive
// blocks reserve nothing and are left out. Bookings of infinite runs are not
// counted because they reserve no capacity.
func (s *Postgres) BlockUtilization(ctx context.Context, resourceID string, from, to time.Time) ([]BlockWindow, error) {
	var out []BlockWindow
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		var err error
		out, err = blockUtilizationTx(ctx, tx, resourceID, from, to)
		return err
	})
	return out, err
}

// blockUtilizationTx is BlockUtilization inside a tenant transaction, so the
// cross-resource report reads every resource in one snapshot.
func blockUtilizationTx(ctx context.Context, tx pgx.Tx, resourceID string, from, to time.Time) ([]BlockWindow, error) {
	r, err := scanResource(tx.QueryRow(ctx, `select `+resourceCols+` from scheduling_resource where id = $1`, resourceID), nil)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, httpx.NewProblem(http.StatusNotFound, CodeResourceNotFound, "resource not found")
	}
	if err != nil {
		return nil, err
	}
	loc, err := time.LoadLocation(r.TimeZone)
	if err != nil {
		return nil, err
	}
	// Lay calendars and windows out from the day before to the day after
	// so windows crossing the range edges in the site's time zone are whole.
	start := from.Add(-24 * time.Hour)
	days := int(to.Sub(from)/(24*time.Hour)) + 3
	ovm, err := loadOverrides(ctx, tx, resourceID)
	if err != nil {
		return nil, err
	}
	open := domain.OpenWithOverrides(loc, r.Shifts, ovm[resourceID], start, days)

	rows, err := tx.Query(ctx, `select `+blockCols+` from scheduling_capacity_block where active and resource_id = $1 order by priority desc, code`, resourceID)
	if err != nil {
		return nil, err
	}
	stored := map[string]CapacityBlock{}
	var blocks []domain.CapacityBlock
	for rows.Next() {
		b, err := scanBlock(rows)
		if err != nil {
			rows.Close()
			return nil, err
		}
		stored[b.ID] = b
		db, err := b.expand(loc, start, days)
		if err != nil {
			rows.Close()
			return nil, err
		}
		blocks = append(blocks, db)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}

	jobs, err := bookedWithAttributes(ctx, tx, resourceID, from, to)
	if err != nil {
		return nil, err
	}
	windows := domain.BlockUtilization(open, blocks, jobs, from, to)
	out := make([]BlockWindow, 0, len(windows))
	for _, w := range windows {
		b := stored[w.BlockID]
		out = append(out, BlockWindow{BlockID: b.ID, Code: b.Code, Priority: b.Priority, Condition: b.Condition,
			Start: w.Window.Start, End: w.Window.End, BlockMinutes: w.BlockMinutes, MatchedMinutes: w.MatchedMinutes, UnusedMinutes: w.UnusedMinutes})
	}
	return out, nil
}

// bookedWithAttributes reads the finite segments on a resource that overlap
// [from, to) with the attributes of their operations.
func bookedWithAttributes(ctx context.Context, tx pgx.Tx, resourceID string, from, to time.Time) ([]domain.BlockJob, error) {
	rows, err := tx.Query(ctx, `select j.start_at, j.end_at, o.attributes
		from scheduled_job j join scheduling_operation o on o.tenant_id = j.tenant_id and o.id = j.job_id
		where j.resource_id = $1 and j.finite and j.start_at < $3 and j.end_at > $2`, resourceID, from, to)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []domain.BlockJob
	for rows.Next() {
		var j domain.BlockJob
		var attrs []byte
		if err := rows.Scan(&j.Start, &j.End, &attrs); err != nil {
			return nil, err
		}
		if err := json.Unmarshal(attrs, &j.Attributes); err != nil {
			return nil, err
		}
		out = append(out, j)
	}
	return out, rows.Err()
}
