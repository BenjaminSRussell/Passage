import { readFileSync } from "node:fs";
import { describe, expect, it } from "vitest";
import { SandboxCarrier } from "../src/sandbox.ts";
import { play, type Fixture } from "./contract.ts";

const fixture = JSON.parse(readFileSync(new URL("../testdata/sandbox-recorded.json", import.meta.url), "utf8")) as Fixture;

describe("carrier adapter contract: sandbox against testdata/sandbox-recorded.json", () => {
  let clock = 0;
  const adapter = new SandboxCarrier({ now: () => clock });

  it("names the adapter it records", () => {
    expect(fixture.carrier).toBe(adapter.code);
    expect(fixture.interactions.length).toBeGreaterThanOrEqual(10);
  });

  for (const i of fixture.interactions) {
    it(i.name, async () => {
      clock = i.nowMs;
      const got = await play(adapter, i);
      expect(got).toEqual("error" in i ? { error: i.error } : i.response);
    });
  }
});
