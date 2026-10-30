package api_test

import (
	"context"
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
	"github.com/BenjaminSRussell/passage/services/payables/internal/fx"
	"github.com/BenjaminSRussell/passage/services/payables/internal/store"
	"github.com/BenjaminSRussell/passage/services/payables/migrations"
)

func fxServer(t *testing.T) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		if r.URL.Path != "/v1/fx/rates" || q.Get("type") != "spot" || q.Get("to") != "USD" || q.Get("from") != "EUR" {
			w.WriteHeader(http.StatusUnprocessableEntity)
			return
		}
		_, _ = w.Write([]byte(`{"rate":"1.0825"}`))
	}))
	t.Cleanup(srv.Close)
	return srv
}

func TestProposalFlow_RealStoreAndFXServer(t *testing.T) {
	admin, app := testkit.DBWithApp(t)
	if err := dbx.Migrate(context.Background(), admin, migrations.FS); err != nil {
		t.Fatal(err)
	}
	pg := store.New(app)
	seedCtx := tenant.With(context.Background(), tenant.Principal{TenantID: testkit.TenantID(1), LegalEntityID: entityA, UserID: "seeder", Duties: map[string]struct{}{}})
	seed := func(source, vendor, due, currency, amount string) store.OpenItem {
		o, _, err := pg.CreateFromInvoice(seedCtx, store.OpenItem{LegalEntityID: entityA, VendorID: vendor, SourceInvoiceID: source, InvoiceNumber: "INV-" + source[len(source)-2:],
			InvoiceDate: "2025-12-01", DueDate: due, Currency: currency, Amount: decimal.RequireFromString(amount)})
		if err != nil {
			t.Fatal(err)
		}
		return o
	}
	usd := seed(itemOne, vendorOne, "2026-01-05", "USD", "1000.00")
	seed(itemTwo, vendorOne, "2026-01-08", "EUR", "200.00")
	held := seed(itemThree, vendorTwo, "2026-01-09", "USD", "75.00")
	seed("00000000-0000-0000-0000-0000000000f4", vendorTwo, "2026-02-09", "USD", "75.00")
	if err := pg.SetPaymentHold(seedCtx, held.ID, true); err != nil {
		t.Fatal(err)
	}

	a := api.New(app, fakeValidator{}, fixedNow).WithProposals(pg, fx.New(fxServer(t).URL, "", nil))
	router := httpx.NewRouter()
	a.Register(router)
	all := "open-item.read,payment-proposal.write,payment-proposal.read,payment-proposal.approve"
	alice, bob := caller{duties: all, user: "alice", entity: entityA}, caller{duties: all, user: "bob", entity: entityA}

	list := alice.send(router, http.MethodGet, "/v1/payables/open-items?currency=EUR&due_to=2026-01-31&value_in=USD", "")
	if list.Code != http.StatusOK || !strings.Contains(list.Body.String(), `"amount":"216.5"`) || strings.Contains(list.Body.String(), itemOne) {
		t.Fatalf("list: %d %s", list.Code, list.Body)
	}

	created := alice.send(router, http.MethodPost, "/v1/payables/payment-proposals", proposalBody)
	body := decode(t, created)
	if created.Code != http.StatusCreated || body["status"] != "DRAFT" || body["createdBy"] != "alice" || body["totalValued"] != "1216.5" {
		t.Fatalf("create: %d %s", created.Code, created.Body)
	}
	id := body["id"].(string)
	if n := len(body["groups"].([]any)); n != 2 || strings.Contains(created.Body.String(), held.ID) {
		t.Fatalf("groups = %d, want 2 and the held item excluded: %s", n, created.Body)
	}

	replay := alice.send(router, http.MethodPost, "/v1/payables/payment-proposals", proposalBody)
	if replay.Code != http.StatusOK || replay.Header().Get("Idempotent-Replayed") != "true" || decode(t, replay)["id"] != id {
		t.Fatalf("replay: %d %s", replay.Code, replay.Body)
	}
	other := alice.send(router, http.MethodPost, "/v1/payables/payment-proposals", strings.Replace(proposalBody, `"run-1"`, `"run-2"`, 1))
	wantProblem(t, other, http.StatusUnprocessableEntity, "AP-PRP-005")
	if strings.Contains(other.Body.String(), usd.ID) {
		t.Fatalf("a second run must not re-select reserved items: %d %s", other.Code, other.Body)
	}

	approvePath := "/v1/payables/payment-proposals/" + id + ":approve"
	wantProblem(t, alice.send(router, http.MethodPost, approvePath, ""), http.StatusForbidden, "AP-PRP-003")
	approved := bob.send(router, http.MethodPost, approvePath, "")
	if got := decode(t, approved); approved.Code != http.StatusOK || got["status"] != "APPROVED" || got["approvedBy"] != "bob" {
		t.Fatalf("approve: %d %s", approved.Code, approved.Body)
	}
	wantProblem(t, bob.send(router, http.MethodPost, approvePath, ""), http.StatusConflict, "AP-PRP-002")

	fetched := bob.send(router, http.MethodGet, "/v1/payables/payment-proposals/"+id, "")
	if got := decode(t, fetched); fetched.Code != http.StatusOK || got["status"] != "APPROVED" || got["createdBy"] != "alice" {
		t.Fatalf("get: %d %s", fetched.Code, fetched.Body)
	}

	outsider := caller{duties: all, user: "mallory", entity: entityB}
	wantProblem(t, outsider.send(router, http.MethodGet, "/v1/payables/payment-proposals/"+id, ""), http.StatusNotFound, "COM-0404")
	wantProblem(t, outsider.send(router, http.MethodPost, approvePath, ""), http.StatusNotFound, "COM-0404")
	cancelPath := "/v1/payables/payment-proposals/" + id + ":cancel"
	wantProblem(t, outsider.send(router, http.MethodPost, cancelPath, ""), http.StatusNotFound, "COM-0404")
	wantProblem(t, alice.send(router, http.MethodPost, cancelPath, ""), http.StatusConflict, "AP-PRP-002")
}
