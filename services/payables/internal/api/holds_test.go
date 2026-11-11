package api_test

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/platform/dbx"
	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/platform/tenant"
	"github.com/BenjaminSRussell/passage/platform/testkit"
	"github.com/BenjaminSRussell/passage/services/payables/internal/api"
	"github.com/BenjaminSRussell/passage/services/payables/internal/domain"
	"github.com/BenjaminSRussell/passage/services/payables/internal/store"
	"github.com/BenjaminSRussell/passage/services/payables/migrations"
)

var (
	holder  = caller{duties: "payment-hold.manage", user: "hana", entity: entityA}
	blocker = caller{duties: "vendor-block.manage", user: "bo", entity: entityA}
)

func TestHoldRoutes_PlaceAndReleaseReturnTheItem(t *testing.T) {
	s := newFakeStore()
	s.items = []store.OpenItem{openItem(itemOne, vendorOne, "2026-02-01", "USD", "100.00")}
	r, _ := proposalRouter(s, nil)
	for path, want := range map[string]string{
		"/v1/payables/open-items/" + itemOne + ":hold":    itemOne + ":true:",
		"/v1/payables/open-items/" + itemOne + ":release": itemOne + ":false:",
	} {
		rec := holder.send(r, http.MethodPost, path, "")
		if body := decode(t, rec); rec.Code != http.StatusOK || body["id"] != itemOne {
			t.Fatalf("%s: %d %s", path, rec.Code, rec.Body)
		}
		if got := s.holdCalls[len(s.holdCalls)-1]; got != want {
			t.Fatalf("%s stored %s, want %s", path, got, want)
		}
	}
}

func TestHoldRoutes_Failures(t *testing.T) {
	s := newFakeStore()
	r, _ := proposalRouter(s, nil)
	hold := "/v1/payables/open-items/" + itemOne + ":hold"
	wantProblem(t, caller{duties: dutyRead, user: "hana", entity: entityA}.send(r, http.MethodPost, hold, ""), http.StatusForbidden, "AUTH-0003")
	wantProblem(t, caller{duties: "payment-hold.manage", entity: entityA}.send(r, http.MethodPost, hold, ""), http.StatusForbidden, "AUTH-0003")
	wantProblem(t, holder.send(r, http.MethodPost, "/v1/payables/open-items/nope:hold", ""), http.StatusBadRequest, "COM-0001")
	s.holdErr = store.ErrNotFound
	wantProblem(t, holder.send(r, http.MethodPost, hold, ""), http.StatusNotFound, "COM-0404")
	s.holdErr = errors.New("db down")
	wantProblem(t, holder.send(r, http.MethodPost, hold, ""), http.StatusInternalServerError, "COM-0500")
	if len(s.holdCalls) != 2 {
		t.Fatalf("calls = %v, want only the two authorised, valid requests", s.holdCalls)
	}
}

func TestVendorBlockRoutes(t *testing.T) {
	s := newFakeStore()
	r, _ := proposalRouter(s, nil)
	block, unblock := "/v1/payables/vendors/"+vendorOne+":block", "/v1/payables/vendors/"+vendorOne+":unblock"

	rec := blocker.send(r, http.MethodPost, block, `{"reason":"  sanctions review  "}`)
	if body := decode(t, rec); rec.Code != http.StatusOK || body["blocked"] != true || body["reason"] != "sanctions review" {
		t.Fatalf("block: %d %s", rec.Code, rec.Body)
	}
	rec = blocker.send(r, http.MethodPost, unblock, "")
	if body := decode(t, rec); rec.Code != http.StatusOK || body["blocked"] != false {
		t.Fatalf("unblock: %d %s", rec.Code, rec.Body)
	}
	if got := strings.Join(s.blockCalls, "|"); got != "block:"+entityA+":"+vendorOne+":sanctions review|unblock:"+entityA+":"+vendorOne {
		t.Fatalf("store calls = %s", got)
	}

	for _, body := range []string{`{}`, `{"reason":"   "}`, `{"reason":"` + strings.Repeat("x", 501) + `"}`} {
		wantProblem(t, blocker.send(r, http.MethodPost, block, body), http.StatusBadRequest, "COM-0001")
	}
	wantProblem(t, holder.send(r, http.MethodPost, block, `{"reason":"x"}`), http.StatusForbidden, "AUTH-0003")
	wantProblem(t, blocker.send(r, http.MethodPost, "/v1/payables/vendors/nope:block", `{"reason":"x"}`), http.StatusBadRequest, "COM-0001")
	wantProblem(t, caller{duties: "vendor-block.manage", entity: entityA}.send(r, http.MethodPost, block, `{"reason":"x"}`), http.StatusForbidden, "AUTH-0003")

	s.blockErr = store.ErrNotFound
	wantProblem(t, blocker.send(r, http.MethodPost, unblock, ""), http.StatusNotFound, "COM-0404")
	s.blockErr = errors.New("db down")
	wantProblem(t, blocker.send(r, http.MethodPost, block, `{"reason":"x"}`), http.StatusInternalServerError, "COM-0500")
}

