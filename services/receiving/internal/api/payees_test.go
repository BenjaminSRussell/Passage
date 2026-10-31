package api_test

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"testing"

	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/services/receiving/internal/api"
	"github.com/BenjaminSRussell/passage/services/receiving/internal/domain"
	"github.com/BenjaminSRussell/passage/services/receiving/internal/store"
	"github.com/BenjaminSRussell/passage/services/receiving/internal/store/memstore"
)

const payeesPath = "/v1/receiving/one-time-payees"

var payeePII = []string{"Erika Mustermann", "Hauptstrasse", "DE89370400440532013000", "DE123456789", "erika@example.org", "COBADEFF"}

func payeeJSON(mutate map[string]any) string {
	body := map[string]any{
		"legalEntityId": legalEntityID, "name": "Erika Mustermann", "address": "Hauptstrasse 1, 10115 Berlin", "country": "DE",
		"taxId": "DE123456789", "email": "erika@example.org", "bankAccount": map[string]any{"iban": "DE89 3704 0044 0532 0130 00", "bic": "COBADEFF"},
	}
	for k, v := range mutate {
		if v == nil {
			delete(body, k)
		} else {
			body[k] = v
		}
	}
	raw, _ := json.Marshal(body)
	return string(raw)
}

func captureLogs(t *testing.T) *bytes.Buffer {
	t.Helper()
	var buf bytes.Buffer
	previous := slog.Default()
	slog.SetDefault(slog.New(slog.NewJSONHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug})))
	t.Cleanup(func() { slog.SetDefault(previous) })
	return &buf
}

func assertNoPII(t *testing.T, where, text string) {
	t.Helper()
	for _, secret := range payeePII {
		if strings.Contains(text, secret) {
			t.Errorf("%s leaks %q: %s", where, secret, text)
		}
	}
}

func (rg *rig) payee(t *testing.T, body string) string {
	t.Helper()
	got := decode[struct{ ID string }](t, rg.call(http.MethodPost, payeesPath, body, duty("VendorInvoicePost", rg.key())), http.StatusCreated)
	return got.ID
}

func invoiceWithPayee(number, receiptID, payeeID string) string {
	return strings.Replace(invoiceBody(number, receiptID, 4, ""), `{"vendorId"`, fmt.Sprintf(`{"oneTimePayeeId":%q,"vendorId"`, payeeID), 1)
}

func TestOneTimePayee_CreateAnswersWithIdsOnlyAndLogsNothing(t *testing.T) {
	logs := captureLogs(t)
	rg := newRig(domain.PeriodOpen)
	rec := rg.call(http.MethodPost, payeesPath, payeeJSON(nil), duty("VendorInvoicePost", rg.key()))
	got := decode[map[string]any](t, rec, http.StatusCreated)
	if len(got) != 4 || got["legalEntityId"] != legalEntityID || got["country"] != "DE" || got["id"] == "" || got["createdAt"] == "" {
		t.Fatalf("response %v", got)
	}
	assertNoPII(t, "response", rec.Body.String())
	replay := rg.call(http.MethodPost, payeesPath, payeeJSON(nil), duty("VendorInvoicePost", "key-1"))
	if replay.Header().Get("Idempotent-Replayed") != "true" || replay.Body.String() != rec.Body.String() {
		t.Fatalf("replay %d %s", replay.Code, replay.Body)
	}
	assertNoPII(t, "logs", logs.String())
}

