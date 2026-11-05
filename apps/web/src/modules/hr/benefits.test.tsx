import { findTable } from "../../test-utils";
import { fireEvent, render, screen, waitFor, within } from "@testing-library/react";
import { MemoryRouter, Route, Routes, useLocation } from "react-router-dom";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { checkAxeAA } from "../../shell/a11y";
import type { BenefitEnrollment, BenefitPlan, BenefitSimulation, LifeEvent, Worker } from "./api/client";
import { validateEnrollment } from "./BenefitsCard";
import { formatRate, validRate } from "./format";
import hr from "./module";
import { validateBenefitPlan, type BenefitPlanValues } from "./NewBenefitPlanPage";

const ADA = "0195f0a0-0000-7000-8000-000000000001";
const BOB = "0195f0a0-0000-7000-8000-000000000002";
const PLAN = "0195f0a0-0000-7000-8000-0000000000c1";
const ENR = "0195f0a0-0000-7000-8000-0000000000e1";
const LIFE = "0195f0a0-0000-7000-8000-0000000000f1";

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
  return <p data-testid="where">{location.pathname + location.search}</p>;
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
const posts = (suffix: string) => calls.filter((c) => c.method === "POST" && c.url.pathname.endsWith(suffix));
const gets = (path: string) => calls.filter((c) => c.method === "GET" && c.url.pathname === path);
const statusTexts = () => screen.queryAllByRole("status").map((el) => el.textContent ?? "");
const alertTexts = () => screen.queryAllByRole("alert").map((el) => el.textContent ?? "");

const medical: BenefitPlan = {
  id: PLAN,
  code: "MED-PPO",
  planType: "MEDICAL",
  currency: "USD",
  eligibilityRule: "employment_type in ['FULL_TIME'] && service_days >= 30",
  enrollmentStart: "2026-11-01",
  enrollmentEnd: "2026-11-30",
  waitingDays: 0,
  preTax: true,
  payrollDeductionCode: "MED",
  options: [
    { code: "EE", coverageLevel: "Employee only", employeeRate: "12.5", employerRate: "180", rateBasis: "PER_PAY_PERIOD" },
    { code: "FAM", coverageLevel: "Family", employeeRate: "48.1", employerRate: "420", rateBasis: "PER_PAY_PERIOD" },
  ],
  createdAt: "2026-10-01T09:00:00Z",
};

const simulation: BenefitSimulation = {
  planId: PLAN,
  planCode: "MED-PPO",
  rule: medical.eligibilityRule,
  asOf: "2026-11-15",
  total: 2,
  eligible: 1,
  rows: [
    { workerId: ADA, personnelNumber: "WRK-1001", legalName: "Ada Lovelace", employmentType: "FULL_TIME", serviceDays: 318, fte: "1", eligible: true, reason: "rule true" },
    { workerId: BOB, personnelNumber: "WRK-1002", legalName: "Bob Byte", employmentType: "PART_TIME", serviceDays: 40, eligible: false, reason: "rule false" },
  ],
};

const enrolled: BenefitEnrollment = {
  id: ENR,
  workerId: ADA,
  planId: PLAN,
  planCode: "MED-PPO",
  planType: "MEDICAL",
  optionCode: "FAM",
  coverageLevel: "Family",
  validFrom: "2026-11-15T00:00:00Z",
  status: "PENDING",
  dependentCount: 2,
  cobraQualifyingEvent: false,
  employeeRate: "48.1",
  employerRate: "420",
  currency: "USD",
  rateBasis: "PER_PAY_PERIOD",
  preTax: true,
  payrollDeductionCode: "MED",
};

const birth: LifeEvent = { id: LIFE, workerId: ADA, kind: "BIRTH", eventDate: "2026-12-10", windowEnd: "2027-01-09", createdAt: "2026-12-11T09:00:00Z" };

