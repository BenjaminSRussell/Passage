import { fireEvent, render, screen, waitFor, within } from "@testing-library/react";
import { MemoryRouter, Route, Routes } from "react-router-dom";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { checkAxeAA } from "../../shell/a11y";
import { PrincipalProvider } from "../../shell/principal";
import { findAlert, findTable } from "../../test-utils";
import { REQUIREMENT_DUTY } from "./CustomerRequirementPages";
import processmfg from "./module";

const ITEM = "1a000000-0000-4000-8000-000000000001";
const CUSTOMER = "3c000000-0000-4000-8000-0000000000c1";
const RULE = "6d000000-0000-4000-8000-0000000000e1";
const OLD = "5b000000-0000-4000-8000-0000000000a1";
const NEW = "5b000000-0000-4000-8000-0000000000a2";
const HOLD = "5b000000-0000-4000-8000-0000000000a3";

type Reply = { status: number; body: unknown };
let handler: (url: URL, init: RequestInit) => Reply | undefined;
let calls: { method: string; url: URL; init: RequestInit; body: unknown }[];

const shellRules = new Set(["page-has-heading-one", "landmark-unique", "region", "bypass"]);

beforeEach(() => {
  calls = [];
  handler = () => undefined;
  vi.stubGlobal(
    "fetch",
    vi.fn(async (input: string, init: RequestInit = {}) => {
      const url = new URL(input, "http://localhost");
      calls.push({ method: init.method ?? "GET", url, init, body: init.body ? JSON.parse(String(init.body)) : undefined });
      const reply = handler(url, init) ?? { status: 404, body: { code: "TEST-404", detail: `no fake for ${url.pathname}` } };
      return new Response(JSON.stringify(reply.body), { status: reply.status, headers: { "Content-Type": "application/json" } });
    }),
  );
});

afterEach(() => vi.unstubAllGlobals());

function renderAt(path: string, duties: string[] = [REQUIREMENT_DUTY, "production.batch-order.read"]) {
  return render(
    <PrincipalProvider duties={duties}>
      <MemoryRouter initialEntries={[path]}>
        <Routes>
          {processmfg.routes.map((route) => (
            <Route key={route.path} path={route.path} element={route.element} />
          ))}
        </Routes>
      </MemoryRouter>
    </PrincipalProvider>,
  );
}

const rule = (over: Record<string, unknown> = {}) => ({
  id: RULE,
  legalEntityId: "11111111-0000-4000-8000-000000000001",
  itemId: ITEM,
  customerId: CUSTOMER,
  level: 3,
  sellableDays: 60,
  attribute: "potency",
  min: "40",
  max: "60",
  version: 1,
  createdBy: "u",
  createdAt: "2026-10-20T08:00:00Z",
  updatedAt: "2026-10-20T08:00:00Z",
  ...over,
});

const batch = (id: string, batchNumber: string, disposition = "available", expiryDate?: string) => ({
  id,
  legalEntityId: "11111111-0000-4000-8000-000000000001",
  itemId: ITEM,
  batchNumber,
  disposition,
  expiryDate,
  version: 1,
  createdBy: "u",
  createdAt: "2026-10-20T08:00:00Z",
  updatedAt: "2026-10-20T08:00:00Z",
});

function fill(label: string, value: string) {
  fireEvent.change(screen.getByLabelText(label), { target: { value } });
}

