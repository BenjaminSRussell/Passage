import { findTable } from "../../test-utils";
import { fireEvent, render, screen, waitFor, within } from "@testing-library/react";
import { MemoryRouter, Route, Routes } from "react-router-dom";
import { afterEach, describe, expect, it, vi } from "vitest";
import type { FirmBatch, PlannedOrder } from "./api/client";
import { isTypingTarget, rangeIds } from "./Firming";
import planning from "./module";

// Keyboard and range selection on the planned-orders grid and `F` to firm
// (PRD 4.22.6 interaction rules, FR-22.10).

const order = (n: number, status: PlannedOrder["status"]): PlannedOrder => ({
  id: `00000000-0000-0000-0000-00000000000${n}`,
  versionId: "00000000-0000-0000-0000-0000000000b1",
  number: `PLN-00000${n}`,
  itemId: "00000000-0000-0000-0000-0000000000c1",
  orderType: "purchase",
  qty: "10",
  orderDate: "2026-11-08",
  deliveryDate: "2026-11-10",
  futuresDate: null,
  delayDays: 0,
  lotRule: "lot-for-lot",
  status,
  version: 1,
  ...(status === "firmed" ? { firmedTargetType: "purchase-order" as const, firmedTargetId: "00000000-0000-0000-0000-0000000000d1" } : {}),
});

const grid = [order(1, "approved"), order(2, "firmed"), order(3, "unprocessed"), order(4, "approved")];
const [a, b, c, d] = grid;

const batch: FirmBatch = {
  id: "00000000-0000-0000-0000-0000000000f1",
  grouping: "vendor-date",
  firmed: 2,
  groups: [
    {
      targetType: "purchase-order",
      targetId: "00000000-0000-0000-0000-0000000000d4",
      orderType: "purchase",
      siteId: "00000000-0000-0000-0000-0000000000f9",
      vendorId: null,
      deliveryDate: "2026-11-10",
      qty: "20",
      plannedOrderIds: [a.id, c.id],
    },
  ],
};

const json = (body: unknown, status = 200) => new Response(JSON.stringify(body), { status });

function stubApi() {
  const fetchMock = vi.fn(async (url: string, _init?: RequestInit) => {
    if (url.includes("/planned-orders:firm")) return json(batch, 201);
    if (url.includes("/planned-orders")) return json({ items: grid });
    throw new Error(`unexpected fetch ${url}`);
  });
  vi.stubGlobal("fetch", fetchMock);
  return fetchMock;
}

const firmPosts = (fetchMock: ReturnType<typeof stubApi>) =>
  fetchMock.mock.calls.filter(([url, init]) => url.includes(":firm") && init?.method === "POST");

async function renderOrders() {
  render(
    <MemoryRouter initialEntries={["/planning/planned-orders"]}>
      <Routes>
        {planning.routes.map((r) => (
          <Route key={r.path} path={r.path} element={r.element} />
        ))}
      </Routes>
    </MemoryRouter>,
  );
  const table = await findTable({ name: "Planned orders" });
  await waitFor(() => expect(within(table).getByText("PLN-000004")).toBeInTheDocument());
  return (o: PlannedOrder) => within(table).getByRole("checkbox", { name: `Select ${o.number} for firming` }) as HTMLInputElement;
}

afterEach(() => {
  fireEvent.keyUp(window, { key: "Shift" });
  vi.unstubAllGlobals();
});

describe("rangeIds", () => {
  it("selects the firmable rows between two orders in either direction", () => {
    expect(rangeIds(grid, a.id, d.id)).toEqual([a.id, c.id, d.id]);
    expect(rangeIds(grid, d.id, c.id)).toEqual([c.id, d.id]);
  });

  it("skips firmed orders and is empty when a row is not listed", () => {
    expect(rangeIds(grid, b.id, b.id)).toEqual([]);
    expect(rangeIds(grid, "gone", d.id)).toEqual([]);
  });
});

