package store

import (
	"context"
	"errors"
	"hash/fnv"
	"math"
	"net/http"
	"sort"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/BenjaminSRussell/passage/platform/dbx"
	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/services/scheduling/internal/domain"
	schedevents "github.com/BenjaminSRussell/passage/services/scheduling/internal/events"
)

// CodeBottleneckNotFound is returned for an unknown bottleneck episode.
const CodeBottleneckNotFound = "SCHED-0147"

// maxLoadWeeks bounds the weeks one resource's materialized load can span.
// When its bookings span more, the latest weeks are kept: the oldest history
// drops out, never the weeks ahead that bottlenecks are about.
const maxLoadWeeks = 260

// refreshChunk is how many resources one transaction of a full rebuild
// rewrites, so a rebuild holds the load locks of a few resources at a time
// instead of every resource of the tenant at once.
const refreshChunk = 50

// Defaults of the bottleneck rule (PRD 4.23.10).
const (
	defaultBottleneckPct   = 90
	defaultBottleneckWeeks = 3
)

// WithBottleneck sets the rule the materialized load and bottleneck episodes
// use: a week is flagged above pct percent load, an episode needs weeks in a
// row (SCHED_BOTTLENECK_PCT, SCHED_BOTTLENECK_WEEKS). It returns s.
func (s *Postgres) WithBottleneck(pct, weeks int) *Postgres {
	s.bnPct, s.bnWeeks = pct, weeks
	return s
}

// WithClock replaces the clock that decides which weeks are still ahead.
func (s *Postgres) WithClock(now func() time.Time) *Postgres {
	s.clock = now
	return s
}

func (s *Postgres) rule() (pct, weeks int) {
	pct, weeks = s.bnPct, s.bnWeeks
	if pct == 0 {
		pct = defaultBottleneckPct
	}
	if weeks == 0 {
		weeks = defaultBottleneckWeeks
	}
	return pct, weeks
}

// currentWeek is the Monday (UTC) of the current week as a date string, the
// first week a bottleneck can still be acted on.
func (s *Postgres) currentWeek() string {
	return domain.WeekStart(s.now()).Format(time.DateOnly)
}

func (s *Postgres) now() time.Time {
	if s.clock != nil {
		return s.clock()
	}
	return time.Now()
}

// loadTracker collects, for one transaction, the resources whose bookings or
// calendar the transaction changed.
type loadTracker struct{ resources map[string]struct{} }

type loadTrackerKey struct{}

func trackerOf(ctx context.Context) *loadTracker {
	t, _ := ctx.Value(loadTrackerKey{}).(*loadTracker)
	return t
}

// trackResources marks resources whose materialized load must be rewritten
// before the transaction commits.
func trackResources(ctx context.Context, rids ...string) {
	if t := trackerOf(ctx); t != nil {
		for _, id := range rids {
			t.resources[id] = struct{}{}
		}
	}
}

// trackOps marks the resources that currently hold a job of the operations
// (as primary or secondary). Call it before deleting the jobs.
func trackOps(ctx context.Context, tx pgx.Tx, opIDs []string) error {
	if trackerOf(ctx) == nil || len(opIDs) == 0 {
		return nil
	}
	rows, err := tx.Query(ctx, `select resource_id::text from scheduled_job where job_id = any($1::uuid[])
		union select resource_id::text from scheduled_job_secondary where job_id = any($1::uuid[])`, opIDs)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return err
		}
		trackResources(ctx, id)
	}
	return rows.Err()
}

// trackPlacements marks the primary and secondary resources of placements.
func trackPlacements(ctx context.Context, ps []domain.Placement) {
	for _, pl := range ps {
		trackResources(ctx, pl.ResourceID)
		trackResources(ctx, pl.Secondaries...)
	}
}

