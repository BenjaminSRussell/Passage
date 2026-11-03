package api_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/platform/tenant"
	"github.com/BenjaminSRussell/passage/services/transport/internal/api"
	"github.com/BenjaminSRussell/passage/services/transport/internal/domain"
	"github.com/BenjaminSRussell/passage/services/transport/internal/store"
)

func (f *fakeStore) CreateTender(_ context.Context, in domain.Tender, idem store.Idem, now time.Time) (domain.Tender, error) {
	f.idems, f.tender, f.tenderNow = append(f.idems, idem), in, now
	if f.tenderErr != nil {
		return domain.Tender{}, f.tenderErr
	}
	in.ID, in.Version = tenderID, 1
	return in, nil
}

func (f *fakeStore) GetTender(_ context.Context, _ string) (domain.Tender, error) {
	return f.tender, f.tenderErr
}

func (f *fakeStore) ListTenders(_ context.Context, filter store.TenderFilter) ([]domain.Tender, error) {
	f.tenderFilter = filter
	return []domain.Tender{f.tender}, nil
}

func (f *fakeStore) RespondTender(_ context.Context, id, carrier string, accept bool, now time.Time) (domain.Tender, error) {
	f.responded, f.tenderNow = append(f.responded, id, carrier), now
	if f.tenderErr != nil {
		return domain.Tender{}, f.tenderErr
	}
	if err := f.tender.Respond(carrier, accept, now); err != nil {
		return domain.Tender{}, err
	}
	return f.tender, nil
}

func (f *fakeStore) ExpireTenders(_ context.Context, now time.Time) ([]domain.Tender, error) {
	f.tenderNow = now
	if len(f.tender.TickAt(now)) == 0 {
		return []domain.Tender{}, nil
	}
	return []domain.Tender{f.tender}, nil
}

func (f *fakeStore) RecordTrackingEvent(_ context.Context, in domain.TrackingEvent) (domain.TrackingEvent, bool, error) {
	f.recorded = in
	in.ID = "0190f0a0-0000-7000-8000-0000000000e9"
	return in, !f.replayed, nil
}

func (f *fakeStore) ListTrackingEvents(_ context.Context, filter store.TrackingFilter) ([]domain.TrackingEvent, error) {
	f.trackFilter = filter
	return f.tracking, nil
}

func (f *fakeStore) CreateVoyage(_ context.Context, in domain.Voyage, idem store.Idem) (domain.Voyage, error) {
	f.idems, f.createdVoyage = append(f.idems, idem), in
	in.ID, in.Status = voyageID, domain.VoyageEstimated
	return in, nil
}

func (f *fakeStore) GetVoyage(_ context.Context, _ string) (domain.Voyage, error) {
	return f.voyage, f.voyageErr
}

func (f *fakeStore) ListVoyages(_ context.Context, _ store.VoyageFilter) ([]store.VoyageSummary, error) {
	return []store.VoyageSummary{{ID: voyageID, Reference: f.voyage.Reference, Status: f.voyage.Status}}, nil
}

func (f *fakeStore) AllocateVoyage(_ context.Context, _ string, _ time.Time) (domain.Voyage, error) {
	if f.voyageErr != nil {
		return domain.Voyage{}, f.voyageErr
	}
	posted, err := f.voyage.Allocate()
	if err != nil {
		return domain.Voyage{}, err
	}
	return posted, nil
}

const (
	tenderID = "0190f0a0-0000-7000-8000-0000000000f1"
	voyageID = "0190f0a0-0000-7000-8000-0000000000f2"
	carrierA = "0190f0a0-0000-7000-8000-0000000000c2"
	carrierB = "0190f0a0-0000-7000-8000-0000000000c3"
	carrierC = "0190f0a0-0000-7000-8000-0000000000c4"
)

var t0 = time.Date(2026, 6, 1, 8, 0, 0, 0, time.UTC)

func conflict(code string) error { return httpx.NewProblem(http.StatusConflict, code, code) }

// clockRouter mounts the API with a clock the test moves.
func clockRouter(st store.Store, now *time.Time) http.Handler {
	r := chi.NewRouter()
	api.New(st).WithClock(func() time.Time { return *now }).Register(r)
	return r
}

type tenderView struct {
	ID                string `json:"id"`
	Status            string `json:"status"`
	TimeoutMin        int    `json:"timeoutMin"`
	AcceptedCarrierID string `json:"acceptedCarrierId"`
	PendingCarrierID  string `json:"pendingCarrierId"`
	Overdue           bool   `json:"overdue"`
	PlannerTask       bool   `json:"plannerTask"`
	Attempts          []struct {
		Rank       int       `json:"rank"`
		CarrierID  string    `json:"carrierId"`
		ExpiresAt  time.Time `json:"expiresAt"`
		Response   string    `json:"response"`
		RateQuoted *string   `json:"rateQuoted"`
	} `json:"attempts"`
}

