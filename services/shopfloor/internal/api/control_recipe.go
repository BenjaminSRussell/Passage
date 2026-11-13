package api

import (
	"net/http"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/platform/authz"
	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/services/shopfloor/internal/domain"
	"github.com/BenjaminSRussell/passage/services/shopfloor/internal/store"
)

// Control recipe duties (FR-25.15; not in the PRD duty table).
const (
	// DutyControlRecipeCreate generates and resends control recipes: held by the planner or supervisor who
	// releases an order and by the service identity of the batch order release (w24).
	DutyControlRecipeCreate = "shopfloor.controlrecipe.create"
	// DutyControlRecipeExecute is held by kiosk operators: they report the status of a kiosk recipe.
	DutyControlRecipeExecute = "shopfloor.controlrecipe.execute"
)

const maxRecipeWrites = 200

// registerControlRecipes mounts the control recipe monitor, generation, kiosk reporting and the
// connector's pull and report (FR-25.15, BR-25.11, BR-25.15).
func (a *API) registerControlRecipes(r chi.Router) {
	r.With(authz.RequireDuty(DutyControlRecipeCreate)).Post("/v1/shopfloor/control-recipes", a.createControlRecipe)
	r.With(authz.RequireDuty(DutyView)).Get("/v1/shopfloor/control-recipes", a.listControlRecipes)
	r.With(authz.RequireDuty(DutyView)).Get("/v1/shopfloor/control-recipes/{id}", a.getControlRecipe)
	r.With(authz.RequireDuty(DutyControlRecipeExecute)).Post("/v1/shopfloor/control-recipes/{id}:report", a.reportControlRecipe)
	r.With(authz.RequireDuty(DutyControlRecipeCreate)).Post("/v1/shopfloor/control-recipes/{id}:resend", a.resendControlRecipe)
	r.With(authz.RequireDuty(DutyEdgeConnect)).Get("/v1/shopfloor/edge-nodes/{id}/control-recipes", a.edgeControlRecipes)
	r.With(authz.RequireDuty(DutyEdgeConnect)).Post("/v1/shopfloor/edge-nodes/{id}/control-recipes/{rid}:report", a.edgeReportControlRecipe)
}

type controlRecipeRequest struct {
	OrderID     string           `json:"orderId"`
	ResourceID  string           `json:"resourceId"`
	Destination string           `json:"destination"`
	EdgeID      *string          `json:"edgeId"`
	Topic       *string          `json:"topic"`
	Recipe      domain.RecipeDoc `json:"recipe"`
}

func (req controlRecipeRequest) input() (store.ControlRecipeInput, map[string]string) {
	f := map[string]string{}
	if !ids.Valid(req.OrderID) {
		f["orderId"] = "must be a UUID"
	}
	if !ids.Valid(req.ResourceID) {
		f["resourceId"] = "must be a UUID"
	}
	if !domain.ValidDestination(req.Destination) {
		f["destination"] = "must be kiosk, pcs-opcua or pcs-mqtt"
		return store.ControlRecipeInput{}, f
	}
	topic := req.Topic
	if topic != nil {
		t := strings.TrimSpace(*topic)
		topic = &t
	}
	switch req.Destination {
	case domain.DestKiosk:
		if req.EdgeID != nil || topic != nil {
			f["destination"] = "a kiosk recipe has no edge node or topic"
		}
	case domain.DestPCSOPCUA, domain.DestPCSMQTT:
		if req.EdgeID == nil || !ids.Valid(*req.EdgeID) {
			f["edgeId"] = "is required for a process control destination: the edge node's UUID"
		}
		switch {
		case req.Destination == domain.DestPCSMQTT && (topic == nil || *topic == "" || len(*topic) > 300):
			f["topic"] = "is required for pcs-mqtt, 1 to 300 characters"
		case req.Destination == domain.DestPCSOPCUA && topic != nil:
			f["topic"] = "applies to pcs-mqtt only"
		}
	}
	for k, v := range req.Recipe.FieldErrors(req.Destination) {
		f[k] = v
	}
	in := store.ControlRecipeInput{OrderID: req.OrderID, ResourceID: req.ResourceID, Destination: req.Destination, EdgeID: req.EdgeID, Topic: topic, Recipe: req.Recipe}
	return in, f
}

