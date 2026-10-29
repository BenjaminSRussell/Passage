// Package store persists SoD conflicts and mitigations in Postgres. Every
// method runs in a tenant-scoped transaction (dbx.WithTenantTx) so row-level
// security applies, and every state change other contexts care about is
// written to the outbox in the same transaction.
package store

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"sort"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/BenjaminSRussell/passage/platform/dbx"
	"github.com/BenjaminSRussell/passage/platform/events"
	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/platform/outbox"
	"github.com/BenjaminSRussell/passage/platform/tenant"
	"github.com/BenjaminSRussell/passage/services/compliance/internal/domain"
	cevents "github.com/BenjaminSRussell/passage/services/compliance/internal/events"
)

// Stable error codes raised by the store (listed in the service README).
const (
	CodeNotFound         = "SOD-1108"
	CodeVersionConflict  = "SOD-1109"
	CodeIdempotencyReuse = "SOD-1110"
	CodeUnknownRule      = "SOD-1111"

	eventSource   = "compliance"
	aggregateType = "sod_conflict"

	// MaxPageSize caps one conflict listing page.
	MaxPageSize = 200
)

// RoleGrants records which roles grant each side of a conflict.
type RoleGrants struct {
	A []string `json:"a"`
	B []string `json:"b"`
}

// Conflict is a stored conflict with its identity, version and audit fields.
type Conflict struct {
	ConflictID     string     `json:"conflictId"`
	UserID         string     `json:"userId"`
	RuleID         string     `json:"ruleId"`
	RiskID         string     `json:"riskId"`
	Severity       string     `json:"severity"`
	DutyA          string     `json:"dutyA"`
	DutyB          string     `json:"dutyB"`
	RolesA         []string   `json:"rolesA"`
	RolesB         []string   `json:"rolesB"`
	State          string     `json:"state"`
	MitigationID   string     `json:"mitigationId,omitempty"`
	ExpiresAt      *time.Time `json:"expiresAt,omitempty"`
	DecidedBy      string     `json:"decidedBy,omitempty"`
	DecisionReason string     `json:"decisionReason,omitempty"`
	DetectedAt     time.Time  `json:"detectedAt"`
	ResolvedAt     *time.Time `json:"resolvedAt,omitempty"`
	UpdatedAt      time.Time  `json:"updatedAt"`
	Version        int        `json:"version"`
}

// Mitigation is one approved compensating control (append-only).
type Mitigation struct {
	MitigationID string    `json:"mitigationId"`
	ConflictID   string    `json:"conflictId"`
	ControlRef   string    `json:"controlRef"`
	Reason       string    `json:"reason"`
	ApproverID   string    `json:"approverId"`
	ApprovedAt   time.Time `json:"approvedAt"`
	ExpiresAt    time.Time `json:"expiresAt"`
}

// ConflictDetail is a conflict with its mitigation history, newest first.
type ConflictDetail struct {
	Conflict
	Mitigations []Mitigation `json:"mitigations"`
}

// Filter narrows a conflict listing. Empty fields do not filter.
type Filter struct {
	State    string
	Severity string
	UserID   string
	RuleID   string
	Limit    int
	Cursor   string
}

// Page is one page of conflicts, newest detection first.
type Page struct {
	Items      []Conflict `json:"items"`
	NextCursor string     `json:"nextCursor,omitempty"`
}

// MitigationInput is the body of a mitigation approval.
type MitigationInput struct {
	ControlRef string    `json:"controlRef"`
	Reason     string    `json:"reason"`
	ExpiresAt  time.Time `json:"expiresAt"`
}

// Postgres is the RLS-aware repository.
type Postgres struct {
	pool  *pgxpool.Pool
	rules func() []domain.Rule
}

// New builds the repository over pool, evaluating the FR-48.4 seed rules.
func New(pool *pgxpool.Pool) *Postgres {
	return &Postgres{pool: pool, rules: domain.SeedRules}
}

// Ping checks the database for readiness.
func (s *Postgres) Ping(ctx context.Context) error { return s.pool.Ping(ctx) }

