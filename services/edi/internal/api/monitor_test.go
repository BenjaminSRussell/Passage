package api

import (
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/BenjaminSRussell/passage/services/edi/internal/domain"
	"github.com/BenjaminSRussell/passage/services/edi/internal/store"
)

const someID = "0195f0a0-0000-7000-8000-0000000000aa"

func TestMonitorRoutes_RequireOperateDuty(t *testing.T) {
	for _, tc := range []struct{ method, path string }{
		{http.MethodGet, "/edi/v1/messages"},
		{http.MethodGet, "/edi/v1/messages/" + someID},
		{http.MethodPost, "/edi/v1/interchanges/" + someID + ":reprocess?dryRun=true"},
		{http.MethodGet, "/edi/v1/partners/sla"},
	} {
		// edi.admin alone does not open the monitor (PRD §6.35.17).
		if rec := call(t, tc.method, tc.path, "", "edi.admin", nil); rec.Code != http.StatusForbidden {
			t.Errorf("%s %s with edi.admin: %d, want 403", tc.method, tc.path, rec.Code)
		}
	}
}

func TestListMessages_RejectsBadFilters(t *testing.T) {
	cases := map[string]string{
		"status":         "status=DONE",
		"transactionSet": "transactionSet=85x",
		"direction":      "direction=SIDEWAYS",
		"partnerId":      "partnerId=nope",
		"controlNumber":  "controlNumber=1234567890",
		"limit":          "limit=201",
		"cursor":         "cursor=not-a-cursor",
	}
	for field, query := range cases {
		t.Run(field, func(t *testing.T) {
			rec := call(t, http.MethodGet, "/edi/v1/messages?"+query, "", "edi.operate", nil)
			if rec.Code != http.StatusBadRequest || problemCode(t, rec) != CodeValidation {
				t.Fatalf("status %d: %s", rec.Code, rec.Body.String())
			}
			if !strings.Contains(rec.Body.String(), `"`+field+`"`) {
				t.Fatalf("no error on %s: %s", field, rec.Body.String())
			}
		})
	}
}

func TestGetMessage_MalformedIDIsNotFound(t *testing.T) {
	rec := call(t, http.MethodGet, "/edi/v1/messages/not-an-id", "", "edi.operate", nil)
	if rec.Code != http.StatusNotFound || problemCode(t, rec) != CodeNotFound {
		t.Fatalf("status %d: %s", rec.Code, rec.Body.String())
	}
}

func TestCursor_RoundTripAndTampering(t *testing.T) {
	in := store.Cursor{At: time.Date(2026, 10, 21, 12, 0, 0, 123456000, time.UTC), ID: someID}
	out, err := decodeCursor(encodeCursor(in))
	if err != nil || !out.At.Equal(in.At) || out.ID != in.ID {
		t.Fatalf("round trip: %+v %v", out, err)
	}
	for _, bad := range []string{"", "!!", encodeCursor(store.Cursor{At: in.At, ID: "x"}), "MjAyNg"} {
		if _, err := decodeCursor(bad); err == nil {
			t.Errorf("cursor %q must be rejected", bad)
		}
	}
}

func TestReprocess_ValidatesBeforeStore(t *testing.T) {
	path := "/edi/v1/interchanges/" + someID + ":reprocess"
	rec := call(t, http.MethodPost, path, "", "edi.operate", nil)
	if rec.Code != http.StatusBadRequest || problemCode(t, rec) != CodeIdempotencyKey {
		t.Fatalf("no key: %d %s", rec.Code, rec.Body.String())
	}
	rec = call(t, http.MethodPost, path+"?dryRun=maybe", "", "edi.operate", nil)
	if rec.Code != http.StatusBadRequest || problemCode(t, rec) != CodeValidation {
		t.Fatalf("bad dryRun: %d %s", rec.Code, rec.Body.String())
	}
	rec = call(t, http.MethodPost, "/edi/v1/interchanges/not-an-id:reprocess", "", "edi.operate", map[string]string{"Idempotency-Key": "k"})
	if rec.Code != http.StatusNotFound || problemCode(t, rec) != CodeNotFound {
		t.Fatalf("bad id: %d %s", rec.Code, rec.Body.String())
	}
}

