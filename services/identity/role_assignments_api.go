package identity

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/BenjaminSRussell/passage/platform/dbx"
	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/platform/tenant"
)

const (
	codeAssignmentNotFound  = "IDN-ASSIGNMENT-NOT-FOUND"
	codeAssignmentOverlap   = "IDN-ASSIGNMENT-OVERLAP"
	codeAssignmentEnded     = "IDN-ASSIGNMENT-ENDED"
	codeUserDeprovisioned   = "IDN-USER-DEPROVISIONED"
	codeLegalEntityInactive = "IDN-LEGAL-ENTITY-INACTIVE"
	codeSelfGrant           = "IAM-0416"
	codeLastTenantAdmin     = "IDN-LAST-TENANT-ADMIN"

	maxReason = 500
)

// RoleAssignmentsAPI manages effective-dated role assignments. A role holds a duty name until the role
// catalogue exists, so assigned roles are validated against the seeded duty catalogue.
type RoleAssignmentsAPI struct {
	pool             *pgxpool.Pool
	catalogue        *Catalogue
	operatorTenantID string
}

func NewRoleAssignmentsAPI(pool *pgxpool.Pool, catalogue *Catalogue, operatorTenantID string) *RoleAssignmentsAPI {
	return &RoleAssignmentsAPI{pool: pool, catalogue: catalogue, operatorTenantID: operatorTenantID}
}

func (a *RoleAssignmentsAPI) Register(r chi.Router) {
	guard := requireAnyDuty(dutyMaintainSecurityRoles)
	r.With(guard).Post("/v1/authz/assignments", a.assign)
	r.With(guard).Get("/v1/authz/assignments", a.list)
	r.With(guard).Get("/v1/authz/assignments/{id}", a.get)
	r.With(guard).Post("/v1/authz/assignments/{id}/end", a.end)
	r.With(guard).Delete("/v1/authz/assignments/{id}", a.end)
}

type roleAssignmentResource struct {
	AssignmentID  string     `json:"assignmentId"`
	UserID        string     `json:"userId"`
	Role          string     `json:"role"`
	LegalEntityID string     `json:"legalEntityId,omitempty"`
	ValidFrom     time.Time  `json:"validFrom"`
	ValidTo       *time.Time `json:"validTo,omitempty"`
	Reason        string     `json:"reason,omitempty"`
	GrantedBy     string     `json:"grantedBy,omitempty"`
	CreatedAt     time.Time  `json:"createdAt"`
}

const assignmentColumns = `assignment_id::text, user_id::text, role, coalesce(legal_entity_id::text, ''), valid_from, valid_to, coalesce(reason, ''), coalesce(granted_by, ''), created_at`

func scanAssignment(row pgx.CollectableRow) (roleAssignmentResource, error) {
	var a roleAssignmentResource
	err := row.Scan(&a.AssignmentID, &a.UserID, &a.Role, &a.LegalEntityID, &a.ValidFrom, &a.ValidTo, &a.Reason, &a.GrantedBy, &a.CreatedAt)
	return a, err
}

