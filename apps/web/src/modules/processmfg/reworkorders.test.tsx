import { fireEvent, render, screen, waitFor, within } from "@testing-library/react";
import { MemoryRouter, Route, Routes } from "react-router-dom";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { checkAxeAA } from "../../shell/a11y";
import { PrincipalProvider } from "../../shell/principal";
import { findAlert, findTable } from "../../test-utils";
import processmfg from "./module";
import { RELEASE_DUTY, REWORK_DUTY } from "./ReworkOrderPages";

const FORMULA = "2a000000-0000-4000-8000-000000000001";
const BATCH = "5b000000-0000-4000-8000-0000000000b1";
const REWORK = "7e000000-0000-4000-8000-0000000000e1";
const ORDER = "6c000000-0000-4000-8000-0000000000c1";

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

function renderAt(path: string, duties: string[] = [REWORK_DUTY, "production.batch-order.read"]) {
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

const rework = (over: Record<string, unknown> = {}) => ({
  id: REWORK,
  legalEntityId: "11111111-0000-4000-8000-000000000001",
  batchOrderId: ORDER,
  orderNumber: "BO-000007",
  orderStatus: "planned",
  sourceBatchId: BATCH,
  sourceBatchNumber: "L-1",
  itemId: "1a000000-0000-4000-8000-000000000001",
  inputQty: "80",
  outputQty: "78",
  keepBatchNumber: true,
  qualityOrderRequired: true,
  outputDisposition: "quarantine",
  varianceType: "rework",
  status: "planned",
  version: 1,
  createdBy: "u",
  createdAt: "2026-10-20T08:00:00Z",
  updatedAt: "2026-10-20T08:00:00Z",
  ...over,
});

function fill(label: string, value: string) {
  fireEvent.change(screen.getByLabelText(label), { target: { value } });
}

describe("rework order list", () => {
  it("lists the rework orders, filtered by status", async () => {
    handler = (url) => (url.pathname === "/api/v1/production/rework-orders" ? { status: 200, body: { items: [rework(), rework({ id: "x", status: "received", sourceBatchNumber: "L-2" })] } } : undefined);
    const { container } = renderAt("/processmfg/rework-orders");
    const table = await findTable({ name: "Rework orders" });
    expect(within(table).getByText("L-1")).toBeInTheDocument();
    expect(within(table).getByText("Received")).toBeInTheDocument();
    expect(screen.getByRole("link", { name: "New rework order" })).toHaveAttribute("href", "/processmfg/rework-orders/new");
    fill("Status", "planned");
    await waitFor(() => expect(calls.some((c) => c.url.searchParams.get("status") === "planned")).toBe(true));
    await waitFor(() => expect(checkAxeAA(container).filter((v) => !shellRules.has(v.id))).toEqual([]));
  });

  it("shows the empty state and hides the create link without the duty", async () => {
    handler = () => ({ status: 200, body: { items: [] } });
    renderAt("/processmfg/rework-orders", ["production.batch-order.read"]);
    expect(await screen.findByText("No rework orders yet")).toBeInTheDocument();
    expect(screen.queryByRole("link", { name: "New rework order" })).toBeNull();
  });

  it("shows a load failure with a retry", async () => {
    handler = () => ({ status: 500, body: { code: "COM-0500", detail: "database unavailable" } });
    renderAt("/processmfg/rework-orders");
    expect(await findAlert()).toHaveTextContent("database unavailable");
    expect(screen.getByRole("button", { name: "Try again" })).toBeInTheDocument();
  });
});

describe("create a rework order", () => {
  function fillForm() {
    fill("Rework formula id", FORMULA);
    fill("Batch size", "80");
    fill("Batch id", BATCH);
    fill("Quantity of the batch", "100");
    fill("Quantity to rework", "80");
    fill("Planned output", "78");
  }

  it("reads the batch version, creates under one key with the defaults and opens the order", async () => {
    handler = (url, init) => {
      if (init.method === "POST") return { status: 201, body: rework() };
      if (url.pathname.endsWith(`/batches/${BATCH}`)) return { status: 200, body: { id: BATCH, version: 4 } };
      if (url.pathname.endsWith(`/rework-orders/${REWORK}`)) return { status: 200, body: rework() };
      return undefined;
    };
    renderAt("/processmfg/rework-orders/new");
    fillForm();
    fireEvent.click(screen.getByRole("button", { name: "Create rework order" }));
    await waitFor(() => expect(calls.some((c) => c.method === "POST")).toBe(true));
    const post = calls.find((c) => c.method === "POST")!;
    expect(post.url.pathname).toBe("/api/v1/production/rework-orders");
    expect(post.body).toEqual({
      formulaId: FORMULA,
      batchSize: "80",
      sourceBatchId: BATCH,
      sourceVersion: 4,
      sourceQty: "100",
      reworkQty: "80",
      outputQty: "78",
      params: { keepBatchNumber: true, requireQualityOrder: true, outputDisposition: "quarantine", capitalize: false },
    });
    expect(new Headers(post.init.headers).get("Idempotency-Key")).toBeTruthy();
    expect(await screen.findByText("Rework of L-1")).toBeInTheDocument();
  });

  it("checks the form before calling", async () => {
    renderAt("/processmfg/rework-orders/new");
    fillForm();
    fill("Quantity to rework", "150");
    fill("Output disposition", "available");
    fireEvent.click(screen.getByRole("button", { name: "Create rework order" }));
    expect(await screen.findByText("The rework quantity cannot exceed the batch quantity.")).toBeInTheDocument();
    expect(screen.getByText("The output cannot be available while a quality order is required.")).toBeInTheDocument();
    expect(calls).toHaveLength(0);
  });

  it("shows the service's refusal", async () => {
    handler = (url, init) =>
      init.method === "POST"
        ? { status: 409, body: { code: "PMF-0132", detail: "batch L-1 already has an open rework order" } }
        : url.pathname.endsWith(`/batches/${BATCH}`)
          ? { status: 200, body: { id: BATCH, version: 4 } }
          : undefined;
    renderAt("/processmfg/rework-orders/new");
    fillForm();
    fireEvent.click(screen.getByRole("button", { name: "Create rework order" }));
    expect(await findAlert()).toHaveTextContent("batch L-1 already has an open rework order");
  });

  it("explains the missing duty", () => {
    renderAt("/processmfg/rework-orders/new", ["production.batch-order.read"]);
    expect(screen.getByText(/needs the batch order duty/)).toBeInTheDocument();
  });
});

describe("rework order detail", () => {
  it("waits for the batch order to be reported before receiving", async () => {
    handler = () => ({ status: 200, body: rework() });
    renderAt(`/processmfg/rework-orders/${REWORK}`);
    expect(await screen.findByText(/must be reported as finished first; it is planned/)).toBeInTheDocument();
    expect(screen.queryByRole("button", { name: "Receive output" })).toBeNull();
  });

  it("receives a kept-number rework under the version read", async () => {
    let received = false;
    handler = (_url, init) => {
      if (init.method === "POST") {
        received = true;
        return { status: 200, body: rework({ status: "received" }) };
      }
      return { status: 200, body: received ? rework({ status: "received", orderStatus: "reported", receivedQty: "78", outputBatchId: BATCH, outputBatchNumber: "L-1", version: 2 }) : rework({ orderStatus: "reported" }) };
    };
    renderAt(`/processmfg/rework-orders/${REWORK}`);
    fireEvent.click(await screen.findByRole("button", { name: "Receive output" }));
    await waitFor(() => expect(calls.some((c) => c.method === "POST")).toBe(true));
    const post = calls.find((c) => c.method === "POST")!;
    expect(post.url.pathname).toBe(`/api/v1/production/rework-orders/${REWORK}:receive`);
    expect(post.body).toEqual({ version: 1 });
    await waitFor(() => expect(screen.queryByRole("button", { name: "Receive output" })).toBeNull());
    expect(screen.getAllByText("Received").length).toBeGreaterThan(0);
    expect(screen.getAllByText("78")).toHaveLength(2);
  });

  it("asks for the new batch number when the number is not kept", async () => {
    handler = (_url, init) => (init.method === "POST" ? { status: 200, body: rework() } : { status: 200, body: rework({ orderStatus: "ended", keepBatchNumber: false }) });
    renderAt(`/processmfg/rework-orders/${REWORK}`);
    fireEvent.click(await screen.findByRole("button", { name: "Receive output" }));
    expect(await screen.findByText(/Enter the new batch number\./)).toBeInTheDocument();
    expect(calls.some((c) => c.method === "POST")).toBe(false);
    fill("New batch number", "L-1R");
    fireEvent.click(screen.getByRole("button", { name: "Receive output" }));
    await waitFor(() => expect(calls.some((c) => c.method === "POST")).toBe(true));
    expect(calls.find((c) => c.method === "POST")!.body).toEqual({ version: 1, newBatchNumber: "L-1R" });
  });

  it("offers a reload on a stale version and shows a load failure", async () => {
    handler = (_url, init) => (init.method === "POST" ? { status: 409, body: { code: "PMF-0104", detail: "stale" } } : { status: 200, body: rework({ orderStatus: "reported" }) });
    renderAt(`/processmfg/rework-orders/${REWORK}`);
    fireEvent.click(await screen.findByRole("button", { name: "Receive output" }));
    expect(await findAlert()).toHaveTextContent("stale");
    expect(screen.getByRole("button", { name: "Reload" })).toBeInTheDocument();
  });

  it("shows a load failure", async () => {
    handler = () => ({ status: 404, body: { code: "PMF-0131", detail: "rework order not found" } });
    renderAt(`/processmfg/rework-orders/${REWORK}`);
    expect(await findAlert()).toHaveTextContent("rework order not found");
  });
});

describe("rework order release duty and genealogy", () => {
  it("leaves the receive of an available output to the disposition duty", async () => {
    handler = () => ({ status: 200, body: rework({ orderStatus: "reported", qualityOrderRequired: false, outputDisposition: "available" }) });
    const { unmount } = renderAt(`/processmfg/rework-orders/${REWORK}`);
    expect(await screen.findByText(/needs the batch disposition duty/)).toBeInTheDocument();
    expect(screen.queryByRole("button", { name: "Receive output" })).toBeNull();
    unmount();
    renderAt(`/processmfg/rework-orders/${REWORK}`, [REWORK_DUTY, RELEASE_DUTY, "production.batch-order.read"]);
    expect(await screen.findByRole("button", { name: "Receive output" })).toBeInTheDocument();
  });

  it("shows the rework orders of a batch in its genealogy (AC-24.18)", async () => {
    handler = (url) => {
      if (url.pathname === `/api/v1/production/batches/${BATCH}`)
        return { status: 200, body: { id: BATCH, legalEntityId: "11111111-0000-4000-8000-000000000001", itemId: "1a000000-0000-4000-8000-000000000001", batchNumber: "L-1", disposition: "quarantine", version: 3, createdBy: "u", createdAt: "2026-10-20T08:00:00Z", updatedAt: "2026-10-20T08:00:00Z", attributes: [], attributeHistory: [], dispositions: [] } };
      if (url.pathname === `/api/v1/production/batches/${BATCH}/genealogy`)
        return { status: 200, body: { batchId: BATCH, parents: [], children: [], reworks: [rework({ status: "received", outputBatchId: BATCH, outputBatchNumber: "L-1", receivedQty: "78" })] } };
      if (url.pathname.endsWith("/batch-attribute-definitions")) return { status: 200, body: { items: [] } };
      return { status: 200, body: { items: [] } };
    };
    renderAt(`/processmfg/batches/${BATCH}`);
    const table = await findTable({ name: "Rework orders" });
    expect(within(table).getByRole("link", { name: "BO-000007" })).toHaveAttribute("href", `/processmfg/rework-orders/${REWORK}`);
    expect(within(table).getByText("Kept")).toBeInTheDocument();
    expect(within(table).getByText("78")).toBeInTheDocument();
  });
});