// loadTx runs fn in a tenant transaction like dbx.WithTenantTx and, before the
// commit, rewrites the materialized capacity load of every resource fn tracked
// (trackOps, trackPlacements, trackResources). fn must use *ctx, which carries
// the tracker. Writers that place, move or remove jobs, or change a calendar,
// go through it, so the table commits with the bookings it summarizes.
func (s *Postgres) loadTx(ctx *context.Context, fn func(tx pgx.Tx) error) error {
	t := &loadTracker{resources: map[string]struct{}{}}
	*ctx = context.WithValue(*ctx, loadTrackerKey{}, t)
	c := *ctx
	return dbx.WithTenantTx(c, s.pool, func(tx pgx.Tx) error {
		clear(t.resources)
		if err := fn(tx); err != nil {
			return err
		}
		rids := make([]string, 0, len(t.resources))
		for id := range t.resources {
			rids = append(rids, id)
		}
		return s.refreshLoad(c, tx, rids)
	})
}

// lockLoad serializes writers of one resource's load rows and episode.
//
// Why this cannot deadlock: it is the only place the key is taken, always at
// the end of a transaction (loadTx) or in a rebuild, and refreshLoad takes the
// keys of all its resources in ascending id order before it reads or writes
// anything. After that the transaction only reads (bookings, resources,
// overrides: no row locks) and writes rows of scheduling_capacity_load and
// scheduling_bottleneck_episode for resources whose key it holds; no other
// code writes those tables, and they carry no foreign key, so the writes take
// no lock on scheduling_resource that UpdateResource (resource row FOR UPDATE,
// then this key) could hold. A transaction waiting here therefore waits only
// on another transaction past its own business writes that waits, if at all,
// on a key higher than one it holds: no cycle can close, whatever row locks
// either took earlier in its own order.
func lockLoad(ctx context.Context, tx pgx.Tx, resourceID string) error {
	h := fnv.New64a()
	_, _ = h.Write([]byte(tenantOf(ctx) + "|capacity-load|" + resourceID))
	_, err := tx.Exec(ctx, `select pg_advisory_xact_lock($1)`, int64(h.Sum64()))
	return err
}

// refreshLoad rewrites the materialized weekly load of the resources and opens
// or closes their bottleneck episodes. It takes every resource's load lock in
// id order first (see lockLoad) and only then reads the calendars, overrides
// and bookings, so a calendar change that committed while this transaction
// waited for the lock is the one it sees (READ COMMITTED reads per statement).
func (s *Postgres) refreshLoad(ctx context.Context, tx pgx.Tx, rids []string) error {
	if len(rids) == 0 {
		return nil
	}
	sort.Strings(rids)
	for _, id := range rids {
		if err := lockLoad(ctx, tx, id); err != nil {
			return err
		}
	}
	resources, _, err := loadResources(ctx, tx)
	if err != nil {
		return err
	}
	byID := make(map[string]domain.Resource, len(resources))
	for _, r := range resources {
		byID[r.ID] = r
	}
	for _, id := range rids {
		r, ok := byID[id]
		if !ok {
			continue
		}
		if err := s.refreshResource(ctx, tx, r); err != nil {
			return err
		}
	}
	return nil
}

func (s *Postgres) refreshResource(ctx context.Context, tx pgx.Tx, r domain.Resource) error {
	rows, err := tx.Query(ctx, `select job_id::text, start_at, end_at from scheduled_job where resource_id = $1::uuid
		union all select job_id::text, start_at, end_at from scheduled_job_secondary where resource_id = $1::uuid`, r.ID)
	if err != nil {
		return err
	}
	var booked []domain.Busy
	for rows.Next() {
		b := domain.Busy{ResourceID: r.ID}
		if err := rows.Scan(&b.JobID, &b.Start, &b.End); err != nil {
			rows.Close()
			return err
		}
		booked = append(booked, b)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}
	var loads []domain.WeekLoad
	if len(booked) > 0 {
		first, last := booked[0].Start, booked[0].End
		for _, b := range booked {
			if b.Start.Before(first) {
				first = b.Start
			}
			if b.End.After(last) {
				last = b.End
			}
		}
		week, lastWeek := domain.WeekStart(first), domain.WeekStart(last.Add(-time.Nanosecond))
		weeks := int(lastWeek.Sub(week).Hours()/(24*7)) + 1
		if weeks > maxLoadWeeks {
			week, weeks = lastWeek.AddDate(0, 0, -7*(maxLoadWeeks-1)), maxLoadWeeks
		}
		pct, consecutive := s.rule()
		loads = domain.WeeklyLoad(r, domain.LoadIntervals(r, week, weeks), booked, week, weeks, pct, consecutive)
	}
	if _, err := tx.Exec(ctx, `delete from scheduling_capacity_load where resource_id = $1::uuid`, r.ID); err != nil {
		return err
	}
	batch := &pgx.Batch{}
	for _, l := range loads {
		if l.BookedMinutes == 0 {
			continue
		}
		batch.Queue(`insert into scheduling_capacity_load (tenant_id, resource_id, week_start, open_minutes, booked_minutes, load_pct, bottleneck)
			values ($1, $2, $3, $4, $5, $6, $7)`, tenantOf(ctx), r.ID, l.WeekStart, l.OpenMinutes, l.BookedMinutes,
			math.Round(l.LoadPct*10)/10, l.Bottleneck)
	}
	if batch.Len() > 0 {
		if err := tx.SendBatch(ctx, batch).Close(); err != nil {
			return err
		}
	}
	return s.syncEpisode(ctx, tx, r, loads)
}

