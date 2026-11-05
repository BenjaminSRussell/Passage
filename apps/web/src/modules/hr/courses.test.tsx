import { findTable } from "../../test-utils";
import { fireEvent, render, screen, waitFor, within } from "@testing-library/react";
import { MemoryRouter, Route, Routes, useLocation } from "react-router-dom";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { checkAxeAA } from "../../shell/a11y";
import type { Course, CourseRegistration, CourseSession, Skill, Worker } from "./api/client";
import { validateSession, type SessionValues } from "./CourseDetailPage";
import { canRecord, completionLines } from "./CourseSessionPage";
import { formatUtc, utcFromLocalInput } from "./format";
import hr from "./module";
import { validateCourse, type CourseValues } from "./NewCoursePage";

const COURSE = "0195f0a0-0000-7000-8000-0000000000c1";
const SESSION = "0195f0a0-0000-7000-8000-0000000000c2";
const ADA = "0195f0a0-0000-7000-8000-000000000001";
const BOB = "0195f0a0-0000-7000-8000-000000000002";
const CY = "0195f0a0-0000-7000-8000-000000000003";
const FORK_ID = "0195f0a0-0000-7000-8000-0000000000a1";

type Reply = { status: number; body: unknown };
type Handler = (url: URL, init: RequestInit) => Reply | undefined;

const shellRules = new Set(["page-has-heading-one", "landmark-unique", "region", "bypass"]);
const pageViolations = (container: HTMLElement) => checkAxeAA(container).filter((v) => !shellRules.has(v.id));

let handler: Handler;
let calls: { method: string; url: URL; init: RequestInit; body: unknown }[];
const json = (status: number, body: unknown): Reply => ({ status, body });

beforeEach(() => {
  calls = [];
  handler = () => undefined;
  vi.stubGlobal(
    "fetch",
    vi.fn(async (input: string, init: RequestInit = {}) => {
      const url = new URL(input, "http://localhost");
      calls.push({ method: init.method ?? "GET", url, init, body: init.body ? JSON.parse(String(init.body)) : undefined });
      const reply = handler(url, init) ?? json(404, { code: "TEST-404", detail: `no fake for ${init.method ?? "GET"} ${url.pathname}` });
      return new Response(JSON.stringify(reply.body), { status: reply.status, headers: { "Content-Type": "application/json" } });
    }),
  );
});

afterEach(() => {
  vi.unstubAllGlobals();
});

function Where() {
  const location = useLocation();
  return <p data-testid="where">{location.pathname}</p>;
}

function renderAt(path: string) {
  return render(
    <MemoryRouter initialEntries={[path]}>
      <Routes>
        {hr.routes.map((route) => (
          <Route key={route.path} path={route.path} element={route.element} />
        ))}
      </Routes>
      <Where />
    </MemoryRouter>,
  );
}

const header = (call: { init: RequestInit }, name: string) => new Headers(call.init.headers).get(name);
const posts = (path: string) => calls.filter((c) => c.method === "POST" && c.url.pathname === path);
const gets = (path: string) => calls.filter((c) => c.method === "GET" && c.url.pathname === path);
const statusTexts = () => screen.queryAllByRole("status").map((el) => el.textContent ?? "");
const alertTexts = () => screen.queryAllByRole("alert").map((el) => el.textContent ?? "");

const forkSkill: Skill = { id: FORK_ID, code: "FORKLIFT", name: "Forklift", scaleMax: 5, createdAt: "2026-02-01T09:00:00Z" };

const fork101: Course = {
  id: COURSE,
  code: "FORK-101",
  name: "Forklift operation",
  courseGroup: "Safety",
  skillOutcomes: [{ skillId: FORK_ID, skillCode: "FORKLIFT", skillName: "Forklift", rating: 3 }],
  certificationCode: "FORKLIFT",
  certificationName: "Forklift licence",
  validityMonths: 24,
  requiresManagerApproval: false,
  createdAt: "2026-03-01T09:00:00Z",
};