func decodeTender(t *testing.T, body []byte) tenderView {
	t.Helper()
	var v tenderView
	if err := json.Unmarshal(body, &v); err != nil {
		t.Fatalf("decode tender: %v (%s)", err, body)
	}
	return v
}

func TestStartTender_SendsToRankOneWithDefaults(t *testing.T) {
	now := t0
	f := &fakeStore{}
	h := clockRouter(f, &now)
	body := `{"carriers":[{"carrierId":"` + carrierA + `","rateQuoted":"412.50"},{"carrierId":"` + carrierB + `"}],"currency":"USD"}`
	rec := doWith(t, h, http.MethodPost, base+"/loads/"+loadID+":tender", body, map[string]string{"Idempotency-Key": "t-1"}, api.DutyTender)
	if rec.Code != http.StatusCreated {
		t.Fatalf("status = %d, body %s", rec.Code, rec.Body.String())
	}
	v := decodeTender(t, rec.Body.Bytes())
	if v.Status != domain.TenderPending || v.TimeoutMin != 120 || v.PendingCarrierID != carrierA || v.Overdue {
		t.Fatalf("tender = %+v, want PENDING on carrier A with the 120 min default", v)
	}
	if len(v.Attempts) != 1 || v.Attempts[0].Rank != 1 || !v.Attempts[0].ExpiresAt.Equal(t0.Add(2*time.Hour)) {
		t.Fatalf("attempts = %+v", v.Attempts)
	}
	if v.Attempts[0].RateQuoted == nil || *v.Attempts[0].RateQuoted != "412.50" {
		t.Fatalf("rate quoted = %v, want 412.50", v.Attempts[0].RateQuoted)
	}
	if f.tender.LoadID != loadID || f.tender.Currency != "USD" || !f.tenderNow.Equal(t0) {
		t.Fatalf("store got %+v at %s", f.tender, f.tenderNow)
	}
	if len(f.idems) != 1 || f.idems[0].Key != "t-1" || f.idems[0].Fingerprint == "" {
		t.Fatalf("idempotency = %+v", f.idems)
	}
}

func TestStartTender_Validation(t *testing.T) {
	h := router(&fakeStore{})
	cases := map[string]struct{ body, field string }{
		"no carriers":         {`{"carriers":[]}`, "carriers"},
		"duplicate carrier":   {`{"carriers":[{"carrierId":"` + carrierA + `"},{"carrierId":"` + carrierA + `"}]}`, "carriers"},
		"upper-case id":       {`{"carriers":[{"carrierId":"0190F0A0-0000-7000-8000-0000000000C2"}]}`, "carriers"},
		"negative rate":       {`{"carriers":[{"carrierId":"` + carrierA + `","rateQuoted":"-1"}],"currency":"USD"}`, "carriers"},
		"rate needs currency": {`{"carriers":[{"carrierId":"` + carrierA + `","rateQuoted":"10"}]}`, "currency"},
		"timeout too short":   {`{"carriers":[{"carrierId":"` + carrierA + `"}],"timeoutMin":4}`, "timeoutMin"},
		"timeout too long":    {`{"carriers":[{"carrierId":"` + carrierA + `"}],"timeoutMin":1441}`, "timeoutMin"},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			rec := do(t, h, http.MethodPost, base+"/loads/"+loadID+":tender", tc.body, api.DutyTender)
			if rec.Code != http.StatusBadRequest {
				t.Fatalf("status = %d, body %s", rec.Code, rec.Body.String())
			}
			if p := problem(t, rec); p.Errors[tc.field] == "" {
				t.Fatalf("errors = %v, want %s", p.Errors, tc.field)
			}
		})
	}
}

func TestStartTender_OpenTenderConflictPassesThrough(t *testing.T) {
	h := router(&fakeStore{tenderErr: conflict(domain.CodeTenderOpen)})
	rec := do(t, h, http.MethodPost, base+"/loads/"+loadID+":tender", `{"carriers":[{"carrierId":"`+carrierA+`"}]}`, api.DutyTender)
	if rec.Code != http.StatusConflict || problem(t, rec).Code != domain.CodeTenderOpen {
		t.Fatalf("status = %d, body %s", rec.Code, rec.Body.String())
	}
}

