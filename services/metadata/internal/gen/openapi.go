package gen

import (
	"encoding/json"
	"strings"

	"github.com/BenjaminSRussell/passage/services/metadata/internal/domain"
)

type obj = map[string]any

func ref(kind, name string) obj { return obj{"$ref": "#/components/" + kind + "/" + name} }

func jsonBody(schema obj) obj {
	return obj{"description": "OK", "content": obj{"application/json": obj{"schema": schema}}}
}

func openAPI(e *domain.Entity) (string, error) {
	duty := snake(e.Name)
	path := collectionPath(e)
	idParam := obj{"name": "id", "in": "path", "required": true, "schema": obj{"type": "string", "format": "uuid"}}
	problem := ref("responses", "Problem")
	operation := func(id, action string, extra obj) obj {
		op := obj{"operationId": id + e.Name, "x-passage-duty": duty + "." + action, "responses": obj{"default": problem}}
		for k, v := range extra {
			op[k] = v
		}
		return op
	}
	entityRef, inputRef := ref("schemas", e.Name), ref("schemas", e.Name+"Input")
	patchRef := ref("schemas", e.Name+"Patch")
	body := obj{"required": true, "content": obj{"application/json": obj{"schema": inputRef}}}
	patchBody := obj{"required": true, "content": obj{"application/json": obj{"schema": patchRef}}}
	responses := func(status string, r obj) obj { return obj{status: r, "default": problem} }
	etagged := jsonBody(entityRef)
	etagged["headers"] = obj{"ETag": obj{"schema": obj{"type": "string"}}}
	conflict := obj{"412": obj{"description": "conflict_version: the If-Match ETag is stale", "content": obj{"application/problem+json": obj{"schema": ref("schemas", "Problem")}}}}
	patchResponses := responses("200", etagged)
	patchResponses["412"] = conflict["412"]
	patchResponses["428"] = problem

	inputProps, entityProps := obj{}, obj{
		"id": obj{"type": "string", "format": "uuid"}, "tenantId": obj{"type": "string", "format": "uuid"},
		"version": obj{"type": "integer"}, "ext": obj{"type": "object"},
	}
	inputRequired, entityRequired := []string{}, []string{"id", "tenantId", "version"}
	if e.PerLegalEntity {
		entityProps["legalEntityId"] = obj{"type": "string", "format": "uuid"}
		entityRequired = append(entityRequired, "legalEntityId")
	}
	for _, f := range e.Fields {
		for _, p := range props(e, f) {
			inputProps[p.Wire], entityProps[p.Wire] = p.Schema, p.Schema
			if f.Required {
				entityRequired = append(entityRequired, p.Wire)
				if f.Default == "" && f.Computed == "" {
					inputRequired = append(inputRequired, p.Wire)
				}
			}
		}
	}
	hooks := []string{}
	for _, h := range e.Hooks {
		hooks = append(hooks, h.Name)
	}
	info := obj{"title": e.Name + " API", "version": "1.0.0", "x-passage-hooks": hooks}
	if phi := e.PHIFields(); len(phi) > 0 {
		wire := make([]string, len(phi))
		for i, name := range phi {
			wire[i] = camel(name)
		}
		info["x-passage-phi-fields"] = wire
	}
	doc := obj{
		"openapi": "3.1.0",
		"info":    info,
		"paths": obj{
			path: obj{
				"get":  operation("list", "read", obj{"responses": responses("200", jsonBody(obj{"type": "array", "items": entityRef}))}),
				"post": operation("create", "write", obj{"parameters": []obj{ref("parameters", "IdempotencyKey")}, "requestBody": body, "responses": responses("201", etagged)}),
			},
			path + "/{id}": obj{
				"get":    operation("get", "read", obj{"parameters": []obj{idParam}, "responses": responses("200", etagged)}),
				"patch":  operation("update", "write", obj{"parameters": []obj{idParam, ref("parameters", "IfMatch")}, "requestBody": patchBody, "responses": patchResponses}),
				"delete": operation("remove", "delete", obj{"parameters": []obj{idParam, ref("parameters", "IfMatch")}, "responses": responses("204", obj{"description": "Deleted"})}),
			},
		},
		"components": obj{
			"parameters": obj{
				"IdempotencyKey": obj{"name": "Idempotency-Key", "in": "header", "required": true, "schema": obj{"type": "string"}},
				"IfMatch":        obj{"name": "If-Match", "in": "header", "required": true, "schema": obj{"type": "string"}},
			},
			"responses": obj{"Problem": obj{"description": "Error", "content": obj{"application/problem+json": obj{"schema": ref("schemas", "Problem")}}}},
			"schemas": obj{
				e.Name:           obj{"type": "object", "required": entityRequired, "properties": entityProps},
				e.Name + "Input": obj{"type": "object", "required": inputRequired, "properties": inputProps, "additionalProperties": false},
				e.Name + "Patch": obj{"type": "object", "properties": inputProps, "additionalProperties": false, "minProperties": 1},
				"Problem": obj{"type": "object", "required": []string{"type", "title", "status", "code", "traceId"}, "properties": obj{
					"type": obj{"type": "string"}, "title": obj{"type": "string"}, "status": obj{"type": "integer"},
					"detail": obj{"type": "string"}, "instance": obj{"type": "string"}, "code": obj{"type": "string"},
					"serviceCode": obj{"type": "string"}, "traceId": obj{"type": "string"},
					"errors": obj{"type": "array", "items": obj{"type": "object"}},
				}},
			},
		},
	}
	out, err := json.Marshal(doc)
	return strings.TrimSpace(string(out)) + "\n", err
}
