import { findTable } from "../../test-utils";
import { fireEvent, render, screen, waitFor, within } from "@testing-library/react";
import { MemoryRouter, Route, Routes, useLocation } from "react-router-dom";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { checkAxeAA } from "../../shell/a11y";
import type { Certification, ExpiringCertification, Position, Skill, SkillGap, SkillRequirement, Worker, WorkerSkill } from "./api/client";
import { validateCertification, type CertificationValues } from "./CertificationsCard";
import hr from "./module";
import { validateSkill } from "./NewSkillPage";
import { validateSearch } from "./SkillSearchPage";
import { validateRating } from "./SkillsCard";

const ADA = "0195f0a0-0000-7000-8000-000000000001";
const BOB = "0195f0a0-0000-7000-8000-000000000002";
const POS = "0195f0a0-0000-7000-8000-0000000000f2";
const WELD_ID = "0195f0a0-0000-7000-8000-0000000000a1";
const PLC_ID = "0195f0a0-0000-7000-8000-0000000000a2";
const CERT = "0195f0a0-0000-7000-8000-0000000000b1";

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
      const body = reply.status === 204 ? null : JSON.stringify(reply.body);
      return new Response(body, { status: reply.status, headers: { "Content-Type": "application/json" } });
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
const posts = (path: string) => calls.filter((c) => c.method === "POST" && c.url.pathname === path);
const gets = (path: string) => calls.filter((c) => c.method === "GET" && c.url.pathname === path);
const statusTexts = () => screen.queryAllByRole("status").map((el) => el.textContent ?? "");
const alertTexts = () => screen.queryAllByRole("alert").map((el) => el.textContent ?? "");

const welding: Skill = { id: WELD_ID, code: "WELDING", name: "Welding", scaleMax: 5, createdAt: "2026-02-01T09:00:00Z" };
const plc: Skill = { id: PLC_ID, code: "PLC", name: "PLC programming", scaleMax: 3, createdAt: "2026-02-02T09:00:00Z" };

const ada: Worker = {
  id: ADA,
  personnelNumber: "WRK-1001",
  workerType: "EMPLOYEE",
  partyPersonId: "0195f0a0-0000-7000-8000-0000000000aa",
  legalName: "Ada Lovelace",
  createdAt: "2026-01-05T09:00:00Z",
  updatedAt: "2026-01-05T09:00:00Z",
};

const pos200: Position = {
  id: POS,
  positionNumber: "POS-200",
  jobCode: "WELDER",
  departmentId: "0195f0a0-0000-7000-8000-0000000000d1",
  legalEntityId: "0195f0a0-0000-7000-8000-0000000000e1",
  fte: "1.00",
  allowOverfill: false,
  validFrom: "2026-01-01T00:00:00Z",
};

const rated: WorkerSkill = {
  workerId: ADA,
  skillId: WELD_ID,
  skillCode: "WELDING",
  skillName: "Welding",
  scaleMax: 5,
  rating: 4,
  assessedOn: "2026-03-01",
  updatedAt: "2026-03-01T09:00:00Z",
};

const forklift: Certification = { id: CERT, workerId: ADA, code: "FORKLIFT", name: "Forklift licence", issuedOn: "2026-01-10", expiresOn: "2028-01-10", valid: true };

/** AC-42.16: POS-200 needs WELDING 3 and PLC 2; the worker has WELDING 4, PLC 1. */
const ac4216: SkillGap = {
  workerId: ADA,
  positionId: POS,
  met: false,
  comparison: [
    { skill: "PLC", required: 2, actual: 1, delta: -1 },
    { skill: "WELDING", required: 3, actual: 4, delta: 1 },
  ],
  gaps: [{ skill: "PLC", required: 2, actual: 1, delta: -1 }],
};

