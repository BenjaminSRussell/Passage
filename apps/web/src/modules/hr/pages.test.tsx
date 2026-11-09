import { findAlert, findStatus, findTable } from "../../test-utils";
import { fireEvent, render, screen, waitFor, within } from "@testing-library/react";
import { MemoryRouter, Route, Routes, useLocation } from "react-router-dom";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { checkAxeAA } from "../../shell/a11y";
import { loadModules } from "../registry";
import type { Employment, Position, Worker } from "./api/client";
import { validateEmployment } from "./EmploymentForm";
import { lastDay, validFte } from "./format";
import hr from "./module";
import { validatePosition } from "./NewPositionPage";
import { validateWorker } from "./NewWorkerPage";

const ADA = "0195f0a0-0000-7000-8000-000000000001";
const BOSS = "0195f0a0-0000-7000-8000-000000000002";
const PARTY = "0195f0a0-0000-7000-8000-0000000000aa";
const LE = "0195f0a0-0000-7000-8000-0000000000e1";
const LE2 = "0195f0a0-0000-7000-8000-0000000000e2";
const DEPT = "0195f0a0-0000-7000-8000-0000000000d1";
const TOP = "0195f0a0-0000-7000-8000-0000000000f1";
const LOW = "0195f0a0-0000-7000-8000-0000000000f2";
const EMP = "0195f0a0-0000-7000-8000-0000000000c1";

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

const worker = (id: string, number: string, name: string): Worker => ({
  id,
  personnelNumber: number,
  workerType: "EMPLOYEE",
  partyPersonId: PARTY,
  legalName: name,
  createdAt: "2026-01-05T09:00:00Z",
  updatedAt: "2026-01-05T09:00:00Z",
});
const ada = worker(ADA, "WRK-1001", "Ada Lovelace");
const boss = worker(BOSS, "WRK-1000", "Grace Hopper");

const employment: Employment = {
  id: EMP,
  workerId: ADA,
  legalEntityId: LE,
  employmentType: "FULL_TIME",
  assignmentKind: "PRIMARY",
  hireDate: "2026-03-16",
  validFrom: "2026-03-16T00:00:00Z",
  status: "ACTIVE",
};

const top: Position = { id: TOP, positionNumber: "POS-TOP", jobCode: "CEO", departmentId: DEPT, legalEntityId: LE, fte: "1.00", allowOverfill: false, validFrom: "2026-01-01T00:00:00Z" };
const low: Position = { ...top, id: LOW, positionNumber: "POS-100", jobCode: "ENG", reportsToPositionId: TOP };

