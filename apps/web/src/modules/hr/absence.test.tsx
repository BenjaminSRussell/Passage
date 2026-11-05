import { findTable } from "../../test-utils";
import { fireEvent, render, screen, waitFor, within } from "@testing-library/react";
import { MemoryRouter, Route, Routes, useLocation } from "react-router-dom";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { checkAxeAA } from "../../shell/a11y";
import { validateAbsenceRequest } from "./AbsenceCard";
import type { AbsencePlan, AbsencePlanDetail, AbsenceRequest, Worker } from "./api/client";
import { addFixed2, lastWholeMonth, planYearOf, validDecimal, validPlanYearStart } from "./format";
import hr from "./module";
import { validateAbsencePlan } from "./NewAbsencePlanPage";

const ADA = "0195f0a0-0000-7000-8000-000000000001";
const PLAN = "0195f0a0-0000-7000-8000-0000000000b1";
const REQ = "0195f0a0-0000-7000-8000-0000000000a1";
const REQ2 = "0195f0a0-0000-7000-8000-0000000000a2";

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

const pto: AbsencePlan = {
  id: PLAN,
  code: "US-PTO-15",
  unit: "DAYS",
  accrualFrequency: "MONTHLY",
  accrualRate: "1.2500",
  accrualDay: "LAST_DAY",
  proration: "DAYS_IN_PERIOD",
  maxBalance: "22.5000",
  carryOverMax: "5.0000",
  carryOverExpiryMonths: 3,
  planYearStart: "01-01",
  negativeBalanceAllowed: false,
  minServiceDays: 0,
  createdAt: "2026-01-02T09:00:00Z",
};

const detail: AbsencePlanDetail = {
  ...pto,
  periods: [{ periodStart: "2026-03-01", periodEnd: "2026-03-31", workers: 1, accrued: "0.65", discarded: "0.00" }],
  closes: [],
};

const submitted: AbsenceRequest = {
  id: REQ,
  workerId: ADA,
  personnelNumber: "WRK-1001",
  workerName: "Ada Lovelace",
  planId: PLAN,
  planCode: "US-PTO-15",
  unit: "DAYS",
  from: "2026-06-01",
  to: "2026-06-04",
  quantity: "4.00",
  status: "SUBMITTED",
  createdAt: "2026-05-20T09:00:00Z",
};

const ada: Worker = {
  id: ADA,
  personnelNumber: "WRK-1001",
  workerType: "EMPLOYEE",
  partyPersonId: "0195f0a0-0000-7000-8000-0000000000aa",
  legalName: "Ada Lovelace",
  createdAt: "2026-01-05T09:00:00Z",
  updatedAt: "2026-01-05T09:00:00Z",
};

describe("absence routes", () => {
  it("registers the plan list, create form and detail, and the request queue", () => {
    const paths = hr.routes.map((r) => r.path);
    for (const path of ["/hr/absence/plans", "/hr/absence/plans/new", "/hr/absence/plans/:id", "/hr/absence/requests"]) {
      expect(paths).toContain(path);
    }
    expect(hr.nav).toContainEqual({ label: "Absence plans", to: "/hr/absence/plans", duty: "hr.absence.admin" });
    expect(hr.nav).toContainEqual({ label: "Absence requests", to: "/hr/absence/requests", duty: "hr.absence.self" });
  });
});

