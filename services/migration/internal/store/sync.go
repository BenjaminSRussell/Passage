package store

import (
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/BenjaminSRussell/passage/platform/dbx"
	"github.com/BenjaminSRussell/passage/platform/events"
	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/platform/tenant"
	"github.com/BenjaminSRussell/passage/services/migration/internal/domain"
	migevents "github.com/BenjaminSRussell/passage/services/migration/internal/events"
)

const (
	CodeSyncClaimLost   = "MIG-SYNC-CLAIM-LOST"
	CodeConflictClosed  = "MIG-CONFLICT-RESOLVED"
	CodeConflictMissing = "AUTH-0404"

	// syncClaimStale is when a claim counts as abandoned: the run deadline plus a margin, as for project claims.
	syncClaimStale = activeRunStale
)

// SyncState is the sync status of one entity binding: the binding's own fields, its schedule (from the migrate
// project that names its connection profile) and what the scheduler has recorded. A binding that was never
// scheduled has Version 0 and no times.
type SyncState struct {
	BindingID           string     `json:"bindingId"`
	Entity              string     `json:"entity"`
	ConnectionProfileID string     `json:"connectionProfileId"`
	ProfileName         string     `json:"connectionProfileName"`
	BindingStatus       string     `json:"bindingStatus"`
	ProjectID           string     `json:"projectId,omitempty"`
	ScheduleCron        string     `json:"scheduleCron,omitempty"`
	SourceOfTruth       string     `json:"sourceOfTruth"`
	Paused              bool       `json:"paused"`
	Watermark           string     `json:"watermark"`
	NextDueAt           *time.Time `json:"nextDueAt,omitempty"`
	Running             bool       `json:"running"`
	LastRunID           string     `json:"lastRunId,omitempty"`
	LastRunAt           *time.Time `json:"lastRunAt,omitempty"`
	LastSuccessAt       *time.Time `json:"lastSuccessAt,omitempty"`
	LastStatus          string     `json:"lastStatus,omitempty"`
	LastError           string     `json:"lastError,omitempty"`
	ConsecutiveFailures int        `json:"consecutiveFailures"`
	RowsLoaded          int64      `json:"rowsLoaded"`
	RowsRejected        int64      `json:"rowsRejected"`
	OpenConflicts       int        `json:"openConflicts"`
	Version             int        `json:"version"`
	// WatermarkBindingVersion is the entity binding version the watermark was read under (nil before the first
	// advance). A watermark read under another version of the binding is not used.
	WatermarkBindingVersion *int `json:"-"`
}

// SyncCandidate names a binding a tenant's scheduler may have to look at. It carries identifiers only: the schedule
// is read again in the tenant's own transaction.
type SyncCandidate struct {
	TenantID  string
	BindingID string
}

// SyncFilter lists sync status in (entity, id) order.
type SyncFilter struct {
	Paused    *bool
	Failing   bool
	BindingID string
	After     *BindingCursor
	Limit     int
}

// SyncEdit is what an operator changes on a binding's sync: pause or resume, which system wins, and a run now.
type SyncEdit struct {
	Paused        *bool
	SourceOfTruth *string
	RunNow        bool
}

// SyncFinish ends a claim. Advance says whether Watermark is stored: only for a run whose loads committed.
type SyncFinish struct {
	BindingID, ClaimID, RunID, Entity string
	Status                            string
	Error                             string
	Advance                           bool
	Watermark                         string
	BindingVersion                    int // the binding version Watermark was read under
	Extracted, Loaded, Rejected       int
	NextCron                          time.Time
	BackoffSecs                       float64
	Failures                          int // consecutive failures after this run
	Conflicts                         []domain.Conflict
}

// SyncConflict is a legacy row held back because Passage is the source of truth.
type SyncConflict struct {
	ID           string         `json:"id"`
	BindingID    string         `json:"bindingId"`
	Entity       string         `json:"entity"`
	RunID        string         `json:"runId,omitempty"`
	SourceKey    string         `json:"sourceKey"`
	TargetID     string         `json:"targetId,omitempty"`
	LegacyHash   string         `json:"-"`
	LegacyFields map[string]any `json:"legacyFields"`
	Status       string         `json:"status"`
	DetectedAt   time.Time      `json:"detectedAt"`
	ResolvedAt   *time.Time     `json:"resolvedAt,omitempty"`
	ResolvedBy   string         `json:"resolvedBy,omitempty"`
	Version      int            `json:"version"`
}

type ConflictFilter struct {
	BindingID string
	Status    string
	After     *ConflictCursor
	Limit     int
}

// ConflictCursor is the keyset position after the last conflict of a page (newest detected first).
type ConflictCursor struct {
	DetectedAt time.Time `json:"d"`
	ID         string    `json:"i"`
}

