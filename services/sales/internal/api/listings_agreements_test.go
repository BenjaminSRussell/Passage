package api

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/services/sales/internal/store"
)

// slice3Store records the listing, agreement and hold calls.
type slice3Store struct {
	fakeStore
	newOrder      *store.NewOrder
	listing       *store.ProductListing
	listingFilter *store.ListingFilter
	deleted       string
	agreement     *store.NewAgreement
	agreements    *store.AgreementFilter
	release       *store.AgreementRelease
	releaseFresh  bool
	hold          *store.HoldRequest
	holdVersion   int
	holdActor     string
}

func (s *slice3Store) CreateOrder(_ context.Context, in store.NewOrder) (store.Order, bool, error) {
	s.newOrder = &in
	return store.Order{ID: ids.New(), Version: 1}, true, nil
}

func (s *slice3Store) CreateListing(_ context.Context, in store.ProductListing) (store.ProductListing, error) {
	s.listing = &in
	in.ID = ids.New()
	return in, nil
}

func (s *slice3Store) ListListings(_ context.Context, f store.ListingFilter) ([]store.ProductListing, error) {
	s.listingFilter = &f
	return []store.ProductListing{}, nil
}

func (s *slice3Store) DeleteListing(_ context.Context, id string) error {
	s.deleted = id
	return nil
}

func (s *slice3Store) CreateAgreement(_ context.Context, in store.NewAgreement) (store.Agreement, bool, error) {
	s.agreement = &in
	return store.Agreement{ID: ids.New(), Version: 1, Commitment: in.Commitment, Remaining: in.Commitment}, true, nil
}

func (s *slice3Store) ListAgreements(_ context.Context, f store.AgreementFilter) ([]store.Agreement, error) {
	s.agreements = &f
	return []store.Agreement{}, nil
}

func (s *slice3Store) ReleaseAgreement(_ context.Context, id string, in store.AgreementRelease, _ time.Time) (store.ReleaseResult, bool, error) {
	s.release = &in
	return store.ReleaseResult{Agreement: store.Agreement{ID: id, Version: 3}, Order: store.Order{ID: ids.New(), Channel: "AGREEMENT"}}, s.releaseFresh, nil
}

func (s *slice3Store) GetAgreement(_ context.Context, id string) (store.Agreement, error) {
	return store.Agreement{ID: id, LegalEntityID: testLE, CustomerID: ids.New(), Version: 2}, nil
}

func (s *slice3Store) ApplyHold(_ context.Context, id string, version int, in store.HoldRequest, actor string) (store.Order, error) {
	s.hold, s.holdVersion, s.holdActor = &in, version, actor
	return store.Order{ID: id, Version: version + 1}, nil
}

func strPtr(s string) *string { return &s }

