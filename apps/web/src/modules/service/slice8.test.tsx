import { findAlert, findStatus, findTable } from "../../test-utils";
import { fireEvent, render, screen } from "@testing-library/react";
import { MemoryRouter, Route, Routes } from "react-router-dom";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { checkAxeAA } from "../../shell/a11y";
import { PrincipalProvider } from "../../shell/principal";
import { emptyAction, validateAction } from "./ActionLogPanel";
import service from "./module";

const LE = "22222222-0000-4000-8000-000000000002";
const TERR = "55555555-0000-4000-8000-000000000005";
const TECH = "77777777-0000-4000-8000-000000000007";
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

/** Office staff keying in a device's actions: the technician duty and work-order maintenance. */
const TECHNICIAN = ["fieldservice.inquire", "fieldservice.work-order.execute", "fieldservice.work-order.maintain"];

function renderAt(path: string, duties: readonly string[] = TECHNICIAN) {
  return render(
    <PrincipalProvider duties={duties}>
      <MemoryRouter initialEntries={[path]}>
        <Routes>
          {service.routes.map((route) => (
            <Route key={route.path} path={route.path} element={route.element} />
          ))}
          <Route path="*" element={<p>Elsewhere</p>} />
        </Routes>
      </MemoryRouter>
    </PrincipalProvider>,
  );
}

const PART = "88888888-0000-4000-8000-000000000008";
const A1 = "99999999-0000-4000-8000-000000000001";

const workOrder = (overrides: Record<string, unknown> = {}) => ({
  id: WO,
  number: "SWO-000008",
  customerPartyId: "11111111-0000-4000-8000-000000000001",
  legalEntityId: LE,
  territoryId: TERR,
  estDurationMinutes: 60,
  currency: "USD",
  status: "DISPATCHED",
  technicianId: TECH,
  lines: [{ id: PART, kind: "PART", qty: "2", unitPrice: "10", unitCost: "5" }],
  requiredSkills: [],
  address: "",
  version: 3,
  ...overrides,
});

const logged = (overrides: Record<string, unknown> = {}) => ({
  id: A1,
  workOrderId: WO,
  technicianId: TECH,
  kind: "WORK_START",
  deviceTime: "2026-10-26T14:10:00Z",
  receivedAt: "2026-10-26T22:00:00Z",
  payload: {},
  outcome: "APPLIED",
  ...overrides,
});

const route = (items: unknown[], wo = workOrder()): Handler => (url, init) => {
  if (url.pathname.endsWith("/actions") && init.method !== "POST") return { status: 200, body: { items } };
  if (url.pathname.endsWith("/actions")) throw new Error("unexpected POST");
  return { status: 200, body: wo, etag: `"${wo.version}"` };
};

describe("action validation", () => {
  it("checks each kind like the service does", () => {
    const ok = validateAction({ ...emptyAction(new Date("2026-10-26T14:00:00Z")), kind: "WORK_START" }, A1);
    expect(ok.errors).toEqual({});
    expect(ok.input).toEqual({ id: A1, kind: "WORK_START", deviceTime: "2026-10-26T14:00:00Z" });
    const part = validateAction({ ...emptyAction(), kind: "PART_USED", lineId: "", qty: "0" }, A1);
    expect(Object.keys(part.errors).sort()).toEqual(["lineId", "qty"]);
    expect(validateAction({ ...emptyAction(), kind: "PART_USED", lineId: PART, qty: "1.5" }, A1).input).toMatchObject({ lineId: PART, qty: "1.5" });
    const sign = validateAction({ ...emptyAction(), kind: "SIGNATURE" }, A1);
    expect(Object.keys(sign.errors).sort()).toEqual(["signatureRef", "signedBy"]);
    expect(validateAction({ ...emptyAction(), deviceTime: "" }, A1).errors.deviceTime).toBeTruthy();
    expect(validateAction({ ...emptyAction(), kind: "NOTE", text: "x".repeat(4001) }, A1).errors.text).toBeTruthy();
  });
});

