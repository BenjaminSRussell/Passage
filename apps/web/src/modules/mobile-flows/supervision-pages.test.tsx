import { findAlert, findTable } from "../../test-utils";
import { fireEvent, render, screen, waitFor, within } from "@testing-library/react";
import { MemoryRouter, Route, Routes } from "react-router-dom";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { checkAxeAA } from "../../shell/a11y";
import type { OfflineConflict } from "./api/client";
import type { PrinterStatus } from "./api/print";
import mobileFlows from "./module";
import { conflictMessage } from "./OfflineConflictsPage";

type Reply = { status: number; body: unknown };
type Handler = (url: URL, init: RequestInit) => Reply | undefined;

const shellRules = new Set(["page-has-heading-one", "landmark-unique", "region", "bypass"]);
function pageViolations(container: HTMLElement) {
  return checkAxeAA(container).filter((v) => !shellRules.has(v.id));
}

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
      return new Response(reply.body === undefined ? null : JSON.stringify(reply.body), {
        status: reply.status,
        headers: { "Content-Type": "application/json" },
      });
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
        {mobileFlows.routes.map((route) => (
          <Route key={route.path} path={route.path} element={route.element} />
        ))}
      </Routes>
    </MemoryRouter>,
  );
}

const conflict: OfflineConflict = {
  idempotencyKey: "dev-7:41",
  deviceId: "dev-7",
  command: "ConfirmWorkLine",
  capturedAt: "2026-10-21T08:00:00Z",
  conflictReason: "line was CANCELLED by alice",
  version: 3,
};

describe("offline conflicts (BR-32.4, AC-32.6)", () => {
  it("formats the MOB-0107 catalog text", () => {
    expect(conflictMessage(conflict)).toBe("Transaction dev-7:41 conflicts: line was CANCELLED by alice.");
  });

  it("lists conflicts and resolves one as a movement with If-Match and an Idempotency-Key", async () => {
    let resolved = false;
    handler = (url, init) => {
      if (init.method === "POST" && url.pathname === "/api/v1/mobile/offline/conflicts/dev-7%3A41:resolve") {
        resolved = true;
        return { status: 204, body: undefined };
      }
      if (url.pathname === "/api/v1/mobile/offline/conflicts") return json(200, { items: resolved ? [] : [conflict] });
      return undefined;
    };
    const { container } = renderAt("/mobile-flows/conflicts");
    const table = await findTable({ name: "Offline conflicts" });
    await waitFor(() => expect(within(table).getByText(/line was CANCELLED by alice/)).toBeInTheDocument());
    expect(pageViolations(container)).toEqual([]);

    fireEvent.click(within(table).getByRole("button", { name: "dev-7:41" }));
    const confirm = screen.getByRole("button", { name: "Confirm resolution" });
    expect(confirm).toBeDisabled();
    fireEvent.change(screen.getByLabelText("Resolution"), { target: { value: "reapply" } });
    expect(screen.getByText(/order line is dropped/)).toBeInTheDocument();
    expect(pageViolations(container)).toEqual([]);
    fireEvent.click(confirm);

    expect(await screen.findByText("Transaction dev-7:41 was applied as a movement.")).toBeInTheDocument();
    expect(await screen.findByText("No offline conflicts")).toBeInTheDocument();
    const post = calls.find((c) => c.method === "POST");
    expect(post?.body).toEqual({ action: "reapply" });
    const headers = new Headers(post?.init.headers);
    expect(headers.get("If-Match")).toBe('"3"');
    expect(headers.get("Idempotency-Key")).toBeTruthy();
  });

  it("keeps the panel open and explains a version conflict on reject", async () => {
    handler = (url, init) => {
      if (init.method === "POST") return json(412, { code: "COM-0412", detail: "version mismatch" });
      if (url.pathname === "/api/v1/mobile/offline/conflicts") return json(200, { items: [conflict] });
      return undefined;
    };
    renderAt("/mobile-flows/conflicts");
    fireEvent.click(await screen.findByRole("button", { name: "dev-7:41" }));
    fireEvent.change(screen.getByLabelText("Resolution"), { target: { value: "reject" } });
    fireEvent.click(screen.getByRole("button", { name: "Confirm resolution" }));
    const alert = await findAlert();
    expect(alert).toHaveTextContent("Another supervisor changed this conflict");
    expect(screen.getByRole("heading", { name: "Resolve dev-7:41" })).toBeInTheDocument();
  });

  it("uses a new Idempotency-Key when the supervisor changes the resolution after a failure", async () => {
    handler = (url, init) => {
      if (init.method === "POST") return json(503, { code: "COM-0500", detail: "unavailable" });
      if (url.pathname === "/api/v1/mobile/offline/conflicts") return json(200, { items: [conflict] });
      return undefined;
    };
    renderAt("/mobile-flows/conflicts");
    fireEvent.click(await screen.findByRole("button", { name: "dev-7:41" }));
    fireEvent.change(screen.getByLabelText("Resolution"), { target: { value: "reject" } });
    fireEvent.click(screen.getByRole("button", { name: "Confirm resolution" }));
    await findAlert();
    fireEvent.click(screen.getByRole("button", { name: "Confirm resolution" }));
    await waitFor(() => expect(calls.filter((c) => c.method === "POST")).toHaveLength(2));
    fireEvent.change(screen.getByLabelText("Resolution"), { target: { value: "reapply" } });
    fireEvent.click(screen.getByRole("button", { name: "Confirm resolution" }));
    await waitFor(() => expect(calls.filter((c) => c.method === "POST")).toHaveLength(3));
    const keys = calls.filter((c) => c.method === "POST").map((c) => new Headers(c.init.headers).get("Idempotency-Key"));
    expect(keys[0]).toBe(keys[1]);
    expect(keys[2]).not.toBe(keys[1]);
  });

  it("shows the empty state", async () => {
    handler = () => json(200, { items: [] });
    renderAt("/mobile-flows/conflicts");
    expect(await screen.findByText("No offline conflicts")).toBeInTheDocument();
  });

  it("explains that the mobile service is not deployed yet and retries", async () => {
    handler = () => ({ status: 404, body: undefined });
    renderAt("/mobile-flows/conflicts");
    const alert = await findAlert();
    expect(alert).toHaveTextContent("The mobile service is not available yet");
    handler = () => json(200, { items: [conflict] });
    fireEvent.click(within(alert).getByRole("button", { name: "Try again" }));
    expect(await screen.findByRole("button", { name: "dev-7:41" })).toBeInTheDocument();
  });
});