describe("hr module", () => {
  it("registers a list, a create form and a detail page for workers and positions, gated by HR duties", () => {
    expect(hr.id).toBe("hr");
    const paths = hr.routes.map((r) => r.path);
    for (const path of ["/hr", "/hr/workers", "/hr/workers/new", "/hr/workers/:id", "/hr/positions", "/hr/positions/new", "/hr/positions/:id"]) {
      expect(paths).toContain(path);
    }
    expect(hr.routes.every((r) => r.element !== null)).toBe(true);
    expect(hr.nav).toEqual([
      { label: "Workers", to: "/hr/workers", duty: "hr.worker.inquire" },
      { label: "Positions", to: "/hr/positions", duty: "hr.org.inquire" },
      { label: "Absence plans", to: "/hr/absence/plans", duty: "hr.absence.admin" },
      { label: "Absence requests", to: "/hr/absence/requests", duty: "hr.absence.self" },
      { label: "Benefit plans", to: "/hr/benefits/plans", duty: "hr.setup.admin" },
      { label: "Skills", to: "/hr/skills", duty: "hr.worker.inquire" },
      { label: "Skill search", to: "/hr/skills/search", duty: "hr.org.inquire" },
      { label: "Expiring certifications", to: "/hr/certifications/expiring", duty: "hr.worker.inquire" },
      { label: "Courses", to: "/hr/courses", duty: "hr.learning.admin" },
      { label: "Personnel actions", to: "/hr/actions", duty: "hr.action.maintain" },
      { label: "Jobs", to: "/hr/jobs", duty: "hr.org.inquire" },
      { label: "Compensation plans", to: "/hr/compensation/plans", duty: "hr.compensation.admin" },
      { label: "Compensation cycles", to: "/hr/compensation/cycles", duty: "hr.compensation.admin" },
      { label: "Requisitions", to: "/hr/requisitions", duty: "hr.recruiting.maintain" },
      { label: "Incidents", to: "/hr/incidents", duty: "hr.incident.admin" },
      { label: "Report an incident", to: "/hr/incidents/new", duty: "hr.incident.report" },
      { label: "Outstanding loans", to: "/hr/loaned-items", duty: "hr.worker.inquire" },
      { label: "Checklist templates", to: "/hr/checklist-templates", duty: "hr.setup.admin" },
      { label: "Tasks", to: "/hr/tasks", duty: "hr.worker.inquire" },
      { label: "Questionnaires", to: "/hr/questionnaires", duty: "hr.questionnaire.admin" },
      { label: "My questionnaires", to: "/hr/my/questionnaires", duty: "hr.questionnaire.self" },
      { label: "My goals", to: "/hr/my/goals", duty: "hr.performance.self" },
      { label: "My reviews", to: "/hr/my/reviews", duty: "hr.performance.self" },
      { label: "My profile", to: "/hr/my/profile", duty: "hr.profile.self" },
      { label: "My absence", to: "/hr/my/absence", duty: "hr.absence.self" },
      { label: "My team", to: "/hr/my/team", duty: "hr.team.manage" },
      { label: "Team changes", to: "/hr/my/team/changes", duty: "hr.team.approve" },
      { label: "Profile changes", to: "/hr/profile-changes", duty: "hr.worker.maintain" },
    ]);
    expect(loadModules().map((m) => m.id)).toContain("hr");
  });
});

describe("workers list", () => {
  it("lists workers, opens one, and passes the page checks", async () => {
    handler = (url) => (url.pathname === "/api/v1/hr/workers" ? json(200, { items: [boss, ada] }) : undefined);
    const { container } = renderAt("/hr/workers");
    const table = await findTable({ name: "Workers" });
    expect(within(table).getByText("Ada Lovelace")).toBeInTheDocument();
    expect(within(table).getAllByText("Employee")).toHaveLength(2);
    expect(pageViolations(container)).toEqual([]);
    fireEvent.click(within(table).getByRole("button", { name: "WRK-1001" }));
    expect(screen.getByTestId("where")).toHaveTextContent(`/hr/workers/${ADA}`);
  });

  it("searches through the URL and pages with the cursor", async () => {
    handler = (url) => {
      if (url.pathname !== "/api/v1/hr/workers") return undefined;
      if (url.searchParams.get("cursor") === "next") return json(200, { items: [ada] });
      return json(200, { items: [boss], nextCursor: "next" });
    };
    renderAt("/hr/workers");
    await screen.findByText("Grace Hopper");
    fireEvent.change(screen.getByLabelText("Name or number"), { target: { value: " hop " } });
    fireEvent.click(screen.getByRole("button", { name: "Find workers" }));
    await waitFor(() => expect(calls.some((c) => c.url.searchParams.get("q") === "hop")).toBe(true));
    fireEvent.click(await screen.findByRole("button", { name: "Load more workers" }));
    expect(await screen.findByText("Ada Lovelace")).toBeInTheDocument();
    expect(calls.at(-1)?.url.searchParams.get("cursor")).toBe("next");
    expect(calls.at(-1)?.url.searchParams.get("q")).toBe("hop");
    expect(screen.queryByRole("button", { name: "Load more workers" })).not.toBeInTheDocument();
  });

  it("shows the empty state and the error with a retry", async () => {
    let fail = true;
    handler = (url) => (url.pathname === "/api/v1/hr/workers" ? (fail ? json(503, { code: "COM-0500", detail: "HR is down" }) : json(200, { items: [] })) : undefined);
    renderAt("/hr/workers");
    expect(await findAlert()).toHaveTextContent("HR is down");
    fail = false;
    fireEvent.click(screen.getByRole("button", { name: "Try again" }));
    expect(await screen.findByText("No workers yet")).toBeInTheDocument();
  });
});

