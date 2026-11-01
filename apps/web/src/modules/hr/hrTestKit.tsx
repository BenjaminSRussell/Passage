import { render } from "@testing-library/react";
import { MemoryRouter, Route, Routes, useLocation } from "react-router-dom";
import { afterEach, beforeEach, vi } from "vitest";
import { checkAxeAA } from "../../shell/a11y";
import hr from "./module";

export interface Reply {
  status: number;
  body: unknown;
}

export interface Call {
  method: string;
  url: URL;
  body: unknown;
  headers: Headers;
}

type Handler = (call: Call) => Reply | undefined;

export const json = (status: number, body: unknown): Reply => ({ status, body });
export const problem = (status: number, code: string, detail: string, errors?: Record<string, string>): Reply => json(status, { code, detail, errors });

export const id = (n: number) => `0195f0a0-0000-7000-8000-${String(n).padStart(12, "0")}`;

const shellRules = new Set(["page-has-heading-one", "landmark-unique", "region", "bypass"]);
export const pageViolations = (container: HTMLElement) => checkAxeAA(container).filter((v) => !shellRules.has(v.id));

export function routes(table: Record<string, Reply | ((call: Call) => Reply)>): Handler {
  return (call) => {
    const hit = table[`${call.method} ${call.url.pathname}`];
    return typeof hit === "function" ? hit(call) : hit;
  };
}

export function useFakeApi() {
  const api = { handler: (() => undefined) as Handler, calls: [] as Call[] };
  beforeEach(() => {
    api.calls = [];
    api.handler = () => undefined;
    vi.stubGlobal(
      "fetch",
      vi.fn(async (input: string, init: RequestInit = {}) => {
        const call: Call = {
          method: init.method ?? "GET",
          url: new URL(input, "http://localhost"),
          body: init.body ? JSON.parse(String(init.body)) : undefined,
          headers: new Headers(init.headers),
        };
        api.calls.push(call);
        const reply = api.handler(call) ?? json(404, { code: "TEST-404", detail: `no fake for ${call.method} ${call.url.pathname}` });
        return new Response(JSON.stringify(reply.body), { status: reply.status, headers: { "Content-Type": "application/json" } });
      }),
    );
  });
  afterEach(() => vi.unstubAllGlobals());
  return {
    set handler(next: Handler) {
      api.handler = next;
    },
    get calls() {
      return api.calls;
    },
    posts: (suffix: string) => api.calls.filter((c) => c.method === "POST" && c.url.pathname.endsWith(suffix)),
  };
}

function Where() {
  const location = useLocation();
  return <p data-testid="where">{location.pathname + location.search}</p>;
}

export function renderAt(path: string) {
  return render(
    <MemoryRouter initialEntries={[path]}>
      <Routes>
        {hr.routes.map((route) => (
          <Route key={route.path} path={route.path} element={route.element} />
        ))}
      </Routes>
      <Where />
    </MemoryRouter>,
  );
}
