package api

import (
	"encoding/json"
	"net/http"
	"regexp"
	"strings"

	"github.com/go-chi/chi/v5"

	"github.com/BenjaminSRussell/passage/platform/authz"
	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/services/shopfloor/internal/domain"
	"github.com/BenjaminSRussell/passage/services/shopfloor/internal/store"
)

// DutyPISheetMaintain designs, releases and retires PI sheets (FR-25.16). Reading them needs the view duty, and
// making a control recipe from a released sheet needs the control recipe create duty.
const DutyPISheetMaintain = "shopfloor.pisheet.maintain"

var piCode = regexp.MustCompile(`^[A-Z0-9][A-Z0-9_.-]{0,39}$`)

// registerPISheets mounts the PI sheet designer's routes.
func (a *API) registerPISheets(r chi.Router) {
	r.With(authz.RequireDuty(DutyPISheetMaintain)).Post("/v1/shopfloor/pi-sheets", a.createPISheet)
	r.With(authz.RequireDuty(DutyView)).Get("/v1/shopfloor/pi-sheets", a.listPISheets)
	r.With(authz.RequireDuty(DutyView)).Get("/v1/shopfloor/pi-sheets/{id}", a.getPISheet)
	r.With(authz.RequireDuty(DutyPISheetMaintain)).Put("/v1/shopfloor/pi-sheets/{id}", a.updatePISheet)
	r.With(authz.RequireDuty(DutyPISheetMaintain)).Post("/v1/shopfloor/pi-sheets/{id}:release", a.releasePISheet)
	r.With(authz.RequireDuty(DutyPISheetMaintain)).Post("/v1/shopfloor/pi-sheets/{id}:new-version", a.newPISheetVersion)
	r.With(authz.RequireDuty(DutyPISheetMaintain)).Post("/v1/shopfloor/pi-sheets/{id}:obsolete", a.obsoletePISheet)
	r.With(authz.RequireDuty(DutyControlRecipeCreate)).Post("/v1/shopfloor/control-recipes:from-pi-sheet", a.createRecipeFromPISheet)
}

type piSheetRequest struct {
	Code   string           `json:"code"`
	Name   string           `json:"name"`
	Rev    int              `json:"rev"`
	Recipe domain.RecipeDoc `json:"recipe"`
}

// docFields validates a sheet's document the way a kiosk recipe is validated: the structure, the instruction
// categories, the setpoint limits. A PI sheet carries no node, since it is made for a kiosk and for any destination.
func piDocFields(doc domain.RecipeDoc) map[string]string {
	return doc.FieldErrors(domain.DestKiosk)
}

func (a *API) createPISheet(w http.ResponseWriter, r *http.Request) {
	if !validTenant(w, r) {
		return
	}
	user, ok := authenticatedUser(w, r, "designing a PI sheet")
	if !ok {
		return
	}
	var req piSheetRequest
	if err := httpx.Decode(r, &req); err != nil {
		httpx.Fail(w, r, err)
		return
	}
	req.Code, req.Name = strings.TrimSpace(req.Code), strings.TrimSpace(req.Name)
	f := piDocFields(req.Recipe)
	if !piCode.MatchString(req.Code) {
		f["code"] = "must be 1 to 40 characters: upper-case letters, digits, '_', '.' or '-'"
	}
	if n := len(req.Name); n < 1 || n > 120 {
		f["name"] = "must be 1 to 120 characters"
	}
	if len(f) > 0 {
		validationFailed(w, r, f)
		return
	}
	out, err := a.store.CreatePISheet(r.Context(), req.Code, req.Name, req.Recipe, user)
	if err != nil {
		a.fail(w, r, "create PI sheet", err)
		return
	}
	httpx.JSON(w, http.StatusCreated, out)
}

func (a *API) listPISheets(w http.ResponseWriter, r *http.Request) {
	if !validTenant(w, r) {
		return
	}
	limit, ok := limitParam(w, r, 200, 1000)
	if !ok {
		return
	}
	q := r.URL.Query()
	f := map[string]string{}
	if s := q.Get("status"); s != "" && s != "draft" && s != "released" && s != "obsolete" {
		f["status"] = "must be draft, released or obsolete"
	}
	if len(f) > 0 {
		validationFailed(w, r, f)
		return
	}
	out, err := a.store.ListPISheets(r.Context(), store.PISheetFilter{Status: q.Get("status"), Code: q.Get("code"), Limit: limit})
	if err != nil {
		a.fail(w, r, "list PI sheets", err)
		return
	}
	httpx.JSON(w, http.StatusOK, itemsResponse[store.PISheet]{Items: out})
}

func piID(w http.ResponseWriter, r *http.Request) (string, bool) {
	id := chi.URLParam(r, "id")
	if !ids.Valid(id) {
		httpx.Fail(w, r, httpx.NewProblem(http.StatusNotFound, store.CodePISheetNotFound, "PI sheet not found"))
		return "", false
	}
	return id, true
}

func (a *API) getPISheet(w http.ResponseWriter, r *http.Request) {
	if !validTenant(w, r) {
		return
	}
	id, ok := piID(w, r)
	if !ok {
		return
	}
	out, err := a.store.GetPISheet(r.Context(), id)
	if err != nil {
		a.fail(w, r, "get PI sheet", err)
		return
	}
	httpx.JSON(w, http.StatusOK, out)
}

