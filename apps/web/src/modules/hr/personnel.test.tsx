import { fireEvent, screen, waitFor } from "@testing-library/react";
import { describe, expect, it } from "vitest";
import { findAlert, findStatus } from "../../test-utils";
import hr from "./module";
import { compareDecimal } from "./format";
import { json, id, pageViolations, problem, renderAt, routes, useFakeApi, type Reply } from "./hrTestKit";
import { validateAction, type ActionValues } from "./NewPersonnelActionPage";
import { validateJob } from "./NewJobPage";

const WORKER = id(1);
const EMPLOYMENT = id(2);
const POSITION = id(3);
const PLAN = id(4);
const ACTION = id(10);

const action = (patch: Record<string, unknown> = {}) => ({
  id: ACTION,
  actionType: "TRANSFER",
  workerId: WORKER,
  employmentId: EMPLOYMENT,
  effectiveDate: "2026-12-01",
  status: "PENDING",
  payload: { positionId: POSITION, fteShare: "1.00" },
  submittedBy: "hr-admin",
  submittedAt: "2026-10-31T10:00:00Z",
  ...patch,
});
const job = { id: id(20), code: "FORKLIFT", title: "Forklift operator", jobType: "HOURLY", jobFunction: "Warehouse", payCurrency: "USD", payMin: "18.00", payMax: "24.00" };
const comp = { id: PLAN, code: "ANNUAL-US", name: "US salaried", payBasis: "ANNUAL", currency: "USD" };

const smoke: Array<[string, string, Record<string, Reply>, string]> = [
  ["actions list", "/hr/actions", { "GET /api/v1/hr/actions": json(200, { items: [action()] }) }, "Transfer"],
  ["action detail", `/hr/actions/${ACTION}`, { [`GET /api/v1/hr/actions/${ACTION}`]: json(200, action()) }, "Transfer action"],
  ["new action", "/hr/actions/new", {}, "Submit personnel action"],
  ["jobs list", "/hr/jobs", { "GET /api/v1/hr/jobs": json(200, { items: [job] }) }, "Forklift operator"],
  ["job detail", `/hr/jobs/${job.id}`, { [`GET /api/v1/hr/jobs/${job.id}`]: json(200, job) }, "Job FORKLIFT"],
  ["new job", "/hr/jobs/new", {}, "New job"],
  ["compensation plans", "/hr/compensation/plans", { "GET /api/v1/hr/compensation/plans": json(200, { items: [comp] }) }, "US salaried"],
  ["new compensation plan", "/hr/compensation/plans/new", {}, "New compensation plan"],
];

