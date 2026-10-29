import { parseJsonBody, providerRejected, providerUnavailable } from "./wire.js";

export interface PostOptions {
  fetchFn: typeof fetch;
  timeoutMs: number;
}

/**
 * POSTs JSON and returns the parsed body. Transport errors, timeouts and 5xx
 * are an outage (BR-47.9 counts them towards failover). 429 and other 4xx are
 * rejections: the workflow may retry a 429 later, but another provider is not
 * the cure. The message never includes the request, the key or the body.
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
  } catch {
    throw providerUnavailable("provider request failed or timed out");
  }
  let text: string;
  try {
    text = await res.text();
  } catch {
    throw providerUnavailable("provider connection dropped while reading the response");
  }
  if (res.status >= 500) throw providerUnavailable(`provider returned ${res.status}`);
  if (res.status === 429) throw providerRejected("provider rate-limited the request (429)");
  if (res.status >= 400) throw providerRejected(`provider rejected the request with ${res.status}`);
  return parseJsonBody(text);
}
