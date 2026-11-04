package api

import (
	"context"
	"fmt"
	"net/http"

	"github.com/go-chi/chi/v5"

	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/platform/tenant"
	"github.com/BenjaminSRussell/passage/services/engineering/internal/domain"
	"github.com/BenjaminSRussell/passage/services/engineering/internal/store"
)

type releasePolicyBody struct {
	ID                string          `json:"id,omitempty"`
	Code              string          `json:"code"`
	CopyBOM           bool            `json:"copyBom"`
	ActivateBOM       bool            `json:"activateBom"`
	CopyRoute         bool            `json:"copyRoute"`
	ActivateRoute     bool            `json:"activateRoute"`
	CopyAttributes    bool            `json:"copyAttributes"`
	CopyOrderSettings bool            `json:"copyOrderSettings"`
	CopyDocuments     bool            `json:"copyDocuments"`
	RequireAcceptance map[string]bool `json:"requireAcceptance"`
}

func bodyFromReleasePolicy(p domain.ReleasePolicy) releasePolicyBody {
	return releasePolicyBody{ID: p.ID, Code: p.Code, CopyBOM: p.CopyBOM, ActivateBOM: p.ActivateBOM, CopyRoute: p.CopyRoute, ActivateRoute: p.ActivateRoute, CopyAttributes: p.CopyAttributes, CopyOrderSettings: p.CopyOrderSettings, CopyDocuments: p.CopyDocuments, RequireAcceptance: p.RequireAcceptance}
}

func (a *API) createReleasePolicy(w http.ResponseWriter, r *http.Request) {
	if !validTenant(w, r) {
		return
	}
	var body releasePolicyBody
	if err := httpx.Decode(r, &body); err != nil {
		httpx.Fail(w, r, err)
		return
	}
	fields := map[string]string{}
	validID("code", body.Code, fields)
	if len(body.RequireAcceptance) > maxReleaseEntities {
		fields["requireAcceptance"] = fmt.Sprintf("must list at most %d legal entities", maxReleaseEntities)
	}
	acceptance := make(map[string]bool, len(body.RequireAcceptance))
	for le, required := range body.RequireAcceptance {
		acceptance[legalEntityID("requireAcceptance", le, fields)] = required
	}
	body.RequireAcceptance = acceptance
	if len(fields) > 0 {
		invalid(w, r, fields)
		return
	}
	created, err := a.store.CreateReleasePolicy(r.Context(), domain.ReleasePolicy{
		Code: body.Code, CopyBOM: body.CopyBOM, ActivateBOM: body.ActivateBOM, CopyRoute: body.CopyRoute, ActivateRoute: body.ActivateRoute,
		CopyAttributes: body.CopyAttributes, CopyOrderSettings: body.CopyOrderSettings, CopyDocuments: body.CopyDocuments, RequireAcceptance: body.RequireAcceptance,
	}, idemOf(r, body))
	if err != nil {
		a.fail(w, r, "create release policy", err, &a.bomFailed)
		return
	}
	httpx.JSON(w, http.StatusCreated, bodyFromReleasePolicy(created))
}

func (a *API) listReleasePolicies(w http.ResponseWriter, r *http.Request) {
	if !validTenant(w, r) {
		return
	}
	listPage(a, w, r, "list release policies", &a.bomFailed, a.store.ListReleasePolicies, bodyFromReleasePolicy)
}

func (a *API) getReleasePolicy(w http.ResponseWriter, r *http.Request) {
	if !validTenant(w, r) {
		return
	}
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	p, err := a.store.GetReleasePolicy(r.Context(), id)
	if err != nil {
		a.fail(w, r, "get release policy", err, &a.bomFailed)
		return
	}
	httpx.JSON(w, http.StatusOK, bodyFromReleasePolicy(p))
}

