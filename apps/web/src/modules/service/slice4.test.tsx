import { findAlert, findStatus, findTable } from "../../test-utils";
import { fireEvent, render, screen, waitFor, within } from "@testing-library/react";
import { MemoryRouter, Route, Routes } from "react-router-dom";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { checkAxeAA } from "../../shell/a11y";
import { parseStages, validateCategory, validateProcess, type CategoryValues } from "./CaseSetupPage";
import { stageNeighbours } from "./GenericCaseDetailPage";
import { validateGenericCase, type GenericCaseValues } from "./GenericCasesPage";
import service from "./module";

const LE = "22222222-0000-4000-8000-000000000002";
const CUSTOMER = "11111111-0000-4000-8000-000000000001";
const WORKER = "33333333-0000-4000-8000-000000000003";
const CASE = "55555555-0000-4000-8000-000000000005";
const NEW_CASE = "55555555-0000-4000-8000-000000000006";
const CATEGORY = "77777777-0000-4000-8000-000000000007";
const SERVICE_CATEGORY = "77777777-0000-4000-8000-000000000008";
const PROCESS = "88888888-0000-4000-8000-000000000008";

const shellRules = new Set(["page-has-heading-one", "landmark-unique", "region", "bypass"]);
const pageViolations = (container: HTMLElement) => checkAxeAA(container).filter((v) => !shellRules.has(v.id));

type Reply = { status: number; body: unknown; etag?: string };
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
      const { status, body, etag } = await handler(url, init);
      const headers: Record<string, string> = { "Content-Type": "application/json" };
      if (etag) headers.ETag = etag;
      return new Response(JSON.stringify(body), { status, headers });
    }),
  );
});

afterEach(() => {
  vi.unstubAllGlobals();
});

function renderAt(path: string) {
  return render(
    <MemoryRouter initialEntries={[path]}>
      <Routes>
        {service.routes.map((route) => (
          <Route key={route.path} path={route.path} element={route.element} />
        ))}
        <Route path="*" element={<p>Elsewhere</p>} />
      </Routes>
    </MemoryRouter>,
  );
}

const writes = (suffix: string) => calls.filter((c) => c.method !== "GET" && c.url.pathname.endsWith(suffix));
const empty = { status: 200, body: { items: [] } };

const hrCategory = {
  id: CATEGORY,
  code: "HR-CASE",
  name: "HR case",
  categoryType: "HR",
  processId: PROCESS,
  defaultPriority: 3,
  reopenWindowDays: 14,
  accessDuty: "HrCaseAccess",
  resolutionCodes: ["FOUND", "NO_ACTION"],
};
const serviceCategory = { ...hrCategory, id: SERVICE_CATEGORY, code: "SVC", name: "Service", categoryType: "SERVICE", processId: undefined, accessDuty: "" };
const hrProcess = {
  id: PROCESS,
  code: "HR-PROC",
  name: "HR process",
  stages: [
    { code: "INTAKE", sequence: 1, requiredActivities: [] },
    { code: "INVESTIGATION", sequence: 2, requiredActivities: ["Interview"] },
    { code: "RESOLUTION", sequence: 3, requiredActivities: [] },
  ],
};

function genericCase(overrides: Record<string, unknown> = {}) {
  return {
    id: CASE,
    number: "CS-000042",
    categoryId: CATEGORY,
    categoryCode: "HR-CASE",
    categoryType: "HR",
    legalEntityId: LE,
    ownerId: WORKER,
    subject: "Grievance",
    description: "",
    priority: 3,
    stageCode: "INVESTIGATION",
    status: "OPEN",
    resolutionText: "",
    openedAt: "2026-10-27T09:00:00Z",
    version: 2,
    createdAt: "2026-10-27T09:00:00Z",
    updatedAt: "2026-10-27T09:00:00Z",
    associations: [{ entityType: "worker", entityId: WORKER, role: "SUBJECT" }],
    log: [{ at: "2026-10-27T09:00:00Z", actor: WORKER, kind: "CREATED", text: "opened in category HR-CASE" }],
    ...overrides,
  };
}

