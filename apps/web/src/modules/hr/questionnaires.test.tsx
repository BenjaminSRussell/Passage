import { fireEvent, screen, waitFor } from "@testing-library/react";
import { describe, expect, it } from "vitest";
import { findAlert } from "../../test-utils";
import hr from "./module";
import { json, id, pageViolations, problem, renderAt, routes, useFakeApi, type Reply } from "./hrTestKit";
import { validateAnswers } from "./MyQuestionnairesPage";
import { parseOptions, validateQuestionnaire } from "./NewQuestionnairePage";
import { validateSchedule } from "./QuestionnairesPage";

const WORKER = id(1);
const QUESTIONNAIRE = id(110);
const SCHEDULE = id(111);

const questions = [
  { code: "Q1", text: "How satisfied are you?", type: "SCALE", scaleMax: 5, weight: "1", mandatory: true, resultGroup: "SAT" },
  { code: "Q2", text: "Favourite colour", type: "SINGLE", options: [{ value: "RED", label: "Red" }, { value: "BLUE", label: "Blue" }], weight: "1", mandatory: false },
  { code: "Q3", text: "Pick any", type: "MULTI", options: [{ value: "X", label: "Ex" }, { value: "Y", label: "Why" }], weight: "1", mandatory: false },
  { code: "Q4", text: "Comments", type: "TEXT", weight: "1", mandatory: false },
  { code: "Q5", text: "Hours slept", type: "NUMERIC", weight: "1", mandatory: false },
  { code: "Q6", text: "Start date", type: "DATE", weight: "1", mandatory: false },
];
const questionnaire = (patch: Record<string, unknown> = {}) => ({ id: QUESTIONNAIRE, code: "PULSE", name: "Pulse survey", anonymous: true, questions, ...patch });
const schedule = (patch: Record<string, unknown> = {}) => ({ id: SCHEDULE, questionnaireId: QUESTIONNAIRE, population: "", opensAt: "2026-10-01T00:00:00Z", closesAt: "2026-11-30T00:00:00Z", invitedCount: 40, status: "OPEN", ...patch });
const view = (patch: Record<string, unknown> = {}) => ({ ...schedule(patch), questionnaire: questionnaire() });
const stats = { respondents: 12, questions: [{ code: "Q1", type: "SCALE", answered: 12, mean: "4.10", distribution: { "4": 8, "5": 4 } }], resultGroups: [{ code: "SAT", responses: 12, meanScore: "4.10" }] };
const results = (patch: Record<string, unknown> = {}) => ({ scheduleId: SCHEDULE, anonymous: true, invited: 40, responded: 12, responseRate: "30.00", overall: stats, suppressed: false, ...patch });
const me = {
  id: WORKER, personnelNumber: "W-1", workerType: "EMPLOYEE", partyPersonId: WORKER, legalName: "Pat", createdAt: "2026-01-01T00:00:00Z", updatedAt: "2026-01-01T00:00:00Z",
  pii: { nationalId: "***", masked: true },
  asOf: { workerId: WORKER, date: "2026-10-31", employments: [], positionId: null, positionNumber: null, fteShare: null, managerWorkerId: null },
};

const resultsUrl = `/api/v1/hr/questionnaires/schedules/${SCHEDULE}/results`;

const smoke: Array<[string, string, Record<string, Reply>, string]> = [
  ["questionnaires", "/hr/questionnaires", { "GET /api/v1/hr/questionnaires": json(200, { items: [questionnaire()] }) }, "Pulse survey"],
  ["new questionnaire", "/hr/questionnaires/new", {}, "New questionnaire"],
  ["questionnaire", `/hr/questionnaires/${QUESTIONNAIRE}`, { [`GET /api/v1/hr/questionnaires/${QUESTIONNAIRE}`]: json(200, questionnaire()) }, "Hours slept"],
  ["results", `/hr/questionnaires/schedules/${SCHEDULE}/results`, { [`GET ${resultsUrl}`]: json(200, results()) }, "Response rate"],
  [
    "my questionnaires",
    "/hr/my/questionnaires",
    { "GET /api/v1/hr/questionnaires/schedules/open": json(200, { items: [schedule()] }), [`GET /api/v1/hr/questionnaires/schedules/${SCHEDULE}`]: json(200, view()) },
    "Pulse survey",
  ],
  [
    "answer a questionnaire",
    `/hr/my/questionnaires/${SCHEDULE}`,
    { [`GET /api/v1/hr/questionnaires/schedules/${SCHEDULE}`]: json(200, view()), "GET /api/v1/hr/me": json(200, me) },
    "Submit answers",
  ],
];

