import { findAlert, findStatus, findTable } from "../../test-utils";
import { fireEvent, render, screen, waitFor, within } from "@testing-library/react";
import { MemoryRouter, Route, Routes, useLocation } from "react-router-dom";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { checkAxeAA } from "../../shell/a11y";
import type { AssetDetail, FunctionalLocation, LocationDetail, LocationHistory } from "./api/client";
import { validateLocation } from "./LocationPages";
import eam from "./module";

// Slice 4 screens: plant structure list, new location form, location detail, and the
// location card on the asset (FR-44.1 functional locations, FR-44.2 install/move/remove history).

const PUMP = "0195f0a0-0000-7000-8000-000000000001";
const SITE = "0195f0a0-0000-7000-8000-0000000000aa";
const ROOT = "0195f0a0-0000-7000-8000-0000000000f1";
const LINE = "0195f0a0-0000-7000-8000-0000000000f2";
const BAY = "0195f0a0-0000-7000-8000-0000000000f3";

type Reply = { status: number; body: unknown };
type Handler = (url: URL, init: RequestInit) => Reply | undefined;

const shellRules = new Set(["page-has-heading-one", "landmark-unique", "region", "bypass"]);
const pageViolations = (container: HTMLElement) => checkAxeAA(container).filter((v) => !shellRules.has(v.id));

let handler: Handler;
let calls: { method: string; url: URL; init: RequestInit; body: unknown }[];
const json = (status: number, body: unknown): Reply => ({ status, body });

beforeEach(() => {
  calls = [];
  handler = () => undefined;
  vi.stubGlobal(
    "fetch",
    vi.fn(async (input: string, init: RequestInit = {}) => {
      const url = new URL(input, "http://localhost");
      calls.push({ method: init.method ?? "GET", url, init, body: init.body ? JSON.parse(String(init.body)) : undefined });
      const reply = handler(url, init) ?? json(500, { code: "TEST-500", detail: `no fake for ${init.method ?? "GET"} ${url.pathname}` });
      return new Response(JSON.stringify(reply.body), { status: reply.status, headers: { "Content-Type": "application/json" } });
    }),
  );
});

afterEach(() => {
  vi.unstubAllGlobals();
});

function Where() {
  const location = useLocation();
  return <p data-testid="where">{location.pathname + location.search}</p>;
}

function renderAt(path: string) {
  return render(
    <MemoryRouter initialEntries={[path]}>
      <Routes>
        {eam.routes.map((route) => (
          <Route key={route.path} path={route.path} element={route.element} />
        ))}
      </Routes>
      <Where />
    </MemoryRouter>,
  );
}

const header = (call: { init: RequestInit }, name: string) => new Headers(call.init.headers).get(name);
const isGet = (path: string) => (url: URL, init: RequestInit) => (init.method ?? "GET") === "GET" && url.pathname === `/api/v1/eam${path}`;
const posts = (suffix: string) => calls.filter((c) => c.method === "POST" && c.url.pathname.endsWith(suffix));

const root: FunctionalLocation = { id: ROOT, code: "PLANT-1", name: "Plant 1", siteId: SITE, depth: 1, path: "PLANT-1", createdAt: "2026-10-01T08:00:00Z" };
const line: FunctionalLocation = {
  id: LINE,
  code: "LINE-2",
  name: "Line 2",
  siteId: SITE,
  parentId: ROOT,
  locationType: "LINE",
  depth: 2,
  path: "PLANT-1/LINE-2",
  createdAt: "2026-10-01T08:00:00Z",
};
const bay: FunctionalLocation = { ...line, id: BAY, code: "BAY-3", name: "Bay 3", parentId: LINE, locationType: "POSITION", depth: 3, path: "PLANT-1/LINE-2/BAY-3" };

const pumpDetail: AssetDetail = {
  id: PUMP,
  assetNumber: "AST-1",
  name: "Feed pump",
  assetType: "PUMP",
  siteId: SITE,
  criticality: 3,
  lifecycleState: "ACTIVE",
  version: 4,
  createdAt: "2026-10-01T08:00:00Z",
  counters: [],
  rules: [],
  openWorkOrders: 0,
  partsCost: "0",
  laborCost: "0",
};

