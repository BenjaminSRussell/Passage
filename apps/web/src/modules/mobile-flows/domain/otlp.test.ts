import { describe, expect, it, vi } from "vitest";
import { buildSpans, exportSpans, SERVICE_NAME, SPAN_NAME, type OtlpPayload } from "./otlp";
import type { StepTiming } from "./telemetry";

const NOW = 1_700_000_000_000; // epoch ms
const DAY = 86_400_000;

const timing = (over: Partial<StepTiming> = {}): StepTiming => ({
  flowCode: "RECEIVE",
  stepId: "scan-lp",
  userId: "user-77",
  deviceId: "dev-1",
  durationMs: 1500,
  failed: false,
  at: NOW - 1000,
  ...over,
});

// Deterministic ids so the assertions can name them.
const fixedRandom = (bytes: number) => "ab".repeat(bytes);

function build(records: StepTiming[], extra: Partial<Parameters<typeof buildSpans>[1]> = {}) {
  const out = buildSpans(records, { now: NOW, randomHex: fixedRandom, deviceModel: "zebra-tc52", ...extra });
  if ("error" in out) throw new Error(out.error);
  return out;
}

describe("buildSpans", () => {
  it("emits one mobile.step span per timing that ends when the step was recorded", () => {
    // The runner records a StepTiming when the step ends (flowRunner: at = now).
    const { payload, spans } = build([timing({ at: NOW, durationMs: 1500 })]);
    expect(spans).toBe(1);
    const span = payload.resourceSpans[0].scopeSpans[0].spans[0];
    expect(span.name).toBe(SPAN_NAME);
    expect(span.startTimeUnixNano).toBe("1699999998500000000");
    expect(span.endTimeUnixNano).toBe("1700000000000000000");
    expect(span.traceId).toHaveLength(32);
    expect(span.spanId).toHaveLength(16);
  });

  it("marks failed steps with error status and others OK", () => {
    const { payload } = build([timing({ failed: true }), timing({ failed: false, stepId: "b" })]);
    const [failed, ok] = payload.resourceSpans[0].scopeSpans[0].spans;
    expect(failed.status.code).toBe(2);
    expect(ok.status.code).toBe(1);
  });

  it("carries flow, step, device and model but never the user id (§6.32.20)", () => {
    const { payload } = build([timing()]);
    const json = JSON.stringify(payload);
    expect(json).not.toContain("user-77");
    expect(json).not.toContain("userId");
    const attrs = payload.resourceSpans[0].scopeSpans[0].spans[0].attributes.map((a) => a.key);
    expect(attrs).toEqual(["passage.flow.code", "passage.step.id", "passage.device.id", "passage.device.model"]);
    expect(payload.resourceSpans[0].resource.attributes).toContainEqual({ key: "service.name", value: { stringValue: SERVICE_NAME } });
  });

  it("drops timings outside the retention window and reports how many", () => {
    const old = timing({ at: NOW - 91 * DAY });
    const fresh = timing({ at: NOW - 89 * DAY });
    const { spans, removed } = build([old, fresh]);
    expect(spans).toBe(1);
    expect(removed).toBe(1);
  });

  it("uses a 30 to 365 day retention bound and rejects anything else", () => {
    expect(buildSpans([], { now: NOW, retentionDays: 10 })).toEqual({
      error: expect.stringContaining("retention"),
    });
    expect(buildSpans([], { now: NOW, retentionDays: 365 })).toMatchObject({ spans: 0 });
  });

  it("builds an empty batch when there is nothing to export", () => {
    const { payload, spans } = build([]);
    expect(spans).toBe(0);
    expect(payload.resourceSpans[0].scopeSpans[0].spans).toEqual([]);
  });
});

describe("exportSpans", () => {
  const payload: OtlpPayload = build([timing()]).payload;

  it("posts OTLP JSON to the collector path under the configured base", async () => {
    const fetchImpl = vi.fn(async () => new Response(null, { status: 200 }));
    const out = await exportSpans("https://otel.example/collector/", payload, fetchImpl as unknown as typeof fetch);
    expect(out).toEqual({ ok: true, sent: 1 });
    const [url, init] = fetchImpl.mock.calls[0] as unknown as [string, RequestInit];
    expect(url).toBe("https://otel.example/collector/v1/traces");
    expect(init.method).toBe("POST");
    expect(JSON.parse(init.body as string)).toEqual(payload);
  });

  it("sends nothing for an empty batch", async () => {
    const fetchImpl = vi.fn();
    expect(await exportSpans("https://otel.example", build([]).payload, fetchImpl as unknown as typeof fetch)).toEqual({ ok: true, sent: 0 });
    expect(fetchImpl).not.toHaveBeenCalled();
  });

  it("rejects relative and non-http endpoints without a request", async () => {
    const fetchImpl = vi.fn();
    const f = fetchImpl as unknown as typeof fetch;
    expect((await exportSpans("/otel", payload, f)).ok).toBe(false);
    expect((await exportSpans("ftp://otel.example", payload, f)).ok).toBe(false);
    expect(fetchImpl).not.toHaveBeenCalled();
  });

  it("reports a collector error status and an unreachable collector", async () => {
    const down = vi.fn(async () => new Response(null, { status: 503 }));
    expect(await exportSpans("https://otel.example", payload, down as unknown as typeof fetch)).toEqual({
      ok: false,
      status: 503,
      error: "collector responded 503",
    });
    const broken = vi.fn(async () => {
      throw new TypeError("network down");
    });
    expect(await exportSpans("https://otel.example", payload, broken as unknown as typeof fetch)).toEqual({
      ok: false,
      error: "collector unreachable",
    });
  });
});