// syncEpisode opens a bottleneck episode, with its event, when the resource
// has a flagged week from the current week on and no episode is open, and
// closes the open one when no flagged week is left. An episode that stays open
// adds no event. An open episode whose last flagged week (last_week, kept at
// every rewrite) is already behind the current week ended when that week did,
// with no change to the resource: it is closed as of then, so an overload
// found now is a new episode with its own event, not absorbed by the old one.
func (s *Postgres) syncEpisode(ctx context.Context, tx pgx.Tx, r domain.Resource, loads []domain.WeekLoad) error {
	current := domain.WeekStart(s.now())
	flag, hot := domain.FlaggedFrom(loads, current)
	var open string
	var lastWeek time.Time
	err := tx.QueryRow(ctx, `select id::text, last_week from scheduling_bottleneck_episode where resource_id = $1::uuid and closed_at is null for update`,
		r.ID).Scan(&open, &lastWeek)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return err
	}
	hasOpen := err == nil
	if hasOpen && lastWeek.Before(current) {
		if _, err := tx.Exec(ctx, `update scheduling_bottleneck_episode set closed_at = $2 where id = $1::uuid`, open, lastWeek.AddDate(0, 0, 7)); err != nil {
			return err
		}
		hasOpen = false
	}
	switch {
	case hot && !hasOpen:
		pct, consecutive := s.rule()
		id := ids.New()
		peak := math.Round(flag.PeakPct*10) / 10
		if _, err := tx.Exec(ctx, `insert into scheduling_bottleneck_episode (tenant_id, id, resource_id, first_week, last_week, flagged_weeks, peak_load_pct, threshold_pct, consecutive_weeks)
			values ($1, $2, $3, $4, $5, $6, $7, $8, $9)`, tenantOf(ctx), id, r.ID, flag.First, flag.Last, flag.Weeks, peak, pct, consecutive); err != nil {
			return err
		}
		periods := make([]schedevents.BottleneckPeriod, len(flag.Periods))
		for i, l := range flag.Periods {
			periods[i] = schedevents.BottleneckPeriod{WeekStart: l.WeekStart.Format(time.DateOnly), LoadPct: math.Round(l.LoadPct*10) / 10}
		}
		return enqueue(ctx, tx, "bottleneck", id, schedevents.BottleneckDetected, schedevents.BottleneckDetectedData{
			EpisodeID: id, ResourceID: r.ID, ResourceCode: r.Code, FirstWeek: flag.First.Format(time.DateOnly), FlaggedWeeks: flag.Weeks,
			Periods: periods, PeakLoadPct: peak, ThresholdPct: pct, ConsecutiveWeeks: consecutive})
	case hot && hasOpen && !flag.Last.Equal(lastWeek):
		_, err := tx.Exec(ctx, `update scheduling_bottleneck_episode set last_week = $2 where id = $1::uuid`, open, flag.Last)
		return err
	case !hot && hasOpen:
		_, err := tx.Exec(ctx, `update scheduling_bottleneck_episode set closed_at = now() where id = $1::uuid`, open)
		return err
	}
	return nil
}