/** Answers the setup lists, the activity panel and the case itself. */
function caseHandler(c: Record<string, unknown>, write?: Handler): Handler {
  return (url, init) => {
    if (init.method && init.method !== "GET" && write) return write(url, init);
    if (url.pathname === "/api/v1/case-categories") return { status: 200, body: { items: [hrCategory, serviceCategory] } };
    if (url.pathname === "/api/v1/case-processes") return { status: 200, body: { items: [hrProcess] } };
    if (url.pathname === "/api/v1/service/activities") return empty;
    if (url.pathname === "/api/v1/cases") return { status: 200, body: { items: [c] } };
    return { status: 200, body: c, etag: `"${String(c.version)}"` };
  };
}

const caseValues = (overrides: Partial<GenericCaseValues> = {}): GenericCaseValues => ({
  categoryId: CATEGORY,
  legalEntityId: LE,
  customerPartyId: "",
  subject: "Grievance",
  description: "",
  priority: "",
  subjectType: "",
  subjectId: "",
  subjectRole: "SUBJECT",
  ...overrides,
});

describe("generic case rules mirrored from the service", () => {
  it("validates a case like the service does", () => {
    expect(validateGenericCase(caseValues(), hrCategory as never).input).toEqual({ categoryId: CATEGORY, legalEntityId: LE, subject: "Grievance" });
    expect(validateGenericCase(caseValues({ subject: "  " })).errors.subject).toBeDefined();
    expect(validateGenericCase(caseValues(), { ...hrCategory, categoryType: "COLLECTIONS" } as never).errors.customerPartyId).toMatch(/customer/);
    expect(validateGenericCase(caseValues({ customerPartyId: CUSTOMER }), { ...hrCategory, categoryType: "COLLECTIONS" } as never).input?.customerPartyId).toBe(CUSTOMER);
    expect(validateGenericCase(caseValues(), serviceCategory as never).errors.categoryId).toMatch(/service case screen/);
    expect(validateGenericCase(caseValues({ subjectType: "worker", subjectId: "nope" })).errors.subjectId).toBeDefined();
    expect(validateGenericCase(caseValues({ subjectType: "worker", subjectId: WORKER, priority: "1" })).input).toMatchObject({
      priority: 1,
      associations: [{ entityType: "worker", entityId: WORKER, role: "SUBJECT" }],
    });
  });

  it("parses one stage per line with its required activities", () => {
    expect(parseStages("intake\nINVESTIGATION: Interview, Collect evidence\n\nRESOLUTION").stages).toEqual([
      { code: "INTAKE", sequence: 1, requiredActivities: [] },
      { code: "INVESTIGATION", sequence: 2, requiredActivities: ["Interview", "Collect evidence"] },
      { code: "RESOLUTION", sequence: 3, requiredActivities: [] },
    ]);
    expect(parseStages("A\nB").error).toMatch(/Line 1/);
    expect(parseStages("AA\nAA").error).toMatch(/twice/);
    expect(parseStages("").error).toMatch(/1 to 20/);
    expect(validateProcess({ code: "x", name: "", stages: "AA" }).errors).toMatchObject({ code: expect.any(String), name: expect.any(String) });
  });

  it("validates a category and its resolution codes", () => {
    const values: CategoryValues = {
      code: "hr-case",
      name: "HR case",
      categoryType: "HR",
      parentId: "",
      processId: PROCESS,
      slaProfileId: "",
      defaultPriority: "2",
      reopenWindowDays: "14",
      accessDuty: "HrCaseAccess",
      resolutionCodes: "found, no_action",
    };
    expect(validateCategory(values).input).toEqual({
      code: "HR-CASE",
      name: "HR case",
      categoryType: "HR",
      processId: PROCESS,
      defaultPriority: 2,
      reopenWindowDays: 14,
      accessDuty: "HrCaseAccess",
      resolutionCodes: ["FOUND", "NO_ACTION"],
    });
    expect(validateCategory({ ...values, reopenWindowDays: "400" }).errors.reopenWindowDays).toBeDefined();
    expect(validateCategory({ ...values, resolutionCodes: "A1, A1" }).errors.resolutionCodes).toBeDefined();
  });

  it("finds the next and previous stage", () => {
    expect(stageNeighbours(hrProcess.stages, "INTAKE")).toMatchObject({ index: 0, next: { code: "INVESTIGATION" }, previous: undefined });
    expect(stageNeighbours(hrProcess.stages, "RESOLUTION")).toMatchObject({ index: 2, next: undefined, previous: { code: "INVESTIGATION" } });
    expect(stageNeighbours(hrProcess.stages, "")).toMatchObject({ index: -1, next: undefined, previous: undefined });
  });
});

