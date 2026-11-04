import { fireEvent, render, screen, waitFor } from "@testing-library/react";
import { MemoryRouter, Route, Routes } from "react-router-dom";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { checkAxeAA } from "../../shell/a11y";
import type { Operation, Resource } from "./api/client";
import scheduling from "./module";
import { initialValues, validateOperationEdit } from "./OperationDetailPage";

const MIX = "3f1c0000-0000-4000-8000-000000000001";
const FILL = "3f1c0000-0000-4000-8000-000000000002";
const TOOL = "3f1c0000-0000-4000-8000-000000000003";
const OP = "7a000000-0000-4000-8000-000000000001";
const ORDER = "b2000000-0000-4000-8000-000000000002";

type Reply = { status: number; body?: unknown };
let handler: (url: URL, init: RequestInit) => Reply | undefined;
let calls: { method: string; url: URL; body: unknown; headers: Headers }[];

const shellRules = new Set(["page-has-heading-one", "landmark-unique", "region", "bypass"]);

beforeEach(() => {
  calls = [];
  vi.stubGlobal(
    "fetch",
    vi.fn(async (input: string, init: RequestInit = {}) => {
      const url = new URL(input, "http://localhost");
      calls.push({ method: init.method ?? "GET", url, body: init.body ? JSON.parse(String(init.body)) : undefined, headers: new Headers(init.headers) });
      const reply = handler(url, init) ?? { status: 404, body: { code: "TEST-404", detail: "no fake" } };
      return new Response(reply.body === undefined ? null : JSON.stringify(reply.body), { status: reply.status, headers: { "Content-Type": "application/json" } });
    }),
  );
});
afterEach(() => vi.unstubAllGlobals());

function renderAt(path: string) {
  return render(
    <MemoryRouter initialEntries={[path]}>
      <Routes>
        {scheduling.routes.map((route) => (
          <Route key={route.path} path={route.path} element={route.element} />
        ))}
      </Routes>
    </MemoryRouter>,
  );
}

const shifts = [1, 2, 3, 4, 5].map((weekday) => ({ weekday, startMinute: 480, endMinute: 960 }));
const resources: Resource[] = [
  { id: MIX, code: "MIX-1", name: "Mixing vessel", finite: true, timeZone: "UTC", shifts, version: 1, createdAt: "2026-10-01T00:00:00Z", engineeringResourceId: null },
  { id: FILL, code: "FILL-1", name: "Filler", finite: true, timeZone: "UTC", shifts, version: 1, createdAt: "2026-10-01T00:00:00Z", engineeringResourceId: null },
  { id: TOOL, code: "TOOL-1", name: "Tool", finite: true, timeZone: "UTC", shifts, version: 1, createdAt: "2026-10-01T00:00:00Z", engineeringResourceId: null },
];
const codes = Object.fromEntries(resources.map((r) => [r.id, r.code]));
const idsByCode = Object.fromEntries(resources.map((r) => [r.code, r.id]));

const op: Operation = {
  id: OP,
  orderId: ORDER,
  operNumber: 10,
  minutes: 60,
  dueAt: "2026-11-05T12:00:00Z",
  priority: 100,
  resourceIds: [MIX],
  secondaryResourceIds: [TOOL],
  setupGroup: null,
  setupMinutes: 0,
  queueBeforeMinutes: 0,
  queueAfterMinutes: 0,
  attributes: {},
  networkId: null,
  status: "scheduled",
  createdAt: "2026-10-26T06:00:00Z",
  version: 2,
};

const base = (url: URL) => {
  if (url.pathname.endsWith("/resources")) return { status: 200, body: { items: resources } };
  return undefined;
};