func (a *API) putSiteMapping(w http.ResponseWriter, r *http.Request) {
	if !validTenant(w, r) {
		return
	}
	var req struct {
		EngineeringLEID string `json:"engineeringLeId"`
		OperationalLEID string `json:"operationalLeId"`
		EngineeringSite string `json:"engineeringSite"`
		OperationalSite string `json:"operationalSite"`
	}
	if err := httpx.Decode(r, &req); err != nil {
		httpx.Fail(w, r, err)
		return
	}
	fields := map[string]string{}
	req.EngineeringLEID = legalEntityID("engineeringLeId", req.EngineeringLEID, fields)
	req.OperationalLEID = legalEntityID("operationalLeId", req.OperationalLEID, fields)
	if len(fields) > 0 {
		invalid(w, r, fields)
		return
	}
	if err := a.store.PutSiteMapping(r.Context(), domain.SiteMapping{EngineeringLEID: req.EngineeringLEID, OperationalLEID: req.OperationalLEID, EngineeringSite: req.EngineeringSite, OperationalSite: req.OperationalSite}); err != nil {
		a.fail(w, r, "put site mapping", err, &a.bomFailed)
		return
	}
	httpx.JSON(w, http.StatusOK, req)
}

type releaseSessionBody struct {
	ID                string            `json:"id,omitempty"`
	Number            string            `json:"number"`
	CreatedBy         string            `json:"createdBy,omitempty"`
	Source            string            `json:"source,omitempty"`
	PolicyID          string            `json:"policyId"`
	EcoID             string            `json:"ecoId,omitempty"`
	ReadinessPolicyID string            `json:"readinessPolicyId,omitempty"`
	EngineeringLEID   string            `json:"engineeringLeId"`
	ProductID         string            `json:"productId"`
	VersionCode       string            `json:"versionCode"`
	LegalEntities     []string          `json:"legalEntities"`
	Lines             []releaseLineBody `json:"lines,omitempty"`
}

type releaseLineBody struct {
	ID                   string `json:"id,omitempty"`
	SessionID            string `json:"sessionId,omitempty"`
	ProductID            string `json:"productId"`
	EngineeringVersion   string `json:"engineeringVersion"`
	LegalEntityID        string `json:"legalEntityId"`
	Status               string `json:"status"`
	Reason               string `json:"reason,omitempty"`
	AcceptedBy           string `json:"acceptedBy,omitempty"`
	CopiedBOMVersionID   string `json:"copiedBomVersionId,omitempty"`
	CopiedRouteVersionID string `json:"copiedRouteVersionId,omitempty"`
}

func bodyFromReleaseLine(l domain.ReleaseLine) releaseLineBody {
	return releaseLineBody{ID: l.ID, SessionID: l.SessionID, ProductID: l.ProductID, EngineeringVersion: l.EngineeringVersion, LegalEntityID: l.LegalEntityID, Status: l.Status, Reason: l.Reason, AcceptedBy: l.AcceptedBy, CopiedBOMVersionID: l.CopiedBOMVersionID, CopiedRouteVersionID: l.CopiedRouteVersionID}
}

func bodyFromReleaseSession(rec store.ReleaseSessionRecord) releaseSessionBody {
	body := releaseSessionBody{ID: rec.Session.ID, Number: rec.Session.Number, CreatedBy: rec.Session.CreatedBy, Source: rec.Session.Source, PolicyID: rec.Session.PolicyID, EcoID: rec.Session.EcoID, ReadinessPolicyID: rec.Session.ReadinessPolicyID, EngineeringLEID: rec.Session.EngineeringLEID, Lines: make([]releaseLineBody, len(rec.Lines))}
	for i, l := range rec.Lines {
		body.Lines[i] = bodyFromReleaseLine(l)
	}
	return body
}