describe("case list", () => {
  it("lists cases from /api/v1/cases, filters by category and opens one", async () => {
    handler = caseHandler(genericCase());
    const { container } = renderAt("/service/case-management");
    const table = await findTable({ name: "Cases" });
    expect(within(table).getByText("CS-000042")).toBeInTheDocument();
    expect(within(table).getByText("HR-CASE (Hr)")).toBeInTheDocument();
    expect(calls.some((c) => c.url.pathname === "/api/v1/cases" && c.url.searchParams.get("status") === "OPEN")).toBe(true);
    // Service categories open on the service screens, so the filter leaves them out.
    await waitFor(() => expect(screen.getByRole("option", { name: "HR-CASE — HR case" })).toBeInTheDocument());
    expect(screen.queryByRole("option", { name: "SVC — Service" })).toBeNull();
    expect(pageViolations(container)).toEqual([]);

    fireEvent.change(screen.getByLabelText("Category"), { target: { value: CATEGORY } });
    await waitFor(() => expect(calls.some((c) => c.url.pathname === "/api/v1/cases" && c.url.searchParams.get("categoryId") === CATEGORY)).toBe(true));
    fireEvent.click(within(await findTable({ name: "Cases" })).getByText("CS-000042"));
    expect(await screen.findByText("Case CS-000042")).toBeInTheDocument();
  });

  it("shows the empty state and a failed load with a retry", async () => {
    handler = (url) => (url.pathname === "/api/v1/cases" ? empty : { status: 200, body: { items: [] } });
    renderAt("/service/case-management");
    expect(await screen.findByText("No cases match")).toBeInTheDocument();
    document.body.innerHTML = "";
    handler = (url) => (url.pathname === "/api/v1/cases" ? { status: 500, body: { code: "COM-0500", detail: "boom" } } : empty);
    renderAt("/service/case-management");
    expect(await findAlert()).toHaveTextContent("Could not load cases");
    expect(screen.getByRole("button", { name: "Try again" })).toBeInTheDocument();
  });
});

