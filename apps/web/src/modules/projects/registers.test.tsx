import { findAlert, findTable } from "../../test-utils";
import { fireEvent, render, screen, waitFor, within } from "@testing-library/react";
import { MemoryRouter, Route, Routes, useLocation } from "react-router-dom";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { checkAxeAA } from "../../shell/a11y";
import type { ApplyResult, ChangeOrder, PlanVersion, RiskIssue, WbsView } from "./api/client";
import { changeOrderInput, emptyLine } from "./ChangeOrderPages";
import projects from "./module";
import { riskInput } from "./RiskPages";

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

const CO_ID = "0b1c2d3e-0000-4000-8000-000000000001";
const NODE_13 = "0b1c2d3e-0000-4000-8000-0000000000a3";

function changeOrder(overrides: Partial<ChangeOrder> = {}): ChangeOrder {
  return {
    id: CO_ID,
    number: "CO-0001",
    projectId: "ES-1001",
    type: "SCOPE_CHANGE",
    title: "Extra panel",
    description: "",
    status: "DRAFT",
    createdBy: "pm-1",
    costImpact: "30000.00",
    revenueImpact: "50000.00",
    lines: [{ lineNo: 1, wbsNodeId: NODE_13, wbsCode: "1.3", costImpact: "30000.00", revenueImpact: "50000.00", effortHoursDelta: "40.00", scheduleDaysDelta: 2 }],
    version: 1,
    createdAt: "2026-10-20T09:00:00Z",
    updatedAt: "2026-10-20T09:00:00Z",
    ...overrides,
  };
}

function risk(overrides: Partial<RiskIssue> = {}): RiskIssue {
  return {
    id: "0b1c2d3e-0000-4000-8000-0000000000b1",
    projectId: "ES-1001",
    kind: "RISK",
    title: "Late steel",
    description: "",
    probability: 2,
    impact: 3,
    score: 6,
    mitigation: "",
    status: "OPEN",
    version: 1,
    createdAt: "2026-10-20T09:00:00Z",
    updatedAt: "2026-10-20T09:00:00Z",
    ...overrides,
  };
}

const plans: PlanVersion[] = [
  { id: "p-work", projectId: "ES-1001", kind: "WORKING", sequence: 1, locked: false, version: 2, createdAt: "", updatedAt: "" },
  { id: "p-old", projectId: "ES-1001", kind: "BASELINE", sequence: 2, locked: true, supersededByVersionId: "p-base", version: 1, createdAt: "", updatedAt: "" },
  { id: "p-base", projectId: "ES-1001", kind: "BASELINE", sequence: 3, locked: true, version: 1, createdAt: "", updatedAt: "" },
];

const baselineWbs: WbsView = {
  planVersion: plans[2],
  nodes: [
    { id: "n1", planVersionId: "p-base", wbsCode: "1", name: "Build", durationDays: 0, constraintType: "ASAP", isBillableMilestone: false },
    { id: NODE_13, planVersionId: "p-base", wbsCode: "1.3", parentId: "n1", name: "Install", durationDays: 4, constraintType: "ASAP", isBillableMilestone: false },
  ],
  dependencies: [],
};

describe("routes", () => {
  it("registers the change order and risk pages", () => {
    const paths = projects.routes.map((r) => r.path);
    for (const path of ["/projects/:id/change-orders", "/projects/:id/change-orders/new", "/projects/:id/change-orders/:coId", "/projects/:id/risks"]) {
      expect(paths).toContain(path);
    }
  });
});

