package identity

import (
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/BenjaminSRussell/passage/platform/dbx"
	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/platform/ids"
)

const (
	codeAssignmentNotFound = "IDN-ASSIGNMENT-NOT-FOUND"
	codeAssignmentOverlap  = "IDN-ASSIGNMENT-OVERLAP"
	codeAssignmentEnded    = "IDN-ASSIGNMENT-ENDED"
	codeUserDeprovisioned  = "IDN-USER-DEPROVISIONED"
)

// RoleAssignmentsAPI manages effective-dated role assignments. A role holds a duty name until the role
// catalogue exists, so assigned roles are validated against the seeded duty catalogue.
type RoleAssignmentsAPI struct {
	pool      *pgxpool.Pool
	catalogue *Catalogue
}

func NewRoleAssignmentsAPI(pool *pgxpool.Pool, catalogue *Catalogue) *RoleAssignmentsAPI {
	return &RoleAssignmentsAPI{pool: pool, catalogue: catalogue}
}

func (a *RoleAssignmentsAPI) Register(r chi.Router) {
	guard := requireAnyDuty(dutyMaintainSecurityRoles)
	r.With(guard).Post("/v1/authz/assignments", a.assign)
	r.With(guard).Get("/v1/authz/assignments", a.list)
	r.With(guard).Get("/v1/authz/assignments/{id}", a.get)
	r.With(guard).Post("/v1/authz/assignments/{id}/end", a.end)
}

type roleAssignmentResource struct {
	AssignmentID  string     `json:"assignmentId"`
	UserID        string     `json:"userId"`
	Role          string     `json:"role"`
	LegalEntityID string     `json:"legalEntityId,omitempty"`
	ValidFrom     time.Time  `json:"validFrom"`
	ValidTo       *time.Time `json:"validTo,omitempty"`
	CreatedAt     time.Time  `json:"createdAt"`
}

const assignmentColumns = `assignment_id::text, user_id::text, role, coalesce(legal_entity_id::text, ''), valid_from, valid_to, created_at`

func scanAssignment(row pgx.CollectableRow) (roleAssignmentResource, error) {
	var a roleAssignmentResource
	err := row.Scan(&a.AssignmentID, &a.UserID, &a.Role, &a.LegalEntityID, &a.ValidFrom, &a.ValidTo, &a.CreatedAt)
	return a, err
}

type assignRequest struct {
	UserID        string     `json:"userId"`
	Role          string     `json:"role"`
	LegalEntityID string     `json:"legalEntityId"`
	ValidFrom     *time.Time `json:"validFrom"`
	ValidTo       *time.Time `json:"validTo"`
}