describe("skill routes", () => {
  it("registers the catalog, create form, search and expiring inquiry with their duties", () => {
    const paths = hr.routes.map((r) => r.path);
    for (const path of ["/hr/skills", "/hr/skills/new", "/hr/skills/search", "/hr/certifications/expiring"]) expect(paths).toContain(path);
    expect(hr.nav).toContainEqual({ label: "Skills", to: "/hr/skills", duty: "hr.worker.inquire" });
    expect(hr.nav).toContainEqual({ label: "Skill search", to: "/hr/skills/search", duty: "hr.org.inquire" });
    expect(hr.nav).toContainEqual({ label: "Expiring certifications", to: "/hr/certifications/expiring", duty: "hr.worker.inquire" });
  });
});

describe("skill catalog", () => {
  it("lists skills with their scale and passes the page checks", async () => {
    handler = (url) => (url.pathname === "/api/v1/hr/skills" ? json(200, { items: [plc, welding] }) : undefined);
    const { container } = renderAt("/hr/skills");
    const table = await findTable({ name: "Skills" });
    expect(within(table).getByText("WELDING")).toBeInTheDocument();
    expect(within(table).getByText("1 to 3")).toBeInTheDocument();
    expect(pageViolations(container)).toEqual([]);
  });

  it("shows the empty state, and an error with a retry that reloads", async () => {
    handler = (url) => (url.pathname === "/api/v1/hr/skills" ? json(200, { items: [] }) : undefined);
    const { unmount } = renderAt("/hr/skills");
    expect(await screen.findByText("No skills yet")).toBeInTheDocument();
    unmount();

    handler = (url) => (url.pathname === "/api/v1/hr/skills" ? json(403, { code: "AUTH-0003", detail: "missing duty hr.worker.inquire" }) : undefined);
    renderAt("/hr/skills");
    await waitFor(() => expect(alertTexts().join(" ")).toContain("missing duty hr.worker.inquire (AUTH-0003)"));
    const before = gets("/api/v1/hr/skills").length;
    fireEvent.click(screen.getByRole("button", { name: "Try again" }));
    await waitFor(() => expect(gets("/api/v1/hr/skills").length).toBeGreaterThan(before));
  });

  it("loads the next page with the cursor", async () => {
    handler = (url) => {
      if (url.pathname !== "/api/v1/hr/skills") return undefined;
      return url.searchParams.get("cursor") ? json(200, { items: [welding] }) : json(200, { items: [plc], nextCursor: "UExD" });
    };
    renderAt("/hr/skills");
    fireEvent.click(await screen.findByRole("button", { name: "Load more skills" }));
    expect(await screen.findByText("WELDING")).toBeInTheDocument();
    expect(gets("/api/v1/hr/skills")[1].url.searchParams.get("cursor")).toBe("UExD");
  });
});

describe("new skill", () => {
  it("upper-cases the code and refuses what the service refuses", () => {
    expect(validateSkill({ code: " welding ", name: " Welding ", scaleMax: "5" }).input).toEqual({ code: "WELDING", name: "Welding", scaleMax: 5 });
    expect(Object.keys(validateSkill({ code: "no spaces", name: "", scaleMax: "11" }).errors).sort()).toEqual(["code", "name", "scaleMax"]);
  });

  it("creates with an Idempotency-Key, puts a duplicate code on the field, then opens the catalog", async () => {
    let duplicate = true;
    handler = (url, init) => {
      if (init.method === "POST" && url.pathname === "/api/v1/hr/skills") {
        if (duplicate) {
          duplicate = false;
          return json(409, { code: "HCM-SKL-DUPLICATE", detail: "a skill with this code already exists" });
        }
        return json(201, welding);
      }
      if (url.pathname === "/api/v1/hr/skills") return json(200, { items: [welding] });
      return undefined;
    };
    renderAt("/hr/skills/new");
    fireEvent.change(screen.getByLabelText("Code"), { target: { value: "welding" } });
    fireEvent.change(screen.getByLabelText("Name"), { target: { value: "Welding" } });
    fireEvent.click(screen.getByRole("button", { name: "Add skill" }));
    expect(await screen.findByText("This skill code is already in the catalog.")).toBeInTheDocument();

    fireEvent.click(screen.getByRole("button", { name: "Add skill" }));
    await waitFor(() => expect(screen.getByTestId("where")).toHaveTextContent(/^\/hr\/skills$/));
    const sent = posts("/api/v1/hr/skills");
    expect(sent).toHaveLength(2);
    expect(sent[1].body).toEqual({ code: "WELDING", name: "Welding", scaleMax: 5 });
    expect(header(sent[1], "Idempotency-Key")).toBeTruthy();
    expect(header(sent[1], "Idempotency-Key")).toBe(header(sent[0], "Idempotency-Key"));
  });
});