func problem(status int, code, detail string) error { return httpx.NewProblem(status, code, detail) }

func principal(ctx context.Context) (tenant.Principal, error) {
	p, ok := tenant.FromContext(ctx)
	if !ok {
		return tenant.Principal{}, errors.New("store: no principal in context")
	}
	return p, nil
}

const conflictColumns = `conflict_id, user_id, rule_id, risk_id, severity, duty_a, duty_b, roles_json, state,
  mitigation_id, mitigation_expires_at, decided_by, decision_reason, detected_at, resolved_at, updated_at, version`

func scanConflict(row pgx.Row) (Conflict, error) {
	var c Conflict
	var roles []byte
	var mitigationID, decidedBy, reason *string
	if err := row.Scan(&c.ConflictID, &c.UserID, &c.RuleID, &c.RiskID, &c.Severity, &c.DutyA, &c.DutyB, &roles, &c.State,
		&mitigationID, &c.ExpiresAt, &decidedBy, &reason, &c.DetectedAt, &c.ResolvedAt, &c.UpdatedAt, &c.Version); err != nil {
		return Conflict{}, err
	}
	var g RoleGrants
	if err := json.Unmarshal(roles, &g); err != nil {
		return Conflict{}, fmt.Errorf("roles_json of %s: %w", c.ConflictID, err)
	}
	c.RolesA, c.RolesB = nonNil(g.A), nonNil(g.B)
	c.MitigationID, c.DecidedBy, c.DecisionReason = deref(mitigationID), deref(decidedBy), deref(reason)
	return c, nil
}

func nonNil(xs []string) []string {
	if xs == nil {
		return []string{}
	}
	return xs
}

func deref(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}

// domainConflict maps a stored conflict onto the pure state machine.
func (c Conflict) domainConflict() domain.Conflict {
	return domain.Conflict{UserID: c.UserID, RuleID: c.RuleID, Severity: c.Severity, RiskID: c.RiskID, DutyA: c.DutyA, DutyB: c.DutyB,
		RolesA: c.RolesA, RolesB: c.RolesB, State: c.State, MitigationID: c.MitigationID, ExpiresAt: c.ExpiresAt}
}

// expire reopens every MITIGATED conflict of the tenant whose mitigation has
// lapsed (BR-48.1: MITIGATED → OPEN on expiry). It runs at the start of every
// read and write so no caller ever sees a lapsed mitigation as current.
func expire(ctx context.Context, tx pgx.Tx, now time.Time) error {
	_, err := tx.Exec(ctx, `update sod_conflict
		set state = 'OPEN', mitigation_id = null, mitigation_expires_at = null, updated_at = $1, version = version + 1
		where state = 'MITIGATED' and mitigation_expires_at <= $1`, now)
	return err
}

func enqueue(ctx context.Context, tx pgx.Tx, p tenant.Principal, typ, conflictID string, data any) error {
	evt, err := events.New(typ, eventSource, conflictID, p.TenantID, data)
	if err != nil {
		return err
	}
	evt.Actor = p.UserID
	return outbox.Enqueue(ctx, tx, aggregateType, conflictID, evt)
}

