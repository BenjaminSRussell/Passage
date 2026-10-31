package store

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/BenjaminSRussell/passage/platform/dbx"
	"github.com/BenjaminSRussell/passage/platform/events"
	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/platform/outbox"
	"github.com/BenjaminSRussell/passage/platform/tenant"
	"github.com/BenjaminSRussell/passage/services/party/internal/domain"
)

const DutyMerge = "party.merge"

var (
	ErrNotFound        = errors.New("party not found")
	ErrNotMergeable    = errors.New("parties cannot be merged: same party, different types, or already merged")
	ErrRoleConflict    = errors.New("both parties hold the same role in the same legal entity")
	ErrInvalidID       = errors.New("party id must be a UUID")
	ErrScopedPrincipal = errors.New("merge and audit need a tenant.administer principal that spans every legal entity")
)

type MergeRequest struct {
	WinnerID string
	LoserID  string
	Rules    map[string]domain.Rule
}

type MergeResult struct {
	MergeID             string
	RolesMoved          int
	ReferencesRewritten int
	Resolved            map[string]string
}

type partyRow struct {
	id        string
	partyType string
	status    string
	updatedAt time.Time
	values    map[string]string
}

func canonicalID(raw string) (string, error) {
	id, err := uuid.Parse(raw)
	if err != nil {
		return "", fmt.Errorf("%w: %q", ErrInvalidID, raw)
	}
	return id.String(), nil
}

// fullScope guards party merge and audit, tenant-level operations: parties are
// shared across legal entities. The predicate is no legal-entity claim AND
// DutyMerge; the transaction then runs with an explicit all-entity database
// scope (as identity withAdminTx), since a no-claim write otherwise has an empty
// scope and RLS hides customer_role.
func fullScope(ctx context.Context) (tenant.Principal, error) {
	p, ok := tenant.FromContext(ctx)
	if !ok {
		return p, errors.New("store: no principal in context")
	}
	if !dbx.TenantWide(p, DutyMerge) {
		return p, ErrScopedPrincipal
	}
	return p, nil
}

func adminTx(ctx context.Context, pool *pgxpool.Pool, p tenant.Principal, fn func(tx pgx.Tx) error) error {
	return dbx.WithTenantWideAdminTx(ctx, pool, p, DutyMerge, fn)
}

func (s *Postgres) Merge(ctx context.Context, req MergeRequest) (MergeResult, error) {
	var res MergeResult
	p, err := fullScope(ctx)
	if err != nil {
		return res, err
	}
	if req.WinnerID, err = canonicalID(req.WinnerID); err != nil {
		return res, err
	}
	if req.LoserID, err = canonicalID(req.LoserID); err != nil {
		return res, err
	}
	if req.WinnerID == req.LoserID {
		return res, ErrNotMergeable
	}
	err = adminTx(ctx, s.pool, p, func(tx pgx.Tx) error {
		rows, err := lockParties(ctx, tx, p.TenantID, req.WinnerID, req.LoserID)
		if err != nil {
			return err
		}
		winner, loser := rows[req.WinnerID], rows[req.LoserID]
		if winner == nil || loser == nil {
			return ErrNotFound
		}
		if winner.partyType != loser.partyType || winner.status == "merged" || loser.status == "merged" {
			return ErrNotMergeable
		}
		resolved, err := domain.Survive(
			domain.Side{Values: winner.values, UpdatedAt: winner.updatedAt},
			domain.Side{Values: loser.values, UpdatedAt: loser.updatedAt}, req.Rules)
		if err != nil {
			return err
		}
		if err := rejectRoleConflicts(ctx, tx, p.TenantID, req); err != nil {
			return err
		}
		if err := updateWinner(ctx, tx, p.TenantID, req.WinnerID, resolved); err != nil {
			return err
		}
		movedRoles, err := moveRoles(ctx, tx, p.TenantID, req)
		if err != nil {
			return err
		}
		tag, err := tx.Exec(ctx, `update customer_role set invoice_account_party_id = $3
			where tenant_id = $1 and invoice_account_party_id = $2`, p.TenantID, req.LoserID, req.WinnerID)
		if err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `update party set status = 'merged', merged_into_id = $3, version = version + 1, updated_at = now()
			where tenant_id = $1 and id = $2`, p.TenantID, req.LoserID, req.WinnerID); err != nil {
			return err
		}
		res = MergeResult{MergeID: ids.New(), RolesMoved: len(movedRoles), ReferencesRewritten: int(tag.RowsAffected()), Resolved: resolved}
		resolution, err := json.Marshal(resolved)
		if err != nil {
			return err
		}
		var mergedAt time.Time
		if err := tx.QueryRow(ctx, `insert into party_merge (tenant_id, id, survivor_party_id, merged_party_id, field_resolution, roles_moved, references_rewritten)
			values ($1, $2, $3, $4, $5, $6, $7) returning merged_at`, p.TenantID, res.MergeID, req.WinnerID, req.LoserID, resolution, res.RolesMoved, res.ReferencesRewritten).Scan(&mergedAt); err != nil {
			return err
		}
		if err := enqueueMerged(ctx, tx, p.TenantID, req, res, movedRoles, mergedAt.UTC()); err != nil {
			return err
		}
		if s.beforeCommit != nil {
			return s.beforeCommit()
		}
		return nil
	})
	return res, err
}

