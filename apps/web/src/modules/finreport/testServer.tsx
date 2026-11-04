// Test harness for the finreport pages: a fetch stub that answers from a handler and records
// every call, and a router that mounts the module's routes.
import { render } from "@testing-library/react";
import { MemoryRouter, Route, Routes } from "react-router-dom";
import { afterEach, beforeEach, vi } from "vitest";
import { checkAxeAA } from "../../shell/a11y";
import { PrincipalProvider } from "../../shell/principal";
import finreport from "./module";

export type Reply = { status: number; body: unknown; headers?: Record<string, string>; text?: string };
export type Handler = (url: URL, init: RequestInit) => Reply | undefined;
export interface Call {
  method: string;
  url: URL;
  init: RequestInit;
  body: unknown;
}

export const server: { handler: Handler; calls: Call[] } = { handler: () => undefined, calls: [] };

export function json(status: number, body: unknown, headers?: Record<string, string>): Reply {
  return { status, body, headers };
}

/** A non-JSON reply, such as a CSV file. */
export function text(status: number, body: string, headers?: Record<string, string>): Reply {
  return { status, body: null, text: body, headers: { "Content-Type": "text/csv", ...headers } };
}

export function problem(status: number, code: string, detail: string): Reply {
  return json(status, { type: `https://passage.dev/problems/${code}`, title: "Error", status, code, detail });
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
        const reply = server.handler(url, init) ?? problem(404, "TEST-404", `no fake for ${init.method ?? "GET"} ${url.pathname}`);
        const payload = reply.text ?? JSON.stringify(reply.body);
        return new Response(payload, { status: reply.status, headers: { "Content-Type": "application/json", ...reply.headers } });
      }),
    );
  });
  afterEach(() => {
    vi.unstubAllGlobals();
  });
}

/** Renders the module at path for a principal holding duties (none by default). */
export function renderAt(path: string, duties: readonly string[] = []) {
  return render(
    <PrincipalProvider duties={duties}>
      <MemoryRouter initialEntries={[path]}>
        <Routes>
          {finreport.routes.map((route) => (
            <Route key={route.path} path={route.path} element={route.element} />
          ))}
        </Routes>
      </MemoryRouter>
    </PrincipalProvider>,
  );
}

export function header(call: Call, name: string): string | null {
  return new Headers(call.init.headers).get(name);
}

const shellRules = new Set(["page-has-heading-one", "landmark-unique", "region", "bypass"]);
/** Axe AA violations of a page, ignoring the rules the shell (not the page) satisfies. */
export function pageViolations(container: HTMLElement) {
  return checkAxeAA(container).filter((v) => !shellRules.has(v.id));
}
