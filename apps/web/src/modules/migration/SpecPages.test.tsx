import { fireEvent, render, screen, waitFor } from "@testing-library/react";
import { MemoryRouter, Route, Routes } from "react-router-dom";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { checkAxeAA } from "../../shell/a11y";
import { PrincipalProvider } from "../../shell/principal";
import { findTable } from "../../test-utils";
import type { MappingSpec } from "./api/client";
import migration from "./module";

type Reply = { status: number; body: unknown };
let handler: (url: URL, init: RequestInit) => Reply | undefined;
let calls: { method: string; url: URL; body: unknown }[];
const json = (status: number, body: unknown): Reply => ({ status, body });
const shellRules = new Set(["page-has-heading-one", "landmark-unique", "region", "bypass"]);

beforeEach(() => {
  calls = [];
  handler = () => undefined;
  vi.stubGlobal(
    "fetch",
    vi.fn(async (input: string, init: RequestInit = {}) => {
      const url = new URL(input, "http://localhost");
      calls.push({ method: init.method ?? "GET", url, body: init.body ? JSON.parse(String(init.body)) : undefined });
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

const PROJECT = "0195f0a0-0000-7000-8000-0000000000c1";
const projects = { items: [{ id: PROJECT, name: "AX 2012 cutover", projectType: "migrate", status: "active", version: 1 }] };
const wide: MappingSpec = { id: "0195f0a0-0000-7000-8000-0000000000b1", entity: "customer", version: 1, checksum: "ab".repeat(32), fieldCount: 2, testCount: 1, naturalKey: [], createdAt: "2026-10-30T09:00:00Z", mapping: { entity: "customer", version: 1 } };
const scoped: MappingSpec = { ...wide, id: "0195f0a0-0000-7000-8000-0000000000b2", projectId: PROJECT, naturalKey: ["company", "account"] };

describe("mapping specs by project", () => {
  it("shows each spec's project and natural key and filters by project", async () => {
    handler = (url) => (url.pathname.endsWith("/projects") ? json(200, projects) : json(200, { items: url.searchParams.get("projectId") === "none" ? [wide] : [wide, scoped] }));
    const { container } = renderAt("/migration/specs");
    const table = await findTable({ name: "Mapping specs" });
    await waitFor(() => expect(table).toHaveTextContent("AX 2012 cutover"));
    expect(table).toHaveTextContent("company, account");
    expect(table).toHaveTextContent("All projects");
    expect(checkAxeAA(container).filter((v) => !shellRules.has(v.id))).toEqual([]);

    fireEvent.change(screen.getByLabelText("Project"), { target: { value: "none" } });
    await waitFor(() => expect(calls.some((c) => c.url.pathname.endsWith("/mapping-specs") && c.url.searchParams.get("projectId") === "none")).toBe(true));
    await waitFor(() => expect(screen.getByRole("table")).not.toHaveTextContent("AX 2012 cutover"));
  });

  it("still lists specs when the projects cannot load", async () => {
    handler = (url) => (url.pathname.endsWith("/projects") ? json(403, { code: "AUTH-0003", detail: "no" }) : json(200, { items: [scoped] }));
    renderAt("/migration/specs");
    expect(await findTable({ name: "Mapping specs" })).toHaveTextContent(PROJECT.slice(0, 8));
  });

  it("shows the empty state for a project without specs", async () => {
    handler = (url) => (url.pathname.endsWith("/projects") ? json(200, projects) : json(200, { items: url.searchParams.get("projectId") === PROJECT ? [] : [wide] }));
    renderAt("/migration/specs");
    await findTable({ name: "Mapping specs" });
    await waitFor(() => expect(screen.getByRole("option", { name: "AX 2012 cutover" })).toBeInTheDocument());
    fireEvent.change(screen.getByLabelText("Project"), { target: { value: PROJECT } });
    expect(await screen.findByText("No mapping specs yet")).toBeInTheDocument();
  });

  it("shows the error state and retries", async () => {
    handler = (url) => (url.pathname.endsWith("/projects") ? json(200, projects) : json(500, { code: "COM-0500", detail: "boom" }));
    renderAt("/migration/specs");
    expect(await screen.findByText(/boom/)).toBeInTheDocument();
    handler = () => json(200, { items: [wide] });
    fireEvent.click(screen.getByRole("button", { name: "Try again" }));
    expect(await findTable({ name: "Mapping specs" })).toHaveTextContent("customer");
  });

  it("publishes into the chosen project", async () => {
    handler = (url, init) => {
      if (url.pathname.endsWith("/projects")) return json(200, projects);
      if (init.method === "POST") return json(201, scoped);
      return json(200, scoped);
    };
    renderAt("/migration/specs/new");
    await waitFor(() => expect(screen.getByRole("option", { name: "AX 2012 cutover" })).toBeInTheDocument());
    fireEvent.change(screen.getByLabelText("Project (optional)"), { target: { value: PROJECT } });
    fireEvent.change(screen.getByLabelText("Mapping JSON"), { target: { value: '{"entity":"customer","version":1}' } });
    fireEvent.click(screen.getByRole("button", { name: "Publish spec" }));
    await waitFor(() => expect(calls.some((c) => c.method === "POST")).toBe(true));
    expect(calls.find((c) => c.method === "POST")?.body).toEqual({ mapping: { entity: "customer", version: 1 }, projectId: PROJECT });
  });

  it("publishes tenant-wide without a project and shows the service's refusal", async () => {
    handler = (url, init) => {
      if (url.pathname.endsWith("/projects")) return json(200, projects);
      if (init.method === "POST") return json(422, { code: "MIG-MAP-INVALID", detail: "naturalKey column x is not produced by any field rule" });
      return json(200, wide);
    };
    renderAt("/migration/specs/new");
    fireEvent.change(await screen.findByLabelText("Mapping JSON"), { target: { value: '{"entity":"customer","version":1}' } });
    fireEvent.click(screen.getByRole("button", { name: "Publish spec" }));
    expect(await screen.findByText(/not produced by any field rule/)).toBeInTheDocument();
    expect(calls.find((c) => c.method === "POST")?.body).toEqual({ mapping: { entity: "customer", version: 1 } });
  });

  it("shows the project and natural key on the detail page", async () => {
    handler = (url) => (url.pathname.endsWith("/projects") ? json(200, projects) : json(200, scoped));
    renderAt(`/migration/specs/${scoped.id}`);
    expect(await screen.findByRole("link", { name: "View the project" })).toHaveAttribute("href", `/migration/projects/${PROJECT}`);
    expect(screen.getByText("company, account")).toBeInTheDocument();
  });

  it("says a spec with no project is tenant-wide", async () => {
    handler = () => json(200, wide);
    renderAt(`/migration/specs/${wide.id}`);
    expect(await screen.findByText("All projects (tenant-wide)")).toBeInTheDocument();
    expect(screen.getByText("Not set")).toBeInTheDocument();
  });
});
