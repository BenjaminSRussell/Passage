/** W3C Trace Context helpers. Wave 4 Kafka is a named span, not an import. */
export interface Span {
  name: string;
  service: string;
  spanId: string;
  parentSpanId?: string;
}

export interface Trace {
  traceId: string;
  spans: Span[];
}

const hex = (bytes: number) =>
  Array.from(crypto.getRandomValues(new Uint8Array(bytes)), (b) => b.toString(16).padStart(2, "0")).join("");

export function newTraceId(): string {
  return hex(16);
}

export function newSpanId(): string {
  return hex(8);
}

export function traceparent(traceId: string, spanId: string): string {
  return `00-${traceId}-${spanId}-01`;
}

/** A UI click followed through gateway → service → DB → Kafka consumer. */
export function clickTrace(action: string, service = "audit"): Trace {
  const traceId = newTraceId();
  const ui = newSpanId();
  const gateway = newSpanId();
  const svc = newSpanId();
  const db = newSpanId();
  const kafka = newSpanId();
  return {
    traceId,
    spans: [
      { name: `ui.click.${action}`, service: "web-shell", spanId: ui },
      { name: "gateway.request", service: "gateway", spanId: gateway, parentSpanId: ui },
      { name: "service.handle", service, spanId: svc, parentSpanId: gateway },
      { name: "db.query", service: "postgres", spanId: db, parentSpanId: svc },
      { name: "kafka.consume", service: `${service}-cdc`, spanId: kafka, parentSpanId: svc },
    ],
  };
}

export function follow(trace: Trace): string[] {
  const root = trace.spans.find((s) => !s.parentSpanId);
  if (!root) {
    return [];
  }
  const path = [root.service];
  const gateway = trace.spans.find((s) => s.parentSpanId === root.spanId);
  if (!gateway) {
    return path;
  }
  path.push(gateway.service);
  const service = trace.spans.find((s) => s.parentSpanId === gateway.spanId);
  if (!service) {
    return path;
  }
  path.push(service.service);
  const db = trace.spans.find((s) => s.parentSpanId === service.spanId && s.service === "postgres");
  const kafka = trace.spans.find((s) => s.parentSpanId === service.spanId && s.name === "kafka.consume");
  if (db) {
    path.push(db.service);
  }
  if (kafka) {
    path.push(kafka.service);
  }
  return path;
}

export function headersFor(trace: Trace): Record<string, string> {
  const root = trace.spans[0];
  return { traceparent: traceparent(trace.traceId, root.spanId) };
}
