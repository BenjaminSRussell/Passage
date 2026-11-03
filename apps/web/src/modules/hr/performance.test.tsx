import { fireEvent, screen, waitFor, within } from "@testing-library/react";
import { describe, expect, it } from "vitest";
import { findAlert, findStatus, findTable } from "../../test-utils";
import { id, json, pageViolations, problem, renderAt, routes, useFakeApi } from "./hrTestKit";
import { validateGoal, validatePeriod, validateProgress, validateSelfAssessment, validateSignOff } from "./PerformanceParts";

const WORKER = id(1);
const JOB = id(20);
const GOAL = id(30);
const REVIEW = id(40);

const goal = (patch: Record<string, unknown> = {}) => ({
  id: GOAL, workerId: WORKER, title: "Reduce backlog", dueOn: "2026-12-31", status: "OPEN", progressPercent: 20, createdBy: id(9), createdAt: "2026-01-01T00:00:00Z", updatedAt: "2026-01-01T00:00:00Z", ...patch,
});
const review = (patch: Record<string, unknown> = {}) => ({
  id: REVIEW, workerId: WORKER, periodStart: "2026-01-01", periodEnd: "2026-12-31", status: "draft", createdBy: id(9), createdAt: "2026-01-01T00:00:00Z", updatedAt: "2026-01-01T00:00:00Z", ...patch,
});

describe("performance validation", () => {
  it("checks goals, progress, reviews and sign-off before sending", () => {
    expect(validateGoal({ title: "  ", description: "", dueOn: "" }).errors.title).toBeTruthy();
    expect(validateGoal({ title: " Ship ", description: "", dueOn: "2026-12-01" }).input).toEqual({ title: "Ship", description: undefined, dueOn: "2026-12-01" });
    expect(validateProgress({ progress: "101", status: "OPEN" }).errors.progress).toBeTruthy();
    expect(validateProgress({ progress: "50", status: "ACHIEVED" }).errors.progress).toContain("100 percent");
    expect(validateProgress({ progress: "100", status: "ACHIEVED" }).input).toEqual({ status: "ACHIEVED", progressPercent: 100 });
    expect(validateSelfAssessment({ achievements: " ", development: "" }).errors.achievements).toBeTruthy();
    expect(validateSignOff({ comments: "", rating: "6" }).errors.rating).toBeTruthy();
    expect(validateSignOff({ comments: "Solid", rating: "4" }).input).toEqual({ managerComments: "Solid", overallRating: 4 });
    expect(validatePeriod({ periodStart: "2026-06-01", periodEnd: "2026-01-01" }).errors.periodEnd).toContain("before");
  });
});

describe("my goals", () => {
  const api = useFakeApi();

  it("lists the worker's goals and sends a progress update to the self route", async () => {
    let current = goal();
    api.handler = routes({
      "GET /api/v1/hr/me/goals": () => json(200, { items: [current] }),
      [`POST /api/v1/hr/me/goals/${GOAL}/progress`]: (call) => {
        current = goal({ progressPercent: (call.body as { progressPercent: number }).progressPercent });
        return json(200, current);
      },
    });
    const { container } = renderAt("/hr/my/goals");
    const table = await findTable({ name: "My goals" });
    expect(within(table).getByText("20%")).toBeInTheDocument();
    expect(pageViolations(container)).toEqual([]);

    const form = screen.getByRole("form", { name: "Update goal progress" });
    expect(within(form).queryByRole("option", { name: "Cancelled" })).toBeNull();
    fireEvent.change(within(form).getByLabelText("Goal"), { target: { value: GOAL } });
    expect((within(form).getByLabelText("Progress (percent)") as HTMLInputElement).value).toBe("20");
    fireEvent.change(within(form).getByLabelText("Progress (percent)"), { target: { value: "60" } });
    fireEvent.click(within(form).getByRole("button", { name: "Save progress" }));
    await waitFor(() => expect(api.calls.some((c) => c.method === "POST" && c.url.pathname === `/api/v1/hr/me/goals/${GOAL}/progress`)).toBe(true));
    expect(api.calls.find((c) => c.method === "POST")?.body).toEqual({ status: "OPEN", progressPercent: 60 });
    expect((await findStatus()).textContent).toContain("is now 60%");
    expect(await within(await findTable({ name: "My goals" })).findByText("60%")).toBeInTheDocument();
  });

  it("refuses an achieved goal below 100 percent without calling the service", async () => {
    api.handler = routes({ "GET /api/v1/hr/me/goals": json(200, { items: [goal()] }) });
    renderAt("/hr/my/goals");
    const form = await screen.findByRole("form", { name: "Update goal progress" });
    fireEvent.change(within(form).getByLabelText("Goal"), { target: { value: GOAL } });
    fireEvent.change(within(form).getByLabelText("Status"), { target: { value: "ACHIEVED" } });
    fireEvent.click(within(form).getByRole("button", { name: "Save progress" }));
    expect(await within(form).findByText("An achieved goal must be at 100 percent.")).toBeInTheDocument();
    expect(api.calls.filter((c) => c.method === "POST")).toHaveLength(0);
  });

  it("shows the empty state and an error with a retry", async () => {
    api.handler = routes({ "GET /api/v1/hr/me/goals": json(200, { items: [] }) });
    const first = renderAt("/hr/my/goals");
    expect(await screen.findByText("No goals yet")).toBeInTheDocument();
    expect(screen.queryByRole("form", { name: "Update goal progress" })).toBeNull();
    first.unmount();

    api.handler = routes({ "GET /api/v1/hr/me/goals": problem(500, "COM-0500", "boom") });
    renderAt("/hr/my/goals");
    expect((await findAlert()).textContent).toContain("boom (COM-0500)");
    expect(screen.getByRole("button", { name: "Try again" })).toBeInTheDocument();
  });
});

