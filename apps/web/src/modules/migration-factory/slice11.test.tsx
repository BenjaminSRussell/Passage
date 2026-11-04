import { fireEvent, render, screen, within } from "@testing-library/react";
import { MemoryRouter, Route, Routes } from "react-router-dom";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { checkAxeAA } from "../../shell/a11y";
import { findTable } from "../../test-utils";
import type { PackRunDetail } from "./api/client";
import { parseOpenOrders } from "./domain";
import migrationFactory from "./module";
import { validateOpenOrdersRun, type OpenOrdersRunValues } from "./NewOpenOrdersRunPage";

const RUN = "0195f0a0-0000-7000-8000-0000000000f1";

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
      const reply = handler(url, init) ?? json(404, { code: "TEST-404", detail: `no fake for ${init.method ?? "GET"} ${url.pathname}` });
      return new Response(JSON.stringify(reply.body), { status: reply.status, headers: { "Content-Type": "application/json" } });
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
        {migrationFactory.routes.map((route) => (
          <Route key={route.path} path={route.path} element={route.element} />
        ))}
      </Routes>
    </MemoryRouter>,
  );
}

const posts = (suffix: string) => calls.filter((c) => c.method === "POST" && c.url.pathname.endsWith(suffix));

const header = "legacyId,orderType,orderNumber,lineNumber,item,party,unit,currency,orderedQty,deliveredQty,unitPrice,deliveryDate";
const lines = [header, "SL-1,sales,SO-1,1,BOLT,C1,EA,usd,10,4,2.5,2027-01-15", "PL-1,PURCHASE,PO-1,1,NUT,V1,EA,USD,7,0,3,"].join("\n");

const values: OpenOrdersRunValues = {
  sourceSystem: "AX2012",
  sourceCompany: " USMF ",
  cutoff: "2026-12-31T00:00",
  freeze: "2026-12-31T18:00",
  quarantineMaxPct: "",
  items: "BOLT, NUT",
  customers: "C1",
  vendors: "V1",
  lines,
};

const ordersRun: PackRunDetail = {
  runId: RUN,
  pack: "open_orders",
  sourceSystem: "AX2012",
  sourceCompany: "USMF",
  cutoffTs: "2026-12-31T00:00:00Z",
  inputHash: "cd".repeat(32),
  status: "LOADED",
  recordsIn: 3,
  recordsLoaded: 2,
  recordsQuarantined: 1,
  alreadyPresent: 0,
  version: 1,
  createdAt: "2027-01-01T04:10:00Z",
  controlTotals: [
    { name: "open_orders.line_count", key: { company: "USMF", order_type: "SALES" }, source: "2.000000", target: "1.000000", delta: "-1.000000", tolerance: "0.000000", status: "MISMATCH" },
  ],
  quarantined: [
    { seq: 1, legacyId: "SL-9", rule: "DOMAIN", reason: "delivered quantity 3 covers the ordered quantity 3; the line is not open", status: "OPEN", party: "C1", account: "NUT", dimensions: "SALES/SO-9/1", currency: "USD", amount: "-0.000000" },
  ],
} as PackRunDetail;

describe("open orders extract helpers", () => {
  it("parses pasted lines, skips the header, normalizes case and keeps decimals as strings", () => {
    expect(parseOpenOrders(lines)).toEqual({
      errors: [],
      records: [
        { legacyId: "SL-1", orderType: "SALES", orderNumber: "SO-1", lineNumber: "1", item: "BOLT", party: "C1", unit: "EA", currency: "USD", orderedQty: "10", deliveredQty: "4", unitPrice: "2.5", deliveryDate: "2027-01-15" },
        { legacyId: "PL-1", orderType: "PURCHASE", orderNumber: "PO-1", lineNumber: "1", item: "NUT", party: "V1", unit: "EA", currency: "USD", orderedQty: "7", deliveredQty: "0", unitPrice: "3" },
      ],
    });
  });

  it("reports each line the service would quarantine for its shape", () => {
    const bad = parseOpenOrders(
      [
        "L1,SALES,SO,1,B,C,EA,USD,1,0,1,2027-01-01,extra",
        "L2,TRANSFER,SO,1,B,C,EA,USD,1,0,1,",
        "L3,SALES,SO,1,B,C,EA,DOLLAR,1,0,1,",
        "L4,SALES,SO,1,B,C,EA,USD,0,0,1,",
        "L5,SALES,SO,1,B,C,EA,USD,2,2,1,",
        "L6,SALES,SO,1,B,C,EA,USD,2,0,-1,",
        "L7,SALES,SO,1,B,C,EA,USD,2,0,1,15/01/2027",
        ",SALES,SO,1,B,C,EA,USD,2,0,1,",
      ].join("\n"),
    );
    expect(bad.records).toEqual([]);
    expect(bad.errors).toEqual([
      "Line 1: expected 12 values (legacyId, orderType, orderNumber, lineNumber, item, party, unit, currency, orderedQty, deliveredQty, unitPrice, deliveryDate), found 13.",
      'Line 2: order type "TRANSFER" is not SALES or PURCHASE.',
      'Line 3: currency "DOLLAR" is not a three-letter code.',
      'Line 4: ordered quantity "0" must be a decimal above zero such as 12.5.',
      "Line 5: delivered quantity 2 covers the ordered 2; the line is not open.",
      'Line 6: unit price "-1" must be a decimal of zero or more such as 2.50.',
      'Line 7: delivery date "15/01/2027" is not a date such as 2027-01-15.',
      "Line 8: the legacy id, order number and line number are required.",
    ]);
  });

  it("refuses a quoted CSV value and a decimal comma instead of misreading them", () => {
    const quoted = parseOpenOrders('"SL-1",SALES,SO-1,1,BOLT,C1,EA,USD,10,4,2.5,');
    expect(quoted.records).toEqual([]);
    expect(quoted.errors).toEqual(["Line 1: quoted values are not supported; paste plain values separated by commas, semicolons or tabs."]);
    // "10,5" ordered with the date left off reads as ordered 10, delivered 5, price 0 and date 2.5.
    const comma = parseOpenOrders("SL-1,SALES,SO-1,1,BOLT,C1,EA,USD,10,5,0,2.5");
    expect(comma.records).toEqual([]);
    expect(comma.errors).toEqual(['Line 1: delivery date "2.5" is not a date such as 2027-01-15.']);
  });
});