describe("open case", () => {
  it("opens a case with an Idempotency-Key and lands on it", async () => {
    handler = caseHandler(genericCase({ stageCode: "INTAKE", version: 1 }), () => ({ status: 201, body: genericCase({ stageCode: "INTAKE", version: 1 }), etag: '"1"' }));
    renderAt("/service/case-management/new");
    await waitFor(() => expect(screen.getByRole("option", { name: "HR-CASE — HR case" })).toBeInTheDocument());
    fireEvent.change(screen.getByLabelText("Category"), { target: { value: CATEGORY } });
    fireEvent.change(screen.getByLabelText("Legal entity"), { target: { value: LE } });
    fireEvent.change(screen.getByLabelText("Subject"), { target: { value: "Grievance" } });
    fireEvent.change(screen.getByLabelText("Record type"), { target: { value: "worker" } });
    fireEvent.change(screen.getByLabelText("Record"), { target: { value: WORKER } });
    fireEvent.click(screen.getByRole("button", { name: "Open case" }));
    expect(await screen.findByText("Case CS-000042")).toBeInTheDocument();
    const [created] = writes("/api/v1/cases");
    expect(created?.headers.get("Idempotency-Key")).toBeTruthy();
    expect(created?.body).toMatchObject({ categoryId: CATEGORY, legalEntityId: LE, subject: "Grievance", associations: [{ entityType: "worker", entityId: WORKER, role: "SUBJECT" }] });
  });

  it("shows field errors before calling the service and the access refusal after", async () => {
    handler = caseHandler(genericCase(), () => ({ status: 403, body: { code: "CASE-SEC-001", detail: "you do not have access to cases in category HR-CASE" } }));
    renderAt("/service/case-management/new");
    fireEvent.click(screen.getByRole("button", { name: "Open case" }));
    expect(await screen.findByText("Choose a category.")).toBeInTheDocument();
    expect(writes("/api/v1/cases")).toHaveLength(0);
    await waitFor(() => expect(screen.getByRole("option", { name: "HR-CASE — HR case" })).toBeInTheDocument());
    fireEvent.change(screen.getByLabelText("Category"), { target: { value: CATEGORY } });
    fireEvent.change(screen.getByLabelText("Legal entity"), { target: { value: LE } });
    fireEvent.change(screen.getByLabelText("Subject"), { target: { value: "Grievance" } });
    fireEvent.click(screen.getByRole("button", { name: "Open case" }));
    expect(await findAlert()).toHaveTextContent("You cannot open cases in this category");
  });
});

