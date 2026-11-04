package store

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"unicode/utf8"

	"github.com/jackc/pgx/v5"

	"github.com/BenjaminSRussell/passage/platform/dbx"
	"github.com/BenjaminSRussell/passage/platform/events"
	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/platform/outbox"
	"github.com/BenjaminSRussell/passage/platform/tenant"
	"github.com/BenjaminSRussell/passage/services/party/internal/domain"
	partyevents "github.com/BenjaminSRussell/passage/services/party/internal/events"
)

const (
	// DutyMergeExecute is the service-only duty of the workflow-ts merge approval: it decides a merge
	// request for the approver workflow-ts authenticated, in a tenant-level token (#450).
	DutyMergeExecute = "party.merge.execute"
	// MergeExecutor is the only service whose token may decide a merge request.
	MergeExecutor = "workflow-ts"

	CodeMergeRequestNotFound = "PTY-MERGE-REQUEST-NOT-FOUND"
	CodeMergePending         = "PTY-MERGE-PENDING"
	CodeMergeDecided         = "PTY-MERGE-DECIDED"
	CodeMergeSelfApproval    = "PTY-MERGE-SELF-APPROVAL"
	CodeMergeStale           = "PTY-MERGE-STALE"

	mergeKeyPrefix      = "merge-request:"
	mergeRequestColumns = `id::text, survivor_party_id::text, merged_party_id::text, rules, status, requested_by, decided_by, decision_reason, merge_id::text, requested_at, decided_at`
)

// MergeRequestInput is a merge to hold for approval. Key is the optional
// Idempotency-Key and Hash the hash of the request body.
type MergeRequestInput struct {
	SurvivorID string
	MergedID   string
	Rules      map[string]domain.Rule
	Key        string
	Hash       string
}

func scanMergeRequest(row pgx.Row) (domain.MergeRequest, error) {
	var m domain.MergeRequest
	var rules []byte
	if err := row.Scan(&m.ID, &m.SurvivorPartyID, &m.MergedPartyID, &rules, &m.Status, &m.RequestedBy, &m.DecidedBy, &m.Reason, &m.MergeID, &m.RequestedAt, &m.DecidedAt); err != nil {
		return m, err
	}
	m.Rules = map[string]domain.Rule{}
	return m, json.Unmarshal(rules, &m.Rules)
}

func enqueueMergeRequest(ctx context.Context, tx pgx.Tx, tenantID, eventType string, m domain.MergeRequest) error {
	data := partyevents.MergeRequestData{MergeRequestID: m.ID, SurvivorPartyID: m.SurvivorPartyID, MergedPartyID: m.MergedPartyID, RequestedBy: m.RequestedBy, Reason: m.Reason}
	if m.DecidedBy != nil {
		data.DecidedBy = *m.DecidedBy
	}
	evt, err := events.New(eventType, domain.EventSource, m.SurvivorPartyID, tenantID, data)
	if err != nil {
		return err
	}
	return outbox.Enqueue(ctx, tx, domain.AggregateParty, m.SurvivorPartyID, evt)
}

