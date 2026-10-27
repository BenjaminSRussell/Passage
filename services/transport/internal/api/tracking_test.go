package api_test

import (
	"encoding/json"
	"net/http"
	"testing"
	"time"

	"github.com/BenjaminSRussell/passage/services/transport/internal/api"
	"github.com/BenjaminSRussell/passage/services/transport/internal/domain"
	"github.com/BenjaminSRussell/passage/services/transport/internal/store"
)

const trackBody = `{"loadId":"` + loadID + `","stopNo":2,"code":"d1","occurredAt":"2026-06-01T11:00:00.1234567-04:00","location":" Dallas TX ","source":"EDI214","ref":"ISA-000123"}`

func TestIngestTracking_StoresNormalisedEvent(t *testing.T) {
	f := &fakeStore{}
	rec := do(t, router(f), http.MethodPost, base+"/tracking-events", trackBody, api.DutyTrackIngest)
	if rec.Code != http.StatusCreated || rec.Header().Get("Idempotent-Replayed") != "" {
		t.Fatalf("status = %d, body %s", rec.Code, rec.Body.String())
	}
	want := time.Date(2026, 6, 1, 15, 0, 0, 123456000, time.UTC)
	if f.recorded.Code != "D1" || f.recorded.Location != "Dallas TX" || !f.recorded.OccurredAt.Equal(want) || f.recorded.OccurredAt.Location() != time.UTC {
		t.Fatalf("recorded = %+v, want D1 at %s UTC truncated to microseconds", f.recorded, want)
	}
}

func TestIngestTracking_ReplayIsOKAndFlagged(t *testing.T) {
	rec := do(t, router(&fakeStore{replayed: true}), http.MethodPost, base+"/tracking-events", trackBody, api.DutyTrackIngest)
	if rec.Code != http.StatusOK || rec.Header().Get("Idempotent-Replayed") != "true" {
		t.Fatalf("status = %d, header %q", rec.Code, rec.Header().Get("Idempotent-Replayed"))
	}
}

func TestIngestTracking_Validation(t *testing.T) {
	h := router(&fakeStore{})
	cases := map[string]struct{ body, field string }{
		"unknown code":   {`{"loadId":"` + loadID + `","stopNo":1,"code":"ZZ","occurredAt":"2026-06-01T08:00:00Z","source":"API","ref":"r"}`, "code"},
		"stop zero":      {`{"loadId":"` + loadID + `","stopNo":0,"code":"AF","occurredAt":"2026-06-01T08:00:00Z","source":"API","ref":"r"}`, "stopNo"},
		"no zone":        {`{"loadId":"` + loadID + `","stopNo":1,"code":"AF","occurredAt":"2026-06-01T08:00:00","source":"API","ref":"r"}`, "occurredAt"},
		"far future":     {`{"loadId":"` + loadID + `","stopNo":1,"code":"AF","occurredAt":"2999-06-01T08:00:00Z","source":"API","ref":"r"}`, "occurredAt"},
		"unknown source": {`{"loadId":"` + loadID + `","stopNo":1,"code":"AF","occurredAt":"2026-06-01T08:00:00Z","source":"FAX","ref":"r"}`, "source"},
		"no ref":         {`{"loadId":"` + loadID + `","stopNo":1,"code":"AF","occurredAt":"2026-06-01T08:00:00Z","source":"API","ref":" "}`, "ref"},
		"bad load":       {`{"loadId":"load-1","stopNo":1,"code":"AF","occurredAt":"2026-06-01T08:00:00Z","source":"API","ref":"r"}`, "loadId"},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			rec := do(t, h, http.MethodPost, base+"/tracking-events", tc.body, api.DutyTrackIngest)
			if rec.Code != http.StatusBadRequest || problem(t, rec).Errors[tc.field] == "" {
				t.Fatalf("status = %d, body %s, want error on %s", rec.Code, rec.Body.String(), tc.field)
			}
		})
	}
}

// TestLoadTracking_AC347: a D1 with no pickup shows the stop DELIVERED with an
// inferred pickup on the timeline (BR-34.9).
func TestLoadTracking_AC347(t *testing.T) {
	at := time.Date(2026, 6, 1, 15, 0, 0, 0, time.UTC)
	f := &fakeStore{tracking: []domain.TrackingEvent{{LoadID: loadID, StopNo: 2, Code: "D1", OccurredAt: at, Source: "EDI214", Ref: "r"}}}
	rec := do(t, router(f), http.MethodGet, base+"/loads/"+loadID+"/tracking", ``, api.DutyView)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body %s", rec.Code, rec.Body.String())
	}
	var resp struct {
		LoadID string
		Events []domain.TrackingEvent
		Stops  []domain.StopState
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	if resp.LoadID != loadID || len(resp.Events) != 1 || len(resp.Stops) != 1 {
		t.Fatalf("resp = %+v", resp)
	}
	s := resp.Stops[0]
	if s.Status != domain.StopDelivered || !s.PickupInferred || s.DeliveredAt == nil || !s.DeliveredAt.Equal(at) {
		t.Fatalf("stop = %+v", s)
	}
	if f.trackFilter.LoadID != loadID {
		t.Fatalf("filter = %+v", f.trackFilter)
	}
}

func TestListTracking_FilterValidated(t *testing.T) {
	f := &fakeStore{}
	h := router(f)
	if rec := do(t, h, http.MethodGet, base+"/tracking-events?loadId=x", ``, api.DutyView); rec.Code != http.StatusBadRequest {
		t.Fatalf("bad loadId = %d", rec.Code)
	}
	rec := do(t, h, http.MethodGet, base+"/tracking-events", ``, api.DutyView)
	if rec.Code != http.StatusOK || f.trackFilter != (store.TrackingFilter{}) {
		t.Fatalf("status = %d filter = %+v", rec.Code, f.trackFilter)
	}
}