// SyncStore is the scheduler's and the monitor's view of the store.
type SyncStore interface {
	// SyncCandidates looks across tenants (no principal in ctx) for bindings that need a state row or are due.
	SyncCandidates(ctx context.Context, max int) ([]SyncCandidate, error)
	// SeedSync reads the binding's schedule (the lowest-id active scheduled migrate project on its profile) in the
	// tenant's transaction and creates its state (due now) or, when its project or schedule changed, moves it to
	// next(cron). A binding with no schedule is left alone.
	SeedSync(ctx context.Context, bindingID string, next func(cron string) (time.Time, bool)) error
	// ClaimSync takes a due, unpaused, unclaimed (or abandoned) binding in one conditional update on the database
	// clock; ok is false when another replica won or nothing is due.
	ClaimSync(ctx context.Context, bindingID, claimID string) (st SyncState, ok bool, err error)
	// ReleaseSync gives a claim back untouched (the project already had a run active).
	ReleaseSync(ctx context.Context, bindingID, claimID string) error
	// FinishSync ends the claim, stores the watermark when f.Advance, the conflicts and the event, in one
	// transaction. A claim that was reclaimed meanwhile answers 409 MIG-SYNC-CLAIM-LOST and changes nothing.
	FinishSync(ctx context.Context, f SyncFinish) error
	GetSync(ctx context.Context, bindingID string) (SyncState, error)
	ListSync(ctx context.Context, f SyncFilter) ([]SyncState, error)
	UpdateSync(ctx context.Context, bindingID string, version int, e SyncEdit) (SyncState, error)
	// KeptConflicts maps source key to the legacy hash an operator chose to leave out.
	KeptConflicts(ctx context.Context, bindingID string) (map[string]string, error)
	ListConflicts(ctx context.Context, f ConflictFilter) ([]SyncConflict, error)
	GetConflict(ctx context.Context, id string) (SyncConflict, error)
	// ResolveConflict closes an open conflict decided on version; 409 MIG-CONFLICT-RESOLVED when it is no longer
	// open, 409 MIG-VERSION-CONFLICT when a run replaced its legacy values since.
	ResolveConflict(ctx context.Context, id string, version int, status, by string) (SyncConflict, error)
}

func syncClaimLost() error {
	return httpx.NewProblem(http.StatusConflict, CodeSyncClaimLost, "the sync claim was reclaimed by another run")
}

func conflictNotFound() error {
	return httpx.NewProblem(http.StatusNotFound, CodeConflictMissing, "sync conflict not found")
}

func conflictClosed() error {
	return httpx.NewProblem(http.StatusConflict, CodeConflictClosed, "this conflict is already resolved")
}

// IsSyncClaimLost reports whether err is the claim-lost refusal.
func IsSyncClaimLost(err error) bool {
	var p *httpx.Problem
	return errors.As(err, &p) && p.Code == CodeSyncClaimLost
}

const syncSelect = `select b.id::text, b.entity, b.connection_profile_id::text, cp.name, b.status,
	coalesce(s.project_id::text, ''), coalesce(sched.schedule_cron, ''), coalesce(s.source_of_truth, 'legacy'), coalesce(s.paused, false), coalesce(s.watermark, ''),
	case when sched.schedule_cron is null then null else s.next_due_at end, coalesce(s.claim_id is not null, false), coalesce(s.last_run_id::text, ''), s.last_run_at, s.last_success_at,
	coalesce(s.last_status, ''), coalesce(s.last_error, ''), coalesce(s.consecutive_failures, 0), coalesce(s.rows_loaded, 0), coalesce(s.rows_rejected, 0),
	(select count(*) from migration.sync_conflict c where c.tenant_id = b.tenant_id and c.binding_id = b.id and c.status = 'open')::int, coalesce(s.version, 0),
	s.watermark_binding_version
	from migration.entity_binding b
	join migration.connection_profile cp on cp.tenant_id = b.tenant_id and cp.id = b.connection_profile_id
	left join migration.sync_state s on s.tenant_id = b.tenant_id and s.binding_id = b.id
	left join lateral (select p.schedule_cron from migration.project p where p.tenant_id = b.tenant_id and p.connection_profile_id = b.connection_profile_id
		and p.project_type = 'migrate' and p.status = 'active' and p.schedule_cron is not null and p.schedule_cron <> '' order by p.id limit 1) sched on true`

func scanSync(row pgx.Row) (SyncState, error) {
	var x SyncState
	err := row.Scan(&x.BindingID, &x.Entity, &x.ConnectionProfileID, &x.ProfileName, &x.BindingStatus, &x.ProjectID, &x.ScheduleCron, &x.SourceOfTruth, &x.Paused, &x.Watermark,
		&x.NextDueAt, &x.Running, &x.LastRunID, &x.LastRunAt, &x.LastSuccessAt, &x.LastStatus, &x.LastError, &x.ConsecutiveFailures, &x.RowsLoaded, &x.RowsRejected, &x.OpenConflicts, &x.Version, &x.WatermarkBindingVersion)
	return x, err
}

