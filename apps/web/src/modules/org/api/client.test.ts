import { afterEach, describe, expect, it, vi } from "vitest";
import { listLegalEntities, listSites, OrgApiError, orgFetch } from "./client";

function respond(body: unknown, status = 200) {
  return new Response(typeof body === "string" ? body : JSON.stringify(body), { status });
}

function stubFetch(...responses: Response[]) {
  const fetchMock = vi.fn();
  responses.forEach((r) => fetchMock.mockResolvedValueOnce(r));
  vi.stubGlobal("fetch", fetchMock);
  return fetchMock;
}

const headersOf = (call: unknown[]) => new Headers((call[1] as RequestInit).headers);

afterEach(() => vi.unstubAllGlobals());

describe("orgFetch", () => {
  it("sends the legal entity header for a single scope", async () => {
    const fetchMock = stubFetch(respond({ ok: true }));
    await orgFetch("/sites", {}, "le-1");
    expect(fetchMock.mock.calls[0][0]).toBe("/api/v1/org/sites");
    expect(headersOf(fetchMock.mock.calls[0]).get("X-Passage-LegalEntity")).toBe("le-1");
    expect(headersOf(fetchMock.mock.calls[0]).get("X-Passage-LegalEntity-Scope")).toBeNull();
  });

  it("sends only the scope marker for ALL on GET", async () => {
    const fetchMock = stubFetch(respond({}));
    await orgFetch("/sites", {}, "ALL");
    const headers = headersOf(fetchMock.mock.calls[0]);
    expect(headers.get("X-Passage-LegalEntity")).toBeNull();
    expect(headers.get("X-Passage-LegalEntity-Scope")).toBe("ALL");
  });

  it.each(["POST", "PATCH", "DELETE"])("refuses ALL on %s without calling fetch", async (method) => {
    const fetchMock = stubFetch();
    await expect(orgFetch("/sites", { method }, "ALL")).rejects.toMatchObject({ code: "COM-0001" });
    expect(fetchMock).not.toHaveBeenCalled();
  });

  it.each([undefined, "", 7])("refuses scope %s", async (scope) => {
    const fetchMock = stubFetch();
    await expect(orgFetch("/x", {}, scope as unknown as string)).rejects.toBeInstanceOf(OrgApiError);
    expect(fetchMock).not.toHaveBeenCalled();
  });

  it.each([
    [401, "AUTH-0003"],
    [403, "AUTH-0003"],
    [404, "ORG-LE-NOT-FOUND"],
  ])("maps a %s problem body to OrgApiError with its code", async (status, code) => {
    stubFetch(respond({ type: "t", title: "Nope", status, code, detail: "detail text" }, status));
    await expect(orgFetch("/x", {}, "le-1")).rejects.toMatchObject({ status, code, message: "detail text" });
  });

  it("falls back to COM-0001 for a non-JSON error body", async () => {
    stubFetch(respond("<html>bad gateway</html>", 502));
    await expect(orgFetch("/x", {}, "le-1")).rejects.toMatchObject({ status: 502, code: "COM-0001" });
  });
});

describe("list functions", () => {
  it("follows nextCursor to exhaustion and clamps the page size", async () => {
    const fetchMock = stubFetch(
      respond({ items: [{ id: "a" }], nextCursor: "c1" }),
      respond({ items: [{ id: "b" }], nextCursor: "c2" }),
      respond({ items: [{ id: "c" }] }),
    );
    const page = await listLegalEntities("ALL", { pageSize: 500 });
    expect(page.items.map((i) => i.id)).toEqual(["a", "b", "c"]);
    const urls = fetchMock.mock.calls.map((c) => new URL(c[0] as string, "http://x"));
    expect(urls[0].searchParams.get("limit")).toBe("100");
    expect(urls[0].searchParams.get("cursor")).toBeNull();
    expect(urls[1].searchParams.get("cursor")).toBe("c1");
    expect(urls[2].searchParams.get("cursor")).toBe("c2");
  });

  it("stops on a repeated cursor and tolerates a missing items array", async () => {
    const fetchMock = stubFetch(respond({ nextCursor: "c1" }), respond({ items: [{ id: "a" }], nextCursor: "c1" }));
    const page = await listLegalEntities("ALL");
    expect(page.items).toEqual([{ id: "a" }]);
    expect(fetchMock).toHaveBeenCalledTimes(2);
  });

  it("passes the legal entity id as a filter for a single scope only", async () => {
    const fetchMock = stubFetch(respond({ items: [] }), respond({ items: [] }));
    await listSites("le-9");
    await listSites("ALL");
    expect(new URL(fetchMock.mock.calls[0][0] as string, "http://x").searchParams.get("legalEntityId")).toBe("le-9");
    expect(new URL(fetchMock.mock.calls[1][0] as string, "http://x").searchParams.has("legalEntityId")).toBe(false);
  });
});
