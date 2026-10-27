import { afterEach, describe, expect, it, vi } from "vitest";
import { activateRouteVersion, ApiError, computeLeadTime, getRouteVersion, listRouteVersions } from "./client";

afterEach(() => vi.unstubAllGlobals());

function stub(response: object) {
  const fetchMock = vi.fn().mockResolvedValue(response);
  vi.stubGlobal("fetch", fetchMock);
  return fetchMock;
}

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
});
