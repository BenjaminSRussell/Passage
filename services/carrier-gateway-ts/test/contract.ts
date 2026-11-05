// The carrier adapter contract (wave 28 AC4 "carrier adapter contract tests against recorded fixtures"). Every adapter
// is replayed against a recorded fixture file: each interaction names the clock, the call and the response the carrier
// gave. The invariants below hold for any adapter, sandbox or real; a real adapter's fixture is recorded from the
// carrier's test environment and replayed through a recorded transport (NOT-IMPLEMENTED.md).

import { expect } from "vitest";
import type { CarrierAdapter, Label, LabelRequest, Rate, RateRequest, Tracking } from "../src/adapter.ts";

export type Interaction =
  | { name: string; nowMs: number; op: "rate"; request: RateRequest; response: Rate[] }
  | { name: string; nowMs: number; op: "label"; request: LabelRequest; response: Label }
  | { name: string; nowMs: number; op: "track"; request: { trackingNumber: string }; response: Tracking }
  | { name: string; nowMs: number; op: "rate" | "label" | "track"; request: unknown; error: string };

export interface Fixture {
  carrier: string;
  interactions: Interaction[];
}

const MONEY = /^\d+\.\d{2}$/;
const STATUSES = ["LABELLED", "IN_TRANSIT", "DELIVERED", "EXCEPTION"];

export function checkRates(adapter: CarrierAdapter, rates: Rate[]): void {
  const services = adapter.services().map((s) => s.code);
  for (const r of rates) {
    expect(r.carrier).toBe(adapter.code);
    expect(services).toContain(r.service);
    expect(r.amount).toMatch(MONEY);
    expect(r.currency).toMatch(/^[A-Z]{3}$/);
    expect(Number.isInteger(r.transitDays) && r.transitDays >= 0).toBe(true);
    expect(r.billableWeightKg).toMatch(/^\d+\.\d{3}$/);
  }
}

export function checkLabel(adapter: CarrierAdapter, label: Label): void {
  expect(label.carrier).toBe(adapter.code);
  expect(label.trackingNumber).toMatch(/^[A-Za-z0-9-]{1,64}$/);
  expect(label.amount).toMatch(MONEY);
  expect(label.format).toBe("ZPL");
  expect(Buffer.from(label.document, "base64").toString("utf8")).toMatch(/^\^XA[\s\S]*\^XZ\n?$/);
  expect(new Date(label.createdAt).toISOString()).toBe(label.createdAt);
}

export function checkTracking(adapter: CarrierAdapter, t: Tracking): void {
  expect(t.carrier).toBe(adapter.code);
  expect(t.events.length).toBeGreaterThan(0);
  const keys = new Set(t.events.map((e) => e.eventKey));
  expect(keys.size).toBe(t.events.length);
  for (const e of t.events) {
    expect(STATUSES).toContain(e.status);
    expect(e.eventKey.length).toBeLessThanOrEqual(128);
    expect(e.description.length).toBeLessThanOrEqual(500);
  }
  const times = t.events.map((e) => Date.parse(e.eventTime));
  expect([...times].sort((a, b) => a - b)).toEqual(times);
  expect(t.status).toBe(t.events[t.events.length - 1]!.status);
}

/** Runs one interaction against an adapter whose clock the caller set to interaction.nowMs. */
export async function play(adapter: CarrierAdapter, i: Interaction): Promise<unknown> {
  try {
    if (i.op === "rate") {
      const r = await adapter.rate(i.request as RateRequest);
      checkRates(adapter, r);
      return r;
    }
    if (i.op === "label") {
      const l = await adapter.label(i.request as LabelRequest);
      checkLabel(adapter, l);
      return l;
    }
    const t = await adapter.track((i.request as { trackingNumber: string }).trackingNumber);
    checkTracking(adapter, t);
    return t;
  } catch (err) {
    const code = (err as { code?: unknown }).code;
    if (typeof code === "string") return { error: code };
    throw err;
  }
}
