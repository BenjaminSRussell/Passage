package api_test

import (
	"context"
	"net/http"
	"testing"

	"github.com/go-chi/chi/v5"

	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/platform/ledgerclient"
	"github.com/BenjaminSRussell/passage/services/inventory/internal/api"
	"github.com/BenjaminSRussell/passage/services/inventory/internal/store"
)

const glEntity = "00000000-0000-0000-0000-0000000000e1" // testkit.Ctx's legal entity

type glBooksStub struct {
	post     store.GLPostRequest
	requeued string
}

func (g *glBooksStub) PostToGL(_ context.Context, _ ledgerclient.Ledger, in store.GLPostRequest) (store.GLPostResult, error) {
	g.post = in
	return store.GLPostResult{LedgerID: in.LedgerID, Booked: []store.GLBooked{{JournalID: ids.New(), EntryID: ids.New(), Lines: 2}}, Failed: []store.GLFailed{}}, nil
}

func (g *glBooksStub) RequeueParkedGL(_ context.Context, legalEntityID string) (int, error) {
	g.requeued = legalEntityID
	return 3, nil
}

func TestGLPostingRoutes(t *testing.T) {
	duty := []string{"inventory.gl.post"}
	ledgerID := ids.New()
	if rec := do(t, newServer(t, &fakeStore{}), http.MethodPost, "/v1/inventory/gl-postings", `{"legalEntityId":"`+glEntity+`","ledgerId":"`+ledgerID+`"}`, duty, nil); rec.Code != http.StatusServiceUnavailable || decodeProblem(t, rec).Code != api.CodeGLUnavailable {
		t.Fatalf("unconfigured ledger: %d %s", rec.Code, rec.Body)
	}

	books := &glBooksStub{}
	r := chi.NewRouter()
	api.New(&fakeStore{}, store.PostParams{}).WithGL(books, ledgerclient.NewFake()).Register(r)
	if rec := do(t, r, http.MethodPost, "/v1/inventory/gl-postings", `{"legalEntityId":"`+glEntity+`","ledgerId":"`+ledgerID+`"}`, nil, nil); rec.Code != http.StatusForbidden {
		t.Fatalf("without the duty: %d", rec.Code)
	}
	if rec := do(t, r, http.MethodPost, "/v1/inventory/gl-postings", `{"legalEntityId":"`+glEntity+`","ledgerId":"x"}`, duty, nil); rec.Code != http.StatusBadRequest || decodeProblem(t, rec).Errors["ledgerId"] == "" {
		t.Fatalf("bad ledger id: %d %s", rec.Code, rec.Body)
	}
	if rec := do(t, r, http.MethodPost, "/v1/inventory/gl-postings", `{"legalEntityId":"`+ids.New()+`","ledgerId":"`+ledgerID+`"}`, duty, nil); rec.Code != http.StatusForbidden {
		t.Fatalf("legal entity outside the scope: %d %s", rec.Code, rec.Body)
	}
	if rec := do(t, r, http.MethodPost, "/v1/inventory/gl-postings", `{"legalEntityId":"`+glEntity+`","ledgerId":"`+ledgerID+`"}`, duty, nil); rec.Code != http.StatusOK ||
		books.post.LegalEntityID != glEntity || books.post.LedgerID != ledgerID {
		t.Fatalf("sweep: %d %s %+v", rec.Code, rec.Body, books.post)
	}
	if rec := do(t, r, http.MethodPost, "/v1/inventory/gl-postings:requeue", `{"legalEntityId":"`+glEntity+`"}`, duty, nil); rec.Code != http.StatusOK ||
		books.requeued != glEntity || rec.Body.String() != "{\"requeued\":3}\n" {
		t.Fatalf("requeue: %d %q", rec.Code, rec.Body)
	}
}
