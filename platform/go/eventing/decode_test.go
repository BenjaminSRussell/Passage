package eventing_test

import (
	"errors"
	"testing"

	"github.com/BenjaminSRussell/passage/platform/eventing"
)

func TestDecodeBinaryMode(t *testing.T) {
	r := eventing.Record{
		Value: []byte(`{"receipt_id":"r1"}`),
		Headers: map[string]string{
			"ce_specversion": "1.0", "ce_id": "e1", "ce_type": eventType, "ce_source": "/passage/receiving", "ce_tenantid": tenantA,
			"ce_correlationid": "c1", "ce_time": "2026-10-31T10:00:00Z", "tenantid": tenantA,
		},
	}
	evt, err := eventing.Decode(r)
	if err != nil {
		t.Fatal(err)
	}
	if evt.ID != "e1" || evt.Type != eventType || evt.TenantID != tenantA || evt.CorrelationID != "c1" || string(evt.Data) != `{"receipt_id":"r1"}` || evt.Time.IsZero() {
		t.Fatalf("event = %+v", evt)
	}
}

func TestDecodeRefusesMalformedEvents(t *testing.T) {
	good := record(t, 1, "e1", eventType, tenantA)
	cases := map[string]func(*eventing.Record){
		"bad json": func(r *eventing.Record) { r.Value = []byte("[") },
		"no data": func(r *eventing.Record) {
			r.Value = []byte(`{"specversion":"1.0","id":"e1","type":"` + eventType + `","tenantid":"` + tenantA + `"}`)
		},
		"tenant header off": func(r *eventing.Record) { r.Headers["tenantid"] = "22222222-2222-4222-8222-222222222222" },
		"binary no id": func(r *eventing.Record) {
			r.Headers = map[string]string{"ce_specversion": "1.0", "ce_type": eventType, "ce_tenantid": tenantA}
			r.Value = []byte(`{}`)
		},
	}
	for name, mutate := range cases {
		r := good
		r.Headers = map[string]string{"tenantid": tenantA}
		mutate(&r)
		if _, err := eventing.Decode(r); !errors.Is(err, eventing.ErrMalformedEvent) {
			t.Errorf("%s: err = %v, want ErrMalformedEvent", name, err)
		}
	}
	if _, err := eventing.Decode(good); err != nil {
		t.Fatalf("structured event refused: %v", err)
	}
}
