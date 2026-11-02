package api

import (
	"context"
	"errors"
	"net/http"
	"slices"
	"strings"
	"unicode/utf8"

	"github.com/go-chi/chi/v5"

	"github.com/BenjaminSRussell/passage/platform/authz"
	"github.com/BenjaminSRussell/passage/platform/fieldcrypt"
	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/platform/tenant"
	"github.com/BenjaminSRussell/passage/services/migration/internal/domain"
	"github.com/BenjaminSRussell/passage/services/migration/internal/probe"
	"github.com/BenjaminSRussell/passage/services/migration/internal/store"
)

const (
	connectionsPath      = "/v1/integration/connection-profiles"
	dutyConnectionWrite  = "migration.connection.write"
	secretFieldPrefix    = "migration.connection_profile.secret:"
	CodeSecretsNotConfig = "MIG-SECRETS-NOT-CONFIGURED"
	CodeProbeNotConfig   = "MIG-PROBE-NOT-CONFIGURED"
	CodeSecretUnreadable = "MIG-SECRET-UNREADABLE"
)

// Prober checks a connection profile; internal/probe.Prober is the production one.
type Prober interface {
	Probe(ctx context.Context, t probe.Target) probe.Result
}

// WithConnections turns on the secret and test routes: keys seal profile secrets (nil keeps the secret routes at 501
// MIG-SECRETS-NOT-CONFIGURED) and prober runs :test.
func (a *API) WithConnections(keys *fieldcrypt.Keys, prober Prober) *API {
	a.keys, a.prober = keys, prober
	return a
}

type createConnectionRequest struct {
	Name         string         `json:"name"`
	Kind         string         `json:"kind"`
	SourceSystem string         `json:"sourceSystem"`
	Config       map[string]any `json:"config"`
}

type updateConnectionRequest struct {
	Version *int            `json:"version"`
	Name    *string         `json:"name"`
	Config  *map[string]any `json:"config"`
	Status  *string         `json:"status"`
}

type secretRequest struct {
	Secret string `json:"secret"`
}

func (a *API) registerConnections(r chi.Router) {
	r.With(authz.RequireDuty(dutyConnectionWrite)).Post(connectionsPath, a.createConnection)
	r.With(authz.RequireDuty(dutyRun)).Get(connectionsPath, a.listConnections)
	r.With(authz.RequireDuty(dutyRun)).Get(connectionsPath+"/{id}", a.getConnection)
	r.With(authz.RequireDuty(dutyConnectionWrite)).Patch(connectionsPath+"/{id}", a.updateConnection)
	r.With(authz.RequireDuty(dutyConnectionWrite)).Put(connectionsPath+"/{id}/secret", a.putConnectionSecret)
	r.With(authz.RequireDuty(dutyConnectionWrite)).Post(connectionsPath+"/{id}:test", a.testConnection)
}

func secretsNotConfigured() error {
	return httpx.NewProblem(http.StatusNotImplemented, CodeSecretsNotConfig, "connection secrets need FIELDCRYPT_KEYS_FILE and FIELDCRYPT_CURRENT_KEY_ID on the service; ask the operator")
}

func (a *API) createConnection(w http.ResponseWriter, r *http.Request) {
	if !tenantWide(w, r) {
		return
	}
	var req createConnectionRequest
	if err := httpx.Decode(r, &req); err != nil {
		fail(w, r, err)
		return
	}
	c := domain.ConnectionProfile{Name: req.Name, Kind: req.Kind, SourceSystem: req.SourceSystem, Config: req.Config, Status: domain.ConnActive}
	if bad := domain.ValidateConnection(c); len(bad) > 0 {
		fail(w, r, validation(bad))
		return
	}
	saved, created, err := a.store.SaveConnectionProfile(r.Context(), store.ConnectionProfile{
		Name: c.Name, Kind: c.Kind, SourceSystem: c.SourceSystem, Config: c.Config, Status: c.Status,
	}, r.Header.Get(idempotencyHeader))
	if err != nil {
		a.logFailure(r, "create connection profile failed", err)
		fail(w, r, err)
		return
	}
	status := http.StatusOK
	if created {
		status = http.StatusCreated
		w.Header().Set("Location", connectionsPath+"/"+saved.ID)
	}
	httpx.JSON(w, status, saved)
}

