import { screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { describe, expect, it } from "vitest";
import { PrincipalProvider } from "../../shell/principal";
import { json, pageViolations, renderRoutes, server, useFakeServer } from "../../shell/testing/fakeServer";
import module from "./module";
import { hasRows } from "./checks";
import { currentStep } from "./pages";
import type { SetupStep } from "../../shell/tenant/api";

useFakeServer();

const stepNames = [
  ["features", "What your company does"],
  ["company", "Company and legal entity"],
  ["fiscal-calendar", "Fiscal calendar"],
];

function setupBody(statuses: Record<string, string>) {
  const steps = stepNames.map(([key, name]): SetupStep => ({ key, name, description: `${name} help`, status: (statuses[key] ?? "todo") as SetupStep["status"] }));
  const finished = steps.filter((s) => s.status !== "todo").length;
  return { steps, total: steps.length, finished, complete: finished === steps.length };
}

function render(path: string, duties: string[]) {
  const routes = module.routes.map((r) => ({ path: r.path, element: <PrincipalProvider duties={duties}>{r.element}</PrincipalProvider> }));
  return renderRoutes(routes, path);
}

describe("setup wizard", () => {
  it("opens on the first step still to do and shows progress", async () => {
    server.handler = (url) => (url.pathname === "/api/v1/tenant/setup" ? json(200, setupBody({ features: "done" })) : undefined);
    const { container } = render("/setup", []);
    expect(await screen.findByText("1 of 3 steps finished.")).toBeInTheDocument();
    expect(screen.getByRole("heading", { name: "Company and legal entity" })).toBeInTheDocument();
    expect(screen.getByRole("link", { name: "Open legal entities" })).toHaveAttribute("href", "/org/legal-entities");
    expect(pageViolations(container)).toEqual([]);
  });

  it("saves a step as done and moves on, so progress survives a reload", async () => {
    const user = userEvent.setup();
    let statuses: Record<string, string> = {};
    server.handler = (url, init) => {
      if (url.pathname === "/api/v1/tenant/setup/steps/features" && init.method === "PUT") {
        statuses = { features: JSON.parse(String(init.body)).status };
        return json(200, setupBody(statuses));
      }
      return url.pathname === "/api/v1/tenant/setup" ? json(200, setupBody(statuses)) : undefined;
    };
    render("/setup", []);
    await user.click(await screen.findByRole("button", { name: "Mark as done" }));
    expect(await screen.findByText("1 of 3 steps finished.")).toBeInTheDocument();
    expect(server.calls.find((c) => c.method === "PUT")?.body).toEqual({ status: "done" });
    expect(screen.getByRole("heading", { name: "Company and legal entity" })).toBeInTheDocument();
  });

  it("lets a person skip a step and reopen a finished one", async () => {
    const user = userEvent.setup();
    server.handler = (url) => (url.pathname === "/api/v1/tenant/setup" ? json(200, setupBody({ features: "done", company: "skipped" })) : undefined);
    render("/setup", []);
    await user.click(await screen.findByRole("button", { name: /What your company does/ }));
    expect(screen.getByRole("button", { name: "Reopen this step" })).toBeInTheDocument();
    expect(screen.getByRole("list", { name: "Setup steps" }).textContent).toContain("Skipped");
  });

  it("says when setup is finished", async () => {
    server.handler = (url) => (url.pathname === "/api/v1/tenant/setup" ? json(200, setupBody({ features: "done", company: "done", "fiscal-calendar": "skipped" })) : undefined);
    render("/setup", []);
    expect(await screen.findByText("Setup is finished.")).toBeInTheDocument();
  });

  it("explains a failure and offers a retry", async () => {
    server.handler = () => json(500, { code: "COM-0500", detail: "boom" });
    render("/setup", []);
    expect(await screen.findByRole("button", { name: "Try again" })).toBeInTheDocument();
  });

  it("finds the step to resume at", () => {
    expect(currentStep(setupBody({ features: "done", company: "skipped" }).steps)?.key).toBe("fiscal-calendar");
    expect(currentStep(setupBody({ features: "done", company: "done", "fiscal-calendar": "done" }).steps)).toBeUndefined();
  });
});

describe("setup checklist", () => {
  it("shows what exists and what is missing per area, and sends people to fix it", async () => {
    server.handler = (url) => {
      if (url.pathname === "/api/v1/org/legal-entities") return json(200, { items: [{ id: "1" }] });
      if (url.pathname === "/api/v1/org/warehouses") return json(200, { items: [] });
      if (url.pathname === "/api/v1/org/sites") return json(500, { code: "COM-0500", detail: "down" });
      return undefined;
    };
    const { container } = render("/setup/checklist", ["org.read"]);
    expect(await screen.findByText("1 thing is still missing.")).toBeInTheDocument();
    const inventory = within(screen.getByRole("list", { name: "Inventory" }));
    expect(inventory.getByText("No warehouse yet, so stock cannot be received")).toBeInTheDocument();
    expect(inventory.getByText("Could not check")).toBeInTheDocument();
    expect(within(screen.getByRole("list", { name: "Company" })).getByText("Legal entity added")).toBeInTheDocument();
    expect(inventory.getAllByRole("link", { name: "Fix this" })[0]).toHaveAttribute("href", "/org/sites");
    expect(pageViolations(container)).toEqual([]);
  });

  it("only checks what the person may read", async () => {
    server.handler = () => json(200, { items: [] });
    render("/setup/checklist", []);
    expect(await screen.findByText("Nothing to check")).toBeInTheDocument();
    expect(server.calls).toHaveLength(0);
  });

  it("reads a list response the way the services send it", () => {
    expect(hasRows({ items: [{}] })).toBe(true);
    expect(hasRows({ items: [] })).toBe(false);
    expect(hasRows([1])).toBe(true);
    expect(hasRows(null)).toBe(false);
  });
});

describe("features page", () => {
  const catalog = (off: string[]) => ({
    features: ["finance", "edi"].map((key) => ({ key, name: key === "edi" ? "Electronic data interchange" : "Finance", description: "", module: key, dependsOn: [], enabled: !off.includes(key) })),
    templates: [{ key: "distribution", name: "Distribution", description: "Buy and sell goods.", features: ["finance"] }],
  });

  it("toggles a feature and applies a template", async () => {
    const user = userEvent.setup();
    let off: string[] = [];
    server.handler = (url, init) => {
      if (url.pathname !== "/api/v1/tenant/features") return undefined;
      if (init.method === "PUT") {
        const body = JSON.parse(String(init.body)) as { template?: string; features?: Record<string, boolean> };
        off = body.template ? ["edi"] : Object.entries(body.features ?? {}).flatMap(([k, on]) => (on ? [] : [k]));
      }
      return json(200, catalog(off));
    };
    render("/setup/features", []);
    const edi = await screen.findByRole("checkbox", { name: /Electronic data interchange/ });
    expect(edi).toBeChecked();
    await user.click(edi);
    await waitFor(() => expect(screen.getByRole("checkbox", { name: /Electronic data interchange/ })).not.toBeChecked());
    expect(server.calls.find((c) => c.method === "PUT")?.body).toEqual({ features: { edi: false } });
    await user.click(screen.getByRole("button", { name: "Use the Distribution template" }));
    await waitFor(() => expect(server.calls.filter((c) => c.method === "PUT")).toHaveLength(2));
    expect(server.calls.filter((c) => c.method === "PUT")[1].body).toEqual({ template: "distribution" });
  });

  it("shows the plain-words dependency message when a change is refused", async () => {
    const user = userEvent.setup();
    server.handler = (url, init) => {
      if (init.method === "PUT") return json(422, { code: "IDN-FEATURE-DEPENDENCY", detail: "x", errors: { finance: "Finance cannot be turned off while Electronic data interchange uses it. Turn those off first." } });
      return url.pathname === "/api/v1/tenant/features" ? json(200, catalog([])) : undefined;
    };
    render("/setup/features", []);
    await user.click(await screen.findByRole("checkbox", { name: /^Finance/ }));
    expect(await screen.findByText(/Finance cannot be turned off while Electronic data interchange uses it/)).toBeInTheDocument();
    expect(screen.getByRole("checkbox", { name: /^Finance/ })).toBeChecked();
  });
});

describe("demo company page", () => {
  it("loads the demo company and reports what was added", async () => {
    const user = userEvent.setup();
    server.handler = (url, init) => (url.pathname === "/api/v1/org/demo-company" && init.method === "POST" ? json(200, { package: "demo-manufacturer", created: 12, present: 3 }) : undefined);
    render("/setup/demo-company", []);
    await user.click(screen.getByRole("button", { name: "Load the demo company" }));
    expect(await screen.findByText("Added 12 sample records. 3 were already there.")).toBeInTheDocument();
  });

  it("explains a refusal", async () => {
    const user = userEvent.setup();
    server.handler = () => json(403, { code: "COM-0403", detail: "no" });
    render("/setup/demo-company", []);
    await user.click(screen.getByRole("button", { name: "Load the demo company" }));
    expect(await screen.findByText(/permission/)).toBeInTheDocument();
  });
});