const ada: Worker = {
  id: ADA,
  personnelNumber: "WRK-1001",
  workerType: "EMPLOYEE",
  partyPersonId: "0195f0a0-0000-7000-8000-0000000000aa",
  legalName: "Ada Lovelace",
  createdAt: "2026-01-05T09:00:00Z",
  updatedAt: "2026-01-05T09:00:00Z",
};

describe("benefit routes", () => {
  it("registers the plan list, create form and detail, with the setup-admin nav entry", () => {
    const paths = hr.routes.map((r) => r.path);
    for (const path of ["/hr/benefits/plans", "/hr/benefits/plans/new", "/hr/benefits/plans/:id"]) expect(paths).toContain(path);
    expect(hr.nav).toContainEqual({ label: "Benefit plans", to: "/hr/benefits/plans", duty: "hr.setup.admin" });
  });
});

describe("money formatting", () => {
  it("checks and pads rates by the currency's minor units without floats", () => {
    expect(validRate("12.50", "USD")).toBe(true);
    expect(validRate("12.505", "USD")).toBe(false);
    expect(validRate("1500", "JPY")).toBe(true);
    expect(validRate("1500.5", "JPY")).toBe(false);
    expect(validRate("1.125", "KWD")).toBe(true);
    expect(validRate("-1", "USD")).toBe(false);
    expect(formatRate("12.5", "USD")).toBe("12.50 USD");
    expect(formatRate("180", "USD")).toBe("180.00 USD");
    expect(formatRate("1500", "JPY")).toBe("1500 JPY");
  });
});

describe("benefit plans list", () => {
  it("lists plans with their window and options, opens one, and passes the page checks", async () => {
    handler = (url) => (url.pathname === "/api/v1/hr/benefits/plans" ? json(200, { items: [medical] }) : undefined);
    const { container } = renderAt("/hr/benefits/plans");
    const table = await findTable({ name: "Benefit plans" });
    expect(within(table).getByText("MED-PPO")).toBeInTheDocument();
    expect(within(table).getByText("2026-11-01 to 2026-11-30")).toBeInTheDocument();
    expect(within(table).getByText("Pre-tax")).toBeInTheDocument();
    expect(pageViolations(container)).toEqual([]);
    fireEvent.click(within(table).getByText("MED-PPO"));
    expect(screen.getByTestId("where")).toHaveTextContent(`/hr/benefits/plans/${PLAN}`);
  });

  it("shows the empty state, and an error with a retry that reloads", async () => {
    handler = (url) => (url.pathname === "/api/v1/hr/benefits/plans" ? json(200, { items: [] }) : undefined);
    const { unmount } = renderAt("/hr/benefits/plans");
    expect(await screen.findByText("No benefit plans yet")).toBeInTheDocument();
    unmount();

    handler = (url) => (url.pathname === "/api/v1/hr/benefits/plans" ? json(503, { code: "COM-0500", detail: "HR is down" }) : undefined);
    renderAt("/hr/benefits/plans");
    await waitFor(() => expect(alertTexts().join(" ")).toContain("HR is down (COM-0500)"));
    fireEvent.click(screen.getByRole("button", { name: "Try again" }));
    await waitFor(() => expect(gets("/api/v1/hr/benefits/plans").length).toBeGreaterThanOrEqual(3));
  });

  it("loads the next page with the cursor", async () => {
    handler = (url) => {
      if (url.pathname !== "/api/v1/hr/benefits/plans") return undefined;
      return url.searchParams.get("cursor") ? json(200, { items: [{ ...medical, id: "p2", code: "VIS-1" }] }) : json(200, { items: [medical], nextCursor: "TUVE" });
    };
    renderAt("/hr/benefits/plans");
    fireEvent.click(await screen.findByRole("button", { name: "Load more plans" }));
    expect(await screen.findByText("VIS-1")).toBeInTheDocument();
    expect(gets("/api/v1/hr/benefits/plans")[1].url.searchParams.get("cursor")).toBe("TUVE");
  });
});

