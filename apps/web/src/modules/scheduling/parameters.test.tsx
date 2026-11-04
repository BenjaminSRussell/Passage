import { fireEvent, render, screen, waitFor } from "@testing-library/react";
import { MemoryRouter, Route, Routes } from "react-router-dom";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { checkAxeAA } from "../../shell/a11y";
import type { SchedulingParameters } from "./api/client";
import scheduling from "./module";
import { validateParameters, valuesOf } from "./ParametersPage";

type Reply = { status: number; body?: unknown };
let handler: (url: URL, init: RequestInit) => Reply | undefined;
let calls: { method: string; url: URL; body: unknown; headers: Headers }[];

const shellRules = new Set(["page-has-heading-one", "landmark-unique", "region", "bypass"]);

beforeEach(() => {
  calls = [];
  vi.stubGlobal(
    "fetch",
    vi.fn(async (input: string, init: RequestInit = {}) => {
      const url = new URL(input, "http://localhost");
      calls.push({ method: init.method ?? "GET", url, body: init.body ? JSON.parse(String(init.body)) : undefined, headers: new Headers(init.headers) });
      const reply = handler(url, init) ?? { status: 404, body: { code: "TEST-404", detail: "no fake" } };
      return new Response(reply.body === undefined ? null : JSON.stringify(reply.body), { status: reply.status, headers: { "Content-Type": "application/json" } });
    }),
  );
});
afterEach(() => vi.unstubAllGlobals());

function renderAt(path: string) {
  return render(
    <MemoryRouter initialEntries={[path]}>
      <Routes>
        {scheduling.routes.map((route) => (
          <Route key={route.path} path={route.path} element={route.element} />
        ))}
      </Routes>
    </MemoryRouter>,
  );
}

const defaults: SchedulingParameters = {
  defaultMode: "jobs",
  defaultDirection: "forward",
  defaultFinite: true,
  dragSnapMinutes: 5,
  propagationOrders: 500,
  propagationLevels: 5,
  version: 1,
  updatedAt: null,
};

describe("validateParameters", () => {
  const same = valuesOf(defaults);
  it("sends only what changed", () => {
    expect(validateParameters({ ...same, defaultDirection: "backward" }, defaults).patch).toEqual({ defaultDirection: "backward" });
    expect(validateParameters({ ...same, defaultFinite: false, dragSnapMinutes: "15", propagationLevels: "3" }, defaults).patch).toEqual({
      defaultFinite: false,
      dragSnapMinutes: 15,
      propagationLevels: 3,
    });
  });
  it("mirrors the service rules", () => {
    expect(validateParameters(same, defaults).errors.form).toBe("Nothing changed.");
    for (const bad of ["", "0", "61", "2.5"]) {
      expect(validateParameters({ ...same, dragSnapMinutes: bad }, defaults).errors.dragSnapMinutes).toBeTruthy();
    }
    expect(validateParameters({ ...same, dragSnapMinutes: "7" }, defaults).patch).toEqual({ dragSnapMinutes: 7 });
    for (const bad of ["", "0", "5001", "1.5", "x"]) {
      expect(validateParameters({ ...same, propagationOrders: bad }, defaults).errors.propagationOrders).toBeTruthy();
    }
    for (const bad of ["", "0", "21"]) {
      expect(validateParameters({ ...same, propagationLevels: bad }, defaults).errors.propagationLevels).toBeTruthy();
    }
    expect(validateParameters({ ...same, defaultMode: "weekly" }, defaults).errors.defaultMode).toBeTruthy();
    expect(validateParameters({ ...same, propagationOrders: "5000", propagationLevels: "20" }, defaults).patch).toEqual({ propagationOrders: 5000, propagationLevels: 20 });
  });
});