func (a *API) listConnections(w http.ResponseWriter, r *http.Request) {
	if !tenantWide(w, r) {
		return
	}
	limit, ok := parseLimit(w, r)
	if !ok {
		return
	}
	if limit == 0 {
		limit = defaultPage
	}
	q := r.URL.Query()
	bad := map[string]string{}
	if v := q.Get("kind"); v != "" && !slices.Contains([]string{domain.ConnSQL, domain.ConnREST, domain.ConnFile}, v) {
		bad["kind"] = "must be one of sql, rest, file"
	}
	if v := q.Get("status"); v != "" && v != domain.ConnActive && v != domain.ConnDisabled {
		bad["status"] = "must be active or disabled"
	}
	var after *store.ProjectCursor
	if v := q.Get("cursor"); v != "" {
		c, ok := decodeProjectCursor(v)
		if !ok {
			bad["cursor"] = "is not a cursor returned by this endpoint"
		}
		after = c
	}
	if len(bad) > 0 {
		fail(w, r, validation(bad))
		return
	}
	rows, err := a.store.ListConnectionProfiles(r.Context(), store.ConnectionFilter{Kind: q.Get("kind"), Status: q.Get("status"), After: after, Limit: limit + 1})
	if err != nil {
		a.logFailure(r, "list connection profiles failed", err)
		fail(w, r, err)
		return
	}
	body := map[string]any{}
	if len(rows) > limit {
		rows = rows[:limit]
		body["nextCursor"] = encodeProjectCursor(store.ProjectCursor{Name: rows[limit-1].Name, ID: rows[limit-1].ID})
	}
	if rows == nil {
		rows = []store.ConnectionProfile{}
	}
	body["items"] = rows
	httpx.JSON(w, http.StatusOK, body)
}