// RequestMerge records a merge for approval and merges nothing. The caller is a
// tenant administrator with party.merge and no legal-entity claim, as for Merge.
// The parties are checked now so a doomed request fails early, and again on approval.
func (s *Postgres) RequestMerge(ctx context.Context, in MergeRequestInput) (domain.MergeRequest, bool, error) {
	var out domain.MergeRequest
	p, err := fullScope(ctx)
	if err != nil {
		return out, false, err
	}
	if err := requireUser(p); err != nil {
		return out, false, err
	}
	req := MergeRequest{Rules: in.Rules}
	if req.WinnerID, err = canonicalID(in.SurvivorID); err != nil {
		return out, false, err
	}
	if req.LoserID, err = canonicalID(in.MergedID); err != nil {
		return out, false, err
	}
	if req.WinnerID == req.LoserID {
		return out, false, ErrNotMergeable
	}
	if err := domain.ValidateRules(in.Rules); err != nil {
		return out, false, err
	}
	rules, err := json.Marshal(in.Rules)
	if err != nil {
		return out, false, err
	}
	if in.Rules == nil {
		rules = []byte(`{}`)
	}
	created := false
	err = adminTx(ctx, s.pool, p, func(tx pgx.Tx) error {
		var key *string
		if in.Key != "" {
			k := mergeKeyPrefix + req.WinnerID + ":" + in.Key
			key = &k
			var storedHash string
			err := tx.QueryRow(ctx, `select request_hash from idempotency_keys where tenant_id = $1 and key = $2`, p.TenantID, k).Scan(&storedHash)
			if err == nil {
				if storedHash != in.Hash {
					return httpx.NewProblem(http.StatusConflict, CodeIdempotencyReuse, "Idempotency-Key was already used with a different request body")
				}
				out, err = scanMergeRequest(tx.QueryRow(ctx, `select `+mergeRequestColumns+` from merge_request where tenant_id = $1 and idempotency_key = $2`, p.TenantID, k))
				return err
			}
			if !errors.Is(err, pgx.ErrNoRows) {
				return err
			}
		}
		rows, err := lockParties(ctx, tx, p.TenantID, req.WinnerID, req.LoserID)
		if err != nil {
			return err
		}
		winner, loser := rows[req.WinnerID], rows[req.LoserID]
		if winner == nil || loser == nil {
			return ErrNotFound
		}
		if winner.partyType != loser.partyType || winner.status == domain.StatusMerged || loser.status == domain.StatusMerged {
			return ErrNotMergeable
		}
		if err := rejectRoleConflicts(ctx, tx, p.TenantID, req); err != nil {
			return err
		}
		out, err = scanMergeRequest(tx.QueryRow(ctx, `insert into merge_request (tenant_id, id, survivor_party_id, merged_party_id, rules, requested_by, idempotency_key)
			values ($1, $2, $3, $4, $5, $6, $7)
			on conflict (tenant_id, survivor_party_id, merged_party_id) where status = 'pending' do nothing returning `+mergeRequestColumns,
			p.TenantID, ids.New(), req.WinnerID, req.LoserID, rules, p.UserID, key))
		if errors.Is(err, pgx.ErrNoRows) {
			problem := httpx.NewProblem(http.StatusConflict, CodeMergePending, "a merge of these parties is already awaiting approval")
			var pendingID string
			if err := tx.QueryRow(ctx, `select id::text from merge_request where tenant_id = $1 and survivor_party_id = $2 and merged_party_id = $3 and status = 'pending'`,
				p.TenantID, req.WinnerID, req.LoserID).Scan(&pendingID); err == nil {
				problem.Errors = map[string]string{"mergeRequestId": pendingID}
			} else if !errors.Is(err, pgx.ErrNoRows) {
				return err
			}
			return problem
		}
		if err != nil {
			return err
		}
		if key != nil {
			if _, err := tx.Exec(ctx, `insert into idempotency_keys (tenant_id, key, request_hash, response_status) values ($1, $2, $3, 202)`, p.TenantID, *key, in.Hash); err != nil {
				return err
			}
		}
		created = true
		return enqueueMergeRequest(ctx, tx, p.TenantID, partyevents.MergeRequested, out)
	})
	return out, created, err
}

func (s *Postgres) GetMergeRequest(ctx context.Context, requestID string) (domain.MergeRequest, error) {
	caller, _ := tenant.FromContext(ctx)
	if err := requireUser(caller); err != nil {
		return domain.MergeRequest{}, err
	}
	id, err := canonicalID(requestID)
	if err != nil {
		return domain.MergeRequest{}, notFound(CodeMergeRequestNotFound, "merge request not found")
	}
	var out domain.MergeRequest
	err = dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		var err error
		out, err = scanMergeRequest(tx.QueryRow(ctx, `select `+mergeRequestColumns+` from merge_request where tenant_id = $1 and id = $2`, caller.TenantID, id))
		if errors.Is(err, pgx.ErrNoRows) {
			return notFound(CodeMergeRequestNotFound, "merge request not found")
		}
		return err
	})
	return out, err
}

