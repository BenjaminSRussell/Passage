import { fireEvent, render, screen, waitFor, within } from "@testing-library/react";
import { MemoryRouter, Route, Routes, useLocation } from "react-router-dom";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { checkAxeAA } from "../../shell/a11y";
import { loadModules } from "../registry";
import type { Project } from "./api/client";
import projects from "./module";
import { validateProject, type ProjectValues } from "./NewProjectPage";

const LE = "11111111-0000-4000-8000-000000000001";
const CUSTOMER = "22222222-0000-4000-8000-000000000002";

// The shell supplies the h1, landmarks and skip link; a page rendered alone is
// checked for everything else.
const shellRules = new Set(["page-has-heading-one", "landmark-unique", "region", "bypass"]);
const pageViolations = (container: HTMLElement) => checkAxeAA(container).filter((v) => !shellRules.has(v.id));

function project(overrides: Partial<Project> = {}): Project {
  return {
    id: "ES-1001",
    legalEntityId: LE,
    projectGroupId: "ENG",
    projectType: "FIXED_PRICE",
    customerPartyId: CUSTOMER,
    currency: "USD",
    contractValue: "500000.00",
    stage: "IN_PROCESS",
    startDate: "2026-09-01",
    version: 3,
    createdAt: "2026-09-01T09:00:00Z",
    updatedAt: "2026-09-02T09:00:00Z",
    ...overrides,
  };
}

type Reply = { status: number; body: unknown };
type Handler = (url: URL, init: RequestInit) => Reply | Promise<Reply>;

let handler: Handler;
let calls: { method: string; url: URL; headers: Headers; body: unknown }[];

beforeEach(() => {
  calls = [];
  vi.stubGlobal(
    "fetch",
    vi.fn(async (input: string, init: RequestInit = {}) => {
      const url = new URL(input, "http://localhost");
      calls.push({ method: init.method ?? "GET", url, headers: new Headers(init.headers), body: init.body ? JSON.parse(init.body as string) : undefined });
      const { status, body } = await handler(url, init);
      return new Response(JSON.stringify(body), { status, headers: { "Content-Type": "application/json" } });
    }),
  );
});

afterEach(() => {
  vi.unstubAllGlobals();
});

function Where() {
  const location = useLocation();
  return <p>At {location.pathname}</p>;
}

function renderAt(path: string) {
  return render(
    <MemoryRouter initialEntries={[path]}>
      <Routes>
        {projects.routes.map((route) => (
          <Route key={route.path} path={route.path} element={route.element} />
        ))}
        <Route path="*" element={<Where />} />
      </Routes>
    </MemoryRouter>,
  );
}

const problem = (status: number, code: string, detail: string, errors?: Record<string, string>): Reply => ({
  status,
  body: { type: `https://passage.dev/problems/${code}`, title: "Error", status, code, detail, ...(errors ? { errors } : {}) },
});

describe("projects module", () => {
  it("uses its folder name as id and declares a list, create and detail page", () => {
    expect(projects.id).toBe("projects");
    const paths = projects.routes.map((r) => r.path);
    for (const path of ["/projects", "/projects/list", "/projects/new", "/projects/:id"]) expect(paths).toContain(path);
    expect(projects.routes.every((r) => r.element !== null)).toBe(true);
  });

  it("gates nav by the service duties: inquiry for reading, ProjMaintain for creating", () => {
    expect(projects.nav.every((n) => n.to.startsWith("/projects"))).toBe(true);
    expect(projects.nav.find((n) => n.to === "/projects/list")?.duty).toBe("ProjInquire");
    expect(projects.nav.find((n) => n.to === "/projects/new")?.duty).toBe("ProjMaintain");
  });

  it("is auto-discovered by the registry", () => {
    expect(loadModules().map((m) => m.id)).toContain("projects");
  });
});