describe("absence plans list", () => {
  it("lists plans with their rate and unit, opens one, and passes the page checks", async () => {
    handler = (url) => (url.pathname === "/api/v1/hr/absence/plans" ? json(200, { items: [pto] }) : undefined);
    const { container } = renderAt("/hr/absence/plans");
    const table = await findTable({ name: "Absence plans" });
    expect(within(table).getByText("US-PTO-15")).toBeInTheDocument();
    expect(within(table).getByText("1.2500 days")).toBeInTheDocument();
    expect(within(table).getByText("Not allowed")).toBeInTheDocument();
    expect(pageViolations(container)).toEqual([]);
    fireEvent.click(within(table).getByText("US-PTO-15"));
    expect(screen.getByTestId("where")).toHaveTextContent(`/hr/absence/plans/${PLAN}`);
  });

  it("shows the empty state, and an error with a retry that reloads", async () => {
    handler = (url) => (url.pathname === "/api/v1/hr/absence/plans" ? json(200, { items: [] }) : undefined);
    const { unmount } = renderAt("/hr/absence/plans");
    expect(await screen.findByText("No absence plans yet")).toBeInTheDocument();
    unmount();

    handler = (url) => (url.pathname === "/api/v1/hr/absence/plans" ? json(503, { code: "COM-0500", detail: "HR is down" }) : undefined);
    renderAt("/hr/absence/plans");
    await waitFor(() => expect(alertTexts().join(" ")).toContain("HR is down (COM-0500)"));
    fireEvent.click(screen.getByRole("button", { name: "Try again" }));
    await waitFor(() => expect(gets("/api/v1/hr/absence/plans").length).toBeGreaterThanOrEqual(3));
  });
});

describe("new absence plan", () => {
  const base = {
    code: "us-pto-15",
    unit: "DAYS" as const,
    accrualFrequency: "MONTHLY" as const,
    accrualRate: "1.25",
    accrualDay: "LAST_DAY" as const,
    proration: "DAYS_IN_PERIOD" as const,
    maxBalance: "22.50",
    carryOverMax: "5.00",
    carryOverExpiryMonths: "3",
    planYearStart: "01-01",
    minServiceDays: "0",
    negativeBalanceAllowed: false,
  };

  it("keeps decimals as strings and refuses what the service refuses", () => {
    const ok = validateAbsencePlan(base);
    expect(ok.input).toEqual({ ...base, code: "US-PTO-15", carryOverExpiryMonths: 3, minServiceDays: 0 });
    expect(typeof ok.input?.accrualRate).toBe("string");
    const bad = validateAbsencePlan({ ...base, accrualRate: "1.23456", planYearStart: "02-29", carryOverExpiryMonths: "37", maxBalance: "-1", code: "no spaces" });
    expect(Object.keys(bad.errors).sort()).toEqual(["accrualRate", "carryOverExpiryMonths", "code", "maxBalance", "planYearStart"]);
    expect(bad.input).toBeUndefined();
  });

  it("creates the plan with an Idempotency-Key and opens it; a taken code is a field error", async () => {
    let first = true;
    handler = (url, init) => {
      if (init.method === "POST" && url.pathname === "/api/v1/hr/absence/plans") {
        if (first) {
          first = false;
          return json(409, { code: "HCM-ABS-PLAN-DUPLICATE", detail: "an absence plan with this code already exists" });
        }
        return json(201, pto);
      }
      if (url.pathname === `/api/v1/hr/absence/plans/${PLAN}`) return json(200, detail);
      return undefined;
    };
    renderAt("/hr/absence/plans/new");
    fireEvent.change(screen.getByLabelText("Plan code"), { target: { value: "us-pto-15" } });
    fireEvent.change(screen.getByLabelText("Rate per period"), { target: { value: "1.25" } });
    fireEvent.change(screen.getByLabelText("Max balance"), { target: { value: "22.50" } });
    fireEvent.click(screen.getByRole("button", { name: "Create plan" }));
    expect(await screen.findByText("This plan code is already used.")).toBeInTheDocument();
    fireEvent.change(screen.getByLabelText("Carry-over max"), { target: { value: "5.00" } });
    fireEvent.click(screen.getByRole("button", { name: "Create plan" }));
    await waitFor(() => expect(screen.getByTestId("where")).toHaveTextContent(`/hr/absence/plans/${PLAN}`));
    const sent = posts("/absence/plans");
    expect(sent).toHaveLength(2);
    expect(sent[1].body).toMatchObject({ code: "US-PTO-15", accrualRate: "1.25", maxBalance: "22.50", carryOverMax: "5.00", planYearStart: "01-01" });
    expect(header(sent[0], "Idempotency-Key")).toBeTruthy();
    expect(header(sent[1], "Idempotency-Key")).not.toBe(header(sent[0], "Idempotency-Key"));
  });
});

