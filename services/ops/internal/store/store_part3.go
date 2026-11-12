package store

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/BenjaminSRussell/passage/platform/events"
	"github.com/BenjaminSRussell/passage/platform/outbox"
	"github.com/BenjaminSRussell/passage/services/ops/internal/domain"
)

// Ops events (part 3). Each is written to the outbox in the transaction of the change it reports; Debezium relays it.
const (
	EventSource           = "/passage/ops/ops"
	EventFailoverComplete = "dev.passage.ops.failover.completed.v1"
	EventTenantProvision  = "dev.passage.ops.tenant.provisioned.v1"
	EventSLOBreached      = "dev.passage.ops.slo.breached.v1"
	EventLicenseChanged   = "dev.passage.ops.license.changed.v1"
)

// WithOperator sets the platform operator tenant, the tenant of the platform-wide events (failover completed, SLO breached).
func (s *Postgres) WithOperator(tenantID string) *Postgres {
	s.operator = tenantID
	return s
}

// enqueue writes evt in tx. The outbox has the platform tenant RLS, so the insert runs with the event's tenant set for the
// rest of this transaction only.
func enqueue(ctx context.Context, tx pgx.Tx, aggregateType, aggregateID, typ, tenantID, actor string, data any) error {
	evt, err := events.New(typ, EventSource, aggregateID, tenantID, data)
	if err != nil {
		return err
	}
	evt.Actor = actor
	if _, err := tx.Exec(ctx, `select set_config('passage.tenant_id', $1, true)`, tenantID); err != nil {
		return err
	}
	return outbox.Enqueue(ctx, tx, aggregateType, aggregateID, evt)
}

// FailoverCompleted is the payload of failover.completed: the plan that ran and when its last step finished.
type FailoverCompleted struct {
	EventID        string    `json:"eventId"`
	From           string    `json:"from"`
	To             string    `json:"to"`
	AcceptDataLoss bool      `json:"acceptDataLoss"`
	LagSeconds     int       `json:"lagSeconds"`
	Steps          int       `json:"steps"`
	CompletedAt    time.Time `json:"completedAt"`
}

// completeFailover marks the failover complete and writes failover.completed once, when every step of its plan is DONE.
func (s *Postgres) completeFailover(ctx context.Context, tx pgx.Tx, eventID, actor string) error {
	if s.operator == "" {
		return nil
	}
	rec, found, err := failoverByEvent(ctx, tx, eventID)
	if err != nil || !found {
		return err
	}
	var done int
	if err := tx.QueryRow(ctx, `select count(*) from ops_failover_step where event_id = $1 and status = 'DONE'`, eventID).Scan(&done); err != nil {
		return err
	}
	if len(rec.Plan.Steps) == 0 || done < len(rec.Plan.Steps) {
		return nil
	}
	var at time.Time
	err = tx.QueryRow(ctx, `update ops_failover set completed_at = now() where event_id = $1 and completed_at is null returning completed_at`, eventID).Scan(&at)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil // already reported
	}
	if err != nil {
		return err
	}
	return enqueue(ctx, tx, "failover", eventID, EventFailoverComplete, s.operator, actor, FailoverCompleted{
		EventID: eventID, From: rec.Plan.From, To: rec.Plan.To, AcceptDataLoss: rec.Plan.AcceptDataLoss, LagSeconds: rec.Plan.LagSeconds,
		Steps: len(rec.Plan.Steps), CompletedAt: at.UTC(),
	})
}

// ---- Tenant provisioning ----

// ProvisionStep is one reported provisioning step.
type ProvisionStep struct {
	Order      int       `json:"order"`
	Action     string    `json:"action"`
	Status     string    `json:"status"`
	Detail     string    `json:"detail,omitempty"`
	ReportedBy string    `json:"reportedBy"`
	RecordedAt time.Time `json:"recordedAt"`
}

// Provision is a tenant provision with its plan and the steps reported so far.
type Provision struct {
	domain.ProvisionRequest
	Status        string          `json:"status"`
	Plan          []string        `json:"plan"`
	Steps         []ProvisionStep `json:"steps"`
	RequestedBy   string          `json:"requestedBy"`
	CreatedAt     time.Time       `json:"createdAt"`
	ProvisionedAt *time.Time      `json:"provisionedAt,omitempty"`
	// Overdue is true while the provision runs past the 15-minute target (provisioning.timeout_min).
	Overdue bool `json:"overdue"`
}