describe("new worker", () => {
  it("checks the fields before sending", () => {
    const { errors, input } = validateWorker({
      personnelNumber: "bad number!",
      workerType: "EMPLOYEE",
      partyPersonId: "nope",
      legalName: " ",
      preferredName: "",
      userId: "x",
      nationalId: "",
      bankAccount: "",
    });
    expect(input).toBeUndefined();
    expect(Object.keys(errors).sort()).toEqual(["legalName", "nationalId", "partyPersonId", "personnelNumber", "userId"]);
  });

  it("creates the worker with an Idempotency-Key and opens it; a duplicate number is a field error", async () => {
    let duplicate = true;
    handler = (url, init) => {
      if (url.pathname === "/api/v1/hr/workers" && init.method === "POST") {
        return duplicate ? json(409, { code: "HCM-WRK-DUPLICATE", detail: "exists" }) : json(201, ada);
      }
      return undefined;
    };
    renderAt("/hr/workers/new");
    fireEvent.change(screen.getByLabelText("Personnel number"), { target: { value: "wrk-1001" } });
    fireEvent.change(screen.getByLabelText("Party id"), { target: { value: PARTY } });
    fireEvent.change(screen.getByLabelText("Legal name"), { target: { value: "Ada Lovelace" } });
    fireEvent.change(screen.getByLabelText("National ID"), { target: { value: "123-45-6789" } });
    fireEvent.click(screen.getByRole("button", { name: "Create worker" }));
    expect(await screen.findByText("This personnel number is already used.")).toBeInTheDocument();
    duplicate = false;
    fireEvent.click(screen.getByRole("button", { name: "Create worker" }));
    await waitFor(() => expect(screen.getByTestId("where")).toHaveTextContent(`/hr/workers/${ADA}`));
    const [first, second] = posts("/workers");
    expect(second.body).toEqual({ personnelNumber: "WRK-1001", workerType: "EMPLOYEE", partyPersonId: PARTY, legalName: "Ada Lovelace", nationalId: "123-45-6789" });
    expect(header(first, "Idempotency-Key")).toBeTruthy();
    expect(header(second, "Idempotency-Key")).toBe(header(first, "Idempotency-Key"));
  });
});

