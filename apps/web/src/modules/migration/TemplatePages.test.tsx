import { fireEvent, render, screen, waitFor } from "@testing-library/react";
import { MemoryRouter, Route, Routes } from "react-router-dom";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { checkAxeAA } from "../../shell/a11y";
import { PrincipalProvider } from "../../shell/principal";
import { findTable } from "../../test-utils";
import type { DataTemplate, MigrationRun } from "./api/client";
import { parseTemplateForm } from "./domain";
import migration from "./module";

type Reply = { status: number; body: unknown };
let handler: (url: URL, init: RequestInit) => Reply | undefined;
let calls: { method: string; url: URL; init: RequestInit; body: unknown }[];
const json = (status: number, body: unknown): Reply => ({ status, body });
const shellRules = new Set(["page-has-heading-one", "landmark-unique", "region", "bypass"]);
const violations = (container: HTMLElement) => checkAxeAA(container).filter((v) => !shellRules.has(v.id));

beforeEach(() => {
  calls = [];
  handler = () => undefined;
  vi.stubGlobal(
    "fetch",
    vi.fn(async (input: string, init: RequestInit = {}) => {
      const url = new URL(input, "http://localhost");
      calls.push({ method: init.method ?? "GET", url, init, body: init.body ? JSON.parse(String(init.body)) : undefined });
      const reply = handler(url, init) ?? json(404, { code: "TEST-404", detail: "no fake" });
      return new Response(JSON.stringify(reply.body), { status: reply.status, headers: { "Content-Type": "application/json" } });
    }),
  );
});
afterEach(() => vi.unstubAllGlobals());

function renderAt(path: string, duties: string[] = ["migration.run", "migration.spec.write"]) {
  return render(
    <PrincipalProvider duties={duties}>
      <MemoryRouter initialEntries={[path]}>
        <Routes>
          {migration.routes.map((route) => (
            <Route key={route.path} path={route.path} element={route.element} />
          ))}
        </Routes>
      </MemoryRouter>
    </PrincipalProvider>,
  );
}

const TEMPLATE = "0195f0a0-0000-7000-8000-0000000000c1";
const SPEC = "0195f0a0-0000-7000-8000-0000000000b1";
const RUN = "0195f0a0-0000-7000-8000-0000000000a1";
const template: DataTemplate = {
  id: TEMPLATE,
  entity: "customer",
  name: "Retail defaults",
  version: 2,
  checksum: "cd".repeat(32),
  defaultCount: 2,
  createdAt: "2026-10-30T09:00:00Z",
  defaults: { paymentTerms: "NET30", creditLimit: "500" },
};
const run: MigrationRun = {
  id: RUN,
  sourceSystem: "ax2012",
  mode: "rehearsal",
  entity: "customer",
  status: "succeeded",
  dryRun: false,
  mappingVersion: 1,
  mappingSpecId: SPEC,
  dataTemplateId: TEMPLATE,
  counts: { extracted: 2, mapped: 2, created: 2, updated: 0, unchanged: 0, templated: 1, rejected: 0, warned: 0 },
  rejects: [],
  warnings: [],
  absent: [],
  createdAt: "2026-10-30T10:00:00Z",
};

describe("template form parsing", () => {
  const ok = { name: " Retail ", entity: "customer", version: "1", defaults: '{"terms":"NET30"}' };
  it("trims and accepts a valid form", () => {
    expect(parseTemplateForm(ok).value).toEqual({ name: "Retail", entity: "customer", version: 1, defaults: { terms: "NET30" } });
  });
  it("reports each field", () => {
    const { errors } = parseTemplateForm({ name: "", entity: "", version: "0", defaults: "" });
    expect(Object.keys(errors).sort()).toEqual(["defaults", "entity", "name", "version"]);
    expect(parseTemplateForm({ ...ok, defaults: "[1]" }).errors.defaults).toMatch(/object/);
    expect(parseTemplateForm({ ...ok, defaults: "{}" }).errors.defaults).toMatch(/at least one/);
    expect(parseTemplateForm({ ...ok, defaults: '{"a":5}' }).errors.defaults).toMatch(/string/);
    expect(parseTemplateForm({ ...ok, defaults: "{" }).errors.defaults).toMatch(/Not valid JSON/);
    expect(parseTemplateForm({ ...ok, version: "1.5" }).errors.version).toBeTruthy();
    expect(parseTemplateForm({ ...ok, name: "x".repeat(81) }).errors.name).toMatch(/80/);
  });
});