// TenantProvisioned is the payload of tenant.provisioned (tenant-provisioning.md step 7).
type TenantProvisioned struct {
	TenantID      string    `json:"tenantId"`
	Name          string    `json:"name"`
	Residency     string    `json:"residency"`
	Region        string    `json:"region"`
	Tier          string    `json:"tier"`
	Realm         string    `json:"realm"`
	ProvisionedAt time.Time `json:"provisionedAt"`
}

func requestHash(req domain.ProvisionRequest) string {
	b, _ := json.Marshal(req)
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

// CreateProvision records a validated provision. The same Idempotency-Key with the same request returns the first record
// (replay true); the same key with another request, or a tenant already provisioned under another key, is OPS-1723.
func (s *Postgres) CreateProvision(ctx context.Context, key string, req domain.ProvisionRequest, user string) (Provision, bool, error) {
	hash := requestHash(req)
	var replay bool
	err := pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		var existingKey, existingHash, existingTenant string
		err := tx.QueryRow(ctx, `select idempotency_key, request_hash, customer_tenant::text from ops_tenant_provision
			where idempotency_key = $1 or customer_tenant = $2::uuid limit 1`, key, req.TenantID).Scan(&existingKey, &existingHash, &existingTenant)
		switch {
		case err == nil && existingKey == key && existingHash == hash:
			replay = true
			return nil
		case err == nil && existingKey == key:
			return &domain.Error{Code: domain.CodeProvisionConflict, Detail: "this Idempotency-Key was used for a different provision"}
		case err == nil:
			return &domain.Error{Code: domain.CodeProvisionConflict, Detail: "tenant " + existingTenant + " already has a provision; read it with GET /v1/ops/tenants/{id}"}
		case !errors.Is(err, pgx.ErrNoRows):
			return err
		}
		_, err = tx.Exec(ctx, `insert into ops_tenant_provision (customer_tenant, name, residency, region, tier, idempotency_key, request_hash, requested_by)
			values ($1::uuid, $2, $3, $4, $5, $6, $7, $8)`, req.TenantID, req.Name, req.Residency, req.Region, req.Tier, key, hash, user)
		return err
	})
	if err != nil {
		return Provision{}, false, Problem(err)
	}
	p, err := s.Provision(ctx, req.TenantID)
	return p, replay, err
}

const provisionCols = `customer_tenant::text, name, residency, region, tier, status, requested_by, created_at, provisioned_at`

func scanProvision(row pgx.Row) (Provision, error) {
	var p Provision
	err := row.Scan(&p.TenantID, &p.Name, &p.Residency, &p.Region, &p.Tier, &p.Status, &p.RequestedBy, &p.CreatedAt, &p.ProvisionedAt)
	p.Plan = domain.ProvisionSteps
	p.Steps = []ProvisionStep{}
	p.Overdue = p.Status == domain.ProvisionRunning && time.Since(p.CreatedAt) > domain.DefaultProvisionT
	return p, err
}

