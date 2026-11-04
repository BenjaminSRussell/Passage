// Test harness for the pricing pages: a fetch stub that answers from a handler and records
// every call, and a router that mounts the module's routes. Used by the *.test.tsx files.
import { render } from "@testing-library/react";
import { MemoryRouter, Route, Routes } from "react-router-dom";
import { afterEach, beforeEach, vi } from "vitest";
import { checkAxeAA } from "../../shell/a11y";
import { PrincipalProvider } from "../../shell/principal";
import pricing from "./module";

/** A JSON reply, or a text reply (such as CSV) when text is set. */
export type Reply = { status: number; body: unknown; text?: string; headers?: Record<string, string> };
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

export function text(status: number, body: string, headers: Record<string, string>): Reply {
  return { status, body: undefined, text: body, headers };
}

/** A request body as the test sees it: parsed JSON, or the raw text (a CSV upload). */
function readBody(raw: BodyInit | null | undefined): unknown {
  if (raw == null) return undefined;
  try {
    return JSON.parse(String(raw));
  } catch {
    return String(raw);
  }
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
        server.calls.push({ method: init.method ?? "GET", url, init, body: readBody(init.body) });
        const reply = server.handler(url, init) ?? json(404, { code: "TEST-404", detail: `no fake for ${init.method ?? "GET"} ${url.pathname}` });
        if (reply.status === 204) return new Response(null, { status: 204 });
        if (reply.text !== undefined) return new Response(reply.text, { status: reply.status, headers: reply.headers });
        return new Response(JSON.stringify(reply.body), { status: reply.status, headers: { "Content-Type": "application/json" } });
      }),
    );
  });
  afterEach(() => {
    vi.unstubAllGlobals();
  });
}

/** Renders the module route for path; duties, when given, are the signed-in user's duties. */
export function renderAt(path: string, duties: readonly string[] = []) {
  return render(
    <PrincipalProvider duties={duties}>
      <MemoryRouter initialEntries={[path]}>
        <Routes>
          {pricing.routes.map((route) => (
            <Route key={route.path} path={route.path} element={route.element} />
          ))}
        </Routes>
      </MemoryRouter>
    </PrincipalProvider>,
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
