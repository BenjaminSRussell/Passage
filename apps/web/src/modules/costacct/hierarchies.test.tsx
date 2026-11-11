import { findAlert, findTable } from "../../test-utils";
import { fireEvent, render, screen, waitFor, within } from "@testing-library/react";
import { createElement } from "react";
import { MemoryRouter, Route, Routes } from "react-router-dom";
import { afterEach, describe, expect, it, vi } from "vitest";
import { checkAxeAA } from "../../shell/a11y";
import type { CostElement, CostObject, DistributionRun, Hierarchy } from "./api/client";
import { CreateHierarchyPage, DistributionRunDetailPage, HierarchiesPage, HierarchyDetailPage } from "./HierarchiesPage";
import { collectError, nodeTree, previewHierarchy, validateDistribution } from "./logic";
import costacct from "./module";

const shellRules = new Set(["page-has-heading-one", "landmark-unique", "region", "bypass"]);
const pageViolations = (container: HTMLElement) => checkAxeAA(container).filter((v) => !shellRules.has(v.id));

const LE = "11111111-1111-4111-8111-111111111111";
const H = "22222222-2222-4222-8222-222222222222";
const PG = "33333333-3333-4333-8333-333333333333";
const PA = "44444444-4444-4444-8444-444444444444";
const PB = "55555555-5555-4555-8555-555555555555";
const ROOT = "66666666-6666-4666-8666-666666666666";
const NA = "77777777-7777-4777-8777-777777777777";
const NB = "88888888-8888-4888-8888-888888888888";
const EL = "99999999-9999-4999-8999-999999999999";
const RUN = "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa";
const PO = "bbbbbbbb-bbbb-4bbb-8bbb-bbbbbbbbbbbb";

const objects: CostObject[] = [
  { id: PG, legalEntityId: LE, kind: "Product", code: "PG-PUMPS", name: "Pumps" },
  { id: PA, legalEntityId: LE, kind: "Product", code: "P-100", name: "Pump 100" },
  { id: PB, legalEntityId: LE, kind: "Product", code: "P-200", name: "Pump 200" },
  { id: PO, legalEntityId: LE, kind: "InternalOrder", code: "PO-7781", name: "Order 7781" },
];
const elements: CostElement[] = [{ id: EL, legalEntityId: LE, kind: "Primary", code: "OVERHEAD", name: "Overhead", mainAccountFrom: "650000", mainAccountTo: "659999" }];
const hierarchy: Hierarchy = {
  id: H,
  legalEntityId: LE,
  code: "PUMPS",
  name: "Pump products",
  type: "Collective",
  nodeCount: 3,
  createdAt: "2026-09-01T08:00:00Z",
  nodes: [
    { id: ROOT, hierarchyId: H, costObjectId: PG, collects: true, distributionBasis: "FixedPercent", createdAt: "2026-09-01T08:00:00Z" },
    { id: NA, hierarchyId: H, parentNodeId: ROOT, costObjectId: PA, collects: false, createdAt: "2026-09-01T08:01:00Z" },
    { id: NB, hierarchyId: H, parentNodeId: ROOT, costObjectId: PB, collects: false, createdAt: "2026-09-01T08:02:00Z" },
  ],
};
const run: DistributionRun = {
  id: RUN,
  legalEntityId: LE,
  hierarchyId: H,
  nodeId: ROOT,
  costElementId: EL,
  period: "2026-09",
  currency: "USD",
  basis: "FixedPercent",
  amount: "900",
  distributed: "810",
  residual: "90",
  shares: [
    { nodeId: NA, costObjectId: PA, basis: "60", amount: "540" },
    { nodeId: NB, costObjectId: PB, basis: "30", amount: "270" },
  ],
  warnings: [{ code: "CO-0645", detail: `Hierarchy node ${ROOT} keeps 90.00 USD undistributed` }],
  status: "Completed",
  createdAt: "2026-10-01T08:00:00Z",
};

type Handler = (url: string, init: RequestInit) => Response | Promise<Response>;
const json = (status: number, body: unknown) => new Response(JSON.stringify(body), { status });

function stubRoutes(routes: Record<string, Handler>) {
  const fetchMock = vi.fn(async (input: RequestInfo | URL, init: RequestInit = {}) => {
    const url = String(input);
    const method = init.method ?? "GET";
    const key = Object.keys(routes)
      .filter((k) => {
        const [m, prefix] = k.split(" ");
        return m === method && url.startsWith(prefix);
      })
      .sort((a, b) => b.length - a.length)[0];
    if (!key) return json(500, { title: "unrouted", code: "TEST-0000", detail: `${method} ${url}` });
    return routes[key](url, init);
  });
  vi.stubGlobal("fetch", fetchMock);
  return fetchMock;
}

