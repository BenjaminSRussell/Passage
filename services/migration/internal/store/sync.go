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
	CodeSyncNoProject   = "MIG-SYNC-NO-PROJECT"
	CodeConflictClosed  = "MIG-CONFLICT-RESOLVED"
	CodeConflictMissing = "AUTH-0404"

	// syncClaimStale is when a claim counts as abandoned: the run deadline plus a margin, as for project claims.
	syncClaimStale = activeRunStale
)

// SyncState is the sync status of one entity binding: the binding's own fields, its schedule (from the migrate
// project that names its connection profile) and what the scheduler has recorded. A binding that was never
// scheduled has Version 0 and no times.
type SyncState struct {
	BindingID           string `json:"bindingId"`
	Entity              string `json:"entity"`
	ConnectionProfileID string `json:"connectionProfileId"`
	ProfileName         string `json:"connectionProfileName"`
	BindingStatus       string `json:"bindingStatus"`
	ProjectID           string `json:"projectId,omitempty"`
	ScheduleCron        string `json:"scheduleCron,omitempty"`
	// ScheduleOverride is the binding's own cron; when set it wins over the project's scheduleCron (which is the default).
	ScheduleOverride string `json:"scheduleOverride,omitempty"`
	// ProjectScheduleCron is the default the binding falls back to without an override: the cron of the lowest-id
	// active scheduled migrate project on its profile. Empty when there is none, so clearing the override stops the binding.
	ProjectScheduleCron string     `json:"projectScheduleCron,omitempty"`
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
	// KeyCheckHours is the period of the full read that finds rows the legacy system deleted (0: off), LastKeyCheckAt
	// when one last completed, KeyCheckError why the last one produced no list ("" when it did) and AbsentCount how
	// many loaded keys the last completed one did not find.
	KeyCheckHours  int        `json:"keyCheckHours"`
	LastKeyCheckAt *time.Time `json:"lastKeyCheckAt,omitempty"`
	KeyCheckError  string     `json:"keyCheckError,omitempty"`
	AbsentCount    int        `json:"absentCount"`
	// KeyCheckAcceptLarge is set while an operator's one-time acceptance of a full read that lists more than half of the
	// loaded keys as newly absent is waiting for the next completed key check.
	KeyCheckAcceptLarge bool `json:"keyCheckAcceptLarge"`
	// KeyCheckDue is computed from the last full read attempted (failed or not), an operator's request, and, before the
	// first attempt, the binding's offset after its anchor (domain.KeyCheckState): the next run must read the source in full.
	KeyCheckDue bool `json:"-"`
	// KeyCheckForce is an operator's request for the next run to be a full read (keyCheckNow, keyCheckAcceptLarge).
	KeyCheckForce bool `json:"-"`
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
	Paused *bool
	// ScheduleCron sets the binding's own cron ("" clears it and the project's cron applies again); nil leaves it.
	ScheduleCron  *string
	SourceOfTruth *string
	KeyCheckHours *int
	// KeyCheckNow makes the next run a full read that looks for deleted rows, whatever the period.
	KeyCheckNow bool
	// KeyCheckAcceptLarge accepts, once, a full read that lists more than half of the loaded keys as newly absent
	// (otherwise refused with keyCheckError); it also makes the next run a full read.
	KeyCheckAcceptLarge bool
	RunNow              bool
}

// SyncAbsent is a source key loaded earlier that a full read of the legacy source did not find.
type SyncAbsent struct {
	BindingID     string    `json:"bindingId"`
	SourceKey     string    `json:"sourceKey"`
	TargetID      string    `json:"targetId,omitempty"`
	RunID         string    `json:"runId,omitempty"`
	FirstAbsentAt time.Time `json:"firstAbsentAt"`
	LastCheckedAt time.Time `json:"lastCheckedAt"`
}

