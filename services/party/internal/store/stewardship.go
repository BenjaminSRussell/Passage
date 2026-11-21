package store

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/BenjaminSRussell/depot/platform/dbx"
	"github.com/BenjaminSRussell/depot/platform/httpx"
	"github.com/BenjaminSRussell/depot/platform/ids"
	"github.com/BenjaminSRussell/depot/platform/tenant"
	"github.com/BenjaminSRussell/depot/services/party/internal/domain"
)

const (
	CodeDupLikely       = "PTY-DUP-LIKELY"
	CodeStewardInvalid  = "PTY-STEWARD-INVALID"
	CodeStewardNotFound = "PTY-STEWARD-NOT-FOUND"
	CodeStewardResolved = "PTY-STEWARD-RESOLVED"
	StewardOpen         = "open"
	StewardResolved     = "resolved"
	MaxReasonLen        = 500
	stewardColumns      = `id::text, party_id::text, legal_entity_id::text, role, candidates, override_reason, status, coalesce(resolution, ''), coalesce(resolution_reason, ''), requested_by, coalesce(resolved_by, ''), created_at, resolved_at`
	partyRecordColumns  = `p.id::text, p.party_number, p.party_type, p.name, coalesce(p.org_registration_number, ''), ` +
		`coalesce((select array_agg(a.normalized) from party_electronic_address a where a.tenant_id = p.tenant_id and a.party_id = p.id and a.kind = 'email'), '{}'), ` +
		`coalesce((select array_agg(a.normalized) from party_electronic_address a where a.tenant_id = p.tenant_id and a.party_id = p.id and a.kind = 'phone'), '{}')`
)

type Candidate struct {
	PartyID     string  `json:"partyId"`
	PartyNumber string  `json:"partyNumber"`
	Name        string  `json:"name"`
	Weight      float64 `json:"weight"`
	Class       string  `json:"class"`
}

type DuplicateError struct {
	*httpx.Problem
	Candidates []Candidate `json:"candidates"`
}

func (e *DuplicateError) Unwrap() error { return e.Problem }

type StewardTask struct {
	ID               string      `json:"id"`
	PartyID          string      `json:"partyId"`
	LegalEntityID    string      `json:"legalEntityId"`
	Role             string      `json:"role"`
	Candidates       []Candidate `json:"candidates"`
	OverrideReason   string      `json:"overrideReason"`
	Status           string      `json:"status"`
	Resolution       string      `json:"resolution,omitempty"`
	ResolutionReason string      `json:"resolutionReason,omitempty"`
	RequestedBy      string      `json:"requestedBy"`
	ResolvedBy       string      `json:"resolvedBy,omitempty"`
	CreatedAt        time.Time   `json:"createdAt"`
	ResolvedAt       *time.Time  `json:"resolvedAt,omitempty"`
}

type ResolveStewardTask struct {
	Resolution string `json:"resolution"`
	Reason     string `json:"reason"`
}

func (r ResolveStewardTask) Clean() ResolveStewardTask {
	r.Resolution = strings.TrimSpace(r.Resolution)
	r.Reason = strings.TrimSpace(r.Reason)
	return r
}

func (r ResolveStewardTask) Validate() map[string]string {
	errs := map[string]string{}
	if r.Resolution != "distinct" && r.Resolution != "duplicate" {
		errs["resolution"] = "must be distinct or duplicate"
	}
	if n := len([]rune(r.Reason)); n < 1 || n > MaxReasonLen {
		errs["reason"] = "is required, at most 500 characters"
	}
	return errs
}

type StewardStore interface {
	ListStewardTasks(ctx context.Context, status string) ([]StewardTask, error)
	ResolveStewardTask(ctx context.Context, taskID string, in ResolveStewardTask) (StewardTask, error)
}

func stewardNotFound() error { return notFound(CodeStewardNotFound, "steward task not found") }

func scanStewardTask(row pgx.Row) (StewardTask, error) {
	var t StewardTask
	err := row.Scan(&t.ID, &t.PartyID, &t.LegalEntityID, &t.Role, &t.Candidates, &t.OverrideReason, &t.Status, &t.Resolution, &t.ResolutionReason, &t.RequestedBy, &t.ResolvedBy, &t.CreatedAt, &t.ResolvedAt)
	return t, err
}

