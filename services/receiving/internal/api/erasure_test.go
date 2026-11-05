package api_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"testing"
	"time"

	"github.com/BenjaminSRussell/passage/platform/eventing"
	"github.com/BenjaminSRussell/passage/platform/events"
	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/platform/tenant"
	"github.com/BenjaminSRussell/passage/platform/testkit"
	"github.com/BenjaminSRussell/passage/services/receiving/internal/api"
	"github.com/BenjaminSRussell/passage/services/receiving/internal/domain"
	"github.com/BenjaminSRussell/passage/services/receiving/internal/store"
	"github.com/BenjaminSRussell/passage/services/receiving/internal/store/memstore"
)

const erasePath = "/v1/receiving/one-time-payees:erase"

func erasureRig(t *testing.T, holds domain.LegalHolds) *rig {
	t.Helper()
	return erasureRigIn(t, holds, "GB")
}

func erasureRigIn(t *testing.T, holds domain.LegalHolds, entityCountry string) *rig {
	t.Helper()
	rg := newRigPorts(memstore.New(), domain.PeriodOpen, func(p *api.Ports) {
		p.LegalHolds = holds
		if entityCountry != "" {
			p.LegalEntityCountry = map[string]string{legalEntityID: entityCountry}
		}
	})
	owner := tenant.Principal{TenantID: testkit.TenantID(1), LegalEntityID: legalEntityID, UserID: "alice"}
	old := time.Now().UTC().AddDate(-8, 0, 0)
	for i, subject := range []string{"party:42", "party:42", "party:43"} {
		payee := store.OneTimePayee{ID: fmt.Sprintf("00000000-0000-4000-8000-%012d", i+1), LegalEntityID: legalEntityID, Country: "GB", SubjectRef: subject, CreatedAt: old}
		if i == 1 {
			payee.CreatedAt = time.Now().UTC()
		}
		if _, err := rg.st.CreateOneTimePayee(tenant.With(context.Background(), owner), payee); err != nil {
			t.Fatal(err)
		}
	}
	return rg
}

func erase(rg *rig, body string) *struct {
	Code int
	Body map[string]any
} {
	rec := rg.call(http.MethodPost, erasePath, body, duty("receiving.payee.erase", rg.key()))
	out := &struct {
		Code int
		Body map[string]any
	}{Code: rec.Code}
	_ = json.Unmarshal(rec.Body.Bytes(), &out.Body)
	return out
}

func TestEraseOneTimePayees_NeedsItsOwnDutyAndAScopedLegalEntity(t *testing.T) {
	rg := erasureRig(t, &domain.FakeLegalHolds{})
	body := fmt.Sprintf(`{"legalEntityId":%q}`, legalEntityID)
	if rec := rg.call(http.MethodPost, erasePath, body, duty("receiving.vendor-invoice.post", rg.key())); rec.Code != http.StatusForbidden {
		t.Fatalf("invoice poster must not erase: %d", rec.Code)
	}
	if got := erase(rg, fmt.Sprintf(`{"legalEntityId":%q}`, ids.New())); got.Code != http.StatusForbidden || got.Body["code"] != "AUTH-0003" {
		t.Fatalf("another legal entity: %d %v", got.Code, got.Body)
	}
	for name, bad := range map[string]string{"no entity": `{}`, "unknown field": fmt.Sprintf(`{"legalEntityId":%q,"all":true}`, legalEntityID), "long subject": fmt.Sprintf(`{"legalEntityId":%q,"subjectRef":%q}`, legalEntityID, string(make([]byte, 201)))} {
		if got := erase(rg, bad); got.Code != http.StatusBadRequest {
			t.Errorf("%s: %d", name, got.Code)
		}
	}
}

func TestEraseOneTimePayees_RefusesWithoutAHoldCheckAndWhenItFails(t *testing.T) {
	rg := erasureRig(t, nil)
	body := fmt.Sprintf(`{"legalEntityId":%q}`, legalEntityID)
	if got := erase(rg, body); got.Code != http.StatusServiceUnavailable || got.Body["code"] != "RCV-HLD-001" {
		t.Fatalf("no hold check configured: %d %v", got.Code, got.Body)
	}
	broken := erasureRig(t, &domain.FakeLegalHolds{Err: errors.New("compliance down")})
	if got := erase(broken, body); got.Code != http.StatusInternalServerError {
		t.Fatalf("hold check failing: %d", got.Code)
	}
	healthy := erasureRig(t, &domain.FakeLegalHolds{})
	if got := erase(healthy, body); got.Code != http.StatusOK || got.Body["erased"] != float64(2) {
		t.Fatalf("a failed run must have erased nothing, so a healthy one erases both expired payees: %d %v", got.Code, got.Body)
	}
}

