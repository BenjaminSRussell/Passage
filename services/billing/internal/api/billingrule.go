package api

import (
	"context"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/BenjaminSRussell/passage/platform/authz"
	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/platform/tenant"
	"github.com/BenjaminSRussell/passage/services/billing/internal/domain"
	"github.com/BenjaminSRussell/passage/services/billing/internal/store"
)

// The SELF_BILLED billing rule (#35, 35-5): a customer that self-bills under an agreement issues the invoice for our deliveries
// itself, so billing proposes none of its own for that customer's shipments while the agreement is valid.
const (
	// CodeSelfBilled refuses invoice proposals from the packing slips of a self-billing customer.
	CodeSelfBilled = "BIL-0150"
	// CodeRuleVersion is a billing rule changed since the caller read it.
	CodeRuleVersion = "BIL-0151"
	pathBillingRule = "/v1/billing/customers/{customerId}/billing-rule"
)

// BillingRuleStore keeps customers' billing rules; *store.Postgres implements it.
type BillingRuleStore interface {
	PutBillingRule(ctx context.Context, r store.BillingRule, ifMatch int) (store.BillingRule, error)
	GetBillingRule(ctx context.Context, legalEntityID, customerID string) (store.BillingRule, bool, error)
}

var _ BillingRuleStore = (*store.Postgres)(nil)

func (a *API) rules() BillingRuleStore {
	s, _ := a.store.(BillingRuleStore)
	return s
}

func (a *API) registerBillingRules(r chi.Router) {
	r.With(authz.RequireDuty(dutyMaintain)).Put(pathBillingRule, a.putBillingRule)
	r.With(authz.RequireDuty(dutyView)).Get(pathBillingRule, a.getBillingRule)
}

func rulesUnavailable() *httpx.Problem {
	return httpx.NewProblem(http.StatusServiceUnavailable, "COM-0503", "billing rules need the database store")
}

type billingRuleBody struct {
	LegalEntityID string `json:"legalEntityId"`
	Rule          string `json:"rule"`
	AgreementRef  string `json:"agreementRef"`
	ValidFrom     string `json:"validFrom"`
	ValidTo       string `json:"validTo"`
}

func optionalDate(fields map[string]string, name, raw string) *time.Time {
	if raw == "" {
		return nil
	}
	d, err := time.Parse(dateLayout, raw)
	if err != nil {
		fields[name] = "must be a YYYY-MM-DD date"
		return nil
	}
	return &d
}

