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
		"/v1/payables/open-items/" + itemOne + ":hold":    itemOne + ":true",
		"/v1/payables/open-items/" + itemOne + ":release": itemOne + ":false",
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
	if got := strings.Join(s.blockCalls, "|"); got != "block:"+vendorOne+":sanctions review|unblock:"+vendorOne {
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

	if rec := all.send(router, http.MethodPost, "/v1/payables/open-items/"+items[0].ID+":release", ""); rec.Code != http.StatusOK || decode(t, rec)["paymentHold"] != false {
		t.Fatalf("release: %d %s", rec.Code, rec.Body)
	}
	if rec := all.send(router, http.MethodPost, "/v1/payables/vendors/"+vendorTwo+":unblock", ""); rec.Code != http.StatusOK {
		t.Fatalf("unblock: %d %s", rec.Code, rec.Body)
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
