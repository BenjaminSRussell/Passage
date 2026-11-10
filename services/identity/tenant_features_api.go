package identity

import (
	"errors"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/BenjaminSRussell/passage/platform/dbx"
	"github.com/BenjaminSRussell/passage/platform/features"
	"github.com/BenjaminSRussell/passage/platform/httpx"
)

const (
	dutyMaintainFeatures = "identity.feature.maintain"

	codeFeatureDependency = "IDN-FEATURE-DEPENDENCY"
)

type TenantFeaturesAPI struct {
	pool *pgxpool.Pool
}

func NewTenantFeaturesAPI(pool *pgxpool.Pool) *TenantFeaturesAPI {
	return &TenantFeaturesAPI{pool: pool}
}

func (a *TenantFeaturesAPI) Register(r chi.Router) {
	r.Get("/v1/tenant/features", a.get)
	r.With(requireAnyDuty(dutyMaintainFeatures)).Put("/v1/tenant/features", a.put)
}

type featureState struct {
	features.Feature
	Enabled   bool       `json:"enabled"`
	ChangedBy string     `json:"changedBy,omitempty"`
	ChangedAt *time.Time `json:"changedAt,omitempty"`
}

type featuresResponse struct {
	Features  []featureState      `json:"features"`
	Templates []features.Template `json:"templates"`
}

type storedFeature struct {
	enabled   bool
	changedBy string
	changedAt time.Time
}

func loadStoredFeatures(r *http.Request, tx pgx.Tx, tenantID string) (map[string]storedFeature, error) {
	rows, err := tx.Query(r.Context(), `select feature_key, enabled, updated_by, updated_at from identity_tenant_features where tenant_id = $1`, tenantID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	stored := map[string]storedFeature{}
	for rows.Next() {
		var key string
		var s storedFeature
		if err := rows.Scan(&key, &s.enabled, &s.changedBy, &s.changedAt); err != nil {
			return nil, err
		}
		stored[key] = s
	}
	return stored, rows.Err()
}

func enabledOf(stored map[string]storedFeature) map[string]bool {
	out := make(map[string]bool, len(stored))
	for k, s := range stored {
		out[k] = s.enabled
	}
	return features.Resolve(out)
}

func featuresBody(stored map[string]storedFeature) featuresResponse {
	enabled := enabledOf(stored)
	body := featuresResponse{Templates: features.Templates()}
	for _, f := range features.Catalog() {
		state := featureState{Feature: f, Enabled: enabled[f.Key]}
		if s, ok := stored[f.Key]; ok {
			at := s.changedAt
			state.ChangedBy, state.ChangedAt = s.changedBy, &at
		}
		body.Features = append(body.Features, state)
	}
	return body
}

func (a *TenantFeaturesAPI) get(w http.ResponseWriter, r *http.Request) {
	p, ok := principal(w, r)
	if !ok {
		return
	}
	var stored map[string]storedFeature
	err := dbx.WithTenantTx(r.Context(), a.pool, func(tx pgx.Tx) (err error) {
		stored, err = loadStoredFeatures(r, tx, p.TenantID)
		return err
	})
	if err != nil {
		httpx.Fail(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, featuresBody(stored))
}

type putFeaturesRequest struct {
	Template string          `json:"template"`
	Features map[string]bool `json:"features"`
}

func (req putFeaturesRequest) changes() (map[string]bool, map[string]string) {
	fields := map[string]string{}
	if (req.Template == "") == (len(req.Features) == 0) {
		fields["features"] = "send either a template or the features to change"
		return nil, fields
	}
	if req.Template != "" {
		state, ok := features.TemplateState(req.Template)
		if !ok {
			fields["template"] = "is not a known template"
		}
		return state, fields
	}
	for key := range req.Features {
		if _, ok := features.Lookup(key); !ok {
			fields[key] = "is not a feature"
		}
	}
	return req.Features, fields
}

func (a *TenantFeaturesAPI) put(w http.ResponseWriter, r *http.Request) {
	p, ok := actingPrincipal(w, r)
	if !ok {
		return
	}
	if !dbx.TenantWide(p, dutyMaintainFeatures) {
		httpx.Fail(w, r, httpx.NewProblem(http.StatusForbidden, codeForbidden, "changing the features of a tenant requires the tenant.administer duty and a request without a legal-entity scope"))
		return
	}
	var req putFeaturesRequest
	if err := httpx.Decode(r, &req); err != nil {
		httpx.Fail(w, r, err)
		return
	}
	changes, fields := req.changes()
	if len(fields) > 0 {
		invalidFields(w, r, "invalid feature change", fields)
		return
	}

	var body featuresResponse
	err := withAdminTx(r.Context(), a.pool, p, dutyMaintainFeatures, func(tx pgx.Tx) error {
		if _, err := tx.Exec(r.Context(), `select pg_advisory_xact_lock(hashtextextended('identity_tenant_features:' || $1::text, 0))`, p.TenantID); err != nil {
			return err
		}
		stored, err := loadStoredFeatures(r, tx, p.TenantID)
		if err != nil {
			return err
		}
		before := enabledOf(stored)
		after, err := features.Apply(before, changes)
		if err != nil {
			return err
		}
		changed := map[string]bool{}
		for key, on := range after {
			if on != before[key] {
				changed[key] = on
			}
		}
		for key, on := range changed {
			if _, err := tx.Exec(r.Context(), `insert into identity_tenant_features (tenant_id, feature_key, enabled, updated_by)
values ($1, $2, $3, $4)
on conflict (tenant_id, feature_key) do update set enabled = excluded.enabled, updated_by = excluded.updated_by, updated_at = now()`, p.TenantID, key, on, p.UserID); err != nil {
				return err
			}
		}
		if len(changed) > 0 {
			if err := record(r.Context(), tx, p.TenantID, p.UserID, "tenantfeatures", p.TenantID, "updated", map[string]any{"changes": changed}); err != nil {
				return err
			}
		}
		if stored, err = loadStoredFeatures(r, tx, p.TenantID); err != nil {
			return err
		}
		body = featuresBody(stored)
		return nil
	})
	var dependency *features.Error
	if errors.As(err, &dependency) {
		problem := httpx.NewProblem(http.StatusUnprocessableEntity, codeFeatureDependency, dependency.Error())
		problem.Errors = dependency.Fields
		httpx.Fail(w, r, problem)
		return
	}
	if err != nil {
		failWrite(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, body)
}
