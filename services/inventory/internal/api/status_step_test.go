package api_test

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"
	"time"

	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/platform/issueclaim"
	"github.com/BenjaminSRussell/passage/services/inventory/internal/domain"
	"github.com/BenjaminSRussell/passage/services/inventory/internal/store"
)

// Slice 6 fake-store methods; their fields are declared on fakeStore in api_test.go.

func (f *fakeStore) CreateTransaction(_ context.Context, in store.CreateTransactionInput) (store.StatusChange, bool, error) {
	f.createIn = in
	if f.err != nil {
		return store.StatusChange{}, false, f.err
	}
	return store.StatusChange{ID: ids.New(), TransactionID: ids.New(), Command: domain.CommandCreate, To: in.Status, Qty: in.Qty,
		Date: "2026-09-30"}, !f.stepReplay, nil
}

func (f *fakeStore) ApplyStatusStep(_ context.Context, in store.StatusStepInput) (store.StatusChange, bool, error) {
	f.stepIn = in
	if f.err != nil {
		return store.StatusChange{}, false, f.err
	}
	return store.StatusChange{ID: ids.New(), TransactionID: in.TransactionID, Command: in.Command, From: in.From, To: in.To,
		Qty: decimal.NewFromInt(5), Date: "2026-09-30"}, !f.stepReplay, nil
}

func (f *fakeStore) GetTransaction(_ context.Context, id string) (store.TransactionRow, []store.StatusChange, error) {
	if f.err != nil {
		return store.TransactionRow{}, nil, f.err
	}
	return store.TransactionRow{ID: id, ProductID: ids.New(), Qty: decimal.NewFromInt(5), ReceiptStatus: "ARRIVED", Currency: "USD"}, f.detailHistory, nil
}

func (f *fakeStore) ListStatusChanges(_ context.Context, filter store.StatusChangeFilter) ([]store.StatusChange, error) {
	f.changeFilter = filter
	out := []store.StatusChange{}
	for i := 0; i < f.changeRows; i++ {
		out = append(out, store.StatusChange{ID: ids.New(), TransactionID: ids.New(), Command: domain.CommandUnpick,
			From: domain.StatusPicked, To: domain.StatusReservedPhysical, Qty: decimal.NewFromInt(-1)})
	}
	return out, nil
}

const (
	applyDuty = "inventory.transaction.apply"
	stepDuty  = "inventory.transaction.step"
	applyURL  = "/internal/v1/inventory/transactions:apply"
)

func TestApplyNeedsTheServiceDutyAndAKey(t *testing.T) {
	h := newServer(t, &fakeStore{})
	body := `{"transactionId":"` + ids.New() + `","fromStatus":"ARRIVED","toStatus":"REGISTERED"}`
	if rec := do(t, h, http.MethodPost, applyURL, body, []string{stepDuty}, key("k")); rec.Code != http.StatusForbidden {
		t.Fatalf("user step duty on the internal command: status %d, want 403", rec.Code)
	}
	rec := do(t, h, http.MethodPost, applyURL, body, []string{applyDuty}, nil)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("without key: status %d, want 400", rec.Code)
	}
	if p := decodeProblem(t, rec); p.Errors["Idempotency-Key"] == "" {
		t.Fatalf("missing key error: %v", p.Errors)
	}
}

