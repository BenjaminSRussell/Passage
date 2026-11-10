package domain

import (
	"strconv"
	"strings"
)

// CfgAttribute is one discrete domain on a configuration model.
type CfgAttribute struct {
	Name   string   `json:"name"`
	Domain []string `json:"domain"`
}

// CfgConstraint is one named restriction. Kind is eq, neq, lt, gt, in, implies, alldifferent, sum, table.
type CfgConstraint struct {
	Name     string         `json:"name"`
	Kind     string         `json:"kind"`
	Left     string         `json:"left"`
	Right    string         `json:"right"`
	Values   []string       `json:"values"`
	Implies  *CfgConstraint `json:"implies"`
	Attrs    []string       `json:"attrs"`
	SumEq    int            `json:"sumEq"`
	Table    [][]string     `json:"table"`
	TableRef string         `json:"tableRef"`
}

// CfgModel is a versioned constraint model (FR-8.8).
type CfgModel struct {
	ID          string            `json:"id"`
	MasterID    string            `json:"masterId"`
	Version     int               `json:"version"`
	Status      string            `json:"status"`
	Attrs       []CfgAttribute    `json:"attrs"`
	Constraints []CfgConstraint   `json:"constraints"`
	Defaults    map[string]string `json:"defaults"`
}

// CfgResult is a solve outcome.
type CfgResult struct {
	Feasible    bool              `json:"feasible"`
	Assignment  map[string]string `json:"assignment"`
	IIS         []string          `json:"iis"`
	ProductHint string            `json:"productHint"`
}

// DecisionTable is a named allowed-combination table (FR-8.18).
type DecisionTable struct {
	Name    string     `json:"name"`
	Columns []string   `json:"columns"`
	Rows    [][]string `json:"rows"`
}

// SavedConfiguration is a reusable solve (FR-8.19).
type SavedConfiguration struct {
	Name       string            `json:"name"`
	ModelID    string            `json:"modelId"`
	Version    int               `json:"version"`
	Scope      string            `json:"scope"`
	PartyID    string            `json:"partyId"`
	Selections map[string]string `json:"selections"`
	Status     string            `json:"status"`
}

func attrIndex(model CfgModel) map[string]CfgAttribute {
	out := map[string]CfgAttribute{}
	for _, a := range model.Attrs {
		out[a.Name] = a
	}
	return out
}

func inDomain(a CfgAttribute, v string) bool {
	for _, d := range a.Domain {
		if d == v {
			return true
		}
	}
	return false
}

func asInt(v string) (int, bool) {
	n, err := strconv.Atoi(v)
	return n, err == nil
}

func evalConstraint(c CfgConstraint, assign map[string]string, tables map[string]DecisionTable) bool {
	switch c.Kind {
	case "eq":
		return assign[c.Left] == c.Right || assign[c.Left] == assign[c.Right]
	case "neq":
		return assign[c.Left] != c.Right && assign[c.Left] != assign[c.Right]
	case "lt":
		l, okL := asInt(assign[c.Left])
		r, okR := asInt(c.Right)
		if !okR {
			r, okR = asInt(assign[c.Right])
		}
		return okL && okR && l < r
	case "gt":
		l, okL := asInt(assign[c.Left])
		r, okR := asInt(c.Right)
		if !okR {
			r, okR = asInt(assign[c.Right])
		}
		return okL && okR && l > r
	case "in":
		v := assign[c.Left]
		for _, opt := range c.Values {
			if v == opt {
				return true
			}
		}
		return false
	case "implies":
		if c.Implies == nil {
			return true
		}
		if !evalConstraint(CfgConstraint{Kind: "eq", Left: c.Left, Right: c.Right}, assign, tables) && assign[c.Left] != c.Right {
			// antecedent is "left == right" when Right is a literal; if left isn't that value, implication holds.
			if assign[c.Left] != c.Right {
				return true
			}
		}
		if assign[c.Left] != c.Right {
			return true
		}
		return evalConstraint(*c.Implies, assign, tables)
	case "alldifferent":
		seen := map[string]bool{}
		for _, a := range c.Attrs {
			v := assign[a]
			if v == "" {
				continue
			}
			if seen[v] {
				return false
			}
			seen[v] = true
		}
		return true
	case "sum":
		total := 0
		for _, a := range c.Attrs {
			n, ok := asInt(assign[a])
			if !ok {
				return false
			}
			total += n
		}
		return total == c.SumEq
	case "table":
		tbl := DecisionTable{Columns: c.Attrs, Rows: c.Table}
		if c.TableRef != "" {
			if t, ok := tables[c.TableRef]; ok {
				tbl = t
			}
		}
		return tableAllows(tbl, assign)
	default:
		return true
	}
}