describe("case detail", () => {
  it("shows the stages, the required activity and the log, without axe violations", async () => {
    handler = caseHandler(genericCase());
    const { container } = renderAt(`/service/case-management/${CASE}`);
    expect(await screen.findByText("Case CS-000042")).toBeInTheDocument();
    const stages = await screen.findByRole("list", { name: "Case stages" });
    expect(within(stages).getByText("Current")).toBeInTheDocument();
    expect(within(stages).getByText(/requires Interview/)).toBeInTheDocument();
    expect(screen.getByRole("link", { name: "Add Interview" })).toHaveAttribute(
      "href",
      `/service/activities/new?entityType=case&entityId=${CASE}&subject=Interview`,
    );
    expect(within(await findTable({ name: "Case log" })).getByText("opened in category HR-CASE")).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "Back to INTAKE" })).toBeInTheDocument();
    expect(pageViolations(container)).toEqual([]);
  });

  it("advances with If-Match and an Idempotency-Key, and explains a refused gate", async () => {
    let refuse = true;
    handler = caseHandler(genericCase(), () =>
      refuse
        ? { status: 422, body: { code: "CASE-STG-001", detail: "stage INVESTIGATION exit criteria not met: required activities not DONE: Interview" } }
        : { status: 200, body: genericCase({ stageCode: "RESOLUTION", version: 3 }), etag: '"3"' },
    );
    renderAt(`/service/case-management/${CASE}`);
    fireEvent.click(await screen.findByRole("button", { name: "Advance to RESOLUTION" }));
    expect(await findAlert()).toHaveTextContent("The stage's required activities are not done");
    const [first] = writes("/stage");
    expect(first?.headers.get("If-Match")).toBe('"2"');
    expect(first?.body).toEqual({ toStage: "RESOLUTION" });
    refuse = false;
    fireEvent.click(screen.getByRole("button", { name: "Advance to RESOLUTION" }));
    expect(await findStatus()).toHaveTextContent("Case moved to stage RESOLUTION.");
    const [, second] = writes("/stage");
    // A retry of the same intent keeps its key, so the service can replay it.
    expect(second?.headers.get("Idempotency-Key")).toBe(first?.headers.get("Idempotency-Key"));
  });

  it("moves back with a reason and resolves with a category code", async () => {
    handler = caseHandler(genericCase(), (url) =>
      url.pathname.endsWith("/stage")
        ? { status: 200, body: genericCase({ stageCode: "INTAKE", version: 3 }), etag: '"3"' }
        : { status: 200, body: genericCase({ stageCode: "INTAKE", status: "RESOLVED", resolutionCode: "FOUND", version: 4 }), etag: '"4"' },
    );
    renderAt(`/service/case-management/${CASE}`);
    fireEvent.click(await screen.findByRole("button", { name: "Back to INTAKE" }));
    fireEvent.click(screen.getByRole("button", { name: "Move back" }));
    expect(await screen.findByText("Say why the case goes back a stage.")).toBeInTheDocument();
    fireEvent.change(screen.getByLabelText("Reason"), { target: { value: "wrong intake" } });
    fireEvent.click(screen.getByRole("button", { name: "Move back" }));
    expect(await findStatus()).toHaveTextContent("Case moved to stage INTAKE.");
    expect(writes("/stage")[0]?.body).toEqual({ toStage: "INTAKE", reason: "wrong intake" });

    fireEvent.click(screen.getByRole("button", { name: "Resolve" }));
    fireEvent.change(screen.getByLabelText("Resolution code"), { target: { value: "FOUND" } });
    fireEvent.click(within(screen.getByRole("form", { name: "Resolve the case" })).getByRole("button", { name: "Resolve" }));
    expect(await findStatus()).toHaveTextContent("Case is now resolved.");
    const [resolve] = writes(":transition");
    expect(resolve?.headers.get("If-Match")).toBe('"3"');
    expect(resolve?.body).toEqual({ to: "RESOLVED", resolutionCode: "FOUND" });
  });

  it("reopens after the window as a new case and goes to it", async () => {
    const closed = genericCase({ status: "CLOSED", resolutionCode: "FOUND", version: 5 });
    handler = (url, init) => {
      if (init.method === "POST") return { status: 201, body: { case: genericCase({ id: NEW_CASE, number: "CS-000043", stageCode: "INTAKE", reopenedFrom: CASE, version: 1 }), created: true } };
      if (url.pathname === `/api/v1/cases/${NEW_CASE}`) return { status: 200, body: genericCase({ id: NEW_CASE, number: "CS-000043", reopenedFrom: CASE, version: 1 }), etag: '"1"' };
      return caseHandler(closed)(url, init);
    };
    renderAt(`/service/case-management/${CASE}`);
    expect(await screen.findByText("Case CS-000042")).toBeInTheDocument();
    expect(screen.queryByRole("button", { name: "Resolve" })).toBeNull();
    fireEvent.click(screen.getByRole("button", { name: "Reopen" }));
    fireEvent.change(screen.getByLabelText("Reason"), { target: { value: "raised again" } });
    fireEvent.click(within(screen.getByRole("form", { name: "Reopen the case" })).getByRole("button", { name: "Reopen" }));
    expect(await screen.findByText("Case CS-000043")).toBeInTheDocument();
    expect(writes(":reopen")[0]?.body).toEqual({ reason: "raised again" });
    expect(writes(":reopen")[0]?.headers.get("If-Match")).toBe('"5"');
  });

  it("associates a record and shows the access refusal on a case the caller may not read", async () => {
    handler = caseHandler(genericCase(), () => ({
      status: 200,
      body: genericCase({ associations: [...genericCase().associations, { entityType: "invoice", entityId: CUSTOMER, role: "RELATED" }], version: 3 }),
      etag: '"3"',
    }));
    renderAt(`/service/case-management/${CASE}`);
    const form = await screen.findByRole("form", { name: "Associate a record" });
    fireEvent.change(within(form).getByLabelText("Record type"), { target: { value: "invoice" } });
    fireEvent.change(within(form).getByLabelText("Record"), { target: { value: CUSTOMER } });
    fireEvent.click(within(form).getByRole("button", { name: "Associate record" }));
    expect(await findStatus()).toHaveTextContent("Invoice associated.");
    expect(writes("/associations")[0]?.body).toEqual({ entityType: "invoice", entityId: CUSTOMER, role: "RELATED" });

    document.body.innerHTML = "";
    handler = (url) =>
      url.pathname === `/api/v1/cases/${CASE}` ? { status: 403, body: { code: "CASE-SEC-001", detail: "you do not have access to cases in category HR-CASE" } } : empty;
    renderAt(`/service/case-management/${CASE}`);
    expect(await findAlert()).toHaveTextContent("You do not have access to this case");
  });
});