func TestVendorBlockList_ScopedFilteredAndReadableByBlockersAndOpenItemReaders(t *testing.T) {
	s := newFakeStore()
	s.blocks = []store.VendorBlock{
		{VendorID: vendorOne, LegalEntityID: entityA, Reason: "sanctions review"},
		{VendorID: vendorTwo, LegalEntityID: entityA, Reason: "bank details query"},
		{VendorID: vendorOne, LegalEntityID: entityB, Reason: "other entity"},
	}
	r, _ := proposalRouter(s, nil)
	list := "/v1/payables/vendor-blocks"
	for _, c := range []caller{blocker, {duties: dutyRead, user: "rita", entity: entityA}} {
		rec := c.send(r, http.MethodGet, list, "")
		if items, _ := decode(t, rec)["items"].([]any); rec.Code != http.StatusOK || len(items) != 2 {
			t.Fatalf("%s list: %d %s", c.duties, rec.Code, rec.Body)
		}
	}
	rec := blocker.send(r, http.MethodGet, list+"?vendor="+vendorTwo, "")
	items, _ := decode(t, rec)["items"].([]any)
	if rec.Code != http.StatusOK || len(items) != 1 || items[0].(map[string]any)["reason"] != "bank details query" {
		t.Fatalf("one vendor: %d %s", rec.Code, rec.Body)
	}
	wantProblem(t, holder.send(r, http.MethodGet, list, ""), http.StatusForbidden, "AUTH-0003")
	wantProblem(t, blocker.send(r, http.MethodGet, list+"?vendor=nope", ""), http.StatusBadRequest, "COM-0001")
	wantProblem(t, blocker.send(r, http.MethodGet, list+"?legal_entity="+entityB, ""), http.StatusForbidden, "AUTH-0003")
	s.blockErr = errors.New("db down")
	wantProblem(t, blocker.send(r, http.MethodGet, list, ""), http.StatusInternalServerError, "COM-0500")
}

func TestListProposals_FiltersPagesAndValidates(t *testing.T) {
	s := newFakeStore()
	s.summaries = []store.ProposalSummary{
		{ID: proposalID, LegalEntityID: entityA, RunDate: "2026-02-03", RunID: "r3", Status: domain.ProposalDraft},
		{ID: "00000000-0000-0000-0000-0000000000d2", LegalEntityID: entityA, RunDate: "2026-02-02", RunID: "r2", Status: domain.ProposalDraft},
		{ID: "00000000-0000-0000-0000-0000000000d3", LegalEntityID: entityA, RunDate: "2026-02-01", RunID: "r1", Status: domain.ProposalDraft},
	}
	r, _ := proposalRouter(s, nil)
	viewer := caller{duties: dutyView, user: "vi", entity: entityA}

	rec := viewer.send(r, http.MethodGet, "/v1/payables/payment-proposals?status=DRAFT&run_from=2026-02-01&run_to=2026-03-01&limit=2", "")
	body := decode(t, rec)
	items, _ := body["items"].([]any)
	next, _ := body["nextCursor"].(string)
	f := s.propFilter
	if rec.Code != http.StatusOK || len(items) != 2 || next == "" || s.propLimit != 3 || f.LegalEntityID != entityA || f.Status != "DRAFT" || f.RunFrom != "2026-02-01" || f.RunTo != "2026-03-01" {
		t.Fatalf("page: %d filter=%+v limit=%d %s", rec.Code, f, s.propLimit, rec.Body)
	}

	rec = viewer.send(r, http.MethodGet, "/v1/payables/payment-proposals?cursor="+next, "")
	if rec.Code != http.StatusOK || s.propFilter.AfterRunDate != "2026-02-02" || s.propFilter.AfterID != "00000000-0000-0000-0000-0000000000d2" {
		t.Fatalf("cursor: %d after=%s/%s %s", rec.Code, s.propFilter.AfterRunDate, s.propFilter.AfterID, rec.Body)
	}

	for _, query := range []string{"status=PAID", "run_from=soon", "run_from=2026-03-01&run_to=2026-02-01", "limit=0", "limit=201", "cursor=zzzz"} {
		wantProblem(t, viewer.send(r, http.MethodGet, "/v1/payables/payment-proposals?"+query, ""), http.StatusBadRequest, "COM-0001")
	}
	wantProblem(t, caller{duties: dutyRead, entity: entityA}.send(r, http.MethodGet, "/v1/payables/payment-proposals", ""), http.StatusForbidden, "AUTH-0003")
	wantProblem(t, viewer.send(r, http.MethodGet, "/v1/payables/payment-proposals?legal_entity="+entityB, ""), http.StatusForbidden, "AUTH-0003")
	s.listPropErr = errors.New("db down")
	wantProblem(t, viewer.send(r, http.MethodGet, "/v1/payables/payment-proposals", ""), http.StatusInternalServerError, "COM-0500")
}

