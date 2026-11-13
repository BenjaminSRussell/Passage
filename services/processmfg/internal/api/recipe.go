package api

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
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
	// An order has one control recipe: once it is made, a repeat answers with it. A recipe in error is sent again
	// by the shopfloor service (POST /v1/shopfloor/control-recipes/{id}:resend), not made a second time here.
	if link, err := a.store.GetControlRecipeLink(r.Context(), o.ID); err != nil {
		fail(w, r, err)
		return
	} else if link.ControlRecipeID != nil {
		httpx.JSON(w, http.StatusOK, link)
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
		// Read the order again under its lock, clearing a previous error: from here until the link is stored the
		// potency adjustments cannot change, so the recipe carries the ones read now.
		var claimed bool
		var cur store.BatchOrder
		if cur, claimed, err = a.store.ClaimRecipeGeneration(ctx, o.ID); err == nil {
			if !claimed {
				return a.currentLink(ctx, o.ID), true
			}
			o = cur
		}
	}
	if err == nil {
		var doc map[string]any
		doc, err = ReleaseRecipe(o, target.Destination)
		req := shopfloor.RecipeRequest{OrderID: o.ID, ResourceID: target.ResourceID, Destination: target.Destination, EdgeID: target.EdgeID,
			Topic: target.Topic, Recipe: doc}
		var key, recipeID string
		if err == nil {
			key, err = RecipeKey(o.ID, req)
		}
		if err == nil {
			recipeID, err = a.recipes.CreateRecipe(ctx, o.LegalEntityID, key, req)
		}
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

// currentLink is the order's stored link, for a generation that found the recipe already made.
func (a *API) currentLink(ctx context.Context, orderID string) store.ControlRecipeLink {
	link, err := a.store.GetControlRecipeLink(ctx, orderID)
	if err != nil {
		slog.Warn("control recipe link not read", "order", orderID, "err", err)
	}
	return link
}

// RecipeKey is the Idempotency-Key of the request that makes an order's control recipe: the order and a hash
// of the request. The same request sent again (a retry after a lost answer) replays the one recipe; a request
// with another body (the order rescaled after a failed generation, or its target changed) is a new key, never
// a reuse of the old one with a new body, which the shopfloor service refuses.
func RecipeKey(orderID string, req shopfloor.RecipeRequest) (string, error) {
	b, err := json.Marshal(req)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(b)
	return "batch-order-recipe-" + orderID + "-" + hex.EncodeToString(sum[:8]), nil
}

// ReleaseRecipe is the control recipe document of a released batch order: one unit procedure that makes the
// batch, one operation with a phase for the charge, and in it the instruction to make the batch and the
// material list scaled to the batch size (BR-24.1). A process control system gets the same document; its own
// setpoints are the plant engineer's to add to the master recipe, which this build does not hold.
//
// An order with potency adjustments (BR-24.2) charges its active ingredients at planned × standard / batch potency
// and its filler lines with what is left, so the batch keeps its size; each adjustment is named in the material
// list's text. Adjustments that no longer fit the order (a rescale emptied the filler) are an error.
func ReleaseRecipe(o store.BatchOrder, destination string) (map[string]any, error) {
	adjusted, err := store.PotencyAdjusted(o)
	if err != nil {
		return nil, err
	}
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
		qty := l.Qty
		if v, ok := adjusted[l.LineNumber]; ok {
			qty = v
		}
		m := map[string]any{"itemId": l.ItemID, "qty": qty.String(), "role": role}
		// Formula lines carry no unit of their own. A percent-controlled line is the batch × percent / 100
		// (BR-24.1), so it is in the batch's unit; the unit of any other line is not recorded and is left out.
		if l.PercentControlled && o.Unit != "" {
			m["unit"] = o.Unit
		}
		materials = append(materials, m)
	}
	instructions := []map[string]any{
		{"seq": 1, "category": "instruction-text", "text": fmt.Sprintf("Make %s %s under batch order %s (formula %s, version %d).",
			o.BatchSize.String(), o.Unit, o.OrderNumber, o.FormulaCode, o.FormulaVersion)},
		{"seq": 2, "category": "material-list", "text": materialText(len(materials), o.PotencyAdjustments), "materials": materials},
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
	}}, nil
}

// materialText is the plain-language summary of the material list, naming each potency adjustment.
func materialText(n int, adjs []domain.PotencyAdjustment) string {
	text := fmt.Sprintf("Charge the %d materials of the formula, scaled to the batch.", n)
	for _, a := range adjs {
		batch := ""
		if a.BatchRef != "" {
			batch = " from batch " + a.BatchRef
		}
		text += fmt.Sprintf(" Line %d is potency-adjusted%s (%s against the standard %s); line %d compensates.", a.LineNumber, batch,
			a.BatchPotency.String(), a.StandardPotency.String(), a.CompensatingLine)
	}
	return text
}

type potencyAdjustmentsRequest struct {
	Adjustments []domain.PotencyAdjustment `json:"adjustments"`
}

// setPotencyAdjustments replaces the potency adjustments of an order (BR-24.2): the ingredient batches of another
// potency its active lines are charged from, each with the filler line that compensates. Its control recipe, made
// at release or by :send-recipe, carries the adjusted quantities; once it is made they can no longer change.
func (a *API) setPotencyAdjustments(w http.ResponseWriter, r *http.Request) {
	p, ok := principal(w, r)
	if !ok {
		return
	}
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	var req potencyAdjustmentsRequest
	if err := httpx.Decode(r, &req); err != nil {
		httpx.Fail(w, r, err)
		return
	}
	fields := map[string]string{}
	if len(req.Adjustments) > 50 {
		fields["adjustments"] = "must hold at most 50 adjustments"
	}
	for i, adj := range req.Adjustments {
		if !adj.StandardPotency.IsPositive() || !adj.BatchPotency.IsPositive() {
			fields[fmt.Sprintf("adjustments[%d]", i)] = "standardPotency and batchPotency must be positive"
		}
		if len(adj.BatchRef) > 100 {
			fields[fmt.Sprintf("adjustments[%d].batchRef", i)] = "must be at most 100 characters"
		}
	}
	if len(fields) > 0 {
		invalid(w, r, fields)
		return
	}
	user := p.UserID
	if user == "" {
		user = "unknown"
	}
	o, err := a.store.SetPotencyAdjustments(r.Context(), id, req.Adjustments, user)
	if err != nil {
		fail(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, o)
}