func (s *Postgres) SyncCandidates(ctx context.Context, max int) ([]SyncCandidate, error) {
	rows, err := s.pool.Query(ctx, `select tenant_id::text, binding_id::text from migration.sync_candidates($1)`, max)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []SyncCandidate
	for rows.Next() {
		var c SyncCandidate
		if err := rows.Scan(&c.TenantID, &c.BindingID); err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

// SyncCanScan reports whether sync_candidates can see past row-level security; false means the scheduler is blind.
func (s *Postgres) SyncCanScan(ctx context.Context) (bool, error) {
	var ok bool
	err := s.pool.QueryRow(ctx, `select migration.sync_can_scan()`).Scan(&ok)
	return ok, err
}

// scheduleOf is the binding's schedule: the lowest-id active migrate project with a schedule on its profile.
const scheduleOf = `select p.id::text, p.schedule_cron from migration.entity_binding b
	join migration.project p on p.tenant_id = b.tenant_id and p.connection_profile_id = b.connection_profile_id and p.project_type = 'migrate'
		and p.status = 'active' and p.schedule_cron is not null and p.schedule_cron <> ''
	where b.tenant_id = $1 and b.id = $2 order by p.id limit 1`

func (s *Postgres) SeedSync(ctx context.Context, bindingID string, next func(cron string) (time.Time, bool)) error {
	p, _ := tenant.FromContext(ctx)
	if !ids.Valid(bindingID) {
		return bindingNotFound()
	}
	return dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		var projectID, cron string
		err := tx.QueryRow(ctx, scheduleOf, p.TenantID, bindingID).Scan(&projectID, &cron)
		if errors.Is(err, pgx.ErrNoRows) {
			return nil
		}
		if err != nil {
			return err
		}
		due, ok := next(cron)
		if !ok {
			return fmt.Errorf("schedule %q of project %s has no next firing", cron, projectID)
		}
		_, err = tx.Exec(ctx, `insert into migration.sync_state (tenant_id, binding_id, project_id, schedule_cron, next_due_at) values ($1, $2, $3, $4, now())
			on conflict (tenant_id, binding_id) do update set project_id = excluded.project_id, schedule_cron = excluded.schedule_cron, next_due_at = $5, updated_at = now()
			where migration.sync_state.schedule_cron is distinct from excluded.schedule_cron or migration.sync_state.project_id is distinct from excluded.project_id`,
			p.TenantID, bindingID, projectID, cron, due)
		return err
	})
}

func (s *Postgres) ClaimSync(ctx context.Context, bindingID, claimID string) (SyncState, bool, error) {
	p, _ := tenant.FromContext(ctx)
	var out SyncState
	claimed := false
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		// One conditional update on the database clock: of two replicas exactly one matches. A claim older than the
		// abandonment window is taken over and counts as a failure of the run that never finished; the watermark is
		// untouched, so the new run re-reads what the abandoned one may not have loaded.
		tag, err := tx.Exec(ctx, `update migration.sync_state s set claim_id = $3, claimed_at = now(),
			consecutive_failures = case when s.claim_id is not null then s.consecutive_failures + 1 else s.consecutive_failures end,
			last_status = case when s.claim_id is not null then 'abandoned' else s.last_status end,
			last_error = case when s.claim_id is not null then 'the previous run did not finish within 40 minutes and was abandoned' else s.last_error end,
			updated_at = now()
			where s.tenant_id = $1 and s.binding_id = $2 and not s.paused and s.schedule_cron is not null and s.next_due_at <= now()
			and (s.claim_id is null or s.claimed_at < now() - make_interval(secs => $4))
			and exists (select 1 from migration.entity_binding b join migration.project p on p.tenant_id = b.tenant_id and p.connection_profile_id = b.connection_profile_id
				where b.tenant_id = s.tenant_id and b.id = s.binding_id and b.status = 'active' and p.id = s.project_id and p.project_type = 'migrate'
				and p.status = 'active' and p.schedule_cron = s.schedule_cron)`,
			p.TenantID, bindingID, claimID, syncClaimStale.Seconds())
		if err != nil || tag.RowsAffected() == 0 {
			return err
		}
		claimed = true
		out, err = scanSync(tx.QueryRow(ctx, syncSelect+` where b.tenant_id = $1 and b.id = $2`, p.TenantID, bindingID))
		return err
	})
	return out, claimed, err
}

func (s *Postgres) ReleaseSync(ctx context.Context, bindingID, claimID string) error {
	p, _ := tenant.FromContext(ctx)
	return dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		_, err := tx.Exec(ctx, `update migration.sync_state set claim_id = null, claimed_at = null, updated_at = now()
			where tenant_id = $1 and binding_id = $2 and claim_id = $3`, p.TenantID, bindingID, claimID)
		return err
	})
}