func provisionSteps(ctx context.Context, q queryer, tenantID string) ([]ProvisionStep, error) {
	rows, err := q.Query(ctx, `select ord, action, status, detail, reported_by, recorded_at from ops_tenant_provision_step
		where customer_tenant = $1::uuid order by ord`, tenantID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []ProvisionStep{}
	for rows.Next() {
		var st ProvisionStep
		if err := rows.Scan(&st.Order, &st.Action, &st.Status, &st.Detail, &st.ReportedBy, &st.RecordedAt); err != nil {
			return nil, err
		}
		out = append(out, st)
	}
	return out, rows.Err()
}

// Provision reads one provision with its steps.
func (s *Postgres) Provision(ctx context.Context, tenantID string) (Provision, error) {
	p, err := scanProvision(s.pool.QueryRow(ctx, `select `+provisionCols+` from ops_tenant_provision where customer_tenant = $1::uuid`, tenantID))
	if errors.Is(err, pgx.ErrNoRows) {
		return p, Problem(&domain.Error{Code: domain.CodeProvisionUnknown, Detail: "no provision is recorded for tenant " + tenantID})
	}
	if err != nil {
		return p, err
	}
	p.Steps, err = provisionSteps(ctx, s.pool, tenantID)
	return p, err
}

// Provisions lists the provisions, newest first (at most 500).
func (s *Postgres) Provisions(ctx context.Context) ([]Provision, error) {
	rows, err := s.pool.Query(ctx, `select `+provisionCols+` from ops_tenant_provision order by created_at desc limit 500`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Provision{}
	for rows.Next() {
		p, err := scanProvision(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

// RecordProvisionStep stores one step result. A DONE step is kept; a FAILED step is replaced by a later result. The license step
// is DONE only once a licence is installed for the tenant (OPS-1724). When the last step turns DONE the provision is PROVISIONED
// and tenant.provisioned is written in the same transaction.
func (s *Postgres) RecordProvisionStep(ctx context.Context, tenantID, action, status, detail, user string) (Provision, error) {
	order := 0
	for i, a := range domain.ProvisionSteps {
		if a == action {
			order = i + 1
		}
	}
	err := pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		p, err := scanProvision(tx.QueryRow(ctx, `select `+provisionCols+` from ops_tenant_provision where customer_tenant = $1::uuid for update`, tenantID))
		if errors.Is(err, pgx.ErrNoRows) {
			return &domain.Error{Code: domain.CodeProvisionUnknown, Detail: "no provision is recorded for tenant " + tenantID}
		}
		if err != nil {
			return err
		}
		if action == "license" && status == "DONE" {
			var n int
			if err := tx.QueryRow(ctx, `select count(*) from ops_tenant_license where licensee_id = $1::uuid`, tenantID).Scan(&n); err != nil {
				return err
			}
			if n == 0 {
				return &domain.Error{Code: domain.CodeProvisionLicense, Detail: "install the tenant's signed licence (PUT /v1/ops/tenants/{id}/license) before reporting the license step"}
			}
		}
		if _, err := tx.Exec(ctx, `insert into ops_tenant_provision_step (customer_tenant, action, ord, status, detail, reported_by)
			values ($1::uuid, $2, $3, $4, $5, $6)
			on conflict (customer_tenant, action) do update set status = excluded.status, detail = excluded.detail, reported_by = excluded.reported_by, recorded_at = now()
			where ops_tenant_provision_step.status <> 'DONE'`, tenantID, action, order, status, detail, user); err != nil {
			return err
		}
		if status == "FAILED" {
			_, err := tx.Exec(ctx, `update ops_tenant_provision set status = 'FAILED' where customer_tenant = $1::uuid and status = 'PROVISIONING'`, tenantID)
			return err
		}
		var done int
		if err := tx.QueryRow(ctx, `select count(*) from ops_tenant_provision_step where customer_tenant = $1::uuid and status = 'DONE'`, tenantID).Scan(&done); err != nil {
			return err
		}
		if done < len(domain.ProvisionSteps) {
			// A retried step after a failure puts the provision back to running.
			_, err := tx.Exec(ctx, `update ops_tenant_provision set status = 'PROVISIONING' where customer_tenant = $1::uuid and status = 'FAILED'
				and not exists (select 1 from ops_tenant_provision_step where customer_tenant = $1::uuid and status = 'FAILED')`, tenantID)
			return err
		}
		var at time.Time
		err = tx.QueryRow(ctx, `update ops_tenant_provision set status = 'PROVISIONED', provisioned_at = now() where customer_tenant = $1::uuid
			and status <> 'PROVISIONED' returning provisioned_at`, tenantID).Scan(&at)
		if errors.Is(err, pgx.ErrNoRows) {
			return nil
		}
		if err != nil {
			return err
		}
		return enqueue(ctx, tx, "tenant", tenantID, EventTenantProvision, tenantID, user, TenantProvisioned{
			TenantID: tenantID, Name: p.Name, Residency: p.Residency, Region: p.Region, Tier: p.Tier, Realm: "passage-" + tenantID, ProvisionedAt: at.UTC(),
		})
	})
	if err != nil {
		return Provision{}, Problem(err)
	}
	return s.Provision(ctx, tenantID)
}

// ---- Tenant cost statements ----

// CostRecord is a stored statement.
type CostRecord struct {
	domain.CostStatement
	RecordedBy string    `json:"recordedBy"`
	RecordedAt time.Time `json:"recordedAt"`
}

// RecordCost stores (or restates) a tenant's month.
func (s *Postgres) RecordCost(ctx context.Context, st domain.CostStatement, user string) (CostRecord, error) {
	b, err := json.Marshal(st)
	if err != nil {
		return CostRecord{}, err
	}
	_, err = s.pool.Exec(ctx, `insert into ops_tenant_cost (customer_tenant, month, statement, cost_usd, cost_eur, model_version, recorded_by)
		values ($1::uuid, $2, $3, $4::numeric, $5::numeric, $6, $7)
		on conflict (customer_tenant, month) do update set statement = excluded.statement, cost_usd = excluded.cost_usd, cost_eur = excluded.cost_eur,
		  model_version = excluded.model_version, recorded_by = excluded.recorded_by, recorded_at = now()`,
		st.TenantID, st.Month, b, st.CostUSD, st.CostEUR, st.ModelVersion, user)
	if err != nil {
		return CostRecord{}, err
	}
	return s.Cost(ctx, st.TenantID, st.Month)
}

func scanCost(row pgx.Row) (CostRecord, error) {
	var r CostRecord
	var raw []byte
	if err := row.Scan(&raw, &r.RecordedBy, &r.RecordedAt); err != nil {
		return r, err
	}
	return r, json.Unmarshal(raw, &r.CostStatement)
}

// Cost reads one statement.
func (s *Postgres) Cost(ctx context.Context, tenantID, month string) (CostRecord, error) {
	r, err := scanCost(s.pool.QueryRow(ctx, `select statement, recorded_by, recorded_at from ops_tenant_cost where customer_tenant = $1::uuid and month = $2`, tenantID, month))
	if errors.Is(err, pgx.ErrNoRows) {
		return r, Problem(&domain.Error{Code: domain.CodeCostUnknown, Detail: "no cost statement is recorded for this tenant and month"})
	}
	return r, err
}

// Costs lists every tenant's statement for a month, by tenant, and the cluster total in USD (the figure reconciled with the
// provider invoice, AC-50.7).
func (s *Postgres) Costs(ctx context.Context, month string) ([]CostRecord, string, error) {
	rows, err := s.pool.Query(ctx, `select statement, recorded_by, recorded_at from ops_tenant_cost where month = $1 order by customer_tenant`, month)
	if err != nil {
		return nil, "", err
	}
	defer rows.Close()
	out := []CostRecord{}
	for rows.Next() {
		r, err := scanCost(rows)
		if err != nil {
			return nil, "", err
		}
		out = append(out, r)
	}
	if err := rows.Err(); err != nil {
		return nil, "", err
	}
	var total string
	err = s.pool.QueryRow(ctx, `select to_char(coalesce(sum(cost_usd), 0), 'FM999999999999990.00') from ops_tenant_cost where month = $1`, month).Scan(&total)
	return out, total, err
}

// ---- Licence user counts and the hourly check ----

// LicenseUsage is the last reported count of active users of a tenant.
type LicenseUsage struct {
	ActiveFull        int        `json:"activeFull"`
	ActiveOperational int        `json:"activeOperational"`
	ActiveSelfService int        `json:"activeSelfservice"`
	CountedAt         *time.Time `json:"countedAt,omitempty"`
	ReportedBy        string     `json:"reportedBy,omitempty"`
	LastState         string     `json:"lastState,omitempty"`
	CheckedAt         *time.Time `json:"checkedAt,omitempty"`
}

// LicenseChanged is the payload of license.changed: a new licence was installed, or the hourly check found the state moved.
type LicenseChanged struct {
	TenantID      string   `json:"tenantId"`
	Reason        string   `json:"reason"` // installed or state_changed
	State         string   `json:"state"`
	PreviousState string   `json:"previousState,omitempty"`
	ExpiresAt     string   `json:"expiresAt"`
	DaysToExpiry  int      `json:"daysToExpiry"`
	FullUsers     int      `json:"fullUsers"`
	ActiveFull    int      `json:"activeFull"`
	Codes         []string `json:"codes"`
	Version       int64    `json:"version"`
}

// ReportLicenseUsage stores a tenant's active user counts (the reporter's hourly count).
func (s *Postgres) ReportLicenseUsage(ctx context.Context, tenantID string, u LicenseUsage, user string) (LicenseUsage, error) {
	_, err := s.pool.Exec(ctx, `insert into ops_license_check (licensee_id, active_full, active_operational, active_selfservice, counted_at, reported_by)
		values ($1::uuid, $2, $3, $4, now(), $5)
		on conflict (licensee_id) do update set active_full = excluded.active_full, active_operational = excluded.active_operational,
		  active_selfservice = excluded.active_selfservice, counted_at = excluded.counted_at, reported_by = excluded.reported_by`,
		tenantID, u.ActiveFull, u.ActiveOperational, u.ActiveSelfService, user)
	if err != nil {
		return LicenseUsage{}, err
	}
	return s.LicenseUsage(ctx, tenantID)
}

// LicenseUsage reads a tenant's last counts and checked state; a tenant never counted has none (zero counts).
func (s *Postgres) LicenseUsage(ctx context.Context, tenantID string) (LicenseUsage, error) {
	var u LicenseUsage
	var full, op, self *int
	var by, state *string
	err := s.pool.QueryRow(ctx, `select active_full, active_operational, active_selfservice, counted_at, reported_by, last_state, checked_at
		from ops_license_check where licensee_id = $1::uuid`, tenantID).Scan(&full, &op, &self, &u.CountedAt, &by, &state, &u.CheckedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return u, nil
	}
	if err != nil {
		return u, err
	}
	u.ActiveFull, u.ActiveOperational, u.ActiveSelfService = deref(full), deref(op), deref(self)
	if by != nil {
		u.ReportedBy = *by
	}
	if state != nil {
		u.LastState = *state
	}
	return u, nil
}

func deref(p *int) int {
	if p == nil {
		return 0
	}
	return *p
}

// CheckLicenses is the hourly licence check: it evaluates every installed licence with the tenant's last reported full-user
// count on day now, stores the state, and writes license.changed for each licence whose state differs from the last check.
// It returns the number of changes.
func (s *Postgres) CheckLicenses(ctx context.Context, now time.Time) (int, error) {
	list, err := s.Licenses(ctx)
	if err != nil {
		return 0, err
	}
	changed := 0
	for _, rec := range list {
		err := pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
			var full *int
			var last *string
			err := tx.QueryRow(ctx, `select active_full, last_state from ops_license_check where licensee_id = $1::uuid for update`, rec.License.TenantID).Scan(&full, &last)
			if err != nil && !errors.Is(err, pgx.ErrNoRows) {
				return err
			}
			st, err := domain.EvaluateLicense(rec.License, deref(full), now)
			if err != nil {
				return err
			}
			if _, err := tx.Exec(ctx, `insert into ops_license_check (licensee_id, last_state, checked_at) values ($1::uuid, $2, $3)
				on conflict (licensee_id) do update set last_state = excluded.last_state, checked_at = excluded.checked_at`,
				rec.License.TenantID, string(st.State), now); err != nil {
				return err
			}
			if last != nil && *last == string(st.State) {
				return nil
			}
			changed++
			prev := ""
			if last != nil {
				prev = *last
			}
			return enqueue(ctx, tx, "license", rec.License.TenantID, EventLicenseChanged, rec.License.TenantID, "system:license-check", LicenseChanged{
				TenantID: rec.License.TenantID, Reason: "state_changed", State: string(st.State), PreviousState: prev, ExpiresAt: rec.License.ExpiresAt,
				DaysToExpiry: st.DaysToExpiry, FullUsers: rec.License.FullUsers, ActiveFull: deref(full), Codes: st.Codes, Version: rec.Version,
			})
		})
		if err != nil {
			return changed, err
		}
	}
	return changed, nil
}

// licenseInstalled writes license.changed for an installed licence (in the install transaction) and records its state, so the
// next hourly check reports only a later change.
func licenseInstalled(ctx context.Context, tx pgx.Tx, rec LicenseRecord, user string, now time.Time) error {
	var full *int
	err := tx.QueryRow(ctx, `select active_full from ops_license_check where licensee_id = $1::uuid`, rec.License.TenantID).Scan(&full)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return err
	}
	st, err := domain.EvaluateLicense(rec.License, deref(full), now)
	if err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `insert into ops_license_check (licensee_id, last_state, checked_at) values ($1::uuid, $2, $3)
		on conflict (licensee_id) do update set last_state = excluded.last_state, checked_at = excluded.checked_at`,
		rec.License.TenantID, string(st.State), now); err != nil {
		return err
	}
	return enqueue(ctx, tx, "license", rec.License.TenantID, EventLicenseChanged, rec.License.TenantID, user, LicenseChanged{
		TenantID: rec.License.TenantID, Reason: "installed", State: string(st.State), ExpiresAt: rec.License.ExpiresAt, DaysToExpiry: st.DaysToExpiry,
		FullUsers: rec.License.FullUsers, ActiveFull: deref(full), Codes: st.Codes, Version: rec.Version,
	})
}