func (a *RoleAssignmentsAPI) assign(w http.ResponseWriter, r *http.Request) {
	p, ok := actingPrincipal(w, r)
	if !ok {
		return
	}
	var req assignRequest
	if err := httpx.Decode(r, &req); err != nil {
		httpx.Fail(w, r, err)
		return
	}
	fields := map[string]string{}
	if !ids.Valid(req.UserID) {
		fields["userId"] = "must be a UUID"
	}
	if req.LegalEntityID != "" && !ids.Valid(req.LegalEntityID) {
		fields["legalEntityId"] = "must be a UUID"
	}
	if !a.catalogue.HasDuty(req.Role) {
		fields["role"] = "must be the name of a seeded duty"
	}
	if req.ValidFrom != nil && req.ValidTo != nil && !req.ValidTo.After(*req.ValidFrom) {
		fields["validTo"] = "must be after validFrom"
	}
	if len(fields) > 0 {
		invalidFields(w, r, "invalid role assignment", fields)
		return
	}
	if req.LegalEntityID == "" && !allLegalEntities(p) {
		httpx.Fail(w, r, httpx.NewProblem(http.StatusForbidden, codeForbidden, "a tenant-wide assignment requires access to all legal entities"))
		return
	}

	created := roleAssignmentResource{}
	err := dbx.WithTenantTx(r.Context(), a.pool, func(tx pgx.Tx) error {
		var status string
		err := tx.QueryRow(r.Context(), `select status from identity_users where tenant_id = $1 and user_id = $2`, p.TenantID, req.UserID).Scan(&status)
		if errors.Is(err, pgx.ErrNoRows) {
			return httpx.NewProblem(http.StatusNotFound, codeUserNotFound, "user not found")
		}
		if err != nil {
			return err
		}
		if status != "active" {
			return httpx.NewProblem(http.StatusConflict, codeUserDeprovisioned, "user is deprovisioned")
		}
		if req.LegalEntityID != "" {
			var exists bool
			if err := tx.QueryRow(r.Context(), `select exists(select 1 from identity_legal_entities where tenant_id = $1 and legal_entity_id = $2)`, p.TenantID, req.LegalEntityID).Scan(&exists); err != nil {
				return err
			}
			if !exists {
				return httpx.NewProblem(http.StatusNotFound, codeLegalEntityNotFound, "legal entity not found")
			}
		}
		rows, err := tx.Query(r.Context(), `insert into identity_role_assignments (tenant_id, assignment_id, user_id, role, legal_entity_id, valid_from, valid_to)
values ($1, $2, $3, $4, nullif($5, '')::uuid, coalesce($6::timestamptz, now()), $7::timestamptz) returning `+assignmentColumns,
			p.TenantID, ids.New(), req.UserID, req.Role, req.LegalEntityID, req.ValidFrom, req.ValidTo)
		if err != nil {
			return err
		}
		if created, err = pgx.CollectExactlyOneRow(rows, scanAssignment); err != nil {
			return err
		}
		return record(r.Context(), tx, p.TenantID, p.UserID, "roleassignment", created.AssignmentID, "assigned", created)
	})
	switch pgCode(err) {
	case pgExclusion:
		httpx.Fail(w, r, httpx.NewProblem(http.StatusConflict, codeAssignmentOverlap, "the user already holds this role for this legal entity during that period"))
		return
	case pgCheck:
		invalidFields(w, r, "invalid role assignment", map[string]string{"validTo": "must be after validFrom"})
		return
	}
	if err != nil {
		failWrite(w, r, err)
		return
	}
	w.Header().Set("Location", "/v1/authz/assignments/"+created.AssignmentID)
	httpx.JSON(w, http.StatusCreated, created)
}