describe("validateOpenOrdersRun", () => {
  it("builds the request the service takes", () => {
    const { errors, input } = validateOpenOrdersRun(values);
    expect(errors).toEqual({});
    expect(input).toEqual({
      sourceSystem: "AX2012",
      sourceCompany: "USMF",
      cutoffTs: "2026-12-31T00:00:00Z",
      freezeTs: "2026-12-31T18:00:00Z",
      items: ["BOLT", "NUT"],
      customers: ["C1"],
      vendors: ["V1"],
      records: parseOpenOrders(lines).records,
    });
  });

  it("refuses what the service would refuse", () => {
    const { errors, input } = validateOpenOrdersRun({ ...values, sourceCompany: "", cutoff: "2027-01-02T00:00", items: " , ", quarantineMaxPct: "11", lines: "L1,SALES,SO,1,B,C,EA,USD,x,0,1," });
    expect(input).toBeUndefined();
    expect(errors).toMatchObject({
      sourceCompany: "Enter the legacy company (DataAreaId or company code).",
      cutoff: "The cutoff can't be after the legacy freeze (MIG-1501).",
      items: "List the item master the lines are checked against.",
      quarantineMaxPct: "Enter a percentage from 0 to 10.",
    });
    expect(errors.lines).toMatch(/ordered quantity "x"/);
    expect(validateOpenOrdersRun({ ...values, lines: "  " }).errors.lines).toBe("Paste at least one line.");
  });

  it("asks for the customer master only when the extract has sales lines, and the vendor master for purchase lines", () => {
    expect(validateOpenOrdersRun({ ...values, customers: "" }).errors).toEqual({ customers: "List the customer master the sales order lines are checked against." });
    expect(validateOpenOrdersRun({ ...values, vendors: "" }).errors).toEqual({ vendors: "List the vendor master the purchase order lines are checked against." });
    const salesOnly = [header, "SL-1,SALES,SO-1,1,BOLT,C1,EA,USD,10,4,2.5,"].join("\n");
    expect(validateOpenOrdersRun({ ...values, vendors: "", lines: salesOnly }).errors).toEqual({});
  });
});