describe("customer batch requirement list (slice 14)", () => {
  it("lists the rules with scope, range and status, filtered by item and customer", async () => {
    handler = (url) =>
      url.pathname === "/api/v1/production/customer-batch-requirements"
        ? { status: 200, body: { items: [rule(), rule({ id: "x", customerId: undefined, level: 1, attribute: undefined, sellableDays: 10, retiredAt: "2026-10-21T08:00:00Z" })] } }
        : undefined;
    const { container } = renderAt("/processmfg/customer-requirements");
    const table = await findTable({ name: "Customer batch requirements" });
    expect(within(table).getByText("Customer 3c000000")).toBeInTheDocument();
    expect(within(table).getByText("potency 40 to 60")).toBeInTheDocument();
    expect(within(table).getByText("Retired")).toBeInTheDocument();
    expect(screen.getByRole("link", { name: "Define requirement" })).toHaveAttribute("href", "/processmfg/customer-requirements/new");
    fill("Item id", ITEM);
    await waitFor(() => expect(calls.some((c) => c.url.searchParams.get("itemId") === ITEM)).toBe(true));
    await waitFor(() => expect(checkAxeAA(container).filter((v) => !shellRules.has(v.id))).toEqual([]));
  });

  it("shows the empty state, hides the define link without the duty and flags a bad filter", async () => {
    handler = () => ({ status: 200, body: { items: [] } });
    renderAt("/processmfg/customer-requirements", ["production.batch-order.read"]);
    expect(await screen.findByText("No customer batch requirements yet")).toBeInTheDocument();
    expect(screen.queryByRole("link", { name: "Define requirement" })).toBeNull();
    fill("Item id", "nope");
    expect(await screen.findByText("Enter the item id as a UUID.")).toBeInTheDocument();
    expect(calls.every((c) => c.url.searchParams.get("itemId") !== "nope")).toBe(true);
  });

  it("shows a load failure with a retry", async () => {
    handler = () => ({ status: 500, body: { code: "COM-0500", detail: "database unavailable" } });
    renderAt("/processmfg/customer-requirements");
    expect(await findAlert()).toHaveTextContent("database unavailable");
    expect(screen.getByRole("button", { name: "Try again" })).toBeInTheDocument();
  });
});

describe("define a customer batch requirement (slice 14)", () => {
  it("sends the rule under one key and opens it", async () => {
    handler = (url, init) => {
      if (init.method === "POST") return { status: 201, body: rule() };
      if (url.pathname.endsWith(`/${RULE}`)) return { status: 200, body: rule() };
      return undefined;
    };
    renderAt("/processmfg/customer-requirements/new");
    fill("Item id", ITEM);
    fill("Customer id", CUSTOMER);
    fill("Sellable days", "60");
    fill("Attribute", "potency");
    fill("Attribute minimum", "40");
    fill("Attribute maximum", "60");
    fill("Valid from", "2027-01-01");
    fireEvent.click(screen.getByLabelText(/must come from one batch/));
    fireEvent.click(screen.getByRole("button", { name: "Define requirement" }));
    await waitFor(() => expect(calls.some((c) => c.method === "POST")).toBe(true));
    const post = calls.find((c) => c.method === "POST")!;
    expect(new Headers(post.init.headers).get("Idempotency-Key")).toBeTruthy();
    expect(post.body).toEqual({
      itemId: ITEM,
      customerId: CUSTOMER,
      sellableDays: 60,
      attribute: "potency",
      min: "40",
      max: "60",
      sameBatch: true,
      validFrom: "2027-01-01",
    });
    expect(await screen.findByText("Retire requirement")).toBeInTheDocument();
  });

  it("validates before calling", async () => {
    renderAt("/processmfg/customer-requirements/new");
    fireEvent.click(screen.getByRole("button", { name: "Define requirement" }));
    expect(await screen.findByText("Enter the item id as a UUID.")).toBeInTheDocument();
    expect(screen.getByText("Enter the customer id as a UUID.")).toBeInTheDocument();
    expect(screen.getByText("Set the sellable days, an attribute range or the same-batch flag.")).toBeInTheDocument();
    fill("Item id", ITEM);
    fill("Customer id", CUSTOMER);
    fill("Sellable days", "4000");
    fill("Attribute", "potency");
    fill("Valid from", "01/02/2027");
    fireEvent.click(screen.getByRole("button", { name: "Define requirement" }));
    expect(await screen.findByText("Enter whole days from 0 to 3650.")).toBeInTheDocument();
    expect(screen.getByText("Enter a minimum or a maximum for the attribute.")).toBeInTheDocument();
    expect(screen.getByText("Enter a date as yyyy-mm-dd.")).toBeInTheDocument();
    expect(calls.filter((c) => c.method === "POST")).toHaveLength(0);
  });

  it("shows a refusal from the service", async () => {
    handler = () => ({ status: 400, body: { code: "COM-0001", detail: "request failed validation" } });
    renderAt("/processmfg/customer-requirements/new");
    fill("Item id", ITEM);
    fill("Customer id", CUSTOMER);
    fill("Sellable days", "30");
    fireEvent.click(screen.getByRole("button", { name: "Define requirement" }));
    expect(await findAlert()).toHaveTextContent("COM-0001");
  });

  it("explains the missing duty instead of offering the form", () => {
    renderAt("/processmfg/customer-requirements/new", ["production.batch-order.read"]);
    expect(screen.getByText(/needs the customer batch requirements duty/)).toBeInTheDocument();
    expect(screen.queryByRole("button", { name: "Define requirement" })).toBeNull();
  });
});