describe("printer status (§6.32.14)", () => {
  const statuses: PrinterStatus[] = [
    { printerId: "p1", code: "DOCK-1", queueCode: "DOCK", state: "ERROR", queueDepth: 2, lastError: "MOB-0111", lastJobAt: "2026-10-27T08:05:00Z" },
    { printerId: "p2", code: "DOCK-2", queueCode: "DOCK", state: "IDLE", queueDepth: 2 },
  ];

  it("lists state, queue depth and last error per printer", async () => {
    handler = (url) => (url.pathname === "/api/v1/print/printers/status" ? json(200, { items: statuses }) : undefined);
    const { container } = renderAt("/mobile-flows/printer-status");
    const table = await findTable({ name: "Printer status" });
    await waitFor(() => expect(within(table).getByText("DOCK-1")).toBeInTheDocument());
    expect(within(table).getByText("Error")).toBeInTheDocument();
    expect(within(table).getByText("Idle")).toBeInTheDocument();
    expect(within(table).getByText("MOB-0111")).toBeInTheDocument();
    expect(within(table).getByText("2026-10-27 08:05 UTC")).toBeInTheDocument();
    expect(pageViolations(container)).toEqual([]);
  });

  it("shows the empty state, a retryable error, and refreshes", async () => {
    handler = () => json(200, { items: [] });
    const { unmount } = renderAt("/mobile-flows/printer-status");
    expect(await screen.findByText("No printers yet")).toBeInTheDocument();
    unmount();

    handler = () => json(503, { code: "COM-0500", detail: "internal error" });
    renderAt("/mobile-flows/printer-status");
    const alert = await findAlert();
    expect(alert).toHaveTextContent("Could not load printer status");
    expect(alert).toHaveTextContent("COM-0500");
    handler = () => json(200, { items: statuses });
    fireEvent.click(within(alert).getByRole("button", { name: "Try again" }));
    expect(await screen.findByText("DOCK-2")).toBeInTheDocument();
    const before = calls.length;
    fireEvent.click(screen.getByRole("button", { name: "Refresh" }));
    await waitFor(() => expect(calls.length).toBe(before + 1));
  });
});
