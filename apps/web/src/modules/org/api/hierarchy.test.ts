import { afterEach, describe, expect, it, vi } from "vitest";
import { OrgApiError } from "./client";
import { compareHierarchyVersions, getHierarchyVersion, listHierarchyTypes, listHierarchyVersions } from "./hierarchy";

const json = (body: unknown, status = 200) => new Response(JSON.stringify(body), { status });

function stub(...replies: Response[]) {
  const queue = [...replies];
  const fetchMock = vi.fn(async (_url: string, _init?: RequestInit) => queue.shift() ?? json({ items: [] }));
  vi.stubGlobal("fetch", fetchMock);
  return fetchMock;
}

const urls = (fetchMock: ReturnType<typeof stub>) => fetchMock.mock.calls.map(([url]) => url);

afterEach(() => vi.unstubAllGlobals());

describe("hierarchy client", () => {
  it("follows nextCursor for types with filters and stops on a repeated cursor", async () => {
    const fetchMock = stub(json({ items: [{ id: "t1" }], nextCursor: "c1" }), json({ items: [{ id: "t2" }], nextCursor: "c1" }));
    const types = await listHierarchyTypes({ purpose: "reporting", status: "active" });
    expect(types.map((type) => type.id)).toEqual(["t1", "t2"]);
    expect(urls(fetchMock)).toEqual([
      "/api/v1/org/hierarchy-types?limit=100&purpose=reporting&status=active",
      "/api/v1/org/hierarchy-types?limit=100&purpose=reporting&status=active&cursor=c1",
    ]);
  });

  it("lists versions of an encoded type id with an optional status and tolerates a missing items array", async () => {
    const fetchMock = stub(json({}), json({ items: [{ id: "v1" }] }));
    expect(await listHierarchyVersions("a/b")).toEqual([]);
    expect(await listHierarchyVersions("t1", "draft")).toEqual([{ id: "v1" }]);
    expect(urls(fetchMock)).toEqual([
      "/api/v1/org/hierarchy-types/a%2Fb/versions?limit=100",
      "/api/v1/org/hierarchy-types/t1/versions?limit=100&status=draft",
    ]);
  });

  it("gets a version with nodes and compares by version number", async () => {
    const fetchMock = stub(json({ id: "v1", nodes: [{ orgUnitId: "o1" }] }), json({ added: null, moved: [{ orgUnitId: "o2" }] }));
    expect((await getHierarchyVersion("v1")).nodes).toHaveLength(1);
    const comparison = await compareHierarchyVersions("t1", 1, 2);
    expect(comparison).toMatchObject({ added: [], removed: [], moved: [{ orgUnitId: "o2" }] });
    expect(urls(fetchMock)).toEqual(["/api/v1/org/hierarchy-versions/v1", "/api/v1/org/hierarchy-types/t1/compare?from=1&to=2"]);
  });

  it("sends no legal-entity header on any read", async () => {
    const fetchMock = stub(json({ items: [] }), json({ items: [] }), json({ id: "v1", nodes: [] }), json({}));
    await listHierarchyTypes();
    await listHierarchyVersions("t1");
    await getHierarchyVersion("v1");
    await compareHierarchyVersions("t1", 1, 2);
    for (const call of fetchMock.mock.calls) {
      const names = [...new Headers(call[1]?.headers).keys()];
      expect(names.filter((name) => name.startsWith("x-passage-legalentity"))).toEqual([]);
    }
  });

  it.each([
    ["hierarchy types", () => listHierarchyTypes(), 403, "AUTH-0003"],
    ["versions", () => listHierarchyVersions("t1"), 500, "COM-0002"],
    ["a version", () => getHierarchyVersion("v1"), 404, "ORG-HIER-VERSION-NOT-FOUND"],
  ])("maps problem+json for %s to OrgApiError", async (_name, call, status, code) => {
    stub(json({ code, detail: "nope", status }, status));
    await expect(call()).rejects.toMatchObject({ status, code, message: "nope" });
  });

  it("falls back to COM-0001 for a non-JSON error body", async () => {
    stub(new Response("boom", { status: 502 }));
    const failure = await compareHierarchyVersions("t1", 1, 2).catch((error: unknown) => error);
    expect(failure).toBeInstanceOf(OrgApiError);
    expect(failure).toMatchObject({ status: 502, code: "COM-0001" });
  });
});
