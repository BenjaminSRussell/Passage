import { fireEvent, render, screen, waitFor, within } from "@testing-library/react";
import { MemoryRouter, Route, Routes } from "react-router-dom";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { validateCountPlan } from "./NewCountPlanPage";
import inventory from "./module";

const ENTITY_ID = "9e8d7c6b-0000-4000-8000-000000000003";
const PLAN_ID = "3d3d3d3d-0000-4000-8000-000000000011";
const SHEET_ID = "4e4e4e4e-0000-4000-8000-000000000012";
const LINE_ID = "5f5f5f5f-0000-4000-8000-000000000021";
const PRODUCT_ID = "5f2c1e7a-0000-4000-8000-000000000002";

type Handler = (url: URL, init: RequestInit) => { status: number; body: unknown };
let handler: Handler;
let calls: { method: string; url: URL; init: RequestInit }[];
const json = (status: number, body: unknown) => ({ status, body });
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
afterEach(() => vi.unstubAllGlobals());

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

const sheet = (overrides: Record<string, unknown> = {}, line: Record<string, unknown> = {}) => ({
  id: SHEET_ID,
  legalEntityId: ENTITY_ID,
  planId: PLAN_ID,
  planName: "Aisle A",
  number: "CNT-000123",
  status: "OPEN",
  blind: false,
  fourEyes: false,
  freezeStock: false,
  recountLines: 0,
  version: 3,
  lineCount: 1,
  countedCount: 1,
  createdAt: "2026-09-01T08:00:00Z",
  lines: [
    {
      id: LINE_ID,
      lineNo: 1,
      productId: PRODUCT_ID,
      dimensions: { site: "S1" },
      expectedQty: "50",
      countedQty: "46",
      postedVariance: null,
      recountRequested: false,
      recountRounds: 0,
      ...line,
    },
  ],
  ...overrides,
});

const base = { legalEntityId: ENTITY_ID, name: "Aisle A", warehouse: "", currency: "USD", unit: "EA", tolerancePct: "5", toleranceValue: "100", blind: false, freezeStock: false, fourEyes: false, locationFrom: "", locationTo: "", abcClass: "" as const, samplePct: "" };