// RefreshOutcome reports what a full rebuild of the materialized load did.
type RefreshOutcome struct {
	Resources    int `json:"resources"`
	LoadRows     int `json:"loadRows"`
	OpenEpisodes int `json:"openEpisodes"`
}

// BottleneckStore is the materialized load and bottleneck part of the Store.
type BottleneckStore interface {
	RefreshCapacityLoad(ctx context.Context) (RefreshOutcome, error)
	ListBottlenecks(ctx context.Context, f BottleneckFilter) ([]BottleneckEpisode, error)
	GetBottleneck(ctx context.Context, id string) (BottleneckDetail, error)
}

// RefreshCapacityLoad rebuilds the materialized load of every resource, for
// data written before the table existed or to repair it. It opens and closes
// episodes like any other refresh. Resources are rewritten refreshChunk at a
// time, each chunk in its own transaction, so a large tenant's runs and moves
// wait for at most one chunk; every chunk is complete on its own, and a
// failure leaves the earlier chunks rebuilt, which a repeat finishes.
func (s *Postgres) RefreshCapacityLoad(ctx context.Context) (RefreshOutcome, error) {
	var out RefreshOutcome
	var rids []string
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `select id::text from scheduling_resource order by id`)
		if err != nil {
			return err
		}
		rids, err = pgx.CollectRows(rows, pgx.RowTo[string])
		return err
	})
	if err != nil {
		return out, err
	}
	for i := 0; i < len(rids); i += refreshChunk {
		chunk := rids[i:min(i+refreshChunk, len(rids))]
		if err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error { return s.refreshLoad(ctx, tx, chunk) }); err != nil {
			return out, err
		}
	}
	out.Resources = len(rids)
	err = dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		return tx.QueryRow(ctx, `select (select count(*) from scheduling_capacity_load),
			(select count(*) from scheduling_bottleneck_episode where closed_at is null)`).Scan(&out.LoadRows, &out.OpenEpisodes)
	})
	return out, err
}

// BottleneckFilter narrows ListBottlenecks. Status is open, closed or all
// (default all).
type BottleneckFilter struct {
	Status     string
	ResourceID string
	Limit      int
}

// BottleneckEpisode is a stretch during which a resource had flagged weeks.
// CurrentFlaggedWeeks and CurrentPeakLoadPct describe the flagged weeks from
// the current week on that the materialized load holds now for an open
// episode (0 once it is closed). An episode whose flagged weeks have all
// passed reads as closed at the end of its last one even before the next
// rewrite of its resource records that.
type BottleneckEpisode struct {
	ID                  string     `json:"id"`
	ResourceID          string     `json:"resourceId"`
	ResourceCode        string     `json:"resourceCode"`
	FirstWeek           string     `json:"firstWeek"`
	FlaggedWeeks        int        `json:"flaggedWeeks"`
	PeakLoadPct         float64    `json:"peakLoadPct"`
	ThresholdPct        int        `json:"thresholdPct"`
	ConsecutiveWeeks    int        `json:"consecutiveWeeks"`
	DetectedAt          time.Time  `json:"detectedAt"`
	ClosedAt            *time.Time `json:"closedAt"`
	CurrentFlaggedWeeks int        `json:"currentFlaggedWeeks"`
	CurrentPeakLoadPct  float64    `json:"currentPeakLoadPct"`
}

// LoadWeek is one materialized week of a resource.
type LoadWeek struct {
	WeekStart     string  `json:"weekStart"`
	OpenMinutes   int     `json:"openMinutes"`
	BookedMinutes int     `json:"bookedMinutes"`
	LoadPct       float64 `json:"loadPct"`
	Bottleneck    bool    `json:"bottleneck"`
}

// BottleneckDetail is an episode with the materialized weeks of its resource
// from the episode's first week on.
type BottleneckDetail struct {
	BottleneckEpisode
	Weeks []LoadWeek `json:"weeks"`
}