describe("data templates list", () => {
  it("shows templates, filters by entity and opens one", async () => {
    handler = () => json(200, { items: [{ ...template, defaults: undefined }] });
    const { container } = renderAt("/migration/templates");
    const table = await findTable({ name: "Data templates" });
    expect(table).toHaveTextContent("Retail defaults");
    expect(violations(container)).toEqual([]);
    fireEvent.change(screen.getByLabelText("Entity"), { target: { value: "vendor" } });
    await waitFor(() => expect(calls.some((c) => c.url.searchParams.get("entity") === "vendor")).toBe(true));
    expect(screen.getByRole("link", { name: "Publish template" })).toBeInTheDocument();
  });

  it("shows the empty state and hides publish without the duty", async () => {
    handler = () => json(200, { items: [] });
    renderAt("/migration/templates", ["migration.run"]);
    expect(await screen.findByText("No data templates yet")).toBeInTheDocument();
    expect(screen.queryByRole("link", { name: "Publish template" })).not.toBeInTheDocument();
  });

  it("shows the error state and retries", async () => {
    handler = () => json(403, { code: "AUTH-0003", detail: "missing duty" });
    renderAt("/migration/templates");
    expect(await screen.findByText(/missing duty \(AUTH-0003\)/)).toBeInTheDocument();
    handler = () => json(200, { items: [template] });
    fireEvent.click(screen.getByRole("button", { name: "Try again" }));
    expect(await findTable({ name: "Data templates" })).toHaveTextContent("Retail defaults");
  });
});

describe("publish data template", () => {
  function fill() {
    fireEvent.change(screen.getByLabelText("Name"), { target: { value: "Retail defaults" } });
    fireEvent.change(screen.getByLabelText("Entity"), { target: { value: "customer" } });
    fireEvent.change(screen.getByLabelText("Defaults (JSON)"), { target: { value: '{"paymentTerms":"NET30"}' } });
  }

  it("validates locally, then posts with an idempotency key and opens the stored template", async () => {
    handler = (_url, init) => json(init.method === "POST" ? 201 : 200, template);
    const { container } = renderAt("/migration/templates/new");
    expect(violations(container)).toEqual([]);
    fireEvent.click(screen.getByRole("button", { name: "Publish template" }));
    expect(await screen.findByText("Enter a template name.")).toBeInTheDocument();
    expect(calls).toHaveLength(0);
    fill();
    fireEvent.click(screen.getByRole("button", { name: "Publish template" }));
    expect(await screen.findByRole("heading", { name: "Retail defaults v2" })).toBeInTheDocument();
    const post = calls.find((c) => c.method === "POST");
    expect(post?.body).toEqual({ name: "Retail defaults", entity: "customer", version: 1, defaults: { paymentTerms: "NET30" } });
    expect(new Headers(post?.init.headers).get("Idempotency-Key")).toBeTruthy();
  });

  it("shows the service problem for a used version", async () => {
    handler = () => json(409, { code: "MIG-TEMPLATE-VERSION-EXISTS", detail: "publish a higher version" });
    renderAt("/migration/templates/new");
    fill();
    fireEvent.click(screen.getByRole("button", { name: "Publish template" }));
    expect(await screen.findByText(/publish a higher version \(MIG-TEMPLATE-VERSION-EXISTS\)/)).toBeInTheDocument();
  });
});

describe("data template detail", () => {
  it("lists the defaults", async () => {
    handler = () => json(200, template);
    renderAt(`/migration/templates/${TEMPLATE}`);
    const table = await findTable({ name: "Template defaults" });
    expect(table).toHaveTextContent("paymentTerms");
    expect(table).toHaveTextContent("NET30");
  });

  it("shows a not-found error", async () => {
    handler = () => json(404, { code: "AUTH-0404", detail: "data template not found" });
    renderAt(`/migration/templates/${TEMPLATE}`);
    expect(await screen.findByText(/data template not found \(AUTH-0404\)/)).toBeInTheDocument();
  });
});

