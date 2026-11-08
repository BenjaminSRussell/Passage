package api

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/services/sales/internal/domain"
	"github.com/BenjaminSRussell/passage/services/sales/internal/store"
)

// slice4Store records the quotation, hold queue and supplementary calls.
type slice4Store struct {
	fakeStore
	quotation   *store.NewQuotation
	quoteFilter *store.QuotationFilter
	convert     *store.ConvertRequest
	sendVersion int
	lostReason  string
	queue       *store.HoldQueueFilter
	rule        *domain.SupplementaryRule
	ruleActor   string
	ruleFilter  *store.SupplementaryRuleFilter
	rules       []domain.SupplementaryRule
	deleted     string
}

func (s *slice4Store) CreateQuotation(_ context.Context, in store.NewQuotation) (store.Quotation, bool, error) {
	s.quotation = &in
	return store.Quotation{ID: ids.New(), Version: 1, Status: domain.QuoteDraft}, true, nil
}

func (s *slice4Store) ListQuotations(_ context.Context, f store.QuotationFilter) ([]store.Quotation, error) {
	s.quoteFilter = &f
	return []store.Quotation{}, nil
}

func (s *slice4Store) SendQuotation(_ context.Context, id string, version int, _ time.Time) (store.Quotation, error) {
	s.sendVersion = version
	return store.Quotation{ID: id, Version: version + 1, Status: domain.QuoteSent}, nil
}

func (s *slice4Store) LoseQuotation(_ context.Context, id string, version int, reason string) (store.Quotation, error) {
	s.lostReason = reason
	return store.Quotation{ID: id, Version: version + 1, Status: domain.QuoteLost}, nil
}

func (s *slice4Store) ConvertQuotation(_ context.Context, id string, in store.ConvertRequest, _ time.Time) (store.QuotationConversion, error) {
	s.convert = &in
	return store.QuotationConversion{Quotation: store.Quotation{ID: id, Version: in.Version + 1, Status: domain.QuoteConverted},
		Order: store.Order{ID: ids.New(), Channel: "QUOTE"}}, nil
}

func (s *slice4Store) ListHolds(_ context.Context, f store.HoldQueueFilter) ([]store.HoldQueueItem, error) {
	s.queue = &f
	return []store.HoldQueueItem{}, nil
}

func (s *slice4Store) CreateSupplementaryRule(_ context.Context, in domain.SupplementaryRule, actor string) (domain.SupplementaryRule, error) {
	s.rule, s.ruleActor = &in, actor
	in.ID = ids.New()
	return in, nil
}

func (s *slice4Store) ListSupplementaryRules(_ context.Context, f store.SupplementaryRuleFilter) ([]domain.SupplementaryRule, error) {
	s.ruleFilter = &f
	if s.rules == nil {
		return []domain.SupplementaryRule{}, nil
	}
	return s.rules, nil
}

func (s *slice4Store) DeleteSupplementaryRule(_ context.Context, id string) error {
	s.deleted = id
	return nil
}

func quoteRequest(lines int) createQuotationRequest {
	req := createQuotationRequest{LegalEntityID: ids.New(), CustomerID: ids.New(), Currency: "USD"}
	for i := 0; i < lines; i++ {
		req.Lines = append(req.Lines, orderLineRequest{ProductID: ids.New(), Quantity: "3", Unit: "EA",
			SiteID: ids.New(), WarehouseID: ids.New(), UnitPrice: "19.99", DiscPct: strPtr("10"), RequestedShipDate: "2027-03-04"})
	}
	return req
}

func TestBuildQuotation_DefaultsAndNets(t *testing.T) {
	today := time.Date(2027, 3, 1, 23, 30, 0, 0, time.UTC)
	req := quoteRequest(2)
	in, fields := buildQuotation(req, today)
	if len(fields) != 0 {
		t.Fatalf("unexpected field errors: %v", fields)
	}
	if !in.ValidUntil.IsZero() || !in.IssuedAt.Equal(today) {
		t.Fatalf("validUntil %s issuedAt %s: an omitted validity is left to the store's parameters", in.ValidUntil, in.IssuedAt)
	}
	if in.ProbabilityPct != nil || in.InvoiceAccountID != req.CustomerID {
		t.Fatalf("defaults: probability %v (an omitted one is left to the store's parameters) invoice %s", in.ProbabilityPct, in.InvoiceAccountID)
	}
	// 3 × 19.99 less 10% = 53.973 at four places.
	if len(in.Lines) != 2 || in.Lines[1].LineNo != 2 || !in.Lines[0].LineNet.Equal(decimal.RequireFromString("53.973")) {
		t.Fatalf("lines not built: %+v", in.Lines)
	}
}