func lockParties(ctx context.Context, tx pgx.Tx, tenantID string, partyIDs ...string) (map[string]*partyRow, error) {
	cols := make([]string, len(domain.MergeFields))
	for i, f := range domain.MergeFields {
		cols[i] = "coalesce(" + f + ", '')"
	}
	rows, err := tx.Query(ctx, `select id::text, party_type, status, updated_at, `+strings.Join(cols, ", ")+`
		from party where tenant_id = $1 and id = any($2::uuid[]) order by id for update`, tenantID, partyIDs)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	found := map[string]*partyRow{}
	for rows.Next() {
		r := &partyRow{values: map[string]string{}}
		fields := make([]string, len(domain.MergeFields))
		dest := []any{&r.id, &r.partyType, &r.status, &r.updatedAt}
		for i := range fields {
			dest = append(dest, &fields[i])
		}
		if err := rows.Scan(dest...); err != nil {
			return nil, err
		}
		for i, f := range domain.MergeFields {
			r.values[f] = fields[i]
		}
		found[r.id] = r
	}
	return found, rows.Err()
}

func rejectRoleConflicts(ctx context.Context, tx pgx.Tx, tenantID string, req MergeRequest) error {
	var conflicts int
	err := tx.QueryRow(ctx, `select
		(select count(*) from customer_role l join customer_role w
			on w.tenant_id = l.tenant_id and w.legal_entity_id = l.legal_entity_id
			where l.tenant_id = $1 and l.party_id = $2 and w.party_id = $3)
		+ (select count(*) from vendor_role l join vendor_role w
			on w.tenant_id = l.tenant_id and w.legal_entity_id = l.legal_entity_id
			where l.tenant_id = $1 and l.party_id = $2 and w.party_id = $3)`, tenantID, req.LoserID, req.WinnerID).Scan(&conflicts)
	if err != nil {
		return err
	}
	if conflicts > 0 {
		return fmt.Errorf("%w: %d legal entities", ErrRoleConflict, conflicts)
	}
	return nil
}

func updateWinner(ctx context.Context, tx pgx.Tx, tenantID, winnerID string, resolved map[string]string) error {
	sets := make([]string, 0, len(domain.MergeFields)+1)
	args := []any{tenantID, winnerID}
	for _, f := range domain.MergeFields {
		args = append(args, resolved[f])
		sets = append(sets, fmt.Sprintf("%s = nullif($%d, '')", f, len(args)))
	}
	args = append(args, domain.Normalize(resolved["name"]))
	sets = append(sets, fmt.Sprintf("search_name = $%d", len(args)))
	_, err := tx.Exec(ctx, `update party set `+strings.Join(sets, ", ")+`, version = version + 1, updated_at = now()
		where tenant_id = $1 and id = $2`, args...)
	return err
}

func moveRoles(ctx context.Context, tx pgx.Tx, tenantID string, req MergeRequest) ([]domain.RoleIDMapping, error) {
	var moved []domain.RoleIDMapping
	for _, role := range []struct{ name, table string }{{domain.RoleCustomer, "customer_role"}, {domain.RoleVendor, "vendor_role"}} {
		rows, err := tx.Query(ctx, `update `+role.table+` set party_id = $3
			where tenant_id = $1 and party_id = $2 returning id::text`, tenantID, req.LoserID, req.WinnerID)
		if err != nil {
			return nil, err
		}
		for rows.Next() {
			var id string
			if err := rows.Scan(&id); err != nil {
				rows.Close()
				return nil, err
			}
			moved = append(moved, domain.RoleIDMapping{Role: role.name, FromID: id, ToID: id})
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			return nil, err
		}
	}
	return moved, nil
}

func enqueueMerged(ctx context.Context, tx pgx.Tx, tenantID string, req MergeRequest, res MergeResult, roleMap []domain.RoleIDMapping, mergedAt time.Time) error {
	evt, err := events.New(domain.PartyMergedType, domain.EventSource, req.WinnerID, tenantID, domain.PartyMerged{
		MergeID: res.MergeID, SurvivorPartyID: req.WinnerID, MergedPartyIDs: []string{req.LoserID}, RoleIDMap: roleMap, MergedAt: mergedAt,
	})
	if err != nil {
		return err
	}
	return outbox.Enqueue(ctx, tx, domain.AggregateParty, req.WinnerID, evt)
}
