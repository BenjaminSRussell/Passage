import { findTable } from "../../test-utils";
import { fireEvent, render, screen, waitFor, within } from "@testing-library/react";
import { MemoryRouter, Route, Routes } from "react-router-dom";
import { afterEach, describe, expect, it, vi } from "vitest";
import planning from "./module";
import { signed } from "./PlanDiffPage";

const item = "00000000-0000-0000-0000-000000000300";
const json = (body: unknown, status = 200) => new Response(JSON.stringify(body), { status });
const version = (number: number, hash: string) => ({
  id: `00000000-0000-0000-0000-00000000010${number}`,
  planCode: "MAIN",
  number,
  status: number === 2 ? "current" : "complete",
  asOf: "2026-11-02",
  itemsPlanned: 3,
  plannedOrders: 2,
  messages: number,
  durationMs: 5,
  planHash: hash,
});
const o = (number: string, qty: string, od = "2026-11-02") => ({ number, itemId: item, orderType: "purchase", qty, orderDate: od, deliveryDate: "2026-11-16" });
const diff = {
  from: version(1, "aaa"),
  to: version(2, "bbb"),
  identical: false,
  messagesDelta: 1,
  addedCount: 1,
  removedCount: 0,
  changedCount: 1,
  truncated: false,
  added: [o("PLN-000009", "5.000000")],
  removed: [],
  changed: [{ from: o("PLN-000001", "100.000000"), to: o("PLN-000002", "120.000000"), qtyDelta: "20.000000" }],
  unchanged: 4,
  qtyDeltaByOrderType: { purchase: "25.000000" },
};

function stub(handlers: [string, () => Response][]) {
  const fetchMock = vi.fn(async (url: string) => {
    for (const [m, r] of handlers) if (url.includes(m)) return r();
    throw new Error(`unexpected fetch ${url}`);
  });
  vi.stubGlobal("fetch", fetchMock);
  return fetchMock;
}

function renderAt(path: string) {
  render(
    <MemoryRouter initialEntries={[path]}>
      <Routes>
        {planning.routes.map((r) => (
          <Route key={r.path} path={r.path} element={r.element} />
        ))}
      </Routes>
    </MemoryRouter>,
  );
}

afterEach(() => vi.unstubAllGlobals());