describe("Load open orders page", () => {
  function fill() {
    fireEvent.change(screen.getByLabelText("Legacy company"), { target: { value: "USMF" } });
    fireEvent.change(screen.getByLabelText("Extract cutoff (UTC)"), { target: { value: "2026-12-31T00:00" } });
    fireEvent.change(screen.getByLabelText("Legacy freeze (UTC)"), { target: { value: "2026-12-31T18:00" } });
    fireEvent.change(screen.getByLabelText("Item master"), { target: { value: "BOLT NUT" } });
    fireEvent.change(screen.getByLabelText(/Customer master/), { target: { value: "C1" } });
    fireEvent.change(screen.getByLabelText(/Vendor master/), { target: { value: "V1" } });
    fireEvent.change(screen.getByLabelText("Open order lines"), { target: { value: lines } });
  }

  it("is reached from the pack runs page, posts the extract with an idempotency key, then opens the run", async () => {
    handler = (url, init) => {
      if (url.pathname === "/api/v1/migration/runs") return json(200, { items: [] });
      if (init.method === "POST") return json(201, ordersRun);
      if (url.pathname === `/api/v1/migration/runs/${RUN}`) return json(200, ordersRun);
      return undefined;
    };
    const list = renderAt("/migration-factory");
    expect(await screen.findByRole("link", { name: "Load open orders" })).toHaveAttribute("href", "/migration-factory/runs/new-orders");
    list.unmount();

    const { container } = renderAt("/migration-factory/runs/new-orders");
    expect(screen.getByRole("heading", { name: "Load open orders" })).toBeInTheDocument();
    expect(pageViolations(container)).toEqual([]);
    fill();
    fireEvent.click(screen.getByRole("button", { name: "Run open_orders" }));
    expect(await screen.findByRole("heading", { name: "Pack run open_orders" })).toBeInTheDocument();
    const [post] = posts("/migration/packs/open_orders/runs");
    expect(post.body).toEqual({
      sourceSystem: "AX2012",
      sourceCompany: "USMF",
      cutoffTs: "2026-12-31T00:00:00Z",
      freezeTs: "2026-12-31T18:00:00Z",
      items: ["BOLT", "NUT"],
      customers: ["C1"],
      vendors: ["V1"],
      records: parseOpenOrders(lines).records,
    });
    expect(new Headers(post.init.headers).get("Idempotency-Key")).toBeTruthy();
  });

  it("starts open_orders runs from the pack catalog", async () => {
    handler = (url) =>
      url.pathname === "/api/v1/migration/packs"
        ? json(200, { items: [{ pack: "open_orders", sourceSystems: ["AX2012"], controlTotals: "open quantity and value", available: true }] })
        : undefined;
    renderAt("/migration-factory/packs");
    expect(await screen.findByRole("link", { name: "Start open_orders run" })).toHaveAttribute("href", "/migration-factory/runs/new-orders");
  });

  it("shows a bad paste on the field and sends nothing", async () => {
    renderAt("/migration-factory/runs/new-orders");
    fill();
    fireEvent.change(screen.getByLabelText("Open order lines"), { target: { value: "L1,SALES,SO,1,B,C,EA,USD,abc,0,1," } });
    fireEvent.click(screen.getByRole("button", { name: "Run open_orders" }));
    expect(await screen.findByText(/ordered quantity "abc" must be a decimal above zero/)).toBeInTheDocument();
    expect(posts("/runs")).toHaveLength(0);
  });

  it("shows pasted markup in an error as text, never as an element", async () => {
    const { container } = renderAt("/migration-factory/runs/new-orders");
    fill();
    fireEvent.change(screen.getByLabelText("Open order lines"), { target: { value: "L1,<img src=x onerror=alert(1)>,SO,1,B,C,EA,USD,1,0,1," } });
    fireEvent.click(screen.getByRole("button", { name: "Run open_orders" }));
    expect(await screen.findByText(/order type "<img src=x onerror=alert\(1\)>" is not SALES or PURCHASE/)).toBeInTheDocument();
    expect(container.querySelector("img")).toBeNull();
    expect(posts("/runs")).toHaveLength(0);
  });

  it("shows the service's refusal with its code and stays on the form", async () => {
    handler = () => json(422, { code: "MIG-0001", detail: "invalid input: SALES order SO-1 line 1 already has an opening line" });
    renderAt("/migration-factory/runs/new-orders");
    fill();
    fireEvent.click(screen.getByRole("button", { name: "Run open_orders" }));
    const alert = await screen.findByRole("alert");
    expect(alert).toHaveTextContent("Could not load the orders");
    expect(alert).toHaveTextContent(/already has an opening line.*\(MIG-0001\)/);
    expect(screen.getByRole("button", { name: "Run open_orders" })).toBeInTheDocument();
  });
});

describe("open_orders run detail", () => {
  it("shows the staged-only notice, the totals and the quarantined line with its order and open quantity", async () => {
    handler = (url) => (url.pathname === `/api/v1/migration/runs/${RUN}` ? json(200, ordersRun) : undefined);
    const { container } = renderAt(`/migration-factory/runs/${RUN}`);
    expect(await screen.findByText("Staged, not posted to sales or purchasing")).toBeInTheDocument();
    const totals = await findTable({ name: "Control totals" });
    expect(totals).toHaveTextContent("open_orders.line_count");
    const quarantine = await findTable({ name: "Quarantined records" });
    expect(quarantine).toHaveTextContent("C1 / NUT / SALES/SO-9/1");
    expect(within(quarantine).getByRole("columnheader", { name: "Open quantity" })).toBeInTheDocument();
    expect(within(quarantine).getByRole("button", { name: /^Release record/ })).toBeInTheDocument();
    expect(pageViolations(container)).toEqual([]);
  });
});