describe("new benefit plan", () => {
  const base: BenefitPlanValues = {
    code: "med-ppo",
    planType: "MEDICAL",
    currency: "usd",
    eligibilityRule: " employment_type in ['FULL_TIME'] ",
    enrollmentStart: "2026-11-01",
    enrollmentEnd: "2026-11-30",
    waitingDays: "30",
    preTax: true,
    payrollDeductionCode: "",
    providerPartyId: "",
    options: [{ code: "ee", coverageLevel: "Employee only", employeeRate: "12.50", employerRate: "180", rateBasis: "PER_PAY_PERIOD" }],
  };

  it("keeps rates as strings and refuses what the service refuses", () => {
    const ok = validateBenefitPlan(base);
    expect(ok.input).toEqual({
      code: "MED-PPO",
      planType: "MEDICAL",
      currency: "USD",
      eligibilityRule: "employment_type in ['FULL_TIME']",
      enrollmentStart: "2026-11-01",
      enrollmentEnd: "2026-11-30",
      waitingDays: 30,
      preTax: true,
      options: [{ code: "EE", coverageLevel: "Employee only", employeeRate: "12.50", employerRate: "180", rateBasis: "PER_PAY_PERIOD" }],
    });
    const bad = validateBenefitPlan({
      ...base,
      code: "no spaces",
      enrollmentEnd: "2026-10-01",
      waitingDays: "400",
      providerPartyId: "nope",
      eligibilityRule: "",
      options: [
        { code: "EE", coverageLevel: "", employeeRate: "1.005", employerRate: "-1", rateBasis: "MONTHLY" },
        { code: "ee", coverageLevel: "Family", employeeRate: "1", employerRate: "1", rateBasis: "MONTHLY" },
      ],
    });
    expect(Object.keys(bad.errors).sort()).toEqual([
      "code",
      "eligibilityRule",
      "enrollmentEnd",
      "options[0].coverageLevel",
      "options[0].employeeRate",
      "options[0].employerRate",
      "options[1].code",
      "providerPartyId",
      "waitingDays",
    ]);
    expect(validateBenefitPlan({ ...base, options: [] }).errors.options).toBeTruthy();
    expect(validateBenefitPlan({ ...base, currency: "JPY", options: [{ ...base.options[0], employeeRate: "1500.5" }] }).errors["options[0].employeeRate"]).toContain("whole amount");
  });

  it("creates the plan with an Idempotency-Key, puts service field errors on the option, and opens it", async () => {
    let first = true;
    handler = (url, init) => {
      if (init.method === "POST" && url.pathname === "/api/v1/hr/benefits/plans") {
        if (first) {
          first = false;
          return json(400, { code: "COM-0001", detail: "request failed validation", errors: { eligibilityRule: "must be a valid expression: rule syntax: unexpected \"#\" at 13" } });
        }
        return json(201, medical);
      }
      if (url.pathname === `/api/v1/hr/benefits/plans/${PLAN}`) return json(200, medical);
      return undefined;
    };
    const { container } = renderAt("/hr/benefits/plans/new");
    fireEvent.change(screen.getByLabelText("Plan code"), { target: { value: "med-ppo" } });
    fireEvent.change(screen.getByLabelText("Open enrollment from"), { target: { value: "2026-11-01" } });
    fireEvent.change(screen.getByLabelText("Open enrollment to"), { target: { value: "2026-11-30" } });
    const option = screen.getByRole("group", { name: "Option 1" });
    fireEvent.change(within(option).getByLabelText("Option code"), { target: { value: "EE" } });
    fireEvent.change(within(option).getByLabelText("Coverage"), { target: { value: "Employee only" } });
    fireEvent.change(within(option).getByLabelText("Employee rate (USD)"), { target: { value: "12.50" } });
    fireEvent.change(within(option).getByLabelText("Employer rate (USD)"), { target: { value: "180" } });
    expect(pageViolations(container)).toEqual([]);

    fireEvent.click(screen.getByRole("button", { name: "Add option" }));
    expect(screen.getByRole("group", { name: "Option 2" })).toBeInTheDocument();
    fireEvent.click(screen.getByRole("button", { name: "Remove option 2" }));
    expect(screen.queryByRole("group", { name: "Option 2" })).not.toBeInTheDocument();

    fireEvent.click(screen.getByRole("button", { name: "Create plan" }));
    expect(await screen.findByText(/unexpected "#" at 13/)).toBeInTheDocument();
    fireEvent.change(screen.getByLabelText("Eligibility rule"), { target: { value: "service_days >= 30" } });
    fireEvent.click(screen.getByRole("button", { name: "Create plan" }));
    await waitFor(() => expect(screen.getByTestId("where")).toHaveTextContent(`/hr/benefits/plans/${PLAN}`));
    const sent = posts("/benefits/plans");
    expect(sent).toHaveLength(2);
    expect(sent[1].body).toMatchObject({ code: "MED-PPO", currency: "USD", eligibilityRule: "service_days >= 30", options: [{ code: "EE", employeeRate: "12.50" }] });
    expect(header(sent[0], "Idempotency-Key")).toBeTruthy();
    expect(header(sent[1], "Idempotency-Key")).not.toBe(header(sent[0], "Idempotency-Key"));
  });

  it("shows a taken code on the code field", async () => {
    handler = (url, init) =>
      init.method === "POST" && url.pathname === "/api/v1/hr/benefits/plans" ? json(409, { code: "HCM-BEN-PLAN-DUPLICATE", detail: "taken" }) : undefined;
    renderAt("/hr/benefits/plans/new");
    fireEvent.change(screen.getByLabelText("Plan code"), { target: { value: "MED" } });
    fireEvent.change(screen.getByLabelText("Open enrollment from"), { target: { value: "2026-11-01" } });
    fireEvent.change(screen.getByLabelText("Open enrollment to"), { target: { value: "2026-11-30" } });
    const option = screen.getByRole("group", { name: "Option 1" });
    fireEvent.change(within(option).getByLabelText("Option code"), { target: { value: "EE" } });
    fireEvent.change(within(option).getByLabelText("Coverage"), { target: { value: "Employee" } });
    fireEvent.change(within(option).getByLabelText("Employee rate (USD)"), { target: { value: "1" } });
    fireEvent.change(within(option).getByLabelText("Employer rate (USD)"), { target: { value: "1" } });
    fireEvent.click(screen.getByRole("button", { name: "Create plan" }));
    expect(await screen.findByText("This plan code is already used.")).toBeInTheDocument();
  });
});