// AbsentFilter lists absent keys of one binding in source key order; After is the last key of the previous page.
type AbsentFilter struct {
	BindingID string
	After     string
	Limit     int
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
	// Cron is the schedule the claim ran under (SyncState.ScheduleCron). NextCron is its next slot; when the stored
	// schedule changed while the run read the source (an override set or cleared, the project's cron edited), the
	// scheduler has already re-seeded next_due_at from the new cron and that is kept. Empty means NextCron applies.
	Cron        string
	BackoffSecs float64
	Failures    int // consecutive failures after this run
	Conflicts   []domain.Conflict
	// FullRead is set when the run read the whole source and did not fail: Absent (possibly empty) is then the
	// complete list of loaded keys the source no longer has, and replaces the stored list.
	FullRead bool
	// KeyCheckAttempted is set when the run tried a full read, whether or not it completed: the next key check is
	// due one period later, so a full read that keeps failing does not turn every run into one.
	KeyCheckAttempted bool
	Absent            []string
	// KeyCheckForced and KeyCheckAcceptLarge are the operator requests the claim saw (SyncState.KeyCheckForce and
	// .KeyCheckAcceptLarge). Only those are spent by this run: a request sent while the run was reading the source is
	// kept for the next one, so an acceptance never applies to a read that began before the operator gave it.
	KeyCheckForced, KeyCheckAcceptLarge bool
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
	// ListAbsent lists the source keys the last full reads of a binding did not find.
	ListAbsent(ctx context.Context, f AbsentFilter) ([]SyncAbsent, error)
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
	coalesce(s.project_id::text, ''), coalesce(sched.cron, ''), coalesce(s.source_of_truth, 'legacy'), coalesce(s.paused, false), coalesce(s.watermark, ''),
	case when sched.cron is null then null else s.next_due_at end, coalesce(s.claim_id is not null, false), coalesce(s.last_run_id::text, ''), s.last_run_at, s.last_success_at,
	coalesce(s.last_status, ''), coalesce(s.last_error, ''), coalesce(s.consecutive_failures, 0), coalesce(s.rows_loaded, 0), coalesce(s.rows_rejected, 0),
	(select count(*) from migration.sync_conflict c where c.tenant_id = b.tenant_id and c.binding_id = b.id and c.status = 'open')::int, coalesce(s.version, 0),
	s.watermark_binding_version, coalesce(s.keycheck_hours, 24), s.last_keycheck_at, coalesce(s.keycheck_error, ''), coalesce(s.absent_count, 0),
	s.keycheck_attempt_at, coalesce(s.keycheck_force, false), coalesce(s.keycheck_accept_large, false), coalesce(s.keycheck_since, now()), coalesce(s.schedule_override, ''), coalesce(sched.project_cron, '')
	from migration.entity_binding b
	join migration.connection_profile cp on cp.tenant_id = b.tenant_id and cp.id = b.connection_profile_id
	left join migration.sync_state s on s.tenant_id = b.tenant_id and s.binding_id = b.id
	left join lateral (select coalesce(nullif(s.schedule_override, ''), p.schedule_cron) as cron, nullif(p.schedule_cron, '') as project_cron from migration.project p
		where p.tenant_id = b.tenant_id and p.connection_profile_id = b.connection_profile_id and p.project_type = 'migrate' and p.status = 'active'
		and (coalesce(s.schedule_override, '') <> '' or coalesce(p.schedule_cron, '') <> '') order by (coalesce(p.schedule_cron, '') = ''), p.id limit 1) sched on true`

func scanSync(row pgx.Row) (SyncState, error) {
	var x SyncState
	var attempt *time.Time
	var since time.Time
	err := row.Scan(&x.BindingID, &x.Entity, &x.ConnectionProfileID, &x.ProfileName, &x.BindingStatus, &x.ProjectID, &x.ScheduleCron, &x.SourceOfTruth, &x.Paused, &x.Watermark,
		&x.NextDueAt, &x.Running, &x.LastRunID, &x.LastRunAt, &x.LastSuccessAt, &x.LastStatus, &x.LastError, &x.ConsecutiveFailures, &x.RowsLoaded, &x.RowsRejected, &x.OpenConflicts, &x.Version, &x.WatermarkBindingVersion, &x.KeyCheckHours, &x.LastKeyCheckAt, &x.KeyCheckError, &x.AbsentCount, &attempt, &x.KeyCheckForce, &x.KeyCheckAcceptLarge, &since, &x.ScheduleOverride, &x.ProjectScheduleCron)
	x.KeyCheckDue = domain.KeyCheckState{BindingID: x.BindingID, Hours: x.KeyCheckHours, LastAttempt: attempt, Force: x.KeyCheckForce, Since: since}.Due(time.Now())
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

// scheduleOf is the binding's schedule: its own override, else the cron of the lowest-id active migrate project with a
// schedule on its profile. The project it runs under is that one; a project without a schedule counts only when the
// binding has its own.
const scheduleOf = `select p.id::text, coalesce(nullif(s.schedule_override, ''), p.schedule_cron) from migration.entity_binding b
	left join migration.sync_state s on s.tenant_id = b.tenant_id and s.binding_id = b.id
	join migration.project p on p.tenant_id = b.tenant_id and p.connection_profile_id = b.connection_profile_id and p.project_type = 'migrate'
		and p.status = 'active' and (coalesce(s.schedule_override, '') <> '' or coalesce(p.schedule_cron, '') <> '')
	where b.tenant_id = $1 and b.id = $2 order by (coalesce(p.schedule_cron, '') = ''), p.id limit 1`

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
				and p.status = 'active' and coalesce(nullif(s.schedule_override, ''), p.schedule_cron) = s.schedule_cron)`,
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
			next_due_at = greatest(case when $16 = '' or schedule_cron is not distinct from $16 or next_due_at is null then $13::timestamptz else next_due_at end,
				now() + make_interval(secs => $14)), updated_at = now()
			where tenant_id = $1 and binding_id = $2 and claim_id = $3 returning next_due_at, watermark`,
			p.TenantID, f.BindingID, f.ClaimID, f.Advance, f.Watermark, f.RunID, f.Status, f.Error, failed, f.Failures, f.Loaded, f.Rejected, f.NextCron, f.BackoffSecs, f.BindingVersion, f.Cron).Scan(&next, &stored)
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
		switch {
		case f.FullRead && !failed:
			if err := recordAbsent(ctx, tx, p.TenantID, f); err != nil {
				return err
			}
		case f.KeyCheckAttempted || f.FullRead:
			// The full read did not complete: the stored list stays, the next check is one period away.
			if _, err := tx.Exec(ctx, `update migration.sync_state set keycheck_attempt_at = now(), keycheck_force = keycheck_force and not $4, keycheck_error = $3 where tenant_id = $1 and binding_id = $2`,
				p.TenantID, f.BindingID, truncateKeyCheckError("the full read did not complete: "+f.Error), f.KeyCheckForced); err != nil {
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

// recordAbsent replaces the stored absent keys of a binding with those of a full read, counts the read as its key
// check and publishes an event when keys became absent that were not before. Only keys this binding can vouch for are
// kept: the xref row must exist in the project's source system and, for an entity scoped to a legal entity, belong to
// the project's target legal entity. When another active binding loads the same entity from the same source system
// (into the same legal entity, or at all for a tenant-wide entity) the key space is shared and a key missing from this
// read may be the other binding's, so nothing is recorded. A read that found no rows at all while keys are loaded is
// taken for a broken source (an empty export, a filter gone wrong) rather than a deleted table, and is not recorded.
func recordAbsent(ctx context.Context, tx pgx.Tx, tenantID string, f SyncFinish) error {
	keys := f.Absent
	if keys == nil {
		keys = []string{}
	}
	var sourceSystem, le string
	var sharedAny, sharedSameLE bool
	err := tx.QueryRow(ctx, `select p.source_system, coalesce(p.target_legal_entity_id::text, ''),
		exists (select 1 from migration.entity_binding b2 join migration.project p2 on p2.tenant_id = b2.tenant_id and p2.connection_profile_id = b2.connection_profile_id
			where b2.tenant_id = s.tenant_id and b2.id <> s.binding_id and b2.entity = $3 and b2.status = 'active'
			and p2.project_type = 'migrate' and p2.status = 'active' and p2.source_system = p.source_system),
		exists (select 1 from migration.entity_binding b2 join migration.project p2 on p2.tenant_id = b2.tenant_id and p2.connection_profile_id = b2.connection_profile_id
			where b2.tenant_id = s.tenant_id and b2.id <> s.binding_id and b2.entity = $3 and b2.status = 'active'
			and p2.project_type = 'migrate' and p2.status = 'active' and p2.source_system = p.source_system
			and (p.target_legal_entity_id is null or p2.target_legal_entity_id is null or p2.target_legal_entity_id = p.target_legal_entity_id))
		from migration.sync_state s join migration.project p on p.tenant_id = s.tenant_id and p.id = s.project_id
		where s.tenant_id = $1 and s.binding_id = $2`, tenantID, f.BindingID, f.Entity).Scan(&sourceSystem, &le, &sharedAny, &sharedSameLE)
	if errors.Is(err, pgx.ErrNoRows) {
		// No project behind the state (it cannot be claimed then): nothing to scope the keys by.
		_, err = tx.Exec(ctx, `update migration.sync_state set keycheck_attempt_at = now(), keycheck_force = keycheck_force and not $3, keycheck_error = 'the binding has no migration project' where tenant_id = $1 and binding_id = $2`, tenantID, f.BindingID, f.KeyCheckForced)
		return err
	}
	if err != nil {
		return err
	}
	if reason := absentRefusal(f, sharedAny, sharedSameLE); reason != "" {
		_, err := tx.Exec(ctx, `update migration.sync_state set keycheck_attempt_at = now(), keycheck_force = keycheck_force and not $4, keycheck_error = $3 where tenant_id = $1 and binding_id = $2`, tenantID, f.BindingID, reason, f.KeyCheckForced)
		return err
	}
	// The vanish threshold: the keys this read would newly list against the keys the binding vouches for that were not
	// listed already (domain.KeyCheckVanishTooLarge), unless the claim carried an operator's acceptance.
	if !f.KeyCheckAcceptLarge {
		var fresh, present int
		if err := tx.QueryRow(ctx, `select
			(select count(*) from unnest($3::text[]) as k
				join migration.xref x on x.tenant_id = $1 and x.source_system = $4 and x.source_entity = $5 and x.source_key = k
				where ($6 = '' or x.legal_entity_id::text = $6 or (x.legal_entity_id is null and not $7))
				and not exists (select 1 from migration.sync_absent a where a.tenant_id = $1 and a.binding_id = $2 and a.source_key = k))::int,
			(select count(*) from migration.xref x where x.tenant_id = $1 and x.source_system = $4 and x.source_entity = $5
				and ($6 = '' or x.legal_entity_id::text = $6 or (x.legal_entity_id is null and not $7))
				and not exists (select 1 from migration.sync_absent a where a.tenant_id = $1 and a.binding_id = $2 and a.source_key = x.source_key))::int`,
			tenantID, f.BindingID, keys, sourceSystem, f.Entity, le, sharedAny).Scan(&fresh, &present); err != nil {
			return err
		}
		if domain.KeyCheckVanishTooLarge(fresh, present) {
			_, err := tx.Exec(ctx, `update migration.sync_state set keycheck_attempt_at = now(), keycheck_force = keycheck_force and not $4, keycheck_error = $3 where tenant_id = $1 and binding_id = $2`,
				tenantID, f.BindingID, vanishRefusal(fresh, present), f.KeyCheckForced)
			return err
		}
	}
	// A row of another legal entity is another binding's; an untagged row is this binding's unless another binding
	// of the entity exists in this source system at all.
	rows, err := tx.Query(ctx, `insert into migration.sync_absent (tenant_id, binding_id, source_key, target_id, run_id)
		select $1, $2, x.source_key, x.target_id::text, nullif($4, '')::uuid
		from unnest($3::text[]) as k
		join migration.xref x on x.tenant_id = $1 and x.source_system = $5 and x.source_entity = $6 and x.source_key = k
		where $7 = '' or x.legal_entity_id::text = $7 or (x.legal_entity_id is null and not $8)
		on conflict (tenant_id, binding_id, source_key) do update set run_id = excluded.run_id, target_id = excluded.target_id, last_checked_at = now()
		returning source_key, (xmax = 0)`, tenantID, f.BindingID, keys, f.RunID, sourceSystem, f.Entity, le, sharedAny)
	if err != nil {
		return err
	}
	var kept, fresh []string
	for rows.Next() {
		var k string
		var inserted bool
		if err := rows.Scan(&k, &inserted); err != nil {
			rows.Close()
			return err
		}
		kept = append(kept, k)
		if inserted {
			fresh = append(fresh, k)
		}
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}
	if kept == nil {
		kept = []string{}
	}
	if _, err := tx.Exec(ctx, `delete from migration.sync_absent where tenant_id = $1 and binding_id = $2 and source_key not in (select unnest($3::text[]))`, tenantID, f.BindingID, kept); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `update migration.sync_state set keycheck_attempt_at = now(), keycheck_force = keycheck_force and not $4,
		keycheck_accept_large = keycheck_accept_large and not $5, last_keycheck_at = now(), keycheck_error = '', absent_count = $3
		where tenant_id = $1 and binding_id = $2`, tenantID, f.BindingID, len(kept), f.KeyCheckForced, f.KeyCheckAcceptLarge); err != nil {
		return err
	}
	if len(fresh) == 0 {
		return nil
	}
	slices.Sort(fresh)
	evt, err := events.New(migevents.SyncAbsentDetected, migevents.Source, f.BindingID, tenantID, absentData(f, len(kept), fresh))
	if err != nil {
		return err
	}
	payload, err := json.Marshal(evt)
	if err != nil {
		return err
	}
	_, err = tx.Exec(ctx, `insert into migration.outbox (id, tenant_id, aggregate_type, aggregate_id, type, payload) values ($1, $2, 'syncabsent', $3, $4, $5)`,
		evt.ID, tenantID, f.BindingID, evt.Type, payload)
	return err
}

// absentRefusal is why a completed full read cannot say which keys the legacy system deleted ("" when it can).
func absentRefusal(f SyncFinish, sharedAny, sharedSameLE bool) string {
	shared := sharedSameLE
	if !domain.XrefLegalEntityScoped(f.Entity) {
		shared = sharedAny
	}
	switch {
	case shared:
		return "another active binding loads " + f.Entity + " from the same source system into the same keys, so a key missing from this binding's read may be the other binding's; deleted rows are not listed"
	case f.Extracted == 0 && len(f.Absent) > 0:
		return "the full read returned no rows while keys are loaded; taken for a broken source rather than a deleted table, so the list was kept"
	}
	return ""
}

// vanishRefusal is keyCheckError for a full read that would list more than half of the loaded keys still present as
// newly absent. The web card recognises it by "newly absent (more than half)" (asserted in exerciseAbsent).
func vanishRefusal(newlyAbsent, present int) string {
	return fmt.Sprintf("the full read lists %d of the %d loaded keys not listed already as newly absent (more than half); taken for a broken source rather than deletions, so the list was kept. Confirm the source, then send keyCheckAcceptLarge to accept it once", newlyAbsent, present)
}

func truncateKeyCheckError(s string) string {
	if len(s) > 500 {
		return s[:500]
	}
	return s
}

func absentData(f SyncFinish, total int, fresh []string) migevents.SyncAbsentData {
	return migevents.SyncAbsentData{BindingID: f.BindingID, Entity: f.Entity, RunID: f.RunID, AbsentCount: total, NewlyAbsent: len(fresh),
		SampleKeys: slices.Clone(fresh[:min(len(fresh), migevents.MaxSampleKeys)])}
}

func (s *Postgres) ListAbsent(ctx context.Context, f AbsentFilter) ([]SyncAbsent, error) {
	p, _ := tenant.FromContext(ctx)
	if !ids.Valid(f.BindingID) {
		return nil, bindingNotFound()
	}
	var out []SyncAbsent
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		var exists bool
		if err := tx.QueryRow(ctx, `select exists (select 1 from migration.entity_binding where tenant_id = $1 and id = $2)`, p.TenantID, f.BindingID).Scan(&exists); err != nil {
			return err
		}
		if !exists {
			return bindingNotFound()
		}
		rows, err := tx.Query(ctx, `select binding_id::text, source_key, target_id, coalesce(run_id::text, ''), first_absent_at, last_checked_at
			from migration.sync_absent where tenant_id = $1 and binding_id = $2 and source_key > $3 order by source_key limit $4`, p.TenantID, f.BindingID, f.After, clampLimit(f.Limit))
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var a SyncAbsent
			if err := rows.Scan(&a.BindingID, &a.SourceKey, &a.TargetID, &a.RunID, &a.FirstAbsentAt, &a.LastCheckedAt); err != nil {
				return err
			}
			out = append(out, a)
		}
		return rows.Err()
	})
	return out, err
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
		rows, err := tx.Query(ctx, syncSelect+` where b.tenant_id = $1 and (s.binding_id is not null or sched.cron is not null)
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
		if e.ScheduleCron != nil && *e.ScheduleCron != "" {
			// The override sets when a run starts; the run itself still belongs to an active migrate project of the profile.
			var ok bool
			if err := tx.QueryRow(ctx, `select exists (select 1 from migration.entity_binding b join migration.project p on p.tenant_id = b.tenant_id
				and p.connection_profile_id = b.connection_profile_id and p.project_type = 'migrate' and p.status = 'active'
				where b.tenant_id = $1 and b.id = $2)`, p.TenantID, bindingID).Scan(&ok); err != nil {
				return err
			}
			if !ok {
				return httpx.NewProblem(http.StatusUnprocessableEntity, CodeSyncNoProject, "no active migrate project uses this binding's connection profile, so the binding has nothing to run under")
			}
		}
		if _, err := tx.Exec(ctx, `update migration.sync_state set paused = coalesce($3, paused), source_of_truth = coalesce($4, source_of_truth),
			keycheck_hours = coalesce($6::int, keycheck_hours), keycheck_force = keycheck_force or $7 or $8, keycheck_accept_large = keycheck_accept_large or $8,
			schedule_override = case when $9::text is null then schedule_override else nullif($9::text, '') end,
			next_due_at = case when $5 and schedule_cron is not null then now() else next_due_at end, version = version + 1, updated_at = now()
			where tenant_id = $1 and binding_id = $2`, p.TenantID, bindingID, e.Paused, e.SourceOfTruth, e.RunNow, e.KeyCheckHours, e.KeyCheckNow, e.KeyCheckAcceptLarge, e.ScheduleCron); err != nil {
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
	absent    []SyncAbsent
	// Events are the sync-run events FinishSync wrote, oldest first.
	events []events.Event
}

type memSyncRow struct {
	SyncState
	cron    string
	project string
	claim   string
	claimAt time.Time
	// keyCheckAttemptAt is keycheck_attempt_at: the last full read tried, failed or not.
	keyCheckAttemptAt *time.Time
	// keyCheckSince is keycheck_since: the anchor of the first key check.
	keyCheckSince time.Time
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

// memProjectFor is the project a binding runs under and its effective cron: the binding's override, else the cron of
// the lowest-id active migrate project with a schedule on its profile. A project without a schedule counts only when
// there is an override.
func (m *Mem) memProjectFor(b EntityBinding) (Project, string, bool) {
	override := ""
	sy := m.sy()
	sy.mu.Lock()
	if row := sy.state[b.ID]; row != nil {
		override = row.ScheduleOverride
	}
	sy.mu.Unlock()
	st := m.pj()
	st.mu.Lock()
	defer st.mu.Unlock()
	var out Project
	found := false
	better := func(p Project) bool {
		return !found || (p.ScheduleCron != "") && (out.ScheduleCron == "") || (p.ScheduleCron != "") == (out.ScheduleCron != "") && p.ID < out.ID
	}
	for _, p := range st.items {
		if p.ConnectionProfileID == b.ConnectionProfileID && p.Type == domain.ProjectMigrate && p.Status == domain.ProjectActive && (p.ScheduleCron != "" || override != "") && better(p) {
			out, found = p, true
		}
	}
	if override != "" {
		return out, override, found
	}
	return out, out.ScheduleCron, found
}

func (m *Mem) SyncCandidates(_ context.Context, max int) ([]SyncCandidate, error) {
	var out []SyncCandidate
	for _, b := range m.allBindings() {
		pr, cron, ok := m.memProjectFor(b)
		if !ok || b.Status != domain.BindingActive {
			continue
		}
		st := m.sy()
		st.mu.Lock()
		row := st.state[b.ID]
		need := row == nil || row.cron != cron || row.project != pr.ID ||
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
	x := SyncState{BindingID: b.ID, Entity: b.Entity, ConnectionProfileID: b.ConnectionProfileID, BindingStatus: b.Status, SourceOfTruth: domain.SourceLegacy, KeyCheckHours: domain.DefaultKeyCheckHours}
	if cp, err := m.GetConnectionProfile(context.Background(), b.ConnectionProfileID); err == nil {
		x.ProfileName = cp.Name
	}
	pr, cron, scheduled := m.memProjectFor(b)
	if row != nil {
		x = row.SyncState
		x.Entity, x.ConnectionProfileID, x.BindingStatus, x.Running = b.Entity, b.ConnectionProfileID, b.Status, row.claim != ""
		if cp, err := m.GetConnectionProfile(context.Background(), b.ConnectionProfileID); err == nil {
			x.ProfileName = cp.Name
		}
	}
	k := domain.KeyCheckState{BindingID: b.ID, Hours: x.KeyCheckHours, Since: time.Now()}
	if row != nil {
		k.LastAttempt, k.Force, k.Since = row.keyCheckAttemptAt, row.KeyCheckForce, row.keyCheckSince
	}
	x.KeyCheckDue = k.Due(time.Now())
	x.ScheduleCron, x.ProjectScheduleCron = "", ""
	if scheduled {
		x.ScheduleCron, x.ProjectScheduleCron = cron, pr.ScheduleCron
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
	pr, cron, ok := m.memProjectFor(b)
	if !ok {
		return nil
	}
	due, ok := next(cron)
	if !ok {
		return fmt.Errorf("schedule %q of project %s has no next firing", cron, pr.ID)
	}
	st := m.sy()
	st.mu.Lock()
	defer st.mu.Unlock()
	row := st.state[bindingID]
	if row == nil {
		now := time.Now()
		st.state[bindingID] = &memSyncRow{SyncState: SyncState{BindingID: bindingID, SourceOfTruth: domain.SourceLegacy, NextDueAt: &now, ProjectID: pr.ID, KeyCheckHours: domain.DefaultKeyCheckHours}, cron: cron, project: pr.ID, keyCheckSince: now}
		return nil
	}
	if row.cron != cron || row.project != pr.ID {
		row.cron, row.project, row.ProjectID, row.NextDueAt = cron, pr.ID, pr.ID, &due
	}
	return nil
}

func (m *Mem) ClaimSync(_ context.Context, bindingID, claimID string) (SyncState, bool, error) {
	b, err := m.GetEntityBinding(context.Background(), bindingID)
	if err != nil {
		return SyncState{}, false, err
	}
	_, cron, scheduled := m.memProjectFor(b)
	st := m.sy()
	st.mu.Lock()
	row := st.state[bindingID]
	if row == nil || !scheduled || b.Status != domain.BindingActive || row.cron != cron || row.Paused || row.NextDueAt == nil || row.NextDueAt.After(time.Now()) ||
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

func (m *Mem) FinishSync(ctx context.Context, f SyncFinish) error {
	// Read the projects and bindings before taking the sync lock (view takes them in the other order).
	scope := m.absentScope(ctx, f)
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
	if f.Cron != "" && row.cron != f.Cron && row.NextDueAt != nil {
		next = *row.NextDueAt // re-seeded from a schedule changed during the run
	}
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
	switch {
	case f.FullRead && !failed:
		if err := m.recordAbsentLocked(st, row, f, scope, now); err != nil {
			return err
		}
	case f.KeyCheckAttempted || f.FullRead:
		row.keyCheckAttemptAt, row.KeyCheckError = &now, truncateKeyCheckError("the full read did not complete: "+f.Error)
		row.KeyCheckForce = row.KeyCheckForce && !f.KeyCheckForced
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

// memAbsentScope is what recordAbsent reads from the project and the other bindings, for the in-memory store.
type memAbsentScope struct {
	sourceSystem, legalEntity string
	sharedAny, sharedSameLE   bool
}

func (m *Mem) absentScope(ctx context.Context, f SyncFinish) memAbsentScope {
	var out memAbsentScope
	b, err := m.GetEntityBinding(ctx, f.BindingID)
	if err != nil {
		return out
	}
	pr, _, ok := m.memProjectFor(b)
	if !ok {
		return out
	}
	out.sourceSystem, out.legalEntity = pr.SourceSystem, pr.TargetLegalEntityID
	pj := m.pj()
	pj.mu.Lock()
	projects := slices.Clone(pj.items)
	pj.mu.Unlock()
	for _, b2 := range m.allBindings() {
		if b2.ID == b.ID || b2.Entity != b.Entity || b2.Status != domain.BindingActive {
			continue
		}
		for _, p2 := range projects {
			if p2.ConnectionProfileID != b2.ConnectionProfileID || p2.Type != domain.ProjectMigrate || p2.Status != domain.ProjectActive || p2.SourceSystem != pr.SourceSystem {
				continue
			}
			out.sharedAny = true
			if pr.TargetLegalEntityID == "" || p2.TargetLegalEntityID == "" || p2.TargetLegalEntityID == pr.TargetLegalEntityID {
				out.sharedSameLE = true
			}
		}
	}
	return out
}

// recordAbsentLocked is recordAbsent for the in-memory store; st.mu is held.
func (m *Mem) recordAbsentLocked(st *memSync, row *memSyncRow, f SyncFinish, scope memAbsentScope, now time.Time) error {
	row.keyCheckAttemptAt = &now
	row.KeyCheckForce = row.KeyCheckForce && !f.KeyCheckForced
	if reason := absentRefusal(f, scope.sharedAny, scope.sharedSameLE); reason != "" {
		row.KeyCheckError = reason
		return nil
	}
	old := map[string]SyncAbsent{}
	var kept []SyncAbsent
	for _, a := range st.absent {
		if a.BindingID == f.BindingID {
			old[a.SourceKey] = a
		} else {
			kept = append(kept, a)
		}
	}
	inScope := func(x XrefRow) bool {
		return scope.legalEntity == "" || x.LegalEntityID == scope.legalEntity || (x.LegalEntityID == "" && !scope.sharedAny)
	}
	if !f.KeyCheckAcceptLarge {
		present, newly := 0, 0
		m.mu.Lock()
		for k, e := range m.xref.Entries {
			if _, listed := old[k.SourceKey]; !listed && k.SourceSystem == scope.sourceSystem && k.Entity == f.Entity && inScope(XrefRow{LegalEntityID: e.LegalEntityID}) {
				present++
			}
		}
		m.mu.Unlock()
		for _, k := range f.Absent {
			x, found, _ := m.Xref().Get(context.Background(), domain.XrefKey{SourceSystem: scope.sourceSystem, Entity: f.Entity, SourceKey: k})
			if _, listed := old[k]; found && !listed && inScope(XrefRow{LegalEntityID: x.LegalEntityID}) {
				newly++
			}
		}
		if domain.KeyCheckVanishTooLarge(newly, present) {
			row.KeyCheckError = vanishRefusal(newly, present) // st.absent is untouched: the list stays
			return nil
		}
	}
	var fresh []string
	total := 0
	for _, k := range f.Absent {
		x, found, _ := m.Xref().Get(context.Background(), domain.XrefKey{SourceSystem: scope.sourceSystem, Entity: f.Entity, SourceKey: k})
		if !found || !(scope.legalEntity == "" || x.LegalEntityID == scope.legalEntity || (x.LegalEntityID == "" && !scope.sharedAny)) {
			continue
		}
		a, ok := old[k]
		if !ok {
			a = SyncAbsent{BindingID: f.BindingID, SourceKey: k, FirstAbsentAt: now}
			fresh = append(fresh, k)
		}
		a.TargetID, a.RunID, a.LastCheckedAt = x.TargetID, f.RunID, now
		kept = append(kept, a)
		total++
	}
	st.absent = kept
	row.LastKeyCheckAt, row.KeyCheckError, row.AbsentCount = &now, "", total
	row.KeyCheckAcceptLarge = row.KeyCheckAcceptLarge && !f.KeyCheckAcceptLarge
	if len(fresh) == 0 {
		return nil
	}
	slices.Sort(fresh)
	evt, err := events.New(migevents.SyncAbsentDetected, migevents.Source, f.BindingID, MemTenant, absentData(f, total, fresh))
	if err != nil {
		return err
	}
	st.events = append(st.events, evt)
	return nil
}

func (m *Mem) ListAbsent(ctx context.Context, f AbsentFilter) ([]SyncAbsent, error) {
	if _, err := m.GetEntityBinding(ctx, f.BindingID); err != nil {
		return nil, err
	}
	st := m.sy()
	st.mu.Lock()
	defer st.mu.Unlock()
	var out []SyncAbsent
	for _, a := range st.absent {
		if a.BindingID == f.BindingID && a.SourceKey > f.After {
			out = append(out, a)
		}
	}
	slices.SortFunc(out, func(a, b SyncAbsent) int { return strings.Compare(a.SourceKey, b.SourceKey) })
	return out[:min(len(out), clampLimit(f.Limit))], nil
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
		_, _, scheduled := m.memProjectFor(b)
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
		row = &memSyncRow{SyncState: SyncState{BindingID: bindingID, SourceOfTruth: domain.SourceLegacy, KeyCheckHours: domain.DefaultKeyCheckHours}, keyCheckSince: time.Now()}
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
	if e.ScheduleCron != nil && *e.ScheduleCron != "" {
		ok := false
		pj := m.pj()
		pj.mu.Lock()
		for _, p := range pj.items {
			ok = ok || p.ConnectionProfileID == b.ConnectionProfileID && p.Type == domain.ProjectMigrate && p.Status == domain.ProjectActive
		}
		pj.mu.Unlock()
		if !ok {
			st.mu.Unlock()
			return SyncState{}, httpx.NewProblem(http.StatusUnprocessableEntity, CodeSyncNoProject, "no active migrate project uses this binding's connection profile, so the binding has nothing to run under")
		}
	}
	if e.ScheduleCron != nil {
		row.ScheduleOverride = *e.ScheduleCron
	}
	if e.Paused != nil {
		row.Paused = *e.Paused
	}
	if e.SourceOfTruth != nil {
		row.SourceOfTruth = *e.SourceOfTruth
	}
	if e.KeyCheckHours != nil {
		row.KeyCheckHours = *e.KeyCheckHours
	}
	if e.KeyCheckNow || e.KeyCheckAcceptLarge {
		row.KeyCheckForce = true
	}
	if e.KeyCheckAcceptLarge {
		row.KeyCheckAcceptLarge = true
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
