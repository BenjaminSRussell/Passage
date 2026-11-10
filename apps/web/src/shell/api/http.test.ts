import { afterEach, describe, expect, it, vi } from "vitest";
import { apiFetch, readProblem, SESSION_EXPIRED_EVENT } from "./http";

afterEach(() => vi.unstubAllGlobals());

describe("apiFetch", () => {
  it("goes through globalThis.fetch at call time and tags the request with an id", async () => {
    const fake = vi.fn().mockResolvedValue(new Response("{}", { status: 200 }));
    vi.stubGlobal("fetch", fake);
    await apiFetch("/api/v1/x", { headers: { Accept: "application/json" } });
    const init = fake.mock.calls[0][1] as { headers: Record<string, string> };
    expect(init.headers.Accept).toBe("application/json");
    expect(init.headers["X-Request-Id"]).toMatch(/\S+/);
  });

  it("keeps a caller-supplied request id", async () => {
    const fake = vi.fn().mockResolvedValue(new Response("{}"));
    vi.stubGlobal("fetch", fake);
    await apiFetch("/api/v1/x", { headers: { "x-request-id": "mine" } });
    expect((fake.mock.calls[0][1] as { headers: Record<string, string> }).headers["x-request-id"]).toBe("mine");
  });

  it("puts the request id on a failed response so the error can show it", async () => {
    vi.stubGlobal("fetch", vi.fn().mockResolvedValue(new Response(JSON.stringify({ code: "COM-0500", detail: "boom" }), { status: 500 })));
    const res = await apiFetch("/api/v1/x", { headers: { "X-Request-Id": "req-9" } });
    expect(res.status).toBe(500);
    const problem = await readProblem(res);
    expect(problem.requestId).toBe("req-9");
    expect(problem.code).toBe("COM-0500");
  });

  it("announces a 401 as an ended session", async () => {
    vi.stubGlobal("fetch", vi.fn().mockResolvedValue(new Response("nope", { status: 401 })));
    const seen = vi.fn();
    window.addEventListener(SESSION_EXPIRED_EVENT, seen);
    const res = await apiFetch("/api/v1/x");
    window.removeEventListener(SESSION_EXPIRED_EVENT, seen);
    expect(res.status).toBe(401);
    expect(seen).toHaveBeenCalledOnce();
  });
});