describe("projects home", () => {
  it("counts projects per stage, and shows 100+ when a stage has more than a page", async () => {
    handler = (url) => {
      const stage = url.searchParams.get("stage");
      if (stage === "IN_PROCESS") return { status: 200, body: { items: [project(), project({ id: "ES-1002" })], nextCursor: "abc" } };
      if (stage === "ON_HOLD") return { status: 200, body: { items: [project({ id: "ES-1003", stage: "ON_HOLD" })] } };
      return { status: 200, body: { items: [] } };
    };
    renderAt("/projects");
    const tiles = await screen.findByRole("list", { name: "Project tiles" });
    expect(tiles).toHaveTextContent("In process 2+");
    expect(tiles).toHaveTextContent("On hold 1");
    expect(tiles).toHaveTextContent("Created 0");
    expect(calls.map((c) => c.url.searchParams.get("limit"))).toEqual(["100", "100", "100", "100"]);
  });

  it("shows an empty state when there are no projects", async () => {
    handler = () => ({ status: 200, body: { items: [] } });
    renderAt("/projects");
    expect(await screen.findByRole("heading", { name: "No projects yet" })).toBeInTheDocument();
  });

  it("shows the failure and loads again on Try again", async () => {
    handler = () => problem(503, "COM-0500", "internal error");
    renderAt("/projects");
    expect(await screen.findByText("Could not load projects")).toBeInTheDocument();
    handler = () => ({ status: 200, body: { items: [project()] } });
    fireEvent.click(screen.getByRole("button", { name: "Try again" }));
    expect(await screen.findByRole("list", { name: "Project tiles" })).toBeInTheDocument();
  });
});

describe("projects list", () => {
  it("lists projects with plain-language type and stage, and opens one", async () => {
    const child = project({ id: "ES-1002", projectType: "INTERNAL", stage: "CREATED", parentProjectId: "ES-1001" });
    handler = (url) => (url.pathname === "/api/v1/projects/ES-1002" ? { status: 200, body: child } : { status: 200, body: { items: [project(), child] } });
    const { container } = renderAt("/projects/list");
    const table = await screen.findByRole("table", { name: "Projects" });
    await waitFor(() => expect(table).toHaveTextContent("ES-1002"));
    expect(table).toHaveTextContent("Fixed price");
    expect(table).toHaveTextContent("In process");
    expect(within(table).getAllByRole("row")[2]).toHaveTextContent("Internal");
    expect(pageViolations(container)).toEqual([]);
    fireEvent.click(within(table).getByRole("button", { name: "ES-1002" }));
    expect(await screen.findByRole("heading", { name: "Project ES-1002" })).toBeInTheDocument();
    expect(screen.getByRole("link", { name: "ES-1001" })).toHaveAttribute("href", "/projects/ES-1001");
  });

  it("sends the stage and type filters and explains an empty filtered result", async () => {
    handler = (url) => ({ status: 200, body: { items: url.searchParams.get("stage") === "ON_HOLD" ? [] : [project()] } });
    renderAt("/projects/list");
    await screen.findByRole("button", { name: "ES-1001" });
    fireEvent.change(screen.getByLabelText("Stage"), { target: { value: "ON_HOLD" } });
    fireEvent.change(screen.getByLabelText("Project type"), { target: { value: "COST" } });
    expect(await screen.findByText("No projects match these filters")).toBeInTheDocument();
    const last = calls[calls.length - 1]!;
    expect(last.url.searchParams.get("stage")).toBe("ON_HOLD");
    expect(last.url.searchParams.get("projectType")).toBe("COST");
  });

  it("loads the next page with the cursor", async () => {
    handler = (url) =>
      url.searchParams.get("cursor") === "next-1"
        ? { status: 200, body: { items: [project({ id: "ES-2000" })] } }
        : { status: 200, body: { items: [project()], nextCursor: "next-1" } };
    renderAt("/projects/list");
    fireEvent.click(await screen.findByRole("button", { name: "Load more" }));
    expect(await screen.findByRole("button", { name: "ES-2000" })).toBeInTheDocument();
    expect(screen.queryByRole("button", { name: "Load more" })).not.toBeInTheDocument();
  });

  it("shows an alert when the list cannot load", async () => {
    handler = () => problem(403, "AUTH-0003", "missing duty ProjInquire");
    renderAt("/projects/list");
    expect(await screen.findByText(/missing duty ProjInquire \(AUTH-0003\)/)).toBeInTheDocument();
    expect(screen.queryByRole("table", { name: "Projects" })).not.toBeInTheDocument();
  });
});