describe("absence plan detail", () => {
  it("shows the rule and periods, runs an accrual and reloads, and passes the page checks", async () => {
    handler = (url, init) => {
      if (init.method === "POST" && url.pathname === "/api/v1/hr/absence/accrue") {
        return json(201, { planId: PLAN, periodStart: "2026-04-01", periodEnd: "2026-04-30", planYearStart: "2026-01-01", credited: 3, alreadyAccrued: 0, accrued: "3.75", discarded: "0.00", replayed: false });
      }
      if (url.pathname === `/api/v1/hr/absence/plans/${PLAN}`) return json(200, detail);
      return undefined;
    };
    const { container } = renderAt(`/hr/absence/plans/${PLAN}`);
    expect(await screen.findByRole("heading", { name: "US-PTO-15", level: 2 })).toBeInTheDocument();
    expect(within(screen.getByRole("table", { name: "Accrual periods" })).getByText("0.65")).toBeInTheDocument();
    expect(screen.getByText("No plan year closed yet")).toBeInTheDocument();
    expect(pageViolations(container)).toEqual([]);

    const form = screen.getByRole("form", { name: "Run accrual" });
    fireEvent.change(within(form).getByLabelText("First day"), { target: { value: "2026-04-01" } });
    fireEvent.change(within(form).getByLabelText("Last day"), { target: { value: "2026-04-30" } });
    fireEvent.click(within(form).getByRole("button", { name: "Run accrual" }));
    await waitFor(() => expect(statusTexts()).toContain("Accrued 3.75 days to 3 workers for the plan year from 2026-01-01."));
    expect(posts("/absence/accrue")[0].body).toEqual({ planId: PLAN, periodStart: "2026-04-01", periodEnd: "2026-04-30" });
    await waitFor(() => expect(gets(`/api/v1/hr/absence/plans/${PLAN}`)).toHaveLength(2));
  });

  it("refuses a reversed period locally and shows the service's refusal of an open year", async () => {
    handler = (url, init) => {
      if (init.method === "POST" && url.pathname === "/api/v1/hr/absence/accrue") {
        return json(409, { code: "HCM-ABS-YEAR-OPEN", detail: "close the plan year starting 2025-01-01 before accruing the next one" });
      }
      if (url.pathname === `/api/v1/hr/absence/plans/${PLAN}`) return json(200, detail);
      return undefined;
    };
    renderAt(`/hr/absence/plans/${PLAN}`);
    const form = await screen.findByRole("form", { name: "Run accrual" });
    fireEvent.change(within(form).getByLabelText("First day"), { target: { value: "2026-04-30" } });
    fireEvent.change(within(form).getByLabelText("Last day"), { target: { value: "2026-04-01" } });
    fireEvent.click(within(form).getByRole("button", { name: "Run accrual" }));
    expect(await within(form).findByText("Enter a period whose last day is on or after its first day.")).toBeInTheDocument();
    expect(posts("/absence/accrue")).toHaveLength(0);

    fireEvent.change(within(form).getByLabelText("First day"), { target: { value: "2026-01-01" } });
    fireEvent.change(within(form).getByLabelText("Last day"), { target: { value: "2026-01-31" } });
    fireEvent.click(within(form).getByRole("button", { name: "Run accrual" }));
    await waitFor(() => expect(alertTexts().join(" ")).toContain("(HCM-ABS-YEAR-OPEN)"));
  });

  it("closes a plan year and reports what carried over", async () => {
    handler = (url, init) => {
      if (init.method === "POST" && url.pathname.endsWith("/year-end")) {
        return json(201, { planId: PLAN, planYearStart: "2026-01-01", kind: "YEAR_END", workers: 1, alreadyClosed: 0, carried: "5.00", expired: "2.90" });
      }
      if (url.pathname === `/api/v1/hr/absence/plans/${PLAN}`) return json(200, detail);
      return undefined;
    };
    renderAt(`/hr/absence/plans/${PLAN}`);
    const form = await screen.findByRole("form", { name: "Plan year" });
    fireEvent.change(within(form).getByLabelText("Plan year starting"), { target: { value: "2026-01-01" } });
    fireEvent.click(within(form).getByRole("button", { name: "Close plan year" }));
    await waitFor(() => expect(statusTexts()).toContain("Closed the plan year from 2026-01-01 for 1 workers: 5.00 days carried over, 2.90 days expired."));
    expect(posts("/year-end")[0].body).toEqual({ planYearStart: "2026-01-01" });
    expect(within(form).getByRole("button", { name: "Expire carry-over" })).toBeInTheDocument();
  });

  it("shows a not-found state for an unknown plan", async () => {
    handler = (url) => (url.pathname.startsWith("/api/v1/hr/absence/plans/") ? json(404, { code: "HCM-ABS-PLAN-NOT-FOUND", detail: "absence plan not found" }) : undefined);
    renderAt(`/hr/absence/plans/${PLAN}`);
    expect(await screen.findByText("We couldn't find this plan")).toBeInTheDocument();
  });
});

