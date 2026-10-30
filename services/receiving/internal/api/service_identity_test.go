package api_test

import (
	"crypto/ed25519"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/BenjaminSRussell/passage/platform/authz"
	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/platform/tenant"
	"github.com/BenjaminSRussell/passage/services/receiving/internal/api"
	"github.com/BenjaminSRussell/passage/services/receiving/internal/domain"
	"github.com/BenjaminSRussell/passage/services/receiving/internal/ledger"
	"github.com/BenjaminSRussell/passage/services/receiving/internal/periods"
	"github.com/BenjaminSRussell/passage/services/receiving/internal/store/memstore"
)

type seenCall struct{ user, service, legalEntity string }

type recorder struct {
	mu    sync.Mutex
	calls []seenCall
}

func (r *recorder) record(req *http.Request) {
	p, _ := tenant.FromContext(req.Context())
	r.mu.Lock()
	defer r.mu.Unlock()
	r.calls = append(r.calls, seenCall{user: p.UserID, service: p.Service, legalEntity: p.LegalEntityID})
}

func (r *recorder) snapshot() []seenCall {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]seenCall(nil), r.calls...)
}

func downstream(t *testing.T, self string, keys map[string]ed25519.PublicKey, duty, path, reply string, status int, rec *recorder) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()
	mux.Handle("POST "+path, authz.RequireDuty(duty)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		rec.record(r)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_, _ = w.Write([]byte(reply))
	})))
	srv := httptest.NewServer(tenant.MiddlewareWith(tenant.NewServiceVerifier(self, keys))(mux))
	t.Cleanup(srv.Close)
	return srv
}

func signedRig(t *testing.T, signer *tenant.ServiceSigner, pub ed25519.PublicKey) (http.Handler, *recorder, *recorder) {
	t.Helper()
	keys := map[string]ed25519.PublicKey{"receiving": pub}
	ledgerSeen, closeSeen := &recorder{}, &recorder{}
	ledgerSrv := downstream(t, "ledger", keys, "ledger.journal.post", "/v1/ledger/journal-entries", `{"journalEntryId":"je-1"}`, http.StatusCreated, ledgerSeen)
	closeSrv := downstream(t, "close", keys, "period.check", "/v1/close/period-statuses:check", `{"allowed":true,"status":"open"}`, http.StatusOK, closeSeen)
	a := api.New(memstore.New(), api.Ports{
		Periods:       periods.New(closeSrv.URL, nil).WithSigner(signer),
		Ledger:        ledger.New(ledgerSrv.URL, nil).WithSigner(signer),
		Profile:       domain.PostingProfile{LedgerID: ledgerID, Inventory: "1400", GRIR: "2100", PPV: "5200", VendorControl: "2000"},
		ResolveMinAge: time.Nanosecond,
	})
	router := httpx.NewRouter()
	a.Register(router)
	return router, ledgerSeen, closeSeen
}

func TestServiceIdentity_UserWithoutLedgerDutiesPostsReceiptAndInvoice(t *testing.T) {
	pub, priv, _ := ed25519.GenerateKey(nil)
	signer, _ := tenant.NewServiceSigner("receiving", priv)
	h, ledgerSeen, closeSeen := signedRig(t, signer, pub)
	rg := &rig{h: h}
	as := func(hdr headers) headers {
		hdr["X-Passage-User"] = "warehouse-user"
		hdr[entityHeader] = legalEntityID
		return hdr
	}

	receipt := decode[receiptJSON](t, rg.call(http.MethodPost, receiptsPath, directBody("PS-1", 100, 100, ""), as(rg.maintain())), http.StatusCreated)
	posted := decode[receiptJSON](t, rg.call(http.MethodPost, receiptsPath+"/"+receipt.ID+":post", "", as(rg.poster(receipt.Version))), http.StatusOK)
	if posted.LedgerEntryID != "je-1" {
		t.Fatalf("receipt post did not reach the ledger: %+v", posted)
	}
	invoice := decode[invoiceJSON](t, rg.call(http.MethodPost, invoicesPath, invoiceBody("INV-1", receipt.ID, 10, ""), as(duty("VendorInvoicePost", rg.key()))), http.StatusCreated)
	if invoice.LedgerEntryID != "je-1" {
		t.Fatalf("invoice post did not reach the ledger: %+v", invoice)
	}

	ledgerCalls, closeCalls := ledgerSeen.snapshot(), closeSeen.snapshot()
	if len(ledgerCalls) != 2 || len(closeCalls) == 0 {
		t.Fatalf("ledger calls %v, close calls %v", ledgerCalls, closeCalls)
	}
	for _, c := range append(ledgerCalls, closeCalls...) {
		if c.user != "warehouse-user" || c.service != "receiving" || c.legalEntity != legalEntityID {
			t.Fatalf("downstream saw %+v, want end user as actor via the receiving service identity in %s", c, legalEntityID)
		}
	}
}

func TestServiceIdentity_UnconfiguredSignerKeepsForwardingUserDuties(t *testing.T) {
	pub, _, _ := ed25519.GenerateKey(nil)
	h, ledgerSeen, _ := signedRig(t, nil, pub)
	rg := &rig{h: h}
	receipt := decode[receiptJSON](t, rg.call(http.MethodPost, receiptsPath, directBody("PS-1", 10, 4, ""), rg.maintain()), http.StatusCreated)
	rec := rg.call(http.MethodPost, receiptsPath+"/"+receipt.ID+":post", "", rg.poster(receipt.Version))
	if rec.Code == http.StatusOK || len(ledgerSeen.snapshot()) != 0 {
		t.Fatalf("without a signer the user's own duties apply and ReceivingPost lacks ledger.journal.post: %d %s", rec.Code, rec.Body.String())
	}
}

func TestServiceIdentity_ForgedSignerIsRejectedByDownstream(t *testing.T) {
	pub, _, _ := ed25519.GenerateKey(nil)
	_, otherPriv, _ := ed25519.GenerateKey(nil)
	forger, _ := tenant.NewServiceSigner("receiving", otherPriv)
	h, ledgerSeen, _ := signedRig(t, forger, pub)
	rg := &rig{h: h}
	hdr := func(h headers) headers { h["X-Passage-User"] = "u"; h[entityHeader] = legalEntityID; return h }
	receipt := decode[receiptJSON](t, rg.call(http.MethodPost, receiptsPath, directBody("PS-1", 10, 4, ""), hdr(rg.maintain())), http.StatusCreated)
	rec := rg.call(http.MethodPost, receiptsPath+"/"+receipt.ID+":post", "", hdr(rg.poster(receipt.Version)))
	if rec.Code == http.StatusOK || strings.Contains(rec.Body.String(), "je-1") || len(ledgerSeen.snapshot()) != 0 {
		t.Fatalf("a token signed by an unknown key must not post: %d %s", rec.Code, rec.Body.String())
	}
}