describe("customer batch requirement detail (slice 14)", () => {
  it("retires the rule at the version read and reloads it", async () => {
    let retired = false;
    handler = (url, init) => {
      if (init.method === "POST") {
        retired = true;
        return { status: 200, body: rule({ version: 2, retiredAt: "2026-10-22T08:00:00Z", retiredBy: "planner" }) };
      }
      if (url.pathname.endsWith(`/${RULE}`)) return { status: 200, body: retired ? rule({ version: 2, retiredAt: "2026-10-22T08:00:00Z", retiredBy: "planner" }) : rule() };
      return undefined;
    };
    renderAt(`/processmfg/customer-requirements/${RULE}`);
    fireEvent.click(await screen.findByRole("button", { name: "Retire requirement" }));
    expect(await screen.findByText("This requirement is retired")).toBeInTheDocument();
    const post = calls.find((c) => c.method === "POST")!;
    expect(post.url.pathname).toBe(`/api/v1/production/customer-batch-requirements/${RULE}:retire`);
    expect(post.body).toEqual({ version: 1 });
    expect(new Headers(post.init.headers).get("Idempotency-Key")).toBeTruthy();
    expect(screen.queryByRole("button", { name: "Retire requirement" })).toBeNull();
  });

  it("shows a stale-version refusal with a reload, and hides retire without the duty", async () => {
    handler = (url, init) =>
      init.method === "POST"
        ? { status: 409, body: { code: "PMF-0104", detail: "requirement is at version 2, not 1" } }
        : url.pathname.endsWith(`/${RULE}`)
          ? { status: 200, body: rule() }
          : undefined;
    renderAt(`/processmfg/customer-requirements/${RULE}`);
    fireEvent.click(await screen.findByRole("button", { name: "Retire requirement" }));
    expect(await findAlert()).toHaveTextContent("PMF-0104");
    expect(screen.getByRole("button", { name: "Reload" })).toBeInTheDocument();
  });

  it("shows a missing rule and no retire for a read-only user", async () => {
    handler = () => ({ status: 404, body: { code: "PMF-0113", detail: "customer batch requirement not found" } });
    renderAt(`/processmfg/customer-requirements/${RULE}`, ["production.batch-order.read"]);
    expect(await findAlert()).toHaveTextContent("PMF-0113");
    expect(screen.queryByRole("button", { name: "Retire requirement" })).toBeNull();
  });
});

