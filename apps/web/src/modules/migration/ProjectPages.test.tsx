import { fireEvent, render, screen, waitFor, within } from "@testing-library/react";
import { MemoryRouter, Route, Routes } from "react-router-dom";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { checkAxeAA } from "../../shell/a11y";
import { PrincipalProvider } from "../../shell/principal";
import { findTable } from "../../test-utils";
import type { KeyRewriteRules, MigrationProject } from "./api/client";
import { checkRewriteRules, cutoverInput, parseProjectEdit, parseProjectForm, type ProjectForm } from "./domain";
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

function renderAt(path: string, duties: string[] = ["migration.run", "migration.project.write"]) {
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

const PROJECT = "0195f0a0-0000-7000-8000-0000000000d1";
const LE_A = "0195f0a0-0000-7000-8000-0000000000e1";
const LE_B = "0195f0a0-0000-7000-8000-0000000000e2";
const copyProject: MigrationProject = {
  id: PROJECT,
  name: "USMF to CAMF",
  projectType: "copy",
  sourceLegalEntityId: LE_A,
  targetLegalEntityId: LE_B,
  status: "active",
  version: 1,
  createdBy: "u1",
  createdAt: "2026-10-30T09:00:00Z",
  updatedAt: "2026-10-30T09:00:00Z",
};
const axProject: MigrationProject = { ...copyProject, id: "0195f0a0-0000-7000-8000-0000000000d2", name: "AX customers", projectType: "migrate", sourceSystem: "ax2012", sourceLegalEntityId: undefined, targetLegalEntityId: undefined, cutoverAt: "2027-03-01T04:00:00Z" };
const noRules: KeyRewriteRules = { version: 1, items: [] };

describe("project form parsing", () => {
  const base: ProjectForm = { name: " USMF to CAMF ", projectType: "copy", sourceSystem: "", sourceLegalEntityId: LE_A, targetLegalEntityId: LE_B, cutoverAt: "", scheduleCron: "", scheduleTimezone: "" };
  it("trims and accepts a copy project", () => {
    expect(parseProjectForm(base).value).toEqual({ name: "USMF to CAMF", projectType: "copy", sourceLegalEntityId: LE_A, targetLegalEntityId: LE_B });
  });
  it("converts the cutover to UTC and keeps the schedule", () => {
    const { value } = parseProjectForm({ ...base, cutoverAt: "2027-03-01T05:00", scheduleCron: " 0 2 * * * " });
    expect(value?.cutoverAt).toBe("2027-03-01T05:00:00.000Z");
    expect(value?.scheduleCron).toBe("0 2 * * *");
  });
  it("reports each field", () => {
    expect(Object.keys(parseProjectForm({ ...base, name: "", sourceLegalEntityId: "x", targetLegalEntityId: "y" }).errors).sort()).toEqual(["name", "sourceLegalEntityId", "targetLegalEntityId"]);
    expect(parseProjectForm({ ...base, targetLegalEntityId: LE_A.toUpperCase() }).errors.targetLegalEntityId).toMatch(/differ/);
    expect(parseProjectForm({ ...base, projectType: "migrate" }).errors.sourceSystem).toBeTruthy();
    expect(parseProjectForm({ ...base, projectType: "migrate", sourceSystem: "ax2012" }).value?.sourceSystem).toBe("ax2012");
    expect(parseProjectForm({ ...base, scheduleCron: "* *" }).errors.scheduleCron).toBeTruthy();
    expect(parseProjectForm({ ...base, name: "x".repeat(81) }).errors.name).toMatch(/80/);
    expect(parseProjectForm({ ...base, projectType: "config_package" }).value).toEqual({ name: "USMF to CAMF", projectType: "config_package" });
  });
  it("reads the schedule in an IANA time zone, UTC when empty", () => {
    expect(parseProjectForm({ ...base, scheduleCron: "30 2 * * *", scheduleTimezone: " Europe/Berlin " }).value?.scheduleTimezone).toBe("Europe/Berlin");
    expect(parseProjectForm(base).value).not.toHaveProperty("scheduleTimezone");
    expect(parseProjectForm({ ...base, scheduleTimezone: "Mars/Base" }).errors.scheduleTimezone).toBeTruthy();
    expect(parseProjectForm({ ...base, scheduleTimezone: "Local" }).errors.scheduleTimezone).toBeTruthy();
    const current = { ...axProject, scheduleCron: "0 2 * * *" };
    const form = { name: current.name, cutoverAt: cutoverInput(current.cutoverAt), scheduleCron: "0 2 * * *", scheduleTimezone: "" };
    expect(parseProjectEdit(form, current).value).toEqual({});
    expect(parseProjectEdit({ ...form, scheduleTimezone: "America/Chicago" }, current).value).toEqual({ scheduleTimezone: "America/Chicago" });
    expect(parseProjectEdit({ ...form, scheduleTimezone: "Nowhere/City" }, current).errors.scheduleTimezone).toBeTruthy();
    expect(parseProjectEdit({ ...form, scheduleTimezone: "" }, { ...current, scheduleTimezone: "America/Chicago" }).value).toEqual({ scheduleTimezone: "" });
  });
  it("checks rewrite rules", () => {
    const rules = [
      { field: "@key", from: "A", to: "B" },
      { field: "@key", from: "A", to: "C" },
      { field: "", from: "A", to: "B" },
      { field: "x", from: "", to: "B" },
      { field: "x", from: "Q", to: "Q" },
    ];
    const problems = checkRewriteRules(rules);
    expect(problems[0]).toBeUndefined();
    expect(problems.slice(1).every(Boolean)).toBe(true);
  });
  it("refuses a rule an earlier, shorter prefix always wins over, but not the other way round", () => {
    expect(checkRewriteRules([{ field: "@key", from: "US", to: "CA" }, { field: "dataAreaId", from: "USMF", to: "CAMF" }, { field: "@key", from: "USMF", to: "CAMF" }])[2]).toMatch(/Rule 1 already matches/);
    expect(checkRewriteRules([{ field: "@key", from: "USMF", to: "CAMF" }, { field: "@key", from: "US", to: "CA" }]).every((p) => p === undefined)).toBe(true);
  });
  it("sends only the changed details, and clears an emptied schedule", () => {
    const current = { ...axProject, scheduleCron: "0 2 * * *" };
    const unchanged = { name: current.name, cutoverAt: cutoverInput(current.cutoverAt), scheduleCron: current.scheduleCron, scheduleTimezone: "" };
    expect(unchanged.cutoverAt).toBe("2027-03-01T04:00");
    expect(parseProjectEdit(unchanged, current).value).toEqual({});
    expect(parseProjectEdit({ ...unchanged, name: " AX vendors ", scheduleCron: "", cutoverAt: "" }, current).value).toEqual({ name: "AX vendors", scheduleCron: "", cutoverAt: "" });
    expect(parseProjectEdit({ ...unchanged, cutoverAt: "2027-04-01T06:30" }, current).value).toEqual({ cutoverAt: "2027-04-01T06:30:00.000Z" });
    expect(Object.keys(parseProjectEdit({ ...unchanged, name: " ", scheduleCron: "0 2" }, current).errors).sort()).toEqual(["name", "scheduleCron"]);
    expect(cutoverInput("2027-03-01T04:00:30Z")).toBe("2027-03-01T04:00:30");
  });
});

describe("projects list", () => {
  it("shows projects, filters by type, and opens one", async () => {
    handler = () => json(200, { items: [copyProject, axProject] });
    const { container } = renderAt("/migration/projects");
    const table = await findTable({ name: "Migration projects" });
    expect(table).toHaveTextContent("USMF to CAMF");
    expect(table).toHaveTextContent("Dynamics AX 2012");
    expect(violations(container)).toEqual([]);
    fireEvent.change(screen.getByLabelText("Type"), { target: { value: "copy" } });
    await waitFor(() => expect(calls.some((c) => c.url.searchParams.get("projectType") === "copy")).toBe(true));
    expect(screen.getByRole("link", { name: "New project" })).toBeInTheDocument();
  });

  it("loads the next page with the cursor", async () => {
    handler = (url) => (url.searchParams.get("cursor") ? json(200, { items: [axProject] }) : json(200, { items: [copyProject], nextCursor: "c1" }));
    renderAt("/migration/projects");
    await findTable({ name: "Migration projects" });
    fireEvent.click(await screen.findByRole("button", { name: "Load more" }));
    await waitFor(() => expect(screen.getByRole("table")).toHaveTextContent("AX customers"));
    expect(screen.getByRole("table")).toHaveTextContent("USMF to CAMF");
    expect(screen.queryByRole("button", { name: "Load more" })).not.toBeInTheDocument();
  });

  it("shows the empty state and hides New project without the duty", async () => {
    handler = () => json(200, { items: [] });
    renderAt("/migration/projects", ["migration.run"]);
    expect(await screen.findByText("No migration projects yet")).toBeInTheDocument();
    expect(screen.queryByRole("link", { name: "New project" })).not.toBeInTheDocument();
  });

  it("shows the error state and retries", async () => {
    handler = () => json(403, { code: "AUTH-0003", detail: "missing duty" });
    renderAt("/migration/projects");
    expect(await screen.findByText(/missing duty/)).toBeInTheDocument();
    handler = () => json(200, { items: [copyProject] });
    fireEvent.click(screen.getByRole("button", { name: "Try again" }));
    expect(await findTable({ name: "Migration projects" })).toHaveTextContent("USMF to CAMF");
  });
});

describe("new project", () => {
  it("validates locally, then posts with an idempotency key and opens the project", async () => {
    handler = (url, init) => {
      if (init.method === "POST") return json(201, copyProject);
      if (url.pathname.endsWith("/key-rewrite-rules")) return json(200, noRules);
      return json(200, copyProject);
    };
    const { container } = renderAt("/migration/projects/new");
    expect(violations(container)).toEqual([]);
    fireEvent.click(screen.getByRole("button", { name: "Create project" }));
    expect(await screen.findByText("Enter a project name.")).toBeInTheDocument();
    expect(calls).toHaveLength(0);
    fireEvent.change(screen.getByLabelText("Name"), { target: { value: "USMF to CAMF" } });
    fireEvent.change(screen.getByLabelText("Source legal entity"), { target: { value: LE_A } });
    fireEvent.change(screen.getByLabelText("Target legal entity"), { target: { value: LE_B } });
    fireEvent.click(screen.getByRole("button", { name: "Create project" }));
    expect(await screen.findByRole("heading", { name: "USMF to CAMF" })).toBeInTheDocument();
    const post = calls.find((c) => c.method === "POST");
    expect(post?.body).toEqual({ name: "USMF to CAMF", projectType: "copy", sourceLegalEntityId: LE_A, targetLegalEntityId: LE_B });
    expect(new Headers(post?.init.headers).get("Idempotency-Key")).toBeTruthy();
  });

  it("asks for a source system on a migrate project and shows the service problem", async () => {
    handler = () => json(409, { code: "MIG-PROJECT-NAME-EXISTS", detail: "a migration project with this name already exists" });
    renderAt("/migration/projects/new");
    fireEvent.change(screen.getByLabelText("Type"), { target: { value: "migrate" } });
    expect(screen.queryByLabelText("Source legal entity")).not.toBeInTheDocument();
    fireEvent.change(screen.getByLabelText("Name"), { target: { value: "AX customers" } });
    fireEvent.click(screen.getByRole("button", { name: "Create project" }));
    expect(await screen.findByText("Choose the source system.")).toBeInTheDocument();
    fireEvent.change(screen.getByLabelText("Source system"), { target: { value: "ax2012" } });
    fireEvent.click(screen.getByRole("button", { name: "Create project" }));
    expect(await screen.findByText(/already exists/)).toBeInTheDocument();
  });
});

describe("new project with a custom source system", () => {
  it("builds custom_<name>, offers only matching connections and posts them with the target legal entity", async () => {
    const acme = { id: "0195f0a0-0000-7000-8000-0000000000c1", name: "Acme ERP", kind: "sql", sourceSystem: "custom_acme", config: {}, secretSet: true, status: "active", version: 1, createdBy: "u", createdAt: "2027-01-01T00:00:00Z", updatedAt: "2027-01-01T00:00:00Z" };
    const other = { ...acme, id: "0195f0a0-0000-7000-8000-0000000000c2", name: "AX box", sourceSystem: "ax2012" };
    handler = (url, init) => {
      if (init.method === "POST") return json(201, { ...axProject, sourceSystem: "custom_acme", connectionProfileId: acme.id });
      if (url.pathname.endsWith("/connection-profiles")) return json(200, { items: [acme, other] });
      return json(200, axProject);
    };
    renderAt("/migration/projects/new");
    fireEvent.change(screen.getByLabelText("Type"), { target: { value: "migrate" } });
    fireEvent.change(screen.getByLabelText("Name"), { target: { value: "Acme migration" } });
    fireEvent.change(screen.getByLabelText("Source system"), { target: { value: "custom" } });
    fireEvent.click(screen.getByRole("button", { name: "Create project" }));
    expect(await screen.findByText(/Name the custom system/)).toBeInTheDocument();
    expect(calls.some((c) => c.method === "POST")).toBe(false);
    fireEvent.change(screen.getByLabelText("Custom system name"), { target: { value: "Acme" } });
    await screen.findByRole("option", { name: "Acme ERP" });
    expect(screen.queryByRole("option", { name: "AX box" })).not.toBeInTheDocument();
    fireEvent.change(screen.getByLabelText(/^Connection/), { target: { value: acme.id } });
    fireEvent.change(screen.getByLabelText(/^Target legal entity/), { target: { value: LE_B } });
    fireEvent.click(screen.getByRole("button", { name: "Create project" }));
    await waitFor(() => expect(calls.some((c) => c.method === "POST")).toBe(true));
    expect(calls.find((c) => c.method === "POST")?.body).toEqual({
      name: "Acme migration",
      projectType: "migrate",
      sourceSystem: "custom_acme",
      targetLegalEntityId: LE_B,
      connectionProfileId: acme.id,
    });
  });
});

describe("project detail", () => {
  it("shows a migrate project without a rules card", async () => {
    handler = () => json(200, axProject);
    renderAt(`/migration/projects/${axProject.id}`);
    expect(await screen.findByRole("heading", { name: "AX customers" })).toBeInTheDocument();
    expect(screen.queryByText("Key rewrite rules")).not.toBeInTheDocument();
    expect(calls.every((c) => !c.url.pathname.includes("key-rewrite-rules"))).toBe(true);
  });

  it("shows a not-found error", async () => {
    handler = () => json(404, { code: "AUTH-0404", detail: "migration project not found" });
    renderAt(`/migration/projects/${PROJECT}`);
    expect(await screen.findByText(/migration project not found/)).toBeInTheDocument();
  });

  it("archives with the current version", async () => {
    handler = (url, init) => {
      if (init.method === "PATCH") return json(200, { ...copyProject, status: "archived", version: 2 });
      if (url.pathname.endsWith("/key-rewrite-rules")) return json(200, noRules);
      return json(200, copyProject);
    };
    renderAt(`/migration/projects/${PROJECT}`);
    fireEvent.click(await screen.findByRole("button", { name: "Archive project" }));
    expect(await screen.findByRole("button", { name: "Reactivate project" })).toBeInTheDocument();
    expect(calls.find((c) => c.method === "PATCH")?.body).toEqual({ version: 1, status: "archived" });
  });

  it("edits the name and schedule with the current version", async () => {
    handler = (_url, init) => {
      if (init.method === "PATCH") return json(200, { ...axProject, name: "AX vendors", scheduleCron: "0 3 * * *", version: 2 });
      return json(200, axProject);
    };
    const { container } = renderAt(`/migration/projects/${axProject.id}`);
    fireEvent.click(await screen.findByRole("button", { name: "Edit details" }));
    fireEvent.change(screen.getByLabelText("Name"), { target: { value: "AX vendors" } });
    fireEvent.change(screen.getByLabelText(/^Schedule/), { target: { value: "0 3 * * *" } });
    expect(violations(container)).toEqual([]);
    fireEvent.click(screen.getByRole("button", { name: "Save details" }));
    expect(await screen.findByRole("heading", { name: "AX vendors" })).toBeInTheDocument();
    expect(screen.getByText("0 3 * * *")).toBeInTheDocument();
    expect(calls.find((c) => c.method === "PATCH")?.body).toEqual({ version: 1, name: "AX vendors", scheduleCron: "0 3 * * *" });
  });

  it("shows the service's field message when an edit is refused", async () => {
    handler = (_url, init) =>
      init.method === "PATCH" ? json(400, { code: "COM-0422", detail: "validation failed", errors: { scheduleCron: "must leave at least 15 minutes between runs" } }) : json(200, axProject);
    renderAt(`/migration/projects/${axProject.id}`);
    fireEvent.click(await screen.findByRole("button", { name: "Edit details" }));
    fireEvent.change(screen.getByLabelText(/^Schedule/), { target: { value: "*/5 * * * *" } });
    fireEvent.click(screen.getByRole("button", { name: "Save details" }));
    expect(await screen.findByText("must leave at least 15 minutes between runs")).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "Save details" })).toBeInTheDocument();
  });

  it("freezes an archived project's details and rules", async () => {
    const archived = { ...copyProject, status: "archived" as const };
    handler = (url) => (url.pathname.endsWith("/key-rewrite-rules") ? json(200, { version: 1, items: [{ field: "@key", from: "USMF", to: "CAMF" }] }) : json(200, archived));
    renderAt(`/migration/projects/${PROJECT}`);
    expect(await screen.findByLabelText("Rule 1 field")).toBeDisabled();
    expect(screen.getByText(/reactivate it to change its rules/)).toBeInTheDocument();
    expect(screen.queryByRole("button", { name: "Edit details" })).not.toBeInTheDocument();
    expect(screen.queryByRole("button", { name: "Save rules" })).not.toBeInTheDocument();
    expect(screen.getByRole("button", { name: "Reactivate project" })).toBeInTheDocument();
  });

  it("hides the write actions without the duty", async () => {
    handler = (url) => (url.pathname.endsWith("/key-rewrite-rules") ? json(200, { version: 1, items: [{ field: "@key", from: "USMF", to: "CAMF" }] }) : json(200, copyProject));
    renderAt(`/migration/projects/${PROJECT}`, ["migration.run"]);
    expect(await screen.findByLabelText("Rule 1 field")).toBeDisabled();
    expect(screen.queryByRole("button", { name: "Archive project" })).not.toBeInTheDocument();
    expect(screen.queryByRole("button", { name: "Save rules" })).not.toBeInTheDocument();
    expect(screen.queryByRole("button", { name: "Add rule" })).not.toBeInTheDocument();
    expect(screen.queryByRole("button", { name: "Edit details" })).not.toBeInTheDocument();
  });
});