type assignRequest struct {
	UserID        string     `json:"userId"`
	Role          string     `json:"role"`
	LegalEntityID string     `json:"legalEntityId"`
	ValidFrom     *time.Time `json:"validFrom"`
	ValidTo       *time.Time `json:"validTo"`
	Reason        string     `json:"reason"`
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
	if userID, ok := canonicalUUID(req.UserID); ok {
		req.UserID = userID
	} else {
		fields["userId"] = "must be a UUID"
	}
	if req.LegalEntityID != "" && !validUUID(req.LegalEntityID) {
		fields["legalEntityId"] = "must be a UUID"
	}
	if !a.catalogue.HasDuty(req.Role) {
		fields["role"] = "must be the name of a seeded duty"
	} else if a.catalogue.ServiceOnly(req.Role) {
		fields["role"] = "is held by a service grant only and cannot be assigned to a user"
	}
	if req.Role == tenant.DutyTenantAdmin && req.LegalEntityID != "" {
		fields["legalEntityId"] = "must be empty: tenant.administer is tenant-wide"
	}
	now := time.Now()
	if req.ValidFrom != nil && req.ValidFrom.Before(now.Add(-clockSkew)) {
		fields["validFrom"] = "must not be in the past"
	}
	if req.ValidTo != nil && !req.ValidTo.After(now) {
		fields["validTo"] = "must be in the future"
	}
	if fields["validTo"] == "" && req.ValidFrom != nil && req.ValidTo != nil && !req.ValidTo.After(*req.ValidFrom) {
		fields["validTo"] = "must be after validFrom"
	}
	req.Reason = strings.TrimSpace(req.Reason)
	if utf8.RuneCountInString(req.Reason) > maxReason {
		fields["reason"] = "must be at most 500 characters"
	}
	if len(fields) > 0 {
		invalidFields(w, r, "invalid role assignment", fields)
		return
	}
	if req.Role == tenant.DutyTenantAdmin && !tenant.IsTenantAdmin(p) {
		httpx.Fail(w, r, httpx.NewProblem(http.StatusForbidden, codeForbidden, "tenant.administer can only be granted by a tenant administrator without a legal-entity scope"))
		return
	}
	wide := dbx.TenantWide(p, dutyMaintainSecurityRoles)
	if req.LegalEntityID == "" && !wide {
		httpx.Fail(w, r, httpx.NewProblem(http.StatusForbidden, codeForbidden, "a tenant-wide assignment requires the tenant.administer duty and a request without a legal-entity scope"))
		return
	}
	if req.Role == dutyMaintainTenants && (a.operatorTenantID == "" || p.TenantID != a.operatorTenantID) {
		httpx.Fail(w, r, httpx.NewProblem(http.StatusForbidden, codeForbidden, "Maintain tenants can only be granted in the operator tenant"))
		return
	}

	created := roleAssignmentResource{}
	err := withAdminTx(r.Context(), a.pool, p, dutyMaintainSecurityRoles, func(tx pgx.Tx) error {
		var status, idpSubject string
		err := tx.QueryRow(r.Context(), `select status, idp_subject from identity_users where tenant_id = $1 and user_id = $2`, p.TenantID, req.UserID).Scan(&status, &idpSubject)
		if errors.Is(err, pgx.ErrNoRows) {
			return httpx.NewProblem(http.StatusNotFound, codeUserNotFound, "user not found")
		}
		if err != nil {
			return err
		}
		if strings.EqualFold(p.UserID, req.UserID) || strings.EqualFold(p.UserID, idpSubject) {
			return httpx.NewProblem(http.StatusForbidden, codeSelfGrant, "You cannot grant a role to yourself")
		}
		if status != "active" {
			return httpx.NewProblem(http.StatusConflict, codeUserDeprovisioned, "user is deprovisioned")
		}
		if req.LegalEntityID != "" {
			var entityStatus string
			err := tx.QueryRow(r.Context(), `select status from identity_legal_entities where tenant_id = $1 and legal_entity_id = $2`, p.TenantID, req.LegalEntityID).Scan(&entityStatus)
			if errors.Is(err, pgx.ErrNoRows) {
				return httpx.NewProblem(http.StatusNotFound, codeLegalEntityNotFound, "legal entity not found")
			}
			if err != nil {
				return err
			}
			if entityStatus != "active" {
				return httpx.NewProblem(http.StatusUnprocessableEntity, codeLegalEntityInactive, "legal entity is inactive")
			}
		}
		rows, err := tx.Query(r.Context(), `insert into identity_role_assignments (tenant_id, assignment_id, user_id, role, legal_entity_id, valid_from, valid_to, reason, granted_by)
values ($1, $2, $3, $4, nullif($5, '')::uuid, coalesce($6::timestamptz, now()), $7::timestamptz, nullif($8, ''), $9) returning `+assignmentColumns,
			p.TenantID, ids.New(), req.UserID, req.Role, req.LegalEntityID, req.ValidFrom, req.ValidTo, req.Reason, p.UserID)
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
	} else if userID != "" && !validUUID(userID) {
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
	if !validUUID(id) {
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
	if !validUUID(id) {
		invalidFields(w, r, "assignment id must be a UUID", map[string]string{"id": "must be a UUID"})
		return
	}
	var req endRequest
	if err := decodeOptional(r, &req); err != nil {
		httpx.Fail(w, r, err)
		return
	}

	var result roleAssignmentResource
	err := withAdminTx(r.Context(), a.pool, p, dutyMaintainSecurityRoles, func(tx pgx.Tx) error {
		rows, err := tx.Query(r.Context(), `select `+assignmentColumns+` from identity_role_assignments where tenant_id = $1 and assignment_id = $2 for update`, p.TenantID, id)
		if err != nil {
			return err
		}
		current, err := pgx.CollectExactlyOneRow(rows, scanAssignment)
		if errors.Is(err, pgx.ErrNoRows) {
			return httpx.NewProblem(http.StatusNotFound, codeAssignmentNotFound, "assignment not found")
		}
		if err != nil {
			return err
		}
		if current.LegalEntityID == "" && !dbx.TenantWide(p, dutyMaintainSecurityRoles) {
			return httpx.NewProblem(http.StatusForbidden, codeForbidden, "ending a tenant-wide assignment requires the tenant.administer duty and a request without a legal-entity scope")
		}
		var now time.Time
		if err := tx.QueryRow(r.Context(), `select now()`).Scan(&now); err != nil {
			return err
		}
		if current.ValidTo != nil && !current.ValidTo.After(now) {
			return httpx.NewProblem(http.StatusConflict, codeAssignmentEnded, "assignment has already ended")
		}
		requested := latest(now, current.ValidFrom)
		if req.ValidTo != nil {
			requested = req.ValidTo.Truncate(time.Microsecond)
			if requested.Before(now) && !requested.Before(now.Add(-clockSkew)) {
				requested = now
			}
		}
		fields := map[string]string{}
		switch {
		case requested.Before(now.Add(-clockSkew)):
			fields["validTo"] = "must not be in the past"
		case requested.Before(current.ValidFrom):
			fields["validTo"] = "must not be before validFrom"
		case current.ValidTo != nil && requested.After(*current.ValidTo):
			fields["validTo"] = "must not extend the current end date"
		}
		if len(fields) > 0 {
			problem := httpx.NewProblem(http.StatusBadRequest, codeInvalid, "invalid end date")
			problem.Errors = fields
			return problem
		}
		validTo := latest(requested, now, current.ValidFrom)
		if current.ValidTo != nil && validTo.Equal(*current.ValidTo) {
			result = current
			return nil
		}
		if current.Role == tenant.DutyTenantAdmin && !current.ValidFrom.After(now) {
			if err := requireOtherTenantAdmin(r.Context(), tx, p.TenantID, id, validTo); err != nil {
				return err
			}
		}
		rows, err = tx.Query(r.Context(), `update identity_role_assignments set valid_to = $3 where tenant_id = $1 and assignment_id = $2 returning `+assignmentColumns,
			p.TenantID, id, validTo)
		if err != nil {
			return err
		}
		if result, err = pgx.CollectExactlyOneRow(rows, scanAssignment); err != nil {
			return err
		}
		return record(r.Context(), tx, p.TenantID, p.UserID, "roleassignment", id, "ended", result)
	})
	if err != nil {
		failWrite(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, result)
}

func requireOtherTenantAdmin(ctx context.Context, tx pgx.Tx, tenantID, endingID string, validTo time.Time) error {
	if _, err := tx.Exec(ctx, `select pg_advisory_xact_lock(hashtextextended($1::text || ':tenant-admin', 0))`, tenantID); err != nil {
		return err
	}
	var others int
	if err := tx.QueryRow(ctx, `select count(*) from identity_role_assignments a join identity_users u using (tenant_id, user_id)
where a.tenant_id = $1 and a.role = $2 and a.legal_entity_id is null and a.assignment_id <> $3 and u.status = 'active'
  and a.valid_from <= clock_timestamp() and (a.valid_to is null or a.valid_to > greatest($4::timestamptz, clock_timestamp()))`,
		tenantID, tenant.DutyTenantAdmin, endingID, validTo).Scan(&others); err != nil {
		return err
	}
	if others == 0 {
		return httpx.NewProblem(http.StatusConflict, codeLastTenantAdmin, "the last active tenant.administer assignment of a tenant cannot be ended")
	}
	return nil
}