func (a *API) getConnection(w http.ResponseWriter, r *http.Request) {
	if !tenantWide(w, r) {
		return
	}
	c, err := a.store.GetConnectionProfile(r.Context(), chi.URLParam(r, "id"))
	if err != nil {
		a.logFailure(r, "get connection profile failed", err)
		fail(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, c)
}

func (a *API) updateConnection(w http.ResponseWriter, r *http.Request) {
	if !tenantWide(w, r) {
		return
	}
	var req updateConnectionRequest
	if err := httpx.Decode(r, &req); err != nil {
		fail(w, r, err)
		return
	}
	if req.Version == nil || *req.Version < 1 {
		fail(w, r, validation(map[string]string{"version": "is required and must be at least 1"}))
		return
	}
	saved, err := a.store.UpdateConnectionProfile(r.Context(), chi.URLParam(r, "id"), *req.Version, func(c *store.ConnectionProfile) error {
		if req.Name != nil {
			c.Name = *req.Name
		}
		if req.Config != nil {
			// Repointing the profile (host, port, database, user, sslmode; base URL, auth type, user name, header name)
			// clears the stored secret: :test must not hand an existing password or token to a server it was not set for.
			if c.SecretSet && domain.SecretInvalidated(c.Kind, c.Config, *req.Config) {
				c.SecretSet, c.SecretUpdatedAt = false, nil
			}
			c.Config = *req.Config
		}
		if req.Status != nil {
			c.Status = *req.Status
		}
		if bad := domain.ValidateConnection(domain.ConnectionProfile{Name: c.Name, Kind: c.Kind, SourceSystem: c.SourceSystem, Config: c.Config, Status: c.Status}); len(bad) > 0 {
			return validation(bad)
		}
		return nil
	})
	if err != nil {
		a.logFailure(r, "update connection profile failed", err)
		fail(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, saved)
}

// putConnectionSecret seals and stores the profile's one secret. The value is never logged or returned: the answer is
// the profile, which says only that a secret is set and when it changed.
func (a *API) putConnectionSecret(w http.ResponseWriter, r *http.Request) {
	if !tenantWide(w, r) {
		return
	}
	if a.keys == nil {
		fail(w, r, secretsNotConfigured())
		return
	}
	var req secretRequest
	if err := httpx.Decode(r, &req); err != nil {
		fail(w, r, err)
		return
	}
	if req.Secret == "" || len(req.Secret) > domain.MaxSecretBytes || !utf8.ValidString(req.Secret) || strings.ContainsRune(req.Secret, 0) {
		fail(w, r, validation(map[string]string{"secret": "is required, at most 4096 bytes of text"}))
		return
	}
	id := chi.URLParam(r, "id")
	if _, err := a.store.GetConnectionProfile(r.Context(), id); err != nil {
		a.logFailure(r, "set connection secret failed", err)
		fail(w, r, err)
		return
	}
	p, _ := tenant.FromContext(r.Context())
	sealed, err := a.keys.Seal(p.TenantID, secretFieldPrefix+id, []byte(req.Secret))
	if err != nil {
		a.logFailure(r, "seal connection secret failed", errors.New("seal failed"))
		fail(w, r, errors.New("seal failed"))
		return
	}
	saved, err := a.store.SetConnectionSecret(r.Context(), id, sealed, a.keys.CurrentID())
	if err != nil {
		a.logFailure(r, "set connection secret failed", err)
		fail(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, saved)
}

// testConnection runs the profile's probe and answers 200 with the result either way: the probe ran, whether the
// connection works is in the body. It needs the write duty because it opens the stored secret.
func (a *API) testConnection(w http.ResponseWriter, r *http.Request) {
	if !tenantWide(w, r) {
		return
	}
	id := chi.URLParam(r, "id")
	c, err := a.store.GetConnectionProfile(r.Context(), id)
	if err != nil {
		a.logFailure(r, "test connection profile failed", err)
		fail(w, r, err)
		return
	}
	if a.prober == nil {
		fail(w, r, httpx.NewProblem(http.StatusNotImplemented, CodeProbeNotConfig, "connection tests are not configured on this deployment"))
		return
	}
	if a.keys == nil && needsSecret(c) {
		fail(w, r, secretsNotConfigured())
		return
	}
	// The stored settings were validated on write; validate again so a row from an older build cannot reach the dialer.
	if bad := domain.ValidateConnection(domain.ConnectionProfile{Name: c.Name, Kind: c.Kind, SourceSystem: c.SourceSystem, Config: c.Config, Status: c.Status}); len(bad) > 0 {
		fail(w, r, validation(bad))
		return
	}
	target := probe.Target{Kind: c.Kind, Config: c.Config}
	if c.SecretSet && a.keys != nil {
		secret, err := a.openConnectionSecret(r, id)
		if err != nil {
			fail(w, r, err)
			return
		}
		target.Secret = secret
	}
	res := a.prober.Probe(r.Context(), target)
	if !res.OK && res.Failure != nil {
		a.logFailure(r, "connection probe failed", errors.New(res.Failure.Code))
	}
	httpx.JSON(w, http.StatusOK, res)
}

// needsSecret tells whether a profile's probe sends a secret: every sql profile and a rest profile with authentication.
func needsSecret(c store.ConnectionProfile) bool {
	switch c.Kind {
	case domain.ConnSQL:
		return true
	case domain.ConnREST:
		auth, _ := c.Config["authType"].(string)
		return auth != "" && auth != "none"
	}
	return false
}

// openConnectionSecret opens a profile's stored secret with the current keys. The plaintext goes to the caller only.
func (a *API) openConnectionSecret(r *http.Request, id string) (string, error) {
	sealed, _, err := a.store.ConnectionSecret(r.Context(), id)
	if err != nil {
		a.logFailure(r, "read connection secret failed", err)
		return "", err
	}
	p, _ := tenant.FromContext(r.Context())
	plain, err := a.keys.Open(p.TenantID, secretFieldPrefix+id, sealed)
	if err != nil {
		a.logFailure(r, "open connection secret failed", errors.New("open failed"))
		return "", httpx.NewProblem(http.StatusUnprocessableEntity, CodeSecretUnreadable, "the stored secret cannot be opened with the current keys; set it again")
	}
	return string(plain), nil
}