function session(startsAt: string, endsAt: string, extra: Partial<CourseSession> = {}): CourseSession {
  return {
    id: SESSION,
    courseId: COURSE,
    courseCode: "FORK-101",
    courseName: "Forklift operation",
    startsAt,
    endsAt,
    location: "Yard B",
    capacity: 10,
    waitlistCapacity: 2,
    costPerAttendee: "250.00",
    currency: "USD",
    confirmed: 1,
    waitlisted: 1,
    requested: 1,
    seatsLeft: 9,
    createdAt: "2026-03-01T09:00:00Z",
    ...extra,
  };
}

const past = session("2026-06-01T09:00:00Z", "2026-06-01T17:00:00Z");
const future = session("2099-06-01T09:00:00Z", "2099-06-01T17:00:00Z");

function reg(id: string, workerId: string, name: string, status: CourseRegistration["status"], extra: Partial<CourseRegistration> = {}): CourseRegistration {
  return { id, sessionId: SESSION, workerId, personnelNumber: `WRK-${name}`, legalName: name, status, queuedAt: "2026-05-01T10:00:00Z", ...extra };
}

const registrations = [
  reg("0195f0a0-0000-7000-8000-0000000000d1", ADA, "Ada", "CONFIRMED"),
  reg("0195f0a0-0000-7000-8000-0000000000d2", BOB, "Bob", "WAITLISTED"),
  reg("0195f0a0-0000-7000-8000-0000000000d3", CY, "Cy", "REQUESTED"),
];

const ada: Worker = {
  id: ADA,
  personnelNumber: "WRK-Ada",
  workerType: "EMPLOYEE",
  partyPersonId: "0195f0a0-0000-7000-8000-0000000000aa",
  legalName: "Ada",
  createdAt: "2026-01-05T09:00:00Z",
  updatedAt: "2026-01-05T09:00:00Z",
};

describe("course routes", () => {
  it("registers the catalog, create form, course and session pages, and the nav entry with its duty", () => {
    const paths = hr.routes.map((r) => r.path);
    for (const path of ["/hr/courses", "/hr/courses/new", "/hr/courses/:id", "/hr/courses/sessions/:id"]) expect(paths).toContain(path);
    expect(hr.nav).toContainEqual({ label: "Courses", to: "/hr/courses", duty: "hr.learning.admin" });
  });
});

describe("course catalog", () => {
  it("lists courses with what a pass awards and passes the page checks", async () => {
    handler = (url) => (url.pathname === "/api/v1/hr/courses" ? json(200, { items: [fork101] }) : undefined);
    const { container } = renderAt("/hr/courses");
    const table = await findTable({ name: "Courses" });
    expect(within(table).getByRole("link", { name: "FORK-101" })).toHaveAttribute("href", `/hr/courses/${COURSE}`);
    expect(within(table).getByText("FORKLIFT 3")).toBeInTheDocument();
    expect(within(table).getByText("FORKLIFT (24 months after the session ends)")).toBeInTheDocument();
    expect(pageViolations(container)).toEqual([]);
  });

  it("shows the empty state, and an error with a retry that reloads", async () => {
    handler = (url) => (url.pathname === "/api/v1/hr/courses" ? json(200, { items: [] }) : undefined);
    const { unmount } = renderAt("/hr/courses");
    expect(await screen.findByText("No courses yet")).toBeInTheDocument();
    unmount();

    handler = (url) => (url.pathname === "/api/v1/hr/courses" ? json(403, { code: "AUTH-0003", detail: "missing duty hr.learning.self" }) : undefined);
    renderAt("/hr/courses");
    await waitFor(() => expect(alertTexts().join(" ")).toContain("missing duty hr.learning.self (AUTH-0003)"));
    const before = gets("/api/v1/hr/courses").length;
    fireEvent.click(screen.getByRole("button", { name: "Try again" }));
    await waitFor(() => expect(gets("/api/v1/hr/courses").length).toBeGreaterThan(before));
  });
});