// executorTx runs fn for tenantID seeing every legal entity, as a merge needs.
// DecideMerge's caller is the workflow-ts tenant-level token (no legal entity,
// party.merge.execute only), which never holds tenant.administer, so
// dbx.WithTenantWideAdminTx cannot be used; MergeExecutorPrincipal gates it instead.
func executorTx(ctx context.Context, s *Postgres, tenantID string, fn func(tx pgx.Tx) error) error {
	return pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `select set_config('passage.tenant_id', $1, true), set_config('passage.legal_entity_scopes', '*', true)`, tenantID); err != nil {
			return err
		}
		return fn(tx)
	})
}

// MergeExecutorPrincipal reports whether p is the workflow-ts tenant-level token that decides merge requests.
func MergeExecutorPrincipal(p tenant.Principal) bool {
	return p.Service == MergeExecutor && p.LegalEntityID == "" && p.HasDuty(DutyMergeExecute)
}

// DecideMerge approves (and executes) or rejects a merge request. Only the
// workflow-ts merge approval calls it, acting for the approver it authenticated
// (the token's act): that approver may not be the requester, the decision is
// recorded once, and a repeat of the same decision by the same approver is a replay.
func (s *Postgres) DecideMerge(ctx context.Context, requestID string, approve bool, reason string) (domain.MergeRequest, error) {
	var out domain.MergeRequest
	caller, _ := tenant.FromContext(ctx)
	if !MergeExecutorPrincipal(caller) {
		return out, httpx.NewProblem(http.StatusForbidden, "AUTH-0003", "a merge request is decided only through the workflow-ts merge approval")
	}
	if err := requireUser(caller); err != nil {
		return out, err
	}
	if utf8.RuneCountInString(reason) > maxReasonLength {
		p := httpx.NewProblem(http.StatusUnprocessableEntity, CodeReasonTooLong, "decision failed validation")
		p.Errors = map[string]string{"reason": "must be at most 500 characters"}
		return out, p
	}
	id, err := canonicalID(requestID)
	if err != nil {
		return out, notFound(CodeMergeRequestNotFound, "merge request not found")
	}
	err = executorTx(ctx, s, caller.TenantID, func(tx pgx.Tx) error {
		cur, err := scanMergeRequest(tx.QueryRow(ctx, `select `+mergeRequestColumns+` from merge_request where tenant_id = $1 and id = $2 for update`, caller.TenantID, id))
		if errors.Is(err, pgx.ErrNoRows) {
			return notFound(CodeMergeRequestNotFound, "merge request not found")
		}
		if err != nil {
			return err
		}
		wanted := domain.MergeRejected
		if approve {
			wanted = domain.MergeApproved
		}
		if cur.Status == wanted && cur.DecidedBy != nil && *cur.DecidedBy == caller.UserID {
			cur.Replayed = true
			out = cur
			return nil
		}
		if cur.Status != domain.MergePending {
			return httpx.NewProblem(http.StatusConflict, CodeMergeDecided, "merge request was already "+cur.Status)
		}
		if caller.UserID == cur.RequestedBy {
			return httpx.NewProblem(http.StatusForbidden, CodeMergeSelfApproval, "the requester cannot decide their own merge")
		}
		var mergeID *string
		if approve {
			res, err := mergeIn(ctx, tx, caller.TenantID, MergeRequest{WinnerID: cur.SurvivorPartyID, LoserID: cur.MergedPartyID, Rules: cur.Rules})
			if errors.Is(err, ErrNotFound) || errors.Is(err, ErrNotMergeable) || errors.Is(err, ErrRoleConflict) {
				return httpx.NewProblem(http.StatusConflict, CodeMergeStale, "the parties changed after this request ("+err.Error()+"); reject it and request again")
			}
			if err != nil {
				return err
			}
			mergeID = &res.MergeID
		}
		out, err = scanMergeRequest(tx.QueryRow(ctx, `update merge_request set status = $3, decided_by = $4, decision_reason = nullif($5, ''), merge_id = $6, decided_at = now()
			where tenant_id = $1 and id = $2 returning `+mergeRequestColumns, caller.TenantID, id, wanted, caller.UserID, reason, mergeID))
		if err != nil || approve {
			return err
		}
		return enqueueMergeRequest(ctx, tx, caller.TenantID, partyevents.MergeRejected, out)
	})
	return out, err
}