describe("technician action log", () => {
  it("lists the log in merge order with each outcome and reason", async () => {
    handler = route([
      logged(),
      logged({ id: "a2", kind: "TRAVEL_START", deviceTime: "2026-10-26T14:20:00Z", outcome: "SUPERSEDED", reason: "the work order is already IN_PROGRESS" }),
      logged({ id: "a3", kind: "COMPLETE", deviceTime: "2026-10-26T15:00:00Z", outcome: "REJECTED", reasonCode: "SVC-WO-001", reason: "the customer's signature is required" }),
      logged({ id: "a4", kind: "PART_USED", payload: { lineId: PART, qty: "2" } }),
    ]);
    const { container } = renderAt(`/service/work-orders/${WO}`);
    const table = await findTable({ name: "Technician actions" });
    expect(table).toHaveTextContent("2026-10-26 14:20 UTC");
    expect(table).toHaveTextContent("Superseded");
    expect(table).toHaveTextContent("SVC-WO-001: the customer's signature is required");
    expect(table).toHaveTextContent("2 of line 88888888");
    expect(pageViolations(container)).toEqual([]);
  });

  it("shows an empty state, and an error with a retry", async () => {
    handler = route([]);
    renderAt(`/service/work-orders/${WO}`);
    expect(await screen.findByText("No actions yet")).toBeInTheDocument();
  });

  it("reports a failed load and retries", async () => {
    let fail = true;
    handler = (url, init) => (url.pathname.endsWith("/actions") && fail ? { status: 500, body: { code: "COM-0500", detail: "boom" } } : route([logged()])(url, init));
    renderAt(`/service/work-orders/${WO}`);
    expect(await screen.findByText("Could not load the action log")).toBeInTheDocument();
    fail = false;
    fireEvent.click(screen.getByRole("button", { name: "Try again" }));
    expect(await findTable({ name: "Technician actions" })).toHaveTextContent("Work start");
  });

  it("keys in an action for the assigned technician and reports the merge", async () => {
    let posted = false;
    handler = (url, init) => {
      if (url.pathname.endsWith("/actions") && init.method === "POST") {
        posted = true;
        return {
          status: 200,
          body: {
            results: [{ id: A1, kind: "PART_USED", outcome: "APPLIED", duplicate: false }],
            applied: 1,
            duplicates: 0,
            superseded: 0,
            rejected: 0,
            workOrder: workOrder({ version: 4 }),
          },
        };
      }
      return route(posted ? [logged({ kind: "PART_USED", payload: { lineId: PART, qty: "1" } })] : [])(url, init);
    };
    renderAt(`/service/work-orders/${WO}`);
    fireEvent.change(await screen.findByLabelText("Action"), { target: { value: "PART_USED" } });
    fireEvent.change(screen.getByLabelText("Part line"), { target: { value: PART } });
    fireEvent.change(screen.getByLabelText("Quantity used"), { target: { value: "1" } });
    fireEvent.click(screen.getByRole("button", { name: "Record action" }));
    expect(await findStatus()).toHaveTextContent("Action recorded.");
    const post = calls.find((c) => c.method === "POST");
    expect(post?.url.pathname).toBe(`/api/v1/service/work-orders/${WO}/actions`);
    expect(post?.body).toMatchObject({ technicianId: TECH, actions: [{ kind: "PART_USED", lineId: PART, qty: "1" }] });
    expect(await findTable({ name: "Technician actions" })).toHaveTextContent("Part used");
  });

  it("surfaces a refused action and a superseded one", async () => {
    handler = (url, init) =>
      url.pathname.endsWith("/actions") && init.method === "POST"
        ? {
            status: 200,
            body: {
              results: [{ id: A1, kind: "COMPLETE", outcome: "REJECTED", code: "SVC-WO-001", reason: "the customer's signature is required", duplicate: false }],
              applied: 0,
              duplicates: 0,
              superseded: 0,
              rejected: 1,
              workOrder: workOrder(),
            },
          }
        : route([])(url, init);
    renderAt(`/service/work-orders/${WO}`);
    fireEvent.change(await screen.findByLabelText("Action"), { target: { value: "COMPLETE" } });
    fireEvent.click(screen.getByRole("button", { name: "Record action" }));
    expect(await findAlert()).toHaveTextContent("SVC-WO-001: the customer's signature is required");
  });

  it("shows a validation error and a service error without losing the form", async () => {
    handler = (url, init) => (url.pathname.endsWith("/actions") && init.method === "POST" ? { status: 422, body: { code: "SVC-SYNC-005", detail: "the action id was already used" } } : route([])(url, init));
    renderAt(`/service/work-orders/${WO}`);
    fireEvent.change(await screen.findByLabelText("Action"), { target: { value: "SIGNATURE" } });
    fireEvent.click(screen.getByRole("button", { name: "Record action" }));
    expect(await screen.findByText("Enter who signed, up to 120 characters.")).toBeInTheDocument();
    expect(calls.some((c) => c.method === "POST")).toBe(false);
    fireEvent.change(screen.getByLabelText("Signed by"), { target: { value: "Pat" } });
    fireEvent.change(screen.getByLabelText("Signature reference"), { target: { value: "s3://sig/1.png" } });
    fireEvent.click(screen.getByRole("button", { name: "Record action" }));
    expect(await findAlert()).toHaveTextContent("SVC-SYNC-005");
  });

  it("offers no entry form without the technician duty or before the work order is assigned", async () => {
    handler = route([]);
    for (const duties of [["fieldservice.inquire"], ["fieldservice.inquire", "fieldservice.work-order.execute"], ["fieldservice.inquire", "fieldservice.work-order.maintain"]]) {
      const { unmount } = renderAt(`/service/work-orders/${WO}`, duties);
      expect(await screen.findByText(/needs the fieldservice.work-order.execute and fieldservice.work-order.maintain duties/)).toBeInTheDocument();
      expect(screen.queryByRole("button", { name: "Record action" })).toBeNull();
      unmount();
    }
    handler = route([], workOrder({ status: "UNSCHEDULED", technicianId: null }));
    renderAt(`/service/work-orders/${WO}`);
    expect(await screen.findByText(/once the work order is assigned and dispatched/)).toBeInTheDocument();
  });

  it("no longer offers to schedule or dispatch by hand", async () => {
    handler = route([], workOrder({ status: "UNSCHEDULED", technicianId: null }));
    renderAt(`/service/work-orders/${WO}`);
    expect(await screen.findByRole("button", { name: "Cancel work order" })).toBeInTheDocument();
    expect(screen.queryByRole("button", { name: "Schedule" })).toBeNull();
    expect(screen.queryByRole("button", { name: "Dispatch" })).toBeNull();
  });
});
