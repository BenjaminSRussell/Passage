import { fireEvent, render, screen, waitFor } from "@testing-library/react";
import { MemoryRouter, Route, Routes } from "react-router-dom";
import { afterEach, describe, expect, it, vi } from "vitest";
import { findStatus } from "../../test-utils";
import planning from "./module";
import { validateRun } from "./RunPlanPage";

const site = "00000000-0000-0000-0000-0000000000d1";
const item = "00000000-0000-0000-0000-0000000000c1";
const version = {
  id: "00000000-0000-0000-0000-0000000000b1",
  planCode: "MAIN",
  number: 1,
  status: "current",
  asOf: "2026-11-02",
  itemsPlanned: 1,
  plannedOrders: 2,
  messages: 0,
  durationMs: 5,
  planHash: "abc",
};

const base = { plan: "MAIN", asOf: "2026-11-02", siteId: site, items: "[{}]" };

function renderRun() {
  render(
    <MemoryRouter initialEntries={["/planning/runs/new"]}>
      <Routes>
        {planning.routes.map((r) => (
          <Route key={r.path} path={r.path} element={r.element} />
        ))}
      </Routes>
    </MemoryRouter>,
  );
}

afterEach(() => vi.unstubAllGlobals());

describe("plan scope and planning horizon on the run form", () => {
  it("sends neither horizon nor scope when the fields are empty", () => {
    const r = validateRun(base);
    expect(r.errors).toEqual({});
    expect(r.horizonDays).toBeUndefined();
    expect(r.scope).toBeUndefined();
  });

  it("builds the scope from the filled fields", () => {
    const r = validateRun({ ...base, horizonDays: "180", scopeGroups: "FIN, SEMI", scopePlanners: "ANN", scopeItemIds: item, llcMin: "0", llcMax: "2" });
    expect(r.errors).toEqual({});
    expect(r.horizonDays).toBe(180);
    expect(r.scope).toEqual({ itemGroups: ["FIN", "SEMI"], planners: ["ANN"], itemIds: [item], llcMin: 0, llcMax: 2 });
  });

  it("keeps spaces inside item groups and planners and splits them on commas", () => {
    const r = validateRun({ ...base, scopeGroups: " RAW MAT , FIN ,", scopePlanners: "Ann Lee" });
    expect(r.scope).toEqual({ itemGroups: ["RAW MAT", "FIN"], planners: ["Ann Lee"] });
    expect(validateRun({ ...base, scopePlanners: "x".repeat(65) }).errors.scopePlanners).toBeDefined();
  });

  it("rejects an out-of-range horizon, bad ids and a reversed code range", () => {
    expect(validateRun({ ...base, horizonDays: "10" }).errors.horizonDays).toMatch(/30 to 1095/);
    expect(validateRun({ ...base, horizonDays: "2000" }).errors.horizonDays).toMatch(/30 to 1095/);
    expect(validateRun({ ...base, horizonDays: "abc" }).errors.horizonDays).toBeDefined();
    expect(validateRun({ ...base, scopeItemIds: "nope" }).errors.scopeItemIds).toBeDefined();
    expect(validateRun({ ...base, llcMin: "3", llcMax: "1" }).errors.llcMax).toMatch(/at least/);
    expect(validateRun({ ...base, llcMax: "100" }).errors.llcMax).toBeDefined();
  });

  it("posts the horizon and scope with the run", async () => {
    const fetchMock = vi.fn(async () => new Response(JSON.stringify(version), { status: 201 }));
    vi.stubGlobal("fetch", fetchMock);
    renderRun();
    fireEvent.change(screen.getByLabelText("Site"), { target: { value: site } });
    fireEvent.change(screen.getByLabelText(/Planning horizon/), { target: { value: "90" } });
    fireEvent.change(screen.getByLabelText("Item groups"), { target: { value: "FIN" } });
    fireEvent.change(screen.getByLabelText("Highest low-level code"), { target: { value: "0" } });
    fireEvent.click(screen.getByRole("button", { name: "Run plan" }));
    await findStatus();
    const body = JSON.parse((fetchMock.mock.calls[0] as unknown as [string, RequestInit])[1].body as string);
    expect(body).toMatchObject({ horizonDays: 90, scope: { itemGroups: ["FIN"], llcMax: 0 } });
    expect(body.scope).not.toHaveProperty("llcMin");
  });

  it("shows field errors instead of calling the service", async () => {
    const fetchMock = vi.fn();
    vi.stubGlobal("fetch", fetchMock);
    renderRun();
    fireEvent.change(screen.getByLabelText("Site"), { target: { value: site } });
    fireEvent.change(screen.getByLabelText(/Planning horizon/), { target: { value: "5" } });
    fireEvent.click(screen.getByRole("button", { name: "Run plan" }));
    await waitFor(() => expect(screen.getByText(/Enter 30 to 1095 days/)).toBeInTheDocument());
    expect(fetchMock).not.toHaveBeenCalled();
  });
});
