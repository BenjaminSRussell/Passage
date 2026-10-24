import { clickTrace, follow, headersFor, traceparent } from "./telemetry";

describe("click trace", () => {
  it("follows a UI click through gateway, service, db and kafka", () => {
    const trace = clickTrace("audit.open");
    expect(trace.traceId).toHaveLength(32);
    expect(follow(trace)).toEqual(["web-shell", "gateway", "audit", "postgres", "audit-cdc"]);
    const header = headersFor(trace);
    expect(header.traceparent).toBe(traceparent(trace.traceId, trace.spans[0].spanId));
    expect(header.traceparent.startsWith("00-")).toBe(true);
    const services = trace.spans.map((s) => s.service);
    expect(services).toEqual(["web-shell", "gateway", "audit", "postgres", "audit-cdc"]);
    expect(trace.spans[1].parentSpanId).toBe(trace.spans[0].spanId);
    expect(trace.spans[2].parentSpanId).toBe(trace.spans[1].spanId);
    expect(trace.spans[3].parentSpanId).toBe(trace.spans[2].spanId);
    expect(trace.spans[4].parentSpanId).toBe(trace.spans[2].spanId);
  });
});
