import { cleanup, fireEvent, render, screen, waitFor } from "@testing-library/react";
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

describe("Edit worker", () => {
  it("shows the current values and saves a change with the worker version as If-Match", async () => {
    serve(worker(), (url, init) => (init.method === "PUT" && url.pathname.endsWith(`/workers/${ANA}`) ? json(200, worker({ version: 4, name: "Ana Lee" })) : undefined));
    const { container } = renderWorker();
    const name = (await screen.findByLabelText("Name")) as HTMLInputElement;
    expect(name.value).toBe("Ana Silva");
    expect((screen.getByLabelText("Badge") as HTMLInputElement).value).toBe("B-1");
    expect(pageViolations(container)).toEqual([]);
    fill("Name", " Ana Lee ");
    fill("Badge", "");
    fill("Cost rate per hour", "22.5");
    fireEvent.click(screen.getByRole("button", { name: "Save worker" }));
    expect(await screen.findByText("Worker saved")).toBeInTheDocument();
    const [put] = sent("PUT", `/workers/${ANA}`);
    expect(put.body).toEqual({ name: "Ana Lee", badgeId: null, userId: "ana@plant", costRate: "22.5", currency: "USD" });
    expect(header(put, "If-Match")).toBe('"3"');
    expect(header(put, "Idempotency-Key")).toMatch(/^[0-9a-f-]{36}$/);
  });

  it("refuses bad input before calling the service", async () => {
    serve(worker());
    renderWorker();
    await screen.findByLabelText("Name");
    fill("Name", "");
    fill("Cost rate per hour", "abc");
    fireEvent.click(screen.getByRole("button", { name: "Save worker" }));
    expect(await screen.findByText("Enter a name of up to 120 characters.")).toBeInTheDocument();
    expect(screen.getByText("Enter an hourly rate such as 21.50.")).toBeInTheDocument();
    expect(sent("PUT", `/workers/${ANA}`)).toHaveLength(0);
  });

  it("names a badge held by another worker on the badge field", async () => {
    serve(worker(), (_url, init) => (init.method === "PUT" ? json(409, { code: "SF-0105", detail: "badge is already assigned to another worker" }) : undefined));
    renderWorker();
    await screen.findByLabelText("Name");
    fill("Badge", "B-2");
    fireEvent.click(screen.getByRole("button", { name: "Save worker" }));
    expect(await screen.findByText("This badge is already assigned to another worker.")).toBeInTheDocument();
  });

  it("explains a stale version", async () => {
    serve(worker(), (_url, init) => (init.method === "PUT" ? json(412, { code: "SF-0143", detail: "worker was changed by someone else" }) : undefined));
    renderWorker();
    await screen.findByLabelText("Name");
    fill("Name", "Ana Lee");
    fireEvent.click(screen.getByRole("button", { name: "Save worker" }));
    expect(await screen.findByText(/changed by someone else. Reload the page and try again. \(SF-0143\)/)).toBeInTheDocument();
  });

  it("explains a currency change after approved pay and an edit of one's own record", async () => {
    let code = "SF-0169";
    serve(worker(), (_url, init) =>
      init.method === "PUT" ? json(code === "SF-0169" ? 422 : 403, { code, detail: "refused" }) : undefined,
    );
    renderWorker();
    await screen.findByLabelText("Name");
    fill("Currency", "eur");
    fireEvent.click(screen.getByRole("button", { name: "Save worker" }));
    expect(await screen.findByText(/currency can't change because pay of this worker is already approved. \(SF-0169\)/)).toBeInTheDocument();
    expect(sent("PUT", `/workers/${ANA}`)[0].body).toMatchObject({ currency: "EUR" });
    code = "SF-0106";
    fireEvent.click(screen.getByRole("button", { name: "Save worker" }));
    expect(await screen.findByText(/can't edit your own worker record. Ask another supervisor. \(SF-0106\)/)).toBeInTheDocument();
  });
});

describe("Deactivate and reactivate", () => {
  it("needs a reason, then deactivates with the version", async () => {
    serve(worker(), (url, init) =>
      init.method === "POST" && url.pathname.endsWith(":deactivate") ? json(200, worker({ version: 4, active: false, deactivationReason: "left" })) : undefined,
    );
    renderWorker();
    await screen.findByLabelText("Name");
    fireEvent.click(screen.getByRole("button", { name: "Deactivate worker" }));
    expect(await screen.findByText(/Enter a reason of up to 500 characters/)).toBeInTheDocument();
    expect(sent("POST", ":deactivate")).toHaveLength(0);
    fill("Reason", " left the company ");
    fireEvent.click(screen.getByRole("button", { name: "Deactivate worker" }));
    expect(await screen.findByText("Worker deactivated")).toBeInTheDocument();
    const [call] = sent("POST", `/workers/${ANA}:deactivate`);
    expect(call.body).toEqual({ reason: "left the company" });
    expect(header(call, "If-Match")).toBe('"3"');
  });

  it("explains that a clocked-in worker cannot be deactivated", async () => {
    serve(worker(), (_url, init) => (init.method === "POST" ? json(409, { code: "SF-0166", detail: "worker is clocked in" }) : undefined));
    renderWorker();
    await screen.findByLabelText("Name");
    fill("Reason", "left");
    fireEvent.click(screen.getByRole("button", { name: "Deactivate worker" }));
    expect(await screen.findByText(/clocked in or has an open job. Close the open time/)).toBeInTheDocument();
  });

  it("shows who deactivated an inactive worker and reactivates", async () => {
    const gone = worker({ active: false, deactivatedAt: "2026-10-31T17:00:00Z", deactivatedBy: "sup-1", deactivationReason: "Left the company" });
    serve(gone, (url, init) => (init.method === "POST" && url.pathname.endsWith(":reactivate") ? json(200, worker({ version: 4 })) : undefined));
    const { container } = renderWorker();
    expect(await screen.findByText("Left the company")).toBeInTheDocument();
    expect(screen.getByText("sup-1")).toBeInTheDocument();
    expect(screen.queryByRole("button", { name: "Deactivate worker" })).toBeNull();
    expect(pageViolations(container)).toEqual([]);
    fireEvent.click(screen.getByRole("button", { name: "Reactivate worker" }));
    expect(await screen.findByText("Worker reactivated")).toBeInTheDocument();
    await waitFor(() => expect(sent("POST", `/workers/${ANA}:reactivate`)).toHaveLength(1));
    const [call] = sent("POST", ":reactivate");
    expect(call.body).toBeUndefined();
    expect(header(call, "If-Match")).toBe('"3"');
  });

  it("reports a failed reactivation", async () => {
    serve(worker({ active: false, deactivationReason: "x" }), (_url, init) => (init.method === "POST" ? json(409, { code: "SF-0168", detail: "worker is already active" }) : undefined));
    renderWorker();
    fireEvent.click(await screen.findByRole("button", { name: "Reactivate worker" }));
    expect(await screen.findByText(/already active. Reload the page. \(SF-0168\)/)).toBeInTheDocument();
  });
});