describe("isTypingTarget", () => {
  it("is true for text fields, selects, textareas and content-editable elements", () => {
    const text = document.createElement("input");
    expect(isTypingTarget(text)).toBe(true);
    expect(isTypingTarget(document.createElement("textarea"))).toBe(true);
    expect(isTypingTarget(document.createElement("select"))).toBe(true);
    const div = document.createElement("div");
    div.setAttribute("contenteditable", "");
    const inner = document.createElement("span");
    div.appendChild(inner);
    expect(isTypingTarget(div)).toBe(true);
    expect(isTypingTarget(inner)).toBe(true);
  });

  it("is false for checkboxes, buttons, other elements and non-elements", () => {
    const box = document.createElement("input");
    box.type = "checkbox";
    expect(isTypingTarget(box)).toBe(false);
    expect(isTypingTarget(document.createElement("button"))).toBe(false);
    expect(isTypingTarget(document.body)).toBe(false);
    expect(isTypingTarget(window)).toBe(false);
    expect(isTypingTarget(null)).toBe(false);
  });
});

describe("Planned-orders grid selection", () => {
  it("Shift+click ticks every open order from the last box ticked, skipping firmed ones, and unticks a range", async () => {
    stubApi();
    const box = await renderOrders();
    fireEvent.click(box(a));
    fireEvent.click(box(d), { shiftKey: true });
    expect(screen.getByText("3 planned orders selected.")).toBeInTheDocument();
    expect([box(a), box(c), box(d)].every((x) => x.checked)).toBe(true);
    expect(screen.queryByRole("checkbox", { name: "Select PLN-000002 for firming" })).not.toBeInTheDocument();

    // Unticking with Shift clears the range from the last box (d) to c.
    fireEvent.click(box(c), { shiftKey: true });
    expect(screen.getByText("1 planned order selected.")).toBeInTheDocument();
    expect(box(a).checked).toBe(true);
  });

  it("Shift held on the keyboard (Shift+Space) extends the selection too", async () => {
    stubApi();
    const box = await renderOrders();
    fireEvent.click(box(c));
    fireEvent.keyDown(window, { key: "Shift" });
    fireEvent.click(box(a)); // the click a Space press makes, without the modifier on it
    fireEvent.keyUp(window, { key: "Shift" });
    expect(screen.getByText("2 planned orders selected.")).toBeInTheDocument();
    // After Shift is released a click toggles one box only.
    fireEvent.click(box(d));
    expect(screen.getByText("3 planned orders selected.")).toBeInTheDocument();
  });

  it("a plain click without an anchor ticks one box", async () => {
    stubApi();
    const box = await renderOrders();
    fireEvent.click(box(d), { shiftKey: true });
    expect(screen.getByText("1 planned order selected.")).toBeInTheDocument();
  });
});

describe("F to firm", () => {
  it("firms the selection when F is pressed outside a text field", async () => {
    const fetchMock = stubApi();
    const box = await renderOrders();
    expect(screen.getByRole("button", { name: "Firm selected" })).toHaveAttribute("aria-keyshortcuts", "F");
    fireEvent.click(box(a));
    fireEvent.click(box(c));
    fireEvent.keyDown(box(c), { key: "f" });
    expect(await screen.findByText("Firmed 2 planned orders into 1 order")).toBeInTheDocument();
    const posts = firmPosts(fetchMock);
    expect(posts).toHaveLength(1);
    expect(JSON.parse(String(posts[0][1]?.body))).toEqual({ plannedOrderIds: [a.id, c.id], grouping: "vendor-date" });
  });

  it("does nothing with no selection, inside a text field, or with a modifier", async () => {
    const fetchMock = stubApi();
    const box = await renderOrders();
    fireEvent.keyDown(document.body, { key: "F" });
    fireEvent.click(box(a));
    fireEvent.keyDown(screen.getByLabelText("Plan"), { key: "f" });
    fireEvent.keyDown(document.body, { key: "f", ctrlKey: true });
    fireEvent.keyDown(document.body, { key: "f", metaKey: true });
    await new Promise((r) => setTimeout(r, 0));
    expect(firmPosts(fetchMock)).toHaveLength(0);
    expect(screen.getByText("1 planned order selected.")).toBeInTheDocument();
  });
});
