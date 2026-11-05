// Records testdata/sandbox-recorded.json from the sandbox carrier: `RECORD_FIXTURES=1 npx vitest --run test/record.test.ts`.
// Rerun it only for an intended change to the sandbox's formula or label layout, and review the diff: the contract test
// replays this file, so for the sandbox it is a golden recording that catches any unintended change in behaviour.

import { writeFileSync } from "node:fs";
import { describe, it } from "vitest";
import type { LabelRequest, RateRequest } from "../src/adapter.ts";
import { SandboxCarrier } from "../src/sandbox.ts";
import { play, type Fixture, type Interaction } from "./contract.ts";
import { BULKY, DE_TO, NOW_MS, SMALL, US_FROM, US_TO } from "./helpers.ts";

const H = 3600_000;
let clock = NOW_MS;
const adapter = new SandboxCarrier({ now: () => clock });
const interactions: Interaction[] = [];

async function record(name: string, nowMs: number, op: Interaction["op"], request: unknown): Promise<unknown> {
  clock = nowMs;
  const shell = { name, nowMs, op, request } as Interaction;
  const got = await play(adapter, shell);
  interactions.push((typeof got === "object" && got !== null && "error" in got ? { ...shell, error: (got as { error: string }).error } : { ...shell, response: got }) as Interaction);
  return got;
}


describe.runIf(process.env["RECORD_FIXTURES"] === "1")("record the sandbox fixture", () => {
  it("records testdata/sandbox-recorded.json", async () => {
    const domestic: RateRequest = { shipFrom: US_FROM, shipTo: US_TO, parcels: [SMALL] };
    await record("rate: domestic, one parcel, every service", NOW_MS, "rate", domestic);
    await record("rate: domestic, volumetric parcel, ground only", NOW_MS, "rate", { ...domestic, parcels: [SMALL, BULKY], services: ["GROUND"] });
    await record("rate: international surcharge and transit", NOW_MS, "rate", { ...domestic, shipTo: DE_TO });
    await record("rate: unknown service", NOW_MS, "rate", { ...domestic, services: ["OVERNIGHT"] });
    await record("rate: country not served", NOW_MS, "rate", { ...domestic, shipTo: { ...US_TO, country: "BR" } });
    const ground: LabelRequest = { ...domestic, service: "GROUND", reference: "SH-000123", idempotencyKey: "tenant|entity|fulfillment|ship-1" };
    const label = (await record("label: domestic ground", NOW_MS, "label", ground)) as { trackingNumber: string };
    const express = (await record("label: international express to the exception postal code", NOW_MS, "label", {
      ...ground,
      shipTo: { ...DE_TO, postalCode: "00000" },
      service: "EXPRESS",
      idempotencyKey: "tenant|entity|fulfillment|ship-2",
    })) as { trackingNumber: string };
    await record("label: unknown service", NOW_MS, "label", { ...ground, service: "OVERNIGHT" });
    await record("track: just labelled", NOW_MS + H, "track", { trackingNumber: label.trackingNumber });
    await record("track: in transit", NOW_MS + 5 * H, "track", { trackingNumber: label.trackingNumber });
    await record("track: delivered after three days", NOW_MS + 80 * H, "track", { trackingNumber: label.trackingNumber });
    await record("track: exception", NOW_MS + 30 * H, "track", { trackingNumber: express.trackingNumber });
    await record("track: not a sandbox number", NOW_MS, "track", { trackingNumber: "SBX000000000000000001" });

    const fixture: Fixture = { carrier: adapter.code, interactions };
    writeFileSync(new URL("../testdata/sandbox-recorded.json", import.meta.url), JSON.stringify(fixture, null, 2) + "\n");
    console.log(`recorded ${interactions.length} interactions`);
  });
});