func TestHoldAndBlockRoutes_RealStoreExcludeItemsFromProposals(t *testing.T) {
	admin, app := testkit.DBWithApp(t)
	if err := dbx.Migrate(context.Background(), admin, migrations.FS); err != nil {
		t.Fatal(err)
	}
	pg := store.New(app)
	seedCtx := tenant.With(context.Background(), tenant.Principal{TenantID: testkit.TenantID(1), LegalEntityID: entityA, UserID: "seeder", Duties: map[string]struct{}{}})
	var items []store.OpenItem
	for i, vendor := range []string{vendorOne, vendorTwo} {
		o, _, err := pg.CreateFromInvoice(seedCtx, store.OpenItem{LegalEntityID: entityA, VendorID: vendor, SourceInvoiceID: fmt.Sprintf("00000000-0000-0000-0000-0000000000f%d", i+1),
			InvoiceNumber: "INV", InvoiceDate: "2025-12-01", DueDate: "2026-01-05", Currency: "USD", Amount: decimal.RequireFromString("100.00")})
		if err != nil {
			t.Fatal(err)
		}
		items = append(items, o)
	}
	router := httpx.NewRouter()
	api.New(app, fakeValidator{}, fixedNow).WithProposals(pg, nil).Register(router)
	all := caller{duties: "payment-hold.manage,vendor-block.manage,payment-proposal.write,payment-proposal.read,open-item.read", user: "hana", entity: entityA}
	propose := func(runID string) *httptest.ResponseRecorder {
		return all.send(router, http.MethodPost, "/v1/payables/payment-proposals", strings.Replace(proposalBody, "run-1", runID, 1))
	}

	if rec := all.send(router, http.MethodPost, "/v1/payables/open-items/"+items[0].ID+":hold", ""); rec.Code != http.StatusOK || decode(t, rec)["paymentHold"] != true {
		t.Fatalf("hold: %d %s", rec.Code, rec.Body)
	}
	if rec := all.send(router, http.MethodPost, "/v1/payables/vendors/"+vendorTwo+":block", `{"reason":"fraud"}`); rec.Code != http.StatusOK {
		t.Fatalf("block: %d %s", rec.Code, rec.Body)
	}
	wantProblem(t, propose("run-1"), http.StatusUnprocessableEntity, "AP-PRP-005")
	blocks := decode(t, all.send(router, http.MethodGet, "/v1/payables/vendor-blocks", ""))["items"].([]any)
	if len(blocks) != 1 {
		t.Fatalf("blocks = %v", blocks)
	}
	if b := blocks[0].(map[string]any); b["vendorId"] != vendorTwo || b["legalEntityId"] != entityA || b["reason"] != "fraud" || b["blockedBy"] != "hana" || b["blockedAt"] == nil {
		t.Fatalf("block = %v", b)
	}
	if other := decode(t, all.send(router, http.MethodGet, "/v1/payables/vendor-blocks?vendor="+vendorOne, ""))["items"].([]any); len(other) != 0 {
		t.Fatalf("vendor one is not blocked: %v", other)
	}

	if rec := all.send(router, http.MethodPost, "/v1/payables/open-items/"+items[0].ID+":release", ""); rec.Code != http.StatusOK || decode(t, rec)["paymentHold"] != false {
		t.Fatalf("release: %d %s", rec.Code, rec.Body)
	}
	if rec := all.send(router, http.MethodPost, "/v1/payables/vendors/"+vendorTwo+":unblock", ""); rec.Code != http.StatusOK {
		t.Fatalf("unblock: %d %s", rec.Code, rec.Body)
	}
	if left := decode(t, all.send(router, http.MethodGet, "/v1/payables/vendor-blocks", ""))["items"].([]any); len(left) != 0 {
		t.Fatalf("blocks after unblock = %v", left)
	}
	created := propose("run-2")
	if created.Code != http.StatusCreated || !strings.Contains(created.Body.String(), items[0].ID) || !strings.Contains(created.Body.String(), items[1].ID) {
		t.Fatalf("proposal after release: %d %s", created.Code, created.Body)
	}

	list := all.send(router, http.MethodGet, "/v1/payables/payment-proposals?status=DRAFT&run_from=2026-01-10", "")
	if body := decode(t, list); list.Code != http.StatusOK || len(body["items"].([]any)) != 1 || body["items"].([]any)[0].(map[string]any)["totalValued"] != "200" {
		t.Fatalf("list: %d %s", list.Code, list.Body)
	}
	outsider := caller{duties: "payment-hold.manage", user: "mallory", entity: entityB}
	wantProblem(t, outsider.send(router, http.MethodPost, "/v1/payables/open-items/"+items[0].ID+":hold", ""), http.StatusNotFound, "COM-0404")
	wantProblem(t, all.send(router, http.MethodPost, "/v1/payables/vendors/"+vendorTwo+":unblock", ""), http.StatusNotFound, "COM-0404")
}

