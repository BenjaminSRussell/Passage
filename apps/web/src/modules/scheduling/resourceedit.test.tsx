import { fireEvent, render, screen, waitFor } from "@testing-library/react";
import { MemoryRouter, Route, Routes } from "react-router-dom";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { checkAxeAA } from "../../shell/a11y";
import type { Resource } from "./api/client";
import { rowsOf, validateEdit } from "./EditResourcePage";
import scheduling from "./module";

const MIX = "3f1c0000-0000-4000-8000-000000000001";

type Reply = { status: number; body?: unknown; headers?: Record<string, string> };
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
const resource: Resource = { id: MIX, code: "MIX-1", name: "Mixing vessel", finite: true, timeZone: "UTC", shifts, version: 3, createdAt: "2026-10-01T00:00:00Z", engineeringResourceId: null };

describe("validateEdit", () => {
  const same = { name: resource.name, timeZone: resource.timeZone, rows: rowsOf(resource.shifts), engineeringResourceId: "" };
  it("sends only what changed", () => {
    expect(validateEdit({ ...same, name: " Mixer 2 " }, resource).patch).toEqual({ name: "Mixer 2" });
    const rows = same.rows.map((r) => ({ ...r, end: "14:00" }));
    expect(validateEdit({ ...same, rows }, resource).patch?.shifts?.[0]).toEqual({ weekday: 1, startMinute: 480, endMinute: 840 });
  });
  it("mirrors the service rules", () => {
    expect(validateEdit(same, resource).errors.form).toBe("Nothing changed.");
    const eng = "0e000000-0000-4000-8000-0000000000AA";
    expect(validateEdit({ ...same, engineeringResourceId: ` ${eng} ` }, resource).patch).toEqual({ engineeringResourceId: eng.toLowerCase() });
    expect(validateEdit({ ...same, engineeringResourceId: "nope" }, resource).errors.engineeringResourceId).toBeTruthy();
    expect(validateEdit({ ...same, engineeringResourceId: "" }, { ...resource, engineeringResourceId: eng.toLowerCase() }).patch).toEqual({ engineeringResourceId: "" });
    expect(validateEdit({ ...same, name: " " }, resource).errors.name).toBeTruthy();
    expect(validateEdit({ ...same, timeZone: "Mars 1" }, resource).errors.timeZone).toBeTruthy();
    expect(validateEdit({ ...same, rows: [] }, resource).errors.shifts).toBeTruthy();
    expect(validateEdit({ ...same, rows: [{ weekday: 1, start: "10:00", end: "09:00" }] }, resource).errors.shifts).toBeTruthy();
  });
});

