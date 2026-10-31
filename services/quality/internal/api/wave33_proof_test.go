package api

import (
	"context"
	"encoding/json"
	"net/http"
	"os"
	"strings"
	"testing"

	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/platform/dbx"
	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/platform/testkit"
	"github.com/BenjaminSRussell/passage/services/quality/internal/domain"
	"github.com/BenjaminSRussell/passage/services/quality/internal/store"
	"github.com/BenjaminSRussell/passage/services/quality/migrations"
)

// Wave 33 proof (issue #33).

// triggerFixture is testdata/quality-triggers-100.json and triggerExpected its
// expectation, both written by testdata/triggers_reference.py (python3 -I),
// which applies BR-33.3 and the sizing rules itself instead of asking the service.
type triggerFixture struct {
	TestGroups   []string `json:"testGroups"`
	Associations []struct {
		TestGroup    string          `json:"testGroup"`
		Trigger      string          `json:"trigger"`
		ItemScope    string          `json:"itemScope"`
		ItemRef      *string         `json:"itemRef"`
		PartyScope   string          `json:"partyScope"`
		PartyRef     *string         `json:"partyRef"`
		SiteScope    string          `json:"siteScope"`
		SiteRef      *string         `json:"siteRef"`
		Sequence     int             `json:"sequence"`
		SamplingPlan json.RawMessage `json:"samplingPlan"`
	} `json:"associations"`
	Events []struct {
		Seq  int             `json:"seq"`
		Kind string          `json:"kind"`
		Key  string          `json:"key"`
		Body json.RawMessage `json:"body"`
	} `json:"events"`
}

type triggerExpected struct {
	Orders            int            `json:"orders"`
	Tally             map[string]int `json:"tally"`
	OrdersByTestGroup map[string]int `json:"ordersByTestGroup"`
	Events            []struct {
		Seq       int    `json:"seq"`
		Outcome   string `json:"outcome"`
		Code      string `json:"code"`
		TestGroup string `json:"testGroup"`
		SampleQty string `json:"sampleQty"`
		AcceptNo  int    `json:"acceptNo"`
		RejectNo  int    `json:"rejectNo"`
		ReplayOf  int    `json:"replayOf"`
	} `json:"events"`
}

func readJSON(t *testing.T, path string, v any) {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(raw, v); err != nil {
		t.Fatalf("%s: %v", path, err)
	}
}

