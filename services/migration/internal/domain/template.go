package domain

import (
	"bytes"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
)

const (
	CodeTemplateInvalid  = "MIG-TEMPLATE-INVALID"
	maxTemplateDefaults  = 200
	maxTemplateName      = 80
	maxTemplateValueSize = 500
)

// DataTemplate holds project-specific default values for one entity (FR-10.18, D365 data templates).
// A default fills a mapping target only when the source row has no value for it; it applies before the
// field rule's own default, and every filled target is recorded as default_from_template in the run lineage.
type DataTemplate struct {
	Name     string            `json:"name"`
	Entity   string            `json:"entity"`
	Version  int               `json:"version"`
	Defaults map[string]string `json:"defaults"`
}

func ParseTemplate(data []byte) (DataTemplate, error) {
	var t DataTemplate
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&t); err != nil {
		return DataTemplate{}, &Error{CodeTemplateInvalid, "template JSON: " + err.Error()}
	}
	t.Name, t.Entity = strings.TrimSpace(t.Name), strings.TrimSpace(t.Entity)
	switch {
	case t.Name == "" || len(t.Name) > maxTemplateName:
		return DataTemplate{}, &Error{CodeTemplateInvalid, "name is required and at most 80 characters"}
	case t.Entity == "":
		return DataTemplate{}, &Error{CodeTemplateInvalid, "entity is required"}
	case t.Version < 1:
		return DataTemplate{}, &Error{CodeTemplateInvalid, "version must be 1 or higher"}
	case len(t.Defaults) == 0 || len(t.Defaults) > maxTemplateDefaults:
		return DataTemplate{}, &Error{CodeTemplateInvalid, "defaults needs 1 to 200 targets"}
	}
	for target, v := range t.Defaults {
		if strings.TrimSpace(target) == "" || len(v) > maxTemplateValueSize {
			return DataTemplate{}, &Error{CodeTemplateInvalid, "every default needs a target and a value of at most 500 characters"}
		}
	}
	return t, nil
}

// Targets lists the defaulted targets in a stable order.
func (t DataTemplate) Targets() []string {
	out := make([]string, 0, len(t.Defaults))
	for k := range t.Defaults {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// CheckTemplate verifies the template fits the mapping: same entity, every target is a mapping target filled from a
// source column (a constant rule is never absent), and every value satisfies the target's type.
func (m Mapping) CheckTemplate(t DataTemplate) error {
	if t.Entity != m.Entity {
		return &Error{CodeTemplateInvalid, fmt.Sprintf("template is for %s, the mapping is for %s", t.Entity, m.Entity)}
	}
	rules := make(map[string]FieldRule, len(m.Fields))
	for _, f := range m.Fields {
		rules[f.Target] = f
	}
	for _, target := range t.Targets() {
		f, ok := rules[target]
		if !ok {
			return &Error{CodeTemplateInvalid, target + " is not a mapping target"}
		}
		if f.Rule == RuleConst {
			return &Error{CodeTemplateInvalid, target + " is a constant rule and is never absent"}
		}
		if _, _, rej := m.coerce(f, t.Defaults[target], nil); rej != nil {
			return &Error{CodeTemplateInvalid, "value for target " + target + " does not satisfy its type"}
		}
	}
	return nil
}

// ApplyTemplate maps a row with the template defaults and returns the targets the template filled.
func (m Mapping) ApplyTemplate(row map[string]any, t *DataTemplate) (map[string]any, []Warning, []Reject, []string) {
	if t == nil {
		out, w, r := m.ApplyWithWarnings(row)
		return out, w, r, nil
	}
	return m.applyTemplated(row, t.Defaults)
}