describe("worker skills and certifications", () => {
  function fakeWorker(extra: Handler = () => undefined, profile: WorkerSkill[] = [rated], certs: Certification[] = [forklift]) {
    handler = (url, init) => {
      const hit = extra(url, init);
      if (hit) return hit;
      if (url.pathname === `/api/v1/hr/workers/${ADA}`) return json(200, { ...ada, pii: { nationalId: "***-**-6789", masked: true } });
      if (url.pathname === `/api/v1/hr/workers/${ADA}/employments`) return json(200, { items: [] });
      if (url.pathname === `/api/v1/hr/workers/${ADA}/as-of`) return json(200, { workerId: ADA, date: "2026-06-01", employments: [], positionId: null, positionNumber: null, fteShare: null, managerWorkerId: null });
      if (url.pathname === `/api/v1/hr/workers/${ADA}/absence/balances`) return json(200, { workerId: ADA, asOf: "2026-06-01", balances: [] });
      if (url.pathname === "/api/v1/hr/absence/requests" || url.pathname === "/api/v1/hr/absence/plans" || url.pathname === "/api/v1/hr/benefits/plans") return json(200, { items: [] });
      if (url.pathname === `/api/v1/hr/workers/${ADA}/benefits`) return json(200, { workerId: ADA, enrollments: [], lifeEvents: [] });
      if (url.pathname === "/api/v1/hr/skills") return json(200, { items: [plc, welding] });
      if (url.pathname === `/api/v1/hr/workers/${ADA}/skills` && init.method !== "POST") return json(200, { items: profile });
      if (url.pathname === `/api/v1/hr/workers/${ADA}/certifications` && init.method !== "POST") return json(200, { asOf: "2026-06-01", items: certs });
      if (url.pathname === "/api/v1/hr/positions") return json(200, { items: [pos200] });
      if (url.pathname === `/api/v1/hr/workers/${ADA}/skill-gap`) return json(200, ac4216);
      return undefined;
    };
  }

  it("checks a rating against the skill's scale and refuses future assessments", () => {
    const skills = [plc, welding];
    expect(validateRating({ skillCode: "PLC", rating: "3", assessedOn: "2026-03-01" }, skills, "2026-06-01").input).toEqual({ skillCode: "PLC", rating: 3, assessedOn: "2026-03-01" });
    expect(validateRating({ skillCode: "PLC", rating: "4", assessedOn: "2026-03-01" }, skills, "2026-06-01").errors.rating).toBe("Choose 1 to 3.");
    expect(validateRating({ skillCode: "PLC", rating: "1", assessedOn: "2026-07-01" }, skills, "2026-06-01").errors.assessedOn).toContain("future");
    expect(validateRating({ skillCode: "NOPE", rating: "1", assessedOn: "2026-03-01" }, skills, "2026-06-01").errors.skillCode).toBe("Choose a skill.");
  });

  it("shows the profile and certificates, and the AC-42.16 gap against a position", async () => {
    fakeWorker();
    const { container } = renderAt(`/hr/workers/${ADA}`);
    const profile = await findTable({ name: "Skill profile" });
    expect(within(profile).getByText("4 of 5")).toBeInTheDocument();
    const certs = await findTable({ name: "Certifications" });
    expect(within(certs).getByText("FORKLIFT")).toBeInTheDocument();
    expect(within(certs).getByText("Valid")).toBeInTheDocument();
    expect(pageViolations(container)).toEqual([]);

    await screen.findByRole("option", { name: "POS-200 (WELDER)" });
    fireEvent.change(screen.getByLabelText("Compare with position"), { target: { value: POS } });
    const gap = await findTable({ name: "Skill gap" });
    expect(within(gap).getByText("-1")).toBeInTheDocument();
    expect(within(gap).getByText("+1")).toBeInTheDocument();
    expect(screen.getByText("1 skill short")).toBeInTheDocument();
    expect(gets(`/api/v1/hr/workers/${ADA}/skill-gap`)[0].url.searchParams.get("positionId")).toBe(POS);
  });

  it("rates a skill; a stale assessment lands on the date field", async () => {
    let stale = true;
    fakeWorker((url, init) => {
      if (init.method === "POST" && url.pathname === `/api/v1/hr/workers/${ADA}/skills`) {
        if (stale) {
          stale = false;
          return json(409, { code: "HCM-SKL-STALE", detail: "the worker has a newer assessment of this skill, dated 2026-05-01" });
        }
        return json(200, { ...rated, skillId: PLC_ID, skillCode: "PLC", skillName: "PLC programming", scaleMax: 3, rating: 2 });
      }
      return undefined;
    }, []);
    renderAt(`/hr/workers/${ADA}`);
    expect(await screen.findByText("Not rated yet")).toBeInTheDocument();
    fireEvent.click(await screen.findByRole("button", { name: "Rate a skill" }));
    const form = screen.getByRole("form", { name: "Rate a skill" });
    fireEvent.change(within(form).getByLabelText("Skill"), { target: { value: "PLC" } });
    expect(within(form).queryByRole("option", { name: "4" })).toBeNull();
    fireEvent.change(within(form).getByLabelText("Rating"), { target: { value: "2" } });
    fireEvent.change(within(form).getByLabelText("Assessed on"), { target: { value: "2026-04-01" } });
    fireEvent.click(within(form).getByRole("button", { name: "Save rating" }));
    expect(await within(form).findByText("the worker has a newer assessment of this skill, dated 2026-05-01")).toBeInTheDocument();

    fireEvent.change(within(form).getByLabelText("Assessed on"), { target: { value: "2026-05-02" } });
    fireEvent.click(within(form).getByRole("button", { name: "Save rating" }));
    await waitFor(() => expect(statusTexts()).toContain("PLC rated 2 of 3."));
    expect(posts(`/api/v1/hr/workers/${ADA}/skills`)[1].body).toEqual({ skillCode: "PLC", rating: 2, assessedOn: "2026-05-02" });
  });

  it("checks a certificate's expiry choice locally", () => {
    const base: CertificationValues = { code: "forklift", name: "Forklift licence", issuer: "", issuedOn: "2026-01-10", expiry: "months", validityMonths: "24", expiresOn: "" };
    expect(validateCertification(base, "2026-06-01").input).toEqual({ code: "FORKLIFT", name: "Forklift licence", issuedOn: "2026-01-10", validityMonths: 24 });
    expect(validateCertification({ ...base, validityMonths: "0" }, "2026-06-01").errors.validityMonths).toBe("Enter 1 to 240 months.");
    expect(validateCertification({ ...base, expiry: "date", expiresOn: "2026-01-10" }, "2026-06-01").errors.expiresOn).toContain("after the issue date");
    expect(validateCertification({ ...base, expiry: "never" }, "2026-06-01").input).toEqual({ code: "FORKLIFT", name: "Forklift licence", issuedOn: "2026-01-10" });
    expect(validateCertification({ ...base, issuedOn: "2026-07-01" }, "2026-06-01").errors.issuedOn).toContain("future");
  });

  it("records a certificate with an Idempotency-Key; a duplicate lands on the issue date", async () => {
    let duplicate = true;
    fakeWorker(
      (url, init) => {
        if (init.method === "POST" && url.pathname === `/api/v1/hr/workers/${ADA}/certifications`) {
          if (duplicate) {
            duplicate = false;
            return json(409, { code: "HCM-CERT-DUPLICATE", detail: "the worker already has a FORKLIFT certificate issued on 2026-01-10" });
          }
          return json(201, forklift);
        }
        return undefined;
      },
      [rated],
      [],
    );
    renderAt(`/hr/workers/${ADA}`);
    expect(await screen.findByText("No certificates")).toBeInTheDocument();
    fireEvent.click(screen.getByRole("button", { name: "Record certificate" }));
    const form = screen.getByRole("form", { name: "Record a certificate" });
    fireEvent.change(within(form).getByLabelText("Code"), { target: { value: "forklift" } });
    fireEvent.change(within(form).getByLabelText("Title"), { target: { value: "Forklift licence" } });
    fireEvent.change(within(form).getByLabelText("Issued on"), { target: { value: "2026-01-10" } });
    fireEvent.click(within(form).getByRole("button", { name: "Record certificate" }));
    expect(await within(form).findByText("the worker already has a FORKLIFT certificate issued on 2026-01-10")).toBeInTheDocument();

    fireEvent.click(within(form).getByRole("button", { name: "Record certificate" }));
    await waitFor(() => expect(statusTexts()).toContain("FORKLIFT recorded, valid until the day before 2028-01-10."));
    const sent = posts(`/api/v1/hr/workers/${ADA}/certifications`);
    expect(sent[1].body).toEqual({ code: "FORKLIFT", name: "Forklift licence", issuedOn: "2026-01-10", validityMonths: 24 });
    expect(header(sent[1], "Idempotency-Key")).toBe(header(sent[0], "Idempotency-Key"));
  });

  it("shows the profile error with a retry", async () => {
    fakeWorker((url, init) =>
      url.pathname === `/api/v1/hr/workers/${ADA}/skills` && init.method !== "POST" ? json(503, { code: "COM-0500", detail: "HR is down" }) : undefined,
    );
    renderAt(`/hr/workers/${ADA}`);
    await waitFor(() => expect(alertTexts().join(" ")).toContain("HR is down (COM-0500)"));
    expect(screen.getByText("Could not load the skill profile")).toBeInTheDocument();
  });
});

