import { findAlert, findStatus, findTable } from "../../test-utils";
import { fireEvent, render, screen, waitFor, within } from "@testing-library/react";
import { createElement } from "react";
import { MemoryRouter, Route, Routes } from "react-router-dom";
import { afterEach, describe, expect, it, vi } from "vitest";
import { checkAxeAA } from "../../shell/a11y";
import type { ActivationResponse, CostingVersion, ItemCostRow } from "./api/client";
import { VersionDetailPage } from "./VersionDetailPage";
import { CreateVersionPage, validateVersion, VersionsPage } from "./VersionsPage";
import { costDelta, parseOnHandCsv } from "./versionParts";

const shellRules = new Set(["page-has-heading-one", "landmark-unique", "region", "bypass"]);
const pageViolations = (container: HTMLElement) => checkAxeAA(container).filter((v) => !shellRules.has(v.id));

const LE = "11111111-1111-4111-8111-111111111111";
const P1 = "22222222-2222-4222-8222-222222222222";
const P2 = "33333333-3333-4333-8333-333333333333";
const S1 = "44444444-4444-4444-8444-444444444444";
const V1 = "55555555-5555-4555-8555-555555555555";
const V0 = "66666666-6666-4666-8666-666666666666";

const draft: CostingVersion = {
  id: V1,
  legalEntityId: LE,
  code: "STD-2027",
  type: "STANDARD",
  status: "DRAFT",
  currency: "EUR",
  effectiveFrom: "2026-10-01",
  blockActivation: false,
  version: 4,
  createdAt: "2026-10-20T08:00:00Z",
  itemCount: 2,
};

const active: CostingVersion = { ...draft, id: V0, code: "STD-2026", status: "ACTIVE", version: 3, activatedAt: "2026-01-02T08:00:00Z" };

const items: ItemCostRow[] = [
  { productId: P1, siteId: S1, unitCost: "11", status: "PENDING", source: "COPY", activeCost: "10" },
  { productId: P2, siteId: S1, unitCost: "5.5", status: "PENDING", source: "IMPORT" },
];

const preview: ActivationResponse = {
  versionId: V1,
  versionCode: "STD-2027",
  version: 4,
  status: "PREVIEW",
  currency: "EUR",
  lineCount: 2,
  revaluationTotal: "110.00",
  sites: [{ siteId: S1, revaluation: "110.00" }],
  topDeltas: [
    { productId: P1, siteId: S1, previousCost: "10", newCost: "11", onHandQty: "100", revaluation: "100.00" },
    { productId: P2, siteId: S1, bookValue: "100.00", newCost: "5.5", onHandQty: "20", revaluation: "10.00" },
  ],
};

type Handler = (url: string, init: RequestInit) => Response | Promise<Response>;

const json = (status: number, body: unknown) => new Response(JSON.stringify(body), { status });

/** Routes fetch by "METHOD path-prefix"; the longest matching key wins. */
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

function calls(fetchMock: ReturnType<typeof vi.fn>, method: string, fragment: string) {
  return (fetchMock.mock.calls as unknown as [string, RequestInit][]).filter(([url, init]) => (init?.method ?? "GET") === method && url.includes(fragment));
}

function at(path: string) {
  return render(
    createElement(
      MemoryRouter,
      { initialEntries: [path] },
      createElement(
        Routes,
        null,
        createElement(Route, { path: "/costing/versions", element: createElement(VersionsPage) }),
        createElement(Route, { path: "/costing/versions/new", element: createElement(CreateVersionPage) }),
        createElement(Route, { path: "/costing/versions/:id", element: createElement(VersionDetailPage) }),
      ),
    ),
  );
}

const ON_HAND = `productId,siteId,qty,bookValue\n${P1},${S1},100,\n${P2},${S1},20,100`;

function detailRoutes(extra: Record<string, Handler> = {}) {
  return stubRoutes({
    [`GET /api/v1/costing/versions/${V1}`]: () => json(200, draft),
    [`GET /api/v1/costing/versions/${V1}/item-costs`]: () => json(200, { items }),
    "GET /api/v1/costing/versions?": () => json(200, { items: [draft, active] }),
    ...extra,
  });
}

afterEach(() => vi.unstubAllGlobals());

