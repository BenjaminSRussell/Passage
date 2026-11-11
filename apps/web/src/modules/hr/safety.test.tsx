import { fireEvent, screen, waitFor, within } from "@testing-library/react";
import { describe, expect, it } from "vitest";
import { findAlert, findStatus } from "../../test-utils";
import hr from "./module";
import { todayIso } from "./format";
import { json, id, pageViolations, problem, renderAt, routes, useFakeApi, type Reply } from "./hrTestKit";
import { validateTemplate } from "./ChecklistTemplatesPage";
import { validateIncident, type IncidentValues } from "./NewIncidentPage";
import { validateLoan } from "./LoansPage";

const WORKER = id(1);
const INCIDENT = id(80);
const PRIVACY = id(81);
const LOAN = id(90);
const CHECKLIST = id(100);
const TASK = id(101);
const TEMPLATE = id(102);

const incident = (patch: Record<string, unknown> = {}) => ({
  id: INCIDENT,
  caseNo: 12,
  workerId: WORKER,
  workerName: "Pat Worker",
  legalEntityId: id(5),
  siteId: id(6),
  incidentType: "INJURY",
  occurredAt: "2026-10-30T09:00:00Z",
  reportedAt: "2026-10-30T10:00:00Z",
  location: "Dock 3",
  bodyParts: ["HAND"],
  daysAway: 3,
  daysRestricted: 0,
  oshaRecordable: true,
  outcome: "DAYS_AWAY",
  privacyCase: false,
  status: "REPORTED",
  reportedBy: "supervisor",
  ...patch,
});
const privacyCase = incident({ id: PRIVACY, caseNo: 13, privacyCase: true, workerId: undefined, workerName: undefined });
const loan = (patch: Record<string, unknown> = {}) => ({ id: LOAN, workerId: WORKER, loanType: "LAPTOP", itemDescription: "Dell laptop", serial: "SN-1", issuedOn: "2020-01-01", dueOn: "2020-06-01", status: "OUT", ...patch });
const task = (patch: Record<string, unknown> = {}) => ({
  id: TASK,
  checklistId: CHECKLIST,
  workerId: WORKER,
  taskCode: "BADGE",
  title: "Issue a badge",
  ownerRole: "HR_PARTNER",
  dueOn: "2020-01-01",
  evidenceRequired: false,
  status: "OPEN",
  ...patch,
});
const checklist = (patch: Record<string, unknown> = {}) => ({ id: CHECKLIST, workerId: WORKER, templateId: TEMPLATE, kind: "ONBOARDING", effectiveDate: "2026-11-01", status: "OPEN", tasks: [task()], ...patch });
const template = { id: TEMPLATE, code: "ONB-STD", kind: "ONBOARDING", name: "Standard onboarding", tasks: [{ code: "BADGE", title: "Issue a badge", ownerRole: "HR_PARTNER", dueOffsetDays: 0, evidenceRequired: false }] };
const osha = {
  year: 2026,
  truncated: false,
  summary: { deaths: 0, daysAwayCases: 1, restrictedCases: 0, otherRecordableCases: 0, totalDaysAway: 3, totalDaysRestricted: 0, byType: { INJURY: 1 } },
  log: [
    { incidentId: INCIDENT, caseNo: 12, employeeName: "Pat Worker", siteId: id(6), occurredOn: "2026-10-30", location: "Dock 3", incidentType: "INJURY", bodyParts: ["HAND"], outcome: "DAYS_AWAY", daysAway: 3, daysRestricted: 0, privacyCase: false, description: "Slipped on oil", witnesses: ["Sam"] },
    { incidentId: PRIVACY, caseNo: 13, employeeName: "Privacy case", siteId: id(6), occurredOn: "2026-10-29", location: "Office", incidentType: "RESPIRATORY", bodyParts: [], outcome: "RESTRICTED", daysAway: 0, daysRestricted: 2, privacyCase: true },
  ],
};

