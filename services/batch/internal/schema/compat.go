// Package schema enforces additive-only BACKWARD_TRANSITIVE compatibility.
package schema

import (
	"fmt"

	"github.com/BenjaminSRussell/passage/services/batch/internal/domain"
)

// JSONSchema is the subset used by event payloads.
type JSONSchema struct {
	Type                 string                `json:"type,omitempty"`
	Enum                 []any                 `json:"enum,omitempty"`
	Required             []string              `json:"required,omitempty"`
	Properties           map[string]JSONSchema `json:"properties,omitempty"`
	Items                *JSONSchema           `json:"items,omitempty"`
	AdditionalProperties *bool                 `json:"additionalProperties,omitempty"`
}

// AssertBackwardCompatible rejects breaking changes (AC-4.4).
func AssertBackwardCompatible(current, next JSONSchema) error {
	return compare(current, next, "")
}

func compare(current, next JSONSchema, path string) error {
	label := path
	if label == "" {
		label = "schema"
	}
	if current.Type != next.Type {
		return incompatible(label + " changed type")
	}
	curReq := map[string]bool{}
	for _, r := range current.Required {
		curReq[r] = true
	}
	for _, r := range next.Required {
		if !curReq[r] {
			return incompatible(join(path, r) + " became required")
		}
	}
	if len(current.Enum) > 0 {
		nextVals := map[string]bool{}
		for _, v := range next.Enum {
			nextVals[fmt.Sprint(v)] = true
		}
		for _, v := range current.Enum {
			if !nextVals[fmt.Sprint(v)] {
				return incompatible(label + " removed enum value " + fmt.Sprint(v))
			}
		}
	}
	closed := current.AdditionalProperties != nil && !*current.AdditionalProperties
	for name, prop := range current.Properties {
		nextProp, ok := next.Properties[name]
		if !ok {
			if closed || (next.AdditionalProperties != nil && !*next.AdditionalProperties) {
				return incompatible(join(path, name) + " was removed")
			}
			continue
		}
		if err := compare(prop, nextProp, join(path, name)); err != nil {
			return err
		}
	}
	if (current.Items == nil) != (next.Items == nil) {
		return incompatible(label + " changed array item constraints")
	}
	if current.Items != nil {
		return compare(*current.Items, *next.Items, label+"[]")
	}
	return nil
}

func incompatible(detail string) error {
	return &domain.Error{Code: domain.CodeSchemaIncompatible, Detail: detail}
}

func join(path, name string) string {
	if path == "" {
		return name
	}
	return path + "." + name
}