func TestBuildListing_Table(t *testing.T) {
	cust, le := ids.New(), ids.New()
	tests := []struct {
		name      string
		req       productListingRequest
		wantField string
	}{
		{name: "customer deny", req: productListingRequest{LegalEntityID: le, ScopeType: "CUSTOMER", ScopeKey: cust, ProductID: ids.New(), ListingType: "DENY"}},
		{name: "group allow with window", req: productListingRequest{LegalEntityID: le, ScopeType: "GROUP", ScopeKey: "EU-RETAIL", ProductID: ids.New(), ListingType: "ALLOW",
			ValidFrom: strPtr("2027-01-01"), ValidTo: strPtr("2027-12-31")}},
		{name: "missing legal entity", req: productListingRequest{ScopeType: "GROUP", ScopeKey: "G", ProductID: ids.New(), ListingType: "DENY"}, wantField: "legalEntityId"},
		{name: "unknown scope", req: productListingRequest{LegalEntityID: le, ScopeType: "REGION", ScopeKey: cust, ProductID: ids.New(), ListingType: "ALLOW"}, wantField: "scopeType"},
		{name: "customer key not a uuid", req: productListingRequest{LegalEntityID: le, ScopeType: "CUSTOMER", ScopeKey: "C9", ProductID: ids.New(), ListingType: "DENY"}, wantField: "scopeKey"},
		{name: "empty group key", req: productListingRequest{LegalEntityID: le, ScopeType: "GROUP", ProductID: ids.New(), ListingType: "DENY"}, wantField: "scopeKey"},
		{name: "bad product", req: productListingRequest{LegalEntityID: le, ScopeType: "GROUP", ScopeKey: "G", ProductID: "FG-120", ListingType: "DENY"}, wantField: "productId"},
		{name: "bad listing type", req: productListingRequest{LegalEntityID: le, ScopeType: "GROUP", ScopeKey: "G", ProductID: ids.New(), ListingType: "BLOCK"}, wantField: "listingType"},
		{name: "validTo before validFrom", req: productListingRequest{LegalEntityID: le, ScopeType: "GROUP", ScopeKey: "G", ProductID: ids.New(), ListingType: "ALLOW",
			ValidFrom: strPtr("2027-06-01"), ValidTo: strPtr("2027-05-31")}, wantField: "validTo"},
		{name: "unparseable date", req: productListingRequest{LegalEntityID: le, ScopeType: "GROUP", ScopeKey: "G", ProductID: ids.New(), ListingType: "ALLOW",
			ValidFrom: strPtr("04/03/2027")}, wantField: "validFrom"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			_, fields := buildListing(tc.req)
			if tc.wantField == "" {
				if len(fields) != 0 {
					t.Fatalf("unexpected field errors %v", fields)
				}
				return
			}
			if _, ok := fields[tc.wantField]; !ok {
				t.Fatalf("want field error on %q, got %v", tc.wantField, fields)
			}
		})
	}
}

func TestBuildAgreement_Table(t *testing.T) {
	good := createAgreementRequest{LegalEntityID: ids.New(), CustomerID: ids.New(), ProductID: ids.New(), Unit: "EA", UnitPrice: "12.50",
		Currency: "USD", CommitmentQty: "1000"}
	tests := []struct {
		name      string
		mutate    func(*createAgreementRequest)
		wantField string
	}{
		{name: "valid", mutate: func(*createAgreementRequest) {}},
		{name: "zero commitment", mutate: func(r *createAgreementRequest) { r.CommitmentQty = "0" }, wantField: "commitmentQty"},
		{name: "seven decimals", mutate: func(r *createAgreementRequest) { r.CommitmentQty = "1.0000001" }, wantField: "commitmentQty"},
		{name: "text commitment", mutate: func(r *createAgreementRequest) { r.CommitmentQty = "lots" }, wantField: "commitmentQty"},
		{name: "negative price", mutate: func(r *createAgreementRequest) { r.UnitPrice = "-1" }, wantField: "unitPrice"},
		{name: "price with five places", mutate: func(r *createAgreementRequest) { r.UnitPrice = "1.00001" }, wantField: "unitPrice"},
		{name: "missing unit", mutate: func(r *createAgreementRequest) { r.Unit = "" }, wantField: "unit"},
		{name: "lowercase currency", mutate: func(r *createAgreementRequest) { r.Currency = "usd" }, wantField: "currency"},
		{name: "customer not uuid", mutate: func(r *createAgreementRequest) { r.CustomerID = "C9" }, wantField: "customerId"},
		{name: "bad invoice account", mutate: func(r *createAgreementRequest) { r.InvoiceAccountID = "INV" }, wantField: "invoiceAccountId"},
		{name: "validity reversed", mutate: func(r *createAgreementRequest) { r.ValidFrom, r.ValidTo = strPtr("2027-02-01"), strPtr("2027-01-01") }, wantField: "validTo"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			req := good
			tc.mutate(&req)
			in, fields := buildAgreement(req)
			if tc.wantField == "" {
				if len(fields) != 0 {
					t.Fatalf("unexpected field errors %v", fields)
				}
				if !in.Commitment.Equal(decimal.NewFromInt(1000)) || !in.UnitPrice.Equal(decimal.RequireFromString("12.5")) {
					t.Fatalf("commitment %s price %s", in.Commitment, in.UnitPrice)
				}
				if in.InvoiceAccountID != req.CustomerID {
					t.Fatalf("invoice account defaults to the customer, got %s", in.InvoiceAccountID)
				}
				return
			}
			if _, ok := fields[tc.wantField]; !ok {
				t.Fatalf("want field error on %q, got %v", tc.wantField, fields)
			}
		})
	}
}