describe("position skill profile", () => {
  const requirement: SkillRequirement = { positionId: POS, skillId: WELD_ID, skillCode: "WELDING", skillName: "Welding", scaleMax: 5, minRating: 3 };

  it("lists, sets and removes required skills", async () => {
    let profile: SkillRequirement[] = [requirement];
    handler = (url, init) => {
      if (url.pathname === `/api/v1/hr/positions/${POS}`) return json(200, { ...pos200, assignments: [] });
      if (url.pathname === "/api/v1/hr/skills") return json(200, { items: [plc, welding] });
      if (url.pathname === `/api/v1/hr/positions/${POS}/skill-requirements`) {
        if (init.method === "POST") {
          const saved: SkillRequirement = { positionId: POS, skillId: PLC_ID, skillCode: "PLC", skillName: "PLC programming", scaleMax: 3, minRating: 2 };
          profile = [saved, ...profile];
          return json(200, saved);
        }
        return json(200, { items: profile });
      }
      if (url.pathname === `/api/v1/hr/positions/${POS}/skill-requirements/WELDING` && init.method === "DELETE") {
        profile = profile.filter((r) => r.skillCode !== "WELDING");
        return json(204, null);
      }
      return undefined;
    };
    const { container } = renderAt(`/hr/positions/${POS}`);
    const table = await findTable({ name: "Required skills" });
    expect(within(table).getByText("3 of 5")).toBeInTheDocument();
    expect(pageViolations(container)).toEqual([]);

    const form = screen.getByRole("form", { name: "Require a skill" });
    await within(form).findByRole("option", { name: "PLC – PLC programming" });
    fireEvent.click(within(form).getByRole("button", { name: "Save requirement" }));
    expect(await within(form).findByText("Choose a skill.")).toBeInTheDocument();
    fireEvent.change(within(form).getByLabelText("Skill"), { target: { value: "PLC" } });
    fireEvent.change(within(form).getByLabelText("Minimum rating"), { target: { value: "2" } });
    fireEvent.click(within(form).getByRole("button", { name: "Save requirement" }));
    await waitFor(() => expect(statusTexts()).toContain("PLC now requires at least 2."));
    expect(posts(`/api/v1/hr/positions/${POS}/skill-requirements`)[0].body).toEqual({ skillCode: "PLC", minRating: 2 });

    fireEvent.click(await screen.findByRole("button", { name: "Remove WELDING" }));
    await waitFor(() => expect(statusTexts()).toContain("WELDING is no longer required."));
    expect(calls.some((c) => c.method === "DELETE" && c.url.pathname === `/api/v1/hr/positions/${POS}/skill-requirements/WELDING`)).toBe(true);
    await waitFor(() => expect(within(screen.getByRole("table", { name: "Required skills" })).queryByText("WELDING")).toBeNull());
  });
});

