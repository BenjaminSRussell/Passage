package api

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/BenjaminSRussell/passage/platform/ids"

	"github.com/BenjaminSRussell/passage/services/edi/internal/store"
)

func bulkBody(t *testing.T, list []string) string {
	t.Helper()
	b, err := json.Marshal(bulkReprocessRequest{InterchangeIDs: list})
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func TestPayloadAndBulkRoutes_RequireOperateDuty(t *testing.T) {
	for _, tc := range []struct{ method, path, body string }{
		{http.MethodGet, "/edi/v1/interchanges/" + someID + "/payload", ""},
		{http.MethodPost, "/edi/v1/interchanges:reprocess?dryRun=true", `{"interchangeIds":["` + someID + `"]}`},
	} {
		// EDI_PII_VIEW only unmasks; it does not open the monitor on its own.
		if rec := call(t, tc.method, tc.path, tc.body, "EDI_PII_VIEW,EDI_ADMIN", nil); rec.Code != http.StatusForbidden {
			t.Errorf("%s %s: %d, want 403", tc.method, tc.path, rec.Code)
		}
	}
}

func TestGetPayload_MalformedIDIsNotFound(t *testing.T) {
	// The nil store proves the id is checked before any lookup.
	rec := call(t, http.MethodGet, "/edi/v1/interchanges/not-an-id/payload", "", "EDI_OPERATE", nil)
	if rec.Code != http.StatusNotFound || problemCode(t, rec) != CodeNotFound {
		t.Fatalf("status %d: %s", rec.Code, rec.Body.String())
	}
}

func TestBulkReprocess_RejectsBeforeTheStore(t *testing.T) {
	many := make([]string, 0, store.MaxBulkReprocess+1)
	for i := 0; i <= store.MaxBulkReprocess; i++ {
		many = append(many, ids.New())
	}
	one := ids.New()
	cases := []struct {
		name, path, body string
		headers          map[string]string
		code, field      string
	}{
		{"empty list", "?dryRun=true", bulkBody(t, []string{}), nil, CodeValidation, "interchangeIds"},
		{"no list", "?dryRun=true", `{}`, nil, CodeValidation, "interchangeIds"},
		{"201 ids", "?dryRun=true", bulkBody(t, many), nil, CodeValidation, "interchangeIds"},
		{"repeated id", "?dryRun=true", bulkBody(t, []string{one, one}), nil, CodeValidation, "interchangeIds[1]"},
		{"malformed id", "?dryRun=true", bulkBody(t, []string{one, "nope"}), nil, CodeValidation, "interchangeIds[1]"},
		{"bad dryRun", "?dryRun=yes", bulkBody(t, []string{one}), nil, CodeValidation, "dryRun"},
		{"no key on a real run", "", bulkBody(t, []string{one}), nil, CodeIdempotencyKey, ""},
		{"key too long", "", bulkBody(t, []string{one}), map[string]string{"Idempotency-Key": strings.Repeat("k", 256)}, CodeIdempotencyKey, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rec := call(t, http.MethodPost, "/edi/v1/interchanges:reprocess"+tc.path, tc.body, "EDI_OPERATE", tc.headers)
			if rec.Code != http.StatusBadRequest || problemCode(t, rec) != tc.code {
				t.Fatalf("status %d: %s", rec.Code, rec.Body.String())
			}
			if tc.field != "" && !strings.Contains(rec.Body.String(), `"`+tc.field+`"`) {
				t.Fatalf("no error on %s: %s", tc.field, rec.Body.String())
			}
		})
	}
}

func TestValidateBulkIDs_AcceptsTheCap(t *testing.T) {
	list := make([]string, 0, store.MaxBulkReprocess)
	for i := 0; i < store.MaxBulkReprocess; i++ {
		list = append(list, ids.New())
	}
	if fields := validateBulkIDs(list); len(fields) != 0 {
		t.Fatalf("200 distinct ids must pass: %v", fields)
	}
	if msg := validateBulkIDs(nil)["interchangeIds"]; !strings.Contains(msg, "200") {
		t.Fatalf("the cap must be stated: %q", msg)
	}
}

func TestBulkReprocessHash_DependsOnIdsAndOrder(t *testing.T) {
	a, b := ids.New(), ids.New()
	if bulkReprocessHash([]string{a, b}) != bulkReprocessHash([]string{a, b}) {
		t.Fatal("same request must hash the same")
	}
	if bulkReprocessHash([]string{a, b}) == bulkReprocessHash([]string{b, a}) {
		t.Fatal("order is part of the request (items come back in request order)")
	}
	if bulkReprocessHash([]string{a}) == bulkReprocessHash([]string{a, b}) {
		t.Fatal("a different list must not replay")
	}
}