const smoke: Array<[string, string, Record<string, Reply>, string]> = [
  ["register", "/hr/incidents", { "GET /api/v1/hr/incidents": json(200, { items: [incident()] }) }, "Pat Worker"],
  ["incident", `/hr/incidents/${INCIDENT}`, { [`GET /api/v1/hr/incidents/${INCIDENT}`]: json(200, incident({ details: { description: "Slipped on oil" } })) }, "Slipped on oil"],
  ["report", "/hr/incidents/new", {}, "Report an incident"],
  ["osha", "/hr/incidents/osha?year=2026", { "GET /api/v1/hr/incidents/osha-extract": json(200, osha) }, "300A summary for 2026"],
  ["outstanding loans", "/hr/loaned-items", { "GET /api/v1/hr/loaned-items/outstanding": json(200, { items: [loan()] }) }, "Dell laptop (SN-1)"],
  ["worker loans", `/hr/workers/${WORKER}/loaned-items`, { [`GET /api/v1/hr/workers/${WORKER}/loaned-items`]: json(200, { items: [loan()] }) }, "Dell laptop (SN-1)"],
  ["templates", "/hr/checklist-templates", { "GET /api/v1/hr/checklist-templates": json(200, { items: [template] }) }, "Standard onboarding"],
  ["new template", "/hr/checklist-templates/new", {}, "New checklist template"],
  [
    "worker checklists",
    `/hr/workers/${WORKER}/checklists`,
    { [`GET /api/v1/hr/workers/${WORKER}/checklists`]: json(200, { items: [checklist()] }), "GET /api/v1/hr/checklist-templates": json(200, { items: [template] }) },
    "0 of 1 finished",
  ],
  ["checklist", `/hr/checklists/${CHECKLIST}`, { [`GET /api/v1/hr/checklists/${CHECKLIST}`]: json(200, checklist()) }, "Issue a badge"],
  ["tasks", `/hr/tasks?owner=${WORKER}`, { "GET /api/v1/hr/tasks": json(200, { items: [task()] }) }, "Issue a badge"],
];

const incidentValues: IncidentValues = {
  workerId: WORKER, legalEntityId: id(5), siteId: id(6), incidentType: "INJURY", occurredAt: "2026-10-30T09:00", location: "Dock 3", bodyParts: [],
  death: false, lossOfConsciousness: false, medicalTreatment: false, daysAway: "0", daysRestricted: "0", recordable: "", overrideReason: "", privacyCase: false,
  supersedesId: "", description: "Slipped on oil", witnesses: "", treatments: "", rootCause: "", correctiveAction: "",
};

