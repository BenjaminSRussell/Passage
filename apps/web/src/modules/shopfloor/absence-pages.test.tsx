import { findTable } from "../../test-utils";
import { fireEvent, render, screen, waitFor } from "@testing-library/react";
import { MemoryRouter, Route, Routes } from "react-router-dom";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { checkAxeAA } from "../../shell/a11y";
import type { AbsenceCode, Worker } from "./api/client";
import shopfloor from "./module";
import { validateAbsenceCode } from "./NewAbsenceCodePage";
import { validateAbsence } from "./RecordAbsencePage";

const ANA = "0195f0a0-0000-7000-8000-000000000001";
const PARTY = "0195f0a0-0000-7000-8000-000000000002";
const SICK = "0195f0a0-0000-7000-8000-000000000041";
const REG = "0195f0a0-0000-7000-8000-000000000042";

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

function renderAt(path: string) {
  return render(
    <MemoryRouter initialEntries={[path]}>
      <Routes>
        {shopfloor.routes.map((route) => (
          <Route key={route.path} path={route.path} element={route.element} />
        ))}
      </Routes>
    </MemoryRouter>,
  );
}

const header = (call: { init: RequestInit }, name: string) => new Headers(call.init.headers).get(name);
const posts = (suffix: string) => calls.filter((c) => c.method === "POST" && c.url.pathname.endsWith(suffix));
const fill = (label: string, value: string) => fireEvent.change(screen.getByLabelText(label), { target: { value } });

const sick: AbsenceCode = {
  id: SICK,
  code: "SICK",
  name: "Sick leave",
  paid: true,
  payMultiplier: "0.8000",
  active: true,
  version: 1,
  createdBy: "admin",
  createdAt: "2026-10-20T08:00:00Z",
  updatedAt: "2026-10-20T08:00:00Z",
};
const unpaid: AbsenceCode = { ...sick, id: "0195f0a0-0000-7000-8000-000000000043", code: "UNP", name: "Unpaid leave", paid: false, payMultiplier: "0" };

const ana: Worker = {
  id: ANA,
  partyId: PARTY,
  name: "Ana Silva",
  badgeId: "B-1",
  userId: null,
  costRate: "20.000000",
  currency: "USD",
  active: true,
  version: 3,
  createdAt: "2026-10-01T08:00:00Z",
  profileId: null,
  payAgreementId: null,
};

describe("Absence codes list", () => {
  it("lists codes with their pay terms", async () => {
    handler = (url) => (url.pathname.endsWith("/absence-codes") ? json(200, { items: [sick, unpaid] }) : undefined);
    const { container } = renderAt("/shopfloor/absence-codes");
    expect(screen.getByRole("heading", { name: "Absence codes" })).toBeInTheDocument();
    const table = await findTable({ name: "Absence codes" });
    await waitFor(() => expect(table).toHaveTextContent("Sick leave"));
    expect(table).toHaveTextContent("0.8000x");
    expect(table).toHaveTextContent("Unpaid leave");
    expect(table).toHaveTextContent("none");
    expect(screen.getByRole("link", { name: "New absence code" })).toHaveAttribute("href", "/shopfloor/absence-codes/new");
    expect(screen.getByRole("link", { name: "Record absence" })).toHaveAttribute("href", "/shopfloor/absences/new");
    expect(pageViolations(container)).toEqual([]);
  });

  it("shows the empty state", async () => {
    handler = () => json(200, { items: [] });
    renderAt("/shopfloor/absence-codes");
    expect(await screen.findByText("No absence codes yet")).toBeInTheDocument();
  });

  it("shows the error with its code and retries", async () => {
    let fail = true;
    handler = () => (fail ? json(503, { code: "COM-0503", detail: "Service unavailable" }) : json(200, { items: [sick] }));
    renderAt("/shopfloor/absence-codes");
    expect(await screen.findByText(/Service unavailable \(COM-0503\)/)).toBeInTheDocument();
    fail = false;
    fireEvent.click(screen.getByRole("button", { name: "Try again" }));
    expect(await screen.findByText("Sick leave")).toBeInTheDocument();
  });
});

describe("New absence code", () => {
  const base = { code: "SICK", name: "Sick leave", pay: "paid", payMultiplier: "0.8" };

  it("validates like the service", () => {
    expect(validateAbsenceCode(base).input).toEqual({ code: "SICK", name: "Sick leave", paid: true, payMultiplier: "0.8" });
    expect(validateAbsenceCode({ ...base, pay: "unpaid", payMultiplier: "junk" }).input).toEqual({ code: "SICK", name: "Sick leave", paid: false });
    const { errors } = validateAbsenceCode({ code: "", name: "", pay: "paid", payMultiplier: "5.5" });
    expect(Object.keys(errors).sort()).toEqual(["code", "name", "payMultiplier"]);
    expect(validateAbsenceCode({ ...base, payMultiplier: "0" }).errors.payMultiplier).toBeDefined();
    expect(validateAbsenceCode({ ...base, payMultiplier: "1.00001" }).errors.payMultiplier).toBeDefined();
  });

  it("creates the code with an Idempotency-Key and returns to the list", async () => {
    handler = (url, init) => {
      if (init.method === "POST" && url.pathname.endsWith("/absence-codes")) return json(201, sick);
      if (url.pathname.endsWith("/absence-codes")) return json(200, { items: [sick] });
      return undefined;
    };
    const { container } = renderAt("/shopfloor/absence-codes/new");
    expect(pageViolations(container)).toEqual([]);
    fill("Code", "SICK");
    fill("Name", "Sick leave");
    fill("Share of the hourly rate", "0.8");
    fireEvent.click(screen.getByRole("button", { name: "Create absence code" }));
    expect(await screen.findByRole("heading", { name: "Absence codes" })).toBeInTheDocument();
    const [create] = posts("/absence-codes");
    expect(create.body).toEqual({ code: "SICK", name: "Sick leave", paid: true, payMultiplier: "0.8" });
    expect(header(create, "Idempotency-Key")).toMatch(/^[0-9a-f-]{36}$/);
  });

  it("hides the rate share for an unpaid code", () => {
    renderAt("/shopfloor/absence-codes/new");
    expect(screen.getByLabelText("Share of the hourly rate")).toBeInTheDocument();
    fill("Pay", "unpaid");
    expect(screen.queryByLabelText("Share of the hourly rate")).not.toBeInTheDocument();
  });

  it("does not send an invalid form and names the fields", () => {
    renderAt("/shopfloor/absence-codes/new");
    fireEvent.click(screen.getByRole("button", { name: "Create absence code" }));
    expect(screen.getByText("Enter a code of up to 40 characters.")).toBeInTheDocument();
    expect(posts("/absence-codes")).toHaveLength(0);
  });

  it("shows a code that is already used on the code field", async () => {
    handler = () => json(409, { code: "SF-0142", detail: "code is already used" });
    renderAt("/shopfloor/absence-codes/new");
    fill("Code", "SICK");
    fill("Name", "Sick leave");
    fireEvent.click(screen.getByRole("button", { name: "Create absence code" }));
    expect(await screen.findByText("This code is already used.")).toBeInTheDocument();
  });
});

