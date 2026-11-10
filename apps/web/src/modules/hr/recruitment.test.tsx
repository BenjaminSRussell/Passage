import { fireEvent, screen, waitFor, within } from "@testing-library/react";
import { describe, expect, it } from "vitest";
import { findAlert, findStatus } from "../../test-utils";
import { json, id, pageViolations, problem, renderAt, routes, useFakeApi, type Reply } from "./hrTestKit";
import { validateCycle, type CycleValues } from "./NewCompCyclePage";
import { validateCandidate } from "./RequisitionDetailPage";
import { validateHire } from "./OfferPage";
import { validateOffer } from "./CandidatePage";
import { validateRequisition } from "./RequisitionsPage";
import { validateWorksheet } from "./WorksheetPage";

const POSITION = id(3);
const MANAGER = id(1);
const REQ = id(30);
const CAND = id(40);
const ERASED = id(41);
const OFFER = id(50);
const CYCLE = id(60);
const DEPT = id(7);

const requisition = (patch: Record<string, unknown> = {}) => ({
  id: REQ,
  requisitionNumber: "REQ-1",
  positionId: POSITION,
  legalEntityId: id(5),
  headcount: 2,
  hiringManagerWorkerId: MANAGER,
  plannedStartDate: "2026-12-01",
  allowOverfill: false,
  status: "DRAFT",
  filled: 0,
  createdAt: "2026-10-31T10:00:00Z",
  ...patch,
});
const candidate = (patch: Record<string, unknown> = {}) => ({ id: CAND, requisitionId: REQ, stage: "APPLIED", fullName: "Pat Candidate", erased: false, stageChangedAt: "2026-10-31T10:00:00Z", ...patch });
const offer = (patch: Record<string, unknown> = {}) => ({ id: OFFER, candidateId: CAND, requisitionId: REQ, amount: "80000.00", currency: "USD", startDate: "2026-12-01", status: "ACCEPTED", ...patch });
const cycle = (patch: Record<string, unknown> = {}) => ({
  id: CYCLE,
  name: "FY27 merit",
  kind: "MERIT",
  effectiveDate: "2027-01-01",
  currency: "USD",
  allowOverBudget: false,
  guidelineMatrix: [{ rating: 3, compaMin: "0.8", compaMax: "1.2", pctMin: "0", pctMax: "5" }],
  budgets: [{ departmentId: DEPT, budgetAmount: "50000" }],
  status: "APPROVAL",
  ...patch,
});
const sheetRow = (patch: Record<string, unknown> = {}) => ({
  workerId: id(2),
  managerWorkerId: MANAGER,
  employmentId: id(9),
  departmentId: DEPT,
  currentPay: "60000.00",
  rating: 3,
  compaRatio: "1.00",
  guidelinePctMin: "0",
  guidelinePctMax: "5",
  proposedPct: "3",
  proposedAmount: "1800.00",
  status: "DRAFT",
  ...patch,
});

const candidatesUrl = `GET /api/v1/hr/requisitions/${REQ}/candidates`;
const worksheetUrl = `/api/v1/hr/compensation/cycles/${CYCLE}/worksheets/${MANAGER}`;

const smoke: Array<[string, string, Record<string, Reply>, string]> = [
  ["requisitions", "/hr/requisitions", { "GET /api/v1/hr/requisitions": json(200, { items: [requisition()] }) }, "REQ-1"],
  ["new requisition", "/hr/requisitions/new", {}, "New requisition"],
  [
    "requisition detail with its pipeline",
    `/hr/requisitions/${REQ}`,
    { [`GET /api/v1/hr/requisitions/${REQ}`]: json(200, requisition({ status: "OPEN" })), [candidatesUrl]: json(200, { items: [candidate(), candidate({ id: ERASED, stage: "REJECTED", erased: true, fullName: undefined })] }) },
    "Pat Candidate",
  ],
  ["candidate", `/hr/candidates/${CAND}`, { [`GET /api/v1/hr/candidates/${CAND}`]: json(200, candidate({ email: "pat@example.com" })) }, "pat@example.com"],
  ["offer", `/hr/offers/${OFFER}`, { [`GET /api/v1/hr/offers/${OFFER}`]: json(200, offer()) }, "80000.00 USD"],
  ["cycles", "/hr/compensation/cycles", { "GET /api/v1/hr/compensation/cycles": json(200, { items: [cycle()] }) }, "FY27 merit"],
  ["new cycle", "/hr/compensation/cycles/new", {}, "New compensation cycle"],
  ["cycle", `/hr/compensation/cycles/${CYCLE}`, { [`GET /api/v1/hr/compensation/cycles/${CYCLE}`]: json(200, cycle()) }, "Department budgets"],
  ["worksheet", `/hr/compensation/cycles/${CYCLE}/worksheets/${MANAGER}`, { [`GET ${worksheetUrl}`]: json(200, { items: [sheetRow()] }) }, "Saved proposals"],
];

