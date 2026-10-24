export interface JsonSchema {
  type?: string;
  enum?: unknown[];
  required?: string[];
  properties?: Record<string, JsonSchema>;
  items?: JsonSchema;
  additionalProperties?: boolean | JsonSchema;
}

/**
 * Enforces the additive subset of Apicurio BACKWARD_TRANSITIVE compatibility.
 * It is deliberately dependency-free so the same check can run before schemas
 * are registered and can be called by a future Wave 03 metadata adapter.
 */
export function assertBackwardCompatible(current: JsonSchema, next: JsonSchema): void {
  compareSchema(current, next, "");
}

function compareSchema(current: JsonSchema, next: JsonSchema, path: string): void {
  const label = path || "schema";
  if (current.type !== next.type) {
    throw new Error(`${label} changed type from ${current.type ?? "any"} to ${next.type ?? "any"}`);
  }

  const currentRequired = new Set(current.required ?? []);
  for (const property of next.required ?? []) {
    if (!currentRequired.has(property)) {
      throw new Error(`${join(path, property)} became required`);
    }
  }

  if (current.enum) {
    const nextValues = new Set(next.enum ?? []);
    for (const value of current.enum) {
      if (!nextValues.has(value)) {
        throw new Error(`${label} removed enum value ${String(value)}`);
      }
    }
  }

  const currentProperties = current.properties ?? {};
  const nextProperties = next.properties ?? {};
  for (const [property, currentProperty] of Object.entries(currentProperties)) {
    const nextProperty = nextProperties[property];
    if (!nextProperty) {
      if (next.additionalProperties === false) {
        throw new Error(`${join(path, property)} was removed`);
      }
      continue;
    }
    compareSchema(currentProperty, nextProperty, join(path, property));
  }

  if (current.items && next.items) {
    compareSchema(current.items, next.items, `${label}[]`);
  } else if (Boolean(current.items) !== Boolean(next.items)) {
    throw new Error(`${label} changed array item constraints`);
  }
}

function join(path: string, property: string): string {
  return path ? `${path}.${property}` : property;
}