func TestApplyStepValidatesAndPassesTheMove(t *testing.T) {
	h := newServer(t, &fakeStore{})
	rec := do(t, h, http.MethodPost, applyURL, `{"transactionId":"nope","fromStatus":"LOST","toStatus":"GONE","date":"2026-13-40","costAmount":"-1","qty":"3"}`,
		[]string{applyDuty}, key("k"))
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status %d, want 400", rec.Code)
	}
	p := decodeProblem(t, rec)
	for _, f := range []string{"transactionId", "fromStatus", "toStatus", "date", "costAmount", "qty"} {
		if p.Errors[f] == "" {
			t.Errorf("missing field error for %s: %v", f, p.Errors)
		}
	}

	fs := &fakeStore{}
	h = newServer(t, fs)
	id := ids.New()
	body := `{"transactionId":"` + id + `","fromStatus":"RECEIVED","toStatus":"PURCHASED","date":"2026-09-30","costAmount":"12.50"}`
	rec = do(t, h, http.MethodPost, applyURL, body, []string{applyDuty}, key("k-1"))
	if rec.Code != http.StatusCreated {
		t.Fatalf("status %d body %s", rec.Code, rec.Body.String())
	}
	in := fs.stepIn
	if in.TransactionID != id || in.Command != domain.CommandStep || in.From != domain.StatusReceived || in.To != domain.StatusPurchased ||
		in.Date.Format("2006-01-02") != "2026-09-30" || in.CostAmount == nil || !in.CostAmount.Equal(decimal.RequireFromString("12.5")) || in.IdempotencyKey != "k-1" {
		t.Fatalf("store input %+v", in)
	}
	var out struct {
		ID, Command, FromStatus, ToStatus string
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	if out.ID == "" || out.Command != "STEP" || out.FromStatus != "RECEIVED" || out.ToStatus != "PURCHASED" {
		t.Fatalf("response %+v", out)
	}

	fs.stepReplay = true
	rec = do(t, h, http.MethodPost, applyURL, body, []string{applyDuty}, key("k-1"))
	if rec.Code != http.StatusOK || rec.Header().Get("Idempotent-Replayed") != "true" {
		t.Fatalf("replay: status %d headers %v", rec.Code, rec.Header())
	}
}

func TestApplyRegistersAnExternalOrigin(t *testing.T) {
	fs := &fakeStore{}
	h := newServer(t, fs)
	le, product, source := ids.New(), ids.New(), ids.New()
	body := `{"origin":{"sourceType":"PURCH_LINE","sourceId":"` + source + `","reference":" PO-0042 "},"legalEntityId":"` + le + `",
		"productId":"` + product + `","dimensions":{"site":"S1","warehouse":"W1"},"qty":"5","enteredUnit":"EA","currency":"USD",
		"costPrice":"2.5","toStatus":"ORDERED","date":"2026-10-01"}`
	rec := do(t, h, http.MethodPost, applyURL, body, []string{applyDuty}, key("reg-1"))
	if rec.Code != http.StatusCreated {
		t.Fatalf("status %d body %s", rec.Code, rec.Body.String())
	}
	in := fs.createIn
	if in.SourceType != "PURCH_LINE" || in.SourceID != source || in.Reference != "PO-0042" || in.LegalEntityID != le || in.ProductID != product ||
		in.Dims["site"] != "S1" || !in.Qty.Equal(decimal.NewFromInt(5)) || !in.EnteredQty.Equal(in.Qty) || in.Status != domain.StatusOrdered ||
		!in.CostPrice.Equal(decimal.RequireFromString("2.5")) || in.Date.Format("2006-01-02") != "2026-10-01" || in.IdempotencyKey != "reg-1" {
		t.Fatalf("store input %+v", in)
	}
}

func TestApplyRegistrationRefusesAnIssueRef(t *testing.T) {
	fs := &fakeStore{}
	h := newServer(t, fs)
	line := ids.New()
	body := `{"origin":{"sourceType":"SALES_LINE","sourceId":"` + line + `"},"legalEntityId":"` + ids.New() + `","productId":"` + ids.New() + `",
		"dimensions":{"site":"S1","warehouse":"W1"},"qty":"-2","enteredUnit":"EA","currency":"USD","toStatus":"SOLD","date":"2026-10-01",
		"issueRef":"` + issueclaim.SalesLineRef(line) + `"}`
	rec := do(t, h, http.MethodPost, applyURL, body, []string{applyDuty}, key("ref"))
	if rec.Code != http.StatusBadRequest || fs.createIn.SourceID != "" {
		t.Fatalf("status %d body %s input %+v, want 400 and no store call", rec.Code, rec.Body.String(), fs.createIn)
	}
}

