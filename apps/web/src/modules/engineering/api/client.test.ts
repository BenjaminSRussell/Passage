import { afterEach, describe, expect, it, vi } from "vitest";
import {
  activateRouteVersion,
  ApiError,
  computeLeadTime,
  createBomVersion,
  getBomVersion,
  listBomVersions,
  getCalendarDays,
  getResource,
  getRouteVersion,
  listCapabilities,
  listResourceGroups,
  listResources,
  listRouteVersions,
} from "./client";

afterEach(() => vi.unstubAllGlobals());

function stub(response: object) {
  const fetchMock = vi.fn().mockResolvedValue(response);
  vi.stubGlobal("fetch", fetchMock);
  return fetchMock;
}

describe("engineering client BOM calls", () => {
  it("prefers the problem detail over the status title and keeps field errors", async () => {
    stub({
      ok: false,
      status: 400,
      json: async () => ({ code: "COM-0001", title: "Bad Request", detail: "request failed validation", errors: { productId: "must be 1 to 64 characters", bad: 3 } }),
    });
    const error = await createBomVersion({ productId: "", effectiveFrom: "", lines: [] }, "k").catch((e: unknown) => e);
    expect(error).toBeInstanceOf(ApiError);
    expect(error).toMatchObject({ message: "request failed validation", status: 400, code: "COM-0001", errors: { productId: "must be 1 to 64 characters" } });
  });


  it("encodes list filters, reads one version and posts with an idempotency key", async () => {
    const fetchMock = stub({ ok: true, json: async () => ({ items: [] }) });
    await listBomVersions({ productId: "A B", siteId: "", status: "draft", asOf: "2026-06-01" }, "c9");
    expect(fetchMock).toHaveBeenLastCalledWith("/api/v1/engineering/bom-versions?productId=A+B&status=draft&asOf=2026-06-01&cursor=c9");
    await getBomVersion("x/y");
    expect(fetchMock).toHaveBeenLastCalledWith("/api/v1/engineering/bom-versions/x%2Fy");
    await createBomVersion({ productId: "P", effectiveFrom: "2026-01-01", lines: [] }, "key-1");
    const init = fetchMock.mock.calls.at(-1)?.[1] as RequestInit;
    expect(init.method).toBe("POST");
    expect(new Headers(init.headers).get("Idempotency-Key")).toBe("key-1");
  });
});

