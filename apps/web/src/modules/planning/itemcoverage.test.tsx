import { findTable } from "../../test-utils";
import { fireEvent, render, screen, waitFor, within } from "@testing-library/react";
import { MemoryRouter, Route, Routes, useLocation } from "react-router-dom";
import { afterEach, describe, expect, it, vi } from "vitest";
import { COVERAGE_DEFAULTS, type CoverageGroup, type ItemCoverage } from "./api/client";
import { compareQty, draftOf, EMPTY_DRAFT, validateItemCoverage, type ItemCoverageDraft } from "./ItemCoveragePage";
import planning from "./module";

const ITEM = "00000000-0000-0000-0000-0000000000b1";
const SITE = "00000000-0000-0000-0000-0000000000a1";
const WH = "00000000-0000-0000-0000-0000000000d1";

const req: CoverageGroup = { ...COVERAGE_DEFAULTS, id: "00000000-0000-0000-0000-0000000000c1", code: "REQ", version: 1 };
const minmax: CoverageGroup = { ...COVERAGE_DEFAULTS, id: "00000000-0000-0000-0000-0000000000c2", code: "MIN", coverageCode: "minmax", version: 1 };

const siteCoverage: ItemCoverage = {
  id: "00000000-0000-0000-0000-0000000000f1",
  version: 1,
  itemId: ITEM,
  siteId: SITE,
  warehouseId: null,
  coverageGroupId: req.id,
  coverageGroupCode: "REQ",
  minQty: "100.500000",
  maxQty: "0.000000",
  safetyMarginReceiptDays: 0,
  safetyMarginIssueDays: 0,
  safetyMarginReorderDays: 0,
  leadTimeDays: null,
  vendorId: null,
  plannedOrderType: "purchase",
  planningStrategy: "mts-net",
  planningScope: "site",
  decouplingPoint: false,
  bufferProfileId: null,
  requiredShelfLifeDays: 0,
  priorityBased: false,
};
const warehouseCoverage: ItemCoverage = {
  ...siteCoverage,
  id: "00000000-0000-0000-0000-0000000000f2",
  planningScope: "warehouse",
  warehouseId: WH,
  coverageGroupId: minmax.id,
  coverageGroupCode: "MIN",
  maxQty: "400",
  decouplingPoint: true,
};

const json = (body: unknown, status = 200) => new Response(JSON.stringify(body), { status });
const problem = (status: number, code: string, detail: string) => json({ type: "t", title: "x", status, code, detail }, status);

type Handler = [match: string, respond: (url: string, init?: RequestInit) => Response];

function stubApi(...routes: Handler[]) {
  const fetchMock = vi.fn(async (url: string, init?: RequestInit) => {
    for (const [match, respond] of routes) {
      if (url.includes(match)) return respond(url, init);
    }
    throw new Error(`unexpected fetch ${url}`);
  });
  vi.stubGlobal("fetch", fetchMock);
  return fetchMock;
}

function Location() {
  const location = useLocation();
  return <div data-testid="location">{location.pathname + location.search}</div>;
}

function renderAt(path: string) {
  render(
    <MemoryRouter initialEntries={[path]}>
      <Routes>
        {planning.routes.map((r) => (
          <Route key={r.path} path={r.path} element={r.element} />
        ))}
      </Routes>
      <Location />
    </MemoryRouter>,
  );
}

const groupsRoute: Handler = ["/coverage-groups", () => json({ items: [minmax, req] })];

afterEach(() => vi.unstubAllGlobals());