describe("questionnaires", () => {
  const api = useFakeApi();

  it.each(smoke)("%s renders its data and passes axe", async (_name, path, table, text) => {
    api.handler = routes(table);
    const { container } = renderAt(path);
    expect((await screen.findAllByText(text)).length).toBeGreaterThan(0);
    expect(pageViolations(container)).toEqual([]);
  });

  it.each([
    ["/hr/questionnaires", "GET /api/v1/hr/questionnaires", "No questionnaires"],
    ["/hr/my/questionnaires", "GET /api/v1/hr/questionnaires/schedules/open", "Nothing to answer"],
  ])("%s shows an empty state and an error with a retry", async (path, key, empty) => {
    api.handler = routes({ [key]: json(200, { items: [] }) });
    const first = renderAt(path);
    expect(await screen.findByText(empty)).toBeTruthy();
    expect(pageViolations(first.container)).toEqual([]);
    first.unmount();

    api.handler = routes({ [key]: problem(500, "COM-0500", "boom") });
    renderAt(path);
    expect((await findAlert()).textContent).toContain("boom (COM-0500)");
    expect(screen.getByRole("button", { name: "Try again" })).toBeTruthy();
  });

  it("shows the suppressed state instead of statistics for an anonymous questionnaire with too few responses", async () => {
    api.handler = routes({ [`GET ${resultsUrl}`]: json(200, results({ responded: 3, responseRate: "7.50", suppressed: true, overall: { respondents: 3, questions: [], resultGroups: [] } })) });
    const { container } = renderAt(`/hr/questionnaires/schedules/${SCHEDULE}/results`);
    expect(await screen.findByText("Results are hidden to protect anonymity")).toBeTruthy();
    expect(screen.getByText("7.50")).toBeTruthy();
    expect(screen.queryByText("Overall")).toBeNull();
    expect(screen.queryByRole("table")).toBeNull();
    expect(pageViolations(container)).toEqual([]);
  });

  it("says anonymous results are released after the close when the service answers 409", async () => {
    api.handler = routes({ [`GET ${resultsUrl}`]: problem(409, "HCM-QST-NOT-CLOSED", "results are released after it closes on 2026-11-30T00:00:00Z") });
    const { container } = renderAt(`/hr/questionnaires/schedules/${SCHEDULE}/results`);
    expect(await screen.findByText("Results are not released yet")).toBeTruthy();
    expect(screen.getByText(/released after it closes/)).toBeTruthy();
    expect(pageViolations(container)).toEqual([]);
  });

  it("explains the legal-entity scope refusal for anonymous results", async () => {
    api.handler = routes({ [`GET ${resultsUrl}`]: problem(403, "HCM-QST-SCOPE", "scope") });
    const { container } = renderAt(`/hr/questionnaires/schedules/${SCHEDULE}/results`);
    expect(await screen.findByText("You can't see these results")).toBeTruthy();
    expect((await findAlert()).textContent).toContain("access to every legal entity");
    expect(pageViolations(container)).toEqual([]);
  });

  it("breaks results down by department and returns to the overall view when a small group is hidden", async () => {
    api.handler = routes({
      [`GET ${resultsUrl}`]: (call) =>
        call.url.searchParams.get("by") === "department"
          ? problem(422, "HCM-QST-002", "breakdown hidden: fewer than 5 respondents in a group; use a wider group")
          : json(200, results()),
    });
    renderAt(`/hr/questionnaires/schedules/${SCHEDULE}/results`);
    fireEvent.click(await screen.findByLabelText("Break down by department"));
    expect(await screen.findByText("This breakdown is hidden")).toBeTruthy();
    expect(api.calls.at(-1)?.url.search).toBe("?by=department");
    fireEvent.click(screen.getByRole("button", { name: "Show overall results" }));
    expect(await screen.findByText("Overall")).toBeTruthy();
  });

  it("shows each department's statistics when the breakdown is allowed", async () => {
    api.handler = routes({ [`GET ${resultsUrl}`]: json(200, results({ groups: [{ departmentId: id(7), stats }, { departmentId: null, stats }] })) });
    renderAt(`/hr/questionnaires/schedules/${SCHEDULE}/results?by=department`);
    expect(await screen.findByText("No department")).toBeTruthy();
    expect(screen.getByText(`Department ${id(7).slice(0, 8)}`)).toBeTruthy();
  });

  it("says a missing schedule was not found", async () => {
    api.handler = routes({ [`GET ${resultsUrl}`]: problem(422, "HCM-QST-SCHEDULE-NOT-FOUND", "schedule not found") });
    renderAt(`/hr/questionnaires/schedules/${SCHEDULE}/results`);
    expect(await screen.findByText("We couldn't find this schedule")).toBeTruthy();
  });

  it("opens the results of a schedule by its id", async () => {
    api.handler = routes({ "GET /api/v1/hr/questionnaires": json(200, { items: [] }), [`GET ${resultsUrl}`]: json(200, results()) });
    renderAt("/hr/questionnaires");
    fireEvent.change(await screen.findByLabelText("Schedule id"), { target: { value: "nope" } });
    fireEvent.click(screen.getByRole("button", { name: "Open results" }));
    expect(await screen.findByText("Enter the schedule's id.")).toBeTruthy();
    fireEvent.change(screen.getByLabelText("Schedule id"), { target: { value: SCHEDULE } });
    fireEvent.click(screen.getByRole("button", { name: "Open results" }));
    await waitFor(() => expect(screen.getByTestId("where").textContent).toBe(`/hr/questionnaires/schedules/${SCHEDULE}/results`));
  });

  it("schedules a questionnaire in UTC and opens its results", async () => {
    api.handler = routes({
      [`GET /api/v1/hr/questionnaires/${QUESTIONNAIRE}`]: json(200, questionnaire()),
      "POST /api/v1/hr/questionnaires/schedules": json(201, schedule()),
      [`GET ${resultsUrl}`]: json(200, results()),
    });
    renderAt(`/hr/questionnaires/${QUESTIONNAIRE}`);
    fireEvent.click(await screen.findByRole("button", { name: "Schedule" }));
    expect(await screen.findByText("Choose when it opens.")).toBeTruthy();
    expect(api.posts("/schedules")).toHaveLength(0);
    fireEvent.change(screen.getByLabelText("Opens"), { target: { value: "2026-11-01T08:00" } });
    fireEvent.change(screen.getByLabelText("Closes"), { target: { value: "2026-11-01T07:00" } });
    fireEvent.click(screen.getByRole("button", { name: "Schedule" }));
    expect(await screen.findByText("It must close after it opens.")).toBeTruthy();
    fireEvent.change(screen.getByLabelText("Closes"), { target: { value: "2026-11-15T08:00" } });
    fireEvent.click(screen.getByRole("button", { name: "Schedule" }));
    await waitFor(() => expect(screen.getByTestId("where").textContent).toBe(`/hr/questionnaires/schedules/${SCHEDULE}/results`));
    expect(api.posts("/schedules")[0]?.body).toEqual({ questionnaireId: QUESTIONNAIRE, opensAt: "2026-11-01T08:00:00Z", closesAt: "2026-11-15T08:00:00Z" });
  });

  it("puts the anonymity minimum on the population field", async () => {
    api.handler = routes({
      [`GET /api/v1/hr/questionnaires/${QUESTIONNAIRE}`]: json(200, questionnaire()),
      "POST /api/v1/hr/questionnaires/schedules": problem(422, "HCM-QST-002", "an anonymous questionnaire needs a population of at least 5 workers"),
    });
    renderAt(`/hr/questionnaires/${QUESTIONNAIRE}`);
    fireEvent.change(await screen.findByLabelText("Opens"), { target: { value: "2026-11-01T08:00" } });
    fireEvent.change(screen.getByLabelText("Closes"), { target: { value: "2026-11-15T08:00" } });
    fireEvent.change(screen.getByLabelText("Population"), { target: { value: "legal_entity == 'x'" } });
    fireEvent.click(screen.getByRole("button", { name: "Schedule" }));
    expect(await screen.findByText(/population of at least 5 workers/)).toBeTruthy();
    expect(api.posts("/schedules")[0]?.body).toMatchObject({ population: "legal_entity == 'x'" });
  });

  it("validates a schedule", () => {
    expect(validateSchedule(QUESTIONNAIRE, { population: "", opensAt: "", closesAt: "" }).errors.opensAt).toBeTruthy();
    expect(validateSchedule(QUESTIONNAIRE, { population: "x".repeat(501), opensAt: "2026-11-01T08:00", closesAt: "2026-11-02T08:00" }).errors.population).toBeTruthy();
  });

  it("designs a questionnaire with options and a scale", async () => {
    api.handler = routes({ "POST /api/v1/hr/questionnaires": json(201, questionnaire()), [`GET /api/v1/hr/questionnaires/${QUESTIONNAIRE}`]: json(200, questionnaire()) });
    renderAt("/hr/questionnaires/new");
    fireEvent.click(screen.getByRole("button", { name: "Create questionnaire" }));
    expect(await screen.findByText("Enter a name of up to 120 characters.")).toBeTruthy();
    expect(api.calls).toHaveLength(0);
    fireEvent.change(screen.getByLabelText("Code", { selector: "#questionnaire-code" }), { target: { value: "pulse" } });
    fireEvent.change(screen.getByLabelText("Name"), { target: { value: "Pulse survey" } });
    fireEvent.click(screen.getByLabelText(/Anonymous/));
    fireEvent.change(screen.getByLabelText("Code", { selector: "#question-0-code" }), { target: { value: "q1" } });
    fireEvent.change(screen.getByLabelText("Question"), { target: { value: "How satisfied are you?" } });
    fireEvent.click(screen.getByRole("button", { name: "Create questionnaire" }));
    await waitFor(() => expect(screen.getByTestId("where").textContent).toBe(`/hr/questionnaires/${QUESTIONNAIRE}`));
    expect(api.posts("/questionnaires")[0]?.body).toEqual({
      code: "PULSE", name: "Pulse survey", anonymous: true,
      questions: [{ code: "Q1", text: "How satisfied are you?", type: "SCALE", weight: "1", mandatory: false, scaleMax: 5 }],
    });
  });

  it("puts a duplicate code on the field", async () => {
    api.handler = routes({ "POST /api/v1/hr/questionnaires": problem(422, "HCM-QST-DUPLICATE", "questionnaire code already exists") });
    renderAt("/hr/questionnaires/new");
    fireEvent.change(screen.getByLabelText("Code", { selector: "#questionnaire-code" }), { target: { value: "pulse" } });
    fireEvent.change(screen.getByLabelText("Name"), { target: { value: "Pulse survey" } });
    fireEvent.change(screen.getByLabelText("Code", { selector: "#question-0-code" }), { target: { value: "q1" } });
    fireEvent.change(screen.getByLabelText("Question"), { target: { value: "How?" } });
    fireEvent.click(screen.getByRole("button", { name: "Create questionnaire" }));
    expect(await screen.findByText(/questionnaire code already exists/)).toBeTruthy();
  });

  it("parses option lines and checks the design rules", () => {
    expect(parseOptions("red, Red, 1\nblue, Blue").options).toEqual([{ value: "RED", label: "Red", score: "1" }, { value: "BLUE", label: "Blue" }]);
    expect(parseOptions("red, Red").error).toContain("2 to 20");
    expect(parseOptions("red, Red\nred, Again").error).toContain("unique");
    expect(parseOptions("red, Red, lots\nblue, Blue").error).toContain("score");
    const base = { code: "P", name: "n", anonymous: false, questions: [{ code: "Q1", text: "t", type: "SINGLE", options: "a, A\nb, B", scaleMax: "5", weight: "1", mandatory: false, resultGroup: "" }] };
    expect(validateQuestionnaire(base).input?.questions[0]).toMatchObject({ options: [{ value: "A", label: "A" }, { value: "B", label: "B" }] });
    expect(validateQuestionnaire({ ...base, questions: [{ ...base.questions[0]!, weight: "0" }] }).errors["questions[0].weight"]).toBeTruthy();
    expect(validateQuestionnaire({ ...base, questions: [{ ...base.questions[0]!, type: "SCALE", scaleMax: "11" }] }).errors["questions[0].scaleMax"]).toBeTruthy();
  });

  it("lists open questionnaires by name with a link to answer", async () => {
    api.handler = routes({
      "GET /api/v1/hr/questionnaires/schedules/open": json(200, { items: [schedule()] }),
      [`GET /api/v1/hr/questionnaires/schedules/${SCHEDULE}`]: json(200, view()),
    });
    renderAt("/hr/my/questionnaires");
    const link = await screen.findByRole("link", { name: "Pulse survey" });
    expect(link.getAttribute("href")).toBe(`/hr/my/questionnaires/${SCHEDULE}`);
    expect(screen.getByText("Anonymous")).toBeTruthy();
  });

  it("answers as the signed-in worker and thanks them without exposing identity for an anonymous survey", async () => {
    api.handler = routes({
      [`GET /api/v1/hr/questionnaires/schedules/${SCHEDULE}`]: json(200, view()),
      "GET /api/v1/hr/me": json(200, me),
      [`POST /api/v1/hr/questionnaires/schedules/${SCHEDULE}/answers`]: json(201, { status: "recorded" }),
    });
    const { container } = renderAt(`/hr/my/questionnaires/${SCHEDULE}`);
    fireEvent.click(await screen.findByRole("button", { name: "Submit answers" }));
    expect(await screen.findByText("This question is required.")).toBeTruthy();
    expect(api.posts("/answers")).toHaveLength(0);
    fireEvent.change(screen.getByLabelText("How satisfied are you? (required)"), { target: { value: "4" } });
    fireEvent.change(screen.getByLabelText("Favourite colour"), { target: { value: "RED" } });
    fireEvent.click(screen.getByLabelText("Ex"));
    fireEvent.click(screen.getByLabelText("Why"));
    fireEvent.change(screen.getByLabelText("Comments"), { target: { value: "Fine" } });
    fireEvent.change(screen.getByLabelText("Hours slept"), { target: { value: "7.5" } });
    fireEvent.change(screen.getByLabelText("Start date"), { target: { value: "2026-11-01" } });
    fireEvent.click(screen.getByRole("button", { name: "Submit answers" }));
    expect(await screen.findByText("Thank you, your answers were recorded")).toBeTruthy();
    expect(screen.getByText(/can't be traced back to you/)).toBeTruthy();
    expect(api.posts("/answers")[0]?.body).toEqual({ workerId: WORKER, answers: { Q1: 4, Q2: "RED", Q3: ["X", "Y"], Q4: "Fine", Q5: 7.5, Q6: "2026-11-01" } });
    expect(pageViolations(container)).toEqual([]);
  });

  it("lets a worker answer when their sign-in cannot read the worker record", async () => {
    api.handler = routes({
      [`GET /api/v1/hr/questionnaires/schedules/${SCHEDULE}`]: json(200, view()),
      "GET /api/v1/hr/me": problem(403, "AUTH-0003", "missing duty hr.profile.self"),
      [`POST /api/v1/hr/questionnaires/schedules/${SCHEDULE}/answers`]: json(201, { status: "recorded" }),
    });
    renderAt(`/hr/my/questionnaires/${SCHEDULE}`);
    fireEvent.change(await screen.findByLabelText("How satisfied are you? (required)"), { target: { value: "4" } });
    fireEvent.click(screen.getByRole("button", { name: "Submit answers" }));
    expect(await screen.findByText("Enter your worker id.")).toBeTruthy();
    fireEvent.change(screen.getByLabelText("Your worker record"), { target: { value: WORKER } });
    fireEvent.click(screen.getByRole("button", { name: "Submit answers" }));
    expect(await screen.findByText("Thank you, your answers were recorded")).toBeTruthy();
    expect(api.posts("/answers")[0]?.body).toEqual({ workerId: WORKER, answers: { Q1: 4 } });
  });

  it("shows server field errors that no input displays in the form alert", async () => {
    api.handler = routes({ "POST /api/v1/hr/compensation/plans": problem(400, "COM-0001", "validation failed", { mystery: "something odd" }) });
    renderAt("/hr/compensation/plans/new");
    fireEvent.change(screen.getByLabelText("Code"), { target: { value: "A" } });
    fireEvent.change(screen.getByLabelText("Name"), { target: { value: "n" } });
    fireEvent.change(screen.getByLabelText("Currency"), { target: { value: "usd" } });
    fireEvent.click(screen.getByRole("button", { name: "Add plan" }));
    expect(await screen.findByText("mystery: something odd")).toBeTruthy();
  });

  it("flags the multi-choice answers invalid when the service rejects them", async () => {
    api.handler = routes({
      [`GET /api/v1/hr/questionnaires/schedules/${SCHEDULE}`]: json(200, view()),
      "GET /api/v1/hr/me": json(200, me),
      [`POST /api/v1/hr/questionnaires/schedules/${SCHEDULE}/answers`]: problem(400, "COM-0001", "validation failed", { "answers.Q3": "must list distinct option values of the question" }),
    });
    renderAt(`/hr/my/questionnaires/${SCHEDULE}`);
    fireEvent.change(await screen.findByLabelText("How satisfied are you? (required)"), { target: { value: "4" } });
    fireEvent.click(screen.getByLabelText("Ex"));
    fireEvent.click(screen.getByRole("button", { name: "Submit answers" }));
    expect(await screen.findByText("must list distinct option values of the question")).toBeTruthy();
    expect(screen.getByLabelText("Ex").getAttribute("aria-invalid")).toBe("true");
  });

  it("tells a worker who already answered that answers can't change", async () => {
    api.handler = routes({
      [`GET /api/v1/hr/questionnaires/schedules/${SCHEDULE}`]: json(200, view()),
      "GET /api/v1/hr/me": json(200, me),
      [`POST /api/v1/hr/questionnaires/schedules/${SCHEDULE}/answers`]: problem(409, "HCM-QST-ANSWERED", "already answered"),
    });
    renderAt(`/hr/my/questionnaires/${SCHEDULE}`);
    fireEvent.change(await screen.findByLabelText("How satisfied are you? (required)"), { target: { value: "4" } });
    fireEvent.click(screen.getByRole("button", { name: "Submit answers" }));
    expect(await screen.findByText("Already answered")).toBeTruthy();
    expect(screen.getByText(/answers can't be changed/)).toBeTruthy();
  });

  it("puts the service's answer error on the question", async () => {
    api.handler = routes({
      [`GET /api/v1/hr/questionnaires/schedules/${SCHEDULE}`]: json(200, view()),
      "GET /api/v1/hr/me": json(200, me),
      [`POST /api/v1/hr/questionnaires/schedules/${SCHEDULE}/answers`]: problem(400, "COM-0001", "validation failed", { "answers.Q1": "must be a whole number from 1 to 5" }),
    });
    renderAt(`/hr/my/questionnaires/${SCHEDULE}`);
    fireEvent.change(await screen.findByLabelText("How satisfied are you? (required)"), { target: { value: "4" } });
    fireEvent.click(screen.getByRole("button", { name: "Submit answers" }));
    expect(await screen.findByText("must be a whole number from 1 to 5")).toBeTruthy();
  });

  it("shows a closed questionnaire without a form and a sign-in with no worker linked", async () => {
    api.handler = routes({ [`GET /api/v1/hr/questionnaires/schedules/${SCHEDULE}`]: json(200, view({ status: "CLOSED" })), "GET /api/v1/hr/me": json(200, me) });
    const closed = renderAt(`/hr/my/questionnaires/${SCHEDULE}`);
    expect(await screen.findByText("This questionnaire has closed")).toBeTruthy();
    expect(screen.queryByRole("button", { name: "Submit answers" })).toBeNull();
    closed.unmount();

    api.handler = routes({ [`GET /api/v1/hr/questionnaires/schedules/${SCHEDULE}`]: json(200, view()), "GET /api/v1/hr/me": problem(404, "HCM-ESS-001", "no worker") });
    renderAt(`/hr/my/questionnaires/${SCHEDULE}`);
    expect(await screen.findByText("No worker record is linked to your sign-in")).toBeTruthy();
  });

  it("checks each answer the way the service does", () => {
    const required = validateAnswers(questions as never, {});
    expect(required.errors.Q1).toBeTruthy();
    expect(validateAnswers(questions as never, { Q1: "3", Q5: "abc" }).errors.Q5).toBe("Enter a number.");
    expect(validateAnswers(questions as never, { Q1: "3", Q6: "11/01/2026" }).errors.Q6).toBe("Choose a date.");
    expect(validateAnswers(questions as never, { Q1: "3", Q3: [] }).answers).toEqual({ Q1: 3 });
  });

  it("gates the questionnaire routes and nav by duty", () => {
    const duty = (path: string) => (hr.routes.find((r) => r.path === path)?.handle as { duty?: string }).duty;
    expect(duty("/hr/questionnaires")).toBe("hr.questionnaire.admin");
    expect(duty("/hr/questionnaires/schedules/:id/results")).toBe("hr.questionnaire.admin");
    expect(duty("/hr/my/questionnaires")).toBe("hr.questionnaire.self");
    expect(duty("/hr/my/questionnaires/:id")).toBe("hr.questionnaire.self");
    expect(hr.nav.find((n) => n.to === "/hr/my/questionnaires")?.duty).toBe("hr.questionnaire.self");
  });
});