// Evaluate records the outcome of an evaluation of one user's effective roles
// (on an assignment change or the nightly run, FR-48.4). New conflicts are
// stored OPEN and emit sod.conflict.detected; a RESOLVED conflict detected
// again is reopened and emitted again; a stored conflict that is no longer
// detected is RESOLVED because the conflicting role was removed (BR-48.1).
// MITIGATED and ACCEPTED_RISK conflicts that are still detected are kept.
// The call is a recomputation, so repeating it changes nothing.
func (s *Postgres) Evaluate(ctx context.Context, userID string, roles []domain.Role, now time.Time) ([]Conflict, error) {
	p, err := principal(ctx)
	if err != nil {
		return nil, err
	}
	detected := domain.Detect(userID, s.rules(), roles)
	var out []Conflict
	err = dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		if err := expire(ctx, tx, now); err != nil {
			return err
		}
		stored, err := userConflicts(ctx, tx, userID, true)
		if err != nil {
			return err
		}
		byRule := map[string]Conflict{}
		for _, c := range stored {
			byRule[c.RuleID] = c
		}
		seen := map[string]bool{}
		for _, d := range detected {
			seen[d.RuleID] = true
			grants, err := json.Marshal(RoleGrants{A: nonNil(d.RolesA), B: nonNil(d.RolesB)})
			if err != nil {
				return err
			}
			existing, ok := byRule[d.RuleID]
			switch {
			case !ok:
				// A concurrent first evaluation of the same user may insert the
				// same (user, rule) first; that transaction owns the event.
				id := ids.New()
				tag, err := tx.Exec(ctx, `insert into sod_conflict (tenant_id, conflict_id, user_id, rule_id, risk_id, severity, duty_a, duty_b,
					roles_json, state, detected_at, updated_at) values ($1, $2, $3, $4, $5, $6, $7, $8, $9, 'OPEN', $10, $10)
					on conflict (tenant_id, user_id, rule_id) do nothing`,
					p.TenantID, id, userID, d.RuleID, d.RiskID, d.Severity, d.DutyA, d.DutyB, grants, now)
				if err != nil {
					return err
				}
				if tag.RowsAffected() == 0 {
					continue
				}
				if err := enqueue(ctx, tx, p, cevents.ConflictDetected, id, detectedData(id, d)); err != nil {
					return err
				}
			case existing.State == domain.StateResolved:
				if _, err := tx.Exec(ctx, `update sod_conflict set state = 'OPEN', severity = $2, risk_id = $3, duty_a = $4, duty_b = $5,
					roles_json = $6, detected_at = $7, resolved_at = null, decided_by = null, decision_reason = null, updated_at = $7,
					version = version + 1 where conflict_id = $1`,
					existing.ConflictID, d.Severity, d.RiskID, d.DutyA, d.DutyB, grants, now); err != nil {
					return err
				}
				if err := enqueue(ctx, tx, p, cevents.ConflictDetected, existing.ConflictID, detectedData(existing.ConflictID, d)); err != nil {
					return err
				}
			default:
				// Still present: refresh which roles grant the duties, keep the state.
				if _, err := tx.Exec(ctx, `update sod_conflict set roles_json = $2, duty_a = $3, duty_b = $4
					where conflict_id = $1 and roles_json is distinct from $2`, existing.ConflictID, grants, d.DutyA, d.DutyB); err != nil {
					return err
				}
			}
		}
		for _, c := range stored {
			if seen[c.RuleID] || c.State == domain.StateResolved {
				continue
			}
			if _, err := tx.Exec(ctx, `update sod_conflict set state = 'RESOLVED', mitigation_id = null, mitigation_expires_at = null,
				resolved_at = $2, updated_at = $2, version = version + 1 where conflict_id = $1`, c.ConflictID, now); err != nil {
				return err
			}
		}
		out, err = userConflicts(ctx, tx, userID, false)
		return err
	})
	return out, err
}

func detectedData(id string, d domain.Conflict) cevents.ConflictDetectedData {
	roles := append(append([]string{}, d.RolesA...), d.RolesB...)
	return cevents.ConflictDetectedData{ConflictID: id, UserID: d.UserID, RuleID: d.RuleID, RiskID: d.RiskID, Severity: d.Severity,
		DutyA: d.DutyA, DutyB: d.DutyB, Roles: uniqueSorted(roles)}
}

func uniqueSorted(xs []string) []string {
	seen := map[string]bool{}
	out := []string{}
	for _, x := range xs {
		if !seen[x] {
			seen[x] = true
			out = append(out, x)
		}
	}
	sort.Strings(out)
	return out
}