describe("validateItemCoverage", () => {
  const draft = (over: Partial<ItemCoverageDraft>): ItemCoverageDraft => ({ ...EMPTY_DRAFT, itemId: ITEM, siteId: SITE, coverageGroupId: req.id, ...over });

  it("turns a valid draft into the request body with nulls for blank optional fields", () => {
    const { errors, input } = validateItemCoverage(draft({ minQty: "10.5", maxQty: "40", leadTimeDays: "" }));
    expect(errors).toEqual({});
    expect(input).toEqual({
      itemId: ITEM,
      siteId: SITE,
      coverageGroupId: req.id,
      planningScope: "site",
      warehouseId: null,
      vendorId: null,
      bufferProfileId: null,
      minQty: "10.5",
      maxQty: "40",
      plannedOrderType: "purchase",
      planningStrategy: "mts-net",
      decouplingPoint: false,
      priorityBased: false,
      safetyMarginReceiptDays: 0,
      safetyMarginIssueDays: 0,
      safetyMarginReorderDays: 0,
      leadTimeDays: null,
      requiredShelfLifeDays: 0,
    });
  });

  it("names every rule the service enforces", () => {
    const { errors, input } = validateItemCoverage({
      ...EMPTY_DRAFT,
      itemId: "FG-100",
      planningScope: "subcontractor",
      minQty: "1.0000001",
      maxQty: "-1",
      safetyMarginReceiptDays: "366",
      safetyMarginIssueDays: "-1",
      safetyMarginReorderDays: "1.5",
      leadTimeDays: "2.5",
      bufferProfileId: "BP-1",
    });
    expect(input).toBeNull();
    expect(Object.keys(errors).sort()).toEqual([
      "bufferProfileId",
      "coverageGroupId",
      "itemId",
      "leadTimeDays",
      "maxQty",
      "minQty",
      "plannedOrderType",
      "safetyMarginIssueDays",
      "safetyMarginReceiptDays",
      "safetyMarginReorderDays",
      "siteId",
      "vendorId",
    ]);
  });

  it("needs the warehouse for a warehouse scope and keeps a maximum of 0 or at least the minimum", () => {
    expect(validateItemCoverage(draft({ planningScope: "warehouse" })).errors).toEqual({ warehouseId: expect.any(String) });
    expect(validateItemCoverage(draft({ minQty: "100", maxQty: "99.999999" })).errors).toEqual({ maxQty: expect.any(String) });
    expect(validateItemCoverage(draft({ minQty: "100", maxQty: "0" })).errors).toEqual({});
  });

  it("sends the warehouse only for a warehouse scope", () => {
    expect(validateItemCoverage(draft({ warehouseId: WH })).input?.warehouseId).toBeNull();
    expect(validateItemCoverage(draft({ planningScope: "warehouse", warehouseId: WH })).input?.warehouseId).toBe(WH);
  });

  it("compares decimal strings exactly", () => {
    expect(compareQty("100", "99.999999")).toBe(1);
    expect(compareQty("0100.50", "100.5")).toBe(0);
    expect(compareQty("999999999999.999998", "999999999999.999999")).toBe(-1);
  });

  it("round-trips a stored coverage through the draft", () => {
    expect(validateItemCoverage(draftOf(warehouseCoverage)).input).toMatchObject({ warehouseId: WH, minQty: "100.5", maxQty: "400", decouplingPoint: true });
  });
});

describe("Item coverage list", () => {
  it("lists coverages with scope, group and limits, filters by item and opens one", async () => {
    const fetchMock = stubApi(
      [`/item-coverages/${siteCoverage.id}`, () => json(siteCoverage)],
      groupsRoute,
      ["/item-coverages", () => json({ items: [siteCoverage, warehouseCoverage] })],
    );
    renderAt("/planning/item-coverages");
    expect(screen.getByRole("heading", { level: 2, name: "Item coverage" })).toBeInTheDocument();
    const table = await findTable({ name: "Item coverage" });
    await waitFor(() => expect(within(table).getByText("MIN")).toBeInTheDocument());
    expect(within(table).getByText(`Warehouse ${WH}`)).toBeInTheDocument();
    expect(within(table).getAllByText("100.5")).toHaveLength(2);
    expect(within(table).getByText("No maximum")).toBeInTheDocument();
    expect(within(table).getByText("Decoupling point")).toBeInTheDocument();

    fireEvent.change(screen.getByLabelText("Item"), { target: { value: "FG-100" } });
    fireEvent.click(screen.getByRole("button", { name: "Show coverage" }));
    expect(await screen.findByText(/Enter the item id as a UUID/)).toBeInTheDocument();
    fireEvent.change(screen.getByLabelText("Item"), { target: { value: ITEM } });
    fireEvent.click(screen.getByRole("button", { name: "Show coverage" }));
    await waitFor(() => expect(fetchMock.mock.calls.some(([url]) => String(url).includes(`itemId=${ITEM}`))).toBe(true));

    fireEvent.click(within(await findTable({ name: "Item coverage" })).getAllByRole("button", { name: new RegExp(ITEM) })[0]);
    expect(screen.getByTestId("location")).toHaveTextContent(`/planning/item-coverages/${siteCoverage.id}`);
  });

  it("shows the empty state", async () => {
    stubApi(["/item-coverages", () => json({ items: [] })]);
    renderAt("/planning/item-coverages");
    expect(await screen.findByText("No item coverage yet")).toBeInTheDocument();
  });

  it("shows the service error instead of the table", async () => {
    stubApi(["/item-coverages", () => problem(403, "AUTH-0003", "missing duty planning.read")]);
    renderAt("/planning/item-coverages");
    expect(await screen.findByText("Could not load item coverage")).toBeInTheDocument();
    expect(screen.getByText(/AUTH-0003/)).toBeInTheDocument();
    expect(screen.queryByRole("table")).not.toBeInTheDocument();
  });
});