func (a *API) createControlRecipe(w http.ResponseWriter, r *http.Request) {
	if !validTenant(w, r) {
		return
	}
	user, ok := authenticatedUser(w, r, "generating a control recipe")
	if !ok {
		return
	}
	idem, ok := readIdem(w, r)
	if !ok {
		return
	}
	var req controlRecipeRequest
	if err := httpx.Decode(r, &req); err != nil {
		httpx.Fail(w, r, err)
		return
	}
	in, fields := req.input()
	if len(fields) > 0 {
		validationFailed(w, r, fields)
		return
	}
	if a.replayed(w, r, idem) {
		return
	}
	out, err := a.store.CreateControlRecipe(r.Context(), in, user, idem)
	if err != nil {
		a.fail(w, r, "create control recipe", err)
		return
	}
	a.recipesSent.Add(1)
	httpx.JSON(w, http.StatusCreated, out)
}

func (a *API) listControlRecipes(w http.ResponseWriter, r *http.Request) {
	if !validTenant(w, r) {
		return
	}
	limit, ok := limitParam(w, r, 200, 1000)
	if !ok {
		return
	}
	q := r.URL.Query()
	f := store.RecipeFilter{Status: q.Get("status"), Destination: q.Get("destination"), OrderID: q.Get("orderId"),
		ResourceID: q.Get("resourceId"), EdgeID: q.Get("edgeId"), Limit: limit}
	fields := map[string]string{}
	switch f.Status {
	case "", domain.RecipeSent, domain.RecipeReceived, domain.RecipeInExecution, domain.RecipeCompleted, domain.RecipeError:
	default:
		fields["status"] = "must be sent, received, in-execution, completed or error"
	}
	if f.Destination != "" && !domain.ValidDestination(f.Destination) {
		fields["destination"] = "must be kiosk, pcs-opcua or pcs-mqtt"
	}
	for name, v := range map[string]string{"orderId": f.OrderID, "resourceId": f.ResourceID, "edgeId": f.EdgeID} {
		if v != "" && !ids.Valid(v) {
			fields[name] = "must be a UUID"
		}
	}
	if len(fields) > 0 {
		validationFailed(w, r, fields)
		return
	}
	items, err := a.store.ListControlRecipes(r.Context(), f)
	if err != nil {
		a.fail(w, r, "list control recipes", err)
		return
	}
	httpx.JSON(w, http.StatusOK, itemsResponse[store.ControlRecipe]{Items: items})
}

func (a *API) getControlRecipe(w http.ResponseWriter, r *http.Request) {
	if !validTenant(w, r) {
		return
	}
	id, ok := pathID(w, r, store.CodeRecipeNotFound, "control recipe")
	if !ok {
		return
	}
	out, err := a.store.GetControlRecipe(r.Context(), id)
	if err != nil {
		a.fail(w, r, "get control recipe", err)
		return
	}
	httpx.JSON(w, http.StatusOK, out)
}

type recipeReportRequest struct {
	Status string  `json:"status"`
	Error  *string `json:"error"`
}

func reportStatusFields(status string, errText *string, required bool) map[string]string {
	f := map[string]string{}
	switch status {
	case domain.RecipeReceived, domain.RecipeInExecution, domain.RecipeCompleted, domain.RecipeError:
	case "":
		if required {
			f["status"] = "is required: received, in-execution, completed or error"
		}
	default:
		f["status"] = "must be received, in-execution, completed or error"
	}
	if errText != nil && (len(strings.TrimSpace(*errText)) < 1 || len(*errText) > 1000) {
		f["error"] = "must be 1 to 1000 characters"
	}
	if status == domain.RecipeError && errText == nil {
		f["error"] = "is required with status error: say what went wrong"
	}
	return f
}

func (a *API) reportControlRecipe(w http.ResponseWriter, r *http.Request) {
	if !validTenant(w, r) {
		return
	}
	user, ok := authenticatedUser(w, r, "reporting a control recipe")
	if !ok {
		return
	}
	id, ok := pathID(w, r, store.CodeRecipeNotFound, "control recipe")
	if !ok {
		return
	}
	var req recipeReportRequest
	if err := httpx.Decode(r, &req); err != nil {
		httpx.Fail(w, r, err)
		return
	}
	if fields := reportStatusFields(req.Status, req.Error, true); len(fields) > 0 {
		validationFailed(w, r, fields)
		return
	}
	out, err := a.store.ReportControlRecipe(r.Context(), id, req.Status, req.Error, user)
	if err != nil {
		a.fail(w, r, "report control recipe", err)
		return
	}
	httpx.JSON(w, http.StatusOK, out)
}

type recipeResendRequest struct {
	Force  bool   `json:"force"`
	Reason string `json:"reason"`
}