func (s *Postgres) FinishSync(ctx context.Context, f SyncFinish) error {
	p, _ := tenant.FromContext(ctx)
	failed := f.Status == domain.SyncFailed
	return dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		var next time.Time
		var stored string
		err := tx.QueryRow(ctx, `update migration.sync_state set claim_id = null, claimed_at = null,
			watermark = case when $4 then $5 else watermark end,
			watermark_binding_version = case when $4 then $15::int else watermark_binding_version end,
			last_run_id = nullif($6, '')::uuid, last_run_at = now(), last_status = $7, last_error = $8,
			last_success_at = case when $9 then last_success_at else now() end,
			consecutive_failures = $10,
			rows_loaded = rows_loaded + $11, rows_rejected = rows_rejected + $12,
			next_due_at = greatest($13::timestamptz, now() + make_interval(secs => $14)), updated_at = now()
			where tenant_id = $1 and binding_id = $2 and claim_id = $3 returning next_due_at, watermark`,
			p.TenantID, f.BindingID, f.ClaimID, f.Advance, f.Watermark, f.RunID, f.Status, f.Error, failed, f.Failures, f.Loaded, f.Rejected, f.NextCron, f.BackoffSecs, f.BindingVersion).Scan(&next, &stored)
		if errors.Is(err, pgx.ErrNoRows) {
			return syncClaimLost()
		}
		if err != nil {
			return err
		}
		for _, c := range f.Conflicts {
			fields, err := json.Marshal(c.Fields)
			if err != nil {
				return err
			}
			if _, err := tx.Exec(ctx, `insert into migration.sync_conflict (tenant_id, id, binding_id, run_id, source_key, target_id, legacy_hash, legacy_fields)
				values ($1, $2, $3, nullif($4, '')::uuid, $5, $6, $7, $8)
				on conflict (tenant_id, binding_id, source_key) do update set run_id = excluded.run_id, target_id = excluded.target_id, legacy_hash = excluded.legacy_hash,
				version = case when migration.sync_conflict.status = 'open' and migration.sync_conflict.legacy_hash = excluded.legacy_hash
					then migration.sync_conflict.version else migration.sync_conflict.version + 1 end,
				legacy_fields = excluded.legacy_fields, detected_at = case when migration.sync_conflict.status = 'open' then migration.sync_conflict.detected_at else now() end,
				status = 'open', resolved_at = null, resolved_by = ''`,
				p.TenantID, ids.New(), f.BindingID, f.RunID, c.SourceKey, c.TargetID, c.ContentHash, fields); err != nil {
				return err
			}
		}
		typ, errText := migevents.SyncRunCompleted, ""
		if failed {
			typ, errText = migevents.SyncRunFailed, f.Error
		}
		nextText := next.UTC().Format(time.RFC3339)
		evt, err := events.New(typ, migevents.Source, f.BindingID, p.TenantID, migevents.SyncRunData{BindingID: f.BindingID, Entity: f.Entity, RunID: f.RunID, Status: f.Status,
			Extracted: f.Extracted, Loaded: f.Loaded, Rejected: f.Rejected, Conflicted: len(f.Conflicts), Watermark: stored, ConsecutiveFailures: f.Failures, NextDueAt: &nextText, Error: errText})
		if err != nil {
			return err
		}
		payload, err := json.Marshal(evt)
		if err != nil {
			return err
		}
		_, err = tx.Exec(ctx, `insert into migration.outbox (id, tenant_id, aggregate_type, aggregate_id, type, payload) values ($1, $2, 'syncrun', $3, $4, $5)`,
			evt.ID, p.TenantID, f.BindingID, evt.Type, payload)
		return err
	})
}

func (s *Postgres) GetSync(ctx context.Context, bindingID string) (SyncState, error) {
	p, _ := tenant.FromContext(ctx)
	if !ids.Valid(bindingID) {
		return SyncState{}, bindingNotFound()
	}
	var out SyncState
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		var err error
		out, err = scanSync(tx.QueryRow(ctx, syncSelect+` where b.tenant_id = $1 and b.id = $2`, p.TenantID, bindingID))
		return err
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return SyncState{}, bindingNotFound()
	}
	return out, err
}

func (s *Postgres) ListSync(ctx context.Context, f SyncFilter) ([]SyncState, error) {
	p, _ := tenant.FromContext(ctx)
	afterEntity, afterID := "", "00000000-0000-0000-0000-000000000000"
	if f.After != nil {
		afterEntity, afterID = f.After.Entity, f.After.ID
	}
	var out []SyncState
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, syncSelect+` where b.tenant_id = $1 and (s.binding_id is not null or sched.schedule_cron is not null)
			and ($2::boolean is null or coalesce(s.paused, false) = $2) and (not $3 or coalesce(s.consecutive_failures, 0) > 0)
			and ($4 = '' or b.id = nullif($4, '')::uuid)
			and ($5::boolean is false or (b.entity, b.id) > ($6, $7::uuid))
			order by b.entity, b.id limit $8`, p.TenantID, f.Paused, f.Failing, f.BindingID, f.After != nil, afterEntity, afterID, clampLimit(f.Limit))
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			x, err := scanSync(rows)
			if err != nil {
				return err
			}
			out = append(out, x)
		}
		return rows.Err()
	})
	return out, err
}