describe("New item coverage page", () => {
  it("validates before sending, then creates with an Idempotency-Key and opens the coverage", async () => {
    const fetchMock = stubApi(
      [`/item-coverages/${warehouseCoverage.id}`, () => json(warehouseCoverage)],
      ["/item-coverages", () => json(warehouseCoverage, 201)],
      groupsRoute,
    );
    renderAt("/planning/item-coverages/new");
    await screen.findByRole("option", { name: "MIN" });
    fireEvent.click(screen.getByRole("button", { name: "Create item coverage" }));
    expect(await screen.findByText("Enter the item id as a UUID.")).toBeInTheDocument();
    expect(fetchMock.mock.calls.some(([, init]) => init?.method === "POST")).toBe(false);

    fireEvent.change(screen.getByLabelText("Item"), { target: { value: ITEM } });
    fireEvent.change(screen.getByLabelText("Site"), { target: { value: SITE } });
    fireEvent.change(screen.getByLabelText("Coverage group"), { target: { value: minmax.id } });
    fireEvent.change(screen.getByLabelText("Planning scope"), { target: { value: "warehouse" } });
    fireEvent.change(screen.getByLabelText("Warehouse"), { target: { value: WH } });
    fireEvent.change(screen.getByLabelText("Maximum quantity"), { target: { value: "400" } });
    fireEvent.click(screen.getByLabelText("Decoupling point (DDMRP buffer)"));
    fireEvent.click(screen.getByRole("button", { name: "Create item coverage" }));

    await waitFor(() => expect(screen.getByTestId("location")).toHaveTextContent(`/planning/item-coverages/${warehouseCoverage.id}`));
    const post = fetchMock.mock.calls.find(([, init]) => init?.method === "POST");
    expect(post?.[0]).toBe("/api/v1/planning/item-coverages");
    expect((post?.[1]?.headers as Record<string, string>)["Idempotency-Key"]).toBeTruthy();
    expect(JSON.parse(String(post?.[1]?.body))).toMatchObject({
      itemId: ITEM,
      siteId: SITE,
      coverageGroupId: minmax.id,
      planningScope: "warehouse",
      warehouseId: WH,
      maxQty: "400",
      decouplingPoint: true,
    });
  });

  it("switches a subcontractor scope to provision transfers", async () => {
    stubApi(groupsRoute);
    renderAt("/planning/item-coverages/new");
    fireEvent.change(screen.getByLabelText("Planning scope"), { target: { value: "subcontractor" } });
    expect(screen.getByLabelText("Planned order type")).toHaveValue("transfer");
    expect(screen.getByLabelText(/Subcontractor \(vendor\)/)).toBeInTheDocument();
    expect(screen.queryByLabelText("Warehouse")).not.toBeInTheDocument();
    await screen.findByRole("option", { name: "REQ" });
  });

  it("shows a taken scope", async () => {
    stubApi(groupsRoute, ["/item-coverages", () => problem(409, "PLAN-ITEM-COVERAGE-EXISTS", "item already has a site-scope coverage at this site")]);
    renderAt("/planning/item-coverages/new");
    await screen.findByRole("option", { name: "REQ" });
    fireEvent.change(screen.getByLabelText("Item"), { target: { value: ITEM } });
    fireEvent.change(screen.getByLabelText("Site"), { target: { value: SITE } });
    fireEvent.change(screen.getByLabelText("Coverage group"), { target: { value: req.id } });
    fireEvent.click(screen.getByRole("button", { name: "Create item coverage" }));
    expect(await screen.findByText("This item already has a coverage for that site and scope")).toBeInTheDocument();
    expect(screen.getByText(/PLAN-ITEM-COVERAGE-EXISTS/)).toBeInTheDocument();
  });

  it("points to coverage group setup when there is no group", async () => {
    stubApi(["/coverage-groups", () => json({ items: [] })]);
    renderAt("/planning/item-coverages/new");
    expect(await screen.findByText("No coverage groups yet")).toBeInTheDocument();
    expect(screen.getByRole("link", { name: "Create a coverage group" })).toHaveAttribute("href", "/planning/coverage-groups/new");
  });
});