function posts(fetchMock: ReturnType<typeof vi.fn>, fragment: string) {
  return (fetchMock.mock.calls as unknown as [string, RequestInit][]).filter(([url, init]) => init?.method === "POST" && url.includes(fragment));
}

function at(path: string) {
  return render(
    createElement(
      MemoryRouter,
      { initialEntries: [path] },
      createElement(
        Routes,
        null,
        createElement(Route, { path: "/costacct/hierarchies", element: createElement(HierarchiesPage) }),
        createElement(Route, { path: "/costacct/hierarchies/new", element: createElement(CreateHierarchyPage) }),
        createElement(Route, { path: "/costacct/hierarchies/:id", element: createElement(HierarchyDetailPage) }),
        createElement(Route, { path: "/costacct/hierarchy-distributions/:id", element: createElement(DistributionRunDetailPage) }),
      ),
    ),
  );
}

const lists = (extra: Record<string, Handler> = {}) =>
  stubRoutes({
    "GET /api/v1/controlling/cost-objects": () => json(200, { items: objects }),
    "GET /api/v1/controlling/cost-elements": () => json(200, { items: elements }),
    "GET /api/v1/controlling/cost-object-hierarchies": () => json(200, { items: [{ ...hierarchy, nodes: undefined }] }),
    [`GET /api/v1/controlling/cost-object-hierarchies/${H}`]: () => json(200, hierarchy),
    [`GET /api/v1/controlling/cost-object-hierarchies/${H}/balances`]: () => json(200, { items: [{ nodeId: ROOT, costElementId: EL, currency: "USD", amount: "900" }] }),
    "GET /api/v1/controlling/hierarchy-distributions": () => json(200, { items: [run] }),
    [`GET /api/v1/controlling/hierarchy-distributions/${RUN}`]: () => json(200, run),
    ...extra,
  });

afterEach(() => vi.unstubAllGlobals());

describe("hierarchy logic", () => {
  it("orders nodes depth first with their depth", () => {
    const rows = nodeTree([
      { id: "c", parentNodeId: "b" },
      { id: "a" },
      { id: "b", parentNodeId: "a" },
      { id: "d", parentNodeId: "a" },
      { id: "orphan", parentNodeId: "gone" },
    ]);
    expect(rows.map((r) => `${r.node.id}:${r.depth}`)).toEqual(["a:0", "b:1", "c:2", "d:1", "orphan:0"]);
  });

  it("previews fixed percentages with the remainder on the node, and proportional bases in full", () => {
    expect(previewHierarchy("900", "FixedPercent", [{ id: "a", basis: "60" }, { id: "b", basis: "30" }])).toEqual({ shares: ["540.00", "270.00"], residual: "90.00" });
    expect(previewHierarchy("10", "FixedPercent", [{ id: "a", basis: "33.33" }, { id: "b", basis: "33.33" }, { id: "c", basis: "33.34" }])).toEqual({
      shares: ["3.33", "3.33", "3.34"],
      residual: "0.00",
    });
    expect(previewHierarchy("100", "ActualOutput", [{ id: "a", basis: "1" }, { id: "b", basis: "1" }, { id: "c", basis: "1" }])).toEqual({
      shares: ["33.34", "33.33", "33.33"],
      residual: "0.00",
    });
    expect(previewHierarchy("50", "StandardCost", [{ id: "a", basis: "0" }])).toEqual({ shares: ["0.00"], residual: "50.00" });
    expect(previewHierarchy("10", "FixedPercent", [{ id: "a", basis: "70" }, { id: "b", basis: "40" }])).toBeNull();
  });

  it("validates collections and distributions", () => {
    expect(collectError("-20.00")).toBeNull();
    expect(collectError("0")).not.toBeNull();
    expect(collectError("1.005")).not.toBeNull();
    const f = { period: "2026-09", currency: "USD", costElementId: EL, basis: "FixedPercent" };
    expect(validateDistribution(f, [{ id: NA, basis: "60" }])).toEqual([]);
    expect(validateDistribution(f, [{ id: NA, basis: "70" }, { id: NB, basis: "40" }])).toEqual(["The percentages total more than 100."]);
    expect(validateDistribution({ ...f, period: "" }, [])).toEqual(["Enter the period as YYYY-MM.", "Choose at least one child node to receive the cost."]);
  });

  it("registers the hierarchy pages behind controlling.view", () => {
    const paths = costacct.routes.map((r) => r.path);
    expect(paths).toEqual(expect.arrayContaining(["/costacct/hierarchies", "/costacct/hierarchies/new", "/costacct/hierarchies/:id", "/costacct/hierarchy-distributions/:id"]));
    expect(costacct.nav.find((n) => n.to === "/costacct/hierarchies")?.duty).toBe("controlling.view");
  });
});