func (a *API) createReleaseSession(w http.ResponseWriter, r *http.Request) {
	if !validTenant(w, r) {
		return
	}
	var body releaseSessionBody
	if err := httpx.Decode(r, &body); err != nil {
		httpx.Fail(w, r, err)
		return
	}
	p, _ := tenant.FromContext(r.Context())
	if p.UserID == "" {
		httpx.Fail(w, r, httpx.NewProblem(http.StatusUnauthorized, "AUTH-0001", "creating a release session needs the acting user"))
		return
	}
	fields := map[string]string{}
	checkUUID("policyId", body.PolicyID, fields)
	if body.PolicyID == "" {
		fields["policyId"] = "must be a UUID"
	}
	checkUUID("ecoId", body.EcoID, fields)
	checkUUID("readinessPolicyId", body.ReadinessPolicyID, fields)
	if body.CreatedBy != "" {
		fields["createdBy"] = "is the signed-in user and cannot be set"
	}
	body.EngineeringLEID = legalEntityID("engineeringLeId", body.EngineeringLEID, fields)
	switch body.Source {
	case "", domain.ReleaseManual, domain.ReleaseEco, domain.ReleaseJob:
	default:
		fields["source"] = "must be manual, eco or job"
	}
	if body.Number != "" {
		validID("number", body.Number, fields)
	}
	validID("productId", body.ProductID, fields)
	validID("versionCode", body.VersionCode, fields)
	if n := len(body.LegalEntities); n == 0 || n > maxReleaseEntities {
		fields["legalEntities"] = fmt.Sprintf("must list 1 to %d legal entities", maxReleaseEntities)
	}
	seen := map[string]bool{}
	for i, le := range body.LegalEntities {
		id := legalEntityID("legalEntities", le, fields)
		if seen[id] {
			fields["legalEntities"] = "each legal entity must be listed once"
		}
		seen[id] = true
		body.LegalEntities[i] = id
	}
	if len(fields) > 0 {
		invalid(w, r, fields)
		return
	}
	rec, err := a.store.CreateReleaseSession(r.Context(), store.ReleaseSessionInput{
		Number: body.Number, CreatedBy: p.UserID, Source: body.Source, PolicyID: body.PolicyID, EcoID: body.EcoID, ReadinessPolicyID: body.ReadinessPolicyID,
		EngineeringLEID: body.EngineeringLEID, ProductID: body.ProductID, VersionCode: body.VersionCode, LegalEntities: body.LegalEntities,
	}, idemOf(r, body))
	if err != nil {
		a.fail(w, r, "create release session", err, &a.bomFailed)
		return
	}
	out := bodyFromReleaseSession(rec)
	out.ProductID, out.VersionCode = body.ProductID, body.VersionCode
	httpx.JSON(w, http.StatusCreated, out)
}

func (a *API) listReleaseSessions(w http.ResponseWriter, r *http.Request) {
	if !validTenant(w, r) {
		return
	}
	listPage(a, w, r, "list release sessions", &a.bomFailed, a.store.ListReleaseSessions, func(s domain.ReleaseSession) releaseSessionBody {
		return releaseSessionBody{ID: s.ID, Number: s.Number, CreatedBy: s.CreatedBy, Source: s.Source, PolicyID: s.PolicyID, EcoID: s.EcoID, ReadinessPolicyID: s.ReadinessPolicyID, EngineeringLEID: s.EngineeringLEID}
	})
}

func (a *API) getReleaseSession(w http.ResponseWriter, r *http.Request) {
	if !validTenant(w, r) {
		return
	}
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	rec, err := a.store.GetReleaseSession(r.Context(), id)
	if err != nil {
		a.fail(w, r, "get release session", err, &a.bomFailed)
		return
	}
	httpx.JSON(w, http.StatusOK, bodyFromReleaseSession(rec))
}

