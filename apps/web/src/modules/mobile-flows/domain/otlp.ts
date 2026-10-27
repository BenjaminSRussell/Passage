// OpenTelemetry spans for step timings (FR-32.11). Pure payload building plus a thin
// OTLP/HTTP JSON exporter. One span per evaluated step: `mobile.step`, with the flow,
// step, device model and outcome as attributes.
//
// Privacy (§6.32.20): spans never carry the user id. Per-user timing is personal data
// and stays in the rollup path behind the duty check. Records outside the telemetry
// retention window are dropped before export.

import { purgeTelemetry, type StepTiming } from "./telemetry";

export const SPAN_NAME = "mobile.step";
export const SCOPE_NAME = "dev.passage.warehouse-mobile";
export const SERVICE_NAME = "passage-warehouse-mobile";

const SPAN_KIND_INTERNAL = 1;
const STATUS_OK = 1;
const STATUS_ERROR = 2;
const NANOS_PER_MS = BigInt(1_000_000);

export type OtlpValue = { stringValue: string } | { intValue: string } | { boolValue: boolean };

export interface OtlpAttribute {
  key: string;
  value: OtlpValue;
}

export interface OtlpSpan {
  traceId: string;
  spanId: string;
  name: string;
  kind: number;
  startTimeUnixNano: string;
  endTimeUnixNano: string;
  attributes: OtlpAttribute[];
  status: { code: number };
}

export interface OtlpPayload {
  resourceSpans: {
    resource: { attributes: OtlpAttribute[] };
    scopeSpans: { scope: { name: string }; spans: OtlpSpan[] }[];
  }[];
}

export interface SpanOptions {
  /** Current time, epoch ms. Used for the retention cut-off. */
  now: number;
  /** Telemetry retention in days (§6.32.13). Defaults to 90. */
  retentionDays?: number;
  /** Device model, e.g. `zebra-tc52`. Added to the resource and each span. */
  deviceModel?: string;
  /** Returns `bytes` random bytes as lowercase hex. Injected for tests. */
  randomHex?: (bytes: number) => string;
}

/** Random lowercase hex from the Web Crypto API, the same source in browsers and Node 20. */
export function cryptoRandomHex(bytes: number): string {
  const buf = new Uint8Array(bytes);
  globalThis.crypto.getRandomValues(buf);
  return Array.from(buf, (b) => b.toString(16).padStart(2, "0")).join("");
}

function str(key: string, value: string): OtlpAttribute {
  return { key, value: { stringValue: value } };
}

/** Converts epoch milliseconds to a nanosecond string (whole milliseconds; the runner records Date.now()). */
function nanos(ms: number): string {
  return (BigInt(Math.round(ms)) * NANOS_PER_MS).toString();
}

/**
 * Builds the OTLP trace payload for the timings inside the retention window. Returns
 * `{error}` when the retention setting is out of range, so a bad config never exports.
 */
export function buildSpans(
  records: readonly StepTiming[],
  opts: SpanOptions,
): { payload: OtlpPayload; spans: number; removed: number } | { error: string } {
  const kept = purgeTelemetry(records, opts.now, opts.retentionDays);
  if ("error" in kept) return kept;
  const random = opts.randomHex ?? cryptoRandomHex;
  const spans: OtlpSpan[] = kept.kept.map((r) => {
    const attributes: OtlpAttribute[] = [str("passage.flow.code", r.flowCode), str("passage.step.id", r.stepId), str("passage.device.id", r.deviceId)];
    if (opts.deviceModel) attributes.push(str("passage.device.model", opts.deviceModel));
    // A StepTiming is recorded when the step ends: `at` is the end, `durationMs` before it the start.
    return {
      traceId: random(16),
      spanId: random(8),
      name: SPAN_NAME,
      kind: SPAN_KIND_INTERNAL,
      startTimeUnixNano: nanos(r.at - r.durationMs),
      endTimeUnixNano: nanos(r.at),
      attributes,
      status: { code: r.failed ? STATUS_ERROR : STATUS_OK },
    };
  });
  const resourceAttributes: OtlpAttribute[] = [str("service.name", SERVICE_NAME)];
  if (opts.deviceModel) resourceAttributes.push(str("passage.device.model", opts.deviceModel));
  return {
    payload: {
      resourceSpans: [{ resource: { attributes: resourceAttributes }, scopeSpans: [{ scope: { name: SCOPE_NAME }, spans }] }],
    },
    spans: spans.length,
    removed: kept.removed,
  };
}

export type ExportResult = { ok: true; sent: number } | { ok: false; status?: number; error: string };

/**
 * POSTs the payload as OTLP/HTTP JSON to `{endpoint}/v1/traces`. An empty batch sends
 * nothing. Only absolute http(s) endpoints are accepted.
 */
export async function exportSpans(endpoint: string, payload: OtlpPayload, fetchImpl: typeof fetch = fetch): Promise<ExportResult> {
  const spanCount = payload.resourceSpans.reduce((n, rs) => n + rs.scopeSpans.reduce((m, ss) => m + ss.spans.length, 0), 0);
  if (spanCount === 0) return { ok: true, sent: 0 };
  let url: URL;
  try {
    url = new URL(`${endpoint.replace(/\/+$/, "")}/v1/traces`);
  } catch {
    return { ok: false, error: "collector endpoint is not an absolute URL" };
  }
  if (url.protocol !== "https:" && url.protocol !== "http:") {
    return { ok: false, error: "collector endpoint must use http or https" };
  }
  try {
    const res = await fetchImpl(url.toString(), {
      method: "POST",
      headers: { "content-type": "application/json" },
      body: JSON.stringify(payload),
    });
    if (!res.ok) return { ok: false, status: res.status, error: `collector responded ${res.status}` };
    return { ok: true, sent: spanCount };
  } catch {
    return { ok: false, error: "collector unreachable" };
  }
}
