package api

import (
	"net/http"
	"strconv"
	"time"

	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/services/purchasing/internal/store"
)

// rfqFromRequisitionsBody is the body of POST /rfqs:from-requisitions (FR-17.3): approved requisition lines that
// need sourcing become the lines of a DRAFT RFQ. Product, description, quantity, unit and the requested date
// are copied from the requisition line; the legal entity is the requisitions'.
type rfqFromRequisitionsBody struct {
	RequisitionLineIDs []string           `json:"requisitionLineIds"`
	ReplyDeadline      time.Time          `json:"replyDeadline"`
	RankingRule        string             `json:"rankingRule,omitempty"`
	Sealed             bool               `json:"sealed,omitempty"`
	Criteria           []rfqCriterionBody `json:"criteria,omitempty"`
}

// createRFQFromRequisitions handles POST /rfqs:from-requisitions. The lines must be OPEN on APPROVED
// requisitions of one legal entity; the store checks them again under lock and refuses a line another running
// RFQ holds (PRC-REQ-006).
func (a *API) createRFQFromRequisitions(w http.ResponseWriter, r *http.Request) {
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
	var body rfqFromRequisitionsBody
	if err := httpx.Decode(r, &body); err != nil {
		fail(w, r, err)
		return
	}
	if len(body.RequisitionLineIDs) == 0 {
		fail(w, r, invalid("requisitionLineIds", "needs at least one requisition line"))
		return
	}
	lineIDs, err := uniqueIDs("requisitionLineIds", body.RequisitionLineIDs)
	if err != nil {
		fail(w, r, err)
		return
	}
	if len(lineIDs) > maxRFQLines {
		fail(w, r, invalid("requisitionLineIds", "may have at most "+strconv.Itoa(maxRFQLines)+" lines"))
		return
	}
	found, err := a.store.RequisitionLinesByID(r.Context(), lineIDs)
	if err != nil {
		fail(w, r, err)
		return
	}
	byID := make(map[string]store.SourcingLine, len(found))
	for _, l := range found {
		byID[l.ID] = l
	}
	spec := rfqBody{ReplyDeadline: body.ReplyDeadline, RankingRule: body.RankingRule, Sealed: body.Sealed, Criteria: body.Criteria}
	for _, id := range lineIDs {
		l, ok := byID[id]
		if !ok {
			fail(w, r, httpx.NewProblem(http.StatusNotFound, store.CodeRequisitionLineNotFound, "requisition line "+id+" not found"))
			return
		}
		if spec.LegalEntityID == "" {
			spec.LegalEntityID = l.LegalEntityID
		} else if spec.LegalEntityID != l.LegalEntityID {
			fail(w, r, httpx.NewProblem(http.StatusUnprocessableEntity, store.CodeRequisitionLegalEntity,
				"requisition lines of different legal entities cannot share an RFQ"))
			return
		}
		day := l.RequestedDate
		spec.Lines = append(spec.Lines, rfqLineBody{ProductID: l.ProductID, Description: l.Description, Qty: l.Qty.String(),
			Unit: l.Unit, RequiredDate: &day})
	}
	in, err := buildRFQ(spec, a.now())
	if err != nil {
		fail(w, r, err)
		return
	}
	for i := range in.Lines {
		in.Lines[i].RequisitionLineID = lineIDs[i]
	}
	if err := inScope(p, in.LegalEntityID); err != nil {
		fail(w, r, err)
		return
	}
	out, err := a.store.CreateRFQ(r.Context(), in, store.Idem{Key: key, Hash: hashBody("rfq.from-requisitions", body)})
	if err != nil {
		fail(w, r, err)
		return
	}
	a.rfqsCreated.Add(1)
	a.writeRFQ(w, r, http.StatusCreated, out)
}