describe("my reviews", () => {
  const api = useFakeApi();

  it("submits the self-assessment of a draft review", async () => {
    let current = review();
    api.handler = routes({
      "GET /api/v1/hr/me/reviews": () => json(200, { items: [current] }),
      [`POST /api/v1/hr/me/reviews/${REVIEW}/self-assessment`]: () => {
        current = review({ status: "self_submitted", selfAchievements: "Closed 40 tickets" });
        return json(200, current);
      },
    });
    const { container } = renderAt("/hr/my/reviews");
    const form = await screen.findByRole("form", { name: "Self-assessment" });
    expect(pageViolations(container)).toEqual([]);
    fireEvent.click(within(form).getByRole("button", { name: "Submit self-assessment" }));
    expect(await within(form).findByText(/Describe your achievements/)).toBeInTheDocument();
    fireEvent.change(within(form).getByLabelText("Achievements"), { target: { value: "Closed 40 tickets" } });
    fireEvent.click(within(form).getByRole("button", { name: "Submit self-assessment" }));
    expect((await findStatus()).textContent).toContain("was submitted");
    expect(api.calls.find((c) => c.method === "POST")?.body).toEqual({ achievements: "Closed 40 tickets" });
    expect(await screen.findByText("Waiting for the manager")).toBeInTheDocument();
    expect(screen.queryByRole("form", { name: "Self-assessment" })).toBeNull();
  });

  it("shows the rating and comments of a signed-off review", async () => {
    api.handler = routes({
      "GET /api/v1/hr/me/reviews": json(200, { items: [review({ status: "signed_off", selfAchievements: "A", overallRating: 4, managerComments: "Solid year", signedOffAt: "2026-12-20T10:00:00Z" })] }),
    });
    renderAt("/hr/my/reviews");
    expect(await screen.findByText("Solid year")).toBeInTheDocument();
    expect(screen.getByText("4 of 5")).toBeInTheDocument();
  });

  it("says when no review is open", async () => {
    api.handler = routes({ "GET /api/v1/hr/me/reviews": json(200, { items: [] }) });
    renderAt("/hr/my/reviews");
    expect(await screen.findByText("No review has been opened for you yet.")).toBeInTheDocument();
  });

  it("says when the sign-in has no worker record, on both self screens", async () => {
    api.handler = routes({
      "GET /api/v1/hr/me/reviews": problem(404, "HCM-ESS-001", "no worker"),
      "GET /api/v1/hr/me/goals": problem(404, "HCM-ESS-001", "no worker"),
    });
    const first = renderAt("/hr/my/reviews");
    expect(await screen.findByText("No worker record is linked to your sign-in")).toBeInTheDocument();
    first.unmount();
    renderAt("/hr/my/goals");
    expect(await screen.findByText("No worker record is linked to your sign-in")).toBeInTheDocument();
    expect(screen.queryByRole("form", { name: "Update goal progress" })).toBeNull();
  });
});