describe("VersionsPage", () => {
  it("lists versions, filters by status and opens a version", async () => {
    const fetchMock = stubRoutes({
      "GET /api/v1/costing/versions": () => json(200, { items: [active, draft] }),
      [`GET /api/v1/costing/versions/${V1}`]: () => json(200, draft),
      [`GET /api/v1/costing/versions/${V1}/item-costs`]: () => json(200, { items: [] }),
    });
    const { container } = at("/costing/versions");
    const table = await findTable({ name: "Costing versions" });
    expect(await within(table).findByText("STD-2026")).toBeInTheDocument();
    expect(within(table).getByText("STD-2027")).toBeInTheDocument();
    expect(pageViolations(container)).toEqual([]);

    fireEvent.change(screen.getByLabelText("Status"), { target: { value: "DRAFT" } });
    await waitFor(() => expect(calls(fetchMock, "GET", "/api/v1/costing/versions?status=DRAFT")).toHaveLength(1));

    fireEvent.click(within(table).getByRole("button", { name: "STD-2027" }));
    expect(await screen.findByRole("heading", { name: "Costing version STD-2027" })).toBeInTheDocument();
  });

  it("shows the empty state, and an error with a retry", async () => {
    let fail = true;
    stubRoutes({
      "GET /api/v1/costing/versions": () => (fail ? json(503, { title: "down", code: "COM-0500", detail: "internal error" }) : json(200, { items: [] })),
    });
    at("/costing/versions");
    expect(await findAlert()).toHaveTextContent("Something went wrong on our side");
    fail = false;
    fireEvent.click(screen.getByRole("button", { name: "Try again" }));
    expect(await screen.findByRole("heading", { name: "No costing versions yet" })).toBeInTheDocument();
  });

  it("loads the next page with the cursor the service returned", async () => {
    const fetchMock = stubRoutes({
      "GET /api/v1/costing/versions": (url) => (url.includes("cursor=c2") ? json(200, { items: [draft] }) : json(200, { items: [active], nextCursor: "c2" })),
    });
    at("/costing/versions");
    fireEvent.click(await screen.findByRole("button", { name: "Load more versions" }));
    expect(await screen.findByText("STD-2027")).toBeInTheDocument();
    expect(screen.getByText("STD-2026")).toBeInTheDocument();
    expect(calls(fetchMock, "GET", "cursor=c2")).toHaveLength(1);
    expect(screen.queryByRole("button", { name: "Load more versions" })).not.toBeInTheDocument();
  });
});

describe("CreateVersionPage", () => {
  it("checks the form before sending anything", () => {
    const fetchMock = stubRoutes({});
    at("/costing/versions/new");
    fireEvent.change(screen.getByLabelText("Code"), { target: { value: "x" } });
    fireEvent.click(screen.getByRole("button", { name: "Create version" }));
    expect(screen.getByText("Enter the legal entity's id (a UUID).")).toBeInTheDocument();
    expect(screen.getByText(/Use 2 to 32 capital letters/)).toBeInTheDocument();
    expect(fetchMock).not.toHaveBeenCalled();
    expect(validateVersion({ legalEntityId: LE, code: "STD-2026", type: "STANDARD", currency: "EUR", effectiveFrom: "2026-01-01", blockActivation: false })).toEqual({});
  });

  it("creates the draft with an Idempotency-Key and opens it", async () => {
    const fetchMock = stubRoutes({
      "POST /api/v1/costing/versions": () => json(201, draft),
      [`GET /api/v1/costing/versions/${V1}`]: () => json(200, draft),
      [`GET /api/v1/costing/versions/${V1}/item-costs`]: () => json(200, { items: [] }),
      "GET /api/v1/costing/versions?": () => json(200, { items: [] }),
    });
    const { container } = at("/costing/versions/new");
    expect(pageViolations(container)).toEqual([]);
    fireEvent.change(screen.getByLabelText("Legal entity"), { target: { value: LE } });
    fireEvent.change(screen.getByLabelText("Code"), { target: { value: "std-2027" } });
    fireEvent.change(screen.getByLabelText("Effective from"), { target: { value: "2026-10-01" } });
    fireEvent.click(screen.getByRole("button", { name: "Create version" }));
    expect(await screen.findByRole("heading", { name: "Costing version STD-2027" })).toBeInTheDocument();
    const [[, init]] = calls(fetchMock, "POST", "/api/v1/costing/versions");
    expect(JSON.parse(String(init.body))).toMatchObject({ legalEntityId: LE, code: "STD-2027", type: "STANDARD", currency: "EUR", effectiveFrom: "2026-10-01" });
    expect((init.headers as Record<string, string>)["Idempotency-Key"]).toMatch(/^[0-9a-f-]{36}$/);
  });

  it("shows the service's refusal with its code", async () => {
    stubRoutes({
      "POST /api/v1/costing/versions": () => json(409, { title: "Conflict", code: "CST-VER-CODE-DUPLICATE", detail: "a costing version with this code already exists" }),
    });
    at("/costing/versions/new");
    fireEvent.change(screen.getByLabelText("Legal entity"), { target: { value: LE } });
    fireEvent.change(screen.getByLabelText("Code"), { target: { value: "STD-2026" } });
    fireEvent.click(screen.getByRole("button", { name: "Create version" }));
    expect(await findAlert()).toHaveTextContent("a costing version with this code already exists");
  });
});