describe("engineering client", () => {
  it("encodes filters and cursor and omits empty filters", async () => {
    const fetchMock = stub({ ok: true, json: async () => ({ items: [] }) });
    await listRouteVersions({ productId: "P 1", siteId: "", status: "active", asOf: "" }, "c1");
    expect(fetchMock).toHaveBeenCalledWith("/api/v1/engineering/route-versions?productId=P+1&status=active&cursor=c1");
    await listRouteVersions({});
    expect(fetchMock).toHaveBeenLastCalledWith("/api/v1/engineering/route-versions");
  });

  it("requests a route version by encoded id", async () => {
    const fetchMock = stub({ ok: true, json: async () => ({}) });
    await getRouteVersion("a/b");
    expect(fetchMock).toHaveBeenCalledWith("/api/v1/engineering/route-versions/a%2Fb");
  });

  it("posts the activation to the encoded id and returns the version", async () => {
    const fetchMock = stub({ ok: true, json: async () => ({ id: "a/b", status: "active" }) });
    expect(await activateRouteVersion("a/b")).toEqual({ id: "a/b", status: "active" });
    expect(fetchMock).toHaveBeenCalledWith("/api/v1/engineering/route-versions/a%2Fb:activate", { method: "POST" });
  });

  it("surfaces the code and status of a refused activation", async () => {
    stub({ ok: false, status: 409, json: async () => ({ code: "ENG-ROUTE-OVERLAP", title: "Conflict" }) });
    expect(await activateRouteVersion("x").catch((e: unknown) => e)).toMatchObject({ code: "ENG-ROUTE-OVERLAP", message: "Conflict", status: 409 });
  });

  it("posts the lead-time request as JSON", async () => {
    const fetchMock = stub({ ok: true, json: async () => ({ hours: "8", calendarDays: 1 }) });
    const body = { operations: [{ no: 10, processQty: "1" }], quantity: "5", hoursPerDay: "8" };
    expect(await computeLeadTime(body)).toEqual({ hours: "8", calendarDays: 1 });
    const [url, init] = fetchMock.mock.calls[0];
    expect(url).toBe("/api/v1/engineering/routes:lead-time");
    expect(init.method).toBe("POST");
    expect(JSON.parse(init.body)).toEqual(body);
  });

  it("surfaces the problem code and title of a problem document", async () => {
    stub({ ok: false, status: 404, json: async () => ({ code: "ENG-ROUTE-NOTFOUND", title: "Route version not found" }) });
    const error = await getRouteVersion("x").catch((e: unknown) => e);
    expect(error).toBeInstanceOf(ApiError);
    expect(error).toMatchObject({ code: "ENG-ROUTE-NOTFOUND", message: "Route version not found", status: 404 });
  });

  it("falls back to a generic message for a non-JSON failure", async () => {
    stub({
      ok: false,
      status: 502,
      json: async () => {
        throw new SyntaxError("not json");
      },
    });
    const error = await listRouteVersions({}).catch((e: unknown) => e);
    expect(error).toMatchObject({ message: "Request failed", status: 502, code: undefined });
  });

  it("serialises resource filters and cursor and omits empty filters", async () => {
    const fetchMock = stub({ ok: true, json: async () => ({ items: [] }) });
    await listResources({ type: "machine", siteId: "", groupId: "g 1", capabilityId: "" }, "c1");
    expect(fetchMock).toHaveBeenLastCalledWith("/api/v1/engineering/resources?type=machine&groupId=g+1&cursor=c1");
    await listResources({});
    expect(fetchMock).toHaveBeenLastCalledWith("/api/v1/engineering/resources");
  });

  it("requests a resource by encoded id", async () => {
    const fetchMock = stub({ ok: true, json: async () => ({}) });
    await getResource("a/b?c");
    expect(fetchMock).toHaveBeenCalledWith("/api/v1/engineering/resources/a%2Fb%3Fc");
  });

  it("serialises group and capability list queries", async () => {
    const fetchMock = stub({ ok: true, json: async () => ({ items: [] }) });
    await listResourceGroups({ siteId: "s1" }, "c2");
    expect(fetchMock).toHaveBeenLastCalledWith("/api/v1/engineering/resource-groups?siteId=s1&cursor=c2");
    await listResourceGroups({});
    expect(fetchMock).toHaveBeenLastCalledWith("/api/v1/engineering/resource-groups");
    await listCapabilities("c3");
    expect(fetchMock).toHaveBeenLastCalledWith("/api/v1/engineering/capabilities?cursor=c3");
    await listCapabilities();
    expect(fetchMock).toHaveBeenLastCalledWith("/api/v1/engineering/capabilities");
  });

  it("requests calendar days for an encoded id and range", async () => {
    const fetchMock = stub({ ok: true, json: async () => ({ items: [] }) });
    await getCalendarDays("c/1", "2026-10-01", "2026-10-31");
    expect(fetchMock).toHaveBeenCalledWith("/api/v1/engineering/calendars/c%2F1/days?from=2026-10-01&to=2026-10-31");
  });

  it("turns a problem response from every new call into an ApiError", async () => {
    stub({ ok: false, status: 422, json: async () => ({ code: "ENG-INVALID-QUERY", title: "Invalid query" }) });
    const calls = [
      () => listResources({}),
      () => getResource("x"),
      () => listResourceGroups({}),
      () => listCapabilities(),
      () => getCalendarDays("x", "a", "b"),
    ];
    for (const call of calls) {
      const error = await call().catch((e: unknown) => e);
      expect(error).toBeInstanceOf(ApiError);
      expect(error).toMatchObject({ code: "ENG-INVALID-QUERY", message: "Invalid query", status: 422 });
    }
  });
});