describe("change order log", () => {
  it("lists change orders with totals and filters by status", async () => {
    handler = (url) => ({ status: 200, body: { items: url.searchParams.get("status") === "REJECTED" ? [] : [changeOrder({ status: "APPLIED", appliedBaselineVersionId: "p-base" })] } });
    const { container } = renderAt("/projects/ES-1001/change-orders");
    const table = await findTable({ name: "Change orders" });
    await waitFor(() => expect(table).toHaveTextContent("CO-0001"));
    expect(table).toHaveTextContent("50000.00");
    expect(table).toHaveTextContent("Applied");
    expect(calls[0].url.searchParams.get("projectId")).toBe("ES-1001");
    expect(pageViolations(container)).toEqual([]);

    fireEvent.change(screen.getByLabelText("Status"), { target: { value: "REJECTED" } });
    await screen.findByText("No change orders match");
    expect(calls.at(-1)?.url.searchParams.get("status")).toBe("REJECTED");
  });

  it("shows the empty state, and an error with retry", async () => {
    handler = () => ({ status: 200, body: { items: [] } });
    const first = renderAt("/projects/ES-1001/change-orders");
    await screen.findByText("No change orders yet");
    first.unmount();

    let fail = true;
    handler = (url) =>
      fail ? problem(500, "COM-0500", "boom") : url.pathname.endsWith(CO_ID) ? { status: 200, body: changeOrder() } : { status: 200, body: { items: [changeOrder()] } };
    renderAt("/projects/ES-1001/change-orders");
    const alert = await findAlert();
    expect(alert).toHaveTextContent("Could not load the change orders");
    fail = false;
    fireEvent.click(within(alert).getByRole("button", { name: "Try again" }));
    fireEvent.click(await screen.findByRole("button", { name: "CO-0001" }));
    await screen.findByRole("heading", { name: "Change order CO-0001" });
  });

  it("says when the project does not exist", async () => {
    handler = () => problem(404, "PROJ-NOT-FOUND", "project not found");
    renderAt("/projects/NOPE/change-orders");
    await screen.findByText("Project not found");
  });
});

describe("change order entry", () => {
  it("validates like the service before sending", () => {
    const bad = changeOrderInput("ES-1001", {
      type: "CLAIM_VENDOR",
      title: " ",
      description: "",
      customerApprovalRef: "",
      vendorPartyId: "acme",
      lines: [{ ...emptyLine(), costImpact: "1e3", effortHoursDelta: "8" }],
    });
    expect(bad.input).toBeUndefined();
    expect(Object.keys(bad.errors).sort()).toEqual(["lines[0].costImpact", "lines[0].wbsNodeId", "title", "vendorPartyId"]);
    const good = changeOrderInput("ES-1001", { type: "SCOPE_CHANGE", title: "Panel", description: "", customerApprovalRef: "", vendorPartyId: "x", lines: [{ ...emptyLine(), costImpact: "-5" }] });
    expect(good.input).toEqual({ projectId: "ES-1001", type: "SCOPE_CHANGE", title: "Panel", lines: [{ costImpact: "-5", revenueImpact: "0", effortHoursDelta: "0", scheduleDaysDelta: 0 }] });
  });

  it("offers the current baseline's tasks, sends the key and opens the new change order", async () => {
    handler = (url, init) => {
      if (init.method === "POST") return { status: 201, body: changeOrder() };
      if (url.pathname.endsWith("/plans")) return { status: 200, body: { items: plans } };
      if (url.pathname.endsWith("/plans/p-base/wbs")) return { status: 200, body: baselineWbs };
      if (url.pathname.endsWith(`/change-orders/${CO_ID}`)) return { status: 200, body: changeOrder() };
      return problem(404, "PROJ-NOT-FOUND", "unexpected " + url.pathname);
    };
    const { container } = renderAt("/projects/ES-1001/change-orders/new");
    await screen.findByText(/Tasks come from the current baseline/);
    expect(pageViolations(container)).toEqual([]);

    fireEvent.click(screen.getByRole("button", { name: "Save draft" }));
    expect(await screen.findByText("Enter a title of up to 120 characters.")).toBeInTheDocument();
    expect(writes()).toHaveLength(0);

    fireEvent.change(screen.getByLabelText("Title"), { target: { value: "Extra panel" } });
    fireEvent.change(screen.getByLabelText("WBS task of line 1"), { target: { value: NODE_13 } });
    fireEvent.change(screen.getByLabelText("Cost impact of line 1"), { target: { value: "30000" } });
    fireEvent.change(screen.getByLabelText("Revenue impact of line 1"), { target: { value: "50000" } });
    fireEvent.change(screen.getByLabelText("Schedule change of line 1"), { target: { value: "2" } });
    fireEvent.click(screen.getByRole("button", { name: "Save draft" }));
    await screen.findByRole("heading", { name: "Change order CO-0001" });
    const [post] = writes();
    expect(post.url.pathname).toBe("/api/v1/projects/change-orders");
    expect(post.headers.get("Idempotency-Key")).toBeTruthy();
    expect(post.body).toEqual({
      projectId: "ES-1001",
      type: "SCOPE_CHANGE",
      title: "Extra panel",
      lines: [{ wbsNodeId: NODE_13, costImpact: "30000", revenueImpact: "50000", effortHoursDelta: "0", scheduleDaysDelta: 2 }],
    });
  });

  it("puts server field errors on the line", async () => {
    handler = (url, init) => {
      if (init.method === "POST") return problem(422, "COM-0001", "an impact line names an unknown WBS node", { "lines[0].wbsNodeId": "is not a WBS node of this project" });
      if (url.pathname.endsWith("/plans")) return { status: 200, body: { items: [] } };
      return problem(404, "PROJ-NOT-FOUND", "nope");
    };
    renderAt("/projects/ES-1001/change-orders/new");
    await screen.findByText(/The project has no WBS yet/);
    fireEvent.change(screen.getByLabelText("Title"), { target: { value: "Rework" } });
    fireEvent.click(screen.getByRole("button", { name: "Save draft" }));
    expect(await screen.findByText("is not a WBS node of this project")).toBeInTheDocument();
    expect(screen.getByText("Could not save the change order")).toBeInTheDocument();
  });
});

