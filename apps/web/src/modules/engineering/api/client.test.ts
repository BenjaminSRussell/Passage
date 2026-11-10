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
  listEcrs,
  getEcr,
  createEcr,
  actOnEcr,
  listDocuments,
  signDocument,
  acceptReleaseLine,
  importEbom,
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
    expect(fetchMock).toHaveBeenLastCalledWith("/api/v1/engineering/bom-versions?productId=A+B&status=draft&asOf=2026-06-01&cursor=c9", expect.anything());
    await getBomVersion("x/y");
    expect(fetchMock).toHaveBeenLastCalledWith("/api/v1/engineering/bom-versions/x%2Fy", expect.anything());
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
    expect(fetchMock).toHaveBeenCalledWith("/api/v1/engineering/route-versions?productId=P+1&status=active&cursor=c1", expect.anything());
    await listRouteVersions({});
    expect(fetchMock).toHaveBeenLastCalledWith("/api/v1/engineering/route-versions", expect.anything());
  });

  it("requests a route version by encoded id", async () => {
    const fetchMock = stub({ ok: true, json: async () => ({}) });
    await getRouteVersion("a/b");
    expect(fetchMock).toHaveBeenCalledWith("/api/v1/engineering/route-versions/a%2Fb", expect.anything());
  });

  it("posts the activation to the encoded id and returns the version", async () => {
    const fetchMock = stub({ ok: true, json: async () => ({ id: "a/b", status: "active" }) });
    expect(await activateRouteVersion("a/b")).toEqual({ id: "a/b", status: "active" });
    expect(fetchMock).toHaveBeenCalledWith("/api/v1/engineering/route-versions/a%2Fb:activate", expect.objectContaining({ method: "POST" }));
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
    expect(fetchMock).toHaveBeenLastCalledWith("/api/v1/engineering/resources?type=machine&groupId=g+1&cursor=c1", expect.anything());
    await listResources({});
    expect(fetchMock).toHaveBeenLastCalledWith("/api/v1/engineering/resources", expect.anything());
  });

  it("requests a resource by encoded id", async () => {
    const fetchMock = stub({ ok: true, json: async () => ({}) });
    await getResource("a/b?c");
    expect(fetchMock).toHaveBeenCalledWith("/api/v1/engineering/resources/a%2Fb%3Fc", expect.anything());
  });

  it("serialises group and capability list queries", async () => {
    const fetchMock = stub({ ok: true, json: async () => ({ items: [] }) });
    await listResourceGroups({ siteId: "s1" }, "c2");
    expect(fetchMock).toHaveBeenLastCalledWith("/api/v1/engineering/resource-groups?siteId=s1&cursor=c2", expect.anything());
    await listResourceGroups({});
    expect(fetchMock).toHaveBeenLastCalledWith("/api/v1/engineering/resource-groups", expect.anything());
    await listCapabilities("c3");
    expect(fetchMock).toHaveBeenLastCalledWith("/api/v1/engineering/capabilities?cursor=c3", expect.anything());
    await listCapabilities();
    expect(fetchMock).toHaveBeenLastCalledWith("/api/v1/engineering/capabilities", expect.anything());
  });

  it("requests calendar days for an encoded id and range", async () => {
    const fetchMock = stub({ ok: true, json: async () => ({ items: [] }) });
    await getCalendarDays("c/1", "2026-10-01", "2026-10-31");
    expect(fetchMock).toHaveBeenCalledWith("/api/v1/engineering/calendars/c%2F1/days?from=2026-10-01&to=2026-10-31", expect.anything());
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

  it("calls the ECR endpoints with filters, an idempotency key and the action routes", async () => {
    const fetchMock = stub({ ok: true, status: 200, json: async () => ({ items: [] }) });
    await listEcrs({ state: "approved", productId: "BIKE" }, "c1");
    expect(fetchMock).toHaveBeenLastCalledWith("/api/v1/engineering/ecrs?state=approved&productId=BIKE&cursor=c1", expect.anything());
    await getEcr("a/1");
    expect(fetchMock).toHaveBeenLastCalledWith("/api/v1/engineering/ecrs/a%2F1", expect.anything());
    await createEcr({ number: "E", title: "t", priority: "low", products: ["A"] }, "key-1");
    const [, create] = fetchMock.mock.calls[fetchMock.mock.calls.length - 1];
    expect(new Headers(create?.headers).get("Idempotency-Key")).toBe("key-1");
    await actOnEcr("a", "reject", "no");
    const [url, init] = fetchMock.mock.calls[fetchMock.mock.calls.length - 1];
    expect(url).toBe("/api/v1/engineering/ecrs/a:reject");
    expect(init?.body).toBe('{"reason":"no"}');
    await actOnEcr("a", "submit");
    expect(fetchMock.mock.calls[fetchMock.mock.calls.length - 1][1]?.body).toBe("{}");
  });

  it("signs a document without a client-supplied assertion and accepts a release line", async () => {
    const fetchMock = stub({ ok: true, status: 200, json: async () => ({ id: "d1", status: "released" }) });
    await signDocument("d/1", { printedName: "Carol Kane", method: "password" });
    const [url, init] = fetchMock.mock.calls[0];
    expect(url).toBe("/api/v1/engineering/documents/d%2F1:sign");
    expect(new Headers(init?.headers).get("X-Passage-Reauth-Assertion")).toBeNull();
    expect(init?.body).toBe('{"printedName":"Carol Kane","method":"password"}');
    await acceptReleaseLine("l1");
    expect(fetchMock.mock.calls.at(-1)?.[0]).toBe("/api/v1/engineering/release-lines/l1:accept");
    await listDocuments("DRW-1");
    expect(fetchMock.mock.calls.at(-1)?.[0]).toBe("/api/v1/engineering/documents?number=DRW-1");
    await importEbom({ source: "cad", partNumber: "BIKE", revision: "A", lines: [{ partNumber: "WHEEL", quantity: "1" }] }, "k1");
    expect(new Headers(fetchMock.mock.calls.at(-1)?.[1]?.headers).get("Idempotency-Key")).toBe("k1");
  });
});