describe("a worker's goals and reviews for the manager", () => {
  const api = useFakeApi();

  it("adds a goal and opens a review", async () => {
    api.handler = routes({
      [`GET /api/v1/hr/workers/${WORKER}/goals`]: json(200, { items: [goal()] }),
      [`GET /api/v1/hr/workers/${WORKER}/reviews`]: json(200, { items: [review({ status: "self_submitted" })] }),
      [`POST /api/v1/hr/workers/${WORKER}/goals`]: json(201, goal({ title: "New goal" })),
      [`POST /api/v1/hr/workers/${WORKER}/reviews`]: json(201, review({ periodStart: "2027-01-01", periodEnd: "2027-12-31" })),
    });
    const { container } = renderAt(`/hr/workers/${WORKER}/performance`);
    await findTable({ name: "Goals" });
    expect(pageViolations(container)).toEqual([]);

    fireEvent.click(screen.getByRole("button", { name: "New goal" }));
    const form = screen.getByRole("form", { name: "New goal" });
    fireEvent.click(within(form).getByRole("button", { name: "Add goal" }));
    expect(await within(form).findByText(/Enter a title/)).toBeInTheDocument();
    fireEvent.change(within(form).getByLabelText("Title"), { target: { value: "New goal" } });
    fireEvent.click(within(form).getByRole("button", { name: "Add goal" }));
    expect((await findStatus()).textContent).toContain('Goal "New goal" added.');
    const created = api.calls.find((c) => c.method === "POST" && c.url.pathname.endsWith("/goals"));
    expect(created?.body).toEqual({ title: "New goal" });
    expect(created?.headers.get("Idempotency-Key")).toBeTruthy();

    fireEvent.click(screen.getByRole("button", { name: "Open a review" }));
    const open = screen.getByRole("form", { name: "Open a review" });
    fireEvent.change(within(open).getByLabelText("Period start"), { target: { value: "2027-01-01" } });
    fireEvent.change(within(open).getByLabelText("Period end"), { target: { value: "2027-12-31" } });
    fireEvent.click(within(open).getByRole("button", { name: "Open review" }));
    await waitFor(() => expect(api.calls.some((c) => c.method === "POST" && c.url.pathname.endsWith("/reviews"))).toBe(true));
    expect(api.calls.find((c) => c.method === "POST" && c.url.pathname.endsWith("/reviews"))?.body).toEqual({ periodStart: "2027-01-01", periodEnd: "2027-12-31" });
  });

  it("lets the manager cancel a goal, which the employee screen can't", async () => {
    api.handler = routes({
      [`GET /api/v1/hr/workers/${WORKER}/goals`]: json(200, { items: [goal()] }),
      [`GET /api/v1/hr/workers/${WORKER}/reviews`]: json(200, { items: [] }),
      [`POST /api/v1/hr/goals/${GOAL}/progress`]: json(200, goal({ status: "CANCELLED" })),
    });
    renderAt(`/hr/workers/${WORKER}/performance`);
    const form = await screen.findByRole("form", { name: "Update goal progress" });
    fireEvent.change(within(form).getByLabelText("Goal"), { target: { value: GOAL } });
    fireEvent.change(within(form).getByLabelText("Status"), { target: { value: "CANCELLED" } });
    fireEvent.click(within(form).getByRole("button", { name: "Save progress" }));
    await waitFor(() => expect(api.calls.some((c) => c.method === "POST" && c.url.pathname === `/api/v1/hr/goals/${GOAL}/progress`)).toBe(true));
    expect(await screen.findByText(/is cancelled at 20%/)).toBeInTheDocument();
  });

  it("says a worker the caller can't manage was not found", async () => {
    api.handler = routes({
      [`GET /api/v1/hr/workers/${WORKER}/goals`]: problem(404, "HCM-WRK-NOT-FOUND", "worker not found"),
      [`GET /api/v1/hr/workers/${WORKER}/reviews`]: problem(404, "HCM-WRK-NOT-FOUND", "worker not found"),
    });
    renderAt(`/hr/workers/${WORKER}/performance`);
    expect(await screen.findByText("We couldn't find this worker")).toBeInTheDocument();
  });
});

describe("a review for the manager", () => {
  const api = useFakeApi();

  it("waits for the self-assessment before offering the sign-off", async () => {
    api.handler = routes({ [`GET /api/v1/hr/reviews/${REVIEW}`]: json(200, review()) });
    const { container } = renderAt(`/hr/reviews/${REVIEW}`);
    expect(await screen.findByText("The worker writes their self-assessment first. You can sign off after they submit it.")).toBeInTheDocument();
    expect(screen.queryByRole("form", { name: "Manager sign-off" })).toBeNull();
    expect(pageViolations(container)).toEqual([]);
  });

  it("signs off a submitted review, and the manager can't sign off their own", async () => {
    let own = true;
    let current = review({ status: "self_submitted", selfAchievements: "Closed 40 tickets" });
    api.handler = routes({
      [`GET /api/v1/hr/reviews/${REVIEW}`]: () => json(200, current),
      [`POST /api/v1/hr/reviews/${REVIEW}/sign-off`]: () => {
        if (own) {
          own = false;
          return problem(403, "HCM-REV-SELF", "you cannot sign off your own review");
        }
        current = review({ status: "signed_off", selfAchievements: "Closed 40 tickets", overallRating: 4, managerComments: "Solid" });
        return json(200, current);
      },
    });
    renderAt(`/hr/reviews/${REVIEW}`);
    expect(await screen.findByText("Closed 40 tickets")).toBeInTheDocument();
    const form = screen.getByRole("form", { name: "Manager sign-off" });
    fireEvent.change(within(form).getByLabelText(/Overall rating/), { target: { value: "4" } });
    fireEvent.click(within(form).getByRole("button", { name: "Sign off" }));
    expect(await within(form).findByText(/cannot sign off your own review/)).toBeInTheDocument();
    fireEvent.click(within(form).getByRole("button", { name: "Sign off" }));
    expect((await findStatus()).textContent).toContain("signed off");
    expect(api.calls.filter((c) => c.method === "POST").at(-1)?.body).toEqual({ overallRating: 4 });
    expect(await screen.findByText("4 of 5")).toBeInTheDocument();
  });
});