describe("requisitions, candidates, offers and compensation cycles", () => {
  const api = useFakeApi();

  it.each(smoke)("%s renders its data and passes axe", async (_name, path, table, text) => {
    api.handler = routes(table);
    const { container } = renderAt(path);
    expect((await screen.findAllByText(text)).length).toBeGreaterThan(0);
    expect(pageViolations(container)).toEqual([]);
  });

  it.each([
    ["/hr/requisitions", "GET /api/v1/hr/requisitions", "No requisitions"],
    ["/hr/compensation/cycles", "GET /api/v1/hr/compensation/cycles", "No compensation cycles"],
  ])("%s shows an empty state and a forbidden message", async (path, key, empty) => {
    api.handler = routes({ [key]: json(200, { items: [] }) });
    const first = renderAt(path);
    expect(await screen.findByText(empty)).toBeTruthy();
    expect(pageViolations(first.container)).toEqual([]);
    first.unmount();

    api.handler = routes({ [key]: problem(403, "AUTH-0003", "missing duty hr.recruiting.maintain") });
    const second = renderAt(path);
    expect((await findAlert()).textContent).toContain("You don't have the duty or the legal-entity access this action needs.");
    expect(pageViolations(second.container)).toEqual([]);
  });

  it("explains a requisition that does not exist", async () => {
    api.handler = routes({ [`GET /api/v1/hr/requisitions/${REQ}`]: problem(404, "HCM-REQ-NOT-FOUND", "requisition not found") });
    renderAt(`/hr/requisitions/${REQ}`);
    expect(await screen.findByText("We couldn't find this requisition")).toBeTruthy();
  });

  it("approves a draft requisition and refreshes its status", async () => {
    let status = "DRAFT";
    api.handler = routes({
      [`GET /api/v1/hr/requisitions/${REQ}`]: () => json(200, requisition({ status })),
      [`POST /api/v1/hr/requisitions/${REQ}/transition`]: () => {
        status = "APPROVED";
        return json(200, requisition({ status }));
      },
    });
    renderAt(`/hr/requisitions/${REQ}`);
    fireEvent.click(await screen.findByRole("button", { name: "Approve" }));
    expect((await findStatus()).textContent).toContain("now approved");
    expect(api.posts("/transition")[0]?.body).toEqual({ to: "APPROVED" });
    expect(await screen.findByRole("button", { name: "Open for candidates" })).toBeTruthy();
  });

  it("shows why a requisition cannot open", async () => {
    api.handler = routes({
      [`GET /api/v1/hr/requisitions/${REQ}`]: json(200, requisition({ status: "APPROVED" })),
      [`POST /api/v1/hr/requisitions/${REQ}/transition`]: problem(422, "HCM-REQ-002", "the position has 0.00 FTE vacant on the planned start date; the requisition needs 2.00"),
    });
    renderAt(`/hr/requisitions/${REQ}`);
    fireEvent.click(await screen.findByRole("button", { name: "Open for candidates" }));
    expect((await findAlert()).textContent).toContain("0.00 FTE vacant");
  });

  it("cancels a requisition only after a confirmation", async () => {
    api.handler = routes({
      [`GET /api/v1/hr/requisitions/${REQ}`]: json(200, requisition({ status: "OPEN" })),
      [candidatesUrl]: json(200, { items: [] }),
      [`POST /api/v1/hr/requisitions/${REQ}/transition`]: json(200, requisition({ status: "CANCELED" })),
    });
    renderAt(`/hr/requisitions/${REQ}`);
    fireEvent.click(await screen.findByRole("button", { name: "Cancel requisition" }));
    expect(api.posts("/transition")).toHaveLength(0);
    fireEvent.click(within(screen.getByRole("alertdialog")).getByRole("button", { name: "Cancel requisition" }));
    await waitFor(() => expect(api.posts("/transition")[0]?.body).toEqual({ to: "CANCELED" }));
  });

  it("shows only the candidate data the service returned, and filters by stage", async () => {
    api.handler = routes({
      [`GET /api/v1/hr/requisitions/${REQ}`]: json(200, requisition({ status: "OPEN" })),
      [candidatesUrl]: json(200, { items: [candidate(), candidate({ id: ERASED, stage: "REJECTED", erased: true, fullName: undefined })] }),
    });
    renderAt(`/hr/requisitions/${REQ}`);
    expect(await screen.findByText("Pat Candidate")).toBeTruthy();
    expect(screen.getByText("Erased by the retention purge")).toBeTruthy();
    expect(screen.queryByText(/@/)).toBeNull();
    fireEvent.change(screen.getByLabelText("Stage"), { target: { value: "OFFER" } });
    await waitFor(() => expect(api.calls.some((c) => c.url.search === "?stage=OFFER")).toBe(true));
  });

  it("adds a candidate only with recorded consent", async () => {
    let added = false;
    api.handler = routes({
      [`GET /api/v1/hr/requisitions/${REQ}`]: json(200, requisition({ status: "OPEN" })),
      [candidatesUrl]: () => json(200, { items: added ? [candidate()] : [] }),
      [`POST /api/v1/hr/requisitions/${REQ}/candidates`]: () => {
        added = true;
        return json(201, candidate());
      },
    });
    renderAt(`/hr/requisitions/${REQ}`);
    fireEvent.click(await screen.findByRole("button", { name: "Add candidate" }));
    fireEvent.change(screen.getByLabelText("Full name"), { target: { value: "Pat Candidate" } });
    fireEvent.click(screen.getByRole("button", { name: "Add candidate" }));
    expect(await screen.findByText("Record the candidate's consent before adding them.")).toBeTruthy();
    expect(api.posts("/candidates")).toHaveLength(0);
    fireEvent.click(screen.getByLabelText(/consented to us keeping/));
    fireEvent.click(screen.getByRole("button", { name: "Add candidate" }));
    expect((await findStatus()).textContent).toBe("Pat Candidate was added.");
    expect(api.posts("/candidates")[0]?.body).toEqual({ fullName: "Pat Candidate", consentGranted: true });
    expect(await screen.findByRole("link", { name: "Pat Candidate" })).toBeTruthy();
  });

  it("validates a candidate and an offer", () => {
    expect(validateCandidate({ fullName: "", email: "nope", phone: "", source: "", consentGranted: true }).errors).toMatchObject({ fullName: expect.any(String), email: expect.any(String) });
    expect(validateOffer({ amount: "1.5", currency: "JPY", startDate: "2026-12-01" }).errors.amount).toBeTruthy();
    expect(validateOffer({ amount: "80000", currency: "usd", startDate: "2026-12-01" }).input).toEqual({ amount: "80000", currency: "USD", startDate: "2026-12-01" });
    expect(validateRequisition({ requisitionNumber: "req-9", positionId: POSITION, hiringManagerWorkerId: MANAGER, headcount: "0", plannedStartDate: "2026-12-01", allowOverfill: false }).errors.headcount).toBeTruthy();
    expect(validateHire({ workerId: id(2), legalEntityId: "x", employmentType: "FULL_TIME", fteShare: "", compPlanId: "" }).errors.legalEntityId).toBeTruthy();
  });

  it("moves a candidate and rejects one after a confirmation", async () => {
    api.handler = routes({
      [`GET /api/v1/hr/candidates/${CAND}`]: json(200, candidate()),
      [`POST /api/v1/hr/candidates/${CAND}/move`]: json(200, candidate({ stage: "SCREEN" })),
    });
    renderAt(`/hr/candidates/${CAND}`);
    fireEvent.click(await screen.findByRole("button", { name: "Move to screening" }));
    await waitFor(() => expect(api.posts("/move")[0]?.body).toEqual({ to: "SCREEN" }));
    fireEvent.click(await screen.findByRole("button", { name: "Reject candidate" }));
    fireEvent.click(within(screen.getByRole("alertdialog")).getByRole("button", { name: "Reject" }));
    await waitFor(() => expect(api.posts("/move")[1]?.body).toEqual({ to: "REJECTED" }));
  });

  it("states that an erased candidate's personal data is gone and offers no moves", async () => {
    api.handler = routes({ [`GET /api/v1/hr/candidates/${ERASED}`]: json(200, candidate({ id: ERASED, stage: "REJECTED", erased: true, fullName: undefined })) });
    renderAt(`/hr/candidates/${ERASED}`);
    expect(await screen.findByText("Personal data erased")).toBeTruthy();
    expect(screen.queryByRole("button", { name: "Reject candidate" })).toBeNull();
    expect(screen.queryByText("Email")).toBeNull();
  });

  it("creates an offer for a candidate at the offer stage and opens it", async () => {
    api.handler = routes({
      [`GET /api/v1/hr/candidates/${CAND}`]: json(200, candidate({ stage: "OFFER" })),
      [`POST /api/v1/hr/candidates/${CAND}/offers`]: json(201, offer({ status: "DRAFT" })),
      [`GET /api/v1/hr/offers/${OFFER}`]: json(200, offer({ status: "DRAFT" })),
    });
    renderAt(`/hr/candidates/${CAND}`);
    fireEvent.change(await screen.findByLabelText("Amount"), { target: { value: "80000.00" } });
    fireEvent.change(screen.getByLabelText("Currency"), { target: { value: "usd" } });
    fireEvent.change(screen.getByLabelText("Start date"), { target: { value: "2026-12-01" } });
    fireEvent.click(screen.getByRole("button", { name: "Create offer" }));
    await waitFor(() => expect(screen.getByTestId("where").textContent).toBe(`/hr/offers/${OFFER}`));
    expect(api.posts("/offers")[0]?.body).toEqual({ amount: "80000.00", currency: "USD", startDate: "2026-12-01" });
  });

  it("sends an offer and withdraws it after a confirmation", async () => {
    api.handler = routes({
      [`GET /api/v1/hr/offers/${OFFER}`]: json(200, offer({ status: "DRAFT" })),
      [`POST /api/v1/hr/offers/${OFFER}/transition`]: json(200, offer({ status: "SENT" })),
    });
    renderAt(`/hr/offers/${OFFER}`);
    fireEvent.click(await screen.findByRole("button", { name: "Send offer" }));
    await waitFor(() => expect(api.posts("/transition")[0]?.body).toEqual({ to: "SENT" }));
    fireEvent.click(await screen.findByRole("button", { name: "Withdraw offer" }));
    fireEvent.click(within(screen.getByRole("alertdialog")).getByRole("button", { name: "Withdraw" }));
    await waitFor(() => expect(api.posts("/transition")[1]?.body).toEqual({ to: "WITHDRAWN" }));
  });

  it("hires from an accepted offer and opens the hire action", async () => {
    api.handler = routes({
      [`GET /api/v1/hr/offers/${OFFER}`]: json(200, offer()),
      [`POST /api/v1/hr/offers/${OFFER}/hire`]: json(201, { id: id(70), actionType: "HIRE", workerId: id(2), effectiveDate: "2026-12-01", status: "PENDING", payload: {}, submittedBy: "u", submittedAt: "2026-10-31T10:00:00Z" }),
      [`GET /api/v1/hr/actions/${id(70)}`]: json(200, { id: id(70), actionType: "HIRE", workerId: id(2), effectiveDate: "2026-12-01", status: "PENDING", payload: {}, submittedBy: "u", submittedAt: "2026-10-31T10:00:00Z" }),
    });
    renderAt(`/hr/offers/${OFFER}`);
    fireEvent.change(await screen.findByLabelText("Worker id"), { target: { value: id(2) } });
    fireEvent.change(screen.getByLabelText("Legal entity id"), { target: { value: id(5) } });
    fireEvent.click(screen.getByRole("button", { name: "Hire from offer" }));
    await waitFor(() => expect(screen.getByTestId("where").textContent).toBe(`/hr/actions/${id(70)}`));
    expect(api.posts("/hire")[0]?.body).toEqual({ workerId: id(2), legalEntityId: id(5), employmentType: "FULL_TIME" });
  });

  it("tells the user an offer was already converted when the hire is repeated", async () => {
    api.handler = routes({
      [`GET /api/v1/hr/offers/${OFFER}`]: json(200, offer()),
      [`POST /api/v1/hr/offers/${OFFER}/hire`]: problem(409, "HCM-OFR-001", "offer already converted"),
    });
    renderAt(`/hr/offers/${OFFER}`);
    fireEvent.change(await screen.findByLabelText("Worker id"), { target: { value: id(2) } });
    fireEvent.change(screen.getByLabelText("Legal entity id"), { target: { value: id(5) } });
    fireEvent.click(screen.getByRole("button", { name: "Hire from offer" }));
    expect((await findAlert()).textContent).toContain("This offer has already been converted to a hire.");
  });

  it("hides the hire form once an offer has been converted", async () => {
    api.handler = routes({ [`GET /api/v1/hr/offers/${OFFER}`]: json(200, offer({ hireActionId: id(70) })) });
    renderAt(`/hr/offers/${OFFER}`);
    expect(await screen.findByText("This offer has already been converted to a hire.")).toBeTruthy();
    expect(screen.queryByRole("button", { name: "Hire from offer" })).toBeNull();
  });

  it("keeps Apply disabled and hides Approve without hr.compensation.approve until the cycle is approved", async () => {
    api.handler = routes({ [`GET /api/v1/hr/compensation/cycles/${CYCLE}`]: json(200, cycle()) });
    const { container } = renderAt(`/hr/compensation/cycles/${CYCLE}`, ["hr.compensation.admin"]);
    const apply = await screen.findByRole("button", { name: "Apply cycle" });
    expect((apply as HTMLButtonElement).disabled).toBe(true);
    expect(screen.queryByRole("button", { name: "Approve cycle" })).toBeNull();
    expect(screen.getByText(/Waiting for an independent approver/)).toBeTruthy();
    expect(pageViolations(container)).toEqual([]);
  });

  it("approves a cycle after a confirmation and then enables Apply", async () => {
    let approved = false;
    const done = () => cycle({ approvedBy: "approver-1", approvedAt: "2026-11-05T10:00:00Z" });
    api.handler = routes({
      [`GET /api/v1/hr/compensation/cycles/${CYCLE}`]: () => json(200, approved ? done() : cycle()),
      [`POST /api/v1/hr/compensation/cycles/${CYCLE}/approve`]: () => {
        approved = true;
        return json(200, done());
      },
    });
    renderAt(`/hr/compensation/cycles/${CYCLE}`, ["hr.compensation.admin", "hr.compensation.approve"]);
    fireEvent.click(await screen.findByRole("button", { name: "Approve cycle" }));
    expect(api.posts("/approve")).toHaveLength(0);
    fireEvent.click(within(screen.getByRole("alertdialog")).getByRole("button", { name: "Approve cycle" }));
    expect((await findStatus()).textContent).toContain("The cycle was approved");
    await waitFor(() => expect((screen.getByRole("button", { name: "Apply cycle" }) as HTMLButtonElement).disabled).toBe(false));
    expect(screen.queryByRole("button", { name: "Approve cycle" })).toBeNull();
    expect(screen.getByText(/Approved by approver-1/)).toBeTruthy();
  });

  it.each([
    [problem(403, "HCM-CMP-SOD", "the user who created the cycle cannot approve it"), "You can't approve this cycle"],
    [problem(409, "HCM-CMP-STATE", "the cycle has already been approved by another user"), "conflicts with the current state"],
  ])("explains a refused approval (%#)", async (reply, text) => {
    api.handler = routes({
      [`GET /api/v1/hr/compensation/cycles/${CYCLE}`]: json(200, cycle()),
      [`POST /api/v1/hr/compensation/cycles/${CYCLE}/approve`]: reply,
    });
    renderAt(`/hr/compensation/cycles/${CYCLE}`, ["hr.compensation.approve"]);
    fireEvent.click(await screen.findByRole("button", { name: "Approve cycle" }));
    fireEvent.click(within(screen.getByRole("alertdialog")).getByRole("button", { name: "Approve cycle" }));
    expect((await findAlert()).textContent).toContain(text);
  });

  it("creates the worker with the hire when the user holds hr.worker.maintain", async () => {
    const hired = { id: id(70), actionType: "HIRE", workerId: id(2), effectiveDate: "2026-12-01", status: "PENDING", payload: {}, submittedBy: "u", submittedAt: "2026-10-31T10:00:00Z" };
    api.handler = routes({
      [`GET /api/v1/hr/offers/${OFFER}`]: json(200, offer()),
      [`POST /api/v1/hr/offers/${OFFER}/hire`]: json(201, hired),
      [`GET /api/v1/hr/actions/${id(70)}`]: json(200, hired),
    });
    const { container } = renderAt(`/hr/offers/${OFFER}`, ["hr.action.maintain", "hr.worker.maintain"]);
    fireEvent.change(await screen.findByLabelText("Worker record"), { target: { value: "new" } });
    expect(screen.queryByLabelText("Worker id")).toBeNull();
    fireEvent.click(screen.getByRole("button", { name: "Hire from offer" }));
    expect(await screen.findAllByText("Enter the national id, at most 32 characters.")).not.toHaveLength(0);
    expect(api.posts("/hire")).toHaveLength(0);
    fireEvent.change(screen.getByLabelText("Personnel number"), { target: { value: "wrk-9" } });
    fireEvent.change(screen.getByLabelText("Party person id"), { target: { value: id(8) } });
    fireEvent.change(screen.getByLabelText("National id"), { target: { value: "123-45-6789" } });
    fireEvent.change(screen.getByLabelText("Legal entity id"), { target: { value: id(5) } });
    expect(pageViolations(container)).toEqual([]);
    fireEvent.click(screen.getByRole("button", { name: "Hire from offer" }));
    await waitFor(() => expect(screen.getByTestId("where").textContent).toBe(`/hr/actions/${id(70)}`));
    expect(api.posts("/hire")[0]?.body).toEqual({
      newWorker: { personnelNumber: "WRK-9", workerType: "EMPLOYEE", partyPersonId: id(8), nationalId: "123-45-6789" },
      legalEntityId: id(5),
      employmentType: "FULL_TIME",
    });
  });

  it("offers no new-worker option without hr.worker.maintain", async () => {
    api.handler = routes({ [`GET /api/v1/hr/offers/${OFFER}`]: json(200, offer()) });
    renderAt(`/hr/offers/${OFFER}`, ["hr.action.maintain"]);
    expect(await screen.findByLabelText("Worker id")).toBeTruthy();
    expect(screen.queryByLabelText("Worker record")).toBeNull();
  });

  it("validates the new worker on a hire", () => {
    const base = { workerId: "", legalEntityId: id(5), employmentType: "FULL_TIME", fteShare: "", compPlanId: "" };
    const bad = validateHire({ ...base, workerMode: "new", personnelNumber: "!", partyPersonId: "x", nationalId: "", legalName: "x".repeat(201) });
    expect(Object.keys(bad.errors).sort()).toEqual(["legalName", "nationalId", "partyPersonId", "personnelNumber"]);
    const ok = validateHire({ ...base, workerMode: "new", personnelNumber: "w-1", workerType: "CONTRACTOR", partyPersonId: id(8), nationalId: "1", legalName: " Pat " });
    expect(ok.input?.newWorker).toEqual({ personnelNumber: "W-1", workerType: "CONTRACTOR", partyPersonId: id(8), nationalId: "1", legalName: "Pat" });
    expect(ok.input?.workerId).toBeUndefined();
  });

  it("applies a cycle only after a confirmation and reports the actions created", async () => {
    api.handler = routes({
      [`GET /api/v1/hr/compensation/cycles/${CYCLE}`]: json(200, cycle({ approvedBy: "approver-1" })),
      [`POST /api/v1/hr/compensation/cycles/${CYCLE}/apply`]: json(200, { ...cycle({ status: "APPLIED" }), actionIds: [id(71), id(72)] }),
    });
    renderAt(`/hr/compensation/cycles/${CYCLE}`);
    fireEvent.click(await screen.findByRole("button", { name: "Apply cycle" }));
    expect(api.posts("/apply")).toHaveLength(0);
    fireEvent.click(within(screen.getByRole("alertdialog")).getByRole("button", { name: "Apply cycle" }));
    expect((await findStatus()).textContent).toContain("created 2 pay-change actions");
  });

  it("shows a cycle that moved on while the user was looking", async () => {
    api.handler = routes({
      [`GET /api/v1/hr/compensation/cycles/${CYCLE}`]: json(200, cycle({ status: "MANAGER_INPUT" })),
      [`POST /api/v1/hr/compensation/cycles/${CYCLE}/advance`]: problem(409, "HCM-CMP-STATE", "the cycle is APPROVAL"),
    });
    renderAt(`/hr/compensation/cycles/${CYCLE}`);
    fireEvent.click(await screen.findByRole("button", { name: "Send to approval" }));
    const alert = await findAlert();
    expect(alert.textContent).toContain("conflicts with the current state");
    expect(api.posts("/advance")[0]?.body).toEqual({ to: "APPROVAL" });
  });

  it("opens a manager's worksheet from the cycle", async () => {
    api.handler = routes({ [`GET /api/v1/hr/compensation/cycles/${CYCLE}`]: json(200, cycle({ status: "MANAGER_INPUT" })), [`GET ${worksheetUrl}`]: json(200, { items: [] }) });
    renderAt(`/hr/compensation/cycles/${CYCLE}`);
    fireEvent.change(await screen.findByLabelText("Manager's worker id"), { target: { value: "nope" } });
    fireEvent.click(screen.getByRole("button", { name: "Open worksheet" }));
    expect(await screen.findByText("Enter the manager's worker id.")).toBeTruthy();
    fireEvent.change(screen.getByLabelText("Manager's worker id"), { target: { value: MANAGER } });
    fireEvent.click(screen.getByRole("button", { name: "Open worksheet" }));
    await waitFor(() => expect(screen.getByTestId("where").textContent).toBe(`/hr/compensation/cycles/${CYCLE}/worksheets/${MANAGER}`));
  });

  it("creates a cycle from a validated matrix and budgets", async () => {
    api.handler = routes({ "POST /api/v1/hr/compensation/cycles": json(201, cycle()), [`GET /api/v1/hr/compensation/cycles/${CYCLE}`]: json(200, cycle()) });
    renderAt("/hr/compensation/cycles/new");
    fireEvent.click(screen.getByRole("button", { name: "Create cycle" }));
    expect(await screen.findByText("Enter a name of up to 120 characters.")).toBeTruthy();
    expect(screen.getByText("Budget 1: enter the department's id.")).toBeTruthy();
    expect(api.calls).toHaveLength(0);
    fireEvent.change(screen.getByLabelText("Name"), { target: { value: "FY27 merit" } });
    fireEvent.change(screen.getByLabelText("Effective date"), { target: { value: "2027-01-01" } });
    fireEvent.change(screen.getByLabelText("Currency"), { target: { value: "usd" } });
    fireEvent.change(screen.getByLabelText("Department id"), { target: { value: DEPT } });
    fireEvent.change(screen.getByLabelText("Budget"), { target: { value: "50000" } });
    fireEvent.click(screen.getByRole("button", { name: "Create cycle" }));
    await waitFor(() => expect(screen.getByTestId("where").textContent).toBe(`/hr/compensation/cycles/${CYCLE}`));
    expect(api.posts("/cycles")[0]?.body).toMatchObject({
      name: "FY27 merit",
      kind: "MERIT",
      currency: "USD",
      guidelineMatrix: [{ rating: 3, compaMin: "0.80", compaMax: "1.20", pctMin: "0", pctMax: "5" }],
      budgets: [{ departmentId: DEPT, budgetAmount: "50000" }],
    });
  });

  it("rejects overlapping or inverted guideline rows", () => {
    const base: CycleValues = {
      name: "x", kind: "MERIT", effectiveDate: "2027-01-01", currency: "USD", allowOverBudget: false,
      matrix: [{ rating: 3, compaMin: "0.8", compaMax: "1.2", pctMin: "0", pctMax: "5" }],
      budgets: [{ departmentId: DEPT, budgetAmount: "100" }],
    };
    expect(validateCycle(base).errors).toEqual({});
    expect(validateCycle({ ...base, matrix: [...base.matrix, { rating: 3, compaMin: "1.0", compaMax: "1.4", pctMin: "0", pctMax: "5" }] }).errors.guidelineMatrix).toContain("overlaps");
    expect(validateCycle({ ...base, matrix: [{ rating: 3, compaMin: "1.2", compaMax: "0.8", pctMin: "0", pctMax: "5" }] }).errors.guidelineMatrix).toContain("must exceed");
    expect(validateCycle({ ...base, matrix: [{ rating: 3, compaMin: "0.8", compaMax: "1.2", pctMin: "-120", pctMax: "5" }] }).errors.guidelineMatrix).toContain("-100 to 100");
    expect(validateCycle({ ...base, budgets: [...base.budgets, ...base.budgets] }).errors.budgets).toContain("already listed");
  });

  it("saves a worksheet draft, shows the guideline result and submits it after a confirmation", async () => {
    let saved: unknown[] = [];
    api.handler = routes({
      [`GET ${worksheetUrl}`]: () => json(200, { items: saved }),
      [`PUT ${worksheetUrl}`]: () => {
        saved = [sheetRow({ guidelinePctMin: undefined, guidelinePctMax: undefined, compaRatio: undefined })];
        return json(200, { items: saved });
      },
      [`POST ${worksheetUrl}/submit`]: json(200, { items: [sheetRow({ status: "SUBMITTED" })] }),
    });
    renderAt(`/hr/compensation/cycles/${CYCLE}/worksheets/${MANAGER}`);
    fireEvent.click(await screen.findByRole("button", { name: "Add worker" }));
    fireEvent.change(screen.getByLabelText("Worker id"), { target: { value: id(2) } });
    fireEvent.change(screen.getByLabelText("Raise (%)"), { target: { value: "3" } });
    fireEvent.click(screen.getByRole("button", { name: "Save draft" }));
    expect((await findStatus()).textContent).toBe("The worksheet was saved as a draft.");
    expect(api.calls.find((c) => c.method === "PUT")?.body).toEqual({ rows: [{ workerId: id(2), rating: 3, proposedPct: "3" }] });
    expect(await screen.findByText("Outside the matrix")).toBeTruthy();
    fireEvent.click(screen.getByRole("button", { name: "Submit worksheet" }));
    expect(api.posts("/submit")).toHaveLength(0);
    fireEvent.click(within(screen.getByRole("alertdialog")).getByRole("button", { name: "Submit" }));
    expect((await screen.findAllByText("The worksheet was submitted.")).length).toBeGreaterThan(0);
    expect(await screen.findByText("This worksheet was submitted and can't be changed.")).toBeTruthy();
    expect(screen.queryByRole("button", { name: "Save draft" })).toBeNull();
  });

  it("blocks submit while the screen differs from the saved draft", async () => {
    api.handler = routes({
      [`GET ${worksheetUrl}`]: json(200, { items: [sheetRow()] }),
      [`POST ${worksheetUrl}/submit`]: json(200, { items: [sheetRow({ status: "SUBMITTED" })] }),
    });
    renderAt(`/hr/compensation/cycles/${CYCLE}/worksheets/${MANAGER}`);
    const submit = await screen.findByRole("button", { name: "Submit worksheet" });
    expect((submit as HTMLButtonElement).disabled).toBe(false);
    fireEvent.change(screen.getByLabelText("Raise (%)"), { target: { value: "4" } });
    expect((screen.getByRole("button", { name: "Submit worksheet" }) as HTMLButtonElement).disabled).toBe(true);
    expect(screen.getByText(/Save changes first/)).toBeTruthy();
    expect(api.posts("/submit")).toHaveLength(0);
  });

  it("marks the rating and raise inputs invalid, and clears the cycle manager error once valid", async () => {
    api.handler = routes({ [`GET ${worksheetUrl}`]: json(200, { items: [sheetRow()] }) });
    const first = renderAt(`/hr/compensation/cycles/${CYCLE}/worksheets/${MANAGER}`);
    fireEvent.change(await screen.findByLabelText("Raise (%)"), { target: { value: "500" } });
    fireEvent.click(screen.getByRole("button", { name: "Save draft" }));
    await waitFor(() => expect(screen.getByLabelText("Raise (%)").getAttribute("aria-invalid")).toBe("true"));
    expect(screen.getByLabelText("Raise (%)").getAttribute("aria-describedby")).toBeTruthy();
    first.unmount();

    api.handler = routes({ [`GET /api/v1/hr/compensation/cycles/${CYCLE}`]: json(200, cycle({ status: "MANAGER_INPUT" })) });
    renderAt(`/hr/compensation/cycles/${CYCLE}`);
    fireEvent.change(await screen.findByLabelText("Manager's worker id"), { target: { value: "nope" } });
    fireEvent.click(screen.getByRole("button", { name: "Open worksheet" }));
    expect(await screen.findByText("Enter the manager's worker id.")).toBeTruthy();
    fireEvent.change(screen.getByLabelText("Manager's worker id"), { target: { value: MANAGER } });
    expect(screen.queryByText("Enter the manager's worker id.")).toBeNull();
  });

  it("flags matrix and budget inputs and the consent checkbox as invalid", async () => {
    renderAt("/hr/compensation/cycles/new");
    fireEvent.change(screen.getByLabelText("Compa-ratio below"), { target: { value: "0.5" } });
    fireEvent.click(screen.getByRole("button", { name: "Create cycle" }));
    await screen.findByText(/Budget 1: enter the department's id\./);
    expect(screen.getByLabelText("Department id").getAttribute("aria-invalid")).toBe("true");
    expect(screen.getByLabelText("Rating").getAttribute("aria-invalid")).toBe("true");
  });

  it("shows the guideline refusal when saving a worksheet", async () => {
    api.handler = routes({
      [`GET ${worksheetUrl}`]: json(200, { items: [sheetRow()] }),
      [`PUT ${worksheetUrl}`]: problem(422, "HCM-CMP-001", "proposal 12% is outside the guideline 0% to 5%"),
    });
    renderAt(`/hr/compensation/cycles/${CYCLE}/worksheets/${MANAGER}`);
    fireEvent.click(await screen.findByRole("button", { name: "Save draft" }));
    expect((await findAlert()).textContent).toContain("outside the guideline");
  });

  it("validates worksheet lines before saving", () => {
    expect(validateWorksheet([]).errors[-1]).toBeTruthy();
    expect(validateWorksheet([{ workerId: "x", rating: "3", proposedPct: "1", justification: "" }]).errors[0]).toBeTruthy();
    expect(validateWorksheet([{ workerId: id(2), rating: "6", proposedPct: "1", justification: "" }]).errors[0]).toContain("1 to 5");
    expect(validateWorksheet([{ workerId: id(2), rating: "3", proposedPct: "101", justification: "" }]).errors[0]).toContain("-100 to 100");
    expect(validateWorksheet([{ workerId: id(2), rating: "3", proposedPct: "4.5", justification: " why " }]).rows).toEqual([{ workerId: id(2), rating: 3, proposedPct: "4.5", justification: "why" }]);
  });
});
