package identity

import (
	"errors"
	"net/http"
	"regexp"
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
	codeLegalEntityNotFound = "IDN-LEGAL-ENTITY-NOT-FOUND"
	codeLegalEntityExists   = "IDN-LEGAL-ENTITY-EXISTS"
)

var legalEntityCodePattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_-]{0,31}$`)

type LegalEntitiesAPI struct {
	pool *pgxpool.Pool
}

func NewLegalEntitiesAPI(pool *pgxpool.Pool) *LegalEntitiesAPI {
	return &LegalEntitiesAPI{pool: pool}
}

func (a *LegalEntitiesAPI) Register(r chi.Router) {
	guard := requireAnyDuty(dutyMaintainLegalEntities)
	r.With(guard).Post("/v1/legal-entities", a.create)
	r.With(guard).Get("/v1/legal-entities", a.list)
	r.With(guard).Get("/v1/legal-entities/{id}", a.get)
}

type legalEntityResource struct {
	LegalEntityID string    `json:"legalEntityId"`
	Code          string    `json:"code"`
	DisplayName   string    `json:"displayName"`
	Status        string    `json:"status"`
	CreatedAt     time.Time `json:"createdAt"`
}

const legalEntityColumns = `legal_entity_id::text, code, display_name, status, created_at`

func scanLegalEntity(row pgx.CollectableRow) (legalEntityResource, error) {
	var e legalEntityResource
	err := row.Scan(&e.LegalEntityID, &e.Code, &e.DisplayName, &e.Status, &e.CreatedAt)
	return e, err
}

type createLegalEntityRequest struct {
	Code        string `json:"code"`
	DisplayName string `json:"displayName"`
}

func (a *LegalEntitiesAPI) create(w http.ResponseWriter, r *http.Request) {
	p, ok := actingPrincipal(w, r)
	if !ok {
		return
	}
	if !dbx.TenantWide(p, dutyMaintainLegalEntities) {
		httpx.Fail(w, r, httpx.NewProblem(http.StatusForbidden, codeForbidden, "creating a legal entity requires a tenant-level request without a legal-entity scope"))
		return
	}
	var req createLegalEntityRequest
	if err := httpx.Decode(r, &req); err != nil {
		httpx.Fail(w, r, err)
		return
	}
	req.DisplayName = strings.TrimSpace(req.DisplayName)
	fields := map[string]string{}
	if !legalEntityCodePattern.MatchString(req.Code) {
		fields["code"] = "must be 1-32 letters, digits, hyphens or underscores"
	}
	if req.DisplayName == "" || len(req.DisplayName) > maxDisplayName {
		fields["displayName"] = "must be 1-200 characters"
	}
	if len(fields) > 0 {
		invalidFields(w, r, "invalid legal entity", fields)
		return
	}

	created := legalEntityResource{}
	err := withAdminTx(r.Context(), a.pool, p, dutyMaintainLegalEntities, func(tx pgx.Tx) error {
		rows, err := tx.Query(r.Context(), `insert into identity_legal_entities (tenant_id, legal_entity_id, code, display_name)
values ($1, $2, $3, $4) returning `+legalEntityColumns, p.TenantID, ids.New(), req.Code, req.DisplayName)
		if err != nil {
			return err
		}
		if created, err = pgx.CollectExactlyOneRow(rows, scanLegalEntity); err != nil {
			return err
		}
		return record(r.Context(), tx, p.TenantID, p.UserID, "legalentity", created.LegalEntityID, "created", created)
	})
	if pgCode(err) == pgUnique {
		httpx.Fail(w, r, httpx.NewProblem(http.StatusConflict, codeLegalEntityExists, "a legal entity with this code already exists"))
		return
	}
	if err != nil {
		failWrite(w, r, err)
		return
	}
	w.Header().Set("Location", "/v1/legal-entities/"+created.LegalEntityID)
	httpx.JSON(w, http.StatusCreated, created)
}

func (a *LegalEntitiesAPI) list(w http.ResponseWriter, r *http.Request) {
	p, ok := principal(w, r)
	if !ok {
		return
	}
	fields := map[string]string{}
	limit, after := pageParams(r, fields)
	if len(fields) > 0 {
		invalidFields(w, r, "invalid paging parameters", fields)
		return
	}
	var items []legalEntityResource
	err := dbx.WithTenantTx(r.Context(), a.pool, func(tx pgx.Tx) error {
		rows, err := tx.Query(r.Context(), `select `+legalEntityColumns+` from identity_legal_entities
where tenant_id = $1 and ($2::text is null or legal_entity_id > $2::uuid) order by legal_entity_id limit $3`, p.TenantID, after, limit+1)
		if err != nil {
			return err
		}
		items, err = pgx.CollectRows(rows, scanLegalEntity)
		return err
	})
	if err != nil {
		httpx.Fail(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, paged(items, limit, func(e legalEntityResource) string { return e.LegalEntityID }))
}

func (a *LegalEntitiesAPI) get(w http.ResponseWriter, r *http.Request) {
	p, ok := principal(w, r)
	if !ok {
		return
	}
	id := chi.URLParam(r, "id")
	if !ids.Valid(id) {
		invalidFields(w, r, "legal entity id must be a UUID", map[string]string{"id": "must be a UUID"})
		return
	}
	var found legalEntityResource
	err := dbx.WithTenantTx(r.Context(), a.pool, func(tx pgx.Tx) error {
		rows, err := tx.Query(r.Context(), `select `+legalEntityColumns+` from identity_legal_entities where tenant_id = $1 and legal_entity_id = $2`, p.TenantID, id)
		if err != nil {
			return err
		}
		found, err = pgx.CollectExactlyOneRow(rows, scanLegalEntity)
		return err
	})
	if errors.Is(err, pgx.ErrNoRows) {
		httpx.Fail(w, r, httpx.NewProblem(http.StatusNotFound, codeLegalEntityNotFound, "legal entity not found"))
		return
	}
	if err != nil {
		httpx.Fail(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, found)
}