// TestTenderCascade_AC343 drives AC-34.3 through the API with a test clock and
// 10-minute timeouts: carrier 1 ignores, carrier 2 declines, carrier 3 accepts.
func TestTenderCascade_AC343(t *testing.T) {
	now := t0
	tender, _ := domain.NewTender(loadID, []string{carrierA, carrierB, carrierC}, 10*time.Minute)
	tender.ID = tenderID
	tender.Send(t0)
	f := &fakeStore{tender: tender}
	h := clockRouter(f, &now)

	now = t0.Add(10 * time.Minute)
	rec := do(t, h, http.MethodPost, base+"/tenders:expire", ``, api.DutyTender)
	var swept struct{ Items []tenderView }
	if err := json.Unmarshal(rec.Body.Bytes(), &swept); err != nil || rec.Code != http.StatusOK || len(swept.Items) != 1 {
		t.Fatalf("sweep = %d %s", rec.Code, rec.Body.String())
	}
	if swept.Items[0].PendingCarrierID != carrierB || swept.Items[0].Attempts[0].Response != domain.ResponseExpired {
		t.Fatalf("after sweep = %+v", swept.Items[0])
	}

	now = t0.Add(12 * time.Minute)
	rec = do(t, h, http.MethodPost, base+"/tenders/"+tenderID+":respond", `{"carrierId":"`+carrierB+`","accept":false}`, api.DutyRespond)
	if rec.Code != http.StatusOK || decodeTender(t, rec.Body.Bytes()).PendingCarrierID != carrierC {
		t.Fatalf("decline = %d %s", rec.Code, rec.Body.String())
	}

	now = t0.Add(15 * time.Minute)
	rec = do(t, h, http.MethodPost, base+"/tenders/"+tenderID+":respond", `{"carrierId":"`+carrierC+`","accept":true}`, api.DutyRespond)
	v := decodeTender(t, rec.Body.Bytes())
	if rec.Code != http.StatusOK || v.Status != domain.TenderAccepted || v.AcceptedCarrierID != carrierC || v.PendingCarrierID != "" {
		t.Fatalf("accept = %d %+v", rec.Code, v)
	}
	if !f.tenderNow.Equal(now) {
		t.Fatalf("store clock = %s, want %s", f.tenderNow, now)
	}
}

func TestRespondTender_Validation(t *testing.T) {
	h := router(&fakeStore{})
	for name, body := range map[string]string{
		"accept missing": `{"carrierId":"` + carrierA + `"}`,
		"bad carrier":    `{"carrierId":"nope","accept":true}`,
	} {
		t.Run(name, func(t *testing.T) {
			rec := do(t, h, http.MethodPost, base+"/tenders/"+tenderID+":respond", body, api.DutyRespond)
			if rec.Code != http.StatusBadRequest {
				t.Fatalf("status = %d, body %s", rec.Code, rec.Body.String())
			}
		})
	}
	rec := do(t, h, http.MethodPost, base+"/tenders/not-a-uuid:respond", `{"carrierId":"`+carrierA+`","accept":true}`, api.DutyRespond)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("bad id status = %d", rec.Code)
	}
}

func TestRespondTender_ExpiredPassesThrough(t *testing.T) {
	h := router(&fakeStore{tenderErr: conflict(domain.CodeTenderExpired)})
	rec := do(t, h, http.MethodPost, base+"/tenders/"+tenderID+":respond", `{"carrierId":"`+carrierA+`","accept":true}`, api.DutyRespond)
	if rec.Code != http.StatusConflict || problem(t, rec).Code != domain.CodeTenderExpired {
		t.Fatalf("status = %d, body %s", rec.Code, rec.Body.String())
	}
}

// TestTenderRoutes_Duties: tendering is TMS_TENDER, responding is the carrier's
// TMS_CARRIER_RESPOND, and neither duty grants the other.
func TestTenderRoutes_Duties(t *testing.T) {
	h := router(&fakeStore{})
	cases := []struct{ method, path, duty string }{
		{http.MethodPost, base + "/loads/" + loadID + ":tender", api.DutyRespond},
		{http.MethodPost, base + "/tenders/" + tenderID + ":respond", api.DutyTender},
		{http.MethodGet, base + "/tenders", api.DutyView},
		{http.MethodPost, base + "/tenders:expire", api.DutyRespond},
		{http.MethodPost, base + "/tracking-events", api.DutyView},
		{http.MethodPost, base + "/voyages", api.DutyView},
		{http.MethodPost, base + "/landed-cost/voyages/" + voyageID + ":allocate", api.DutyReconcile},
	}
	for _, tc := range cases {
		if rec := do(t, h, tc.method, tc.path, `{}`, tc.duty); rec.Code != http.StatusForbidden {
			t.Fatalf("%s %s with %s = %d, want 403", tc.method, tc.path, tc.duty, rec.Code)
		}
	}
}

