package api

import (
	"encoding/json"
	"net/http"
	"os"
	"testing"

	"github.com/BenjaminSRussell/passage/platform/ids"
)

// A DELIVERED report names the batches and serials it took (#480); the
// entries reach the store in order and count in the request hash.
func TestRecordProgress_Tracking(t *testing.T) {
	id := ids.New()
	path := orders + "/" + id + ":record-progress"
	body := `{"kind":"DELIVERED","sourceRef":"PS-T","lines":[{"lineNo":1,"quantity":"3","tracking":[{"batch":" B1 ","quantity":"2"},{"batch":"B2","serial":"SN-1","quantity":"1"}]}]}`
	s := &progressStore{created: true}
	rec := serveBody(t, s, http.MethodPost, path, body, principal(dutyRecordDelivery), map[string]string{idempotencyKey: "k1"})
	if rec.Code != http.StatusCreated || len(s.got.Lines) != 1 || len(s.got.Lines[0].Tracking) != 2 {
		t.Fatalf("record: %d %s %+v", rec.Code, rec.Body, s.got)
	}
	tr := s.got.Lines[0].Tracking
	if tr[0].Batch != "B1" || tr[0].Serial != "" || tr[0].Quantity.String() != "2" || tr[1].Batch != "B2" || tr[1].Serial != "SN-1" {
		t.Fatalf("tracking %+v", tr)
	}
	tracked := s.got.RequestHash
	plain := `{"kind":"DELIVERED","sourceRef":"PS-T","lines":[{"lineNo":1,"quantity":"3"}]}`
	serveBody(t, s, http.MethodPost, path, plain, principal(dutyRecordDelivery), map[string]string{idempotencyKey: "k2"})
	if s.got.RequestHash == tracked {
		t.Fatal("the request hash ignores tracking")
	}
	weighed := `{"kind":"DELIVERED","sourceRef":"PS-W","lines":[{"lineNo":1,"quantity":"5","actualWeight":"51","tracking":[{"batch":"B1","quantity":"2","actualWeight":"20.5"},{"batch":"B2","quantity":"3","actualWeight":"30.5"}]}]}`
	if rec := serveBody(t, s, http.MethodPost, path, weighed, principal(dutyRecordDelivery), map[string]string{idempotencyKey: "k3"}); rec.Code != http.StatusCreated ||
		s.got.Lines[0].Tracking[1].ActualWeight == nil || s.got.Lines[0].Tracking[1].ActualWeight.String() != "30.5" {
		t.Fatalf("weighed entries: %d %s %+v", rec.Code, rec.Body, s.got.Lines)
	}
}

// Fulfillment's packing slip report (#496) is accepted as fulfillment sends it:
// the shared fixture, which fulfillment's body test builds member for member,
// names the lines by id and carries serials, batches and catch weights.
func TestRecordProgress_FulfillmentReportContract(t *testing.T) {
	raw, err := os.ReadFile("../../../fulfillment/testdata/sales-progress/record-progress-delivered.json")
	if err != nil {
		t.Fatal(err)
	}
	path := orders + "/" + ids.New() + ":record-progress"
	s := &progressStore{created: true}
	rec := serveBody(t, s, http.MethodPost, path, string(raw), principal(dutyRecordDelivery), map[string]string{idempotencyKey: "fulfillment-packing-slip:x"})
	if rec.Code != http.StatusCreated || s.got.SourceRef != "PS-0000042" || len(s.got.Lines) != 4 {
		t.Fatalf("record: %d %s %+v", rec.Code, rec.Body, s.got)
	}
	l := s.got.Lines
	if l[0].LineID != "5a1c0000-0000-4000-8000-000000000001" || l[0].LineNo != 0 || len(l[0].Tracking) != 3 || l[0].Tracking[2].Batch != "LOT-S" || l[0].Tracking[2].Serial != "SN-3" ||
		len(l[1].Tracking) != 2 || l[1].Tracking[0].Quantity.String() != "8" ||
		l[2].ActualWeight == nil || l[2].ActualWeight.String() != "51.5" || l[2].Tracking[0].ActualWeight == nil ||
		l[3].Quantity.String() != "6.5" || l[3].Tracking != nil {
		t.Fatalf("lines %+v", l)
	}
}

func TestRecordProgress_TrackingRules(t *testing.T) {
	path := orders + "/" + ids.New() + ":record-progress"
	line := func(qty, actual, tracking string) string {
		a := ""
		if actual != "" {
			a = `,"actualWeight":"` + actual + `"`
		}
		return `{"kind":"DELIVERED","sourceRef":"PS-R","lines":[{"lineNo":1,"quantity":"` + qty + `"` + a + `,"tracking":` + tracking + `}]}`
	}
	for name, tc := range map[string]struct{ body, field string }{
		"invoice report":         {`{"kind":"INVOICED","sourceRef":"INV-1","lines":[{"lineNo":1,"quantity":"1","tracking":[{"batch":"B1","quantity":"1"}]}]}`, "lines[0].tracking"},
		"no batch or serial":     {line("1", "", `[{"quantity":"1"}]`), "lines[0].tracking[0]"},
		"serial not one":         {line("2", "", `[{"serial":"SN-1","quantity":"2"}]`), "lines[0].tracking[0].quantity"},
		"sum differs":            {line("3", "", `[{"batch":"B1","quantity":"2"}]`), "lines[0].tracking"},
		"same batch twice":       {line("2", "", `[{"batch":"B1","quantity":"1"},{"batch":"B1","quantity":"1"}]`), "lines[0].tracking[1]"},
		"same serial twice":      {line("2", "", `[{"batch":"B1","serial":"S","quantity":"1"},{"batch":"B2","serial":"S","quantity":"1"}]`), "lines[0].tracking[1]"},
		"batch too long":         {line("1", "", `[{"batch":"0123456789012345678901234567890123456789012345678901234567890","quantity":"1"}]`), "lines[0].tracking[0]"},
		"some entries weighed":   {line("2", "20", `[{"batch":"B1","quantity":"1","actualWeight":"10"},{"batch":"B2","quantity":"1"}]`), "lines[0].tracking"},
		"weights not the line's": {line("2", "21", `[{"batch":"B1","quantity":"1","actualWeight":"10"},{"batch":"B2","quantity":"1","actualWeight":"10"}]`), "lines[0].actualWeight"},
	} {
		t.Run(name, func(t *testing.T) {
			s := &progressStore{created: true}
			duty := dutyRecordDelivery
			if name == "invoice report" {
				duty = dutyRecordInvoice
			}
			rec := serveBody(t, s, http.MethodPost, path, tc.body, principal(duty), map[string]string{idempotencyKey: "k"})
			var p struct {
				Errors map[string]string `json:"errors"`
			}
			if rec.Code != http.StatusBadRequest || json.Unmarshal(rec.Body.Bytes(), &p) != nil || p.Errors[tc.field] == "" || s.got.OrderID != "" {
				t.Fatalf("got %d %s, want 400 on %s", rec.Code, rec.Body, tc.field)
			}
		})
	}
}