describe("personnel actions, jobs and compensation plans", () => {
  const api = useFakeApi();

  it.each(smoke)("%s renders its data and passes axe", async (_name, path, table, text) => {
    api.handler = routes(table);
    const { container } = renderAt(path);
    expect((await screen.findAllByText(text)).length).toBeGreaterThan(0);
    expect(pageViolations(container)).toEqual([]);
  });

  it.each([
    ["/hr/actions", "GET /api/v1/hr/actions", "No personnel actions"],
    ["/hr/jobs", "GET /api/v1/hr/jobs", "No jobs yet"],
    ["/hr/compensation/plans", "GET /api/v1/hr/compensation/plans", "No compensation plans"],
  ])("%s shows an empty state and an error with a retry", async (path, key, empty) => {
    api.handler = routes({ [key]: json(200, { items: [] }) });
    const first = renderAt(path);
    expect(await screen.findByText(empty)).toBeTruthy();
    expect(pageViolations(first.container)).toEqual([]);
    first.unmount();

    api.handler = routes({ [key]: problem(500, "COM-0500", "boom") });
    const second = renderAt(path);
    expect((await findAlert()).textContent).toContain("boom");
    expect(screen.getByRole("button", { name: "Try again" })).toBeTruthy();
    expect(pageViolations(second.container)).toEqual([]);
  });

  it("explains a missing or forbidden record", async () => {
    api.handler = routes({ [`GET /api/v1/hr/actions/${ACTION}`]: problem(404, "HCM-ACT-NOT-FOUND", "personnel action not found") });
    const first = renderAt(`/hr/actions/${ACTION}`);
    expect(await screen.findByText("We couldn't find this personnel action")).toBeTruthy();
    first.unmount();

    api.handler = routes({ [`GET /api/v1/hr/jobs/${id(99)}`]: problem(403, "AUTH-0003", "missing duty hr.org.inquire") });
    renderAt(`/hr/jobs/${id(99)}`);
    const alert = await findAlert();
    expect(alert.textContent).toContain("You can't view this job");
    expect(alert.textContent).toContain("You don't have the duty or the legal-entity access this action needs.");
    expect(alert.textContent).toContain("missing duty hr.org.inquire");
  });

  it("filters actions by status through the URL", async () => {
    api.handler = routes({ "GET /api/v1/hr/actions": json(200, { items: [] }) });
    renderAt("/hr/actions?status=PENDING");
    await screen.findByText("No personnel actions");
    expect(api.calls[0]?.url.search).toBe("?status=PENDING");
  });

  it("withdraws a pending action after a confirmation and shows it withdrawn", async () => {
    let withdrawn = false;
    api.handler = routes({
      [`GET /api/v1/hr/actions/${ACTION}`]: () => json(200, action(withdrawn ? { status: "WITHDRAWN" } : {})),
      [`POST /api/v1/hr/actions/${ACTION}/withdraw`]: () => {
        withdrawn = true;
        return json(200, action({ status: "WITHDRAWN" }));
      },
    });
    renderAt(`/hr/actions/${ACTION}`);
    fireEvent.click(await screen.findByRole("button", { name: "Withdraw action" }));
    expect(api.posts("/withdraw")).toHaveLength(0);
    fireEvent.click(screen.getByRole("button", { name: "Withdraw" }));
    expect((await findStatus()).textContent).toBe("The action was withdrawn.");
    expect(api.posts("/withdraw")).toHaveLength(1);
    await waitFor(() => expect(screen.queryByRole("button", { name: "Withdraw action" })).toBeNull());
    expect(screen.getByText(/Only pending actions can be withdrawn/)).toBeTruthy();
  });

  it("shows the conflict when an action was applied before the withdrawal", async () => {
    api.handler = routes({
      [`GET /api/v1/hr/actions/${ACTION}`]: json(200, action()),
      [`POST /api/v1/hr/actions/${ACTION}/withdraw`]: problem(409, "HCM-ACT-STATE", "only a pending action can be withdrawn"),
    });
    renderAt(`/hr/actions/${ACTION}`);
    fireEvent.click(await screen.findByRole("button", { name: "Withdraw action" }));
    fireEvent.click(screen.getByRole("button", { name: "Withdraw" }));
    const alert = await findAlert();
    expect(alert.textContent).toContain("conflicts with the current state");
    expect(alert.textContent).toContain("HCM-ACT-STATE");
  });

  it("does not offer withdrawal for an applied action and says why a failed one failed", async () => {
    api.handler = routes({
      [`GET /api/v1/hr/actions/${ACTION}`]: json(200, action({ status: "FAILED", failureCode: "HCM-ACT-004", failureDetail: "the employment is no longer active" })),
    });
    renderAt(`/hr/actions/${ACTION}`);
    expect(await screen.findByText("This action failed when it came due")).toBeTruthy();
    expect(screen.getByText(/the employment is no longer active/)).toBeTruthy();
    expect(screen.queryByRole("button", { name: "Withdraw action" })).toBeNull();
  });

  it("never invents pay: an amount appears only when the service returned it", async () => {
    api.handler = routes({
      [`GET /api/v1/hr/actions/${ACTION}`]: json(200, action({ actionType: "COMP_CHANGE", payload: {} })),
    });
    const hidden = renderAt(`/hr/actions/${ACTION}`);
    expect(await screen.findByText("The pay amount is not shown to your role.")).toBeTruthy();
    expect(screen.queryByText("Pay")).toBeNull();
    hidden.unmount();

    api.handler = routes({
      [`GET /api/v1/hr/actions/${ACTION}`]: json(200, action({ actionType: "COMP_CHANGE", payload: { compPlanId: PLAN, amount: "90000.00", currency: "USD" } })),
    });
    renderAt(`/hr/actions/${ACTION}`);
    expect(await screen.findByText("90000.00 USD")).toBeTruthy();
    expect(screen.queryByText("The pay amount is not shown to your role.")).toBeNull();
  });

  it("submits a hire with one idempotency key and opens the new action", async () => {
    api.handler = routes({ "POST /api/v1/hr/actions": json(201, action({ actionType: "HIRE", id: id(11) })), [`GET /api/v1/hr/actions/${id(11)}`]: json(200, action({ id: id(11), actionType: "HIRE" })) });
    renderAt("/hr/actions/new");
    fireEvent.change(screen.getByLabelText("Worker id"), { target: { value: WORKER } });
    fireEvent.change(screen.getByLabelText("Effective date"), { target: { value: "2026-11-02" } });
    fireEvent.change(screen.getByLabelText("Legal entity id"), { target: { value: id(5) } });
    fireEvent.click(screen.getByRole("button", { name: "Submit action" }));
    await waitFor(() => expect(screen.getByTestId("where").textContent).toBe(`/hr/actions/${id(11)}`));
    const [post] = api.posts("/actions");
    expect(post?.body).toEqual({ actionType: "HIRE", workerId: WORKER, effectiveDate: "2026-11-02", details: { legalEntityId: id(5), employmentType: "FULL_TIME" } });
    expect(post?.headers.get("Idempotency-Key")).toMatch(/^[0-9a-f-]{36}$/);
  });

  it("refuses an incomplete form without calling the service", async () => {
    renderAt("/hr/actions/new");
    fireEvent.click(screen.getByRole("button", { name: "Submit action" }));
    expect(await screen.findByText("Enter the worker's id.")).toBeTruthy();
    expect(screen.getByText("Choose the effective date.")).toBeTruthy();
    expect(api.calls).toHaveLength(0);
  });

  it("says pay needs the compensation duty when the service answers 403", async () => {
    api.handler = routes({ "POST /api/v1/hr/actions": problem(403, "AUTH-0003", "setting compensation needs the hr.compensation.admin duty") });
    renderAt("/hr/actions/new");
    fireEvent.change(screen.getByLabelText("Action"), { target: { value: "COMP_CHANGE" } });
    fireEvent.change(screen.getByLabelText("Worker id"), { target: { value: WORKER } });
    fireEvent.change(screen.getByLabelText("Employment id"), { target: { value: EMPLOYMENT } });
    fireEvent.change(screen.getByLabelText("Effective date"), { target: { value: "2026-11-02" } });
    fireEvent.change(screen.getByLabelText("Compensation plan id"), { target: { value: PLAN } });
    fireEvent.change(screen.getByLabelText("Amount"), { target: { value: "95000" } });
    fireEvent.change(screen.getByLabelText("Currency"), { target: { value: "usd" } });
    fireEvent.click(screen.getByRole("button", { name: "Submit action" }));
    const alert = await findAlert();
    expect(alert.textContent).toContain("You don't have the duty or the legal-entity access this action needs.");
    expect(alert.textContent).toContain("setting compensation needs the hr.compensation.admin duty");
    expect(api.posts("/actions")[0]?.body).toMatchObject({ details: { compPlanId: PLAN, amount: "95000", currency: "USD" } });
  });

  it("puts a date the service refuses on the date field", async () => {
    api.handler = routes({ "POST /api/v1/hr/actions": problem(422, "HCM-ACT-001", "an action can be at most 365 days ahead") });
    renderAt("/hr/actions/new");
    fireEvent.change(screen.getByLabelText("Worker id"), { target: { value: WORKER } });
    fireEvent.change(screen.getByLabelText("Effective date"), { target: { value: "2028-01-01" } });
    fireEvent.change(screen.getByLabelText("Legal entity id"), { target: { value: id(5) } });
    fireEvent.click(screen.getByRole("button", { name: "Submit action" }));
    expect(await screen.findByText(/at most 365 days ahead/)).toBeTruthy();
  });

  it("validates each action type the way the service does", () => {
    const base: ActionValues = {
      actionType: "TERMINATE", workerId: WORKER, employmentId: EMPLOYMENT, effectiveDate: "2026-11-02", reason: "", legalEntityId: "", employmentType: "FULL_TIME",
      assignmentKind: "", positionId: "", fteShare: "", terminationReason: "", compPlanId: "", amount: "", currency: "",
    };
    expect(validateAction(base).errors.terminationReason).toBeTruthy();
    expect(validateAction({ ...base, terminationReason: "resign" }).input?.details).toEqual({ terminationReason: "RESIGN" });
    expect(validateAction({ ...base, actionType: "TRANSFER" }).errors.positionId).toBeTruthy();
    expect(validateAction({ ...base, actionType: "COMP_CHANGE", compPlanId: PLAN, currency: "JPY", amount: "1.5" }).errors.amount).toBeTruthy();
    expect(validateAction({ ...base, actionType: "HIRE", employmentId: "", legalEntityId: id(5), fteShare: "2.50" }).errors.fteShare).toBeTruthy();
  });

  it("creates a job and puts a duplicate code on the field", async () => {
    api.handler = routes({ "POST /api/v1/hr/jobs": problem(409, "HCM-JOB-DUPLICATE", "job code already exists") });
    renderAt("/hr/jobs/new");
    fireEvent.change(screen.getByLabelText("Code"), { target: { value: "forklift" } });
    fireEvent.change(screen.getByLabelText("Title"), { target: { value: "Forklift operator" } });
    fireEvent.change(screen.getByLabelText("Job type"), { target: { value: "hourly" } });
    fireEvent.click(screen.getByRole("button", { name: "Add job" }));
    expect(await screen.findByText(/job code already exists/)).toBeTruthy();
    expect(api.posts("/jobs")[0]?.body).toEqual({ code: "FORKLIFT", title: "Forklift operator", jobType: "HOURLY" });
  });

  it("takes a pay range only as currency, minimum and maximum together", () => {
    const base = { code: "A", title: "T", jobType: "H", jobFunction: "", payCurrency: "", payMin: "", payMax: "" };
    expect(validateJob({ ...base, payCurrency: "USD" }).errors.payMin).toBeTruthy();
    expect(validateJob({ ...base, payCurrency: "USD", payMin: "20.00", payMax: "10.00" }).errors.payMax).toBeTruthy();
    expect(validateJob({ ...base, payCurrency: "USD", payMin: "9.5", payMax: "10.25" }).input).toMatchObject({ payCurrency: "USD", payMin: "9.5", payMax: "10.25" });
    expect(compareDecimal("10.25", "9.5")).toBe(1);
    expect(compareDecimal("-1.5", "-1.50")).toBe(0);
  });

  it("creates a compensation plan", async () => {
    api.handler = routes({ "POST /api/v1/hr/compensation/plans": json(201, comp), "GET /api/v1/hr/compensation/plans": json(200, { items: [comp] }) });
    renderAt("/hr/compensation/plans/new");
    fireEvent.change(screen.getByLabelText("Code"), { target: { value: "annual-us" } });
    fireEvent.change(screen.getByLabelText("Name"), { target: { value: "US salaried" } });
    fireEvent.change(screen.getByLabelText("Currency"), { target: { value: "usd" } });
    fireEvent.click(screen.getByRole("button", { name: "Add plan" }));
    await waitFor(() => expect(screen.getByTestId("where").textContent).toBe("/hr/compensation/plans"));
    expect(api.posts("/plans")[0]?.body).toEqual({ code: "ANNUAL-US", name: "US salaried", payBasis: "ANNUAL", currency: "USD" });
  });

  it("gates the personnel routes and nav by duty", () => {
    const duty = (path: string) => (hr.routes.find((r) => r.path === path)?.handle as { duty?: string }).duty;
    expect(duty("/hr/actions")).toBe("hr.action.maintain");
    expect(duty("/hr/jobs")).toBe("hr.org.inquire");
    expect(duty("/hr/jobs/new")).toBe("hr.compensation.admin");
    expect(duty("/hr/compensation/plans")).toBe("hr.compensation.admin");
    expect(hr.nav.find((n) => n.to === "/hr/actions")?.duty).toBe("hr.action.maintain");
    expect(hr.nav.find((n) => n.to === "/hr/compensation/plans")?.duty).toBe("hr.compensation.admin");
  });
});