describe("Edit resource", () => {
  it("loads, saves with If-Match and lists the jobs the calendar no longer covers", async () => {
    handler = (url, init) => {
      if (init.method === "PATCH") {
        return {
          status: 200,
          body: {
            ...resource,
            name: "Mixer 2",
            version: 4,
            affectedJobs: [{ jobId: "a1", orderId: "b2000000-0000-4000-8000-000000000002", operNumber: 10, start: "2026-11-02T15:00:00Z", end: "2026-11-02T17:00:00Z", firmed: true }],
          },
        };
      }
      if (url.pathname.endsWith(`/resources/${MIX}`)) return { status: 200, body: resource };
    };
    const { container } = renderAt(`/scheduling/resources/${MIX}/edit`);
    const name = await screen.findByLabelText(/Name/);
    expect(checkAxeAA(container).filter((v) => !shellRules.has(v.id))).toEqual([]);
    fireEvent.click(screen.getByRole("button", { name: "Save changes" }));
    expect(await screen.findByText("Nothing changed.")).toBeTruthy();
    fireEvent.change(name, { target: { value: "Mixer 2" } });
    fireEvent.click(screen.getByRole("button", { name: "Save changes" }));
    expect(await screen.findByText("The resource was saved (version 4).")).toBeTruthy();
    const patch = calls.find((c) => c.method === "PATCH")!;
    expect(patch.url.pathname).toBe(`/api/v1/scheduling/resources/${MIX}`);
    expect(patch.headers.get("If-Match")).toBe('"3"');
    expect(patch.headers.get("Idempotency-Key")).toBeTruthy();
    expect(patch.body).toEqual({ name: "Mixer 2" });
    expect(screen.getByText("1 booked job(s) are outside the new calendar")).toBeTruthy();
    expect(screen.getByRole("table", { name: "Jobs outside the calendar" })).toBeTruthy();
    // A second save uses the new version.
    fireEvent.change(name, { target: { value: "Mixer 3" } });
    fireEvent.click(screen.getByRole("button", { name: "Save changes" }));
    await waitFor(() => expect(calls.filter((c) => c.method === "PATCH")).toHaveLength(2));
    expect(calls.filter((c) => c.method === "PATCH")[1].headers.get("If-Match")).toBe('"4"');
  });

  it("adds and removes shifts", async () => {
    handler = (url) => (url.pathname.endsWith(`/resources/${MIX}`) ? { status: 200, body: resource } : undefined);
    renderAt(`/scheduling/resources/${MIX}/edit`);
    await screen.findByLabelText(/Name/);
    expect(screen.getAllByRole("button", { name: /^Remove shift/ })).toHaveLength(5);
    fireEvent.click(screen.getByRole("button", { name: "Remove shift 5" }));
    fireEvent.click(screen.getByRole("button", { name: "Add a shift" }));
    expect(screen.getAllByRole("button", { name: /^Remove shift/ })).toHaveLength(5);
  });

  it("shows a stale version as its own message and reloads the latest version", async () => {
    let current = resource;
    handler = (url, init) => {
      if (init.method === "PATCH") return { status: 412, body: { code: "SCHED-0124", detail: "resource was changed by another user; current version is 5" } };
      if (url.pathname.endsWith(`/resources/${MIX}`)) return { status: 200, body: current };
    };
    renderAt(`/scheduling/resources/${MIX}/edit`);
    fireEvent.change(await screen.findByLabelText(/Name/), { target: { value: "Other" } });
    fireEvent.click(screen.getByRole("button", { name: "Save changes" }));
    expect(await screen.findByText("The resource changed since you opened it")).toBeTruthy();
    current = { ...resource, name: "Renamed elsewhere", version: 5 };
    fireEvent.click(screen.getByRole("button", { name: "Load the latest version" }));
    await waitFor(() => expect((screen.getByLabelText(/Name/) as HTMLInputElement).value).toBe("Renamed elsewhere"));
    expect(screen.queryByText("The resource changed since you opened it")).toBeNull();
    fireEvent.change(screen.getByLabelText(/Name/), { target: { value: "Other" } });
    fireEvent.click(screen.getByRole("button", { name: "Save changes" }));
    await waitFor(() => expect(calls.filter((c) => c.method === "PATCH")).toHaveLength(2));
    expect(calls.filter((c) => c.method === "PATCH")[1].headers.get("If-Match")).toBe('"5"');
  });

  it("shows field errors from the service on their fields", async () => {
    handler = (url, init) => {
      if (init.method === "PATCH") return { status: 400, body: { code: "validation_failed", detail: "invalid", errors: { timeZone: "must be an IANA time zone name" } } };
      if (url.pathname.endsWith(`/resources/${MIX}`)) return { status: 200, body: resource };
    };
    renderAt(`/scheduling/resources/${MIX}/edit`);
    fireEvent.change(await screen.findByLabelText(/Time zone/), { target: { value: "Mars/Olympus" } });
    fireEvent.click(screen.getByRole("button", { name: "Save changes" }));
    expect(await screen.findByText("must be an IANA time zone name")).toBeTruthy();
    expect(screen.queryByText("Could not save the resource")).toBeNull();
  });

  it("shows the loading, error and retry states", async () => {
    let fail = true;
    handler = (url) => {
      if (!url.pathname.endsWith(`/resources/${MIX}`)) return undefined;
      return fail ? { status: 500, body: { code: "COM-0500", detail: "boom" } } : { status: 200, body: resource };
    };
    renderAt(`/scheduling/resources/${MIX}/edit`);
    expect(screen.getByText("Loading the resource…")).toBeTruthy();
    expect(await screen.findByText("Could not load the resource")).toBeTruthy();
    fail = false;
    fireEvent.click(screen.getByRole("button", { name: "Try again" }));
    expect(await screen.findByLabelText(/Name/)).toBeTruthy();
  });
});
