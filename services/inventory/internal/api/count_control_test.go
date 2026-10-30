package api_test

import (
	"encoding/json"
	"net/http"
	"testing"

	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/services/inventory/internal/domain"
)

func TestCountSheetTransitionsNeedDutyAndIfMatch(t *testing.T) {
	fs := &fakeStore{sheet: blindSheet()}
	h := newServer(t, fs)
	base := "/v1/inventory/count-sheets/" + fs.sheet.ID
	cases := []struct {
		name, path, duty string
		want             string
	}{
		{"submit", base + ":submit", "inventory.count.write", domain.CountCounted},
		{"approve", base + ":approve", "inventory.count.approve", domain.CountApproved},
		{"cancel", base + ":cancel", "inventory.count.write", domain.CountCancelled},
	}
	for _, c := range cases {
		if rec := do(t, h, http.MethodPost, c.path, "", []string{"inventory.read"}, map[string]string{"If-Match": "2"}); rec.Code != http.StatusForbidden {
			t.Fatalf("%s without duty: %d", c.name, rec.Code)
		}
		if rec := do(t, h, http.MethodPost, c.path, "", []string{c.duty}, nil); rec.Code != http.StatusPreconditionRequired {
			t.Fatalf("%s without If-Match: %d", c.name, rec.Code)
		}
		fs.sheet = blindSheet()
		rec := do(t, h, http.MethodPost, c.path, "", []string{c.duty}, map[string]string{"If-Match": `"2"`})
		if rec.Code != http.StatusOK || fs.countVersion != 2 {
			t.Fatalf("%s: status %d version %d", c.name, rec.Code, fs.countVersion)
		}
		if got := decodeSheetStatus(t, rec.Body.Bytes()); got != c.want {
			t.Fatalf("%s: status %s, want %s", c.name, got, c.want)
		}
	}
	fs.err = httpx.NewProblem(http.StatusForbidden, domain.CodeCountFourEyes, "counted by the approver")
	rec := do(t, h, http.MethodPost, base+":approve", "", []string{"inventory.count.approve"}, map[string]string{"If-Match": "2"})
	if rec.Code != http.StatusForbidden || decodeProblem(t, rec).Code != domain.CodeCountFourEyes {
		t.Fatalf("four-eyes refusal: %d %s", rec.Code, rec.Body.String())
	}
}

func TestRecountNeedsAReasonAndApproveDuty(t *testing.T) {
	fs := &fakeStore{sheet: blindSheet()}
	h := newServer(t, fs)
	path := "/v1/inventory/count-sheets/" + fs.sheet.ID + "/lines/" + fs.sheet.Lines[0].ID + ":recount"
	hdr := map[string]string{"If-Match": "2"}
	if rec := do(t, h, http.MethodPost, path, `{"reason":"x"}`, []string{"inventory.count.write"}, hdr); rec.Code != http.StatusForbidden {
		t.Fatalf("counter must not request a recount: %d", rec.Code)
	}
	if rec := do(t, h, http.MethodPost, path, `{"reason":"  "}`, []string{"inventory.count.approve"}, hdr); rec.Code != http.StatusBadRequest {
		t.Fatalf("blank reason: %d", rec.Code)
	}
	rec := do(t, h, http.MethodPost, path, `{"reason":" miscounted bay 4 "}`, []string{"inventory.count.approve"}, hdr)
	if rec.Code != http.StatusOK || fs.recountReason != "miscounted bay 4" {
		t.Fatalf("status %d reason %q", rec.Code, fs.recountReason)
	}
}

func TestCountPlanSelectionIsValidated(t *testing.T) {
	h := newServer(t, &fakeStore{})
	body := func(extra string) string {
		return `{"legalEntityId":"` + "0b0e7a52-1a4c-4f6a-9d0e-6a3f6a1c1e11" + `","name":"Aisle count","currency":"EUR","unit":"PCS"` + extra + `}`
	}
	hdr := map[string]string{"Idempotency-Key": "k-1"}
	for name, extra := range map[string]string{
		"class":     `,"abcClass":"Z"`,
		"range":     `,"locationFrom":"B-9","locationTo":"A-1"`,
		"sample":    `,"samplePct":"101"`,
		"sample0":   `,"samplePct":"-1"`,
		"samplenum": `,"samplePct":"abc"`,
	} {
		rec := do(t, h, http.MethodPost, "/v1/inventory/count-plans", body(extra), []string{"inventory.count.write"}, hdr)
		if rec.Code != http.StatusBadRequest {
			t.Fatalf("%s: %d %s", name, rec.Code, rec.Body.String())
		}
	}
	rec := do(t, h, http.MethodPost, "/v1/inventory/count-plans", body(`,"abcClass":"A","locationFrom":"A-1","locationTo":"A-9","samplePct":"25","freezeStock":true,"fourEyes":true`),
		[]string{"inventory.count.write"}, hdr)
	if rec.Code != http.StatusCreated && rec.Code != http.StatusOK {
		t.Fatalf("valid plan: %d %s", rec.Code, rec.Body.String())
	}
}

func decodeSheetStatus(t *testing.T, body []byte) string {
	t.Helper()
	var s struct {
		Status string `json:"status"`
	}
	if err := json.Unmarshal(body, &s); err != nil {
		t.Fatalf("decode sheet: %v %s", err, body)
	}
	return s.Status
}

// A COUNTED or APPROVED blind sheet can still go back to OPEN for a recount,
// so the counter must not see the book quantity until the sheet is final.
func TestBlindSheetStaysBlindUntilFinal(t *testing.T) {
	fs := &fakeStore{sheet: blindSheet()}
	h := newServer(t, fs)
	path := "/v1/inventory/count-sheets/" + fs.sheet.ID
	for _, status := range []string{domain.CountCounted, domain.CountApproved} {
		fs.sheet.Status = status
		if e := expectedOf(t, do(t, h, http.MethodGet, path, "", []string{"inventory.read"}, nil).Body.Bytes()); e != nil {
			t.Fatalf("counter saw expected %s on a %s blind sheet", *e, status)
		}
	}
	fs.sheet.Status = domain.CountCancelled
	if e := expectedOf(t, do(t, h, http.MethodGet, path, "", []string{"inventory.read"}, nil).Body.Bytes()); e == nil {
		t.Fatal("a cancelled sheet is final; expected quantities are shown")
	}
}