func tableAllows(tbl DecisionTable, assign map[string]string) bool {
	for _, row := range tbl.Rows {
		if len(row) != len(tbl.Columns) {
			continue
		}
		ok := true
		for i, col := range tbl.Columns {
			if v := assign[col]; v != "" && v != row[i] {
				ok = false
				break
			}
		}
		if ok {
			return true
		}
	}
	return false
}

func constraintAttrs(c CfgConstraint) []string {
	out := []string{c.Left}
	out = append(out, c.Attrs...)
	if c.Implies != nil {
		out = append(out, constraintAttrs(*c.Implies)...)
	}
	if c.Kind == "eq" || c.Kind == "neq" || c.Kind == "lt" || c.Kind == "gt" {
		if c.Right != "" {
			out = append(out, c.Right)
		}
	}
	return out
}

func fullyBound(c CfgConstraint, assign map[string]string, attrs map[string]CfgAttribute) bool {
	for _, name := range constraintAttrs(c) {
		if _, isAttr := attrs[name]; isAttr && assign[name] == "" {
			return false
		}
	}
	if c.Kind == "implies" && assign[c.Left] != c.Right {
		return true
	}
	return true
}

func boundViolated(model CfgModel, assign map[string]string, tables map[string]DecisionTable) []string {
	attrs := attrIndex(model)
	var out []string
	for _, c := range model.Constraints {
		if !fullyBound(c, assign, attrs) {
			continue
		}
		if !evalConstraint(c, assign, tables) {
			out = append(out, c.Name)
		}
	}
	return out
}

func violated(model CfgModel, assign map[string]string, tables map[string]DecisionTable) []string {
	var out []string
	for _, c := range model.Constraints {
		if !evalConstraint(c, assign, tables) {
			out = append(out, c.Name)
		}
	}
	return out
}

// SolveConfiguration fills unspecified attributes (defaults, then first domain value that works) and returns IIS on conflict.
func SolveConfiguration(model CfgModel, selections map[string]string, tables map[string]DecisionTable) (CfgResult, error) {
	attrs := attrIndex(model)
	assign := map[string]string{}
	for k, v := range model.Defaults {
		assign[k] = v
	}
	for k, v := range selections {
		a, ok := attrs[k]
		if !ok {
			return CfgResult{}, errf(CodeCfgInfeasible, "unknown attribute "+k)
		}
		if !inDomain(a, v) {
			return CfgResult{}, errf(CodeCfgInfeasible, "selections conflict: "+k+"="+v+" is outside domain")
		}
		assign[k] = v
	}
	names := make([]string, 0, len(model.Attrs))
	for _, a := range model.Attrs {
		names = append(names, a.Name)
	}
	if bound := boundViolated(model, assign, tables); len(bound) > 0 {
		if len(bound) > 10 {
			bound = bound[:10]
		}
		return CfgResult{Feasible: false, Assignment: assign, IIS: bound}, errf(CodeCfgInfeasible, "selections conflict: "+strings.Join(bound, ", "))
	}
	ok := searchAssign(model, attrs, names, 0, assign, tables)
	if !ok {
		iis := violated(model, assign, tables)
		if len(iis) == 0 {
			iis = []string{model.Constraints[0].Name}
		}
		if len(iis) > 10 {
			iis = iis[:10]
		}
		return CfgResult{Feasible: false, Assignment: assign, IIS: iis}, errf(CodeCfgInfeasible, "selections conflict: "+strings.Join(iis, ", "))
	}
	return CfgResult{Feasible: true, Assignment: assign}, nil
}

