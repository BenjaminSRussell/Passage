import { describe, expect, it } from "vitest";
import { parseDefinitionText, STARTER_DEFINITION, STARTER_EVENT, toJsonText } from "./outboundDomain";

describe("outbound starter with array iteration", () => {
  it("round-trips a definition whose collection maps the order lines", () => {
    const parsed = parseDefinitionText(toJsonText(STARTER_DEFINITION));
    expect(parsed.error).toBeUndefined();
    expect(parsed.value?.collections?.[0]).toMatchObject({ name: "Lines", path: "data.lines" });
  });

  it("carries lines in the sample event the collection reads", () => {
    const lines = (STARTER_EVENT.data as { lines: unknown[] }).lines;
    expect(lines).toHaveLength(1);
  });
});