describe("change order detail", () => {
  it("submits with If-Match and the approval reference, then shows the next step", async () => {
    handler = (_url, init) => (init.method === "POST" ? { status: 200, body: changeOrder({ status: "SUBMITTED", version: 2, customerApprovalRef: "Letter 7" }) } : { status: 200, body: changeOrder() });
    const { container } = renderAt(`/projects/ES-1001/change-orders/${CO_ID}`);
    const lines = await findTable({ name: "Impact lines" });
    expect(lines).toHaveTextContent("1.3");
    expect(pageViolations(container)).toEqual([]);
    fireEvent.change(screen.getByLabelText(/Customer approval reference/), { target: { value: "Letter 7" } });
    fireEvent.click(screen.getByRole("button", { name: "Submit for approval" }));
    await screen.findByText("Change order CO-0001 is submitted for approval.");
    const [post] = writes();
    expect(post.url.pathname).toBe(`/api/v1/projects/change-orders/${CO_ID}/submit`);
    expect(post.headers.get("If-Match")).toBe('"1"');
    expect(post.body).toEqual({ customerApprovalRef: "Letter 7" });
    expect(screen.getByRole("button", { name: "Approve" })).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "Reject" })).toBeInTheDocument();
  });

  it("explains the creator-cannot-approve rule", async () => {
    handler = (_url, init) => (init.method === "POST" ? problem(422, "PROJ-CO-004", "Change order CO-0001 must be decided by someone other than its creator") : { status: 200, body: changeOrder({ status: "SUBMITTED", version: 2 }) });
    renderAt(`/projects/ES-1001/change-orders/${CO_ID}`);
    fireEvent.click(await screen.findByRole("button", { name: "Approve" }));
    expect(await screen.findByText("Someone else must decide")).toBeInTheDocument();
  });

  it("applies and shows the new contract value and baseline", async () => {
    const result: ApplyResult = {
      changeOrder: changeOrder({ status: "APPLIED", version: 4, appliedAt: "2026-10-21T10:00:00Z", appliedBy: "controller-1", appliedBaselineVersionId: "p-base" }),
      effect: { contractValueDelta: "50000.00", budgetDelta: "30000.00", contractValue: "550000.00", effortHoursDelta: "40.00", scheduleDaysDelta: 2 },
      baseline: plans[2],
    };
    handler = (_url, init) => (init.method === "POST" ? { status: 200, body: result } : { status: 200, body: changeOrder({ status: "APPROVED", version: 3 }) });
    renderAt(`/projects/ES-1001/change-orders/${CO_ID}`);
    fireEvent.click(await screen.findByRole("button", { name: "Apply to project" }));
    expect(await screen.findByText(/The contract value is now 550000.00/)).toBeInTheDocument();
    expect(screen.getByRole("link", { name: "Open the new baseline" })).toHaveAttribute("href", "/projects/ES-1001/plans/p-base");
    expect(writes()[0].headers.get("If-Match")).toBe('"3"');
    expect(screen.getByText("This change order is applied and can no longer change.")).toBeInTheDocument();
  });

  it("offers a reload when someone else changed it", async () => {
    let version = 2;
    handler = (_url, init) => (init.method === "POST" ? problem(412, "conflict_version", "stale") : { status: 200, body: changeOrder({ status: "SUBMITTED", version: version++ }) });
    renderAt(`/projects/ES-1001/change-orders/${CO_ID}`);
    fireEvent.click(await screen.findByRole("button", { name: "Reject" }));
    fireEvent.click(await screen.findByRole("button", { name: "Reload change order" }));
    await waitFor(() => expect(screen.queryByText("Someone else changed this change order")).not.toBeInTheDocument());
    expect(calls.filter((c) => c.method === "GET")).toHaveLength(2);
  });

  it("says when the change order does not exist", async () => {
    handler = () => problem(404, "PROJ-NOT-FOUND", "change order not found");
    renderAt(`/projects/ES-1001/change-orders/${CO_ID}`);
    await screen.findByText("Change order not found");
  });
});