describe("validateOperationEdit", () => {
  const same = initialValues(op, codes);
  it("sends only what changed", () => {
    expect(validateOperationEdit({ ...same, minutes: "90" }, op, idsByCode).patch).toEqual({ minutes: 90 });
    expect(validateOperationEdit({ ...same, priority: "5" }, op, idsByCode).patch).toEqual({ priority: 5 });
    expect(validateOperationEdit({ ...same, dueAt: "2026-11-06T08:30" }, op, idsByCode).patch).toEqual({ dueAt: "2026-11-06T08:30:00Z" });
    expect(validateOperationEdit({ ...same, queueBeforeMinutes: "30", queueAfterMinutes: "45" }, op, idsByCode).patch).toEqual({ queueBeforeMinutes: 30, queueAfterMinutes: 45 });
    expect(validateOperationEdit({ ...same, candidates: "fill-1, MIX-1" }, op, idsByCode).patch).toEqual({ resourceIds: [FILL, MIX] });
  });
  it("mirrors the service rules", () => {
    expect(validateOperationEdit(same, op, idsByCode).errors.form).toBe("Nothing changed.");
    for (const bad of ["", "0", "100001", "1.5", "x"]) {
      expect(validateOperationEdit({ ...same, minutes: bad }, op, idsByCode).errors.minutes).toBeTruthy();
    }
    expect(validateOperationEdit({ ...same, priority: "1001" }, op, idsByCode).errors.priority).toBeTruthy();
    expect(validateOperationEdit({ ...same, setupMinutes: "-1" }, op, idsByCode).errors.setupMinutes).toBeTruthy();
    expect(validateOperationEdit({ ...same, queueBeforeMinutes: "-1" }, op, idsByCode).errors.queueBeforeMinutes).toBeTruthy();
    expect(validateOperationEdit({ ...same, queueAfterMinutes: "10081" }, op, idsByCode).errors.queueAfterMinutes).toBeTruthy();
    expect(validateOperationEdit({ ...same, candidates: "" }, op, idsByCode).errors.candidates).toBeTruthy();
    expect(validateOperationEdit({ ...same, candidates: "NOPE" }, op, idsByCode).errors.candidates).toMatch(/No resource/);
    expect(validateOperationEdit({ ...same, candidates: "MIX-1, MIX-1" }, op, idsByCode).errors.candidates).toMatch(/twice/);
    expect(validateOperationEdit({ ...same, candidates: "TOOL-1" }, op, idsByCode).errors.candidates).toMatch(/secondary/);
  });
});