describe("project runs", () => {
  const SPEC = "0195f0a0-0000-7000-8000-0000000000b1";
  const projectRun = {
    id: "0195f0a0-0000-7000-8000-0000000000f1",
    sourceSystem: "ax2012",
    mode: "rehearsal",
    entity: "customer",
    status: "partial",
    dryRun: false,
    mappingVersion: 1,
    projectId: axProject.id,
    counts: { extracted: 3, mapped: 2, created: 2, updated: 0, unchanged: 0, rejected: 1, warned: 0 },
    rejects: [],
    warnings: [],
    absent: [],
    createdAt: "2026-10-30T09:00:00Z",
  };

  it("lists the runs under a migrate project and offers Start run", async () => {
    handler = (url) => (url.pathname.endsWith("/migration-runs") ? json(200, { items: [projectRun] }) : json(200, axProject));
    renderAt(`/migration/projects/${axProject.id}`);
    const table = await findTable({ name: "Project runs" });
    expect(within(table).getByText("customer")).toBeInTheDocument();
    expect(calls.some((c) => c.url.pathname.endsWith("/migration-runs") && c.url.searchParams.get("projectId") === axProject.id)).toBe(true);
    expect(screen.getByRole("link", { name: "Start run" })).toHaveAttribute("href", `/migration/runs/new?project=${axProject.id}`);
  });

  it("pages the project's runs with Load more and keeps the project filter on the next page", async () => {
    const older = { ...projectRun, id: "0195f0a0-0000-7000-8000-0000000000a9", entity: "vendor" };
    handler = (url) => {
      if (!url.pathname.endsWith("/migration-runs")) return json(200, axProject);
      return url.searchParams.get("cursor") === "p1" ? json(200, { items: [older] }) : json(200, { items: [projectRun], nextCursor: "p1" });
    };
    renderAt(`/migration/projects/${axProject.id}`);
    await findTable({ name: "Project runs" });
    fireEvent.click(await screen.findByRole("button", { name: "Load more" }));
    await waitFor(() => expect(screen.getByRole("table", { name: "Project runs" })).toHaveTextContent("vendor"));
    const next = calls.find((c) => c.url.searchParams.get("cursor") === "p1");
    expect(next?.url.searchParams.get("projectId")).toBe(axProject.id);
    expect(screen.queryByRole("button", { name: "Load more" })).not.toBeInTheDocument();
  });

  it("shows the empty and error states, and hides Start run for an archived project or without the duty", async () => {
    handler = (url) => (url.pathname.endsWith("/migration-runs") ? json(200, { items: [] }) : json(200, { ...axProject, status: "archived" }));
    renderAt(`/migration/projects/${axProject.id}`);
    expect(await screen.findByText("No runs under this project yet")).toBeInTheDocument();
    expect(screen.queryByRole("link", { name: "Start run" })).not.toBeInTheDocument();
  });

  it("shows an error when the runs cannot be loaded", async () => {
    handler = (url) => (url.pathname.endsWith("/migration-runs") ? json(500, { code: "COM-0500", detail: "boom" }) : json(200, axProject));
    renderAt(`/migration/projects/${axProject.id}`);
    expect(await screen.findByText(/boom/)).toBeInTheDocument();
  });

  it("starts a run under the preselected project with the project's source system", async () => {
    handler = (url, init) => {
      if (init.method === "POST") return json(202, { run: projectRun });
      if (url.pathname.endsWith("/mapping-specs")) return json(200, { items: [{ id: SPEC, entity: "customer", version: 1, checksum: "ab", fieldCount: 2, testCount: 1, createdAt: "2026-10-30T09:00:00Z" }] });
      if (url.pathname.endsWith("/data-templates")) return json(200, { items: [] });
      if (url.pathname.endsWith("/projects")) return json(200, { items: [{ ...axProject, sourceSystem: "sap_s4" }] });
      return json(200, projectRun);
    };
    renderAt(`/migration/runs/new?project=${axProject.id}`);
    await screen.findByRole("option", { name: "customer v1" });
    await screen.findByRole("option", { name: "AX customers" });
    expect(screen.getByLabelText("Project (optional)")).toHaveValue(axProject.id);
    expect(screen.getByLabelText("Source system")).toBeDisabled();
    fireEvent.change(screen.getByLabelText("Mapping spec"), { target: { value: SPEC } });
    fireEvent.click(screen.getByRole("button", { name: "Start run" }));
    await screen.findByText("its migration project");
    expect(calls.find((c) => c.method === "POST")?.body).toMatchObject({ projectId: axProject.id, sourceSystem: "sap_s4", mappingSpecId: SPEC });
  });

  it("shows the service's refusal when the project already has an active run", async () => {
    handler = (url, init) => {
      if (init.method === "POST") return json(409, { code: "MIG-RUN-ACTIVE", detail: "Run 0195f0a0-0000-7000-8000-0000000000f9 is already active for this project; wait for it to finish" });
      if (url.pathname.endsWith("/mapping-specs")) return json(200, { items: [{ id: SPEC, entity: "customer", version: 1, checksum: "ab", fieldCount: 2, testCount: 1, createdAt: "2026-10-30T09:00:00Z" }] });
      if (url.pathname.endsWith("/data-templates")) return json(200, { items: [] });
      if (url.pathname.endsWith("/projects")) return json(200, { items: [axProject] });
      return json(404, {});
    };
    renderAt(`/migration/runs/new?project=${axProject.id}`);
    await screen.findByRole("option", { name: "AX customers" });
    fireEvent.change(await screen.findByLabelText("Mapping spec"), { target: { value: SPEC } });
    fireEvent.click(screen.getByRole("button", { name: "Start run" }));
    expect(await screen.findByText(/already active for this project/)).toBeInTheDocument();
  });

  it("still starts a run without a project when projects cannot be loaded", async () => {
    handler = (url, init) => {
      if (init.method === "POST") return json(202, { run: { ...projectRun, projectId: undefined } });
      if (url.pathname.endsWith("/mapping-specs")) return json(200, { items: [{ id: SPEC, entity: "customer", version: 1, checksum: "ab", fieldCount: 2, testCount: 1, createdAt: "2026-10-30T09:00:00Z" }] });
      if (url.pathname.endsWith("/data-templates")) return json(200, { items: [] });
      if (url.pathname.endsWith("/projects")) return json(500, { code: "COM-0500", detail: "boom" });
      return json(200, { ...projectRun, projectId: undefined });
    };
    renderAt("/migration/runs/new");
    await screen.findByText(/Projects could not be loaded/);
    fireEvent.change(await screen.findByLabelText("Mapping spec"), { target: { value: SPEC } });
    fireEvent.click(screen.getByRole("button", { name: "Start run" }));
    await waitFor(() => expect(calls.find((c) => c.method === "POST")).toBeDefined());
    expect(calls.find((c) => c.method === "POST")?.body).not.toHaveProperty("projectId");
  });

  it("keeps the project from the link when the project list cannot be loaded", async () => {
    handler = (url, init) => {
      if (init.method === "POST") return json(202, { run: projectRun });
      if (url.pathname.endsWith("/mapping-specs")) return json(200, { items: [{ id: SPEC, entity: "customer", version: 1, checksum: "ab", fieldCount: 2, testCount: 1, createdAt: "2026-10-30T09:00:00Z" }] });
      if (url.pathname.endsWith("/data-templates")) return json(200, { items: [] });
      if (url.pathname.endsWith("/projects")) return json(500, { code: "COM-0500", detail: "boom" });
      return json(200, projectRun);
    };
    renderAt(`/migration/runs/new?project=${axProject.id}`);
    expect(await screen.findByText(/the run starts under the project from the link/)).toBeInTheDocument();
    expect(screen.getByLabelText("Project (optional)")).toHaveValue(axProject.id);
    fireEvent.change(await screen.findByLabelText("Mapping spec"), { target: { value: SPEC } });
    fireEvent.click(screen.getByRole("button", { name: "Start run" }));
    await waitFor(() => expect(calls.find((c) => c.method === "POST")).toBeDefined());
    expect(calls.find((c) => c.method === "POST")?.body).toMatchObject({ projectId: axProject.id });
  });
});