describe("Compare plan versions", () => {
  it("compares the previous and newest run by default", async () => {
    const f = stub([
      ["/diff", () => json(diff)],
      ["/versions", () => json({ items: [version(2, "bbb"), version(1, "aaa")] })],
    ]);
    renderAt("/planning/runs/compare?plan=MAIN");
    const changed = await findTable({ name: "Changed orders" });
    expect(within(changed).getByText("100 → 120")).toBeInTheDocument();
    expect(within(changed).getByText("+20")).toBeInTheDocument();
    expect(screen.getByText("Plan hashes differ")).toBeInTheDocument();
    expect(f.mock.calls.some(([u]) => String(u).includes("/plans/MAIN/diff?from=1&to=2&limit=500"))).toBe(true);
    expect(within(await findTable({ name: "Added orders" })).getByText("PLN-000009")).toBeInTheDocument();
    expect(screen.queryByText("Showing the first orders of each list")).not.toBeInTheDocument();
  });

  it("shows the planning scope of an added order (FR-22.18)", async () => {
    const wh = "00000000-0000-0000-0000-0000000000c2";
    stub([
      ["/diff", () => json({ ...diff, added: [o("PLN-000009", "5.000000"), { ...o("PLN-000010", "3.000000"), orderType: "transfer", warehouseId: wh }] })],
      ["/versions", () => json({ items: [version(2, "bbb"), version(1, "aaa")] })],
    ]);
    renderAt("/planning/runs/compare?plan=MAIN");
    const added = await findTable({ name: "Added orders" });
    expect(within(added).getByText("Site")).toBeInTheDocument();
    expect(within(added).getByText("Warehouse 00000000")).toBeInTheDocument();
  });

  it("compares a newest simulation with the current operational version, not the run before it", async () => {
    const sim = (n: number) => ({ ...version(n, "ccc"), status: "complete", kind: "simulation", scenarioCode: "UP20" });
    const f = stub([
      ["/diff", () => json({ ...diff, from: version(2, "bbb"), to: sim(4) })],
      ["/versions", () => json({ items: [sim(4), sim(3), version(2, "bbb"), version(1, "aaa")] })],
    ]);
    renderAt("/planning/runs/compare?plan=MAIN&to=4");
    await waitFor(() => expect(f.mock.calls.some(([u]) => String(u).includes("/plans/MAIN/diff?from=2&to=4"))).toBe(true));
    expect(f.mock.calls.some(([u]) => String(u).includes("diff?from=3"))).toBe(false);
    expect(screen.getAllByRole("option", { name: "Version 4 (simulation UP20)" }).length).toBeGreaterThan(0);
    expect(screen.getAllByRole("option", { name: "Version 2 (current)" }).length).toBeGreaterThan(0);
  });

  it("counts every order and says when the lists are cut", async () => {
    stub([
      ["/diff", () => json({ ...diff, addedCount: 812, truncated: true })],
      ["/versions", () => json({ items: [version(2, "bbb"), version(1, "aaa")] })],
    ]);
    renderAt("/planning/runs/compare?plan=MAIN");
    expect(await screen.findByText("Showing the first orders of each list")).toBeInTheDocument();
    expect(screen.getByText("812")).toBeInTheDocument();
  });

  it("puts the earlier version first whichever way round they are chosen", async () => {
    const f = stub([
      ["/diff", () => json(diff)],
      ["/versions", () => json({ items: [version(3, "c"), version(2, "bbb"), version(1, "aaa")] })],
    ]);
    renderAt("/planning/runs/compare?plan=MAIN&from=3&to=1");
    await findTable({ name: "Changed orders" });
    expect(f.mock.calls.some(([u]) => String(u).includes("diff?from=1&to=3"))).toBe(true);
  });

  it("warns instead of comparing a version with itself", async () => {
    const f = stub([["/versions", () => json({ items: [version(2, "bbb"), version(1, "aaa")] })]]);
    renderAt("/planning/runs/compare?plan=MAIN&from=2&to=2");
    expect(await screen.findByText("Choose two different versions")).toBeInTheDocument();
    expect(f.mock.calls.some(([u]) => String(u).includes("/diff"))).toBe(false);
  });

  it("is reachable from the plan runs page", async () => {
    stub([["/versions", () => json({ items: [version(1, "aaa")] })]]);
    renderAt("/planning/runs?plan=MAIN");
    expect(await screen.findByRole("link", { name: "Compare versions" })).toHaveAttribute("href", expect.stringContaining("/planning/runs/compare"));
  });

  it("re-compares when a version is chosen", async () => {
    const f = stub([
      ["/diff", () => json(diff)],
      ["/versions", () => json({ items: [version(3, "c"), version(2, "bbb"), version(1, "aaa")] })],
    ]);
    renderAt("/planning/runs/compare?plan=MAIN");
    await findTable({ name: "Changed orders" });
    fireEvent.change(screen.getByLabelText("Earlier version"), { target: { value: "1" } });
    fireEvent.change(screen.getByLabelText("Later version"), { target: { value: "2" } });
    await waitFor(() => expect(f.mock.calls.some(([u]) => String(u).includes("diff?from=1&to=2"))).toBe(true));
  });

  it("says when there is nothing to compare", async () => {
    stub([["/versions", () => json({ items: [version(1, "aaa")] })]]);
    renderAt("/planning/runs/compare");
    expect(await screen.findByText("Nothing to compare yet")).toBeInTheDocument();
  });

  it("shows an empty state for identical versions", async () => {
    stub([
      ["/diff", () => json({ ...diff, identical: true, added: [], changed: [], addedCount: 0, changedCount: 0, messagesDelta: 0, qtyDeltaByOrderType: {} })],
      ["/versions", () => json({ items: [version(2, "x"), version(1, "x")] })],
    ]);
    renderAt("/planning/runs/compare");
    expect(await screen.findByText("No planned-order differences")).toBeInTheDocument();
    expect(screen.getByText("Identical plan hash")).toBeInTheDocument();
  });

  it("shows the problem when the comparison fails", async () => {
    stub([
      ["/diff", () => json({ type: "t", title: "x", status: 404, code: "PLAN-VERSION-NOT-FOUND", detail: "plan version not found" }, 404)],
      ["/versions", () => json({ items: [version(2, "x"), version(1, "y")] })],
    ]);
    renderAt("/planning/runs/compare");
    expect(await screen.findByText(/PLAN-VERSION-NOT-FOUND/)).toBeInTheDocument();
  });

  it("signs quantities", () => {
    expect(signed("20.000000")).toBe("+20");
    expect(signed("-10.500000")).toBe("-10.5");
    expect(signed("0.000000")).toBe("0");
  });
});