describe("Record absence", () => {
  const values = { workerId: ANA, absenceCodeId: SICK, date: "2026-10-20", start: "13:30", hours: "4.5", note: "" };

  it("builds the absence registration from the day and hours", () => {
    const { input, errors } = validateAbsence(values, REG);
    expect(errors).toEqual({});
    expect(input).toMatchObject({ id: REG, workerId: ANA, kind: "absence", absenceCodeId: SICK, note: null });
    expect(input!.startedAt).toBe(new Date("2026-10-20T13:30:00").toISOString());
    expect(new Date(input!.stoppedAt!).getTime() - new Date(input!.startedAt).getTime()).toBe(4.5 * 3600 * 1000);
    expect(Object.keys(validateAbsence({ ...values, workerId: "", absenceCodeId: "", hours: "17", date: "x", start: "25:00" }, REG).errors).sort()).toEqual(["absenceCodeId", "date", "hours", "start", "workerId"]);
  });

  function serve(extra?: Handler) {
    handler = (url, init) => {
      const custom = extra?.(url, init);
      if (custom) return custom;
      if (url.pathname.endsWith("/workers")) return json(200, { items: [ana] });
      if (url.pathname.endsWith("/absence-codes")) return json(200, { items: [sick] });
      return undefined;
    };
  }

  it("records an absence and returns to the absence codes", async () => {
    serve((url, init) => (init.method === "POST" && url.pathname.endsWith("/registrations") ? json(200, { items: [{ id: REG, status: "accepted", flags: [], autoStopped: [], warnings: [] }] }) : undefined));
    const { container } = renderAt("/shopfloor/absences/new");
    await screen.findByRole("option", { name: "Ana Silva" });
    expect(pageViolations(container)).toEqual([]);
    fill("Worker", ANA);
    fill("Absence code", SICK);
    fill("Day", "2026-10-20");
    fill("Starts at", "06:00");
    fill("Hours", "8");
    fireEvent.click(screen.getByRole("button", { name: "Record absence" }));
    expect(await screen.findByRole("heading", { name: "Absence codes" })).toBeInTheDocument();
    const [create] = posts("/registrations");
    expect(create.body).toMatchObject({ items: [{ workerId: ANA, kind: "absence", absenceCodeId: SICK, startedAt: new Date("2026-10-20T06:00:00").toISOString() }] });
  });

  it("shows an inactive code problem on the code field", async () => {
    serve((_url, init) => (init.method === "POST" ? json(422, { code: "SF-0146", detail: "absence code is inactive" }) : undefined));
    renderAt("/shopfloor/absences/new");
    await screen.findByRole("option", { name: "Ana Silva" });
    fill("Worker", ANA);
    fill("Absence code", SICK);
    fireEvent.click(screen.getByRole("button", { name: "Record absence" }));
    expect(await screen.findByText("This absence code is inactive.")).toBeInTheDocument();
  });

  it("does not send an invalid form", async () => {
    serve();
    renderAt("/shopfloor/absences/new");
    await screen.findByRole("option", { name: "Ana Silva" });
    fireEvent.click(screen.getByRole("button", { name: "Record absence" }));
    expect(screen.getByText("Choose a worker.")).toBeInTheDocument();
    expect(posts("/registrations")).toHaveLength(0);
  });

  it("points to the code form when no code exists", async () => {
    handler = (url) => (url.pathname.endsWith("/workers") ? json(200, { items: [ana] }) : json(200, { items: [] }));
    renderAt("/shopfloor/absences/new");
    expect(await screen.findByText("No active absence codes")).toBeInTheDocument();
    expect(screen.getByRole("link", { name: "New absence code" })).toHaveAttribute("href", "/shopfloor/absence-codes/new");
  });

  it("shows the load error and retries", async () => {
    let fail = true;
    handler = (url) => (fail ? json(503, { code: "COM-0503", detail: "Service unavailable" }) : url.pathname.endsWith("/workers") ? json(200, { items: [ana] }) : json(200, { items: [sick] }));
    renderAt("/shopfloor/absences/new");
    expect(await screen.findByText(/Service unavailable \(COM-0503\)/)).toBeInTheDocument();
    fail = false;
    fireEvent.click(screen.getByRole("button", { name: "Try again" }));
    expect(await screen.findByRole("option", { name: "Ana Silva" })).toBeInTheDocument();
  });
});