func TestBuildQuotation_RejectsBadFields(t *testing.T) {
	today := time.Date(2027, 3, 1, 0, 0, 0, 0, time.UTC)
	for _, tc := range []struct {
		name  string
		edit  func(*createQuotationRequest)
		field string
	}{
		{"validity in the past", func(r *createQuotationRequest) { r.ValidUntil = strPtr("2027-02-28") }, "validUntil"},
		{"validity beyond a year", func(r *createQuotationRequest) { r.ValidUntil = strPtr("2028-03-02") }, "validUntil"},
		{"validity not a date", func(r *createQuotationRequest) { r.ValidUntil = strPtr("31/03/2027") }, "validUntil"},
		{"probability above 100", func(r *createQuotationRequest) { p := 101; r.ProbabilityPct = &p }, "probabilityPct"},
		{"no legal entity", func(r *createQuotationRequest) { r.LegalEntityID = "" }, "legalEntityId"},
		{"no lines", func(r *createQuotationRequest) { r.Lines = nil }, "lines"},
		{"bad line price", func(r *createQuotationRequest) { r.Lines[0].UnitPrice = "ten" }, "lines[0].unitPrice"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			req := quoteRequest(1)
			tc.edit(&req)
			if _, fields := buildQuotation(req, today); fields[tc.field] == "" {
				t.Fatalf("want an error on %s, got %v", tc.field, fields)
			}
		})
	}
	for _, tc := range []struct {
		name  string
		edit  func(*createQuotationRequest)
		field string
	}{
		{"empty customer group", func(r *createQuotationRequest) { r.CustomerGroup = strPtr("") }, "customerGroup"},
		{"long customer group", func(r *createQuotationRequest) { r.CustomerGroup = strPtr(strings.Repeat("g", maxScopeKey+1)) }, "customerGroup"},
		{"long override reason", func(r *createQuotationRequest) { r.ListingOverrideReason = strings.Repeat("r", maxOverrideReason+1) }, "listingOverrideReason"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			req := quoteRequest(1)
			tc.edit(&req)
			if _, fields := buildQuotation(req, today); fields[tc.field] == "" {
				t.Fatalf("want an error on %s, got %v", tc.field, fields)
			}
		})
	}
	req := quoteRequest(1)
	req.ValidUntil = strPtr("2027-03-01")
	if _, fields := buildQuotation(req, today); len(fields) != 0 {
		t.Fatalf("a quotation valid only today is allowed: %v", fields)
	}
}

func TestCreateQuotation_KeyScopeAndDuty(t *testing.T) {
	req := quoteRequest(1)
	req.LegalEntityID = testLE
	raw, _ := json.Marshal(req)
	body := string(raw)

	s := &slice4Store{}
	if rec := serveBody(t, s, http.MethodPost, quotations, body, principal(dutyQuotation), nil); rec.Code != http.StatusBadRequest || s.quotation != nil {
		t.Fatalf("missing Idempotency-Key: status %d", rec.Code)
	}
	if rec := serveBody(t, s, http.MethodPost, quotations, body, principal(dutyWrite), map[string]string{idempotencyKey: "q1"}); rec.Code != http.StatusForbidden {
		t.Fatalf("sales.order.write alone must not create quotations: status %d", rec.Code)
	}
	scoped := principal(dutyQuotation)
	scoped.LegalEntityID = ids.New()
	if rec := serveBody(t, s, http.MethodPost, quotations, body, scoped, map[string]string{idempotencyKey: "q1"}); rec.Code != http.StatusForbidden || s.quotation != nil {
		t.Fatalf("another legal entity: status %d", rec.Code)
	}
	rec := serveBody(t, s, http.MethodPost, quotations, body, principal(dutyQuotation), map[string]string{idempotencyKey: "q1"})
	if rec.Code != http.StatusCreated || rec.Header().Get("ETag") != `"1"` {
		t.Fatalf("status %d body %s", rec.Code, rec.Body.String())
	}
	if s.quotation.IdempotencyKey != "q1" || s.quotation.RequestHash == "" || s.quotation.Actor != "u1" || s.quotation.LegalEntityID != req.LegalEntityID {
		t.Fatalf("store got %+v", s.quotation)
	}
}