describe("Operation detail", () => {
  it("loads, saves with If-Match and says the jobs were removed", async () => {
    handler = (url, init) => {
      if (init.method === "PATCH") return { status: 200, body: { ...op, minutes: 90, status: "open", version: 3, jobsRemoved: true } };
      if (url.pathname.endsWith(`/operations/${OP}`)) return { status: 200, body: op };
      return base(url);
    };
    const { container } = renderAt(`/scheduling/operations/${OP}`);
    const minutes = await screen.findByLabelText(/Duration/);
    expect(checkAxeAA(container).filter((v) => !shellRules.has(v.id))).toEqual([]);
    fireEvent.click(screen.getByRole("button", { name: "Save changes" }));
    expect(await screen.findByText("Nothing changed.")).toBeTruthy();
    fireEvent.change(minutes, { target: { value: "90" } });
    fireEvent.click(screen.getByRole("button", { name: "Save changes" }));
    expect(await screen.findByText("The operation was saved (version 3).")).toBeTruthy();
    expect(screen.getByText("The operation is off the schedule")).toBeTruthy();
    const patch = calls.find((c) => c.method === "PATCH")!;
    expect(patch.url.pathname).toBe(`/api/v1/scheduling/operations/${OP}`);
    expect(patch.headers.get("If-Match")).toBe('"2"');
    expect(patch.headers.get("Idempotency-Key")).toBeTruthy();
    expect(patch.body).toEqual({ minutes: 90 });
  });

  it("offers the latest version after a stale save and shows a firmed refusal", async () => {
    let reply: Reply = { status: 412, body: { code: "SCHED-0137", detail: "operation was changed by another user; current version is 5" } };
    let current = op;
    handler = (url, init) => {
      if (init.method === "PATCH") return reply;
      if (url.pathname.endsWith(`/operations/${OP}`)) return { status: 200, body: current };
      return base(url);
    };
    renderAt(`/scheduling/operations/${OP}`);
    fireEvent.change(await screen.findByLabelText(/Duration/), { target: { value: "30" } });
    fireEvent.click(screen.getByRole("button", { name: "Save changes" }));
    expect(await screen.findByText("Someone else changed this operation")).toBeTruthy();
    current = { ...op, minutes: 45, version: 5 };
    fireEvent.click(screen.getByRole("button", { name: "Load the latest version" }));
    await waitFor(() => expect((screen.getByLabelText(/Duration/) as HTMLInputElement).value).toBe("45"));
    reply = { status: 409, body: { code: "SCHED-0138", detail: "the operation is held by a firmed job; unfirm it first" } };
    fireEvent.change(screen.getByLabelText(/Duration/), { target: { value: "50" } });
    fireEvent.click(screen.getByRole("button", { name: "Save changes" }));
    expect(await screen.findByText("A firmed job holds this operation")).toBeTruthy();
    expect(calls.filter((c) => c.method === "PATCH")[1].headers.get("If-Match")).toBe('"5"');
  });

  it("shows service field errors on their fields", async () => {
    handler = (url, init) => {
      if (init.method === "PATCH") return { status: 400, body: { code: "validation_failed", detail: "invalid", errors: { priority: "must be 0 to 1000" } } };
      if (url.pathname.endsWith(`/operations/${OP}`)) return { status: 200, body: op };
      return base(url);
    };
    renderAt(`/scheduling/operations/${OP}`);
    fireEvent.change(await screen.findByLabelText(/Duration/), { target: { value: "61" } });
    fireEvent.click(screen.getByRole("button", { name: "Save changes" }));
    expect(await screen.findByText("must be 0 to 1000")).toBeTruthy();
  });

  it("deletes after a confirmation and returns to the list", async () => {
    handler = (url, init) => {
      if (init.method === "DELETE") return { status: 200, body: { operationId: OP, orderId: ORDER, operNumber: 10, jobsRemoved: true } };
      if (url.pathname.endsWith(`/operations/${OP}`)) return { status: 200, body: op };
      if (url.pathname.endsWith("/operations")) return { status: 200, body: { items: [] } };
      if (url.pathname.endsWith("/networks")) return { status: 200, body: { items: [] } };
      return base(url);
    };
    renderAt(`/scheduling/operations/${OP}`);
    fireEvent.click(await screen.findByRole("button", { name: "Delete this operation" }));
    expect(calls.some((c) => c.method === "DELETE")).toBe(false);
    fireEvent.click(screen.getByRole("button", { name: "Keep the operation" }));
    fireEvent.click(screen.getByRole("button", { name: "Delete this operation" }));
    fireEvent.click(screen.getByRole("button", { name: "Delete the operation" }));
    expect(await screen.findByText("No operations yet")).toBeTruthy();
    const del = calls.find((c) => c.method === "DELETE")!;
    expect(del.headers.get("If-Match")).toBe('"2"');
    expect(del.headers.get("Idempotency-Key")).toBeTruthy();
  });

  it("takes the order off the schedule and reports firmed operations that stayed", async () => {
    let status: Operation["status"] = "scheduled";
    handler = (url, init) => {
      if (url.pathname.endsWith(`/orders/${ORDER}:unschedule`)) {
        status = "open";
        return { status: 200, body: { orderId: ORDER, operations: [10], jobsRemoved: 1, keptFirmed: [20] } };
      }
      if (url.pathname.endsWith(`/operations/${OP}`) && init.method === undefined) return { status: 200, body: { ...op, status } };
      return base(url);
    };
    renderAt(`/scheduling/operations/${OP}`);
    fireEvent.click(await screen.findByRole("button", { name: "Take the order off the schedule" }));
    fireEvent.click(screen.getByRole("button", { name: "Take the order off the schedule" }));
    expect(await screen.findByText("Order taken off the schedule: 1 operation(s) are open again.")).toBeTruthy();
    expect(screen.getByText("Firmed operations stayed where they are")).toBeTruthy();
    expect(screen.getByText("Open")).toBeTruthy();
    const post = calls.find((c) => c.method === "POST")!;
    expect(post.url.pathname).toBe(`/api/v1/scheduling/orders/${ORDER}:unschedule`);
    expect(post.headers.get("Idempotency-Key")).toBeTruthy();
  });

  it("shows the error and retry states", async () => {
    let fail = true;
    handler = (url) => {
      if (url.pathname.endsWith(`/operations/${OP}`)) return fail ? { status: 404, body: { code: "SCHED-0136", detail: "operation not found" } } : { status: 200, body: op };
      return base(url);
    };
    renderAt(`/scheduling/operations/${OP}`);
    expect(screen.getByText("Loading the operation…")).toBeTruthy();
    expect(await screen.findByText("Could not load the operation")).toBeTruthy();
    fail = false;
    fireEvent.click(screen.getByRole("button", { name: "Try again" }));
    expect(await screen.findByLabelText(/Duration/)).toBeTruthy();
  });
});