func orderBody(le string) string {
	return `{"legalEntityId":"` + le + `","customerId":"` + ids.New() + `","invoiceAccountId":"` + ids.New() + `","currency":"USD",
		"requestedShipDate":"2027-03-04","customerGroup":"EU-RETAIL","listingOverrideReason":"key account exception",
		"lines":[{"productId":"` + ids.New() + `","quantity":"1","unit":"EA","siteId":"` + ids.New() + `","warehouseId":"` + ids.New() + `",
		"unitPrice":"10","requestedShipDate":"2027-03-04"}]}`
}

// TestCreateOrder_ListingOverrideNeedsApproveDuty: the override reason reaches
// the store either way, but only a caller with sales.order.approve may use it
// (FR-27.15); the customer group and order date are passed for BR-27.13.
func TestCreateOrder_ListingOverrideNeedsApproveDuty(t *testing.T) {
	for _, tc := range []struct {
		duties []string
		want   bool
	}{
		{[]string{dutyWrite}, false},
		{[]string{dutyWrite, dutyOrderApprove}, true},
	} {
		s := &slice3Store{}
		rec := serveBody(t, s, http.MethodPost, orders, orderBody(testLE), principal(tc.duties...), map[string]string{idempotencyKey: "k1"})
		if rec.Code != http.StatusCreated {
			t.Fatalf("status %d body %s", rec.Code, rec.Body.String())
		}
		if s.newOrder.CanOverrideListing != tc.want || s.newOrder.ListingOverrideReason != "key account exception" {
			t.Fatalf("duties %v: override=%v reason=%q", tc.duties, s.newOrder.CanOverrideListing, s.newOrder.ListingOverrideReason)
		}
		if s.newOrder.CustomerGroup == nil || *s.newOrder.CustomerGroup != "EU-RETAIL" || s.newOrder.OrderDate.IsZero() {
			t.Fatalf("customer group %v order date %v", s.newOrder.CustomerGroup, s.newOrder.OrderDate)
		}
	}
}

// TestApplyHold covers the holds API contract: CREDIT cannot be applied by
// hand, If-Match is required, and a valid request passes the version and actor.
func TestApplyHold(t *testing.T) {
	path := orders + "/" + ids.New() + "/holds"
	p := principal(dutyHold)

	s := &slice3Store{}
	rec := serveBody(t, s, http.MethodPost, path, `{"holdType":"CREDIT","reasonCode":"X"}`, p, map[string]string{"If-Match": `"2"`})
	if rec.Code != http.StatusBadRequest || s.hold != nil {
		t.Fatalf("CREDIT by hand: status %d, store called %v", rec.Code, s.hold != nil)
	}
	rec = serveBody(t, s, http.MethodPost, path, `{"holdType":"FRAUD","reasonCode":""}`, p, map[string]string{"If-Match": `"2"`})
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("empty reason: status %d", rec.Code)
	}
	rec = serveBody(t, s, http.MethodPost, path, `{"holdType":"FRAUD","reasonCode":"CARD-MISMATCH"}`, p, nil)
	if rec.Code != http.StatusPreconditionRequired {
		t.Fatalf("missing If-Match: status %d", rec.Code)
	}
	rec = serveBody(t, s, http.MethodPost, path, `{"holdType":"FRAUD","reasonCode":"CARD-MISMATCH"}`, p, map[string]string{"If-Match": `"2"`})
	if rec.Code != http.StatusCreated || rec.Header().Get("ETag") != `"3"` {
		t.Fatalf("status %d etag %s", rec.Code, rec.Header().Get("ETag"))
	}
	if s.hold.HoldType != "FRAUD" || s.holdVersion != 2 || s.holdActor != "u1" {
		t.Fatalf("store got %+v version %d actor %s", s.hold, s.holdVersion, s.holdActor)
	}
	rec = serveBody(t, &slice3Store{}, http.MethodPost, path, `{"holdType":"FRAUD","reasonCode":"X"}`, principal(dutyRead), map[string]string{"If-Match": `"2"`})
	if rec.Code != http.StatusForbidden {
		t.Fatalf("without sales.order.hold: status %d", rec.Code)
	}
}

