package api

import (
	"encoding/json"
	"net/http"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/platform/authz"
	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/platform/tenant"
	"github.com/BenjaminSRussell/passage/services/shopfloor/internal/domain"
	"github.com/BenjaminSRussell/passage/services/shopfloor/internal/store"
)

// Edge fleet duties (FR-25.19; not in the PRD duty table).
const (
	// DutyEdgeConfigure registers edge nodes, binds their connector identity and publishes
	// their configuration, whose write allowlist decides which machine nodes a connector may
	// write. It is kept apart from shopfloor.setup.maintain so that maintaining machines and
	// pay setup does not grant control writes.
	DutyEdgeConfigure = "shopfloor.edge.configure"
	// DutyEdgeConnect is held by the connectors' own identities, never by people: it pulls the
	// configuration and reports heartbeats, and only for the node bound to the caller.
	DutyEdgeConnect = "shopfloor.edge.connect"
)

// registerEdge mounts the edge fleet routes (FR-25.19). The fleet screens read with the view
// duty and write with the configure duty; a connector pulls its configuration and reports in
// with the connect duty, and the store checks that the caller is the node's bound connector.
func (a *API) registerEdge(r chi.Router) {
	r.With(authz.RequireDuty(DutyEdgeConfigure)).Post("/v1/shopfloor/edge-nodes", a.createEdgeNode)
	r.With(authz.RequireDuty(DutyView)).Get("/v1/shopfloor/edge-nodes", a.listEdgeNodes)
	r.With(authz.RequireDuty(DutyView)).Get("/v1/shopfloor/edge-nodes/{id}", a.getEdgeNode)
	r.With(authz.RequireDuty(DutyEdgeConfigure)).Put("/v1/shopfloor/edge-nodes/{id}", a.updateEdgeNode)
	r.With(authz.RequireDuty(DutyEdgeConfigure)).Post("/v1/shopfloor/edge-nodes/{id}/config-versions", a.publishEdgeConfig)
	r.With(authz.RequireDuty(DutyView)).Get("/v1/shopfloor/edge-nodes/{id}/config-versions", a.listEdgeConfigs)
	r.With(authz.RequireDuty(DutyEdgeConnect)).Get("/v1/shopfloor/edge-nodes/{id}/config", a.currentEdgeConfig)
	r.With(authz.RequireDuty(DutyEdgeConnect)).Post("/v1/shopfloor/edge-nodes/{id}:heartbeat", a.edgeHeartbeat)
}

type edgeNodeRequest struct {
	SiteID           *string `json:"siteId"`
	Name             string  `json:"name"`
	Platform         string  `json:"platform"`
	Ring             string  `json:"ring"`
	ConnectorSubject *string `json:"connectorSubject"`
	Active           *bool   `json:"active"`
}

// connectorSubject is the identity a connector call is made as: the authenticated subject the
// gateway forwards. A signed service token is not a connector.
func connectorSubject(r *http.Request) string {
	p, ok := tenant.FromContext(r.Context())
	if !ok || p.Service != "" {
		return ""
	}
	return p.UserID
}

func (req edgeNodeRequest) input(creating bool) (store.EdgeNodeInput, map[string]string) {
	fields := map[string]string{}
	name := strings.TrimSpace(req.Name)
	if len(name) < 1 || len(name) > 120 {
		fields["name"] = "must be 1 to 120 characters"
	}
	if creating && !domain.ValidEdgePlatform(req.Platform) {
		fields["platform"] = "must be k3s, windows or linux"
	}
	ring := req.Ring
	if ring == "" && creating {
		ring = domain.RingAll // the PRD default (parameter "Edge rollout ring")
	}
	if !domain.ValidRing(ring) {
		fields["ring"] = "must be canary, ring1 or all"
	}
	if req.SiteID != nil && !ids.Valid(*req.SiteID) {
		fields["siteId"] = "must be a UUID"
	}
	var subject *string
	if req.ConnectorSubject != nil {
		v := strings.TrimSpace(*req.ConnectorSubject)
		switch {
		case len(v) < 1 || len(v) > 200:
			fields["connectorSubject"] = "must be 1 to 200 characters, or null to unbind"
		case tenant.IsServiceActor(v):
			fields["connectorSubject"] = "must be a connector identity, not a service actor"
		}
		subject = &v
	}
	return store.EdgeNodeInput{SiteID: req.SiteID, Name: name, Platform: req.Platform, Ring: ring, ConnectorSubject: subject,
		Active: req.Active == nil || *req.Active}, fields
}