describe("benefit plan detail", () => {
  it("shows the terms and options, simulates eligibility with reasons, filters, and passes the page checks", async () => {
    handler = (url) => {
      if (url.pathname === `/api/v1/hr/benefits/plans/${PLAN}/simulate`) return json(200, { ...simulation, asOf: url.searchParams.get("asOf") });
      if (url.pathname === `/api/v1/hr/benefits/plans/${PLAN}`) return json(200, medical);
      return undefined;
    };
    const { container } = renderAt(`/hr/benefits/plans/${PLAN}`);
    expect(await screen.findByRole("heading", { name: "MED-PPO", level: 2 })).toBeInTheDocument();
    const options = screen.getByRole("table", { name: "Coverage options" });
    expect(within(options).getByText("48.10 USD")).toBeInTheDocument();
    expect(within(options).getByText("420.00 USD")).toBeInTheDocument();
    expect(screen.getByText(medical.eligibilityRule)).toBeInTheDocument();
    expect(pageViolations(container)).toEqual([]);

    const form = screen.getByRole("form", { name: "Simulate eligibility" });
    fireEvent.change(within(form).getByLabelText("As of"), { target: { value: "2026-11-15" } });
    fireEvent.click(within(form).getByRole("button", { name: "Simulate" }));
    const table = await findTable({ name: "Eligibility on 2026-11-15" });
    expect(gets(`/api/v1/hr/benefits/plans/${PLAN}/simulate`)[0].url.searchParams.get("asOf")).toBe("2026-11-15");
    expect(within(table).getByText("rule false")).toBeInTheDocument();
    expect(within(table).getByRole("link", { name: "WRK-1001" })).toHaveAttribute("href", `/hr/workers/${ADA}`);
    const tiles = screen.getByRole("list", { name: "Eligibility on 2026-11-15" });
    expect(within(tiles).getByText("Workers checked").parentElement).toHaveTextContent("2");
    fireEvent.click(screen.getByLabelText("Show eligible workers only"));
    expect(within(table).queryByText("WRK-1002")).not.toBeInTheDocument();
    expect(within(table).getByText("WRK-1001")).toBeInTheDocument();
    expect(pageViolations(container)).toEqual([]);
  });

  it("shows not-found as an empty state and a simulation failure as an alert", async () => {
    handler = (url) => (url.pathname === `/api/v1/hr/benefits/plans/${PLAN}` ? json(404, { code: "HCM-BEN-PLAN-NOT-FOUND", detail: "benefit plan not found" }) : undefined);
    const { unmount } = renderAt(`/hr/benefits/plans/${PLAN}`);
    expect(await screen.findByText("We couldn't find this plan")).toBeInTheDocument();
    unmount();

    handler = (url) => {
      if (url.pathname.endsWith("/simulate")) return json(403, { code: "AUTH-0003", detail: "missing duty hr.setup.admin" });
      if (url.pathname === `/api/v1/hr/benefits/plans/${PLAN}`) return json(200, medical);
      return undefined;
    };
    renderAt(`/hr/benefits/plans/${PLAN}`);
    fireEvent.click(await screen.findByRole("button", { name: "Simulate" }));
    await waitFor(() => expect(alertTexts().join(" ")).toContain("missing duty hr.setup.admin (AUTH-0003)"));
  });
});

