package api

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/BenjaminSRussell/passage/platform/ids"
)

func catchWeightOrderBody(quantity, spec string) string {
	line := map[string]any{"productId": ids.New(), "quantity": quantity, "unit": "CS", "siteId": ids.New(), "warehouseId": ids.New(), "unitPrice": "2", "requestedShipDate": "2027-03-04"}
	if spec != "" {
		var parsed any
		if err := json.Unmarshal([]byte(spec), &parsed); err != nil {
			panic(err)
		}
		line["catchWeight"] = parsed
	}
	raw, _ := json.Marshal(map[string]any{"legalEntityId": testLE, "customerId": ids.New(), "invoiceAccountId": ids.New(), "currency": "USD", "requestedShipDate": "2027-03-04",
		"lines": []any{line}})
	return string(raw)
}

const goodSpec = `{"nominal":"10","min":"9.8","max":"11"}`

func TestCreateOrder_CatchWeightLine(t *testing.T) {
	st := &configStore{}
	rec := serveWith(New(st), http.MethodPost, orders, catchWeightOrderBody("10", goodSpec), principal(dutyWrite))
	if rec.Code != http.StatusCreated || len(st.created) != 1 {
		t.Fatalf("valid catch-weight line: %d %s", rec.Code, rec.Body)
	}
	line := st.created[0].Lines[0]
	if line.LineNet.String() != "200" || line.CatchWeight == nil {
		t.Fatalf("10 cases at nominal 10 and 2.00 per weight must be 200: %+v", line)
	}
}

func TestCreateOrder_CatchWeightValidation(t *testing.T) {
	tests := []struct{ name, qty, spec, field string }{
		{"fractional cases", "2.5", goodSpec, "lines[0].quantity"},
		{"zero spec", "10", `{"nominal":"0","min":"0","max":"0"}`, "lines[0].catchWeight"},
		{"min above max", "10", `{"nominal":"10","min":"12","max":"11"}`, "lines[0].catchWeight"},
		{"nominal outside band", "10", `{"nominal":"12","min":"9.8","max":"11"}`, "lines[0].catchWeight"},
		{"seven decimals", "10", `{"nominal":"10","min":"9.8000001","max":"11"}`, "lines[0].catchWeight"},
		{"oversized", "10", `{"nominal":"10","min":"9.8","max":"100000000000000000"}`, "lines[0].catchWeight"},
		{"missing max", "10", `{"nominal":"10","min":"9.8"}`, "lines[0].catchWeight.max"},
		{"not a number", "10", `{"nominal":"x","min":"9.8","max":"11"}`, "lines[0].catchWeight.nominal"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			st := &configStore{}
			rec := serveWith(New(st), http.MethodPost, orders, catchWeightOrderBody(tc.qty, tc.spec), principal(dutyWrite))
			if rec.Code != http.StatusBadRequest || len(st.created) != 0 || !strings.Contains(rec.Body.String(), tc.field) {
				t.Fatalf("got %d %s, want 400 naming %s", rec.Code, rec.Body, tc.field)
			}
		})
	}
}

func TestRecordProgress_ActualWeightIsValidatedAndHashed(t *testing.T) {
	id := ids.New()
	path := orders + "/" + id + ":record-progress"
	h := map[string]string{idempotencyKey: "k1"}
	report := func(weight string) string {
		return `{"kind":"DELIVERED","sourceRef":"PS-1","lines":[{"lineNo":1,"quantity":"5","actualWeight":` + weight + `}]}`
	}
	s := &progressStore{created: true}
	if rec := serveBody(t, s, http.MethodPost, path, report(`"51"`), principal(dutyRecordDelivery), h); rec.Code != http.StatusCreated ||
		s.got.Lines[0].ActualWeight == nil || s.got.Lines[0].ActualWeight.String() != "51" {
		t.Fatalf("valid weight: %d %s %+v", rec.Code, rec.Body, s.got)
	}
	heavy := s.got.RequestHash
	serveBody(t, s, http.MethodPost, path, report(`"52"`), principal(dutyRecordDelivery), h)
	if s.got.RequestHash == heavy {
		t.Fatal("another actual weight must hash differently")
	}
	for name, bad := range map[string]string{
		"zero":       `"0"`,
		"negative":   `"-1"`,
		"seven dp":   `"51.0000001"`,
		"oversized":  `"100000000000000000"`,
		"not number": `"x"`,
	} {
		if rec := serveBody(t, s, http.MethodPost, path, report(bad), principal(dutyRecordDelivery), h); rec.Code != http.StatusBadRequest ||
			!strings.Contains(rec.Body.String(), "lines[0].actualWeight") {
			t.Errorf("%s: %d %s", name, rec.Code, rec.Body)
		}
	}
}