func (a *API) createEdgeNode(w http.ResponseWriter, r *http.Request) {
	if !validTenant(w, r) {
		return
	}
	user, ok := authenticatedUser(w, r, "registering an edge node")
	if !ok {
		return
	}
	idem, ok := readIdem(w, r)
	if !ok {
		return
	}
	var req edgeNodeRequest
	if err := httpx.Decode(r, &req); err != nil {
		httpx.Fail(w, r, err)
		return
	}
	in, fields := req.input(true)
	if len(fields) > 0 {
		validationFailed(w, r, fields)
		return
	}
	if a.replayed(w, r, idem) {
		return
	}
	out, err := a.store.CreateEdgeNode(r.Context(), in, user, idem)
	if err != nil {
		a.fail(w, r, "create edge node", err)
		return
	}
	w.Header().Set("ETag", etag(out.Version))
	httpx.JSON(w, http.StatusCreated, out)
}

func (a *API) updateEdgeNode(w http.ResponseWriter, r *http.Request) {
	if !validTenant(w, r) {
		return
	}
	user, ok := authenticatedUser(w, r, "maintaining an edge node")
	if !ok {
		return
	}
	id, ok := pathID(w, r, store.CodeEdgeNotFound, "edge node")
	if !ok {
		return
	}
	version, ok := ifMatch(w, r)
	if !ok {
		return
	}
	var req edgeNodeRequest
	if err := httpx.Decode(r, &req); err != nil {
		httpx.Fail(w, r, err)
		return
	}
	in, fields := req.input(false)
	if len(fields) > 0 {
		validationFailed(w, r, fields)
		return
	}
	out, err := a.store.UpdateEdgeNode(r.Context(), id, version, in, user)
	if err != nil {
		a.fail(w, r, "update edge node", err)
		return
	}
	w.Header().Set("ETag", etag(out.Version))
	httpx.JSON(w, http.StatusOK, out)
}

func (a *API) getEdgeNode(w http.ResponseWriter, r *http.Request) {
	if !validTenant(w, r) {
		return
	}
	id, ok := pathID(w, r, store.CodeEdgeNotFound, "edge node")
	if !ok {
		return
	}
	out, err := a.store.GetEdgeNode(r.Context(), id)
	if err != nil {
		a.fail(w, r, "get edge node", err)
		return
	}
	w.Header().Set("ETag", etag(out.Version))
	httpx.JSON(w, http.StatusOK, out)
}

func (a *API) listEdgeNodes(w http.ResponseWriter, r *http.Request) {
	if !validTenant(w, r) {
		return
	}
	limit, ok := limitParam(w, r, 200, 1000)
	if !ok {
		return
	}
	q := r.URL.Query()
	ring, status := q.Get("ring"), q.Get("status")
	fields := map[string]string{}
	if ring != "" && !domain.ValidRing(ring) {
		fields["ring"] = "must be canary, ring1 or all"
	}
	switch status {
	case "", domain.EdgePending, domain.EdgeOnline, domain.EdgeOffline, domain.EdgeInactive:
	default:
		fields["status"] = "must be pending, online, offline or inactive"
	}
	if len(fields) > 0 {
		validationFailed(w, r, fields)
		return
	}
	items, err := a.store.ListEdgeNodes(r.Context(), ring, status, limit)
	if err != nil {
		a.fail(w, r, "list edge nodes", err)
		return
	}
	httpx.JSON(w, http.StatusOK, itemsResponse[store.EdgeNode]{Items: items})
}

type publishConfigRequest struct {
	Document json.RawMessage `json:"document"`
}