describe("skill search", () => {
  it("checks terms like the service", () => {
    const ok = validateSearch({ skills: "welding:3, plc", minRating: "2", certs: "osha10", asOf: "2026-06-01" });
    expect(ok.query).toEqual({ skills: "WELDING:3,PLC", minRating: "2", certs: "OSHA10", asOf: "2026-06-01" });
    expect(validateSearch({ skills: "WELDING:11", minRating: "", certs: "", asOf: "2026-06-01" }).errors.skills).toContain("1 to 10");
    expect(validateSearch({ skills: "", minRating: "", certs: "", asOf: "2026-06-01" }).errors.skills).toBe("Enter at least one skill or certificate.");
    expect(validateSearch({ skills: "A", minRating: "0", certs: "bad code", asOf: "" }).errors).toEqual({
      minRating: "Enter a rating from 1 to 10, or leave it empty for 1.",
      certs: "\"BAD CODE\" isn't a certificate code.",
      asOf: "Enter a date.",
    });
  });

  it("asks for a search first, then searches from the URL and lists matches", async () => {
    handler = (url) =>
      url.pathname === "/api/v1/hr/skills/search"
        ? json(200, {
            asOf: "2026-06-01",
            truncated: true,
            items: [{ workerId: ADA, personnelNumber: "WRK-1001", legalName: "Ada Lovelace", skills: [{ skill: "WELDING", required: 3, actual: 4, delta: 1 }] }],
          })
        : undefined;
    const { container } = renderAt("/hr/skills/search");
    expect(screen.getByText("Search for a skill profile")).toBeInTheDocument();
    expect(gets("/api/v1/hr/skills/search")).toHaveLength(0);

    fireEvent.change(screen.getByLabelText("Skills"), { target: { value: "welding:3" } });
    fireEvent.change(screen.getByLabelText(/^On/), { target: { value: "2026-06-01" } });
    fireEvent.click(screen.getByRole("button", { name: "Search" }));
    const table = await findTable({ name: "Matching workers" });
    expect(within(table).getByRole("link", { name: "WRK-1001" })).toHaveAttribute("href", `/hr/workers/${ADA}`);
    expect(within(table).getByText("(needs 3)")).toBeInTheDocument();
    expect(screen.getByText("Showing the first 500 matches")).toBeInTheDocument();
    expect(screen.getByTestId("where")).toHaveTextContent("skills=WELDING%3A3");
    const sent = gets("/api/v1/hr/skills/search")[0].url.searchParams;
    expect(sent.get("skills")).toBe("WELDING:3");
    expect(sent.get("asOf")).toBe("2026-06-01");
    expect(pageViolations(container)).toEqual([]);
  });

  it("shows the empty result and the service's refusal", async () => {
    handler = (url) => (url.pathname === "/api/v1/hr/skills/search" ? json(200, { asOf: "2026-06-01", truncated: false, items: [] }) : undefined);
    const { unmount } = renderAt("/hr/skills/search?skills=WELDING&asOf=2026-06-01");
    expect(await screen.findByText("Nobody matches")).toBeInTheDocument();
    unmount();

    handler = (url) => (url.pathname === "/api/v1/hr/skills/search" ? json(403, { code: "AUTH-0003", detail: "missing duty hr.org.inquire" }) : undefined);
    renderAt("/hr/skills/search?skills=WELDING&asOf=2026-06-01");
    await waitFor(() => expect(alertTexts().join(" ")).toContain("missing duty hr.org.inquire (AUTH-0003)"));
  });
});