describe("location module wiring", () => {
  it("registers the plant structure, the new location form and the location detail, with a nav entry", () => {
    const paths = eam.routes.map((r) => r.path);
    for (const path of ["/eam/locations", "/eam/locations/new", "/eam/locations/:id"]) expect(paths).toContain(path);
    expect(eam.nav.find((n) => n.to === "/eam/locations")).toMatchObject({ label: "Plant structure", duty: "eam.inquire" });
  });
});

describe("location form checks", () => {
  const base = { code: " line-2 ", name: " Line 2 ", siteId: SITE, locationType: "line" };

  it("sends upper-case codes, the parent and the type as the service expects", () => {
    expect(validateLocation(base, ROOT).input).toEqual({ code: "LINE-2", name: "Line 2", siteId: SITE, parentId: ROOT, locationType: "LINE" });
    expect(validateLocation({ ...base, locationType: "" }).input).toEqual({ code: "LINE-2", name: "Line 2", siteId: SITE });
  });

  it("refuses a slash in the code (it joins path segments), a bad site and a bad type", () => {
    const { errors, input } = validateLocation({ code: "A/B", name: "", siteId: "site-1", locationType: "x" });
    expect(input).toBeUndefined();
    expect(Object.keys(errors).sort()).toEqual(["code", "locationType", "name", "siteId"]);
  });
});

describe("plant structure", () => {
  it("lists locations indented by level in path order and opens one", async () => {
    handler = (url, init) => (isGet("/functional-locations")(url, init) ? json(200, { items: [root, line, bay] }) : undefined);
    const { container } = renderAt("/eam/locations");
    const table = await findTable({ name: "Functional locations" });
    const rows = within(table).getAllByRole("row").slice(1);
    expect(rows.map((r) => within(r).getAllByRole("cell")[0].textContent)).toEqual(["PLANT-1", "PLANT-1/LINE-2", "PLANT-1/LINE-2/BAY-3"]);
    expect(within(rows[2]).getByText("PLANT-1/LINE-2/BAY-3")).toHaveStyle("--depth: 3");
    expect(pageViolations(container)).toEqual([]);
    fireEvent.click(within(table).getByRole("button", { name: "PLANT-1/LINE-2" }));
    expect(screen.getByTestId("where")).toHaveTextContent(`/eam/locations/${LINE}`);
  });

  it("filters by site and refuses a site that is not a UUID", async () => {
    handler = (url, init) => (isGet("/functional-locations")(url, init) ? json(200, { items: [] }) : undefined);
    renderAt("/eam/locations");
    expect(await screen.findByText("No functional locations yet")).toBeInTheDocument();
    fireEvent.change(screen.getByLabelText(/Site/), { target: { value: "plant" } });
    fireEvent.click(screen.getByRole("button", { name: "Show locations" }));
    expect(await screen.findByText(/Enter the site id/)).toBeInTheDocument();
    fireEvent.change(screen.getByLabelText(/Site/), { target: { value: SITE } });
    fireEvent.click(screen.getByRole("button", { name: "Show locations" }));
    await waitFor(() => expect(calls.some((c) => c.url.searchParams.get("site") === SITE)).toBe(true));
    expect(await screen.findByText("No locations here")).toBeInTheDocument();
    expect(screen.getByRole("link", { name: "New location" })).toHaveAttribute("href", `/eam/locations/new?site=${SITE}`);
  });

  it("asks the service for one subtree from the detail page link", async () => {
    handler = (url, init) => (isGet("/functional-locations")(url, init) ? json(200, { items: [line, bay] }) : undefined);
    renderAt(`/eam/locations?site=${SITE}&under=${LINE}`);
    await findTable({ name: "Functional locations" });
    const call = calls.find((c) => c.url.pathname === "/api/v1/eam/functional-locations");
    expect(call?.url.searchParams.get("under")).toBe(LINE);
    expect(screen.getByRole("link", { name: "Show every location" })).toHaveAttribute("href", `/eam/locations?site=${SITE}`);
  });

  it("shows the service error with its code", async () => {
    handler = (url, init) => (isGet("/functional-locations")(url, init) ? json(503, { code: "COM-0503", detail: "database unavailable" }) : undefined);
    renderAt("/eam/locations");
    expect(await findAlert()).toHaveTextContent("(COM-0503)");
  });
});

