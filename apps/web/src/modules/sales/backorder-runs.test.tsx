import { findTable } from "../../test-utils";
import { fireEvent, render, screen, waitFor, within } from "@testing-library/react";
import { MemoryRouter, Route, Routes } from "react-router-dom";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { checkAxeAA } from "../../shell/a11y";
import { validateRunRequest, emptyRunValues } from "./BackorderRunsPage";
import { lineEffect } from "./BackorderRunDetailPage";
import sales from "./module";

const LE = "11111111-0000-4000-8000-000000000001";
const CUSTOMER = "22222222-0000-4000-8000-000000000002";
const PRODUCT = "33333333-0000-4000-8000-000000000003";
const ORDER_ID = "66666666-0000-4000-8000-000000000006";

const shellRules = new Set(["page-has-heading-one", "landmark-unique", "region", "bypass"]);
const pageViolations = (container: HTMLElement) => checkAxeAA(container).filter((v) => !shellRules.has(v.id));

type Reply = { status: number; body: unknown; etag?: string };
type Handler = (url: URL, init: RequestInit) => Reply | Promise<Reply>;

let handler: Handler;
let calls: { method: string; url: URL; headers: Headers; body: unknown }[];

beforeEach(() => {
  calls = [];
  vi.stubGlobal(
    "fetch",
    vi.fn(async (input: string, init: RequestInit = {}) => {
      const url = new URL(input, "http://localhost");
      calls.push({ method: init.method ?? "GET", url, headers: new Headers(init.headers), body: init.body ? JSON.parse(init.body as string) : undefined });
      const { status, body, etag } = await handler(url, init);
      const headers: Record<string, string> = { "Content-Type": "application/json" };
      if (etag) headers.ETag = etag;
      return new Response(status === 204 ? null : JSON.stringify(body), { status, headers });
    }),
  );
});

afterEach(() => {
  vi.unstubAllGlobals();
});

function renderAt(path: string) {
  return render(
    <MemoryRouter initialEntries={[path]}>
      <Routes>
        {sales.routes.map((route) => (
          <Route key={route.path} path={route.path} element={route.element} />
        ))}
        <Route path="*" element={<p>Elsewhere</p>} />
      </Routes>
    </MemoryRouter>,
  );
}

function fill(label: string | RegExp, value: string) {
  fireEvent.change(screen.getByLabelText(label), { target: { value } });
}

const SITE = "44444444-0000-4000-8000-000000000004";
const WH = "55555555-0000-4000-8000-000000000005";
const RUN_ID = "77777777-0000-4000-8000-000000000007";

const run = {
  id: RUN_ID,
  legalEntityId: LE,
  productId: PRODUCT,
  siteId: SITE,
  warehouseId: WH,
  strategy: "REDISTRIBUTE",
  supply: "40",
  supplyDate: "2027-03-10",
  loseThreshold: 3,
  defaultPriorityClass: 5,
  status: "PREVIEW",
  allocated: "40",
  deconfirmedCount: 1,
  changedCount: 1,
  version: 1,
  createdBy: "planner",
  createdAt: "2027-03-01T09:00:00Z",
  warning: "backorder run would deconfirm 1 lines",
  warningCode: "SLS-0015",
  lines: [
    { seq: 1, orderId: ORDER_ID, orderNumber: "SO-0000001", orderVersion: 2, lineId: "l1", lineNo: 1, customerId: CUSTOMER, priorityClass: 1, requestedDate: "2027-03-01", orderValue: "200", demand: "20", before: "20", after: "20", changed: false, deconfirmed: false },
    { seq: 2, orderId: "aaaaaaaa-0000-4000-8000-00000000000a", orderNumber: "SO-0000003", orderVersion: 2, lineId: "l3", lineNo: 1, customerId: LE, priorityClass: 5, requestedDate: "2027-03-01", orderValue: "180", demand: "20", before: "20", after: "0", changed: true, deconfirmed: true },
  ],
};