describe("incidents, OSHA logs, loaned items and checklists", () => {
  const api = useFakeApi();

  it.each(smoke)("%s renders its data and passes axe", async (_name, path, table, text) => {
    api.handler = routes(table);
    const { container } = renderAt(path);
    expect((await screen.findAllByText(text)).length).toBeGreaterThan(0);
    expect(pageViolations(container)).toEqual([]);
  });

  it.each([
    ["/hr/incidents", "GET /api/v1/hr/incidents", "No incidents"],
    ["/hr/loaned-items", "GET /api/v1/hr/loaned-items/outstanding", "Nothing is out on loan"],
    ["/hr/checklist-templates", "GET /api/v1/hr/checklist-templates", "No checklist templates"],
    [`/hr/workers/${WORKER}/checklists`, `GET /api/v1/hr/workers/${WORKER}/checklists`, "No checklists"],
    [`/hr/tasks?owner=${WORKER}`, "GET /api/v1/hr/tasks", "No tasks"],
  ])("%s shows an empty state and an error with a retry", async (path, key, empty) => {
    api.handler = routes({ "GET /api/v1/hr/checklist-templates": json(200, { items: [] }), [key]: json(200, { items: [] }) });
    const first = renderAt(path);
    expect(await screen.findByText(empty)).toBeTruthy();
    expect(pageViolations(first.container)).toEqual([]);
    first.unmount();

    api.handler = routes({ "GET /api/v1/hr/checklist-templates": json(200, { items: [] }), [key]: problem(500, "COM-0500", "boom") });
    renderAt(path);
    expect((await findAlert()).textContent).toContain("boom");
    expect(screen.getAllByRole("button", { name: "Try again" }).length).toBeGreaterThan(0);
  });

  it("withholds a privacy case's worker in the register", async () => {
    api.handler = routes({ "GET /api/v1/hr/incidents": json(200, { items: [incident(), privacyCase] }) });
    renderAt("/hr/incidents");
    expect(await screen.findByText("Privacy case")).toBeTruthy();
    expect(screen.getByText("Pat Worker")).toBeTruthy();
    expect(document.body.textContent).not.toContain(WORKER);
  });

  it("shows medical details only when the service returned them", async () => {
    api.handler = routes({ [`GET /api/v1/hr/incidents/${INCIDENT}`]: json(200, incident()) });
    const without = renderAt(`/hr/incidents/${INCIDENT}`);
    expect(await screen.findByText("The service returned no medical details for this case.")).toBeTruthy();
    without.unmount();

    api.handler = routes({ [`GET /api/v1/hr/incidents/${INCIDENT}`]: json(200, incident({ details: { description: "Slipped on oil", witnesses: ["Sam"], treatments: ["Ice"], rootCause: "Spill" } })) });
    renderAt(`/hr/incidents/${INCIDENT}`);
    expect(await screen.findByText("Slipped on oil")).toBeTruthy();
    expect(screen.getByText("Sam")).toBeTruthy();
    expect(screen.getByText("Spill")).toBeTruthy();
  });

  it("starts a review and refuses to close an incident the user reported", async () => {
    let status = "REPORTED";
    api.handler = routes({
      [`GET /api/v1/hr/incidents/${INCIDENT}`]: () => json(200, incident({ status })),
      [`POST /api/v1/hr/incidents/${INCIDENT}/status`]: (call) => {
        if ((call.body as { status: string }).status === "CLOSED") return problem(403, "HCM-INC-SELF-CLOSE", "the reporter cannot close the incident");
        status = "UNDER_REVIEW";
        return json(200, incident({ status }));
      },
    });
    renderAt(`/hr/incidents/${INCIDENT}`);
    fireEvent.click(await screen.findByRole("button", { name: "Start review" }));
    expect((await findStatus()).textContent).toBe("The incident is under review.");
    fireEvent.click(await screen.findByRole("button", { name: "Close incident" }));
    fireEvent.click(within(screen.getByRole("alertdialog")).getByRole("button", { name: "Close incident" }));
    expect((await findAlert()).textContent).toContain("Whoever reported an incident can't close it. Ask another incident administrator.");
  });

  it("explains a closed incident that cannot change status", async () => {
    api.handler = routes({
      [`GET /api/v1/hr/incidents/${INCIDENT}`]: json(200, incident({ status: "UNDER_REVIEW" })),
      [`POST /api/v1/hr/incidents/${INCIDENT}/status`]: problem(422, "HCM-INC-STATE", "a CLOSED incident is immutable"),
    });
    renderAt(`/hr/incidents/${INCIDENT}`);
    fireEvent.click(await screen.findByRole("button", { name: "Close incident" }));
    fireEvent.click(within(screen.getByRole("alertdialog")).getByRole("button", { name: "Close incident" }));
    expect((await findAlert()).textContent).toContain("a CLOSED incident is immutable");
  });

  it("says a missing incident was not found and a forbidden one is off limits", async () => {
    api.handler = routes({ [`GET /api/v1/hr/incidents/${INCIDENT}`]: problem(422, "HCM-INC-NOT-FOUND", "incident not found") });
    const first = renderAt(`/hr/incidents/${INCIDENT}`);
    expect(await screen.findByText("We couldn't find this incident")).toBeTruthy();
    first.unmount();
    api.handler = routes({ [`GET /api/v1/hr/incidents/${INCIDENT}`]: problem(403, "AUTH-0003", "missing duty hr.incident.admin") });
    renderAt(`/hr/incidents/${INCIDENT}`);
    expect((await findAlert()).textContent).toContain("You can't view this incident");
  });

  it("reports an incident and confirms the case number", async () => {
    api.handler = routes({ "POST /api/v1/hr/incidents": json(201, incident()) });
    const { container } = renderAt("/hr/incidents/new");
    fireEvent.click(screen.getByRole("button", { name: "Report incident" }));
    expect(await screen.findByText("Describe what happened, up to 4000 characters.")).toBeTruthy();
    expect(api.calls).toHaveLength(0);
    fireEvent.change(screen.getByLabelText("Worker"), { target: { value: WORKER } });
    fireEvent.change(screen.getByLabelText("Legal entity"), { target: { value: id(5) } });
    fireEvent.change(screen.getByLabelText("Site"), { target: { value: id(6) } });
    fireEvent.change(screen.getByLabelText("When it happened"), { target: { value: "2026-10-30T09:00" } });
    fireEvent.change(screen.getByLabelText("Location"), { target: { value: "Dock 3" } });
    fireEvent.change(screen.getByLabelText("What happened"), { target: { value: "Slipped on oil" } });
    fireEvent.click(screen.getByLabelText("Hand"));
    fireEvent.click(screen.getByRole("button", { name: "Report incident" }));
    expect((await findStatus()).textContent).toContain("Case #12 was recorded");
    expect(api.posts("/incidents")[0]?.body).toEqual({
      workerId: WORKER, legalEntityId: id(5), siteId: id(6), incidentType: "INJURY", occurredAt: "2026-10-30T09:00:00Z", location: "Dock 3", bodyParts: ["HAND"],
      death: false, lossOfConsciousness: false, medicalTreatment: false, daysAway: 0, daysRestricted: 0, privacyCase: false, description: "Slipped on oil", witnesses: [], treatments: [],
    });
    expect(api.posts("/incidents")[0]?.headers.get("Idempotency-Key")).toBeTruthy();
    expect(pageViolations(container)).toEqual([]);
  });

  it("puts a refused recordable override on the reason field", async () => {
    api.handler = routes({ "POST /api/v1/hr/incidents": problem(422, "HCM-INC-001", "overriding the recordable decision needs a justification") });
    renderAt("/hr/incidents/new");
    fireEvent.change(screen.getByLabelText("Worker"), { target: { value: WORKER } });
    fireEvent.change(screen.getByLabelText("Legal entity"), { target: { value: id(5) } });
    fireEvent.change(screen.getByLabelText("Site"), { target: { value: id(6) } });
    fireEvent.change(screen.getByLabelText("When it happened"), { target: { value: "2026-10-30T09:00" } });
    fireEvent.change(screen.getByLabelText("Location"), { target: { value: "Dock 3" } });
    fireEvent.change(screen.getByLabelText("What happened"), { target: { value: "Slipped" } });
    fireEvent.change(screen.getByLabelText("OSHA recordable"), { target: { value: "no" } });
    fireEvent.click(screen.getByRole("button", { name: "Report incident" }));
    expect(await screen.findByText("Say why you are overriding the recordable decision.")).toBeTruthy();
    fireEvent.change(screen.getByLabelText("Reason for the override"), { target: { value: "first aid only" } });
    fireEvent.click(screen.getByRole("button", { name: "Report incident" }));
    expect(await screen.findByText(/needs a justification/)).toBeTruthy();
    expect(api.posts("/incidents")[0]?.body).toMatchObject({ oshaRecordable: false, overrideReason: "first aid only" });
  });

  it("validates an incident the way the service does", () => {
    const now = new Date("2026-10-31T12:00:00Z");
    expect(validateIncident(incidentValues, now).errors).toEqual({});
    expect(validateIncident({ ...incidentValues, occurredAt: "2026-11-02T09:00" }, now).errors.occurredAt).toContain("future");
    expect(validateIncident({ ...incidentValues, daysAway: "400" }, now).errors.daysAway).toBeTruthy();
    expect(validateIncident({ ...incidentValues, witnesses: "a\nb" }, now).input?.witnesses).toEqual(["a", "b"]);
    expect(validateIncident({ ...incidentValues, recordable: "yes" }, now).errors.overrideReason).toBeTruthy();
  });

  it("shows the OSHA 300A summary, the 300 log with the privacy label, and 301 detail only when returned", async () => {
    api.handler = routes({ "GET /api/v1/hr/incidents/osha-extract": json(200, { ...osha, truncated: true }) });
    renderAt("/hr/incidents/osha?year=2026");
    expect(await screen.findByText("300A summary for 2026")).toBeTruthy();
    expect(screen.getAllByText("Privacy case").length).toBeGreaterThan(0);
    expect(screen.getByText("The log was cut short")).toBeTruthy();
    expect(screen.getByText("Case 12: Pat Worker")).toBeTruthy();
    expect(screen.queryByText(/Case 13: /)).toBeNull();
    expect(api.calls[0]?.url.search).toBe("?year=2026");
  });

  it("asks for a valid year and explains a forbidden extract", async () => {
    api.handler = routes({ "GET /api/v1/hr/incidents/osha-extract": problem(403, "AUTH-0003", "missing duty hr.incident.admin") });
    renderAt("/hr/incidents/osha?year=20");
    expect(await screen.findByText("Enter a four-digit year.")).toBeTruthy();
    expect(await screen.findByText("You can't view this OSHA extract")).toBeTruthy();
  });

  it("lists overdue loans and records one returned today", async () => {
    let out = true;
    api.handler = routes({
      "GET /api/v1/hr/loaned-items/outstanding": () => json(200, { items: out ? [loan()] : [] }),
      [`POST /api/v1/hr/loaned-items/${LOAN}/close`]: () => {
        out = false;
        return json(200, loan({ status: "RETURNED" }));
      },
    });
    renderAt("/hr/loaned-items");
    expect(await screen.findByText("Overdue")).toBeTruthy();
    fireEvent.click(screen.getByRole("button", { name: "Record Dell laptop returned" }));
    expect((await findStatus()).textContent).toBe("Dell laptop was recorded returned.");
    expect(api.posts("/close")[0]?.body).toEqual({ status: "RETURNED", closedOn: todayIso() });
    expect(await screen.findByText("Nothing is out on loan")).toBeTruthy();
  });

  it("shows why a loan could not be closed", async () => {
    api.handler = routes({
      "GET /api/v1/hr/loaned-items/outstanding": json(200, { items: [loan()] }),
      [`POST /api/v1/hr/loaned-items/${LOAN}/close`]: problem(422, "HCM-LOAN-STATE", "the item is already closed"),
    });
    renderAt("/hr/loaned-items");
    fireEvent.click(await screen.findByRole("button", { name: "Record Dell laptop lost" }));
    expect((await findAlert()).textContent).toContain("the item is already closed");
  });

  it("lends an item and sends the legal entity error to its field", async () => {
    api.handler = routes({
      [`GET /api/v1/hr/workers/${WORKER}/loaned-items`]: json(200, { items: [] }),
      [`POST /api/v1/hr/workers/${WORKER}/loaned-items`]: problem(422, "HCM-ENTITY-REQUIRED", "legalEntityId is required: the worker is employed in several legal entities"),
    });
    renderAt(`/hr/workers/${WORKER}/loaned-items`);
    fireEvent.change(await screen.findByLabelText("Loan type"), { target: { value: "laptop" } });
    fireEvent.change(screen.getByLabelText("Item"), { target: { value: "Dell laptop" } });
    fireEvent.click(screen.getByRole("button", { name: "Lend item" }));
    expect(await screen.findByText(/legalEntityId is required/)).toBeTruthy();
    expect(api.posts("/loaned-items")[0]?.body).toEqual({ loanType: "LAPTOP", itemDescription: "Dell laptop", issuedOn: todayIso() });
  });

  it("validates a loan", () => {
    const base = { loanType: "badge", itemDescription: "Door badge", serial: "", issuedOn: "2026-10-01", dueOn: "", conditionOut: "", legalEntityId: "" };
    expect(validateLoan(base).input).toEqual({ loanType: "BADGE", itemDescription: "Door badge", issuedOn: "2026-10-01" });
    expect(validateLoan({ ...base, dueOn: "2026-09-01" }).errors.dueOn).toBeTruthy();
    expect(validateLoan({ ...base, loanType: "A".repeat(21) }).errors.loanType).toBeTruthy();
  });

  it("finishes a task with the evidence the template requires", async () => {
    api.handler = routes({
      [`GET /api/v1/hr/checklists/${CHECKLIST}`]: json(200, checklist({ tasks: [task({ evidenceRequired: true })] })),
      [`POST /api/v1/hr/tasks/${TASK}/finish`]: (call) =>
        (call.body as { evidenceRef?: string }).evidenceRef ? json(200, task({ status: "DONE" })) : problem(422, "HCM-CHK-EVIDENCE", "evidenceRef is required for this task"),
    });
    renderAt(`/hr/checklists/${CHECKLIST}`);
    fireEvent.click(await screen.findByRole("button", { name: "Mark done: Issue a badge" }));
    expect((await findAlert()).textContent).toContain("evidenceRef is required for this task");
    fireEvent.change(screen.getByLabelText("Evidence for Issue a badge"), { target: { value: "ticket-42" } });
    fireEvent.click(screen.getByRole("button", { name: "Mark done: Issue a badge" }));
    expect((await findStatus()).textContent).toBe("Issue a badge is done.");
    expect(api.posts("/finish")[1]?.body).toEqual({ status: "DONE", evidenceRef: "ticket-42" });
  });

  it("explains why a checklist cannot complete while items are out", async () => {
    api.handler = routes({
      [`GET /api/v1/hr/checklists/${CHECKLIST}`]: json(200, checklist({ kind: "OFFBOARDING" })),
      [`POST /api/v1/hr/checklists/${CHECKLIST}/complete`]: problem(422, "HCM-LOAN-001", "2 loaned items are still OUT"),
    });
    renderAt(`/hr/checklists/${CHECKLIST}`);
    fireEvent.click(await screen.findByRole("button", { name: "Complete checklist" }));
    expect((await findAlert()).textContent).toContain("Items are still out on loan. Record them returned or lost first.");
  });

  it("starts a checklist from a template, and falls back to an id when templates are off limits", async () => {
    api.handler = routes({
      [`GET /api/v1/hr/workers/${WORKER}/checklists`]: json(200, { items: [] }),
      "GET /api/v1/hr/checklist-templates": json(200, { items: [template] }),
      [`POST /api/v1/hr/workers/${WORKER}/checklists`]: json(201, checklist()),
    });
    const first = renderAt(`/hr/workers/${WORKER}/checklists`);
    await waitFor(() => expect((screen.getByLabelText("Template") as HTMLSelectElement).options.length).toBe(2));
    fireEvent.change(screen.getByLabelText("Template"), { target: { value: TEMPLATE } });
    fireEvent.change(screen.getByLabelText("Effective date"), { target: { value: "2026-11-01" } });
    fireEvent.click(screen.getByRole("button", { name: "Start checklist" }));
    expect((await findStatus()).textContent).toBe("Started the onboarding checklist effective 2026-11-01.");
    expect(api.posts(`/workers/${WORKER}/checklists`)[0]?.body).toEqual({ templateId: TEMPLATE, effectiveDate: "2026-11-01" });
    first.unmount();

    api.handler = routes({
      [`GET /api/v1/hr/workers/${WORKER}/checklists`]: json(200, { items: [] }),
      "GET /api/v1/hr/checklist-templates": problem(403, "AUTH-0003", "missing duty hr.setup.admin"),
    });
    renderAt(`/hr/workers/${WORKER}/checklists`);
    expect(await screen.findByLabelText("Template id")).toBeTruthy();
  });

  it("asks for an owner before listing tasks and defaults to my tasks on request", async () => {
    api.handler = routes({
      "GET /api/v1/hr/me": json(200, { id: WORKER, personnelNumber: "W-1", workerType: "EMPLOYEE", partyPersonId: WORKER, legalName: "Pat", createdAt: "2026-01-01T00:00:00Z", updatedAt: "2026-01-01T00:00:00Z", pii: { nationalId: "***", masked: true }, asOf: { workerId: WORKER, date: "2026-10-31", employments: [], positionId: null, positionNumber: null, fteShare: null, managerWorkerId: null } }),
      "GET /api/v1/hr/tasks": json(200, { items: [task()] }),
    });
    renderAt("/hr/tasks");
    expect(await screen.findByText("Choose an owner")).toBeTruthy();
    await waitFor(() => expect((screen.getByRole("button", { name: "My tasks" }) as HTMLButtonElement).disabled).toBe(false));
    fireEvent.click(screen.getByRole("button", { name: "My tasks" }));
    expect(await screen.findByText("Issue a badge")).toBeTruthy();
    expect(api.calls.find((c) => c.url.pathname === "/api/v1/hr/tasks")?.url.search).toBe(`?ownerWorkerId=${WORKER}&status=OPEN`);
  });

  it("creates a checklist template with upper-case codes and refuses the reserved task code", async () => {
    api.handler = routes({ "POST /api/v1/hr/checklist-templates": problem(422, "HCM-CHK-TEMPLATE-DUPLICATE", "template code already exists") });
    renderAt("/hr/checklist-templates/new");
    fireEvent.change(screen.getByLabelText("Code", { selector: "#template-code" }), { target: { value: "onb-std" } });
    fireEvent.change(screen.getByLabelText("Name"), { target: { value: "Standard onboarding" } });
    fireEvent.change(screen.getByLabelText("Code", { selector: "#task-0-code" }), { target: { value: "badge" } });
    fireEvent.change(screen.getByLabelText("Title"), { target: { value: "Issue a badge" } });
    fireEvent.click(screen.getByRole("button", { name: "Add template" }));
    expect(await screen.findByText(/template code already exists/)).toBeTruthy();
    expect(api.posts("/checklist-templates")[0]?.body).toEqual({
      code: "ONB-STD", kind: "ONBOARDING", name: "Standard onboarding",
      tasks: [{ code: "BADGE", title: "Issue a badge", ownerRole: "MANAGER", dueOffsetDays: 0, evidenceRequired: false }],
    });
    const reserved = validateTemplate({ code: "A", kind: "OFFBOARDING", name: "n", tasks: [{ code: "return_loaned_items", title: "t", ownerRole: "IT", dueOffsetDays: "0", instructions: "", evidenceRequired: false }] });
    expect(reserved.errors["tasks[0].code"]).toContain("RETURN_LOANED_ITEMS");
  });

  it("gates the safety routes and nav by duty", () => {
    const duty = (path: string) => (hr.routes.find((r) => r.path === path)?.handle as { duty?: string }).duty;
    expect(duty("/hr/incidents")).toBe("hr.incident.admin");
    expect(duty("/hr/incidents/osha")).toBe("hr.incident.admin");
    expect(duty("/hr/incidents/new")).toBe("hr.incident.report");
    expect(duty("/hr/checklist-templates")).toBe("hr.setup.admin");
    expect(hr.nav.find((n) => n.to === "/hr/incidents/new")?.duty).toBe("hr.incident.report");
    expect(hr.nav.find((n) => n.to === "/hr/incidents")?.duty).toBe("hr.incident.admin");
  });
});
