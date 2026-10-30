import { findTable } from "../../test-utils";
import { fireEvent, render, screen, waitFor } from "@testing-library/react";
import { MemoryRouter, Route, Routes } from "react-router-dom";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { checkAxeAA } from "../../shell/a11y";
import type { AutoClockOutReview } from "./api/client";
import { validateAfterHours } from "./AutoClockOutsPage";
import shopfloor from "./module";

const WORKER = "0195f0a0-0000-7000-8000-000000000001";
const REG = "0195f0a0-0000-7000-8000-000000000003";

type Reply = { status: number; body: unknown };
type Handler = (url: URL, init: RequestInit) => Reply | undefined;

const shellRules = new Set(["page-has-heading-one", "landmark-unique", "region", "bypass"]);
const pageViolations = (container: HTMLElement) => checkAxeAA(container).filter((v) => !shellRules.has(v.id));

let handler: Handler;
let calls: { method: string; url: URL }[];
const json = (status: number, body: unknown): Reply => ({ status, body });

beforeEach(() => {
  calls = [];
  handler = () => undefined;
  vi.stubGlobal(
    "fetch",
    vi.fn(async (input: string, init: RequestInit = {}) => {
      const url = new URL(input, "http://localhost");
      calls.push({ method: init.method ?? "GET", url });
      const reply = handler(url, init) ?? json(404, { code: "TEST-404", detail: `no fake for ${init.method ?? "GET"} ${url.pathname}` });
      return new Response(JSON.stringify(reply.body), { status: reply.status, headers: { "Content-Type": "application/json" } });
    }),
  );
});

afterEach(() => {
  vi.unstubAllGlobals();
});

function renderPage() {
  return render(
    <MemoryRouter initialEntries={["/shopfloor/auto-clock-outs"]}>
      <Routes>
        {shopfloor.routes.map((route) => (
          <Route key={route.path} path={route.path} element={route.element} />
        ))}
      </Routes>
    </MemoryRouter>,
  );
}

const review: AutoClockOutReview = {
  registrationId: REG,
  workerId: WORKER,
  workerName: "Ana Rivera",
  clockedInAt: "2026-10-20T06:00:00Z",
  clockedOutAt: "2026-10-20T22:00:00Z",
  closedAt: "2026-10-21T01:00:00Z",
  reviewed: false,
};

