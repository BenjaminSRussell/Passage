package domain

import (
	"fmt"
	"regexp"
	"strings"
	"unicode/utf8"

	"github.com/BenjaminSRussell/passage/platform/ids"
)

// Limits per tenant (FR-39.1 notes: ≤ 50 cost groups, ≤ 20 components).
const (
	MaxComponents = 20
	MaxCostGroups = 50
)

// Cost group types (FR-39.1, AX cost group type) and behaviours. The same
// enumerations classify a cost component (costing.componentStructure).
const (
	GroupDirectMaterial      = "DirectMaterial"
	GroupDirectManufacturing = "DirectManufacturing"
	GroupIndirect            = "Indirect"
	GroupUndefined           = "Undefined"
	BehaviourFixed           = "Fixed"
	BehaviourVariable        = "Variable"
)

// Codes for the cost structure rules. They extend the wave-39 catalog
// (section 7.39.18) for slice 5; the README lists them.
const (
	CodeComponentLimit     = "CST-0790"
	CodeComponentDuplicate = "CST-0791"
	CodeGroupLimit         = "CST-0792"
	CodeGroupDuplicate     = "CST-0793"
	CodeGroupComponent     = "CST-0794"
)

const maxStructureName = 120

// structureCode is the code of a cost component or cost group: 1–20 upper-case
// letters, digits or hyphens, starting with a letter or digit (DM, IND-MAT).
var structureCode = regexp.MustCompile(`^[A-Z0-9][A-Z0-9-]{0,19}$`)

// CostComponent is one column of the cost component split (DM, DL, IND-MAT,
// IND-LAB, SUB). Its code is what the roll-up's splits are keyed by.
type CostComponent struct {
	Code          string `json:"code"`
	Name          string `json:"name"`
	CostGroupType string `json:"costGroupType"`
	Behaviour     string `json:"behaviour"`
}

// CostGroup classifies item costs, cost categories and indirect nodes (FR-39.1)
// and maps them to one cost component of the split.
type CostGroup struct {
	Code        string `json:"code"`
	Name        string `json:"name"`
	Type        string `json:"type"`
	Behaviour   string `json:"behaviour"`
	ComponentID string `json:"componentId"`
}

// Normalize trims the text fields and defaults the behaviour to Variable.
func (c CostComponent) Normalize() CostComponent {
	c.Code = strings.TrimSpace(c.Code)
	c.Name = strings.TrimSpace(c.Name)
	if c.Behaviour == "" {
		c.Behaviour = BehaviourVariable
	}
	return c
}

// Validate returns a field → message map; empty means the component is well formed.
func (c CostComponent) Validate() map[string]string {
	fields := map[string]string{}
	checkStructure(c.Code, c.Name, c.CostGroupType, c.Behaviour, "costGroupType", fields)
	return fields
}

// Normalize trims the text fields and defaults the behaviour to Variable.
func (g CostGroup) Normalize() CostGroup {
	g.Code = strings.TrimSpace(g.Code)
	g.Name = strings.TrimSpace(g.Name)
	g.ComponentID = strings.TrimSpace(g.ComponentID)
	if g.Behaviour == "" {
		g.Behaviour = BehaviourVariable
	}
	return g
}

// Validate returns a field → message map. Whether the component exists is the
// store's check (CST-0794).
func (g CostGroup) Validate() map[string]string {
	fields := map[string]string{}
	checkStructure(g.Code, g.Name, g.Type, g.Behaviour, "type", fields)
	if !ids.Valid(g.ComponentID) {
		fields["componentId"] = "must be the id of a cost component"
	}
	return fields
}

func checkStructure(code, name, typ, behaviour, typeField string, fields map[string]string) {
	if !structureCode.MatchString(code) {
		fields["code"] = "must be 1-20 upper-case letters, digits or hyphens"
	}
	switch {
	case name == "":
		fields["name"] = "is required"
	case utf8.RuneCountInString(name) > maxStructureName:
		fields["name"] = fmt.Sprintf("must be at most %d characters", maxStructureName)
	}
	switch typ {
	case GroupDirectMaterial, GroupDirectManufacturing, GroupIndirect, GroupUndefined:
	default:
		fields[typeField] = "must be DirectMaterial, DirectManufacturing, Indirect or Undefined"
	}
	switch behaviour {
	case BehaviourFixed, BehaviourVariable:
	default:
		fields["behaviour"] = "must be Fixed or Variable"
	}
}

// CheckComponentCapacity is CST-0790 when the tenant already holds MaxComponents.
func CheckComponentCapacity(existing int) error {
	if existing >= MaxComponents {
		return coded(CodeComponentLimit, "a tenant holds at most %d cost components", MaxComponents)
	}
	return nil
}

// CheckGroupCapacity is CST-0792 when the tenant already holds MaxCostGroups.
func CheckGroupCapacity(existing int) error {
	if existing >= MaxCostGroups {
		return coded(CodeGroupLimit, "a tenant holds at most %d cost groups", MaxCostGroups)
	}
	return nil
}