describe("key rewrite rules", () => {
  function rulesHandler(stored: KeyRewriteRules) {
    handler = (url, init) => {
      if (url.pathname.endsWith(":preview")) return json(200, { items: [{ key: "USMF-C1", rewritten: "CAMF-C1", changed: true }, { key: "DE-C2", rewritten: "DE-C2", changed: false }] });
      if (url.pathname.endsWith("/key-rewrite-rules")) {
        if (init.method === "PUT") return json(200, { version: 2, items: (JSON.parse(String(init.body)) as { rules: unknown[] }).rules });
        return json(200, stored);
      }
      return json(200, copyProject);
    };
  }

  it("shows the empty state, adds a rule and saves it with the version", async () => {
    rulesHandler(noRules);
    const { container } = renderAt(`/migration/projects/${PROJECT}`);
    expect(await screen.findByText(/No rules yet/)).toBeInTheDocument();
    fireEvent.click(screen.getByRole("button", { name: "Add rule" }));
    fireEvent.change(screen.getByLabelText("Rule 1 replace prefix"), { target: { value: "USMF" } });
    fireEvent.change(screen.getByLabelText("Rule 1 with prefix"), { target: { value: "CAMF" } });
    expect(violations(container)).toEqual([]);
    fireEvent.click(screen.getByRole("button", { name: "Save rules" }));
    expect(await screen.findByText("Rules saved")).toBeInTheDocument();
    expect(calls.find((c) => c.method === "PUT")?.body).toEqual({ version: 1, rules: [{ field: "@key", from: "USMF", to: "CAMF" }] });
  });

  it("blocks an invalid rule locally", async () => {
    rulesHandler(noRules);
    renderAt(`/migration/projects/${PROJECT}`);
    await screen.findByText(/No rules yet/);
    fireEvent.click(screen.getByRole("button", { name: "Add rule" }));
    fireEvent.click(screen.getByRole("button", { name: "Save rules" }));
    expect(await screen.findByText("Enter the prefix to replace.")).toBeInTheDocument();
    expect(calls.some((c) => c.method === "PUT")).toBe(false);
  });

  it("removes a rule", async () => {
    rulesHandler({ version: 1, items: [{ field: "@key", from: "USMF", to: "CAMF" }, { field: "dataAreaId", from: "USMF", to: "CAMF" }] });
    renderAt(`/migration/projects/${PROJECT}`);
    await screen.findByLabelText("Rule 2 field");
    fireEvent.click(screen.getByRole("button", { name: "Remove rule 1" }));
    expect(screen.queryByLabelText("Rule 2 field")).not.toBeInTheDocument();
    expect(screen.getByLabelText("Rule 1 field")).toHaveValue("dataAreaId");
  });

  it("previews sample keys with the unsaved rules", async () => {
    rulesHandler({ version: 1, items: [{ field: "@key", from: "USMF", to: "CAMF" }] });
    renderAt(`/migration/projects/${PROJECT}`);
    await screen.findByLabelText("Rule 1 field");
    fireEvent.click(screen.getByRole("button", { name: "Preview" }));
    expect(await screen.findByText(/Enter at least one sample key/)).toBeInTheDocument();
    fireEvent.change(screen.getByLabelText("Sample keys (one per line)"), { target: { value: "USMF-C1\n\nDE-C2\n" } });
    fireEvent.change(screen.getByLabelText("Rule 1 with prefix"), { target: { value: "MXMF" } });
    fireEvent.click(screen.getByRole("button", { name: "Preview" }));
    const table = await findTable({ name: "Rewrite preview" });
    expect(within(table).getByText("CAMF-C1")).toBeInTheDocument();
    expect(calls.find((c) => c.url.pathname.endsWith(":preview"))?.body).toEqual({ keys: ["USMF-C1", "DE-C2"], rules: [{ field: "@key", from: "USMF", to: "MXMF" }] });
  });

  it("offers a reload after a version conflict", async () => {
    handler = (url, init) => {
      if (url.pathname.endsWith("/key-rewrite-rules")) return init.method === "PUT" ? json(409, { code: "MIG-VERSION-CONFLICT", detail: "current version is 3" }) : json(200, noRules);
      return json(200, copyProject);
    };
    renderAt(`/migration/projects/${PROJECT}`);
    await screen.findByText(/No rules yet/);
    fireEvent.click(screen.getByRole("button", { name: "Add rule" }));
    fireEvent.change(screen.getByLabelText("Rule 1 replace prefix"), { target: { value: "A" } });
    fireEvent.click(screen.getByRole("button", { name: "Save rules" }));
    expect(await screen.findByText(/current version is 3/)).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "Reload rules" })).toBeInTheDocument();
  });

  it("shows the error state for the rules", async () => {
    handler = (url) => (url.pathname.endsWith("/key-rewrite-rules") ? json(500, { code: "COM-0500", detail: "internal error" }) : json(200, copyProject));
    renderAt(`/migration/projects/${PROJECT}`);
    expect(await screen.findByText(/internal error/)).toBeInTheDocument();
  });
});