func TestApplyRegistrationRejectsBadInput(t *testing.T) {
	h := newServer(t, &fakeStore{})
	body := `{"origin":{"sourceType":"INVENT_JOURNAL_LINE","sourceId":"x"},"legalEntityId":"x","productId":"x","qty":"0.0000001",
		"enteredUnit":"each unit","currency":"usd","costPrice":"-2","fromStatus":"ORDERED","toStatus":"ORDERED"}`
	rec := do(t, h, http.MethodPost, applyURL, body, []string{applyDuty}, key("k"))
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status %d, want 400", rec.Code)
	}
	p := decodeProblem(t, rec)
	for _, f := range []string{"origin.sourceType", "origin.sourceId", "legalEntityId", "productId", "qty", "enteredUnit", "currency", "costPrice", "fromStatus"} {
		if p.Errors[f] == "" {
			t.Errorf("missing field error for %s: %v", f, p.Errors)
		}
	}
	both := `{"origin":{"sourceType":"PURCH_LINE","sourceId":"` + ids.New() + `"},"transactionId":"` + ids.New() + `","toStatus":"ORDERED"}`
	if p := decodeProblem(t, do(t, h, http.MethodPost, applyURL, both, []string{applyDuty}, key("k"))); p.Errors["origin"] == "" {
		t.Fatalf("origin and transactionId together must be refused: %v", p.Errors)
	}
	neither := `{"toStatus":"ORDERED"}`
	if p := decodeProblem(t, do(t, h, http.MethodPost, applyURL, neither, []string{applyDuty}, key("k"))); p.Errors["transactionId"] == "" {
		t.Fatalf("a request naming no transaction must be refused: %v", p.Errors)
	}
}

func TestNamedCommandsCarryTheirCommand(t *testing.T) {
	cases := map[string]domain.StepCommand{
		":unpick":             domain.CommandUnpick,
		":unregister":         domain.CommandUnregister,
		":cancel-reservation": domain.CommandCancelReservation,
	}
	for verb, cmd := range cases {
		t.Run(verb, func(t *testing.T) {
			fs := &fakeStore{}
			h := newServer(t, fs)
			id := ids.New()
			path := "/v1/inventory/transactions/" + id + verb
			if rec := do(t, h, http.MethodPost, path, "", []string{"inventory.read"}, key("k")); rec.Code != http.StatusForbidden {
				t.Fatalf("read duty: status %d, want 403", rec.Code)
			}
			rec := do(t, h, http.MethodPost, path, `{"date":"2026-09-30"}`, []string{stepDuty}, key("k-2"))
			if rec.Code != http.StatusCreated {
				t.Fatalf("status %d body %s", rec.Code, rec.Body.String())
			}
			want := time.Date(2026, 9, 30, 0, 0, 0, 0, time.UTC)
			if fs.stepIn.Command != cmd || fs.stepIn.TransactionID != id || !fs.stepIn.Date.Equal(want) || fs.stepIn.IdempotencyKey != "k-2" {
				t.Fatalf("store input %+v", fs.stepIn)
			}
			if rec := do(t, h, http.MethodPost, path, "", []string{stepDuty}, key("k-3")); rec.Code != http.StatusCreated || !fs.stepIn.Date.IsZero() {
				t.Fatalf("empty body: status %d date %v", rec.Code, fs.stepIn.Date)
			}
		})
	}
}

func TestNamedStepRejectsBadIDKeyAndDate(t *testing.T) {
	h := newServer(t, &fakeStore{})
	rec := do(t, h, http.MethodPost, "/v1/inventory/transactions/not-a-uuid:unpick", "", []string{stepDuty}, key("k"))
	if rec.Code != http.StatusNotFound || decodeProblem(t, rec).Code != domain.CodeTransactionNotFound {
		t.Fatalf("bad id: status %d", rec.Code)
	}
	rec = do(t, h, http.MethodPost, "/v1/inventory/transactions/"+ids.New()+":unregister", `{"date":"30/09/2026"}`, []string{stepDuty}, nil)
	p := decodeProblem(t, rec)
	if rec.Code != http.StatusBadRequest || p.Errors["Idempotency-Key"] == "" || p.Errors["date"] == "" {
		t.Fatalf("status %d errors %v", rec.Code, p.Errors)
	}
}