func (a *API) updatePISheet(w http.ResponseWriter, r *http.Request) {
	if !validTenant(w, r) {
		return
	}
	user, ok := authenticatedUser(w, r, "designing a PI sheet")
	if !ok {
		return
	}
	id, ok := piID(w, r)
	if !ok {
		return
	}
	var req piSheetRequest
	if err := httpx.Decode(r, &req); err != nil {
		httpx.Fail(w, r, err)
		return
	}
	req.Name = strings.TrimSpace(req.Name)
	f := piDocFields(req.Recipe)
	if n := len(req.Name); n < 1 || n > 120 {
		f["name"] = "must be 1 to 120 characters"
	}
	if req.Rev < 1 {
		f["rev"] = "must be the rev you read"
	}
	if len(f) > 0 {
		validationFailed(w, r, f)
		return
	}
	out, err := a.store.UpdatePISheet(r.Context(), id, req.Rev, req.Name, req.Recipe, user)
	if err != nil {
		a.fail(w, r, "update PI sheet", err)
		return
	}
	httpx.JSON(w, http.StatusOK, out)
}

type revRequest struct {
	Rev int `json:"rev"`
}

func (a *API) piTransition(op string, run func(a *API, r *http.Request, id string, rev int, user string) (store.PISheet, error)) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !validTenant(w, r) {
			return
		}
		user, ok := authenticatedUser(w, r, "changing a PI sheet")
		if !ok {
			return
		}
		id, ok := piID(w, r)
		if !ok {
			return
		}
		var req revRequest
		if r.ContentLength != 0 {
			if err := httpx.Decode(r, &req); err != nil {
				httpx.Fail(w, r, err)
				return
			}
		}
		out, err := run(a, r, id, req.Rev, user)
		if err != nil {
			a.fail(w, r, op, err)
			return
		}
		httpx.JSON(w, http.StatusOK, out)
	}
}

func (a *API) releasePISheet(w http.ResponseWriter, r *http.Request) {
	a.piTransition("release PI sheet", func(a *API, r *http.Request, id string, rev int, user string) (store.PISheet, error) {
		if rev < 1 {
			return store.PISheet{}, httpx.NewProblem(http.StatusBadRequest, "COM-0001", "rev must be the rev you read")
		}
		sh, err := a.store.GetPISheet(r.Context(), id)
		if err != nil {
			return sh, err
		}
		var doc domain.RecipeDoc
		if err := jsonUnmarshal(sh.Doc, &doc); err != nil {
			return sh, err
		}
		if f := piDocFields(doc); len(f) > 0 {
			for k, v := range f {
				return sh, httpx.NewProblem(http.StatusUnprocessableEntity, store.CodePISheetUnusable, "the sheet is not complete: "+strings.TrimPrefix(k, "recipe.")+" "+v)
			}
		}
		return a.store.ReleasePISheet(r.Context(), id, rev, user)
	})(w, r)
}

func (a *API) newPISheetVersion(w http.ResponseWriter, r *http.Request) {
	a.piTransition("new PI sheet version", func(a *API, r *http.Request, id string, _ int, user string) (store.PISheet, error) {
		return a.store.NewPISheetVersion(r.Context(), id, user)
	})(w, r)
}

func (a *API) obsoletePISheet(w http.ResponseWriter, r *http.Request) {
	a.piTransition("obsolete PI sheet", func(a *API, r *http.Request, id string, rev int, user string) (store.PISheet, error) {
		if rev < 1 {
			return store.PISheet{}, httpx.NewProblem(http.StatusBadRequest, "COM-0001", "rev must be the rev you read")
		}
		return a.store.ObsoletePISheet(r.Context(), id, rev, user)
	})(w, r)
}

type recipeFromSheetRequest struct {
	PISheetID   string  `json:"piSheetId"`
	OrderID     string  `json:"orderId"`
	ResourceID  string  `json:"resourceId"`
	Destination string  `json:"destination"`
	EdgeID      *string `json:"edgeId"`
	Topic       *string `json:"topic"`
}

func (a *API) createRecipeFromPISheet(w http.ResponseWriter, r *http.Request) {
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
	var req recipeFromSheetRequest
	if err := httpx.Decode(r, &req); err != nil {
		httpx.Fail(w, r, err)
		return
	}
	if req.Destination == "" {
		req.Destination = domain.DestKiosk
	}
	// The same checks as a recipe sent whole, except the document, which comes from the sheet.
	shell := controlRecipeRequest{OrderID: req.OrderID, ResourceID: req.ResourceID, Destination: req.Destination, EdgeID: req.EdgeID, Topic: req.Topic}
	in, f := shell.input()
	for k := range f {
		if strings.HasPrefix(k, "recipe") {
			delete(f, k) // the sheet is the document, checked for the destination when it is copied
		}
	}
	if !ids.Valid(req.PISheetID) {
		f["piSheetId"] = "must be the PI sheet's UUID"
	}
	if len(f) > 0 {
		validationFailed(w, r, f)
		return
	}
	if a.replayed(w, r, idem) {
		return
	}
	out, err := a.store.CreateControlRecipeFromPISheet(r.Context(), req.PISheetID, in, user, idem)
	if err != nil {
		a.fail(w, r, "create control recipe from PI sheet", err)
		return
	}
	a.recipesSent.Add(1)
	httpx.JSON(w, http.StatusCreated, out)
}

func jsonUnmarshal(b []byte, v any) error { return json.Unmarshal(b, v) }
