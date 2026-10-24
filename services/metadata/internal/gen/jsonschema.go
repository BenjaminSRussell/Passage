package gen

import (
	"encoding/json"
	"strings"

	"github.com/BenjaminSRussell/passage/services/metadata/internal/domain"
)

// JSONSchema returns the effective input schema for a generated entity API.
func JSONSchema(entity *domain.Entity) (string, error) {
	properties := obj{}
	required := []string{}
	for _, field := range entity.Fields {
		for _, property := range props(entity, field) {
			schema := property.Schema
			if field.Default != "" {
				schema["default"] = field.Default
			}
			if field.Computed != "" {
				schema["readOnly"] = true
				schema["x-passage-computed"] = field.Computed
			}
			if field.Visibility != "" {
				schema["x-passage-visibility"] = field.Visibility
			}
			if field.Audit != nil {
				schema["x-passage-audit"] = *field.Audit
			}
			if field.Label != "" {
				schema["title"] = field.Label
			}
			properties[property.Wire] = schema
			if field.Required && field.Default == "" && field.Computed == "" {
				required = append(required, property.Wire)
			}
		}
	}
	schema := obj{
		"$schema":              "https://json-schema.org/draft/2020-12/schema",
		"$id":                  "https://passage.dev/schemas/" + entity.Domain + "/" + entity.Name,
		"title":                entity.Name,
		"type":                 "object",
		"additionalProperties": false,
		"properties":           properties,
	}
	if len(required) > 0 {
		schema["required"] = required
	}
	data, err := json.Marshal(schema)
	return strings.TrimSpace(string(data)) + "\n", err
}
