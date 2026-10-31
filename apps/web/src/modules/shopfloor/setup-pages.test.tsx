import { findTable } from "../../test-utils";
import { fireEvent, render, screen, waitFor } from "@testing-library/react";
import { MemoryRouter, Route, Routes } from "react-router-dom";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { checkAxeAA } from "../../shell/a11y";
import type { PayAgreement, TimeProfile, Worker } from "./api/client";
import shopfloor from "./module";
import { validateAgreement } from "./NewPayAgreementPage";
import { validateProfile, type ProfileValues } from "./NewTimeProfilePage";
import { describeDay } from "./TimeProfilesPage";

const ANA = "0195f0a0-0000-7000-8000-000000000001";
const PARTY = "0195f0a0-0000-7000-8000-000000000002";
const PROFILE = "0195f0a0-0000-7000-8000-000000000031";
const AGREEMENT = "0195f0a0-0000-7000-8000-000000000032";

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

const profile: TimeProfile = {
  id: PROFILE,
  code: "DAY",
  name: "Day shift",
  active: true,
  version: 1,
  days: [
    { dayType: "weekday", shiftStart: "07:00", shiftEnd: "15:30", breaks: [{ from: "12:00", to: "12:30" }], roundingMinutes: 5, toleranceBeforeMin: 10, toleranceAfterMin: 5, flexAllowed: false },
    { dayType: "sunday", shiftStart: null, shiftEnd: null, breaks: [], roundingMinutes: 1, toleranceBeforeMin: 10, toleranceAfterMin: 5, flexAllowed: false },
  ],
  createdBy: "admin",
  createdAt: "2026-10-20T08:00:00Z",
  updatedAt: "2026-10-20T08:00:00Z",
};

const agreement: PayAgreement = {
  id: AGREEMENT,
  code: "US",
  name: "US hourly",
  dailyThreshold: "8.00",
  weeklyThreshold: "40.00",
  overtimeMultiplier: "1.5000",
  sundayMultiplier: "2.0000",
  weekStart: 1,
  active: true,
  version: 1,
  createdBy: "admin",
  createdAt: "2026-10-20T08:00:00Z",
  updatedAt: "2026-10-20T08:00:00Z",
};

const worker = (extra: Partial<Worker> = {}): Worker => ({
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
  ...extra,
});

describe("Pay agreements list", () => {
  it("lists agreements with thresholds, multipliers and the pay week start", async () => {
    handler = (url) => (url.pathname.endsWith("/pay-agreements") ? json(200, { items: [agreement] }) : undefined);
    const { container } = renderAt("/shopfloor/pay-agreements");
    expect(screen.getByRole("heading", { name: "Pay agreements" })).toBeInTheDocument();
    const table = await findTable({ name: "Pay agreements" });
    await waitFor(() => expect(table).toHaveTextContent("US hourly"));
    expect(table).toHaveTextContent("8.00 h");
    expect(table).toHaveTextContent("1.5000x");
    expect(table).toHaveTextContent("Monday");
    expect(table).toHaveTextContent("Active");
    expect(screen.getByRole("link", { name: "New pay agreement" })).toHaveAttribute("href", "/shopfloor/pay-agreements/new");
    expect(pageViolations(container)).toEqual([]);
  });

  it("shows the empty state", async () => {
    handler = () => json(200, { items: [] });
    renderAt("/shopfloor/pay-agreements");
    expect(await screen.findByText("No pay agreements yet")).toBeInTheDocument();
  });

  it("shows the error with its code and retries", async () => {
    let fail = true;
    handler = () => (fail ? json(503, { code: "COM-0503", detail: "Service unavailable" }) : json(200, { items: [agreement] }));
    renderAt("/shopfloor/pay-agreements");
    expect(await screen.findByText(/Service unavailable \(COM-0503\)/)).toBeInTheDocument();
    fail = false;
    fireEvent.click(screen.getByRole("button", { name: "Try again" }));
    expect(await screen.findByText("US hourly")).toBeInTheDocument();
  });
});