describe("Auto clock-outs page", () => {
  it("lists closed clock-ins for review with a link to correct each", async () => {
    let asked: URL | undefined;
    handler = (url) => {
      if (!url.pathname.endsWith("/auto-clock-outs")) return undefined;
      asked = url;
      return json(200, { items: [review, { ...review, registrationId: "0195f0a0-0000-7000-8000-000000000004", workerName: "Ben Okoye", reviewed: true }] });
    };
    const { container } = renderPage();
    const table = await findTable({ name: "Auto clock-outs" });
    await waitFor(() => expect(table).toHaveTextContent("Ana Rivera"));
    expect(table).toHaveTextContent("Needs review");
    expect(table).toHaveTextContent("Reviewed");
    expect(table).toHaveTextContent("2026-10-20 22:00 UTC");
    expect(asked?.searchParams.get("timeZone")).toBeTruthy();
    expect(screen.getByRole("link", { name: "Confirm or correct the clock-out of Ana Rivera" })).toHaveAttribute("href", `/shopfloor/registrations/${REG}/correct`);
    expect(screen.getByRole("link", { name: "Correct the clock-out of Ben Okoye" })).toBeInTheDocument();
    expect(pageViolations(container)).toEqual([]);
  });

  it("confirms an auto clock-out from the list by keeping its exact time (BR-25.3)", async () => {
    const at = "2026-10-20T22:00:41Z";
    let sent: Record<string, unknown> | undefined;
    handler = (url, init) => {
      if (url.pathname.endsWith("/auto-clock-outs")) return json(200, { items: [{ ...review, clockedOutAt: at }] });
      if (init.method === "POST" && url.pathname.endsWith(`/registrations/${REG}:correct`)) {
        sent = JSON.parse(String(init.body));
        return json(200, {
          id: "0195f0a0-0000-7000-8000-000000000009", registrationId: REG, workerId: WORKER, reason: "worked a double", oldStartedAt: at, oldStoppedAt: null,
          oldHours: null, newStartedAt: at, newStoppedAt: null, newHours: null, correctedBy: "sup", correctedAt: "2026-10-21T08:00:00Z",
        });
      }
      return undefined;
    };
    renderPage();
    fireEvent.click(await screen.findByRole("link", { name: "Confirm or correct the clock-out of Ana Rivera" }));
    expect(await screen.findByLabelText("Started (UTC)")).toHaveValue("2026-10-20T22:00");
    expect(screen.getByText("Closed automatically. Keep this time to confirm it, or change it.")).toBeInTheDocument();
    expect(screen.queryByLabelText(/Stopped/)).toBeNull();
    fireEvent.change(screen.getByLabelText("Reason"), { target: { value: "worked a double" } });
    fireEvent.click(screen.getByRole("button", { name: "Save correction" }));
    expect(await screen.findByText(/Confirmed at 2026-10-20 22:00 UTC/)).toBeInTheDocument();
    expect(sent).toEqual({ startedAt: at, reason: "worked a double" });
  });

  it("shows an empty state", async () => {
    handler = (url) => (url.pathname.endsWith("/auto-clock-outs") ? json(200, { items: [] }) : undefined);
    renderPage();
    expect(await screen.findByText("No automatic clock-outs in this range")).toBeInTheDocument();
  });

  it("shows the error and retries", async () => {
    let failing = true;
    handler = (url) => {
      if (!url.pathname.endsWith("/auto-clock-outs")) return undefined;
      return failing ? json(500, { code: "COM-0500", detail: "database down" }) : json(200, { items: [review] });
    };
    renderPage();
    expect(await screen.findByText(/database down/)).toBeInTheDocument();
    failing = false;
    fireEvent.click(screen.getByRole("button", { name: "Try again" }));
    expect(await screen.findByText("Ana Rivera")).toBeInTheDocument();
  });

  it("runs the sweep with the chosen hours, reports the count and reloads the list", async () => {
    handler = (url, init) => {
      if (url.pathname.endsWith("/registrations:auto-clock-out") && init.method === "POST") {
        return json(200, { afterHours: Number(url.searchParams.get("afterHours")), items: [{ registrationId: REG, workerId: WORKER, clockInId: "x", clockedInAt: "a", clockedOutAt: "b" }] });
      }
      if (url.pathname.endsWith("/auto-clock-outs")) return json(200, { items: [] });
      return undefined;
    };
    renderPage();
    await screen.findByText("No automatic clock-outs in this range");
    fireEvent.change(screen.getByLabelText("Close clock-ins open longer than (hours)"), { target: { value: "12" } });
    fireEvent.click(screen.getByRole("button", { name: "Close forgotten clock-ins" }));
    expect(await screen.findByText("Closed 1 clock-in open longer than 12 hours.")).toBeInTheDocument();
    expect(calls.filter((c) => c.method === "POST")[0].url.searchParams.get("afterHours")).toBe("12");
    await waitFor(() => expect(calls.filter((c) => c.url.pathname.endsWith("/auto-clock-outs")).length).toBe(2));
  });

  it("refuses hours outside 4 to 24 without calling the service and shows service errors", async () => {
    handler = (url, init) => {
      if (url.pathname.endsWith("/registrations:auto-clock-out") && init.method === "POST") return json(403, { code: "COM-0403", detail: "duty missing" });
      if (url.pathname.endsWith("/auto-clock-outs")) return json(200, { items: [] });
      return undefined;
    };
    renderPage();
    await screen.findByText("No automatic clock-outs in this range");
    const input = screen.getByLabelText("Close clock-ins open longer than (hours)");
    fireEvent.change(input, { target: { value: "2" } });
    fireEvent.click(screen.getByRole("button", { name: "Close forgotten clock-ins" }));
    expect(await screen.findByText("Enter whole hours from 4 to 24.")).toBeInTheDocument();
    expect(calls.some((c) => c.method === "POST")).toBe(false);
    fireEvent.change(input, { target: { value: "16" } });
    fireEvent.click(screen.getByRole("button", { name: "Close forgotten clock-ins" }));
    expect(await screen.findByText(/duty missing/)).toBeInTheDocument();
  });

  it("validates the hours like the service", () => {
    expect(validateAfterHours("16")).toBeNull();
    expect(validateAfterHours("4")).toBeNull();
    expect(validateAfterHours("24")).toBeNull();
    for (const bad of ["", "3", "25", "1.5", "abc"]) expect(validateAfterHours(bad)).not.toBeNull();
  });
});
