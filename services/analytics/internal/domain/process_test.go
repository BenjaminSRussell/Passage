package domain

import (
	"encoding/json"
	"encoding/xml"
	"fmt"
	"testing"
	"time"

	"pgregory.net/rapid"
)

var t0 = time.Date(2026, 10, 1, 8, 0, 0, 0, time.UTC)

func p2pChain(prefix string, start time.Time, automated bool) []Document {
	return []Document{
		{ID: prefix + "-req", Type: "REQUISITION", At: start, Actor: "alice", Automated: automated},
		{ID: prefix + "-po", Type: "PURCHASE_ORDER", ParentID: prefix + "-req", At: start.Add(time.Hour), Actor: "buyer", Automated: automated},
		{ID: prefix + "-gr", Type: "RECEIPT", ParentID: prefix + "-po", At: start.Add(3 * time.Hour), Actor: "wh", Automated: automated},
		{ID: prefix + "-inv", Type: "VENDOR_INVOICE", ParentID: prefix + "-po", At: start.Add(5 * time.Hour), Actor: "ap", Automated: automated},
		{ID: prefix + "-pay", Type: "PAYMENT", ParentID: prefix + "-inv", At: start.Add(9 * time.Hour), Actor: "treasury", Automated: automated},
	}
}

func TestBuildCases_ChainsByReference(t *testing.T) {
	docs := p2pChain("a", t0, false)
	cases := BuildCases(docs)
	if len(cases) != 1 {
		t.Fatalf("cases = %d, want 1", len(cases))
	}
	c := cases[0]
	if c.ID != "a-req" || c.Type != CaseP2P {
		t.Fatalf("case = %s/%s", c.ID, c.Type)
	}
	if len(c.Events) != 5 || c.Events[0].Activity != "Create requisition" || c.Events[4].Activity != "Pay invoice" {
		t.Fatalf("events out of order: %+v", c.Events)
	}
}

func TestBuildCases_UnlinkedRules(t *testing.T) {
	cases := BuildCases([]Document{
		{ID: "orphan", Type: "VENDOR_INVOICE", ParentID: "missing", At: t0},
		{ID: "cyc1", Type: "RECEIPT", ParentID: "cyc2", At: t0},
		{ID: "cyc2", Type: "RECEIPT", ParentID: "cyc1", At: t0},
		{ID: "badroot", Type: "RECEIPT", At: t0},
		{ID: "mystery", Type: "GIFT_CARD", At: t0},
		{ID: "q1", Type: "QUOTE", At: t0},
		{ID: "o1", Type: "SALES_ORDER", ParentID: "q1", At: t0.Add(time.Hour)},
	})
	types := map[string]string{}
	for _, c := range cases {
		types[c.ID] = c.Type
	}
	want := map[string]string{
		"orphan": CaseUnlinked, "cyc1": CaseUnlinked, "cyc2": CaseUnlinked,
		"badroot": CaseUnlinked, "mystery": CaseUnlinked, "q1": CaseO2C,
	}
	for id, typ := range want {
		if types[id] != typ {
			t.Errorf("%s type = %q, want %q", id, types[id], typ)
		}
	}
	if _, ok := types["o1"]; ok {
		t.Error("child o1 must join the q1 case, not stand alone")
	}
}

func TestVariants_RankingReworkAndExclusion(t *testing.T) {
	var docs []Document
	// Three straight-through P2P cases, one with a rework receipt, one unlinked.
	docs = append(docs, p2pChain("s1", t0, true)...)
	docs = append(docs, p2pChain("s2", t0, true)...)
	docs = append(docs, p2pChain("s3", t0, true)...)
	docs = append(docs, Document{ID: "rw-req", Type: "REQUISITION", At: t0, Actor: "u"})
	docs = append(docs, Document{ID: "rw-po", Type: "PURCHASE_ORDER", ParentID: "rw-req", At: t0.Add(time.Hour), Actor: "b"})
	docs = append(docs, Document{ID: "rw-gr1", Type: "RECEIPT", ParentID: "rw-po", At: t0.Add(2 * time.Hour), Actor: "w"})
	docs = append(docs, Document{ID: "rw-gr2", Type: "RECEIPT", ParentID: "rw-po", At: t0.Add(3 * time.Hour), Actor: "w"})
	docs = append(docs, Document{ID: "x", Type: "VENDOR_INVOICE", ParentID: "nope", At: t0})

	vs := Variants(BuildCases(docs))
	if len(vs) != 2 {
		t.Fatalf("variants = %d, want 2", len(vs))
	}
	top := vs[0]
	if top.Count != 3 {
		t.Fatalf("top count = %d, want 3", top.Count)
	}
	if top.SharePct.String() != "75" {
		t.Fatalf("share = %s, want 75 (3 of 4 linked cases)", top.SharePct)
	}
	if top.AutomationPct.String() != "100" {
		t.Fatalf("automation = %s, want 100", top.AutomationPct)
	}
	if top.MedianCycle != 9*3600 || top.P90Cycle != 9*3600 {
		t.Fatalf("cycle median/p90 = %d/%d", top.MedianCycle, top.P90Cycle)
	}
	if top.StepMedian[0] != 0 || top.StepMedian[1] != 3600 {
		t.Fatalf("step medians = %v", top.StepMedian)
	}
	rework := vs[1]
	if rework.Rework != 1 || rework.AutomationPct.String() != "0" {
		t.Fatalf("rework variant = %+v", rework)
	}
}