describe("new course", () => {
  const blank: CourseValues = {
    code: "",
    name: "",
    courseGroup: "",
    requiresManagerApproval: false,
    certificationCode: "",
    certificationName: "",
    validityMonths: "",
    outcomes: [{ skillCode: "", rating: "" }],
  };

  it("checks ratings against each skill's scale and pairs the certificate fields", () => {
    const ok = validateCourse(
      { ...blank, code: " fork-101 ", name: "Forklift", certificationCode: "forklift", certificationName: "Licence", validityMonths: "24", outcomes: [{ skillCode: "FORKLIFT", rating: "3" }] },
      [forkSkill],
    );
    expect(ok.input).toEqual({
      code: "FORK-101",
      name: "Forklift",
      skillOutcomes: [{ skillCode: "FORKLIFT", rating: 3 }],
      certificationCode: "FORKLIFT",
      certificationName: "Licence",
      validityMonths: 24,
      requiresManagerApproval: false,
    });
    expect(validateCourse({ ...blank, code: "X", name: "x" }, []).input).toEqual({ code: "X", name: "x", skillOutcomes: [], requiresManagerApproval: false });
    const bad = validateCourse(
      { ...blank, code: "no spaces", name: "", certificationCode: "CERT", validityMonths: "12", outcomes: [{ skillCode: "FORKLIFT", rating: "6" }, { skillCode: "FORKLIFT", rating: "1" }] },
      [forkSkill],
    ).errors;
    expect(bad).toMatchObject({
      code: expect.any(String),
      name: expect.any(String),
      certificationName: expect.any(String),
      "skillOutcomes[0].rating": "Choose a rating from 1 to 5.",
      "skillOutcomes[1].skillCode": "Each skill can appear once.",
    });
    expect(validateCourse({ ...blank, code: "X", name: "x", validityMonths: "6" }, []).errors.validityMonths).toBe("A validity needs a certificate.");
  });

  it("creates with an Idempotency-Key, puts a duplicate code on the field, then opens the course", async () => {
    let duplicate = true;
    handler = (url, init) => {
      if (url.pathname === "/api/v1/hr/skills") return json(200, { items: [forkSkill] });
      if (init.method === "POST" && url.pathname === "/api/v1/hr/courses") {
        if (duplicate) {
          duplicate = false;
          return json(409, { code: "HCM-CRS-DUPLICATE", detail: "a course with code FORK-101 already exists" });
        }
        return json(201, fork101);
      }
      if (url.pathname === `/api/v1/hr/courses/${COURSE}`) return json(200, fork101);
      if (url.pathname === `/api/v1/hr/courses/${COURSE}/sessions`) return json(200, { items: [] });
      return undefined;
    };
    const { container } = renderAt("/hr/courses/new");
    await screen.findByRole("option", { name: "FORKLIFT — Forklift" });
    expect(pageViolations(container)).toEqual([]);
    fireEvent.change(screen.getByLabelText("Code"), { target: { value: "fork-101" } });
    fireEvent.change(screen.getByLabelText("Name"), { target: { value: "Forklift operation" } });
    fireEvent.change(screen.getByLabelText("Skill"), { target: { value: "FORKLIFT" } });
    fireEvent.change(screen.getByLabelText("Rating on a pass"), { target: { value: "3" } });
    fireEvent.change(screen.getByLabelText("Certificate code"), { target: { value: "FORKLIFT" } });
    fireEvent.change(screen.getByLabelText("Certificate title"), { target: { value: "Forklift licence" } });
    fireEvent.change(screen.getByLabelText("Valid for (months)"), { target: { value: "24" } });
    fireEvent.click(screen.getByRole("button", { name: "Add course" }));
    expect(await screen.findByText("This course code is already in the catalog.")).toBeInTheDocument();

    fireEvent.click(screen.getByRole("button", { name: "Add course" }));
    await waitFor(() => expect(screen.getByTestId("where")).toHaveTextContent(`/hr/courses/${COURSE}`));
    const sent = posts("/api/v1/hr/courses");
    expect(sent).toHaveLength(2);
    expect(sent[1].body).toEqual({
      code: "FORK-101",
      name: "Forklift operation",
      skillOutcomes: [{ skillCode: "FORKLIFT", rating: 3 }],
      certificationCode: "FORKLIFT",
      certificationName: "Forklift licence",
      validityMonths: 24,
      requiresManagerApproval: false,
    });
    expect(header(sent[1], "Idempotency-Key")).toBeTruthy();
    expect(header(sent[1], "Idempotency-Key")).toBe(header(sent[0], "Idempotency-Key"));
  });
});

