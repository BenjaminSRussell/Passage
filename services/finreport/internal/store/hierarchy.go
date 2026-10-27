package store

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/BenjaminSRussell/passage/platform/dbx"
	"github.com/BenjaminSRussell/passage/platform/ids"

	"github.com/BenjaminSRussell/passage/services/finreport/internal/domain"
	"github.com/BenjaminSRussell/passage/services/finreport/internal/events"
)

// CodeHierarchyNotFound is returned when a hierarchy, its draft or a version
// does not exist. It follows the REPORT-0020 (definition not found) numbering.
const CodeHierarchyNotFound = "REPORT-0027"

// HierarchySummary is one hierarchy code in the list: its newest name and
// subject, the newest published version (0 when never published), whether a
// draft is open, and the leaf and unassigned counts of that published version.
type HierarchySummary struct {
	Code            string    `json:"code"`
	Name            string    `json:"name"`
	Subject         string    `json:"subject"`
	Strict          bool      `json:"strict"`
	LatestVersion   int       `json:"latestVersion"`
	HasDraft        bool      `json:"hasDraft"`
	AssignedCount   int       `json:"assignedCount"`
	UnassignedCount int       `json:"unassignedCount"`
	UpdatedAt       time.Time `json:"updatedAt"`
}

// ListHierarchies returns one summary per hierarchy code, ordered by code.
func (s *Postgres) ListHierarchies(ctx context.Context) ([]HierarchySummary, error) {
	out := []HierarchySummary{}
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `select code,
				(array_agg(name order by (status = 'draft') desc, version desc))[1],
				(array_agg(subject order by (status = 'draft') desc, version desc))[1],
				(array_agg(strict order by (status = 'draft') desc, version desc))[1],
				coalesce(max(version) filter (where status = 'published'), 0),
				bool_or(status = 'draft'),
				coalesce((array_agg(membership order by version desc) filter (where status = 'published'))[1], '{}'::jsonb),
				max(updated_at)
			from report_hierarchy group by code order by code`)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var h HierarchySummary
			var raw []byte
			if err := rows.Scan(&h.Code, &h.Name, &h.Subject, &h.Strict, &h.LatestVersion, &h.HasDraft, &raw, &h.UpdatedAt); err != nil {
				return err
			}
			var m domain.Membership
			if err := json.Unmarshal(raw, &m); err != nil {
				return err
			}
			h.AssignedCount, h.UnassignedCount = len(m.Assigned), len(m.Unassigned)
			out = append(out, h)
		}
		return rows.Err()
	})
	return out, err
}

// SaveHierarchy creates or replaces the draft of h.Code. The revision rule is
// the one of SaveDraft: once a draft exists, If-Match must carry the revision
// read (428 when missing, 412 conflict_version when stale), and the new
// revision is returned as the ETag. When the body names no owner, the code
// keeps its current owner, and a new code is owned by owner (the saving user);
// the owner is notified about unassigned members (FR-15.13).
func (s *Postgres) SaveHierarchy(ctx context.Context, h domain.Hierarchy, ifMatch *int, owner string) (domain.Hierarchy, int, error) {
	h.Version = 0
	h.Status = "draft"
	h.SchemaVersion = domain.SchemaVersion
	var revision int
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		if h.OwnerUserID == "" {
			err := tx.QueryRow(ctx, `select owner_user_id from report_hierarchy where code=$1 and owner_user_id<>''
				order by (status = 'draft') desc, version desc limit 1`, h.Code).Scan(&h.OwnerUserID)
			if errors.Is(err, pgx.ErrNoRows) {
				h.OwnerUserID = owner
			} else if err != nil {
				return err
			}
		}
		body, err := json.Marshal(h)
		if err != nil {
			return err
		}
		var current int
		err = tx.QueryRow(ctx, `select revision from report_hierarchy where code=$1 and status='draft' for update`, h.Code).Scan(&current)
		switch {
		case errors.Is(err, pgx.ErrNoRows):
			revision = 1
			_, err = tx.Exec(ctx, `insert into report_hierarchy(tenant_id, id, code, version, status, revision, name, subject, strict, body, owner_user_id, updated_at)
				values($1,$2,$3,0,'draft',1,$4,$5,$6,$7,$8,now())`, tenantID(ctx), ids.New(), h.Code, h.Name, string(h.Subject), h.Strict, body, h.OwnerUserID)
			if isUniqueViolation(err) {
				return problem(409, CodeRevisionConflict, "a draft for "+h.Code+" was created concurrently")
			}
			return err
		case err != nil:
			return err
		}
		if ifMatch == nil {
			return problem(428, "precondition_required", "If-Match is required to replace the draft of "+h.Code)
		}
		if *ifMatch != current {
			return problem(412, CodeRevisionConflict, fmt.Sprintf("draft revision is %d, not %d", current, *ifMatch))
		}
		revision = current + 1
		_, err = tx.Exec(ctx, `update report_hierarchy set name=$2, subject=$3, strict=$4, body=$5, revision=$6, owner_user_id=$7, updated_at=now()
			where code=$1 and status='draft'`, h.Code, h.Name, string(h.Subject), h.Strict, body, revision, h.OwnerUserID)
		return err
	})
	return h, revision, err
}