describe("absence requests queue", () => {
  it("lists submitted requests by default, approves one and reloads, and passes the page checks", async () => {
    let decided = false;
    handler = (url, init) => {
      if (init.method === "POST" && url.pathname === `/api/v1/hr/absence/requests/${REQ}/decide`) {
        decided = true;
        return json(200, { ...submitted, status: "APPROVED" });
      }
      if (url.pathname === "/api/v1/hr/absence/requests") return json(200, { items: decided ? [] : [submitted] });
      return undefined;
    };
    const { container } = renderAt("/hr/absence/requests");
    const table = await findTable({ name: "Absence requests" });
    expect(within(table).getByRole("link", { name: "Ada Lovelace (WRK-1001)" })).toHaveAttribute("href", `/hr/workers/${ADA}`);
    expect(within(table).getByText("4.00 days")).toBeInTheDocument();
    expect(gets("/api/v1/hr/absence/requests")[0].url.searchParams.get("status")).toBe("SUBMITTED");
    expect(pageViolations(container)).toEqual([]);

    fireEvent.click(within(table).getByRole("button", { name: "Approve Ada Lovelace's request from 2026-06-01" }));
    await waitFor(() => expect(statusTexts()).toContain("Ada Lovelace's request from 2026-06-01 is now approved."));
    expect(posts("/decide")[0].body).toEqual({ decision: "APPROVE" });
    expect(await screen.findByText("Nothing waiting for a decision")).toBeInTheDocument();
  });

  it("shows a refused self-approval and filters by status through the URL", async () => {
    handler = (url, init) => {
      if (init.method === "POST" && url.pathname.endsWith("/decide")) {
        return json(403, { code: "HCM-ABS-SELF-APPROVAL", detail: "an approver cannot decide their own absence request" });
      }
      if (url.pathname === "/api/v1/hr/absence/requests") {
        return json(200, { items: url.searchParams.get("status") === "APPROVED" ? [{ ...submitted, id: REQ2, status: "APPROVED" }] : [submitted] });
      }
      return undefined;
    };
    renderAt("/hr/absence/requests");
    const table = await findTable({ name: "Absence requests" });
    fireEvent.click(within(table).getByRole("button", { name: "Approve Ada Lovelace's request from 2026-06-01" }));
    await waitFor(() => expect(alertTexts().join(" ")).toContain("an approver cannot decide their own absence request (HCM-ABS-SELF-APPROVAL)"));

    fireEvent.change(screen.getByLabelText("Status"), { target: { value: "APPROVED" } });
    await waitFor(() => expect(screen.getByTestId("where")).toHaveTextContent("/hr/absence/requests?status=APPROVED"));
    const approved = await findTable({ name: "Absence requests" });
    await waitFor(() => expect(within(approved).getByText("Approved")).toBeInTheDocument());
    expect(within(approved).queryByRole("button", { name: /Approve/ })).toBeNull();
  });
});