describe("course detail and sessions", () => {
  const values: SessionValues = {
    startsAt: "2026-06-01T09:00",
    endsAt: "2026-06-01T17:00",
    location: "Yard B",
    virtualUrl: "",
    instructorWorkerId: "",
    capacity: "10",
    waitlistCapacity: "2",
    costPerAttendee: "",
    currency: "",
  };

  it("reads times as UTC and checks seats, place and cost", () => {
    expect(utcFromLocalInput("2026-06-01T09:00")).toBe("2026-06-01T09:00:00Z");
    expect(utcFromLocalInput("June 1")).toBeNull();
    expect(formatUtc("2026-06-01T09:00:00Z")).toBe("2026-06-01 09:00 UTC");
    expect(validateSession(values).input).toEqual({ startsAt: "2026-06-01T09:00:00Z", endsAt: "2026-06-01T17:00:00Z", location: "Yard B", capacity: 10, waitlistCapacity: 2 });
    expect(validateSession({ ...values, waitlistCapacity: "" }).input?.waitlistCapacity).toBe(5);
    expect(validateSession({ ...values, costPerAttendee: "1500", currency: "jpy" }).input).toMatchObject({ costPerAttendee: "1500", currency: "JPY" });
    const bad = validateSession({ ...values, endsAt: "2026-06-01T08:00", location: "", capacity: "0", waitlistCapacity: "101", costPerAttendee: "1.5", currency: "JPY" }).errors;
    expect(Object.keys(bad).sort()).toEqual(["capacity", "costPerAttendee", "endsAt", "location", "waitlistCapacity"]);
    expect(validateSession({ ...values, virtualUrl: "ftp://x" }).errors.virtualUrl).toBeTruthy();
    expect(validateSession({ ...values, costPerAttendee: "10" }).errors.currency).toBeTruthy();
  });

  it("shows what a pass awards and the sessions, and schedules a session in UTC with a key", async () => {
    let scheduled = false;
    handler = (url, init) => {
      if (url.pathname === `/api/v1/hr/courses/${COURSE}`) return json(200, fork101);
      if (url.pathname === `/api/v1/hr/courses/${COURSE}/sessions`) {
        if (init.method === "POST") {
          scheduled = true;
          return json(201, past);
        }
        return json(200, { items: scheduled ? [past] : [] });
      }
      return undefined;
    };
    const { container } = renderAt(`/hr/courses/${COURSE}`);
    expect(await screen.findByText("FORKLIFT (Forklift) to 3")).toBeInTheDocument();
    expect(screen.getByText("24 months after the session ends")).toBeInTheDocument();
    expect(await screen.findByText("No sessions yet")).toBeInTheDocument();
    expect(pageViolations(container)).toEqual([]);

    fireEvent.click(screen.getByRole("button", { name: "Schedule session" }));
    fireEvent.change(screen.getByLabelText("Starts (UTC)"), { target: { value: "2026-06-01T09:00" } });
    fireEvent.change(screen.getByLabelText("Ends (UTC)"), { target: { value: "2026-06-01T17:00" } });
    fireEvent.change(screen.getByLabelText("Location"), { target: { value: "Yard B" } });
    fireEvent.change(screen.getByLabelText("Seats"), { target: { value: "10" } });
    fireEvent.change(screen.getByLabelText("Cost per attendee"), { target: { value: "250" } });
    fireEvent.change(screen.getByLabelText("Currency"), { target: { value: "usd" } });
    fireEvent.click(within(screen.getByRole("form", { name: "Schedule a session" })).getByRole("button", { name: "Schedule session" }));

    await waitFor(() => expect(statusTexts().join(" ")).toContain("Session on 2026-06-01 09:00 UTC scheduled with 10 seats."));
    const sent = posts(`/api/v1/hr/courses/${COURSE}/sessions`);
    expect(sent[0].body).toEqual({
      startsAt: "2026-06-01T09:00:00Z",
      endsAt: "2026-06-01T17:00:00Z",
      location: "Yard B",
      capacity: 10,
      waitlistCapacity: 5,
      costPerAttendee: "250",
      currency: "USD",
    });
    expect(header(sent[0], "Idempotency-Key")).toBeTruthy();
    const table = await findTable({ name: "Sessions" });
    expect(within(table).getByRole("link", { name: "2026-06-01 09:00 UTC" })).toHaveAttribute("href", `/hr/courses/sessions/${SESSION}`);
    expect(within(table).getByText("250.00 USD")).toBeInTheDocument();
  });

  it("explains a course it cannot find", async () => {
    handler = (url) => (url.pathname.startsWith(`/api/v1/hr/courses/${COURSE}`) ? json(404, { code: "HCM-CRS-NOT-FOUND", detail: "course not found" }) : undefined);
    renderAt(`/hr/courses/${COURSE}`);
    expect(await screen.findByText("We couldn't find this course")).toBeInTheDocument();
  });
});

