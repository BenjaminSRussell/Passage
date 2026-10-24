import { describe, expect, it } from "vitest";
import {
  assertBackwardCompatible,
  type JsonSchema,
} from "../src/schemaCompatibility.js";

const current: JsonSchema = {
  type: "object",
  additionalProperties: false,
  required: ["id", "amount"],
  properties: {
    id: { type: "string" },
    amount: { type: "number" },
    status: { type: "string", enum: ["draft", "submitted", "approved"] },
  },
};

describe("schema compatibility gate", () => {
  it("accepts additive optional fields", () => {
    expect(() => assertBackwardCompatible(current, {
      ...current,
      properties: {
        ...current.properties,
        description: { type: "string" },
      },
    })).not.toThrow();
  });

  it.each([
    {
      name: "changes an existing property type",
      next: {
        ...current,
        properties: { ...current.properties, amount: { type: "string" } },
      },
      message: "amount changed type",
    },
    {
      name: "adds a required property",
      next: {
        ...current,
        required: [...current.required!, "description"],
        properties: {
          ...current.properties,
          description: { type: "string" },
        },
      },
      message: "description became required",
    },
    {
      name: "narrows an enum",
      next: {
        ...current,
        properties: {
          ...current.properties,
          status: { type: "string", enum: ["submitted", "approved"] },
        },
      },
      message: "status removed enum value draft",
    },
  ])("rejects a breaking change that $name", ({ next, message }) => {
    expect(() => assertBackwardCompatible(current, next)).toThrow(message);
  });
});