describe("Item coverage page", () => {
  it("edits the coverage under If-Match and shows the new version", async () => {
    const fetchMock = stubApi(
      [
        `/item-coverages/${siteCoverage.id}`,
        (_url, init) => (init?.method === "PUT" ? json({ ...siteCoverage, version: 2, leadTimeDays: 4 }) : json(siteCoverage)),
      ],
      groupsRoute,
    );
    renderAt(`/planning/item-coverages/${siteCoverage.id}`);
    const lead = await screen.findByLabelText(/Lead time override/);
    expect(screen.getByLabelText("Minimum quantity")).toHaveValue("100.5");
    fireEvent.change(lead, { target: { value: "4" } });
    fireEvent.change(screen.getByLabelText(/Issue margin/), { target: { value: "2" } });
    fireEvent.change(screen.getByLabelText(/Reorder margin/), { target: { value: "6" } });
    fireEvent.click(screen.getByRole("button", { name: "Save changes" }));
    expect(await screen.findByText("Saved. Version 2.")).toBeInTheDocument();
    const put = fetchMock.mock.calls.find(([, init]) => init?.method === "PUT");
    expect(put?.[0]).toBe(`/api/v1/planning/item-coverages/${siteCoverage.id}`);
    expect((put?.[1]?.headers as Record<string, string>)["If-Match"]).toBe('"1"');
    expect(JSON.parse(String(put?.[1]?.body))).toMatchObject({
      leadTimeDays: 4,
      safetyMarginIssueDays: 2,
      safetyMarginReorderDays: 6,
      minQty: "100.5",
      maxQty: "0",
      warehouseId: null,
    });
  });

  it("offers a reload when someone else changed the coverage", async () => {
    stubApi(
      [
        `/item-coverages/${siteCoverage.id}`,
        (_url, init) =>
          init?.method === "PUT" ? problem(412, "conflict_version", "the item coverage was changed by another user; current version is 2") : json(siteCoverage),
      ],
      groupsRoute,
    );
    renderAt(`/planning/item-coverages/${siteCoverage.id}`);
    await screen.findByLabelText("Minimum quantity");
    fireEvent.click(screen.getByRole("button", { name: "Save changes" }));
    expect(await screen.findByText("Someone else changed this item coverage")).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "Reload coverage" })).toBeInTheDocument();
  });

  it("shows an unknown coverage", async () => {
    stubApi(["/item-coverages/", () => problem(404, "PLAN-ITEM-COVERAGE-NOT-FOUND", "item coverage not found")]);
    renderAt(`/planning/item-coverages/${siteCoverage.id}`);
    expect(await screen.findByText("Could not load the item coverage")).toBeInTheDocument();
    expect(screen.getByText(/PLAN-ITEM-COVERAGE-NOT-FOUND/)).toBeInTheDocument();
  });
});