describe("course session", () => {
  function fakeSession(s: CourseSession, extra: Handler = () => undefined, rows: CourseRegistration[] = registrations) {
    handler = (url, init) => {
      const hit = extra(url, init);
      if (hit) return hit;
      if (url.pathname === `/api/v1/hr/courses/sessions/${SESSION}`) return json(200, s);
      if (url.pathname === `/api/v1/hr/courses/sessions/${SESSION}/registrations` && init.method !== "POST") return json(200, { items: rows });
      if (url.pathname === "/api/v1/hr/workers") return json(200, { items: [ada] });
      return undefined;
    };
  }

  it("sends only lines for rows that can take an outcome", () => {
    const attendedNoResult = reg("r4", "w4", "Dee", "ATTENDED");
    const passed = reg("r5", "w5", "Eve", "ATTENDED", { result: "PASS" });
    expect([registrations[0], registrations[1], attendedNoResult, passed].map(canRecord)).toEqual([true, false, true, false]);
    expect(
      completionLines({ [registrations[0].id]: "PASS", [registrations[1].id]: "NO_SHOW", r4: "FAIL", r5: "FAIL" }, [...registrations, attendedNoResult, passed]),
    ).toEqual([
      { workerId: ADA, outcome: "ATTENDED", result: "PASS" },
      { workerId: "w4", outcome: "ATTENDED", result: "FAIL" },
    ]);
    expect(completionLines({ [registrations[0].id]: "NO_SHOW" }, registrations)).toEqual([{ workerId: ADA, outcome: "NO_SHOW" }]);
  });

  it("shows seats and the attendance list, and approves a requested registration", async () => {
    fakeSession(future, (url, init) =>
      init.method === "POST" && url.pathname === `/api/v1/hr/courses/registrations/${registrations[2].id}/approve` ? json(200, { ...registrations[2], status: "CONFIRMED" }) : undefined,
    );
    const { container } = renderAt(`/hr/courses/sessions/${SESSION}`);
    const table = await findTable({ name: "Registrations" });
    expect(within(table).getByText("Waitlisted")).toBeInTheDocument();
    expect(within(table).getByText("Awaiting approval")).toBeInTheDocument();
    expect(screen.getByText("1 of 10")).toBeInTheDocument();
    expect(screen.getByText("Attendance can be recorded once the session starts.")).toBeInTheDocument();
    expect(pageViolations(container)).toEqual([]);

    fireEvent.click(screen.getByRole("button", { name: "Approve Cy's registration" }));
    await waitFor(() => expect(statusTexts().join(" ")).toContain("Cy is now confirmed."));
    expect(posts(`/api/v1/hr/courses/registrations/${registrations[2].id}/approve`)).toHaveLength(1);
    await waitFor(() => expect(gets(`/api/v1/hr/courses/sessions/${SESSION}`).length).toBeGreaterThan(1));
  });

  it("registers a worker and shows the approval warning; a full session explains itself", async () => {
    let full = false;
    fakeSession(future, (url, init) => {
      if (init.method === "POST" && url.pathname === `/api/v1/hr/courses/sessions/${SESSION}/registrations`) {
        if (full) return json(422, { code: "HCM-CRS-001", detail: "the session and its waitlist are full" });
        full = true;
        return json(201, { ...reg("r9", ADA, "Ada", "REQUESTED"), warnings: [{ code: "HCM-CRS-002", detail: "This course needs manager approval." }] });
      }
      return undefined;
    });
    renderAt(`/hr/courses/sessions/${SESSION}`);
    await screen.findByRole("option", { name: "Ada (WRK-Ada)" });
    fireEvent.click(screen.getByRole("button", { name: "Register" }));
    expect(await screen.findByText("Choose the worker to register.")).toBeInTheDocument();
    expect(posts(`/api/v1/hr/courses/sessions/${SESSION}/registrations`)).toHaveLength(0);

    fireEvent.change(screen.getByLabelText("Worker"), { target: { value: ADA } });
    fireEvent.click(screen.getByRole("button", { name: "Register" }));
    await waitFor(() => expect(statusTexts().join(" ")).toContain("Ada is awaiting approval. This course needs manager approval."));
    expect(posts(`/api/v1/hr/courses/sessions/${SESSION}/registrations`)[0].body).toEqual({ workerId: ADA });

    fireEvent.change(screen.getByLabelText("Worker"), { target: { value: ADA } });
    fireEvent.click(screen.getByRole("button", { name: "Register" }));
    expect(await screen.findByText("The session and its waitlist are full. Pick another session.")).toBeInTheDocument();
  });

  it("records attendance once the session has started, and shows a refusal", async () => {
    let refuse = true;
    fakeSession(past, (url, init) => {
      if (init.method === "POST" && url.pathname === `/api/v1/hr/courses/sessions/${SESSION}/complete`) {
        if (refuse) {
          refuse = false;
          return json(409, { code: "HCM-CRS-STATE", detail: "the registration already has a different outcome recorded" });
        }
        return json(200, { items: [{ ...registrations[0], status: "ATTENDED", result: "PASS" }], changed: 1, passed: 1 });
      }
      return undefined;
    });
    renderAt(`/hr/courses/sessions/${SESSION}`);
    await findTable({ name: "Registrations" });
    expect(screen.getByText("The session has ended, so registration is closed.")).toBeInTheDocument();
    fireEvent.click(screen.getByRole("button", { name: "Record attendance" }));
    expect(await screen.findByText(/Choose an outcome for at least one worker/)).toBeInTheDocument();

    fireEvent.change(screen.getByLabelText("Attendance for Ada"), { target: { value: "PASS" } });
    fireEvent.click(screen.getByRole("button", { name: "Record attendance" }));
    await waitFor(() => expect(alertTexts().join(" ")).toContain("different outcome recorded (HCM-CRS-STATE)"));

    fireEvent.click(screen.getByRole("button", { name: "Record attendance" }));
    await waitFor(() => expect(statusTexts().join(" ")).toContain("Recorded 1 of 1 line; 1 passed"));
    const sent = posts(`/api/v1/hr/courses/sessions/${SESSION}/complete`);
    expect(sent).toHaveLength(2);
    expect(sent[1].body).toEqual({ results: [{ workerId: ADA, outcome: "ATTENDED", result: "PASS" }] });
  });

  it("cancels a waitlisted registration and explains a session it cannot find", async () => {
    fakeSession(future, (url, init) =>
      init.method === "POST" && url.pathname === `/api/v1/hr/courses/registrations/${registrations[1].id}/cancel` ? json(200, { ...registrations[1], status: "CANCELED" }) : undefined,
    );
    const { unmount } = renderAt(`/hr/courses/sessions/${SESSION}`);
    fireEvent.click(await screen.findByRole("button", { name: "Cancel Bob's registration" }));
    await waitFor(() => expect(statusTexts().join(" ")).toContain("Bob is now cancelled."));
    unmount();

    handler = (url) => (url.pathname.startsWith(`/api/v1/hr/courses/sessions/${SESSION}`) ? json(404, { code: "HCM-CRS-SESSION-NOT-FOUND", detail: "session not found" }) : undefined);
    renderAt(`/hr/courses/sessions/${SESSION}`);
    expect(await screen.findByText("We couldn't find this session")).toBeInTheDocument();
  });
});