func TestVariants_TopTwentyCap(t *testing.T) {
	var docs []Document
	for i := range 25 {
		// Each case has a distinct length, so each is its own variant.
		id := fmt.Sprintf("c%02d", i)
		docs = append(docs, Document{ID: id, Type: "REQUISITION", At: t0})
		parent := id
		for j := 0; j < i%5; j++ {
			child := fmt.Sprintf("%s-%d", id, j)
			docs = append(docs, Document{ID: child, Type: "PURCHASE_ORDER", ParentID: parent, At: t0.Add(time.Duration(j+1) * time.Hour)})
			parent = child
		}
		for j := 0; j < i; j++ {
			docs = append(docs, Document{ID: fmt.Sprintf("%s-x%d", id, j), Type: "RECEIPT", ParentID: id, At: t0.Add(time.Duration(j+10) * time.Hour)})
		}
	}
	if got := len(Variants(BuildCases(docs))); got != MaxVariants {
		t.Fatalf("variants = %d, want cap %d", got, MaxVariants)
	}
}

func TestExportOCEL_WellFormed(t *testing.T) {
	cases := BuildCases(p2pChain("e", t0, false))
	raw, err := ExportOCEL(cases)
	if err != nil {
		t.Fatal(err)
	}
	type declared struct {
		Name       string `json:"name"`
		Attributes []struct {
			Name string `json:"name"`
		} `json:"attributes"`
	}
	type carried struct {
		Name string `json:"name"`
	}
	var doc struct {
		ObjectTypes []declared `json:"objectTypes"`
		EventTypes  []declared `json:"eventTypes"`
		Objects     []struct {
			ID         string    `json:"id"`
			Type       string    `json:"type"`
			Attributes []carried `json:"attributes"`
		} `json:"objects"`
		Events []struct {
			ID            string    `json:"id"`
			Type          string    `json:"type"`
			Time          string    `json:"time"`
			Attributes    []carried `json:"attributes"`
			Relationships []struct {
				ObjectID  string `json:"objectId"`
				Qualifier string `json:"qualifier"`
			} `json:"relationships"`
		} `json:"events"`
	}
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatal(err)
	}
	if len(doc.Events) != 5 || len(doc.EventTypes) != 5 || len(doc.ObjectTypes) != 2 {
		t.Fatalf("events=%d eventTypes=%d objectTypes=%d", len(doc.Events), len(doc.EventTypes), len(doc.ObjectTypes))
	}
	// 5 documents plus 1 case object.
	if len(doc.Objects) != 6 {
		t.Fatalf("objects = %d, want 6", len(doc.Objects))
	}
	// OCEL 2.0: every attribute an object or event carries is declared on its type.
	declaredOn := func(types []declared) map[string]map[string]bool {
		out := map[string]map[string]bool{}
		for _, ty := range types {
			out[ty.Name] = map[string]bool{}
			for _, a := range ty.Attributes {
				out[ty.Name][a.Name] = true
			}
		}
		return out
	}
	objectAttrs, eventAttrs := declaredOn(doc.ObjectTypes), declaredOn(doc.EventTypes)
	for _, o := range doc.Objects {
		for _, a := range o.Attributes {
			if !objectAttrs[o.Type][a.Name] {
				t.Errorf("object %s carries undeclared attribute %s", o.ID, a.Name)
			}
		}
	}
	for _, e := range doc.Events {
		if len(e.Relationships) != 2 || e.Time == "" {
			t.Fatalf("event %s missing relationships or time", e.ID)
		}
		for _, a := range e.Attributes {
			if !eventAttrs[e.Type][a.Name] {
				t.Errorf("event %s carries undeclared attribute %s", e.ID, a.Name)
			}
		}
	}
}

func TestExportXES_RoundTrip(t *testing.T) {
	cases := BuildCases(p2pChain("x", t0, true))
	raw, err := ExportXES(cases)
	if err != nil {
		t.Fatal(err)
	}
	var log struct {
		Version string `xml:"xes.version,attr"`
		Traces  []struct {
			Events []struct {
				Strings []struct {
					Key   string `xml:"key,attr"`
					Value string `xml:"value,attr"`
				} `xml:"string"`
			} `xml:"event"`
		} `xml:"trace"`
	}
	if err := xml.Unmarshal(raw, &log); err != nil {
		t.Fatal(err)
	}
	if log.Version != "2.0" || len(log.Traces) != 1 || len(log.Traces[0].Events) != 5 {
		t.Fatalf("log = %+v", log)
	}
}

func TestPercentileNearestRank(t *testing.T) {
	v := []int64{10, 20, 30, 40}
	if percentile(v, 50) != 20 || percentile(v, 90) != 40 || percentile(nil, 50) != 0 {
		t.Fatal("nearest-rank percentile wrong")
	}
}

func TestBuildCases_PropertyEveryDocumentOnce(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		n := rapid.IntRange(1, 40).Draw(t, "n")
		types := []string{"REQUISITION", "PURCHASE_ORDER", "RECEIPT", "VENDOR_INVOICE", "GIFT"}
		docs := make([]Document, n)
		for i := range docs {
			docs[i] = Document{
				ID:   fmt.Sprintf("d%d", i),
				Type: rapid.SampledFrom(types).Draw(t, "type"),
				At:   t0.Add(time.Duration(i) * time.Minute),
			}
			if i > 0 && rapid.Bool().Draw(t, "hasParent") {
				docs[i].ParentID = fmt.Sprintf("d%d", rapid.IntRange(0, i-1).Draw(t, "parent"))
			}
		}
		seen := map[string]int{}
		total := 0
		for _, c := range BuildCases(docs) {
			for _, e := range c.Events {
				seen[e.DocID]++
				total++
			}
		}
		if total != n {
			t.Fatalf("events = %d, want %d", total, n)
		}
		for id, k := range seen {
			if k != 1 {
				t.Fatalf("document %s appears %d times", id, k)
			}
		}
	})
}