describe("case setup", () => {
  it("lists categories and processes, with empty states", async () => {
    handler = caseHandler(genericCase());
    const { container } = renderAt("/service/case-setup");
    const categories = await findTable({ name: "Case categories" });
    expect(within(categories).getByText("HR-CASE")).toBeInTheDocument();
    expect(within(categories).getByText("HrCaseAccess")).toBeInTheDocument();
    const processes = await findTable({ name: "Case processes" });
    expect(within(processes).getByText("INTAKE → INVESTIGATION (Interview) → RESOLUTION")).toBeInTheDocument();
    expect(pageViolations(container)).toEqual([]);

    document.body.innerHTML = "";
    handler = () => empty;
    renderAt("/service/case-setup");
    expect(await screen.findByText("No case categories yet")).toBeInTheDocument();
    expect(await screen.findByText("No case processes yet")).toBeInTheDocument();
  });

  it("creates a process from one stage per line", async () => {
    handler = caseHandler(genericCase(), () => ({ status: 201, body: hrProcess }));
    renderAt("/service/case-setup/processes/new");
    fireEvent.change(screen.getByLabelText("Code"), { target: { value: "hr-proc" } });
    fireEvent.change(screen.getByLabelText("Name"), { target: { value: "HR process" } });
    fireEvent.change(screen.getByLabelText("Stages"), { target: { value: "INTAKE\nINVESTIGATION: Interview\nRESOLUTION" } });
    fireEvent.click(screen.getByRole("button", { name: "Create process" }));
    expect(await screen.findByText("Case setup")).toBeInTheDocument();
    const [created] = writes("/api/v1/case-processes");
    expect(created?.headers.get("Idempotency-Key")).toBeTruthy();
    expect(created?.body).toEqual({
      code: "HR-PROC",
      name: "HR process",
      stages: [
        { code: "INTAKE", sequence: 1, requiredActivities: [] },
        { code: "INVESTIGATION", sequence: 2, requiredActivities: ["Interview"] },
        { code: "RESOLUTION", sequence: 3, requiredActivities: [] },
      ],
    });
  });

  it("creates a category and shows the service's duplicate-code refusal", async () => {
    handler = caseHandler(genericCase(), () => ({ status: 409, body: { code: "CASE-CAT-001", detail: "a case category with this code already exists" } }));
    renderAt("/service/case-setup/categories/new");
    fireEvent.change(screen.getByLabelText("Code"), { target: { value: "HR-CASE" } });
    fireEvent.change(screen.getByLabelText("Name"), { target: { value: "HR case" } });
    fireEvent.change(screen.getByLabelText("Type"), { target: { value: "HR" } });
    fireEvent.change(screen.getByLabelText("Access duty"), { target: { value: "HrCaseAccess" } });
    fireEvent.click(screen.getByRole("button", { name: "Create category" }));
    expect(await findAlert()).toHaveTextContent("Could not create the category");
    expect(writes("/api/v1/case-categories")[0]?.body).toMatchObject({ code: "HR-CASE", categoryType: "HR", accessDuty: "HrCaseAccess", reopenWindowDays: 14, defaultPriority: 3 });
  });
});