func TestOneTimePayee_RejectionsNeverEchoPayeeData(t *testing.T) {
	logs := captureLogs(t)
	rg := newRig(domain.PeriodOpen)
	tests := []struct {
		name   string
		body   string
		status int
		code   string
	}{
		{"missing name", payeeJSON(map[string]any{"name": ""}), http.StatusBadRequest, "COM-0001"},
		{"missing address", payeeJSON(map[string]any{"address": nil}), http.StatusBadRequest, "COM-0001"},
		{"lower-case country", payeeJSON(map[string]any{"country": "de"}), http.StatusBadRequest, "COM-0001"},
		{"email without an at sign", payeeJSON(map[string]any{"email": "erika.example.org"}), http.StatusBadRequest, "COM-0001"},
		{"bad legal entity", payeeJSON(map[string]any{"legalEntityId": "x"}), http.StatusBadRequest, "COM-0001"},
		{"unknown field", payeeJSON(map[string]any{"nickname": "Erika Mustermann"}), http.StatusBadRequest, "COM-0001"},
		{"truncated JSON", `{"name":"Erika Mustermann" "address":"Hauptstrasse 1"}`, http.StatusBadRequest, "COM-0001"},
		{"IBAN checksum", payeeJSON(map[string]any{"bankAccount": map[string]any{"iban": "DE89370400440532013001"}}), http.StatusUnprocessableEntity, "AP-BNK-001"},
		{"empty bank block", payeeJSON(map[string]any{"bankAccount": map[string]any{}}), http.StatusUnprocessableEntity, "AP-BNK-001"},
		{"ABA checksum", payeeJSON(map[string]any{"bankAccount": map[string]any{"routingNumber": "021000022", "accountNumber": "123456789"}}), http.StatusUnprocessableEntity, "AP-BNK-001"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			rec := rg.call(http.MethodPost, payeesPath, tc.body, duty("VendorInvoicePost", rg.key()))
			problemCode(t, rec, tc.status, tc.code)
			assertNoPII(t, "response", rec.Body.String())
		})
	}
	assertNoPII(t, "logs", logs.String())
}

func TestOneTimePayee_DutyUserAndScope(t *testing.T) {
	rg := newRig(domain.PeriodOpen)
	problemCode(t, rg.call(http.MethodPost, payeesPath, payeeJSON(nil), duty("ReceivingMaintain", rg.key())), http.StatusForbidden, "AUTH-0003")
	noUser := duty("VendorInvoicePost", rg.key())
	delete(noUser, "X-Passage-User")
	problemCode(t, rg.call(http.MethodPost, payeesPath, payeeJSON(nil), noUser), http.StatusUnauthorized, "AUTH-0001")
	problemCode(t, rg.call(http.MethodPost, payeesPath, payeeJSON(map[string]any{"legalEntityId": ledgerID}), duty("VendorInvoicePost", rg.key())), http.StatusForbidden, "AUTH-0003")
	problemCode(t, rg.call(http.MethodPost, payeesPath, payeeJSON(nil), headers{"Idempotency-Key": "k"}), http.StatusUnauthorized, "AUTH-0001")
}

type keylessStore struct{ *memstore.Store }

func (keylessStore) CreateOneTimePayee(context.Context, store.OneTimePayee) (store.OneTimePayee, error) {
	return store.OneTimePayee{}, store.ErrNoFieldCrypt
}

func TestOneTimePayee_WithoutKeysIsA503NamingNoData(t *testing.T) {
	logs := captureLogs(t)
	a := api.New(keylessStore{memstore.New()}, api.Ports{Periods: domain.FakePeriods{Status: domain.PeriodOpen}, Ledger: &domain.FakeLedger{}})
	router := httpx.NewRouter()
	a.Register(router)
	rec := doWith(router, http.MethodPost, payeesPath, payeeJSON(nil), duty("VendorInvoicePost", "k"))
	problemCode(t, rec, http.StatusServiceUnavailable, "RCV-CRY-001")
	assertNoPII(t, "response", rec.Body.String())
	assertNoPII(t, "logs", logs.String())
}