func TestQuotationLifecycleHandlers(t *testing.T) {
	id := ids.New()
	p := principal(dutyQuotation)

	s := &slice4Store{}
	if rec := serveBody(t, s, http.MethodPost, quotations+"/"+id+":send", "", p, nil); rec.Code != http.StatusPreconditionRequired {
		t.Fatalf("send without If-Match: status %d", rec.Code)
	}
	if rec := serveBody(t, s, http.MethodPost, quotations+"/"+id+":send", "", p, map[string]string{"If-Match": `"4"`}); rec.Code != http.StatusOK ||
		s.sendVersion != 4 || rec.Header().Get("ETag") != `"5"` {
		t.Fatalf("send: status %d version %d", rec.Code, s.sendVersion)
	}
	if rec := serveBody(t, s, http.MethodPost, quotations+"/"+id+":lose", `{"reason":"   "}`, p, map[string]string{"If-Match": `"1"`}); rec.Code != http.StatusBadRequest {
		t.Fatalf("blank reason: status %d", rec.Code)
	}
	if rec := serveBody(t, s, http.MethodPost, quotations+"/"+id+":lose", `{"reason":" price "}`, p, map[string]string{"If-Match": `"1"`}); rec.Code != http.StatusOK ||
		s.lostReason != "price" {
		t.Fatalf("lose: status %d reason %q", rec.Code, s.lostReason)
	}
	if rec := serveBody(t, s, http.MethodPost, quotations+"/not-a-uuid:send", "", p, map[string]string{"If-Match": `"1"`}); rec.Code != http.StatusNotFound {
		t.Fatalf("bad id: status %d", rec.Code)
	}
}