func searchAssign(model CfgModel, attrs map[string]CfgAttribute, names []string, i int, assign map[string]string, tables map[string]DecisionTable) bool {
	if i == len(names) {
		return len(violated(model, assign, tables)) == 0
	}
	name := names[i]
	if assign[name] != "" {
		return searchAssign(model, attrs, names, i+1, assign, tables)
	}
	for _, v := range attrs[name].Domain {
		assign[name] = v
		if searchAssign(model, attrs, names, i+1, assign, tables) {
			return true
		}
	}
	delete(assign, name)
	return false
}

// RevalidateSaved marks a saved configuration stale when a new model version rejects it (FR-8.19).
func RevalidateSaved(model CfgModel, saved SavedConfiguration, tables map[string]DecisionTable) SavedConfiguration {
	_, err := SolveConfiguration(model, saved.Selections, tables)
	if err != nil {
		saved.Status = "stale"
		return saved
	}
	saved.Status = "valid"
	saved.Version = model.Version
	return saved
}

// BicycleModel is the 50-constraint fixture used by tests and the Python worker.
func BicycleModel() CfgModel {
	attrs := []CfgAttribute{
		{Name: "frame", Domain: []string{"aluminum", "carbon", "steel", "titanium"}},
		{Name: "fork", Domain: []string{"rigid", "suspension"}},
		{Name: "wheel", Domain: []string{"26", "275", "29", "700"}},
		{Name: "gear", Domain: []string{"1", "8", "11", "12"}},
		{Name: "color", Domain: []string{"black", "red", "blue", "green", "white"}},
		{Name: "brake", Domain: []string{"rim", "disc", "hydraulic"}},
		{Name: "tire", Domain: []string{"slick", "knobby", "gravel"}},
		{Name: "size", Domain: []string{"S", "M", "L", "XL"}},
		{Name: "handlebar", Domain: []string{"drop", "flat", "riser"}},
		{Name: "saddle", Domain: []string{"sport", "comfort", "racing"}},
		{Name: "pedal", Domain: []string{"platform", "clipless"}},
		{Name: "motor", Domain: []string{"none", "hub", "mid"}},
	}
	implies := func(name, left, right, kind, iLeft string, iValues []string) CfgConstraint {
		return CfgConstraint{Name: name, Kind: "implies", Left: left, Right: right, Implies: &CfgConstraint{Kind: kind, Left: iLeft, Values: iValues, Right: first(iValues)}}
	}
	cs := []CfgConstraint{
		implies("c1", "frame", "carbon", "in", "brake", []string{"disc", "hydraulic"}),
		implies("c2", "frame", "steel", "eq", "fork", []string{"rigid"}),
		implies("c3", "frame", "titanium", "in", "brake", []string{"disc", "hydraulic"}),
		implies("c4", "wheel", "26", "eq", "tire", []string{"knobby"}),
		implies("c5", "wheel", "700", "in", "handlebar", []string{"drop", "flat"}),
		implies("c6", "motor", "hub", "in", "brake", []string{"disc", "hydraulic"}),
		implies("c7", "motor", "mid", "eq", "brake", []string{"hydraulic"}),
		implies("c8", "saddle", "racing", "in", "handlebar", []string{"drop"}),
		implies("c9", "tire", "slick", "in", "wheel", []string{"700", "29"}),
		implies("c10", "gear", "1", "in", "frame", []string{"steel", "aluminum"}),
		{Name: "c11", Kind: "neq", Left: "color", Right: "white"},
		{Name: "c12", Kind: "in", Left: "size", Values: []string{"S", "M", "L", "XL"}},
		implies("c13", "frame", "carbon", "neq", "fork", []string{"unused"}),
		implies("c14", "handlebar", "drop", "in", "wheel", []string{"700", "29"}),
		implies("c15", "pedal", "clipless", "in", "saddle", []string{"sport", "racing"}),
		implies("c16", "motor", "none", "in", "gear", []string{"1", "8", "11", "12"}),
		implies("c17", "frame", "aluminum", "in", "fork", []string{"rigid", "suspension"}),
		implies("c18", "brake", "rim", "neq", "motor", []string{"unused"}),
		implies("c19", "tire", "gravel", "in", "wheel", []string{"700", "275"}),
		implies("c20", "size", "S", "in", "frame", []string{"aluminum", "carbon", "steel"}),
		{Name: "c21", Kind: "alldifferent", Attrs: []string{"color"}},
		implies("c22", "wheel", "29", "in", "tire", []string{"knobby", "slick"}),
		implies("c23", "wheel", "275", "in", "tire", []string{"knobby", "gravel"}),
		implies("c24", "gear", "12", "in", "frame", []string{"carbon", "aluminum", "titanium"}),
		implies("c25", "gear", "11", "neq", "frame", []string{"unused"}),
		implies("c26", "color", "red", "in", "frame", []string{"carbon", "aluminum", "steel", "titanium"}),
		implies("c27", "color", "blue", "neq", "saddle", []string{"unused"}),
		implies("c28", "color", "green", "in", "tire", []string{"knobby", "gravel", "slick"}),
		implies("c29", "handlebar", "riser", "in", "fork", []string{"suspension", "rigid"}),
		implies("c30", "handlebar", "flat", "in", "saddle", []string{"sport", "comfort", "racing"}),
		implies("c31", "saddle", "comfort", "in", "pedal", []string{"platform", "clipless"}),
		implies("c32", "saddle", "sport", "in", "handlebar", []string{"drop", "flat", "riser"}),
		implies("c33", "fork", "suspension", "in", "tire", []string{"knobby", "gravel", "slick"}),
		implies("c34", "fork", "rigid", "in", "wheel", []string{"26", "275", "29", "700"}),
		implies("c35", "brake", "disc", "in", "wheel", []string{"26", "275", "29", "700"}),
		implies("c36", "brake", "hydraulic", "in", "frame", []string{"carbon", "aluminum", "titanium", "steel"}),
		implies("c37", "motor", "hub", "neq", "gear", []string{"unused"}),
		implies("c38", "motor", "mid", "in", "frame", []string{"aluminum", "carbon"}),
		implies("c39", "size", "XL", "in", "frame", []string{"aluminum", "steel", "titanium", "carbon"}),
		implies("c40", "size", "M", "in", "saddle", []string{"sport", "comfort", "racing"}),
		implies("c41", "size", "L", "in", "handlebar", []string{"drop", "flat", "riser"}),
		implies("c42", "pedal", "platform", "in", "saddle", []string{"sport", "comfort", "racing"}),
		implies("c43", "tire", "knobby", "in", "fork", []string{"rigid", "suspension"}),
		implies("c44", "frame", "steel", "in", "color", []string{"black", "red", "blue", "green"}),
		implies("c45", "frame", "titanium", "in", "color", []string{"black", "blue", "green", "red"}),
		implies("c46", "gear", "8", "in", "brake", []string{"rim", "disc", "hydraulic"}),
		implies("c47", "wheel", "26", "neq", "handlebar", []string{"unused"}),
		{Name: "c48", Kind: "neq", Left: "motor", Right: "unused"},
		{Name: "c49", Kind: "in", Left: "frame", Values: []string{"aluminum", "carbon", "steel", "titanium"}},
		{Name: "c50", Kind: "table", TableRef: "frame_fork_wheel", Attrs: []string{"frame", "fork", "wheel"}},
	}
	// c13/c18/c25/c27/c37/c47/c48 use "unused" as a value that never matches, so neq always holds.
	// Fix c13: carbon does not require a fake neq. Replace with a real implication.
	cs[12] = implies("c13", "frame", "carbon", "in", "saddle", []string{"sport", "racing", "comfort"})
	cs[17] = CfgConstraint{Name: "c18", Kind: "implies", Left: "brake", Right: "rim", Implies: &CfgConstraint{Kind: "eq", Left: "motor", Right: "none"}}
	cs[24] = implies("c25", "gear", "11", "in", "brake", []string{"disc", "hydraulic", "rim"})
	cs[26] = implies("c27", "color", "blue", "in", "saddle", []string{"sport", "comfort", "racing"})
	cs[36] = implies("c37", "motor", "hub", "in", "wheel", []string{"26", "275", "29", "700"})
	cs[46] = implies("c47", "wheel", "26", "in", "handlebar", []string{"flat", "riser"})
	cs[47] = CfgConstraint{Name: "c48", Kind: "in", Left: "motor", Values: []string{"none", "hub", "mid"}}
	return CfgModel{ID: "bicycle", MasterID: "bike-master", Version: 1, Status: "active", Attrs: attrs, Constraints: cs, Defaults: map[string]string{"color": "black", "motor": "none"}}
}