func (a *API) resendControlRecipe(w http.ResponseWriter, r *http.Request) {
	if !validTenant(w, r) {
		return
	}
	user, ok := authenticatedUser(w, r, "resending a control recipe")
	if !ok {
		return
	}
	id, ok := pathID(w, r, store.CodeRecipeNotFound, "control recipe")
	if !ok {
		return
	}
	idem, ok := readIdem(w, r)
	if !ok {
		return
	}
	var req recipeResendRequest
	if err := httpx.Decode(r, &req); err != nil {
		httpx.Fail(w, r, err)
		return
	}
	if len(req.Reason) > 1000 {
		validationFailed(w, r, map[string]string{"reason": "must be at most 1000 characters"})
		return
	}
	if a.replayed(w, r, idem) {
		return
	}
	out, err := a.store.ResendControlRecipe(r.Context(), id, req.Force, req.Reason, user, idem)
	if err != nil {
		a.fail(w, r, "resend control recipe", err)
		return
	}
	a.recipesResent.Add(1)
	httpx.JSON(w, http.StatusOK, out)
}

func (a *API) edgeControlRecipes(w http.ResponseWriter, r *http.Request) {
	if !validTenant(w, r) {
		return
	}
	id, ok := pathID(w, r, store.CodeEdgeNotFound, "edge node")
	if !ok {
		return
	}
	items, err := a.store.EdgeControlRecipes(r.Context(), id, connectorSubject(r))
	if err != nil {
		a.fail(w, r, "pull control recipes", err)
		return
	}
	httpx.JSON(w, http.StatusOK, itemsResponse[store.ControlRecipe]{Items: items})
}

type edgeWriteRequest struct {
	PhasePath string           `json:"phasePath"`
	Node      string           `json:"node"`
	Value     decimal.Decimal  `json:"value"`
	Readback  *decimal.Decimal `json:"readback"`
	NodeType  string           `json:"nodeType"`
	Written   bool             `json:"written"`
	Reason    *string          `json:"reason"`
	At        *time.Time       `json:"at"`
}

type edgeRecipeReportRequest struct {
	recipeReportRequest
	Writes []edgeWriteRequest `json:"writes"`
}

func (a *API) edgeReportControlRecipe(w http.ResponseWriter, r *http.Request) {
	if !validTenant(w, r) {
		return
	}
	edgeID, ok := pathID(w, r, store.CodeEdgeNotFound, "edge node")
	if !ok {
		return
	}
	recipeID := chi.URLParam(r, "rid")
	if !ids.Valid(recipeID) {
		httpx.Fail(w, r, httpx.NewProblem(http.StatusNotFound, store.CodeRecipeNotFound, "control recipe not found"))
		return
	}
	var req edgeRecipeReportRequest
	if err := httpx.Decode(r, &req); err != nil {
		httpx.Fail(w, r, err)
		return
	}
	fields := reportStatusFields(req.Status, req.Error, false)
	if req.Status == "" && len(req.Writes) == 0 {
		fields["status"] = "is required unless the report carries writes"
	}
	if len(req.Writes) > maxRecipeWrites {
		fields["writes"] = "must hold at most 200 writes"
	}
	rep := store.EdgeRecipeReport{Status: req.Status, Error: req.Error}
	for _, wr := range req.Writes {
		if strings.TrimSpace(wr.PhasePath) == "" || len(wr.PhasePath) > 500 || strings.TrimSpace(wr.Node) == "" || len(wr.Node) > 300 {
			fields["writes"] = "each write needs a phasePath (at most 500 characters) and a node (at most 300)"
			break
		}
		if wr.Reason != nil && len(*wr.Reason) > 1000 {
			fields["writes"] = "reason must be at most 1000 characters"
			break
		}
		if !domain.KnownNodeType(wr.NodeType) {
			fields["writes"] = "nodeType must be an OPC-UA numeric or Boolean type (Double, Float, Int32, ...) or left out"
			break
		}
		e := store.EdgeWrite{PhasePath: wr.PhasePath, Node: wr.Node, Value: wr.Value, Readback: wr.Readback, NodeType: wr.NodeType, Written: wr.Written, Reason: wr.Reason}
		if wr.At != nil {
			e.At = wr.At.UTC()
		}
		rep.Writes = append(rep.Writes, e)
	}
	if len(fields) > 0 {
		validationFailed(w, r, fields)
		return
	}
	out, err := a.store.EdgeReportControlRecipe(r.Context(), edgeID, connectorSubject(r), recipeID, rep)
	if err != nil {
		a.fail(w, r, "edge control recipe report", err)
		return
	}
	httpx.JSON(w, http.StatusOK, out)
}