func TestConvertQuotation(t *testing.T) {
	path := quotations + "/" + ids.New() + ":convert"
	match := map[string]string{"If-Match": `"2"`}

	s := &slice4Store{}
	if rec := serveBody(t, s, http.MethodPost, path, "", principal(dutyWrite), match); rec.Code != http.StatusForbidden {
		t.Fatalf("convert needs sales.quotation.maintain: status %d", rec.Code)
	}
	if rec := serveBody(t, s, http.MethodPost, path, `{"lineNos":[1,0]}`, principal(dutyQuotation), match); rec.Code != http.StatusBadRequest || s.convert != nil {
		t.Fatalf("line 0: status %d", rec.Code)
	}
	if rec := serveBody(t, s, http.MethodPost, path, "", principal(dutyQuotation), nil); rec.Code != http.StatusPreconditionRequired {
		t.Fatalf("missing If-Match: status %d", rec.Code)
	}
	rec := serveBody(t, s, http.MethodPost, path, "", principal(dutyQuotation), match)
	if rec.Code != http.StatusCreated || s.convert.Version != 2 || len(s.convert.LineNos) != 0 || s.convert.CanOverrideListing || s.convert.Actor != "u1" {
		t.Fatalf("no body converts every open line: status %d %+v", rec.Code, s.convert)
	}
	var res struct {
		Quotation struct{ Status string }  `json:"quotation"`
		Order     struct{ Channel string } `json:"order"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &res); err != nil || res.Quotation.Status != domain.QuoteConverted || res.Order.Channel != "QUOTE" {
		t.Fatalf("body %s", rec.Body.String())
	}
	rec = serveBody(t, s, http.MethodPost, path, `{"lineNos":[1,3],"listingOverrideReason":"agreed"}`, principal(dutyQuotation, dutyOrderApprove), match)
	if rec.Code != http.StatusCreated || len(s.convert.LineNos) != 2 || !s.convert.CanOverrideListing || s.convert.ListingOverrideReason != "agreed" {
		t.Fatalf("selected lines with an override: status %d %+v", rec.Code, s.convert)
	}
}

func TestListQuotations_Filters(t *testing.T) {
	s := &slice4Store{}
	if rec := serve(t, s, http.MethodGet, quotations+"?status=OPEN", principal(dutyRead), nil); rec.Code != http.StatusBadRequest || s.quoteFilter != nil {
		t.Fatalf("unknown status: %d", rec.Code)
	}
	cust := ids.New()
	rec := serve(t, s, http.MethodGet, quotations+"?status=SENT&customerId="+cust+"&limit=10", allScopePrincipal(dutyRead), nil)
	if rec.Code != http.StatusOK || rec.Body.String() != "{\"items\":[]}\n" {
		t.Fatalf("status %d body %q", rec.Code, rec.Body.String())
	}
	if *s.quoteFilter != (store.QuotationFilter{Status: "SENT", CustomerID: cust, Limit: 10}) {
		t.Fatalf("filter %+v", s.quoteFilter)
	}
}

func TestListHolds_FilterAndDuty(t *testing.T) {
	s := &slice4Store{}
	if rec := serve(t, s, http.MethodGet, holdQueue, principal(dutyRead), nil); rec.Code != http.StatusForbidden {
		t.Fatalf("the queue needs sales.credit.release or sales.order.hold: %d", rec.Code)
	}
	if rec := serve(t, s, http.MethodGet, holdQueue+"?holdType=SOMETHING", principal(dutyCreditRelease), nil); rec.Code != http.StatusBadRequest {
		t.Fatalf("unknown hold type: %d", rec.Code)
	}
	p := principal(dutyCreditRelease)
	p.LegalEntityID = ids.New()
	rec := serve(t, s, http.MethodGet, holdQueue+"?holdType=CREDIT", p, nil)
	if rec.Code != http.StatusOK || *s.queue != (store.HoldQueueFilter{LegalEntityID: p.LegalEntityID, HoldType: "CREDIT", Limit: defaultPageSize}) {
		t.Fatalf("status %d filter %+v", rec.Code, s.queue)
	}
	if rec := serve(t, s, http.MethodGet, holdQueue+"?legalEntityId="+ids.New(), p, nil); rec.Code != http.StatusForbidden {
		t.Fatalf("another legal entity: %d", rec.Code)
	}
}

func TestBuildSupplementaryRule_Table(t *testing.T) {
	valid := func() supplementaryRuleRequest {
		return supplementaryRuleRequest{LegalEntityID: ids.New(), ParentProductID: ids.New(), SupplementaryProduct: ids.New(), Unit: "EA",
			Requirement: domain.SuppMandatory, BaseQuantity: "10", QuantityPerBase: "1", Pricing: domain.SuppFree}
	}
	if rule, fields := buildSupplementaryRule(valid()); len(fields) != 0 || !rule.UnitPrice.IsZero() {
		t.Fatalf("a FREE rule needs no price: %v", fields)
	}
	for _, tc := range []struct {
		name  string
		edit  func(*supplementaryRuleRequest)
		field string
	}{
		{"FREE with a price", func(r *supplementaryRuleRequest) { r.UnitPrice = "3.00" }, "unitPrice"},
		{"PRICED without a price", func(r *supplementaryRuleRequest) { r.Pricing = domain.SuppPriced }, "unitPrice"},
		{"price with 5 places", func(r *supplementaryRuleRequest) { r.Pricing, r.UnitPrice = domain.SuppPriced, "1.00001" }, "unitPrice"},
		{"base not a decimal", func(r *supplementaryRuleRequest) { r.BaseQuantity = "ten" }, "baseQuantity"},
		{"bad customer", func(r *supplementaryRuleRequest) { r.CustomerID = strPtr("C9") }, "customerId"},
		{"item is the parent", func(r *supplementaryRuleRequest) { r.SupplementaryProduct = r.ParentProductID }, "supplementaryProductId"},
		{"no unit", func(r *supplementaryRuleRequest) { r.Unit = "" }, "unit"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			req := valid()
			tc.edit(&req)
			if _, fields := buildSupplementaryRule(req); fields[tc.field] == "" {
				t.Fatalf("want an error on %s, got %v", tc.field, fields)
			}
		})
	}
}

func TestSupplementaryHandlers(t *testing.T) {
	parent, manual := ids.New(), ids.New()
	le := testLE
	s := &slice4Store{rules: []domain.SupplementaryRule{{ID: "r1", LegalEntityID: le, ParentProductID: parent, SupplementaryProduct: manual,
		Unit: "EA", Requirement: domain.SuppMandatory, BaseQuantity: decimal.NewFromInt(10), QuantityPerBase: decimal.NewFromInt(1), Pricing: domain.SuppFree}}}

	body := `{"legalEntityId":"` + le + `","productId":"` + parent + `","customerId":"` + ids.New() + `","quantity":"25"}`
	rec := serveBody(t, s, http.MethodPost, suppEvaluate, body, principal(dutyRead), nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("evaluate: status %d body %s", rec.Code, rec.Body.String())
	}
	var got supplementaryEvaluation
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil || len(got.Items) != 1 || !got.Items[0].Quantity.Equal(decimal.NewFromInt(2)) ||
		got.Items[0].ProductID != manual || got.Items[0].SupplementaryOf != parent {
		t.Fatalf("AC-27.10 evaluation: %s", rec.Body.String())
	}
	if s.ruleFilter.LegalEntityID != le || s.ruleFilter.ParentProductID != parent {
		t.Fatalf("rules read with %+v", s.ruleFilter)
	}
	rec = serveBody(t, s, http.MethodPost, suppEvaluate, `{"legalEntityId":"`+le+`","productId":"`+parent+`","quantity":"5"}`, principal(dutyRead), nil)
	if rec.Code != http.StatusOK || rec.Body.String() != "{\"items\":[]}\n" {
		t.Fatalf("below base quantity is an empty list, not null: %s", rec.Body.String())
	}
	if rec := serveBody(t, s, http.MethodPost, suppEvaluate, `{"legalEntityId":"`+le+`","productId":"`+parent+`","quantity":"-1"}`, principal(dutyRead), nil); rec.Code != http.StatusBadRequest {
		t.Fatalf("negative quantity: %d", rec.Code)
	}

	rule := `{"legalEntityId":"` + le + `","parentProductId":"` + parent + `","supplementaryProductId":"` + manual +
		`","unit":"EA","requirement":"OPTIONAL","baseQuantity":"10","quantityPerBase":"1","pricing":"PRICED","unitPrice":"2.50"}`
	if rec := serveBody(t, s, http.MethodPost, suppRules, rule, principal(dutyRead), nil); rec.Code != http.StatusForbidden {
		t.Fatalf("create needs sales.listing.maintain: %d", rec.Code)
	}
	rec = serveBody(t, s, http.MethodPost, suppRules, rule, principal(dutyListing), nil)
	if rec.Code != http.StatusCreated || s.ruleActor != "u1" || !s.rule.UnitPrice.Equal(decimal.RequireFromString("2.5")) || s.rule.Requirement != domain.SuppOptional {
		t.Fatalf("create: status %d %+v", rec.Code, s.rule)
	}
	if rec := serve(t, s, http.MethodGet, suppRules+"?parentProductId=nope", principal(dutyRead), nil); rec.Code != http.StatusBadRequest {
		t.Fatalf("bad parent filter: %d", rec.Code)
	}
	if rec := serve(t, s, http.MethodDelete, suppRules+"/not-a-uuid", principal(dutyListing), nil); rec.Code != http.StatusNotFound {
		t.Fatalf("bad id: %d", rec.Code)
	}
	id := ids.New()
	if rec := serve(t, s, http.MethodDelete, suppRules+"/"+id, principal(dutyListing), nil); rec.Code != http.StatusNoContent || s.deleted != id {
		t.Fatalf("delete: %d", rec.Code)
	}
}

// TestCreateQuotation_PassesGroupAndOverrideDuty: the customer group and the
// override reason reach the store, and only a caller with sales.order.approve
// can override the listing at quotation entry (FR-27.15).
func TestCreateQuotation_PassesGroupAndOverrideDuty(t *testing.T) {
	req := quoteRequest(1)
	req.LegalEntityID = testLE
	req.CustomerGroup = strPtr("EU-RETAIL")
	req.ListingOverrideReason = "customer accepted"
	raw, _ := json.Marshal(req)
	s := &slice4Store{}
	if rec := serveBody(t, s, http.MethodPost, quotations, string(raw), principal(dutyQuotation), map[string]string{idempotencyKey: "q1"}); rec.Code != http.StatusCreated {
		t.Fatalf("status %d body %s", rec.Code, rec.Body.String())
	}
	if s.quotation.CustomerGroup == nil || *s.quotation.CustomerGroup != "EU-RETAIL" || s.quotation.ListingOverrideReason != "customer accepted" || s.quotation.CanOverrideListing {
		t.Fatalf("without the approve duty the override must not be allowed: %+v", s.quotation)
	}
	s = &slice4Store{}
	if rec := serveBody(t, s, http.MethodPost, quotations, string(raw), principal(dutyQuotation, dutyOrderApprove), map[string]string{idempotencyKey: "q2"}); rec.Code != http.StatusCreated {
		t.Fatalf("status %d body %s", rec.Code, rec.Body.String())
	}
	if !s.quotation.CanOverrideListing {
		t.Fatalf("sales.order.approve must allow the override: %+v", s.quotation)
	}
}