func (a *API) listReleaseLines(w http.ResponseWriter, r *http.Request) {
	if !validTenant(w, r) {
		return
	}
	q := r.URL.Query()
	f := store.ReleaseLineFilter{Status: q.Get("status"), LegalEntityID: q.Get("legalEntityId")}
	if f.LegalEntityID != "" {
		fields := map[string]string{}
		if f.LegalEntityID = legalEntityID("legalEntityId", f.LegalEntityID, fields); len(fields) > 0 {
			invalid(w, r, fields)
			return
		}
	}
	switch f.Status {
	case "", domain.LinePending, domain.LineSent, domain.LineAccepted, domain.LineRejected, domain.LineFailed:
	default:
		invalid(w, r, map[string]string{"status": "must be pending, sent, accepted, rejected or failed"})
		return
	}
	listPage(a, w, r, "list release lines", &a.bomFailed, func(ctx context.Context, after string, limit int) (store.Page[domain.ReleaseLine], error) {
		return a.store.ListReleaseLines(ctx, f, after, limit)
	}, bodyFromReleaseLine)
}

func (a *API) decideReleaseLine(accept bool) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !validTenant(w, r) {
			return
		}
		id, ok := pathID(w, r)
		if !ok {
			return
		}
		p, _ := tenant.FromContext(r.Context())
		if p.UserID == "" {
			httpx.Fail(w, r, httpx.NewProblem(http.StatusUnauthorized, "AUTH-0001", "accepting a release needs the acting user"))
			return
		}
		var body struct {
			Reason string `json:"reason"`
		}
		if r.ContentLength != 0 {
			if err := httpx.Decode(r, &body); err != nil {
				httpx.Fail(w, r, err)
				return
			}
		}
		line, err := a.store.DecideReleaseLine(r.Context(), id, p.UserID, body.Reason, accept)
		if err != nil {
			a.fail(w, r, "decide release line", err, &a.bomFailed)
			return
		}
		httpx.JSON(w, http.StatusOK, bodyFromReleaseLine(line))
	}
}

type readinessPolicyBody struct {
	ID          string                  `json:"id,omitempty"`
	Code        string                  `json:"code"`
	Checks      []domain.ReadinessCheck `json:"checks"`
	TargetState string                  `json:"targetState,omitempty"`
	Continuous  bool                    `json:"continuous"`
}

func bodyFromReadinessPolicy(p domain.ReadinessPolicy) readinessPolicyBody {
	return readinessPolicyBody{ID: p.ID, Code: p.Code, Checks: p.Checks, TargetState: p.TargetState, Continuous: p.Continuous}
}

func (a *API) createReadinessPolicy(w http.ResponseWriter, r *http.Request) {
	if !validTenant(w, r) {
		return
	}
	var body readinessPolicyBody
	if err := httpx.Decode(r, &body); err != nil {
		httpx.Fail(w, r, err)
		return
	}
	fields := map[string]string{}
	validID("code", body.Code, fields)
	if len(body.Checks) > maxReadinessChecks {
		fields["checks"] = fmt.Sprintf("must list at most %d checks", maxReadinessChecks)
	}
	keys := map[string]bool{}
	for i, c := range body.Checks {
		field := fmt.Sprintf("checks[%d]", i)
		switch {
		case len(c.Key) == 0 || len(c.Key) > maxIDLength:
			fields[field] = fmt.Sprintf("key must be 1 to %d characters", maxIDLength)
		case keys[c.Key]:
			fields[field] = "key is listed twice"
		case c.Kind != domain.CheckAuto && c.Kind != domain.CheckManual:
			fields[field] = "kind must be auto or manual"
		}
		keys[c.Key] = true
	}
	if len(fields) > 0 {
		invalid(w, r, fields)
		return
	}
	created, err := a.store.CreateReadinessPolicy(r.Context(), domain.ReadinessPolicy{Code: body.Code, Checks: body.Checks, TargetState: body.TargetState, Continuous: body.Continuous}, idemOf(r, body))
	if err != nil {
		a.fail(w, r, "create readiness policy", err, &a.bomFailed)
		return
	}
	httpx.JSON(w, http.StatusCreated, bodyFromReadinessPolicy(created))
}