describe("job skill requirements and gap report", () => {
  const api = useFakeApi();
  const job = { id: JOB, code: "WELDER", title: "Welder", jobType: "SKILLED" };
  const requirement = { jobId: JOB, skillId: id(50), skillCode: "WELDING", skillName: "Welding", scaleMax: 5, minRating: 3 };
  const base = {
    [`GET /api/v1/hr/jobs/${JOB}`]: json(200, job),
    [`GET /api/v1/hr/jobs/${JOB}/skill-requirements`]: json(200, { items: [requirement] }),
    "GET /api/v1/hr/skills": json(200, { items: [{ id: id(50), code: "WELDING", name: "Welding", scaleMax: 5 }] }),
  };

  it("lists the requirements and the workers short of them as of a date", async () => {
    api.handler = routes({
      ...base,
      [`GET /api/v1/hr/jobs/${JOB}/skill-gaps`]: json(200, {
        jobId: JOB,
        jobCode: "WELDER",
        asOf: "2026-11-01",
        truncated: false,
        items: [
          { workerId: WORKER, personnelNumber: "W-1", legalName: "Ada", met: false, gaps: [{ skill: "WELDING", required: 3, actual: 1, delta: -2 }] },
          { workerId: id(2), personnelNumber: "W-2", legalName: "Bo", met: true, gaps: [] },
        ],
      }),
    });
    const { container } = renderAt(`/hr/jobs/${JOB}`);
    expect(within(await findTable({ name: "Required skills" })).getByText("3 of 5")).toBeInTheDocument();
    const gaps = await findTable({ name: "Job skill gaps" });
    expect(within(gaps).getByText("WELDING 1 of 3")).toBeInTheDocument();
    expect(screen.getByText("1 of 2 workers short")).toBeInTheDocument();
    expect(pageViolations(container)).toEqual([]);

    fireEvent.change(screen.getByLabelText("As of"), { target: { value: "2026-06-01" } });
    await waitFor(() => expect(api.calls.at(-1)?.url.searchParams.get("asOf")).toBe("2026-06-01"));
  });

  it("sets and removes a job requirement", async () => {
    api.handler = routes({
      ...base,
      [`GET /api/v1/hr/jobs/${JOB}/skill-gaps`]: json(200, { jobId: JOB, jobCode: "WELDER", asOf: "2026-11-01", truncated: false, items: [] }),
      [`POST /api/v1/hr/jobs/${JOB}/skill-requirements`]: json(200, { ...requirement, minRating: 4 }),
      [`DELETE /api/v1/hr/jobs/${JOB}/skill-requirements/WELDING`]: json(204, null),
    });
    renderAt(`/hr/jobs/${JOB}`);
    const form = await screen.findByRole("form", { name: "Require a skill" });
    fireEvent.change(within(form).getByLabelText("Skill"), { target: { value: "WELDING" } });
    fireEvent.change(within(form).getByLabelText("Minimum rating"), { target: { value: "4" } });
    fireEvent.click(within(form).getByRole("button", { name: "Save requirement" }));
    expect((await findStatus()).textContent).toContain("WELDING now requires at least 4.");
    expect(api.calls.find((c) => c.method === "POST")?.body).toEqual({ skillCode: "WELDING", minRating: 4 });
  });

  it("shows an error with a retry when the gap report fails", async () => {
    api.handler = routes({ ...base, [`GET /api/v1/hr/jobs/${JOB}/skill-gaps`]: problem(500, "COM-0500", "boom") });
    renderAt(`/hr/jobs/${JOB}`);
    expect((await findAlert()).textContent).toContain("boom (COM-0500)");
  });
});
