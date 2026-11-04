import { render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { createElement } from "react";
import { createMemoryRouter, RouterProvider } from "react-router-dom";
import { vi } from "vitest";
import { HierarchyDesignerPage } from "./HierarchyDesignerPage";
import { OrgLayout } from "./pages";

export const json = (body: unknown, status = 200) => new Response(JSON.stringify(body), { status });

export type Reply = () => Response;

export function serve(routes: Record<string, Reply>) {
  const fetchMock = vi.fn(async (url: string, init?: RequestInit) => {
    const key = `${init?.method ?? "GET"} ${url.replace("/api/v1/org", "")}`;
    const match = Object.keys(routes)
      .sort((a, b) => b.length - a.length)
      .find((prefix) => key.startsWith(prefix));
    return match ? routes[match]() : json({ items: [] });
  });
  vi.stubGlobal("fetch", fetchMock);
  return fetchMock;
}

export const callsTo = (fetchMock: ReturnType<typeof serve>, path: string, method?: string) =>
  fetchMock.mock.calls.filter(([url, init]) => String(url).includes(path) && (method === undefined || (init?.method ?? "GET") === method));

export const bodyOf = (call: readonly unknown[]) => JSON.parse(String((call[1] as RequestInit).body));

export const summary = (versionNo: number, status: string, effectiveFrom: string | null, effectiveTo: string | null, nodeCount: number) => ({
  id: `v${versionNo}`,
  hierarchyTypeId: "t1",
  versionNo,
  status,
  effectiveFrom,
  effectiveTo,
  nodeCount,
});

export const node = (orgUnitId: string, parentOrgUnitId: string | null, sortOrder: number, kind = "operating_unit") => ({
  orgUnitId,
  kind,
  parentOrgUnitId,
  sortOrder,
});

export const versionList = [
  summary(3, "draft", null, null, 4),
  summary(2, "published", "2026-06-01", null, 4),
  summary(1, "superseded", "2026-01-01", "2026-05-31", 3),
];
export const published = [node("le1", null, 0, "legal_entity"), node("ou-a", "le1", 1), node("ou-b", "le1", 2), node("ou-c", "ou-a", 1)];
export const draftNodes = [node("le1", null, 0, "legal_entity"), node("ou-a", "le1", 1), node("ou-b", "le1", 2), node("ou-c", "ou-b", 1)];
export const first = [node("le1", null, 0, "legal_entity"), node("ou-a", "le1", 1), node("ou-b", "le1", 2)];

export const defaultRoutes: Record<string, Reply> = {
  "GET /legal-entities": () => json({ items: [{ id: "le1", code: "PI", name: "Passage Inc", status: "active" }] }),
  "GET /operating-units": () =>
    json({
      items: [
        { id: "ou-a", code: "ALPHA", name: "Alpha", status: "active" },
        { id: "ou-b", code: "BETA", name: "Beta", status: "active" },
      ],
    }),
  "GET /hierarchy-types?": () => json({ items: [{ id: "t1", code: "REP", name: "Reporting", purposes: ["reporting", "security"], backdateAllowed: true, status: "active" }] }),
  "GET /hierarchy-types/t1/versions": () => json({ items: versionList }),
  "GET /hierarchy-types/t1/compare": () => json({ hierarchyTypeId: "t1", fromVersionNo: 2, toVersionNo: 3, added: [], removed: [], moved: [{ orgUnitId: "ou-c", kind: "operating_unit", oldParentOrgUnitId: "ou-a", newParentOrgUnitId: "ou-b" }] }),
  "GET /hierarchy-versions/v1": () => json({ ...versionList[2], version: 1, nodes: first }),
  "GET /hierarchy-versions/v2": () => json({ ...versionList[1], version: 4, nodes: published }),
  "GET /hierarchy-versions/v3": () => json({ ...versionList[0], version: 7, nodes: draftNodes }),
};

export const allDuties = ["org.read", "org.hierarchy.write", "org.hierarchy.publish", "org.hierarchy.backdate", "tenant.administer"];

export function renderPage(overrides: Record<string, Reply> = {}, duties: string[] = allDuties) {
  const fetchMock = serve({ ...defaultRoutes, ...overrides });
  const router = createMemoryRouter(
    [{ element: createElement(OrgLayout, { duties }), children: [{ path: "/org/hierarchy", element: createElement(HierarchyDesignerPage) }, { path: "/org", element: <p>Org home</p> }] }],
    { initialEntries: ["/org/hierarchy"] },
  );
  return { fetchMock, router, user: userEvent.setup(), ...render(<RouterProvider router={router} />) };
}

export async function openType(user: ReturnType<typeof userEvent.setup>) {
  await screen.findByRole("option", { name: "REP · Reporting" });
  await user.selectOptions(screen.getByLabelText("Hierarchy type"), "t1");
}