// episodeCols selects an episode; cw is the placeholder of the current week's
// Monday. episodeOpen is the condition of an episode that is still open.
func episodeCols(cw string) string {
	open := episodeOpen(cw)
	return `e.id::text, e.resource_id::text, r.code, e.first_week::text, e.flagged_weeks, e.peak_load_pct::float8, e.threshold_pct,
	e.consecutive_weeks, e.detected_at,
	coalesce(e.closed_at, case when e.last_week < ` + cw + `::date then (e.last_week + 7)::timestamp at time zone 'UTC' end),
	case when ` + open + ` then (select count(*) from scheduling_capacity_load l where l.resource_id = e.resource_id and l.bottleneck and l.week_start >= ` + cw + `::date) else 0 end,
	case when ` + open + ` then coalesce((select max(l.load_pct) from scheduling_capacity_load l where l.resource_id = e.resource_id and l.bottleneck and l.week_start >= ` + cw + `::date), 0)::float8 else 0 end`
}

func episodeOpen(cw string) string {
	return `(e.closed_at is null and e.last_week >= ` + cw + `::date)`
}

func scanEpisode(row pgx.Row) (BottleneckEpisode, error) {
	var e BottleneckEpisode
	err := row.Scan(&e.ID, &e.ResourceID, &e.ResourceCode, &e.FirstWeek, &e.FlaggedWeeks, &e.PeakLoadPct, &e.ThresholdPct,
		&e.ConsecutiveWeeks, &e.DetectedAt, &e.ClosedAt, &e.CurrentFlaggedWeeks, &e.CurrentPeakLoadPct)
	return e, err
}

// ListBottlenecks returns episodes, newest first.
func (s *Postgres) ListBottlenecks(ctx context.Context, f BottleneckFilter) ([]BottleneckEpisode, error) {
	if f.Limit <= 0 || f.Limit > 200 {
		f.Limit = 50
	}
	out := []BottleneckEpisode{}
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `select `+episodeCols("$4")+` from scheduling_bottleneck_episode e
			join scheduling_resource r on r.tenant_id = e.tenant_id and r.id = e.resource_id
			where ($1 = 'all' or ($1 = 'open') = `+episodeOpen("$4")+`) and ($2 = '' or e.resource_id = $2::uuid)
			order by e.detected_at desc, e.id limit $3`, f.statusOrAll(), f.ResourceID, f.Limit, s.currentWeek())
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			e, err := scanEpisode(rows)
			if err != nil {
				return err
			}
			out = append(out, e)
		}
		return rows.Err()
	})
	return out, err
}

func (f BottleneckFilter) statusOrAll() string {
	if f.Status == "open" || f.Status == "closed" {
		return f.Status
	}
	return "all"
}

// GetBottleneck returns one episode and the materialized weeks of its
// resource from its first week on (at most 104).
func (s *Postgres) GetBottleneck(ctx context.Context, id string) (BottleneckDetail, error) {
	var out BottleneckDetail
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		e, err := scanEpisode(tx.QueryRow(ctx, `select `+episodeCols("$2")+` from scheduling_bottleneck_episode e
			join scheduling_resource r on r.tenant_id = e.tenant_id and r.id = e.resource_id where e.id = $1::uuid`, id, s.currentWeek()))
		if errors.Is(err, pgx.ErrNoRows) {
			return httpx.NewProblem(http.StatusNotFound, CodeBottleneckNotFound, "bottleneck episode not found")
		}
		if err != nil {
			return err
		}
		out = BottleneckDetail{BottleneckEpisode: e, Weeks: []LoadWeek{}}
		rows, err := tx.Query(ctx, `select week_start::text, open_minutes, booked_minutes, load_pct::float8, bottleneck
			from scheduling_capacity_load where resource_id = $1::uuid and week_start >= $2::date order by week_start limit 104`, e.ResourceID, e.FirstWeek)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var w LoadWeek
			if err := rows.Scan(&w.WeekStart, &w.OpenMinutes, &w.BookedMinutes, &w.LoadPct, &w.Bottleneck); err != nil {
				return err
			}
			out.Weeks = append(out.Weeks, w)
		}
		return rows.Err()
	})
	return out, err
}