describe("VersionDetailPage", () => {
  it("shows the details and compares each pending cost to the active one", async () => {
    detailRoutes();
    const { container } = at(`/costing/versions/${V1}`);
    const table = await findTable({ name: "Item costs" });
    await waitFor(() => expect(within(table).getAllByRole("row")[1]).toHaveTextContent("11"));
    const rows = within(table).getAllByRole("row");
    expect(rows[1]).toHaveTextContent("+1");
    expect(rows[2]).toHaveTextContent("5.5");
    expect(rows[2]).toHaveTextContent("—");
    expect(screen.getByText(LE)).toBeInTheDocument();
    expect(await screen.findByRole("option", { name: "STD-2026 (2 costs)" })).toBeInTheDocument();
    expect(pageViolations(container)).toEqual([]);
  });

  it("shows not found for an unknown version", async () => {
    stubRoutes({ [`GET /api/v1/costing/versions/${V1}`]: () => json(404, { title: "Not Found", code: "CST-VER-NOT-FOUND", detail: "costing version not found" }) });
    at(`/costing/versions/${V1}`);
    expect(await screen.findByRole("heading", { name: "Costing version not found" })).toBeInTheDocument();
  });

  it("keeps Activate disabled until the preview, then activates at the previewed version", async () => {
    const fetchMock = detailRoutes({
      [`POST /api/v1/costing/versions/${V1}:preview-activation`]: () => json(200, preview),
      [`POST /api/v1/costing/versions/${V1}:activate`]: () => json(200, { ...preview, status: "ACTIVATED", version: 5 }),
    });
    at(`/costing/versions/${V1}`);
    const activate = await screen.findByRole("button", { name: "Activate version" });
    expect(activate).toBeDisabled();
    fireEvent.change(screen.getByLabelText("On hand at activation (CSV)"), { target: { value: ON_HAND } });
    fireEvent.click(screen.getByRole("button", { name: "Preview revaluation" }));
    const result = await screen.findByRole("region", { name: "Revaluation preview" });
    expect(within(result).getByRole("table", { name: "Revaluation by site" })).toHaveTextContent("110.00");
    expect(within(result).getByRole("table", { name: "Largest revaluation deltas" })).toHaveTextContent("Book value 100.00");
    const [[, previewInit]] = calls(fetchMock, "POST", ":preview-activation");
    expect(JSON.parse(String(previewInit.body))).toEqual({
      onHand: [
        { productId: P1, siteId: S1, qty: "100" },
        { productId: P2, siteId: S1, qty: "20", bookValue: "100" },
      ],
    });
    await waitFor(() => expect(activate).toBeEnabled());

    // Editing the on-hand makes the preview stale again.
    fireEvent.change(screen.getByLabelText("On hand at activation (CSV)"), { target: { value: `${ON_HAND}\n` } });
    expect(activate).toBeDisabled();
    fireEvent.change(screen.getByLabelText("On hand at activation (CSV)"), { target: { value: ON_HAND } });
    expect(activate).toBeEnabled();

    fireEvent.click(activate);
    expect(await findStatus()).toHaveTextContent("Version STD-2027 is active. Revaluation 110.00 EUR.");
    const [[, activateInit]] = calls(fetchMock, "POST", ":activate");
    expect((activateInit.headers as Record<string, string>)["If-Match"]).toBe('"4"');
  });

  it("asks for a new preview when the version changed since it", async () => {
    detailRoutes({
      [`POST /api/v1/costing/versions/${V1}:preview-activation`]: () => json(200, preview),
      [`POST /api/v1/costing/versions/${V1}:activate`]: () => json(412, { title: "Precondition Failed", code: "conflict_version", detail: "changed" }),
    });
    at(`/costing/versions/${V1}`);
    fireEvent.click(await screen.findByRole("button", { name: "Preview revaluation" }));
    await screen.findByRole("region", { name: "Revaluation preview" });
    fireEvent.click(screen.getByRole("button", { name: "Activate version" }));
    expect(await screen.findByText(/changed since the preview/)).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "Activate version" })).toBeDisabled();
  });

  it("shows a refused preview's message and rejects bad on-hand before sending", async () => {
    const fetchMock = detailRoutes({
      [`POST /api/v1/costing/versions/${V1}:preview-activation`]: () =>
        json(422, { title: "Unprocessable Entity", code: "CST-ACT-007", detail: "1 product and site pairs have stock but no active standard cost" }),
    });
    at(`/costing/versions/${V1}`);
    fireEvent.change(await screen.findByLabelText("On hand at activation (CSV)"), { target: { value: "productId,siteId,qty\nnope,x,1" } });
    fireEvent.click(screen.getByRole("button", { name: "Preview revaluation" }));
    expect(await screen.findByText("Line 2: productId and siteId must be UUIDs.")).toBeInTheDocument();
    expect(calls(fetchMock, "POST", ":preview-activation")).toHaveLength(0);
    fireEvent.change(screen.getByLabelText("On hand at activation (CSV)"), { target: { value: `productId,siteId,qty\n${P2},${S1},20` } });
    fireEvent.click(screen.getByRole("button", { name: "Preview revaluation" }));
    await waitFor(() => expect(screen.getAllByRole("alert").some((a) => a.textContent?.includes("no active standard cost"))).toBe(true));
  });

  it("imports a CSV file and copies another version, then reloads", async () => {
    const fetchMock = detailRoutes({
      [`POST /api/v1/costing/versions/${V1}/item-costs:import`]: () => json(200, { versionId: V1, imported: 1 }),
      [`POST /api/v1/costing/versions/${V1}/item-costs:copy`]: () => json(200, { versionId: V1, copied: 2 }),
    });
    at(`/costing/versions/${V1}`);
    const csv = `productId,siteId,unitCost\n${P1},${S1},12.5`;
    fireEvent.change(await screen.findByLabelText("Item costs (CSV)"), { target: { value: csv } });
    fireEvent.click(screen.getByRole("button", { name: "Import costs" }));
    expect(await screen.findByText("1 item costs imported.")).toBeInTheDocument();
    const [[, importInit]] = calls(fetchMock, "POST", "item-costs:import");
    expect(importInit.body).toBe(csv);
    expect((importInit.headers as Record<string, string>)["content-type"]).toBe("text/csv");

    await screen.findByRole("option", { name: "STD-2026 (2 costs)" });
    fireEvent.change(screen.getByLabelText("Percent change"), { target: { value: "2.5" } });
    fireEvent.click(screen.getByRole("button", { name: "Copy costs" }));
    expect(await screen.findByText("2 item costs copied.")).toBeInTheDocument();
    const [[, copyInit]] = calls(fetchMock, "POST", "item-costs:copy");
    expect(JSON.parse(String(copyInit.body))).toEqual({ sourceVersionId: V0, percentChange: "2.5" });
    await waitFor(() => expect(calls(fetchMock, "GET", `/api/v1/costing/versions/${V1}/item-costs`).length).toBeGreaterThanOrEqual(3));
  });

  it("offers the recorded revaluation, not activation, on an active version", async () => {
    stubRoutes({
      [`GET /api/v1/costing/versions/${V0}`]: () => json(200, active),
      [`GET /api/v1/costing/versions/${V0}/item-costs`]: () => json(200, { items: [] }),
      [`POST /api/v1/costing/versions/${V0}:preview-activation`]: () => json(200, { ...preview, versionId: V0, status: "ALREADY_ACTIVE" }),
    });
    at(`/costing/versions/${V0}`);
    fireEvent.click(await screen.findByRole("button", { name: "Show recorded revaluation" }));
    expect(await screen.findByRole("region", { name: "Recorded revaluation" })).toBeInTheDocument();
    expect(screen.queryByRole("button", { name: "Activate version" })).not.toBeInTheDocument();
    expect(screen.queryByRole("button", { name: "Import costs" })).not.toBeInTheDocument();
  });
});