func userConflicts(ctx context.Context, tx pgx.Tx, userID string, lock bool) ([]Conflict, error) {
	q := `select ` + conflictColumns + ` from sod_conflict where user_id = $1 order by rule_id`
	if lock {
		q += ` for update`
	}
	rows, err := tx.Query(ctx, q, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Conflict{}
	for rows.Next() {
		c, err := scanConflict(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

// KnownStates returns the recorded state of each of the user's conflicts, by
// rule id, after lapsed mitigations are reopened. Simulation uses it so only a
// mitigation the service itself recorded can exempt a HIGH conflict.
func (s *Postgres) KnownStates(ctx context.Context, userID string, now time.Time) (map[string]string, error) {
	out := map[string]string{}
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		if err := expire(ctx, tx, now); err != nil {
			return err
		}
		cs, err := userConflicts(ctx, tx, userID, false)
		for _, c := range cs {
			out[c.RuleID] = c.State
		}
		return err
	})
	return out, err
}

type cursor struct {
	DetectedAt time.Time `json:"d"`
	ID         string    `json:"i"`
}

func encodeCursor(c Conflict) string {
	raw, _ := json.Marshal(cursor{DetectedAt: c.DetectedAt, ID: c.ConflictID})
	return base64.RawURLEncoding.EncodeToString(raw)
}

// DecodeCursor parses a listing cursor; the API validates it before listing.
func DecodeCursor(s string) (time.Time, string, error) {
	raw, err := base64.RawURLEncoding.DecodeString(s)
	if err != nil {
		return time.Time{}, "", err
	}
	var c cursor
	if err := json.Unmarshal(raw, &c); err != nil {
		return time.Time{}, "", err
	}
	if !ids.Valid(c.ID) || c.DetectedAt.IsZero() {
		return time.Time{}, "", errors.New("malformed cursor")
	}
	return c.DetectedAt, c.ID, nil
}

// List returns conflicts newest detection first, keyset-paginated.
func (s *Postgres) List(ctx context.Context, f Filter, now time.Time) (Page, error) {
	limit := f.Limit
	if limit <= 0 || limit > MaxPageSize {
		limit = MaxPageSize
	}
	where := []string{"true"}
	args := []any{}
	add := func(clause string, v any) {
		args = append(args, v)
		where = append(where, fmt.Sprintf(clause, len(args)))
	}
	if f.State != "" {
		add("state = $%d", f.State)
	}
	if f.Severity != "" {
		add("severity = $%d", f.Severity)
	}
	if f.UserID != "" {
		add("user_id = $%d", f.UserID)
	}
	if f.RuleID != "" {
		add("rule_id = $%d", f.RuleID)
	}
	if f.Cursor != "" {
		at, id, err := DecodeCursor(f.Cursor)
		if err != nil {
			return Page{}, problem(http.StatusBadRequest, "COM-0002", "cursor is malformed")
		}
		args = append(args, at, id)
		where = append(where, fmt.Sprintf("(detected_at, conflict_id) < ($%d, $%d)", len(args)-1, len(args)))
	}
	args = append(args, limit+1)
	q := `select ` + conflictColumns + ` from sod_conflict where ` + strings.Join(where, " and ") +
		fmt.Sprintf(` order by detected_at desc, conflict_id desc limit $%d`, len(args))
	page := Page{Items: []Conflict{}}
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		if err := expire(ctx, tx, now); err != nil {
			return err
		}
		rows, err := tx.Query(ctx, q, args...)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			c, err := scanConflict(rows)
			if err != nil {
				return err
			}
			page.Items = append(page.Items, c)
		}
		return rows.Err()
	})
	if err != nil {
		return Page{}, err
	}
	if len(page.Items) > limit {
		page.Items = page.Items[:limit]
		page.NextCursor = encodeCursor(page.Items[limit-1])
	}
	return page, nil
}

// Get returns one conflict with its mitigation history.
func (s *Postgres) Get(ctx context.Context, id string, now time.Time) (ConflictDetail, error) {
	var out ConflictDetail
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		if err := expire(ctx, tx, now); err != nil {
			return err
		}
		var err error
		out, err = detail(ctx, tx, id, false)
		return err
	})
	return out, err
}

