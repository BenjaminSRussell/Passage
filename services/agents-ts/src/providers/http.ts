import { parseJsonBody, providerRejected, providerUnavailable } from "./wire.js";

export interface PostOptions {
  fetchFn: typeof fetch;
  timeoutMs: number;
}

/** Connection errors that mean the request never reached the provider, so nothing was billed. */
const NOT_CONNECTED = ["ECONNREFUSED", "ENOTFOUND"] as const;

/** Codes and names on the error, its `cause` chain and AggregateError members (undici nests them). */
function errorMarks(err: unknown, depth = 0): string[] {
  if (depth > 5 || typeof err !== "object" || err === null) return [];
  const e = err as { code?: unknown; name?: unknown; cause?: unknown; errors?: unknown };
  const marks: string[] = [];
  if (typeof e.code === "string") marks.push(e.code);
  if (typeof e.name === "string") marks.push(e.name);
  marks.push(...errorMarks(e.cause, depth + 1));
  if (Array.isArray(e.errors)) for (const inner of e.errors) marks.push(...errorMarks(inner, depth + 1));
  return marks;
}

/**
 * POSTs JSON and returns the parsed body. Transport errors, timeouts and 5xx
 * are an outage (BR-47.9 counts them towards failover). 429 and other 4xx are
 * rejections: the workflow may retry a 429 later, but another provider is not
 * the cure. An HTTP error status or a connection that was never made
 * (ECONNREFUSED, ENOTFOUND) is `unbilled`; a timeout, any other transport
 * error or a mid-response drop is not, because the model may have run. The
 * message never includes the request, the key or the body.
 */
export async function postJson(url: string, headers: Record<string, string>, body: unknown, opts: PostOptions): Promise<unknown> {
  let res: Response;
  try {
    res = await opts.fetchFn(url, {
      method: "POST",
      headers: { "content-type": "application/json", ...headers },
      body: JSON.stringify(body),
      signal: AbortSignal.timeout(opts.timeoutMs),
    });
  } catch (err) {
    const marks = errorMarks(err);
    if (marks.includes("TimeoutError")) throw providerUnavailable("provider request timed out");
    const notConnected = NOT_CONNECTED.find((code) => marks.includes(code));
    if (notConnected !== undefined) throw providerUnavailable(`provider connection failed (${notConnected})`, true);
    throw providerUnavailable("provider request failed before a response");
  }
  let text: string;
  try {
    text = await res.text();
  } catch {
    throw providerUnavailable("provider connection dropped while reading the response");
  }
  if (res.status >= 500) throw providerUnavailable(`provider returned ${res.status}`, true);
  if (res.status === 429) throw providerRejected("provider rate-limited the request (429)", true);
  if (res.status >= 400) throw providerRejected(`provider rejected the request with ${res.status}`, true);
  return parseJsonBody(text);
}