func doIssuedBy(t *testing.T, h http.Handler, issuer, method, path string, duties ...string) *httptest.ResponseRecorder {
	t.Helper()
	granted := map[string]struct{}{}
	for _, d := range duties {
		granted[d] = struct{}{}
	}
	principal := tenant.Principal{TenantID: "00000000-0000-0000-0000-000000000001", LegalEntityID: "00000000-0000-0000-0000-0000000000e1", UserID: "user-test", Duties: granted, Service: issuer}
	body := `{}`
	if strings.HasSuffix(path, ":tender") {
		body = `{"carriers":[{"carrierId":"` + carrierA + `"}]}`
	}
	req := httptest.NewRequest(method, path, strings.NewReader(body)).WithContext(tenant.With(context.Background(), principal))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Idempotency-Key", "issued-by")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

func TestTenderServiceDuty_ReachesOnlyTheWorkflowRoutes(t *testing.T) {
	tender, _ := domain.NewTender(loadID, []string{carrierA}, 2*time.Hour)
	tender.ID = tenderID
	tender.Send(t0)
	h := router(&fakeStore{tender: tender})
	reach := []struct{ method, path string }{
		{http.MethodPost, base + "/loads/" + loadID + ":tender"},
		{http.MethodGet, base + "/tenders/" + tenderID},
		{http.MethodPost, base + "/tenders:expire"},
	}
	for _, tc := range reach {
		if rec := doIssuedBy(t, h, "workflow-ts", tc.method, tc.path, api.DutyTenderExec); rec.Code < 200 || rec.Code >= 300 {
			t.Fatalf("%s %s with the service duty = %d, want 2xx, body %s", tc.method, tc.path, rec.Code, rec.Body.String())
		}
		if rec := doIssuedBy(t, h, "", tc.method, tc.path, api.DutyTenderExec); rec.Code != http.StatusForbidden {
			t.Fatalf("%s %s with the service duty and no workflow-ts issuer = %d, want 403", tc.method, tc.path, rec.Code)
		}
		if rec := doIssuedBy(t, h, "payables", tc.method, tc.path, api.DutyTenderExec); rec.Code != http.StatusForbidden {
			t.Fatalf("%s %s with the service duty from another issuer = %d, want 403", tc.method, tc.path, rec.Code)
		}
	}
	refuse := []struct{ method, path string }{
		{http.MethodGet, base + "/tenders"},
		{http.MethodPost, base + "/tenders/" + tenderID + ":respond"},
		{http.MethodPost, base + "/tracking-events"},
		{http.MethodPost, base + "/freight-bills"},
	}
	for _, tc := range refuse {
		if rec := doIssuedBy(t, h, "workflow-ts", tc.method, tc.path, api.DutyTenderExec); rec.Code != http.StatusForbidden {
			t.Fatalf("%s %s with the service duty = %d, want 403", tc.method, tc.path, rec.Code)
		}
	}
}

func TestListTenders_FiltersAndFlagsOverdue(t *testing.T) {
	now := t0.Add(3 * time.Hour)
	tender, _ := domain.NewTender(loadID, []string{carrierA}, 2*time.Hour)
	tender.ID = tenderID
	tender.Send(t0)
	f := &fakeStore{tender: tender}
	h := clockRouter(f, &now)
	rec := do(t, h, http.MethodGet, base+"/tenders?status=PENDING&loadId="+loadID, ``, api.DutyTender)
	var list struct{ Items []tenderView }
	if err := json.Unmarshal(rec.Body.Bytes(), &list); err != nil || rec.Code != http.StatusOK || len(list.Items) != 1 {
		t.Fatalf("list = %d %s", rec.Code, rec.Body.String())
	}
	if !list.Items[0].Overdue {
		t.Fatal("a pending attempt past its deadline must be flagged overdue")
	}
	if f.tenderFilter != (store.TenderFilter{Status: "PENDING", LoadID: loadID}) {
		t.Fatalf("filter = %+v", f.tenderFilter)
	}
	if rec := do(t, h, http.MethodGet, base+"/tenders?status=OPEN", ``, api.DutyTender); rec.Code != http.StatusBadRequest {
		t.Fatalf("bad status filter = %d", rec.Code)
	}
}

func TestGetTender_NotFound(t *testing.T) {
	h := router(&fakeStore{tenderErr: store.NotFound("tender")})
	rec := do(t, h, http.MethodGet, base+"/tenders/"+tenderID, ``, api.DutyTender)
	if rec.Code != http.StatusNotFound || problem(t, rec).Code != domain.CodeNotFound {
		t.Fatalf("status = %d, body %s", rec.Code, rec.Body.String())
	}
}
