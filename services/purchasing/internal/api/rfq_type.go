package api

import (
	"context"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/BenjaminSRussell/passage/platform/authz"
	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/services/purchasing/internal/domain"
	"github.com/BenjaminSRussell/passage/services/purchasing/internal/store"
)

// rfqTypeBody is the POST body of an RFQ type. PUT takes the same fields; code and legalEntityId are
// ignored there because they do not change.
type rfqTypeBody struct {
	LegalEntityID     string             `json:"legalEntityId"`
	Code              string             `json:"code"`
	Name              string             `json:"name"`
	Sealed            bool               `json:"sealed,omitempty"`
	AmendmentsAllowed *bool              `json:"amendmentsAllowed,omitempty"`
	RankingRule       string             `json:"rankingRule,omitempty"`
	Status            string             `json:"status,omitempty"`
	Criteria          []rfqCriterionBody `json:"criteria,omitempty"`
}

// criteriaFrom turns criteria bodies into domain criteria (weights are decimal strings).
func criteriaFrom(in []rfqCriterionBody) ([]domain.ScoringCriterion, error) {
	out := make([]domain.ScoringCriterion, 0, len(in))
	for i, c := range in {
		weight, err := decimalField("criteria["+strconv.Itoa(i)+"].weight", c.Weight, "")
		if err != nil {
			return nil, err
		}
		out = append(out, domain.ScoringCriterion{Name: strings.TrimSpace(c.Name), Weight: weight})
	}
	return out, nil
}

// rfqTypeFrom validates a body into an RFQ type: amendments allowed by default, ranking by lowest price,
// ACTIVE.
func rfqTypeFrom(body rfqTypeBody) (domain.RFQType, error) {
	if err := uuidField("legalEntityId", body.LegalEntityID, true); err != nil {
		return domain.RFQType{}, err
	}
	criteria, err := criteriaFrom(body.Criteria)
	if err != nil {
		return domain.RFQType{}, err
	}
	allowed := true
	if body.AmendmentsAllowed != nil {
		allowed = *body.AmendmentsAllowed
	}
	t := domain.RFQType{LegalEntityID: body.LegalEntityID, Code: strings.ToUpper(strings.TrimSpace(body.Code)),
		Name: strings.TrimSpace(body.Name), Sealed: body.Sealed, AmendmentsAllowed: allowed,
		RankingRule: orDefault(body.RankingRule, domain.RankLowestPrice), Status: orDefault(body.Status, domain.RFQTypeActive),
		Criteria: criteria}
	if err := t.Validate(); err != nil {
		return domain.RFQType{}, invalid("rfqType", err.(*domain.Error).Detail)
	}
	return t, nil
}

func (a *API) createRFQType(w http.ResponseWriter, r *http.Request) {
	p, err := principal(r.Context())
	if err != nil {
		fail(w, r, err)
		return
	}
	key, err := idempotencyKey(r)
	if err != nil {
		fail(w, r, err)
		return
	}
	var body rfqTypeBody
	if err := httpx.Decode(r, &body); err != nil {
		fail(w, r, err)
		return
	}
	t, err := rfqTypeFrom(body)
	if err != nil {
		fail(w, r, err)
		return
	}
	if err := inScope(p, t.LegalEntityID); err != nil {
		fail(w, r, err)
		return
	}
	out, err := a.store.CreateRFQType(r.Context(), t, store.Idem{Key: key, Hash: hashBody("rfq-type.create", body)})
	if err != nil {
		fail(w, r, err)
		return
	}
	a.rfqTypesSaved.Add(1)
	w.Header().Set("ETag", etag(out.Version))
	httpx.JSON(w, http.StatusCreated, out)
}

func (a *API) getRFQType(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r)
	if err != nil {
		fail(w, r, err)
		return
	}
	out, err := a.store.GetRFQType(r.Context(), id)
	if err != nil {
		fail(w, r, err)
		return
	}
	w.Header().Set("ETag", etag(out.Version))
	httpx.JSON(w, http.StatusOK, out)
}

func (a *API) listRFQTypes(w http.ResponseWriter, r *http.Request) {
	limit, after, err := listParams(r)
	if err != nil {
		fail(w, r, err)
		return
	}
	q := r.URL.Query()
	f := store.RFQTypeFilter{LegalEntityID: q.Get("legalEntityId"), Status: q.Get("status")}
	if err := uuidField("legalEntityId", f.LegalEntityID, false); err != nil {
		fail(w, r, err)
		return
	}
	if f.Status != "" && f.Status != domain.RFQTypeActive && f.Status != domain.RFQTypeInactive {
		fail(w, r, invalid("status", "must be ACTIVE or INACTIVE"))
		return
	}
	page, err := a.store.ListRFQTypes(r.Context(), f, after, limit)
	if err != nil {
		fail(w, r, err)
		return
	}
	writePage(w, page)
}