func (s *Postgres) UpdateSync(ctx context.Context, bindingID string, version int, e SyncEdit) (SyncState, error) {
	p, _ := tenant.FromContext(ctx)
	if !ids.Valid(bindingID) {
		return SyncState{}, bindingNotFound()
	}
	var out SyncState
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		var cur int
		err := tx.QueryRow(ctx, `select version from migration.sync_state where tenant_id = $1 and binding_id = $2 for update`, p.TenantID, bindingID).Scan(&cur)
		if errors.Is(err, pgx.ErrNoRows) {
			// A binding that was never scheduled gets its row on the first edit (version 0 -> 1).
			var exists bool
			if err := tx.QueryRow(ctx, `select exists (select 1 from migration.entity_binding where tenant_id = $1 and id = $2)`, p.TenantID, bindingID).Scan(&exists); err != nil {
				return err
			}
			if !exists {
				return bindingNotFound()
			}
			if _, err := tx.Exec(ctx, `insert into migration.sync_state (tenant_id, binding_id, version) values ($1, $2, 0) on conflict do nothing`, p.TenantID, bindingID); err != nil {
				return err
			}
			cur = 0
		} else if err != nil {
			return err
		}
		if cur != version {
			return versionConflict(cur)
		}
		if _, err := tx.Exec(ctx, `update migration.sync_state set paused = coalesce($3, paused), source_of_truth = coalesce($4, source_of_truth),
			next_due_at = case when $5 and schedule_cron is not null then now() else next_due_at end, version = version + 1, updated_at = now()
			where tenant_id = $1 and binding_id = $2`, p.TenantID, bindingID, e.Paused, e.SourceOfTruth, e.RunNow); err != nil {
			return err
		}
		out, err = scanSync(tx.QueryRow(ctx, syncSelect+` where b.tenant_id = $1 and b.id = $2`, p.TenantID, bindingID))
		return err
	})
	return out, err
}

func (s *Postgres) KeptConflicts(ctx context.Context, bindingID string) (map[string]string, error) {
	p, _ := tenant.FromContext(ctx)
	out := map[string]string{}
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `select source_key, legacy_hash from migration.sync_conflict where tenant_id = $1 and binding_id = $2 and status = 'kept'`, p.TenantID, bindingID)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var k, h string
			if err := rows.Scan(&k, &h); err != nil {
				return err
			}
			out[k] = h
		}
		return rows.Err()
	})
	return out, err
}

const conflictSelect = `select c.id::text, c.binding_id::text, b.entity, coalesce(c.run_id::text, ''), c.source_key, c.target_id, c.legacy_hash, c.legacy_fields, c.status, c.detected_at, c.resolved_at, c.resolved_by, c.version
	from migration.sync_conflict c join migration.entity_binding b on b.tenant_id = c.tenant_id and b.id = c.binding_id`

func scanConflict(row pgx.Row) (SyncConflict, error) {
	var c SyncConflict
	var fields []byte
	if err := row.Scan(&c.ID, &c.BindingID, &c.Entity, &c.RunID, &c.SourceKey, &c.TargetID, &c.LegacyHash, &fields, &c.Status, &c.DetectedAt, &c.ResolvedAt, &c.ResolvedBy, &c.Version); err != nil {
		return c, err
	}
	return c, json.Unmarshal(fields, &c.LegacyFields)
}

func (s *Postgres) ListConflicts(ctx context.Context, f ConflictFilter) ([]SyncConflict, error) {
	p, _ := tenant.FromContext(ctx)
	afterAt, afterID := time.Time{}, "00000000-0000-0000-0000-000000000000"
	if f.After != nil {
		afterAt, afterID = f.After.DetectedAt, f.After.ID
	}
	var out []SyncConflict
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, conflictSelect+` where c.tenant_id = $1 and ($2 = '' or c.binding_id = nullif($2, '')::uuid) and ($3 = '' or c.status = $3)
			and ($4::boolean is false or (c.detected_at, c.id) < ($5::timestamptz, $6::uuid)) order by c.detected_at desc, c.id desc limit $7`,
			p.TenantID, f.BindingID, f.Status, f.After != nil, afterAt, afterID, clampLimit(f.Limit))
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			c, err := scanConflict(rows)
			if err != nil {
				return err
			}
			out = append(out, c)
		}
		return rows.Err()
	})
	return out, err
}

func (s *Postgres) GetConflict(ctx context.Context, id string) (SyncConflict, error) {
	p, _ := tenant.FromContext(ctx)
	if !ids.Valid(id) {
		return SyncConflict{}, conflictNotFound()
	}
	var c SyncConflict
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		var err error
		c, err = scanConflict(tx.QueryRow(ctx, conflictSelect+` where c.tenant_id = $1 and c.id = $2`, p.TenantID, id))
		return err
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return SyncConflict{}, conflictNotFound()
	}
	return c, err
}