// TestTriggerFixture_AC331 is AC1 / AC-33.1: the 60 purchase receipts and 40
// production RAFs of the fixture, posted in order against overlapping
// associations (ties broken by sequence, distractor associations on other
// triggers, scopes that must not match, re-posted lines and replayed keys),
// create exactly the 73 expected quality orders with the expected test groups
// and samples, and every other event is refused or replayed as expected.
func TestTriggerFixture_AC331(t *testing.T) {
	pool := testkit.DB(t)
	if err := dbx.Migrate(context.Background(), pool, migrations.FS); err != nil {
		t.Fatal(err)
	}
	h := newHandler(store.New(pool))

	var fx triggerFixture
	var want triggerExpected
	readJSON(t, "testdata/quality-triggers-100.json", &fx)
	readJSON(t, "testdata/quality-triggers-100.expected.json", &want)
	if len(fx.Events) != 100 || len(want.Events) != 100 || want.Orders != 73 {
		t.Fatalf("fixture shape: %d events, %d expectations, %d orders", len(fx.Events), len(want.Events), want.Orders)
	}
	receipts := 0
	for _, e := range fx.Events {
		if e.Kind == "receipt" {
			receipts++
		}
	}
	if receipts != 60 {
		t.Fatalf("fixture has %d receipts, want 60 (and 40 RAFs)", receipts)
	}

	groupID := map[string]string{}
	codeOf := map[string]string{}
	for _, code := range fx.TestGroups {
		body, _ := json.Marshal(map[string]any{"code": code, "tests": []map[string]any{{
			"testId": ids.New(), "name": "appearance", "kind": "ATTRIBUTE", "required": true, "passingOutcomeIds": []string{ids.New()},
		}}})
		rec := call(h, reqSpec{method: http.MethodPost, path: "/v1/quality/test-groups", body: string(body), duties: allDuties, key: ids.New()})
		if rec.Code != http.StatusCreated {
			t.Fatalf("test group %s: %d %s", code, rec.Code, rec.Body.String())
		}
		var g domain.TestGroup
		_ = json.Unmarshal(rec.Body.Bytes(), &g)
		groupID[code], codeOf[g.ID] = g.ID, code
	}
	for _, a := range fx.Associations {
		body := map[string]any{"trigger": a.Trigger, "itemScope": a.ItemScope, "partyScope": a.PartyScope, "siteScope": a.SiteScope,
			"sequence": a.Sequence, "testGroupId": groupID[a.TestGroup], "samplingPlan": a.SamplingPlan}
		for name, ref := range map[string]*string{"itemRef": a.ItemRef, "partyRef": a.PartyRef, "siteRef": a.SiteRef} {
			if ref != nil {
				body[name] = *ref
			}
		}
		raw, _ := json.Marshal(body)
		rec := call(h, reqSpec{method: http.MethodPost, path: "/v1/quality/associations", body: string(raw), duties: allDuties, key: ids.New()})
		if rec.Code != http.StatusCreated {
			t.Fatalf("association %s: %d %s", a.TestGroup, rec.Code, rec.Body.String())
		}
	}

	orderOfSeq := map[int]string{}
	tally := map[string]int{}
	for i, e := range fx.Events {
		w := want.Events[i]
		if w.Seq != e.Seq {
			t.Fatalf("expectation %d is for event %d", e.Seq, w.Seq)
		}
		rec := call(h, reqSpec{method: http.MethodPost, path: "/v1/quality/quality-orders", body: string(e.Body), duties: allDuties, key: e.Key})
		var got domain.Order
		_ = json.Unmarshal(rec.Body.Bytes(), &got)
		switch w.Outcome {
		case "created":
			if rec.Code != http.StatusCreated {
				t.Errorf("event %d: status %d %s, want 201 with %s", e.Seq, rec.Code, rec.Body.String(), w.TestGroup)
				continue
			}
			if codeOf[got.TestGroupID] != w.TestGroup || !got.SampleQty.Equal(decimal.RequireFromString(w.SampleQty)) ||
				got.AcceptNo != w.AcceptNo || got.RejectNo != w.RejectNo {
				t.Errorf("event %d: got %s n=%s %d/%d, want %s n=%s %d/%d", e.Seq, codeOf[got.TestGroupID], got.SampleQty, got.AcceptNo, got.RejectNo,
					w.TestGroup, w.SampleQty, w.AcceptNo, w.RejectNo)
			}
			orderOfSeq[e.Seq] = got.ID
		case "replay":
			if rec.Code != http.StatusOK || got.ID == "" || got.ID != orderOfSeq[w.ReplayOf] {
				t.Errorf("event %d: status %d order %s, want 200 replaying event %d's order %s", e.Seq, rec.Code, got.ID, w.ReplayOf, orderOfSeq[w.ReplayOf])
			}
		case "no_association":
			if rec.Code != http.StatusUnprocessableEntity || !strings.Contains(rec.Body.String(), w.Code) {
				t.Errorf("event %d: status %d %s, want 422 %s", e.Seq, rec.Code, rec.Body.String(), w.Code)
			}
		case "duplicate":
			if rec.Code != http.StatusConflict || !strings.Contains(rec.Body.String(), w.Code) {
				t.Errorf("event %d: status %d %s, want 409 %s", e.Seq, rec.Code, rec.Body.String(), w.Code)
			}
		default:
			t.Fatalf("event %d: unknown outcome %q", e.Seq, w.Outcome)
		}
		tally[w.Outcome]++
	}

	// What exists afterwards, read back from the database: exactly 73 orders,
	// one per created event, with the expected test group counts.
	rec := call(h, reqSpec{method: http.MethodGet, path: "/v1/quality/quality-orders?limit=500", duties: allDuties})
	var list struct {
		Items []domain.Order `json:"items"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &list); err != nil || rec.Code != http.StatusOK {
		t.Fatalf("list orders: %d %v", rec.Code, err)
	}
	if len(list.Items) != want.Orders || len(orderOfSeq) != want.Orders {
		t.Fatalf("orders in the database: %d (created responses %d), want %d", len(list.Items), len(orderOfSeq), want.Orders)
	}
	byGroup := map[string]int{}
	created := map[string]bool{}
	for _, id := range orderOfSeq {
		created[id] = true
	}
	for _, o := range list.Items {
		if !created[o.ID] {
			t.Errorf("order %s exists but no event created it", o.ID)
		}
		byGroup[codeOf[o.TestGroupID]]++
	}
	for code, n := range want.OrdersByTestGroup {
		if byGroup[code] != n {
			t.Errorf("test group %s: %d orders, want %d", code, byGroup[code], n)
		}
	}
	if len(byGroup) != len(want.OrdersByTestGroup) {
		t.Errorf("orders by test group %v, want %v", byGroup, want.OrdersByTestGroup)
	}
	for outcome, n := range want.Tally {
		if tally[outcome] != n {
			t.Errorf("%s: %d events, want %d", outcome, tally[outcome], n)
		}
	}
}

// TestSizeSample_Z14TablesOverHTTP sizes through the API at a special level and
// under tightened and reduced inspection (AC3); expected cells are read off
// Tables I, II-B and II-C by hand.
func TestSizeSample_Z14TablesOverHTTP(t *testing.T) {
	h := newHandler(newFake())
	for _, c := range []struct{ body, want string }{
		{`{"lotQty":"1201","method":"AQL","aql":"2.5","severity":"REDUCED"}`, `"codeLetter":"K","sampleQty":"50","acceptNo":3,"rejectNo":6`},
		{`{"lotQty":"1201","method":"AQL","aql":"2.5","severity":"TIGHTENED"}`, `"codeLetter":"K","sampleQty":"125","acceptNo":5,"rejectNo":6`},
		{`{"lotQty":"35001","method":"AQL","aql":"1.0","inspectionLevel":"S-4"}`, `"codeLetter":"J","sampleQty":"80","acceptNo":2,"rejectNo":3`},
		{`{"lotQty":"50","method":"AQL","aql":"2.5"}`, `"codeLetter":"D","sampleQty":"5","acceptNo":0,"rejectNo":1`},
	} {
		rec := call(h, reqSpec{method: http.MethodPost, path: "/v1/quality/sampling-plans:size", body: c.body, duties: "QM_VIEW"})
		if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), c.want) {
			t.Errorf("%s: %d %s, want %s", c.body, rec.Code, rec.Body.String(), c.want)
		}
	}
}