func TestStatusStepStoreProblemsKeepTheirCode(t *testing.T) {
	fs := &fakeStore{err: httpx.NewProblem(http.StatusUnprocessableEntity, domain.CodeStatusStep, "UNPICK does not apply")}
	h := newServer(t, fs)
	rec := do(t, h, http.MethodPost, "/v1/inventory/transactions/"+ids.New()+":unpick", "", []string{stepDuty}, key("k"))
	if rec.Code != http.StatusUnprocessableEntity || decodeProblem(t, rec).Code != domain.CodeStatusStep {
		t.Fatalf("status %d body %s", rec.Code, rec.Body.String())
	}
}

func TestTransactionDetailCarriesItsHistory(t *testing.T) {
	fs := &fakeStore{detailHistory: []store.StatusChange{
		{ID: ids.New(), Command: domain.CommandCreate, To: domain.StatusRegistered, Qty: decimal.NewFromInt(5)},
		{ID: ids.New(), Command: domain.CommandUnregister, From: domain.StatusRegistered, To: domain.StatusArrived, Qty: decimal.NewFromInt(5)},
	}}
	h := newServer(t, fs)
	id := ids.New()
	rec := do(t, h, http.MethodGet, "/v1/inventory/transactions/"+id, "", []string{"inventory.read"}, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d body %s", rec.Code, rec.Body.String())
	}
	var out struct {
		ID            string `json:"id"`
		ReceiptStatus string `json:"receiptStatus"`
		StatusChanges []struct {
			Command    string `json:"command"`
			FromStatus string `json:"fromStatus"`
			ToStatus   string `json:"toStatus"`
		} `json:"statusChanges"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	if out.ID != id || out.ReceiptStatus != "ARRIVED" || len(out.StatusChanges) != 2 || out.StatusChanges[0].FromStatus != "" ||
		out.StatusChanges[1].Command != "UNREGISTER" || out.StatusChanges[1].ToStatus != "ARRIVED" {
		t.Fatalf("detail %+v", out)
	}
	if rec := do(t, h, http.MethodGet, "/v1/inventory/transactions/nope", "", []string{"inventory.read"}, nil); rec.Code != http.StatusNotFound {
		t.Fatalf("bad id: status %d", rec.Code)
	}
}

func TestStatusChangeInquiryFiltersAndPages(t *testing.T) {
	fs := &fakeStore{changeRows: 3}
	h := newServer(t, fs)
	txn := ids.New()
	rec := do(t, h, http.MethodGet, "/v1/inventory/status-changes?transaction="+txn+"&command=UNPICK&limit=2", "", []string{"inventory.read"}, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d body %s", rec.Code, rec.Body.String())
	}
	if fs.changeFilter.TransactionID != txn || fs.changeFilter.Command != domain.CommandUnpick || fs.changeFilter.Limit != 3 {
		t.Fatalf("filter %+v (limit must ask one extra row)", fs.changeFilter)
	}
	var page struct {
		Items      []json.RawMessage `json:"items"`
		NextCursor string            `json:"nextCursor"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &page); err != nil {
		t.Fatal(err)
	}
	if len(page.Items) != 2 || page.NextCursor == "" {
		t.Fatalf("page of %d with cursor %q, want 2 and a cursor", len(page.Items), page.NextCursor)
	}
	rec = do(t, h, http.MethodGet, "/v1/inventory/status-changes?transaction=x&command=MOVE&cursor=y", "", []string{"inventory.read"}, nil)
	p := decodeProblem(t, rec)
	if rec.Code != http.StatusBadRequest || p.Errors["transaction"] == "" || p.Errors["command"] == "" || p.Errors["cursor"] == "" {
		t.Fatalf("status %d errors %v", rec.Code, p.Errors)
	}
}