func TestVendorBlockRoutes_AreScopedToTheCallersLegalEntity(t *testing.T) {
	s := newFakeStore()
	r, _ := proposalRouter(s, nil)
	block, unblock := "/v1/payables/vendors/"+vendorOne+":block", "/v1/payables/vendors/"+vendorOne+":unblock"
	wantProblem(t, blocker.send(r, http.MethodPost, block, `{"legalEntityId":"`+entityB+`","reason":"x"}`), http.StatusForbidden, "AUTH-0003")
	wantProblem(t, blocker.send(r, http.MethodPost, unblock, `{"legalEntityId":"`+entityB+`"}`), http.StatusForbidden, "AUTH-0003")
	wantProblem(t, caller{duties: "vendor-block.manage", user: "bo", noScope: true}.send(r, http.MethodPost, block, `{"reason":"x"}`), http.StatusBadRequest, "COM-0001")
	if len(s.blockCalls) != 0 {
		t.Fatalf("store reached for a forbidden entity: %v", s.blockCalls)
	}
	if rec := blocker.send(r, http.MethodPost, block, `{"legalEntityId":"`+entityA+`","reason":"x"}`); rec.Code != http.StatusOK || decode(t, rec)["legalEntityId"] != entityA {
		t.Fatalf("block in own entity: %d %s", rec.Code, rec.Body)
	}
}

func TestHoldRoutes_ReasonConflictAndNoop(t *testing.T) {
	s := newFakeStore()
	s.items = []store.OpenItem{openItem(itemOne, vendorOne, "2026-02-01", "USD", "100.00")}
	r, a := proposalRouter(s, nil)
	hold := "/v1/payables/open-items/" + itemOne + ":hold"
	if rec := holder.send(r, http.MethodPost, hold, `{"reason":" bank query "}`); rec.Code != http.StatusOK || s.holdCalls[0] != itemOne+":true:bank query" {
		t.Fatalf("hold with reason: %d %v", rec.Code, s.holdCalls)
	}
	wantProblem(t, holder.send(r, http.MethodPost, hold, `{"reason":"`+strings.Repeat("x", 501)+`"}`), http.StatusBadRequest, "COM-0001")
	s.holdErr = store.ErrItemClosed
	wantProblem(t, holder.send(r, http.MethodPost, hold, ""), http.StatusConflict, "AP-OPN-001")

	s.holdErr, s.holdNoop = nil, true
	holder.send(r, http.MethodPost, hold, "")
	rec := httptest.NewRecorder()
	a.Metrics().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/metrics", nil))
	if !strings.Contains(rec.Body.String(), `payables_payment_holds_total{outcome="held"} 1`) {
		t.Fatalf("a no-op hold must not be counted: %s", rec.Body)
	}
}