describe("New pay agreement", () => {
  const base = { code: "US", name: "US hourly", dailyThreshold: "8", weeklyThreshold: "40", overtimeMultiplier: "1.5", sundayMultiplier: "2", weekStart: "1" };

  it("validates like the service", () => {
    expect(validateAgreement(base).input).toEqual({ code: "US", name: "US hourly", dailyThreshold: "8", weeklyThreshold: "40", overtimeMultiplier: "1.5", sundayMultiplier: "2", weekStart: 1 });
    const { errors } = validateAgreement({ ...base, code: "", dailyThreshold: "25", weeklyThreshold: "169", overtimeMultiplier: "0.9", sundayMultiplier: "2.12345", weekStart: "9" });
    expect(Object.keys(errors).sort()).toEqual(["code", "dailyThreshold", "overtimeMultiplier", "sundayMultiplier", "weekStart", "weeklyThreshold"]);
  });

  it("creates the agreement with an Idempotency-Key and returns to the list", async () => {
    handler = (url, init) => {
      if (init.method === "POST" && url.pathname.endsWith("/pay-agreements")) return json(201, agreement);
      if (url.pathname.endsWith("/pay-agreements")) return json(200, { items: [agreement] });
      return undefined;
    };
    const { container } = renderAt("/shopfloor/pay-agreements/new");
    expect(pageViolations(container)).toEqual([]);
    fill("Code", "US");
    fill("Name", "US hourly");
    fireEvent.click(screen.getByRole("button", { name: "Create pay agreement" }));
    expect(await screen.findByRole("heading", { name: "Pay agreements" })).toBeInTheDocument();
    const [create] = posts("/pay-agreements");
    expect(create.body).toEqual({ code: "US", name: "US hourly", dailyThreshold: "8", weeklyThreshold: "40", overtimeMultiplier: "1.5", sundayMultiplier: "2", weekStart: 1 });
    expect(header(create, "Idempotency-Key")).toMatch(/^[0-9a-f-]{36}$/);
  });

  it("does not send an invalid form and names the fields", () => {
    renderAt("/shopfloor/pay-agreements/new");
    fireEvent.click(screen.getByRole("button", { name: "Create pay agreement" }));
    expect(screen.getByText("Enter a code of up to 40 characters.")).toBeInTheDocument();
    expect(posts("/pay-agreements")).toHaveLength(0);
  });

  it("shows a code that is already used on the code field", async () => {
    handler = () => json(409, { code: "SF-0142", detail: "code is already used" });
    renderAt("/shopfloor/pay-agreements/new");
    fill("Code", "US");
    fill("Name", "US hourly");
    fireEvent.click(screen.getByRole("button", { name: "Create pay agreement" }));
    expect(await screen.findByText("This code is already used.")).toBeInTheDocument();
  });
});

describe("Time profiles list", () => {
  it("lists profiles with a summary of each day type", async () => {
    handler = (url) => (url.pathname.endsWith("/time-profiles") ? json(200, { items: [profile] }) : undefined);
    const { container } = renderAt("/shopfloor/time-profiles");
    const table = await findTable({ name: "Time profiles" });
    await waitFor(() => expect(table).toHaveTextContent("Day shift"));
    expect(table).toHaveTextContent("Weekday 07:00-15:30, 1 break");
    expect(table).toHaveTextContent("Sunday no shift window, 0 breaks");
    expect(screen.getByRole("link", { name: "New time profile" })).toHaveAttribute("href", "/shopfloor/time-profiles/new");
    expect(pageViolations(container)).toEqual([]);
    expect(describeDay(profile.days[0]!)).toBe("Weekday 07:00-15:30, 1 break");
  });

  it("shows the empty state and the error", async () => {
    handler = () => json(200, { items: [] });
    const first = renderAt("/shopfloor/time-profiles");
    expect(await screen.findByText("No time profiles yet")).toBeInTheDocument();
    first.unmount();
    handler = () => json(503, { code: "COM-0503", detail: "Service unavailable" });
    renderAt("/shopfloor/time-profiles");
    expect(await screen.findByText(/Service unavailable \(COM-0503\)/)).toBeInTheDocument();
  });
});