// putBillingRule sets a customer's billing rule in a legal entity. If-Match carries the version read (none for a customer without a
// rule); a stale one is 409 BIL-0151. SELF_BILLED needs the agreement reference and its first valid day.
func (a *API) putBillingRule(w http.ResponseWriter, r *http.Request) {
	if a.rules() == nil {
		httpx.Fail(w, r, rulesUnavailable())
		return
	}
	customer := strings.ToLower(chi.URLParam(r, "customerId"))
	var body billingRuleBody
	if err := httpx.Decode(r, &body); err != nil {
		httpx.Fail(w, r, err)
		return
	}
	fields := map[string]string{}
	if !ids.Valid(customer) {
		fields["customerId"] = "must be a UUID"
	}
	if !ids.Valid(body.LegalEntityID) {
		fields["legalEntityId"] = "must be a UUID"
	}
	if body.Rule != store.RuleStandard && body.Rule != store.RuleSelfBilled {
		fields["rule"] = "must be STANDARD or SELF_BILLED"
	}
	body.AgreementRef = strings.TrimSpace(body.AgreementRef)
	if len(body.AgreementRef) > 100 {
		fields["agreementRef"] = "must be at most 100 characters"
	}
	from, to := optionalDate(fields, "validFrom", body.ValidFrom), optionalDate(fields, "validTo", body.ValidTo)
	if body.Rule == store.RuleSelfBilled {
		if body.AgreementRef == "" {
			fields["agreementRef"] = "is required for SELF_BILLED: the self-billing agreement on file"
		}
		if from == nil && fields["validFrom"] == "" {
			fields["validFrom"] = "is required for SELF_BILLED"
		}
	}
	if from != nil && to != nil && to.Before(*from) {
		fields["validTo"] = "must not be before validFrom"
	}
	version, ok := parseIfMatch(r.Header.Get("If-Match"))
	if !ok {
		fields["If-Match"] = "must be the version last read"
	}
	if len(fields) > 0 {
		writeFields(w, r, fields)
		return
	}
	p, _ := tenant.FromContext(r.Context())
	if !tenant.InLegalEntityScope(p, body.LegalEntityID) {
		httpx.Fail(w, r, httpx.NewProblem(http.StatusForbidden, "AUTH-0003", "the caller is scoped to another legal entity"))
		return
	}
	out, err := a.rules().PutBillingRule(r.Context(), store.BillingRule{LegalEntityID: strings.ToLower(body.LegalEntityID), CustomerID: customer, Rule: body.Rule,
		AgreementRef: body.AgreementRef, ValidFrom: from, ValidTo: to, UpdatedBy: p.UserID}, version)
	if errors.Is(err, store.ErrRuleVersion) {
		httpx.Fail(w, r, httpx.NewProblem(http.StatusConflict, CodeRuleVersion, "the billing rule was changed since it was read; read it again"))
		return
	}
	if err != nil {
		httpx.Fail(w, r, err)
		return
	}
	w.Header().Set("ETag", strconv.Quote(strconv.Itoa(out.Version)))
	httpx.JSON(w, http.StatusOK, out)
}

func (a *API) getBillingRule(w http.ResponseWriter, r *http.Request) {
	if a.rules() == nil {
		httpx.Fail(w, r, rulesUnavailable())
		return
	}
	customer := strings.ToLower(chi.URLParam(r, "customerId"))
	le := r.URL.Query().Get("legalEntityId")
	if !ids.Valid(customer) || !ids.Valid(le) {
		writeFields(w, r, map[string]string{"legalEntityId": "customerId and legalEntityId must be UUIDs"})
		return
	}
	if p, _ := tenant.FromContext(r.Context()); !tenant.InLegalEntityScope(p, le) {
		httpx.Fail(w, r, httpx.NewProblem(http.StatusForbidden, "AUTH-0003", "the caller is scoped to another legal entity"))
		return
	}
	out, found, err := a.rules().GetBillingRule(r.Context(), strings.ToLower(le), customer)
	if err != nil {
		httpx.Fail(w, r, err)
		return
	}
	if found {
		w.Header().Set("ETag", strconv.Quote(strconv.Itoa(out.Version)))
	}
	httpx.JSON(w, http.StatusOK, out)
}

// refuseSelfBilled is the problem for invoice proposals of packing slips the customer self-bills: a slip whose own posting date
// falls within the self-billing agreement. The caller's invoice date does not decide it, so backdating the invoice cannot bill a
// slip the customer self-bills, and a slip shipped before the agreement is still invoiced. nil otherwise.
func (a *API) refuseSelfBilled(ctx context.Context, legalEntityID, customerID string, slips []domain.Slip) error {
	rs := a.rules()
	if rs == nil {
		return nil
	}
	rule, found, err := rs.GetBillingRule(ctx, strings.ToLower(legalEntityID), strings.ToLower(customerID))
	if err != nil || !found {
		return err
	}
	for _, sl := range slips {
		if rule.SelfBilledOn(sl.Date) {
			return httpx.NewProblem(http.StatusConflict, CodeSelfBilled, "the packing slip posted "+sl.Date.Format(dateLayout)+" falls under the customer's self-billing agreement "+
				rule.AgreementRef+": its self-billed document is the invoice for that shipment, so billing proposes none")
		}
	}
	return nil
}