func detail(ctx context.Context, tx pgx.Tx, id string, lock bool) (ConflictDetail, error) {
	q := `select ` + conflictColumns + ` from sod_conflict where conflict_id = $1`
	if lock {
		q += ` for update`
	}
	c, err := scanConflict(tx.QueryRow(ctx, q, id))
	if errors.Is(err, pgx.ErrNoRows) {
		return ConflictDetail{}, problem(http.StatusNotFound, CodeNotFound, "no SoD conflict with that id")
	}
	if err != nil {
		return ConflictDetail{}, err
	}
	rows, err := tx.Query(ctx, `select mitigation_id, conflict_id, control_ref, reason, approver_id, approved_at, expires_at
		from sod_mitigation where conflict_id = $1 order by approved_at desc, mitigation_id desc`, id)
	if err != nil {
		return ConflictDetail{}, err
	}
	defer rows.Close()
	out := ConflictDetail{Conflict: c, Mitigations: []Mitigation{}}
	for rows.Next() {
		var m Mitigation
		if err := rows.Scan(&m.MitigationID, &m.ConflictID, &m.ControlRef, &m.Reason, &m.ApproverID, &m.ApprovedAt, &m.ExpiresAt); err != nil {
			return ConflictDetail{}, err
		}
		out.Mitigations = append(out.Mitigations, m)
	}
	return out, rows.Err()
}

func checkVersion(c Conflict, expected int) error {
	if c.Version != expected {
		return problem(http.StatusPreconditionFailed, CodeVersionConflict, "the conflict was changed by another user; reload and reapply")
	}
	return nil
}

// Mitigate approves a compensating control for an OPEN conflict (FR-48.5,
// AC-48.4). The approver is the calling user and must not be the conflicted
// user; the expiry is capped by the rule and by tenantCapDays
// (sod.max_mitigation_days). The mitigation row, the conflict change and
// sod.mitigation.approved commit together. A repeated Idempotency-Key replays
// the first answer.
func (s *Postgres) Mitigate(ctx context.Context, id string, expected int, in MitigationInput, tenantCapDays int, key string, now time.Time) (ConflictDetail, error) {
	p, err := principal(ctx)
	if err != nil {
		return ConflictDetail{}, err
	}
	var out ConflictDetail
	err = dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		request := struct {
			ID       string          `json:"id"`
			Version  int             `json:"version"`
			Input    MitigationInput `json:"input"`
			Approver string          `json:"approver"`
		}{id, expected, in, p.UserID}
		var err error
		out, err = idempotent(ctx, tx, p.TenantID, key, "sod.mitigate", request, func() (ConflictDetail, error) {
			if err := expire(ctx, tx, now); err != nil {
				return ConflictDetail{}, err
			}
			cur, err := detail(ctx, tx, id, true)
			if err != nil {
				return ConflictDetail{}, err
			}
			if err := checkVersion(cur.Conflict, expected); err != nil {
				return ConflictDetail{}, err
			}
			rule, ok := domain.RuleByID(s.rules(), cur.RuleID)
			if !ok {
				return ConflictDetail{}, problem(http.StatusUnprocessableEntity, CodeUnknownRule, "the conflict's rule is no longer in the rule set")
			}
			dc := cur.domainConflict()
			m := domain.Mitigation{MitigationID: ids.New(), ControlRef: strings.TrimSpace(in.ControlRef), Reason: strings.TrimSpace(in.Reason),
				ApproverID: p.UserID, ExpiresAt: in.ExpiresAt.UTC()}
			if err := dc.Mitigate(rule, m, now, tenantCapDays); err != nil {
				return ConflictDetail{}, err
			}
			if _, err := tx.Exec(ctx, `insert into sod_mitigation (tenant_id, mitigation_id, conflict_id, control_ref, reason, approver_id,
				approved_at, expires_at) values ($1, $2, $3, $4, $5, $6, $7, $8)`,
				p.TenantID, m.MitigationID, id, m.ControlRef, m.Reason, m.ApproverID, now, m.ExpiresAt); err != nil {
				return ConflictDetail{}, err
			}
			if _, err := tx.Exec(ctx, `update sod_conflict set state = 'MITIGATED', mitigation_id = $2, mitigation_expires_at = $3,
				decided_by = $4, decision_reason = $5, updated_at = $6, version = version + 1 where conflict_id = $1`,
				id, m.MitigationID, m.ExpiresAt, m.ApproverID, m.Reason, now); err != nil {
				return ConflictDetail{}, err
			}
			if err := enqueue(ctx, tx, p, cevents.MitigationApproved, id, cevents.MitigationApprovedData{
				ConflictID: id, MitigationID: m.MitigationID, UserID: cur.UserID, RuleID: cur.RuleID, ControlRef: m.ControlRef,
				Approver: m.ApproverID, ExpiresAt: m.ExpiresAt}); err != nil {
				return ConflictDetail{}, err
			}
			return detail(ctx, tx, id, false)
		})
		return err
	})
	return out, err
}

