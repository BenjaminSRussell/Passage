import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { render } from "@testing-library/react";
import { MemoryRouter, Route, Routes } from "react-router-dom";
import { afterEach, beforeEach, vi } from "vitest";
import { checkAxeAA } from "../../shell/a11y";
import fx from "./module";

export type Reply = { status: number; body: unknown };
export interface Call {
  method: string;
  url: URL;
  body: unknown;
}

export const server: { handler: (url: URL, method: string) => Reply | undefined; calls: Call[] } = { handler: () => undefined, calls: [] };

export function json(status: number, body: unknown): Reply {
  return { status, body };
}

export function useFakeServer() {
  beforeEach(() => {
    server.calls = [];
    server.handler = () => undefined;
    vi.stubGlobal(
      "fetch",
      vi.fn(async (input: string, init: RequestInit = {}) => {
        const url = new URL(input, "http://localhost");
        const method = init.method ?? "GET";
        server.calls.push({ method, url, body: init.body ? JSON.parse(String(init.body)) : undefined });
        const reply = server.handler(url, method) ?? json(404, { code: "TEST-404", detail: `no fake for ${method} ${url.pathname}` });
        return new Response(JSON.stringify(reply.body), { status: reply.status, headers: { "Content-Type": "application/json" } });
      }),
    );
  });
  afterEach(() => {
    vi.unstubAllGlobals();
  });
}

export function renderAt(path: string) {
  const client = new QueryClient({ defaultOptions: { queries: { retry: false }, mutations: { retry: false } } });
  return render(
    <QueryClientProvider client={client}>
      <MemoryRouter initialEntries={[path]}>
        <Routes>
          {fx.routes.map((route) => (
            <Route key={route.path} path={route.path} element={route.element} />
          ))}
        </Routes>
      </MemoryRouter>
    </QueryClientProvider>,
  );
}

const shellRules = new Set(["page-has-heading-one", "landmark-unique", "region", "bypass"]);
export function pageViolations(container: HTMLElement) {
  return checkAxeAA(container).filter((v) => !shellRules.has(v.id));
}
