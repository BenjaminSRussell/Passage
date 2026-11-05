package api

import (
	"net/http"
	"strconv"
	"strings"

	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/platform/tenant"
	"github.com/BenjaminSRussell/passage/services/assets/internal/domain"
	"github.com/BenjaminSRussell/passage/services/assets/internal/store"
)

// DutyCapexApprove covers the approver's decisions on capex requests and their
// release and closing (FR-37.17, PRD 7.37.17 assets.capex-request.approve).
const DutyCapexApprove = "assets.capex-request.approve"

const (
	maxDecisionComment = 1000
	minTaxYear         = 1900
	maxTaxYear         = 2200
)

type capexDecisionBody struct {
	Comment string `json:"comment"`
}

// decideCapex returns the handler of one decision route. The body is optional
// ({"comment": "..."}); reject and postpone need a comment so the requester
// knows why. The version comes from If-Match, as for submit.
func (a *API) decideCapex(decision domain.CapexDecision) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id, ok := capexID(w, r)
		if !ok {
			return
		}
		if p, _ := tenant.FromContext(r.Context()); p.UserID == "" {
			httpx.Fail(w, r, httpx.NewProblem(http.StatusUnauthorized, "AUTH-0001", "a capex decision needs an authenticated user"))
			return
		}
		version, ok := ifMatch(w, r)
		if !ok {
			return
		}
		var body capexDecisionBody
		if r.ContentLength != 0 {
			if err := httpx.Decode(r, &body); err != nil {
				httpx.Fail(w, r, err)
				return
			}
		}
		comment := strings.TrimSpace(body.Comment)
		switch {
		case len([]rune(comment)) > maxDecisionComment:
			writeValidation(w, r, map[string]string{"comment": "must be at most 1000 characters"})
			return
		case comment == "" && (decision == domain.DecisionReject || decision == domain.DecisionPostpone):
			writeValidation(w, r, map[string]string{"comment": "is required to " + string(decision) + " a request"})
			return
		}
		in := store.CapexDecisionInput{Decision: decision, Version: version}
		if comment != "" {
			in.Comment = &comment
		}
		out, err := a.store.DecideCapexRequest(r.Context(), id, in)
		if err != nil {
			writeDomain(w, r, err)
			return
		}
		a.decided.Add(1)
		setETag(w, out.Version)
		httpx.JSON(w, http.StatusOK, out)
	}
}

// midQuarterTest runs the BR-37.5 test on demand for one tax book code of a
// legal entity and one tax year (the mid-quarter-test job, 7.37.16, run by
// hand). It changes nothing.
func (a *API) midQuarterTest(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	fields := map[string]string{}
	legalEntity := q.Get("legalEntityId")
	if !ids.Valid(legalEntity) {
		fields["legalEntityId"] = "must be a UUID"
	}
	code := q.Get("bookCode")
	if !bookCode.MatchString(code) {
		fields["bookCode"] = "must be a book code such as TAX"
	}
	year, err := strconv.Atoi(q.Get("year"))
	if err != nil || year < minTaxYear || year > maxTaxYear {
		fields["year"] = "must be a four-digit year"
	}
	if len(fields) > 0 {
		writeValidation(w, r, fields)
		return
	}
	if !inScope(w, r, legalEntity) {
		return
	}
	report, err := a.store.MidQuarterTest(r.Context(), store.MidQuarterFilter{LegalEntityID: legalEntity, BookCode: code, Year: year})
	if err != nil {
		httpx.Fail(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, report)
}