describe("New time profile", () => {
  const day = { defined: true, shiftStart: "07:00", shiftEnd: "15:30", breaks: "12:00-12:30", roundingMinutes: "5", toleranceBeforeMin: "10", toleranceAfterMin: "5", flexAllowed: false };
  const off = { ...day, defined: false };
  const values = (weekday = day): ProfileValues => ({ code: "DAY", name: "Day shift", days: { weekday, saturday: off, sunday: off } });

  it("builds the request from the defined day types and sorts the breaks", () => {
    const input = validateProfile(values({ ...day, breaks: "15:00-15:15\n12:00-12:30" })).input;
    expect(input?.days).toHaveLength(1);
    expect(input?.days[0]).toMatchObject({ dayType: "weekday", shiftStart: "07:00", shiftEnd: "15:30", breaks: [{ from: "12:00", to: "12:30" }, { from: "15:00", to: "15:15" }], roundingMinutes: 5 });
  });

  it("names the day and field of each problem", () => {
    const bad = { ...day, shiftEnd: "06:00", breaks: "13:00-12:00", roundingMinutes: "16", toleranceBeforeMin: "61", toleranceAfterMin: "x" };
    const { errors } = validateProfile({ ...values(bad), code: "" });
    expect(Object.keys(errors).sort()).toEqual(["code", "weekday.breaks", "weekday.roundingMinutes", "weekday.shiftEnd", "weekday.toleranceAfterMin", "weekday.toleranceBeforeMin"]);
    expect(validateProfile(values({ ...day, shiftEnd: "" })).errors["weekday.shiftStart"]).toMatch(/both/);
    expect(validateProfile(values({ ...day, breaks: "12:00-13:00\n12:30-13:30" })).errors["weekday.breaks"]).toBe("Breaks must not overlap.");
  });

  it("creates the profile with an Idempotency-Key and returns to the list", async () => {
    handler = (url, init) => {
      if (init.method === "POST" && url.pathname.endsWith("/time-profiles")) return json(201, profile);
      if (url.pathname.endsWith("/time-profiles")) return json(200, { items: [profile] });
      return undefined;
    };
    const { container } = renderAt("/shopfloor/time-profiles/new");
    expect(pageViolations(container)).toEqual([]);
    fill("Code", "DAY");
    fill("Name", "Day shift");
    fill("Weekday shift start", "07:00");
    fill("Weekday shift end", "15:30");
    fill("Weekday unpaid breaks", "12:00-12:30");
    fireEvent.click(screen.getByRole("button", { name: "Create time profile" }));
    expect(await screen.findByRole("heading", { name: "Time profiles" })).toBeInTheDocument();
    const [create] = posts("/time-profiles");
    expect(create.body).toMatchObject({ code: "DAY", name: "Day shift", days: [{ dayType: "weekday", shiftStart: "07:00", breaks: [{ from: "12:00", to: "12:30" }] }] });
    expect(header(create, "Idempotency-Key")).toMatch(/^[0-9a-f-]{36}$/);
  });

  it("shows a service problem on the day field it names", async () => {
    handler = () => json(400, { code: "COM-0001", detail: "request failed validation", errors: { "days[0].breaks": "breaks must not overlap" } });
    renderAt("/shopfloor/time-profiles/new");
    fill("Code", "DAY");
    fill("Name", "Day shift");
    fireEvent.click(screen.getByRole("button", { name: "Create time profile" }));
    expect(await screen.findByText("breaks must not overlap")).toBeInTheDocument();
  });

  it("reveals the fields of a day type when it is defined", () => {
    renderAt("/shopfloor/time-profiles/new");
    expect(screen.queryByLabelText("Saturday shift start")).not.toBeInTheDocument();
    fireEvent.click(screen.getByLabelText("Define saturday rules"));
    expect(screen.getByLabelText("Saturday shift start")).toBeInTheDocument();
  });
});

