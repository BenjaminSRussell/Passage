import { findTable } from "../../test-utils";
import { fireEvent, render, screen, waitFor, within } from "@testing-library/react";
import { MemoryRouter, Route, Routes, useLocation } from "react-router-dom";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { checkAxeAA } from "../../shell/a11y";
import type { PlanVersion, PostingProfile, ProjectGroup, ScheduleView, WbsView } from "./api/client";
import projects from "./module";
import { fieldLabel, parentCode, wbsBody } from "./PlanPages";
import { profileInput } from "./SetupPages";

const shellRules = new Set(["page-has-heading-one", "landmark-unique", "region", "bypass"]);
const pageViolations = (container: HTMLElement) => checkAxeAA(container).filter((v) => !shellRules.has(v.id));

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

const writes = () => calls.filter((c) => c.method !== "GET");

function group(overrides: Partial<ProjectGroup> = {}): ProjectGroup {
  return { id: "ENG", name: "Engineering", version: 2, createdAt: "2026-10-01T09:00:00Z", updatedAt: "2026-10-02T09:00:00Z", ...overrides };
}

describe("projects module routes", () => {
  it("registers setup, plans and WBS pages and gates their nav by inquiry", () => {
    const paths = projects.routes.map((r) => r.path);
    for (const path of ["/projects/groups", "/projects/posting-profiles", "/projects/:id/plans", "/projects/:id/plans/:planId"]) expect(paths).toContain(path);
    expect(projects.nav.find((n) => n.to === "/projects/groups")?.duty).toBe("projects.inquire");
    expect(projects.nav.find((n) => n.to === "/projects/posting-profiles")?.duty).toBe("projects.inquire");
  });
});

describe("project groups", () => {
  it("lists groups, adds one without If-Match and edits one with its version", async () => {
    let items = [group()];
    handler = (url, init) => {
      if (init.method === "PUT") {
        const body = JSON.parse(init.body as string) as { name: string };
        const id = decodeURIComponent(url.pathname.split("/").pop()!);
        const saved = group({ id, name: body.name, version: id === "ENG" ? 3 : 1 });
        items = [...items.filter((g) => g.id !== id), saved];
        return { status: id === "ENG" ? 200 : 201, body: saved };
      }
      return { status: 200, body: { items } };
    };
    const { container } = renderAt("/projects/groups");
    const table = await findTable({ name: "Project groups" });
    await waitFor(() => expect(table).toHaveTextContent("Engineering"));
    expect(pageViolations(container)).toEqual([]);

    fireEvent.change(screen.getByLabelText("Group id"), { target: { value: "ops" } });
    fireEvent.change(screen.getByLabelText("Name"), { target: { value: "Operations" } });
    fireEvent.change(screen.getByLabelText("Default project type"), { target: { value: "TIME_MATERIAL" } });
    fireEvent.click(screen.getByRole("button", { name: "Add group" }));
    expect(await screen.findByText("Project group OPS is saved.")).toBeInTheDocument();
    const created = writes()[0]!;
    expect(created.url.pathname).toBe("/api/v1/projects/groups/OPS");
    expect(created.headers.get("If-Match")).toBeNull();
    expect(created.body).toEqual({ name: "Operations", defaultProjectType: "TIME_MATERIAL" });

    fireEvent.click(within(await findTable({ name: "Project groups" })).getByRole("button", { name: "ENG" }));
    expect(screen.getByRole("heading", { name: "Edit group ENG" })).toBeInTheDocument();
    expect(screen.getByLabelText("Group id")).toHaveAttribute("readonly");
    fireEvent.change(screen.getByLabelText("Name"), { target: { value: "Engineering services" } });
    fireEvent.click(screen.getByRole("button", { name: "Save changes" }));
    expect(await screen.findByText("Project group ENG is saved.")).toBeInTheDocument();
    expect(writes()[1]!.headers.get("If-Match")).toBe('"2"');
  });

  it("checks the id before sending and shows the service's field errors", async () => {
    handler = (_url, init) => (init.method === "PUT" ? problem(400, "COM-0001", "request failed validation", { name: "must be 1 to 120 characters" }) : { status: 200, body: { items: [] } });
    renderAt("/projects/groups");
    expect(await screen.findByText("No project groups yet")).toBeInTheDocument();
    fireEvent.change(screen.getByLabelText("Group id"), { target: { value: "*" } });
    fireEvent.change(screen.getByLabelText("Name"), { target: { value: "x" } });
    fireEvent.click(screen.getByRole("button", { name: "Add group" }));
    expect(await screen.findByText(/Use 1 to 10 capital letters/)).toBeInTheDocument();
    expect(writes()).toHaveLength(0);

    fireEvent.change(screen.getByLabelText("Group id"), { target: { value: "ENG" } });
    fireEvent.click(screen.getByRole("button", { name: "Add group" }));
    expect(await screen.findByText("must be 1 to 120 characters")).toBeInTheDocument();
    expect(screen.getByText("Could not save the project group")).toBeInTheDocument();
  });

  it("shows a load failure and retries", async () => {
    handler = () => problem(403, "AUTH-0003", "missing duty projects.inquire");
    renderAt("/projects/groups");
    expect(await screen.findByText(/missing duty projects.inquire \(AUTH-0003\)/)).toBeInTheDocument();
    handler = () => ({ status: 200, body: { items: [group()] } });
    fireEvent.click(screen.getByRole("button", { name: "Try again" }));
    expect(await screen.findByRole("button", { name: "ENG" })).toBeInTheDocument();
  });
});