func (s *Postgres) ResolveConflict(ctx context.Context, id string, version int, status, by string) (SyncConflict, error) {
	p, _ := tenant.FromContext(ctx)
	if !ids.Valid(id) {
		return SyncConflict{}, conflictNotFound()
	}
	var out SyncConflict
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		tag, err := tx.Exec(ctx, `update migration.sync_conflict set status = $3, resolved_at = now(), resolved_by = $4, version = version + 1
			where tenant_id = $1 and id = $2 and status = 'open' and version = $5`, p.TenantID, id, status, by, version)
		if err != nil {
			return err
		}
		if tag.RowsAffected() == 0 {
			var cur string
			var curVersion int
			err := tx.QueryRow(ctx, `select status, version from migration.sync_conflict where tenant_id = $1 and id = $2`, p.TenantID, id).Scan(&cur, &curVersion)
			if errors.Is(err, pgx.ErrNoRows) {
				return conflictNotFound()
			}
			if err != nil {
				return err
			}
			if cur != domain.ConflictOpen {
				return conflictClosed()
			}
			return versionConflict(curVersion)
		}
		out, err = scanConflict(tx.QueryRow(ctx, conflictSelect+` where c.tenant_id = $1 and c.id = $2`, p.TenantID, id))
		return err
	})
	return out, err
}

// ---- in-memory fake -------------------------------------------------------------------------------------------

type memSync struct {
	mu        sync.Mutex
	state     map[string]*memSyncRow
	conflicts []SyncConflict
	// Events are the sync-run events FinishSync wrote, oldest first.
	events []events.Event
}

type memSyncRow struct {
	SyncState
	cron    string
	project string
	claim   string
	claimAt time.Time
}

func (m *Mem) sy() *memSync {
	m.syOnce.Do(func() { m.syState = &memSync{state: map[string]*memSyncRow{}} })
	return m.syState
}

// SyncEvents returns the events FinishSync wrote (tests only).
func (m *Mem) SyncEvents() []events.Event {
	st := m.sy()
	st.mu.Lock()
	defer st.mu.Unlock()
	return slices.Clone(st.events)
}

// memTenant is the tenant the in-memory scheduler tests run as.
const MemTenant = "00000000-0000-7000-8000-0000000000a1"

func (m *Mem) memProjectFor(b EntityBinding) (Project, bool) {
	st := m.pj()
	st.mu.Lock()
	defer st.mu.Unlock()
	var out Project
	found := false
	for _, p := range st.items {
		if p.ConnectionProfileID == b.ConnectionProfileID && p.Type == domain.ProjectMigrate && p.Status == domain.ProjectActive && p.ScheduleCron != "" &&
			(!found || p.ID < out.ID) {
			out, found = p, true
		}
	}
	return out, found
}

func (m *Mem) SyncCandidates(_ context.Context, max int) ([]SyncCandidate, error) {
	var out []SyncCandidate
	for _, b := range m.allBindings() {
		pr, ok := m.memProjectFor(b)
		if !ok || b.Status != domain.BindingActive {
			continue
		}
		st := m.sy()
		st.mu.Lock()
		row := st.state[b.ID]
		need := row == nil || row.cron != pr.ScheduleCron || row.project != pr.ID ||
			(!row.Paused && row.NextDueAt != nil && !row.NextDueAt.After(time.Now()) && (row.claim == "" || time.Since(row.claimAt) > syncClaimStale))
		st.mu.Unlock()
		if need {
			out = append(out, SyncCandidate{TenantID: MemTenant, BindingID: b.ID})
		}
	}
	return out[:min(len(out), max)], nil
}

func (m *Mem) allBindings() []EntityBinding {
	st := m.bn()
	st.mu.Lock()
	defer st.mu.Unlock()
	return slices.Clone(st.items)
}

func (m *Mem) view(b EntityBinding, row *memSyncRow) SyncState {
	x := SyncState{BindingID: b.ID, Entity: b.Entity, ConnectionProfileID: b.ConnectionProfileID, BindingStatus: b.Status, SourceOfTruth: domain.SourceLegacy}
	if cp, err := m.GetConnectionProfile(context.Background(), b.ConnectionProfileID); err == nil {
		x.ProfileName = cp.Name
	}
	pr, scheduled := m.memProjectFor(b)
	if row != nil {
		x = row.SyncState
		x.Entity, x.ConnectionProfileID, x.BindingStatus, x.Running = b.Entity, b.ConnectionProfileID, b.Status, row.claim != ""
		if cp, err := m.GetConnectionProfile(context.Background(), b.ConnectionProfileID); err == nil {
			x.ProfileName = cp.Name
		}
	}
	x.ScheduleCron = ""
	if scheduled {
		x.ScheduleCron = pr.ScheduleCron
	} else {
		x.NextDueAt = nil
	}
	m.sy().mu.Lock()
	defer m.sy().mu.Unlock()
	for _, c := range m.sy().conflicts {
		if c.BindingID == b.ID && c.Status == domain.ConflictOpen {
			x.OpenConflicts++
		}
	}
	return x
}

func (m *Mem) SeedSync(ctx context.Context, bindingID string, next func(cron string) (time.Time, bool)) error {
	b, err := m.GetEntityBinding(ctx, bindingID)
	if err != nil {
		return err
	}
	pr, ok := m.memProjectFor(b)
	if !ok {
		return nil
	}
	due, ok := next(pr.ScheduleCron)
	if !ok {
		return fmt.Errorf("schedule %q of project %s has no next firing", pr.ScheduleCron, pr.ID)
	}
	st := m.sy()
	st.mu.Lock()
	defer st.mu.Unlock()
	row := st.state[bindingID]
	if row == nil {
		now := time.Now()
		st.state[bindingID] = &memSyncRow{SyncState: SyncState{BindingID: bindingID, SourceOfTruth: domain.SourceLegacy, NextDueAt: &now, ProjectID: pr.ID}, cron: pr.ScheduleCron, project: pr.ID}
		return nil
	}
	if row.cron != pr.ScheduleCron || row.project != pr.ID {
		row.cron, row.project, row.ProjectID, row.NextDueAt = pr.ScheduleCron, pr.ID, pr.ID, &due
	}
	return nil
}