func TestReprocessPlan(t *testing.T) {
	good := sample850(t, "1")
	failed := func(set, control string) store.Message {
		return store.Message{ID: "m-" + set + "-" + control, TransactionSet: set, STControl: control, Status: "FAILED", Error: "old"}
	}

	t.Run("a transaction that maps now becomes MAPPED with its canonical order", func(t *testing.T) {
		plan, err := reprocessPlan(good, []store.Message{failed("850", "0001")})
		if err != nil {
			t.Fatal(err)
		}
		if len(plan) != 1 || plan[0].ID != "m-850-0001" || plan[0].Status != "MAPPED" || plan[0].Error != "" ||
			!strings.Contains(string(plan[0].Canonical), "PO1234") {
			t.Fatalf("plan = %+v", plan)
		}
	})
	t.Run("a transaction that still breaks a rule stays FAILED with the rule", func(t *testing.T) {
		plan, err := reprocessPlan(sample850(t, "2"), []store.Message{failed("850", "0001")})
		if err != nil {
			t.Fatal(err)
		}
		if plan[0].Status != "FAILED" || !strings.Contains(plan[0].Error, domain.CodeCountMismatch) {
			t.Fatalf("plan = %+v", plan)
		}
	})
	t.Run("a transaction missing from the payload stays FAILED with a reason", func(t *testing.T) {
		plan, err := reprocessPlan(good, []store.Message{failed("850", "0009")})
		if err != nil {
			t.Fatal(err)
		}
		if plan[0].Status != "FAILED" || !strings.Contains(plan[0].Error, "not in the stored interchange") {
			t.Fatalf("plan = %+v", plan)
		}
	})
	t.Run("an ST02 used in two groups is not guessed", func(t *testing.T) {
		ic, err := domain.Parse(good)
		if err != nil {
			t.Fatal(err)
		}
		second := ic.Groups[0]
		second.Control = "2"
		ic.Groups = append(ic.Groups, second)
		raw, err := domain.Serialize(ic)
		if err != nil {
			t.Fatal(err)
		}
		plan, err := reprocessPlan(raw, []store.Message{failed("850", "0001")})
		if err != nil {
			t.Fatal(err)
		}
		if plan[0].Status != "FAILED" || !strings.Contains(plan[0].Error, "more than one group") {
			t.Fatalf("plan = %+v", plan)
		}
	})
	t.Run("a stored payload that no longer parses is reported, not guessed", func(t *testing.T) {
		if _, err := reprocessPlan("ISA*garbage~", []store.Message{failed("850", "0001")}); err == nil || !strings.Contains(err.Error(), errStoredPayload.Error()) {
			t.Fatalf("err = %v", err)
		}
	})
}

func TestPartnerSLA_ValidatesWindow(t *testing.T) {
	for _, q := range []string{"days=0", "days=366", "days=ten"} {
		rec := call(t, http.MethodGet, "/edi/v1/partners/sla?"+q, "", "edi.operate", nil)
		if rec.Code != http.StatusBadRequest || problemCode(t, rec) != CodeValidation {
			t.Fatalf("%s: %d %s", q, rec.Code, rec.Body.String())
		}
	}
}

func TestToSLARow_RatesAndLatency(t *testing.T) {
	p95 := 5430.0 // 90.5 minutes
	row := toSLARow(store.PartnerSLA{Messages: 3, Failed: 1, P95AckSeconds: &p95})
	if row.FailureRatePct != 33.33 || row.P95AckMinutes == nil || *row.P95AckMinutes != 90.5 {
		t.Fatalf("row = %+v (p95 %v)", row, row.P95AckMinutes)
	}
	row = toSLARow(store.PartnerSLA{Messages: 3, Failed: 2})
	if row.FailureRatePct != 66.67 || row.P95AckMinutes != nil {
		t.Fatalf("row = %+v", row)
	}
	if row := toSLARow(store.PartnerSLA{}); row.FailureRatePct != 0 {
		t.Fatalf("no traffic must be 0%%, got %v", row.FailureRatePct)
	}
}
