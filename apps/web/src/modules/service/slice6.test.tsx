import { findAlert, findStatus, findTable } from "../../test-utils";
import { fireEvent, render, screen, waitFor } from "@testing-library/react";
import { MemoryRouter, Route, Routes } from "react-router-dom";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { checkAxeAA } from "../../shell/a11y";
import { validateVisit, visitsUsed, type VisitValues } from "./AgreementVisitsPanel";
import service from "./module";

const CUSTOMER = "11111111-0000-4000-8000-000000000001";
const LE = "22222222-0000-4000-8000-000000000002";
const PROFILE = "33333333-0000-4000-8000-000000000003";
const AGREEMENT = "44444444-0000-4000-8000-000000000004";
const WO = "66666666-0000-4000-8000-000000000006";

const shellRules = new Set(["page-has-heading-one", "landmark-unique", "region", "bypass"]);
const pageViolations = (container: HTMLElement) => checkAxeAA(container).filter((v) => !shellRules.has(v.id));

type Reply = { status: number; body: unknown; etag?: string };
type Handler = (url: URL, init: RequestInit) => Reply | Promise<Reply>;

let handler: Handler;
let calls: { method: string; url: URL; headers: Headers; body: unknown }[];

beforeEach(() => {
  calls = [];
  vi.stubGlobal(
    "fetch",
    vi.fn(async (input: string, init: RequestInit = {}) => {
      const url = new URL(input, "http://localhost");
      calls.push({ method: init.method ?? "GET", url, headers: new Headers(init.headers), body: init.body ? JSON.parse(init.body as string) : undefined });
      const { status, body, etag } = await handler(url, init);
      const headers: Record<string, string> = { "Content-Type": "application/json" };
      if (etag) headers.ETag = etag;
      return new Response(JSON.stringify(body), { status, headers });
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
        {service.routes.map((route) => (
          <Route key={route.path} path={route.path} element={route.element} />
        ))}
        <Route path="*" element={<p>Elsewhere</p>} />
      </Routes>
    </MemoryRouter>,
  );
}


const path = (suffix = "") => `/api/v1/service/agreements/${AGREEMENT}${suffix}`;

function agreement(overrides: Record<string, unknown> = {}) {
  return {
    id: AGREEMENT,
    number: "SA-000007",
    customerPartyId: CUSTOMER,
    legalEntityId: LE,
    validFrom: "2026-01-01",
    slaProfileId: PROFILE,
    billingModel: "PER_VISIT",
    priorityRank: 5,
    status: "ACTIVE",
    lines: [{ includedVisits: 2, includedHours: "1", partsCovered: false, laborCovered: true, travelCovered: false }],
    hoursConsumed: "0",
    visitsConsumed: 0,
    version: 2,
    ...overrides,
  };
}

const visit = (overrides: Record<string, unknown> = {}) => ({
  id: "aaaaaaaa-0000-4000-8000-00000000000a",
  agreementId: AGREEMENT,
  lineIndex: 0,
  kind: "PREVENTIVE",
  visitDate: "2026-03-01",
  workOrderId: null,
  note: "Annual service",
  recordedBy: "u1",
  createdAt: "2026-03-01T09:00:00Z",
  visitsLeft: 1,
  ...overrides,
});

/** Serves the agreement, its visits and its history; extra handles the writes. */
function serve(state: { agreement: Record<string, unknown>; visits: unknown[]; history: unknown[] }, writesTo?: Handler): Handler {
  return (url, init) => {
    if (init.method === "POST" && writesTo) return writesTo(url, init);
    if (url.pathname === path("/visits")) return { status: 200, body: { items: state.visits } };
    if (url.pathname === path("/history")) return { status: 200, body: { items: state.history } };
    if (url.pathname === path()) return { status: 200, body: state.agreement, etag: `"${state.agreement.version}"` };
    return { status: 404, body: { code: "not_found", detail: "unexpected" } };
  };
}

describe("agreement lifecycle", () => {
  it("activates a draft agreement at its ETag and shows the new status", async () => {
    const state = { agreement: agreement({ status: "DRAFT", version: 1 }), visits: [], history: [] };
    handler = serve(state, (url) => {
      expect(url.pathname).toBe(path(":transition"));
      return { status: 200, body: agreement({ status: "ACTIVE", version: 2 }), etag: '"2"' };
    });
    renderAt(`/service/agreements/${AGREEMENT}`);
    fireEvent.click(await screen.findByRole("button", { name: "Activate" }));
    expect(await findStatus()).toHaveTextContent("Agreement is now active");
    const move = calls.find((c) => c.method === "POST");
    expect(move?.body).toEqual({ to: "ACTIVE" });
    expect(move?.headers.get("If-Match")).toBe('"1"');
    expect(move?.headers.get("Idempotency-Key")).toBeTruthy();
    expect(screen.queryByRole("button", { name: "Activate" })).toBeNull();
    expect(screen.getByRole("button", { name: "Suspend" })).toBeInTheDocument();
  });

  it("asks for a reason before suspending", async () => {
    const state = { agreement: agreement(), visits: [], history: [] };
    handler = serve(state, () => ({ status: 200, body: agreement({ status: "SUSPENDED", statusReason: "credit hold", version: 3 }), etag: '"3"' }));
    renderAt(`/service/agreements/${AGREEMENT}`);
    fireEvent.click(await screen.findByRole("button", { name: "Suspend" }));
    fireEvent.click(screen.getByRole("button", { name: "Confirm suspension" }));
    expect(screen.getByText("Give a reason; it is kept in the status history.")).toBeInTheDocument();
    expect(calls.some((c) => c.method === "POST")).toBe(false);
    fireEvent.change(screen.getByLabelText("Reason"), { target: { value: "credit hold" } });
    fireEvent.click(screen.getByRole("button", { name: "Confirm suspension" }));
    expect(await findStatus()).toHaveTextContent("Agreement is now suspended");
    expect(calls.find((c) => c.method === "POST")?.body).toEqual({ to: "SUSPENDED", reason: "credit hold" });
    expect(screen.getByRole("button", { name: "Resume" })).toBeInTheDocument();
    expect(screen.queryByRole("button", { name: "Record visit" })).toBeNull();
  });

  it("offers no move on an expired agreement and reloads after a version conflict", async () => {
    handler = serve({ agreement: agreement({ status: "EXPIRED" }), visits: [], history: [] });
    const view = renderAt(`/service/agreements/${AGREEMENT}`);
    await screen.findByRole("heading", { name: "Service agreement SA-000007" });
    expect(screen.queryByRole("button", { name: "Activate" })).toBeNull();
    expect(screen.queryByRole("button", { name: "Cancel agreement" })).toBeNull();
    view.unmount();

    const state = { agreement: agreement(), visits: [], history: [] };
    handler = serve(state, () => {
      state.agreement = agreement({ status: "SUSPENDED", version: 3 });
      return { status: 412, body: { code: "conflict_version", detail: "current version is 3" } };
    });
    renderAt(`/service/agreements/${AGREEMENT}`);
    fireEvent.click(await screen.findByRole("button", { name: "Cancel agreement" }));
    fireEvent.change(screen.getByLabelText("Reason"), { target: { value: "left" } });
    fireEvent.click(screen.getByRole("button", { name: "Confirm cancellation" }));
    expect(await findAlert()).toHaveTextContent("Someone else changed this agreement");
    expect(await screen.findByRole("button", { name: "Resume" })).toBeInTheDocument();
  });

  it("shows the reloaded agreement when a later move meets a version conflict", async () => {
    const state = { agreement: agreement({ status: "DRAFT", version: 1 }), visits: [], history: [] };
    let posts = 0;
    handler = serve(state, () => {
      posts += 1;
      if (posts === 1) return { status: 200, body: agreement({ status: "ACTIVE", version: 2 }), etag: '"2"' };
      state.agreement = agreement({ status: "CANCELED", statusReason: "closed elsewhere", version: 4 });
      return { status: 412, body: { code: "conflict_version", detail: "current version is 4" } };
    });
    renderAt(`/service/agreements/${AGREEMENT}`);
    fireEvent.click(await screen.findByRole("button", { name: "Activate" }));
    fireEvent.click(await screen.findByRole("button", { name: "Suspend" }));
    fireEvent.change(screen.getByLabelText("Reason"), { target: { value: "hold" } });
    fireEvent.click(screen.getByRole("button", { name: "Confirm suspension" }));
    expect(await findAlert()).toHaveTextContent("Someone else changed this agreement");
    expect(await screen.findByText("closed elsewhere")).toBeInTheDocument();
    expect(screen.queryByRole("button", { name: "Suspend" })).toBeNull();
    expect(calls.filter((c) => c.method === "POST")[1]?.headers.get("If-Match")).toBe('"2"');
  });

  it("reports an agreement that cannot be loaded", async () => {
    handler = () => ({ status: 404, body: { code: "SVC-AGR-004", detail: "service agreement not found" } });
    renderAt(`/service/agreements/${AGREEMENT}`);
    expect(await findAlert()).toHaveTextContent("SVC-AGR-004");
  });

  it("runs the expiry pass from the list and reloads it", async () => {
    let lists = 0;
    handler = (url, init) => {
      if (init.method === "POST") {
        expect(url.pathname).toBe("/api/v1/service/agreements:expire");
        return { status: 200, body: { scanned: 3, expired: 2 } };
      }
      lists += 1;
      return { status: 200, body: { items: [agreement()] } };
    };
    renderAt("/service/agreements");
    fireEvent.click(await screen.findByRole("button", { name: "Expire ended agreements" }));
    expect(await findStatus()).toHaveTextContent("2 agreements expired.");
    await waitFor(() => expect(lists).toBeGreaterThanOrEqual(2));
  });

  it("reports a failed expiry pass", async () => {
    handler = (_url, init) => (init.method === "POST" ? { status: 403, body: { code: "forbidden", detail: "duty missing" } } : { status: 200, body: { items: [] } });
    renderAt("/service/agreements");
    fireEvent.click(await screen.findByRole("button", { name: "Expire ended agreements" }));
    expect(await findAlert()).toHaveTextContent("forbidden");
  });
});

describe("agreement visits", () => {
  const values = (): VisitValues => ({ lineIndex: "0", kind: "PREVENTIVE", visitDate: "2026-03-01", workOrderId: "", note: "" });

  it("validates a visit like the service does", () => {
    expect(validateVisit(values(), 1).input).toEqual({ lineIndex: 0, kind: "PREVENTIVE", visitDate: "2026-03-01" });
    expect(validateVisit({ ...values(), workOrderId: ` ${WO} `, note: "x" }, 1).input).toMatchObject({ workOrderId: WO, note: "x" });
    const bad = validateVisit({ ...values(), lineIndex: "3", visitDate: "", workOrderId: "nope", note: "x".repeat(501) }, 1);
    expect(Object.keys(bad.errors).sort()).toEqual(["lineIndex", "note", "visitDate", "workOrderId"]);
  });

  it("counts visits per line", () => {
    expect(visitsUsed([visit(), visit({ lineIndex: 1 }), visit()] as never, 0)).toBe(2);
  });

  it("lists visits and history, and records a visit with the remaining allowance", async () => {
    const state = {
      agreement: agreement({ visitsConsumed: 1 }),
      visits: [visit()] as unknown[],
      history: [{ from: "DRAFT", to: "ACTIVE", reason: "", actor: "u1", at: "2026-01-02T08:00:00Z" }],
    };
    handler = serve(state, () => {
      state.visits.push(visit({ id: "bbbbbbbb-0000-4000-8000-00000000000b", visitDate: "2026-04-01" }));
      state.agreement = agreement({ visitsConsumed: 2 });
      return { status: 201, body: { ...visit({ visitDate: "2026-04-01" }), visitsLeft: 0 } };
    });
    const view = renderAt(`/service/agreements/${AGREEMENT}`);
    const visits = await findTable({ name: "Agreement visits" });
    expect(visits).toHaveTextContent("Annual service");
    expect(await findTable({ name: "Agreement status history" })).toHaveTextContent("Draft to Active");
    expect(await screen.findByText(/Line 1: 1 of 2 included visits used/)).toBeInTheDocument();

    fireEvent.change(screen.getByLabelText("Visit date"), { target: { value: "2026-04-01" } });
    fireEvent.change(screen.getByLabelText("Kind"), { target: { value: "CORRECTIVE" } });
    fireEvent.click(screen.getByRole("button", { name: "Record visit" }));
    expect(await findStatus()).toHaveTextContent("Visit recorded; 0 left on line 1.");
    const post = calls.find((c) => c.method === "POST");
    expect(post?.url.pathname).toBe(path("/visits"));
    expect(post?.body).toEqual({ lineIndex: 0, kind: "CORRECTIVE", visitDate: "2026-04-01" });
    expect(post?.headers.get("Idempotency-Key")).toBeTruthy();
    expect(await screen.findByText(/Line 1: 2 of 2 included visits used/)).toBeInTheDocument();
    expect(pageViolations(view.container)).toEqual([]);
  });

  it("shows the service's refusal when no included visit is left", async () => {
    handler = serve({ agreement: agreement(), visits: [], history: [] }, () => ({
      status: 422,
      body: { code: "SVC-AGR-010", detail: "the line includes 2 visits and all are used" },
    }));
    renderAt(`/service/agreements/${AGREEMENT}`);
    fireEvent.click(await screen.findByRole("button", { name: "Record visit" }));
    expect(await findAlert()).toHaveTextContent("SVC-AGR-010");
  });

  it("explains an empty visit list and a failed one", async () => {
    handler = serve({ agreement: agreement(), visits: [], history: [] });
    const view = renderAt(`/service/agreements/${AGREEMENT}`);
    expect(await screen.findByText("No visits recorded")).toBeInTheDocument();
    expect(await screen.findByText("No status changes yet")).toBeInTheDocument();
    view.unmount();

    handler = (url) =>
      url.pathname === path("/visits") ? { status: 500, body: { code: "internal", detail: "boom" } } : serve({ agreement: agreement(), visits: [], history: [] })(url, {});
    renderAt(`/service/agreements/${AGREEMENT}`);
    expect(await findAlert()).toHaveTextContent("Could not load the visits");
  });
});