func (m *Mem) ClaimSync(_ context.Context, bindingID, claimID string) (SyncState, bool, error) {
	b, err := m.GetEntityBinding(context.Background(), bindingID)
	if err != nil {
		return SyncState{}, false, err
	}
	pr, scheduled := m.memProjectFor(b)
	st := m.sy()
	st.mu.Lock()
	row := st.state[bindingID]
	if row == nil || !scheduled || b.Status != domain.BindingActive || row.cron != pr.ScheduleCron || row.Paused || row.NextDueAt == nil || row.NextDueAt.After(time.Now()) ||
		(row.claim != "" && time.Since(row.claimAt) <= syncClaimStale) {
		st.mu.Unlock()
		return SyncState{}, false, nil
	}
	if row.claim != "" {
		row.ConsecutiveFailures++
		row.LastStatus, row.LastError = domain.SyncAbandoned, "the previous run did not finish within 40 minutes and was abandoned"
	}
	row.claim, row.claimAt = claimID, time.Now()
	st.mu.Unlock()
	return m.view(b, row), true, nil
}

func (m *Mem) ReleaseSync(_ context.Context, bindingID, claimID string) error {
	st := m.sy()
	st.mu.Lock()
	defer st.mu.Unlock()
	if row := st.state[bindingID]; row != nil && row.claim == claimID {
		row.claim = ""
	}
	return nil
}

func (m *Mem) FinishSync(_ context.Context, f SyncFinish) error {
	st := m.sy()
	st.mu.Lock()
	defer st.mu.Unlock()
	row := st.state[f.BindingID]
	if row == nil || row.claim != f.ClaimID {
		return syncClaimLost()
	}
	now := time.Now()
	failed := f.Status == domain.SyncFailed
	row.claim = ""
	if f.Advance {
		v := f.BindingVersion
		row.Watermark, row.WatermarkBindingVersion = f.Watermark, &v
	}
	row.LastRunID, row.LastRunAt, row.LastStatus, row.LastError = f.RunID, &now, f.Status, f.Error
	if !failed {
		row.LastSuccessAt = &now
	}
	row.ConsecutiveFailures = f.Failures
	row.RowsLoaded += int64(f.Loaded)
	row.RowsRejected += int64(f.Rejected)
	next := f.NextCron
	if after := now.Add(time.Duration(f.BackoffSecs * float64(time.Second))); after.After(next) {
		next = after
	}
	row.NextDueAt = &next
	for _, c := range f.Conflicts {
		fields := c.Fields
		found := false
		for i := range st.conflicts {
			if st.conflicts[i].BindingID == f.BindingID && st.conflicts[i].SourceKey == c.SourceKey {
				x := &st.conflicts[i]
				if x.Status != domain.ConflictOpen || x.LegacyHash != c.ContentHash {
					x.Version++
				}
				x.RunID, x.TargetID, x.LegacyHash, x.LegacyFields, x.Status, x.ResolvedAt, x.ResolvedBy = f.RunID, c.TargetID, c.ContentHash, fields, domain.ConflictOpen, nil, ""
				found = true
			}
		}
		if !found {
			st.conflicts = append(st.conflicts, SyncConflict{ID: ids.New(), BindingID: f.BindingID, Entity: f.Entity, RunID: f.RunID, SourceKey: c.SourceKey, TargetID: c.TargetID,
				LegacyHash: c.ContentHash, LegacyFields: fields, Status: domain.ConflictOpen, DetectedAt: now, Version: 1})
		}
	}
	typ, errText := migevents.SyncRunCompleted, ""
	if failed {
		typ, errText = migevents.SyncRunFailed, f.Error
	}
	evt, err := events.New(typ, migevents.Source, f.BindingID, MemTenant, migevents.SyncRunData{BindingID: f.BindingID, Entity: f.Entity, RunID: f.RunID, Status: f.Status, Extracted: f.Extracted,
		Loaded: f.Loaded, Rejected: f.Rejected, Conflicted: len(f.Conflicts), Watermark: row.Watermark, ConsecutiveFailures: f.Failures, Error: errText})
	if err != nil {
		return err
	}
	st.events = append(st.events, evt)
	return nil
}

func (m *Mem) GetSync(ctx context.Context, bindingID string) (SyncState, error) {
	b, err := m.GetEntityBinding(ctx, bindingID)
	if err != nil {
		return SyncState{}, err
	}
	st := m.sy()
	st.mu.Lock()
	row := st.state[bindingID]
	st.mu.Unlock()
	return m.view(b, row), nil
}