describe("runs with templates", () => {
  const spec = { id: SPEC, entity: "customer", version: 1, checksum: "ab", fieldCount: 2, testCount: 1, createdAt: "2026-10-30T09:00:00Z" };

  it("offers templates for the chosen entity and sends the chosen id", async () => {
    const vendorTemplate = { ...template, id: "0195f0a0-0000-7000-8000-0000000000c2", entity: "vendor", name: "Vendor defaults" };
    handler = (url, init) => {
      if (init.method === "POST") return json(202, { run });
      if (url.pathname.endsWith("/mapping-specs")) return json(200, { items: [spec] });
      if (url.pathname.endsWith("/data-templates")) return json(200, { items: [template, vendorTemplate] });
      return json(200, run);
    };
    renderAt("/migration/runs/new");
    await screen.findByRole("option", { name: "customer v1" });
    fireEvent.change(screen.getByLabelText("Mapping spec"), { target: { value: SPEC } });
    expect(await screen.findByRole("option", { name: "Retail defaults v2 (customer)" })).toBeInTheDocument();
    expect(screen.queryByRole("option", { name: /Vendor defaults/ })).not.toBeInTheDocument();
    fireEvent.change(screen.getByLabelText("Data template (optional)"), { target: { value: TEMPLATE } });
    fireEvent.click(screen.getByRole("button", { name: "Start run" }));
    await screen.findByRole("heading", { name: "customer migration" });
    expect(calls.find((c) => c.method === "POST")?.body).toMatchObject({ mappingSpecId: SPEC, dataTemplateId: TEMPLATE });
  });

  it("drops a chosen template when the spec changes to another entity", async () => {
    const vendorSpec = { ...spec, id: "0195f0a0-0000-7000-8000-0000000000b2", entity: "vendor" };
    handler = (url, init) => {
      if (init.method === "POST") return json(202, { run: { ...run, dataTemplateId: undefined } });
      if (url.pathname.endsWith("/mapping-specs")) return json(200, { items: [spec, vendorSpec] });
      if (url.pathname.endsWith("/data-templates")) return json(200, { items: [template] });
      return json(200, run);
    };
    renderAt("/migration/runs/new");
    await screen.findByRole("option", { name: "customer v1" });
    fireEvent.change(screen.getByLabelText("Mapping spec"), { target: { value: SPEC } });
    fireEvent.change(await screen.findByLabelText("Data template (optional)"), { target: { value: TEMPLATE } });
    fireEvent.change(screen.getByLabelText("Mapping spec"), { target: { value: vendorSpec.id } });
    expect(screen.getByLabelText("Data template (optional)")).toHaveValue("");
    fireEvent.click(screen.getByRole("button", { name: "Start run" }));
    await screen.findByRole("heading", { name: "customer migration" });
    const body = calls.find((c) => c.method === "POST")?.body;
    expect(body).toMatchObject({ mappingSpecId: vendorSpec.id });
    expect(body).not.toHaveProperty("dataTemplateId");
  });

  it("still starts a run when the templates cannot be loaded", async () => {
    handler = (url, init) => {
      if (init.method === "POST") return json(202, { run: { ...run, dataTemplateId: undefined } });
      if (url.pathname.endsWith("/mapping-specs")) return json(200, { items: [spec] });
      if (url.pathname.endsWith("/data-templates")) return json(500, { code: "COM-0500", detail: "boom" });
      return json(200, run);
    };
    renderAt("/migration/runs/new");
    await screen.findByRole("option", { name: "customer v1" });
    expect(await screen.findByText(/Data templates could not be loaded/)).toBeInTheDocument();
    fireEvent.change(screen.getByLabelText("Mapping spec"), { target: { value: SPEC } });
    fireEvent.click(screen.getByRole("button", { name: "Start run" }));
    await screen.findByRole("heading", { name: "customer migration" });
    expect(calls.find((c) => c.method === "POST")?.body).not.toHaveProperty("dataTemplateId");
  });

  it("shows the lineage of a templated run", async () => {
    handler = (url) =>
      url.pathname.endsWith("/ledger")
        ? json(200, { items: [{ sourceKey: "C1", action: "created", defaultFromTemplate: ["paymentTerms"] }] })
        : json(200, run);
    renderAt(`/migration/runs/${RUN}`);
    const table = await findTable({ name: "Rows that took template defaults" });
    expect(table).toHaveTextContent("C1");
    expect(table).toHaveTextContent("paymentTerms");
    expect(calls.some((c) => c.url.pathname.endsWith("/ledger") && c.url.searchParams.get("templated") === "true")).toBe(true);
    expect(screen.getByText("Used template defaults")).toBeInTheDocument();
  });

  it("shows a lineage error and the empty state", async () => {
    handler = (url) => (url.pathname.endsWith("/ledger") ? json(500, { code: "COM-0500", detail: "boom" }) : json(200, run));
    renderAt(`/migration/runs/${RUN}`);
    expect(await screen.findByText(/boom \(COM-0500\)/)).toBeInTheDocument();
    handler = (url) => (url.pathname.endsWith("/ledger") ? json(200, { items: [] }) : json(200, run));
    fireEvent.click(screen.getByRole("button", { name: "Try again" }));
    expect(await screen.findByText("No rows used a template default")).toBeInTheDocument();
  });
});
