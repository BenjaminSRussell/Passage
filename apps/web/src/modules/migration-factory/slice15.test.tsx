import { fireEvent, render, screen, waitFor } from "@testing-library/react";
import { MemoryRouter, Route, Routes } from "react-router-dom";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { checkAxeAA } from "../../shell/a11y";
import { findTable } from "../../test-utils";
import type { Tolerance } from "./api/client";
import migrationFactory from "./module";
import { validateTolerance } from "./SetTolerancePage";

type Reply = { status: number; body: unknown };
type Handler = (url: URL, init: RequestInit) => Reply | undefined;

const shellRules = new Set(["page-has-heading-one", "landmark-unique", "region", "bypass"]);
const pageViolations = (container: HTMLElement) => checkAxeAA(container).filter((v) => !shellRules.has(v.id));

let handler: Handler;
let calls: { method: string; url: URL; init: RequestInit; body: unknown }[];
const json = (status: number, body: unknown): Reply => ({ status, body });

const TOL: Tolerance = {
  pack: "open_items",
  total: "open_items.amount",
  amount: "0.050000",
  reason: "AX rounding of converted amounts",
  version: 2,
  updatedBy: "reviewer",
  updatedAt: "2026-12-01T10:00:00Z",
};

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

describe("tolerance validation", () => {
  const base = { pack: "open_items" as const, total: "open_items.amount", amount: "0.05", reason: "rounding" };
  it("builds the request and carries the stored version", () => {
    expect(validateTolerance(base).input).toEqual({ pack: "open_items", total: "open_items.amount", amount: "0.05", reason: "rounding" });
    expect(validateTolerance(base, TOL).input?.version).toBe(2);
  });
  it("needs a reason above zero only", () => {
    expect(validateTolerance({ ...base, reason: " " }).errors.reason).toMatch(/needs a reason/);
    expect(validateTolerance({ ...base, amount: "0", reason: "" }).input).toBeDefined();
  });
  it("refuses a total of another pack, a negative and a long amount", () => {
    expect(validateTolerance({ ...base, total: "onhand.value" }).errors.total).toBeDefined();
    expect(validateTolerance({ ...base, total: "*" }).input).toBeDefined();
    expect(validateTolerance({ ...base, amount: "-1" }).errors.amount).toBeDefined();
    expect(validateTolerance({ ...base, amount: "0.0000001" }).errors.amount).toBeDefined();
  });
  it("refuses a count, a quantity and a pack without money totals", () => {
    expect(validateTolerance({ ...base, total: "open_items.count" }).errors.total).toMatch(/money total/);
    expect(validateTolerance({ ...base, pack: "onhand", total: "onhand.quantity" }).errors.total).toBeDefined();
    expect(validateTolerance({ ...base, pack: "project_wip", total: "*" }).errors.pack).toMatch(/compare at 0.00/);
  });
});

describe("tolerances list", () => {
  it("shows the configured tolerances", async () => {
    handler = (url) => (url.pathname === "/api/v1/migration/tolerances" ? json(200, { items: [TOL, { ...TOL, total: "*", amount: "0", reason: "" }] }) : undefined);
    const { container } = renderAt("/migration-factory/tolerances");
    const table = await findTable({ name: "Control-total tolerances" });
    expect(table).toHaveTextContent("open_items.amount");
    expect(table).toHaveTextContent("0.05");
    expect(table).toHaveTextContent("Exact (0.00)");
    expect(table).toHaveTextContent("All money totals of the pack");
    expect(screen.getByRole("link", { name: /Change tolerance of open_items.amount/ })).toHaveAttribute("href", expect.stringContaining("total=open_items.amount"));
    expect(pageViolations(container)).toEqual([]);
  });

  it("explains an empty list", async () => {
    handler = () => json(200, { items: [] });
    renderAt("/migration-factory/tolerances");
    expect(await screen.findByText("No tolerances")).toBeInTheDocument();
  });

  it("shows a failure with a way to retry", async () => {
    handler = () => json(403, { code: "AUTH-0003", detail: "missing duty migration.factory.view" });
    renderAt("/migration-factory/tolerances");
    expect(await screen.findByText(/missing duty migration.factory.view \(AUTH-0003\)/)).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "Try again" })).toBeInTheDocument();
  });
});

describe("set tolerance form", () => {
  it("changes an existing tolerance with its version and an idempotency key", async () => {
    handler = (url, init) => {
      if (url.pathname !== "/api/v1/migration/tolerances") return undefined;
      return init.method === "PUT" ? json(200, { ...TOL, amount: "0.1", version: 3 }) : json(200, { items: [TOL] });
    };
    const { container } = renderAt("/migration-factory/tolerances/set?pack=open_items&total=open_items.amount");
    await screen.findByText(/Currently 0.050000 \(version 2\)/);
    expect(pageViolations(container)).toEqual([]);
    fireEvent.change(screen.getByLabelText("Tolerance"), { target: { value: "0.10" } });
    fireEvent.change(screen.getByLabelText(/Reason/), { target: { value: "wider rounding" } });
    fireEvent.click(screen.getByRole("button", { name: "Save tolerance" }));
    await waitFor(() => expect(calls.some((c) => c.method === "PUT")).toBe(true));
    const put = calls.find((c) => c.method === "PUT")!;
    expect(put.body).toEqual({ pack: "open_items", total: "open_items.amount", amount: "0.10", reason: "wider rounding", version: 2 });
    expect(new Headers(put.init.headers).get("Idempotency-Key")).toBeTruthy();
  });

  it("offers only the money totals of the chosen pack", async () => {
    handler = () => json(200, { items: [] });
    renderAt("/migration-factory/tolerances/set?pack=onhand&total=onhand.quantity");
    await screen.findByText(/No tolerance is set/);
    const total = screen.getByLabelText("Control total") as HTMLSelectElement;
    expect(total.value).toBe("*");
    const options = Array.from(total.options).map((o) => o.value);
    expect(options).toEqual(["*", "onhand.value", "onhand.value_entity", "onhand.inventory_account"]);
    const packs = Array.from((screen.getByLabelText("Pack") as HTMLSelectElement).options).map((o) => o.value);
    expect(packs).not.toContain("project_wip");
    fireEvent.change(screen.getByLabelText("Pack"), { target: { value: "bank_balances" } });
    expect(Array.from(total.options).map((o) => o.value)).toEqual(["*", "bank_balances.balance", "bank_balances.balance_entity"]);
  });

  it("shows validation errors without calling the service", async () => {
    handler = () => json(200, { items: [] });
    renderAt("/migration-factory/tolerances/set");
    await screen.findByText(/No tolerance is set/);
    fireEvent.click(screen.getByRole("button", { name: "Save tolerance" }));
    expect(await screen.findByText("Enter an amount of 0 or more with at most 6 decimals.")).toBeInTheDocument();
    expect(calls.some((c) => c.method === "PUT")).toBe(false);
  });

  it("shows the service's refusal", async () => {
    handler = (_url, init) =>
      init.method === "PUT" ? json(409, { code: "MIG-0006", detail: "tolerance for open_items.amount is at version 4, request expected 2" }) : json(200, { items: [TOL] });
    renderAt("/migration-factory/tolerances/set?pack=open_items&total=open_items.amount");
    await screen.findByText(/Currently/);
    fireEvent.change(screen.getByLabelText("Tolerance"), { target: { value: "0" } });
    fireEvent.click(screen.getByRole("button", { name: "Save tolerance" }));
    expect(await screen.findByText(/is at version 4, request expected 2 \(MIG-0006\)/)).toBeInTheDocument();
  });
});
