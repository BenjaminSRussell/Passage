package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/platform/tenant"
	"github.com/BenjaminSRussell/passage/services/sales/internal/domain"
)

func planningRequest(body string) *http.Request {
	r := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(body))
	ctx := tenant.With(r.Context(), tenant.Principal{TenantID: ids.New(), UserID: "planner", Duties: map[string]struct{}{}})
	return r.WithContext(ctx)
}

// TestCheckRelease_HoldAndOpenLines covers AC-27.13: a cumulative mismatch holds
// the release with no schedule lines, and a clean release creates open lines
// totalling the open quantity.
func TestCheckRelease_HoldAndOpenLines(t *testing.T) {
	a := New(nil)
	cases := []struct {
		name     string
		body     string
		wantHeld bool
		wantOpen string
		wantLens int
	}{
		{
			name:     "mismatch holds",
			body:     `{"cumReceived":"1150","cumShipped":"1200","buckets":[{"date":"2027-04-15","cumRequired":"1500"}]}`,
			wantHeld: true, wantOpen: "0", wantLens: 0,
		},
		{
			name:     "clean release opens 300",
			body:     `{"cumReceived":"1200","cumShipped":"1200","buckets":[{"date":"2027-04-15","cumRequired":"1500"}]}`,
			wantOpen: "300", wantLens: 1,
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			w := httptest.NewRecorder()
			a.checkRelease(w, planningRequest(c.body))
			if w.Code != http.StatusOK {
				t.Fatalf("status %d: %s", w.Code, w.Body.String())
			}
			var got struct {
				Held         bool                 `json:"held"`
				Code         string               `json:"code"`
				OpenQuantity decimal.Decimal      `json:"openQuantity"`
				Lines        []domain.ReleaseLine `json:"lines"`
			}
			if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
				t.Fatal(err)
			}
			if got.Held != c.wantHeld || len(got.Lines) != c.wantLens {
				t.Fatalf("held=%v lines=%d, want held=%v lines=%d", got.Held, len(got.Lines), c.wantHeld, c.wantLens)
			}
			if got.Held && got.Code != "CUM_QTY_MISMATCH" {
				t.Fatalf("held release code = %q", got.Code)
			}
			if got.OpenQuantity.String() != c.wantOpen {
				t.Fatalf("open quantity %s, want %s", got.OpenQuantity, c.wantOpen)
			}
			if c.wantLens == 1 && (got.Lines[0].Date != "2027-04-15" || got.Lines[0].Quantity.String() != "300") {
				t.Fatalf("schedule line %+v", got.Lines[0])
			}
		})
	}
}

func TestCheckRelease_RejectsBadInput(t *testing.T) {
	a := New(nil)
	cases := map[string]string{
		"missing buckets": `{"cumReceived":"1","cumShipped":"1"}`,
		"not a decimal":   `{"cumReceived":"x","cumShipped":"1","buckets":[{"date":"2027-04-15","cumRequired":"2"}]}`,
		"unordered dates": `{"cumReceived":"1","cumShipped":"1","buckets":[{"date":"2027-05-01","cumRequired":"2"},{"date":"2027-04-01","cumRequired":"3"}]}`,
	}
	for name, body := range cases {
		t.Run(name, func(t *testing.T) {
			w := httptest.NewRecorder()
			a.checkRelease(w, planningRequest(body))
			if w.Code != http.StatusBadRequest && w.Code != http.StatusUnprocessableEntity {
				t.Fatalf("status %d, want 400 or 422", w.Code)
			}
		})
	}
}

// TestPreviewBackorder_RedistributeDeconfirms covers the preview contract of
// FR-27.17: priority class 1 is served first, the deconfirmed line is counted
// and warned about (SLS-0015), and nothing is committed.
func TestPreviewBackorder_RedistributeDeconfirms(t *testing.T) {
	a := New(nil)
	body := `{"strategy":"REDISTRIBUTE","available":"40","lines":[
		{"id":"b","priorityClass":2,"requestedDate":"2027-03-01","orderValue":"50","demand":"30","confirmed":"30"},
		{"id":"a","priorityClass":1,"requestedDate":"2027-03-02","orderValue":"100","demand":"20","confirmed":"0"}]}`
	w := httptest.NewRecorder()
	a.previewBackorder(w, planningRequest(body))
	if w.Code != http.StatusOK {
		t.Fatalf("status %d: %s", w.Code, w.Body.String())
	}
	var got struct {
		Preview          bool            `json:"preview"`
		Allocated        decimal.Decimal `json:"allocated"`
		DeconfirmedCount int             `json:"deconfirmedCount"`
		WarningCode      string          `json:"warningCode"`
		Lines            []struct {
			ID          string          `json:"id"`
			After       decimal.Decimal `json:"after"`
			Deconfirmed bool            `json:"deconfirmed"`
		} `json:"lines"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if !got.Preview || got.DeconfirmedCount != 1 || got.WarningCode != "SLS-0015" || !got.Allocated.Equal(decimal.NewFromInt(40)) {
		t.Fatalf("preview = %+v", got)
	}
	if len(got.Lines) != 2 || got.Lines[0].ID != "a" || got.Lines[0].After.String() != "20" || got.Lines[1].ID != "b" || !got.Lines[1].Deconfirmed {
		t.Fatalf("lines = %+v", got.Lines)
	}
}

func TestPreviewBackorder_RejectsUnknownStrategy(t *testing.T) {
	a := New(nil)
	body := `{"strategy":"HOPE","available":"1","lines":[{"id":"a","priorityClass":1,"requestedDate":"2027-03-01","orderValue":"1","demand":"1","confirmed":"0"}]}`
	w := httptest.NewRecorder()
	a.previewBackorder(w, planningRequest(body))
	if w.Code != http.StatusUnprocessableEntity {
		t.Fatalf("status %d, want 422", w.Code)
	}
}

func TestCheckRelease_BadBucketDateIsAFieldError(t *testing.T) {
	w := httptest.NewRecorder()
	New(nil).checkRelease(w, planningRequest(`{"cumReceived":"1","cumShipped":"1","buckets":[{"date":"15.04.2027","cumRequired":"2"}]}`))
	if w.Code != http.StatusBadRequest || !strings.Contains(w.Body.String(), `"buckets[0].date"`) {
		t.Fatalf("status %d: %s", w.Code, w.Body.String())
	}
}

// TestPreviewBackorder_WinRefusesShortSupply covers BR-27.11: WIN never reduces a
// confirmed quantity, so a run whose supply is below the confirmed total is refused.
func TestPreviewBackorder_WinRefusesShortSupply(t *testing.T) {
	body := `{"strategy":"WIN","available":"10","lines":[{"id":"a","priorityClass":1,"requestedDate":"2027-03-01","orderValue":"1","demand":"20","confirmed":"20"}]}`
	w := httptest.NewRecorder()
	New(nil).previewBackorder(w, planningRequest(body))
	if w.Code != http.StatusUnprocessableEntity || !strings.Contains(w.Body.String(), "SLS-0027") {
		t.Fatalf("status %d: %s", w.Code, w.Body.String())
	}
}