describe("risks and issues", () => {
  it("validates like the service", () => {
    const bad = riskInput({ kind: "RISK", title: "", description: "", probability: "0", impact: "6", ownerWorkerId: "bob", mitigation: "", dueDate: "", status: "OPEN" });
    expect(Object.keys(bad.errors).sort()).toEqual(["impact", "ownerWorkerId", "probability", "title"]);
    const good = riskInput({ kind: "ISSUE", title: "Crane", description: "", probability: "5", impact: "4", ownerWorkerId: "", mitigation: "Hire", dueDate: "2026-12-01", status: "OPEN" });
    expect(good.input).toEqual({ kind: "ISSUE", title: "Crane", probability: 5, impact: 4, mitigation: "Hire", dueDate: "2026-12-01" });
  });

  it("lists the register, adds an entry with a key and reloads", async () => {
    let items: RiskIssue[] = [];
    handler = (_url, init) => {
      if (init.method === "POST") {
        const added = risk({ kind: "ISSUE", title: "Crane", probability: 5, impact: 4, score: 20 });
        items = [added];
        return { status: 201, body: added };
      }
      return { status: 200, body: { items } };
    };
    const { container } = renderAt("/projects/ES-1001/risks");
    await screen.findByText("No risks or issues yet");
    expect(pageViolations(container)).toEqual([]);

    const form = screen.getByRole("form", { name: "New risk or issue" });
    fireEvent.change(within(form).getByLabelText("Kind"), { target: { value: "ISSUE" } });
    fireEvent.change(within(form).getByLabelText("Title"), { target: { value: "Crane" } });
    fireEvent.change(within(form).getByLabelText("Probability"), { target: { value: "5" } });
    fireEvent.change(within(form).getByLabelText("Impact"), { target: { value: "4" } });
    fireEvent.click(within(form).getByRole("button", { name: "Add to register" }));
    await screen.findByText('Issue "Crane" is added to the register.');
    const table = await findTable({ name: "Risks and issues" });
    await waitFor(() => expect(table).toHaveTextContent("20"));
    const [post] = writes();
    expect(post.url.pathname).toBe("/api/v1/projects/ES-1001/risks");
    expect(post.headers.get("Idempotency-Key")).toBeTruthy();
    expect(post.body).toEqual({ kind: "ISSUE", title: "Crane", probability: 5, impact: 4 });
  });

  it("edits an entry under If-Match with only the allowed statuses, and shows a refused move", async () => {
    const entry = risk({ status: "MITIGATED", version: 2 });
    handler = (_url, init) => (init.method === "PUT" ? problem(422, "PROJ-RISK-001", "A CLOSED risk or issue cannot change") : { status: 200, body: { items: [entry, risk({ id: "closed-1", title: "Old", status: "CLOSED" })] } });
    renderAt("/projects/ES-1001/risks");
    fireEvent.click(await screen.findByRole("button", { name: "Edit Late steel" }));
    expect(screen.queryByRole("button", { name: "Edit Old" })).not.toBeInTheDocument();
    const form = screen.getByRole("form", { name: "Edit risk or issue" });
    const status = within(form).getByLabelText("Status");
    expect(within(status).getAllByRole("option").map((o) => o.textContent)).toEqual(["Mitigated", "Open", "Closed"]);
    fireEvent.change(status, { target: { value: "CLOSED" } });
    fireEvent.click(within(form).getByRole("button", { name: "Save changes" }));
    expect(await screen.findByText("That status change is not possible")).toBeInTheDocument();
    const [put] = writes();
    expect(put.method).toBe("PUT");
    expect(put.url.pathname).toBe(`/api/v1/projects/ES-1001/risks/${entry.id}`);
    expect(put.headers.get("If-Match")).toBe('"2"');
    expect(put.body).toMatchObject({ status: "CLOSED", title: "Late steel", probability: 2, impact: 3 });
  });

  it("shows a load error with retry", async () => {
    let fail = true;
    handler = () => (fail ? problem(503, "COM-0503", "down") : { status: 200, body: { items: [risk()] } });
    renderAt("/projects/ES-1001/risks");
    const alert = await screen.findByText("Could not load the register");
    fail = false;
    fireEvent.click(within(alert.closest("[role=alert]") as HTMLElement).getByRole("button", { name: "Try again" }));
    await findTable({ name: "Risks and issues" });
  });
});