describe("Backorder runs", () => {
  it("validates the new run like the service", () => {
    expect(validateRunRequest(emptyRunValues).errors).toMatchObject({ legalEntityId: expect.any(String), supply: expect.any(String), supplyDate: expect.any(String) });
    const ok = { ...emptyRunValues, legalEntityId: LE, productId: PRODUCT, siteId: SITE, warehouseId: WH, supply: "40", supplyDate: "2027-03-10", priorities: `${CUSTOMER} 1` };
    expect(validateRunRequest(ok).input).toMatchObject({ strategy: "REDISTRIBUTE", priorityClasses: { [CUSTOMER]: 1 } });
    expect(validateRunRequest({ ...ok, priorities: "C1 2" }).errors.priorities).toBeTruthy();
    expect(validateRunRequest({ ...ok, loseThreshold: "10" }).errors.loseThreshold).toBeTruthy();
    expect(lineEffect(run.lines[1])).toBe("Loses 20");
    expect(lineEffect(run.lines[0])).toBe("Unchanged");
    expect(lineEffect({ ...run.lines[1], before: "0.3", after: "0.1" })).toBe("Loses 0.2");
    expect(lineEffect({ ...run.lines[1], before: "12.5", after: "20.000001", deconfirmed: false })).toBe("Gains 7.500001");
  });

  it("lists runs with status and passes the filter", async () => {
    handler = () => ({ status: 200, body: { items: [{ ...run, lines: undefined }, { ...run, id: "r2", status: "COMMITTED", createdAt: "2027-03-02T09:00:00Z" }] } });
    const { container } = renderAt("/sales/backorder-processing");
    const table = await findTable({ name: "Backorder runs" });
    await waitFor(() => expect(within(table).getByText("Committed")).toBeInTheDocument());
    expect(within(table).getByText("Preview")).toBeInTheDocument();
    expect(pageViolations(container)).toEqual([]);
    fill("Product", PRODUCT);
    fill("Status", "PREVIEW");
    fireEvent.click(screen.getByRole("button", { name: "Filter" }));
    await waitFor(() => expect(calls.at(-1)?.url.searchParams.get("productId")).toBe(PRODUCT));
    expect(calls.at(-1)?.url.searchParams.get("status")).toBe("PREVIEW");
  });

  it("explains an empty list and shows a failure with retry", async () => {
    handler = () => ({ status: 200, body: { items: [] } });
    const first = renderAt("/sales/backorder-processing");
    expect(await screen.findByRole("heading", { name: "No backorder runs" })).toBeInTheDocument();
    first.unmount();
    let fail = true;
    handler = () => (fail ? { status: 503, body: { code: "COM-0503", detail: "store unavailable" } } : { status: 200, body: { items: [run] } });
    renderAt("/sales/backorder-processing");
    expect(await screen.findByText(/COM-0503/)).toBeInTheDocument();
    fail = false;
    fireEvent.click(screen.getByRole("button", { name: "Try again" }));
    expect(await screen.findByText("Preview")).toBeInTheDocument();
  });

  it("plans a run with an Idempotency-Key and opens it", async () => {
    handler = (_url, init) => (init.method === "POST" ? { status: 201, body: run, etag: '"1"' } : { status: 200, body: run, etag: '"1"' });
    const { container } = renderAt("/sales/backorder-processing/new");
    fireEvent.click(await screen.findByRole("button", { name: "Plan run" }));
    expect(screen.getAllByText("Enter a UUID.").length).toBeGreaterThan(0);
    expect(calls).toHaveLength(0);
    expect(pageViolations(container)).toEqual([]);
    fill("Legal entity", LE);
    fill("Product", PRODUCT);
    fill("Site", SITE);
    fill("Warehouse", WH);
    fill("Supply", "40");
    fill("Supply date", "2027-03-10");
    fill("Customer priority classes", `${CUSTOMER} 1`);
    fireEvent.click(screen.getByRole("button", { name: "Plan run" }));
    await waitFor(() => expect(calls.some((c) => c.method === "POST")).toBe(true));
    const post = calls.find((c) => c.method === "POST");
    expect(post?.url.pathname).toBe("/api/v1/sales/backorder-runs");
    expect(post?.headers.get("Idempotency-Key")).toBeTruthy();
    expect(post?.body).toMatchObject({ strategy: "REDISTRIBUTE", supply: "40", supplyDate: "2027-03-10", priorityClasses: { [CUSTOMER]: 1 } });
  });

  it("shows the refusal when no line is waiting", async () => {
    handler = () => ({ status: 422, body: { code: "SLS-0027", detail: "no open line of a CONFIRMED order is waiting" } });
    renderAt("/sales/backorder-processing/new");
    await screen.findByLabelText("Legal entity");
    fill("Legal entity", LE);
    fill("Product", PRODUCT);
    fill("Site", SITE);
    fill("Warehouse", WH);
    fill("Supply", "40");
    fill("Supply date", "2027-03-10");
    fireEvent.click(screen.getByRole("button", { name: "Plan run" }));
    expect(await screen.findByText(/SLS-0027/)).toBeInTheDocument();
    expect(screen.getByText("The run cannot be planned")).toBeInTheDocument();
  });

  it("shows the run with its lines and warning, and commits at its ETag", async () => {
    let committed = false;
    handler = (_url, init) => {
      if (init.method === "POST") {
        committed = true;
        return { status: 200, body: { ...run, status: "COMMITTED", version: 2, committedBy: "planner", committedAt: "2027-03-01T10:00:00Z", warning: undefined, warningCode: undefined }, etag: '"2"' };
      }
      return { status: 200, body: run, etag: '"1"' };
    };
    const { container } = renderAt(`/sales/backorder-processing/${RUN_ID}`);
    const table = await findTable({ name: "Run lines" });
    expect(within(table).getByText("Loses 20")).toBeInTheDocument();
    expect(screen.getByText("backorder run would deconfirm 1 lines")).toBeInTheDocument();
    expect(pageViolations(container)).toEqual([]);
    fireEvent.click(screen.getByRole("button", { name: "Commit run" }));
    await waitFor(() => expect(committed).toBe(true));
    const post = calls.find((c) => c.method === "POST");
    expect(post?.url.pathname).toBe(`/api/v1/sales/backorder-runs/${RUN_ID}:commit`);
    expect(post?.headers.get("If-Match")).toBe('"1"');
    expect(await screen.findByText(/the schedule lines were rewritten/)).toBeInTheDocument();
    expect(screen.queryByRole("button", { name: "Commit run" })).toBeNull();
  });

  it("explains a stale commit and offers a new run", async () => {
    handler = (_url, init) =>
      init.method === "POST" ? { status: 409, body: { code: "SLS-0042", detail: "order SO-0000003 line 1 changed since the preview; run a new preview" } } : { status: 200, body: run, etag: '"1"' };
    renderAt(`/sales/backorder-processing/${RUN_ID}`);
    fireEvent.click(await screen.findByRole("button", { name: "Commit run" }));
    expect(await screen.findByText("An order changed since the preview")).toBeInTheDocument();
    expect(screen.getByRole("link", { name: "Plan a new run" })).toHaveAttribute("href", "/sales/backorder-processing/new");
  });

  it("shows loading and a load failure on the detail page", async () => {
    handler = () => ({ status: 404, body: { code: "SLS-0111", detail: "backorder run not found" } });
    renderAt(`/sales/backorder-processing/${RUN_ID}`);
    expect(await screen.findByText(/SLS-0111/)).toBeInTheDocument();
  });

  it("is on the module's navigation behind a read duty", () => {
    expect(sales.nav?.find((n) => n.to === "/sales/backorder-processing")?.duty).toBe("sales.order.read");
    expect(sales.routes.some((r) => r.path === "/sales/backorder-processing/:id")).toBe(true);
  });
});
