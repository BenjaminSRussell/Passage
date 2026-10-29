package domain

import (
	"encoding/json"
	"encoding/xml"
	"slices"
	"strings"
	"time"

	"github.com/shopspring/decimal"
)

// Process case types (FR-46.17). UNLINKED holds documents that cannot be chained
// to a known case root; they are excluded from cycle-time measures (BR-46.16).
const (
	CaseP2P        = "P2P"
	CaseO2C        = "O2C"
	CaseProduction = "PRODUCTION"
	CaseUnlinked   = "UNLINKED"

	// MaxVariants is how many variants the explorer lists.
	MaxVariants = 20
)

// Document is one business document as seen by the event stream. ParentID is the
// document reference field the document was created from; empty for a root.
type Document struct {
	ID        string
	Type      string
	ParentID  string
	At        time.Time
	Actor     string
	Automated bool
}

// Event is one activity in a case.
type Event struct {
	DocID     string
	DocType   string
	Activity  string
	At        time.Time
	Actor     string
	Automated bool
}

// Case is the ordered history of one originating document.
type Case struct {
	ID     string
	Type   string
	Events []Event
}

// caseTypeFor names the case type of a root document type.
func caseTypeFor(docType string) (string, bool) {
	switch docType {
	case "REQUISITION", "PURCHASE_ORDER":
		return CaseP2P, true
	case "QUOTE", "SALES_ORDER":
		return CaseO2C, true
	case "PRODUCTION_ORDER":
		return CaseProduction, true
	}
	return "", false
}

// activityFor maps a document type to the activity it records.
func activityFor(docType string) (string, bool) {
	switch docType {
	case "REQUISITION":
		return "Create requisition", true
	case "PURCHASE_ORDER":
		return "Create purchase order", true
	case "RECEIPT":
		return "Receive goods", true
	case "VENDOR_INVOICE":
		return "Post vendor invoice", true
	case "PAYMENT":
		return "Pay invoice", true
	case "QUOTE":
		return "Create quote", true
	case "SALES_ORDER":
		return "Create sales order", true
	case "SHIPMENT":
		return "Ship goods", true
	case "CUSTOMER_INVOICE":
		return "Post customer invoice", true
	case "CASH_RECEIPT":
		return "Receive cash", true
	case "PRODUCTION_ORDER":
		return "Create production order", true
	case "RELEASE":
		return "Release production order", true
	case "RAF":
		return "Report as finished", true
	case "PRODUCTION_CLOSE":
		return "Close production order", true
	}
	return "", false
}

// BuildCases chains documents into cases by following ParentID to the root. The
// case id is the root's document id. A document whose reference is missing, whose
// chain cycles, whose root is not a known case root, or whose type has no
// activity lands alone in an UNLINKED case.
func BuildCases(docs []Document) []Case {
	byID := make(map[string]Document, len(docs))
	for _, d := range docs {
		byID[d.ID] = d
	}
	caseOf := func(d Document) (string, string) {
		cur := d
		for range len(docs) {
			if cur.ParentID == "" {
				break
			}
			parent, ok := byID[cur.ParentID]
			if !ok {
				return d.ID, CaseUnlinked
			}
			cur = parent
		}
		if cur.ParentID != "" {
			return d.ID, CaseUnlinked // reference cycle
		}
		typ, ok := caseTypeFor(cur.Type)
		if !ok {
			return d.ID, CaseUnlinked
		}
		return cur.ID, typ
	}

	groups := map[string]*Case{}
	for _, d := range docs {
		activity, known := activityFor(d.Type)
		root, typ := caseOf(d)
		if !known {
			root, typ = d.ID, CaseUnlinked
		}
		c, ok := groups[root]
		if !ok {
			c = &Case{ID: root, Type: typ}
			groups[root] = c
		}
		c.Events = append(c.Events, Event{
			DocID: d.ID, DocType: d.Type, Activity: activity,
			At: d.At, Actor: d.Actor, Automated: d.Automated,
		})
	}

	out := make([]Case, 0, len(groups))
	for _, c := range groups {
		slices.SortFunc(c.Events, func(a, b Event) int {
			if !a.At.Equal(b.At) {
				if a.At.Before(b.At) {
					return -1
				}
				return 1
			}
			return strings.Compare(a.DocID, b.DocID)
		})
		out = append(out, *c)
	}
	slices.SortFunc(out, func(a, b Case) int { return strings.Compare(a.ID, b.ID) })
	return out
}

// Variant is one distinct activity sequence among linked cases.
type Variant struct {
	Sequence      []string
	Count         int
	SharePct      decimal.Decimal
	MedianCycle   int64
	P90Cycle      int64
	AutomationPct decimal.Decimal
	Rework        int

	// StepMedian and StepP90 are seconds since the previous event, per position
	// (position 0 is 0).
	StepMedian []int64
	StepP90    []int64
}