func (a *API) listReadinessPolicies(w http.ResponseWriter, r *http.Request) {
	if !validTenant(w, r) {
		return
	}
	listPage(a, w, r, "list readiness policies", &a.bomFailed, a.store.ListReadinessPolicies, bodyFromReadinessPolicy)
}

func (a *API) getReadinessPolicy(w http.ResponseWriter, r *http.Request) {
	if !validTenant(w, r) {
		return
	}
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	p, err := a.store.GetReadinessPolicy(r.Context(), id)
	if err != nil {
		a.fail(w, r, "get readiness policy", err, &a.bomFailed)
		return
	}
	httpx.JSON(w, http.StatusOK, bodyFromReadinessPolicy(p))
}

type releasedProductBody struct {
	ID                 string                        `json:"id"`
	ProductID          string                        `json:"productId"`
	LegalEntityID      string                        `json:"legalEntityId"`
	EngineeringVersion string                        `json:"engineeringVersion,omitempty"`
	SessionID          string                        `json:"sessionId,omitempty"`
	State              string                        `json:"state"`
	Checks             []domain.ReadinessCheckResult `json:"checks,omitempty"`
}

func bodyFromReleasedProduct(p store.ReleasedProduct) releasedProductBody {
	return releasedProductBody{ID: p.ID, ProductID: p.ProductID, LegalEntityID: p.LegalEntityID, EngineeringVersion: p.EngineeringVersion, SessionID: p.SessionID, State: p.State, Checks: p.Checks}
}

func (a *API) listReleasedProducts(w http.ResponseWriter, r *http.Request) {
	if !validTenant(w, r) {
		return
	}
	legalEntity := r.URL.Query().Get("legalEntityId")
	if legalEntity != "" {
		fields := map[string]string{}
		if legalEntity = legalEntityID("legalEntityId", legalEntity, fields); len(fields) > 0 {
			invalid(w, r, fields)
			return
		}
	}
	listPage(a, w, r, "list released products", &a.bomFailed, func(ctx context.Context, after string, limit int) (store.Page[store.ReleasedProduct], error) {
		return a.store.ListReleasedProducts(ctx, legalEntity, after, limit)
	}, bodyFromReleasedProduct)
}

func (a *API) getReleasedProduct(w http.ResponseWriter, r *http.Request) {
	if !validTenant(w, r) {
		return
	}
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	p, err := a.store.GetReleasedProduct(r.Context(), id)
	if err != nil {
		a.fail(w, r, "get released product", err, &a.bomFailed)
		return
	}
	httpx.JSON(w, http.StatusOK, bodyFromReleasedProduct(p))
}

func (a *API) processPolicy(w http.ResponseWriter, r *http.Request) {
	if !validTenant(w, r) {
		return
	}
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	process := r.URL.Query().Get("process")
	if process == "" {
		invalid(w, r, map[string]string{"process": "is required"})
		return
	}
	p, err := a.store.GetReleasedProduct(r.Context(), id)
	if err != nil {
		a.fail(w, r, "process policy", err, &a.bomFailed)
		return
	}
	httpx.JSON(w, http.StatusOK, map[string]string{"code": domain.ProcessPolicy(p.State, process), "state": p.State})
}

func (a *API) signReadinessCheck(w http.ResponseWriter, r *http.Request) {
	if !validTenant(w, r) {
		return
	}
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	key := chi.URLParam(r, "key")
	if key == "" {
		invalid(w, r, map[string]string{"key": "is required"})
		return
	}
	p, _ := tenant.FromContext(r.Context())
	if p.UserID == "" {
		httpx.Fail(w, r, httpx.NewProblem(http.StatusUnauthorized, "AUTH-0001", "signing a readiness check needs the acting user"))
		return
	}
	rec, err := a.store.SignReadinessCheck(r.Context(), id, key, p.UserID)
	if err != nil {
		a.fail(w, r, "sign readiness check", err, &a.bomFailed)
		return
	}
	httpx.JSON(w, http.StatusOK, bodyFromReleasedProduct(rec))
}