func (a *API) publishEdgeConfig(w http.ResponseWriter, r *http.Request) {
	if !validTenant(w, r) {
		return
	}
	user, ok := authenticatedUser(w, r, "publishing an edge configuration")
	if !ok {
		return
	}
	id, ok := pathID(w, r, store.CodeEdgeNotFound, "edge node")
	if !ok {
		return
	}
	idem, ok := readIdem(w, r)
	if !ok {
		return
	}
	var req publishConfigRequest
	if err := httpx.Decode(r, &req); err != nil {
		httpx.Fail(w, r, err)
		return
	}
	if _, fields := domain.PrepareEdgeConfig(req.Document, 1); len(fields) > 0 {
		validationFailed(w, r, fields)
		return
	}
	if a.replayed(w, r, idem) {
		return
	}
	out, err := a.store.PublishEdgeConfig(r.Context(), id, req.Document, user, idem)
	if err != nil {
		a.fail(w, r, "publish edge configuration", err)
		return
	}
	httpx.JSON(w, http.StatusCreated, out)
}

func (a *API) listEdgeConfigs(w http.ResponseWriter, r *http.Request) {
	if !validTenant(w, r) {
		return
	}
	id, ok := pathID(w, r, store.CodeEdgeNotFound, "edge node")
	if !ok {
		return
	}
	limit, ok := limitParam(w, r, 50, 200)
	if !ok {
		return
	}
	items, err := a.store.ListEdgeConfigs(r.Context(), id, limit)
	if err != nil {
		a.fail(w, r, "list edge configurations", err)
		return
	}
	httpx.JSON(w, http.StatusOK, itemsResponse[store.EdgeConfigVersion]{Items: items})
}

func (a *API) currentEdgeConfig(w http.ResponseWriter, r *http.Request) {
	if !validTenant(w, r) {
		return
	}
	id, ok := pathID(w, r, store.CodeEdgeNotFound, "edge node")
	if !ok {
		return
	}
	out, err := a.store.CurrentEdgeConfig(r.Context(), id, connectorSubject(r))
	if err != nil {
		a.fail(w, r, "pull edge configuration", err)
		return
	}
	w.Header().Set("ETag", etag(out.Version))
	httpx.JSON(w, http.StatusOK, out)
}

type heartbeatRequest struct {
	ConnectorVersion     *string                `json:"connectorVersion"`
	BufferFillPct        *decimal.Decimal       `json:"bufferFillPct"`
	Machines             []domain.MachineStatus `json:"machines"`
	AppliedConfigVersion *int                   `json:"appliedConfigVersion"`
	CertSerial           *string                `json:"certSerial"`
	CertExpiresAt        *time.Time             `json:"certExpiresAt"`
}

func (a *API) edgeHeartbeat(w http.ResponseWriter, r *http.Request) {
	if !validTenant(w, r) {
		return
	}
	id, ok := pathID(w, r, store.CodeEdgeNotFound, "edge node")
	if !ok {
		return
	}
	var req heartbeatRequest
	if err := httpx.Decode(r, &req); err != nil {
		httpx.Fail(w, r, err)
		return
	}
	fields := domain.ValidateMachineStatuses(req.Machines)
	if fields == nil {
		fields = map[string]string{}
	}
	if req.BufferFillPct == nil || req.BufferFillPct.IsNegative() || req.BufferFillPct.GreaterThan(decimal.NewFromInt(100)) {
		fields["bufferFillPct"] = "is required and must be between 0 and 100"
	} else if req.BufferFillPct.Exponent() < -2 {
		fields["bufferFillPct"] = "has at most two decimals"
	}
	if req.AppliedConfigVersion != nil && *req.AppliedConfigVersion < 1 {
		fields["appliedConfigVersion"] = "must be at least 1"
	}
	if v := req.ConnectorVersion; v != nil && (len(*v) < 1 || len(*v) > 60) {
		fields["connectorVersion"] = "must be 1 to 60 characters"
	}
	if v := req.CertSerial; v != nil && (len(*v) < 1 || len(*v) > 200) {
		fields["certSerial"] = "must be 1 to 200 characters"
	}
	if len(fields) > 0 {
		validationFailed(w, r, fields)
		return
	}
	if req.Machines == nil {
		req.Machines = []domain.MachineStatus{}
	}
	out, err := a.store.Heartbeat(r.Context(), id, connectorSubject(r), store.HeartbeatInput{ConnectorVersion: req.ConnectorVersion, BufferFillPct: *req.BufferFillPct,
		Machines: req.Machines, AppliedConfigVersion: req.AppliedConfigVersion, CertSerial: req.CertSerial, CertExpiresAt: req.CertExpiresAt})
	if err != nil {
		a.fail(w, r, "edge heartbeat", err)
		return
	}
	httpx.JSON(w, http.StatusOK, out)
}
