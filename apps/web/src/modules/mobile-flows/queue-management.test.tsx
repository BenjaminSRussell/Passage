import { fireEvent, render, screen, waitFor, within } from "@testing-library/react";
import { MemoryRouter, Route, Routes } from "react-router-dom";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import type { Printer, RoutingRule } from "./api/print";
import mobileFlows from "./module";

type Reply = { status: number; body?: unknown };
let handler: (method: string, path: string, body: unknown) => Reply | undefined;
let calls: { method: string; path: string; body: unknown }[];

beforeEach(() => {
  calls = [];
  handler = () => undefined;
  vi.stubGlobal(
    "fetch",
    vi.fn(async (input: string, init: RequestInit = {}) => {
      const path = new URL(input, "http://localhost").pathname;
      const method = init.method ?? "GET";
      const body = init.body ? JSON.parse(String(init.body)) : undefined;
      calls.push({ method, path, body });
      const reply = handler(method, path, body) ?? { status: 404, body: { code: "TEST-404", detail: `no fake for ${method} ${path}` } };
      return reply.status === 204
        ? new Response(null, { status: 204 })
        : new Response(JSON.stringify(reply.body), { status: reply.status, headers: { "Content-Type": "application/json" } });
    }),
  );
});

afterEach(() => vi.unstubAllGlobals());

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

const printer: Printer = {
  id: "0195f0a0-0000-7000-8000-000000000001",
  code: "DOCK-1",
  name: "Dock door 1",
  deviceType: "ZPL_II",
  dpi: 203,
  mediaSize: "4X6_IN",
  host: "10.0.4.20",
  port: 9100,
  queueCode: "DOCK",
  failoverRank: 1,
  createdAt: "2026-10-27T08:00:00Z",
};

const rule: RoutingRule = { id: "0195f0a0-0000-7000-8000-0000000000b1", documentType: "PICK_LIST", queueCode: "DOCK", sequence: 1 };

describe("print queue management", () => {
  it("retires a printer after confirmation and reloads the list", async () => {
    let retired = false;
    handler = (method, path) => {
      if (path === "/api/v1/print/printers" && method === "GET") return { status: 200, body: { items: retired ? [] : [printer] } };
      if (path === `/api/v1/print/printers/${printer.id}:retire`) {
        retired = true;
        return { status: 200, body: { ...printer, retiredAt: "2026-11-10T10:00:00Z" } };
      }
      return undefined;
    };
    renderAt("/mobile-flows/printers");
    fireEvent.click(await screen.findByRole("button", { name: "Retire DOCK-1" }));
    const dialog = await screen.findByRole("alertdialog");
    expect(dialog).toHaveTextContent("leaves queue DOCK");
    fireEvent.click(within(dialog).getByRole("button", { name: "Retire printer" }));
    expect(await screen.findByText("No printers yet")).toBeInTheDocument();
    expect(calls.some((c) => c.method === "POST" && c.path.endsWith(":retire"))).toBe(true);
  });

  it("keeps the printer when the retire is cancelled", async () => {
    handler = (_m, path) => (path === "/api/v1/print/printers" ? { status: 200, body: { items: [printer] } } : undefined);
    renderAt("/mobile-flows/printers");
    fireEvent.click(await screen.findByRole("button", { name: "Retire DOCK-1" }));
    fireEvent.click(within(await screen.findByRole("alertdialog")).getByRole("button", { name: "Keep printer" }));
    expect(calls.filter((c) => c.method === "POST")).toEqual([]);
  });

  it("edits a printer and sends only the editable fields", async () => {
    handler = (method, path) => {
      if (path === "/api/v1/print/printers" && method === "GET") return { status: 200, body: { items: [printer] } };
      if (path === `/api/v1/print/printers/${printer.id}` && method === "PATCH") return { status: 200, body: printer };
      return undefined;
    };
    renderAt(`/mobile-flows/printers/${printer.id}/edit`);
    const queue = await screen.findByLabelText("Queue");
    fireEvent.change(queue, { target: { value: "OFFICE" } });
    fireEvent.click(screen.getByRole("button", { name: "Save printer" }));
    await waitFor(() => expect(calls.some((c) => c.method === "PATCH")).toBe(true));
    const patch = calls.find((c) => c.method === "PATCH");
    expect(patch?.body).toEqual({ name: "Dock door 1", dpi: 203, mediaSize: "4X6_IN", host: "10.0.4.20", port: 9100, queueCode: "OFFICE", failoverRank: 1 });
  });

  it("shows a rank clash on the failover rank field", async () => {
    handler = (method, path) => {
      if (path === "/api/v1/print/printers" && method === "GET") return { status: 200, body: { items: [printer] } };
      if (method === "PATCH") return { status: 409, body: { code: "PRN-0006", detail: "taken" } };
      return undefined;
    };
    renderAt(`/mobile-flows/printers/${printer.id}/edit`);
    fireEvent.click(await screen.findByRole("button", { name: "Save printer" }));
    expect(await screen.findByText("Another printer in this queue already has this failover rank.")).toBeInTheDocument();
  });

  it("deletes a routing rule after confirmation", async () => {
    let gone = false;
    handler = (method, path) => {
      if (path === "/api/v1/print/routing-rules" && method === "GET") return { status: 200, body: { items: gone ? [] : [rule] } };
      if (method === "DELETE" && path === `/api/v1/print/routing-rules/${rule.id}`) {
        gone = true;
        return { status: 204 };
      }
      return undefined;
    };
    renderAt("/mobile-flows/routing-rules");
    fireEvent.click(await screen.findByRole("button", { name: /Delete the .* rule for DOCK/ }));
    fireEvent.click(within(await screen.findByRole("alertdialog")).getByRole("button", { name: "Delete rule" }));
    expect(await screen.findByText("No routing rules yet")).toBeInTheDocument();
  });
});