describe("project detail", () => {
  it("shows the header and moves the stage with the version as If-Match", async () => {
    handler = (_url, init) =>
      init.method === "POST" ? { status: 200, body: project({ stage: "ON_HOLD", version: 4 }) } : { status: 200, body: project() };
    const { container } = renderAt("/projects/ES-1001");
    expect(await screen.findByRole("heading", { name: "Project ES-1001" })).toBeInTheDocument();
    expect(screen.getByText(CUSTOMER)).toBeInTheDocument();
    expect(pageViolations(container)).toEqual([]);
    const actions = screen.getByRole("group", { name: "Stage actions" });
    expect(within(actions).getAllByRole("button").map((b) => b.textContent)).toEqual(["Put on hold", "Finish project"]);

    fireEvent.click(within(actions).getByRole("button", { name: "Put on hold" }));
    expect(await screen.findByText("Project ES-1001 is now on hold.")).toBeInTheDocument();
    const post = calls.find((c) => c.method === "POST")!;
    expect(post.url.pathname).toBe("/api/v1/projects/ES-1001/transitions");
    expect(post.headers.get("If-Match")).toBe('"3"');
    expect(post.body).toEqual({ to: "ON_HOLD" });
    expect(within(screen.getByRole("group", { name: "Stage actions" })).getByRole("button", { name: "Resume work" })).toBeInTheDocument();
  });

  it("explains a stale version and reloads the project", async () => {
    let version = 3;
    handler = (_url, init) =>
      init.method === "POST" ? problem(412, "conflict_version", "the project changed since it was read") : { status: 200, body: project({ version }) };
    renderAt("/projects/ES-1001");
    fireEvent.click(await screen.findByRole("button", { name: "Finish project" }));
    expect(await screen.findByText("Someone else changed this project")).toBeInTheDocument();
    version = 5;
    fireEvent.click(screen.getByRole("button", { name: "Reload project" }));
    await waitFor(() => expect(screen.queryByText("Someone else changed this project")).not.toBeInTheDocument());
    expect(await screen.findByText("5")).toBeInTheDocument();
  });

  it("shows a refused move with its code", async () => {
    handler = (_url, init) =>
      init.method === "POST" ? problem(422, "PROJ-STAGE-001", "Project cannot move from CREATED to FINISHED") : { status: 200, body: project({ stage: "CREATED", version: 1 }) };
    renderAt("/projects/ES-1001");
    fireEvent.click(await screen.findByRole("button", { name: "Start work" }));
    expect(await screen.findByText(/\(PROJ-STAGE-001\)/)).toBeInTheDocument();
  });

  it("offers no moves on a finished project", async () => {
    handler = () => ({ status: 200, body: project({ stage: "FINISHED" }) });
    renderAt("/projects/ES-1001");
    expect(await screen.findByText(/Finished projects cannot move/)).toBeInTheDocument();
    expect(screen.queryByRole("group", { name: "Stage actions" })).not.toBeInTheDocument();
  });

  it("shows not found for an unknown project", async () => {
    handler = () => problem(404, "PROJ-NOT-FOUND", "project not found");
    renderAt("/projects/NOPE");
    expect(await screen.findByRole("heading", { name: "Project not found" })).toBeInTheDocument();
    expect(screen.getByRole("link", { name: "Back to projects" })).toHaveAttribute("href", "/projects/list");
  });

  it("shows a load failure with Try again", async () => {
    handler = () => problem(500, "COM-0500", "internal error");
    renderAt("/projects/ES-1001");
    expect(await screen.findByText("Could not load the project")).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "Try again" })).toBeInTheDocument();
  });
});