func (s *Postgres) checkDuplicates(ctx context.Context, tx pgx.Tx, caller tenant.Principal, legalEntityID, partyID, role, overrideReason string) error {
	var subject domain.PartyRecord
	var searchName, number string
	err := tx.QueryRow(ctx, `select `+partyRecordColumns+`, p.search_name from party p where p.tenant_id = $1 and p.id = $2`, caller.TenantID, partyID).
		Scan(&subject.ID, &number, &subject.Type, &subject.Name, &subject.TaxID, &subject.Emails, &subject.Phones, &searchName)
	if err != nil {
		return err
	}
	addresses := append(append([]string{}, subject.Emails...), subject.Phones...)
	rows, err := tx.Query(ctx, `select `+partyRecordColumns+` from party p
		where p.tenant_id = $1 and p.id <> $2 and p.status <> 'merged' and p.party_type = $3
		and exists (select 1 from `+role+`_role r where r.tenant_id = p.tenant_id and r.party_id = p.id and r.legal_entity_id = $4)
		and (p.search_name operator(public.%) $5
			or ($6::text <> '' and p.org_registration_number = $6)
			or exists (select 1 from party_electronic_address a where a.tenant_id = p.tenant_id and a.party_id = p.id and a.normalized = any($7::text[])))
		limit 200`, caller.TenantID, partyID, subject.Type, legalEntityID, searchName, subject.TaxID, addresses)
	if err != nil {
		return err
	}
	defer rows.Close()
	pool, byID := []domain.PartyRecord{}, map[string]Candidate{}
	for rows.Next() {
		var r domain.PartyRecord
		var c Candidate
		if err := rows.Scan(&r.ID, &c.PartyNumber, &r.Type, &r.Name, &r.TaxID, &r.Emails, &r.Phones); err != nil {
			return err
		}
		c.PartyID, c.Name = r.ID, r.Name
		pool, byID[r.ID] = append(pool, r), c
	}
	if err := rows.Err(); err != nil {
		return err
	}
	rows.Close()
	candidates := []Candidate{}
	for _, m := range domain.FindCandidates(pool, subject) {
		c := byID[m.B]
		c.Weight, c.Class = m.Weight, m.Class
		candidates = append(candidates, c)
	}
	if len(candidates) == 0 {
		return nil
	}
	if overrideReason == "" {
		p := httpx.NewProblem(http.StatusConflict, CodeDupLikely, "a similar "+role+" already exists; review the candidates or retry with an override reason")
		return &DuplicateError{Problem: p, Candidates: candidates}
	}
	encoded, err := json.Marshal(candidates)
	if err != nil {
		return err
	}
	_, err = tx.Exec(ctx, `insert into steward_task (tenant_id, id, party_id, legal_entity_id, role, candidates, override_reason, requested_by)
		values ($1, $2, $3, $4, $5, $6::text::jsonb, $7, $8)`, caller.TenantID, ids.New(), partyID, legalEntityID, role, string(encoded), overrideReason, caller.UserID)
	return err
}

func (s *Postgres) ListStewardTasks(ctx context.Context, status string) ([]StewardTask, error) {
	caller, _ := tenant.FromContext(ctx)
	tasks := []StewardTask{}
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `select `+stewardColumns+` from steward_task where tenant_id = $1 and status = $2 order by created_at, id limit 100`, caller.TenantID, status)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			t, err := scanStewardTask(rows)
			if err != nil {
				return err
			}
			tasks = append(tasks, t)
		}
		return rows.Err()
	})
	return tasks, err
}

func (s *Postgres) ResolveStewardTask(ctx context.Context, taskID string, in ResolveStewardTask) (StewardTask, error) {
	caller, _ := tenant.FromContext(ctx)
	if err := requireUser(caller); err != nil {
		return StewardTask{}, err
	}
	id, err := canonicalID(taskID)
	if err != nil {
		return StewardTask{}, stewardNotFound()
	}
	var task StewardTask
	err = dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		var err error
		task, err = scanStewardTask(tx.QueryRow(ctx, `update steward_task set status = 'resolved', resolution = $3, resolution_reason = $4, resolved_by = $5, resolved_at = now()
			where tenant_id = $1 and id = $2 and status = 'open' returning `+stewardColumns, caller.TenantID, id, in.Resolution, in.Reason, caller.UserID))
		if !errors.Is(err, pgx.ErrNoRows) {
			return err
		}
		var known bool
		if err := tx.QueryRow(ctx, `select exists (select 1 from steward_task where tenant_id = $1 and id = $2)`, caller.TenantID, id).Scan(&known); err != nil {
			return err
		}
		if !known {
			return stewardNotFound()
		}
		return httpx.NewProblem(http.StatusConflict, CodeStewardResolved, "the steward task is already resolved")
	})
	return task, err
}