// ---- SLO breach state ----

// SLOBreached is the payload of slo.breached.
type SLOBreached struct {
	Service            string   `json:"service"`
	Window             string   `json:"window"`
	Availability       *float64 `json:"availability"`
	AvailabilityTarget float64  `json:"availabilityTarget"`
	LatencyP95Ms       *float64 `json:"latencyP95Ms"`
	LatencyTargetMs    float64  `json:"latencyTargetMs"`
	BreachedAt         string   `json:"breachedAt"`
}

// RecordSLO stores the breach state of each service in rep and writes slo.breached for each service that turned breached since
// the last check (a service that stays breached is reported once, and again only after it recovered). It fills BreachedSince
// on the report lines and returns the number of new breaches.
func (s *Postgres) RecordSLO(ctx context.Context, rep *domain.SLOReport) (int, error) {
	opened := 0
	err := pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		for i := range rep.Services {
			line := &rep.Services[i]
			var since *time.Time
			var was bool
			err := tx.QueryRow(ctx, `select breached, breached_since from ops_slo_state where service = $1 for update`, line.Service).Scan(&was, &since)
			if err != nil && !errors.Is(err, pgx.ErrNoRows) {
				return err
			}
			if line.Breached && !was {
				t := rep.GeneratedAt
				since = &t
				opened++
				if s.operator != "" {
					if err := enqueue(ctx, tx, "slo", line.Service, EventSLOBreached, s.operator, "system:slo-check", SLOBreached{
						Service: line.Service, Window: rep.Window, Availability: line.Availability, AvailabilityTarget: line.AvailabilityTarget,
						LatencyP95Ms: line.LatencyP95Ms, LatencyTargetMs: line.LatencyTargetMs, BreachedAt: t.Format(time.RFC3339),
					}); err != nil {
						return err
					}
				}
			}
			if !line.Breached {
				since = nil
			}
			if _, err := tx.Exec(ctx, `insert into ops_slo_state (service, breached, breached_since, checked_at, last_window) values ($1, $2, $3, $4, $5)
				on conflict (service) do update set breached = excluded.breached, breached_since = excluded.breached_since, checked_at = excluded.checked_at,
				  last_window = excluded.last_window`, line.Service, line.Breached, since, rep.GeneratedAt, rep.Window); err != nil {
				return err
			}
			if since != nil {
				v := since.UTC().Format(time.RFC3339)
				line.BreachedSince = &v
			}
		}
		return nil
	})
	return opened, err
}

// SLOStates returns when each currently breached service turned breached, for a report read that does not record.
func (s *Postgres) SLOStates(ctx context.Context) (map[string]time.Time, error) {
	rows, err := s.pool.Query(ctx, `select service, breached_since from ops_slo_state where breached and breached_since is not null`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]time.Time{}
	for rows.Next() {
		var svc string
		var at time.Time
		if err := rows.Scan(&svc, &at); err != nil {
			return nil, err
		}
		out[svc] = at
	}
	return out, rows.Err()
}