describe("worker detail", () => {
  function fakeWorker(extra: Handler = () => undefined) {
    handler = (url, init) => {
      const hit = extra(url, init);
      if (hit) return hit;
      if (url.pathname === `/api/v1/hr/workers/${ADA}`) return json(200, { ...ada, pii: { nationalId: "***-**-6789", bankAccount: "****5432", masked: true } });
      if (url.pathname === `/api/v1/hr/workers/${ADA}/employments`) return json(200, { items: [employment] });
      if (url.pathname === `/api/v1/hr/workers/${ADA}/as-of`) {
        return json(200, { workerId: ADA, date: url.searchParams.get("date"), employments: [employment], positionId: LOW, positionNumber: "POS-100", fteShare: "1.00", managerWorkerId: BOSS });
      }
      return undefined;
    };
  }

  it("shows masked PII, the employments and the as-of manager", async () => {
    fakeWorker();
    const { container } = renderAt(`/hr/workers/${ADA}?date=2026-04-01`);
    expect(await screen.findByRole("heading", { name: "Ada Lovelace", level: 2 })).toBeInTheDocument();
    expect(screen.getByText("***-**-6789")).toBeInTheDocument();
    expect(screen.getByText("****5432")).toBeInTheDocument();
    const employments = screen.getByRole("table", { name: "Employments" });
    expect(within(employments).getByText("Active")).toBeInTheDocument();
    expect(within(employments).getByText("Open-ended")).toBeInTheDocument();
    const asOf = await screen.findByLabelText("Worker as of 2026-04-01");
    expect(within(asOf).getByRole("link", { name: "POS-100" })).toHaveAttribute("href", `/hr/positions/${LOW}`);
    expect(within(asOf).getByRole("link", { name: "Open manager" })).toHaveAttribute("href", `/hr/workers/${BOSS}`);
    expect(calls.find((c) => c.url.pathname.endsWith("/as-of"))?.url.searchParams.get("date")).toBe("2026-04-01");
    expect(pageViolations(container)).toEqual([]);
  });

  it("requires a ten-character reason before revealing, then shows unmasked values", async () => {
    fakeWorker((url) => (url.pathname === `/api/v1/hr/workers/${ADA}/pii` ? json(200, { nationalId: "123-45-6789", bankAccount: "GB82WEST12345698765432", masked: false }) : undefined));
    renderAt(`/hr/workers/${ADA}`);
    fireEvent.change(await screen.findByLabelText("Reason for reveal"), { target: { value: "short" } });
    fireEvent.click(screen.getByRole("button", { name: "Reveal" }));
    expect(await screen.findByText(/Give a reason of at least 10 characters/)).toBeInTheDocument();
    expect(calls.some((c) => c.url.pathname.endsWith("/pii"))).toBe(false);

    fireEvent.change(screen.getByLabelText("Reason for reveal"), { target: { value: "payroll correction ticket 42" } });
    fireEvent.click(screen.getByRole("button", { name: "Reveal" }));
    expect(await screen.findByText("123-45-6789")).toBeInTheDocument();
    expect(screen.getByText("Unmasked values shown")).toBeInTheDocument();
    expect(calls.find((c) => c.url.pathname.endsWith("/pii"))?.url.searchParams.get("reason")).toBe("payroll correction ticket 42");
    fireEvent.click(screen.getByRole("button", { name: "Mask again" }));
    expect(screen.getByText("***-**-6789")).toBeInTheDocument();
  });

  it("explains a reveal the user has no duty for", async () => {
    fakeWorker((url) => (url.pathname.endsWith("/pii") ? json(403, { code: "AUTH-0003", detail: "missing duty hr.pii.read" }) : undefined));
    renderAt(`/hr/workers/${ADA}`);
    fireEvent.change(await screen.findByLabelText("Reason for reveal"), { target: { value: "audit sample review" } });
    fireEvent.click(screen.getByRole("button", { name: "Reveal" }));
    expect(await screen.findByText("missing duty hr.pii.read")).toBeInTheDocument();
    expect(screen.getByText("***-**-6789")).toBeInTheDocument();
  });

  it("starts an employment and reloads the list", async () => {
    fakeWorker((url, init) => (init.method === "POST" && url.pathname.endsWith("/employments") ? json(201, { ...employment, id: "new", hireDate: "2026-05-01" }) : undefined));
    renderAt(`/hr/workers/${ADA}`);
    fireEvent.click(await screen.findByRole("button", { name: "Start employment" }));
    const form = screen.getByRole("form", { name: "Start employment" });
    fireEvent.change(within(form).getByLabelText("Legal entity id"), { target: { value: LE2 } });
    fireEvent.change(within(form).getByLabelText("Hire date"), { target: { value: "2026-05-01" } });
    fireEvent.click(within(form).getByRole("button", { name: "Start employment" }));
    await waitFor(() => expect(screen.getAllByRole("status").map((el) => el.textContent)).toContain("Employment from 2026-05-01 started."));
    expect(posts("/employments")[0].body).toEqual({ legalEntityId: LE2, employmentType: "FULL_TIME", assignmentKind: "PRIMARY", hireDate: "2026-05-01" });
    expect(header(posts("/employments")[0], "Idempotency-Key")).toBeTruthy();
    await waitFor(() => expect(calls.filter((c) => c.method === "GET" && c.url.pathname.endsWith("/employments"))).toHaveLength(2));
  });

  it("shows a not-found state for an unknown worker", async () => {
    handler = () => json(404, { code: "HCM-WRK-NOT-FOUND", detail: "worker not found" });
    renderAt(`/hr/workers/${ADA}`);
    expect(await screen.findByText("We couldn't find this worker")).toBeInTheDocument();
    expect(screen.getByRole("link", { name: "Back to workers" })).toHaveAttribute("href", "/hr/workers");
  });
});

