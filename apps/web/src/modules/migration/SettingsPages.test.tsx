import { fireEvent, render, screen, waitFor } from "@testing-library/react";
import { MemoryRouter, Route, Routes } from "react-router-dom";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { checkAxeAA } from "../../shell/a11y";
import { PrincipalProvider } from "../../shell/principal";
import type { MigrationSettings } from "./api/client";
import migration from "./module";
import { parseInterval } from "./SettingsPages";

type Reply = { status: number; body: unknown };
let handler: (method: string, body: unknown) => Reply | undefined;
let calls: { url: string; method: string; body: unknown }[];
const json = (status: number, body: unknown): Reply => ({ status, body });
const shellRules = new Set(["page-has-heading-one", "landmark-unique", "region", "bypass"]);

beforeEach(() => {
  calls = [];
  handler = () => undefined;
  vi.stubGlobal(
    "fetch",
    vi.fn(async (input: string, init: RequestInit = {}) => {
      const method = init.method ?? "GET";
      const body = init.body ? JSON.parse(String(init.body)) : undefined;
      calls.push({ url: input, method, body });
      const reply = handler(method, body) ?? json(404, { code: "TEST-404", detail: "no fake" });
      return new Response(JSON.stringify(reply.body), { status: reply.status, headers: { "Content-Type": "application/json" } });
    }),
  );
});
afterEach(() => vi.unstubAllGlobals());

function renderPage(duties: string[] = ["migration.run", "migration.project.write"]) {
  return render(
    <PrincipalProvider duties={duties}>
      <MemoryRouter initialEntries={["/migration/settings"]}>
        <Routes>
          {migration.routes.map((route) => (
            <Route key={route.path} path={route.path} element={route.element} />
          ))}
        </Routes>
      </MemoryRouter>
    </PrincipalProvider>,
  );
}

const defaults: MigrationSettings = { recurringMinIntervalMin: 15, version: 0 };
const saved: MigrationSettings = { recurringMinIntervalMin: 60, version: 1, updatedBy: "ben", updatedAt: "2026-11-03T10:00:00Z" };

describe("interval field", () => {
  it("accepts whole minutes within the bounds only", () => {
    expect(parseInterval("15").value).toBe(15);
    expect(parseInterval(" 1440 ").value).toBe(1440);
    for (const bad of ["", "14", "1441", "1.5", "-20", "abc", "60m"]) expect(parseInterval(bad).error).toBeTruthy();
  });
});

describe("migration settings", () => {
  it("shows the default until the tenant saves one", async () => {
    handler = () => json(200, defaults);
    const { container } = renderPage();
    expect(await screen.findByText("15 minutes")).toBeInTheDocument();
    expect(screen.getByText("Not changed from the default.")).toBeInTheDocument();
    expect(checkAxeAA(container).filter((v) => !shellRules.has(v.id))).toEqual([]);
  });

  it("validates before sending, then saves under the version it read", async () => {
    handler = (method, body) => (method === "PUT" ? json(200, { ...saved, recurringMinIntervalMin: (body as { recurringMinIntervalMin: number }).recurringMinIntervalMin }) : json(200, defaults));
    renderPage();
    fireEvent.click(await screen.findByRole("button", { name: "Edit setting" }));
    const field = screen.getByLabelText("Shortest gap between scheduled runs (minutes)");
    fireEvent.change(field, { target: { value: "5" } });
    fireEvent.click(screen.getByRole("button", { name: "Save setting" }));
    expect(await screen.findByText("Must be between 15 and 1440 minutes.")).toBeInTheDocument();
    expect(field).toHaveAttribute("aria-invalid", "true");
    expect(calls.some((c) => c.method === "PUT")).toBe(false);

    fireEvent.change(field, { target: { value: "60" } });
    fireEvent.click(screen.getByRole("button", { name: "Save setting" }));
    expect(await screen.findByText("60 minutes")).toBeInTheDocument();
    expect(calls.find((c) => c.method === "PUT")?.body).toEqual({ version: 0, recurringMinIntervalMin: 60 });
    expect(calls.find((c) => c.method === "PUT")?.url).toBe("/api/v1/integration/migration-settings");
    expect(screen.getByText(/Saved .* by ben\./)).toBeInTheDocument();
  });

  it("offers a reload when another admin saved first", async () => {
    let current = defaults;
    handler = (method) => (method === "PUT" ? json(409, { code: "MIG-VERSION-CONFLICT", detail: "current version is 1" }) : json(200, current));
    renderPage();
    fireEvent.click(await screen.findByRole("button", { name: "Edit setting" }));
    fireEvent.change(screen.getByLabelText("Shortest gap between scheduled runs (minutes)"), { target: { value: "30" } });
    fireEvent.click(screen.getByRole("button", { name: "Save setting" }));
    expect(await screen.findByText(/current version is 1 \(MIG-VERSION-CONFLICT\)/)).toBeInTheDocument();
    current = saved;
    fireEvent.click(screen.getByRole("button", { name: "Reload" }));
    expect(await screen.findByText("60 minutes")).toBeInTheDocument();
  });

  it("is read only without the project duty", async () => {
    handler = () => json(200, saved);
    renderPage(["migration.run"]);
    expect(await screen.findByText("60 minutes")).toBeInTheDocument();
    expect(screen.queryByRole("button", { name: "Edit setting" })).not.toBeInTheDocument();
  });

  it("shows the error state and retries", async () => {
    handler = () => json(403, { code: "AUTH-0003", detail: "missing duty" });
    renderPage();
    expect(await screen.findByText(/missing duty \(AUTH-0003\)/)).toBeInTheDocument();
    handler = () => json(200, saved);
    fireEvent.click(screen.getByRole("button", { name: "Try again" }));
    await waitFor(() => expect(screen.getByText("60 minutes")).toBeInTheDocument());
  });

  it("is in the nav behind the seeded read duty", () => {
    expect(migration.nav.find((n) => n.to === "/migration/settings")?.duty).toBe("migration.run");
  });
});
