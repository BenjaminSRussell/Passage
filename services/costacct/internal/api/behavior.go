package api

import (
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/BenjaminSRussell/passage/platform/authz"
	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/services/costacct/internal/domain"
	"github.com/BenjaminSRussell/passage/services/costacct/internal/store"
)

func (a *API) registerBehavior(r chi.Router) {
	r.With(authz.RequireDuty(DutyMaintain)).Post("/v1/controlling/cost-behavior-rules", a.createBehaviorRule)
	r.With(authz.RequireDuty(DutyView)).Get("/v1/controlling/cost-behavior-rules", a.listBehaviorRules)
}

// createBehaviorRule stores an effective-dated cost behavior rule (FR-38.16).
// Rules classify journal lines mirrored after they are stored; entries that
// already exist keep their class.
func (a *API) createBehaviorRule(w http.ResponseWriter, r *http.Request) {
	var in store.BehaviorRule
	key, hash, ok := prepare(w, r, &in)
	if !ok {
		return
	}
	_, dateErr := time.Parse("2006-01-02", in.EffectiveFrom)
	switch {
	case in.ID != "" || in.CreatedAt != "":
		invalid(w, r, "id and createdAt are assigned by the service")
	case !ids.Valid(in.LegalEntityID) || !ids.Valid(in.ElementID):
		invalid(w, r, "legalEntityId and costElementId must be UUIDs")
	case in.ObjectKind != "" && !domain.ValidObjectKind(in.ObjectKind):
		invalid(w, r, "objectKind must be CostCenter, InternalOrder, Product or Project, or empty for every kind")
	case !domain.ValidBehavior(in.Behavior):
		invalid(w, r, "behavior must be Fixed or Variable")
	case dateErr != nil:
		invalid(w, r, "effectiveFrom must be a date as YYYY-MM-DD")
	default:
		status, raw, err := a.st.CreateBehaviorRule(r.Context(), key, hash, in)
		writeCommand(w, r, status, raw, err)
	}
}

func (a *API) listBehaviorRules(w http.ResponseWriter, r *http.Request) {
	le, ok := legalEntityFilter(w, r)
	if !ok {
		return
	}
	element := r.URL.Query().Get("costElementId")
	if element != "" && !ids.Valid(element) {
		invalid(w, r, "costElementId must be a UUID")
		return
	}
	rows, err := a.st.ListBehaviorRules(r.Context(), le, element)
	list(w, r, rows, err)
}
