import { act, fireEvent, render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { createMemoryRouter, RouterProvider } from "react-router-dom";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import org from "./module";
import { stubLocalStorage } from "./testStorage";

const json = (body: unknown, status = 200) => new Response(JSON.stringify(body), { status });

type Reply = () => Response;

function serve(routes: Record<string, Reply>) {
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

const callsTo = (fetchMock: ReturnType<typeof serve>, path: string) => fetchMock.mock.calls.filter(([url]) => String(url).includes(path));

const summary = (versionNo: number, status: string, effectiveFrom: string | null, effectiveTo: string | null, nodeCount: number) => ({
  id: `v${versionNo}`,
  hierarchyTypeId: "t1",
  versionNo,
  status,
  effectiveFrom,
  effectiveTo,
  nodeCount,
});

const node = (orgUnitId: string, parentOrgUnitId: string | null, sortOrder: number, kind = "operating_unit") => ({
  orgUnitId,
  kind,
  parentOrgUnitId,
  sortOrder,
});

const versionList = [
  summary(3, "draft", null, null, 4),
  summary(2, "published", "2026-06-01", null, 4),
  summary(1, "superseded", "2026-01-01", "2026-05-31", 3),
];
const published = [node("le1", null, 0, "legal_entity"), node("ou-a", "le1", 1), node("ou-b", "le1", 2), node("ou-c", "ou-a", 1)];
const draftNodes = [node("le1", null, 0, "legal_entity"), node("ou-a", "le1", 1), node("ou-b", "le1", 2), node("ou-c", "ou-b", 1)];
const first = [node("le1", null, 0, "legal_entity"), node("ou-a", "le1", 1), node("ou-b", "le1", 2)];

const defaultRoutes: Record<string, Reply> = {
  "GET /legal-entities": () => json({ items: [{ id: "le1", code: "PI", name: "Passage Inc", status: "active" }] }),
  "GET /operating-units": () =>
    json({
      items: [
        { id: "ou-a", code: "ALPHA", name: "Alpha", status: "active" },
        { id: "ou-b", code: "BETA", name: "Beta", status: "active" },
      ],
    }),
  "GET /hierarchy-types?": () => json({ items: [{ id: "t1", code: "REP", name: "Reporting", purposes: ["reporting", "security"], status: "active" }] }),
  "GET /hierarchy-types/t1/versions": () => json({ items: versionList }),
  "GET /hierarchy-types/t1/compare": () => json({ hierarchyTypeId: "t1", fromVersionNo: 2, toVersionNo: 3, added: [], removed: [], moved: [{ orgUnitId: "ou-c", kind: "operating_unit", oldParentOrgUnitId: "ou-a", newParentOrgUnitId: "ou-b" }] }),
  "GET /hierarchy-versions/v1": () => json({ ...versionList[2], nodes: first }),
  "GET /hierarchy-versions/v2": () => json({ ...versionList[1], nodes: published }),
  "GET /hierarchy-versions/v3": () => json({ ...versionList[0], nodes: draftNodes }),
};

function renderPage(overrides: Record<string, Reply> = {}) {
  const fetchMock = serve({ ...defaultRoutes, ...overrides });
  const router = createMemoryRouter(org.routes, { initialEntries: ["/org/hierarchy"] });
  return { fetchMock, user: userEvent.setup(), ...render(<RouterProvider router={router} />) };
}


async function openType(user: ReturnType<typeof userEvent.setup>) {
  await screen.findByRole("option", { name: "REP · Reporting" });
  await user.selectOptions(screen.getByLabelText("Hierarchy type"), "t1");
}

beforeEach(() => void stubLocalStorage());
afterEach(() => vi.unstubAllGlobals());

describe("module wiring", () => {
  it("serves /org/hierarchy and lists Hierarchies in nav", () => {
    expect(org.nav).toContainEqual({ label: "Hierarchies", to: "/org/hierarchy", duty: "org.read" });
  });
});

describe("selection and fetch counts", () => {
  it("loads only the chosen type's versions and the displayed version's nodes, and names once", async () => {
    const { fetchMock, user } = renderPage();
    await openType(user);
    await screen.findByRole("tree");
    expect(screen.getByText("reporting")).toBeInTheDocument();
    expect(screen.getByText("security")).toBeInTheDocument();
    expect(callsTo(fetchMock, "/hierarchy-types?")).toHaveLength(1);
    expect(callsTo(fetchMock, "/hierarchy-types/t1/versions")).toHaveLength(1);
    expect(callsTo(fetchMock, "/hierarchy-versions/")).toHaveLength(1);
    expect(callsTo(fetchMock, "/hierarchy-versions/v2")).toHaveLength(1);
    expect(callsTo(fetchMock, "/operating-units")).toHaveLength(1);
    expect(callsTo(fetchMock, "/legal-entities")).toHaveLength(1);
    for (const [, init] of fetchMock.mock.calls.filter(([url]) => String(url).includes("/hierarchy-"))) {
      expect([...new Headers(init?.headers).keys()].filter((name) => name.startsWith("x-passage-legalentity"))).toEqual([]);
    }
    expect(screen.getAllByRole("row")).toHaveLength(4);
  });

  it("shows the empty state when a type has no versions", async () => {
    const { user } = renderPage({ "GET /hierarchy-types/t1/versions": () => json({ items: [] }) });
    await openType(user);
    expect(await screen.findByText("No versions yet")).toBeInTheDocument();
    expect(screen.queryByRole("tree")).toBeNull();
  });

  it("shows the problem code when versions fail to load", async () => {
    const { user } = renderPage({ "GET /hierarchy-types/t1/versions": () => json({ code: "AUTH-0003", detail: "No duty" }, 403) });
    await openType(user);
    expect(await screen.findByText(/No duty \(AUTH-0003\)/)).toBeInTheDocument();
    expect(screen.queryByRole("tree")).toBeNull();
  });

  it("shows the problem code when the version nodes fail to load", async () => {
    const { user } = renderPage({ "GET /hierarchy-versions/v2": () => json({ code: "ORG-HIER-VERSION-NOT-FOUND", detail: "Gone" }, 404) });
    await openType(user);
    expect(await screen.findByText(/Gone \(ORG-HIER-VERSION-NOT-FOUND\)/)).toBeInTheDocument();
  });
});

describe("cancellation", () => {
  it("aborts in-flight reads when the page unmounts", async () => {
    const { fetchMock, user, unmount } = renderPage({ "GET /hierarchy-versions/v2": () => new Promise<Response>(() => undefined) as never });
    await openType(user);
    await waitFor(() => expect(callsTo(fetchMock, "/hierarchy-versions/v2")).toHaveLength(1));
    const signal = callsTo(fetchMock, "/hierarchy-versions/v2")[0][1]?.signal;
    expect(signal?.aborted).toBe(false);
    unmount();
    expect(signal?.aborted).toBe(true);
  });
});

describe("tree", () => {
  it("renders tree roles, names, codes, child counts and an unknown id", async () => {
    const { user } = renderPage({ "GET /hierarchy-versions/v2": () => json({ ...versionList[1], nodes: [...published, node("ou-x", "le1", 3)] }) });
    await openType(user);
    const tree = await screen.findByRole("tree");
    const root = screen.getByRole("treeitem", { name: "Passage Inc" });
    expect(tree).toContainElement(root);
    expect(root).toHaveAttribute("aria-level", "1");
    expect(root).toHaveAttribute("aria-expanded", "true");
    expect(screen.getByRole("treeitem", { name: "Alpha" })).toHaveAttribute("aria-level", "2");
    expect(screen.getByRole("treeitem", { name: "Beta" })).not.toHaveAttribute("aria-expanded");
    expect(screen.getByText("ALPHA")).toBeInTheDocument();
    expect(screen.getByText("3 children")).toBeInTheDocument();
    expect(screen.getByRole("treeitem", { name: "ou-x" })).toBeInTheDocument();
    expect(screen.queryByRole("button", { name: /edit|drag|publish|add|replace/i })).toBeNull();
    expect(document.querySelector("[draggable]")).toBeNull();
  });

  it("moves with arrow keys and expands and collapses with Right and Left", async () => {
    const { user } = renderPage();
    await openType(user);
    await screen.findByRole("tree");
    const item = (name: string) => screen.getByRole("treeitem", { name });
    act(() => item("Passage Inc").focus());
    await user.keyboard("{ArrowDown}");
    expect(item("Alpha")).toHaveFocus();
    await user.keyboard("{ArrowRight}");
    expect(item("ou-c")).toHaveFocus();
    await user.keyboard("{ArrowLeft}");
    expect(item("Alpha")).toHaveFocus();
    await user.keyboard("{ArrowLeft}");
    expect(item("Alpha")).toHaveAttribute("aria-expanded", "false");
    expect(screen.queryByRole("treeitem", { name: "ou-c" })).toBeNull();
    await user.keyboard("{ArrowDown}");
    expect(item("Beta")).toHaveFocus();
    await user.keyboard("{ArrowUp}{ArrowRight}");
    expect(item("Alpha")).toHaveAttribute("aria-expanded", "true");
  });
});

describe("effective-date preview", () => {
  it("re-selects the version from the date input and the slider, and shows none before the first version", async () => {
    const { fetchMock, user } = renderPage();
    await openType(user);
    await screen.findByRole("tree");
    expect(screen.getByRole("status")).toHaveTextContent("Version 2, effective from 2026-06-01 to open");

    fireEvent.change(screen.getByLabelText("As of date"), { target: { value: "2026-05-31" } });
    expect(screen.getByRole("status")).toHaveTextContent("Version 1, effective from 2026-01-01 to 2026-05-31");
    await waitFor(() => expect(callsTo(fetchMock, "/hierarchy-versions/v1")).toHaveLength(1));

    const slider = screen.getByRole("slider", { name: "Effective date slider" });
    fireEvent.change(slider, { target: { value: "151" } });
    expect(screen.getByRole("status")).toHaveTextContent("Version 2, effective from 2026-06-01");
    expect(slider).toHaveAttribute("aria-valuetext", "2026-06-01");

    fireEvent.change(screen.getByLabelText("As of date"), { target: { value: "2025-12-31" } });
    expect(screen.getByRole("status")).toHaveTextContent("No version in force on this date");
    expect(screen.queryByRole("tree")).toBeNull();
  });

  it("issues no compare request for a first version or while no version is displayed", async () => {
    const { fetchMock, user } = renderPage();
    await openType(user);
    await screen.findByRole("tree");
    await waitFor(() => expect(callsTo(fetchMock, "/compare")).toHaveLength(1));
    fetchMock.mockClear();
    fireEvent.change(screen.getByLabelText("As of date"), { target: { value: "2026-01-15" } });
    await screen.findByRole("treeitem", { name: "Beta" });
    fireEvent.change(screen.getByLabelText("As of date"), { target: { value: "2025-06-01" } });
    expect(callsTo(fetchMock, "/compare")).toHaveLength(0);
    expect(screen.queryByText("changed")).toBeNull();
  });
});

describe("change highlighting (AC-6.9)", () => {
  it("flags the node moved only in the draft and names its previous parent in the tooltip", async () => {
    const { fetchMock, user } = renderPage();
    await openType(user);
    await screen.findByRole("tree");
    fireEvent.change(screen.getByLabelText("As of date"), { target: { value: "2026-06-01" } });
    await user.click(screen.getByRole("checkbox", { name: "Show draft" }));
    expect(screen.getByRole("status")).toHaveTextContent("Version 3 (draft)");
    const moved = await screen.findByTitle("Previously under Alpha (ALPHA)");
    expect(moved).toHaveTextContent("changed");
    expect(screen.getAllByText("changed")).toHaveLength(1);
    const treeItem = screen.getByRole("treeitem", { name: "ou-c" });
    expect(treeItem).toContainElement(moved);
    expect(treeItem).toHaveAccessibleDescription("Previously under Alpha (ALPHA)");
    expect(callsTo(fetchMock, "/compare?from=2&to=3")).toHaveLength(1);
  });
});