describe("employment rules", () => {
  const base = { legalEntityId: LE, employmentType: "FULL_TIME" as const, assignmentKind: "PRIMARY" as const, homeLegalEntityId: "", hireDate: "2026-03-16", terminationDate: "", plannedEndDate: "" };

  it("refuses a termination before the hire date (HCM-EMP-002) but accepts the same day", () => {
    expect(validateEmployment({ ...base, terminationDate: "2026-03-15" }).errors.terminationDate).toMatch(/before the hire date/);
    expect(validateEmployment({ ...base, terminationDate: "2026-03-16" }).input?.terminationDate).toBe("2026-03-16");
  });

  it("needs a different home entity and a planned end for a global host (BR-42.14)", () => {
    const { errors } = validateEmployment({ ...base, assignmentKind: "GLOBAL_HOST", homeLegalEntityId: LE });
    expect(Object.keys(errors).sort()).toEqual(["homeLegalEntityId", "plannedEndDate"]);
    const ok = validateEmployment({ ...base, assignmentKind: "GLOBAL_HOST", homeLegalEntityId: LE2, plannedEndDate: "2027-06-30" });
    expect(ok.input).toMatchObject({ assignmentKind: "GLOBAL_HOST", homeLegalEntityId: LE2, plannedEndDate: "2027-06-30" });
  });
});

