package api

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"strings"

	"github.com/go-chi/chi/v5"

	"github.com/BenjaminSRussell/passage/platform/authz"
	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/platform/tenant"
	"github.com/BenjaminSRussell/passage/services/processmfg/internal/domain"
	"github.com/BenjaminSRussell/passage/services/processmfg/internal/shopfloor"
	"github.com/BenjaminSRussell/passage/services/processmfg/internal/store"
)

// Control recipes of released batch orders (#25, 25-5). A recipe target says where the recipes of one item
// go; releasing an order of that item generates its control recipe in the shopfloor service, as the releasing
// user, and the order keeps the recipe's id, or the reason it could not be generated. A failed generation
// never undoes the release: :send-recipe repeats it.

// WithShopfloor binds the client that generates control recipes. Without it releasing generates none.
func (a *API) WithShopfloor(s shopfloor.Sender) *API {
	a.recipes = s
	return a
}

var recipeDestinations = map[string]bool{"kiosk": true, "pcs-opcua": true, "pcs-mqtt": true}

func (a *API) registerRecipes(r chi.Router) {
	r.With(authz.RequireDuty(DutyFormulaWrite)).Put("/v1/production/recipe-targets/{itemId}", a.putRecipeTarget)
	r.With(authz.RequireDuty(DutyRead)).Get("/v1/production/recipe-targets", a.listRecipeTargets)
	r.With(authz.RequireDuty(DutyFormulaWrite)).Delete("/v1/production/recipe-targets/{itemId}", a.deleteRecipeTarget)
	r.With(authz.RequireDuty(DutyRead)).Get("/v1/production/batch-orders/{id}/control-recipe", a.getControlRecipeLink)
	r.With(authz.RequireDuty(DutyBatchOrderWrite)).Post("/v1/production/batch-orders/{id}:send-recipe", a.sendRecipe)
}

type recipeTargetRequest struct {
	LegalEntityID string  `json:"legalEntityId"`
	ResourceID    string  `json:"resourceId"`
	Destination   string  `json:"destination"`
	EdgeID        *string `json:"edgeId"`
	Topic         *string `json:"topic"`
}

