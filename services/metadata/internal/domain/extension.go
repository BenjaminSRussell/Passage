package domain

import (
	"errors"
	"fmt"
	"slices"
)

const extensionCodeLimit = 2000000000

// Extension is an additive customer-layer overlay on a published entity.
type Extension struct {
	Name       string          `json:"name" yaml:"name"`
	Entity     string          `json:"entity" yaml:"entity"`
	Fields     []Field         `json:"fields,omitempty" yaml:"fields"`
	EnumValues []EnumExtension `json:"enum_values,omitempty" yaml:"enum_values"`
	Hooks      []Hook          `json:"hooks,omitempty" yaml:"hooks"`
}

// EnumExtension adds customer-owned values to an existing enum.
type EnumExtension struct {
	Enum   string      `json:"enum" yaml:"enum"`
	Values []EnumValue `json:"values" yaml:"values"`
}

// ApplyExtension returns an effective definition without mutating base.
func ApplyExtension(base *Entity, ext Extension) (*Entity, error) {
	if base == nil || ext.Entity != base.Name || ext.Name == "" {
		return nil, Violation{Path: "extension", Code: "MET-0101", Msg: "extension name and target entity are required"}
	}
	effective := cloneEntity(base)
	fieldNames := make(map[string]struct{}, len(effective.Fields))
	for _, field := range effective.Fields {
		fieldNames[field.Name] = struct{}{}
	}
	var errs []error
	for i, field := range ext.Fields {
		if _, exists := fieldNames[field.Name]; exists || isReserved(field.Name) {
			errs = append(errs, Violation{Path: fmt.Sprintf("fields[%d].name", i), Code: "MET-0102", Msg: "extension field collides with a published field"})
			continue
		}
		fieldNames[field.Name] = struct{}{}
		effective.Fields = append(effective.Fields, field)
	}
	for i, addition := range ext.EnumValues {
		enumIndex := slices.IndexFunc(effective.Enums, func(enum Enum) bool { return enum.Name == addition.Enum })
		if enumIndex < 0 {
			errs = append(errs, Violation{Path: fmt.Sprintf("enum_values[%d].enum", i), Code: "MET-0103", Msg: "extension enum does not exist"})
			continue
		}
		names := effective.EnumNames(addition.Enum)
		codes := map[int]struct{}{}
		for _, value := range effective.Enums[enumIndex].Values {
			codes[value.Code] = struct{}{}
		}
		for j, value := range addition.Values {
			path := fmt.Sprintf("enum_values[%d].values[%d]", i, j)
			if value.Code < firstExtensionCode || value.Code >= extensionCodeLimit {
				errs = append(errs, Violation{Path: path + ".code", Code: "MET-0103", Msg: fmt.Sprintf("extension enum code must be in %d..%d", firstExtensionCode, extensionCodeLimit-1)})
				continue
			}
			if slices.Contains(names, value.Name) {
				errs = append(errs, Violation{Path: path + ".name", Code: "MET-0103", Msg: "extension enum value name collides"})
				continue
			}
			if _, exists := codes[value.Code]; exists {
				errs = append(errs, Violation{Path: path + ".code", Code: "MET-0103", Msg: "extension enum value code collides"})
				continue
			}
			names = append(names, value.Name)
			codes[value.Code] = struct{}{}
			effective.Enums[enumIndex].Values = append(effective.Enums[enumIndex].Values, value)
		}
	}
	effective.Hooks = append(effective.Hooks, ext.Hooks...)
	if len(errs) > 0 {
		return nil, errors.Join(errs...)
	}
	if violations := ValidateEffective(effective); len(violations) > 0 {
		joined := make([]error, len(violations))
		for i, violation := range violations {
			joined[i] = violation
		}
		return nil, errors.Join(joined...)
	}
	return effective, nil
}

func cloneEntity(entity *Entity) *Entity {
	clone := *entity
	clone.TitleFields = slices.Clone(entity.TitleFields)
	clone.Fields = slices.Clone(entity.Fields)
	clone.Relations = slices.Clone(entity.Relations)
	clone.Indexes = make([]Index, len(entity.Indexes))
	for i, index := range entity.Indexes {
		clone.Indexes[i] = index
		clone.Indexes[i].Fields = slices.Clone(index.Fields)
	}
	clone.Hooks = slices.Clone(entity.Hooks)
	clone.Enums = make([]Enum, len(entity.Enums))
	for i, enum := range entity.Enums {
		clone.Enums[i] = enum
		clone.Enums[i].Values = slices.Clone(enum.Values)
	}
	return &clone
}

// ValidateEffective validates a definition that may contain customer enum codes.
func ValidateEffective(entity *Entity) []Violation {
	clone := cloneEntity(entity)
	for i := range clone.Enums {
		clone.Enums[i].Values = slices.DeleteFunc(clone.Enums[i].Values, func(value EnumValue) bool {
			return value.Code >= firstExtensionCode
		})
		if len(clone.Enums[i].Values) == 0 {
			clone.Enums[i].Values = []EnumValue{{Name: "_extension_placeholder", Code: 0}}
		}
	}
	violations := Validate(clone)
	for enumIndex, enum := range entity.Enums {
		for valueIndex, value := range enum.Values {
			if value.Code >= firstExtensionCode && value.Code < extensionCodeLimit && matches(fieldNamePattern, value.Name) {
				continue
			}
			if value.Code >= firstExtensionCode {
				violations = append(violations, Violation{
					Path: fmt.Sprintf("enums[%d].values[%d]", enumIndex, valueIndex),
					Code: "MET-0103", Msg: "invalid extension enum value",
				})
			}
		}
	}
	return violations
}