// AcceptRisk records an accepted risk on an OPEN LOW or MEDIUM conflict with a
// reason (BR-48.1). The SoDAdmin duty is enforced by the API.
func (s *Postgres) AcceptRisk(ctx context.Context, id string, expected int, reason string, now time.Time) (ConflictDetail, error) {
	p, err := principal(ctx)
	if err != nil {
		return ConflictDetail{}, err
	}
	var out ConflictDetail
	err = dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		if err := expire(ctx, tx, now); err != nil {
			return err
		}
		cur, err := detail(ctx, tx, id, true)
		if err != nil {
			return err
		}
		if err := checkVersion(cur.Conflict, expected); err != nil {
			return err
		}
		dc := cur.domainConflict()
		reason = strings.TrimSpace(reason)
		if err := dc.AcceptRisk(reason); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `update sod_conflict set state = 'ACCEPTED_RISK', decided_by = $2, decision_reason = $3, updated_at = $4,
			version = version + 1 where conflict_id = $1`, id, p.UserID, reason, now); err != nil {
			return err
		}
		out, err = detail(ctx, tx, id, false)
		return err
	})
	return out, err
}

// idempotent runs a command at most once per (tenant, Idempotency-Key). The key
// is claimed first, so a concurrent request with the same key waits on the
// primary key and then replays; the response is stored in the same
// transaction as the change. Reusing a key with a different request is 422.
func idempotent[T any](ctx context.Context, tx pgx.Tx, tenantID, key, scope string, request any, run func() (T, error)) (T, error) {
	var zero T
	raw, err := json.Marshal(request)
	if err != nil {
		return zero, err
	}
	sum := sha256.Sum256(append([]byte(scope+"\n"), raw...))
	hash := hex.EncodeToString(sum[:])
	tag, err := tx.Exec(ctx, `insert into idempotency_keys (tenant_id, key, request_hash, response_status) values ($1, $2, $3, 0)
		on conflict (tenant_id, key) do nothing`, tenantID, key, hash)
	if err != nil {
		return zero, err
	}
	if tag.RowsAffected() == 0 {
		var stored string
		var body []byte
		if err := tx.QueryRow(ctx, `select request_hash, response_body from idempotency_keys where tenant_id = $1 and key = $2`, tenantID, key).Scan(&stored, &body); err != nil {
			return zero, err
		}
		if stored != hash {
			return zero, problem(http.StatusUnprocessableEntity, CodeIdempotencyReuse, "Idempotency-Key was already used with a different request")
		}
		var out T
		if err := json.Unmarshal(body, &out); err != nil {
			return zero, fmt.Errorf("replay %s: %w", key, err)
		}
		return out, nil
	}
	out, err := run()
	if err != nil {
		return zero, err
	}
	body, err := json.Marshal(out)
	if err != nil {
		return zero, err
	}
	_, err = tx.Exec(ctx, `update idempotency_keys set response_status = $3, response_body = $4 where tenant_id = $1 and key = $2`,
		tenantID, key, http.StatusCreated, body)
	return out, err
}