func TestInvoice_ReferencesAOneTimePayeeAndEventsCarryItsIdOnly(t *testing.T) {
	logs := captureLogs(t)
	rg := newRig(domain.PeriodOpen)
	r := rg.posted(t, "PS-1", 10, 4)
	payeeID := rg.payee(t, payeeJSON(nil))

	rec := rg.call(http.MethodPost, invoicesPath, invoiceWithPayee("OT-1", r.ID, payeeID), rg.apPoster())
	got := decode[struct {
		OneTimePayeeID string `json:"oneTimePayeeId"`
		Status         string `json:"status"`
	}](t, rec, http.StatusCreated)
	if got.OneTimePayeeID != payeeID || got.Status != "POSTED" {
		t.Fatalf("invoice %+v", got)
	}
	evts := rg.st.Events()
	posted := evts[len(evts)-1]
	if posted.Type != "dev.passage.payables.invoice.posted.v1" || !strings.Contains(string(posted.Data), `"one_time_payee_id":"`+payeeID+`"`) {
		t.Fatalf("event %s %s", posted.Type, posted.Data)
	}
	for _, e := range evts {
		assertNoPII(t, "event "+e.Type, string(e.Data))
	}
	assertNoPII(t, "invoice response", rec.Body.String())
	assertNoPII(t, "logs", logs.String())
}

func TestInvoice_PayeeMustExistAndBelongToTheInvoiceEntity(t *testing.T) {
	rg := newRig(domain.PeriodOpen)
	r := rg.posted(t, "PS-1", 10, 4)
	problemCode(t, rg.call(http.MethodPost, invoicesPath, invoiceWithPayee("OT-1", r.ID, "00000000-0000-4000-8000-0000000000ff"), rg.apPoster()), http.StatusNotFound, "RCV-NF-006")
	rec := rg.call(http.MethodPost, invoicesPath, invoiceWithPayee("OT-1", r.ID, "nope"), rg.apPoster())
	if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), `"oneTimePayeeId"`) {
		t.Fatalf("got %d %s", rec.Code, rec.Body)
	}
	elsewhere := decode[struct{ ID string }](t, rg.call(http.MethodPost, payeesPath, payeeJSON(map[string]any{"legalEntityId": ledgerID}),
		duty("VendorInvoicePost", rg.key(), entityHeader, ledgerID)), http.StatusCreated)
	// Outside the caller's legal-entity scope the payee is hidden (404), as under Postgres RLS (#335).
	problemCode(t, rg.call(http.MethodPost, invoicesPath, invoiceWithPayee("OT-1", r.ID, elsewhere.ID), rg.apPoster()), http.StatusNotFound, "RCV-NF-006")
	// In scope but of another entity than the invoice: a validation error.
	problemCode(t, rg.call(http.MethodPost, invoicesPath, invoiceWithPayee("OT-1", r.ID, elsewhere.ID),
		duty("VendorInvoicePost", rg.key(), entityHeader, legalEntityID+","+ledgerID)), http.StatusUnprocessableEntity, "RCV-VAL-001")
}

func TestInvoice_AuditHoldAndReleaseKeepThePayeeOnBothEvents(t *testing.T) {
	rg := newRigPorts(memstore.New(), domain.PeriodOpen, func(p *api.Ports) { p.AuditSampler = testSampler("seed-one-0123456789") })
	rg.sampleRate(t, "100")
	r := rg.posted(t, "PS-1", 10, 4)
	payeeID := rg.payee(t, payeeJSON(nil))
	held := decode[auditedInvoiceJSON](t, rg.call(http.MethodPost, invoicesPath, invoiceWithPayee("OT-1", r.ID, payeeID), userDuty("VendorInvoicePost", rg.key(), "alice")), http.StatusCreated)
	if held.Status != "AUDIT_BLOCKED" {
		t.Fatalf("held %+v", held)
	}
	if code, body := rg.release(held.ID, "auditor"); code != http.StatusOK {
		t.Fatalf("release %d %s", code, body)
	}
	var withPayee []string
	for _, e := range rg.st.Events() {
		if strings.Contains(string(e.Data), `"one_time_payee_id":"`+payeeID+`"`) {
			withPayee = append(withPayee, e.Type)
		}
		assertNoPII(t, "event "+e.Type, string(e.Data))
	}
	want := []string{"dev.passage.payables.invoice.sampled.v1", "dev.passage.payables.invoice.posted.v1"}
	if fmt.Sprint(withPayee) != fmt.Sprint(want) {
		t.Fatalf("events carrying the payee: %v, want %v", withPayee, want)
	}
}