func (a *API) putRecipeTarget(w http.ResponseWriter, r *http.Request) {
	p, ok := principal(w, r)
	if !ok {
		return
	}
	var req recipeTargetRequest
	if err := httpx.Decode(r, &req); err != nil {
		httpx.Fail(w, r, err)
		return
	}
	item := chi.URLParam(r, "itemId")
	if req.LegalEntityID == "" {
		req.LegalEntityID = p.LegalEntityID
	}
	f := map[string]string{}
	if !ids.Valid(item) {
		f["itemId"] = "must be a UUID"
	}
	if !ids.Valid(req.LegalEntityID) {
		f["legalEntityId"] = "must be a UUID (defaults to the caller's legal entity)"
	}
	if !ids.Valid(req.ResourceID) {
		f["resourceId"] = "must be the shop floor resource's UUID"
	}
	if !recipeDestinations[req.Destination] {
		f["destination"] = "must be kiosk, pcs-opcua or pcs-mqtt"
	} else {
		topic := ""
		if req.Topic != nil {
			topic = strings.TrimSpace(*req.Topic)
			req.Topic = &topic
		}
		switch req.Destination {
		case "kiosk":
			if req.EdgeID != nil || req.Topic != nil {
				f["destination"] = "a kiosk recipe has no edge node or topic"
			}
		default:
			if req.EdgeID == nil || !ids.Valid(*req.EdgeID) {
				f["edgeId"] = "is required for a process control destination: the edge node's UUID"
			}
			if req.Destination == "pcs-mqtt" && (topic == "" || len(topic) > 300) {
				f["topic"] = "is required for pcs-mqtt, 1 to 300 characters"
			}
			if req.Destination == "pcs-opcua" && req.Topic != nil {
				f["topic"] = "applies to pcs-mqtt only"
			}
		}
	}
	if len(f) > 0 {
		invalid(w, r, f)
		return
	}
	if !tenant.InLegalEntityScope(p, req.LegalEntityID) {
		httpx.Fail(w, r, httpx.NewProblem(http.StatusForbidden, "AUTH-0003", "legal entity "+req.LegalEntityID+" is outside your scope"))
		return
	}
	out, err := a.store.PutRecipeTarget(r.Context(), store.RecipeTarget{LegalEntityID: strings.ToLower(req.LegalEntityID), ItemID: strings.ToLower(item),
		ResourceID: strings.ToLower(req.ResourceID), Destination: req.Destination, EdgeID: req.EdgeID, Topic: req.Topic})
	if err != nil {
		fail(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, out)
}

func (a *API) listRecipeTargets(w http.ResponseWriter, r *http.Request) {
	if _, ok := principal(w, r); !ok {
		return
	}
	limit, ok := limitParam(w, r)
	if !ok {
		return
	}
	out, err := a.store.ListRecipeTargets(r.Context(), limit)
	if err != nil {
		fail(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, map[string]any{"items": out})
}

func (a *API) deleteRecipeTarget(w http.ResponseWriter, r *http.Request) {
	p, ok := principal(w, r)
	if !ok {
		return
	}
	item := chi.URLParam(r, "itemId")
	le := r.URL.Query().Get("legalEntityId")
	if le == "" {
		le = p.LegalEntityID
	}
	if !ids.Valid(item) || !ids.Valid(le) {
		invalid(w, r, map[string]string{"itemId": "must be a UUID", "legalEntityId": "must be a UUID (defaults to the caller's legal entity)"})
		return
	}
	if !tenant.InLegalEntityScope(p, le) {
		httpx.Fail(w, r, httpx.NewProblem(http.StatusForbidden, "AUTH-0003", "legal entity "+le+" is outside your scope"))
		return
	}
	gone, err := a.store.DeleteRecipeTarget(r.Context(), strings.ToLower(le), strings.ToLower(item))
	if err != nil {
		fail(w, r, err)
		return
	}
	if !gone {
		httpx.Fail(w, r, httpx.NewProblem(http.StatusNotFound, "PMF-0404", "no recipe target for this item"))
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (a *API) getControlRecipeLink(w http.ResponseWriter, r *http.Request) {
	if _, ok := principal(w, r); !ok {
		return
	}
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	out, err := a.store.GetControlRecipeLink(r.Context(), id)
	if err != nil {
		fail(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, out)
}

// sendRecipe repeats the generation of a released or started order's control recipe.
func (a *API) sendRecipe(w http.ResponseWriter, r *http.Request) {
	if _, ok := principal(w, r); !ok {
		return
	}
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	if a.recipes == nil {
		httpx.Fail(w, r, httpx.NewProblem(http.StatusServiceUnavailable, "PMF-0503", "control recipes are not connected: this deployment has no SHOPFLOOR_URL"))
		return
	}
	o, err := a.store.GetBatchOrder(r.Context(), id)
	if err != nil {
		fail(w, r, err)
		return
	}
	if o.Status != "released" && o.Status != "started" {
		httpx.Fail(w, r, httpx.NewProblem(http.StatusConflict, "PMF-0504", "a control recipe is generated for a released or started batch order, this one is "+o.Status))
		return
	}
	link, generated := a.generateRecipe(r.Context(), o)
	if !generated {
		httpx.Fail(w, r, httpx.NewProblem(http.StatusUnprocessableEntity, "PMF-0505", "no recipe target is set for the item of this batch order (PUT /v1/production/recipe-targets/{itemId})"))
		return
	}
	httpx.JSON(w, http.StatusOK, link)
}

// generateRecipe builds and sends the control recipe of a released order when its item has a target. It
// reports generated=false when there is no target, and otherwise the link (the recipe id or the reason it
// failed), which it also stores on the order. It never returns an error: the release it follows has
// happened, and a failure is the order's to show.
func (a *API) generateRecipe(ctx context.Context, o store.BatchOrder) (store.ControlRecipeLink, bool) {
	if a.recipes == nil {
		return store.ControlRecipeLink{}, false
	}
	target, found, err := a.store.GetRecipeTarget(ctx, o.LegalEntityID, o.ItemID)
	if err == nil && !found {
		return store.ControlRecipeLink{}, false
	}
	var link store.ControlRecipeLink
	if err == nil {
		var recipeID string
		recipeID, err = a.recipes.CreateRecipe(ctx, o.LegalEntityID, "batch-order-recipe-"+o.ID, shopfloor.RecipeRequest{
			OrderID: o.ID, ResourceID: target.ResourceID, Destination: target.Destination, EdgeID: target.EdgeID, Topic: target.Topic,
			Recipe: ReleaseRecipe(o, target.Destination)})
		if err == nil {
			link.ControlRecipeID = &recipeID
		}
	}
	if err != nil {
		msg := err.Error()
		if len(msg) > 1000 {
			msg = msg[:1000]
		}
		link.Error = &msg
		slog.Warn("control recipe of a released batch order not generated", "order", o.ID, "err", err)
	}
	if serr := a.store.SetControlRecipe(ctx, o.ID, link.ControlRecipeID, link.Error); serr != nil {
		slog.Error("control recipe link not stored", "order", o.ID, "err", serr)
	}
	return link, true
}

// ReleaseRecipe is the control recipe document of a released batch order: one unit procedure that makes the
// batch, one operation with a phase for the charge, and in it the instruction to make the batch and the
// material list scaled to the batch size (BR-24.1). A process control system gets the same document; its own
// setpoints are the plant engineer's to add to the master recipe, which this build does not hold.
func ReleaseRecipe(o store.BatchOrder, destination string) map[string]any {
	materials := make([]map[string]any, 0, len(o.Lines))
	for _, l := range o.Lines {
		role := "ingredient"
		switch l.Type {
		case domain.LineCoProduct:
			role = "co-product"
		case domain.LineByProduct:
			role = "by-product"
		case domain.LinePhantom:
			continue // a phantom explodes into its own components and is never charged
		}
		materials = append(materials, map[string]any{"itemId": l.ItemID, "qty": l.Qty.String(), "role": role})
	}
	instructions := []map[string]any{
		{"seq": 1, "category": "instruction-text", "text": fmt.Sprintf("Make %s %s under batch order %s (formula %s, version %d).",
			o.BatchSize.String(), o.Unit, o.OrderNumber, o.FormulaCode, o.FormulaVersion)},
		{"seq": 2, "category": "material-list", "text": fmt.Sprintf("Charge the %d materials of the formula, scaled to the batch.", len(materials)), "materials": materials},
	}
	return map[string]any{"procedure": map[string]any{
		"name": "Batch " + o.OrderNumber,
		"unitProcedures": []map[string]any{{
			"name": "Make the batch",
			"operations": []map[string]any{{
				"name":   "Charge",
				"phases": []map[string]any{{"name": "Charge materials", "instructions": instructions}},
			}},
		}},
	}}
}
