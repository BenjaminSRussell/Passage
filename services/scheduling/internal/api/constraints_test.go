package api

import (
	"fmt"
	"net/http"
	"strings"
	"testing"
)

const netID = "00000000-0000-0000-0000-0000000000c1"

func operationBody(extra string) string {
	return `{"orderId":"00000000-0000-0000-0000-0000000000d1","operNumber":20,"minutes":60,"resourceIds":["` + resID + `"]` + extra + `}`
}

// The slice 3 fields reach the store normalized: group trimmed, network id
// lower-cased, route setup and attributes as sent.
func TestCreateOperation_PassesSetupAttributesAndNetwork(t *testing.T) {
	f := &fakeStore{}
	body := operationBody(`,"setupGroup":" WHITE ","setupMinutes":25,"attributes":{"color":"white"},"networkId":"` + strings.ToUpper(netID) + `"`)
	rec := do(newRouter(f), http.MethodPost, "/v1/scheduling/operations", body, allDuties, "op-1")
	if rec.Code != http.StatusCreated {
		t.Fatalf("status %d body %s", rec.Code, rec.Body)
	}
	got := f.lastOp
	if got.SetupGroup == nil || *got.SetupGroup != "WHITE" || got.SetupMinutes != 25 || got.Attributes["color"] != "white" || got.NetworkID == nil || *got.NetworkID != netID {
		t.Fatalf("store got %+v", got)
	}
	rec = do(newRouter(f), http.MethodPost, "/v1/scheduling/operations", operationBody(""), allDuties, "op-2")
	if rec.Code != http.StatusCreated || f.lastOp.SetupGroup != nil || f.lastOp.NetworkID != nil || f.lastOp.SetupMinutes != 0 {
		t.Fatalf("without the new fields: status %d store got %+v", rec.Code, f.lastOp)
	}
}

func TestCreateOperation_ValidatesSetupAttributesAndNetwork(t *testing.T) {
	many := make([]string, 0, 21)
	for i := range 21 {
		many = append(many, fmt.Sprintf(`"a%d":"x"`, i))
	}
	cases := []struct{ name, extra, field string }{
		{"blank setup group", `,"setupGroup":"  "`, `"setupGroup"`},
		{"setup group over 32 characters", `,"setupGroup":"` + strings.Repeat("G", 33) + `"`, `"setupGroup"`},
		{"negative route setup", `,"setupMinutes":-1`, `"setupMinutes"`},
		{"route setup over a week", `,"setupMinutes":10081`, `"setupMinutes"`},
		{"too many attributes", `,"attributes":{` + strings.Join(many, ",") + `}`, `"attributes"`},
		{"attribute name with spaces", `,"attributes":{" color":"white"}`, `"attributes"`},
		{"attribute value too long", `,"attributes":{"color":"` + strings.Repeat("w", 121) + `"}`, `"attributes"`},
		{"network id not a UUID", `,"networkId":"MIX-FILL"`, `"networkId"`},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			f := &fakeStore{}
			rec := do(newRouter(f), http.MethodPost, "/v1/scheduling/operations", operationBody(c.extra), allDuties, "op-x")
			if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), c.field) {
				t.Fatalf("status %d body %s, want 400 naming %s", rec.Code, rec.Body, c.field)
			}
			if f.lastOp.OrderID != "" {
				t.Fatal("an invalid request reached the store")
			}
		})
	}
}

// A network whose maximum wait is shorter than its transfer can never be met,
// so it is refused when created.
func TestCreateNetwork_RefusesMaxWaitShorterThanTransfer(t *testing.T) {
	body := `{"code":"MIX-FILL","name":"Mixing","activeFrom":"2027-01-01","edges":[{"fromResourceId":"` + resID +
		`","toResourceId":"00000000-0000-0000-0000-0000000000b2","transferMinutes":30,"maxWaitMinutes":20}]}`
	rec := do(newRouter(&fakeStore{}), http.MethodPost, "/v1/scheduling/networks", body, allDuties, "n-1")
	if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), "transfer") {
		t.Fatalf("status %d body %s", rec.Code, rec.Body)
	}
}