// changeRFQType handles PUT /rfq-types/{id}: it replaces everything but the code and the legal entity under
// If-Match. RFQs already created keep the flags they copied.
func (a *API) changeRFQType(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r)
	if err != nil {
		fail(w, r, err)
		return
	}
	want, err := ifMatch(r)
	if err != nil {
		fail(w, r, err)
		return
	}
	var body rfqTypeBody
	if err := httpx.Decode(r, &body); err != nil {
		fail(w, r, err)
		return
	}
	cur, err := a.store.GetRFQType(r.Context(), id)
	if err != nil {
		fail(w, r, err)
		return
	}
	body.LegalEntityID, body.Code = cur.LegalEntityID, cur.Code
	t, err := rfqTypeFrom(body)
	if err != nil {
		fail(w, r, err)
		return
	}
	out, err := a.store.UpdateRFQType(r.Context(), id, want, t)
	if err != nil {
		fail(w, r, err)
		return
	}
	a.rfqTypesSaved.Add(1)
	w.Header().Set("ETag", etag(out.Version))
	httpx.JSON(w, http.StatusOK, out)
}

// applyRFQType fills an RFQ request from its type (FR-17.19): the sealed flag (a sealed type forces it), the
// ranking rule and the scoring group the request left out. The type must be ACTIVE and belong to the RFQ's
// legal entity. It returns the type, or nil when the request names none.
func (a *API) applyRFQType(ctx context.Context, body *rfqBody) (*domain.RFQType, error) {
	if body.RFQTypeID == "" {
		return nil, nil
	}
	if err := uuidField("rfqTypeId", body.RFQTypeID, false); err != nil {
		return nil, err
	}
	t, err := a.store.GetRFQType(ctx, body.RFQTypeID)
	if err != nil {
		return nil, err
	}
	if t.Status != domain.RFQTypeActive {
		return nil, &domain.Error{Code: domain.CodeRFQTypeInvalid, Detail: "RFQ type " + t.Code + " is inactive"}
	}
	if t.LegalEntityID != body.LegalEntityID {
		return nil, &domain.Error{Code: domain.CodeRFQTypeInvalid, Detail: "RFQ type " + t.Code + " belongs to another legal entity"}
	}
	body.Sealed = body.Sealed || t.Sealed
	if body.RankingRule == "" {
		body.RankingRule = t.RankingRule
	}
	if len(body.Criteria) == 0 {
		for _, c := range t.Criteria {
			body.Criteria = append(body.Criteria, rfqCriterionBody{Name: c.Name, Weight: c.Weight.String()})
		}
	}
	return &t, nil
}

// withType records the type an RFQ was created from and the flags it copied.
func withType(in domain.RFQ, t *domain.RFQType) domain.RFQ {
	in.AmendmentsAllowed = true
	if t != nil {
		in.RFQTypeID, in.AmendmentsAllowed, in.SealedRequired = t.ID, t.AmendmentsAllowed, t.Sealed
	}
	return in
}

// rfqEditBody is the body of PUT /rfqs/{id}.
type rfqEditBody struct {
	ReplyDeadline time.Time          `json:"replyDeadline"`
	RankingRule   string             `json:"rankingRule,omitempty"`
	Sealed        bool               `json:"sealed,omitempty"`
	Criteria      []rfqCriterionBody `json:"criteria,omitempty"`
}

// editRFQ handles PUT /rfqs/{id} (procurement.rfq.maintain, If-Match): the deadline, ranking rule, sealed
// flag and scoring criteria of a DRAFT RFQ (FR-17.19). A sent RFQ changes only by amendment.
func (a *API) editRFQ(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r)
	if err != nil {
		fail(w, r, err)
		return
	}
	want, err := ifMatch(r)
	if err != nil {
		fail(w, r, err)
		return
	}
	var body rfqEditBody
	if err := httpx.Decode(r, &body); err != nil {
		fail(w, r, err)
		return
	}
	if body.ReplyDeadline.IsZero() {
		fail(w, r, invalid("replyDeadline", "is required"))
		return
	}
	criteria, err := criteriaFrom(body.Criteria)
	if err != nil {
		fail(w, r, err)
		return
	}
	in := domain.RFQEdit{ReplyDeadline: body.ReplyDeadline.UTC(), RankingRule: orDefault(body.RankingRule, domain.RankLowestPrice),
		Sealed: body.Sealed, Criteria: criteria}
	if domain.ValidRankingRule(in.RankingRule) != nil {
		fail(w, r, invalid("rankingRule", "must be LOWEST_PRICE, LEAD_TIME or WEIGHTED_SCORE"))
		return
	}
	if err := domain.ValidateCriteria(in.RankingRule, in.Criteria); err != nil {
		fail(w, r, invalid("criteria", err.(*domain.Error).Detail))
		return
	}
	out, err := a.store.EditRFQ(r.Context(), id, want, in, a.now())
	if err != nil {
		fail(w, r, err)
		return
	}
	a.rfqsEdited.Add(1)
	a.writeRFQ(w, r, http.StatusOK, out)
}

