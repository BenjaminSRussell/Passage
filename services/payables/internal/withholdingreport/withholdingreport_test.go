package withholdingreport

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"
	"time"

	"github.com/BenjaminSRussell/passage/platform/testkit"
	"github.com/BenjaminSRussell/passage/services/payables/internal/store"
	"github.com/BenjaminSRussell/passage/services/payables/internal/tax"
)

type statusRecorder struct{ status int }

func (r statusRecorder) RecordWithholding(context.Context, string, []tax.Entry) error {
	if r.status == 0 {
		return nil
	}
	return &tax.StatusError{Status: r.status}
}

// A refused token (401, 403), a timeout and throttling are retried with back-off like an outage; any other 4xx parks the report.
func TestAttempt_RetriesARefusedTokenAndParksARefusal(t *testing.T) {
	body, _ := json.Marshal([]tax.Entry{{ID: "e1", PartyRef: "v1", Code: "WHT30"}})
	claim := store.ClaimedWithholdingReport{ID: "r1", LegalEntityID: "0190f0a0-0000-7000-8000-000000000001", RunID: "run", Body: body, Attempts: 1}
	for status, want := range map[int]string{
		0:                              store.WithholdingReported,
		http.StatusUnauthorized:        store.WithholdingPending,
		http.StatusForbidden:           store.WithholdingPending,
		http.StatusRequestTimeout:      store.WithholdingPending,
		http.StatusTooManyRequests:     store.WithholdingPending,
		http.StatusServiceUnavailable:  store.WithholdingPending,
		http.StatusUnprocessableEntity: store.WithholdingParked,
		http.StatusBadRequest:          store.WithholdingParked,
	} {
		r := &Reporter{Tax: statusRecorder{status}, Now: func() time.Time { return time.Unix(0, 0) }}
		if got := r.attempt(context.Background(), testkit.TenantID(1), claim); got.Status != want {
			t.Errorf("status %d: %s, want %s", status, got.Status, want)
		}
	}
}