describe("new project", () => {
  const base: ProjectValues = {
    id: "ES-1001",
    legalEntityId: LE,
    parentProjectId: "",
    projectGroupId: "ENG",
    projectType: "FIXED_PRICE",
    customerPartyId: CUSTOMER,
    contractId: "",
    currency: "USD",
    startDate: "2026-10-01",
    endDate: "",
  };

  it("validates like the service", () => {
    expect(validateProject(base).input).toEqual({
      id: "ES-1001",
      legalEntityId: LE,
      projectGroupId: "ENG",
      projectType: "FIXED_PRICE",
      customerPartyId: CUSTOMER,
      currency: "USD",
      startDate: "2026-10-01",
    });
    expect(validateProject({ ...base, customerPartyId: "" }).errors.customerPartyId).toMatch(/bills a customer/);
    expect(validateProject({ ...base, projectType: "INTERNAL", customerPartyId: "" }).input).toBeDefined();
    expect(validateProject({ ...base, id: "es 1" }).errors.id).toBeDefined();
    expect(validateProject({ ...base, endDate: "2026-09-01" }).errors.endDate).toMatch(/before the start/);
    expect(validateProject({ ...base, projectGroupId: "" }).errors.projectGroupId).toBeDefined();
  });

  it("lets a sub-project inherit group and customer", () => {
    const sub = validateProject({ ...base, id: "ES-1001-01", parentProjectId: "ES-1001", projectGroupId: "", customerPartyId: "" });
    expect(sub.errors).toEqual({});
    expect(sub.input).toEqual({ id: "ES-1001-01", legalEntityId: LE, parentProjectId: "ES-1001", projectType: "FIXED_PRICE", currency: "USD", startDate: "2026-10-01" });
  });

  it("creates the project with an Idempotency-Key and opens it", async () => {
    handler = (_url, init) => (init.method === "POST" ? { status: 201, body: project({ id: "ES-3000", stage: "CREATED", version: 1 }) } : { status: 200, body: project({ id: "ES-3000", stage: "CREATED", version: 1 }) });
    const { container } = renderAt("/projects/new");
    expect(pageViolations(container)).toEqual([]);
    fireEvent.change(screen.getByLabelText("Project id"), { target: { value: "es-3000" } });
    fireEvent.change(screen.getByLabelText("Legal entity"), { target: { value: LE } });
    fireEvent.change(screen.getByLabelText("Project group"), { target: { value: "ENG" } });
    fireEvent.change(screen.getByLabelText("Customer"), { target: { value: CUSTOMER } });
    fireEvent.change(screen.getByLabelText("Start date"), { target: { value: "2026-11-02" } });
    fireEvent.click(screen.getByRole("button", { name: "Create project" }));
    expect(await screen.findByRole("heading", { name: "Project ES-3000" })).toBeInTheDocument();
    const post = calls.find((c) => c.method === "POST")!;
    expect(post.url.pathname).toBe("/api/v1/projects");
    expect(post.headers.get("Idempotency-Key")).toMatch(/^[0-9a-f-]{36}$/);
    expect(post.body).toEqual({ id: "ES-3000", legalEntityId: LE, projectGroupId: "ENG", projectType: "TIME_MATERIAL", customerPartyId: CUSTOMER, currency: "USD", startDate: "2026-11-02" });
  });

  it("does not call the service while the form is invalid", async () => {
    handler = () => ({ status: 500, body: {} });
    renderAt("/projects/new");
    fireEvent.click(screen.getByRole("button", { name: "Create project" }));
    expect(await screen.findByText(/Use 1 to 20 capital letters/)).toBeInTheDocument();
    expect(screen.getByText(/Enter the legal entity's UUID/)).toBeInTheDocument();
    expect(calls).toHaveLength(0);
  });

  it("prefills a sub-project from the parent and shows a parent conflict", async () => {
    handler = () => problem(422, "PROJ-PARENT-002", "Sub-project ES-1001-02 cannot change the customer of its parent ES-1001");
    renderAt(`/projects/new?parent=ES-1001&legalEntity=${LE}`);
    expect(screen.getByRole("heading", { name: "New sub-project" })).toBeInTheDocument();
    expect(screen.getByLabelText("Parent project")).toHaveValue("ES-1001");
    expect(screen.getByLabelText("Legal entity")).toHaveValue(LE);
    fireEvent.change(screen.getByLabelText("Project id"), { target: { value: "ES-1001-02" } });
    fireEvent.change(screen.getByLabelText("Customer"), { target: { value: "33333333-0000-4000-8000-000000000003" } });
    fireEvent.click(screen.getByRole("button", { name: "Create project" }));
    expect(await screen.findByText("The sub-project conflicts with its parent")).toBeInTheDocument();
    expect(calls[0]!.body).toMatchObject({ parentProjectId: "ES-1001" });
    expect(calls[0]!.body).not.toHaveProperty("projectGroupId");
  });

  it("puts server field errors next to their fields", async () => {
    handler = () => problem(400, "COM-0001", "request failed validation", { currency: "must be an ISO 4217 alpha-3 code in capitals" });
    renderAt("/projects/new");
    fireEvent.change(screen.getByLabelText("Project id"), { target: { value: "ES-4000" } });
    fireEvent.change(screen.getByLabelText("Legal entity"), { target: { value: LE } });
    fireEvent.change(screen.getByLabelText("Project group"), { target: { value: "ENG" } });
    fireEvent.change(screen.getByLabelText("Project type"), { target: { value: "INTERNAL" } });
    fireEvent.click(screen.getByRole("button", { name: "Create project" }));
    expect(await screen.findByText("must be an ISO 4217 alpha-3 code in capitals")).toBeInTheDocument();
  });
});