describe("parseOnHandCsv", () => {
  it("reads columns in any order with an optional book value", () => {
    expect(parseOnHandCsv("")).toEqual({ ok: true, value: [] });
    expect(parseOnHandCsv(`qty,SiteId,productid\n-3,${S1},${P1}`)).toEqual({ ok: true, value: [{ productId: P1, siteId: S1, qty: "-3" }] });
  });

  it("names the line of the first bad row", () => {
    expect(parseOnHandCsv("a,b\n")).toMatchObject({ ok: false });
    expect(parseOnHandCsv(`productId,siteId,qty\n${P1},${S1},1.0000001`)).toEqual({ ok: false, error: "Line 2: qty must be a number with at most 6 decimals." });
    expect(parseOnHandCsv(`productId,siteId,qty\n${P1},${S1},1\n${P1.toUpperCase()},${S1},2`)).toEqual({ ok: false, error: "Line 3: this product and site is already listed." });
  });
});

describe("costDelta", () => {
  it("subtracts decimal strings exactly", () => {
    expect(costDelta("11", "10")).toBe("+1");
    expect(costDelta("0.3", "0.1")).toBe("+0.2");
    expect(costDelta("4.125", "4.5")).toBe("-0.375");
    expect(costDelta("10", "10.000000")).toBe("0");
    expect(costDelta("5", undefined)).toBe("—");
  });
});