describe("Worker pay setup", () => {
  function serve(w: Worker, extra?: Handler) {
    handler = (url, init) => {
      const custom = extra?.(url, init);
      if (custom) return custom;
      if (url.pathname.endsWith(`/workers/${ANA}`)) return json(200, w);
      if (url.pathname.endsWith("/time-profiles")) return json(200, { items: [profile] });
      if (url.pathname.endsWith("/pay-agreements")) return json(200, { items: [agreement] });
      if (url.pathname.endsWith("/registrations") || url.pathname.endsWith("/worker-days")) return json(200, { items: [] });
      return undefined;
    };
  }

  it("lists only active choices and saves the link with the worker version as If-Match", async () => {
    serve(worker(), (url, init) =>
      init.method === "POST" && url.pathname.endsWith(":assign-setup") ? json(200, worker({ version: 4, profileId: PROFILE, payAgreementId: AGREEMENT })) : undefined,
    );
    const { container } = renderAt(`/shopfloor/workers/${ANA}`);
    const profileSelect = await screen.findByLabelText("Time profile");
    expect(calls.some((c) => c.url.pathname.endsWith("/time-profiles") && c.url.searchParams.get("active") === "true")).toBe(true);
    expect(screen.getByRole("button", { name: "Save pay setup" })).toBeDisabled();
    await screen.findByRole("option", { name: "DAY - Day shift" });
    fireEvent.change(profileSelect, { target: { value: PROFILE } });
    fireEvent.change(screen.getByLabelText("Pay agreement"), { target: { value: AGREEMENT } });
    expect(pageViolations(container)).toEqual([]);
    fireEvent.click(screen.getByRole("button", { name: "Save pay setup" }));
    expect(await screen.findByText("Pay setup saved")).toBeInTheDocument();
    const [assign] = posts(`/workers/${ANA}:assign-setup`);
    expect(assign.body).toEqual({ profileId: PROFILE, payAgreementId: AGREEMENT });
    expect(header(assign, "If-Match")).toBe('"3"');
    expect(header(assign, "Idempotency-Key")).toMatch(/^[0-9a-f-]{36}$/);
  });

  it("clears a link by choosing the defaults", async () => {
    serve(worker({ profileId: PROFILE }), (url, init) => (init.method === "POST" && url.pathname.endsWith(":assign-setup") ? json(200, worker({ version: 4 })) : undefined));
    renderAt(`/shopfloor/workers/${ANA}`);
    const select = await screen.findByLabelText("Time profile");
    await screen.findByRole("option", { name: "DAY - Day shift" });
    fireEvent.change(select, { target: { value: "" } });
    fireEvent.click(screen.getByRole("button", { name: "Save pay setup" }));
    await screen.findByText("Pay setup saved");
    expect(posts(`/workers/${ANA}:assign-setup`)[0]?.body).toEqual({ profileId: null, payAgreementId: null });
  });

  it("explains a stale version and a refused link", async () => {
    serve(worker(), (url, init) =>
      init.method === "POST" && url.pathname.endsWith(":assign-setup") ? json(412, { code: "SF-0143", detail: "worker was changed by someone else" }) : undefined,
    );
    renderAt(`/shopfloor/workers/${ANA}`);
    const select = await screen.findByLabelText("Time profile");
    await screen.findByRole("option", { name: "DAY - Day shift" });
    fireEvent.change(select, { target: { value: PROFILE } });
    fireEvent.click(screen.getByRole("button", { name: "Save pay setup" }));
    expect(await screen.findByText(/changed by someone else. Reload the page and try again. \(SF-0143\)/)).toBeInTheDocument();
  });

  it("shows an error loading the choices and retries", async () => {
    let fail = true;
    serve(worker(), (url) => (fail && url.pathname.endsWith("/time-profiles") ? json(503, { code: "COM-0503", detail: "Service unavailable" }) : undefined));
    renderAt(`/shopfloor/workers/${ANA}`);
    expect(await screen.findByText(/Could not load profiles and agreements/)).toBeInTheDocument();
    fail = false;
    fireEvent.click(screen.getAllByRole("button", { name: "Try again" })[0]!);
    expect(await screen.findByLabelText("Time profile")).toBeInTheDocument();
  });
});