// percentile is nearest-rank over sorted values. p is 1..100.
func percentile(sorted []int64, p int) int64 {
	if len(sorted) == 0 {
		return 0
	}
	rank := max((p*len(sorted)+99)/100, 1)
	return sorted[rank-1]
}

func sortedInt64(v []int64) []int64 {
	s := slices.Clone(v)
	slices.Sort(s)
	return s
}

// Variants groups linked cases by activity sequence and returns the top
// MaxVariants by count (ties by sequence). Cycle time runs from the first to the
// last event of a case. UNLINKED cases are excluded (BR-46.16).
func Variants(cases []Case) []Variant {
	groups := map[string][]Case{}
	total := 0
	for _, c := range cases {
		if c.Type == CaseUnlinked || len(c.Events) == 0 {
			continue
		}
		seq := make([]string, len(c.Events))
		for i, e := range c.Events {
			seq[i] = e.Activity
		}
		key := strings.Join(seq, " > ")
		groups[key] = append(groups[key], c)
		total++
	}
	keys := make([]string, 0, len(groups))
	for k := range groups {
		keys = append(keys, k)
	}
	variants := make([]Variant, 0, len(keys))
	for _, k := range keys {
		variants = append(variants, buildVariant(groups[k], total))
	}
	slices.SortFunc(variants, func(a, b Variant) int {
		if a.Count != b.Count {
			return b.Count - a.Count
		}
		return strings.Compare(strings.Join(a.Sequence, " > "), strings.Join(b.Sequence, " > "))
	})
	if len(variants) > MaxVariants {
		variants = variants[:MaxVariants]
	}
	return variants
}

func buildVariant(cases []Case, total int) Variant {
	steps := len(cases[0].Events)
	v := Variant{Count: len(cases)}
	for _, e := range cases[0].Events {
		v.Sequence = append(v.Sequence, e.Activity)
	}
	v.SharePct = decimal.NewFromInt(int64(len(cases))).Mul(decimal.NewFromInt(100)).Div(decimal.NewFromInt(int64(total))).RoundBank(2)

	var cycles []int64
	stepVals := make([][]int64, steps)
	automated := 0
	for _, c := range cases {
		first, last := c.Events[0].At, c.Events[len(c.Events)-1].At
		cycles = append(cycles, int64(last.Sub(first).Seconds()))
		seen := map[string]bool{}
		isAuto := true
		for i, e := range c.Events {
			if i > 0 {
				stepVals[i] = append(stepVals[i], int64(e.At.Sub(c.Events[i-1].At).Seconds()))
			} else {
				stepVals[0] = append(stepVals[0], 0)
			}
			if seen[e.Activity] {
				v.Rework++
			}
			seen[e.Activity] = true
			isAuto = isAuto && e.Automated
		}
		if isAuto {
			automated++
		}
	}
	sc := sortedInt64(cycles)
	v.MedianCycle, v.P90Cycle = percentile(sc, 50), percentile(sc, 90)
	for _, vals := range stepVals {
		s := sortedInt64(vals)
		v.StepMedian = append(v.StepMedian, percentile(s, 50))
		v.StepP90 = append(v.StepP90, percentile(s, 90))
	}
	v.AutomationPct = decimal.NewFromInt(int64(automated)).Mul(decimal.NewFromInt(100)).Div(decimal.NewFromInt(int64(len(cases)))).RoundBank(2)
	return v
}

// ocel is the OCEL 2.0 JSON document (objects, events and their relationships).
type ocel struct {
	ObjectTypes []ocelObjectType `json:"objectTypes"`
	EventTypes  []ocelEventType  `json:"eventTypes"`
	Objects     []ocelObject     `json:"objects"`
	Events      []ocelEvent      `json:"events"`
}

// ocelAttrType declares an attribute on an object or event type. OCEL 2.0
// requires every attribute an object or event carries to be declared on its type.
type ocelAttrType struct {
	Name string `json:"name"`
	Type string `json:"type"`
}

type ocelObjectType struct {
	Name       string         `json:"name"`
	Attributes []ocelAttrType `json:"attributes"`
}

type ocelEventType struct {
	Name       string         `json:"name"`
	Attributes []ocelAttrType `json:"attributes"`
}

type ocelAttr struct {
	Name  string `json:"name"`
	Time  string `json:"time,omitempty"`
	Value any    `json:"value"`
}

type ocelRel struct {
	ObjectID  string `json:"objectId"`
	Qualifier string `json:"qualifier"`
}

type ocelObject struct {
	ID            string     `json:"id"`
	Type          string     `json:"type"`
	Attributes    []ocelAttr `json:"attributes"`
	Relationships []ocelRel  `json:"relationships"`
}

type ocelEvent struct {
	ID            string     `json:"id"`
	Type          string     `json:"type"`
	Time          string     `json:"time"`
	Attributes    []ocelAttr `json:"attributes"`
	Relationships []ocelRel  `json:"relationships"`
}

