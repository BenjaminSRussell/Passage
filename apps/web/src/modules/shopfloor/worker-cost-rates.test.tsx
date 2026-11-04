import { cleanup, fireEvent, render, screen } from "@testing-library/react";
import { MemoryRouter, Route, Routes } from "react-router-dom";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { checkAxeAA } from "../../shell/a11y";
import type { Worker } from "./api/client";
import shopfloor from "./module";

const ANA = "0195f0a0-0000-7000-8000-000000000001";
const PARTY = "0195f0a0-0000-7000-8000-000000000002";

type Reply = { status: number; body: unknown };
type Handler = (url: URL, init: RequestInit) => Reply | undefined;

const shellRules = new Set(["page-has-heading-one", "landmark-unique", "region", "bypass"]);
const pageViolations = (container: HTMLElement) => checkAxeAA(container).filter((v) => !shellRules.has(v.id));

let handler: Handler;
let calls: { method: string; url: URL; init: RequestInit; body: unknown }[];
const json = (status: number, body: unknown): Reply => ({ status, body });

const worker = (extra: Partial<Worker> = {}): Worker => ({
  id: ANA,
  partyId: PARTY,
  name: "Ana Silva",
  badgeId: "B-1",
  userId: "ana@plant",
  costRate: "20.000000",
  currency: "USD",
  active: true,
  version: 3,
  createdAt: "2026-10-01T08:00:00Z",
  profileId: null,
  payAgreementId: null,
  defaultResourceId: null,
  deactivatedAt: null,
  deactivatedBy: null,
  deactivationReason: null,
  ...extra,
});

function serve(w: Worker, extra?: Handler) {
  handler = (url, init) => {
    const custom = extra?.(url, init);
    if (custom) return custom;
    if (url.pathname.endsWith(`/workers/${ANA}`) && (init.method ?? "GET") === "GET") return json(200, w);
    if (["/time-profiles", "/pay-agreements", "/registrations", "/worker-days", "/machines", "/jobs"].some((p) => url.pathname.endsWith(p))) return json(200, { items: [] });
    return undefined;
  };
}

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
  cleanup();
  vi.unstubAllGlobals();
});

function renderWorker() {
  return render(
    <MemoryRouter initialEntries={[`/shopfloor/workers/${ANA}`]}>
      <Routes>
        {shopfloor.routes.map((route) => (
          <Route key={route.path} path={route.path} element={route.element} />
        ))}
      </Routes>
    </MemoryRouter>,
  );
}

const header = (call: { init: RequestInit }, name: string) => new Headers(call.init.headers).get(name);
const sent = (method: string, suffix: string) => calls.filter((c) => c.method === method && c.url.pathname.endsWith(suffix));
const fill = (label: string, value: string) => fireEvent.change(screen.getByLabelText(label), { target: { value } });

const RATE = (extra: Record<string, unknown> = {}) => ({
  id: "0195f0a0-0000-7000-8000-0000000000c1",
  workerId: ANA,
  effectiveFrom: "2026-07-01",
  costRate: "24.500000",
  note: "annual raise",
  createdBy: "sup@plant",
  createdAt: "2026-06-20T08:00:00Z",
  ...extra,
});

describe("Cost rate history", () => {
  it("lists the dated rates and shows an empty state without any", async () => {
    serve(worker(), (url) => (url.pathname.endsWith("/cost-rates") ? json(200, { items: [] }) : undefined));
    const empty = renderWorker();
    expect(await screen.findByText("No dated cost rates")).toBeInTheDocument();
    expect(screen.getByText(/Days before the first dated rate use the worker's own rate, 20.00 USD/)).toBeInTheDocument();
    empty.unmount();

    serve(worker(), (url) => (url.pathname.endsWith("/cost-rates") ? json(200, { items: [RATE()] }) : undefined));
    const { container } = renderWorker();
    expect(await screen.findByText("2026-07-01")).toBeInTheDocument();
    expect(screen.getByText("24.50")).toBeInTheDocument();
    expect(screen.getByText("annual raise")).toBeInTheDocument();
    expect(pageViolations(container)).toEqual([]);
  });

  it("adds a rate from a date with an Idempotency-Key and reloads the list", async () => {
    let added = false;
    serve(worker(), (url, init) => {
      if (!url.pathname.endsWith("/cost-rates")) return undefined;
      if (init.method === "POST") {
        added = true;
        return json(201, RATE());
      }
      return json(200, { items: added ? [RATE()] : [] });
    });
    renderWorker();
    await screen.findByText("No dated cost rates");
    fill("Rate starts on", "2026-07-01");
    fill("New rate per hour (USD)", " 24.5 ");
    fill("Note", " annual raise ");
    fireEvent.click(screen.getByRole("button", { name: "Add cost rate" }));
    expect(await screen.findByText("Cost rate added")).toBeInTheDocument();
    const [post] = sent("POST", "/cost-rates");
    expect(post.body).toEqual({ effectiveFrom: "2026-07-01", costRate: "24.5", note: "annual raise" });
    expect(header(post, "Idempotency-Key")).toMatch(/^[0-9a-f-]{36}$/);
    expect(await screen.findByText("annual raise")).toBeInTheDocument();
  });

  it("refuses bad input before calling the service", async () => {
    serve(worker(), (url) => (url.pathname.endsWith("/cost-rates") ? json(200, { items: [] }) : undefined));
    renderWorker();
    await screen.findByText("No dated cost rates");
    fill("Rate starts on", "");
    fill("New rate per hour (USD)", "-3");
    fireEvent.click(screen.getByRole("button", { name: "Add cost rate" }));
    expect(await screen.findByText("Enter the date the rate starts.")).toBeInTheDocument();
    expect(screen.getByText("Enter a rate of zero or more, with at most 6 decimals.")).toBeInTheDocument();
    expect(sent("POST", "/cost-rates")).toHaveLength(0);
  });

  it("explains a second rate for one date and the worker's own record", async () => {
    let code = "SF-0170";
    serve(worker(), (url, init) => {
      if (!url.pathname.endsWith("/cost-rates")) return undefined;
      return init.method === "POST" ? json(code === "SF-0170" ? 409 : 403, { code, detail: "refused" }) : json(200, { items: [] });
    });
    renderWorker();
    await screen.findByText("No dated cost rates");
    fill("New rate per hour (USD)", "30");
    fireEvent.click(screen.getByRole("button", { name: "Add cost rate" }));
    expect(await screen.findByText(/set the new rate from a later date. \(SF-0170\)/)).toBeInTheDocument();
    code = "SF-0106";
    fireEvent.click(screen.getByRole("button", { name: "Add cost rate" }));
    expect(await screen.findByText(/You can't set your own cost rate/)).toBeInTheDocument();
  });

  it("shows an error with a retry when the rates cannot load", async () => {
    let fail = true;
    serve(worker(), (url) => (url.pathname.endsWith("/cost-rates") ? (fail ? json(500, { code: "COM-0500", detail: "boom" }) : json(200, { items: [RATE()] })) : undefined));
    renderWorker();
    expect(await screen.findByText(/Could not load the cost rates/)).toBeInTheDocument();
    fail = false;
    fireEvent.click(screen.getByRole("button", { name: "Try again" }));
    expect(await screen.findByText("2026-07-01")).toBeInTheDocument();
  });
});