// TestReleaseOrder covers :release-order: the Idempotency-Key is required, the
// request reaches the store, a new release is 201 with the agreement and order,
// and a replay is 200.
func TestReleaseOrder(t *testing.T) {
	path := agreements + "/" + ids.New() + ":release-order"
	body := `{"quantity":"40","siteId":"` + ids.New() + `","warehouseId":"` + ids.New() + `","requestedShipDate":"2027-04-15"}`
	p := principal(dutyAgreement)

	s := &slice3Store{releaseFresh: true}
	if rec := serveBody(t, s, http.MethodPost, path, body, p, nil); rec.Code != http.StatusBadRequest || s.release != nil {
		t.Fatalf("missing key: status %d", rec.Code)
	}
	if rec := serveBody(t, s, http.MethodPost, path, `{"quantity":"0","siteId":"x"}`, p, map[string]string{idempotencyKey: "r1"}); rec.Code != http.StatusBadRequest {
		t.Fatalf("bad body: status %d", rec.Code)
	}
	rec := serveBody(t, s, http.MethodPost, path, body, p, map[string]string{idempotencyKey: "r1"})
	if rec.Code != http.StatusCreated {
		t.Fatalf("status %d body %s", rec.Code, rec.Body.String())
	}
	var res struct {
		Agreement struct{ Version int }    `json:"agreement"`
		Order     struct{ Channel string } `json:"order"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &res); err != nil || res.Agreement.Version != 3 || res.Order.Channel != "AGREEMENT" {
		t.Fatalf("body %s err %v", rec.Body.String(), err)
	}
	if !s.release.Quantity.Equal(decimal.NewFromInt(40)) || s.release.IdempotencyKey != "r1" || s.release.RequestHash == "" || s.release.Actor != "u1" ||
		s.release.CustomerGroup == nil || *s.release.CustomerGroup != "EU-RETAIL" {
		t.Fatalf("store got %+v", s.release)
	}
	s.releaseFresh = false
	if rec := serveBody(t, s, http.MethodPost, path, body, p, map[string]string{idempotencyKey: "r1"}); rec.Code != http.StatusOK {
		t.Fatalf("replay: status %d", rec.Code)
	}
}

// TestListingsAndAgreements_Scope: a caller scoped to one legal entity cannot
// create or list another entity's listings or agreements; within scope the
// filters reach the store.
func TestListingsAndAgreements_Scope(t *testing.T) {
	mine, other := ids.New(), ids.New()
	scoped := principal(dutyListing, dutyAgreement, dutyRead)
	scoped.LegalEntityID = mine
	listing := func(le string) string {
		return `{"legalEntityId":"` + le + `","scopeType":"GROUP","scopeKey":"EU","productId":"` + ids.New() + `","listingType":"DENY"}`
	}

	s := &slice3Store{}
	if rec := serveBody(t, s, http.MethodPost, productListings, listing(other), scoped, nil); rec.Code != http.StatusForbidden || s.listing != nil {
		t.Fatalf("other entity listing: status %d", rec.Code)
	}
	if rec := serveBody(t, s, http.MethodPost, productListings, listing(mine), scoped, nil); rec.Code != http.StatusCreated || s.listing.CreatedBy != "u1" {
		t.Fatalf("own entity listing: status %d", rec.Code)
	}
	if rec := serveBody(t, s, http.MethodGet, productListings+"?legalEntityId="+other, "", scoped, nil); rec.Code != http.StatusForbidden {
		t.Fatalf("list other entity: status %d", rec.Code)
	}
	product := ids.New()
	if rec := serveBody(t, s, http.MethodGet, productListings+"?productId="+product+"&scopeKey=EU", "", scoped, nil); rec.Code != http.StatusOK {
		t.Fatalf("list: status %d", rec.Code)
	}
	if s.listingFilter.LegalEntityID != mine || s.listingFilter.ProductID != product || s.listingFilter.ScopeKey != "EU" || s.listingFilter.Limit != defaultPageSize {
		t.Fatalf("filter %+v", s.listingFilter)
	}
	if rec := serveBody(t, s, http.MethodGet, productListings+"?productId=FG-1", "", scoped, nil); rec.Code != http.StatusBadRequest {
		t.Fatalf("bad product filter: status %d", rec.Code)
	}

	agreement := `{"legalEntityId":"` + other + `","customerId":"` + ids.New() + `","productId":"` + ids.New() + `","unit":"EA","unitPrice":"1","currency":"EUR","commitmentQty":"10"}`
	if rec := serveBody(t, s, http.MethodPost, agreements, agreement, scoped, map[string]string{idempotencyKey: "a1"}); rec.Code != http.StatusForbidden || s.agreement != nil {
		t.Fatalf("other entity agreement: status %d", rec.Code)
	}
	if rec := serveBody(t, s, http.MethodGet, agreements+"?status=OPEN", "", scoped, nil); rec.Code != http.StatusBadRequest {
		t.Fatalf("bad status filter: status %d", rec.Code)
	}
	if rec := serveBody(t, s, http.MethodGet, agreements+"?status=ACTIVE", "", scoped, nil); rec.Code != http.StatusOK || s.agreements.LegalEntityID != mine || s.agreements.Status != "ACTIVE" {
		t.Fatalf("list agreements: status %d filter %+v", rec.Code, s.agreements)
	}
}

func TestDeleteListing(t *testing.T) {
	s := &slice3Store{}
	id := ids.New()
	if rec := serveBody(t, s, http.MethodDelete, productListings+"/"+id, "", principal(dutyListing), nil); rec.Code != http.StatusNoContent || s.deleted != id {
		t.Fatalf("status %d deleted %q", rec.Code, s.deleted)
	}
	if rec := serveBody(t, s, http.MethodDelete, productListings+"/not-a-uuid", "", principal(dutyListing), nil); rec.Code != http.StatusNotFound {
		t.Fatalf("bad id: status %d", rec.Code)
	}
	if rec := serveBody(t, s, http.MethodDelete, productListings+"/"+id, "", principal(dutyRead), nil); rec.Code != http.StatusForbidden {
		t.Fatalf("without sales.listing.maintain: status %d", rec.Code)
	}
}

type lifecycleStore struct {
	fakeStore
	revision store.AgreementRevision
	version  int
	reason   string
	err      error
}

func (s *lifecycleStore) ReviseAgreement(_ context.Context, id string, version int, in store.AgreementRevision, _ string, _ time.Time) (store.Agreement, error) {
	s.revision, s.version = in, version
	return store.Agreement{ID: id, Version: version + 1}, s.err
}

func (s *lifecycleStore) CloseAgreement(_ context.Context, id string, version int, reason, _ string, _ time.Time) (store.Agreement, error) {
	s.reason, s.version = reason, version
	return store.Agreement{ID: id, Version: version + 1, Status: "CLOSED"}, s.err
}

func (s *lifecycleStore) ListAgreementVersions(_ context.Context, _ string) ([]store.AgreementVersion, error) {
	return []store.AgreementVersion{{Version: 3, Kind: "REVISED"}}, nil
}

// TestReviseAgreement covers PATCH /agreements/{id}: If-Match is required, at
// least one field, decimals and dates are validated, the request reaches the
// store and the response ETag is the new version.
func TestReviseAgreement(t *testing.T) {
	path := agreements + "/" + ids.New()
	p := principal(dutyAgreement)
	s := &lifecycleStore{}
	if rec := serveBody(t, s, http.MethodPatch, path, `{"commitmentQty":"1500"}`, p, nil); rec.Code != http.StatusPreconditionRequired {
		t.Fatalf("no If-Match: %d", rec.Code)
	}
	for _, body := range []string{`{}`, `{"commitmentQty":"0"}`, `{"unitPrice":"1.00001"}`, `{"validTo":"soon"}`, `{"cumTolerance":"-1"}`} {
		if rec := serveBody(t, s, http.MethodPatch, path, body, p, map[string]string{"If-Match": `"2"`}); rec.Code != http.StatusBadRequest {
			t.Fatalf("%s: status %d", body, rec.Code)
		}
	}
	rec := serveBody(t, s, http.MethodPatch, path, `{"commitmentQty":"1500","unitPrice":"15","validTo":"","reason":"deal"}`, p, map[string]string{"If-Match": `"2"`})
	if rec.Code != http.StatusOK || rec.Header().Get("ETag") != `"3"` || s.version != 2 {
		t.Fatalf("status %d etag %s body %s", rec.Code, rec.Header().Get("ETag"), rec.Body.String())
	}
	r := s.revision
	if r.Commitment == nil || !r.Commitment.Equal(decimal.NewFromInt(1500)) || r.UnitPrice == nil || r.ValidTo == nil || *r.ValidTo != "" || r.ValidFrom != nil || r.Reason != "deal" {
		t.Fatalf("store got %+v", r)
	}
	if rec := serveBody(t, s, http.MethodPatch, path, `{"commitmentQty":"1500"}`, principal(dutyRead), map[string]string{"If-Match": `"2"`}); rec.Code != http.StatusForbidden {
		t.Fatalf("read duty must not revise: %d", rec.Code)
	}
	s.err = httpx.NewProblem(http.StatusUnprocessableEntity, store.CodeCommitmentBelow, "below")
	if rec := serveBody(t, s, http.MethodPatch, path, `{"commitmentQty":"1"}`, p, map[string]string{"If-Match": `"2"`}); rec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("store refusal: %d", rec.Code)
	}
}

// TestCloseAgreement covers :close (If-Match, reason required, duty) and the
// version history read.
func TestCloseAgreement(t *testing.T) {
	id := ids.New()
	path := agreements + "/" + id + ":close"
	p := principal(dutyAgreement)
	s := &lifecycleStore{}
	if rec := serveBody(t, s, http.MethodPost, path, `{"reason":"x"}`, p, nil); rec.Code != http.StatusPreconditionRequired {
		t.Fatalf("no If-Match: %d", rec.Code)
	}
	if rec := serveBody(t, s, http.MethodPost, path, `{"reason":""}`, p, map[string]string{"If-Match": `"4"`}); rec.Code != http.StatusBadRequest {
		t.Fatalf("empty reason: %d", rec.Code)
	}
	rec := serveBody(t, s, http.MethodPost, path, `{"reason":"customer left"}`, p, map[string]string{"If-Match": `"4"`})
	if rec.Code != http.StatusOK || rec.Header().Get("ETag") != `"5"` || s.reason != "customer left" || s.version != 4 {
		t.Fatalf("status %d etag %s", rec.Code, rec.Header().Get("ETag"))
	}
	if rec := serveBody(t, s, http.MethodPost, path, `{"reason":"x"}`, principal(dutyRead), map[string]string{"If-Match": `"4"`}); rec.Code != http.StatusForbidden {
		t.Fatalf("read duty must not close: %d", rec.Code)
	}
	rec = serveBody(t, s, http.MethodGet, agreements+"/"+id+"/versions", "", principal(dutyRead), nil)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"REVISED"`) {
		t.Fatalf("versions: %d %s", rec.Code, rec.Body.String())
	}
}