func TestEraseOneTimePayees_SubjectRunReportsErasedRetainedAndHeld(t *testing.T) {
	holds := &domain.FakeLegalHolds{ByRecord: map[string][]string{"00000000-0000-4000-8000-000000000003": {"hold-1"}}}
	rg := erasureRig(t, holds)
	got := erase(rg, fmt.Sprintf(`{"legalEntityId":%q,"subjectRef":"party:42"}`, legalEntityID))
	if got.Code != http.StatusOK || got.Body["erased"] != float64(1) || got.Body["retained"] != float64(1) || got.Body["held"] != float64(0) {
		t.Fatalf("party:42: %d %v", got.Code, got.Body)
	}
	got = erase(rg, fmt.Sprintf(`{"legalEntityId":%q}`, legalEntityID))
	if got.Body["erased"] != float64(0) || got.Body["held"] != float64(1) || got.Body["retained"] != float64(1) {
		t.Fatalf("sweep: %v", got.Body)
	}
	for _, q := range holds.Queries {
		if q.DataClass != domain.PayeeDataClass || q.LegalEntityID != legalEntityID || q.RecordRef == "" {
			t.Errorf("hold query %+v", q)
		}
	}
}

func TestEraseOneTimePayees_KeepsEverythingWhenTheEntityCountryHasAHigherFloorOrIsUnknown(t *testing.T) {
	body := fmt.Sprintf(`{"legalEntityId":%q}`, legalEntityID)
	for name, country := range map[string]string{"DE": "DE", "FR": "FR", "unknown": ""} {
		rg := erasureRigIn(t, &domain.FakeLegalHolds{}, country)
		if got := erase(rg, body); got.Code != http.StatusOK || got.Body["erased"] != float64(0) || got.Body["retained"] != float64(3) {
			t.Errorf("%s entity: %d %v", name, got.Code, got.Body)
		}
	}
}

func TestEraseOneTimePayees_RequiresAndReplaysTheIdempotencyKey(t *testing.T) {
	rg := erasureRig(t, &domain.FakeLegalHolds{})
	body := fmt.Sprintf(`{"legalEntityId":%q}`, legalEntityID)
	if rec := rg.call(http.MethodPost, erasePath, body, duty("receiving.payee.erase", "")); rec.Code != http.StatusBadRequest {
		t.Fatalf("missing key: %d", rec.Code)
	}
	first := rg.call(http.MethodPost, erasePath, body, duty("receiving.payee.erase", "erase-1"))
	second := rg.call(http.MethodPost, erasePath, body, duty("receiving.payee.erase", "erase-1"))
	if first.Code != http.StatusOK || second.Header().Get("Idempotent-Replayed") != "true" || first.Body.String() != second.Body.String() {
		t.Fatalf("replay: %d %d %q", first.Code, second.Code, second.Header().Get("Idempotent-Replayed"))
	}
}

func erasureEvent(t *testing.T, data map[string]any) events.Event {
	t.Helper()
	evt, err := events.New(api.ErasureRequestedType, "compliance", ids.New(), testkit.TenantID(1), data)
	if err != nil {
		t.Fatal(err)
	}
	return evt
}

func TestErasureConsumer_ActsOnItsClassAndRefusesMalformedEvents(t *testing.T) {
	holds := &domain.FakeLegalHolds{}
	rg := erasureRig(t, holds)
	handle := func(data map[string]any) error {
		return rg.api.ErasureConsumerHandlers()[api.ErasureRequestedType].Handle(context.Background(), nil, erasureEvent(t, data))
	}
	command := func(classes []string) map[string]any {
		return map[string]any{"erasure_id": ids.New(), "legal_entity_id": legalEntityID, "subject_ref": "party:42", "data_classes": classes}
	}
	if err := handle(command([]string{"CANDIDATE"})); err != nil || len(holds.Queries) != 0 {
		t.Fatalf("another class is not this service's: %v %d", err, len(holds.Queries))
	}
	for name, bad := range map[string]map[string]any{
		"no subject": {"erasure_id": ids.New(), "legal_entity_id": legalEntityID, "subject_ref": " "},
		"no entity":  {"erasure_id": ids.New(), "subject_ref": "party:42"},
	} {
		if err := handle(bad); !eventing.IsPermanent(err) {
			t.Errorf("%s: %v", name, err)
		}
	}
	holds.Err = errors.New("compliance down")
	if err := handle(command([]string{domain.PayeeDataClass})); err == nil || eventing.IsPermanent(err) {
		t.Fatalf("an unreachable hold check must be retried, not dropped: %v", err)
	}
	holds.Err = nil
	if err := handle(command(nil)); err != nil {
		t.Fatalf("an empty class list means every class: %v", err)
	}
	if got := erase(rg, fmt.Sprintf(`{"legalEntityId":%q,"subjectRef":"party:42"}`, legalEntityID)); got.Body["erased"] != float64(0) || got.Body["retained"] != float64(1) {
		t.Fatalf("the consumer already erased the expired payee: %v", got.Body)
	}
}