describe("Count control", () => {
  it("validates plan selection like the service", () => {
    expect(validateCountPlan(base)).toEqual({});
    expect(validateCountPlan({ ...base, locationFrom: "B-9", locationTo: "A-1", samplePct: "101" })).toEqual({
      locationTo: "The range end must not be before its start.",
      samplePct: "Enter a percentage from 0 to 100.",
    });
  });

  it("sends the selection and control options with the plan", async () => {
    handler = (_url, init) => (init.method === "POST" ? json(201, { id: PLAN_ID }) : json(200, { items: [] }));
    renderAt("/inventory/count-plans/new");
    fireEvent.change(screen.getByLabelText("Legal entity"), { target: { value: ENTITY_ID } });
    fireEvent.change(screen.getByLabelText("Name"), { target: { value: "Aisle A" } });
    fireEvent.change(screen.getByLabelText("Currency"), { target: { value: "usd" } });
    fireEvent.change(screen.getByLabelText(/Locations from/), { target: { value: "A-01" } });
    fireEvent.change(screen.getByLabelText(/Locations to/), { target: { value: "A-99" } });
    fireEvent.change(screen.getByLabelText(/ABC class/), { target: { value: "A" } });
    fireEvent.change(screen.getByLabelText(/Sample/), { target: { value: "25" } });
    fireEvent.click(screen.getByLabelText("Freeze stock while counting"));
    fireEvent.click(screen.getByLabelText("Separate approval"));
    fireEvent.click(screen.getByRole("button", { name: "Create count plan" }));
    await waitFor(() => expect(calls.some((c) => c.method === "POST")).toBe(true));
    const body = JSON.parse(calls.find((c) => c.method === "POST")!.init.body as string);
    expect(body).toMatchObject({ locationFrom: "A-01", locationTo: "A-99", abcClass: "A", samplePct: "25", freezeStock: true, fourEyes: true, currency: "USD" });
  });

  it("lists the plan's selection and control", async () => {
    handler = () =>
      json(200, {
        items: [{ id: PLAN_ID, legalEntityId: ENTITY_ID, name: "Aisle A", currency: "USD", unit: "EA", tolerancePct: "5", toleranceValue: "100", blind: false, freezeStock: true, fourEyes: true, locationFrom: "A-01", locationTo: "A-99", abcClass: "A", samplePct: "25", createdAt: "2026-09-01T08:00:00Z" }],
      });
    renderAt("/inventory/count-plans");
    const table = await screen.findByRole("table", { name: "Count plans" });
    await waitFor(() => expect(table).toHaveTextContent("Locations A-01 to A-99 · Class A · 25 % sample"));
    expect(table).toHaveTextContent("Freezes stock · Four eyes");
  });

  it("submits an open sheet for review with the shown version", async () => {
    handler = (url, init) => {
      if (init.method === "POST" && url.pathname.endsWith(":submit")) return json(200, sheet({ status: "COUNTED", version: 4, submittedBy: "alice", fourEyes: true, freezeStock: true }));
      return json(200, sheet({ fourEyes: true, freezeStock: true }));
    };
    renderAt(`/inventory/count-sheets/${SHEET_ID}`);
    expect(await screen.findByText("Stock is frozen")).toBeInTheDocument();
    expect(screen.getByText("Separate approval required")).toBeInTheDocument();
    expect(screen.queryByRole("button", { name: "Post count" })).toBeNull();
    fireEvent.click(screen.getByRole("button", { name: "Submit for review" }));
    expect(await screen.findByRole("button", { name: "Approve count" })).toBeInTheDocument();
    const post = calls.find((c) => c.method === "POST")!;
    expect(header(post, "If-Match")).toBe('"3"');
    expect(screen.getByText("Count submitted for review.")).toBeInTheDocument();
    expect(screen.queryByRole("button", { name: "Post count" })).toBeNull();
  });

  it("shows the four-eyes refusal when the approver also counted", async () => {
    handler = (_url, init) =>
      init.method === "POST" ? json(403, { code: "INV-CNT-007", detail: "line 1 was counted by the approver" }) : json(200, sheet({ status: "COUNTED", fourEyes: true }));
    renderAt(`/inventory/count-sheets/${SHEET_ID}`);
    fireEvent.click(await screen.findByRole("button", { name: "Approve count" }));
    expect(await screen.findByText(/line 1 was counted by the approver \(INV-CNT-007\)/)).toBeInTheDocument();
  });

  it("posts an approved four-eyes sheet", async () => {
    handler = (_url, init) =>
      init.method === "POST" ? json(200, sheet({ status: "POSTED", fourEyes: true, version: 6 })) : json(200, sheet({ status: "APPROVED", fourEyes: true, approvedBy: "bob" }));
    renderAt(`/inventory/count-sheets/${SHEET_ID}`);
    fireEvent.click(await screen.findByRole("button", { name: "Post count" }));
    await waitFor(() => expect(screen.getByText(/Count posted/)).toBeInTheDocument());
    expect(calls.find((c) => c.method === "POST")!.url.pathname).toMatch(/:post$/);
  });

  it("sends a counted line back for recount with a reason", async () => {
    handler = (_url, init) => {
      if (init.method === "POST") return json(200, sheet({ status: "OPEN", version: 5, recountLines: 1 }, { recountRequested: true, recountReason: "shelf looked light", recountRounds: 1 }));
      return json(200, sheet({ status: "COUNTED" }));
    };
    renderAt(`/inventory/count-sheets/${SHEET_ID}`);
    fireEvent.click(await screen.findByRole("button", { name: "Request recount, line 1" }));
    fireEvent.click(screen.getByRole("button", { name: "Send recount request, line 1" }));
    expect(calls.filter((c) => c.method === "POST")).toHaveLength(0); // a reason is required
    fireEvent.change(screen.getByLabelText("Recount reason, line 1"), { target: { value: "shelf looked light" } });
    fireEvent.click(screen.getByRole("button", { name: "Send recount request, line 1" }));
    expect(await screen.findByText("Recount: shelf looked light")).toBeInTheDocument();
    const post = calls.find((c) => c.method === "POST")!;
    expect(post.url.pathname).toMatch(/lines\/.+:recount$/);
    expect(JSON.parse(post.init.body as string)).toEqual({ reason: "shelf looked light" });
    expect(screen.getByText("Recount requested")).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "Submit for review" })).toBeDisabled();
  });

  it("cancels a sheet and lifts the freeze", async () => {
    handler = (_url, init) => (init.method === "POST" ? json(200, sheet({ status: "CANCELLED", freezeStock: true, version: 4 })) : json(200, sheet({ freezeStock: true })));
    renderAt(`/inventory/count-sheets/${SHEET_ID}`);
    fireEvent.click(await screen.findByRole("button", { name: "Cancel sheet" }));
    const dialog = screen.getByRole("alertdialog", { name: "Cancel this count sheet?" });
    fireEvent.click(within(dialog).getByRole("button", { name: "Keep sheet" }));
    expect(screen.queryByRole("alertdialog")).toBeNull();
    expect(calls.filter((c) => c.method === "POST")).toHaveLength(0); // nothing is cancelled without confirmation
    fireEvent.click(screen.getByRole("button", { name: "Cancel sheet" }));
    fireEvent.click(within(screen.getByRole("alertdialog")).getByRole("button", { name: "Cancel sheet" }));
    expect(await screen.findByText("Count sheet cancelled. Its stock is no longer frozen.")).toBeInTheDocument();
    expect(header(calls.find((c) => c.method === "POST"), "If-Match")).toBe('"3"');
    expect(screen.queryByText("Stock is frozen")).toBeNull();
    expect(screen.queryByRole("button", { name: "Cancel sheet" })).toBeNull();
  });

  it("keeps a submitted blind sheet blind for the counter", async () => {
    handler = () => json(200, sheet({ status: "COUNTED", blind: true }, { expectedQty: null }));
    renderAt(`/inventory/count-sheets/${SHEET_ID}`);
    expect(await screen.findByText("Blind count")).toBeInTheDocument();
  });

  it("shows the separate-approval refusal when posting", async () => {
    handler = (_url, init) =>
      init.method === "POST" ? json(409, { code: "INV-CNT-008", detail: "this plan needs a separate approval" }) : json(200, sheet({ status: "APPROVED" }));
    renderAt(`/inventory/count-sheets/${SHEET_ID}`);
    fireEvent.click(await screen.findByRole("button", { name: "Post count" }));
    expect(await screen.findByText(/this plan needs a separate approval \(INV-CNT-008\)/)).toBeInTheDocument();
  });

  it("filters sheets by the new statuses", async () => {
    handler = () => json(200, { items: [] });
    renderAt("/inventory/count-sheets");
    await screen.findByText(/No count sheets|Nothing/i).catch(() => undefined);
    fireEvent.change(screen.getByLabelText("Status"), { target: { value: "COUNTED" } });
    await waitFor(() => expect(calls.some((c) => c.url.searchParams.get("status") === "COUNTED")).toBe(true));
  });
});