describe("Scheduling parameters page", () => {
  it("shows the built-in defaults and saves a change with If-Match and a key", async () => {
    handler = (_url, init) => {
      if (init.method === "PATCH") return { status: 200, body: { ...defaults, defaultDirection: "backward", version: 2, updatedAt: "2026-11-03T10:00:00Z" } };
      return { status: 200, body: defaults };
    };
    const { container } = renderAt("/scheduling/parameters");
    const direction = (await screen.findByLabelText(/Default direction/)) as HTMLSelectElement;
    expect(direction.value).toBe("forward");
    expect(screen.getByText("Never changed: these are the built-in defaults.")).toBeTruthy();
    expect(checkAxeAA(container).filter((v) => !shellRules.has(v.id))).toEqual([]);
    fireEvent.click(screen.getByRole("button", { name: "Save parameters" }));
    expect(await screen.findByText("Nothing changed.")).toBeTruthy();
    fireEvent.change(direction, { target: { value: "backward" } });
    fireEvent.click(screen.getByRole("button", { name: "Save parameters" }));
    expect(await screen.findByText("The parameters were saved (version 2).")).toBeTruthy();
    const patch = calls.find((c) => c.method === "PATCH")!;
    expect(patch.url.pathname).toBe("/api/v1/scheduling/parameters");
    expect(patch.headers.get("If-Match")).toBe('"1"');
    expect(patch.headers.get("Idempotency-Key")).toBeTruthy();
    expect(patch.body).toEqual({ defaultDirection: "backward" });
    // The next save uses the new version.
    fireEvent.change(screen.getByLabelText(/Gantt drag snap/), { target: { value: "30" } });
    fireEvent.click(screen.getByRole("button", { name: "Save parameters" }));
    await waitFor(() => expect(calls.filter((c) => c.method === "PATCH")).toHaveLength(2));
    expect(calls.filter((c) => c.method === "PATCH")[1].headers.get("If-Match")).toBe('"2"');
  });

  it("shows loading, then an error with a retry", async () => {
    let fail = true;
    handler = () => (fail ? { status: 500, body: { code: "COM-0500", detail: "database down" } } : { status: 200, body: defaults });
    renderAt("/scheduling/parameters");
    expect(screen.getByText("Loading the parameters…")).toBeTruthy();
    expect(await screen.findByText(/database down/)).toBeTruthy();
    fail = false;
    fireEvent.click(screen.getByRole("button", { name: "Try again" }));
    expect(await screen.findByLabelText(/Default direction/)).toBeTruthy();
  });

  it("offers the latest version after a stale change", async () => {
    let current = defaults;
    handler = (_url, init) =>
      init.method === "PATCH" ? { status: 412, body: { code: "SCHED-0140", detail: "current version is 3" } } : { status: 200, body: current };
    renderAt("/scheduling/parameters");
    fireEvent.change(await screen.findByLabelText(/Gantt drag snap/), { target: { value: "10" } });
    fireEvent.click(screen.getByRole("button", { name: "Save parameters" }));
    expect(await screen.findByText("The parameters changed since you opened them")).toBeTruthy();
    current = { ...defaults, dragSnapMinutes: 60, version: 3, updatedAt: "2026-11-03T10:00:00Z" };
    fireEvent.click(screen.getByRole("button", { name: "Load the latest version" }));
    await waitFor(() => expect((screen.getByLabelText(/Gantt drag snap/) as HTMLInputElement).value).toBe("60"));
  });

  it("puts a service field error next to its field and explains a missing duty", async () => {
    handler = (_url, init) =>
      init.method === "PATCH" ? { status: 400, body: { code: "COM-0001", detail: "request failed validation", errors: { propagationOrders: "must be 1 to 5000" } } } : { status: 200, body: defaults };
    renderAt("/scheduling/parameters");
    fireEvent.change(await screen.findByLabelText(/Propagation: most orders/), { target: { value: "50" } });
    fireEvent.click(screen.getByRole("button", { name: "Save parameters" }));
    expect(await screen.findByText("must be 1 to 5000")).toBeTruthy();
  });

  it("explains a refused change when the duty is missing", async () => {
    handler = (_url, init) => (init.method === "PATCH" ? { status: 403, body: { code: "AUTH-0002", detail: "missing duty" } } : { status: 200, body: defaults });
    renderAt("/scheduling/parameters");
    fireEvent.change(await screen.findByLabelText(/Gantt drag snap/), { target: { value: "10" } });
    fireEvent.click(screen.getByRole("button", { name: "Save parameters" }));
    expect(await screen.findByText(/needs the scheduling.setup.write duty/)).toBeTruthy();
  });
});

describe("New run page with parameters", () => {
  it("starts from the parameters and keeps what the planner changed", async () => {
    handler = () => ({ status: 200, body: { ...defaults, defaultDirection: "backward", defaultFinite: false, defaultMode: "operations" } });
    renderAt("/scheduling/runs/new");
    await waitFor(() => expect((screen.getByLabelText("Direction") as HTMLSelectElement).value).toBe("backward"));
    expect((screen.getByLabelText("Finite capacity") as HTMLInputElement).checked).toBe(false);
  });

  it("does not overwrite a choice made before the parameters arrived", async () => {
    let release: () => void = () => {};
    const gate = new Promise<void>((resolve) => (release = resolve));
    vi.stubGlobal(
      "fetch",
      vi.fn(async () => {
        await gate;
        return new Response(JSON.stringify({ ...defaults, defaultDirection: "backward" }), { status: 200, headers: { "Content-Type": "application/json" } });
      }),
    );
    renderAt("/scheduling/runs/new");
    fireEvent.change(screen.getByLabelText("Direction"), { target: { value: "forward" } });
    fireEvent.click(screen.getByLabelText("Finite capacity"));
    release();
    await waitFor(() => expect(vi.mocked(fetch)).toHaveBeenCalled());
    await new Promise((r) => setTimeout(r, 20));
    expect((screen.getByLabelText("Direction") as HTMLSelectElement).value).toBe("forward");
  });
});

describe("Workspace with parameters", () => {
  it("names the stored propagation limits", async () => {
    handler = (url) => {
      if (url.pathname.endsWith("/parameters")) return { status: 200, body: { ...defaults, propagationOrders: 100, propagationLevels: 3, version: 4 } };
      if (url.pathname.endsWith("/resources") || url.pathname.endsWith("/gantt")) return { status: 200, body: { items: [] } };
      return undefined;
    };
    renderAt("/scheduling");
    expect(await screen.findByText(/up to 100 orders or 3 levels/)).toBeTruthy();
  });
});