function profile(overrides: Partial<PostingProfile> = {}): PostingProfile {
  return {
    projectGroup: "ENG",
    category: "*",
    lineProperty: "*",
    costAccount: "5200",
    wipAccount: "1400",
    accruedRevenueAccount: "1410",
    invoicedRevenueAccount: "4000",
    onAccountAccount: "2400",
    version: 1,
    ...overrides,
  };
}

describe("posting profiles", () => {
  it("only builds the three keys the resolver reads", () => {
    const accounts = { costAccount: "5100", wipAccount: "1400", accruedRevenueAccount: "1410", invoicedRevenueAccount: "4000", onAccountAccount: "2400" };
    const base = { projectGroup: "ENG", category: "LABOR", lineProperty: "CHARGEABLE" as const, ...accounts };
    expect(profileInput({ ...base, level: "exact" }).input).toMatchObject({ projectGroup: "ENG", category: "LABOR", lineProperty: "CHARGEABLE" });
    expect(profileInput({ ...base, level: "group" }).input).toMatchObject({ projectGroup: "ENG", category: "*", lineProperty: "*" });
    expect(profileInput({ ...base, level: "all" }).input).toMatchObject({ projectGroup: "*", category: "*", lineProperty: "*" });
    expect(profileInput({ ...base, level: "exact", category: "" }).errors.category).toBeTruthy();
    expect(profileInput({ ...base, level: "group", wipAccount: "" }).errors.wipAccount).toBeTruthy();
  });

  it("lists profiles, replaces a known key with its version and adds a catch-all without one", async () => {
    handler = (_url, init) => (init.method === "PUT" ? { status: 200, body: profile({ version: 2 }) } : { status: 200, body: { items: [profile()] } });
    const { container } = renderAt("/projects/posting-profiles");
    const table = await findTable({ name: "Posting profiles" });
    await waitFor(() => expect(table).toHaveTextContent("5200"));
    expect(table).toHaveTextContent("Any");
    expect(pageViolations(container)).toEqual([]);

    fireEvent.click(within(table).getByRole("button", { name: "ENG" }));
    expect(screen.getByLabelText("Applies to")).toHaveValue("group");
    fireEvent.change(screen.getByLabelText("Cost account"), { target: { value: "5250" } });
    fireEvent.click(screen.getByRole("button", { name: "Save changes" }));
    expect(await screen.findByText("The posting profile is saved.")).toBeInTheDocument();
    expect(writes()[0]!.headers.get("If-Match")).toBe('"1"');
    expect(writes()[0]!.body).toMatchObject({ projectGroup: "ENG", category: "*", lineProperty: "*", costAccount: "5250" });

    fireEvent.change(screen.getByLabelText("Applies to"), { target: { value: "all" } });
    expect(screen.queryByLabelText("Project group", { selector: "#profile-group" })).not.toBeInTheDocument();
    for (const [label, value] of [["Cost account", "5900"], ["WIP account", "1400"], ["Accrued revenue account", "1410"], ["Invoiced revenue account", "4000"], ["On-account account", "2400"]]) {
      fireEvent.change(screen.getByLabelText(label!), { target: { value } });
    }
    fireEvent.click(screen.getByRole("button", { name: "Add profile" }));
    await waitFor(() => expect(writes()).toHaveLength(2));
    expect(writes()[1]!.headers.get("If-Match")).toBeNull();
    expect(writes()[1]!.body).toMatchObject({ projectGroup: "*", category: "*", lineProperty: "*", costAccount: "5900" });
  });

  it("looks up the accounts for a posting and explains PROJ-POST-001", async () => {
    handler = (url) => {
      if (url.pathname.endsWith("/resolve")) {
        return calls.filter((c) => c.url.pathname.endsWith("/resolve")).length === 1
          ? { status: 200, body: profile() }
          : problem(422, "PROJ-POST-001", "No posting profile for project group OPS, category LABOR, line property CHARGEABLE");
      }
      return { status: 200, body: { items: [] } };
    };
    renderAt("/projects/posting-profiles");
    expect(await screen.findByText("No posting profiles yet")).toBeInTheDocument();
    const lookup = screen.getByRole("form", { name: "Look up accounts" });
    fireEvent.change(within(lookup).getByLabelText("Project group"), { target: { value: "eng" } });
    fireEvent.change(within(lookup).getByLabelText("Category"), { target: { value: "labor" } });
    fireEvent.click(within(lookup).getByRole("button", { name: "Look up" }));
    expect(await screen.findByText(/Uses the fallback for group ENG: cost 5200/)).toBeInTheDocument();
    expect(calls.find((c) => c.url.pathname.endsWith("/resolve"))!.body).toEqual({ projectGroup: "ENG", category: "LABOR", lineProperty: "CHARGEABLE" });

    fireEvent.change(within(lookup).getByLabelText("Project group"), { target: { value: "OPS" } });
    fireEvent.click(within(lookup).getByRole("button", { name: "Look up" }));
    expect(await screen.findByText("No posting profile applies")).toBeInTheDocument();
  });
});