// ExportOCEL renders cases as an OCEL 2.0 JSON log. Case objects are "case" typed,
// document objects are "document" typed, and each event relates to its document
// and its case. Output is deterministic: cases sort by id.
func ExportOCEL(cases []Case) ([]byte, error) {
	doc := ocel{
		ObjectTypes: []ocelObjectType{
			{Name: "case", Attributes: []ocelAttrType{{Name: "caseType", Type: "string"}}},
			{Name: "document", Attributes: []ocelAttrType{{Name: "docType", Type: "string"}}},
		},
		EventTypes: []ocelEventType{},
		Objects:    []ocelObject{},
		Events:     []ocelEvent{},
	}
	activities := map[string]bool{}
	for _, c := range cases {
		if len(c.Events) == 0 {
			continue
		}
		caseObj := ocelObject{Type: "case", Relationships: []ocelRel{}}
		caseObj.ID = "case:" + c.ID
		caseObj.Attributes = []ocelAttr{{Name: "caseType", Time: stamp(c.Events[0].At), Value: c.Type}}
		doc.Objects = append(doc.Objects, caseObj)
		for _, e := range c.Events {
			activities[e.Activity] = true
			docObj := ocelObject{Type: "document", Relationships: []ocelRel{}}
			docObj.ID = e.DocID
			docObj.Attributes = []ocelAttr{{Name: "docType", Time: stamp(e.At), Value: e.DocType}}
			doc.Objects = append(doc.Objects, docObj)

			ev := ocelEvent{Type: e.Activity, Time: stamp(e.At)}
			ev.ID = "ev:" + e.DocID
			ev.Attributes = []ocelAttr{{Name: "actor", Value: e.Actor}, {Name: "automated", Value: e.Automated}}
			ev.Relationships = []ocelRel{{ObjectID: e.DocID, Qualifier: "document"}, {ObjectID: "case:" + c.ID, Qualifier: "case"}}
			doc.Events = append(doc.Events, ev)
		}
	}
	names := make([]string, 0, len(activities))
	for a := range activities {
		names = append(names, a)
	}
	slices.Sort(names)
	for _, n := range names {
		doc.EventTypes = append(doc.EventTypes, ocelEventType{Name: n, Attributes: []ocelAttrType{
			{Name: "actor", Type: "string"}, {Name: "automated", Type: "boolean"},
		}})
	}
	return json.MarshalIndent(doc, "", "  ")
}

func stamp(t time.Time) string { return t.UTC().Format(time.RFC3339Nano) }

// xesLog is an IEEE XES 2.0 log using the standard concept, time and org extensions.
type xesLog struct {
	XMLName    xml.Name       `xml:"log"`
	Version    string         `xml:"xes.version,attr"`
	Features   string         `xml:"xes.features,attr"`
	Extensions []xesExtension `xml:"extension"`
	Traces     []xesTrace     `xml:"trace"`
}

type xesExtension struct {
	Name   string `xml:"name,attr"`
	Prefix string `xml:"prefix,attr"`
	URI    string `xml:"uri,attr"`
}

type xesTrace struct {
	Strings []xesString `xml:"string"`
	Events  []xesEvent  `xml:"event"`
}

type xesEvent struct {
	Strings []xesString `xml:"string"`
	Dates   []xesDate   `xml:"date"`
}

type xesString struct {
	Key   string `xml:"key,attr"`
	Value string `xml:"value,attr"`
}

type xesDate struct {
	Key   string `xml:"key,attr"`
	Value string `xml:"value,attr"`
}

// ExportXES renders cases as an XES log: one trace per case with concept:name,
// and one event per activity with concept:name, time:timestamp and org:resource.
func ExportXES(cases []Case) ([]byte, error) {
	log := xesLog{Version: "2.0"}
	log.Extensions = []xesExtension{
		{Name: "Concept", Prefix: "concept", URI: "http://www.xes-standard.org/concept.xesext"},
		{Name: "Time", Prefix: "time", URI: "http://www.xes-standard.org/time.xesext"},
		{Name: "Organizational", Prefix: "org", URI: "http://www.xes-standard.org/org.xesext"},
	}
	for _, c := range cases {
		tr := xesTrace{Strings: []xesString{{Key: "concept:name", Value: c.ID}, {Key: "case:type", Value: c.Type}}}
		for _, e := range c.Events {
			tr.Events = append(tr.Events, xesEvent{
				Strings: []xesString{{Key: "concept:name", Value: e.Activity}, {Key: "org:resource", Value: e.Actor}},
				Dates:   []xesDate{{Key: "time:timestamp", Value: stamp(e.At)}},
			})
		}
		log.Traces = append(log.Traces, tr)
	}
	body, err := xml.MarshalIndent(log, "", "  ")
	if err != nil {
		return nil, err
	}
	return append([]byte(xml.Header), body...), nil
}