// rfqAmendmentBody is the body of POST /rfqs/{id}/amendments. A criteria member, even empty, replaces the
// scoring group; leaving it out keeps the group.
type rfqAmendmentBody struct {
	Summary       string              `json:"summary"`
	ReplyDeadline *time.Time          `json:"replyDeadline,omitempty"`
	Criteria      *[]rfqCriterionBody `json:"criteria,omitempty"`
}

// amendRFQ handles POST /rfqs/{id}/amendments (procurement.rfq.maintain, Idempotency-Key, If-Match): an
// addendum to a sent RFQ with a summary, optionally a later reply deadline and a new scoring group; every
// invited vendor is asked to acknowledge it (FR-17.19).
func (a *API) amendRFQ(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r)
	if err != nil {
		fail(w, r, err)
		return
	}
	key, err := idempotencyKey(r)
	if err != nil {
		fail(w, r, err)
		return
	}
	want, err := ifMatch(r)
	if err != nil {
		fail(w, r, err)
		return
	}
	var body rfqAmendmentBody
	if err := httpx.Decode(r, &body); err != nil {
		fail(w, r, err)
		return
	}
	in := domain.AmendmentInput{Summary: strings.TrimSpace(body.Summary)}
	if in.Summary == "" {
		fail(w, r, invalid("summary", "is required"))
		return
	}
	if body.ReplyDeadline != nil {
		d := body.ReplyDeadline.UTC()
		in.ReplyDeadline = &d
	}
	if body.Criteria != nil {
		if in.Criteria, err = criteriaFrom(*body.Criteria); err != nil {
			fail(w, r, err)
			return
		}
		in.ReplaceCriteria = true
	}
	idem := store.Idem{Key: key, Hash: hashBody("rfq.amend", map[string]any{"id": id, "ifMatch": want, "body": body})}
	out, err := a.store.AmendRFQ(r.Context(), id, want, in, a.now(), idem)
	if err != nil {
		fail(w, r, err)
		return
	}
	a.rfqsAmended.Add(1)
	a.writeRFQ(w, r, http.StatusCreated, out)
}

// acknowledgeAmendment handles PUT /rfqs/{id}/amendments/{no}/acknowledgements/{vendorId}
// (procurement.rfq.maintain): the buyer records that an invited vendor acknowledged the amendment. It is
// idempotent and does not change the RFQ version.
func (a *API) acknowledgeAmendment(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r)
	if err != nil {
		fail(w, r, err)
		return
	}
	no, err := strconv.Atoi(chi.URLParam(r, "no"))
	if err != nil || no < 1 {
		fail(w, r, invalid("no", "must be a positive amendment number"))
		return
	}
	vendorID := chi.URLParam(r, "vendorId")
	if err := uuidField("vendorId", vendorID, true); err != nil {
		fail(w, r, err)
		return
	}
	out, err := a.store.AcknowledgeAmendment(r.Context(), id, no, vendorID, a.now())
	if err != nil {
		fail(w, r, err)
		return
	}
	a.rfqAmendmentsAcked.Add(1)
	a.writeRFQ(w, r, http.StatusOK, out)
}

// registerRFQTypeRoutes mounts the RFQ type setup routes under /v1/procurement. Writes need
// procurement.policy.maintain; reads also open to the RFQ duties, since buyers pick a type.
func (a *API) registerRFQTypeRoutes(r chi.Router) {
	write := r.With(authz.RequireDuty(dutyPolicyMaintain))
	read := r.With(authz.Require(anyDuty{dutyPolicyMaintain, dutyRFQMaintain, dutyRFQAward},
		dutyPolicyMaintain+" or "+dutyRFQMaintain+" or "+dutyRFQAward, ""))
	write.Post("/rfq-types", a.createRFQType)
	read.Get("/rfq-types", a.listRFQTypes)
	read.Get("/rfq-types/{id}", a.getRFQType)
	write.Put("/rfq-types/{id}", a.changeRFQType)
	rfqMaintain := r.With(authz.RequireDuty(dutyRFQMaintain))
	rfqMaintain.Put("/rfqs/{id}", a.editRFQ)
	rfqMaintain.Post("/rfqs/{id}/amendments", a.amendRFQ)
	rfqMaintain.Put("/rfqs/{id}/amendments/{no}/acknowledgements/{vendorId}", a.acknowledgeAmendment)
}
