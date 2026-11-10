// Test-only helpers for the purchasing page tests: a stubbed fetch, problem
// responses, and the module's routes mounted in a plain MemoryRouter.
import { render } from "@testing-library/react";
import { MemoryRouter, Route, Routes, useLocation } from "react-router-dom";
import { vi } from "vitest";
import { checkAxeAA } from "../../shell/a11y";
import { installLookupStub } from "./lookupStub";
import purchasing from "./module";

export const json = (body: unknown, status = 200, headers: Record<string, string> = {}) =>
  new Response(JSON.stringify(body), { status, headers: { "Content-Type": "application/json", ...headers } });

export const problem = (status: number, code: string, detail: string, errors?: Record<string, string>) =>
  json({ type: `https://passage.dev/problems/${code}`, title: "Problem", status, code, detail, errors }, status);

export type Handler = (url: string, init: RequestInit) => Response | Promise<Response>;

export function stubFetch(handler: Handler) {
  const mock = vi.fn(async (url: string, init: RequestInit = {}) => handler(url, init));
  vi.stubGlobal("fetch", mock);
  return mock;
}

/** Renders the module's routes at path; router.state.location tracks navigation. */
export function renderAt(path: string) {
  installLookupStub();
  const router = { state: { location: { pathname: path } } };
  function Probe() {
    router.state.location.pathname = useLocation().pathname;
    return null;
  }
  const view = render(
    <MemoryRouter initialEntries={[path]}>
      <Probe />
      <Routes>
        {purchasing.routes.map((r) => (
          <Route key={r.path} path={r.path} element={r.element} />
        ))}
      </Routes>
    </MemoryRouter>,
  );
  return { router, ...view };
}

// Shell-level rules (one h1, nav, main, skip link) belong to the shell, which
// these page tests do not mount; everything else must pass.
const shellRules = new Set(["page-has-heading-one", "landmark-unique", "region", "bypass"]);
export const pageViolations = (root: ParentNode) => checkAxeAA(root).filter((v) => !shellRules.has(v.id));
