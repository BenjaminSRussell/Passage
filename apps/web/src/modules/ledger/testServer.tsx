// Test harness for the ledger pages: a fetch stub that answers from a handler and
// records every call, and a router (with a query client) that mounts the module's routes.
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { render } from "@testing-library/react";
import { MemoryRouter, Route, Routes, useLocation } from "react-router-dom";
import { afterEach, beforeEach, vi } from "vitest";
import { checkAxeAA } from "../../shell/a11y";
import ledger from "./module";

export type Reply = { status: number; body: unknown };
export type Handler = (url: URL, init: RequestInit) => Reply | undefined;
export interface Call {
  method: string;
  url: URL;
  init: RequestInit;
  body: unknown;
}

export const server: { handler: Handler; calls: Call[] } = { handler: () => undefined, calls: [] };

export function json(status: number, body: unknown): Reply {
  return { status, body };
}

/** Installs the fetch stub for the calling test file. */
export function useFakeServer() {
  beforeEach(() => {
    server.calls = [];
    server.handler = () => undefined;
    vi.stubGlobal(
      "fetch",
      vi.fn(async (input: string, init: RequestInit = {}) => {
        const url = new URL(input, "http://localhost");
        server.calls.push({ method: init.method ?? "GET", url, init, body: init.body ? JSON.parse(String(init.body)) : undefined });
        const reply = server.handler(url, init) ?? json(404, { code: "TEST-404", detail: `no fake for ${init.method ?? "GET"} ${url.pathname}` });
        return new Response(reply.status === 204 ? null : JSON.stringify(reply.body), { status: reply.status, headers: { "Content-Type": "application/json" } });
      }),
    );
  });
  afterEach(() => {
    vi.unstubAllGlobals();
  });
}

function Location() {
  const location = useLocation();
  return <output data-testid="location">{location.pathname}</output>;
}

export function renderAt(path: string) {
  const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  return render(
    <QueryClientProvider client={client}>
      <MemoryRouter initialEntries={[path]}>
        <Routes>
          {ledger.routes.map((route) => (
            <Route key={route.path} path={route.path} element={route.element} />
          ))}
        </Routes>
        <Location />
      </MemoryRouter>
    </QueryClientProvider>,
  );
}

export function idempotencyKey(call: { init: RequestInit }): string | null {
  return new Headers(call.init.headers).get("Idempotency-Key");
}

const shellRules = new Set(["page-has-heading-one", "landmark-unique", "region", "bypass"]);
/** Axe AA violations of a page, ignoring the rules the shell (not the page) satisfies. */
export function pageViolations(container: HTMLElement) {
  return checkAxeAA(container).filter((v) => !shellRules.has(v.id));
}