describe("check stored batches against requirements (slice 14, AC-24.14)", () => {
  const answer = {
    requirement: { sellableDays: 60, sameBatch: false, ranges: {} },
    qualifying: [{ batchId: NEW, qty: "100", expiry: "2027-03-31" }],
    excluded: [
      { batchId: OLD, reason: "45 days of shelf life left at shipment, 60 required", warningCode: "PMF-0009" },
      { batchId: HOLD, reason: "disposition is on-hold", warningCode: "PMF-0009" },
    ],
    qtyAvailable: "100",
    sameBatchId: null,
    applied: [RULE],
    batchNumbers: { [OLD]: "L-45", [NEW]: "L-90", [HOLD]: "L-HOLD" },
  };

  it("sends the line and on-hand quantities and lists qualifying and excluded batches", async () => {
    handler = (url, init) => {
      if (url.pathname.endsWith("/batch-requirements:check-stored")) return { status: 200, body: answer };
      if (init.method === undefined || init.method === "GET") {
        return { status: 200, body: { items: [batch(OLD, "L-45", "available", "2027-02-15"), batch(NEW, "L-90", "available", "2027-03-31"), batch(HOLD, "L-HOLD", "on-hold")] } };
      }
      return undefined;
    };
    const { container } = renderAt("/processmfg/requirement-check-stored");
    fill("Item id", ITEM);
    await findTable({ name: "Batches of the item" });
    fill("Customer id", CUSTOMER);
    fill("Ship date", "2027-01-01");
    fill("Line quantity", "80");
    fill("On hand of batch L-45", "50");
    fill("On hand of batch L-90", "100");
    fill("On hand of batch L-HOLD", "100");
    fireEvent.click(screen.getByRole("button", { name: "Check batches" }));
    const qualifying = await findTable({ name: "Qualifying batches" });
    expect(within(qualifying).getByRole("link", { name: "L-90" })).toHaveAttribute("href", `/processmfg/batches/${NEW}`);
    const excluded = screen.getByRole("table", { name: "Excluded batches" });
    expect(within(excluded).getByText("45 days of shelf life left at shipment, 60 required")).toBeInTheDocument();
    expect(within(excluded).getAllByText("PMF-0009")).toHaveLength(2);
    expect(screen.getByRole("list", { name: "Resolved requirement" })).toHaveTextContent("Sellable days 60");
    const post = calls.find((c) => c.method === "POST")!;
    expect(post.body).toEqual({
      itemId: ITEM,
      customerId: CUSTOMER,
      shipDate: "2027-01-01",
      qty: "80",
      onHand: [
        { batchId: OLD, qty: "50" },
        { batchId: NEW, qty: "100" },
        { batchId: HOLD, qty: "100" },
      ],
    });
    await waitFor(() => expect(checkAxeAA(container).filter((v) => !shellRules.has(v.id))).toEqual([]));
  });

  it("validates before calling and shows the empty state", async () => {
    handler = () => ({ status: 200, body: { items: [] } });
    renderAt("/processmfg/requirement-check-stored");
    fireEvent.click(screen.getByRole("button", { name: "Check batches" }));
    expect(await screen.findByText("Enter the item id as a UUID.")).toBeInTheDocument();
    expect(screen.getByText("Enter the ship date as yyyy-mm-dd.")).toBeInTheDocument();
    expect(screen.getByText("Enter the line quantity above zero.")).toBeInTheDocument();
    fill("Item id", ITEM);
    expect(await screen.findByText("No batches registered for this item")).toBeInTheDocument();
    fill("Ship date", "2027-01-01");
    fill("Line quantity", "1");
    fireEvent.click(screen.getByRole("button", { name: "Check batches" }));
    expect(await screen.findByText("Enter the on-hand quantity of at least one batch.")).toBeInTheDocument();
    expect(calls.filter((c) => c.method === "POST")).toHaveLength(0);
  });

  it("shows the same-batch shortfall from the service", async () => {
    handler = (url, init) =>
      url.pathname.endsWith(":check-stored")
        ? { status: 409, body: { code: "PMF-0008", detail: "no single batch can supply 150 (largest 100)" } }
        : init.method === undefined || init.method === "GET"
          ? { status: 200, body: { items: [batch(NEW, "L-90")] } }
          : undefined;
    renderAt("/processmfg/requirement-check-stored");
    fill("Item id", ITEM);
    await findTable({ name: "Batches of the item" });
    fill("Ship date", "2027-01-01");
    fill("Line quantity", "150");
    fill("On hand of batch L-90", "100");
    fireEvent.click(screen.getByRole("button", { name: "Check batches" }));
    expect(await findAlert()).toHaveTextContent("largest 100");
  });

  it("shows a load failure with a retry", async () => {
    handler = () => ({ status: 500, body: { code: "COM-0500", detail: "database unavailable" } });
    renderAt("/processmfg/requirement-check-stored");
    fill("Item id", ITEM);
    expect(await findAlert()).toHaveTextContent("database unavailable");
    expect(screen.getByRole("button", { name: "Try again" })).toBeInTheDocument();
  });
});