describe("new location", () => {
  it("creates a child in the parent's site with an idempotency key and opens it", async () => {
    handler = (url, init) => {
      if (isGet(`/functional-locations/${ROOT}`)(url, init)) return json(200, { ...root, children: [], assets: [] } satisfies LocationDetail);
      if (init.method === "POST" && url.pathname === "/api/v1/eam/functional-locations") return json(201, line);
      return undefined;
    };
    const { container } = renderAt(`/eam/locations/new?parent=${ROOT}`);
    expect(await screen.findByText("Under PLANT-1, at level 2.")).toBeInTheDocument();
    expect(screen.queryByLabelText(/^Site/)).not.toBeInTheDocument();
    fireEvent.change(screen.getByLabelText("Code"), { target: { value: "line-2" } });
    fireEvent.change(screen.getByLabelText("Name"), { target: { value: "Line 2" } });
    expect(pageViolations(container)).toEqual([]);
    fireEvent.click(screen.getByRole("button", { name: "Create location" }));
    await waitFor(() => expect(screen.getByTestId("where")).toHaveTextContent(`/eam/locations/${LINE}`));
    const [call] = posts("/functional-locations");
    expect(call.body).toEqual({ code: "LINE-2", name: "Line 2", siteId: SITE, parentId: ROOT });
    expect(header(call, "Idempotency-Key")).toBeTruthy();
  });

  it("explains a duplicate path (EAM-DUP-001)", async () => {
    handler = (url, init) =>
      init.method === "POST" && url.pathname === "/api/v1/eam/functional-locations" ? json(409, { code: "EAM-DUP-001", detail: "functional location PLANT-1 already exists at this site" }) : undefined;
    renderAt(`/eam/locations/new?site=${SITE}`);
    fireEvent.change(screen.getByLabelText("Code"), { target: { value: "PLANT-1" } });
    fireEvent.change(screen.getByLabelText("Name"), { target: { value: "Plant 1" } });
    fireEvent.click(screen.getByRole("button", { name: "Create location" }));
    const alert = await findAlert();
    expect(alert).toHaveTextContent("This path already exists at the site");
    expect(alert).toHaveTextContent("(EAM-DUP-001)");
  });

  it("refuses to add under a level-12 location (EAM-FL-001)", async () => {
    handler = (url, init) => (isGet(`/functional-locations/${ROOT}`)(url, init) ? json(200, { ...root, depth: 12, path: "A/B/C/D/E/F/G/H/I/J/K/L", children: [], assets: [] }) : undefined);
    renderAt(`/eam/locations/new?parent=${ROOT}`);
    expect(await screen.findByText(/cannot go deeper \(EAM-FL-001\)/)).toBeInTheDocument();
    expect(screen.queryByRole("button", { name: "Create location" })).not.toBeInTheDocument();
  });
});

describe("location detail", () => {
  it("shows the location, its children and the installed assets", async () => {
    const detail: LocationDetail = { ...line, children: [bay], assets: [{ ...pumpDetail, functionalLocationId: LINE }] };
    handler = (url, init) => (isGet(`/functional-locations/${LINE}`)(url, init) ? json(200, detail) : undefined);
    const { container } = renderAt(`/eam/locations/${LINE}`);
    expect(await screen.findByRole("heading", { name: "Line 2" })).toBeInTheDocument();
    expect(screen.getByText("2 of 12")).toBeInTheDocument();
    expect(within(screen.getByRole("table", { name: "Child locations" })).getByText("PLANT-1/LINE-2/BAY-3")).toBeInTheDocument();
    expect(within(screen.getByRole("table", { name: "Installed assets" })).getByText("Feed pump")).toBeInTheDocument();
    expect(screen.getByRole("link", { name: "Add location under it" })).toHaveAttribute("href", `/eam/locations/new?parent=${LINE}`);
    expect(screen.getByRole("link", { name: "Show its subtree" })).toHaveAttribute("href", `/eam/locations?site=${SITE}&under=${LINE}`);
    expect(pageViolations(container)).toEqual([]);
  });

  it("says when the location is not found", async () => {
    handler = (url, init) => (isGet(`/functional-locations/${LINE}`)(url, init) ? json(404, { code: "EAM-NF-001", detail: "functional location not found" }) : undefined);
    renderAt(`/eam/locations/${LINE}`);
    expect(await screen.findByText("We couldn't find this location")).toBeInTheDocument();
  });
});