const PLAN_W = "01a00000-0000-7000-8000-000000000001";
const PLAN_B = "01a00000-0000-7000-8000-000000000002";

function planVersion(overrides: Partial<PlanVersion> = {}): PlanVersion {
  return { id: PLAN_W, projectId: "ES-1001", kind: "WORKING", sequence: 1, locked: false, version: 2, createdAt: "2026-10-01T09:00:00Z", updatedAt: "2026-10-01T09:00:00Z", ...overrides };
}

describe("plan versions", () => {
  it("offers a working plan when there is none and opens it after creating", async () => {
    handler = (_url, init) => (init.method === "POST" ? { status: 201, body: planVersion({ version: 1 }) } : readReply(_url));
    function readReply(url: URL): Reply {
      return url.pathname.endsWith("/wbs") ? { status: 200, body: { planVersion: planVersion({ version: 1 }), nodes: [], dependencies: [] } } : { status: 200, body: { items: [] } };
    }
    renderAt("/projects/ES-1001/plans");
    expect(await screen.findByText("No plan versions yet")).toBeInTheDocument();
    fireEvent.click(screen.getByRole("button", { name: "Create working plan" }));
    expect(await screen.findByRole("heading", { name: "Working plan 1 for ES-1001" })).toBeInTheDocument();
    const post = writes()[0]!;
    expect(post.url.pathname).toBe("/api/v1/projects/ES-1001/plans");
    expect(post.body).toEqual({ kind: "WORKING" });
    expect(post.headers.get("Idempotency-Key")).toBeTruthy();
    expect(screen.getByText("No tasks yet")).toBeInTheDocument();
  });

  it("lists versions and saves a baseline as a copy of the working plan", async () => {
    handler = (_url, init) =>
      init.method === "POST"
        ? problem(422, "PROJ-PLAN-001", "the project already has a BASELINE plan version")
        : { status: 200, body: { items: [planVersion()] } };
    const { container } = renderAt("/projects/ES-1001/plans");
    const table = await findTable({ name: "Plan versions" });
    await waitFor(() => expect(table).toHaveTextContent("Working plan 1"));
    expect(pageViolations(container)).toEqual([]);
    const actions = screen.getByRole("group", { name: "Plan actions" });
    expect(within(actions).getAllByRole("button").map((b) => b.textContent)).toEqual(["Save as baseline", "New simulation"]);
    fireEvent.click(within(actions).getByRole("button", { name: "Save as baseline" }));
    expect(await screen.findByText(/already has a BASELINE plan version \(PROJ-PLAN-001\)/)).toBeInTheDocument();
    expect(writes()[0]!.body).toEqual({ kind: "BASELINE", copyFromVersionId: PLAN_W });
  });

  it("shows not found for an unknown project", async () => {
    handler = () => problem(404, "PROJ-NOT-FOUND", "project not found");
    renderAt("/projects/NOPE/plans");
    expect(await screen.findByText("Project not found")).toBeInTheDocument();
  });
});