describe("worker absence", () => {
  function fakeWorker(extra: Handler = () => undefined) {
    handler = (url, init) => {
      const hit = extra(url, init);
      if (hit) return hit;
      if (url.pathname === `/api/v1/hr/workers/${ADA}`) return json(200, { ...ada, pii: { nationalId: "***-**-6789", masked: true } });
      if (url.pathname === `/api/v1/hr/workers/${ADA}/employments`) return json(200, { items: [] });
      if (url.pathname === `/api/v1/hr/workers/${ADA}/as-of`) return json(200, { workerId: ADA, date: "2026-04-01", employments: [], positionId: null, positionNumber: null, fteShare: null, managerWorkerId: null });
      if (url.pathname === `/api/v1/hr/workers/${ADA}/absence/balances`) {
        return json(200, {
          workerId: ADA,
          asOf: url.searchParams.get("asOf"),
          balances: [
            {
              planId: PLAN,
              planCode: "US-PTO-15",
              unit: "DAYS",
              planYearStart: "2027-01-01",
              carryOver: { accrued: "5.00", used: "1.50", adjusted: "0.00", expired: "0.00", balance: "3.50" },
              current: { accrued: "1.25", used: "0.25", adjusted: "0.00", expired: "0.00", balance: "1.00" },
              total: "4.50",
            },
          ],
        });
      }
      if (url.pathname === "/api/v1/hr/absence/requests") return json(200, { items: [submitted] });
      if (url.pathname === "/api/v1/hr/absence/plans") return json(200, { items: [pto] });
      return undefined;
    };
  }

  it("shows the plan-year balance with carry-over and the worker's requests", async () => {
    fakeWorker();
    const { container } = renderAt(`/hr/workers/${ADA}`);
    const balances = await findTable({ name: "Absence balances" });
    expect(within(balances).getByText("4.50 days")).toBeInTheDocument();
    expect(within(balances).getByText("1.75")).toBeInTheDocument();
    expect(within(balances).getByText("3.50")).toBeInTheDocument();
    expect(gets(`/api/v1/hr/workers/${ADA}/absence/balances`)[0].url.searchParams.get("asOf")).toMatch(/^\d{4}-\d{2}-\d{2}$/);
    expect(gets("/api/v1/hr/absence/requests")[0].url.searchParams.get("workerId")).toBe(ADA);
    expect(within(screen.getByRole("table", { name: "Absence requests" })).getByText("Submitted")).toBeInTheDocument();
    expect(pageViolations(container)).toEqual([]);
  });

  it("submits a request with an Idempotency-Key, shows a balance refusal on the quantity, then the overlap warning", async () => {
    let attempt = 0;
    fakeWorker((url, init) => {
      if (init.method === "POST" && url.pathname === "/api/v1/hr/absence/requests") {
        attempt += 1;
        if (attempt === 1) return json(422, { code: "HCM-ABS-002", detail: "Requested 3.00 exceeds balance 2.00" });
        return json(201, { ...submitted, id: REQ2, from: "2026-06-03", to: "2026-06-04", quantity: "2.00", warnings: [{ code: "HCM-ABS-003", detail: `Request overlaps submitted request ${REQ}` }] });
      }
      return undefined;
    });
    renderAt(`/hr/workers/${ADA}`);
    fireEvent.click(await screen.findByRole("button", { name: "Request absence" }));
    const form = screen.getByRole("form", { name: "Request absence" });
    fireEvent.change(within(form).getByLabelText("First day"), { target: { value: "2026-06-03" } });
    fireEvent.change(within(form).getByLabelText("Last day"), { target: { value: "2026-06-04" } });
    fireEvent.change(within(form).getByLabelText("Quantity (days)"), { target: { value: "3" } });
    fireEvent.click(within(form).getByRole("button", { name: "Submit request" }));
    expect(await within(form).findByText("These dates allow at most 2 days.")).toBeInTheDocument();
    expect(posts("/absence/requests")).toHaveLength(0);

    fireEvent.change(within(form).getByLabelText("Quantity (days)"), { target: { value: "2" } });
    fireEvent.click(within(form).getByRole("button", { name: "Submit request" }));
    expect(await within(form).findByText("Requested 3.00 exceeds balance 2.00")).toBeInTheDocument();
    fireEvent.click(within(form).getByRole("button", { name: "Submit request" }));
    await waitFor(() => expect(statusTexts().join(" ")).toContain("Requested 2.00 days from 2026-06-03 to 2026-06-04."));
    expect(statusTexts().join(" ")).toContain("(HCM-ABS-003)");
    const sent = posts("/absence/requests");
    expect(sent[1].body).toEqual({ workerId: ADA, planId: PLAN, from: "2026-06-03", to: "2026-06-04", quantity: "2" });
    expect(header(sent[1], "Idempotency-Key")).toBe(header(sent[0], "Idempotency-Key"));
  });

  it("cancels a submitted request", async () => {
    fakeWorker((url, init) => (init.method === "POST" && url.pathname.endsWith("/cancel") ? json(200, { ...submitted, status: "CANCELED" }) : undefined));
    renderAt(`/hr/workers/${ADA}`);
    fireEvent.click(await screen.findByRole("button", { name: "Cancel the request from 2026-06-01" }));
    await waitFor(() => expect(statusTexts()).toContain("The request from 2026-06-01 is cancelled."));
    expect(posts(`/absence/requests/${REQ}/cancel`)).toHaveLength(1);
  });
});

