import { fireEvent, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { describe, expect, it } from "vitest";
import { PrincipalProvider } from "../../shell/principal";
import { json, pageViolations, renderRoutes, server, useFakeServer } from "../../shell/testing/fakeServer";
import module from "./module";
import { hasRows } from "./checks";
import { templateColumns } from "./ImportStep";
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

  it("asks before resetting, and resets only after the person confirms", async () => {
    const user = userEvent.setup();
    server.handler = (url, init) => (url.pathname === "/api/v1/org/demo-company:reset" && init.method === "POST" ? json(200, { package: "demo-manufacturer", removed: 40, created: 40, present: 0 }) : undefined);
    const { container } = render("/setup/demo-company", []);
    await user.click(screen.getByRole("button", { name: "Reset the demo company" }));
    const dialog = within(screen.getByRole("alertdialog", { name: "Reset the demo company?" }));
    expect(dialog.getByText(/cannot be undone/)).toBeInTheDocument();
    expect(pageViolations(container)).toEqual([]);
    await user.click(dialog.getByRole("button", { name: "Keep it as it is" }));
    expect(screen.queryByRole("alertdialog")).not.toBeInTheDocument();
    expect(server.calls).toHaveLength(0);

    await user.click(screen.getByRole("button", { name: "Reset the demo company" }));
    await user.click(within(screen.getByRole("alertdialog")).getByRole("button", { name: "Reset the demo company" }));
    expect(await screen.findByText("Removed 40 demo records and added 40 fresh ones.")).toBeInTheDocument();
    expect(server.calls.map((c) => `${c.method} ${c.url.pathname}`)).toEqual(["POST /api/v1/org/demo-company:reset"]);
  });

  it("says why a reset was refused and that nothing was removed", async () => {
    const user = userEvent.setup();
    server.handler = () => json(409, { code: "ORG-DEMO-IN-USE", detail: "records that the demo company did not create are attached to it (1 site)" });
    render("/setup/demo-company", []);
    await user.click(screen.getByRole("button", { name: "Reset the demo company" }));
    await user.click(within(screen.getByRole("alertdialog")).getByRole("button", { name: "Reset the demo company" }));
    expect(await screen.findByText(/wasn't reset, because records that are not part of the demo have been added under it. Nothing was removed./)).toBeInTheDocument();
  });

  it("says so when there was no demo company to remove", async () => {
    const user = userEvent.setup();
    server.handler = () => json(200, { package: "demo-manufacturer", removed: 0, created: 40, present: 0 });
    render("/setup/demo-company", []);
    await user.click(screen.getByRole("button", { name: "Reset the demo company" }));
    await user.click(within(screen.getByRole("alertdialog")).getByRole("button", { name: "Reset the demo company" }));
    expect(await screen.findByText("There was no demo company to remove. Added 40 sample records.")).toBeInTheDocument();
  });
});

describe("setup wizard import step", () => {
  const IMPORT_DUTIES = ["migration.run", "migration.connection.write"];
  const importSetup = { steps: [{ key: "import", name: "Import your first records", description: "Bring in vendors, customers and products from a spreadsheet.", status: "todo" }], total: 1, finished: 0, complete: false };
  const connection = { id: "c1", name: "Spreadsheets", kind: "file", sourceSystem: "custom_files", config: {}, status: "active", version: 1 };
  const binding = (entity: string) => ({ id: `b-${entity}`, connectionProfileId: "c1", entity, source: { file: `${entity}s.csv` }, keyFields: ["number"], mappingSpecId: `s-${entity}`, status: "active", version: 1, createdBy: "u", createdAt: "2026-11-01T00:00:00Z", updatedAt: "2026-11-01T00:00:00Z" });
  const spec = (entity: string) => ({ id: `s-${entity}`, entity, version: 1, checksum: "x", fieldCount: 2, testCount: 0, naturalKey: ["number"], createdAt: "2026-11-01T00:00:00Z", mapping: { entity, version: 1, fields: [{ source: "number", target: "number", type: "string" }, { source: "name", target: "name", type: "string" }] } });
  const preview = {
    ok: true,
    durationMs: 12,
    mapping: { entity: "vendor", version: 1, mappingSpecId: "s-vendor" },
    columns: ["number", "name"],
    more: false,
    mapped: 1,
    rejected: 1,
    rows: [
      { index: 1, sourceKey: "V-1", source: { number: "V-1", name: "Acme" }, target: { number: "V-1", name: "Acme" } },
      { index: 2, sourceKey: "V-2", source: { number: "V-2", name: "" }, rejects: [{ field: "name", code: "MIG-MAP-REQUIRED", detail: "a name is required" }] },
    ],
  };

  function migrationServer(withBindings: boolean) {
    server.handler = (url, init) => {
      if (url.pathname === "/api/v1/tenant/setup") return json(200, importSetup);
      const entity = url.searchParams.get("entity") ?? "";
      if (url.pathname === "/api/v1/integration/entity-bindings") return json(200, { items: withBindings ? [binding(entity)] : [] });
      if (url.pathname === "/api/v1/integration/connection-profiles") return json(200, { items: [connection] });
      if (url.pathname === "/api/v1/integration/mapping-specs") return json(200, { items: [spec(entity)] });
      if (url.pathname.endsWith(":preview") && init.method === "POST") return json(200, preview);
      return undefined;
    };
  }

  it("checks a chosen file with the migration service and lists what is wrong with each row", async () => {
    migrationServer(true);
    render("/setup", IMPORT_DUTIES);
    const upload = await screen.findByLabelText(/^Try a file/);
    expect(screen.getByLabelText("What to import")).toHaveValue("vendor");
    const file = new File(["number,name\nV-1,Acme\nV-2,\n"], "vendors.csv", { type: "text/csv" });
    Object.defineProperty(file, "arrayBuffer", { value: async () => new TextEncoder().encode("number,name\nV-1,Acme\nV-2,\n").buffer });
    fireEvent.change(upload, { target: { files: [file] } });
    await waitFor(() => expect(screen.getByRole("button", { name: "Preview rows" })).toBeEnabled());
    fireEvent.click(screen.getByRole("button", { name: "Preview rows" }));
    expect(await screen.findByText(/name: a name is required/)).toBeInTheDocument();
    const sent = server.calls.find((c) => c.url.pathname === "/api/v1/integration/entity-bindings/b-vendor:preview")?.body as { upload?: { name: string } };
    expect(sent.upload?.name).toBe("vendors.csv");
    expect(screen.getByRole("link", { name: "Start the import" })).toHaveAttribute("href", "/migration/runs/new");
    expect(screen.queryByRole("link", { name: "Open import" })).not.toBeInTheDocument();
  });

  it("switches between vendors, customers and products", async () => {
    const user = userEvent.setup();
    migrationServer(true);
    render("/setup", IMPORT_DUTIES);
    await screen.findByLabelText(/^Try a file/);
    await user.selectOptions(screen.getByLabelText("What to import"), "product");
    await waitFor(() => expect(server.calls.some((c) => c.url.pathname === "/api/v1/integration/entity-bindings" && c.url.searchParams.get("entity") === "product")).toBe(true));
    expect(within(screen.getByLabelText("What to import")).getAllByRole("option").map((o) => o.textContent)).toEqual(["Vendors", "Customers", "Products"]);
  });

  it("says what to set up when no file import exists for the records yet", async () => {
    migrationServer(false);
    const { container } = render("/setup", IMPORT_DUTIES);
    expect(await screen.findByText("No file import is set up for vendors yet")).toBeInTheDocument();
    expect(screen.getByRole("link", { name: "Create a file connection" })).toHaveAttribute("href", "/migration/connections/new");
    expect(screen.getByRole("link", { name: "Create a binding for vendors" })).toHaveAttribute("href", "/migration/bindings/new");
    expect(screen.queryByRole("button", { name: "Preview rows" })).not.toBeInTheDocument();
    expect(pageViolations(container)).toEqual([]);
  });

  it("explains a missing duty and asks the migration service for nothing", async () => {
    migrationServer(true);
    render("/setup", []);
    expect(await screen.findByText("Your account can't import files")).toBeInTheDocument();
    expect(server.calls.filter((c) => c.url.pathname.startsWith("/api/v1/integration"))).toHaveLength(0);
    expect(screen.getByRole("button", { name: "Skip for now" })).toBeInTheDocument();
  });

  it("lets a person who can run imports but not check files start the import", async () => {
    migrationServer(true);
    render("/setup", ["migration.run"]);
    expect(await screen.findByText("Your account can't check files here")).toBeInTheDocument();
    expect(screen.getByRole("link", { name: "Start the import" })).toBeInTheDocument();
    expect(screen.queryByRole("button", { name: "Preview rows" })).not.toBeInTheDocument();
  });

  it("builds the template header from the columns the mapping reads", () => {
    expect(templateColumns({ fields: [{ source: "number" }, { sources: ["first", "last"], target: "name" }, { source: "number" }, { source: "", value: "x" }, { source: "changed", timeSource: "tz" }] })).toEqual(["number", "first", "last", "changed", "tz"]);
    expect(templateColumns(undefined)).toEqual([]);
    expect(templateColumns({ fields: "no" })).toEqual([]);
  });
});
