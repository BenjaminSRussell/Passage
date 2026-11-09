import { findAlert, findTable } from "../../test-utils";
import { fireEvent, render, screen, waitFor, within } from "@testing-library/react";
import { MemoryRouter, Route, Routes } from "react-router-dom";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { checkAxeAA } from "../../shell/a11y";
import inventory from "./module";
import { validateBlocking } from "./NewBlockingPage";
import { validateCountPlan } from "./NewCountPlanPage";
import { validateReservation } from "./NewReservationPage";
import { emptyDimensions } from "./parts";

const PRODUCT_ID = "5f2c1e7a-0000-4000-8000-000000000002";
const ENTITY_ID = "9e8d7c6b-0000-4000-8000-000000000003";
const JOURNAL_ID = "0a1b2c3d-0000-4000-8000-000000000001";
const RES_ID = "7a7a7a7a-0000-4000-8000-000000000004";
const BLK_ID = "6b6b6b6b-0000-4000-8000-000000000005";
const PLAN_ID = "4c4c4c4c-0000-4000-8000-000000000006";
const SHEET_ID = "3d3d3d3d-0000-4000-8000-000000000007";

type Handler = (url: URL, init: RequestInit) => { status: number; body: unknown };

const shellRules = new Set(["page-has-heading-one", "landmark-unique", "region", "bypass"]);
function pageViolations(container: HTMLElement) {
  return checkAxeAA(container).filter((v) => !shellRules.has(v.id));
}

let handler: Handler;
let calls: { method: string; url: URL; init: RequestInit }[];

const json = (status: number, body: unknown) => ({ status, body });
const sent = (method: string) => calls.filter((c) => c.method === method);
const bodyOf = (call?: { init: RequestInit }) => JSON.parse((call?.init.body as string) ?? "null");
const header = (call: { init: RequestInit } | undefined, name: string) => new Headers(call?.init.headers).get(name);

