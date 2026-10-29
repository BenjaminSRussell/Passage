import { isObject, type JsonObject } from "./tool.js";

/**
 * Validates an upstream response against a tool's `outputSchema` (MCP tool
 * schema conformance). Supports the JSON Schema keywords the generated catalog
 * uses: type (one name or a list), OpenAPI's `nullable: true`, enum,
 * properties, required, items. Other keywords, `$ref` included, are ignored,
 * so a schema outside this subset passes rather than blocking a read. Returns
 * human-readable violations with JSON-path-style paths; an empty list means
 * the value conforms.
 */
export function schemaViolations(schema: JsonObject, value: unknown, path = "$"): string[] {
  const out: string[] = [];
  // OpenAPI 3.0 `nullable: true` (still used in Passage's 3.1 documents) admits null.
  if (value === null && schema.nullable === true) return out;
  const types = schema.type;
  if (types !== undefined) {
    const allowed = Array.isArray(types) ? types.map(String) : [String(types)];
    if (!allowed.some((t) => matchesType(t, value))) {
      out.push(`${path} must be ${allowed.join(" or ")}`);
      return out;
    }
  }
  if (Array.isArray(schema.enum) && !schema.enum.some((e) => sameJson(e, value))) {
    out.push(`${path} is not one of the allowed values`);
  }
  if (isObject(value)) {
    if (Array.isArray(schema.required)) {
      for (const key of schema.required) {
        if (typeof key === "string" && !Object.prototype.hasOwnProperty.call(value, key)) {
          out.push(`${path}.${key} is required`);
        }
      }
    }
    if (isObject(schema.properties)) {
      for (const [key, sub] of Object.entries(schema.properties)) {
        if (isObject(sub) && Object.prototype.hasOwnProperty.call(value, key)) {
          out.push(...schemaViolations(sub, value[key], `${path}.${key}`));
        }
      }
    }
  }
  if (Array.isArray(value) && isObject(schema.items)) {
    value.forEach((item, i) => out.push(...schemaViolations(schema.items as JsonObject, item, `${path}[${i}]`)));
  }
  return out;
}

function matchesType(type: string, value: unknown): boolean {
  switch (type) {
    case "object":
      return isObject(value);
    case "array":
      return Array.isArray(value);
    case "string":
      return typeof value === "string";
    case "number":
      return typeof value === "number" && Number.isFinite(value);
    case "integer":
      return typeof value === "number" && Number.isInteger(value);
    case "boolean":
      return typeof value === "boolean";
    case "null":
      return value === null;
    default:
      return true;
  }
}

function sameJson(a: unknown, b: unknown): boolean {
  return JSON.stringify(a) === JSON.stringify(b);
}