func (m *Mem) ListSync(_ context.Context, f SyncFilter) ([]SyncState, error) {
	var out []SyncState
	bs := m.allBindings()
	slices.SortFunc(bs, func(a, b EntityBinding) int {
		return cmp.Or(strings.Compare(a.Entity, b.Entity), strings.Compare(a.ID, b.ID))
	})
	for _, b := range bs {
		st := m.sy()
		st.mu.Lock()
		row := st.state[b.ID]
		st.mu.Unlock()
		x := m.view(b, row)
		_, scheduled := m.memProjectFor(b)
		if row == nil && !scheduled {
			continue
		}
		if (f.Paused != nil && x.Paused != *f.Paused) || (f.Failing && x.ConsecutiveFailures == 0) || (f.BindingID != "" && b.ID != f.BindingID) ||
			(f.After != nil && cmp.Or(strings.Compare(b.Entity, f.After.Entity), strings.Compare(b.ID, f.After.ID)) <= 0) {
			continue
		}
		out = append(out, x)
	}
	return out[:min(len(out), clampLimit(f.Limit))], nil
}

func (m *Mem) UpdateSync(ctx context.Context, bindingID string, version int, e SyncEdit) (SyncState, error) {
	b, err := m.GetEntityBinding(ctx, bindingID)
	if err != nil {
		return SyncState{}, err
	}
	st := m.sy()
	st.mu.Lock()
	row := st.state[bindingID]
	if row == nil {
		row = &memSyncRow{SyncState: SyncState{BindingID: bindingID, SourceOfTruth: domain.SourceLegacy}}
		st.state[bindingID] = row
	}
	if row.Version != version {
		cur := row.Version
		if cur == 0 && version != 0 {
			delete(st.state, bindingID)
		}
		st.mu.Unlock()
		return SyncState{}, versionConflict(cur)
	}
	if e.Paused != nil {
		row.Paused = *e.Paused
	}
	if e.SourceOfTruth != nil {
		row.SourceOfTruth = *e.SourceOfTruth
	}
	if e.RunNow && row.cron != "" {
		now := time.Now()
		row.NextDueAt = &now
	}
	row.Version++
	st.mu.Unlock()
	return m.view(b, row), nil
}

func (m *Mem) KeptConflicts(_ context.Context, bindingID string) (map[string]string, error) {
	st := m.sy()
	st.mu.Lock()
	defer st.mu.Unlock()
	out := map[string]string{}
	for _, c := range st.conflicts {
		if c.BindingID == bindingID && c.Status == domain.ConflictKept {
			out[c.SourceKey] = c.LegacyHash
		}
	}
	return out, nil
}

func (m *Mem) ListConflicts(_ context.Context, f ConflictFilter) ([]SyncConflict, error) {
	st := m.sy()
	st.mu.Lock()
	defer st.mu.Unlock()
	newest := func(a, b SyncConflict) int {
		return cmp.Or(b.DetectedAt.Compare(a.DetectedAt), strings.Compare(b.ID, a.ID))
	}
	var out []SyncConflict
	for _, c := range st.conflicts {
		if (f.BindingID == "" || c.BindingID == f.BindingID) && (f.Status == "" || c.Status == f.Status) &&
			(f.After == nil || newest(c, SyncConflict{DetectedAt: f.After.DetectedAt, ID: f.After.ID}) > 0) {
			out = append(out, c)
		}
	}
	slices.SortFunc(out, newest)
	return out[:min(len(out), clampLimit(f.Limit))], nil
}

func (m *Mem) GetConflict(_ context.Context, id string) (SyncConflict, error) {
	st := m.sy()
	st.mu.Lock()
	defer st.mu.Unlock()
	for _, c := range st.conflicts {
		if c.ID == id {
			return c, nil
		}
	}
	return SyncConflict{}, conflictNotFound()
}

func (m *Mem) ResolveConflict(_ context.Context, id string, version int, status, by string) (SyncConflict, error) {
	st := m.sy()
	st.mu.Lock()
	defer st.mu.Unlock()
	for i := range st.conflicts {
		if st.conflicts[i].ID == id {
			if st.conflicts[i].Status != domain.ConflictOpen {
				return SyncConflict{}, conflictClosed()
			}
			if st.conflicts[i].Version != version {
				return SyncConflict{}, versionConflict(st.conflicts[i].Version)
			}
			now := time.Now()
			st.conflicts[i].Status, st.conflicts[i].ResolvedAt, st.conflicts[i].ResolvedBy = status, &now, by
			st.conflicts[i].Version++
			return st.conflicts[i], nil
		}
	}
	return SyncConflict{}, conflictNotFound()
}

// AgeSyncClaim makes a binding's claim look d older (tests only: the real store ages claims on the database clock).
func (m *Mem) AgeSyncClaim(bindingID string, d time.Duration) {
	st := m.sy()
	st.mu.Lock()
	defer st.mu.Unlock()
	if row := st.state[bindingID]; row != nil {
		row.claimAt = row.claimAt.Add(-d)
	}
}