// GetHierarchy returns version (Draft, Latest or an exact published version)
// and the draft revision (0 for published versions).
func (s *Postgres) GetHierarchy(ctx context.Context, code string, version int) (domain.Hierarchy, int, error) {
	var h domain.Hierarchy
	var body []byte
	var revision int
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		switch version {
		case Draft:
			return tx.QueryRow(ctx, `select body, revision from report_hierarchy where code=$1 and status='draft'`, code).Scan(&body, &revision)
		case Latest:
			return tx.QueryRow(ctx, `select body, 0 from report_hierarchy where code=$1 and status='published' order by version desc limit 1`, code).Scan(&body, &revision)
		default:
			return tx.QueryRow(ctx, `select body, 0 from report_hierarchy where code=$1 and status='published' and version=$2`, code, version).Scan(&body, &revision)
		}
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return h, 0, problem(404, CodeHierarchyNotFound, fmt.Sprintf("hierarchy %s has no %s", code, versionLabel(version)))
	}
	if err != nil {
		return h, 0, err
	}
	return h, revision, json.Unmarshal(body, &h)
}

func versionLabel(version int) string {
	switch {
	case version == Draft:
		return "draft"
	case version > 0:
		return fmt.Sprintf("version %d", version)
	default:
		return "published version"
	}
}

// PublishHierarchy turns the draft into the next immutable version and stores
// its materialized membership over members (the accounts, categories or
// dimension values the caller classifies). Overlaps (REPORT-0014) and, for
// strict hierarchies, unassigned members (REPORT-0013) refuse the publish. The
// unassigned members of a non-strict hierarchy each raise
// hierarchy.unassigned.v1 in the same transaction (BR-15.13).
func (s *Postgres) PublishHierarchy(ctx context.Context, code, actor string, members []domain.Member) (domain.Hierarchy, domain.Membership, error) {
	var out domain.Hierarchy
	var mem domain.Membership
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		var draftID string
		var body []byte
		err := tx.QueryRow(ctx, `select id::text, body from report_hierarchy where code=$1 and status='draft' for update`, code).Scan(&draftID, &body)
		if errors.Is(err, pgx.ErrNoRows) {
			return problem(404, CodeHierarchyNotFound, "hierarchy "+code+" has no draft to publish")
		}
		if err != nil {
			return err
		}
		if err := json.Unmarshal(body, &out); err != nil {
			return err
		}
		if issues := out.Validate(); len(issues) > 0 {
			return problem(422, issues[0].Code, issues[0].Field+": "+issues[0].Detail)
		}
		m, err := out.Materialize(members)
		if err != nil {
			return asProblem(err)
		}
		if err := out.CheckPublish(m); err != nil {
			return asProblem(err)
		}
		var next int
		if err := tx.QueryRow(ctx, `select coalesce(max(version),0)+1 from report_hierarchy where code=$1 and status<>'draft'`, code).Scan(&next); err != nil {
			return err
		}
		out.Version = next
		out.Status = "published"
		out.SchemaVersion = domain.SchemaVersion
		published, err := json.Marshal(out)
		if err != nil {
			return err
		}
		membership, err := json.Marshal(m)
		if err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `update report_hierarchy set version=$2, status='published', body=$3, membership=$4, published_at=now(), updated_at=now()
			where id=$1::uuid`, draftID, next, published, membership); err != nil {
			return err
		}
		for _, id := range m.Unassigned {
			if err := enqueue(ctx, tx, "hierarchy", code, events.HierarchyUnassigned, tenantID(ctx), events.HierarchyUnassignedData{
				Code: code, Version: next, MemberID: id, MemberKind: string(out.Subject), OwnerUserID: out.OwnerUserID, Actor: actor,
			}); err != nil {
				return err
			}
		}
		mem = m
		return nil
	})
	return out, mem, err
}

// HierarchyMembership returns the materialized membership of a published
// version (Latest or an exact version) and that version's number. Drafts have
// no membership.
func (s *Postgres) HierarchyMembership(ctx context.Context, code string, version int) (domain.Membership, int, error) {
	var m domain.Membership
	var raw []byte
	var got int
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		if version == Latest {
			return tx.QueryRow(ctx, `select membership, version from report_hierarchy where code=$1 and status='published' order by version desc limit 1`, code).Scan(&raw, &got)
		}
		return tx.QueryRow(ctx, `select membership, version from report_hierarchy where code=$1 and status='published' and version=$2`, code, version).Scan(&raw, &got)
	})
	if errors.Is(err, pgx.ErrNoRows) || (err == nil && raw == nil) {
		return m, 0, problem(404, CodeHierarchyNotFound, fmt.Sprintf("hierarchy %s has no %s", code, versionLabel(version)))
	}
	if err != nil {
		return m, 0, err
	}
	return m, got, json.Unmarshal(raw, &m)
}

// asProblem maps a domain rule violation to a 422 problem with its catalog code.
func asProblem(err error) error {
	var de *domain.Error
	if errors.As(err, &de) {
		return problem(422, de.Code, de.Detail)
	}
	return err
}