const N1 = "01b00000-0000-7000-8000-000000000001";
const N11 = "01b00000-0000-7000-8000-000000000011";
const N12 = "01b00000-0000-7000-8000-000000000012";

function wbs(overrides: Partial<WbsView> = {}): WbsView {
  return {
    planVersion: planVersion(),
    nodes: [
      { id: N1, planVersionId: PLAN_W, wbsCode: "1", name: "Design", durationDays: 0, constraintType: "ASAP", isBillableMilestone: false },
      { id: N11, planVersionId: PLAN_W, wbsCode: "1.1", parentId: N1, name: "Draft", durationDays: 3, effortHours: "24.00", constraintType: "ASAP", isBillableMilestone: false },
      { id: N12, planVersionId: PLAN_W, wbsCode: "1.2", parentId: N1, name: "Review", durationDays: 2, constraintType: "SNET", constraintDate: "2026-10-08", isBillableMilestone: true },
    ],
    dependencies: [{ predecessorId: N11, successorId: N12, type: "FS", lagDays: 2 }],
    ...overrides,
  };
}

describe("WBS page", () => {
  it("edits the WBS and saves the whole tree with the plan version as If-Match", async () => {
    handler = (_url, init) => (init.method === "PUT" ? { status: 200, body: wbs({ planVersion: planVersion({ version: 3 }) }) } : { status: 200, body: wbs() });
    const { container } = renderAt(`/projects/ES-1001/plans/${PLAN_W}`);
    expect(await screen.findByRole("heading", { name: "Working plan 1 for ES-1001" })).toBeInTheDocument();
    expect(pageViolations(container)).toEqual([]);
    expect(screen.getByRole("button", { name: "Save WBS" })).toBeDisabled();

    fireEvent.click(screen.getByRole("button", { name: "Add task" }));
    fireEvent.change(screen.getByLabelText("Code of task 4"), { target: { value: "1.3" } });
    fireEvent.change(screen.getByLabelText("Name of task 4"), { target: { value: "Sign-off" } });
    fireEvent.change(screen.getByLabelText("Days of task 4"), { target: { value: "1" } });
    expect(screen.getByRole("button", { name: "Run schedule" })).toBeDisabled();
    fireEvent.click(screen.getByRole("button", { name: "Save WBS" }));
    expect(await screen.findByText(/The WBS is saved \(version 3\)/)).toBeInTheDocument();

    const put = writes()[0]!;
    expect(put.url.pathname).toBe(`/api/v1/projects/ES-1001/plans/${PLAN_W}/wbs`);
    expect(put.headers.get("If-Match")).toBe('"2"');
    expect(put.body).toEqual({
      nodes: [
        { wbsCode: "1", name: "Design", durationDays: 0, isBillableMilestone: false, constraintType: "ASAP" },
        { wbsCode: "1.1", parentWbsCode: "1", name: "Draft", durationDays: 3, effortHours: "24.00", isBillableMilestone: false, constraintType: "ASAP" },
        { wbsCode: "1.2", parentWbsCode: "1", name: "Review", durationDays: 2, isBillableMilestone: true, constraintType: "SNET", constraintDate: "2026-10-08" },
        { wbsCode: "1.3", parentWbsCode: "1", name: "Sign-off", durationDays: 1, isBillableMilestone: false, constraintType: "ASAP" },
      ],
      dependencies: [{ predecessorWbsCode: "1.1", successorWbsCode: "1.2", type: "FS", lagDays: 2 }],
    });
  });

  it("runs the schedule and shows dates, slack and the critical tasks", async () => {
    const scheduled: ScheduleView = {
      ...wbs(),
      nodes: wbs().nodes.map((n) => ({ ...n, earlyStart: "2026-10-01", earlyFinish: "2026-10-09", lateFinish: "2026-10-09", slackDays: 0, critical: true })),
      finishDate: "2026-10-09",
      criticalPath: [N11, N12],
    };
    handler = (_url, init) => (init.method === "POST" ? { status: 200, body: scheduled } : { status: 200, body: wbs() });
    renderAt(`/projects/ES-1001/plans/${PLAN_W}`);
    fireEvent.click(await screen.findByRole("button", { name: "Run schedule" }));
    expect(await screen.findByText("Scheduled. The plan finishes on 2026-10-09; 2 tasks are critical.")).toBeInTheDocument();
    const table = screen.getByRole("table", { name: "Schedule" });
    expect(table).toHaveTextContent("2026-10-09");
    // Three critical badges plus the column header.
    expect(within(table).getAllByText("Critical")).toHaveLength(4);
    expect(writes()[0]!.url.pathname).toBe(`/api/v1/projects/ES-1001/plans/${PLAN_W}/schedule`);
  });

  it("shows row errors from the service and offers a reload on a stale version", async () => {
    let reply: Reply = problem(422, "PROJ-WBS-001", "Dependency 1.2 → 1.1 creates a cycle");
    handler = (_url, init) => (init.method === "PUT" ? reply : { status: 200, body: wbs() });
    renderAt(`/projects/ES-1001/plans/${PLAN_W}`);
    fireEvent.change(await screen.findByLabelText("Name of task 2"), { target: { value: "Draft v2" } });
    fireEvent.click(screen.getByRole("button", { name: "Save WBS" }));
    expect(await screen.findByText(/creates a cycle \(PROJ-WBS-001\)/)).toBeInTheDocument();

    reply = problem(400, "COM-0001", "request failed validation", { "dependencies[0]": "must link tasks, not summary nodes" });
    fireEvent.click(screen.getByRole("button", { name: "Save WBS" }));
    expect(await screen.findByText("Dependency 1: must link tasks, not summary nodes")).toBeInTheDocument();

    reply = problem(412, "conflict_version", "the plan version changed since it was read; re-read and retry");
    fireEvent.click(screen.getByRole("button", { name: "Save WBS" }));
    expect(await screen.findByText("Someone else changed this plan")).toBeInTheDocument();
    fireEvent.click(screen.getByRole("button", { name: "Reload plan" }));
    await waitFor(() => expect(screen.getByLabelText("Name of task 2")).toHaveValue("Draft"));
  });

  it("keeps a baseline read-only", async () => {
    handler = () => ({ status: 200, body: wbs({ planVersion: planVersion({ id: PLAN_B, kind: "BASELINE", sequence: 2, locked: true, copiedFromVersionId: PLAN_W }) }) });
    renderAt(`/projects/ES-1001/plans/${PLAN_B}`);
    expect(await screen.findByRole("heading", { name: "Baseline plan 2 for ES-1001" })).toBeInTheDocument();
    expect(screen.queryByRole("button", { name: "Run schedule" })).not.toBeInTheDocument();
    expect(screen.queryByRole("button", { name: "Save WBS" })).not.toBeInTheDocument();
    expect(screen.getByText(/This baseline is locked/)).toBeInTheDocument();
  });

  it("shows not found for an unknown plan version", async () => {
    handler = () => problem(404, "PROJ-NOT-FOUND", "plan version not found");
    renderAt(`/projects/ES-1001/plans/${PLAN_B}`);
    expect(await screen.findByText("Plan version not found")).toBeInTheDocument();
  });

  it("derives parents from codes and checks rows before sending", () => {
    const codes = new Set(["1", "1.2", "2"]);
    expect(parentCode("1.2", codes)).toBe("1");
    expect(parentCode("1.2.3", codes)).toBe("1.2");
    expect(parentCode("1.3.1", codes)).toBeUndefined();
    expect(parentCode("2", codes)).toBeUndefined();
    const { errors } = wbsBody(
      [{ key: "a", wbsCode: "A", name: "", durationDays: "-1", effortHours: "1.234", startNoEarlierThan: "", isBillableMilestone: false }],
      [{ key: "d", predecessor: "1", successor: "2", type: "FS", lagDays: "x" }],
    );
    expect(Object.keys(errors).sort()).toEqual(["dependencies[0].lagDays", "nodes[0].durationDays", "nodes[0].effortHours", "nodes[0].name", "nodes[0].wbsCode"]);
    expect(fieldLabel("nodes[1].name")).toBe("Task 2 (name)");
    expect(fieldLabel("dependencies[0]")).toBe("Dependency 1");
  });
});