describe("Cost object hierarchies", () => {
  it("lists hierarchies and opens one", async () => {
    const { container } = (lists(), at("/costacct/hierarchies"));
    const table = await findTable({ name: "Cost object hierarchies" });
    expect(await within(table).findByText("Pump products")).toBeInTheDocument();
    expect(pageViolations(container)).toEqual([]);
    fireEvent.click(within(table).getByRole("button", { name: "PUMPS" }));
    expect(await screen.findByRole("heading", { name: "PUMPS Pump products" })).toBeInTheDocument();
  });

  it("shows the empty state and a load error with a retry", async () => {
    let fail = true;
    stubRoutes({ "GET /api/v1/controlling/cost-object-hierarchies": () => (fail ? json(503, { title: "down", code: "COM-0500" }) : json(200, { items: [] })) });
    at("/costacct/hierarchies");
    expect(await findAlert()).toHaveTextContent("Something went wrong on our side");
    fail = false;
    fireEvent.click(screen.getByRole("button", { name: "Try again" }));
    expect(await screen.findByText("No hierarchies yet")).toBeInTheDocument();
  });

  it("validates and creates a hierarchy, then opens it", async () => {
    const fetchMock = lists({ "POST /api/v1/controlling/cost-object-hierarchies": () => json(201, { ...hierarchy, nodes: undefined }) });
    const { container } = at("/costacct/hierarchies/new");
    fireEvent.click(screen.getByRole("button", { name: "Create hierarchy" }));
    expect(await screen.findByText("Enter the legal entity's id (a UUID).")).toBeInTheDocument();
    expect(posts(fetchMock, "cost-object-hierarchies")).toHaveLength(0);
    fireEvent.change(screen.getByLabelText("Legal entity"), { target: { value: LE } });
    fireEvent.change(screen.getByLabelText("Code"), { target: { value: "pumps" } });
    fireEvent.change(screen.getByLabelText("Name"), { target: { value: "Pump products" } });
    expect(pageViolations(container)).toEqual([]);
    fireEvent.click(screen.getByRole("button", { name: "Create hierarchy" }));
    expect(await screen.findByRole("heading", { name: "PUMPS Pump products" })).toBeInTheDocument();
    const [[, init]] = posts(fetchMock, "cost-object-hierarchies");
    expect(JSON.parse(init.body as string)).toEqual({ legalEntityId: LE, code: "PUMPS", name: "Pump products", type: "Collective" });
    expect((init.headers as Record<string, string>)["Idempotency-Key"]).toBeTruthy();
  });

  it("shows the node tree with what each node holds", async () => {
    const { container } = (lists(), at(`/costacct/hierarchies/${H}`));
    const nodes = await findTable({ name: "Nodes" });
    expect(await within(nodes).findByRole("link", { name: "PG-PUMPS" })).toHaveAttribute("href", `/costacct/cost-objects/${PG}`);
    expect(within(nodes).getByText("Fixed percentage")).toBeInTheDocument();
    expect(await within(nodes).findByText("900.00 USD OVERHEAD")).toBeInTheDocument();
    const history = await findTable({ name: "Distribution runs" });
    expect(await within(history).findByText("810.00")).toBeInTheDocument();
    expect(pageViolations(container)).toEqual([]);
  });

  it("adds a node under a parent", async () => {
    const fetchMock = lists({ [`POST /api/v1/controlling/cost-object-hierarchies/${H}/nodes`]: () => json(201, { id: "n" }) });
    at(`/costacct/hierarchies/${H}`);
    await screen.findByRole("option", { name: "PO-7781 Order 7781" });
    fireEvent.click(screen.getByRole("button", { name: "Add node" }));
    expect(await screen.findByText("Choose the cost object for the node.")).toBeInTheDocument();
    fireEvent.change(screen.getByLabelText("Parent node"), { target: { value: NA } });
    fireEvent.change(screen.getByLabelText("Cost object"), { target: { value: PO } });
    // Objects already in the hierarchy are not offered again.
    expect(within(screen.getByLabelText("Cost object")).queryByRole("option", { name: "P-100 Pump 100" })).not.toBeInTheDocument();
    fireEvent.click(screen.getByRole("button", { name: "Add node" }));
    await waitFor(() => expect(posts(fetchMock, "/nodes")).toHaveLength(1));
    expect(JSON.parse(posts(fetchMock, "/nodes")[0][1].body as string)).toEqual({ parentNodeId: NA, costObjectId: PO, collects: false });
  });

  it("collects at a node and relays a correction below zero", async () => {
    let refuse = true;
    const fetchMock = lists({
      [`POST /api/v1/controlling/hierarchy-nodes/${ROOT}:collect`]: () =>
        refuse
          ? json(422, { title: "Unprocessable", code: "CST-0028", detail: "the correction would leave the node at -100.00" })
          : json(201, { entryId: "e", nodeId: ROOT, costObjectId: PG, costElementId: EL, period: "2026-09", currency: "USD", amount: "100", behavior: "Unclassified", balance: "1000" }),
    });
    at(`/costacct/hierarchies/${H}`);
    await screen.findAllByRole("option", { name: "OVERHEAD Overhead" });
    fireEvent.change(screen.getByLabelText("Period"), { target: { value: "2026-09" } });
    fireEvent.change(screen.getByLabelText("Collecting node"), { target: { value: ROOT } });
    fireEvent.change(screen.getAllByLabelText("Cost element")[0], { target: { value: EL } });
    fireEvent.change(screen.getByLabelText("Amount to collect"), { target: { value: "-1000" } });
    fireEvent.click(screen.getByRole("button", { name: "Collect" }));
    expect(await screen.findByText(/would leave the node at -100.00/)).toBeInTheDocument();
    refuse = false;
    fireEvent.change(screen.getByLabelText("Amount to collect"), { target: { value: "100" } });
    fireEvent.click(screen.getByRole("button", { name: "Collect" }));
    expect(await screen.findByText(/the node now holds 1000.00 USD/)).toBeInTheDocument();
    const sent = posts(fetchMock, ":collect");
    expect(JSON.parse(sent[1][1].body as string)).toEqual({ period: "2026-09", currency: "USD", costElementId: EL, amount: "100" });
    const keys = sent.map(([, init]) => (init.headers as Record<string, string>)["Idempotency-Key"]);
    expect(keys[0]).not.toEqual(keys[1]);
  });

  it("previews a distribution, posts it and opens the run", async () => {
    const fetchMock = lists({ "POST /api/v1/controlling/hierarchy-distributions": () => json(201, run) });
    const { container } = at(`/costacct/hierarchies/${H}`);
    await screen.findAllByRole("option", { name: "OVERHEAD Overhead" });
    fireEvent.change(screen.getByLabelText("Period"), { target: { value: "2026-09" } });
    fireEvent.change(screen.getByLabelText("Node to distribute"), { target: { value: ROOT } });
    fireEvent.click(screen.getByRole("button", { name: "Run distribution" }));
    expect(await screen.findByText("Choose the cost element to distribute.")).toBeInTheDocument();
    expect(posts(fetchMock, "hierarchy-distributions")).toHaveLength(0);

    fireEvent.change(screen.getAllByLabelText("Cost element")[1], { target: { value: EL } });
    fireEvent.click(screen.getByLabelText("Include P-100 Pump 100"));
    fireEvent.change(screen.getByLabelText("Percent of P-100 Pump 100"), { target: { value: "60" } });
    fireEvent.click(screen.getByLabelText("Include P-200 Pump 200"));
    fireEvent.change(screen.getByLabelText("Percent of P-200 Pump 200"), { target: { value: "30" } });
    const grid = await findTable({ name: "Distribution basis" });
    expect(await within(grid).findByText("540.00")).toBeInTheDocument();
    expect(screen.getByText("Stays on the node: 90.00")).toBeInTheDocument();
    expect(pageViolations(container)).toEqual([]);

    fireEvent.click(screen.getByRole("button", { name: "Run distribution" }));
    expect(await screen.findByRole("heading", { name: "Distribution 2026-09 from PG-PUMPS" })).toBeInTheDocument();
    expect(JSON.parse(posts(fetchMock, "hierarchy-distributions")[0][1].body as string)).toEqual({
      nodeId: ROOT,
      period: "2026-09",
      currency: "USD",
      costElementId: EL,
      children: [
        { nodeId: NA, basis: "60" },
        { nodeId: NB, basis: "30" },
      ],
    });
  });

  it("shows a run's shares and the CO-0645 residual", async () => {
    const { container } = (lists(), at(`/costacct/hierarchy-distributions/${RUN}`));
    expect(await screen.findByText("Residual kept on the node")).toBeInTheDocument();
    expect(screen.getByText(/CO-0645/)).toBeInTheDocument();
    const shares = screen.getByRole("table", { name: "Shares" });
    expect(await within(shares).findByRole("link", { name: "P-100" })).toHaveAttribute("href", `/costacct/cost-objects/${PA}`);
    expect(within(shares).getByText("270.00")).toBeInTheDocument();
    expect(pageViolations(container)).toEqual([]);
  });

  it("shows a load error for a missing run", async () => {
    stubRoutes({ [`GET /api/v1/controlling/hierarchy-distributions/${RUN}`]: () => json(404, { title: "Not found", code: "CST-0027", detail: "hierarchy distribution run not found" }) });
    at(`/costacct/hierarchy-distributions/${RUN}`);
    expect(await findAlert()).toHaveTextContent("hierarchy distribution run not found");
  });
});
