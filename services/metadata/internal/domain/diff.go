package domain

import (
	"errors"
	"reflect"
	"slices"
)

type Change struct {
	Path     string
	Kind     string
	Breaking bool
}

func Diff(old, next *Entity) []Change {
	firstPublish := old == nil
	if firstPublish {
		old = &Entity{}
	}
	var out []Change
	if !firstPublish && old.Name != next.Name {
		out = append(out, Change{"name", "entity.name_changed", true})
	}
	if !firstPublish && old.Domain != next.Domain {
		out = append(out, Change{"domain", "entity.domain_changed", true})
	}
	if !firstPublish && old.PerLegalEntity != next.PerLegalEntity {
		out = append(out, Change{"per_legal_entity", "entity.scope_changed", true})
	}
	oldFields := map[string]Field{}
	for _, f := range old.Fields {
		oldFields[f.Name] = f
	}
	nextFields := map[string]Field{}
	for _, f := range next.Fields {
		nextFields[f.Name] = f
	}
	for _, o := range old.Fields {
		n, ok := nextFields[o.Name]
		switch {
		case !ok:
			out = append(out, Change{"fields." + o.Name, "field.removed", true})
		case narrowed(o, n):
			out = append(out, Change{"fields." + o.Name, "field.type_changed", true})
		case o.Encrypted != n.Encrypted:
			out = append(out, Change{"fields." + o.Name, "field.encryption_changed", true})
		case !o.Required && n.Required:
			out = append(out, Change{"fields." + o.Name, "field.required_added", true})
		case !o.Unique && n.Unique:
			out = append(out, Change{"fields." + o.Name, "field.unique_added", true})
		case !reflect.DeepEqual(o, n):
			out = append(out, Change{"fields." + o.Name, "field.relaxed", false})
		}
	}
	for _, n := range next.Fields {
		if _, ok := oldFields[n.Name]; !ok {
			out = append(out, Change{"fields." + n.Name, "field.added", !firstPublish && n.Required && n.Default == ""})
		}
	}
	for _, oe := range old.Enums {
		nextIndex := slices.IndexFunc(next.Enums, func(enum Enum) bool { return enum.Name == oe.Name })
		if nextIndex < 0 {
			out = append(out, Change{"enums." + oe.Name, "enum.removed", true})
			continue
		}
		nextEnum := next.Enums[nextIndex]
		names := next.EnumNames(oe.Name)
		for _, v := range oe.Values {
			if !slices.Contains(names, v.Name) {
				out = append(out, Change{"enums." + oe.Name + "." + v.Name, "enum.value_removed", true})
				continue
			}
			nextValue := nextEnum.Values[slices.IndexFunc(nextEnum.Values, func(candidate EnumValue) bool { return candidate.Name == v.Name })]
			if nextValue.Code != v.Code {
				out = append(out, Change{"enums." + oe.Name + "." + v.Name + ".code", "enum.code_changed", true})
			}
		}
	}
	for _, ne := range next.Enums {
		names := old.EnumNames(ne.Name)
		for _, v := range ne.Values {
			if !slices.Contains(names, v.Name) {
				out = append(out, Change{"enums." + ne.Name + "." + v.Name, "enum.value_added", false})
			}
		}
	}
	for i, oldIndex := range old.Indexes {
		if i >= len(next.Indexes) {
			out = append(out, Change{"indexes", "index.removed", false})
			continue
		}
		nextIndex := next.Indexes[i]
		if !reflect.DeepEqual(oldIndex.Fields, nextIndex.Fields) || oldIndex.Where != nextIndex.Where {
			out = append(out, Change{"indexes", "index.definition_changed", true})
		} else if !oldIndex.Unique && nextIndex.Unique {
			out = append(out, Change{"indexes", "index.unique_added", true})
		} else if oldIndex.Unique && !nextIndex.Unique {
			out = append(out, Change{"indexes", "index.unique_removed", false})
		}
	}
	for i := len(old.Indexes); i < len(next.Indexes); i++ {
		out = append(out, Change{"indexes", "index.added", next.Indexes[i].Unique && !firstPublish})
	}
	for i, relation := range old.Relations {
		if i >= len(next.Relations) {
			out = append(out, Change{"relations", "relation.removed", true})
			continue
		}
		if !reflect.DeepEqual(relation, next.Relations[i]) {
			out = append(out, Change{"relations", "relation.changed", true})
		}
	}
	if len(next.Relations) > len(old.Relations) {
		out = append(out, Change{"relations", "relation.added", false})
	}
	return out
}

func Breaking(changes []Change) []Change {
	return slices.DeleteFunc(slices.Clone(changes), func(c Change) bool { return !c.Breaking })
}

func CompareVersions(old, next *Entity) error {
	if next.Breaking {
		return nil
	}
	var errs []error
	for _, c := range Breaking(Diff(old, next)) {
		errs = append(errs, Violation{Path: c.Path, Code: "MET-0201", Msg: c.Kind + " is a breaking change; set breaking: true and bump the API major"})
	}
	return errors.Join(errs...)
}

func narrowed(o, n Field) bool {
	switch {
	case o.Type == n.Type:
		return n.Length < o.Length || n.Precision-n.Scale < o.Precision-o.Scale || n.Scale < o.Scale || o.Enum != n.Enum
	case o.Type == "int32" && n.Type == "int64", o.Type == "string" && n.Type == "text":
		return false
	}
	return true
}