describe("worker benefits card", () => {
  function fakeWorker(extra: Handler = () => undefined, benefits = { workerId: ADA, enrollments: [enrolled], lifeEvents: [birth] }) {
    handler = (url, init) => {
      const hit = extra(url, init);
      if (hit) return hit;
      if (url.pathname === `/api/v1/hr/workers/${ADA}`) return json(200, { ...ada, pii: { nationalId: "***-**-6789", masked: true } });
      if (url.pathname === `/api/v1/hr/workers/${ADA}/employments`) return json(200, { items: [] });
      if (url.pathname === `/api/v1/hr/workers/${ADA}/as-of`) return json(200, { workerId: ADA, date: "2026-11-15", employments: [], positionId: null, positionNumber: null, fteShare: null, managerWorkerId: null });
      if (url.pathname === `/api/v1/hr/workers/${ADA}/absence/balances`) return json(200, { workerId: ADA, asOf: "2026-11-15", balances: [] });
      if (url.pathname === "/api/v1/hr/absence/requests" || url.pathname === "/api/v1/hr/absence/plans") return json(200, { items: [] });
      if (url.pathname === `/api/v1/hr/workers/${ADA}/benefits`) return json(200, benefits);
      if (url.pathname === "/api/v1/hr/benefits/plans") return json(200, { items: [medical] });
      return undefined;
    };
  }

  it("checks enrollment locally: plan option, window unless a life event, dependents", () => {
    const values = { planId: PLAN, optionCode: "EE", effectiveDate: "2026-11-15", lifeEventId: "", dependentCount: "1" };
    expect(validateEnrollment(ADA, values, medical).input).toEqual({ workerId: ADA, planId: PLAN, optionCode: "EE", effectiveDate: "2026-11-15", dependentCount: 1 });
    expect(validateEnrollment(ADA, { ...values, effectiveDate: "2026-12-15" }, medical).errors.effectiveDate).toContain("choose a life event");
    expect(validateEnrollment(ADA, { ...values, effectiveDate: "2026-12-15", lifeEventId: LIFE }, medical).input?.lifeEventId).toBe(LIFE);
    expect(validateEnrollment(ADA, { ...values, optionCode: "GOLD", dependentCount: "21" }, medical).errors).toEqual({
      optionCode: "Choose one of the plan's options.",
      dependentCount: "Enter 0 to 20 dependents.",
    });
    expect(validateEnrollment(ADA, values, undefined).errors.planId).toBe("Choose a plan.");
  });

  it("shows enrollments with their cost and life events with their window, and passes the page checks", async () => {
    fakeWorker();
    const { container } = renderAt(`/hr/workers/${ADA}`);
    const enrollments = await findTable({ name: "Benefit enrollments" });
    expect(within(enrollments).getByText("48.10 USD per pay period")).toBeInTheDocument();
    expect(within(enrollments).getByText("Starts later")).toBeInTheDocument();
    expect(within(screen.getByRole("table", { name: "Life events" })).getByText("2027-01-09")).toBeInTheDocument();
    expect(pageViolations(container)).toEqual([]);
  });

  it("enrolls with a cost preview and an Idempotency-Key; an eligibility refusal shows the reason", async () => {
    let refuse = true;
    fakeWorker(
      (url, init) => {
        if (init.method === "POST" && url.pathname === "/api/v1/hr/benefits/enrollments") {
          if (refuse) {
            refuse = false;
            return json(422, { code: "HCM-BEN-002", detail: "Worker not eligible for plan MED-PPO: rule false" });
          }
          return json(201, enrolled);
        }
        return undefined;
      },
      { workerId: ADA, enrollments: [], lifeEvents: [birth] },
    );
    renderAt(`/hr/workers/${ADA}`);
    await screen.findByText("Not enrolled in any plan");
    fireEvent.click(screen.getByRole("button", { name: "Enroll" }));
    const form = screen.getByRole("form", { name: "Enroll in a benefit plan" });
    fireEvent.change(within(form).getByLabelText("Coverage"), { target: { value: "FAM" } });
    expect(within(form).getByText(/The employee pays 48.10 USD per pay period before tax; the employer pays 420.00 USD/)).toBeInTheDocument();
    fireEvent.change(within(form).getByLabelText("Coverage starts"), { target: { value: "2026-11-15" } });
    fireEvent.change(within(form).getByLabelText("Dependents covered"), { target: { value: "2" } });
    fireEvent.click(within(form).getByRole("button", { name: "Enroll" }));
    await waitFor(() => expect(alertTexts().join(" ")).toContain("Worker not eligible for plan MED-PPO: rule false (HCM-BEN-002)"));
    expect(screen.getByText("Not eligible for this plan")).toBeInTheDocument();

    fireEvent.click(within(form).getByRole("button", { name: "Enroll" }));
    await waitFor(() => expect(statusTexts()).toContain("Enrolled in MED-PPO (Family) from 2026-11-15."));
    const sent = posts("/benefits/enrollments");
    expect(sent).toHaveLength(2);
    expect(sent[1].body).toEqual({ workerId: ADA, planId: PLAN, optionCode: "FAM", effectiveDate: "2026-11-15", dependentCount: 2 });
    expect(header(sent[0], "Idempotency-Key")).toBe(header(sent[1], "Idempotency-Key"));
    await waitFor(() => expect(gets(`/api/v1/hr/workers/${ADA}/benefits`)).toHaveLength(2));
  });

  it("puts a window refusal on the date field", async () => {
    fakeWorker(
      (url, init) =>
        init.method === "POST" && url.pathname.endsWith("/enrollments")
          ? json(422, { code: "HCM-BEN-001", detail: "Enrollment outside the open window or life-event period" })
          : undefined,
      { workerId: ADA, enrollments: [], lifeEvents: [birth] },
    );
    renderAt(`/hr/workers/${ADA}`);
    await screen.findByText("Not enrolled in any plan");
    fireEvent.click(screen.getByRole("button", { name: "Enroll" }));
    const form = screen.getByRole("form", { name: "Enroll in a benefit plan" });
    fireEvent.change(within(form).getByLabelText("Coverage starts"), { target: { value: "2027-01-20" } });
    fireEvent.change(within(form).getByLabelText("Life event"), { target: { value: LIFE } });
    fireEvent.click(within(form).getByRole("button", { name: "Enroll" }));
    expect(await within(form).findByText("Enrollment outside the open window or life-event period")).toBeInTheDocument();
    expect(posts("/benefits/enrollments")[0].body).toMatchObject({ lifeEventId: LIFE, effectiveDate: "2027-01-20" });
  });

  it("records a life event and reports its window", async () => {
    fakeWorker((url, init) => (init.method === "POST" && url.pathname.endsWith("/life-events") ? json(201, birth) : undefined));
    renderAt(`/hr/workers/${ADA}`);
    fireEvent.click(await screen.findByRole("button", { name: "Record life event" }));
    const form = screen.getByRole("form", { name: "Record a life event" });
    fireEvent.change(within(form).getByLabelText("Event"), { target: { value: "BIRTH" } });
    fireEvent.change(within(form).getByLabelText("Date of the event"), { target: { value: "2026-12-10" } });
    fireEvent.click(within(form).getByRole("button", { name: "Record event" }));
    await waitFor(() => expect(statusTexts()).toContain("Recorded birth on 2026-12-10. Enrollment is open until 2027-01-09."));
    expect(posts("/life-events")[0].body).toEqual({ workerId: ADA, kind: "BIRTH", eventDate: "2026-12-10" });
    expect(header(posts("/life-events")[0], "Idempotency-Key")).toBeTruthy();
  });

  it("ends coverage for a termination, refuses a day before the start locally, and reports the last day", async () => {
    fakeWorker((url, init) =>
      init.method === "POST" && url.pathname.endsWith("/end")
        ? json(200, { ...enrolled, validTo: "2027-01-01T00:00:00Z", status: "ENDED", endReason: "TERMINATION", cobraQualifyingEvent: true })
        : undefined,
    );
    renderAt(`/hr/workers/${ADA}`);
    const enrollments = await findTable({ name: "Benefit enrollments" });
    fireEvent.click(within(enrollments).getByRole("button", { name: "End MED-PPO coverage" }));
    const form = screen.getByRole("form", { name: "End MED-PPO coverage" });
    fireEvent.change(within(form).getByLabelText("Reason"), { target: { value: "TERMINATION" } });
    expect(within(form).getByText("Coverage continues to the end of this month.")).toBeInTheDocument();
    fireEvent.change(within(form).getByLabelText("Termination date"), { target: { value: "2026-11-01" } });
    fireEvent.click(within(form).getByRole("button", { name: "End coverage" }));
    expect(await within(form).findByText(/the last day can't be before it/)).toBeInTheDocument();
    expect(posts("/end")).toHaveLength(0);

    fireEvent.change(within(form).getByLabelText("Termination date"), { target: { value: "2026-12-10" } });
    fireEvent.click(within(form).getByRole("button", { name: "End coverage" }));
    await waitFor(() => expect(statusTexts()).toContain("MED-PPO coverage ends after 2026-12-31."));
    expect(posts("/end")[0].body).toEqual({ lastDay: "2026-12-10", reason: "TERMINATION" });
  });

  it("shows a load failure with a retry, and the no-plans note", async () => {
    fakeWorker((url) => (url.pathname.endsWith("/benefits") ? json(403, { code: "HCM-BEN-NOT-OWN", detail: "you can only see and change your own benefits" }) : undefined));
    const { unmount } = renderAt(`/hr/workers/${ADA}`);
    await waitFor(() => expect(alertTexts().join(" ")).toContain("you can only see and change your own benefits (HCM-BEN-NOT-OWN)"));
    unmount();

    fakeWorker((url) => (url.pathname === "/api/v1/hr/benefits/plans" ? json(200, { items: [] }) : undefined), { workerId: ADA, enrollments: [], lifeEvents: [] });
    renderAt(`/hr/workers/${ADA}`);
    expect(await screen.findByText("There are no benefit plans yet, so nobody can enroll.")).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "Enroll" })).toBeDisabled();
  });
});