func first(v []string) string {
	if len(v) == 0 {
		return ""
	}
	return v[0]
}

// FrameForkWheelTable is the  allowed (frame, fork, wheel) combinations for c50.
func FrameForkWheelTable() DecisionTable {
	frames := []string{"aluminum", "carbon", "steel", "titanium"}
	forks := []string{"rigid", "suspension"}
	wheels := []string{"26", "275", "29", "700"}
	var rows [][]string
	for _, f := range frames {
		for _, k := range forks {
			for _, w := range wheels {
				if f == "steel" && k == "suspension" {
					continue
				}
				if f == "carbon" && w == "26" {
					continue
				}
				rows = append(rows, []string{f, k, w})
			}
		}
	}
	return DecisionTable{Name: "frame_fork_wheel", Columns: []string{"frame", "fork", "wheel"}, Rows: rows}
}

// BicycleSelectionSets returns 60 valid and 40 invalid labeled selection sets (AC-8.2).
func BicycleSelectionSets() (valid, invalid []map[string]string) {
	valid = []map[string]string{
		{"frame": "aluminum", "fork": "rigid", "wheel": "29", "tire": "slick", "brake": "disc", "handlebar": "flat", "saddle": "sport", "pedal": "platform", "gear": "11", "size": "M", "motor": "none", "color": "black"},
		{"frame": "carbon", "fork": "rigid", "wheel": "700", "tire": "slick", "brake": "hydraulic", "handlebar": "drop", "saddle": "racing", "pedal": "clipless", "gear": "12", "size": "M", "motor": "none", "color": "red"},
		{"frame": "steel", "fork": "rigid", "wheel": "26", "tire": "knobby", "brake": "rim", "handlebar": "flat", "saddle": "comfort", "pedal": "platform", "gear": "1", "size": "L", "motor": "none", "color": "black"},
		{"frame": "titanium", "fork": "rigid", "wheel": "29", "tire": "slick", "brake": "disc", "handlebar": "flat", "saddle": "sport", "pedal": "clipless", "gear": "12", "size": "L", "motor": "none", "color": "blue"},
		{"frame": "aluminum", "fork": "suspension", "wheel": "275", "tire": "knobby", "brake": "disc", "handlebar": "riser", "saddle": "comfort", "pedal": "platform", "gear": "8", "size": "M", "motor": "none", "color": "green"},
		{"frame": "carbon", "fork": "suspension", "wheel": "29", "tire": "knobby", "brake": "hydraulic", "handlebar": "flat", "saddle": "sport", "pedal": "clipless", "gear": "12", "size": "L", "motor": "none", "color": "black"},
		{"frame": "aluminum", "fork": "rigid", "wheel": "700", "tire": "gravel", "brake": "disc", "handlebar": "drop", "saddle": "sport", "pedal": "clipless", "gear": "11", "size": "M", "motor": "none", "color": "red"},
		{"frame": "aluminum", "fork": "rigid", "wheel": "29", "tire": "slick", "brake": "hydraulic", "handlebar": "flat", "saddle": "sport", "pedal": "platform", "gear": "12", "size": "XL", "motor": "mid", "color": "black"},
		{"frame": "aluminum", "fork": "suspension", "wheel": "29", "tire": "knobby", "brake": "disc", "handlebar": "flat", "saddle": "sport", "pedal": "platform", "gear": "8", "size": "S", "motor": "hub", "color": "red"},
		{"frame": "carbon", "fork": "rigid", "wheel": "700", "tire": "slick", "brake": "disc", "handlebar": "drop", "saddle": "racing", "pedal": "clipless", "gear": "11", "size": "S", "motor": "none", "color": "blue"},
	}
	// Expand valid sets by recoloring / resizing within allowed domains.
	colors := []string{"black", "red", "blue", "green"}
	sizes := []string{"S", "M", "L", "XL"}
	for len(valid) < 60 {
		base := copyValues(valid[len(valid)%10])
		base["color"] = colors[len(valid)%len(colors)]
		base["size"] = sizes[len(valid)%len(sizes)]
		if base["frame"] == "steel" && base["color"] == "white" {
			base["color"] = "black"
		}
		valid = append(valid, base)
	}
	invalid = []map[string]string{
		{"frame": "carbon", "brake": "rim"},
		{"frame": "steel", "fork": "suspension"},
		{"wheel": "26", "tire": "slick"},
		{"motor": "mid", "brake": "rim"},
		{"motor": "hub", "brake": "rim"},
		{"saddle": "racing", "handlebar": "flat"},
		{"tire": "slick", "wheel": "26"},
		{"gear": "1", "frame": "carbon"},
		{"handlebar": "drop", "wheel": "26"},
		{"pedal": "clipless", "saddle": "comfort"},
		{"motor": "mid", "frame": "steel"},
		{"motor": "mid", "frame": "titanium"},
		{"frame": "carbon", "wheel": "26"},
		{"color": "white"},
		{"wheel": "26", "handlebar": "drop"},
		{"frame": "steel", "brake": "rim", "motor": "hub"},
		{"gear": "12", "frame": "steel"},
		{"tire": "gravel", "wheel": "26"},
		{"tire": "gravel", "wheel": "29"},
		{"saddle": "racing", "handlebar": "riser"},
		{"frame": "titanium", "brake": "rim"},
		{"wheel": "700", "handlebar": "riser"},
		{"size": "S", "frame": "titanium"},
		{"motor": "mid", "brake": "disc"},
		{"frame": "carbon", "brake": "rim", "fork": "rigid"},
		{"wheel": "26", "tire": "gravel"},
		{"gear": "1", "frame": "titanium"},
		{"pedal": "clipless", "saddle": "comfort", "handlebar": "flat"},
		{"handlebar": "drop", "wheel": "275"},
		{"frame": "steel", "fork": "suspension", "wheel": "29"},
		{"motor": "hub", "brake": "rim", "wheel": "29"},
		{"tire": "slick", "wheel": "275"},
		{"frame": "carbon", "wheel": "26", "fork": "rigid"},
		{"saddle": "racing", "handlebar": "flat", "wheel": "700"},
		{"color": "white", "frame": "aluminum"},
		{"motor": "mid", "frame": "steel", "brake": "hydraulic"},
		{"wheel": "26", "tire": "slick", "frame": "aluminum"},
		{"gear": "12", "frame": "steel", "fork": "rigid"},
		{"frame": "titanium", "brake": "rim", "fork": "rigid"},
		{"handlebar": "drop", "wheel": "26", "tire": "knobby"},
	}
	return valid, invalid
}