describe("absence rules in the browser", () => {
  it("checks request dates, quantity and span before sending", () => {
    const ok = validateAbsenceRequest(ADA, { planId: PLAN, from: "2026-06-01", to: "2026-06-01", quantity: "0.5", reason: " dentist " });
    expect(ok.input).toEqual({ workerId: ADA, planId: PLAN, from: "2026-06-01", to: "2026-06-01", quantity: "0.5", reason: "dentist" });
    expect(validateAbsenceRequest(ADA, { planId: PLAN, from: "2026-06-02", to: "2026-06-01", quantity: "1", reason: "" }).errors.to).toBeTruthy();
    expect(validateAbsenceRequest(ADA, { planId: PLAN, from: "2026-06-01", to: "2026-06-01", quantity: "0", reason: "" }).errors.quantity).toBeTruthy();
    expect(validateAbsenceRequest(ADA, { planId: PLAN, from: "2026-06-01", to: "2026-06-01", quantity: "8", reason: "" }, "HOURS").input).toBeDefined();
    expect(validateAbsenceRequest(ADA, { planId: PLAN, from: "2026-06-01", to: "2026-06-01", quantity: "25", reason: "" }, "HOURS").errors.quantity).toBeTruthy();
  });

  it("formats and checks absence values without floats", () => {
    expect(addFixed2("0.10", "0.20")).toBe("0.30");
    expect(addFixed2("-1.50", "0.25")).toBe("-1.25");
    expect(validDecimal("1.2500", 4)).toBe(true);
    expect(validDecimal("1.23456", 4)).toBe(false);
    expect(validDecimal("0", 2, false)).toBe(false);
    expect(validPlanYearStart("04-15")).toBe(true);
    expect(validPlanYearStart("02-29")).toBe(false);
    expect(validPlanYearStart("13-01")).toBe(false);
    expect(lastWholeMonth("2026-03-16")).toEqual({ start: "2026-02-01", end: "2026-02-28" });
    expect(lastWholeMonth("2026-01-05")).toEqual({ start: "2025-12-01", end: "2025-12-31" });
    expect(planYearOf("04-15", "2026-04-14")).toBe("2025-04-15");
    expect(planYearOf("01-01", "2026-03-16")).toBe("2026-01-01");
  });
});