func (a *RoleAssignmentsAPI) list(w http.ResponseWriter, r *http.Request) {
	p, ok := principal(w, r)
	if !ok {
		return
	}
	q := r.URL.Query()
	userID, role, activeOnly := q.Get("userId"), strings.TrimSpace(q.Get("role")), q.Get("active") == "true"
	fields := map[string]string{}
	limit, after := pageParams(r, fields)
	if userID == "" && role == "" {
		fields["userId"] = "userId or role is required"
	} else if userID != "" && !ids.Valid(userID) {
		fields["userId"] = "must be a UUID"
	}
	if raw := q.Get("active"); raw != "" && raw != "true" && raw != "false" {
		fields["active"] = "must be true or false"
	}
	if len(fields) > 0 {
		invalidFields(w, r, "invalid role assignment filter", fields)
		return
	}
	var items []roleAssignmentResource
	err := dbx.WithTenantTx(r.Context(), a.pool, func(tx pgx.Tx) error {
		rows, err := tx.Query(r.Context(), `select `+assignmentColumns+` from identity_role_assignments
where tenant_id = $1 and ($2::text = '' or user_id = nullif($2::text, '')::uuid) and ($3::text = '' or role = $3::text)
  and (not $4 or (valid_from <= now() and (valid_to is null or valid_to > now())))
  and ($5::text is null or assignment_id > $5::uuid)
order by assignment_id limit $6`, p.TenantID, userID, role, activeOnly, after, limit+1)
		if err != nil {
			return err
		}
		items, err = pgx.CollectRows(rows, scanAssignment)
		return err
	})
	if err != nil {
		httpx.Fail(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, paged(items, limit, func(x roleAssignmentResource) string { return x.AssignmentID }))
}

func (a *RoleAssignmentsAPI) get(w http.ResponseWriter, r *http.Request) {
	p, ok := principal(w, r)
	if !ok {
		return
	}
	id := chi.URLParam(r, "id")
	if !ids.Valid(id) {
		invalidFields(w, r, "assignment id must be a UUID", map[string]string{"id": "must be a UUID"})
		return
	}
	var found roleAssignmentResource
	err := dbx.WithTenantTx(r.Context(), a.pool, func(tx pgx.Tx) error {
		rows, err := tx.Query(r.Context(), `select `+assignmentColumns+` from identity_role_assignments where tenant_id = $1 and assignment_id = $2`, p.TenantID, id)
		if err != nil {
			return err
		}
		found, err = pgx.CollectExactlyOneRow(rows, scanAssignment)
		return err
	})
	if errors.Is(err, pgx.ErrNoRows) {
		httpx.Fail(w, r, httpx.NewProblem(http.StatusNotFound, codeAssignmentNotFound, "assignment not found"))
		return
	}
	if err != nil {
		httpx.Fail(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, found)
}

type endRequest struct {
	ValidTo *time.Time `json:"validTo"`
}

func (a *RoleAssignmentsAPI) end(w http.ResponseWriter, r *http.Request) {
	p, ok := actingPrincipal(w, r)
	if !ok {
		return
	}
	id := chi.URLParam(r, "id")
	if !ids.Valid(id) {
		invalidFields(w, r, "assignment id must be a UUID", map[string]string{"id": "must be a UUID"})
		return
	}
	var req endRequest
	if r.ContentLength != 0 {
		if err := httpx.Decode(r, &req); err != nil {
			httpx.Fail(w, r, err)
			return
		}
	}

	var ended roleAssignmentResource
	err := dbx.WithTenantTx(r.Context(), a.pool, func(tx pgx.Tx) error {
		var validFrom, now time.Time
		var currentTo *time.Time
		err := tx.QueryRow(r.Context(), `select valid_from, valid_to, now() from identity_role_assignments where tenant_id = $1 and assignment_id = $2 for update`,
			p.TenantID, id).Scan(&validFrom, &currentTo, &now)
		if errors.Is(err, pgx.ErrNoRows) {
			return httpx.NewProblem(http.StatusNotFound, codeAssignmentNotFound, "assignment not found")
		}
		if err != nil {
			return err
		}
		if currentTo != nil && !currentTo.After(now) {
			return httpx.NewProblem(http.StatusConflict, codeAssignmentEnded, "assignment has already ended")
		}
		validTo := now
		if req.ValidTo != nil {
			validTo = *req.ValidTo
		}
		fields := map[string]string{}
		switch {
		case validTo.Before(now):
			fields["validTo"] = "must not be in the past"
		case !validTo.After(validFrom):
			fields["validTo"] = "must be after validFrom"
		case currentTo != nil && validTo.After(*currentTo):
			fields["validTo"] = "must not extend the current end date"
		}
		if len(fields) > 0 {
			problem := httpx.NewProblem(http.StatusBadRequest, codeInvalid, "invalid end date")
			problem.Errors = fields
			return problem
		}
		rows, err := tx.Query(r.Context(), `update identity_role_assignments set valid_to = $3 where tenant_id = $1 and assignment_id = $2 returning `+assignmentColumns,
			p.TenantID, id, validTo)
		if err != nil {
			return err
		}
		if ended, err = pgx.CollectExactlyOneRow(rows, scanAssignment); err != nil {
			return err
		}
		return record(r.Context(), tx, p.TenantID, p.UserID, "roleassignment", id, "ended", ended)
	})
	if err != nil {
		failWrite(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, ended)
}
