import { findAlert, findTable } from "../../test-utils";
import { fireEvent, render, screen, waitFor, within } from "@testing-library/react";
import { MemoryRouter, Route, Routes } from "react-router-dom";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { checkAxeAA } from "../../shell/a11y";
import processmfg from "./module";

const ITEM = "1a000000-0000-4000-8000-000000000001";

type Reply = { status: number; body: unknown };
type Handler = (url: URL, init: RequestInit) => Reply | undefined;

const shellRules = new Set(["page-has-heading-one", "landmark-unique", "region", "bypass"]);
function pageViolations(container: HTMLElement) {
  return checkAxeAA(container).filter((v) => !shellRules.has(v.id));
}

let handler: Handler;
let calls: { method: string; url: URL; init: RequestInit; body: unknown }[];

function json(status: number, body: unknown): Reply {
  return { status, body };
}

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
        {processmfg.routes.map((route) => (
          <Route key={route.path} path={route.path} element={route.element} />
        ))}
      </Routes>
    </MemoryRouter>,
  );
}



const BATCH_OLD = "5b000000-0000-4000-8000-000000000001";
const BATCH_HOLD = "5b000000-0000-4000-8000-000000000002";

const batch = (id: string, batchNumber: string, disposition: string, expiryDate?: string) => ({
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

describe("pick from stored batches (slice 12)", () => {
  it("lists the item's batches, sends only on-hand quantities and shows batch numbers (FR-24.6, AC-24.10)", async () => {
    handler = (url) => {
      if (url.pathname === "/api/v1/production/batches") {
        return json(200, { items: [batch(BATCH_OLD, "L-OLD", "available", "2027-02-01"), batch(BATCH_HOLD, "L-HOLD", "on-hold")] });
      }
      if (url.pathname === "/api/v1/production/batch-picking:propose-stored") {
        return json(200, {
          picked: [{ batchId: BATCH_OLD, qty: "50", expiry: "2027-02-01", expiredOverride: false }],
          excluded: [{ batchId: BATCH_HOLD, code: "PMF-0016", detail: "batch is on-hold; not pickable" }],
          shortfall: "30",
          overrideHeld: false,
          batchNumbers: { [BATCH_OLD]: "L-OLD", [BATCH_HOLD]: "L-HOLD" },
        });
      }
      return undefined;
    };
    const { container } = renderAt("/processmfg/picking-stored");
    expect(screen.queryByRole("table", { name: "Batches of the item" })).not.toBeInTheDocument();
    fireEvent.change(screen.getByLabelText("Item id"), { target: { value: ITEM } });
    const listed = await findTable({ name: "Batches of the item" });
    expect(within(listed).getByText("L-HOLD")).toBeInTheDocument();
    expect(calls[0].url.searchParams.get("itemId")).toBe(ITEM);
    fireEvent.change(screen.getByLabelText("Line quantity"), { target: { value: "80" } });
    fireEvent.change(screen.getByLabelText("On hand of batch L-OLD"), { target: { value: "50" } });
    fireEvent.change(screen.getByLabelText("Potency minimum %"), { target: { value: "40" } });
    fireEvent.change(screen.getByLabelText("Potency maximum %"), { target: { value: "60" } });
    fireEvent.click(screen.getByRole("button", { name: "Propose batches" }));
    const excluded = await findTable({ name: "Excluded batches" });
    expect(within(excluded).getByText("L-HOLD")).toBeInTheDocument();
    expect(within(excluded).getByText("Blocked by disposition")).toBeInTheDocument();
    expect(within(screen.getByRole("table", { name: "Picked batches" })).getByText("L-OLD")).toBeInTheDocument();
    expect(screen.getByRole("list", { name: "Picking proposal" })).toHaveTextContent("Shortfall 30");
    const sent = calls.find((c) => c.method === "POST")?.body as Record<string, unknown>;
    expect(sent.itemId).toBe(ITEM);
    expect(sent.qty).toBe("80");
    expect(sent.potency).toEqual({ min: "40", max: "60" });
    expect(sent.onHand).toEqual([{ batchId: BATCH_OLD, qty: "50" }]);
    expect(sent).not.toHaveProperty("batches");
    await waitFor(() => expect(pageViolations(container)).toEqual([]));
  });

  it("validates before calling, and shows the empty and error states", async () => {
    handler = () => json(200, { items: [] });
    renderAt("/processmfg/picking-stored");
    fireEvent.click(screen.getByRole("button", { name: "Propose batches" }));
    expect(await screen.findByText("Enter the item id as a UUID.")).toBeInTheDocument();
    expect(screen.getByText("Enter a quantity with at most 6 decimals.")).toBeInTheDocument();
    fireEvent.change(screen.getByLabelText("Item id"), { target: { value: ITEM } });
    expect(await screen.findByText("No batches registered for this item")).toBeInTheDocument();
    fireEvent.change(screen.getByLabelText("Line quantity"), { target: { value: "5" } });
    fireEvent.click(screen.getByRole("button", { name: "Propose batches" }));
    expect(await screen.findByText("Enter the on-hand quantity of at least one batch.")).toBeInTheDocument();
    expect(calls.filter((c) => c.method === "POST")).toHaveLength(0);
  });

  it("shows a load failure with a retry and a refusal from the service", async () => {
    handler = () => json(500, { code: "COM-0500", detail: "database unavailable" });
    renderAt("/processmfg/picking-stored");
    fireEvent.change(screen.getByLabelText("Item id"), { target: { value: ITEM } });
    expect(await findAlert()).toHaveTextContent("database unavailable (COM-0500)");
    handler = (url) =>
      url.pathname === "/api/v1/production/batches"
        ? json(200, { items: [batch(BATCH_OLD, "L-OLD", "available")] })
        : json(404, { code: "PMF-0108", detail: "batch not found" });
    fireEvent.click(screen.getByRole("button", { name: "Try again" }));
    await findTable({ name: "Batches of the item" });
    fireEvent.change(screen.getByLabelText("Line quantity"), { target: { value: "5" } });
    fireEvent.change(screen.getByLabelText("On hand of batch L-OLD"), { target: { value: "5" } });
    fireEvent.click(screen.getByRole("button", { name: "Propose batches" }));
    expect(await findAlert()).toHaveTextContent("batch not found (PMF-0108)");
  });
});
