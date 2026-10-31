import { findTable } from "../../test-utils";
import { fireEvent, render, screen, waitFor } from "@testing-library/react";
import { MemoryRouter, Route, Routes } from "react-router-dom";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { checkAxeAA } from "../../shell/a11y";
import type { CapacityOverride, Resource } from "./api/client";
import scheduling from "./module";
import { validateOverride } from "./NewCapacityOverridePage";

const MIX = "3f1c0000-0000-4000-8000-000000000001";
const OV = "7a000000-0000-4000-8000-000000000001";

type Reply = { status: number; body?: unknown };
let handler: (url: URL, init: RequestInit) => Reply | undefined;
let calls: { method: string; url: URL; body: unknown }[];

const shellRules = new Set(["page-has-heading-one", "landmark-unique", "region", "bypass"]);

beforeEach(() => {
  calls = [];
  vi.stubGlobal(
    "fetch",
    vi.fn(async (input: string, init: RequestInit = {}) => {
      const url = new URL(input, "http://localhost");
      calls.push({ method: init.method ?? "GET", url, body: init.body ? JSON.parse(String(init.body)) : undefined });
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
const resource: Resource = { id: MIX, code: "MIX-1", name: "Mixing vessel", finite: true, timeZone: "UTC", shifts, version: 1, createdAt: "2026-10-01T00:00:00Z" };
const downtime: CapacityOverride = { id: OV, resourceId: MIX, kind: "downtime", start: "2026-11-02T10:00:00Z", end: "2026-11-02T12:00:00Z", reason: "press repair", createdAt: "2026-10-30T00:00:00Z" };

describe("validateOverride", () => {
  const ok = { resourceId: MIX, kind: "downtime" as const, start: "2026-11-02T10:00", end: "2026-11-02T12:00", reason: " repair " };
  it("builds the request with UTC instants and a trimmed reason", () => {
    expect(validateOverride(ok).input).toEqual({ kind: "downtime", start: "2026-11-02T10:00:00Z", end: "2026-11-02T12:00:00Z", reason: "repair" });
  });
  it("mirrors the service rules", () => {
    expect(validateOverride({ ...ok, start: "" }).errors.start).toBeTruthy();
    expect(validateOverride({ ...ok, end: "2026-11-02T10:00" }).errors.end).toMatch(/after the start/);
    expect(validateOverride({ ...ok, end: "2026-12-30T10:00" }).errors.end).toMatch(/31 days/);
    expect(validateOverride({ ...ok, resourceId: "" }).errors.resourceId).toBeTruthy();
    expect(validateOverride({ ...ok, reason: "x".repeat(201) }).errors.reason).toBeTruthy();
  });
});

describe("Capacity overrides list", () => {
  it("shows loading, then the overrides, and removes one", async () => {
    let items = [downtime];
    handler = (url, init) => {
      if (url.pathname.endsWith("/resources")) return { status: 200, body: { items: [resource] } };
      if (init.method === "DELETE") {
        items = [];
        return { status: 204 };
      }
      if (url.pathname.endsWith("/capacity-overrides")) return { status: 200, body: { items } };
    };
    const { container } = renderAt("/scheduling/capacity-overrides");
    const table = await findTable({ name: "Capacity overrides" });
    expect(table).toBeTruthy();
    expect(await screen.findByText("press repair")).toBeTruthy();
    expect(checkAxeAA(container).filter((v) => !shellRules.has(v.id))).toEqual([]);
    fireEvent.click(screen.getByRole("button", { name: /^Remove downtime from / }));
    expect(await screen.findByText(/was removed/)).toBeTruthy();
    expect(calls.some((c) => c.method === "DELETE" && c.url.pathname === `/api/v1/scheduling/resources/${MIX}/capacity-overrides/${OV}`)).toBe(true);
    expect(await screen.findByText("No downtimes or extra shifts")).toBeTruthy();
  });

  it("shows the error state and retries", async () => {
    let fail = true;
    handler = (url) => {
      if (url.pathname.endsWith("/resources")) return { status: 200, body: { items: [resource] } };
      if (fail) return { status: 500, body: { code: "COM-0500", detail: "boom" } };
      return { status: 200, body: { items: [] } };
    };
    renderAt("/scheduling/capacity-overrides");
    expect(await screen.findByText("Could not load capacity overrides")).toBeTruthy();
    fail = false;
    fireEvent.click(screen.getByRole("button", { name: "Try again" }));
    expect(await screen.findByText("No downtimes or extra shifts")).toBeTruthy();
  });

  it("asks for a resource first when there is none", async () => {
    handler = (url) => (url.pathname.endsWith("/resources") ? { status: 200, body: { items: [] } } : undefined);
    renderAt("/scheduling/capacity-overrides");
    expect(await screen.findByText("Add a resource first")).toBeTruthy();
  });
});

describe("New capacity override", () => {
  it("posts the override and lists the jobs a downtime overlaps", async () => {
    handler = (url, init) => {
      if (url.pathname.endsWith("/resources")) return { status: 200, body: { items: [resource] } };
      if (init.method === "POST") {
        return {
          status: 201,
          body: { ...downtime, affectedJobs: [{ jobId: "a1", orderId: "b2000000-0000-4000-8000-000000000002", operNumber: 10, start: downtime.start, end: downtime.end, firmed: false }] },
        };
      }
    };
    const { container } = renderAt(`/scheduling/capacity-overrides/new?resourceId=${MIX}`);
    const start = await screen.findByLabelText(/From/);
    fireEvent.click(screen.getByRole("button", { name: "Save override" }));
    expect(await screen.findByText("Enter the start date and time.")).toBeTruthy();
    fireEvent.change(start, { target: { value: "2026-11-02T10:00" } });
    fireEvent.change(screen.getByLabelText(/To/), { target: { value: "2026-11-02T12:00" } });
    fireEvent.change(screen.getByLabelText(/Reason/), { target: { value: "press repair" } });
    fireEvent.click(screen.getByRole("button", { name: "Save override" }));
    expect(await screen.findByText("1 booked job(s) overlap the downtime")).toBeTruthy();
    const post = calls.find((c) => c.method === "POST")!;
    expect(post.url.pathname).toBe(`/api/v1/scheduling/resources/${MIX}/capacity-overrides`);
    expect(post.body).toEqual({ kind: "downtime", start: "2026-11-02T10:00:00Z", end: "2026-11-02T12:00:00Z", reason: "press repair" });
    expect(checkAxeAA(container).filter((v) => !shellRules.has(v.id))).toEqual([]);
  });

  it("shows a service problem", async () => {
    handler = (url, init) => {
      if (url.pathname.endsWith("/resources")) return { status: 200, body: { items: [resource] } };
      if (init.method === "POST") return { status: 404, body: { code: "SCHED-0101", detail: "resource not found" } };
    };
    renderAt("/scheduling/capacity-overrides/new");
    fireEvent.change(await screen.findByLabelText(/From/), { target: { value: "2026-11-02T10:00" } });
    fireEvent.change(screen.getByLabelText(/To/), { target: { value: "2026-11-02T12:00" } });
    fireEvent.click(screen.getByRole("button", { name: "Save override" }));
    await waitFor(() => expect(screen.getByText(/resource not found/)).toBeTruthy());
  });
});