beforeEach(() => {
  calls = [];
  vi.stubGlobal(
    "fetch",
    vi.fn(async (input: string, init: RequestInit = {}) => {
      const url = new URL(input, "http://localhost");
      calls.push({ method: init.method ?? "GET", url, init });
      const { status, body } = handler(url, init);
      return new Response(JSON.stringify(body), { status, headers: { "Content-Type": "application/json" } });
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
        {inventory.routes.map((route) => (
          <Route key={route.path} path={route.path} element={route.element} />
        ))}
      </Routes>
    </MemoryRouter>,
  );
}

const txn = (overrides: Record<string, unknown> = {}) => ({
  id: "11111111-0000-4000-8000-000000000001",
  legalEntityId: ENTITY_ID,
  originId: "o1",
  sourceType: "INVENT_JOURNAL_LINE",
  sourceId: "s1",
  journalId: JOURNAL_ID,
  productId: PRODUCT_ID,
  dimensions: { site: "S1", batch: "B1" },
  qty: "-4",
  enteredQty: "-4",
  enteredUnit: "EA",
  issueStatus: "SOLD",
  physicalDate: "2026-09-01",
  financialDate: "2026-09-01",
  currency: "USD",
  costAmountPhysical: "-10.00",
  costAmountPosted: "-10.00",
  createdAt: "2026-09-01T08:00:00Z",
  ...overrides,
});

const reservation = (overrides: Record<string, unknown> = {}) => ({
  id: RES_ID,
  legalEntityId: ENTITY_ID,
  productId: PRODUCT_ID,
  dimensions: { site: "S1", batch: "B3" },
  qty: "5",
  demandType: "SALES_ORDER",
  demandId: "SO-1",
  policy: "FEFO",
  status: "ACTIVE",
  allocationNo: 1,
  createdAt: "2026-09-01T08:00:00Z",
  ...overrides,
});

const blocking = (overrides: Record<string, unknown> = {}) => ({
  id: BLK_ID,
  legalEntityId: ENTITY_ID,
  productId: PRODUCT_ID,
  dimensions: { site: "S1", batch: "B1" },
  qty: "4",
  unit: "EA",
  targetStatus: "QUARANTINE",
  reason: "RECALL",
  note: "lot recall",
  state: "ACTIVE",
  physicalDate: "2026-09-02",
  version: 1,
  ...overrides,
});

const sheet = (overrides: Record<string, unknown> = {}) => ({
  id: SHEET_ID,
  legalEntityId: ENTITY_ID,
  planId: PLAN_ID,
  planName: "Weekly W1",
  number: "CNT-ABCD1234",
  status: "OPEN",
  blind: true,
  version: 2,
  lineCount: 1,
  countedCount: 0,
  createdAt: "2026-09-01T08:00:00Z",
  lines: [{ id: "line-1", lineNo: 1, productId: PRODUCT_ID, dimensions: { site: "S1", warehouse: "W1" }, expectedQty: null, countedQty: null, postedVariance: null }],
  ...overrides,
});

describe("Transaction inquiry", () => {
  it("lists transactions with status, value and a link to the journal", async () => {
    handler = () => json(200, { items: [txn(), txn({ id: "22222222-0000-4000-8000-000000000002", journalId: undefined, sourceType: "BLOCKING", reference: BLK_ID, issueStatus: undefined, receiptStatus: "PURCHASED", qty: "4" })] });
    const { container } = renderAt("/inventory/transactions");
    expect(screen.getByRole("table", { name: "Transactions" })).toHaveAttribute("aria-busy", "true");
    const table = await findTable({ name: "Transactions" });
    await waitFor(() => expect(table).toHaveTextContent("Issue · Sold"));
    expect(table).toHaveTextContent("-10.00 USD");
    expect(table).toHaveTextContent("Receipt · Purchased");
    expect(table).toHaveTextContent("Blocking 6b6b6b6b");
    expect(within(table).getByRole("link", { name: "Journal 0a1b2c3d" })).toHaveAttribute("href", `/inventory/journals/${JOURNAL_ID}`);
    expect(pageViolations(container)).toEqual([]);
  });

  it("applies filters, refuses bad ones locally and loads more", async () => {
    handler = (url) => (url.searchParams.get("cursor") ? json(200, { items: [txn({ id: "33333333-0000-4000-8000-000000000003", qty: "-9" })] }) : json(200, { items: [txn()], nextCursor: "c1" }));
    renderAt("/inventory/transactions");
    fireEvent.click(await screen.findByRole("button", { name: "Load more transactions" }));
    await waitFor(() => expect(screen.getByRole("table", { name: "Transactions" })).toHaveTextContent("-9 EA"));
    const before = calls.length;
    fireEvent.change(screen.getByLabelText("Product"), { target: { value: "SHIRT" } });
    fireEvent.click(screen.getByRole("button", { name: "Apply filters" }));
    expect(await screen.findByText("Enter the product id (a UUID).")).toBeInTheDocument();
    expect(calls.length).toBe(before);
    fireEvent.change(screen.getByLabelText("Product"), { target: { value: PRODUCT_ID } });
    fireEvent.change(screen.getByLabelText("Status"), { target: { value: "SOLD" } });
    fireEvent.change(screen.getByLabelText("From"), { target: { value: "2026-09-01" } });
    fireEvent.click(screen.getByRole("button", { name: "Apply filters" }));
    await waitFor(() => expect(calls.at(-1)?.url.searchParams.get("status")).toBe("SOLD"));
    expect(calls.at(-1)?.url.searchParams.get("product")).toBe(PRODUCT_ID);
    expect(calls.at(-1)?.url.searchParams.get("from")).toBe("2026-09-01");
  });

  it("explains an empty result and shows failures", async () => {
    handler = () => json(200, { items: [] });
    const first = renderAt("/inventory/transactions");
    expect(await screen.findByText("No transactions match")).toBeInTheDocument();
    first.unmount();
    handler = () => json(403, { code: "AUTH-0003", detail: "missing duty inventory.read" });
    renderAt("/inventory/transactions");
    expect(await findAlert()).toHaveTextContent("missing duty inventory.read");
  });
});

describe("Reservations", () => {
  it("lists active reservations and releases one", async () => {
    let released = false;
    handler = (_url, init) => {
      if (init.method === "DELETE") {
        released = true;
        return json(200, reservation({ status: "RELEASED" }));
      }
      return json(200, { items: released ? [] : [reservation()] });
    };
    const { container } = renderAt("/inventory/reservations");
    const table = await findTable({ name: "Reservations" });
    await waitFor(() => expect(table).toHaveTextContent("Sales order SO-1"));
    expect(calls[0].url.searchParams.get("status")).toBe("ACTIVE");
    expect(table).toHaveTextContent("Site S1 · Batch B3");
    expect(pageViolations(container)).toEqual([]);
    fireEvent.click(within(table).getByRole("button", { name: "Release reservation 7a7a7a7a" }));
    expect(await screen.findByText("No reservations")).toBeInTheDocument();
    expect(screen.getByRole("status")).toHaveTextContent("Reservation 7a7a7a7a released: 5 available again.");
    expect(sent("DELETE")[0].url.pathname).toBe(`/api/v1/inventory/reservations/${RES_ID}`);
  });

  it("shows a failed release with its code", async () => {
    handler = (_url, init) => (init.method === "DELETE" ? json(404, { code: "INV-RES-003", detail: "reservation not found" }) : json(200, { items: [reservation()] }));
    renderAt("/inventory/reservations");
    fireEvent.click(await screen.findByRole("button", { name: "Release reservation 7a7a7a7a" }));
    expect(await findAlert()).toHaveTextContent("reservation not found");
  });
});

describe("New reservation", () => {
  const values = {
    legalEntityId: ENTITY_ID,
    productId: PRODUCT_ID,
    qty: "5",
    policy: "FEFO" as const,
    demandType: "SALES_ORDER" as const,
    demandId: "SO-1",
    requiredDate: "2026-09-01",
    shelfLifeMarginDays: "7",
    ttlHours: "",
    allowPartial: false,
    dimensions: emptyDimensions(),
    segmentType: "NONE" as const,
    segmentRef: "",
    segmentFallback: true,
  };

  it("validates before calling the service", () => {
    expect(validateReservation(values)).toEqual({});
    expect(validateReservation({ ...values, policy: "MANUAL" })).toEqual({ dimensions: "A manual reservation names the site and every dimension of the stock." });
    expect(validateReservation({ ...values, qty: "0", demandId: " ", shelfLifeMarginDays: "400", productId: "x" })).toEqual({
      qty: "Enter a quantity above zero.",
      demandId: "Enter the order or reference the stock is held for.",
      shelfLifeMarginDays: "Enter 0 to 365 days.",
      productId: "Enter the product id (a UUID).",
    });
  });

  it("reserves by policy and shows the allocations and shortfall", async () => {
    handler = () => json(201, { items: [reservation(), reservation({ id: "r2", allocationNo: 2, dimensions: { site: "S1", batch: "B5" }, qty: "3" })], shortfall: "2" });
    const { container } = renderAt("/inventory/reservations/new");
    expect(pageViolations(container)).toEqual([]);
    fireEvent.change(screen.getByLabelText("Legal entity"), { target: { value: ENTITY_ID } });
    fireEvent.change(screen.getByLabelText("Product"), { target: { value: PRODUCT_ID } });
    fireEvent.change(screen.getByLabelText("Quantity"), { target: { value: "10" } });
    fireEvent.change(screen.getByLabelText("Policy"), { target: { value: "FEFO" } });
    fireEvent.change(screen.getByLabelText("Demand reference"), { target: { value: "SO-1" } });
    fireEvent.change(screen.getByLabelText("Site"), { target: { value: "S1" } });
    fireEvent.click(screen.getByLabelText("Reserve what is available if not all of it is"));
    fireEvent.click(screen.getByRole("button", { name: "Reserve" }));
    const allocations = await findTable({ name: "Allocations" });
    expect(allocations).toHaveTextContent("Batch B5");
    expect(screen.getByRole("status")).toHaveTextContent("2 could not be reserved");
    const post = sent("POST")[0];
    expect(bodyOf(post)).toMatchObject({ policy: "FEFO", qty: "10", demandId: "SO-1", allowPartial: true, dimensions: { site: "S1" } });
    expect(bodyOf(post).dimensions).toEqual({ site: "S1" });
    expect(header(post, "Idempotency-Key")).toBeTruthy();
  });

  it("puts service field errors on fields and rule failures in an alert", async () => {
    handler = () => json(422, { code: "INV-RES-001", detail: "only 3 available to reserve, 10 requested" });
    renderAt("/inventory/reservations/new");
    fireEvent.change(screen.getByLabelText("Legal entity"), { target: { value: ENTITY_ID } });
    fireEvent.change(screen.getByLabelText("Product"), { target: { value: PRODUCT_ID } });
    fireEvent.change(screen.getByLabelText("Quantity"), { target: { value: "10" } });
    fireEvent.change(screen.getByLabelText("Demand reference"), { target: { value: "SO-1" } });
    fireEvent.click(screen.getByRole("button", { name: "Reserve" }));
    expect(await findAlert()).toHaveTextContent("only 3 available to reserve, 10 requested");
    handler = () => json(400, { code: "COM-0001", detail: "one or more fields are invalid", errors: { demandId: "is required (1 to 60 characters)" } });
    fireEvent.click(screen.getByRole("button", { name: "Reserve" }));
    expect(await screen.findByText("is required (1 to 60 characters)")).toBeInTheDocument();
  });
});

describe("Blocking", () => {
  it("lists active blockings and releases one at its version", async () => {
    let released = false;
    handler = (_url, init) => {
      if (init.method === "POST") {
        released = true;
        return json(200, blocking({ state: "RELEASED", disposition: "RELEASE", version: 2 }));
      }
      return json(200, { items: released ? [] : [blocking()] });
    };
    const { container } = renderAt("/inventory/blockings");
    const table = await findTable({ name: "Blockings" });
    await waitFor(() => expect(table).toHaveTextContent("Quarantine"));
    expect(table).toHaveTextContent("Recall: lot recall");
    expect(table).toHaveTextContent("4 EA");
    expect(pageViolations(container)).toEqual([]);
    fireEvent.click(within(table).getByRole("button", { name: "Release blocking 6b6b6b6b" }));
    expect(await screen.findByText("No blocked stock")).toBeInTheDocument();
    const post = sent("POST")[0];
    expect(post.url.pathname).toBe(`/api/v1/inventory/blockings/${BLK_ID}:release`);
    expect(header(post, "If-Match")).toBe('"1"');
    expect(bodyOf(post)).toEqual({ disposition: "RELEASE" });
  });

  it("shows load failures", async () => {
    handler = () => json(500, { code: "COM-0500", detail: "internal error" });
    renderAt("/inventory/blockings");
    expect(await findAlert()).toHaveTextContent("internal error");
  });

  it("validates a blocking before sending it", () => {
    const base = { legalEntityId: ENTITY_ID, productId: PRODUCT_ID, qty: "2", unit: "EA", targetStatus: "BLOCKED" as const, reason: "QUALITY" as const, note: "", physicalDate: "2026-09-01", expectedReleaseDate: "", dimensions: { ...emptyDimensions(), site: "S1" } };
    expect(validateBlocking(base)).toEqual({});
    expect(validateBlocking({ ...base, dimensions: emptyDimensions() })).toEqual({ dimensions: "Site is required." });
    expect(validateBlocking({ ...base, dimensions: { ...base.dimensions, serial: "SN-1" } }).qty).toBe("A serial number is blocked one unit at a time.");
  });

  it("blocks stock and returns to the list", async () => {
    handler = (_url, init) => (init.method === "POST" ? json(201, blocking()) : json(200, { items: [blocking()] }));
    const { container } = renderAt("/inventory/blockings/new");
    expect(pageViolations(container)).toEqual([]);
    fireEvent.change(screen.getByLabelText("Legal entity"), { target: { value: ENTITY_ID } });
    fireEvent.change(screen.getByLabelText("Product"), { target: { value: PRODUCT_ID } });
    fireEvent.change(screen.getByLabelText("Quantity"), { target: { value: "4" } });
    fireEvent.change(screen.getByLabelText("Block as"), { target: { value: "QUARANTINE" } });
    fireEvent.change(screen.getByLabelText("Reason"), { target: { value: "RECALL" } });
    fireEvent.change(screen.getByLabelText("Site"), { target: { value: "S1" } });
    fireEvent.change(screen.getByLabelText("Batch"), { target: { value: "B1" } });
    fireEvent.click(screen.getByRole("button", { name: "Block stock" }));
    expect(await screen.findByRole("heading", { name: "Blocking and quarantine" })).toBeInTheDocument();
    const post = sent("POST")[0];
    expect(bodyOf(post)).toMatchObject({ qty: "4", unit: "EA", targetStatus: "QUARANTINE", reason: "RECALL", dimensions: { site: "S1", batch: "B1" } });
    expect(header(post, "Idempotency-Key")).toBeTruthy();
  });

  it("explains a rejected blocking", async () => {
    handler = () => json(422, { code: "INV-BLK-003", detail: "blocked quantity 9 exceeds available 4" });
    renderAt("/inventory/blockings/new");
    fireEvent.change(screen.getByLabelText("Legal entity"), { target: { value: ENTITY_ID } });
    fireEvent.change(screen.getByLabelText("Product"), { target: { value: PRODUCT_ID } });
    fireEvent.change(screen.getByLabelText("Quantity"), { target: { value: "9" } });
    fireEvent.change(screen.getByLabelText("Site"), { target: { value: "S1" } });
    fireEvent.click(screen.getByRole("button", { name: "Block stock" }));
    expect(await findAlert()).toHaveTextContent("exceeds available 4");
  });
});

describe("Cycle counting", () => {
  const plan = { id: PLAN_ID, legalEntityId: ENTITY_ID, name: "Weekly W1", warehouse: "W1", currency: "USD", unit: "EA", tolerancePct: "5", toleranceValue: "100", blind: true, createdAt: "2026-09-01T08:00:00Z" };

  it("lists plans and generates a sheet, opening it", async () => {
    handler = (url, init) => {
      if (init.method === "POST") return json(201, sheet());
      if (url.pathname.endsWith(`/count-sheets/${SHEET_ID}`)) return json(200, sheet());
      return json(200, { items: [plan] });
    };
    const { container } = renderAt("/inventory/count-plans");
    const table = await findTable({ name: "Count plans" });
    await waitFor(() => expect(table).toHaveTextContent("5 % or 100 USD"));
    expect(pageViolations(container)).toEqual([]);
    fireEvent.click(within(table).getByRole("button", { name: "Generate sheet for Weekly W1" }));
    expect(await screen.findByRole("heading", { name: "Count sheet CNT-ABCD1234" })).toBeInTheDocument();
    const post = sent("POST")[0];
    expect(post.url.pathname).toBe(`/api/v1/inventory/count-plans/${PLAN_ID}:generate`);
    expect(header(post, "Idempotency-Key")).toBeTruthy();
  });

  it("explains an empty plan list", async () => {
    handler = () => json(200, { items: [] });
    renderAt("/inventory/count-plans");
    expect(await screen.findByText("No count plans yet")).toBeInTheDocument();
  });

  it("validates and creates a count plan", async () => {
    const values = { legalEntityId: ENTITY_ID, name: "Weekly", warehouse: "", currency: "USD", unit: "EA", tolerancePct: "5", toleranceValue: "100", blind: false, freezeStock: false, fourEyes: false, locationFrom: "", locationTo: "", abcClass: "" as const, samplePct: "", minDaysSinceCount: "" };
    expect(validateCountPlan(values)).toEqual({});
    expect(validateCountPlan({ ...values, tolerancePct: "120", toleranceValue: "-1", name: "" })).toEqual({
      name: "Name the plan (up to 120 characters).",
      tolerancePct: "Enter a percentage from 0 to 100.",
      toleranceValue: "Enter an amount of zero or more.",
    });
    handler = (_url, init) => (init.method === "POST" ? json(201, plan) : json(200, { items: [plan] }));
    const { container } = renderAt("/inventory/count-plans/new");
    expect(pageViolations(container)).toEqual([]);
    fireEvent.change(screen.getByLabelText("Legal entity"), { target: { value: ENTITY_ID } });
    fireEvent.change(screen.getByLabelText("Name"), { target: { value: "Weekly W1" } });
    fireEvent.change(screen.getByLabelText("Warehouse"), { target: { value: "W1" } });
    fireEvent.change(screen.getByLabelText("Currency"), { target: { value: "usd" } });
    fireEvent.click(screen.getByLabelText("Blind count"));
    fireEvent.click(screen.getByRole("button", { name: "Create count plan" }));
    expect(await screen.findByRole("heading", { name: "Count plans" })).toBeInTheDocument();
    expect(bodyOf(sent("POST")[0])).toEqual({ legalEntityId: ENTITY_ID, name: "Weekly W1", warehouse: "W1", currency: "USD", unit: "EA", tolerancePct: "5", toleranceValue: "100", blind: true, freezeStock: false, fourEyes: false });
  });

  it("lists open sheets and opens one", async () => {
    handler = (url) => (url.pathname.endsWith(`/count-sheets/${SHEET_ID}`) ? json(200, sheet()) : json(200, { items: [sheet({ lines: undefined })] }));
    renderAt("/inventory/count-sheets");
    const table = await findTable({ name: "Count sheets" });
    await waitFor(() => expect(table).toHaveTextContent("0 of 1"));
    expect(calls[0].url.searchParams.get("status")).toBe("OPEN");
    fireEvent.click(within(table).getByRole("button", { name: "CNT-ABCD1234" }));
    expect(await screen.findByRole("heading", { name: "Count sheet CNT-ABCD1234" })).toBeInTheDocument();
  });

  it("hides blind expected quantities, records a count at the sheet version and posts", async () => {
    const counted = sheet({ version: 3, countedCount: 1, lines: [{ ...sheet().lines[0], countedQty: "46" }] });
    const posted = sheet({ version: 4, status: "POSTED", countedCount: 1, journalId: JOURNAL_ID, approvedBy: "u1", lines: [{ ...sheet().lines[0], expectedQty: "50", countedQty: "46", postedVariance: "-4" }] });
    handler = (_url, init) => {
      if (init.method === "PUT") return json(200, counted);
      if (init.method === "POST") return json(200, posted);
      return json(200, sheet());
    };
    const { container } = renderAt(`/inventory/count-sheets/${SHEET_ID}`);
    expect(screen.getByText("Loading count sheet…")).toBeInTheDocument();
    const table = await findTable({ name: "Count lines" });
    expect(table).toHaveTextContent("Hidden");
    expect(screen.getByText("Blind count")).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "Post count" })).toBeDisabled();
    expect(pageViolations(container)).toEqual([]);

    fireEvent.change(within(table).getByLabelText("Counted quantity, line 1"), { target: { value: "46" } });
    fireEvent.click(within(table).getByRole("button", { name: "Save count, line 1" }));
    await waitFor(() => expect(screen.getByRole("button", { name: "Post count" })).toBeEnabled());
    const put = sent("PUT")[0];
    expect(put.url.pathname).toBe(`/api/v1/inventory/count-sheets/${SHEET_ID}/lines/line-1`);
    expect(header(put, "If-Match")).toBe('"2"');
    expect(bodyOf(put)).toEqual({ countedQty: "46" });

    fireEvent.click(screen.getByRole("button", { name: "Post count" }));
    await waitFor(() => expect(screen.getByRole("table", { name: "Count lines" })).toHaveTextContent("-4"));
    expect(header(sent("POST")[0], "If-Match")).toBe('"3"');
    expect(screen.getByRole("status")).toHaveTextContent("Variances went to count journal 0a1b2c3d");
    expect(screen.getByRole("link", { name: "Open the count journal" })).toHaveAttribute("href", `/inventory/journals/${JOURNAL_ID}`);
    expect(screen.queryByRole("button", { name: "Post count" })).not.toBeInTheDocument();
  });

  it("refuses a negative count locally and explains a variance that needs approval", async () => {
    const counted = sheet({ version: 3, countedCount: 1, lines: [{ ...sheet().lines[0], countedQty: "46" }] });
    handler = (_url, init) => (init.method === "POST" ? json(403, { code: "INV-CNT-001", detail: "line 1 variance -4 against book 50 exceeds the tolerance" }) : json(200, counted));
    renderAt(`/inventory/count-sheets/${SHEET_ID}`);
    const table = await findTable({ name: "Count lines" });
    fireEvent.change(within(table).getByLabelText("Counted quantity, line 1"), { target: { value: "-2" } });
    fireEvent.click(within(table).getByRole("button", { name: "Save count, line 1" }));
    await waitFor(() => expect(within(table).getByLabelText("Counted quantity, line 1")).toHaveAttribute("aria-invalid", "true"));
    expect(sent("PUT")).toHaveLength(0);
    fireEvent.click(screen.getByRole("button", { name: "Post count" }));
    const alert = await findAlert();
    expect(alert).toHaveTextContent("Supervisor approval required");
    expect(alert).toHaveTextContent("INV-CNT-001");
  });

  it("shows a not-found state for an unknown sheet", async () => {
    handler = () => json(404, { code: "INV-CNT-004", detail: "count sheet not found" });
    renderAt(`/inventory/count-sheets/${SHEET_ID}`);
    expect(await screen.findByRole("heading", { name: "Count sheet not found" })).toBeInTheDocument();
  });
});