describe("positions", () => {
  it("lists positions with their reports-to number", async () => {
    handler = (url) => (url.pathname === "/api/v1/hr/positions" ? json(200, { items: [low, top] }) : undefined);
    const { container } = renderAt("/hr/positions");
    const table = await findTable({ name: "Positions" });
    const row = within(table).getByRole("button", { name: "POS-100" }).closest("tr") as HTMLElement;
    expect(within(row).getByText("POS-TOP")).toBeInTheDocument();
    expect(pageViolations(container)).toEqual([]);
  });

  it("validates FTE as a decimal string from 0.05 to 2.00", () => {
    expect(validFte("1.00", 5)).toBe(true);
    expect(validFte("2.00", 5)).toBe(true);
    expect(validFte("0.04", 5)).toBe(false);
    expect(validFte("2.01", 5)).toBe(false);
    expect(validFte("0.333", 5)).toBe(false);
    expect(validFte("0.01", 1)).toBe(true);
    const { errors } = validatePosition({ positionNumber: "POS-1", jobCode: "ENG", departmentId: DEPT, legalEntityId: LE, fte: "2.5", allowOverfill: false, validFrom: "2026-01-01", validTo: "2026-01-01", reportsToPositionId: "" });
    expect(Object.keys(errors).sort()).toEqual(["fte", "validTo"]);
  });

  it("creates a position under a reports-to position", async () => {
    handler = (url, init) => {
      if (url.pathname === "/api/v1/hr/positions" && init.method === "POST") return json(201, low);
      if (url.pathname === "/api/v1/hr/positions") return json(200, { items: [top] });
      return undefined;
    };
    renderAt("/hr/positions/new");
    fireEvent.change(screen.getByLabelText("Position number"), { target: { value: "pos-100" } });
    fireEvent.change(screen.getByLabelText("Job code"), { target: { value: "eng" } });
    fireEvent.change(screen.getByLabelText("Department id"), { target: { value: DEPT } });
    fireEvent.change(screen.getByLabelText("Legal entity id"), { target: { value: LE } });
    fireEvent.change(screen.getByLabelText("Starts"), { target: { value: "2026-01-01" } });
    await screen.findByRole("option", { name: "POS-TOP (CEO)" });
    fireEvent.change(screen.getByLabelText("Reports to"), { target: { value: TOP } });
    fireEvent.click(screen.getByRole("button", { name: "Create position" }));
    await waitFor(() => expect(screen.getByTestId("where")).toHaveTextContent(`/hr/positions/${LOW}`));
    expect(posts("/positions")[0].body).toEqual({
      positionNumber: "POS-100",
      jobCode: "ENG",
      departmentId: DEPT,
      legalEntityId: LE,
      fte: "1.00",
      allowOverfill: false,
      validFrom: "2026-01-01",
      reportsToPositionId: TOP,
    });
  });

  it("shows the assignments and assigns a worker through an employment in the position's entity", async () => {
    let over = true;
    const otherEntity: Employment = { ...employment, id: "elsewhere", legalEntityId: LE2 };
    handler = (url, init) => {
      if (url.pathname === `/api/v1/hr/positions/${LOW}/assignments` && init.method === "POST") {
        return over ? json(422, { code: "HCM-POS-001", detail: "Position FTE 1.00 would be exceeded" }) : json(201, { id: "a2", positionId: LOW, workerId: ADA, employmentId: EMP, fteShare: "0.50", isPrimary: true, validFrom: "2026-04-01T00:00:00Z" });
      }
      if (url.pathname === `/api/v1/hr/positions/${LOW}`) {
        return json(200, { ...low, assignments: [{ id: "a1", positionId: LOW, workerId: BOSS, employmentId: "e0", fteShare: "1.00", isPrimary: true, validFrom: "2026-01-01T00:00:00Z", validTo: "2026-04-01T00:00:00Z" }] });
      }
      if (url.pathname === "/api/v1/hr/workers") return json(200, { items: [ada] });
      if (url.pathname === `/api/v1/hr/workers/${ADA}/employments`) return json(200, { items: [employment, otherEntity] });
      return undefined;
    };
    const { container } = renderAt(`/hr/positions/${LOW}`);
    const table = await findTable({ name: "Assignments" });
    expect(within(table).getByText("2026-03-31")).toBeInTheDocument();
    expect(pageViolations(container)).toEqual([]);

    fireEvent.click(screen.getByRole("button", { name: "Assign worker" }));
    const form = screen.getByRole("form", { name: "Assign worker" });
    await within(form).findByRole("option", { name: "WRK-1001 Ada Lovelace" });
    fireEvent.change(within(form).getByLabelText("Worker"), { target: { value: ADA } });
    await within(form).findByRole("option", { name: "PRIMARY from 2026-03-16" });
    expect(within(form).getAllByRole("option").filter((o) => o.textContent?.startsWith("PRIMARY"))).toHaveLength(1);
    fireEvent.change(within(form).getByLabelText("Employment"), { target: { value: EMP } });
    fireEvent.change(within(form).getByLabelText("FTE share"), { target: { value: "0.50" } });
    fireEvent.change(within(form).getByLabelText("Starts"), { target: { value: "2026-04-01" } });
    fireEvent.click(within(form).getByRole("button", { name: "Assign worker" }));
    expect(await within(form).findByText("Position FTE 1.00 would be exceeded")).toBeInTheDocument();
    over = false;
    fireEvent.click(within(form).getByRole("button", { name: "Assign worker" }));
    expect(await findStatus()).toHaveTextContent("Worker assigned from 2026-04-01.");
    expect(posts("/assignments")[1].body).toEqual({ workerId: ADA, employmentId: EMP, fteShare: "0.50", isPrimary: true, validFrom: "2026-04-01" });
  });
});

describe("format", () => {
  it("shows the last day of a half-open range", () => {
    expect(lastDay("2026-04-01T00:00:00Z")).toBe("2026-03-31");
    expect(lastDay(undefined)).toBe("Open-ended");
  });
});