describe("expiring certifications", () => {
  const row = (id: string, workerId: string, number: string, daysLeft: number, expiresOn: string): ExpiringCertification => ({
    ...forklift,
    id,
    workerId,
    personnelNumber: number,
    legalName: number === "WRK-1001" ? "Ada Lovelace" : "Bob Byte",
    expiresOn,
    daysLeft,
  });

  it("lists soonest first with the days left, changes the window and pages", async () => {
    handler = (url) => {
      if (url.pathname !== "/api/v1/hr/certifications/expiring") return undefined;
      if (url.searchParams.get("cursor")) return json(200, { asOf: "2026-06-01", withinDays: 60, items: [row("c3", BOB, "WRK-1002", 50, "2026-07-21")] });
      return json(200, { asOf: "2026-06-01", withinDays: 60, items: [row("c1", ADA, "WRK-1001", 5, "2026-06-06"), row("c2", BOB, "WRK-1002", 20, "2026-06-21")], nextCursor: "2026-06-21/c2" });
    };
    const { container } = renderAt("/hr/certifications/expiring?asOf=2026-06-01");
    const table = await findTable({ name: "Expiring certifications" });
    expect(within(table).getByText("2026-06-06")).toBeInTheDocument();
    expect(within(table).getByText("5")).toBeInTheDocument();
    expect(gets("/api/v1/hr/certifications/expiring")[0].url.searchParams.get("withinDays")).toBe("60");
    expect(pageViolations(container)).toEqual([]);

    fireEvent.click(screen.getByRole("button", { name: "Load more" }));
    expect(await screen.findByText("2026-07-21")).toBeInTheDocument();
    expect(gets("/api/v1/hr/certifications/expiring")[1].url.searchParams.get("cursor")).toBe("2026-06-21/c2");

    fireEvent.change(screen.getByLabelText("Expiring within"), { target: { value: "7" } });
    await waitFor(() => expect(gets("/api/v1/hr/certifications/expiring").some((c) => c.url.searchParams.get("withinDays") === "7")).toBe(true));
    expect(screen.getByTestId("where")).toHaveTextContent("withinDays=7");
  });

  it("shows the empty state and an error", async () => {
    handler = (url) => (url.pathname === "/api/v1/hr/certifications/expiring" ? json(200, { asOf: "2026-06-01", withinDays: 60, items: [] }) : undefined);
    const { unmount } = renderAt("/hr/certifications/expiring");
    expect(await screen.findByText("Nothing expires in this window")).toBeInTheDocument();
    unmount();

    handler = (url) => (url.pathname === "/api/v1/hr/certifications/expiring" ? json(503, { code: "COM-0500", detail: "HR is down" }) : undefined);
    renderAt("/hr/certifications/expiring");
    await waitFor(() => expect(alertTexts().join(" ")).toContain("HR is down (COM-0500)"));
  });
});