describe("asset location card", () => {
  function fakeAsset(detail: () => AssetDetail, history: () => LocationHistory[], extra: Handler = () => undefined) {
    handler = (url, init) => {
      const hit = extra(url, init);
      if (hit) return hit;
      if (isGet(`/assets/${PUMP}`)(url, init)) return json(200, detail());
      if (isGet("/functional-locations")(url, init)) return json(200, { items: [root, line, bay] });
      if (isGet(`/assets/${PUMP}/location-history`)(url, init)) return json(200, { items: history() });
      if (isGet("/kpis")(url, init)) return json(200, { failures: 0, mtbfHours: 0, mttrHours: 0, availability: 1 });
      if (isGet("/work-orders")(url, init)) return json(200, { items: [] });
      for (const part of ["criticality", "warranties", "isolation-points"]) if (isGet(`/assets/${PUMP}/${part}`)(url, init)) return json(200, { items: [] });
      return undefined;
    };
  }

  it("installs the asset at a location of its site with If-Match and shows the history", async () => {
    let asset: AssetDetail = pumpDetail;
    let history: LocationHistory[] = [];
    fakeAsset(
      () => asset,
      () => history,
      (url, init) => {
        if (init.method === "POST" && url.pathname === `/api/v1/eam/assets/${PUMP}/location`) {
          asset = { ...asset, functionalLocationId: LINE, location: line, version: 5 };
          history = [{ id: "h1", assetId: PUMP, action: "INSTALL", toLocationId: LINE, toPath: line.path, actor: "planner-1", at: "2026-10-21T08:00:00Z" }];
          return json(200, asset);
        }
        return undefined;
      },
    );
    renderAt(`/eam/assets/${PUMP}`);
    expect(await screen.findByText("No location changes yet")).toBeInTheDocument();
    await waitFor(() => expect(calls.find((c) => c.url.pathname === "/api/v1/eam/functional-locations")?.url.searchParams.get("site")).toBe(SITE));
    fireEvent.click(await screen.findByRole("button", { name: "Install asset" }));
    expect(await screen.findByText("Choose a location of the asset's site.")).toBeInTheDocument();
    expect(posts("/location")).toHaveLength(0);
    fireEvent.change(screen.getByLabelText("Install at"), { target: { value: LINE } });
    fireEvent.click(screen.getByRole("button", { name: "Install asset" }));
    expect(await findStatus()).toHaveTextContent("Installed at PLANT-1/LINE-2.");
    const [call] = posts("/location");
    expect(call.body).toEqual({ functionalLocationId: LINE });
    expect(header(call, "If-Match")).toBe('"4"');
    expect(header(call, "Idempotency-Key")).toBeTruthy();
    const table = await findTable({ name: "Location history" });
    expect(within(table).getByText("Installed")).toBeInTheDocument();
    expect(within(table).getByText("PLANT-1/LINE-2")).toBeInTheDocument();
  });

  it("removes an installed asset with null and reports a stale version", async () => {
    const installed: AssetDetail = { ...pumpDetail, functionalLocationId: LINE, location: line };
    fakeAsset(
      () => installed,
      () => [{ id: "h1", assetId: PUMP, action: "INSTALL", toLocationId: LINE, toPath: line.path, actor: "planner-1", at: "2026-10-21T08:00:00Z" }],
      (url, init) =>
        init.method === "POST" && url.pathname === `/api/v1/eam/assets/${PUMP}/location` ? json(412, { code: "conflict_version", detail: "asset version changed" }) : undefined,
    );
    renderAt(`/eam/assets/${PUMP}`);
    const move = await screen.findByLabelText("Move to");
    expect(within(move).queryByRole("option", { name: /^PLANT-1\/LINE-2 / })).not.toBeInTheDocument();
    fireEvent.click(screen.getByRole("button", { name: "Remove from location" }));
    const alert = await findAlert();
    expect(alert).toHaveTextContent("Someone else changed this asset");
    expect(alert).toHaveTextContent("(conflict_version)");
    expect(posts("/location")[0].body).toEqual({ functionalLocationId: null });
  });

  it("offers no placement for a disposed asset", async () => {
    fakeAsset(
      () => ({ ...pumpDetail, lifecycleState: "DISPOSED" }),
      () => [],
    );
    renderAt(`/eam/assets/${PUMP}`);
    expect(await screen.findByText("A disposed asset cannot be placed at a location.")).toBeInTheDocument();
    expect(screen.queryByRole("button", { name: "Install asset" })).not.toBeInTheDocument();
  });
});
