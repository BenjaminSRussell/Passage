import type { Upstream, UpstreamRequest, UpstreamResponse } from "./gateway.js";

/**
 * HTTP upstream to the Passage API. Path parameters are filled from args;
 * GET and DELETE send the rest as query string, other methods as JSON body.
 * ifMatch and idempotencyKey map to their headers and are not sent in the body.
 */
export function createFetchUpstream(
  baseUrl: string,
  timeoutMs = 60_000,
  fetchFn: typeof fetch = fetch,
): Upstream {
  return {
    async send({ tool, args, headers }: UpstreamRequest): Promise<UpstreamResponse> {
      const rest: Record<string, unknown> = { ...args };
      const path = tool.path.replace(/\{([^}]+)\}/g, (_match, key: string) => {
        const value = rest[key];
        delete rest[key];
        if (value === undefined || value === null || value === "") {
          throw new Error(`path parameter ${key} is missing for ${tool.name}`);
        }
        return encodeURIComponent(String(value));
      });
      const reqHeaders: Record<string, string> = { ...headers, accept: "application/json" };
      if (typeof rest.ifMatch === "string") reqHeaders["if-match"] = rest.ifMatch;
      if (typeof rest.idempotencyKey === "string") reqHeaders["idempotency-key"] = rest.idempotencyKey;
      delete rest.ifMatch;
      delete rest.idempotencyKey;

      const url = new URL(path, baseUrl);
      const init: RequestInit = { method: tool.method, headers: reqHeaders, signal: AbortSignal.timeout(timeoutMs) };
      if (tool.method === "GET" || tool.method === "DELETE") {
        for (const [k, v] of Object.entries(rest)) {
          if (v === undefined || v === null) continue;
          url.searchParams.set(k, typeof v === "object" ? JSON.stringify(v) : String(v));
        }
      } else {
        reqHeaders["content-type"] = "application/json";
        init.body = JSON.stringify(rest);
      }

      const res = await fetchFn(url, init);
      const text = await res.text();
      return { status: res.status, body: parseBody(text) };
    },
  };
}

function parseBody(text: string): unknown {
  if (text === "") return null;
  try {
    return JSON.parse(text) as unknown;
  } catch {
    return text;
  }
}
